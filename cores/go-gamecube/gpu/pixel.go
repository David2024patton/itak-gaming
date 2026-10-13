package gpu

// Pixel pipeline: alpha test, depth test, blending, logic
// ops, and the EFB color encoding, driven by these BP
// registers:
//
//   bpZMode (0x40): enable bit 0, compare function bits 1-3,
//     update enable bit 4. The early-z override lives in
//     bpPEControl bit 6 and is stored only (this renderer
//     always tests depth at the fragment, which is the
//     result early-z would produce for opaque fragments).
//   bpBlendMode (0x41): blend enable bit 0, logic op enable
//     bit 1, dither bit 2 (stored, ignored), color update
//     bit 3, alpha update bit 4, destination factor bits
//     5-7, source factor bits 8-10, subtract bit 11, logic
//     op bits 12-15.
//   bpPEControl (0x43): pixel format bits 0-2, z format
//     bits 3-4 (stored), early-z bit 6 (stored).
//   bpAlphaFunc (0xF3): compare A function bits 0-2, ref A
//     bits 3-10, combine op bits 11-12 (0 AND, 1 OR, 2 XOR,
//     3 XNOR), compare B function bits 13-15, ref B bits
//     16-23.
//
// Compare functions (depth and alpha): 0 NEVER, 1 LESS,
// 2 EQUAL, 3 LEQUAL, 4 GREATER, 5 NEQUAL, 6 GEQUAL,
// 7 ALWAYS, of the incoming value against the stored depth
// or the reference value.
//
// Blend factors: 0 ZERO, 1 ONE, 2 SRCCLR, 3 INVSRCCLR,
// 4 SRCALPHA, 5 INVSRCALPHA, 6 DSTALPHA, 7 INVDSTALPHA.
// With blend enabled the result is src*srcFactor plus
// dst*dstFactor per channel, or src*srcFactor minus
// dst*dstFactor when the subtract bit is set, clamped to
// the channel range. Logic ops replace blending when the
// logic enable bit is set: 0 CLEAR, 1 AND, 2 AND_REVERSE,
// 3 COPY, 4 AND_INVERTED, 5 NOOP, 6 XOR, 7 OR, 8 NOR,
// 9 EQUIV, 10 INVERT, 11 OR_REVERSE, 12 COPY_INVERTED,
// 13 OR_INVERTED, 14 NAND, 15 SET, applied bitwise to the
// 8-bit channels.

type color8 struct {
	r, g, b, a uint8
}

func expand4(v uint8) uint8 { v &= 0xF; return v<<4 | v }
func expand5(v uint8) uint8 { v &= 0x1F; return v<<3 | v>>2 }
func expand6(v uint8) uint8 { v &= 0x3F; return v<<2 | v>>4 }
func expand3(v uint8) uint8 { v &= 7; return v<<5 | v<<2 | v>>1 }

// decodeEFB converts one raw EFB color word under the given
// PE pixel format to 8-bit channels. RGBA6 channels expand
// with (v<<2)|(v>>4); the other widths use the matching
// documented expansions.
func decodeEFB(format uint32, raw uint32) (r, g, b, a uint8) {
	switch format {
	case pfbRGB565:
		return expand5(uint8(raw >> 11)), expand6(uint8((raw >> 5) & 0x3F)), expand5(uint8(raw & 0x1F)), 255
	case pfbRGBA8:
		return uint8(raw >> 24), uint8(raw >> 16), uint8(raw >> 8), uint8(raw)
	case pfbRGBA6Z24:
		return expand6(uint8(raw >> 18)), expand6(uint8((raw >> 12) & 0x3F)), expand6(uint8((raw >> 6) & 0x3F)), expand6(uint8(raw & 0x3F))
	default: // pfbRGB8
		return uint8(raw >> 16), uint8(raw >> 8), uint8(raw), 255
	}
}

// encodeEFB converts 8-bit channels to the raw EFB word for
// the given format. RGBA6 channels are truncated to 6 bits,
// as on hardware.
func encodeEFB(format uint32, c color8) uint32 {
	switch format {
	case pfbRGB565:
		return uint32(c.r>>3)<<11 | uint32(c.g>>2)<<5 | uint32(c.b>>3)
	case pfbRGBA8:
		return uint32(c.r)<<24 | uint32(c.g)<<16 | uint32(c.b)<<8 | uint32(c.a)
	case pfbRGBA6Z24:
		return uint32(c.r>>2)<<18 | uint32(c.g>>2)<<12 | uint32(c.b>>2)<<6 | uint32(c.a>>2)
	default: // pfbRGB8
		return uint32(c.r)<<16 | uint32(c.g)<<8 | uint32(c.b)
	}
}

func compare(fn uint32, incoming, ref uint32) bool {
	switch fn & 7 {
	case 0:
		return false
	case 1:
		return incoming < ref
	case 2:
		return incoming == ref
	case 3:
		return incoming <= ref
	case 4:
		return incoming > ref
	case 5:
		return incoming != ref
	case 6:
		return incoming >= ref
	default:
		return true
	}
}

