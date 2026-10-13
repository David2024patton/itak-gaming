package rdp

// Triangle and texture rectangle rasterization. The
// plane-evaluation model and the stepping rules are
// described at the top of render.go.

// sext14 sign-extends a 14-bit Y coordinate (11.2).
func sext14(v uint32) int32 {
	x := int32(v & 0x3FFF)
	if x&0x2000 != 0 {
		x -= 0x4000
	}
	return x
}

// sext16 sign-extends a 16-bit field.
func sext16(v uint32) int32 {
	x := int32(v & 0xFFFF)
	if x&0x8000 != 0 {
		x -= 0x10000
	}
	return x
}

// pairFrac rebuilds a 32-bit 16.16 value whose integer
// half sits in one word and whose fraction half sits in
// a paired word, at the same 16-bit lane (lane 0 is the
// topmost, bits 63..48).
func pairFrac(intWord, fracWord uint64, lane int) int32 {
	shift := uint(48 - 16*lane)
	i := uint32(intWord>>shift) & 0xFFFF
	f := uint32(fracWord>>shift) & 0xFFFF
	return int32(i<<16 | f)
}

// triCmd is a decoded triangle command.
type triCmd struct {
	lft   bool
	level int
	tile  int

	yh, ym, yl int32 // 11.2 subpixel units
	// Edge coefficients, 16.16.
	xh, dxhdy int32
	xm, dxmdy int32
	xl, dxldy int32

	shaded    bool
	tex, hasZ bool

	// Shade attributes at the top vertex, 16.16 in
	// 0..255 color units (colQ) and derivatives.
	col    [4]int32 // R,G,B,A
	dColDx [4]int32
	dColDe [4]int32
	dColDy [4]int32

	// Texture attributes at the top vertex, 16.16 raw.
	s, t, w int32
	dsDx    int32
	dtDx    int32
	dwDx    int32
	dsDe    int32
	dtDe    int32
	dwDe    int32
	dsDy    int32
	dtDy    int32
	dwDy    int32

	z    int32 // 16.16
	dzDx int32
	dzDe int32
	dzDy int32
}

// cmdTriangle decodes and rasterizes one triangle
// command. words holds the full command (edge group,
// then shade, texture, and z groups as present).
func (r *RDP) cmdTriangle(op int, words []uint64) {
	var tc triCmd
	tc.shaded = op&0x04 != 0
	tc.tex = op&0x02 != 0
	tc.hasZ = op&0x01 != 0

	w0 := words[0]
	tc.lft = (w0>>55)&1 != 0
	tc.level = int((w0 >> 51) & 7)
	tc.tile = int((w0 >> 48) & 7)
	tc.yl = sext14(uint32(w0 >> 32))
	tc.ym = sext14(uint32(w0 >> 16))
	tc.yh = sext14(uint32(w0))

	tc.xh = int32(words[1] >> 32)
	tc.dxhdy = int32(words[1])
	tc.xm = int32(words[2] >> 32)
	tc.dxmdy = int32(words[2])
	tc.xl = int32(words[3] >> 32)
	tc.dxldy = int32(words[3])

	wi := 4
	if tc.shaded {
		// Lanes: 0=R, 1=G, 2=B, 3=A.
		for lane := 0; lane < 4; lane++ {
			tc.col[lane] = pairFrac(words[wi+0], words[wi+2], lane)
			tc.dColDx[lane] = pairFrac(words[wi+1], words[wi+3], lane)
			tc.dColDe[lane] = pairFrac(words[wi+4], words[wi+6], lane)
			tc.dColDy[lane] = pairFrac(words[wi+5], words[wi+7], lane)
		}
		wi += 8
	}
	if tc.tex {
		// Lanes: 0=S, 1=T, 2=W.
		tc.s = pairFrac(words[wi+0], words[wi+2], 0)
		tc.t = pairFrac(words[wi+0], words[wi+2], 1)
		tc.w = pairFrac(words[wi+0], words[wi+2], 2)
		tc.dsDx = pairFrac(words[wi+1], words[wi+3], 0)
		tc.dtDx = pairFrac(words[wi+1], words[wi+3], 1)
		tc.dwDx = pairFrac(words[wi+1], words[wi+3], 2)
		tc.dsDe = pairFrac(words[wi+4], words[wi+6], 0)
		tc.dtDe = pairFrac(words[wi+4], words[wi+6], 1)
		tc.dwDe = pairFrac(words[wi+4], words[wi+6], 2)
		tc.dsDy = pairFrac(words[wi+5], words[wi+7], 0)
		tc.dtDy = pairFrac(words[wi+5], words[wi+7], 1)
		tc.dwDy = pairFrac(words[wi+5], words[wi+7], 2)
		wi += 8
	}
	if tc.hasZ {
		tc.z = int32(words[wi] >> 32)
		tc.dzDx = int32(words[wi])
		tc.dzDe = int32(words[wi+1] >> 32)
		tc.dzDy = int32(words[wi+1])
	}

	r.rasterTri(&tc)
}

