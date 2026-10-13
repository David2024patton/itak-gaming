package system

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The GBA-2 integration gate. The synthetic program
// below boots through the core's own BIOS stub, installs
// an IRQ handler in ROM, enables the VBlank interrupt
// end to end (DISPSTAT enable, IE, IME, CPSR I bit), and
// idles in a branch loop. The handler increments a
// counter in IWRAM on every interrupt it is called for.
// If any link in the chain (PPU event, interrupt
// controller, CPU IRQ entry, BIOS dispatcher, handler
// return) is broken, the counter does not move.

// armSTRH builds STRH rd, [rn] with a zero immediate
// offset.
func armSTRH(rd, rn uint32) uint32 {
	return 0xE1C000B0 | rn<<16 | rd<<12
}

// irqSceneROM builds a 4 KB image whose main code sits
// at 0xC0 (reached by the standard entry branch) and
// whose IRQ handler sits at ROM offset 0x200.
func irqSceneROM() []byte {
	rom := make([]byte, 0x1000)
	binary.LittleEndian.PutUint32(rom[0x00:], 0xEA00002E)
	copy(rom[0xA0:], "IRQSCENE")
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
		0xEAFFFFFE,                // B . (idle loop)
	)
	for i, w := range code {
		binary.LittleEndian.PutUint32(rom[0xC0+4*i:], w)
	}

	// The handler at 0x200: bump the word at
	// IWRAM+0x10, preserving the registers it uses, and
	// return to the dispatcher with BX LR.
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

