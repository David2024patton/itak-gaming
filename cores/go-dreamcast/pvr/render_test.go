package pvr

import (
	"encoding/binary"
	"math"
	"testing"
)

// Test scaffolding for the renderer gates. The frame is
// 32 by 32 pixels at VRAM offset 0x100000 in RGB0565, the
// texture area starts at 0x2000, and TA parameter blocks
// land at TA_ISP_BASE 0 (all clear of each other).

const (
	testFB   = 0x100000
	testTex  = 0x2000
	testW    = 32
	testH    = 32
	regBase  = 0x005F8000
	bg565Val = 0x4410 // the 0565 encoding of the test background
)

// bgWord is the ISP_BACKGND_S value used by most gates: its
// channels are exact 0565 values (r5 8, g6 32, b5 16), so it
// round trips through the framebuffer unchanged.
const bgWord = 0xFF428284

// bgPresent is bgWord as Present reports it.
const bgPresent = 0xFF428284

func newRenderPVR(t *testing.T) (*PVR, []byte) {
	t.Helper()
	vram := make([]byte, 8<<20)
	p := New(vram, nil)
	wr := func(off, v uint32) { p.Write32(regBase+off, v) }
	wr(RegFBXClip, 31<<16)
	wr(RegFBYClip, 31<<16)
	wr(RegFBWCTRL, 1|(Fmt0565<<1)) // write enable, RGB0565
	wr(RegFBWSOF1, testFB)
	wr(RegFBRCTRL, 1|(Fmt0565<<1)) // display enable, RGB0565
	wr(RegFBRSOF1, testFB)
	wr(RegFBRSize, 15|(31<<10)) // 16 words wide, 32 lines
	wr(RegISPBackgndS, bgWord)
	wr(RegISPBackgndD, math.Float32bits(0))
	return p, vram
}

// TA parameters are fed with the packet helper from
// pvr_test.go (one 32-byte parameter, zero padded).

func pcw(para, list uint32) uint32 { return para<<29 | list<<24 }

func f32(f float32) uint32 { return math.Float32bits(f) }

// ispWord builds an ISP/TSP instruction word.
func ispWord(depth, cull int, textured, gouraud, offset bool) uint32 {
	w := uint32(depth) | uint32(cull)<<4
	if textured {
		w |= ispTextured
	}
	if gouraud {
		w |= ispGouraud
	}
	if offset {
		w |= ispOffset
	}
	return w
}

// tspWord builds a TSP word. sizeCode maps a texture size to
// its field code: size is 8 shifted left by the code.
func tspWord(filter int, srcBlend, dstBlend int, useAlpha bool, uSize, vSize int) uint32 {
	w := uint32(filter)
	w |= uint32(dstBlend) << 16
	w |= uint32(srcBlend) << 19
	if useAlpha {
		w |= 1 << 23
	}
	w |= uint32(sizeCode(uSize)) << 26
	w |= uint32(sizeCode(vSize)) << 29
	return w
}

func sizeCode(size int) int {
	c := 0
	for s := 8; s < size; s *= 2 {
		c++
	}
	return c
}

// tcwWord builds a TCW word for a texture at a VRAM byte
// offset in the given pixel format.
func tcwWord(addr uint32, format int, vq bool, palSelect int) uint32 {
	w := (addr / 8) & 0x1FFFFF
	w |= uint32(palSelect&0x3F) << 21
	w |= uint32(format) << 27
	if vq {
		w |= 1 << 30
	}
	return w
}

// vertex submits one 32-byte vertex parameter.
func vertex(p *PVR, list uint32, eos bool, x, y, z, u, v float32, base, off uint32) {
	w := pcw(paraVertex, list)
	if eos {
		w |= pcwEOS
	}
	packet(p, w, f32(x), f32(y), f32(z), f32(u), f32(v), base, off)
}