// plane evaluates an attribute plane at pixel (x, line)
// anchored at (xhTop, yh): attr(x, y) = a0 + Dx*(x -
// xhTop) + Dy*(ySub - yh)/4, with ySub = line*4, all in
// 16.16 fixed point. xhTop is the major edge position
// at the top vertex's height, in 16.16.
func plane(a0, dx, dy int32, xPix int, xhTop int32, lineSub, yh int32) int32 {
	dxTerm := (int64(dx) * (int64(int32(xPix))<<16 - int64(xhTop))) >> 16
	dyTerm := (int64(dy) * int64(lineSub-yh)) >> 2
	return a0 + int32(dxTerm) + int32(dyTerm)
}

// ceilDiv4 returns ceil(v/4) for the 11.2 domain.
func ceilDiv4(v int32) int32 { return (v + 3) >> 2 }

// rasterTri walks the triangle scanline by scanline.
func (r *RDP) rasterTri(tc *triCmd) {
	firstLine := int(ceilDiv4(tc.yh))
	lastLine := int(ceilDiv4(tc.yl)) // exclusive
	midLine := int(ceilDiv4(tc.ym))
	if lastLine <= firstLine {
		return
	}
	prevLine := int32(firstLine - 1)
	// Major edge position at the top vertex height:
	// the given XH sits at prevLine, so step it to YH.
	xhTop := tc.xh + int32((int64(tc.dxhdy)*int64(tc.yh-prevLine*4))>>2)

	// LOD setup for textured triangles.
	lodLevel, lodFrac := 0, r.primLvlFrc
	persp := r.omPerspTex()
	if tc.tex {
		lodLevel, lodFrac = r.triLOD(tc, xhTop, persp)
		if lodLevel > tc.level {
			lodLevel = tc.level
		}
		if lodLevel < r.primMinLvl {
			lodLevel = r.primMinLvl
			lodFrac = r.primLvlFrc
		}
	}

	for y := firstLine; y < lastLine; y++ {
		// Major edge at this line (stepped once from
		// the previous line before the first line).
		steps := int32(y - int(prevLine))
		xh := tc.xh + int32(int64(tc.dxhdy)*int64(steps))
		var xMinor int32
		if y < midLine {
			xMinor = tc.xm + int32(int64(tc.dxmdy)*int64(steps))
		} else {
			lSteps := int32(y - midLine)
			xMinor = tc.xl + int32(int64(tc.dxldy)*int64(lSteps))
		}
		left, right := xh, xMinor
		if !tc.lft {
			// lft 0: the major edge is the left edge.
			left, right = xh, xMinor
		} else {
			left, right = xMinor, xh
		}
		if right <= left {
			continue
		}
		lineSub := int32(y * 4)
		// Pixel span touched by the coverage interval.
		x0 := int(left >> 16)
		x1 := int((right - 1) >> 16)
		for x := x0; x <= x1; x++ {
			cvg := spanCoverage(left, right, x)
			if cvg <= 0 {
				continue
			}
			f := &frag{x: x, y: y, cvg: cvg, tile: -1}
			if tc.shaded {
				var cr [4]int32
				for i := 0; i < 4; i++ {
					cr[i] = plane(tc.col[i], tc.dColDx[i], tc.dColDy[i],
						x, xhTop, lineSub, tc.yh)
				}
				f.shade = RGBA{
					clamp255(cr[0] >> 16),
					clamp255(cr[1] >> 16),
					clamp255(cr[2] >> 16),
					clamp255(cr[3] >> 16),
				}
			}
			if tc.hasZ {
				zf := plane(tc.z, tc.dzDx, tc.dzDy, x, xhTop, lineSub, tc.yh)
				f.z = clampZ18(zf)
				dzx := abs32(tc.dzDx)
				dzy := abs32(tc.dzDy)
				if dzy > dzx {
					dzx = dzy
				}
				f.dz = clampZ18(dzx)
			}
			if tc.tex {
				sRaw := plane(tc.s, tc.dsDx, tc.dsDy, x, xhTop, lineSub, tc.yh)
				tRaw := plane(tc.t, tc.dtDx, tc.dtDy, x, xhTop, lineSub, tc.yh)
				wRaw := plane(tc.w, tc.dwDx, tc.dwDy, x, xhTop, lineSub, tc.yh)
				if persp && wRaw != 0 {
					// Both values are raw 16.16: their
					// ratio is already in 1/32 texel
					// units (S = st * texels * 32 at a
					// vertex, W = 1.0 there).
					f.texS = int32(float64(sRaw) / float64(wRaw))
					f.texT = int32(float64(tRaw) / float64(wRaw))
				} else {
					f.texS = sRaw >> 16
					f.texT = tRaw >> 16
				}
				// The positions above are in units of
				// (texel * 32): raw 16.16 S with W=1 maps
				// S/65536 = texel*32, so persp divides the
				// raw values and rescales; see README.
				f.tile = tc.tile
				f.lodLevel = lodLevel
				f.lodFrac = lodFrac
			}
			r.plot(f)
		}
	}
}

