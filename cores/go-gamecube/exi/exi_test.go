package exi_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/exi"
)

type mem1 struct{ b []byte }

func newMem() *mem1 { return &mem1{b: make([]byte, 1<<16)} }

func (m *mem1) Read8(addr uint32) uint8 {
	if int(addr) < len(m.b) {
		return m.b[addr]
	}
	return 0
}

func (m *mem1) Write8(addr uint32, v uint8) {
	if int(addr) < len(m.b) {
		m.b[addr] = v
	}
}

// Channel register offsets: block stride 0x14.
func w32(e *exi.EXI, off uint32, v uint32) {
	e.Write16(off, uint16(v>>16))
	e.Write16(off+2, uint16(v))
}

func r32(e *exi.EXI, off uint32) uint32 {
	return uint32(e.Read16(off))<<16 | uint32(e.Read16(off+2))
}

func chanBase(ch int) uint32 { return uint32(ch) * 0x14 }

func setCSR(e *exi.EXI, ch int, v uint32) { w32(e, chanBase(ch)+0x00, v) }
func csr(e *exi.EXI, ch int) uint32       { return r32(e, chanBase(ch)+0x00) }

func selectDev(e *exi.EXI, ch, dev int) {
	setCSR(e, ch, 1<<uint(7+dev))
}

func deselect(e *exi.EXI, ch int) { setCSR(e, ch, 0) }

func dmaWrite(e *exi.EXI, m *mem1, ch int, mar uint32, data []byte) {
	copy(m.b[mar:], data)
	w32(e, chanBase(ch)+0x04, mar)
	w32(e, chanBase(ch)+0x08, uint32(len(data)))
	// CR: TSTART | DMA | RW=1 (write).
	w32(e, chanBase(ch)+0x0C, 1|2|(1<<2))
}

func dmaRead(e *exi.EXI, m *mem1, ch int, mar uint32, n int) []byte {
	w32(e, chanBase(ch)+0x04, mar)
	w32(e, chanBase(ch)+0x08, uint32(n))
	// CR: TSTART | DMA | RW=0 (read).
	w32(e, chanBase(ch)+0x0C, 1|2)
	out := make([]byte, n)
	copy(out, m.b[mar:mar+uint32(n)])
	return out
}

func immWrite(e *exi.EXI, ch int, data uint32, tlenCode uint32) {
	w32(e, chanBase(ch)+0x10, data)
	// CR: TSTART | RW=1 (write) | TLEN code.
	w32(e, chanBase(ch)+0x0C, 1|(1<<2)|(tlenCode<<4))
}

func immRead(e *exi.EXI, ch int, tlenCode uint32) uint32 {
	// CR: TSTART | RW=0 (read) | TLEN code.
	w32(e, chanBase(ch)+0x0C, 1|(tlenCode<<4))
	return r32(e, chanBase(ch)+0x10)
}

// SRAM helpers: each call is a full session (select, command,
// stream, deselect) exactly as software would drive it.

func sramWrite(e *exi.EXI, m *mem1, addr uint16, data []byte) {
	selectDev(e, 0, 2)
	stream := []byte{0x20, byte(addr >> 8), byte(addr), 0x00}
	stream = append(stream, data...)
	dmaWrite(e, m, 0, 0x1000, stream)
	deselect(e, 0)
}

func sramRead(e *exi.EXI, m *mem1, addr uint16, n int) []byte {
	selectDev(e, 0, 2)
	dmaWrite(e, m, 0, 0x1000, []byte{0x20, byte(addr >> 8), byte(addr), 0x00})
	out := dmaRead(e, m, 0, 0x2000, n)
	deselect(e, 0)
	return out
}

func rtcRead(e *exi.EXI, m *mem1) uint32 {
	return binary.BigEndian.Uint32(sramRead(e, m, 0x0100, 4))
}

func TestSRAMRoundTripDMA(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	data := []byte("Patriot SRAM payload")
	sramWrite(e, m, 0x0010, data)

	if got := e.SRAM()[0x10 : 0x10+len(data)]; !bytes.Equal(got, data) {
		t.Fatalf("SRAM() at 0x10 = %q, want %q", got, data)
	}
	if e.SRAM()[0x00] != 0 {
		t.Fatalf("SRAM()[0] = %#x, want 0 (write landed at wrong offset)", e.SRAM()[0])
	}
	if got := sramRead(e, m, 0x0010, len(data)); !bytes.Equal(got, data) {
		t.Fatalf("SRAM read back = %q, want %q", got, data)
	}
}

