package vdp1

import "testing"

// Synthetic fixtures only: fresh memory per test, commands written
// by hand into VRAM, pixels read back from the frame buffer slices.

type fixture struct {
	v    *VDP1
	vram []byte
	fb   []byte
	regs []byte
}

func newFixture() *fixture {
	f := &fixture{
		vram: make([]byte, 512*1024),
		fb:   make([]byte, 512*1024),
		regs: make([]byte, 128),
	}
	f.v = New(f.vram, f.fb, f.regs)
	f.v.SetDisplaySize(64, 64)
	return f
}

func putWord(b []byte, addr int, val uint16) {
	b[addr] = byte(val >> 8)
	b[addr+1] = byte(val)
}

func getWord(b []byte, addr int) uint16 {
	return uint16(b[addr])<<8 | uint16(b[addr+1])
}

func (f *fixture) setReg(off int, val uint16) { putWord(f.regs, off, val) }
func (f *fixture) getReg(off int) uint16      { return getWord(f.regs, off) }

// cmdSpec is one command table. The v array holds the eight vertex
// words in XA, YA, XB, YB, XC, YC, XD, YD order as raw words.
type cmdSpec struct {
	ctrl uint16
	link uint16
	pmod uint16
	colr uint16
	srca uint16
	size uint16
	grda uint16
	v    [8]uint16
}

func (f *fixture) writeCmd(addr int, s cmdSpec) {
	putWord(f.vram, addr+0x00, s.ctrl)
	putWord(f.vram, addr+0x02, s.link)
	putWord(f.vram, addr+0x04, s.pmod)
	putWord(f.vram, addr+0x06, s.colr)
	putWord(f.vram, addr+0x08, s.srca)
	putWord(f.vram, addr+0x0A, s.size)
	for i := 0; i < 8; i += 1 {
		putWord(f.vram, addr+0x0C+i*2, s.v[i])
	}
	putWord(f.vram, addr+0x1C, s.grda)
}

func (f *fixture) drawPixel(x, y int) uint16 {
	off := f.v.DrawBank()*262144 + (y*64+x)*2
	return getWord(f.fb, off)
}

func checkPix(t *testing.T, f *fixture, x, y int, want uint16) {
	t.Helper()
	if got := f.drawPixel(x, y); got != want {
		t.Errorf("pixel (%d, %d) = %#06x, want %#06x", x, y, got, want)
	}
}

// patternMode0 fills a 16x8 mode 0 pattern at 0x1000 with code 1,
// first dot code 2.
func (f *fixture) patternMode0() {
	for i := 0; i < 64; i += 1 {
		f.vram[0x1000+i] = 0x11
	}
	f.vram[0x1000] = 0x21
}

// Gate 1: command list with system clipping, local coordinate and
// a scaled sprite, plus END termination.
func TestCommandListGate(t *testing.T) {
	f := newFixture()
	f.patternMode0()
	f.writeCmd(0x00, cmdSpec{ctrl: 9, v: [8]uint16{0, 0, 0, 0, 63, 63, 0, 0}})
	f.writeCmd(0x20, cmdSpec{ctrl: 10, v: [8]uint16{10, 5, 0, 0, 0, 0, 0, 0}})
	f.writeCmd(0x40, cmdSpec{
		ctrl: 1, colr: 0x1230, srca: 0x200, size: 0x0107,
		v: [8]uint16{0, 0, 0, 0, 15, 7, 0, 0},
	})
	f.writeCmd(0x60, cmdSpec{ctrl: 0x8000})
	// A polygon after END must never execute.
	f.writeCmd(0x80, cmdSpec{
		ctrl: 4, colr: 0x7C00,
		v: [8]uint16{50, 50, 60, 50, 60, 60, 50, 60},
	})
	f.v.Draw()
	checkPix(t, f, 10, 5, 0x1232)
	checkPix(t, f, 11, 5, 0x1231)
	checkPix(t, f, 25, 12, 0x1231)
	checkPix(t, f, 9, 5, 0)
	checkPix(t, f, 26, 5, 0)
	checkPix(t, f, 55, 55, 0)
}

