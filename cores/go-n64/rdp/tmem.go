package rdp

// TMEM loads and texture sampling. TMEM is 4 KB,
// addressed in 64-bit words (0..511). The documented
// odd-line 32-bit half interleave is a TMEM banking
// artifact that only matters to software that pokes
// texture memory and expects the pattern back; loads
// here write the logical texel layout and fetches read
// the same layout, so Load Tile and Load Block agree
// and sampling is exact for normal images. That
// simplification is stated in the README.

// texelBytes returns the byte width of one texel of a
// size class, as a numerator over 2 (4-bit counts half).
func texelBytes2(size int) int {
	switch size {
	case Size4:
		return 1 // 1/2 byte
	case Size8:
		return 2 // 1 byte
	case Size16:
		return 4 // 2 bytes
	case Size32:
		return 8 // 4 bytes
	}
	return 2
}

// imgByteOffset converts a texel index in a line-major
// image of the given width (pixels) and size to a byte
// offset.
func imgByteOffset(idx, size int) int {
	return idx * texelBytes2(size) / 2
}

// loadBlock implements Load Block. The image span of
// (SH - SL + 1) texels starting at (SL, TL) in the
// texture image is copied into TMEM at the tile base.
// DxT (unsigned 1.11) is the amount the source T
// coordinate advances per 8 TMEM bytes written; each
// time the accumulated T crosses an integer, the source
// moves to the next image line at the same SL. DxT is
// programmed as the ceiling of the true reciprocal of
// the line length in 64-bit words for full-width loads,
// which makes the walk land line-exact.
func (r *RDP) loadBlock(tileIdx, dxt int) {
	t := &r.tiles[tileIdx]
	img := r.texImg
	if img.Width <= 0 {
		return
	}
	texels := t.SH - t.SL + 1
	if texels <= 0 {
		return
	}
	totalBytes := texels * texelBytes2(img.Size) / 2
	lineBytes := imgByteOffset(img.Width, img.Size)
	srcRow := imgByteOffset(t.TL*img.Width+t.SL, img.Size)
	dst := t.TMem * 8
	tAcc := 0
	srcInRow := 0 // bytes consumed in the current source row
	for written := 0; written < totalBytes; written += 8 {
		n := totalBytes - written
		if n > 8 {
			n = 8
		}
		for i := 0; i < n; i++ {
			r.tmem[(dst+written+i)&0xFFF] =
				r.dramByte(img.Addr + uint32(srcRow+srcInRow+i))
		}
		srcInRow += 8
		if dxt > 0 {
			tAcc += dxt
			if tAcc >= 0x800 {
				tAcc -= 0x800
				srcRow += lineBytes
				srcInRow = 0
			}
		}
	}
}

// loadTile implements Load Tile: the rectangle of the
// texture image at the tile's SL/TL..SH/TH (10.2 units,
// so texel bounds are the raw values shifted right by
// 2) is copied into TMEM in the tile's own format and
// size, laid out by the tile's line stride.
func (r *RDP) loadTile(tileIdx int) {
	t := &r.tiles[tileIdx]
	img := r.texImg
	sl, tl := t.SL>>2, t.TL>>2
	sh, th := t.SH>>2, t.TH>>2
	sameFormat := t.Format == img.Format && t.Size == img.Size
	for ty := tl; ty <= th; ty++ {
		for sx := sl; sx <= sh; sx++ {
			if sameFormat {
				// The normal case: a straight texel move.
				r.writeTMEMRaw(t, sx-sl, ty-tl,
					r.readImageRaw(img, sx, ty))
			} else {
				v := r.readImageTexel(img, sx, ty)
				r.writeTMEMTexel(t, sx-sl, ty-tl, v)
			}
		}
	}
}

// loadTLUT implements Load TLUT. The palette entries
// starting at (SL>>2, TL>>2) in the texture image are
// stored in TMEM at the tile base. Following the
// documented quadrication, each 16-bit entry occupies
// a full 64-bit word with the value repeated in all
// four halfword slots; fetches read the first slot.
func (r *RDP) loadTLUT(tileIdx int) {
	t := &r.tiles[tileIdx]
	img := r.texImg
	count := (t.SH >> 2) - (t.SL >> 2) + 1
	if count <= 0 {
		return
	}
	base := imgByteOffset((t.TL>>2)*img.Width+(t.SL>>2), img.Size)
	dst := t.TMem * 8
	for i := 0; i < count; i++ {
		entry := r.dramRead16(img.Addr + uint32(base+i*2))
		for k := 0; k < 4; k++ {
			off := (dst + i*8 + k*2) & 0xFFF
			r.tmem[off] = byte(entry >> 8)
			r.tmem[off+1] = byte(entry)
		}
	}
}