// TestVBlankIRQCount is the interrupt gate: exactly one
// VBlank interrupt must reach the handler per frame.
func TestVBlankIRQCount(t *testing.T) {
	m := New()
	if err := m.LoadCart(irqSceneROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	for i := 0; i < 3; i++ {
		m.RunFrame()
	}
	if got := m.Bus().Read(0x03000010, 4); got != 3 {
		t.Fatalf("VBlank handler ran %d times in 3 frames, want exactly 3", got)
	}
	// The BIOS dispatcher must also have recorded the
	// flags in the interrupt check word.
	if got := m.Bus().Read(0x03007FF8, 4); got&1 == 0 {
		t.Fatalf("BIOS check word = %#08x, want the VBlank bit set", got)
	}
}

// setupScene paints the deterministic scene the
// determinism gate renders: a text tile background, one
// sprite on top of it, and an HBlank DMA that rewrites
// the backdrop palette entry every line.
func setupScene(t *testing.T, m *Machine) {
	t.Helper()
	b := m.Bus()
	// BG palette: entry 1 = red (BGR555 0x001F).
	b.Write(0x05000002, 2, 0x001F)
	// OBJ palette: entry 1 = green (BGR555 0x03E0).
	b.Write(0x05000202, 2, 0x03E0)
	// VRAM tile 1 (char base 0): all nibbles = color 1.
	for off := uint32(0); off < 32; off += 4 {
		b.Write(0x06000020+off, 4, 0x11111111)
	}
	// OBJ tile 0 at 0x06010000: all nibbles = color 1.
	for off := uint32(0); off < 32; off += 4 {
		b.Write(0x06010000+off, 4, 0x11111111)
	}
	// Tilemap at screen base 0x800: entries (0,0) and
	// (1,0) show tile 1.
	b.Write(0x06000800, 2, 1)
	b.Write(0x06000802, 2, 1)
	// OAM entry 0: 8x8 sprite at (0,0), tile 0, prio 0.
	b.Write(0x07000000, 2, 0) // attr0: y=0
	b.Write(0x07000002, 2, 0) // attr1: x=0, size 0
	b.Write(0x07000004, 2, 0) // attr2: tile 0
	// BG0CNT: char base 0, screen base block 1.
	b.Write(0x04000008, 2, 0x0100)
	// DISPCNT: mode 0, BG0 on, OBJ on.
	b.Write(0x04000000, 2, 0x1100)
	// HBlank DMA3: EWRAM pattern to the backdrop
	// palette entry, one halfword per line, repeating
	// with destination reload.
	b.Write(0x02000000, 4, 0x00007C00)
	b.Write(0x040000D4, 4, 0x02000000) // DMA3SAD
	b.Write(0x040000D8, 4, 0x05000000) // DMA3DAD
	b.Write(0x040000DC, 2, 1)          // DMA3CNT_L: 1 unit
	b.Write(0x040000DE, 2, 0xA360)     // DMA3CNT_H: enable, HBlank, repeat, fixed src, dst reload
}

// TestSceneDeterminismGate renders the scene, takes a
// save state mid-frame, and requires the continuation
// after a restore into a fresh machine to be identical
// in state bytes and framebuffer to the uninterrupted
// run.
func TestSceneDeterminismGate(t *testing.T) {
	rom := irqSceneROM()
	m := New()
	if err := m.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	setupScene(t, m)
	m.runLines(100) // mid-frame
	save := m.Serialize()
	m.runLines(linesPerFrame - 100)
	m.RunFrame()
	wantState := m.Serialize()
	wantFrame := append([]uint32{}, m.Framebuffer()...)

	// Scene spot checks on the uninterrupted run.
	if got := m.Bus().Read(0x03000010, 4); got != 2 {
		t.Errorf("VBlank handler count = %d after 2 frames, want 2", got)
	}
	fb := m.Framebuffer()
	if got := fb[0]; got != 0xFF00FF00 {
		t.Errorf("pixel (0,0) = %#08x, want 0xFF00FF00 (sprite green over BG)", got)
	}
	if got := fb[8]; got != 0xFFFF0000 {
		t.Errorf("pixel (8,0) = %#08x, want 0xFFFF0000 (BG red tile)", got)
	}
	if got := fb[100*Width+100]; got != 0xFF0000FF {
		t.Errorf("pixel (100,100) = %#08x, want 0xFF0000FF (DMA blue backdrop)", got)
	}
	if got := m.Bus().Read(0x05000000, 2); got != 0x7C00 {
		t.Errorf("backdrop palette entry = %#04x, want 0x7C00 (HBlank DMA wrote it)", got)
	}

	m2 := New()
	if err := m2.LoadCart(rom); err != nil {
		t.Fatalf("LoadCart (second machine): %v", err)
	}
	if err := m2.Deserialize(save); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	m2.runLines(linesPerFrame - 100)
	m2.RunFrame()
	if got := m2.Serialize(); !bytes.Equal(got, wantState) {
		t.Fatal("state after a mid-frame restore and continuation differs from the uninterrupted run")
	}
	got2 := m2.Framebuffer()
	for i := range wantFrame {
		if got2[i] != wantFrame[i] {
			t.Fatalf("framebuffer pixel %d after restore = %#08x, want %#08x", i, got2[i], wantFrame[i])
		}
	}
}

// TestKeypadRegister checks the KEYINPUT wiring: bits
// read active-low in GBATEK order.
func TestKeypadRegister(t *testing.T) {
	m := New()
	if err := m.LoadCart(irqSceneROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	m.SetButtons(ButtonA | ButtonStart)
	if got := m.Bus().Read(0x04000130, 2); got != 0x03F6 {
		t.Fatalf("KEYINPUT = %#04x, want 0x03F6 (A and Start pressed, active low)", got)
	}
	m.SetButtons(0)
	if got := m.Bus().Read(0x04000130, 2); got != 0x03FF {
		t.Fatalf("KEYINPUT with no buttons = %#04x, want 0x03FF", got)
	}
}

// TestStateVersion2 checks the v2 state shape: version
// byte 2, exact size, and a v1-shaped input rejected.
func TestStateVersion2(t *testing.T) {
	m := New()
	if err := m.LoadCart(irqSceneROM()); err != nil {
		t.Fatalf("LoadCart: %v", err)
	}
	m.RunFrame()
	s := m.Serialize()
	if len(s) != m.StateSize() {
		t.Fatalf("state size = %d, StateSize = %d", len(s), m.StateSize())
	}
	if s[0] != stateVersion || stateVersion != 2 {
		t.Fatalf("version byte = %d, want 2", s[0])
	}
	v1 := append([]byte{}, s...)
	v1[0] = 1
	if err := m.Deserialize(v1); err == nil {
		t.Fatal("Deserialize accepted a version 1 state under the v2 format")
	}
}
