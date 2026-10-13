package system

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/disc"
)

func d(op, f1v, f2v, imm uint32) uint32 {
	return op<<26 | f1v<<21 | f2v<<16 | imm&0xFFFF
}

func x(f1v, f2v, f3v, xov uint32) uint32 {
	return 31<<26 | f1v<<21 | f2v<<16 | f3v<<11 | xov<<1
}

func put32(b []byte, off uint32, v uint32) {
	binary.BigEndian.PutUint32(b[off:], v)
}

// testProgram is a hand-assembled PowerPC program: it
// sums the 16 data bytes at 0x80002000 into the word at
// 0x80003000, loads the decrementer with 40, enables
// external interrupts (which also drops the interrupt
// prefix, so the decrementer vector is physical 0x900),
// and spins. A one-word handler (b .) sits at 0x900.
func testProgram() []uint32 {
	mtsprDEC := uint32(31<<26 | 8<<21 | 704<<11 | 467<<1) // mtspr 22, r8
	return []uint32{
		d(14, 3, 0, 0),                  // addi r3, r0, 0        ; sum = 0
		d(15, 4, 0, 0x8000),             // addis r4, r0, 0x8000  ; r4 = 0x80000000
		d(14, 4, 4, 0x2000),             // addi r4, r4, 0x2000   ; data pointer
		d(14, 5, 0, 16),                 // addi r5, r0, 16       ; count
		d(34, 6, 4, 0),                  // loop: lbz r6, 0(r4)
		x(3, 3, 6, 266),                 // add r3, r3, r6
		d(14, 4, 4, 1),                  // addi r4, r4, 1
		d(14, 5, 5, 0xFFFF),             // addi r5, r5, -1
		d(11, 0, 5, 0),                  // cmpwi r5, 0
		16<<26 | 4<<21 | 2<<16 | 0xFFEC, // bne loop (-20)
		d(15, 7, 0, 0x8000),             // addis r7, r0, 0x8000
		d(14, 7, 7, 0x3000),             // addi r7, r7, 0x3000
		d(36, 3, 7, 0),                  // stw r3, 0(r7)
		d(14, 8, 0, 40),                 // addi r8, r0, 40
		mtsprDEC,                        // mtspr DEC, r8
		d(24, 0, 9, 0x8000),             // ori r9, r0, 0x8000 (MSR EE)
		x(9, 0, 0, 146),                 // mtmsr r9
		18 << 26,                        // spin: b .
	}
}

// syntheticImage builds a GC disc image whose DOL contains
// testProgram plus the decrementer handler and data.
func syntheticImage() []byte {
	data := make([]byte, 0x10000)
	data[0] = 'G'
	copy(data[0x20:], "System Synthetic")
	put32(data, 0x1C, disc.GCMagic)
	put32(data, 0x420, 0x5000) // DOL offset

	dol := uint32(0x5000)
	prog := testProgram()
	put32(data, dol+0x00, 0x100)      // text0 offset
	put32(data, dol+0x48, 0x80001000) // text0 address
	put32(data, dol+0x90, uint32(4*len(prog)))
	put32(data, dol+0x04, 0x200)      // text1 offset
	put32(data, dol+0x4C, 0x80000900) // text1 address (the DEC vector page)
	put32(data, dol+0x94, 4)
	put32(data, dol+0x1C, 0x300)      // data0 offset
	put32(data, dol+0x64, 0x80002000) // data0 address
	put32(data, dol+0xAC, 16)
	put32(data, dol+0xE0, 0x80001000) // entry

	for i, w := range prog {
		put32(data, 0x5100+uint32(4*i), w)
	}
	put32(data, 0x5200, 18<<26) // handler: b .
	for i := 0; i < 16; i++ {
		data[0x5300+i] = byte(i + 1)
	}
	return data
}

