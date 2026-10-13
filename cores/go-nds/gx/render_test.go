package gx_test

import (
	"testing"
)

func (r *rig) line(x, y int) uint32 {
	var dst [256]uint32
	r.g.Line3D(y, &dst)
	return dst[x]
}

// prep installs the power bits Line3D needs, the identity
// transform, and the far clear depth real software uses.
func (r *rig) prep() {
	r.b.IO9[0x304] = 0x0F // LCD + engine A + 3D render + geometry
	r.g.WriteIO(0x354, 4, 0x7FFF)
	r.identity()
}

// vtx emits one VTX_16 vertex (12-frac coordinates).
func (r *rig) vtx(x, y, z int) {
	r.cmd(0x23, uint32(uint16(x))|uint32(uint16(y))<<16, uint32(uint16(z)))
}

// vtxST emits a texcoord then a vertex at z = 0.
func (r *rig) vtxST(x, y, s, t int) {
	r.cmd(0x22, uint32(uint16(s))|uint32(uint16(t))<<16)
	r.vtx(x, y, 0)
}

// quad emits a full quad primitive (NDC corners, z fixed).
func (r *rig) quad(x0, y0, x1, y1, z int) {
	r.cmd(0x40, 1) // BEGIN_VTXS quads
	r.vtx(x0, y0, z)
	r.vtx(x1, y0, z)
	r.vtx(x1, y1, z)
	r.vtx(x0, y1, z)
}

const attrBothOpaque = 31<<16 | 0xC0

func TestRedTriangleCenter(t *testing.T) {
	r := newRig()
	r.prep()
	r.cmd(0x29, attrBothOpaque)
	r.cmd(0x20, 0x001F) // COLOR red
	r.cmd(0x40, 0)      // triangles
	r.vtx(-0x800, -0x800, 0)
	r.vtx(0x800, -0x800, 0)
	r.vtx(0, 0x800, 0)
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(128, 96); got != 63|31<<18 {
		t.Fatalf("center = %08x want red opaque", got)
	}
	if got := r.line(2, 2); got != 0 {
		t.Fatalf("corner = %08x want transparent", got)
	}
}

func TestDepthNearerWins(t *testing.T) {
	r := newRig()
	r.prep()
	r.cmd(0x29, attrBothOpaque)
	r.cmd(0x20, 0x001F) // red, far
	r.quad(-0xC00, -0xC00, 0xC00, 0xC00, 0x800)
	r.cmd(0x20, 0x03E0) // green, near
	r.quad(-0x600, -0x600, 0x600, 0x600, -0x800)
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(128, 96); got != 63<<6|31<<18 {
		t.Fatalf("overlap = %08x want green", got)
	}
	if got := r.line(204, 38); got != 63|31<<18 {
		t.Fatalf("far-only = %08x want red", got)
	}
}

func TestEqualDepthFlag(t *testing.T) {
	draw := func(equal bool) uint32 {
		r := newRig()
		r.prep()
		r.cmd(0x29, attrBothOpaque)
		r.cmd(0x20, 0x001F)
		r.quad(-0x800, -0x800, 0x800, 0x800, 0)
		attr := uint32(attrBothOpaque)
		if equal {
			attr |= 1 << 14
		}
		r.cmd(0x29, attr)
		r.cmd(0x20, 0x03E0)
		r.quad(-0x800, -0x800, 0x800, 0x800, 0)
		r.cmd(0x50, 0)
		r.g.OnVBlank()
		return r.line(128, 96)
	}
	if got := draw(false); got != 63|31<<18 {
		t.Fatalf("plain equal depth = %08x want red (second rejected)", got)
	}
	if got := draw(true); got != 63<<6|31<<18 {
		t.Fatalf("equal-flag depth = %08x want green (second drawn)", got)
	}
}