// quad submits a four vertex strip covering the rectangle
// (x0,y0) to (x1,y1), with UV corners (u0,v0) to (u1,v1).
func quad(p *PVR, list uint32, z, u0, v0, u1, v1, x0, y0, x1, y1 float32, base uint32) {
	vertex(p, list, false, x0, y0, z, u0, v0, base, 0)
	vertex(p, list, false, x1, y0, z, u1, v0, base, 0)
	vertex(p, list, false, x0, y1, z, u0, v1, base, 0)
	vertex(p, list, true, x1, y1, z, u1, v1, base, 0)
}

// polyHeader submits a single-volume polygon header.
func polyHeader(p *PVR, list uint32, isp, tsp, tcw, baseColor uint32) {
	packet(p, pcw(paraPolygon, list), isp, tsp, tcw, baseColor, 0, 0, 0)
}

// renderNow triggers a render the way software does, by
// writing STARTRENDER, and returns the presented frame.
func renderNow(t *testing.T, p *PVR) []uint32 {
	t.Helper()
	p.Write32(regBase+RegStartRender, 1)
	out := make([]uint32, testW*testH)
	p.Present(out, testW, testH)
	return out
}

func px(out []uint32, x, y int) uint32 { return out[y*testW+x] }

// expand565 converts a raw 0565 framebuffer word to the
// XRGB8888 value Present reports for it.
func expand565(v uint16) uint32 {
	r := int((v >> 11) & 0x1F)
	g := int((v >> 5) & 0x3F)
	b := int(v & 0x1F)
	return 0xFF000000 | uint32((r<<3)|(r>>2))<<16 |
		uint32((g<<2)|(g>>4))<<8 | uint32((b<<3)|(b>>2))
}

func putTexel565(vram []byte, base uint32, x, y, w, h int, val uint16) {
	off := base + uint32(twiddleIndex(x, y, w, h))*2
	binary.LittleEndian.PutUint16(vram[off:], val)
}

// Gate (a): the background plane fills the frame.
func TestRenderBackground(t *testing.T) {
	p, vram := newRenderPVR(t)
	out := renderNow(t, p)
	if p.RenderCount() != 1 {
		t.Fatalf("render count = %d, want 1", p.RenderCount())
	}
	for _, xy := range [][2]int{{0, 0}, {31, 0}, {0, 31}, {31, 31}, {16, 16}} {
		if got := px(out, xy[0], xy[1]); got != bgPresent {
			t.Errorf("pixel %v = %#08x, want %#08x", xy, got, bgPresent)
		}
	}
	// The raw framebuffer holds the hand-computed 0565
	// word: r5 8 in bits 15:11, g6 32 in bits 10:5,
	// b5 16 in bits 4:0 gives 0x4410.
	if got := binary.LittleEndian.Uint16(vram[testFB:]); got != bg565Val {
		t.Errorf("raw framebuffer word = %#06x, want %#06x", got, bg565Val)
	}
	// The last pixel of the frame, at stride 64 bytes.
	off := testFB + 31*64 + 31*2
	if got := binary.LittleEndian.Uint16(vram[off:]); got != bg565Val {
		t.Errorf("last pixel word = %#06x, want %#06x", got, bg565Val)
	}
}

// Gate (b): one opaque textured quad, point sampled, plus a
// bilinear midpoint computed by hand.
func TestRenderTexturedQuadPoint(t *testing.T) {
	p, vram := newRenderPVR(t)
	// 8x8 RGB565 texture. Texel (x,y) encodes r5 = x*4,
	// g6 = y*8, b5 = 31 - x*4, all distinct per texel.
	texel := func(x, y int) uint16 {
		return uint16(x*4)<<11 | uint16(y*8)<<5 | uint16(31-x*4)
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			putTexel565(vram, testTex, x, y, 8, 8, texel(x, y))
		}
	}
	isp := ispWord(DepthAlways, 0, true, false, false)
	tsp := tspWord(0, BlendOne, BlendZero, false, 8, 8) // point filter
	tcw := tcwWord(testTex, Tex565, false, 0)
	polyHeader(p, ListOpaque, isp, tsp, tcw, 0xFFFFFFFF)
	quad(p, ListOpaque, 0.5, 0, 0, 1, 1, 0, 0, 32, 32, 0xFFFFFFFF)
	out := renderNow(t, p)
	// Pixel centers map to texels by (px/4, py/4).
	for _, tc := range []struct{ px, py, tx, ty int }{
		{2, 2, 0, 0}, {6, 2, 1, 0}, {14, 10, 3, 2},
		{30, 30, 7, 7}, {18, 26, 4, 6},
	} {
		want := expand565(texel(tc.tx, tc.ty))
		if got := px(out, tc.px, tc.py); got != want {
			t.Errorf("pixel (%d,%d) = %#08x, want texel (%d,%d) %#08x",
				tc.px, tc.py, got, tc.tx, tc.ty, want)
		}
	}
}

