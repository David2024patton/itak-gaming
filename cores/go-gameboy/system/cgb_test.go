package system

import (
	"path/filepath"
	"testing"
)

// homebrew ROM paths (see homebrew/SOURCES.md): a DMG game and a
// GBC game.
func homebrewPath(name string) string {
	return filepath.Join("..", "..", "..", "homebrew", name)
}

func TestCGBModeDetection(t *testing.T) {
	dmg, err := New(loadROM(t, homebrewPath("tobutobugirl.gb")))
	if err != nil {
		t.Fatal(err)
	}
	if dmg.CGB() {
		t.Error("DMG homebrew engaged CGB mode")
	}
	gbc, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	if !gbc.CGB() {
		t.Error("GBC homebrew did not engage CGB mode")
	}
	if gbc.compat {
		t.Error("CGB-flagged cart booted in compatibility mode")
	}
	forced, err := NewCGB(loadROM(t, homebrewPath("tobutobugirl.gb")))
	if err != nil {
		t.Fatal(err)
	}
	if !forced.CGB() || !forced.compat {
		t.Error("forced CGB machine for a DMG cart is not in compatibility mode")
	}
}

func TestCGBBootRegisters(t *testing.T) {
	gbc, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	r := gbc.CPU.Regs()
	if r.A != 0x11 || r.F != 0x80 || r.B != 0x00 || r.C != 0x00 ||
		r.D != 0xFF || r.E != 0x56 || r.H != 0x00 || r.L != 0x0D ||
		r.SP != 0xFFFE || r.PC != 0x0100 {
		t.Errorf("CGB boot registers = %+v", r)
	}
	compat, err := NewCGB(loadROM(t, homebrewPath("tobutobugirl.gb")))
	if err != nil {
		t.Fatal(err)
	}
	r = compat.CPU.Regs()
	if r.A != 0x11 || r.D != 0x00 || r.E != 0x08 || r.H != 0x00 || r.L != 0x7C {
		t.Errorf("compat boot registers = %+v", r)
	}
}

func TestPaletteAccess(t *testing.T) {
	s, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	s.Write(0xFF40, 0x00) // LCD off: palette ports never blocked
	s.Write(0xFF68, 0x80) // BG palette index 0, auto-increment
	s.Write(0xFF69, 0x34)
	s.Write(0xFF69, 0x12)
	if got := s.Read(0xFF68); got != 0x40|0x82 {
		t.Errorf("BCPS after two auto-incremented writes = 0x%02X, want 0xC2", got)
	}
	s.Write(0xFF68, 0x80)
	if lo := s.Read(0xFF69); lo != 0x34 {
		t.Errorf("BGPD[0] = 0x%02X, want 0x34", lo)
	}
	if hi := s.Read(0xFF69); hi != 0x12 {
		t.Errorf("BGPD[1] (after read auto-increment) = 0x%02X, want 0x12", hi)
	}
}

func TestVRAMBanking(t *testing.T) {
	s, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	s.Write(0xFF40, 0x00) // LCD off so VRAM is always reachable
	s.Write(0xFF4F, 0)
	s.Write(0x8000, 0x11)
	s.Write(0xFF4F, 1)
	if got := s.Read(0x8000); got != 0x00 {
		t.Errorf("bank 1 VRAM starts 0x%02X, want 0x00", got)
	}
	s.Write(0x8000, 0x22)
	s.Write(0xFF4F, 0)
	if got := s.Read(0x8000); got != 0x11 {
		t.Errorf("bank 0 VRAM = 0x%02X, want 0x11", got)
	}
	if got := s.Read(0xFF4F); got != 0xFE {
		t.Errorf("VBK readback = 0x%02X, want 0xFE", got)
	}
}