// readImageTexel reads one texel of an image in RDRAM
// and returns it expanded to RGBA in the image's own
// format and size, without TLUT lookup (a CI image
// expands its index through the palette only on the
// sampling path) and with YUV kept in component form.
func (r *RDP) readImageTexel(img Image, x, y int) RGBA {
	return r.expandSample(img.Format, img.Size,
		r.readImageRaw(img, x, y), 0, 0, true)
}

// readImageRaw reads the packed texel bits of one image
// texel: for 4-bit sizes the low bits hold the texel
// value, for 8-bit the byte, for 16-bit the halfword,
// for 32-bit the word.
func (r *RDP) readImageRaw(img Image, x, y int) uint32 {
	off := imgByteOffset(y*img.Width+x, img.Size)
	switch img.Size {
	case Size4:
		b := r.dramByte(img.Addr + uint32(off))
		if (y*img.Width+x)%2 == 0 {
			return uint32(b >> 4)
		}
		return uint32(b & 0xF)
	case Size8:
		return uint32(r.dramByte(img.Addr + uint32(off)))
	case Size16:
		return uint32(r.dramRead16(img.Addr + uint32(off)))
	case Size32:
		return r.dramRead32(img.Addr + uint32(off))
	}
	return 0
}

// writeTMEMTexel stores one texel (already expanded to
// RGBA from its source format) into TMEM through the
// tile's format and size packing, at tile-relative
// coordinates.
func (r *RDP) writeTMEMTexel(t *Tile, x, y int, c RGBA) {
	v := packTexel(t.Format, t.Size, c)
	r.writeTMEMRaw(t, x, y, v)
}

// writeTMEMRaw packs a raw texel value into TMEM at
// tile-relative coordinates.
func (r *RDP) writeTMEMRaw(t *Tile, x, y int, v uint32) {
	switch t.Size {
	case Size4:
		idx := y*(t.Line*16) + x
		off := t.TMem*8 + idx/2
		b := r.tmem[off&0xFFF]
		if idx%2 == 0 {
			b = b&0x0F | byte(v)<<4
		} else {
			b = b&0xF0 | byte(v&0xF)
		}
		r.tmem[off&0xFFF] = b
	case Size8:
		off := t.TMem*8 + y*(t.Line*8) + x
		r.tmem[off&0xFFF] = byte(v)
	case Size16:
		off := t.TMem*8 + (y*(t.Line*4)+x)*2
		r.tmem[off&0xFFF] = byte(v >> 8)
		r.tmem[(off+1)&0xFFF] = byte(v)
	case Size32:
		off := t.TMem*8 + (y*(t.Line*2)+x)*4
		for i := 0; i < 4; i++ {
			r.tmem[(off+i)&0xFFF] = byte(v >> uint(24-8*i))
		}
	}
}

// readTMEMRaw reads a raw texel value from TMEM at
// tile-relative coordinates.
func (r *RDP) readTMEMRaw(t *Tile, x, y int) uint32 {
	switch t.Size {
	case Size4:
		idx := y*(t.Line*16) + x
		b := r.tmem[(t.TMem*8+idx/2)&0xFFF]
		if idx%2 == 0 {
			return uint32(b >> 4)
		}
		return uint32(b & 0xF)
	case Size8:
		return uint32(r.tmem[(t.TMem*8+y*(t.Line*8)+x)&0xFFF])
	case Size16:
		off := t.TMem*8 + (y*(t.Line*4)+x)*2
		return uint32(r.tmem[off&0xFFF])<<8 | uint32(r.tmem[(off+1)&0xFFF])
	case Size32:
		off := t.TMem*8 + (y*(t.Line*2)+x)*4
		var v uint32
		for i := 0; i < 4; i++ {
			v = v<<8 | uint32(r.tmem[(off+i)&0xFFF])
		}
		return v
	}
	return 0
}

// packTexel quantizes an RGBA color back to a format
// and size's packed bits (used by Load Tile when the
// source format differs, and by 32-bit image writes).
func packTexel(format, size int, c RGBA) uint32 {
	switch size {
	case Size32:
		return uint32(c.R)<<24 | uint32(c.G)<<16 | uint32(c.B)<<8 | uint32(c.A)
	case Size16:
		if format == FmtIA {
			return uint32(c.R)<<8 | uint32(c.A)
		}
		a := uint32(0)
		if c.A >= 128 {
			a = 1
		}
		return uint32(c.R>>3)<<11 | uint32(c.G>>3)<<6 | uint32(c.B>>3)<<1 | a
	case Size8:
		if format == FmtIA {
			return uint32(c.R&0xF0) | uint32(c.A>>4)
		}
		return uint32(c.R)
	case Size4:
		if format == FmtIA {
			return uint32(c.R>>5)<<1 | uint32(c.A>>7)
		}
		return uint32(c.R >> 4)
	}
	return 0
}