func TestRenderTexturedQuadBilinearMidpoint(t *testing.T) {
	p, vram := newRenderPVR(t)
	// Texels (2,2) pure red and (3,2) pure green, all else
	// black.
	putTexel565(vram, testTex, 2, 2, 8, 8, 0xF800)
	putTexel565(vram, testTex, 3, 2, 8, 8, 0x07E0)
	isp := ispWord(DepthAlways, 0, true, false, false)
	tsp := tspWord(1, BlendOne, BlendZero, false, 8, 8) // bilinear
	tcw := tcwWord(testTex, Tex565, false, 0)
	polyHeader(p, ListOpaque, isp, tsp, tcw, 0xFFFFFFFF)
	// UV offset 1/32 and range 2 in both axes. At pixel
	// (5,4) the center (5.5, 4.5) maps to u = 6/16 and
	// v = 10/32, so s = u*8 - 0.5 = 2.5 (exactly between
	// texels 2 and 3 in U) and t = v*8 - 0.5 = 2.0 (exactly
	// on texel row 2, so the V fraction is 0). The sample
	// is the exact average of red and green.
	quad(p, ListOpaque, 0.5, 0.03125, 0.03125, 2.03125, 2.03125, 0, 0, 32, 32, 0xFFFFFFFF)
	out := renderNow(t, p)
	// Average (0.5, 0.5, 0) quantizes in 0565 to
	// r5 = round(0.5*31) = 16, g6 = round(0.5*63) = 32,
	// which Present expands to R 132, G 130, B 0.
	want := uint32(0xFF848200)
	if got := px(out, 5, 4); got != want {
		t.Errorf("bilinear midpoint = %#08x, want %#08x", got, want)
	}
	if got := binary.LittleEndian.Uint16(vram[testFB+4*64+5*2:]); got != 0x8400 {
		t.Errorf("raw midpoint word = %#06x, want 0x8400", got)
	}
}

// Gate (c): depth compare. The far quad has Z 0.25, the near
// quad Z 0.75 (Z is 1/w, larger is nearer).
func TestRenderDepthGreater(t *testing.T) {
	const (
		red   = 0xFFFF0000
		green = 0xFF00FF00
	)
	isp := ispWord(DepthGreater, 0, false, false, false)
	tsp := tspWord(0, BlendOne, BlendZero, false, 8, 8)

	// Far first, near second: the near quad wins.
	p, _ := newRenderPVR(t)
	polyHeader(p, ListOpaque, isp, tsp, 0, red)
	quad(p, ListOpaque, 0.25, 0, 0, 0, 0, 0, 0, 32, 32, red)
	polyHeader(p, ListOpaque, isp, tsp, 0, green)
	quad(p, ListOpaque, 0.75, 0, 0, 0, 0, 0, 0, 32, 32, green)
	out := renderNow(t, p)
	if got := px(out, 16, 16); got != 0xFF00FF00 {
		t.Errorf("far then near: center = %#08x, want green", got)
	}

	// Near first (it writes depth), far second with
	// DepthGreater: 0.25 > 0.75 is false, so the far quad
	// does not overwrite.
	p2, _ := newRenderPVR(t)
	polyHeader(p2, ListOpaque, isp, tsp, 0, green)
	quad(p2, ListOpaque, 0.75, 0, 0, 0, 0, 0, 0, 32, 32, green)
	polyHeader(p2, ListOpaque, isp, tsp, 0, red)
	quad(p2, ListOpaque, 0.25, 0, 0, 0, 0, 0, 0, 32, 32, red)
	out2 := renderNow(t, p2)
	if got := px(out2, 16, 16); got != 0xFF00FF00 {
		t.Errorf("near then far: center = %#08x, want green", got)
	}
}

