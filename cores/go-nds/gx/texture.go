package gx

// Texture fetch: the texture image space (four 128 KB slots,
// banks A-D assigned with VRAMCNT MST 3, the OFS field
// selecting the slot) and the texture palette space (up to
// six 16 KB slots: bank E with MST 3 covers slots 0-3, banks
// F and G with MST 3 cover slot (OFS bit 0) + (OFS bit 1)*4),
// decoded from the same VRAMCNT bytes the video package uses
// (GBATEK layout: MST bits 0-2, OFS bits 3-4, enable bit 7).
// The spaces are rebuilt when the VRAMCNT bytes change.

// rebuildTex rebuilds the texture spaces if VRAMCNT changed.
func (g *GX) rebuildTex() {
	var cur [9]byte
	copy(cur[:7], g.bus.IO9[0x240:0x247])
	cur[7] = g.bus.IO9[0x248]
	cur[8] = g.bus.IO9[0x249]
	if g.texValid && cur == g.texCache {
		return
	}
	g.texCache = cur
	g.texValid = true
	for i := range g.texImg {
		g.texImg[i] = 0
	}
	for i := range g.texPal {
		g.texPal[i] = 0
	}
	for bank := 0; bank < 9; bank++ {
		cnt := cur[bank]
		if cnt&0x80 == 0 || cnt&7 != 3 {
			continue
		}
		ofs := (cnt >> 3) & 3
		data := g.bus.VRAMBank(bank)
		switch bank {
		case 0, 1, 2, 3: // texture image slot OFS
			copy(g.texImg[uint32(ofs)*0x20000:], data)
		case 4: // E: texture palette slots 0-3
			copy(g.texPal[0:], data)
		case 5, 6: // F, G: texture palette slot (OFS.0) + (OFS.1)*4
			slot := uint32(ofs&1) + uint32(ofs>>1)*4
			copy(g.texPal[slot*0x4000:], data)
		}
	}
}

func (g *GX) texByte(addr uint32) uint8 {
	return g.texImg[addr&0x7FFFF]
}

func (g *GX) texHalf(addr uint32) uint16 {
	return uint16(g.texByte(addr)) | uint16(g.texByte(addr+1))<<8
}

func (g *GX) palHalf(addr uint32) uint16 {
	a := addr % 0x18000
	return uint16(g.texPal[a]) | uint16(g.texPal[(a+1)%0x18000])<<8
}

// texel is one decoded texture sample: RGB15 color and a
// 5-bit alpha.
type texel struct {
	rgb   uint16
	alpha uint8
	ok    bool // false: no texture (format 0 or mapping off)
}

// wrapCoord applies the wrap mode of one axis to a texel
// coordinate: clamp to the edge, repeat modulo the size, or
// flip (mirror every second repeat, which requires repeat;
// without repeat the flip bit is ignored and the coordinate
// clamps).
func wrapCoord(c, size int, repeat, flip bool) int {
	if repeat {
		if flip {
			m := c % (2 * size)
			if m < 0 {
				m += 2 * size
			}
			if m >= size {
				m = 2*size - 1 - m
			}
			return m
		}
		m := c % size
		if m < 0 {
			m += size
		}
		return m
	}
	if c < 0 {
		return 0
	}
	if c >= size {
		return size - 1
	}
	return c
}