func (g *GPU) alphaPass(a uint8) bool {
	v := g.bp[bpAlphaFunc]
	fnA := v & 7
	refA := (v >> 3) & 0xFF
	op := (v >> 11) & 3
	fnB := (v >> 13) & 7
	refB := (v >> 16) & 0xFF
	pa := compare(fnA, uint32(a), refA)
	pb := compare(fnB, uint32(a), refB)
	switch op {
	case 1:
		return pa || pb
	case 2:
		return pa != pb
	case 3:
		return pa == pb
	default:
		return pa && pb
	}
}

func blendFactor(fn uint32, src, dst color8) [4]float64 {
	one := func(x uint8) float64 { return float64(x) / 255 }
	switch fn & 7 {
	case 1:
		return [4]float64{1, 1, 1, 1}
	case 2: // SRCCLR
		return [4]float64{one(src.r), one(src.g), one(src.b), one(src.a)}
	case 3: // INVSRCCLR
		return [4]float64{1 - one(src.r), 1 - one(src.g), 1 - one(src.b), 1 - one(src.a)}
	case 4: // SRCALPHA
		s := one(src.a)
		return [4]float64{s, s, s, s}
	case 5: // INVSRCALPHA
		s := 1 - one(src.a)
		return [4]float64{s, s, s, s}
	case 6: // DSTALPHA
		s := one(dst.a)
		return [4]float64{s, s, s, s}
	case 7: // INVDSTALPHA
		s := 1 - one(dst.a)
		return [4]float64{s, s, s, s}
	default: // ZERO
		return [4]float64{}
	}
}

func logicOp(op uint32, s, d uint8) uint8 {
	switch op & 0xF {
	case 0:
		return 0
	case 1:
		return s & d
	case 2:
		return s &^ d
	case 3:
		return s
	case 4:
		return ^s & d
	case 5:
		return d
	case 6:
		return s ^ d
	case 7:
		return s | d
	case 8:
		return ^(s | d)
	case 9:
		return ^(s ^ d)
	case 10:
		return ^d
	case 11:
		return s | ^d
	case 12:
		return ^s
	case 13:
		return ^s | d
	case 14:
		return ^(s & d)
	default:
		return 0xFF
	}
}

func clamp255(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// plotFragment runs one fragment through the pixel pipeline
// at EFB pixel (x, y) with the given source color (0-1
// floats) and raw 24-bit depth.
func (g *GPU) plotFragment(x, y int, src color8, depth uint32) {
	if x < 0 || y < 0 || x >= EFBWidth || y >= EFBHeight {
		return
	}
	// Scissor: the fragment must fall inside the inclusive
	// scissor rectangle.
	tlx := int(g.bp[bpScissorTL] & 0xFFF)
	tly := int((g.bp[bpScissorTL] >> 12) & 0xFFF)
	brx := int(g.bp[bpScissorBR] & 0xFFF)
	bry := int((g.bp[bpScissorBR] >> 12) & 0xFFF)
	if x < tlx || x > brx || y < tly || y > bry {
		return
	}
	if !g.alphaPass(src.a) {
		return
	}
	idx := y*EFBWidth + x
	zm := g.bp[bpZMode]
	if zm&1 != 0 {
		if !compare((zm>>1)&7, depth, g.efbDepth[idx]) {
			return
		}
	}
	format := g.bp[bpPEControl] & 7
	dr, dg, db, da := decodeEFB(format, g.efbColor[idx])
	dst := color8{dr, dg, db, da}
	out := src
	bm := g.bp[bpBlendMode]
	if bm&(1<<1) != 0 {
		// Logic op mode.
		op := (bm >> 12) & 0xF
		out = color8{
			logicOp(op, src.r, dst.r),
			logicOp(op, src.g, dst.g),
			logicOp(op, src.b, dst.b),
			logicOp(op, src.a, dst.a),
		}
	} else if bm&1 != 0 {
		sf := blendFactor((bm>>8)&7, src, dst)
		df := blendFactor((bm>>5)&7, src, dst)
		sub := bm&(1<<11) != 0
		calc := func(s, d uint8, fs, fd float64) uint8 {
			if sub {
				return clamp255(float64(s)*fs - float64(d)*fd)
			}
			return clamp255(float64(s)*fs + float64(d)*fd)
		}
		out = color8{
			calc(src.r, dst.r, sf[0], df[0]),
			calc(src.g, dst.g, sf[1], df[1]),
			calc(src.b, dst.b, sf[2], df[2]),
			calc(src.a, dst.a, sf[3], df[3]),
		}
	}
	// The constant alpha register (bpConstAlpha) is stored
	// only: destination alpha selection is a TEV feature and
	// out of scope for this slice.
	// Color and alpha update masks.
	if bm&(1<<3) == 0 {
		out.r, out.g, out.b = dst.r, dst.g, dst.b
	}
	if bm&(1<<4) == 0 {
		out.a = dst.a
	}
	g.efbColor[idx] = encodeEFB(format, out)
	if zm&(1<<4) != 0 {
		g.efbDepth[idx] = depth & 0xFFFFFF
	}
}