// texRig assigns bank A as texture slot 0 and bank E as the
// texture palette, enables texture mapping, and sets a white
// vertex color.
func (r *rig) texRig() {
	r.b.IO9[0x240] = 0x83 // bank A: texture image slot 0
	r.b.IO9[0x244] = 0x83 // bank E: texture palette slots 0-3
	r.b.IO9[0x60] = 0x01  // DISP3DCNT: texture mapping on
	r.cmd(0x29, attrBothOpaque)
	r.cmd(0x20, 0x7FFF) // white
}

// texQuad draws a full-screen quad mapping the whole texture.
func (r *rig) texQuad() {
	r.cmd(0x40, 1)
	r.vtxST(-0x1000, -0x1000, 0, 128)
	r.vtxST(0x1000, -0x1000, 128, 128)
	r.vtxST(0x1000, 0x1000, 128, 0)
	r.vtxST(-0x1000, 0x1000, 0, 0)
}

func TestTexturedQuadPalette256(t *testing.T) {
	r := newRig()
	r.prep()
	r.texRig()
	a := r.b.VRAMBank(0)
	for i := 0; i < 64; i++ {
		a[i] = 1
	}
	e := r.b.VRAMBank(4)
	e[2], e[3] = 0x1F, 0x00 // palette entry 1 = red
	r.cmd(0x2A, 4<<26)      // TEXIMAGE_PARAM: 8x8, 256-color palette
	r.cmd(0x2B, 0)          // PLTT_BASE 0
	r.texQuad()
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(128, 96); got != 63|31<<18 {
		t.Fatalf("textured center = %08x want red", got)
	}
}

func TestA3I5BlendOverClear(t *testing.T) {
	r := newRig()
	r.prep()
	r.texRig()
	r.b.IO9[0x60] = 0x09 // + alpha blending enable
	r.g.WriteIO(0x350, 4, 0x7C00|31<<16)
	a := r.b.VRAMBank(0)
	for i := 0; i < 64; i++ {
		a[i] = 0x81 // index 1, alpha 3-bit 4 (5-bit 18)
	}
	e := r.b.VRAMBank(4)
	e[2], e[3] = 0x1F, 0x00
	r.cmd(0x2A, 1<<26) // A3I5
	r.cmd(0x2B, 0)
	r.texQuad()
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	got := r.line(128, 96)
	// Blend: R (63*19 + 0*13)/32 = 37, B (0*19 + 63*13)/32
	// = 25, alpha max(18, 31) = 31.
	if got != 37|25<<12|31<<18 {
		t.Fatalf("A3I5 blend = %08x want (37,0,25) opaque", got)
	}
}

func TestCompressedTextureBlock(t *testing.T) {
	r := newRig()
	r.prep()
	r.texRig()
	r.b.IO9[0x241] = 0x8B // bank B: texture image slot 1 (index data)
	a := r.b.VRAMBank(0)
	// Block (0,0): every row texels 0,1,2,3 = byte 0xE4.
	for i := 0; i < 4; i++ {
		a[i] = 0xE4
	}
	// Index header for block (0,0) sits at slot-1 offset 0
	// (bank B start): palette offset 0, mode 0. It is
	// already zero. Palette entries: black, red, green,
	// blue.
	e := r.b.VRAMBank(4)
	e[2], e[3] = 0x1F, 0x00
	e[4], e[5] = 0xE0, 0x03
	e[6], e[7] = 0x00, 0x7C
	r.cmd(0x2A, 5<<26) // 4x4 compressed
	r.cmd(0x2B, 0)
	r.texQuad()
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	// Block (0,0) covers screen x 0..127 (texels 0..3 of
	// 8), rows near the top (T = 0 at the top).
	if got := r.line(16, 12); got != 31<<18 {
		t.Fatalf("texel 0 = %08x want black opaque", got)
	}
	if got := r.line(48, 12); got != 63|31<<18 {
		t.Fatalf("texel 1 = %08x want red", got)
	}
	if got := r.line(80, 12); got != 63<<6|31<<18 {
		t.Fatalf("texel 2 = %08x want green", got)
	}
	if got := r.line(112, 12); got != 63<<12|31<<18 {
		t.Fatalf("texel 3 = %08x want blue", got)
	}
}

