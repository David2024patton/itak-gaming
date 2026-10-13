package system

import "testing"

// buildSmokeImage assembles the synthetic LoROM used by
// the smoke test: a program at bank 0 that switches to
// native mode, widens the registers, stores through a
// long address into Work RAM, block-copies its own first
// bytes into Work RAM, then jumps long into bank 1, which
// stores a marker and stops.
func buildSmokeImage() []byte {
	img := make([]byte, 0x80000)
	program := []byte{
		0x18,       // CLC
		0xFB,       // XCE (native mode)
		0xC2, 0x30, // REP #$30 (16-bit A and indexes)
		0xA9, 0x34, 0x12, // LDA #$1234
		0x8F, 0x00, 0x00, 0x7E, // STA $7E0000
		0xA9, 0x02, 0x00, // LDA #$0002 (MVN count: 3 bytes)
		0xA2, 0x00, 0x80, // LDX #$8000 (source offset)
		0xA0, 0x10, 0x00, // LDY #$0010 (destination offset)
		0x54, 0x7E, 0x00, // MVN dst $7E, src $00
		0x5C, 0x00, 0x80, 0x01, // JML $018000
	}
	copy(img, program)
	bank1 := []byte{
		0xA9, 0xEF, 0xBE, // LDA #$BEEF
		0x8F, 0x02, 0x00, 0x7E, // STA $7E0002
		0xDB, // STP
	}
	copy(img[0x8000:], bank1)
	h := img[0x7FC0 : 0x7FC0+0x40]
	copy(h, "SNES-1 SMOKE TEST    ")
	h[0x15] = 0x20 // LoROM
	h[0x16] = 0x02 // ROM + RAM + battery
	h[0x17] = 0x09 // 512 KB
	h[0x18] = 0x03 // 8 KB SRAM
	h[0x1C], h[0x1D] = 0x55, 0xAA
	h[0x1E], h[0x1F] = 0xAA, 0x55
	h[0x3C], h[0x3D] = 0x00, 0x80 // reset vector 0x8000
	return img
}

func TestSmokeProgram(t *testing.T) {
	sys, err := New(buildSmokeImage())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sys.Title() != "SNES-1 SMOKE TEST" {
		t.Errorf("title = %q", sys.Title())
	}
	regs := sys.CPU.Regs()
	if regs.PC != 0x8000 || !regs.E {
		t.Fatalf("after reset: PC = %#x, E = %v; want 0x8000, true", regs.PC, regs.E)
	}
	for i := 0; i < 100 && !sys.CPU.Stopped(); i++ {
		sys.CPU.Step()
	}
	if !sys.CPU.Stopped() {
		t.Fatalf("program never reached STP; PC = %#x", sys.CPU.Regs().PC)
	}
	wram := sys.Map.WRAM()
	if wram[0] != 0x34 || wram[1] != 0x12 {
		t.Errorf("WRAM[0..1] = %#x %#x, want 0x34 0x12 (long store)", wram[0], wram[1])
	}
	if wram[2] != 0xEF || wram[3] != 0xBE {
		t.Errorf("WRAM[2..3] = %#x %#x, want 0xEF 0xBE (bank 1 marker)", wram[2], wram[3])
	}
	want := []byte{0x18, 0xFB, 0xC2}
	for i, b := range want {
		if wram[0x10+i] != b {
			t.Errorf("WRAM[%#x] = %#x, want %#x (MVN copy)", 0x10+i, wram[0x10+i], b)
		}
	}
	regs = sys.CPU.Regs()
	if regs.E {
		t.Errorf("still in emulation mode after XCE")
	}
	if regs.PBR != 0x01 {
		t.Errorf("PBR = %#x, want 0x01 (JML landed in bank 1)", regs.PBR)
	}
}