// Gate (d): translucent blending, half alpha white over the
// background, SrcAlpha / OneMinusSrcA.
func TestRenderTranslucentBlend(t *testing.T) {
	p, _ := newRenderPVR(t)
	isp := ispWord(DepthAlways, 0, false, false, false)
	tsp := tspWord(0, BlendSrcAlpha, BlendOneMinusSrcA, false, 8, 8)
	polyHeader(p, ListTrans, isp, tsp, 0, 0x80FFFFFF)
	quad(p, ListTrans, 0.5, 0, 0, 0, 0, 0, 0, 32, 32, 0x80FFFFFF)
	out := renderNow(t, p)
	// src alpha a = 128/255. Background channels are
	// (66, 130, 132)/255. out = a*1 + (1-a)*bg per channel:
	// R: 0.50196 + 0.49804*0.25882 = 0.63088, quantized
	// r5 = 20, expanded to 165.
	// G: 0.50196 + 0.49804*0.50980 = 0.75587, g6 = 48,
	// expanded to 195.
	// B: 0.50196 + 0.49804*0.51765 = 0.75979, b5 = 24,
	// expanded to 198.
	want := uint32(0xFFA5C3C6)
	if got := px(out, 16, 16); got != want {
		t.Errorf("blended center = %#08x, want %#08x", got, want)
	}
}

// Gate (e): punchthrough alpha test against ALPHA_REF 0x80.
func TestRenderPunchthrough(t *testing.T) {
	p, vram := newRenderPVR(t)
	p.Write32(regBase+RegTextControl, 0x80<<24) // ALPHA_REF = 128/255
	// 8x8 ARGB4444 texture: left half alpha nibble 4
	// (68/255, below the reference), red; right half
	// alpha nibble 12 (204/255, above), green.
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			val := uint16(0x4F00)
			if x >= 4 {
				val = 0xC0F0
			}
			off := testTex + uint32(twiddleIndex(x, y, 8, 8))*2
			binary.LittleEndian.PutUint16(vram[off:], val)
		}
	}
	isp := ispWord(DepthAlways, 0, true, false, false)
	tsp := tspWord(0, BlendOne, BlendZero, false, 8, 8)
	tcw := tcwWord(testTex, Tex4444, false, 0)
	polyHeader(p, ListPunch, isp, tsp, tcw, 0xFFFFFFFF)
	quad(p, ListPunch, 0.5, 0, 0, 1, 1, 0, 0, 32, 32, 0xFFFFFFFF)
	out := renderNow(t, p)
	if got := px(out, 6, 16); got != bgPresent {
		t.Errorf("low alpha half = %#08x, want background %#08x", got, bgPresent)
	}
	if got := px(out, 26, 16); got != 0xFF00FF00 {
		t.Errorf("high alpha half = %#08x, want green", got)
	}
}

