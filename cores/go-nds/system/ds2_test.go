package system

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nds/periph"
)

// buildROM assembles a synthetic cartridge with the given
// hand-assembled programs: ARM9 loads at 0x02000000, ARM7 at
// 0x03800000, entries at the load addresses.
func buildROM(arm9prog, arm7prog []uint32) []byte {
	rom := make([]byte, 0x4000)
	copy(rom[0x000:], "DS2 PROOF")
	copy(rom[0x00C:], "ADSP")
	copy(rom[0x010:], "01")
	le := binary.LittleEndian
	le.PutUint32(rom[0x020:], 0x1000)
	le.PutUint32(rom[0x024:], 0x02000000)
	le.PutUint32(rom[0x028:], 0x02000000)
	le.PutUint32(rom[0x02C:], uint32(len(arm9prog)*4))
	le.PutUint32(rom[0x030:], 0x2000)
	le.PutUint32(rom[0x034:], 0x03800000)
	le.PutUint32(rom[0x038:], 0x03800000)
	le.PutUint32(rom[0x03C:], uint32(len(arm7prog)*4))
	for i, w := range arm9prog {
		le.PutUint32(rom[0x1000+i*4:], w)
	}
	for i, w := range arm7prog {
		le.PutUint32(rom[0x2000+i*4:], w)
	}
	return rom
}

func spin() []uint32 { return []uint32{0xEAFFFFFE} } // B .

func ramWord(m *Machine, addr uint32) uint32 {
	return binary.LittleEndian.Uint32(m.Bus.MainRAM[addr-0x02000000:])
}

// TestDirectBootGate: the ARM9 program verifies the boot flag
// halfword at 0x027FFFFA and its own user stack pointer
// against the value direct boot installed (load + size), then
// writes a success magic. The ARM7 program verifies its stack
// pointer and link register, then answers over the IPC FIFO.
func TestDirectBootGate(t *testing.T) {
	arm9 := []uint32{
		0xE59F0038, // LDR R0, [PC, #0x38] -> 0x027FFFFA
		0xE1D010B0, // LDRH R1, [R0]
		0xE3510001, // CMP R1, #1
		0x1A000007, // BNE fail
		0xE1A0300D, // MOV R3, SP
		0xE59F4028, // LDR R4, [PC, #0x28] -> expected SP
		0xE1530004, // CMP R3, R4
		0x1A000003, // BNE fail
		0xE59F5020, // LDR R5, [PC, #0x20] -> result addr
		0xE59F6020, // LDR R6, [PC, #0x20] -> success magic
		0xE5856000, // STR R6, [R5]
		0xEAFFFFFE, // B .
		0xE59F5010, // fail: LDR R5, [PC, #0x10] -> result addr
		0xE59F6014, // LDR R6, [PC, #0x14] -> bad magic
		0xE5856000, // STR R6, [R5]
		0xEAFFFFFE, // B .
		0x027FFFFA, // literal 0
		0x02000054, // literal 1: load 0x02000000 + 21 words * 4
		0x02010000, // literal 2: result address
		0x600D600D, // literal 3: success magic
		0x0BAD0BAD, // literal 4: bad magic
	}
	if len(arm9) != 21 {
		t.Fatalf("arm9 program is %d words, layout math wants 21", len(arm9))
	}
	arm7 := []uint32{
		0xE1A0300D, // MOV R3, SP
		0xE59F4040, // LDR R4, [PC, #0x40] -> expected SP 0x0380FE00
		0xE1530004, // CMP R3, R4
		0x1A00000A, // BNE fail
		0xE1A0500E, // MOV R5, LR
		0xE59F6034, // LDR R6, [PC, #0x34] -> entry 0x03800000
		0xE1550006, // CMP R5, R6
		0x1A000006, // BNE fail
		0xE59F002C, // LDR R0, [PC, #0x2C] -> FIFOCNT addr
		0xE59F102C, // LDR R1, [PC, #0x2C] -> 0x8000
		0xE5801000, // STR R1, [R0]
		0xE59F0028, // LDR R0, [PC, #0x28] -> FIFOSEND addr
		0xE59F1028, // LDR R1, [PC, #0x28] -> magic
		0xE5801000, // STR R1, [R0]
		0xEAFFFFFE, // B .
		0xE59F0020, // fail: LDR R0, [PC, #0x20] -> result addr
		0xE59F1020, // LDR R1, [PC, #0x20] -> bad magic
		0xE5801000, // STR R1, [R0]
		0xEAFFFFFE, // B .
		0x0380FE00, // literal: expected SP
		0x03800000, // literal: entry
		0x04000184, // literal: FIFOCNT
		0x00008000, // literal: FIFO enable
		0x04000188, // literal: FIFOSEND
		0xCAFEBABE, // literal: answer magic
		0x02010004, // literal: result address
		0x0D15EA5E, // literal: bad magic
	}
	if len(arm7) != 27 {
		t.Fatalf("arm7 program is %d words, layout math wants 27", len(arm7))
	}
	m := New()
	if err := m.LoadROM(buildROM(arm9, arm7)); err != nil {
		t.Fatal(err)
	}
	m.Start()
	m.RunFrame()
	m.RunFrame()
	if got := ramWord(m, 0x02010000); got != 0x600D600D {
		t.Fatalf("ARM9 boot check: %08x want 600D600D", got)
	}
	if got := ramWord(m, 0x02010004); got == 0x0D15EA5E {
		t.Fatal("ARM7 boot check wrote the failure magic")
	}
	// The ARM7's answer is waiting in the IPC FIFO.
	v9 := m.Bus.ARM9()
	v9.Write(0x04000184, 4, 0x8000)
	if got := v9.Read(0x04100000, 4); got != 0xCAFEBABE {
		t.Fatalf("ARM7 IPC answer: %08x want CAFEBABE", got)
	}
}

