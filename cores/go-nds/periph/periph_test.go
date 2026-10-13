package periph

import (
	"bytes"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
)

// rig wires a Set onto a bare bus, the way the system does.
type rig struct {
	b   *bus.Bus
	s   *Set
	v9  bus.View
	v7  bus.View
	rom []byte
}

func newRig() *rig {
	r := &rig{rom: make([]byte, 0x8000)}
	for i := range r.rom {
		r.rom[i] = byte(i*7 + 3)
	}
	r.b = bus.New()
	r.b.SetROM(r.rom)
	keys := uint32(0)
	r.s = New(r.b,
		func() []byte { return r.rom },
		func() uint32 { return keys },
		func() (uint16, uint16, bool) { return 100, 50, true })
	r.b.SetIOHook(r.s)
	r.v9 = r.b.ARM9()
	r.v7 = r.b.ARM7()
	return r
}

func TestIRQControlAndAck(t *testing.T) {
	r := newRig()
	r.s.Raise(true, IRQVBlank)
	if r.s.PendingIRQ(true) {
		t.Fatal("pending with IME clear")
	}
	r.v9.Write(0x04000210, 4, 1) // IE = VBlank
	r.v9.Write(0x04000208, 4, 1) // IME = 1
	if !r.s.PendingIRQ(true) {
		t.Fatal("not pending with IME+IE+IF set")
	}
	if got := r.v9.Read(0x04000214, 4); got != 1 {
		t.Fatalf("IF readback: %08x", got)
	}
	r.v9.Write(0x04000214, 4, 1) // ack
	if r.s.PendingIRQ(true) || r.s.IFlags(true) != 0 {
		t.Fatal("IF ack failed")
	}
	// ARM7 side is independent.
	if r.s.IFlags(false) != 0 {
		t.Fatal("ARM7 IF polluted")
	}
}

func TestTimersExactAndCascade(t *testing.T) {
	r := newRig()
	// Timer 0 (ARM9): reload for a 547-tick period at F/1024.
	r.v9.Write(0x04000100, 2, 64989)
	r.v9.Write(0x04000102, 2, 0xC3) // start, IRQ on, F/1024
	r.s.AdvanceTimers(true, 5601900)
	if got := r.v9.Read(0x04000100, 2); got != 64989 {
		t.Fatalf("counter after 10 exact periods: %d want 64989", got)
	}
	if r.s.IFlags(true)&(1<<IRQTimer0) == 0 {
		t.Fatal("timer overflow IRQ not raised")
	}
	r.v9.Write(0x04000214, 4, 1<<IRQTimer0) // ack
	// Remainder is 620 cycles; 547*1024-620 more gives exactly
	// one more overflow, landing back on the reload value.
	r.s.AdvanceTimers(true, 547*1024-620)
	if got := r.v9.Read(0x04000100, 2); got != 64989 {
		t.Fatalf("counter after 11th period: %d want 64989", got)
	}
	if r.s.IFlags(true)&(1<<IRQTimer0) == 0 {
		t.Fatal("11th overflow IRQ not raised")
	}
	// Cascade: timer 2 period 1 tick (reload 0xFFFF) at F/1024,
	// timer 3 cascades from it.
	r.v9.Write(0x04000108, 2, 0xFFFF)
	r.v9.Write(0x0400010A, 2, 0xC3)
	r.v9.Write(0x0400010C, 2, 100)
	r.v9.Write(0x0400010E, 2, 0x84) // start, cascade
	r.s.AdvanceTimers(true, 1024*5)
	if got := r.v9.Read(0x0400010C, 2); got != 105 {
		t.Fatalf("cascade counter: %d want 105", got)
	}
}

func TestDMA9Immediate(t *testing.T) {
	r := newRig()
	src := []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4, 0, 0, 0}
	r.b.LoadBytes(true, 0x02001000, src)
	r.v9.Write(0x040000B0, 4, 0x02001000) // SAD
	r.v9.Write(0x040000B4, 4, 0x02002000) // DAD
	r.v9.Write(0x040000B8, 4, 0x84000004) // enable, 32-bit, 4 units
	for i := 0; i < 4; i++ {
		got := r.v9.Read(0x02002000+uint32(i*4), 4)
		if got != uint32(i+1) {
			t.Fatalf("DMA word %d: %08x", i, got)
		}
	}
	if got := r.v9.Read(0x040000B8, 4); got&(1<<31) != 0 {
		t.Fatal("enable bit not cleared after immediate transfer")
	}
}