// Gate (f): a modifier volume over the left half, then a
// two-volume translucent modifier quad. The second
// parameter set applies where the counter is odd.
func TestRenderModifierVolumeTwoVolume(t *testing.T) {
	p, _ := newRenderPVR(t)
	p.Write32(regBase+RegISPBackgndS, 0xFF000000) // black bg

	// Volume primitive in the opaque modifier list over
	// the left half. Its strip triangles are front
	// facing (signed area positive after strip parity),
	// so the counter goes up by 1 inside.
	ispVol := ispWord(DepthAlways, 0, false, false, false)
	packet(p, pcw(paraPolygon, ListOpaqueMod)|pcwShadow, ispVol, 0, 0, 0, 0, 0, 0)
	quad(p, ListOpaqueMod, 0.5, 0, 0, 0, 0, 0, 0, 16, 32, 0)

	// Two-volume fullscreen quad in the translucent
	// modifier list. Set 0 (even counter, outside) is
	// blue; set 1 (odd counter, inside) is red. Both sets
	// blend One / Zero so the source color lands as is.
	isp1 := ispWord(DepthAlways, 0, false, true, false)
	tsp1 := tspWord(0, BlendOne, BlendZero, false, 8, 8)
	packet(p, pcw(paraPolygon, ListTransMod)|pcwVolume,
		isp1, tsp1, 0, isp1, tsp1, 0, 0)
	twoVolQuad(p, ListTransMod)
	out := renderNow(t, p)
	if got := px(out, 8, 16); got != 0xFFFF0000 {
		t.Errorf("inside pixel = %#08x, want red (second set)", got)
	}
	if got := px(out, 24, 16); got != 0xFF0000FF {
		t.Errorf("outside pixel = %#08x, want blue (first set)", got)
	}
}

// twoVolQuad submits a fullscreen two-volume quad strip:
// first set color blue, second set color red, per vertex.
func twoVolQuad(p *PVR, list uint32) {
	const blue = 0xFF0000FF
	const red = 0xFFFF0000
	corners := [][4]float32{
		{0, 0, 0, 0}, {32, 0, 1, 0}, {0, 32, 0, 1}, {32, 32, 1, 1},
	}
	for i, c := range corners {
		w := pcw(paraVertex, list)
		if i == len(corners)-1 {
			w |= pcwEOS
		}
		// First 32 bytes: PCW, X, Y, Z, U, V, base, offset.
		packet(p, w, f32(c[0]), f32(c[1]), f32(0.5), f32(c[2]), f32(c[3]), blue, 0)
		// Second 32 bytes: U2, V2, base2, offset2, unused.
		packet(p, f32(c[2]), f32(c[3]), red, 0, 0, 0, 0, 0)
	}
}

// Gate (g): VQ texture. The declared size is 8x8 (the TSP
// size fields cannot express 4), and the quad samples only
// the 0 to 0.5 UV range, which is the top-left 4x4 texels.
// Every block index is 0, so every 2x2 block shows codebook
// entry 0, whose four texels are red, green, blue, white
// in block order (0,0), (1,0), (0,1), (1,1).
func TestRenderVQTexture(t *testing.T) {
	p, vram := newRenderPVR(t)
	entry := [4]uint16{0xF800, 0x07E0, 0x001F, 0xFFFF}
	for i, v := range entry {
		binary.LittleEndian.PutUint16(vram[testTex+i*2:], v)
	}
	// Index data follows the 2048-byte codebook; the
	// 8x8 texture has a 4x4 block grid, 16 bytes, all 0.
	isp := ispWord(DepthAlways, 0, true, false, false)
	tsp := tspWord(0, BlendOne, BlendZero, false, 8, 8)
	tcw := tcwWord(testTex, Tex565, true, 0)
	polyHeader(p, ListOpaque, isp, tsp, tcw, 0xFFFFFFFF)
	quad(p, ListOpaque, 0.5, 0, 0, 0.5, 0.5, 0, 0, 32, 32, 0xFFFFFFFF)
	out := renderNow(t, p)
	// Texel (tx,ty) center sits at pixel (tx*8+4, ty*8+4).
	for _, tc := range []struct {
		px, py int
		want   uint32
	}{
		{4, 4, 0xFFFF0000},   // texel (0,0) red
		{12, 4, 0xFF00FF00},  // texel (1,0) green
		{4, 12, 0xFF0000FF},  // texel (0,1) blue
		{12, 12, 0xFFFFFFFF}, // texel (1,1) white
		{20, 4, 0xFFFF0000},  // block (1,0), texel (0,0) again
		{28, 12, 0xFFFFFFFF}, // block (1,0), texel (1,1)
	} {
		if got := px(out, tc.px, tc.py); got != tc.want {
			t.Errorf("pixel (%d,%d) = %#08x, want %#08x", tc.px, tc.py, got, tc.want)
		}
	}
}

