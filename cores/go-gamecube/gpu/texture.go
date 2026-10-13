package gpu

import (
	"math"
)

// Texture stage 0 registers live in the first texture image
// group at bpTexGroup0 (0x80), stride 8 per group:
//
//   +0 SETMODE0: wrap S bits 0-1, wrap T bits 2-3, mag
//      filter bit 4 (0 nearest, 1 linear), min filter bits
//      5-7 (GX filter codes: 0 near, 1 linear, 2 near-mip-
//      near, 3 linear-mip-near, 4 near-mip-linear, 5
//      linear-mip-linear; mip levels beyond 0 are not
//      sampled in this slice, so the base filter of each
//      code applies)
//   +1 SETMODE1: LOD parameters, stored only
//   +2 SETIMAGE0: width-1 bits 0-9, height-1 bits 10-19,
//      format bits 20-23
//   +3 SETIMAGE1: TMEM preload descriptor, stored only (this
//      renderer samples image data directly from memory, so
//      the preload is not needed for correctness)
//   +4 SETIMAGE2: stored only
//   +5 SETIMAGE3: image base: MEM1 byte address = value << 5
//   +6 SETTLUT: TLUT TMEM byte offset = (value & 0x3FF) << 5,
//      TLUT format bits 10-11 (0 IA8, 1 RGB565, 2 RGB5A3)
//
// Texture formats: 0 I4, 1 I8, 2 IA4, 3 IA8, 4 RGB565,
// 5 RGB5A3, 6 RGBA8 (the 32-bit two-plane layout), 8 CI4,
// 9 CI8, 14 CMP (the documented S3TC-style 4x4 block format).
// Wrap modes: 0 clamp, 1 repeat, 2 mirror.

const (
	tfI4    = 0
	tfI8    = 1
	tfIA4   = 2
	tfIA8   = 3
	tfRGB   = 4 // RGB565
	tfRGB5A = 5
	tfRGBA  = 6
	tfCI4   = 8
	tfCI8   = 9
	tfCMP   = 14
)

type texInfo struct {
	enabled bool
	w, h    int
	format  uint32
	addr    uint32 // MEM1 byte address of the image
	wrapS   uint32
	wrapT   uint32
	magLin  bool
	minLin  bool
	tlutOff uint32 // TMEM byte offset of the palette
	tlutFmt uint32
}

func (g *GPU) stage0Texture() texInfo {
	var ti texInfo
	mode0 := g.bp[bpTexGroup0+0]
	img0 := g.bp[bpTexGroup0+2]
	ti.wrapS = mode0 & 3
	ti.wrapT = (mode0 >> 2) & 3
	ti.magLin = mode0&(1<<4) != 0
	switch (mode0 >> 5) & 7 {
	case 1, 3, 5:
		ti.minLin = true
	}
	ti.w = int(img0&0x3FF) + 1
	ti.h = int((img0>>10)&0x3FF) + 1
	ti.format = (img0 >> 20) & 0xF
	ti.addr = (g.bp[bpTexGroup0+5] & 0xFFFFFF) << 5
	tlut := g.bp[bpTexGroup0+6]
	ti.tlutOff = (tlut & 0x3FF) << 5
	ti.tlutFmt = (tlut >> 10) & 3
	ti.enabled = ti.w > 1 || ti.h > 1 || g.bp[bpTexGroup0+5] != 0
	if img0 == 0 {
		ti.enabled = false
	}
	return ti
}

// tileGeom returns the tile width, tile height and bytes
// per tile for a texture format.
func tileGeom(format uint32) (tw, th, bytes int) {
	switch format {
	case tfI4, tfCI4:
		return 8, 8, 32
	case tfI8, tfIA4, tfCI8:
		return 8, 4, 32
	case tfCMP:
		return 8, 8, 32
	default: // IA8, RGB565, RGB5A3, RGBA8 (64 bytes)
		if format == tfRGBA {
			return 4, 4, 64
		}
		return 4, 4, 32
	}
}

func (g *GPU) texByte(addr uint32) uint8 {
	if g.mem == nil {
		return 0
	}
	return g.mem.Read8(addr)
}

func (g *GPU) texU16(addr uint32) uint16 {
	if g.mem == nil {
		return 0
	}
	return g.mem.Read16(addr)
}