// sampleTexture fetches the texel at integer texel
// coordinates (s, t) for a polygon with the given latched
// TEXIMAGE_PARAM and PLTT_BASE values.
func (g *GX) sampleTexture(tex, pltt uint32, s, t int) texel {
	format := (tex >> 26) & 7
	if format == 0 {
		return texel{}
	}
	w := 8 << ((tex >> 20) & 7)
	h := 8 << ((tex >> 23) & 7)
	s = wrapCoord(s, w, tex&0x10000 != 0, tex&0x40000 != 0)
	t = wrapCoord(t, h, tex&0x20000 != 0, tex&0x80000 != 0)
	base := (tex & 0xFFFF) * 8
	color0Trans := tex&(1<<29) != 0
	palBase := func() uint32 {
		if format == 2 {
			return (pltt & 0x1FFF) * 8
		}
		return (pltt & 0x1FFF) * 16
	}
	switch format {
	case 1, 6: // A3I5, A5I3: one byte per texel
		b := g.texByte(base + uint32(t*w+s))
		if format == 1 {
			a := uint32(b >> 5)
			return texel{rgb: g.palHalf(palBase() + uint32(b&31)*2),
				alpha: uint8(a*4 + a/2), ok: true}
		}
		return texel{rgb: g.palHalf(palBase() + uint32(b&7)*2),
			alpha: b >> 3, ok: true}
	case 2: // 4-color palette, 2bpp
		b := g.texByte(base + uint32(t*(w/4)+s/4))
		idx := uint32((b >> (uint(s&3) * 2)) & 3)
		return palTexel(g.palHalf(palBase()+uint32(idx)*2), idx, color0Trans)
	case 3: // 16-color palette, 4bpp
		b := g.texByte(base + uint32(t*(w/2)+s/2))
		idx := uint32((b >> (uint(s&1) * 4)) & 15)
		return palTexel(g.palHalf(palBase()+uint32(idx)*2), idx, color0Trans)
	case 4: // 256-color palette, 8bpp
		idx := g.texByte(base + uint32(t*w+s))
		return palTexel(g.palHalf(palBase()+uint32(idx)*2), uint32(idx), color0Trans)
	case 5: // 4x4 compressed
		return g.sampleCompressed(base, palBase(), w, s, t)
	case 7: // direct color
		v := g.texHalf(base + uint32(t*w+s)*2)
		a := uint8(0)
		if v&0x8000 != 0 {
			a = 31
		}
		return texel{rgb: v & 0x7FFF, alpha: a, ok: true}
	}
	return texel{}
}

// palTexel applies the color-0 transparency rule of the
// palette formats.
func palTexel(rgb uint16, idx uint32, color0Trans bool) texel {
	a := uint8(31)
	if idx == 0 && color0Trans {
		a = 0
	}
	return texel{rgb: rgb, alpha: a, ok: true}
}

// sampleCompressed fetches one texel of a 4x4 compressed
// texture. The 32-bit texel blocks live in texture slot 0 or
// 2; the 16-bit palette-index header for each block lives in
// slot 1 at half the block's in-slot offset (plus 0x10000 for
// slot 2), per GBATEK. Header bits 0-13 give the palette
// offset in 4-byte steps from the palette base, bits 14-15
// the interpolation mode.
func (g *GX) sampleCompressed(base, palBase uint32, w, s, t int) texel {
	blockIdx := uint32((t/4)*(w/4) + s/4)
	blockAddr := base + blockIdx*4
	word := uint32(g.texByte(blockAddr)) | uint32(g.texByte(blockAddr+1))<<8 |
		uint32(g.texByte(blockAddr+2))<<16 | uint32(g.texByte(blockAddr+3))<<24
	var hdrAddr uint32
	if blockAddr < 0x20000 {
		hdrAddr = 0x20000 + blockAddr/2
	} else {
		hdrAddr = 0x20000 + (blockAddr-0x40000)/2 + 0x10000
	}
	hdr := g.texHalf(hdrAddr)
	mode := hdr >> 14
	palAddr := palBase + uint32(hdr&0x3FFF)*4
	row := (word >> (uint(t&3) * 8)) & 0xFF
	idx := (row >> (uint(s&3) * 2)) & 3
	c0 := g.palHalf(palAddr)
	c1 := g.palHalf(palAddr + 2)
	switch mode {
	case 0, 2: // four palette colors
		return texel{rgb: g.palHalf(palAddr + uint32(idx)*2), alpha: 31, ok: true}
	case 1: // c0, c1, average, transparent
		switch idx {
		case 0:
			return texel{rgb: c0, alpha: 31, ok: true}
		case 1:
			return texel{rgb: c1, alpha: 31, ok: true}
		case 2:
			return texel{rgb: avg15(c0, c1, 1, 1, 2), alpha: 31, ok: true}
		}
		return texel{rgb: 0, alpha: 0, ok: true}
	case 3: // c0, c1, and the 5/8 and 3/8 interpolations
		switch idx {
		case 0:
			return texel{rgb: c0, alpha: 31, ok: true}
		case 1:
			return texel{rgb: c1, alpha: 31, ok: true}
		case 2:
			return texel{rgb: avg15(c0, c1, 5, 3, 8), alpha: 31, ok: true}
		}
		return texel{rgb: avg15(c0, c1, 3, 5, 8), alpha: 31, ok: true}
	}
	return texel{}
}