func TestWRAMBanking(t *testing.T) {
	s, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	s.Write(0xFF70, 2)
	s.Write(0xD000, 0x33)
	s.Write(0xFF70, 3)
	if got := s.Read(0xD000); got != 0x00 {
		t.Errorf("bank 3 WRAM starts 0x%02X, want 0x00", got)
	}
	s.Write(0xD000, 0x44)
	s.Write(0xFF70, 2)
	if got := s.Read(0xD000); got != 0x33 {
		t.Errorf("bank 2 WRAM = 0x%02X, want 0x33", got)
	}
	// SVBK 0 selects bank 1; the C000 fixed window never banks.
	s.Write(0xFF70, 0)
	s.Write(0xD000, 0x55)
	s.Write(0xFF70, 1)
	if got := s.Read(0xD000); got != 0x55 {
		t.Errorf("bank 1 via SVBK 0 = 0x%02X, want 0x55", got)
	}
	if got := s.Read(0xFF70); got != 0xF9 {
		t.Errorf("SVBK readback = 0x%02X, want 0xF9", got)
	}
	s.Write(0xC000, 0x66)
	if got := s.Read(0xE000); got != 0x66 {
		t.Errorf("echo of C000 = 0x%02X, want 0x66", got)
	}
}

// spinROM builds a minimal ROM image whose entry code arms KEY1,
// executes STOP, then spins: enough to drive the speed switch
// through the real bus.
func spinROM() []byte {
	rom := make([]byte, 0x8000)
	code := []byte{
		0x3E, 0x01, // LD A,1
		0xE0, 0x4D, // LDH (FF4D),A : arm the speed switch
		0x10, 0x00, // STOP
		0x18, 0xFE, // JR -2 (spin)
	}
	copy(rom[0x100:], code)
	return rom
}

func TestSpeedSwitch(t *testing.T) {
	m, err := New(spinROM())
	if err != nil {
		t.Fatal(err)
	}
	// The spin ROM is DMG-flagged on DMG hardware: KEY1 is absent
	// and STOP is a plain stop.
	if got := m.Read(0xFF4D); got != 0xFF {
		t.Errorf("DMG KEY1 read = 0x%02X, want 0xFF", got)
	}
	c, err := NewCGB(spinROM())
	if err != nil {
		t.Fatal(err)
	}
	// Forced CGB + DMG cart = compatibility mode: KEY1 locks too.
	if got := c.Read(0xFF4D); got != 0xFF {
		t.Errorf("compat KEY1 read = 0x%02X, want 0xFF", got)
	}
	// Full CGB mode: use a CGB-flagged header on the spin ROM.
	rom := spinROM()
	rom[0x143] = 0x80
	g, err := New(rom)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Read(0xFF4D); got != 0x7E {
		t.Errorf("KEY1 idle read = 0x%02X, want 0x7E", got)
	}
	for i := 0; i < 10 && g.Speed() == 1; i++ {
		g.Step()
	}
	if g.Speed() != 2 {
		t.Fatal("STOP with KEY1 armed did not switch to double speed")
	}
	if got := g.Read(0xFF4D); got != 0xFE {
		t.Errorf("KEY1 in double speed = 0x%02X, want 0xFE", got)
	}
	// Device dots per M-cycle: a plain spin loop on a fresh
	// machine (never switches) against the switched machine.
	plain := spinROM()
	plain[0x100] = 0x18 // JR -2 from the entry point, no KEY1 code
	plain[0x101] = 0xFE
	plain[0x143] = 0x80
	n, err := New(plain)
	if err != nil {
		t.Fatal(err)
	}
	before := n.Cycles()
	n.RunMCycles(102)
	normalDots := n.Cycles() - before
	before = g.Cycles()
	g.RunMCycles(102)
	doubleDots := g.Cycles() - before
	if normalDots != 2*doubleDots {
		t.Errorf("dots per 102 M-cycles: normal %d, double %d (want exactly half)", normalDots, doubleDots)
	}
}

func TestHDMAGeneralPurpose(t *testing.T) {
	s, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	s.Write(0xFF40, 0x00) // LCD off
	for i := 0; i < 32; i++ {
		s.Write(0xC000+uint16(i), uint8(0xA0+i))
	}
	s.Write(0xFF51, 0xC0) // source 0xC000
	s.Write(0xFF52, 0x00)
	s.Write(0xFF53, 0x00) // destination offset 0
	s.Write(0xFF54, 0x00)
	s.Write(0xFF55, 0x01) // general-purpose, 2 blocks
	if got := s.Read(0xFF55); got != 0xFF {
		t.Errorf("HDMA5 after GP completion = 0x%02X, want 0xFF", got)
	}
	s.Write(0xFF4F, 0)
	for i := 0; i < 32; i++ {
		if got := s.Read(0x8000 + uint16(i)); got != uint8(0xA0+i) {
			t.Fatalf("VRAM[%d] = 0x%02X, want 0x%02X", i, got, 0xA0+i)
		}
	}
}