// triLOD computes the mip level and LOD fraction for a
// textured triangle from the derivative magnitudes at
// the top vertex, in texels per pixel.
func (r *RDP) triLOD(tc *triCmd, xhTop int32, persp bool) (int, int) {
	if !r.omTexLOD() {
		return 0, r.primLvlFrc
	}
	// Derivatives of the texture coordinates in texels
	// per pixel, at the top vertex.
	var dsdx, dtdx, dsdy, dtdy float64
	if persp && tc.w != 0 {
		w := float64(tc.w)
		quo := func(v, dv, dw int32) float64 {
			return (float64(dv)*w - float64(v)*float64(dw)) / (w * w) / 32.0
		}
		dsdx = quo(tc.s, tc.dsDx, tc.dwDx)
		dtdx = quo(tc.t, tc.dtDx, tc.dwDx)
		dsdy = quo(tc.s, tc.dsDy, tc.dwDy)
		dtdy = quo(tc.t, tc.dtDy, tc.dwDy)
	} else {
		const scale = 1.0 / (65536.0 * 32.0)
		dsdx = float64(tc.dsDx) * scale
		dtdx = float64(tc.dtDx) * scale
		dsdy = float64(tc.dsDy) * scale
		dtdy = float64(tc.dtDy) * scale
	}
	mag := absF(dsdx)
	if v := absF(dtdx); v > mag {
		mag = v
	}
	if v := absF(dsdy); v > mag {
		mag = v
	}
	if v := absF(dtdy); v > mag {
		mag = v
	}
	if mag <= 1.0 {
		return 0, 0
	}
	level := 0
	m := mag
	for m >= 2.0 && level < 7 {
		m /= 2.0
		level++
	}
	frac := int((m - 1.0) * 255.0)
	if frac > 255 {
		frac = 255
	}
	return level, frac
}

// spanCoverage computes horizontal coverage in eighths
// of pixel x by the interval [left, right) (16.16).
func spanCoverage(left, right int32, x int) int {
	lo := int64(int32(x)) << 16
	hi := lo + 65536
	l := int64(left)
	if l < lo {
		l = lo
	}
	rr := int64(right)
	if rr > hi {
		rr = hi
	}
	overlap := rr - l
	if overlap <= 0 {
		return 0
	}
	c := int((overlap*8 + 32768) >> 16)
	if c > 8 {
		c = 8
	}
	return c
}

// clampZ18 converts a 16.16 z plane value into the
// internal 18-bit depth range. The command's z is an
// unsigned fraction spanning the full 32-bit field
// (z18 = value >> 14), so the shift is unsigned.
func clampZ18(zFixed int32) int32 {
	z := int32(uint32(zFixed) >> 14)
	if z > 0x3FFFF {
		return 0x3FFFF
	}
	return z
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// cmdTexRect rasterizes a Texture Rectangle or its
// flipped form. Word 0: XH (55..44), YH (43..32) in
// 10.2, tile (26..24), XL (23..12), YL (11..0). Word 1:
// S (63..48) and T (47..32) in signed 10.5, DsDx
// (31..16) and DtDy (15..0) in signed 5.10.
//
// Unflipped: S steps along X by DsDx and T steps along
// Y by DtDy. Flipped: the roles swap, S steps along Y
// by DtDy and T steps along X by DsDx. Positions are
// accumulated in 1/1024 texel units (S and T fields are
// 10.5, so raw * 32; the step fields are 5.10 already)
// and converted to the sampler's 1/32 units per pixel.
func (r *RDP) cmdTexRect(op int, w []uint64) {
	w0, w1 := w[0], w[1]
	xh := int((w0>>44)&0xFFF) >> 2
	yh := int((w0>>32)&0xFFF) >> 2
	tile := int((w0 >> 24) & 7)
	xl := int((w0>>12)&0xFFF) >> 2
	yl := int(w0&0xFFF) >> 2
	s0 := sext16(uint32(w1 >> 48))
	t0 := sext16(uint32(w1 >> 32))
	dsdx := sext16(uint32(w1 >> 16))
	dtdy := sext16(uint32(w1))

	ct := r.omCycleType()
	// Lower-right corner: inclusive in fill and copy
	// modes, exclusive in 1-cycle and 2-cycle modes.
	xEnd, yEnd := xl, yl
	if ct == 0 || ct == 1 {
		xEnd--
		yEnd--
	}
	flip := op == CmdTexRectFlip

	for y := yh; y <= yEnd; y++ {
		for x := xh; x <= xEnd; x++ {
			var sPos, tPos int32
			if !flip {
				sPos = (s0*32 + int32(x-xh)*dsdx) >> 5
				tPos = (t0*32 + int32(y-yh)*dtdy) >> 5
			} else {
				sPos = (s0*32 + int32(y-yh)*dtdy) >> 5
				tPos = (t0*32 + int32(x-xh)*dsdx) >> 5
			}
			f := &frag{
				x: x, y: y, cvg: 8, tile: tile,
				texS: sPos, texT: tPos,
				lodFrac: r.primLvlFrc,
			}
			if r.omZSourcePrim() {
				f.z = int32(r.primZ) << 2
			}
			r.plot(f)
		}
	}
}
