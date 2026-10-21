package ppu

import "testing"

// setup returns a PPU with the display on at full
// brightness showing BG1 only.
func setup(t *testing.T) *PPU {
	t.Helper()
	p := New()
	wr(p, 0x2100, 0x0F) // brightness 15, not forced blank
	return p
}

func color(p *PPU, idx uint8, v uint16) {
	wr(p, 0x2121, idx)
	wr(p, 0x2122, uint8(v))
	wr(p, 0x2122, uint8(v>>8))
}

// setVOFSZero fakes the scroll pair that makes display
// line 0 sample tile row 0 (vertical scroll 0xFFFF wraps
// through the documented +1).
func setVOFS(p *PPU, reg uint16) {
	wr(p, reg, 0xFF)
	wr(p, reg, 0xFF)
}

const (
	red   = 0x001F
	green = 0x03E0
	blue  = 0x7C00
	white = 0x7FFF
)

func expand15(v uint16) uint32 {
	r := uint32(v & 0x1F)
	g := uint32((v >> 5) & 0x1F)
	b := uint32((v >> 10) & 0x1F)
	return 0xFF000000 | ((r<<3)|(r>>2))<<16 | ((g<<3)|(g>>2))<<8 | ((b << 3) | (b >> 2))
}

func px(p *PPU, x, y int) uint32 { return p.Framebuffer()[y*Width+x] }

func TestRenderMode0Basic(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00) // mode 0
	wr(p, 0x2107, 0x00) // BG1 tilemap at word 0
	wr(p, 0x210B, 0x01) // BG1 tiles at word 0x1000
	color(p, 0, 0x0000)
	color(p, 1, red)
	color(p, 2, green)
	color(p, 3, blue)
	// Tilemap entry 0: tile 0, palette 0, no priority.
	p.vram[0] = 0x0000
	// Tile 0 row 0: pixel indices 1,2,3,0 then zeros.
	// Plane pair word: low byte plane 0, high byte plane 1.
	p.vram[0x1000] = 0x60A0
	wr(p, 0x212C, 0x01) // TM: BG1 on main
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	if got := px(p, 0, 0); got != expand15(red) {
		t.Errorf("pixel(0,0) = %#08x, want red %#08x", got, expand15(red))
	}
	if got := px(p, 1, 0); got != expand15(green) {
		t.Errorf("pixel(1,0) = %#08x, want green", got)
	}
	if got := px(p, 2, 0); got != expand15(blue) {
		t.Errorf("pixel(2,0) = %#08x, want blue", got)
	}
	if got := px(p, 3, 0); got != expand15(0) {
		t.Errorf("pixel(3,0) = %#08x, want backdrop black", got)
	}
}

func TestRenderMode0PriorityAndSprite(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00)
	wr(p, 0x2107, 0x00)
	wr(p, 0x210B, 0x01)
	color(p, 0, 0)
	color(p, 1, red)
	color(p, 128+1, green) // sprite palette color 1
	// BG1 tile 0: all pixels index 1, entry has the
	// priority bit set (rank 12 in mode 0).
	p.vram[0] = 0x8000
	for r := 0; r < 8; r++ {
		p.vram[0x1000+r] = 0x00FF
	}
	// Sprite 0 at (4, 0), tile 0, palette 0, priority 0:
	// its rank (4) loses to the priority BG tile. Sprite
	// tiles live at word 0x4000 (OBSEL base select 2).
	wr(p, 0x2101, 0x02)
	p.oam[0] = 4
	p.oam[1] = 0
	p.oam[2] = 0
	p.oam[3] = 0
	for r := 0; r < 8; r++ {
		p.vram[0x4000+r] = 0x00FF
	}
	wr(p, 0x212C, 0x11) // BG1 + OBJ
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	if got := px(p, 5, 0); got != expand15(red) {
		t.Errorf("pixel(5,0) = %#08x, want BG red over sprite", got)
	}
	// Raise the sprite to priority 3 (rank 13): now the
	// sprite wins.
	p.oam[3] = 0x30
	p.RenderLine(0)
	if got := px(p, 5, 0); got != expand15(green) {
		t.Errorf("pixel(5,0) = %#08x, want sprite green over BG", got)
	}
}