// Gate 2a: distorted sprite as a plain rectangle.
func TestDistortedSpriteRectangle(t *testing.T) {
	f := newFixture()
	for row := 0; row < 32; row += 1 {
		for u := 0; u < 32; u += 1 {
			f.vram[0x1000+row*32+u] = byte(u/8 + 1)
		}
	}
	f.writeCmd(0x00, cmdSpec{
		ctrl: 2, pmod: 4 << 3, colr: 0x4000, srca: 0x200, size: 0x031F,
		v: [8]uint16{0, 0, 31, 0, 31, 31, 0, 31},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	// Center (16, 16): u is 16, code is 16/8 + 1 = 3.
	checkPix(t, f, 16, 16, 0x4003)
}

// Gate 2b: distorted sprite as a true trapezoid.
//
// Hand computation for pixel (16, 16) with quad A(8,0), B(23,0),
// C(31,31), D(0,31) and a 32x32 texture:
//
//	left edge A to D at y=16: x = 8 - 8*16/31 = 3.87, u = 0,
//	  vv = 32*16/31 = 16.52
//	right edge B to C at y=16: x = 23 + 8*16/31 = 27.13, u = 32,
//	  vv = 16.52
//	span fraction at x=16: (16 - 3.87) / (27.13 - 3.87) = 0.5215
//	u = 0.5215 * 32 = 16.69, floored to 16
//	code = 16/8 + 1 = 3, so the pixel is 0x4003.
func TestDistortedSpriteTrapezoid(t *testing.T) {
	f := newFixture()
	for row := 0; row < 32; row += 1 {
		for u := 0; u < 32; u += 1 {
			f.vram[0x1000+row*32+u] = byte(u/8 + 1)
		}
	}
	f.writeCmd(0x00, cmdSpec{
		ctrl: 2, pmod: 4 << 3, colr: 0x4000, srca: 0x200, size: 0x031F,
		v: [8]uint16{8, 0, 23, 0, 31, 31, 0, 31},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 16, 16, 0x4003)
}

// Gate 3: polygon fill.
func TestPolygonFill(t *testing.T) {
	f := newFixture()
	f.writeCmd(0x00, cmdSpec{
		ctrl: 4, colr: 0x7C00,
		v: [8]uint16{10, 10, 29, 10, 29, 19, 10, 19},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 10, 10, 0x7C00)
	checkPix(t, f, 29, 19, 0x7C00)
	checkPix(t, f, 20, 15, 0x7C00)
	checkPix(t, f, 30, 15, 0)
	checkPix(t, f, 9, 15, 0)
}

// Gate 4: gouraud midpoint. The polygon is 21 by 21 so (20, 20) is
// the exact center. Corner channels are A(20,0,0), B(0,20,0),
// C(0,0,20), D(20,20,20); each channel sums to 40 over the four
// corners, so the center average is exactly (10, 10, 10), which
// packs to 10<<10 | 10<<5 | 10 = 0x294A. Base color is 0 and the
// color calculation mode is 4 (gouraud only), so the pixel is
// the interpolated gouraud value alone.
func TestGouraudMidpoint(t *testing.T) {
	f := newFixture()
	putWord(f.vram, 0x2000, 0x5000) // A: RGB(20, 0, 0)
	putWord(f.vram, 0x2002, 0x0280) // B: RGB(0, 20, 0)
	putWord(f.vram, 0x2004, 0x0014) // C: RGB(0, 0, 20)
	putWord(f.vram, 0x2006, 0x5294) // D: RGB(20, 20, 20)
	f.writeCmd(0x00, cmdSpec{
		ctrl: 4, pmod: 4, colr: 0, grda: 0x0400,
		v: [8]uint16{10, 10, 30, 10, 30, 30, 10, 30},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 20, 20, 0x294A)
}

// Gate 5: mesh pattern.
func TestMesh(t *testing.T) {
	f := newFixture()
	f.writeCmd(0x00, cmdSpec{
		ctrl: 4, pmod: 0x0100, colr: 0x7C00,
		v: [8]uint16{10, 10, 29, 10, 29, 19, 10, 19},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	// (10 xor 10) and 1 is 0, so the dot is skipped.
	checkPix(t, f, 10, 10, 0)
	// (11 xor 10) and 1 is 1, so the dot is drawn.
	checkPix(t, f, 11, 10, 0x7C00)
}

// Gate 6: half transparent blending. Existing pixel RGB(0, 0, 31),
// new pixel RGB(31, 0, 0): channels become ((31+0)>>1, 0,
// (0+31)>>1) = (15, 0, 15), packed as 15<<10 | 15 = 0x3C0F.
func TestHalfTransparent(t *testing.T) {
	f := newFixture()
	verts := [8]uint16{10, 10, 20, 10, 20, 20, 10, 20}
	f.writeCmd(0x00, cmdSpec{ctrl: 4, colr: 0x001F, v: verts})
	f.writeCmd(0x20, cmdSpec{ctrl: 4, pmod: 2, colr: 0x7C00, v: verts})
	f.writeCmd(0x40, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 15, 15, 0x3C0F)
}

// Gate 7: user clipping, inside mode and outside mode.
func TestUserClipping(t *testing.T) {
	build := func(cmod uint16) *fixture {
		f := newFixture()
		f.writeCmd(0x00, cmdSpec{ctrl: 8, v: [8]uint16{20, 20, 0, 0, 29, 29, 0, 0}})
		f.writeCmd(0x20, cmdSpec{
			ctrl: 4, pmod: 0x0400 | (cmod << 9), colr: 0x7C00,
			v: [8]uint16{0, 0, 63, 0, 63, 63, 0, 63},
		})
		f.writeCmd(0x40, cmdSpec{ctrl: 0x8000})
		f.v.Draw()
		return f
	}
	inside := build(0)
	checkPix(t, inside, 25, 25, 0x7C00)
	checkPix(t, inside, 15, 15, 0)
	outside := build(1)
	checkPix(t, outside, 25, 25, 0)
	checkPix(t, outside, 15, 15, 0x7C00)
}

// Gate 8a: 1 cycle frame change swaps banks, erases the new draw
// bank and auto draws; the display bank shows the drawn pixels.
func TestFrameChangeOneCycle(t *testing.T) {
	f := newFixture()
	f.setReg(0x00, 0)      // TVMR: no VBE, 16 bit
	f.setReg(0x02, 0)      // FBCR: 1 cycle mode
	f.setReg(0x04, 0)      // PTMR: auto draw
	f.setReg(0x06, 0x4242) // EWDR
	f.setReg(0x08, 0)      // EWLR: X1 0, Y1 0
	f.setReg(0x0A, (7<<9)|63)
	f.writeCmd(0x00, cmdSpec{
		ctrl: 4, colr: 0x7C00,
		v: [8]uint16{5, 5, 10, 5, 10, 10, 5, 10},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	if f.v.DrawBank() != 0 {
		t.Fatalf("draw bank before frame boundary = %d, want 0", f.v.DrawBank())
	}
	f.v.FrameBoundary()
	if f.v.DrawBank() != 1 {
		t.Fatalf("draw bank after frame boundary = %d, want 1", f.v.DrawBank())
	}
	// The new draw bank was erased with the fill word where the
	// auto draw did not paint.
	checkPix(t, f, 30, 30, 0x4242)
	// Auto draw repainted the polygon into the new draw bank.
	checkPix(t, f, 5, 5, 0x7C00)
	// The display bank is the old bank and shows the drawn pixels.
	w, ok := f.v.SpriteWordAt(5, 5, 0)
	if !ok || w != 0x7C00 {
		t.Errorf("SpriteWordAt(5, 5) = %#06x, %v; want 0x7c00, true", w, ok)
	}
}

// Gate 8b: manual change swaps without erasing and clears FCT.
func TestFrameChangeManualChange(t *testing.T) {
	f := newFixture()
	f.setReg(0x00, 0)
	f.setReg(0x02, 0x0003) // FBCR: FCM 1, FCT 1
	f.setReg(0x06, 0x4242)
	f.setReg(0x08, 0)
	f.setReg(0x0A, (7<<9)|63)
	f.v.FrameBoundary()
	if f.v.DrawBank() != 1 {
		t.Fatalf("draw bank = %d, want 1", f.v.DrawBank())
	}
	if got := f.getReg(0x02); got != 0x0002 {
		t.Errorf("FBCR after manual change = %#06x, want 0x0002 (FCT cleared)", got)
	}
	// No erase happened: the new draw bank is still zeroed.
	checkPix(t, f, 30, 30, 0)
}

// Gate 9a: jump assign skips a command.
func TestJumpAssignSkips(t *testing.T) {
	f := newFixture()
	f.writeCmd(0x00, cmdSpec{ctrl: 0x100A, link: 0x40 / 8})
	f.writeCmd(0x20, cmdSpec{
		ctrl: 4, colr: 0x7C00,
		v: [8]uint16{0, 0, 10, 0, 10, 10, 0, 10},
	})
	f.writeCmd(0x40, cmdSpec{
		ctrl: 4, colr: 0x03E0,
		v: [8]uint16{20, 20, 30, 20, 30, 30, 20, 30},
	})
	f.writeCmd(0x60, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 5, 5, 0)
	checkPix(t, f, 25, 25, 0x03E0)
}

// Gate 9b: call and return run a subroutine command.
func TestJumpCallReturn(t *testing.T) {
	f := newFixture()
	f.writeCmd(0x00, cmdSpec{ctrl: 0x200A, link: 0x40 / 8})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.writeCmd(0x40, cmdSpec{
		ctrl: 0x3004, colr: 0x001F,
		v: [8]uint16{20, 20, 30, 20, 30, 30, 20, 30},
	})
	f.v.Draw()
	checkPix(t, f, 25, 25, 0x001F)
}

// Gate 10: normal sprite with horizontal flip. Pattern codes are
// 5 at u=0 and 9 at u=7, so the flipped leftmost dot carries 9.
func TestNormalSpriteFlipH(t *testing.T) {
	f := newFixture()
	f.vram[0x1000] = 0x51
	f.vram[0x1001] = 0x11
	f.vram[0x1002] = 0x11
	f.vram[0x1003] = 0x19
	f.writeCmd(0x00, cmdSpec{
		ctrl: 0x0010, colr: 0x1000, srca: 0x200, size: 0,
		v: [8]uint16{0, 0, 0, 0, 0, 0, 0, 0},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 0, 0, 0x1009)
	checkPix(t, f, 7, 0, 0x1005)
}

// Gate 11: CLUT color mode. Pattern code 3 selects CLUT entry 3,
// which holds green 0x03E0.
func TestCLUTMode(t *testing.T) {
	f := newFixture()
	for i := 0; i < 4; i += 1 {
		f.vram[0x1000+i] = 0x33
	}
	putWord(f.vram, 0x2000+3*2, 0x03E0)
	f.writeCmd(0x00, cmdSpec{
		ctrl: 0, pmod: 1 << 3, colr: 0x0400, srca: 0x200, size: 0,
		v: [8]uint16{0, 0, 0, 0, 0, 0, 0, 0},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 0, 0, 0x03E0)
}

// Gate 12: end code stops the rest of the row when ECD is 0 and is
// drawn as data when ECD is 1. Row codes are 1, 0xF, 1, 1, then 1s.
func TestEndCode(t *testing.T) {
	build := func(ecd uint16) *fixture {
		f := newFixture()
		f.vram[0x1000] = 0x1F
		f.vram[0x1001] = 0x11
		f.vram[0x1002] = 0x11
		f.vram[0x1003] = 0x11
		f.writeCmd(0x00, cmdSpec{
			ctrl: 0, pmod: ecd << 7, colr: 0x1000, srca: 0x200, size: 0,
			v: [8]uint16{0, 0, 0, 0, 0, 0, 0, 0},
		})
		f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
		f.v.Draw()
		return f
	}
	off := build(0)
	checkPix(t, off, 0, 0, 0x1001)
	checkPix(t, off, 1, 0, 0)
	checkPix(t, off, 2, 0, 0)
	checkPix(t, off, 3, 0, 0)
	on := build(1)
	checkPix(t, on, 1, 0, 0x100F)
	checkPix(t, on, 2, 0, 0x1001)
}

// Gate 13: LOPR, COPR, EDSR and the draw end flag after a draw of
// two commands plus END at 0x40.
func TestStatusRegisters(t *testing.T) {
	f := newFixture()
	f.writeCmd(0x00, cmdSpec{ctrl: 10, v: [8]uint16{0, 0, 0, 0, 0, 0, 0, 0}})
	f.writeCmd(0x20, cmdSpec{
		ctrl: 4, colr: 0x7C00,
		v: [8]uint16{0, 0, 10, 0, 10, 10, 0, 10},
	})
	f.writeCmd(0x40, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	if got := f.getReg(0x12); got != 0x40 {
		t.Errorf("LOPR = %#06x, want 0x0040", got)
	}
	if got := f.getReg(0x14); got != 0x40 {
		t.Errorf("COPR = %#06x, want 0x0040", got)
	}
	if got := f.getReg(0x10); got&1 == 0 {
		t.Errorf("EDSR = %#06x, want CEF bit set", got)
	}
	if !f.v.TakeDrawEnd() {
		t.Error("TakeDrawEnd = false, want true once")
	}
	if f.v.TakeDrawEnd() {
		t.Error("TakeDrawEnd = true twice, want false the second time")
	}
}

// Extra gate: scaled sprite zoom point form. ZP is 5, so the
// horizontal anchor is half the width and the vertical anchor is
// half the height. The zoom point is (20, 20) and the size words
// XB and YB are 16 each, so the destination is (12, 12) to
// (27, 27).
func TestScaledZoomPoint(t *testing.T) {
	f := newFixture()
	for i := 0; i < 32; i += 1 {
		f.vram[0x1000+i] = 0x11
	}
	f.writeCmd(0x00, cmdSpec{
		ctrl: 1 | (5 << 8), colr: 0x1000, srca: 0x200, size: 0x0007,
		v: [8]uint16{20, 20, 16, 16, 0, 0, 0, 0},
	})
	f.writeCmd(0x20, cmdSpec{ctrl: 0x8000})
	f.v.Draw()
	checkPix(t, f, 12, 12, 0x1001)
	checkPix(t, f, 27, 27, 0x1001)
	checkPix(t, f, 11, 12, 0)
	checkPix(t, f, 28, 28, 0)
}

// Extra gate: Misc packs and restores bank, draw end and previous
// PTM state.
func TestMiscRoundTrip(t *testing.T) {
	f := newFixture()
	f.v.SetMisc(0x000D) // bank 1, previous PTM 3
	if f.v.DrawBank() != 1 {
		t.Errorf("DrawBank = %d, want 1", f.v.DrawBank())
	}
	if got := f.v.Misc(); got != 0x000D {
		t.Errorf("Misc = %#06x, want 0x000d", got)
	}
	f.v.SetMisc(0)
	if got := f.v.Misc(); got != 0 {
		t.Errorf("Misc after clear = %#06x, want 0", got)
	}
}