// Gate (h): 8-bit palette texture with 0565 palette RAM.
func TestRenderPaletteTexture(t *testing.T) {
	p, vram := newRenderPVR(t)
	p.Write32(regBase+RegPALRamCtrl, 1) // palette format 0565
	// Palette select 16 puts the Pal8 bank at index 256:
	// ((16 & 0x30) << 4) = 256. Texture bytes 1 and 2 then
	// resolve to entries 257 (red) and 258 (green).
	p.Write32(regBase+0x400+4*257, 0xF800)
	p.Write32(regBase+0x400+4*258, 0x07E0)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			b := byte(1)
			if x >= 4 {
				b = 2
			}
			vram[testTex+uint32(twiddleIndex(x, y, 8, 8))] = b
		}
	}
	isp := ispWord(DepthAlways, 0, true, false, false)
	tsp := tspWord(0, BlendOne, BlendZero, false, 8, 8)
	tcw := tcwWord(testTex, TexPal8, false, 16)
	polyHeader(p, ListOpaque, isp, tsp, tcw, 0xFFFFFFFF)
	quad(p, ListOpaque, 0.5, 0, 0, 1, 1, 0, 0, 32, 32, 0xFFFFFFFF)
	out := renderNow(t, p)
	if got := px(out, 6, 16); got != 0xFFFF0000 {
		t.Errorf("palette left = %#08x, want red", got)
	}
	if got := px(out, 26, 16); got != 0xFF00FF00 {
		t.Errorf("palette right = %#08x, want green", got)
	}
}

// f16 encodes a float32 with magnitude in the normal half
// range as IEEE half bits, for sprite UV words. Only the
// values 0 and 1 are used by the sprite gate.
func f16(f float32) uint32 {
	if f == 0 {
		return 0
	}
	bits := math.Float32bits(f)
	sign := (bits >> 31) & 1
	exp := int((bits>>23)&0xFF) - 127 + 15
	frac := (bits >> 13) & 0x3FF
	return sign<<15 | uint32(exp)<<10 | frac
}

// Sprite gate: a header of parameter type 5 plus the
// 64-byte sprite vertex parameter renders a solid quad.
func TestRenderSprite(t *testing.T) {
	p, _ := newRenderPVR(t)
	isp := ispWord(DepthAlways, 0, false, false, false)
	tsp := tspWord(0, BlendOne, BlendZero, false, 8, 8)
	packet(p, pcw(paraSprite, ListOpaque), isp, tsp, 0, 0, 0, 0, 0)
	// Corners A (4,4), B (28,4), C (4,28); the fourth
	// corner D = B + C - A = (28,28) is computed by the TA.
	// First half: unused word, A xyz, B xyz, C x.
	packet(p, 0, f32(4), f32(4), f32(0.5), f32(28), f32(4), f32(0.5), f32(4))
	// Second half: C y, C z, three UV words, base color
	// (solid magenta), offset color, unused word.
	uv := f16(0) | f16(0)<<16
	packet(p, f32(28), f32(0.5), uv, uv, uv, 0xFFFF00FF, 0, 0)
	out := renderNow(t, p)
	if got := px(out, 16, 16); got != 0xFFFF00FF {
		t.Errorf("sprite center = %#08x, want magenta", got)
	}
	if got := px(out, 26, 26); got != 0xFFFF00FF {
		t.Errorf("sprite near D corner = %#08x, want magenta", got)
	}
	if got := px(out, 1, 1); got != bgPresent {
		t.Errorf("outside sprite = %#08x, want background %#08x", got, bgPresent)
	}
}