func TestRenderSpriteLimits(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00)
	wr(p, 0x212C, 0x10) // OBJ only
	color(p, 0, 0)
	color(p, 128+1, red)
	for r := 0; r < 8; r++ {
		p.vram[r] = 0x00FF
	}
	// 33 sprites on line 0 at distinct X positions.
	for i := 0; i < 33; i++ {
		p.oam[i*4] = uint8(i * 7)
		p.oam[i*4+1] = 0
		p.oam[i*4+2] = 0
		p.oam[i*4+3] = 0
	}
	p.RenderLine(0)
	if !p.rangeOver {
		t.Errorf("range over flag not set with 33 sprites on a line")
	}
	if v := p.ReadReg(0x213E, 0); v&0x80 == 0 {
		t.Errorf("STAT77 = %#x, want range-over bit (0x80) set", v)
	}
}

func TestRenderMode3DirectColor(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x03)    // mode 3: BG1 8bpp, BG2 4bpp
	wr(p, 0x2107, 0x04)    // BG1 tilemap at word 0x0400
	wr(p, 0x210B, 0x00)    // BG1 tiles at word 0
	wr(p, 0x2130, 0x01)    // CGWSEL: direct color on (bit 0)
	p.vram[0x400] = 0x0000 // tilemap entry: tile 0
	// 8bpp tile 0 row 0: pixel 0 has index 0xE3
	// (direct color BB=3, GGG=4, RRR=3). Plane pair
	// words live at +0,+8,+16,+24; pixel 0 uses bit 7
	// of each plane byte. Index bits of 0xE3:
	// b0=1,b1=1,b2=0,b3=0,b4=0,b5=1,b6=1,b7=1.
	p.vram[0] = 0x8080  // plane0=0x80 (b0), plane1=0x80 (b1)
	p.vram[8] = 0x0000  // plane2=0 (b2), plane3=0 (b3)
	p.vram[16] = 0x8000 // plane4=0, plane5=0x80 (b5)
	p.vram[24] = 0x8080 // plane6=0x80 (b6), plane7=0x80 (b7)
	wr(p, 0x212C, 0x01)
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	// Direct color expansion: RRR=3 -> R5 = 3<<2 = 12,
	// GGG=4 -> G5 = 4<<7, BB=3 -> B5 = 3<<13.
	want := expand15(uint16(3<<2) | uint16(4<<7) | uint16(3<<13))
	if got := px(p, 0, 0); got != want {
		t.Errorf("pixel(0,0) = %#08x, want direct color %#08x", got, want)
	}
}

func TestRenderMode7Identity(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x07) // mode 7
	color(p, 0, white)  // backdrop
	color(p, 3, blue)
	// Identity matrix: A = D = 0x0100, B = C = 0.
	wr(p, 0x211B, 0x00)
	wr(p, 0x211B, 0x01)
	wr(p, 0x211C, 0x00)
	wr(p, 0x211C, 0x00)
	wr(p, 0x211D, 0x00) // M7B via $211C order fixed below
	_ = p
	// Mode 7 registers: A $211B, B $211C, C $211D, D $211E,
	// center X $211F, Y $2120.
	wr(p, 0x211D, 0x00)
	wr(p, 0x211D, 0x00) // C = 0
	wr(p, 0x211E, 0x00)
	wr(p, 0x211E, 0x01) // D = 0x0100
	// Tilemap: word 0 low byte = tile 0. Tile 0 pixel
	// (0,0) is the high byte of word 0 of the tile data:
	// tiles sit at word tile*64, pixel index stored in
	// the high byte.
	p.vram[0] = 0x0000
	p.vram[0] = 0x0300 // word for pixel (0,0): high byte 3
	wr(p, 0x212C, 0x01)
	p.RenderLine(0)
	if got := px(p, 0, 0); got != expand15(blue) {
		t.Errorf("mode7 pixel(0,0) = %#08x, want blue", got)
	}
	if got := px(p, 1, 0); got != expand15(white) {
		t.Errorf("mode7 pixel(1,0) = %#08x, want backdrop white", got)
	}
}

