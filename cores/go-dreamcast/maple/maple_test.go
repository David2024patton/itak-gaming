package maple

import (
	"bytes"
	"encoding/binary"
	"testing"
)

const (
	testBase  = 0x005F6C00
	ramWindow = 0x0C000000
	ramSize   = 1 << 20

	tableAddr = 0x0C010000
	recvA     = 0x0C020000
	recvB     = 0x0C021000
)

// fakeMem is a byte slice standing in for the main RAM window.
type fakeMem struct {
	b []byte
}

func newFakeMem() *fakeMem { return &fakeMem{b: make([]byte, ramSize)} }

func (f *fakeMem) Read32(pa uint32) uint32 {
	off := pa - ramWindow
	if off+4 > uint32(len(f.b)) {
		return 0
	}
	return binary.LittleEndian.Uint32(f.b[off:])
}

func (f *fakeMem) Write32(pa uint32, v uint32) {
	off := pa - ramWindow
	if off+4 > uint32(len(f.b)) {
		return
	}
	binary.LittleEndian.PutUint32(f.b[off:], v)
}

// fakeSink records the Holly normal bank bits raised to it.
type fakeSink struct {
	raised  uint32
	cleared uint32
}

func (s *fakeSink) RaiseNormal(bits uint32) { s.raised |= bits }
func (s *fakeSink) ClearNormal(bits uint32) { s.cleared |= bits }

// frame builds a frame word slice: header word then payload.
func frame(cmd, recipient, sender byte, payload ...uint32) []uint32 {
	out := make([]uint32, 0, len(payload)+1)
	head := uint32(cmd)<<24 | uint32(recipient)<<16 | uint32(sender)<<8 | uint32(len(payload))
	return append(append(out, head), payload...)
}

// writeEntry writes one DMA table entry at addr and returns the
// address of the next entry.
func writeEntry(f *fakeMem, addr, recv uint32, last bool, fr []uint32) uint32 {
	hdr := uint32(len(fr) - 1)
	if last {
		hdr |= 1 << 31
	}
	f.Write32(addr, hdr)
	f.Write32(addr+4, recv)
	for i, w := range fr {
		f.Write32(addr+8+uint32(i)*4, w)
	}
	return addr + uint32(2+len(fr))*4
}

// start programs MDSTAR, enables DMA, and fires the software
// trigger.
func start(m *Maple, table uint32) {
	m.Write32(testBase+regMDSTAR, table)
	m.Write32(testBase+regMDEN, 1)
	m.Write32(testBase+regMDST, 1)
}

// runOne runs a single entry table holding fr with receive
// buffer recv against a fresh Maple on f.
func runOne(f *fakeMem, m *Maple, recv uint32, fr []uint32) {
	writeEntry(f, tableAddr, recv, true, fr)
	start(m, tableAddr)
}