// texelAddress computes the byte address of texel (x, y)
// for the non-CMP formats, plus the sub-texel position used
// by the packed formats.
func (ti texInfo) texelAddress(x, y int) uint32 {
	tw, th, tb := tileGeom(ti.format)
	tilesPerRow := (ti.w + tw - 1) / tw
	tile := (y/th)*tilesPerRow + (x / tw)
	within := (y%th)*tw + (x % tw)
	base := ti.addr + uint32(tile*tb)
	switch ti.format {
	case tfI4, tfCI4, tfIA4:
		return base + uint32(within/2)
	case tfRGBA:
		// Two planes of 32 bytes: alpha/red first, then
		// green/blue, two bytes per texel in each plane.
		return base + uint32(within*2)
	default:
		bpp := 1
		if ti.format == tfIA8 || ti.format == tfRGB || ti.format == tfRGB5A {
			bpp = 2
		}
		return base + uint32(within*bpp)
	}
}

// tlutColor decodes palette entry idx through the TMEM
// palette.
func (g *GPU) tlutColor(ti texInfo, idx uint32) color8 {
	off := ti.tlutOff + idx*2
	if off+1 >= TMEMSize {
		return color8{}
	}
	w := uint16(g.tmem[off])<<8 | uint16(g.tmem[off+1])
	switch ti.tlutFmt {
	case 1: // RGB565
		return decodeColor(0, false, []byte{byte(w >> 8), byte(w)})
	case 2: // RGB5A3
		return decodeRGB5A3(w)
	default: // IA8: intensity in the high byte, alpha low
		return color8{byte(w >> 8), byte(w >> 8), byte(w >> 8), byte(w)}
	}
}

func decodeRGB5A3(w uint16) color8 {
	if w&0x8000 != 0 {
		return color8{expand5(uint8(w >> 10)), expand5(uint8((w >> 5) & 0x1F)), expand5(uint8(w & 0x1F)), 255}
	}
	return color8{
		expand4(uint8((w >> 8) & 0xF)),
		expand4(uint8((w >> 4) & 0xF)),
		expand4(uint8(w & 0xF)),
		expand3(uint8((w >> 12) & 7)),
	}
}

// sampleTexel fetches the raw (unfiltered) texel at integer
// texel coordinates, which must already be inside the image.
func (g *GPU) sampleTexel(ti texInfo, x, y int) color8 {
	switch ti.format {
	case tfI4:
		b := g.texByte(ti.texelAddress(x, y))
		v := b >> 4
		if x%2 == 1 {
			v = b & 0xF
		}
		i := expand4(v)
		return color8{i, i, i, 255}
	case tfI8:
		i := g.texByte(ti.texelAddress(x, y))
		return color8{i, i, i, 255}
	case tfIA4:
		b := g.texByte(ti.texelAddress(x, y))
		v := b >> 4
		if x%2 == 1 {
			v = b & 0xF
		}
		i := expand3(v >> 1)
		a := uint8(255)
		if v&1 != 0 {
			a = 255
		} else {
			a = 0
		}
		return color8{i, i, i, a}
	case tfIA8:
		w := g.texU16(ti.texelAddress(x, y))
		return color8{byte(w >> 8), byte(w >> 8), byte(w >> 8), byte(w)}
	case tfRGB:
		w := g.texU16(ti.texelAddress(x, y))
		return decodeColor(0, false, []byte{byte(w >> 8), byte(w)})
	case tfRGB5A:
		return decodeRGB5A3(g.texU16(ti.texelAddress(x, y)))
	case tfRGBA:
		base := ti.texelAddress(x, y)
		ar := g.texU16(base)
		gb := g.texU16(base + 32)
		return color8{byte(ar), byte(gb >> 8), byte(gb), byte(ar >> 8)}
	case tfCI4:
		b := g.texByte(ti.texelAddress(x, y))
		idx := uint32(b >> 4)
		if x%2 == 1 {
			idx = uint32(b & 0xF)
		}
		return g.tlutColor(ti, idx)
	case tfCI8:
		idx := uint32(g.texByte(ti.texelAddress(x, y)))
		return g.tlutColor(ti, idx)
	case tfCMP:
		return g.sampleCMP(ti, x, y)
	}
	return color8{}
}