// TestBootBlock checks the documented boot block fields
// directly after LoadROM.
func TestBootBlock(t *testing.T) {
	m := New()
	if err := m.LoadROM(buildROM(spin(), spin())); err != nil {
		t.Fatal(err)
	}
	ram := m.Bus.MainRAM
	off := func(a uint32) int { return int((a - 0x02000000) & (4<<20 - 1)) }
	if got := binary.LittleEndian.Uint32(ram[off(0x027FF800):]); got != periph.ChipID {
		t.Fatalf("chip ID at 027FF800: %08x", got)
	}
	if got := binary.LittleEndian.Uint16(ram[off(0x027FFFFA):]); got != 1 {
		t.Fatalf("boot flag: %04x", got)
	}
	if !bytes.Equal(ram[off(0x027FFE00):off(0x027FFE00)+9], []byte("DS2 PROOF")) {
		t.Fatal("header copy at 027FFE00 missing")
	}
	if got := uint16(m.Bus.IO9[0x304]) | uint16(m.Bus.IO9[0x305])<<8; got != 0x020F {
		t.Fatalf("POWCNT1: %04x", got)
	}
	if m.CPU9.R[13] != 0x02000000+4 { // spin() is one word
		t.Fatalf("ARM9 user SP: %08x", m.CPU9.R[13])
	}
	if m.CPU7.R[13] != 0x0380FE00 || m.CPU7.RSVC[0] != 0x0380FDC0 || m.CPU7.RIRQ[0] != 0x0380FD80 {
		t.Fatalf("ARM7 stacks: sp %08x svc %08x irq %08x",
			m.CPU7.R[13], m.CPU7.RSVC[0], m.CPU7.RIRQ[0])
	}
	if m.CPU9.R[12] != 0x02000000 || m.CPU9.R[14] != 0x02000000 {
		t.Fatal("ARM9 entry registers r12/r14 wrong")
	}
}