func TestSRAMImmediateTransfers(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	selectDev(e, 0, 2)
	// Command word: 0x20, addr hi 0x00, addr lo 0x20, ignored.
	immWrite(e, 0, 0x20002000, 2) // TLEN code 2 = 4 bytes
	immWrite(e, 0, 0x58000000, 0) // TLEN code 0 = 1 byte, DATA>>24
	immWrite(e, 0, 0x59000000, 0)
	deselect(e, 0)
	if e.SRAM()[0x20] != 'X' || e.SRAM()[0x21] != 'Y' {
		t.Fatalf("SRAM()[0x20:0x22] = %q, want XY", e.SRAM()[0x20:0x22])
	}
	selectDev(e, 0, 2)
	immWrite(e, 0, 0x20002000, 2)
	if got := immRead(e, 0, 0); got>>24 != 'X' {
		t.Fatalf("immediate read 1 = %#x, want X in top byte", got)
	}
	if got := immRead(e, 0, 0); got>>24 != 'Y' {
		t.Fatalf("immediate read 2 = %#x, want Y (auto-increment)", got)
	}
	deselect(e, 0)
}

func TestSRAMBadCommandSinks(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	selectDev(e, 0, 2)
	dmaWrite(e, m, 0, 0x1000, []byte{0x99, 0x00, 0x00, 0x00, 0xAA})
	if got := dmaRead(e, m, 0, 0x2000, 2); !bytes.Equal(got, []byte{0, 0}) {
		t.Fatalf("sunk session read = %v, want zeros", got)
	}
	deselect(e, 0)
	if e.SRAM()[0] == 0xAA {
		t.Fatal("bad command byte wrote into SRAM")
	}
}

func TestRTCAdvanceAndRemainder(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	e.SetRTC(1000)
	if got := rtcRead(e, m); got != 1000 {
		t.Fatalf("RTC base readback = %d, want 1000", got)
	}
	e.Advance(486000000)
	if got := rtcRead(e, m); got != 1001 {
		t.Fatalf("RTC after 486M cycles = %d, want 1001", got)
	}
	e.Advance(243000000)
	if got := rtcRead(e, m); got != 1001 {
		t.Fatalf("RTC after a half second = %d, want still 1001", got)
	}
	e.Advance(243000000)
	if got := rtcRead(e, m); got != 1002 {
		t.Fatalf("RTC after two half seconds total = %d, want 1002", got)
	}
}

func TestRTCProtocolWriteResetsElapsed(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	e.SetRTC(7)
	e.Advance(486000000)
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], 4242)
	sramWrite(e, m, 0x0100, buf[:])
	if got := rtcRead(e, m); got != 4242 {
		t.Fatalf("RTC protocol write readback = %d, want 4242", got)
	}
	e.Advance(486000000)
	if got := rtcRead(e, m); got != 4243 {
		t.Fatalf("RTC after protocol write plus 1s = %d, want 4243", got)
	}
}

// Card helpers. Slot A is channel 0, slot B is channel 1,
// both device 1.

func cardChannel(slot int) int { return slot }

func wakeCard(t *testing.T, e *exi.EXI, m *mem1, slot int) []byte {
	t.Helper()
	ch := cardChannel(slot)
	selectDev(e, ch, 1)
	dmaWrite(e, m, ch, 0x1000, []byte{0x00})
	id := dmaRead(e, m, ch, 0x3000, 2)
	deselect(e, ch)
	return id
}

func cardStatus(e *exi.EXI, m *mem1, slot int) byte {
	ch := cardChannel(slot)
	selectDev(e, ch, 1)
	dmaWrite(e, m, ch, 0x1000, []byte{0x81})
	out := dmaRead(e, m, ch, 0x3000, 1)
	deselect(e, ch)
	return out[0]
}

func cardWritePage(e *exi.EXI, m *mem1, slot int, addr uint32, data []byte) {
	ch := cardChannel(slot)
	selectDev(e, ch, 1)
	stream := []byte{0xF2, byte(addr >> 24), byte(addr >> 16), byte(addr >> 8), byte(addr)}
	stream = append(stream, data...)
	dmaWrite(e, m, ch, 0x1000, stream)
	deselect(e, ch)
}