func TestFogFullDensity(t *testing.T) {
	r := newRig()
	r.prep()
	r.b.IO9[0x60] = 0x80 // DISP3DCNT: fog master enable
	r.g.WriteIO(0x358, 4, 0x001F|31<<16)
	for i := 0; i < 8; i++ {
		r.g.WriteIO(0x360+uint32(i*4), 4, 0x7F7F7F7F)
	}
	// Clear color black, opaque, with its fog flag set.
	r.g.WriteIO(0x350, 4, 0x8000|31<<16)
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(10, 10); got != 63|31<<18 {
		t.Fatalf("fogged rear pixel = %08x want fog red", got)
	}
}

func TestFogFlagOffStaysClear(t *testing.T) {
	r := newRig()
	r.prep()
	r.b.IO9[0x60] = 0x80
	r.g.WriteIO(0x358, 4, 0x001F|31<<16)
	for i := 0; i < 8; i++ {
		r.g.WriteIO(0x360+uint32(i*4), 4, 0x7F7F7F7F)
	}
	// Clear color black opaque, fog flag clear.
	r.g.WriteIO(0x350, 4, 31<<16)
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(10, 10); got != 31<<18 {
		t.Fatalf("unflagged rear pixel = %08x want plain black", got)
	}
}

func TestWireframeEdgesOnly(t *testing.T) {
	r := newRig()
	r.prep()
	r.cmd(0x29, 0xC0) // alpha 0: wireframe, both faces
	r.cmd(0x20, 0x001F)
	r.quad(-0xC00, -0xC00, 0xC00, 0xC00, 0)
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(128, 96); got != 0 {
		t.Fatalf("wireframe interior = %08x want transparent", got)
	}
	// Left edge at NDC x -0.75: screen x 32.
	if got := r.line(32, 96); got != 63|31<<18 {
		t.Fatalf("wireframe left edge = %08x want red", got)
	}
}

func TestToonShading(t *testing.T) {
	r := newRig()
	r.prep()
	r.texRig()
	a := r.b.VRAMBank(0)
	for i := 0; i < 64; i++ {
		a[i] = 1
	}
	e := r.b.VRAMBank(4)
	e[2], e[3] = 0xFF, 0x7F // palette entry 1 = white
	// Toon table entry 31 (indexed by the vertex red) =
	// green.
	r.g.WriteIO(0x380+62, 2, 0x03E0)
	// Polygon shading mode 2 (toon/highlight), vertex red 31.
	r.cmd(0x29, attrBothOpaque|2<<4)
	r.cmd(0x20, 0x001F)
	r.cmd(0x2A, 4<<26)
	r.cmd(0x2B, 0)
	r.texQuad()
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(128, 96); got != 63<<6|31<<18 {
		t.Fatalf("toon pixel = %08x want green from table", got)
	}
}

func TestClearImageBitmap(t *testing.T) {
	r := newRig()
	r.prep()
	r.texRig()                       // banks A (slot 0) and E assigned; slots follow
	r.b.IO9[0x242] = 0x80 | 3 | 2<<3 // bank C: texture slot 2
	r.b.IO9[0x243] = 0x80 | 3 | 3<<3 // bank D: texture slot 3
	r.b.IO9[0x60] = 0x01             // texture on
	r.b.IO9[0x61] = 0x40             // DISP3DCNT bit 14: bitmap rear plane
	// Color bitmap entry (0,0): red with alpha bit set.
	c := r.b.VRAMBank(2)
	c[0], c[1] = 0x1F, 0x80
	// Depth bitmap entry (0,0): 0x7FFF (far) is fine.
	d := r.b.VRAMBank(3)
	d[0], d[1] = 0xFF, 0x7F
	r.cmd(0x50, 0)
	r.g.OnVBlank()
	if got := r.line(0, 0); got != 63|31<<18 {
		t.Fatalf("clear image pixel = %08x want red", got)
	}
}