// TestVBlankHandlerCount: an ARM9 IRQ handler installed in
// ITCM (the vector branch range cannot reach main RAM, as on
// hardware), reached through a branch planted at the IRQ
// vector, counts VBlanks in RAM and acknowledges IF.
func TestVBlankHandlerCount(t *testing.T) {
	main := []uint32{
		0xE59F002C, // LDR R0, [PC, #0x2C] -> handler addr (unused beyond setup symmetry)
		0xE3A01018, // MOV R1, #0x18 (ITCM IRQ vector)
		0xE59F2028, // LDR R2, [PC, #0x28] -> branch word
		0xE5812000, // STR R2, [R1]
		0xE59F0024, // LDR R0, [PC, #0x24] -> IME addr
		0xE3A01001, // MOV R1, #1
		0xE5801000, // STR R1, [R0]  IME = 1
		0xE59F001C, // LDR R0, [PC, #0x1C] -> IE addr
		0xE5801000, // STR R1, [R0]  IE = VBlank
		0xE59F0018, // LDR R0, [PC, #0x18] -> DISPSTAT addr
		0xE3A01008, // MOV R1, #8
		0xE5801000, // STR R1, [R0]  DISPSTAT VBlank IRQ enable
		0xEAFFFFFE, // B .
		0x00000040, // literal: handler address in ITCM
		0xEA000008, // literal: B from 0x18 to 0x40
		0x04000208, // literal: IME
		0x04000210, // literal: IE
		0x04000004, // literal: DISPSTAT
	}
	handler := []uint32{
		0xE59F0018, // LDR R0, [PC, #0x18] -> count addr
		0xE5901000, // LDR R1, [R0]
		0xE2811001, // ADD R1, R1, #1
		0xE5801000, // STR R1, [R0]
		0xE59F200C, // LDR R2, [PC, #0x0C] -> IF addr
		0xE3A03001, // MOV R3, #1
		0xE5823000, // STR R3, [R2]  ack VBlank
		0xE25EF004, // SUBS PC, LR, #4
		0x02000200, // literal: count address
		0x04000214, // literal: IF
	}
	m := New()
	if err := m.LoadROM(buildROM(main, spin())); err != nil {
		t.Fatal(err)
	}
	m.Bus.LoadBytes(true, 0x00000040, words(handler))
	m.Start9()
	for i := 0; i < 5; i++ {
		m.RunFrame()
	}
	if got := ramWord(m, 0x02000200); got != 5 {
		t.Fatalf("VBlank handler count: %d want 5", got)
	}
}

func words(ws []uint32) []byte {
	out := make([]byte, len(ws)*4)
	for i, w := range ws {
		binary.LittleEndian.PutUint32(out[i*4:], w)
	}
	return out
}

// TestARM7TimerIRQIndependence: the ARM7 timer 0 raises its
// IRQ while the ARM9 stays fully masked, proving the two
// interrupt controllers are independent.
func TestARM7TimerIRQIndependence(t *testing.T) {
	m := New()
	if err := m.LoadROM(buildROM(spin(), spin())); err != nil {
		t.Fatal(err)
	}
	v7 := m.Bus.ARM7()
	// Reload 5865-tick period at F/64: about two overflows in
	// three frames of ARM7 cycles.
	v7.Write(0x04000100, 2, 0x10000-5865)
	v7.Write(0x04000102, 2, 0xC1) // start, IRQ on, F/64
	v7.Write(0x04000210, 4, 1<<periph.IRQTimer0)
	v7.Write(0x04000208, 4, 1) // IME7 = 1; ARM9 IME/IE stay 0
	m.Start()
	for i := 0; i < 3; i++ {
		m.RunFrame()
	}
	if m.Periph.IFlags(false)&(1<<periph.IRQTimer0) == 0 {
		t.Fatal("ARM7 timer IRQ never raised")
	}
	if !m.Periph.PendingIRQ(false) {
		t.Fatal("ARM7 IRQ line not pending")
	}
	if m.Periph.IFlags(true) != 0 || m.Periph.PendingIRQ(true) {
		t.Fatalf("ARM9 side disturbed: IF %08x", m.Periph.IFlags(true))
	}
}

// TestDMAGXFIFO: a GX FIFO timing channel fires at frame
// start and the sink counts the words; a plain CPU write to
// the FIFO port counts too.
func TestDMAGXFIFO(t *testing.T) {
	m := New()
	if err := m.LoadROM(buildROM(spin(), spin())); err != nil {
		t.Fatal(err)
	}
	v9 := m.Bus.ARM9()
	m.Bus.LoadBytes(true, 0x02001000, make([]byte, 64))
	v9.Write(0x040000B0, 4, 0x02001000)
	v9.Write(0x040000B4, 4, 0x04000400)
	v9.Write(0x040000B8, 4, 0xBC400008) // enable, GX timing, fixed dest, 32-bit, 8 words
	m.RunFrame()
	if got := m.GXFIFOCount(); got != 8 {
		t.Fatalf("GX sink after frame: %d want 8", got)
	}
	v9.Write(0x04000400, 4, 0x12345678)
	if got := m.GXFIFOCount(); got != 9 {
		t.Fatalf("GX sink after CPU write: %d want 9", got)
	}
}