func cardReadAt(e *exi.EXI, m *mem1, slot int, addr uint32, n int) []byte {
	ch := cardChannel(slot)
	selectDev(e, ch, 1)
	dmaWrite(e, m, ch, 0x1000, []byte{0x52, byte(addr >> 24), byte(addr >> 16), byte(addr >> 8), byte(addr)})
	out := dmaRead(e, m, ch, 0x3000, n)
	deselect(e, ch)
	return out
}

func TestCardIDStatusAndWake(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	if got := cardStatus(e, m, 0); got != 0x00 {
		t.Fatalf("asleep card status = %#x, want 0x00", got)
	}
	if id := wakeCard(t, e, m, 0); !bytes.Equal(id, []byte{0x10, 0x00}) {
		t.Fatalf("slot A ID = %v, want [0x10 0x00] (16 Mbit)", id)
	}
	if got := cardStatus(e, m, 0); got != 0x40 {
		t.Fatalf("awake card status = %#x, want 0x40", got)
	}
}

func TestCardDMAWriteReadAndErase(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	wakeCard(t, e, m, 0)

	page := make([]byte, 512)
	for i := range page {
		page[i] = byte(i*7 + 3)
	}
	cardWritePage(e, m, 0, 0x1000, page)
	if got := e.CardImage(0)[0x1000:0x1200]; !bytes.Equal(got, page) {
		t.Fatal("CardImage(0) does not reflect the 512 byte page written at 0x1000")
	}
	if got := cardReadAt(e, m, 0, 0x1000, 512); !bytes.Equal(got, page) {
		t.Fatal("DMA read back of the page at 0x1000 differs from what was written")
	}

	selectDev(e, 0, 1)
	dmaWrite(e, m, 0, 0x1000, []byte{0x89, 0x00, 0x00, 0x10, 0x00})
	deselect(e, 0)
	img := e.CardImage(0)
	for i := 0x1000; i < 0x3000; i++ {
		if img[i] != 0xFF {
			t.Fatalf("after erase at 0x1000, CardImage byte %#x = %#x, want 0xFF", i, img[i])
		}
	}
}

func TestCardPartialPageDiscarded(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	wakeCard(t, e, m, 0)
	cardWritePage(e, m, 0, 0x2000, bytes.Repeat([]byte{0x5A}, 100))
	for i := 0x2000; i < 0x2200; i++ {
		if e.CardImage(0)[i] != 0xFF {
			t.Fatalf("partial page byte %#x = %#x, want 0xFF (discarded at session end)", i, e.CardImage(0)[i])
		}
	}
}

func TestCardAsleep(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	// Direct read with no command, card still asleep.
	selectDev(e, 0, 1)
	got := dmaRead(e, m, 0, 0x3000, 16)
	deselect(e, 0)
	for i, b := range got {
		if b != 0xFF {
			t.Fatalf("asleep card read byte %d = %#x, want 0xFF", i, b)
		}
	}
	// A write page command while asleep is ignored.
	cardWritePage(e, m, 0, 0x1000, bytes.Repeat([]byte{0x11}, 512))
	for i := 0x1000; i < 0x1200; i++ {
		if e.CardImage(0)[i] != 0xFF {
			t.Fatalf("asleep card accepted a page write at %#x", i)
		}
	}
	// After the ignored attempt the card still wakes on ID
	// (a fresh session must recover from the sunk one).
	if id := wakeCard(t, e, m, 0); !bytes.Equal(id, []byte{0x10, 0x00}) {
		t.Fatalf("ID after asleep write attempt = %v, want [0x10 0x00]", id)
	}
	if got := cardReadAt(e, m, 0, 0x1000, 4); !bytes.Equal(got, []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Fatalf("read at 0x1000 after wake = %v, want all 0xFF", got)
	}
}

func TestCardSizesAndSlotB(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	if len(e.CardImage(0)) != 2<<20 {
		t.Fatalf("default slot A image = %d bytes, want 2 MiB", len(e.CardImage(0)))
	}
	e.SetCardSizeMBits(1, 64)
	if len(e.CardImage(1)) != 64<<17 {
		t.Fatalf("slot B image after resize = %d bytes, want %d", len(e.CardImage(1)), 64<<17)
	}
	if id := wakeCard(t, e, m, 1); !bytes.Equal(id, []byte{64, 0x00}) {
		t.Fatalf("slot B ID = %v, want [64 0x00]", id)
	}
	e.SetCardSizeMBits(1, 3) // not a supported size
	if len(e.CardImage(1)) != 64<<17 {
		t.Fatal("unsupported card size changed the image")
	}
	if e.CardImage(7) != nil {
		t.Fatal("CardImage(7) should be nil")
	}
}