func TestRenderWindowClip(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00)
	wr(p, 0x2107, 0x00)
	wr(p, 0x210B, 0x01)
	color(p, 0, 0)
	color(p, 1, red)
	p.vram[0] = 0x0000
	for r := 0; r < 8; r++ {
		p.vram[0x1000+r] = 0x00FF
	}
	// Window 1 covers x 5..10, BG1 uses it (enable bit),
	// TMW masks BG1 on the main screen inside it.
	wr(p, 0x2123, 0x02) // BG1: W1 enable
	wr(p, 0x2126, 5)    // WH0
	wr(p, 0x2127, 10)   // WH1
	wr(p, 0x212C, 0x01)
	wr(p, 0x212E, 0x01) // TMW: BG1
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	if got := px(p, 4, 0); got != expand15(red) {
		t.Errorf("pixel(4,0) = %#08x, want red (outside window)", got)
	}
	if got := px(p, 7, 0); got != expand15(0) {
		t.Errorf("pixel(7,0) = %#08x, want clipped to backdrop", got)
	}
	if got := px(p, 11, 0); got != expand15(red) {
		t.Errorf("pixel(11,0) = %#08x, want red", got)
	}
}

func TestRenderColorMathAddFixed(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00)
	wr(p, 0x2107, 0x00)
	wr(p, 0x210B, 0x01)
	color(p, 1, green)
	p.vram[0] = 0x0000
	for r := 0; r < 8; r++ {
		p.vram[0x1000+r] = 0x00FF
	}
	wr(p, 0x212C, 0x01) // BG1 main, sub screen empty
	wr(p, 0x2132, 0x20|31)
	// COLDATA: bit5 selects red; value 31. (A write with
	// only the red bit set updates red alone.)
	wr(p, 0x2131, 0x01) // CGADSUB: add, BG1 math on
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	want := expand15(red | green) // green + fixed red = yellow
	if got := px(p, 0, 0); got != want {
		t.Errorf("pixel(0,0) = %#08x, want yellow %#08x", got, want)
	}
}

func TestRenderColorMathHalf(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00)
	wr(p, 0x2107, 0x00)
	wr(p, 0x210B, 0x01)
	color(p, 1, white)
	p.vram[0] = 0x0000
	for r := 0; r < 8; r++ {
		p.vram[0x1000+r] = 0x00FF
	}
	wr(p, 0x212C, 0x01)
	wr(p, 0x2131, 0x41) // add + half, BG1
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	// Half color math divides the result: the average
	// of white (31) and the black fixed color is mid
	// gray (15) per channel.
	gray := uint16(15 | 15<<5 | 15<<10)
	if got := px(p, 0, 0); got != expand15(gray) {
		t.Errorf("pixel(0,0) = %#08x, want mid gray %#08x", got, expand15(gray))
	}
}

func TestRenderMosaic(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00)
	wr(p, 0x2107, 0x00)
	wr(p, 0x210B, 0x01)
	color(p, 1, red)
	color(p, 2, green)
	p.vram[0] = 0x0000
	// Row 0: pixel 0 index 1, pixel 1 index 2.
	p.vram[0x1000] = 0x4080
	wr(p, 0x2106, 0x11) // mosaic size 2, BG1 enabled
	wr(p, 0x212C, 0x01)
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	if got := px(p, 1, 0); got != expand15(red) {
		t.Errorf("pixel(1,0) = %#08x, want red (mosaic block sample)", got)
	}
	if got := px(p, 0, 0); got != expand15(red) {
		t.Errorf("pixel(0,0) = %#08x, want red", got)
	}
}

