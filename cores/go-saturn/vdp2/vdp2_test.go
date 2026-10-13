package vdp2

import "testing"

// fakeSprite is a synthetic sprite source. It returns the same
// word at every dot position.
type fakeSprite struct {
	word    uint16
	present bool
	eight   bool
}

func (f *fakeSprite) SpriteWordAt(x, y, line int) (uint16, bool) {
	return f.word, f.present
}

func (f *fakeSprite) EightBit() bool { return f.eight }

type fixture struct {
	v          *VDP2
	vram, cram []byte
	regs       []byte
	fb         []uint32
	w, h       int
}

func newFixture(spr SpriteSource) *fixture {
	f := &fixture{
		vram: make([]byte, 512*1024),
		cram: make([]byte, 4096),
		regs: make([]byte, 512),
	}
	f.v = New(f.vram, f.cram, f.regs, spr)
	f.setReg(0x000, 0x8000) // TVMD: DISP on, 320 by 224
	// Back screen: single blue entry at VRAM 0x4600.
	f.setReg(0x0AE, 0x4600)
	f.vramWord(0x4600, 0x001F)
	f.w, f.h = f.v.DisplaySize()
	f.fb = make([]uint32, f.w*f.h)
	return f
}

func (f *fixture) setReg(off int, val uint16) {
	f.regs[off] = byte(val >> 8)
	f.regs[off+1] = byte(val)
}

func (f *fixture) vramWord(addr int, val uint16) {
	f.vram[addr] = byte(val >> 8)
	f.vram[addr+1] = byte(val)
}

func (f *fixture) cram555(idx int, val uint16) {
	off := (idx & 0x3FF) * 2
	f.cram[off] = byte(val >> 8)
	f.cram[off+1] = byte(val)
}

func (f *fixture) fillChar(charNo int, b byte) {
	base := charNo * 0x20
	for i := 0; i < 32; i++ {
		f.vram[base+i] = b
	}
}

func (f *fixture) render() {
	for y := 0; y < f.h; y++ {
		f.v.RenderLine(f.fb, y)
	}
}

func (f *fixture) pixel(x, y int) uint32 {
	return f.fb[y*f.w+x]
}