func TestNoDeviceIPLAndAD16(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	// No CS: reads return 0xFF bytes, writes go nowhere.
	deselect(e, 0)
	if got := dmaRead(e, m, 0, 0x3000, 4); !bytes.Equal(got, []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Fatalf("no-device read = %v, want 0xFF bytes", got)
	}
	// IPL ROM window on channel 0 device 0: reads are 0x00.
	selectDev(e, 0, 0)
	if got := dmaRead(e, m, 0, 0x3000, 4); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("IPL read = %v, want zeros", got)
	}
	deselect(e, 0)
	// AD16 on channel 2 device 0: repeating 0x04, 0x12.
	selectDev(e, 2, 0)
	if got := dmaRead(e, m, 2, 0x3000, 6); !bytes.Equal(got, []byte{0x04, 0x12, 0x04, 0x12, 0x04, 0x12}) {
		t.Fatalf("AD16 read = %v, want repeating 04 12", got)
	}
	deselect(e, 2)
}

func TestTCINTAndIRQ(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	const (
		tcintmsk = 1 << 2
		tcint    = 1 << 3
		csDev2   = 1 << 9
	)
	setCSR(e, 0, tcintmsk|csDev2)
	if e.IRQAsserted() {
		t.Fatal("IRQ asserted before any transfer")
	}
	immRead(e, 0, 0) // one byte read transfer completes
	if csr(e, 0)&tcint == 0 {
		t.Fatal("TCINT not set after a completed transfer")
	}
	if r32(e, chanBase(0)+0x0C)&1 != 0 {
		t.Fatal("CR TSTART should read back 0 after the synchronous transfer")
	}
	if !e.IRQAsserted() {
		t.Fatal("IRQ not asserted with TCINT set and TCINTMSK enabled")
	}
	// Writing CSR with the TCINT bit clear must preserve
	// TCINT (the status bits are write 1 to clear only).
	setCSR(e, 0, tcintmsk|csDev2)
	if csr(e, 0)&tcint == 0 {
		t.Fatal("TCINT cleared by a CSR write that did not set the TCINT bit")
	}
	if !e.IRQAsserted() {
		t.Fatal("IRQ dropped after a CSR write that left TCINT alone")
	}
	// Writing the TCINT bit clears it.
	setCSR(e, 0, tcintmsk|tcint|csDev2)
	if csr(e, 0)&tcint != 0 {
		t.Fatal("TCINT not cleared by a write 1 to clear")
	}
	if e.IRQAsserted() {
		t.Fatal("IRQ still asserted after TCINT was cleared")
	}
	// With the mask off, a fresh TCINT does not raise IRQ.
	setCSR(e, 0, csDev2)
	immRead(e, 0, 0)
	if csr(e, 0)&tcint == 0 {
		t.Fatal("TCINT not set on the masked transfer")
	}
	if e.IRQAsserted() {
		t.Fatal("IRQ asserted while TCINTMSK is off")
	}
	deselect(e, 0)
}

func TestEXISerializeRoundTrip(t *testing.T) {
	m := newMem()
	e := exi.New(m)
	data := []byte("state!")
	sramWrite(e, m, 0x0008, data)
	e.SetRTC(9000)
	e.Advance(243000000) // half a second into the remainder
	blob := e.Serialize()
	if len(blob) != exi.SerializeSize {
		t.Fatalf("Serialize length = %d, want %d", len(blob), exi.SerializeSize)
	}

	m2 := newMem()
	e2 := exi.New(m2)
	if err := e2.Deserialize(blob); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got := e2.SRAM()[0x08 : 0x08+len(data)]; !bytes.Equal(got, data) {
		t.Fatalf("restored SRAM = %q, want %q", got, data)
	}
	if got := rtcRead(e2, m2); got != 9000 {
		t.Fatalf("restored RTC = %d, want 9000", got)
	}
	e2.Advance(243000000)
	if got := rtcRead(e2, m2); got != 9001 {
		t.Fatalf("restored RTC after the other half second = %d, want 9001 (remainder preserved)", got)
	}
	if err := e2.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Fatal("Deserialize of a short state should fail")
	}
	if got := e2.CardImage(0); len(got) != 2<<20 {
		t.Fatal("Deserialize should leave card images alone")
	}
}