func TestRenderMode2OffsetPerTile(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x02) // mode 2: BG1/BG2 4bpp, BG3 = OPT source
	wr(p, 0x2107, 0x00) // BG1 tilemap at 0
	wr(p, 0x2109, 0x10) // BG3 tilemap at word 0x1000
	wr(p, 0x210B, 0x00) // BG1 tiles at word 0
	color(p, 1, red)
	// Offset table, BG3 tilemap word 0: bit 15 (BG1
	// horizontal flag) set, offset 8 pixels.
	p.vram[0x1000] = 0x8008
	// BG1 tilemap entry at column 1 names tile 1, the
	// tile the OPT offset scrolls into view.
	p.vram[1] = 0x0001
	// BG1 tile 1: all pixels color index 1. 4bpp tile:
	// 16 words; row plane pair word = 0x00FF.
	for r := 0; r < 8; r++ {
		p.vram[16+r] = 0x00FF
	}
	wr(p, 0x212C, 0x01)
	setVOFS(p, 0x210E) // BG1 vofs to row 0
	// BG3 scrolls must be zero: they already are after
	// the $210E writes? No: $210E is BG1VOFS. BG3 VOFS is
	// $2112; leave at power-on 0, but then its +1 puts
	// the OPT lookup at row (0 + 0 + 1)/8 = 0 either way.
	p.RenderLine(0)
	if got := px(p, 0, 0); got != expand15(red) {
		t.Errorf("pixel(0,0) = %#08x, want red from OPT-shifted tile", got)
	}
	// Control: clear the flag bit and the pixel falls
	// back to backdrop.
	p.vram[0x1000] = 0x0008
	p.RenderLine(0)
	if got := px(p, 0, 0); got != expand15(0) {
		t.Errorf("control pixel(0,0) = %#08x, want backdrop", got)
	}
}

func TestRenderMode5PairAveraging(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x05) // mode 5 hi-res
	wr(p, 0x2107, 0x04) // BG1 tilemap at word 0x0400
	wr(p, 0x210B, 0x00) // BG1 tiles at word 0
	color(p, 1, red)
	color(p, 2, blue)
	p.vram[0x400] = 0x0000 // tilemap entry: tile 0
	// BG1 tile 0 (left half): row 0 all index 1 except
	// sub-pixel 7 which is index 2. 4bpp: plane pair
	// word low byte plane0 bits: idx1=b0: set all but
	// pixel 7; idx2 at pixel 7 is plane1.
	// Simpler: tile 0 all index 1; tile 1 (right half)
	// all index 2; scroll 1 makes output pixel 3 sample
	// sub-pixels 7 (red) and 8 (blue), averaging them.
	for r := 0; r < 8; r++ {
		p.vram[r] = 0x00FF    // tile 0 row r: index 1
		p.vram[16+r] = 0xFF00 // tile 1 row r: plane1 high byte -> index 2
	}
	wr(p, 0x210D, 0x01) // BG1 HOFS low
	wr(p, 0x210D, 0x00) // high: total scroll 1
	wr(p, 0x212C, 0x01)
	setVOFS(p, 0x210E)
	p.RenderLine(0)
	want := uint32(0xFF000000)
	want |= uint32(127) << 16 // blue (255+0)/2
	want |= uint32(127)       // red (255+0)/2
	if got := px(p, 3, 0); got != want {
		t.Errorf("hi-res pixel(3,0) = %#08x, want averaged %#08x", got, want)
	}
	if got := px(p, 0, 0); got != expand15(red) {
		t.Errorf("hi-res pixel(0,0) = %#08x, want red", got)
	}
}

// The CGWSEL region fields live in the top nibble
// (bits 7-6 main black region, bits 5-4 sub transparent
// region). These tests pin that layout: an earlier build
// read the fields from bits 0-3, which made any game
// using CGWSEL $12 (sub screen addend, sub transparent
// outside the color window) clip its whole main screen
// to black inside the color window.

func cgwBlackSetup(t *testing.T) *PPU {
	t.Helper()
	p := setup(t)
	wr(p, 0x2105, 0x00)
	wr(p, 0x2107, 0x00)
	wr(p, 0x210B, 0x01)
	color(p, 0, 0)
	color(p, 1, red)
	p.vram[0] = 0x0000
	for r := 0; r < 8; r++ {
		p.vram[0x1000+r] = 0x00FF
	}
	// Color window 1 covers x 5..10 (WOBJSEL color
	// window 1 enable is bit 5).
	wr(p, 0x2125, 0x20)
	wr(p, 0x2126, 5)  // WH0
	wr(p, 0x2127, 10) // WH1
	wr(p, 0x212C, 0x01)
	setVOFS(p, 0x210E)
	return p
}