// avg15 blends two RGB15 colors per channel with integer
// weights over a power-of-two denominator (truncating).
func avg15(a, b uint16, wa, wb, den uint32) uint16 {
	ch := func(x, y uint32) uint32 { return (x*wa + y*wb) / den }
	return uint16(ch(uint32(a&31), uint32(b&31)) |
		ch(uint32((a>>5)&31), uint32((b>>5)&31))<<5 |
		ch(uint32((a>>10)&31), uint32((b>>10)&31))<<10)
}

// blendTexture combines a sampled texel with a vertex color
// per the polygon mode, in the 6-bit domain with GBATEK's
// formulas. Vertex alpha comes only from POLYGON_ATTR.
// Modes: 0 modulation, 1 decal, 2 toon or highlight (the
// DISP3DCNT shading select picks), 3 shadow (rendered as
// modulation in this module; true shadow volumes need the
// polygon ID buffer test, stated in the README).
func (g *GX) blendTexture(tx texel, v vertex, attr uint32, disp3d uint32) (uint8, uint8, uint8, uint8) {
	av := uint8((attr >> 16) & 31)
	if !tx.ok {
		return v.r, v.g, v.b, av
	}
	tr := expandGX(uint32(tx.rgb & 31))
	tg := expandGX(uint32((tx.rgb >> 5) & 31))
	tb := expandGX(uint32((tx.rgb >> 10) & 31))
	vr, vg, vb := uint32(v.r), uint32(v.g), uint32(v.b)
	mod := func(t6, v6 uint32) uint32 { return ((t6+1)*(v6+1) - 1) / 64 }
	mode := (attr >> 4) & 3
	switch mode {
	case 1: // decal
		at := uint32(tx.alpha)
		if at == 0 {
			return v.r, v.g, v.b, av
		}
		if at == 31 {
			return uint8(tr), uint8(tg), uint8(tb), av
		}
		a6 := 2*at + 1
		dec := func(t6, v6 uint32) uint32 { return (t6*a6 + v6*(63-a6)) / 64 }
		return uint8(dec(tr, vr)), uint8(dec(tg, vg)), uint8(dec(tb, vb)), av
	case 2: // toon / highlight
		tt := g.toonTable[v.r>>1]
		tr2 := expandGX(uint32(tt & 31))
		tg2 := expandGX(uint32((tt >> 5) & 31))
		tb2 := expandGX(uint32((tt >> 10) & 31))
		if disp3d&2 != 0 { // highlight: modulation plus table color
			return uint8(min63(mod(tr, vr) + tr2)),
				uint8(min63(mod(tg, vg) + tg2)),
				uint8(min63(mod(tb, vb) + tb2)),
				blendAlpha(tx.alpha, av)
		}
		return uint8(mod(tr, tr2)), uint8(mod(tg, tg2)), uint8(mod(tb, tb2)),
			blendAlpha(tx.alpha, av)
	}
	// Modulation (and the shadow fallback).
	return uint8(mod(tr, vr)), uint8(mod(tg, vg)), uint8(mod(tb, vb)),
		blendAlpha(tx.alpha, av)
}

// blendAlpha combines texel and vertex alpha in the 6-bit
// formula domain and returns the 5-bit result.
func blendAlpha(at, av uint8) uint8 {
	a6 := ((expandGX(uint32(at))+1)*(expandGX(uint32(av))+1) - 1) / 64
	return uint8(a6 >> 1)
}

func min63(v uint32) uint32 {
	if v > 63 {
		return 63
	}
	return v
}