func pack(r, g, b int) uint32 {
	return 0xFF000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

var (
	red   = pack(255, 0, 0)
	green = pack(0, 255, 0)
	blue  = pack(0, 0, 255)
	white = pack(255, 255, 255)
)

// setupBasicNBG0 builds the probe used by several tests: NBG0 in
// 16 color 1 word cell mode, plane A map value 1, cell 0 holding
// character 1 with palette 1. Character 1 is all code 1 except
// its first dot, which is code 2. CRAM 17 is red, CRAM 18 green.
func setupBasicNBG0(f *fixture) {
	f.setReg(0x020, 0x0001) // BGON: NBG0 on
	f.setReg(0x030, 0x8000) // PNCN0: 1 word patterns
	f.setReg(0x0F8, 0x0002) // PRINA: NBG0 priority 2
	f.setReg(0x040, 0x0001) // MPABN0: plane A map value 1
	f.setReg(0x078, 0x0001) // ZMXIN0: zoom increment 1
	f.setReg(0x07C, 0x0001) // ZMYIN0: zoom increment 1
	f.vramWord(0x2000, (1<<10)|1)
	f.fillChar(1, 0x11)
	f.vram[0x20] = 0x21 // first dot code 2, second dot code 1
	f.cram555(17, 0x7C00)
	f.cram555(18, 0x03E0)
}

func TestNBG0CellMode(t *testing.T) {
	f := newFixture(nil)
	setupBasicNBG0(f)
	f.render()
	if got := f.pixel(0, 0); got != green {
		t.Fatalf("pixel(0,0) = %08x, want green %08x", got, green)
	}
	if got := f.pixel(1, 0); got != red {
		t.Fatalf("pixel(1,0) = %08x, want red %08x", got, red)
	}
	if got := f.pixel(7, 0); got != red {
		t.Fatalf("pixel(7,0) = %08x, want red %08x", got, red)
	}
	// Scroll right by 3: screen dot 0 shows pattern dot 3, and
	// screen dot 5 lands on the empty cell 1, showing the back
	// screen.
	f.setReg(0x070, 3) // SCXIN0
	f.render()
	if got := f.pixel(0, 0); got != red {
		t.Fatalf("scrolled pixel(0,0) = %08x, want red %08x", got, red)
	}
	if got := f.pixel(5, 0); got != blue {
		t.Fatalf("scrolled pixel(5,0) = %08x, want back blue %08x", got, blue)
	}
}

func TestPatternNameVariants(t *testing.T) {
	// 2 word pattern: palette 2 from the word, special priority
	// bit set in the word, character 1 in the second word.
	f := newFixture(nil)
	f.setReg(0x020, 0x0001)
	f.setReg(0x030, 0x0000) // PNCN0: 2 word patterns
	f.setReg(0x0F8, 0x0002)
	f.setReg(0x040, 0x0001)
	f.setReg(0x078, 0x0001)
	f.setReg(0x07C, 0x0001)
	f.vramWord(0x2000, 0x2000|(2<<5))
	f.vramWord(0x2002, 1)
	f.fillChar(1, 0x11)
	f.cram555(33, 0x7C00) // palette 2, code 1
	f.render()
	if got := f.pixel(0, 0); got != red {
		t.Fatalf("2 word pixel(0,0) = %08x, want red %08x", got, red)
	}

	// 1 word pattern with CNSM set: 12 bit character number and
	// palette in bits 15 to 12.
	g := newFixture(nil)
	g.setReg(0x020, 0x0001)
	g.setReg(0x030, 0xC000) // PNCN0: 1 word, CNSM on
	g.setReg(0x0F8, 0x0002)
	g.setReg(0x040, 0x0001)
	g.setReg(0x078, 0x0001)
	g.setReg(0x07C, 0x0001)
	g.vramWord(0x2000, (1<<12)|1)
	g.fillChar(1, 0x11)
	g.cram555(17, 0x7C00) // palette 1, code 1
	g.render()
	if got := g.pixel(0, 0); got != red {
		t.Fatalf("CNSM pixel(0,0) = %08x, want red %08x", got, red)
	}
}

// setupRowCells places three solid characters in cells 0, 1, 2 of
// row 0 and the matching row 1 cells for the scroll tests.
func setupRowCells(f *fixture) {
	f.setReg(0x020, 0x0001)
	f.setReg(0x030, 0x8000)
	f.setReg(0x0F8, 0x0002)
	f.setReg(0x040, 0x0001)
	f.setReg(0x078, 0x0001)
	f.setReg(0x07C, 0x0001)
	f.vramWord(0x2000, 1) // cell (0,0): char 1
	f.vramWord(0x2002, 2) // cell (1,0): char 2
	f.vramWord(0x2004, 3) // cell (2,0): char 3
	f.vramWord(0x2080, 2) // cell (0,1): char 2
	f.vramWord(0x2082, 2) // cell (1,1): char 2
	f.fillChar(1, 0x11)
	f.fillChar(2, 0x22)
	f.fillChar(3, 0x33)
	f.cram555(1, 0x7C00) // code 1: red
	f.cram555(2, 0x001F) // code 2: blue
	f.cram555(3, 0x03E0) // code 3: green
}

func TestLineScroll(t *testing.T) {
	f := newFixture(nil)
	setupRowCells(f)
	f.setReg(0x09A, 0x0002) // SCRCTL NBG0: LSCX on
	f.setReg(0x0A0, 0x0000) // LSTA0 upper
	f.setReg(0x0A2, 0x5000) // LSTA0 lower: table at 0x5000
	// Line 5 entry: X integer 16, fraction 0. Each entry is one
	// field of two words, so line 5 sits at plus 20 bytes.
	f.vramWord(0x5000+20, 16)
	f.vramWord(0x5000+22, 0)
	f.render()
	if got := f.pixel(0, 5); got != green {
		t.Fatalf("line 5 pixel(0,5) = %08x, want green %08x (shifted by 16)", got, green)
	}
	if got := f.pixel(0, 4); got != red {
		t.Fatalf("line 4 pixel(0,4) = %08x, want red %08x (unshifted)", got, red)
	}
}

func TestVerticalCellScroll(t *testing.T) {
	f := newFixture(nil)
	setupRowCells(f)
	f.setReg(0x09A, 0x0001) // SCRCTL NBG0: VCSC on
	f.setReg(0x09C, 0x0000) // VCSTA upper
	f.setReg(0x09E, 0x5200) // VCSTA lower: table at 0x5200
	// Cell column 1 entry: integer 8, fraction 0, at plus 4.
	f.vramWord(0x5200+4, 8)
	f.vramWord(0x5200+6, 0)
	f.render()
	if got := f.pixel(0, 0); got != red {
		t.Fatalf("pixel(0,0) = %08x, want red %08x", got, red)
	}
	if got := f.pixel(8, 0); got != blue {
		t.Fatalf("pixel(8,0) = %08x, want blue %08x (sampled 8 dots lower)", got, blue)
	}
}

// setupRotation builds RBG0 with the identity transform over the
// same probe pattern as the NBG0 test.
func setupRotation(f *fixture) {
	f.setReg(0x020, 0x0010) // BGON: RBG0 on
	f.setReg(0x038, 0x8000) // PNCR: 1 word patterns
	f.setReg(0x0FC, 0x0001) // PRIR: RBG0 priority 1
	f.setReg(0x050, 0x0001) // MPABRA: plane A map value 1
	f.setReg(0x0BE, 0x6000) // RPTAL: parameter table at 0x6000
	f.vramWord(0x2000, (1<<10)|1)
	f.fillChar(1, 0x11)
	f.vram[0x20] = 0x21
	f.cram555(17, 0x7C00)
	f.cram555(18, 0x03E0)
	const tab = 0x6000
	f.vramWord(tab+0x10, 1) // dYst integer: advance Y by 1 per line
	f.vramWord(tab+0x14, 1) // dX integer: advance X by 1 per dot
	f.vramWord(tab+0x1C, 1) // matrix A: 1
	f.vramWord(tab+0x28, 1) // matrix D: 1
	f.vramWord(tab+0x4C, 1) // kx integer: 1
	f.vramWord(tab+0x50, 1) // ky integer: 1
}

func TestRBG0IdentityAndZoom(t *testing.T) {
	f := newFixture(nil)
	setupRotation(f)
	f.render()
	// These are the same values the NBG0 probe produces.
	if got := f.pixel(0, 0); got != green {
		t.Fatalf("rotation pixel(0,0) = %08x, want green %08x", got, green)
	}
	if got := f.pixel(1, 0); got != red {
		t.Fatalf("rotation pixel(1,0) = %08x, want red %08x", got, red)
	}
	if got := f.pixel(0, 1); got != red {
		t.Fatalf("rotation pixel(0,1) = %08x, want red %08x", got, red)
	}
	// Halve the scale factors: screen dot 1 now samples source
	// dot 0 and screen dot 2 samples source dot 1.
	f.vramWord(0x6000+0x4C, 0)
	f.vramWord(0x6000+0x4E, 0x8000)
	f.vramWord(0x6000+0x50, 0)
	f.vramWord(0x6000+0x52, 0x8000)
	f.render()
	if got := f.pixel(1, 0); got != green {
		t.Fatalf("half zoom pixel(1,0) = %08x, want green %08x (source dot 0)", got, green)
	}
	if got := f.pixel(2, 0); got != red {
		t.Fatalf("half zoom pixel(2,0) = %08x, want red %08x (source dot 1)", got, red)
	}
}

func TestFBRotationCoord(t *testing.T) {
	f := newFixture(nil)
	setupRotation(f)
	x, y, ok := f.v.FBRotationCoord(5, 0, 0)
	if !ok || x != 5 || y != 0 {
		t.Fatalf("FBRotationCoord(5,0,0) = (%d,%d,%v), want (5,0,true)", x, y, ok)
	}
	x, y, ok = f.v.FBRotationCoord(5, 0, 3)
	if !ok || x != 5 || y != 3 {
		t.Fatalf("FBRotationCoord(5,0,3) = (%d,%d,%v), want (5,3,true)", x, y, ok)
	}
}

func TestPriorityAndSpecialPriority(t *testing.T) {
	f := newFixture(nil)
	// NBG0: solid red, cells 0 and 1.
	f.setReg(0x020, 0x0003) // BGON: NBG0 and NBG1 on
	f.setReg(0x030, 0x8000)
	f.setReg(0x040, 0x0001)
	f.setReg(0x078, 0x0001)
	f.setReg(0x07C, 0x0001)
	f.vramWord(0x2000, 1)
	f.vramWord(0x2002, 1)
	f.fillChar(1, 0x11)
	f.cram555(1, 0x7C00)
	// NBG1: solid green via 2 word patterns, palette 3, char 2.
	// Cell 0 carries the special priority bit, cell 1 does not.
	f.setReg(0x032, 0x0000)
	f.setReg(0x044, 0x0002) // MPABN1: plane A map value 2
	f.setReg(0x088, 0x0001)
	f.setReg(0x08C, 0x0001)
	f.vramWord(0x4000, 0x2000|(3<<5))
	f.vramWord(0x4002, 2)
	f.vramWord(0x4004, 3<<5)
	f.vramWord(0x4006, 2)
	f.fillChar(2, 0x11)
	f.cram555(49, 0x03E0)
	// Phase A: plain register priorities 2 against 5, green wins.
	f.setReg(0x0F8, 0x0502)
	f.render()
	if got := f.pixel(0, 0); got != green {
		t.Fatalf("phase A pixel(0,0) = %08x, want green %08x", got, green)
	}
	// Phase B: SFPRMD mode 1 for NBG1, both registers at 4. The
	// special bit raises the cell 0 dot to priority 5 (green
	// wins) while the cell 1 dot ties at 4 and loses to NBG0 by
	// the fixed tie break order (red wins).
	f.setReg(0x0EA, 0x0004) // SFPRMD: NBG1 mode 1
	f.setReg(0x0F8, 0x0404)
	f.render()
	if got := f.pixel(0, 0); got != green {
		t.Fatalf("phase B pixel(0,0) = %08x, want green %08x (special priority 5)", got, green)
	}
	if got := f.pixel(8, 0); got != red {
		t.Fatalf("phase B pixel(8,0) = %08x, want red %08x (tie at 4, NBG0 first)", got, red)
	}
}

func TestColorCalculationBlend(t *testing.T) {
	f := newFixture(nil)
	f.setReg(0x020, 0x0003)
	// NBG0 top: solid red, palette 0 code 1.
	f.setReg(0x030, 0x8000)
	f.setReg(0x040, 0x0001)
	f.setReg(0x078, 0x0001)
	f.setReg(0x07C, 0x0001)
	f.vramWord(0x2000, 1)
	f.fillChar(1, 0x11)
	f.cram555(1, 0x7C00)
	// NBG1 second: solid blue, palette 2 code 1, char 3.
	f.setReg(0x032, 0x8000)
	f.setReg(0x044, 0x0003)
	f.setReg(0x088, 0x0001)
	f.setReg(0x08C, 0x0001)
	f.vramWord(0x6000, (2<<10)|3)
	f.fillChar(3, 0x11)
	f.cram555(33, 0x001F)
	// Priorities 5 over 3, NBG0 ratio 16, NBG0 CC enabled.
	f.setReg(0x0F8, 0x0305)
	f.setReg(0x108, 0x0010) // CCRNA: NBG0 ratio 16
	f.setReg(0x0EC, 0x0001) // CCCTL: N0CCEN on
	f.render()
	want := pack(127, 0, 127)
	if got := f.pixel(0, 0); got != want {
		t.Fatalf("blend pixel(0,0) = %08x, want %08x", got, want)
	}
}

func TestWindowOutside(t *testing.T) {
	f := newFixture(nil)
	// NBG0 solid red across cells 0, 1, and 2.
	f.setReg(0x020, 0x0001)
	f.setReg(0x030, 0x8000)
	f.setReg(0x0F8, 0x0002)
	f.setReg(0x040, 0x0001)
	f.setReg(0x078, 0x0001)
	f.setReg(0x07C, 0x0001)
	f.vramWord(0x2000, 1)
	f.vramWord(0x2002, 1)
	f.vramWord(0x2004, 1)
	f.fillChar(1, 0x11)
	f.cram555(1, 0x7C00)
	// W0 covers x 10 to 19 on all lines.
	f.setReg(0x0C0, 10)
	f.setReg(0x0C2, 0)
	f.setReg(0x0C4, 19)
	f.setReg(0x0C6, 223)
	// WCTLA low byte for NBG0: W0E on, W0A set (visible outside).
	f.setReg(0x0D0, 0x0003)
	f.render()
	if got := f.pixel(15, 0); got != blue {
		t.Fatalf("pixel(15,0) = %08x, want back blue %08x (inside window, hidden)", got, blue)
	}
	if got := f.pixel(9, 0); got != red {
		t.Fatalf("pixel(9,0) = %08x, want red %08x (outside window, visible)", got, red)
	}
	if got := f.pixel(20, 0); got != red {
		t.Fatalf("pixel(20,0) = %08x, want red %08x (outside window, visible)", got, red)
	}
}

func TestCRAMMode2(t *testing.T) {
	f := newFixture(nil)
	f.setReg(0x00E, 0x2000) // RAMCTL: CRMD mode 2
	f.setReg(0x020, 0x0001)
	f.setReg(0x030, 0x8000)
	f.setReg(0x0F8, 0x0002)
	f.setReg(0x040, 0x0001)
	f.setReg(0x078, 0x0001)
	f.setReg(0x07C, 0x0001)
	f.vramWord(0x2000, 1)
	f.fillChar(1, 0x55) // all dots code 5
	// Long index 5 split layout: byte 1 at offset 11 is red,
	// byte 2 at 0x800 plus 10 is green, byte 3 next is blue.
	f.cram[11] = 0x11
	f.cram[0x800+10] = 0x22
	f.cram[0x800+11] = 0x33
	f.render()
	want := pack(0x11, 0x22, 0x33)
	if got := f.pixel(0, 0); got != want {
		t.Fatalf("CRAM mode 2 pixel(0,0) = %08x, want %08x", got, want)
	}
}

func TestBackScreenAndLineColor(t *testing.T) {
	f := newFixture(nil)
	// NBG0 enabled but fully transparent, with its line color
	// enable bit set so insertion fires with no top candidate.
	f.setReg(0x020, 0x0001)
	f.setReg(0x030, 0x8000)
	f.setReg(0x040, 0x0001)
	f.setReg(0x0E8, 0x0001) // LNCLEN: NBG0 on
	// Line color table per line at VRAM 0x4800.
	f.setReg(0x0A8, 0x8000) // LCTAU: LCCLMD per line
	f.setReg(0x0AA, 0x4800)
	f.vramWord(0x4800+3*2, 0x8000|0x03E0) // line 3: insert, green
	f.render()
	if got := f.pixel(0, 3); got != green {
		t.Fatalf("line 3 pixel = %08x, want line color green %08x", got, green)
	}
	if got := f.pixel(0, 4); got != blue {
		t.Fatalf("line 4 pixel = %08x, want back blue %08x", got, blue)
	}
}

func TestSpriteComposition(t *testing.T) {
	spr := &fakeSprite{word: 0x8003, present: true}
	f := newFixture(spr)
	setupBasicNBG0(f)
	// Make the NBG0 cell solid red for a clean witness: palette 0
	// code 1 via a second entry is overkill; reuse palette 1
	// code 1 red at CRAM 17 and accept the green first dot by
	// probing dot 1 instead of dot 0.
	f.setReg(0x0E0, 0x0000) // SPCTL: type 0
	f.cram555(3, 0x7FFF)    // sprite color code 3: white
	// Sprite word: PR bit 15 set means index 4, S4 in PRISC.
	f.setReg(0x0F4, 0x0006) // PRISC: S4 priority 6
	f.render()
	if got := f.pixel(1, 0); got != white {
		t.Fatalf("sprite pixel(1,0) = %08x, want white %08x (sprite priority 6)", got, white)
	}
	f.setReg(0x0F4, 0x0001) // S4 priority 1, under NBG0 priority 2
	f.render()
	if got := f.pixel(1, 0); got != red {
		t.Fatalf("sprite pixel(1,0) = %08x, want red %08x (NBG0 wins)", got, red)
	}
	f.setReg(0x0F4, 0x0000) // S4 priority 0: sprite invisible
	f.render()
	if got := f.pixel(1, 0); got != red {
		t.Fatalf("sprite pixel(1,0) = %08x, want red %08x (sprite priority 0)", got, red)
	}
}

func TestZoom(t *testing.T) {
	f := newFixture(nil)
	setupRowCells(f)
	// Register zoom 2: screen dot 4 samples source dot 8, which
	// is the blue cell 1.
	f.setReg(0x078, 0x0002) // ZMXIN0 = 2
	f.render()
	if got := f.pixel(4, 0); got != blue {
		t.Fatalf("zoom 2 pixel(4,0) = %08x, want blue %08x (source dot 8)", got, blue)
	}
	// Register zoom 3: screen dot 6 samples source dot 18, the
	// green cell 2.
	f.setReg(0x078, 0x0003)
	f.render()
	if got := f.pixel(6, 0); got != green {
		t.Fatalf("zoom 3 pixel(6,0) = %08x, want green %08x (source dot 18)", got, green)
	}
	// ZMHF forces increment 2 regardless of the register: screen
	// dot 6 samples source dot 12, the blue cell 1.
	f.setReg(0x098, 0x0001) // ZMCTL NBG0: ZMHF on
	f.render()
	if got := f.pixel(6, 0); got != blue {
		t.Fatalf("ZMHF pixel(6,0) = %08x, want blue %08x (source dot 12)", got, blue)
	}
}

func TestDisplaySizeAndTotalLines(t *testing.T) {
	cases := []struct {
		tvmd      uint16
		w, h, tot int
	}{
		{0x0000, 320, 224, 263},
		{(1 << 4) | 1, 352, 240, 263},
		{2, 640, 224, 263},
		{7, 704, 480, 561},
		{4, 320, 480, 525},
		{5, 352, 480, 561},
		{3 << 6, 320, 448, 263},
	}
	for _, c := range cases {
		f := newFixture(nil)
		f.setReg(0x000, c.tvmd)
		w, h := f.v.DisplaySize()
		if w != c.w || h != c.h {
			t.Fatalf("TVMD %04x size = %dx%d, want %dx%d", c.tvmd, w, h, c.w, c.h)
		}
		if tot := f.v.TotalLines(); tot != c.tot {
			t.Fatalf("TVMD %04x total lines = %d, want %d", c.tvmd, tot, c.tot)
		}
	}
}

func TestSetTimingAndHBlank(t *testing.T) {
	f := newFixture(nil)
	f.v.SetTiming(300, true)
	if got := f.v.reg16(0x00A); got != 300 {
		t.Fatalf("VCNT = %d, want 300", got)
	}
	st := f.v.reg16(0x004)
	if st&(1<<3) == 0 {
		t.Fatalf("TVSTAT %04x missing VBLANK at line 300 of 224", st)
	}
	if st&(1<<1) == 0 {
		t.Fatalf("TVSTAT %04x missing ODD", st)
	}
	f.v.SetTiming(10, false)
	st = f.v.reg16(0x004)
	if st&(1<<3) != 0 || st&(1<<1) != 0 {
		t.Fatalf("TVSTAT %04x should have VBLANK and ODD clear at line 10", st)
	}
	f.v.SetHBlank(true)
	if f.v.reg16(0x004)&(1<<2) == 0 {
		t.Fatalf("TVSTAT missing HBLANK after SetHBlank(true)")
	}
	f.v.SetHBlank(false)
	if f.v.reg16(0x004)&(1<<2) != 0 {
		t.Fatalf("TVSTAT HBLANK stuck after SetHBlank(false)")
	}
}