func TestRenderCGWSELMainBlackRegion(t *testing.T) {
	cases := []struct {
		name         string
		cgwsel       uint8
		insideBlack  bool // x=7, inside the color window
		outsideBlack bool // x=4, outside the color window
	}{
		{"never", 0x00, false, false},
		{"black outside", 0x40, false, true},
		{"black inside", 0x80, true, false},
		{"black everywhere", 0xC0, true, true},
		// The value SMW uses for its title and attract
		// demo: no main blacking at all.
		{"smw value 0x12", 0x12, false, false},
	}
	for _, c := range cases {
		p := cgwBlackSetup(t)
		wr(p, 0x2130, c.cgwsel)
		p.RenderLine(0)
		want := func(black bool) uint32 {
			if black {
				return expand15(0)
			}
			return expand15(red)
		}
		if got := px(p, 7, 0); got != want(c.insideBlack) {
			t.Errorf("%s: pixel(7,0) = %#08x, want %#08x", c.name, got, want(c.insideBlack))
		}
		if got := px(p, 4, 0); got != want(c.outsideBlack) {
			t.Errorf("%s: pixel(4,0) = %#08x, want %#08x", c.name, got, want(c.outsideBlack))
		}
	}
}

func TestRenderCGWSELSubTransparentRegion(t *testing.T) {
	p := setup(t)
	wr(p, 0x2105, 0x00) // mode 0
	wr(p, 0x2107, 0x00) // BG1 map at 0
	wr(p, 0x2108, 0x00) // BG2 map at 0
	wr(p, 0x210B, 0x11) // BG1 and BG2 tiles at word 0x1000
	color(p, 1, 0)      // BG1 pixels: opaque black
	color(p, 33, green) // BG2 pixels (mode 0 quarter 2): green
	p.vram[0] = 0x0000
	for r := 0; r < 8; r++ {
		p.vram[0x1000+r] = 0x00FF
	}
	wr(p, 0x2125, 0x20) // color window 1 enable
	wr(p, 0x2126, 5)
	wr(p, 0x2127, 10)
	wr(p, 0x212C, 0x01) // TM: BG1
	wr(p, 0x212D, 0x02) // TS: BG2
	wr(p, 0x2132, 0x20|31)
	// COLDATA: fixed color = red.
	wr(p, 0x2131, 0x01) // CGADSUB: add, BG1 math on
	setVOFS(p, 0x210E)
	setVOFS(p, 0x2110) // BG2 VOFS
	// CGWSEL $12: addend = sub screen, sub forced
	// transparent outside the color window. Inside the
	// window the green sub pixel is the addend; outside
	// it the fixed red takes its place.
	wr(p, 0x2130, 0x12)
	p.RenderLine(0)
	if got := px(p, 7, 0); got != expand15(green) {
		t.Errorf("inside: pixel(7,0) = %#08x, want green (sub addend)", got)
	}
	if got := px(p, 4, 0); got != expand15(red) {
		t.Errorf("outside: pixel(4,0) = %#08x, want red (fixed fallback)", got)
	}
	// Control: with the sub region set to nowhere
	// ($02), the sub pixel is the addend everywhere.
	wr(p, 0x2130, 0x02)
	p.RenderLine(0)
	if got := px(p, 4, 0); got != expand15(green) {
		t.Errorf("control: pixel(4,0) = %#08x, want green", got)
	}
}

func TestRenderCGWSELBlackedPixelStillGetsMath(t *testing.T) {
	p := cgwBlackSetup(t)
	wr(p, 0x2132, 0x40|31)
	// COLDATA: fixed color = green.
	wr(p, 0x2131, 0x01) // CGADSUB: add, BG1 math on
	// CGWSEL $C0: main forced black everywhere, addend
	// is the fixed color. The blacked red pixel still
	// receives math: 0 + green = green.
	wr(p, 0x2130, 0xC0)
	p.RenderLine(0)
	if got := px(p, 0, 0); got != expand15(green) {
		t.Errorf("pixel(0,0) = %#08x, want green (math on blacked pixel)", got)
	}
	// With half math requested, halving is suppressed
	// on a force-blacked pixel: still full green, not
	// the halved (0+31)/2.
	wr(p, 0x2131, 0x41)
	p.RenderLine(0)
	if got := px(p, 0, 0); got != expand15(green) {
		t.Errorf("half pixel(0,0) = %#08x, want full green (half suppressed)", got)
	}
}