func loadMachine(t *testing.T) *Machine {
	t.Helper()
	img, err := disc.Parse(syntheticImage())
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMemoryMap(t *testing.T) {
	m := New()
	m.Write32(0x80000000, 0xDEADBEEF)
	if got := m.Read32(0x00000000); got != 0xDEADBEEF {
		t.Fatalf("physical alias read = %#x", got)
	}
	if got := m.Read32(0xC0000000); got != 0xDEADBEEF {
		t.Fatalf("uncached alias read = %#x", got)
	}
	m.Write16(0x0C001234, 0xABCD)
	if got := m.Read16(0x0C001234); got != 0xABCD {
		t.Fatalf("hw page = %#x", got)
	}
	m.Write8(0x0D00FFFF, 0x5A)
	if got := m.Read8(0x0D00FFFF); got != 0x5A {
		t.Fatalf("hw page 0D = %#x", got)
	}
	m.Write32(0xE0000100, 0x12345678)
	if got := m.Read32(0xE0000100); got != 0x12345678 {
		t.Fatalf("locked cache = %#x", got)
	}
	if got := m.Read32(0xFFF00100); got != 0 {
		t.Fatalf("IPL stub read = %#x, want 0", got)
	}
	m.Write32(0xFFF00100, 0xFFFFFFFF)
	if got := m.Read32(0xFFF00100); got != 0 {
		t.Fatalf("IPL stub accepted a write")
	}
	if got := m.Read32(0x10000000); got != 0 {
		t.Fatalf("unmapped read = %#x", got)
	}
}

func TestLoadAndRunWithSaveState(t *testing.T) {
	m := loadMachine(t)
	if m.CPU().PC != 0x80001000 {
		t.Fatalf("entry PC = %#x", m.CPU().PC)
	}
	if m.Started() {
		t.Fatal("machine should start halted")
	}
	// Halted: cycles pass, nothing executes, no result.
	m.RunCycles(200)
	if got := m.Read32(0x80003000); got != 0 {
		t.Fatalf("halted machine produced %#x", got)
	}
	if got := m.Read8(0x80002000); got != 1 {
		t.Fatalf("data section not staged: %#x", got)
	}
	if got := m.Read8(0x8000200F); got != 16 {
		t.Fatalf("data section end: %#x", got)
	}

	m.SetButtons(PadState{Buttons: PadA | PadStart, TriggerL: 12, StickX: 200, CStickY: 33})
	m.Start()
	// 103 instructions take the program through the stw
	// of the checksum (4 setup + 96 loop + 3).
	m.RunCycles(103)
	if got := m.Read32(0x80003000); got != 136 {
		t.Fatalf("checksum = %d, want 136", got)
	}
	state1 := m.Serialize()
	if len(state1) != StateSize {
		t.Fatalf("state size = %d, want %d", len(state1), StateSize)
	}

	// Continue: the decrementer (40 ticks of 4 cycles,
	// loaded at instruction 105, EE enabled at 107) fires
	// and the CPU lands in the handler at 0x900 spinning.
	m.RunCycles(400)
	if pc := m.CPU().PC; pc != 0x900 {
		t.Fatalf("PC after DEC exception = %#x, want 0x900", pc)
	}
	if srr0 := m.CPU().SRR0; srr0 != 0x80001044 {
		t.Fatalf("SRR0 = %#x, want the spin loop 0x80001044", srr0)
	}
	stateA := m.Serialize()

	// A fresh machine restored from the mid-run state must
	// continue bit-identically.
	m2 := loadMachine(t)
	if err := m2.Deserialize(state1); err != nil {
		t.Fatal(err)
	}
	if !m2.Started() {
		t.Fatal("restored machine lost its started flag")
	}
	if m2.Buttons() != m.Buttons() {
		t.Fatalf("restored buttons = %+v", m2.Buttons())
	}
	m2.RunCycles(400)
	if pc := m2.CPU().PC; pc != 0x900 {
		t.Fatalf("restored PC = %#x, want 0x900", pc)
	}
	if got := m2.Read32(0x80003000); got != 136 {
		t.Fatalf("restored checksum = %d", got)
	}
	if !bytes.Equal(m2.Serialize(), stateA) {
		t.Fatal("continuation after restore diverged from the uninterrupted run")
	}
}

func TestFramebufferAndBadStates(t *testing.T) {
	m := New()
	fb := m.Framebuffer()
	if len(fb) != Width*Height {
		t.Fatalf("framebuffer = %d pixels", len(fb))
	}
	for i, px := range fb {
		if px != 0 {
			t.Fatalf("framebuffer pixel %d = %#x, want blank", i, px)
		}
	}
	if CyclesPerFrame != 8_100_000 {
		t.Fatalf("CyclesPerFrame = %d", CyclesPerFrame)
	}
	if err := m.Deserialize([]byte{1, 2, 3}); err != ErrBadState {
		t.Fatalf("short state err = %v", err)
	}
	bad := m.Serialize()
	bad[0] = 99
	if err := m.Deserialize(bad); err != ErrBadState {
		t.Fatalf("wrong version err = %v", err)
	}
}

func TestLoadWiiDiscFails(t *testing.T) {
	data := make([]byte, 0x50000)
	put32(data, 0x18, disc.WiiMagic)
	img, err := disc.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.LoadDisc(img); err != disc.ErrWiiDisc {
		t.Fatalf("LoadDisc(Wii) err = %v", err)
	}
}