func TestHDMAHBlankAndCancel(t *testing.T) {
	s, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		s.Write(0xC000+uint16(i), uint8(0x50+i))
	}
	setRegs := func() {
		s.Write(0xFF51, 0xC0)
		s.Write(0xFF52, 0x00)
		s.Write(0xFF53, 0x00)
		s.Write(0xFF54, 0x00)
	}
	setRegs()
	s.Write(0xFF55, 0x83) // HBlank mode, 4 blocks
	if got := s.Read(0xFF55); got != 0x03 {
		t.Errorf("HDMA5 while HBlank transfer runs = 0x%02X, want 0x03", got)
	}
	s.Write(0xFF55, 0x00) // cancel
	if got := s.Read(0xFF55); got != 0x83 {
		t.Errorf("HDMA5 after cancel = 0x%02X, want 0x83", got)
	}
	setRegs()
	s.Write(0xFF55, 0x81) // HBlank mode, 2 blocks
	for i := 0; i < 3; i++ {
		s.RunFrame()
	}
	if got := s.Read(0xFF55); got != 0xFF {
		t.Errorf("HDMA5 after HBlank completion = 0x%02X, want 0xFF", got)
	}
	s.Write(0xFF40, 0x00) // LCD off to inspect VRAM
	s.Write(0xFF4F, 0)
	for i := 0; i < 32; i++ {
		if got := s.Read(0x8000 + uint16(i)); got != uint8(0x50+i) {
			t.Fatalf("VRAM[%d] = 0x%02X, want 0x%02X", i, got, 0x50+i)
		}
	}
}

func TestCompatLocks(t *testing.T) {
	s, err := NewCGB(loadROM(t, homebrewPath("tobutobugirl.gb")))
	if err != nil {
		t.Fatal(err)
	}
	for _, chk := range []struct {
		addr uint16
		want uint8
	}{
		{0xFF4D, 0xFF}, // KEY1
		{0xFF51, 0xFF}, // HDMA
		{0xFF55, 0xFF},
		{0xFF70, 0xFF}, // SVBK
		{0xFF74, 0xFF},
		{0xFF69, 0xFF}, // BGPD
		{0xFF6C, 0xFF}, // OPRI
	} {
		if got := s.Read(chk.addr); got != chk.want {
			t.Errorf("compat read 0x%04X = 0x%02X, want 0x%02X", chk.addr, got, chk.want)
		}
	}
	// The index registers still respond in compatibility mode.
	s.Write(0xFF68, 0x85)
	if got := s.Read(0xFF68); got != 0x40|0x85 {
		t.Errorf("compat BCPS = 0x%02X, want 0xC5", got)
	}
	// FF72 storage is not part of the lock set.
	s.Write(0xFF72, 0x5A)
	if got := s.Read(0xFF72); got != 0x5A {
		t.Errorf("compat FF72 = 0x%02X, want 0x5A", got)
	}
}

func TestCGBSaveStateDeterminism(t *testing.T) {
	s, err := New(loadROM(t, homebrewPath("ucity.gbc")))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CGB whole-machine save state: %d bytes", s.SerializeSize())
	for i := 0; i < 120; i++ {
		s.RunFrame()
	}
	_ = s.AudioBuffer() // drain the title-screen audio
	state := make([]byte, s.SerializeSize())
	if err := s.Serialize(state); err != nil {
		t.Fatal(err)
	}
	var wantAudio []int16
	for i := 0; i < 45; i++ {
		s.RunFrame()
		wantAudio = append(wantAudio, s.AudioBuffer()...)
	}
	wantFrame := hashFrame(s)
	if err := s.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	var gotAudio []int16
	for i := 0; i < 45; i++ {
		s.RunFrame()
		gotAudio = append(gotAudio, s.AudioBuffer()...)
	}
	if got := hashFrame(s); got != wantFrame {
		t.Errorf("frame after restore = 0x%X, want 0x%X", got, wantFrame)
	}
	if len(gotAudio) != len(wantAudio) || len(gotAudio) == 0 {
		t.Fatalf("audio after restore: %d samples, want %d", len(gotAudio), len(wantAudio))
	}
	for i := range wantAudio {
		if gotAudio[i] != wantAudio[i] {
			t.Fatalf("audio sample %d after restore differs", i)
		}
	}
}