func bytesToWords(b []byte) []uint32 {
	out := make([]uint32, len(b)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return out
}

func wordsToBytes(ws []uint32) []byte {
	out := make([]byte, len(ws)*4)
	for i, w := range ws {
		binary.LittleEndian.PutUint32(out[i*4:], w)
	}
	return out
}

// Test 1: Device Request on port 0 returns the controller device
// info and raises the DMA done event.
func TestDeviceRequestPort0(t *testing.T) {
	f := newFakeMem()
	sink := &fakeSink{}
	m := New(f, sink)

	runOne(f, m, recvA, frame(1, 0x20, 0x00))

	head := f.Read32(recvA)
	wantHead := uint32(5)<<24 | uint32(0x00)<<16 | uint32(0x20)<<8 | 28
	if head != wantHead {
		t.Fatalf("response header = %#08x, want %#08x", head, wantHead)
	}
	if got := f.Read32(recvA + 4); got != 0x01000000 {
		t.Fatalf("function word = %#08x, want 0x01000000", got)
	}
	if got := f.Read32(recvA + 8); got != 0xFE060F00 {
		t.Fatalf("capability word = %#08x, want 0xFE060F00", got)
	}
	if sink.raised&(1<<12) == 0 {
		t.Fatalf("sink raised = %#x, want bit 12 set", sink.raised)
	}
	if got := m.Read32(testBase + regMDST); got != 0 {
		t.Fatalf("MDST after transfer = %#x, want 0", got)
	}
}

// Test 2: Get Condition packs buttons and analogs exactly.
// A and Start pressed, TriggerL 200, TriggerR 0, JoyX 0,
// JoyY 255.
func TestGetCondition(t *testing.T) {
	f := newFakeMem()
	m := New(f, &fakeSink{})
	m.SetPad(0, PadState{
		Buttons:  (1 << 0) | (1 << 4), // A + Start in the system mask
		TriggerL: 200,
		TriggerR: 0,
		JoyX:     0,
		JoyY:     255,
	})

	runOne(f, m, recvA, frame(9, 0x20, 0x00, 0x01000000))

	head := f.Read32(recvA)
	wantHead := uint32(7)<<24 | uint32(0x20)<<8 | 3
	if head != wantHead {
		t.Fatalf("response header = %#08x, want %#08x", head, wantHead)
	}
	if got := f.Read32(recvA + 4); got != 0x01000000 {
		t.Fatalf("payload word 0 = %#08x, want function 0x01000000", got)
	}
	// Wire bits: A is 1<<2 and Start is 1<<3, so pressed is
	// 0x000C and active low is 0xFFF3. Word 1 is active low in
	// the high half, rtrig 0, ltrig 200.
	wantW1 := uint32(0xFFF3)<<16 | uint32(0)<<8 | 200
	if got := f.Read32(recvA + 8); got != wantW1 {
		t.Fatalf("condition word 1 = %#08x, want %#08x", got, wantW1)
	}
	// Word 2: joyx 0, joyy 255, second stick centered at 128.
	wantW2 := uint32(0) | uint32(255)<<8 | uint32(128)<<16 | uint32(128)<<24
	if got := f.Read32(recvA + 12); got != wantW2 {
		t.Fatalf("condition word 2 = %#08x, want %#08x", got, wantW2)
	}
}

// Test 3: VMU block write then block read through the DMA path
// round trips 512 bytes at block 7.
func TestVMUBlockRoundTrip(t *testing.T) {
	f := newFakeMem()
	m := New(f, &fakeSink{})

	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i*3 + 5)
	}
	writePayload := []uint32{0x02000000, 7}
	writePayload = append(writePayload, bytesToWords(data)...)
	runOne(f, m, recvA, frame(12, 0x21, 0x00, writePayload...))

	head := f.Read32(recvA)
	wantHead := uint32(7)<<24 | uint32(0x21)<<8 | 2
	if head != wantHead {
		t.Fatalf("write reply header = %#08x, want %#08x", head, wantHead)
	}
	if got := f.Read32(recvA + 8); got != 7 {
		t.Fatalf("write reply address echo = %#x, want 7", got)
	}
	img := m.VMUImage(0)
	if img == nil || len(img) != 128*1024 {
		t.Fatalf("VMUImage length = %d, want %d", len(img), 128*1024)
	}
	if !bytes.Equal(img[7*512:8*512], data) {
		t.Fatal("VMU image block 7 does not match the written data")
	}

	runOne(f, m, recvB, frame(11, 0x21, 0x00, 0x02000000, 7))
	head = f.Read32(recvB)
	wantHead = uint32(8)<<24 | uint32(0x21)<<8 | 130
	if head != wantHead {
		t.Fatalf("read reply header = %#08x, want %#08x", head, wantHead)
	}
	if got := f.Read32(recvB + 4); got != 0x02000000 {
		t.Fatalf("read reply function = %#08x, want 0x02000000", got)
	}
	if got := f.Read32(recvB + 8); got != 7 {
		t.Fatalf("read reply address echo = %#x, want 7", got)
	}
	gotWords := make([]uint32, 128)
	for i := range gotWords {
		gotWords[i] = f.Read32(recvB + 12 + uint32(i)*4)
	}
	if got := wordsToBytes(gotWords); !bytes.Equal(got, data) {
		t.Fatal("block read data does not match the written data")
	}
}

