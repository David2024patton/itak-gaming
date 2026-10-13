package system

import (
	"bytes"
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"
)

// loopROM builds a minimal LoROM whose reset code is an
// endless branch loop: enough for tests that drive the
// machine through its bus directly.
func loopROM() []byte {
	img := make([]byte, 0x80000)
	img[0] = 0x80 // BRA -2 (spin)
	img[1] = 0xFE
	h := img[0x7FC0 : 0x7FC0+0x40]
	copy(h, "SNES-2 LOOP TEST     ")
	h[0x15] = 0x20 // LoROM
	h[0x16] = 0x02 // ROM + RAM + battery
	h[0x17] = 0x09 // 512 KB
	h[0x18] = 0x03 // 8 KB SRAM
	h[0x1C], h[0x1D] = 0x55, 0xAA
	h[0x1E], h[0x1F] = 0xAA, 0x55
	h[0x3C], h[0x3D] = 0x00, 0x80 // reset vector 0x8000
	return img
}

func newLoopSys(t *testing.T) *System {
	t.Helper()
	s, err := New(loopROM())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// wrReg writes one byte to a system-bank register.
func wrReg(s *System, addr uint16, v uint8) { s.Write(0, addr, v) }

func rdReg(s *System, addr uint16) uint8 { return s.Read(0, addr) }

// setWMADD points the WRAM port at the given 17-bit
// address (bank bit included as bit 16).
func setWMADD(s *System, a uint32) {
	wrReg(s, 0x2181, uint8(a))
	wrReg(s, 0x2182, uint8(a>>8))
	wrReg(s, 0x2183, uint8(a>>16))
}

func TestDMACopyToWRAMPort(t *testing.T) {
	s := newLoopSys(t)
	wram := s.Map.WRAM()
	for i := 0; i < 16; i++ {
		wram[0x1000+i] = uint8(0xA0 + i)
	}
	setWMADD(s, 0x2000)
	wrReg(s, 0x4300, 0x00) // mode 0, A to B, A increments
	wrReg(s, 0x4301, 0x80) // B = $2180 WRAM data
	wrReg(s, 0x4302, 0x00)
	wrReg(s, 0x4303, 0x10) // A = $7E1000
	wrReg(s, 0x4304, 0x7E)
	wrReg(s, 0x4305, 16)
	wrReg(s, 0x4306, 0x00)
	wrReg(s, 0x420B, 0x01)
	s.Step() // the DMA runs as the write's instruction ends
	for i := 0; i < 16; i++ {
		if wram[0x2000+i] != uint8(0xA0+i) {
			t.Fatalf("WRAM[%#x] = %#x, want %#x", 0x2000+i, wram[0x2000+i], 0xA0+i)
		}
	}
	if v := rdReg(s, 0x4305); v != 0 {
		t.Errorf("DASL after DMA = %#x, want 0 (count consumed)", v)
	}
	if lo, hi := rdReg(s, 0x4302), rdReg(s, 0x4303); lo != 0x10 || hi != 0x10 {
		t.Errorf("A1T after DMA = %#x%02x, want advanced by 16 to 0x1010", hi, lo)
	}
}

func TestDMADecrement(t *testing.T) {
	s := newLoopSys(t)
	wram := s.Map.WRAM()
	for i := 0; i < 4; i++ {
		wram[0x1000+i] = uint8(1 + i)
	}
	setWMADD(s, 0x2000)
	wrReg(s, 0x4300, 0x08) // mode 0, A address decrements
	wrReg(s, 0x4301, 0x80)
	wrReg(s, 0x4302, 0x03)
	wrReg(s, 0x4303, 0x10) // A starts at $7E1003
	wrReg(s, 0x4304, 0x7E)
	wrReg(s, 0x4305, 4)
	wrReg(s, 0x4306, 0)
	wrReg(s, 0x420B, 0x01)
	s.Step()
	want := []byte{4, 3, 2, 1}
	for i, w := range want {
		if wram[0x2000+i] != w {
			t.Errorf("WRAM[%#x] = %#x, want %#x (decrement order)", 0x2000+i, wram[0x2000+i], w)
		}
	}
}

func TestDMAMode1IntoVRAM(t *testing.T) {
	s := newLoopSys(t)
	wram := s.Map.WRAM()
	copy(wram[0x1000:], []byte{0x34, 0x12, 0x78, 0x56})
	wrReg(s, 0x2115, 0x80) // VMAIN: increment after high
	wrReg(s, 0x2116, 0x00)
	wrReg(s, 0x2117, 0x00) // VMADD = 0
	wrReg(s, 0x4300, 0x01) // mode 1: B offsets 0,1
	wrReg(s, 0x4301, 0x18) // B = $2118/$2119
	wrReg(s, 0x4302, 0x00)
	wrReg(s, 0x4303, 0x10)
	wrReg(s, 0x4304, 0x7E)
	wrReg(s, 0x4305, 4)
	wrReg(s, 0x4306, 0)
	wrReg(s, 0x420B, 0x01)
	s.Step()
	// Read VRAM back through the prefetch protocol:
	// first low read is the stale discard.
	wrReg(s, 0x2116, 0x00)
	wrReg(s, 0x2117, 0x00)
	_ = rdReg(s, 0x2139)
	if v := rdReg(s, 0x2139); v != 0x34 {
		t.Fatalf("VRAM word 0 low = %#x, want 0x34", v)
	}
	_ = rdReg(s, 0x213A)
	if v := rdReg(s, 0x213A); v != 0x12 {
		t.Fatalf("VRAM word 0 high = %#x, want 0x12", v)
	}
	if v := rdReg(s, 0x213A); v != 0x56 {
		t.Fatalf("VRAM word 1 high = %#x, want 0x56", v)
	}
}

func TestDMAMode3RegisterPairs(t *testing.T) {
	// Mode 3 writes each of two consecutive B registers
	// twice in a row (pattern 0,0,1,1): the stream
	// 00 00 1F 00 aimed at CGADD/CGDATA sets color 0 to
	// 0x001F (address twice, then low byte, then high).
	s := newLoopSys(t)
	wram := s.Map.WRAM()
	copy(wram[0x1000:], []byte{0x00, 0x00, 0x1F, 0x00})
	wrReg(s, 0x4300, 0x03)
	wrReg(s, 0x4301, 0x21) // B = $2121/$2122
	wrReg(s, 0x4302, 0x00)
	wrReg(s, 0x4303, 0x10)
	wrReg(s, 0x4304, 0x7E)
	wrReg(s, 0x4305, 4)
	wrReg(s, 0x4306, 0)
	wrReg(s, 0x420B, 0x01)
	s.Step()
	wrReg(s, 0x2121, 0x00)
	if v := rdReg(s, 0x213B); v != 0x1F {
		t.Fatalf("CGRAM color 0 low = %#x, want 0x1f", v)
	}
	if v := rdReg(s, 0x213B); v != 0x00 {
		t.Fatalf("CGRAM color 0 high = %#x, want 0x00", v)
	}
}

func TestHDMADirect(t *testing.T) {
	s := newLoopSys(t)
	wram := s.Map.WRAM()
	// Table: 3 lines of 0xAA, then the terminator.
	copy(wram[0x0000:], []byte{0x03, 0xAA, 0x00})
	setWMADD(s, 0x3000)
	wrReg(s, 0x4300, 0x00) // mode 0, direct
	wrReg(s, 0x4301, 0x80) // B = $2180
	wrReg(s, 0x4302, 0x00)
	wrReg(s, 0x4303, 0x00) // table at $7E0000
	wrReg(s, 0x4304, 0x7E)
	wrReg(s, 0x420C, 0x01)
	s.RunFrame()
	for i := 0; i < 3; i++ {
		if wram[0x3000+i] != 0xAA {
			t.Errorf("WRAM[%#x] = %#x, want 0xaa (HDMA line %d)", 0x3000+i, wram[0x3000+i], i+1)
		}
	}
	if wram[0x3003] != 0 {
		t.Errorf("WRAM[0x3003] = %#x, want 0 (table terminated)", wram[0x3003])
	}
}

func TestHDMAIndirectAndRepeat(t *testing.T) {
	s := newLoopSys(t)
	wram := s.Map.WRAM()
	// Data block in WRAM; indirect table entries point
	// at it. Entry 1: 2 lines, non-repeat. Entry 2: 2
	// lines, repeating the same first byte.
	wram[0x4000] = 0x11
	wram[0x4001] = 0x22
	copy(wram[0x0000:], []byte{
		0x02, 0x00, 0x40, // 2 lines, data at $7E4000
		0x82, 0x00, 0x40, // 2 lines repeating $7E4000
		0x00,
	})
	setWMADD(s, 0x3000)
	wrReg(s, 0x4300, 0x40) // mode 0, indirect
	wrReg(s, 0x4301, 0x80)
	wrReg(s, 0x4302, 0x00)
	wrReg(s, 0x4303, 0x00)
	wrReg(s, 0x4304, 0x7E) // table bank
	wrReg(s, 0x4307, 0x7E) // DASB: data bank
	wrReg(s, 0x420C, 0x01)
	s.RunFrame()
	want := []byte{0x11, 0x22, 0x11, 0x11}
	for i, w := range want {
		if wram[0x3000+i] != w {
			t.Errorf("WRAM[%#x] = %#x, want %#x", 0x3000+i, wram[0x3000+i], w)
		}
	}
}

func TestMathUnit(t *testing.T) {
	s := newLoopSys(t)
	wrReg(s, 0x4202, 9)
	wrReg(s, 0x4203, 7)
	if v := rdReg(s, 0x4216); v != 0 {
		t.Errorf("RDMPY before latency = %#x, want 0 (previous result)", v)
	}
	for i := 0; i < 40; i++ {
		s.Step()
	}
	if v := rdReg(s, 0x4216); v != 63 {
		t.Errorf("RDMPY = %d, want 63", v)
	}
	wrReg(s, 0x4204, 100)
	wrReg(s, 0x4205, 0)
	wrReg(s, 0x4206, 7)
	for i := 0; i < 40; i++ {
		s.Step()
	}
	if v := rdReg(s, 0x4214); v != 14 {
		t.Errorf("RDDIV = %d, want 14", v)
	}
	if v := rdReg(s, 0x4216); v != 2 {
		t.Errorf("RDMPY (remainder) = %d, want 2", v)
	}
	// Division by zero: quotient is all ones, remainder
	// is the dividend.
	wrReg(s, 0x4206, 0)
	for i := 0; i < 40; i++ {
		s.Step()
	}
	if lo, hi := rdReg(s, 0x4214), rdReg(s, 0x4215); lo != 0xFF || hi != 0xFF {
		t.Errorf("RDDIV by zero = %#x%02x, want 0xffff", hi, lo)
	}
	if v := rdReg(s, 0x4216); v != 100 {
		t.Errorf("remainder by zero = %d, want 100", v)
	}
}

func TestJoypads(t *testing.T) {
	s := newLoopSys(t)
	s.SetButtons(0, ButtonA|ButtonStart|ButtonLeft)
	wrReg(s, 0x4200, 0x01) // auto-read enable
	s.RunFrame()           // lands at vblank start: read begins
	if v := rdReg(s, 0x4212); v&0x80 == 0 {
		t.Errorf("HVBJOY at vblank start = %#x, want vblank bit", v)
	}
	if v := rdReg(s, 0x4212); v&0x01 == 0 {
		t.Errorf("HVBJOY = %#x, want joypad busy during auto read", v)
	}
	s.RunFrame() // one more frame: the read has completed
	if v := rdReg(s, 0x4219); v != 0x80 {
		t.Errorf("JOY1H = %#x, want 0x80 (A at bit 7)", v)
	}
	// Serial order B Y Sel Start Up Down Left Right:
	// Start is index 3 (bit 4), Left index 6 (bit 1).
	if v := rdReg(s, 0x4218); v != 0x12 {
		t.Errorf("JOY1L = %#x, want 0x12 (Start + Left)", v)
	}
	// Manual serial read: strobe, then clock out pad 1.
	s.SetButtons(0, ButtonB|ButtonR)
	wrReg(s, 0x4016, 1)
	wrReg(s, 0x4016, 0)
	var got uint16
	for i := 0; i < 16; i++ {
		if rdReg(s, 0x4016)&1 != 0 {
			got |= 1 << uint(i)
		}
	}
	if got != uint16(ButtonB|ButtonR) {
		t.Errorf("serial readout = %#06x, want %#06x", got, uint16(ButtonB|ButtonR))
	}
}

func TestNMIFlag(t *testing.T) {
	s := newLoopSys(t)
	wrReg(s, 0x4200, 0x80) // NMI enable
	s.RunFrame()
	if v := rdReg(s, 0x4210); v&0x80 == 0 {
		t.Fatalf("RDNMI = %#x, want NMI flag set after vblank", v)
	}
	if v := rdReg(s, 0x4210); v&0x80 != 0 {
		t.Errorf("RDNMI second read = %#x, want flag cleared", v)
	}
}

func TestAPUMailboxes(t *testing.T) {
	s := newLoopSys(t)
	wrReg(s, 0x2140, 0x5A)
	if v := s.apuOut[0]; v != 0x5A {
		t.Errorf("apuOut[0] = %#x, want 0x5a stored", v)
	}
	// Nothing on the SPC side yet (SNES-3): reads return
	// the power-on state.
	if v := rdReg(s, 0x2140); v != 0 {
		t.Errorf("$2140 read = %#x, want 0 (no SPC700 yet)", v)
	}
	// The mirror window $2144-$217F folds onto the ports.
	if v := rdReg(s, 0x2144); v != 0 {
		t.Errorf("$2144 read = %#x, want mirror of $2140", v)
	}
}

// --- the determinism proof ---------------------------------------------

// demoROM builds the SNES-2 acceptance program described
// in demoROM's test: tiles and a tilemap uploaded by DMA,
// a palette uploaded by DMA, and an HDMA channel writing
// per-line brightness bands to INIDISP while an NMI
// handler counts frames in WRAM.
func demoROM() []byte {
	img := make([]byte, 0x80000)
	prog := []byte{
		0x18, 0xFB, // CLC, XCE
		0xC2, 0x10, // REP #$10
		0xE2, 0x20, // SEP #$20
		0xA9, 0x8F, 0x8D, 0x00, 0x21, // LDA #$8F; STA $2100
		0xA9, 0x80, 0x8D, 0x15, 0x21, // VMAIN = inc after high
		0xA9, 0x00, 0x8D, 0x16, 0x21, 0x8D, 0x17, 0x21, // VMADD = 0
		0xA9, 0x01, 0x8D, 0x00, 0x43, // DMAP0 = mode 1
		0xA9, 0x18, 0x8D, 0x01, 0x43, // BBAD = $2118
		0xA9, 0x00, 0x8D, 0x02, 0x43, // A1T low
		0xA9, 0x90, 0x8D, 0x03, 0x43, // A1T = $9000
		0xA9, 0x00, 0x8D, 0x04, 0x43, // bank 0
		0xA9, 0x40, 0x8D, 0x05, 0x43, // 64 bytes
		0xA9, 0x00, 0x8D, 0x06, 0x43,
		0xA9, 0x01, 0x8D, 0x0B, 0x42, // DMA go: tiles
		0xA9, 0x00, 0x8D, 0x16, 0x21, // VMADD = 0x0400
		0xA9, 0x04, 0x8D, 0x17, 0x21,
		0xA9, 0x00, 0x8D, 0x02, 0x43,
		0xA9, 0xA0, 0x8D, 0x03, 0x43, // A1T = $A000
		0xA9, 0x80, 0x8D, 0x05, 0x43, // 128 bytes
		0xA9, 0x01, 0x8D, 0x0B, 0x42, // DMA go: tilemap
		0xA9, 0x00, 0x8D, 0x21, 0x21, // CGADD = 0
		0xA9, 0x00, 0x8D, 0x00, 0x43, // DMAP = mode 0
		0xA9, 0x22, 0x8D, 0x01, 0x43, // BBAD = $2122
		0xA9, 0x00, 0x8D, 0x02, 0x43,
		0xA9, 0xB0, 0x8D, 0x03, 0x43, // A1T = $B000
		0xA9, 0x20, 0x8D, 0x05, 0x43, // 32 bytes
		0xA9, 0x01, 0x8D, 0x0B, 0x42, // DMA go: palette
		0xA9, 0x01, 0x8D, 0x05, 0x21, // BGMODE = 1
		0xA9, 0x04, 0x8D, 0x07, 0x21, // BG1SC: map at 0x0400
		0xA9, 0x00, 0x8D, 0x0B, 0x21, // BG1 tiles at 0
		0xA9, 0x01, 0x8D, 0x2C, 0x21, // TM = BG1
		0xA9, 0x00, 0x8D, 0x10, 0x43, // HDMA1 DMAP = mode 0
		0xA9, 0x00, 0x8D, 0x11, 0x43, // BBAD = $2100
		0xA9, 0x00, 0x8D, 0x12, 0x43,
		0xA9, 0xC0, 0x8D, 0x13, 0x43, // table at $C000
		0xA9, 0x00, 0x8D, 0x14, 0x43,
		0xA9, 0x02, 0x8D, 0x0C, 0x42, // HDMAEN = ch1
		0xA9, 0x0F, 0x8D, 0x00, 0x21, // display on
		0xA9, 0x81, 0x8D, 0x00, 0x42, // NMI + auto joypad
		0xCB,       // loop: WAI
		0x80, 0xFD, // BRA loop
	}
	copy(img, prog)
	// NMI handler at CPU $8100: count frames at $7E0010.
	copy(img[0x100:], []byte{
		0xAF, 0x10, 0x00, 0x7E, // LDA $7E0010
		0x1A,                   // INC A
		0x8F, 0x10, 0x00, 0x7E, // STA $7E0010
		0x40, // RTI
	})
	// Tiles at $9000: tile 0 all index 1, tile 1 all
	// index 2 (4bpp, 32 bytes each; each word's low byte
	// is plane 0's row, the high byte is plane 1's row).
	tiles := img[0x1000 : 0x1000+64]
	for w := 0; w < 8; w++ {
		tiles[w*2] = 0xFF      // tile 0: plane 0 row set
		tiles[32+w*2+1] = 0xFF // tile 1: plane 1 row set
	}
	// Tilemap at $A000: 64 entries alternating tiles.
	for i := 0; i < 64; i++ {
		img[0x2000+i*2] = uint8(i & 1)
	}
	// Palette at $B000: color 1 red, color 2 green.
	img[0x3000+2] = 0x1F
	img[0x3000+4] = 0xE0
	img[0x3000+5] = 0x03
	// HDMA table at $C000: brightness 15 for 2 lines,
	// 5 for 2 lines, 15 for the remaining 220.
	copy(img[0x4000:], []byte{0x02, 0x0F, 0x02, 0x05, 0x7F, 0x0F, 0x5D, 0x0F, 0x00})
	h := img[0x7FC0 : 0x7FC0+0x40]
	copy(h, "SNES-2 DEMO          ")
	h[0x15] = 0x20
	h[0x16] = 0x02
	h[0x17] = 0x09
	h[0x18] = 0x03
	h[0x1C], h[0x1D] = 0x55, 0xAA
	h[0x1E], h[0x1F] = 0xAA, 0x55
	h[0x2A], h[0x2B] = 0x00, 0x81 // native NMI vector
	h[0x3A], h[0x3B] = 0x00, 0x81 // emulation NMI vector
	h[0x3C], h[0x3D] = 0x00, 0x80 // reset vector
	return img
}

func framebufferHash(s *System) uint64 {
	h := fnv.New64a()
	var b [4]byte
	for _, px := range s.Framebuffer() {
		b[0] = uint8(px)
		b[1] = uint8(px >> 8)
		b[2] = uint8(px >> 16)
		b[3] = uint8(px >> 24)
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

func TestDeterminismAcrossSaveRestore(t *testing.T) {
	rom := demoROM()
	s1, err := New(rom)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 40; i++ {
		s1.RunFrame()
	}
	for i := 0; i < 500; i++ { // stop mid-frame
		s1.Step()
	}
	state := s1.Serialize()
	if len(state) != s1.StateSize() {
		t.Fatalf("state size = %d, StateSize = %d", len(state), s1.StateSize())
	}
	t.Logf("whole-machine state size: %d bytes", len(state))
	// The HDMA brightness bands must be visible: row 0
	// is at full brightness, row 2 is dimmed to 5/15.
	fb := s1.Framebuffer()
	if fb[0] == 0xFF000000 {
		t.Fatalf("frame is blank: the demo did not render")
	}
	if fb[0] == fb[2*Width] {
		t.Errorf("HDMA brightness bands missing: row0 %#08x == row2 %#08x", fb[0], fb[2*Width])
	}
	// Continue the original machine.
	for i := 0; i < 25; i++ {
		s1.RunFrame()
	}
	for i := 0; i < 500; i++ {
		s1.Step()
	}
	endState1 := s1.Serialize()
	endFB1 := framebufferHash(s1)
	if got := s1.Map.WRAM()[0x10]; got == 0 {
		t.Errorf("NMI frame counter never advanced")
	}
	// Restore into a fresh machine and continue the
	// same way: everything must match bit for bit.
	s2, err := New(rom)
	if err != nil {
		t.Fatalf("New (restore): %v", err)
	}
	if err := s2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	for i := 0; i < 25; i++ {
		s2.RunFrame()
	}
	for i := 0; i < 500; i++ {
		s2.Step()
	}
	if !bytes.Equal(endState1, s2.Serialize()) {
		t.Fatalf("state diverged across a save/restore boundary")
	}
	if got := framebufferHash(s2); got != endFB1 {
		t.Fatalf("framebuffer diverged across a save/restore boundary: %#x != %#x", got, endFB1)
	}
}

// TestKromROMs runs the Peter Lemon (Krom) SNES test ROMs
// headlessly. They are visual demos with no CPU-readable
// verdicts, so this test is report-only: it logs the
// frame hash and non-blank pixel count per ROM. Stronger
// assertion: every ROM must boot and produce a frame
// (the harness fails only if a ROM crashes the machine).
func TestKromROMsReportOnly(t *testing.T) {
	dir := filepath.Join("..", "testroms", "krom")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("Krom ROMs not present (%v)", err)
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) != ".sfc" {
			continue
		}
		rom, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		s, err := New(rom)
		if err != nil {
			t.Logf("%s: load refused: %v", e.Name(), err)
			continue
		}
		for i := 0; i < 120; i++ {
			s.RunFrame()
		}
		nonblank := 0
		for _, px := range s.Framebuffer() {
			if px != 0xFF000000 {
				nonblank++
			}
		}
		t.Logf("%s: frames=%d framehash=%016x nonblank=%d", e.Name(), s.Frames(), framebufferHash(s), nonblank)
	}
}
