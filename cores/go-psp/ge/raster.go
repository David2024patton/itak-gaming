package ge

import "math"

// This file holds the fixed function pixel path: pixel format
// conversion, vertex fetching and transformation, primitive assembly,
// rasterization, texturing, and the fragment pipeline.
//
// Loud documentation note: triangle attribute interpolation is plain
// linear interpolation in screen space at pixel centers. There is NO
// perspective correction in this slice, and NO backface culling
// (CULLMODE is stored only).

// -----------------------------------------------------------------------------
// Pixel formats
// -----------------------------------------------------------------------------

// rgba8 is one decoded pixel, 8 bits per channel.
type rgba8 struct{ r, g, b, a uint8 }

// fcolor is a float color with channels in the 0..255 range, used for
// interpolation and blending before the final rounding at write time.
type fcolor struct{ r, g, b, a float64 }

func (c rgba8) f() fcolor {
	return fcolor{float64(c.r), float64(c.g), float64(c.b), float64(c.a)}
}

func (c fcolor) bytes() rgba8 {
	return rgba8{roundByte(c.r), roundByte(c.g), roundByte(c.b), roundByte(c.a)}
}

// roundByte rounds to nearest and clamps to [0,255].
func roundByte(v float64) uint8 {
	v = math.Floor(v + 0.5)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// bytesPerPixel for a pixel format code (0=5650, 1=5551, 2=4444, 3=8888).
func bytesPerPixel(format int) int {
	if format == 3 {
		return 4
	}
	return 2
}

// unpack16 decodes a 16 bit pixel in formats 0 (5650), 1 (5551), or
// 2 (4444). Bit layouts are documented in the package spec: red sits in
// the low bits.
func unpack16(v uint16, format int) rgba8 {
	switch format {
	case 0: // 5650
		r5 := uint32(v & 0x1F)
		g6 := uint32((v >> 5) & 0x3F)
		b5 := uint32((v >> 11) & 0x1F)
		return rgba8{
			uint8((r5 << 3) | (r5 >> 2)),
			uint8((g6 << 2) | (g6 >> 4)),
			uint8((b5 << 3) | (b5 >> 2)),
			255,
		}
	case 1: // 5551
		r5 := uint32(v & 0x1F)
		g5 := uint32((v >> 5) & 0x1F)
		b5 := uint32((v >> 10) & 0x1F)
		a := uint8(0)
		if v&0x8000 != 0 {
			a = 255
		}
		return rgba8{
			uint8((r5 << 3) | (r5 >> 2)),
			uint8((g5 << 3) | (g5 >> 2)),
			uint8((b5 << 3) | (b5 >> 2)),
			a,
		}
	default: // 4444, nibble n expands to n<<4 | n
		r := uint32(v & 0xF)
		gr := uint32((v >> 4) & 0xF)
		b := uint32((v >> 8) & 0xF)
		a := uint32((v >> 12) & 0xF)
		return rgba8{
			uint8(r<<4 | r), uint8(gr<<4 | gr),
			uint8(b<<4 | b), uint8(a<<4 | a),
		}
	}
}

// unpack8888 decodes a 32 bit pixel: R in bits 7:0, G 15:8, B 23:16,
// A 31:24.
func unpack8888(v uint32) rgba8 {
	return rgba8{uint8(v), uint8(v >> 8), uint8(v >> 16), uint8(v >> 24)}
}

// packPixel converts RGBA8 down to a pixel format by truncating high
// bits (5 bit fields take r>>3, 6 bit fields g>>2, 4444 takes v>>4, and
// 5551 alpha is a >= 128).
func packPixel(c rgba8, format int) uint32 {
	switch format {
	case 0: // 5650
		return uint32(c.r>>3) | uint32(c.g>>2)<<5 | uint32(c.b>>3)<<11
	case 1: // 5551
		v := uint32(c.r>>3) | uint32(c.g>>3)<<5 | uint32(c.b>>3)<<10
		if c.a >= 128 {
			v |= 1 << 15
		}
		return v
	case 2: // 4444
		return uint32(c.r>>4) | uint32(c.g>>4)<<4 | uint32(c.b>>4)<<8 | uint32(c.a>>4)<<12
	default: // 8888
		return uint32(c.r) | uint32(c.g)<<8 | uint32(c.b)<<16 | uint32(c.a)<<24
	}
}

// readPixelAt reads one pixel in the given format from a buffer whose
// rows are width pixels apart. Out of range reads return zero.
func (g *GE) readPixelAt(addr uint32, width, format, x, y int) rgba8 {
	if g.mem == nil || width <= 0 || x < 0 || y < 0 || x >= width {
		return rgba8{}
	}
	off := addr + uint32((y*width+x)*bytesPerPixel(format))
	if format == 3 {
		return unpack8888(g.mem.Read32(off))
	}
	return unpack16(g.mem.Read16(off), format)
}

// writePixelAt packs and stores one pixel.
func (g *GE) writePixelAt(addr uint32, width, format, x, y int, c rgba8) {
	if g.mem == nil || width <= 0 || x < 0 || y < 0 || x >= width {
		return
	}
	off := addr + uint32((y*width+x)*bytesPerPixel(format))
	if format == 3 {
		g.mem.Write32(off, packPixel(c, format))
	} else {
		g.mem.Write16(off, uint16(packPixel(c, format)))
	}
}

func (g *GE) readDrawPixel(x, y int) rgba8 {
	return g.readPixelAt(g.fbAddr, g.fbWidth(), g.pixelFormat(), x, y)
}

// readDepth reads the 16 bit depth value at a pixel.
func (g *GE) readDepth(x, y int) uint16 {
	if g.mem == nil {
		return 0
	}
	return g.mem.Read16(g.zAddr + uint32((y*g.zWidth()+x)*2))
}

// writeDepth stores a 16 bit depth value at a pixel.
func (g *GE) writeDepth(x, y int, v uint16) {
	if g.mem == nil {
		return
	}
	g.mem.Write16(g.zAddr+uint32((y*g.zWidth()+x)*2), v)
}

// -----------------------------------------------------------------------------
// Register derived draw state
// -----------------------------------------------------------------------------

func (g *GE) fbWidth() int {
	w := int(g.regs[opFRAMEBUFWIDTH] & 0xFFFF)
	if w == 0 {
		return 512
	}
	return w
}

func (g *GE) zWidth() int {
	w := int(g.regs[0x9F] & 0xFFFF)
	if w == 0 {
		return g.fbWidth()
	}
	return w
}

func (g *GE) pixelFormat() int { return int(g.regs[0xE8] & 3) }

// scissorRect returns the effective scissor rectangle, inclusive on
// both ends. A SCISSOR2 of (0,0) counts as never set and falls back to
// the full framebuffer width and the 512 row limit.
func (g *GE) scissorRect() (x1, y1, x2, y2 int) {
	a := g.regs[0xD0]
	b := g.regs[0xD1]
	x1, y1 = int(a&1023), int((a>>10)&1023)
	x2, y2 = int(b&1023), int((b>>10)&1023)
	if x2 == 0 && y2 == 0 {
		x2 = g.fbWidth() - 1
		if x2 < 0 {
			x2 = 0
		}
		y2 = fbMaxHeight - 1
	}
	return
}

// regionRect returns the effective REGION rectangle, inclusive. A
// REGION2 of (0,0) counts as never set and falls back to the scissor
// rectangle, which itself falls back to (fbWidth-1, 511).
func (g *GE) regionRect() (x1, y1, x2, y2 int) {
	a := g.regs[0x15]
	b := g.regs[0x16]
	x1, y1 = int(a&1023), int((a>>10)&1023)
	x2, y2 = int(b&1023), int((b>>10)&1023)
	if x2 == 0 && y2 == 0 {
		_, _, x2, y2 = g.scissorRect()
	}
	return
}

// stencilAt returns the stencil plane, allocating it on first use at
// fbWidth*512 bytes as the spec fixes it.
func (g *GE) stencilAt() []byte {
	if g.stencil == nil {
		size := g.fbWidth() * fbMaxHeight
		if size <= 0 {
			size = fbMaxHeight * fbMaxHeight
		}
		g.stencil = make([]byte, size)
	}
	return g.stencil
}

func (g *GE) stencilIndex(x, y int) (int, bool) {
	if x < 0 || y < 0 || x >= g.fbWidth() || y >= fbMaxHeight {
		return 0, false
	}
	idx := y*g.fbWidth() + x
	if idx >= len(g.stencilAt()) {
		return 0, false
	}
	return idx, true
}

// -----------------------------------------------------------------------------
// Compare, stencil ops, blend, logic
// -----------------------------------------------------------------------------

// comparePass applies the shared compare function set: 0 Never,
// 1 Always, 2 Equal, 3 NotEqual, 4 Less, 5 LessEqual, 6 Greater,
// 7 GreaterEqual. It answers whether "a compared to b" passes.
func comparePass(fn, a, b int) bool {
	switch fn & 7 {
	case 0:
		return false
	case 1:
		return true
	case 2:
		return a == b
	case 3:
		return a != b
	case 4:
		return a < b
	case 5:
		return a <= b
	case 6:
		return a > b
	case 7:
		return a >= b
	}
	return false
}

// applyStencilOp computes the new stencil value for op:
// 0 Keep, 1 Zero, 2 Replace (with ref), 3 Invert, 4 Increment
// saturating, 5 Decrement saturating, 6 Increment wrapping,
// 7 Decrement wrapping.
func applyStencilOp(op, stored, ref int) int {
	switch op & 15 {
	case 0:
		return stored
	case 1:
		return 0
	case 2:
		return ref & 0xFF
	case 3:
		return (^stored) & 0xFF
	case 4:
		if stored >= 255 {
			return 255
		}
		return stored + 1
	case 5:
		if stored <= 0 {
			return 0
		}
		return stored - 1
	case 6:
		return (stored + 1) & 0xFF
	case 7:
		return (stored - 1) & 0xFF
	}
	return stored
}

// doStencilOp applies op at a pixel when the stencil test is enabled.
func (g *GE) doStencilOp(which, x, y int) {
	reg := g.regs[0xDE]
	var op int
	switch which {
	case 0: // sfail
		op = int(reg & 15)
	case 1: // zfail
		op = int((reg >> 4) & 15)
	default: // zpass
		op = int((reg >> 8) & 15)
	}
	idx, ok := g.stencilIndex(x, y)
	if !ok {
		return
	}
	ref := int((g.regs[0xDD] >> 8) & 0xFF)
	plane := g.stencilAt()
	plane[idx] = uint8(applyStencilOp(op, int(plane[idx]), ref))
}

// factorVector returns the per channel blend factor in [0,1] floats.
// For the alpha channel, color based factors use their alpha component.
// Factor 10 (Fixed) uses the fixed color handed in by the caller:
// FIXEDA when computing the source factor, FIXEDB for the destination.
func factorVector(f int, src, dst, fixed fcolor) (fr, fg, fb, fa float64) {
	sn := func(v float64) float64 { return v / 255 }
	switch f {
	case 0: // Zero
		return 0, 0, 0, 0
	case 1: // One
		return 1, 1, 1, 1
	case 2: // SrcColor
		return sn(src.r), sn(src.g), sn(src.b), sn(src.a)
	case 3: // OneMinusSrcColor
		return 1 - sn(src.r), 1 - sn(src.g), 1 - sn(src.b), 1 - sn(src.a)
	case 4: // SrcAlpha
		s := sn(src.a)
		return s, s, s, s
	case 5: // OneMinusSrcAlpha
		s := 1 - sn(src.a)
		return s, s, s, s
	case 6: // DstAlpha
		s := sn(dst.a)
		return s, s, s, s
	case 7: // OneMinusDstAlpha
		s := 1 - sn(dst.a)
		return s, s, s, s
	case 8: // DstColor
		return sn(dst.r), sn(dst.g), sn(dst.b), sn(dst.a)
	case 9: // OneMinusDstColor
		return 1 - sn(dst.r), 1 - sn(dst.g), 1 - sn(dst.b), 1 - sn(dst.a)
	case 10: // Fixed
		return sn(fixed.r), sn(fixed.g), sn(fixed.b), sn(fixed.a)
	}
	return 1, 1, 1, 1 // unknown factors act as One in this slice
}

// blend applies the blend equation to src over dst. Fixed colors are
// 24 bit RGB arguments with alpha forced to 255 (the argument is only
// 24 bits, so there is no alpha byte to read).
func (g *GE) blend(src, dst fcolor) fcolor {
	reg := g.regs[0xD7]
	op := int(reg & 15)
	sf := int((reg >> 4) & 15)
	df := int((reg >> 8) & 15)
	fixedA := rgba8{
		uint8(g.regs[0xD8]), uint8(g.regs[0xD8] >> 8), uint8(g.regs[0xD8] >> 16), 255,
	}.f()
	fixedB := rgba8{
		uint8(g.regs[0xD9]), uint8(g.regs[0xD9] >> 8), uint8(g.regs[0xD9] >> 16), 255,
	}.f()
	sfr, sfg, sfb, sfa := factorVector(sf, src, dst, fixedA)
	dfr, dfg, dfb, dfa := factorVector(df, src, dst, fixedB)
	ch := func(s, d, fs, fd float64) float64 {
		switch op {
		case 0: // Add
			return s*fs + d*fd
		case 1: // Subtract
			return s*fs - d*fd
		case 2: // ReverseSubtract
			return d*fd - s*fs
		case 3: // Min, factors ignored
			return math.Min(s, d)
		case 4: // Max, factors ignored
			return math.Max(s, d)
		case 5: // AbsDiff, factors ignored
			return math.Abs(s - d)
		}
		return s*fs + d*fd
	}
	return fcolor{
		ch(src.r, dst.r, sfr, dfr),
		ch(src.g, dst.g, sfg, dfg),
		ch(src.b, dst.b, sfb, dfb),
		ch(src.a, dst.a, sfa, dfa),
	}
}

// logicOp applies a logical op per RGB channel between the current
// fragment (src) and the framebuffer value (dst), in whole bytes. The
// alpha channel takes the source alpha, except for the NoOp and Invert
// family entries (ops 5 and 10), which keep the destination alpha.
func logicOp(op int, src, dst rgba8) rgba8 {
	ch := func(s, d uint8) uint8 {
		switch op & 15 {
		case 0: // Clear
			return 0
		case 1: // And
			return s & d
		case 2: // AndReverse
			return s &^ d
		case 3: // Copy
			return s
		case 4: // AndInverted
			return (^s) & d
		case 5: // NoOp
			return d
		case 6: // Xor
			return s ^ d
		case 7: // Or
			return s | d
		case 8: // Nor
			return ^(s | d)
		case 9: // Equiv
			return ^(s ^ d)
		case 10: // Invert
			return ^d
		case 11: // OrReverse
			return s | (^d)
		case 12: // CopyInverted
			return ^s
		case 13: // OrInverted
			return (^s) | d
		case 14: // Nand
			return ^(s & d)
		case 15: // Set
			return 0xFF
		}
		return s
	}
	out := rgba8{ch(src.r, dst.r), ch(src.g, dst.g), ch(src.b, dst.b), src.a}
	if op&15 == 5 || op&15 == 10 {
		out.a = dst.a
	}
	return out
}

// -----------------------------------------------------------------------------
// Vertex fetching and transformation
// -----------------------------------------------------------------------------

// vertex is one decoded and transformed vertex.
type vertex struct {
	sx, sy, sz float64 // screen position and depth (z already clamped)
	u, v       float64 // texture coordinates, pre texture transform
	col        fcolor  // RGBA in 0..255 floats
}

func compSize(code int) int {
	switch code {
	case 1:
		return 1
	case 2:
		return 2
	case 3:
		return 4
	}
	return 0
}

// vtype decodes the VTYPE register (0x12).
type vtype struct {
	tcCode, colorCode, normalCode, posCode int
	weightCode, weightCount                int
	indexCode                              int
	through                                bool
}

func (g *GE) vertexType() vtype {
	a := g.regs[opVTYPE]
	vt := vtype{
		tcCode:     int(a & 3),
		colorCode:  int((a >> 2) & 7),
		normalCode: int((a >> 5) & 3),
		posCode:    int((a >> 7) & 3),
		weightCode: int((a >> 9) & 3),
		indexCode:  int((a >> 21) & 3),
		through:    (a>>23)&1 != 0,
	}
	if vt.weightCode != 0 {
		vt.weightCount = int((a>>14)&15) + 1
	}
	return vt
}

// stride is the exact byte size of one vertex: weights, texcoord,
// color, normal, position, in memory order, with no alignment padding.
// That no-padding choice is a documented simplification of this slice.
// Color codes 5..7 are treated as 8888 sized.
func (vt vtype) stride() int {
	n := vt.weightCount * compSize(vt.weightCode)
	n += 2 * compSize(vt.tcCode)
	switch {
	case vt.colorCode == 0:
	case vt.colorCode <= 3:
		n += 2
	default:
		n += 4
	}
	n += 3 * compSize(vt.normalCode)
	pos := vt.posCode
	if pos == 0 {
		pos = 2 // position code 0 is treated as s16
	}
	n += 3 * compSize(pos)
	return n
}

// readTCScalar reads one texcoord component in the given size code.
func (g *GE) readTCScalar(addr uint32, code int) float64 {
	switch code {
	case 1:
		return float64(g.mem.Read8(addr)) / 255
	case 2:
		return float64(g.mem.Read16(addr)) / 65535
	case 3:
		return float64(math.Float32frombits(g.mem.Read32(addr)))
	}
	return 0
}

// fetchVertex decodes the vertex stored at addr per the current VTYPE
// and transforms it to screen space.
func (g *GE) fetchVertex(addr uint32, vt vtype) vertex {
	off := uint32(0)
	// Weights: read for layout only, skinning is not in this slice.
	off += uint32(vt.weightCount * compSize(vt.weightCode))

	var out vertex
	out.col = fcolor{255, 255, 255, 255} // no color field means white
	if vt.tcCode != 0 {
		out.u = g.readTCScalar(addr+off, vt.tcCode)
		out.v = g.readTCScalar(addr+off+uint32(compSize(vt.tcCode)), vt.tcCode)
		off += uint32(2 * compSize(vt.tcCode))
	}
	if vt.colorCode != 0 {
		switch {
		case vt.colorCode <= 3:
			out.col = unpack16(g.mem.Read16(addr+off), vt.colorCode-1).f()
			off += 2
		default: // 8888 (codes 4..7)
			out.col = unpack8888(g.mem.Read32(addr + off)).f()
			off += 4
		}
	}
	// Normal: read for layout only.
	off += uint32(3 * compSize(vt.normalCode))

	posCode := vt.posCode
	if posCode == 0 {
		posCode = 2
	}
	ps := uint32(compSize(posCode))
	var raw [3]float64 // signed or float value per component
	var rawU [3]uint32 // raw unsigned bits per component
	for i := 0; i < 3; i++ {
		a := addr + off + uint32(i)*ps
		switch posCode {
		case 1:
			v := g.mem.Read8(a)
			rawU[i] = uint32(v)
			raw[i] = float64(int8(v))
		case 3:
			raw[i] = float64(math.Float32frombits(g.mem.Read32(a)))
		default: // s16
			v := g.mem.Read16(a)
			rawU[i] = uint32(v)
			raw[i] = float64(int16(v))
		}
	}

	if vt.through {
		switch posCode {
		case 3: // float: x and y are pixels, z clamps to depth range
			out.sx, out.sy = raw[0], raw[1]
			out.sz = clampF(raw[2], 0, 65535)
		case 1: // s8: values are pixels, z is the unsigned byte
			out.sx, out.sy = raw[0], raw[1]
			out.sz = float64(rawU[2])
		default: // s16: x and y are 12.4 fixed point, z is raw uint16
			out.sx = float64(int32(int16(uint16(rawU[0]))) >> 4)
			out.sy = float64(int32(int16(uint16(rawU[1]))) >> 4)
			out.sz = float64(rawU[2])
		}
		return out
	}

	// Model to screen through the viewport registers. Positions feed
	// the transform as their decoded numeric values.
	out.sx = raw[0]*g.floatReg(0x4A) + g.floatReg(0x4C) + g.floatReg(0x50)
	out.sy = raw[1]*g.floatReg(0x4B) + g.floatReg(0x4D) + g.floatReg(0x51)
	sz := raw[2]*g.floatReg(0x4E) + g.floatReg(0x4F)
	near := float64(g.regs[0xD2] & 0xFFFF)
	far := float64(g.regs[0xD3] & 0xFFFF)
	if (near != 0 || far != 0) && far >= near {
		out.sz = clampF(sz, near, far)
	} else {
		out.sz = clampF(sz, 0, 65535)
	}
	return out
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fetchVertices reads count vertices for a PRIM, honoring the index
// stream when VTYPE declares one.
func (g *GE) fetchVertices(count int, vt vtype) []vertex {
	stride := vt.stride()
	out := make([]vertex, 0, count)
	for i := 0; i < count; i++ {
		addr := g.vertexPtr + uint32(i*stride)
		if vt.indexCode != 0 {
			isize := compSize(vt.indexCode)
			ia := g.indexPtr + uint32(i*isize)
			var idx uint32
			switch vt.indexCode {
			case 1:
				idx = uint32(g.mem.Read8(ia))
			case 2:
				idx = uint32(g.mem.Read16(ia))
			case 3:
				idx = g.mem.Read32(ia)
			}
			addr = g.vertexPtr + idx*uint32(stride)
		}
		out = append(out, g.fetchVertex(addr, vt))
	}
	return out
}

// -----------------------------------------------------------------------------
// Texture sampling
// -----------------------------------------------------------------------------

func (g *GE) texSize() (w, h int) {
	a := g.regs[0xB8]
	return 1 << (a & 31), 1 << ((a >> 8) & 31)
}

func (g *GE) texBufWidth() int {
	w := int(g.regs[0xA8] & 0xFFFF)
	if w == 0 {
		tw, _ := g.texSize()
		return tw
	}
	return w
}

// texScale reads a TEXSCALE register; never written means 1.0.
func (g *GE) texScale(op int) float64 {
	if g.regs[op] == 0 {
		return 1
	}
	return g.floatReg(op)
}

// texelIndex resolves a texel space index for one axis under the
// axis wrap mode: only mode 1 clamps, everything else repeats.
func texelIndex(i, size, mode int) int {
	if size <= 0 {
		return 0
	}
	if mode == 1 {
		if i < 0 {
			return 0
		}
		if i > size-1 {
			return size - 1
		}
		return i
	}
	i %= size
	if i < 0 {
		i += size
	}
	return i
}

// fetchTexel reads the texel at integer coordinates (already resolved
// through texelIndex) in the current texture format.
func (g *GE) fetchTexel(x, y int) rgba8 {
	format := int(g.regs[0xC5] & 7)
	bw := g.texBufWidth()
	swizzled := g.regs[0xC4]&1 != 0

	if format == 4 { // CLUT4: two indices per byte
		var raw uint8
		if swizzled {
			byteX := x >> 1
			off := (y>>3)*(bw/2)*8 + (byteX>>4)*128 + (y&7)*16 + (byteX & 15)
			raw = g.mem.Read8(g.texAddr + uint32(off))
		} else {
			raw = g.mem.Read8(g.texAddr + uint32((y*bw+x)/2))
		}
		var v uint32
		if x&1 != 0 {
			v = uint32(raw >> 4)
		} else {
			v = uint32(raw & 0xF)
		}
		return g.clutLookup(v)
	}

	bpp := 1
	switch format {
	case 0, 1, 2, 6: // 5650, 5551, 4444, CLUT16
		bpp = 2
	case 3, 7: // 8888, CLUT32
		bpp = 4
	}
	var off int
	if swizzled {
		rowBytes := bw * bpp
		off = (y>>3)*rowBytes*8 + ((x*bpp)>>4)*128 + (y&7)*16 + ((x * bpp) & 15)
	} else {
		off = (y*bw + x) * bpp
	}
	addr := g.texAddr + uint32(off)
	switch format {
	case 0, 1, 2:
		return unpack16(g.mem.Read16(addr), format)
	case 3:
		return unpack8888(g.mem.Read32(addr))
	case 5: // CLUT8
		return g.clutLookup(uint32(g.mem.Read8(addr)))
	case 6: // CLUT16
		return g.clutLookup(uint32(g.mem.Read16(addr)))
	case 7: // CLUT32
		return g.clutLookup(g.mem.Read32(addr))
	}
	return rgba8{}
}

// clutLookup maps a raw texture index through the CLUT shift, mask,
// and start fields. Out of range indices are transparent black.
func (g *GE) clutLookup(v uint32) rgba8 {
	_, shift, mask, start := g.clutParams()
	idx := start + int((v>>uint(shift))&uint32(mask))
	if idx < 0 || idx >= 512 {
		return rgba8{}
	}
	return unpack8888(g.clut[idx])
}

// sampleTexture samples the current texture at (u, v) with the
// TEXSCALE/TEXOFFSET transform, wrap modes, and filter selection.
func (g *GE) sampleTexture(u, v float64) rgba8 {
	tw, th := g.texSize()
	wrap := g.regs[0xC7]
	uMode, vMode := int(wrap&7), int((wrap>>8)&7)

	// Filter: linear when either the MIN or MAG selection is 1 or 5
	// (linear or linear-mipmap style codes). No mipmaps in this slice.
	freg := g.regs[0xC6]
	minF, magF := int(freg&7), int((freg>>8)&7)
	linear := minF == 1 || minF == 5 || magF == 1 || magF == 5

	sampleAxis := func(t, scale, offset float64, size, mode int) (int, int, float64) {
		n := t*scale + offset
		if mode == 1 {
			n = clampF(n, 0, 1)
		} else {
			n = n - math.Floor(n)
		}
		s := n * float64(size)
		if !linear {
			return texelIndex(int(math.Floor(s)), size, mode), 0, 0
		}
		s2 := s - 0.5
		i0 := int(math.Floor(s2))
		frac := s2 - float64(i0)
		return texelIndex(i0, size, mode), texelIndex(i0+1, size, mode), frac
	}

	x0, x1, fx := sampleAxis(u, g.texScale(0xC0), g.floatReg(0xC2), tw, uMode)
	y0, y1, fy := sampleAxis(v, g.texScale(0xC1), g.floatReg(0xC3), th, vMode)
	if !linear {
		return g.fetchTexel(x0, y0)
	}
	c00 := g.fetchTexel(x0, y0).f()
	c10 := g.fetchTexel(x1, y0).f()
	c01 := g.fetchTexel(x0, y1).f()
	c11 := g.fetchTexel(x1, y1).f()
	mix := func(a, b, t float64) float64 { return a + (b-a)*t }
	return fcolor{
		mix(mix(c00.r, c10.r, fx), mix(c01.r, c11.r, fx), fy),
		mix(mix(c00.g, c10.g, fx), mix(c01.g, c11.g, fx), fy),
		mix(mix(c00.b, c10.b, fx), mix(c01.b, c11.b, fx), fy),
		mix(mix(c00.a, c10.a, fx), mix(c01.a, c11.a, fx), fy),
	}.bytes()
}

// -----------------------------------------------------------------------------
// Fragment pipeline
// -----------------------------------------------------------------------------

// plot runs one fragment through the whole pipeline and writes it if
// it survives. Steps, in order: scissor and bounds clip, alpha test,
// color test, stencil test, depth test, stencil zpass op, texture
// combine, blend, logical op, masked write.
func (g *GE) plot(x, y int, z float64, col fcolor, u, v float64) {
	// 1. Clip to scissor and framebuffer bounds.
	sx1, sy1, sx2, sy2 := g.scissorRect()
	if x < sx1 || x > sx2 || y < sy1 || y > sy2 {
		return
	}
	if x < 0 || y < 0 || x >= g.fbWidth() || y >= fbMaxHeight {
		return
	}

	src := col.bytes()

	// 2. Alpha test: (srcA & mask) vs (ref & mask).
	if g.regs[0xDA]&1 != 0 {
		reg := g.regs[0xDB]
		fn := int(reg & 7)
		ref := int((reg >> 8) & 0xFF)
		mask := int((reg >> 16) & 0xFF)
		if !comparePass(fn, int(src.a)&mask, ref&mask) {
			return
		}
	}

	// 3. Color test: 24 bit (srcRGB & mask) vs (ref & mask).
	if g.regs[0xDF]&1 != 0 {
		fn := int(g.regs[0xE0] & 7)
		ref := int(g.regs[0xE1] & 0xFFFFFF)
		mask := int(g.regs[0xE2] & 0xFFFFFF)
		srcRGB := int(src.r) | int(src.g)<<8 | int(src.b)<<16
		if !comparePass(fn, srcRGB&mask, ref&mask) {
			return
		}
	}

	stencilOn := g.regs[0xDC]&1 != 0

	// 4. Stencil test: (ref & mask) vs (stored & mask). A failure
	// applies the sfail op and drops the fragment.
	if stencilOn {
		reg := g.regs[0xDD]
		fn := int(reg & 7)
		ref := int((reg >> 8) & 0xFF)
		mask := int((reg >> 16) & 0xFF)
		stored := 0
		if idx, ok := g.stencilIndex(x, y); ok {
			stored = int(g.stencilAt()[idx])
		}
		if !comparePass(fn, ref&mask, stored&mask) {
			g.doStencilOp(0, x, y)
			return
		}
	}

	// 5. Depth test. When ZTESTENABLE bit0 is clear, no depth reads or
	// writes happen at all. A pass writes depth only when bit1 is set
	// and DEPTHMASK bit0 is clear.
	if g.regs[0xD4]&1 != 0 {
		fn := int(g.regs[0xD5] & 7)
		zi := int(math.Floor(z + 0.5))
		if zi < 0 {
			zi = 0
		}
		if zi > 65535 {
			zi = 65535
		}
		stored := int(g.readDepth(x, y))
		if !comparePass(fn, zi, stored) {
			if stencilOn {
				g.doStencilOp(1, x, y)
			}
			return
		}
		if g.regs[0xD4]&2 != 0 && g.regs[0xE7]&1 == 0 {
			g.writeDepth(x, y, uint16(zi))
		}
	}

	// 6. Stencil zpass op, now that every test passed.
	if stencilOn {
		g.doStencilOp(2, x, y)
	}

	// 7. Texture combine.
	out := col
	if g.regs[0x53]&1 != 0 {
		texel := g.sampleTexture(u, v).f()
		switch int(g.regs[0xC8] & 7) {
		case 1: // decal: texel RGB, source alpha
			out = fcolor{texel.r, texel.g, texel.b, col.a}
		case 3: // replace
			out = texel
		case 4: // add
			out = fcolor{
				clampF(col.r+texel.r, 0, 255),
				clampF(col.g+texel.g, 0, 255),
				clampF(col.b+texel.b, 0, 255),
				clampF(col.a+texel.a, 0, 255),
			}
		default: // 0 modulate, 2 blend (acts as modulate in this slice),
			// and anything else
			out = fcolor{
				col.r * texel.r / 255,
				col.g * texel.g / 255,
				col.b * texel.b / 255,
				col.a * texel.a / 255,
			}
		}
	}

	old := g.readDrawPixel(x, y)

	// 8. Blend.
	if g.regs[0xD6]&1 != 0 {
		out = g.blend(out, old.f())
	}

	// 9. Logical op, between the current fragment color and the
	// framebuffer value, in whole bytes.
	final := out.bytes()
	if g.regs[0xE3]&1 != 0 {
		final = logicOp(int(g.regs[0xE4]&15), final, old)
	}

	// 10. Masked write. Never written masks read as fully open.
	rgbMask := g.regs[0xE5]
	if rgbMask == 0 {
		rgbMask = 0xFFFFFF
	}
	aMask := g.regs[0xE6] & 0xFF
	if aMask == 0 {
		aMask = 0xFF
	}
	final = rgba8{
		(final.r & uint8(rgbMask)) | (old.r &^ uint8(rgbMask)),
		(final.g & uint8(rgbMask>>8)) | (old.g &^ uint8(rgbMask>>8)),
		(final.b & uint8(rgbMask>>16)) | (old.b &^ uint8(rgbMask>>16)),
		(final.a & uint8(aMask)) | (old.a &^ uint8(aMask)),
	}
	g.writePixelAt(g.fbAddr, g.fbWidth(), g.pixelFormat(), x, y, final)
}

// -----------------------------------------------------------------------------
// PRIM
// -----------------------------------------------------------------------------

// prim implements PRIM (0x04), including the CLEAR mode path.
func (g *GE) prim(arg uint32) {
	// Clear mode: while CLEAR bit0 is set, PRIM ignores its vertices
	// and fills the region intersect scissor rectangle instead.
	if g.regs[0xCC]&1 != 0 {
		g.clearFill()
		return
	}
	primType := int((arg >> 16) & 7)
	count := int(arg & 0xFFFF)
	if count == 0 {
		return
	}
	vt := g.vertexType()
	verts := g.fetchVertices(count, vt)

	switch primType {
	case 0: // points
		for _, v := range verts {
			g.plot(roundCoord(v.sx), roundCoord(v.sy), v.sz, v.col, v.u, v.v)
		}
	case 1: // lines, in pairs
		for i := 0; i+1 < len(verts); i += 2 {
			g.line(verts[i], verts[i+1])
		}
	case 2: // line strip
		for i := 0; i+1 < len(verts); i++ {
			g.line(verts[i], verts[i+1])
		}
	case 3: // triangles, in triples
		for i := 0; i+2 < len(verts); i += 3 {
			g.triangle(verts[i], verts[i+1], verts[i+2])
		}
	case 4: // triangle strip
		for i := 0; i+2 < len(verts); i++ {
			g.triangle(verts[i], verts[i+1], verts[i+2])
		}
	case 5: // triangle fan around verts[0]
		for i := 1; i+1 < len(verts); i++ {
			g.triangle(verts[0], verts[i], verts[i+1])
		}
	case 6: // sprites, in pairs
		for i := 0; i+1 < len(verts); i += 2 {
			g.sprite(verts[i], verts[i+1])
		}
	default:
		// Primitive type 7 is not defined in this slice: counted, not drawn.
	}
}

// clearFill fills region intersect scissor with the clear values. Color
// goes through the masked write path but bypasses blend, logic, and
// all tests; depth and stencil are written directly.
func (g *GE) clearFill() {
	mode := g.regs[0xCC]
	rx1, ry1, rx2, ry2 := g.regionRect()
	sx1, sy1, sx2, sy2 := g.scissorRect()
	x1, y1 := max(rx1, sx1), max(ry1, sy1)
	x2, y2 := min(rx2, sx2), min(ry2, sy2)
	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 >= g.fbWidth() {
		x2 = g.fbWidth() - 1
	}
	if y2 >= fbMaxHeight {
		y2 = fbMaxHeight - 1
	}
	cc := g.regs[0xC9]
	clearCol := rgba8{uint8(cc), uint8(cc >> 8), uint8(cc >> 16), uint8(g.regs[0xCA])}
	stencilVal := uint8(g.regs[0xCA] >> 8)
	depthVal := uint16(g.regs[0xCB] & 0xFFFF)
	rgbMask := g.regs[0xE5]
	if rgbMask == 0 {
		rgbMask = 0xFFFFFF
	}
	aMask := g.regs[0xE6] & 0xFF
	if aMask == 0 {
		aMask = 0xFF
	}
	for y := y1; y <= y2; y++ {
		for x := x1; x <= x2; x++ {
			if mode&2 != 0 { // target color
				old := g.readDrawPixel(x, y)
				c := rgba8{
					(clearCol.r & uint8(rgbMask)) | (old.r &^ uint8(rgbMask)),
					(clearCol.g & uint8(rgbMask>>8)) | (old.g &^ uint8(rgbMask>>8)),
					(clearCol.b & uint8(rgbMask>>16)) | (old.b &^ uint8(rgbMask>>16)),
					(clearCol.a & uint8(aMask)) | (old.a &^ uint8(aMask)),
				}
				g.writePixelAt(g.fbAddr, g.fbWidth(), g.pixelFormat(), x, y, c)
			}
			if mode&4 != 0 { // target alpha/stencil plane
				if idx, ok := g.stencilIndex(x, y); ok {
					g.stencilAt()[idx] = stencilVal
				}
			}
			if mode&8 != 0 { // target depth
				g.writeDepth(x, y, depthVal)
			}
		}
	}
}

// roundCoord rounds a screen coordinate to the nearest pixel index.
func roundCoord(v float64) int {
	return int(math.Floor(v + 0.5))
}

// line rasterizes one line with Bresenham steps, interpolating z, uv,
// and color by t along the line.
func (g *GE) line(a, b vertex) {
	x0, y0 := roundCoord(a.sx), roundCoord(a.sy)
	x1, y1 := roundCoord(b.sx), roundCoord(b.sy)
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	stepX, stepY := 1, 1
	if x0 > x1 {
		stepX = -1
	}
	if y0 > y1 {
		stepY = -1
	}
	err := dx + dy
	steps := max(dx, -dy)
	if steps < 1 {
		steps = 1
	}
	x, y := x0, y0
	for step := 0; ; step++ {
		t := float64(step) / float64(steps)
		g.plot(x, y,
			lerp(a.sz, b.sz, t),
			lerpColor(a.col, b.col, t),
			lerp(a.u, b.u, t), lerp(a.v, b.v, t))
		if x == x1 && y == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x += stepX
		}
		if e2 <= dx {
			err += dx
			y += stepY
		}
	}
}

// triangle rasterizes one triangle with barycentric interpolation at
// pixel centers. Interpolation is affine in screen space: NO
// perspective correction in this slice. Both windings are accepted
// because there is no backface culling.
func (g *GE) triangle(a, b, c vertex) {
	area := edge(a.sx, a.sy, b.sx, b.sy, c.sx, c.sy)
	if area == 0 {
		return
	}
	minX := int(math.Floor(math.Min(a.sx, math.Min(b.sx, c.sx))))
	maxX := int(math.Ceil(math.Max(a.sx, math.Max(b.sx, c.sx))))
	minY := int(math.Floor(math.Min(a.sy, math.Min(b.sy, c.sy))))
	maxY := int(math.Ceil(math.Max(a.sy, math.Max(b.sy, c.sy))))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			w0 := edge(b.sx, b.sy, c.sx, c.sy, px, py) / area
			w1 := edge(c.sx, c.sy, a.sx, a.sy, px, py) / area
			w2 := edge(a.sx, a.sy, b.sx, b.sy, px, py) / area
			if w0 < 0 || w1 < 0 || w2 < 0 {
				continue
			}
			g.plot(x, y,
				w0*a.sz+w1*b.sz+w2*c.sz,
				fcolor{
					w0*a.col.r + w1*b.col.r + w2*c.col.r,
					w0*a.col.g + w1*b.col.g + w2*c.col.g,
					w0*a.col.b + w1*b.col.b + w2*c.col.b,
					w0*a.col.a + w1*b.col.a + w2*c.col.a,
				},
				w0*a.u+w1*b.u+w2*c.u,
				w0*a.v+w1*b.v+w2*c.v)
		}
	}
}