// sampleCMP decompresses one texel of a CMP texture. An
// 8x8 tile holds four 4x4 sub-blocks of 8 bytes each, in
// row-major sub-block order. Each sub-block stores two
// RGB565 endpoints and four rows of four 2-bit indices
// (most significant bits first). When the first endpoint is
// greater than the second, the palette is the two endpoints
// plus their 1/3 and 2/3 interpolations; otherwise it is
// the two endpoints, their midpoint, and transparent black.
func (g *GPU) sampleCMP(ti texInfo, x, y int) color8 {
	tw, _, tb := tileGeom(tfCMP)
	tilesPerRow := (ti.w + tw - 1) / tw
	tile := (y/8)*tilesPerRow + (x / 8)
	subIdx := ((y%8)/4)*2 + ((x % 8) / 4)
	base := ti.addr + uint32(tile*tb+subIdx*8)
	c0 := g.texU16(base)
	c1 := g.texU16(base + 2)
	row := y % 4
	bits := g.texByte(base + 4 + uint32(row))
	shift := uint(6 - 2*(x%4))
	idx := (bits >> shift) & 3
	col := func(w uint16) (int, int, int) {
		return int(expand5(uint8(w >> 11))), int(expand6(uint8((w >> 5) & 0x3F))), int(expand5(uint8(w & 0x1F)))
	}
	r0, g0, b0 := col(c0)
	r1, g1, b1 := col(c1)
	mix := func(a, b int, num, den int) uint8 {
		return uint8((a*(den-num) + b*num + den/2) / den)
	}
	switch idx {
	case 0:
		return color8{uint8(r0), uint8(g0), uint8(b0), 255}
	case 1:
		return color8{uint8(r1), uint8(g1), uint8(b1), 255}
	case 2:
		if c0 > c1 {
			return color8{mix(r0, r1, 1, 3), mix(g0, g1, 1, 3), mix(b0, b1, 1, 3), 255}
		}
		return color8{mix(r0, r1, 1, 2), mix(g0, g1, 1, 2), mix(b0, b1, 1, 2), 255}
	default:
		if c0 > c1 {
			return color8{mix(r0, r1, 2, 3), mix(g0, g1, 2, 3), mix(b0, b1, 2, 3), 255}
		}
		return color8{0, 0, 0, 0}
	}
}

func wrapCoord(v, dim int, mode uint32) int {
	if dim <= 0 {
		return 0
	}
	switch mode {
	case 1: // repeat
		v %= dim
		if v < 0 {
			v += dim
		}
		return v
	case 2: // mirror
		v %= 2 * dim
		if v < 0 {
			v += 2 * dim
		}
		if v >= dim {
			v = 2*dim - 1 - v
		}
		return v
	default: // clamp
		if v < 0 {
			return 0
		}
		if v >= dim {
			return dim - 1
		}
		return v
	}
}

// sampleTexture samples stage 0 at normalized coordinates
// (s, t) with the mode0 wrap and filter settings. Level 0
// only. Linear filtering uses the documented texel-center
// convention: the sample point in texel space is
// (s*w - 0.5, t*h - 0.5), and the four surrounding texels
// blend by the fractional offsets.
func (g *GPU) sampleTexture(ti texInfo, s, t float64) color8 {
	linear := ti.magLin // magnification applies to level 0 here
	if !linear {
		x := wrapCoord(int(math.Floor(s*float64(ti.w))), ti.w, ti.wrapS)
		y := wrapCoord(int(math.Floor(t*float64(ti.h))), ti.h, ti.wrapT)
		return g.sampleTexel(ti, x, y)
	}
	fx := s*float64(ti.w) - 0.5
	fy := t*float64(ti.h) - 0.5
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	ax := fx - float64(x0)
	ay := fy - float64(y0)
	var acc [4]float64
	for j := 0; j < 2; j++ {
		for i := 0; i < 2; i++ {
			wgt := (float64(i)*ax + float64(1-i)*(1-ax)) * (float64(j)*ay + float64(1-j)*(1-ay))
			c := g.sampleTexel(ti,
				wrapCoord(x0+i, ti.w, ti.wrapS),
				wrapCoord(y0+j, ti.h, ti.wrapT))
			acc[0] += float64(c.r) * wgt
			acc[1] += float64(c.g) * wgt
			acc[2] += float64(c.b) * wgt
			acc[3] += float64(c.a) * wgt
		}
	}
	return color8{
		uint8(acc[0] + 0.5), uint8(acc[1] + 0.5),
		uint8(acc[2] + 0.5), uint8(acc[3] + 0.5),
	}
}