// expandSample expands a raw TMEM or image texel to
// sampling path, including TLUT lookup and the YUV
// conversion. yuvPartner carries the paired texel's
// raw value for YUV chroma sharing when needed.
func (r *RDP) expandSample(format, size int, raw uint32, palette int, partner uint32, forLoad bool) RGBA {
	switch format {
	case FmtRGBA:
		if size == Size16 {
			return expandRGBA16(uint16(raw))
		}
		return RGBA{uint8(raw >> 24), uint8(raw >> 16), uint8(raw >> 8), uint8(raw)}
	case FmtYUV:
		// 16-bit YUYV pairs: even texel holds Y0 in the
		// high byte, odd holds Y1; U and V are shared.
		y := uint8(raw >> 8)
		u := uint8(raw)
		v := uint8(partner)
		if forLoad {
			return RGBA{y, u, v, 255}
		}
		return r.convertYUV(y, u, v)
	case FmtCI:
		idx := raw
		if !r.omEnTLUT() {
			// Index shown as intensity (games always
			// enable the TLUT for CI; this keeps the
			// result defined).
			g := uint8(idx)
			if size == Size4 {
				g = uint8(idx * 17)
			}
			return RGBA{g, g, g, 255}
		}
		var entryAddr int
		if size == Size4 {
			entryAddr = 2048 + (palette*16+int(idx))*8
		} else {
			entryAddr = 2048 + int(idx)*8
		}
		entry := uint16(r.tmem[entryAddr&0xFFF])<<8 | uint16(r.tmem[(entryAddr+1)&0xFFF])
		if r.omTLUTType() == 1 {
			return RGBA{uint8(entry >> 8), uint8(entry >> 8), uint8(entry >> 8), uint8(entry)}
		}
		return expandRGBA16(entry)
	case FmtIA:
		switch size {
		case Size4:
			i3 := (raw >> 1) & 7
			inten := uint8((i3 << 5) | (i3 << 2) | (i3 >> 1))
			a := uint8(0)
			if raw&1 != 0 {
				a = 255
			}
			return RGBA{inten, inten, inten, a}
		case Size8:
			i := uint8((raw >> 4) * 17)
			a := uint8((raw & 0xF) * 17)
			return RGBA{i, i, i, a}
		case Size16:
			i := uint8(raw >> 8)
			return RGBA{i, i, i, uint8(raw)}
		}
	case FmtI:
		if size == Size4 {
			g := uint8(raw * 17)
			return RGBA{g, g, g, 255}
		}
		g := uint8(raw)
		return RGBA{g, g, g, 255}
	}
	return RGBA{}
}

// expandRGBA16 expands an RGBA5551 halfword.
func expandRGBA16(v uint16) RGBA {
	r5 := uint8((v >> 11) & 31)
	g5 := uint8((v >> 6) & 31)
	b5 := uint8((v >> 1) & 31)
	a := uint8(0)
	if v&1 != 0 {
		a = 255
	}
	return RGBA{(r5 << 3) | (r5 >> 2), (g5 << 3) | (g5 >> 2), (b5 << 3) | (b5 >> 2), a}
}

// convertYUV applies the documented two-stage YUV to
// RGB conversion: the texture-filter stage forms
// R' = Y + K0*V, G' = Y + K1*U + K2*V, B' = Y + K3*U
// with U and V centered on zero, and the combiner
// stage forms out = (in - K4) * K5 + in per channel.
// The K coefficients are signed 1.7 fixed point.
func (r *RDP) convertYUV(y, u, v uint8) RGBA {
	uc := int32(u) - 128
	vc := int32(v) - 128
	yi := int32(y)
	rp := yi + (r.conv[0]*vc)>>7
	gp := yi + (r.conv[1]*uc)>>7 + (r.conv[2]*vc)>>7
	bp := yi + (r.conv[3]*uc)>>7
	stage2 := func(c int32) uint8 {
		o := ((c - r.conv[4]) * r.conv[5]) >> 7
		return clamp255(c + o)
	}
	return RGBA{stage2(rp), stage2(gp), stage2(bp), 255}
}