// Test 4: wrong function on Get Condition is a device error with
// the function as payload, and an unknown command byte gets the
// unknown command response.
func TestErrors(t *testing.T) {
	f := newFakeMem()
	m := New(f, &fakeSink{})

	runOne(f, m, recvA, frame(9, 0x20, 0x00, 0xDEADBEEF))
	head := f.Read32(recvA)
	wantHead := uint32(0xFC)<<24 | uint32(0x20)<<8 | 1
	if head != wantHead {
		t.Fatalf("device error header = %#08x, want %#08x", head, wantHead)
	}
	if got := f.Read32(recvA + 4); got != 0xDEADBEEF {
		t.Fatalf("device error payload = %#08x, want 0xDEADBEEF", got)
	}

	runOne(f, m, recvB, frame(0x55, 0x20, 0x00))
	head = f.Read32(recvB)
	wantHead = uint32(0xFE)<<24 | uint32(0x20)<<8 | 0
	if head != wantHead {
		t.Fatalf("unknown command header = %#08x, want %#08x", head, wantHead)
	}
}

// Test 5: a frame to an empty sub device address gets the single
// no device word.
func TestEmptySubAddress(t *testing.T) {
	f := newFakeMem()
	m := New(f, &fakeSink{})
	f.Write32(recvA, 0x12345678)

	runOne(f, m, recvA, frame(1, 0x22, 0x00))

	if got := f.Read32(recvA); got != 0xFFFFFFFF {
		t.Fatalf("receive word = %#08x, want 0xFFFFFFFF", got)
	}
}

// Test 6: a two entry table serves both entries, with the last
// flag on the second.
func TestMultiEntryTable(t *testing.T) {
	f := newFakeMem()
	sink := &fakeSink{}
	m := New(f, sink)
	m.SetPad(1, PadState{Buttons: 1 << 1, JoyX: 128, JoyY: 128}) // B

	next := writeEntry(f, tableAddr, recvA, false, frame(1, 0x20, 0x00))
	writeEntry(f, next, recvB, true, frame(9, 0x60, 0x40, 0x01000000))
	start(m, tableAddr)

	headA := f.Read32(recvA)
	wantA := uint32(5)<<24 | uint32(0x20)<<8 | 28
	if headA != wantA {
		t.Fatalf("port 0 header = %#08x, want %#08x", headA, wantA)
	}
	headB := f.Read32(recvB)
	wantB := uint32(7)<<24 | uint32(0x40)<<16 | uint32(0x60)<<8 | 3
	if headB != wantB {
		t.Fatalf("port 1 header = %#08x, want %#08x", headB, wantB)
	}
	if got := f.Read32(recvB + 4); got != 0x01000000 {
		t.Fatalf("port 1 function = %#08x, want 0x01000000", got)
	}
	if sink.raised&(1<<12) == 0 {
		t.Fatal("sink did not get the DMA done bit")
	}
	if got := m.Read32(testBase + regMTXDAD); got != next+uint32(2+2)*4 {
		t.Fatalf("MTXDAD = %#x, want address past the last entry", got)
	}
}