func TestIPCLoopbackAndSync(t *testing.T) {
	r := newRig()
	r.v9.Write(0x04000184, 4, 0x8000) // ARM9 FIFO enable
	r.v7.Write(0x04000184, 4, 0x8400) // ARM7 enable + recv IRQ
	r.v9.Write(0x04000188, 4, 0xDEADBEEF)
	if r.s.IFlags(false)&(1<<IRQIPCRecv) == 0 {
		t.Fatal("ARM7 receive IRQ not raised")
	}
	if got := r.v7.Read(0x04100000, 4); got != 0xDEADBEEF {
		t.Fatalf("FIFO ARM9 to ARM7: %08x", got)
	}
	r.v9.Write(0x04000184, 4, 0x8400)
	r.v7.Write(0x04000188, 4, 0x12345678)
	if got := r.v9.Read(0x04100000, 4); got != 0x12345678 {
		t.Fatalf("FIFO ARM7 to ARM9: %08x", got)
	}
	// IPCSYNC: ARM9 enables its sync IRQ, ARM7 sends nibble 5.
	r.v9.Write(0x04000180, 2, 1<<13)
	r.v7.Write(0x04000180, 2, 0x0500)
	if got := r.v9.Read(0x04000180, 2); got&0xF != 5 {
		t.Fatalf("sync nibble: %04x", got)
	}
	if r.s.IFlags(true)&(1<<IRQIPCSync) == 0 {
		t.Fatal("sync IRQ not raised")
	}
	// Underflow sets the error flag.
	r.v7.Read(0x04100000, 4)
	if got := r.v7.Read(0x04000184, 4); got&(1<<14) == 0 {
		t.Fatal("FIFO error flag not set on underflow")
	}
}

func TestCardCommandsAndDMA(t *testing.T) {
	r := newRig()
	// Chip ID command (0xB8), block size code 1 (0x200).
	r.v9.Write(0x040001A8, 4, 0xB8)
	r.v9.Write(0x040001A4, 4, 0x81000000)
	if r.s.IFlags(true)&(1<<IRQCardData) == 0 {
		t.Fatal("card data-ready IRQ not raised")
	}
	for i := 0; i < 4; i++ {
		if got := r.v9.Read(0x04100010, 4); got != ChipID {
			t.Fatalf("chip ID word %d: %08x", i, got)
		}
	}
	// Data read 0xB7 at ROM offset 0x1000, drained by DMA.
	r.v9.Write(0x040001A8, 4, 0xB7)
	r.v9.Write(0x040001AC, 4, 0x00100000) // addr bytes big-endian in cmd[1..3]
	r.v9.Write(0x040001A4, 4, 0x81000000)
	r.v9.Write(0x040000B0, 4, 0x04100010)
	r.v9.Write(0x040000B4, 4, 0x02004000)
	r.v9.Write(0x040000B8, 4, 0xAD000080) // enable, card timing, fixed source, 32-bit, 128 words
	for i := 0; i < 0x200; i++ {
		if r.b.MainRAM[0x4000+i] != r.rom[0x1000+i] {
			t.Fatalf("card DMA byte %d: %02x want %02x", i, r.b.MainRAM[0x4000+i], r.rom[0x1000+i])
		}
	}
	if r.s.IFlags(true)&(1<<IRQCardIO) == 0 {
		t.Fatal("card transfer-complete IRQ not raised")
	}
}

func TestAuxSPIEEPROM(t *testing.T) {
	r := newRig()
	w := func(addr uint32, size int, v uint32) { r.v9.Write(addr, size, v) }
	w(0x040001A0, 2, 0x40) // hold chip select
	w(0x040001A2, 1, 0x06) // WREN
	w(0x040001A2, 1, 0x02) // WRITE
	w(0x040001A2, 1, 0x10) // address
	w(0x040001A2, 1, 0xAA) // data
	w(0x040001A2, 1, 0xBB) // data
	w(0x040001A0, 2, 0x00) // end transaction
	if r.s.SaveRAM()[0x10] != 0xAA || r.s.SaveRAM()[0x11] != 0xBB {
		t.Fatalf("save RAM: %02x %02x", r.s.SaveRAM()[0x10], r.s.SaveRAM()[0x11])
	}
	w(0x040001A0, 2, 0x40)
	w(0x040001A2, 1, 0x03) // READ
	w(0x040001A2, 1, 0x10) // address
	if got := r.v9.Read(0x040001A2, 1); got != 0xAA {
		t.Fatalf("EEPROM read 1: %02x", got)
	}
	if got := r.v9.Read(0x040001A2, 1); got != 0xBB {
		t.Fatalf("EEPROM read 2: %02x", got)
	}
}