// sceneSetup builds the two-engine scene used by the swap and
// determinism gates through the bus views: engine A shows a
// red text tile, engine B a white one.
func sceneSetup(m *Machine) {
	v9 := m.Bus.ARM9()
	w16 := func(addr uint32, val uint32) { v9.Write(addr, 2, val) }
	w32 := func(addr uint32, val uint32) { v9.Write(addr, 4, val) }
	v9.Write(0x04000240, 1, 0x81) // VRAMCNT A: BG A
	v9.Write(0x04000248, 1, 0x81) // VRAMCNT H: BG B
	w32(0x04000000, 0x00010100)
	w16(0x04000008, 0x0204)
	w32(0x04001000, 0x00010100)
	w16(0x04001008, 0x0204)
	pal := func(off int, c uint16) {
		m.Bus.Palette[off] = byte(c)
		m.Bus.Palette[off+1] = byte(c >> 8)
	}
	pal(2, 0x001F)     // A palette 1: red
	pal(0x402, 0x7FFF) // B palette 1: white
	for i := 0; i < 32; i++ {
		m.Bus.VRAM[0x4000+32+i] = 0x11
		m.Bus.VRAM[0x98000+0x4000+32+i] = 0x11
	}
	m.Bus.VRAM[0x1000] = 1
	m.Bus.VRAM[0x98000+0x1000] = 1
	// A capture pass into bank D, blended with itself.
	w32(0x04000064, (1<<31)|(3<<20)|(3<<16)|(1<<24)|16)
}

// TestDisplaySwap: POWCNT1 swap exchanges which buffer
// Framebuffer returns first.
func TestDisplaySwap(t *testing.T) {
	m := New()
	if err := m.LoadROM(buildROM(spin(), spin())); err != nil {
		t.Fatal(err)
	}
	sceneSetup(m)
	m.RunFrame()
	top, _ := m.Framebuffer()
	if top[0] != 0xFFFF0000 {
		t.Fatalf("top before swap: %08x want engine A red", top[0])
	}
	m.Bus.IO9[0x305] |= 0x80
	top, bottom := m.Framebuffer()
	if top[0] != 0xFFFFFFFF || bottom[0] != 0xFFFF0000 {
		t.Fatalf("after swap: top %08x bottom %08x", top[0], bottom[0])
	}
}

// TestDeterminismGate: direct boot, render a two-engine scene
// with a capture pass, save mid-run, restore into a fresh
// machine, and require identical framebuffers and state
// bytes after the same continuation.
func TestDeterminismGate(t *testing.T) {
	rom := buildROM(spin(), spin())
	m := New()
	if err := m.LoadROM(rom); err != nil {
		t.Fatal(err)
	}
	sceneSetup(m)
	m.Start()
	m.RunFrame()
	m.RunFrame()
	m.RunFrame()
	saved := m.Serialize()
	if len(saved) != StateSize {
		t.Fatalf("state size: %d want %d", len(saved), StateSize)
	}
	m.RunFrame()
	m.RunFrame()
	topA, botA := m.Framebuffer()
	wantTop := append([]uint32(nil), topA...)
	wantBot := append([]uint32(nil), botA...)
	wantState := m.Serialize()
	if topA[0] != 0xFFFF0000 {
		t.Fatalf("scene did not render: top %08x", topA[0])
	}
	// The capture pass must have written engine A pixels into
	// bank D during the run.
	if got := binary.LittleEndian.Uint16(m.Bus.VRAMBank(3)); got == 0 {
		t.Fatal("capture bank D is empty")
	}

	f := New()
	if err := f.LoadROM(rom); err != nil {
		t.Fatal(err)
	}
	sceneSetup(f)
	f.Start()
	f.RunFrame()
	f.RunFrame()
	f.RunFrame()
	if !f.Deserialize(saved) {
		t.Fatal("Deserialize refused the DS-2 state")
	}
	f.RunFrame()
	f.RunFrame()
	topB, botB := f.Framebuffer()
	if !uint32sEqual(topB, wantTop) || !uint32sEqual(botB, wantBot) {
		t.Fatal("restored run: framebuffers differ")
	}
	if got := f.Serialize(); !bytes.Equal(got, wantState) {
		t.Fatal("restored run: state bytes differ")
	}
}

func uint32sEqual(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
