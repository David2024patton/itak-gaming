package system

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The IntrWait regression gate, added by GBA-4 after the
// Anguna homebrew game exposed the deadlock it covers:
// a game whose main loop sleeps in VBlankIntrWait (SWI
// 0x05) never woke, because the CPU took the VBlank IRQ
// exception while halted but kept its halted flag set,
// freezing the core inside the exception entry with the
// BIOS dispatcher (the code that records the event in
// the check word the wait polls) unreachable.
//
// The synthetic program below boots through the core's
// own BIOS stub, installs a VBlank handler that counts
// its calls in IWRAM, and then loops on VBlankIntrWait,
// counting completed waits beside it. If any link in the
// chain (IRQ wake from halt, dispatcher, check word,
// wait completion) breaks, one of the counters stalls.

// waitSceneROM builds the 4 KB test image: main code at
// 0xC0, IRQ handler at 0x200, in the irqSceneROM layout.
func waitSceneROM() []byte {
	rom := make([]byte, 0x1000)
	binary.LittleEndian.PutUint32(rom[0x00:], 0xEA00002E)
	copy(rom[0xA0:], "WAITSCENE")
	copy(rom[0xAC:], "ASYE")
	copy(rom[0xB0:], "01")
	rom[0xB2] = 0x96
	rom[0xBC] = 0x01
	sum := 0x19
	for _, c := range rom[0xA0 : 0xBC+1] {
		sum += int(c)
	}
	rom[0xBD] = uint8(-sum)

	var code []uint32
	code = append(code, armBuildConst(0, 0x03007F00)...) // R0 = user stack top
	code = append(code, 0xE1A0D000)                      // MOV SP, R0
	code = append(code, armBuildConst(1, 0x03007FFC)...) // R1 = handler pointer slot
	code = append(code, armBuildConst(2, 0x08000200)...) // R2 = handler address
	code = append(code, armSTR(2, 1, false))             // STR R2, [R1]
	code = append(code, armBuildConst(1, 0x04000004)...) // R1 = DISPSTAT
	code = append(code, armImm(13, 0, 2, 0, 8))          // MOV R2, #8 (VBlank IRQ enable)
	code = append(code, armSTRH(2, 1))                   // STRH R2, [R1]
	code = append(code, armBuildConst(1, 0x04000200)...) // R1 = IE
	code = append(code, armImm(13, 0, 2, 0, 1))          // MOV R2, #1 (VBlank)
	code = append(code, armSTRH(2, 1))                   // STRH R2, [R1]
	code = append(code, armBuildConst(1, 0x04000208)...) // R1 = IME
	code = append(code, armImm(13, 0, 2, 0, 1))          // MOV R2, #1
	code = append(code, armSTRH(2, 1))                   // STRH R2, [R1]
	code = append(code,
		0xE10F0000,                // MRS R0, CPSR
		armImm(14, 0, 0, 0, 0x80), // BIC R0, R0, #0x80 (IRQ enable)
		0xE121F000,                // MSR CPSR_c, R0
	)
	code = append(code, armBuildConst(4, 0x03000020)...) // R4 = progress counter
	swiAt := len(code)
	code = append(code, 0xEF050000) // SWI 0x05: VBlankIntrWait
	code = append(code,
		0xE5941000,            // LDR R1, [R4]
		armImm(4, 1, 1, 0, 1), // ADD R1, R1, #1
		0xE5841000,            // STR R1, [R4]
	)
	branchAddr := uint32(0xC0 + 4*len(code))
	target := uint32(0xC0 + 4*swiAt)
	off := (int32(target) - int32(branchAddr) - 8) / 4
	code = append(code, 0xEA000000|(uint32(off)&0xFFFFFF)) // B back to the SWI
	for i, w := range code {
		binary.LittleEndian.PutUint32(rom[0xC0+4*i:], w)
	}

	// The handler at 0x200: bump the word at IWRAM+0x10
	// and return to the dispatcher with BX LR.
	var handler []uint32
	handler = append(handler, 0xE92D0003) // PUSH {R0, R1}
	handler = append(handler, armBuildConst(0, 0x03000010)...)
	handler = append(handler,
		0xE5901000,            // LDR R1, [R0]
		armImm(4, 1, 1, 0, 1), // ADD R1, R1, #1
		0xE5801000,            // STR R1, [R0]
		0xE8BD0003,            // POP {R0, R1}
		0xE12FFF1E,            // BX LR
	)
	for i, w := range handler {
		binary.LittleEndian.PutUint32(rom[0x200+4*i:], w)
	}
	return rom
}

// TestVBlankIntrWaitProgress is the core of the gate:
// one handler call and one completed wait per frame,
// with the check word consumed after each wake.
func TestVBlankIntrWaitProgress(t *testing.T) {
	m := New()
	if err := m.LoadCart(waitSceneROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	for i := 0; i < 5; i++ {
		m.RunFrame()
	}
	if got := m.Bus().Read(0x03000010, 4); got != 5 {
		t.Fatalf("VBlank handler ran %d times in 5 frames, want exactly 5", got)
	}
	if got := m.Bus().Read(0x03000020, 4); got != 5 {
		t.Fatalf("VBlankIntrWait completed %d times in 5 frames, want exactly 5 (the wait deadlocked or double-woke)", got)
	}
	if got := m.Bus().Read(0x03007FF8, 4); got != 0 {
		t.Fatalf("BIOS check word = %#08x, want 0 (each wake consumes its bit)", got)
	}
}

// TestVBlankIntrWaitMidWaitState covers the save path: a
// state taken while the CPU sits halted inside a wait
// must restore into a wait that still completes, and the
// continuation must be byte identical to the
// uninterrupted run.
func TestVBlankIntrWaitMidWaitState(t *testing.T) {
	rom := waitSceneROM()
	m := New()
	if err := m.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	for i := 0; i < 3; i++ {
		m.RunFrame()
	}
	if !m.cpu.Halted || !m.biosH.Waiting() {
		t.Fatalf("after 3 frames the machine should be halted inside wait 4 (halted %v, waiting %v)",
			m.cpu.Halted, m.biosH.Waiting())
	}
	save := m.Serialize()
	m.RunFrame()
	m.RunFrame()
	wantState := m.Serialize()

	m2 := New()
	if err := m2.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart (second machine): %v", err)
	}
	if err := m2.Deserialize(save); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !m2.biosH.Waiting() {
		t.Fatal("the restored machine lost the outstanding BIOS wait")
	}
	m2.RunFrame()
	m2.RunFrame()
	if got := m2.Bus().Read(0x03000010, 4); got != 5 {
		t.Fatalf("restored machine: handler ran %d times in 5 frames, want 5", got)
	}
	if got := m2.Bus().Read(0x03000020, 4); got != 5 {
		t.Fatalf("restored machine: waits completed %d times in 5 frames, want 5", got)
	}
	if got := m2.Serialize(); !bytes.Equal(got, wantState) {
		t.Fatal("state after a mid-wait restore and continuation differs from the uninterrupted run")
	}
}