// edge is the 2D cross product of (b-a) and (p-a), the signed area
// kernel for barycentric weights.
func edge(ax, ay, bx, by, px, py float64) float64 {
	return (px-bx)*(ay-by) - (ax-bx)*(py-by)
}

// sprite fills the half open rectangle [x0,x1) by [y0,y1) from two
// corner vertices. UVs interpolate linearly across the rectangle with
// the step formula from the spec; z and color come from the first
// corner.
func (g *GE) sprite(a, b vertex) {
	x0, y0 := roundCoord(a.sx), roundCoord(a.sy)
	x1, y1 := roundCoord(b.sx), roundCoord(b.sy)
	if x1 <= x0 || y1 <= y0 {
		return
	}
	for y := y0; y < y1; y++ {
		v := a.v + (b.v-a.v)*(float64(y-y0)+0.5)/float64(y1-y0)
		for x := x0; x < x1; x++ {
			u := a.u + (b.u-a.u)*(float64(x-x0)+0.5)/float64(x1-x0)
			g.plot(x, y, a.sz, a.col, u, v)
		}
	}
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func lerpColor(a, b fcolor, t float64) fcolor {
	return fcolor{lerp(a.r, b.r, t), lerp(a.g, b.g, t), lerp(a.b, b.b, t), lerp(a.a, b.a, t)}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