// Test 7: serialize and deserialize preserve VMU contents,
// registers, and pad state.
func TestSerializeRoundTrip(t *testing.T) {
	f := newFakeMem()
	m := New(f, &fakeSink{})

	img := m.VMUImage(2)
	for i := 0; i < 512; i++ {
		img[3*512+i] = byte(i ^ 0x5A)
	}
	m.SetPad(3, PadState{Buttons: 0x155, TriggerL: 9, TriggerR: 250, JoyX: 1, JoyY: 254})
	m.Write32(testBase+regMDSTAR, 0x0C030000)
	m.Write32(testBase+regMDTSEL, 1)
	m.Write32(testBase+regMSYS, 0x12345678)
	m.Write32(testBase+regMDAPRO, 0x61550001)
	m.Write32(testBase+regMMSEL, 0)

	dst := make([]byte, SerializeSize)
	if n := m.Serialize(dst); n != SerializeSize {
		t.Fatalf("Serialize returned %d, want %d", n, SerializeSize)
	}
	if n := m.Serialize(make([]byte, 8)); n != 0 {
		t.Fatalf("Serialize into a short buffer returned %d, want 0", n)
	}

	f2 := newFakeMem()
	m2 := New(f2, &fakeSink{})
	if !m2.Deserialize(dst) {
		t.Fatal("Deserialize rejected a valid state")
	}
	if m2.Deserialize(make([]byte, 8)) {
		t.Fatal("Deserialize accepted a short state")
	}
	if !bytes.Equal(m2.VMUImage(2), m.VMUImage(2)) {
		t.Fatal("VMU image did not survive the round trip")
	}
	if got := m2.Read32(testBase + regMDSTAR); got != 0x0C030000 {
		t.Fatalf("MDSTAR = %#x, want 0x0C030000", got)
	}
	if got := m2.Read32(testBase + regMDTSEL); got != 1 {
		t.Fatalf("MDTSEL = %#x, want 1", got)
	}
	if got := m2.Read32(testBase + regMSYS); got != 0x12345678 {
		t.Fatalf("MSYS = %#x, want 0x12345678", got)
	}
	if got := m2.Read32(testBase + regMMSEL); got != 0 {
		t.Fatalf("MMSEL = %#x, want 0", got)
	}
	dst2 := make([]byte, SerializeSize)
	m2.Serialize(dst2)
	if !bytes.Equal(dst, dst2) {
		t.Fatal("second serialize differs: pad or VMU state did not round trip")
	}
}

// Extra: vblank trigger mode ignores the software start bit and
// runs on OnVblankOut.
func TestVblankTriggerMode(t *testing.T) {
	f := newFakeMem()
	sink := &fakeSink{}
	m := New(f, sink)
	writeEntry(f, tableAddr, recvA, true, frame(1, 0x20, 0x00))
	m.Write32(testBase+regMDSTAR, tableAddr)
	m.Write32(testBase+regMDTSEL, 1)
	m.Write32(testBase+regMDEN, 1)

	m.Write32(testBase+regMDST, 1) // ignored in vblank mode
	if got := f.Read32(recvA); got != 0 {
		t.Fatalf("receive buffer = %#x after ignored MDST write, want 0", got)
	}
	m.OnVblankOut()
	head := f.Read32(recvA)
	wantHead := uint32(5)<<24 | uint32(0x20)<<8 | 28
	if head != wantHead {
		t.Fatalf("header after vblank = %#08x, want %#08x", head, wantHead)
	}
	if sink.raised&(1<<12) == 0 {
		t.Fatal("sink did not get the DMA done bit after vblank transfer")
	}
}

// Extra: power on values and stored registers.
func TestRegisterDefaults(t *testing.T) {
	m := New(newFakeMem(), &fakeSink{})
	if got := m.Read32(testBase + regMSYS); got != 0x3A980000 {
		t.Fatalf("MSYS power on = %#08x, want 0x3A980000", got)
	}
	if got := m.Read32(testBase + regMMSEL); got != 1 {
		t.Fatalf("MMSEL power on = %#x, want 1", got)
	}
	if got := m.Read32(testBase + regMST); got != 0xFF {
		t.Fatalf("MST = %#x, want 0xFF", got)
	}
	if got := SerializeSize; got != 524360 {
		t.Fatalf("SerializeSize = %d, want 524360", got)
	}
}
