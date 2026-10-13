package system

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func rI(op, rs, rt, imm uint32) uint32 { return op<<26 | rs<<21 | rt<<16 | (imm & 0xFFFF) }
func rR(rs, rt, rd, sh, fn uint32) uint32 {
	return rs<<21 | rt<<16 | rd<<11 | sh<<6 | fn
}

const (
	t0 = 8
	t1 = 9
	t2 = 10
	t3 = 11
	t4 = 12
	t5 = 13
	t6 = 14
)

// checksumProgram sums 16 words at 0x80011000 into
// 0x80012000, then spins. Register plan: t0 = base
// 0x80010000, t1 = count, t2 = sum, t3 = i.
func checksumProgram() []uint32 {
	return []uint32{
		rI(0x0F, 0, t0, 0x8001),  // lui t0, 0x8001
		rI(0x09, 0, t1, 16),      // addiu t1, 16
		rI(0x09, 0, t2, 0),       // addiu t2, 0
		rI(0x09, 0, t3, 0),       // addiu t3, 0
		rR(0, t3, t5, 2, 0x00),   // loop: sll t5, t3, 2
		rR(t0, t5, t6, 0, 0x21),  // addu t6, t0, t5
		rI(0x23, t6, t4, 0x1000), // lw t4, 0x1000(t6)
		0,                        // nop (load delay)
		rR(t2, t4, t2, 0, 0x21),  // addu t2, t2, t4
		rI(0x09, t3, t3, 1),      // addiu t3, 1
		rI(0x05, t3, t1, 0xFFF9), // bne t3, t1, loop (-7)
		0,                        // nop
		rI(0x2B, t0, t2, 0x2000), // sw t2, 0x2000(t0)
		rI(0x04, 0, 0, 0xFFFF),   // spin: beq zero, zero, -1
		0,                        // nop
	}
}

func loadChecksumMachine(t *testing.T) *Machine {
	t.Helper()
	m := New()
	for i, w := range checksumProgram() {
		m.Bus.Write32(0x80010000+4*uint32(i), w)
	}
	for i := 0; i < 16; i++ {
		m.Bus.Write32(0x80011000+4*uint32(i), uint32(i+1))
	}
	m.CPU.PC = 0x80010000
	m.CPU.NextPC = 0x80010004
	return m
}

func TestSyntheticChecksumAndSaveState(t *testing.T) {
	m := loadChecksumMachine(t)
	for i := 0; i < 40; i++ {
		m.Step()
	}
	save := m.Serialize()
	if len(save) != m.StateSize() {
		t.Fatalf("state size = %d, want %d", len(save), m.StateSize())
	}
	m.RunCycles(2000)
	result := m.Bus.Read32(0x80012000)
	if result != 136 {
		t.Fatalf("checksum = %d, want 136 (sum of 1..16)", result)
	}
	finalState := m.Serialize()

	m2 := New()
	if err := m2.Deserialize(save); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	m2.RunCycles(2000)
	if got := m2.Bus.Read32(0x80012000); got != result {
		t.Errorf("restored checksum = %d, want %d", got, result)
	}
	if m2.CPU.Regs != m.CPU.Regs {
		t.Errorf("registers diverged after restore")
	}
	if !bytes.Equal(m2.Serialize(), finalState) {
		t.Errorf("full state diverged after restore and identical continuation")
	}
}

func TestMachineBasics(t *testing.T) {
	m := New()
	// PS1-2 contract change: the framebuffer is the
	// GPU display area at native resolution (the
	// documented reset default is 256x240, display
	// off), not the fixed 640x480 blank of PS1-1.
	fb := m.Framebuffer()
	if len(fb) != m.DisplayWidth()*m.DisplayHeight() {
		t.Fatalf("framebuffer = %d pixels, want %d (native display)", len(fb), m.DisplayWidth()*m.DisplayHeight())
	}
	if fb[0] != 0xFF000000 || fb[len(fb)-1] != 0xFF000000 {
		t.Errorf("framebuffer not blank black")
	}
	m.SetButtons(ButtonStart | ButtonCross)
	if m.Buttons() != ButtonStart|ButtonCross {
		t.Errorf("Buttons = 0x%X, want 0x%X", m.Buttons(), uint32(ButtonStart|ButtonCross))
	}
	if err := m.LoadDisc(nil); err == nil {
		t.Errorf("LoadDisc(nil) accepted, want error")
	}
	if err := m.Deserialize([]byte{1}); err == nil {
		t.Errorf("short state accepted, want error")
	}
	bad := m.Serialize()
	bad[0] = 99
	if err := m.Deserialize(bad); err == nil {
		t.Errorf("bad version state accepted, want error")
	}
}

func TestRunFrameBudget(t *testing.T) {
	m := New()
	m.RunFrame()
	if m.Frames() != 1 {
		t.Errorf("Frames = %d, want 1", m.Frames())
	}
	// NTSC frame budget: 33,868,800 / 59.94 = 565,045.04
	// cycles; RunCycles may overshoot by one instruction.
	c := m.CPU.Cycles()
	if c < 565045 || c > 565045+8 {
		t.Errorf("frame consumed %d CPU cycles, want about 565045", c)
	}
}

func TestOpenDiscFromFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "flat.iso")
	if err := os.WriteFile(p, make([]byte, 2048*20), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.OpenDisc(p); err != nil {
		t.Fatalf("OpenDisc: %v", err)
	}
	if m.Disc == nil {
		t.Fatalf("Disc is nil after OpenDisc")
	}
	tracks := m.Disc.Tracks()
	if len(tracks) != 1 || tracks[0].Length != 20 {
		t.Errorf("tracks = %+v, want one 20-sector track", tracks)
	}
}
