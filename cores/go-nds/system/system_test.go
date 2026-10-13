package system

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// syntheticROM builds the DS-1 proof cartridge: the ARM9 program
// writes a magic word into shared WRAM bank 0, and the ARM7
// program reads it back, increments it, and writes the result to
// the next word. Both programs are hand-assembled below.
//
//	ARM9 (loads at 0x02000000):
//	  MOV R1, #0x03000000    E3A01403
//	  MOV R2, #0xAB000000    E3A024AB
//	  STR R2, [R1]           E5812000
//	  B .                    EAFFFFFE
//
//	ARM7 (loads at 0x03800000):
//	  MOV R1, #0x03000000    E3A01403
//	  LDR R2, [R1]           E5912000
//	  ADD R2, R2, #1         E2822001
//	  STR R2, [R1, #4]       E5812004
//	  B .                    EAFFFFFE
func syntheticROM() []byte {
	rom := make([]byte, 0x4000)
	copy(rom[0x000:], "DS1 PROOF")
	copy(rom[0x00C:], "ADSP")
	copy(rom[0x010:], "01")
	le := binary.LittleEndian
	le.PutUint32(rom[0x020:], 0x1000)
	le.PutUint32(rom[0x024:], 0x02000000)
	le.PutUint32(rom[0x028:], 0x02000000)
	le.PutUint32(rom[0x02C:], 16)
	le.PutUint32(rom[0x030:], 0x2000)
	le.PutUint32(rom[0x034:], 0x03800000)
	le.PutUint32(rom[0x038:], 0x03800000)
	le.PutUint32(rom[0x03C:], 20)
	arm9prog := []uint32{0xE3A01403, 0xE3A024AB, 0xE5812000, 0xEAFFFFFE}
	for i, w := range arm9prog {
		le.PutUint32(rom[0x1000+i*4:], w)
	}
	arm7prog := []uint32{0xE3A01403, 0xE5912000, 0xE2822001, 0xE5812004, 0xEAFFFFFE}
	for i, w := range arm7prog {
		le.PutUint32(rom[0x2000+i*4:], w)
	}
	return rom
}

func sharedWord(m *Machine, off int) uint32 {
	return binary.LittleEndian.Uint32(m.Bus.Shared[off:])
}

// TestDualCPUDeterminism is the DS-1 system gate: the two CPUs
// communicate through shared WRAM under WRAMCNT ownership, and a
// mid-run save state restores to a byte-identical continuation.
func TestDualCPUDeterminism(t *testing.T) {
	rom := syntheticROM()

	// Phase 1: ARM9 owns both shared banks and writes the magic.
	m := New()
	if err := m.LoadROM(rom); err != nil {
		t.Fatal(err)
	}
	m.Bus.SetWRAMCNT(3)
	m.Start9()
	m.RunFrame()
	if got := sharedWord(m, 0); got != 0xAB000000 {
		t.Fatalf("phase 1: shared word 0 = %08x, want AB000000", got)
	}

	// Save mid-run, with the ARM7 not yet started.
	saved := m.Serialize()
	if len(saved) != StateSize {
		t.Fatalf("state size: got %d want %d", len(saved), StateSize)
	}

	// Phase 2: hand both banks to the ARM7 and let it answer.
	m.Bus.SetWRAMCNT(0)
	m.Start7()
	m.RunFrame()
	if got := sharedWord(m, 4); got != 0xAB000001 {
		t.Fatalf("phase 2: shared word 1 = %08x, want AB000001", got)
	}
	wantShared := append([]byte(nil), m.Bus.Shared...)
	wantState := m.Serialize()

	// Restore the mid-run state into a fresh machine and replay
	// phase 2: the outcome must be byte-identical.
	f := New()
	if err := f.LoadROM(rom); err != nil {
		t.Fatal(err)
	}
	if !f.Deserialize(saved) {
		t.Fatal("Deserialize refused the mid-run state")
	}
	f.Bus.SetWRAMCNT(0)
	f.Start7()
	f.RunFrame()
	if !bytes.Equal(f.Bus.Shared, wantShared) {
		t.Fatal("restored run: shared WRAM differs from the direct run")
	}
	if got := f.Serialize(); !bytes.Equal(got, wantState) {
		t.Fatal("restored run: full state differs from the direct run")
	}
}

func TestLoadROMStaging(t *testing.T) {
	m := New()
	if err := m.LoadROM(syntheticROM()); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(m.Bus.MainRAM[0:]); got != 0xE3A01403 {
		t.Errorf("ARM9 binary not staged in main RAM: %08x", got)
	}
	if got := binary.LittleEndian.Uint32(m.Bus.ARM7WRAM[0:]); got != 0xE3A01403 {
		t.Errorf("ARM7 binary not staged in ARM7 WRAM: %08x", got)
	}
	if m.CPU9.R[15] != 8 && !m.CPU9.Halted {
		t.Error("CPU9 should sit halted after staging")
	}
	h := m.Header()
	if h == nil || h.Title != "DS1 PROOF" {
		t.Errorf("header: %+v", h)
	}
	if err := New().LoadROM([]byte{1, 2, 3}); err == nil {
		t.Error("tiny image accepted")
	}
}

func TestFramebufferAndInputs(t *testing.T) {
	m := New()
	top, bottom := m.Framebuffer()
	if len(top) != ScreenWidth*ScreenHeight || len(bottom) != ScreenWidth*ScreenHeight {
		t.Fatalf("framebuffer sizes: %d %d", len(top), len(bottom))
	}
	for i, v := range top {
		if v != 0 {
			t.Fatalf("top buffer not blank at %d: %08x", i, v)
		}
	}
	m.SetButtons(0x201)
	if m.Buttons() != 0x201 {
		t.Errorf("buttons: %08x", m.Buttons())
	}
	m.SetTouch(100, 50, true)
	if x, y, down := m.Touch(); x != 100 || y != 50 || !down {
		t.Errorf("touch: %d %d %v", x, y, down)
	}
	// Inputs ride along in the save state.
	st := m.Serialize()
	f := New()
	if !f.Deserialize(st) {
		t.Fatal("Deserialize refused")
	}
	if f.Buttons() != 0x201 {
		t.Errorf("restored buttons: %08x", f.Buttons())
	}
	if x, y, down := f.Touch(); x != 100 || y != 50 || !down {
		t.Errorf("restored touch: %d %d %v", x, y, down)
	}
	if f.Deserialize([]byte{9}) {
		t.Error("garbage state accepted")
	}
}