// clamp255 clamps an int32 to the byte range.
func clamp255(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// wrapCoord applies one axis' addressing mode to an
// integer texel coordinate. With a zero mask the
// coordinate clamps to the tile's own coordinate range
// [lo, hi] (the Set Tile Size bounds). With a mask the
// coordinate wraps modulo 2^mask, mirror flips alternate
// blocks, and the clamp bit (when set) clamps the
// result to [lo, hi] afterwards.
func wrapCoord(c, mask int, clampEn, mirrorEn bool, lo, hi int) int {
	if mask == 0 {
		if c < lo {
			return lo
		}
		if c > hi {
			return hi
		}
		return c
	}
	span := 1 << uint(mask)
	v := c & (span - 1)
	if mirrorEn && (c&(span)) != 0 {
		v = span - 1 - v
	}
	if clampEn {
		if v < lo {
			v = lo
		}
		if v > hi {
			v = hi
		}
	}
	return v
}

// fetchTexel reads one fully expanded texel of the tile
// at possibly out-of-range integer coordinates, applying
// the tile's wrap and mirror modes. This is the nearest
// (point) fetch; the filter builds on it.
func (r *RDP) fetchTexel(tileIdx, s, t int) RGBA {
	tile := &r.tiles[tileIdx]
	lo := tile.SL >> 2
	hi := tile.SH >> 2
	s = wrapCoord(s, tile.MaskS, tile.ClampS, tile.MirrorS, lo, hi)
	lo = tile.TL >> 2
	hi = tile.TH >> 2
	t = wrapCoord(t, tile.MaskT, tile.ClampT, tile.MirrorT, lo, hi)
	// Tile-relative coordinates into TMEM.
	rs := s - (tile.SL >> 2)
	rt := t - (tile.TL >> 2)

	if tile.Format == FmtYUV {
		// Chroma is shared across the horizontal pair:
		// the even texel's raw holds Y0,U and the odd
		// texel's holds Y1,V in the 16-bit packing used
		// here (Y in the high byte; the second byte of
		// the pair word is U for even, V for odd).
		evenRaw := r.readTMEMRaw(tile, rs&^1, rt)
		oddRaw := r.readTMEMRaw(tile, rs|1, rt)
		y := uint8(evenRaw >> 8)
		if rs&1 != 0 {
			y = uint8(oddRaw >> 8)
		}
		return r.expandSample(FmtYUV, tile.Size,
			uint32(y)<<8|uint32(uint8(evenRaw)), tile.Palette,
			uint32(uint8(oddRaw)), false)
	}
	raw := r.readTMEMRaw(tile, rs, rt)
	return r.expandSample(tile.Format, tile.Size, raw, tile.Palette, 0, false)
}

// fetchFiltered samples a tile at a position given in
// 1/32 texel units (already shifted by the tile's shift
// fields by the caller). sampleType selects the hardware
// 2x2 mode; bilerp selects whether the filter runs the
// three-texel triangle blend for this pass or the
// color-convert operation (modeled as point sampling
// for non-YUV formats; YUV conversion happens in the
// fetch itself).
func (r *RDP) fetchFiltered(tileIdx int, sPos, tPos int32, bilerp bool) RGBA {
	if r.omSampleType() == 0 || !bilerp || r.omCycleType() == 3 {
		return r.fetchTexel(tileIdx, floorDiv32(sPos), floorDiv32(tPos))
	}
	// Three-texel triangle filter: the 2x2 block around
	// the sample center contributes its three texels on
	// the sample's side of the diagonal, weighted by
	// the fractional position (in 1/32 units).
	s0 := floorDiv32(sPos)
	t0 := floorDiv32(tPos)
	fx := int(sPos - int32(s0)*32)
	fy := int(tPos - int32(t0)*32)
	p00 := r.fetchTexel(tileIdx, s0, t0)
	p10 := r.fetchTexel(tileIdx, s0+1, t0)
	p01 := r.fetchTexel(tileIdx, s0, t0+1)
	p11 := r.fetchTexel(tileIdx, s0+1, t0+1)
	blend3 := func(w00, w10, w01, w11 int, get func(RGBA) uint8) uint8 {
		sum := w00*int(get(p00)) + w10*int(get(p10)) +
			w01*int(get(p01)) + w11*int(get(p11))
		return uint8((sum + 16) >> 5)
	}
	if fx+fy <= 32 {
		return RGBA{
			blend3(32-fx-fy, fx, fy, 0, func(c RGBA) uint8 { return c.R }),
			blend3(32-fx-fy, fx, fy, 0, func(c RGBA) uint8 { return c.G }),
			blend3(32-fx-fy, fx, fy, 0, func(c RGBA) uint8 { return c.B }),
			blend3(32-fx-fy, fx, fy, 0, func(c RGBA) uint8 { return c.A }),
		}
	}
	return RGBA{
		blend3(0, 32-fy, 32-fx, fx+fy-32, func(c RGBA) uint8 { return c.R }),
		blend3(0, 32-fy, 32-fx, fx+fy-32, func(c RGBA) uint8 { return c.G }),
		blend3(0, 32-fy, 32-fx, fx+fy-32, func(c RGBA) uint8 { return c.B }),
		blend3(0, 32-fy, 32-fx, fx+fy-32, func(c RGBA) uint8 { return c.A }),
	}
}

// floorDiv32 floors a 1/32-unit position to its texel.
func floorDiv32(v int32) int {
	if v >= 0 {
		return int(v >> 5)
	}
	return int(-(((-v) + 31) >> 5))
}