func TestSPITouchAndFirmware(t *testing.T) {
	r := newRig()
	// Touchscreen: X channel control 0xD0, ADC for x=100.
	r.v7.Write(0x040001C0, 2, 0x8240)
	r.v7.Write(0x040001C2, 1, 0xD0)
	r.v7.Write(0x040001C2, 1, 0)
	b1 := r.v7.Read(0x040001C2, 1)
	r.v7.Write(0x040001C2, 1, 0)
	b2 := r.v7.Read(0x040001C2, 1)
	if adc := (b1<<8 | b2) >> 3; adc != 0x67D {
		t.Fatalf("touch X ADC: %03x want 67d (b1 %02x b2 %02x)", adc, b1, b2)
	}
	// Firmware: read the settings version byte at 0x3FE00.
	r.v7.Write(0x040001C0, 2, 0x0000)
	r.v7.Write(0x040001C0, 2, 0x8140)
	for _, b := range []byte{0x03, 0x03, 0xFE, 0x00} {
		r.v7.Write(0x040001C2, 1, uint32(b))
	}
	r.v7.Write(0x040001C2, 1, 0)
	if got := r.v7.Read(0x040001C2, 1); got != 5 {
		t.Fatalf("firmware settings version: %02x want 05", got)
	}
}

func TestFirmwareSettingsBlock(t *testing.T) {
	fw := buildFirmware()
	if len(fw) != FirmwareSize {
		t.Fatalf("firmware size %d", len(fw))
	}
	blk := fw[SettingsOffset : SettingsOffset+0x100]
	if blk[0] != 5 {
		t.Fatalf("settings version %d", blk[0])
	}
	crc := uint16(blk[0x70]) | uint16(blk[0x71])<<8
	if crc != crc16Settings(blk[:0x70]) {
		t.Fatal("settings CRC mismatch")
	}
	dup := fw[SettingsOffset+0x100 : SettingsOffset+0x200]
	if !bytes.Equal(blk, dup) {
		t.Fatal("duplicate settings block differs")
	}
	if got := uint16(blk[0x58]) | uint16(blk[0x59])<<8; got != 0x0200 {
		t.Fatalf("calibration ADC X1: %04x", got)
	}
}

func TestRTCStub(t *testing.T) {
	r := newRig()
	if got := r.v7.Read(0x04000138, 1); got != 0x00 {
		t.Fatalf("RTC seconds at epoch: %02x", got)
	}
	for i := 0; i < 60; i++ {
		r.s.FrameTick()
	}
	if r.s.IFlags(false)&(1<<IRQRTC) == 0 {
		t.Fatal("RTC IRQ not raised after one second")
	}
	r.v7.Write(0x04000138, 1, 0) // reset read index
	if got := r.v7.Read(0x04000138, 1); got != 0x01 {
		t.Fatalf("RTC seconds after 60 frames: %02x want 01", got)
	}
}

func TestPeriphStateRoundTrip(t *testing.T) {
	r := newRig()
	r.v9.Write(0x04000100, 2, 1234)
	r.v9.Write(0x04000102, 2, 0xC1)
	r.s.AdvanceTimers(true, 5000)
	r.v9.Write(0x04000184, 4, 0x8000)
	r.v9.Write(0x04000188, 4, 0xFEEDFACE)
	r.s.rtc.frames = 77
	r.s.SaveRAM()[3] = 0x5A
	blob := make([]byte, StateSize)
	if n := r.s.Serialize(blob); n != StateSize {
		t.Fatalf("serialize n = %d want %d", n, StateSize)
	}
	r2 := newRig()
	if n := r2.s.Deserialize(blob); n != StateSize {
		t.Fatalf("deserialize n = %d want %d", n, StateSize)
	}
	blob2 := make([]byte, StateSize)
	r2.s.Serialize(blob2)
	if !bytes.Equal(blob, blob2) {
		t.Fatal("state bytes differ after round trip")
	}
	if got := r2.v9.Read(0x04000100, 2); got != r.v9.Read(0x04000100, 2) {
		t.Fatalf("timer counter restored: %d", got)
	}
	if got := r2.v7.Read(0x04100000, 4); got != 0xFEEDFACE {
		t.Fatalf("FIFO word restored: %08x", got)
	}
	if r2.s.rtc.frames != 77 || r2.s.SaveRAM()[3] != 0x5A {
		t.Fatal("scalar state not restored")
	}
}
