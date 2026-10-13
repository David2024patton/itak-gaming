package rdp

// The rasterizer and the per-pixel pipeline: combiner,
// blender, z test, coverage bookkeeping, and the color
// and depth image accesses in RDRAM.
//
// Model notes (all stated in the README as well):
//   - Y coordinates are 11.2 fixed point, so the
//     rasterizer is exact at quarter-pixel steps. Edge
//     X positions and slopes are 16.16.
//   - The given XH and XM edge positions sit at the
//     scanline above the first rendered one; the major
//     and upper minor edges step once before the first
//     rendered line. XL is given at the first scanline
//     at or below the mid vertex and steps after each
//     line from there; the mid line itself belongs to
//     the lower edge.
//   - Attributes (shade, texture, z) are evaluated with
//     the exact plane equations anchored at the top
//     vertex, using the command's Dx and Dy derivatives
//     (De is then implied; hardware steps De along the
//     major edge and gets the same plane).
//   - Coverage comes from horizontal fractional
//     coverage in eighths; vertical coverage is treated
//     as full because edges are line-snapped in this
//     model.
//   - Depth: internal z is 18-bit (z18). The stored
//     RDRAM word holds (z18 >> 2) in the high 14 bits
//     and a 2-bit dz code; the code decodes through
//     the table {16, 64, 256, 1024} in z18 units. That
//     dz compression is exact at only those four
//     magnitudes (documented approximation).

// frag carries one rasterized fragment into the pixel
// pipeline.
type frag struct {
	x, y int
	cvg  int // coverage 0..8

	shade RGBA

	texS, texT int32 // texture position, 1/32 texel units (pre-shift)
	lodLevel   int
	lodFrac    int // 0..255
	tile       int

	z  int32 // z18
	dz int32 // dz in z18 units

	keyScale int // chroma-key scale 0..255, valid when keyEn
}

// scissorYOK reports whether a line passes the field
// scissor rule.
func (r *RDP) inScissorY(y int) bool {
	if !r.sciField {
		return true
	}
	return (y&1 != 0) == r.sciOdd
}

// inScissor reports whether (x, y) is inside the
// scissor box. Coordinates in the box are 10.2; pixel
// bounds are [XH>>2, XL>>2) by [YH>>2, YL>>2).
func (r *RDP) inScissor(x, y int) bool {
	return x >= r.sciX0>>2 && x < r.sciX1>>2 &&
		y >= r.sciY0>>2 && y < r.sciY1>>2 && r.inScissorY(y)
}

// plot runs the full per-pixel pipeline for one
// fragment. fillMode writes the fill color directly
// (already handled by writeFillPixel); plot handles the
// 1-cycle, 2-cycle, and copy cycle types.
func (r *RDP) plot(f *frag) {
	if !r.inScissor(f.x, f.y) {
		return
	}
	ct := r.omCycleType()
	if ct == 3 { // fill
		r.writeFillPixel(f.x, f.y)
		return
	}

	// Texture fetch. texel0 uses the tile selected by
	// the LOD level when texture LOD is on; texel1 is
	// the next descriptor, fetched for 2-cycle mode.
	var tex0, tex1 RGBA
	needTex := f.tile >= 0
	if needTex {
		tile0 := f.tile
		if r.omTexLOD() {
			tile0 += f.lodLevel
			if tile0 > 7 {
				tile0 = 7
			}
		}
		t0 := r.tiles[tile0]
		tex0 = r.fetchFiltered(tile0,
			f.texS>>uint(t0.ShiftS), f.texT>>uint(t0.ShiftT),
			r.omBiLerp0())
		if ct == 1 {
			tile1 := tile0 + 1
			if tile1 > 7 {
				tile1 = 7
			}
			t1 := r.tiles[tile1]
			tex1 = r.fetchFiltered(tile1,
				f.texS>>uint(t1.ShiftS), f.texT>>uint(t1.ShiftT),
				r.omBiLerp1())
		}
	}

	if ct == 2 { // copy mode: texel straight to memory
		if r.omAlphaCompare() && int(tex0.A) < r.alphaThreshold(f) {
			return
		}
		r.writeColor(f.x, f.y, tex0, 8)
		return
	}

	// Chroma key: the filtered texel's alpha is replaced
	// by the key result, which also feeds KEYSCALE.
	if r.omKeyEn() && needTex {
		ka := r.keyAlpha(tex0)
		tex0.A = ka
		f.keyScale = int(ka)
	}

	cyc0, cyc1 := r.combine(f, tex0, tex1)
	out := cyc0
	if ct == 1 {
		out = cyc1
	}

	// Alpha compare discards under the blend color
	// alpha (or the dither value when alpha dither is
	// the noise mode).
	if r.omAlphaCompare() && int(out.A) < r.alphaThreshold(f) {
		return
	}

	// Z test.
	zPass := true
	var zPix, dzPix int32
	if r.omZSourcePrim() {
		zPix = int32(r.primZ) << 2
		dzPix = int32(r.primDZ) << 2
		if dzPix > 0x3FFFF {
			dzPix = 0x3FFFF
		}
	} else {
		zPix = f.z
		dzPix = f.dz
	}
	if r.omZCompare() {
		zOld, _ := r.readZ(f.x, f.y)
		switch r.omZMode() {
		case 0: // opaque
			zPass = zPix < zOld
		case 1, 3: // interpenetrating, decal
			zPass = zPix <= zOld+dzPix
		case 2: // transparent
			zPass = zPix < zOld
		}
	}
	if !zPass {
		return
	}

	// Read the destination.
	mem, memCvg := r.readColor(f.x, f.y)

	// Pixel alpha after the coverage adjustments the
	// blender sees.
	adjAlpha := func(c RGBA) int {
		a := int(c.A)
		if r.omCvgTimesAlpha() {
			a = a * f.cvg / 8
		} else if r.omAlphaCvgSel() {
			a = f.cvg * 255 / 8
		}
		return a
	}

	final := out
	if r.omImageRead() || r.omForceBlend() {
		passes := 1
		if ct == 1 {
			passes = 2
		}
		cur := mem
		curCvg := memCvg
		cycleCols := [2]RGBA{cyc0, cyc1}
		for p := 0; p < passes; p++ {
			pc := cycleCols[p]
			if p == 1 {
				// Second blend cycle: memory is the
				// first pass result with full coverage.
				curCvg = 8
			}
			final = r.blendPass(p, pc, adjAlpha(pc), cur, curCvg, f)
			cur = final
		}
	}

	// Coverage algebra at the destination.
	newCvg := combineCvg(memCvg, f.cvg, r.omCvgDest())

	colorWrite := true
	if r.omColorOnCvg() && newCvg < 8 {
		colorWrite = false // coverage only this pixel
	}
	if colorWrite {
		r.writeColor(f.x, f.y, final, newCvg)
	} else {
		r.storeCvg(f.x, f.y, newCvg)
	}

	if r.omZUpdate() {
		r.writeZ(f.x, f.y, zPix, dzPix)
	}
}

// alphaThreshold is the alpha-compare threshold for a
// fragment: the blend color alpha, or the per-pixel
// noise value when alpha dither selects noise.
func (r *RDP) alphaThreshold(f *frag) int {
	if r.omDitherAlpha() && r.omAlphaDither() == 2 {
		return int(noiseByte(f.x, f.y))
	}
	return int(r.blendColor.A)
}

// combineCvg folds incoming coverage into stored
// coverage per the cvg_dest mode.
func combineCvg(old, inc, dest int) int {
	switch dest {
	case 0: // clamp
		if old+inc > 8 {
			return 8
		}
		return old + inc
	case 1: // wrap
		return (old + inc) & 7
	case 2: // zap (full)
		return 8
	case 3: // save (unchanged)
		return old
	}
	return old + inc
}

// keyAlpha computes the chroma-key alpha for a texel
// from the Set Key registers. A channel whose width
// exceeds 1.0 (raw above 16 in s7.4) does not take part.
func (r *RDP) keyAlpha(c RGBA) uint8 {
	key := 255
	apply := func(v uint8, kc keyChan) {
		if kc.Width > 16 {
			return
		}
		diff := (int(v) - kc.Center) * kc.Scale / 255
		if diff < 0 {
			diff = -diff
		}
		ka := kc.Width*255/16 - diff
		if ka < key {
			key = ka
		}
	}
	apply(c.R, r.keyR)
	apply(c.G, keyChan{r.keyGB.WidthG, r.keyGB.CenterG, r.keyGB.ScaleG})
	apply(c.B, keyChan{r.keyGB.WidthB, r.keyGB.CenterB, r.keyGB.ScaleB})
	if key < 0 {
		key = 0
	}
	return uint8(key)
}

// combine runs the color combiner for both cycles and
// returns the two cycle outputs. In 1-cycle mode the
// second output equals the first.
func (r *RDP) combine(f *frag, tex0, tex1 RGBA) (RGBA, RGBA) {
	cw := r.combineReg
	fields := func(cycle int) (subAR, mulR, subBR, addR, subAA, mulA, subBA, addA int) {
		if cycle == 0 {
			return int((cw >> 52) & 0xF), int((cw >> 47) & 0x1F),
				int((cw >> 28) & 0xF), int((cw >> 15) & 7),
				int((cw >> 44) & 7), int((cw >> 41) & 7),
				int((cw >> 12) & 7), int((cw >> 9) & 7)
		}
		return int((cw >> 37) & 0xF), int((cw >> 32) & 0x1F),
			int((cw >> 24) & 0xF), int((cw >> 6) & 7),
			int((cw >> 21) & 7), int((cw >> 18) & 7),
			int((cw >> 3) & 7), int(cw & 7)
	}

	rgbIn := func(sel int, combined RGBA, haveCombined bool) RGBA {
		switch sel {
		case 0:
			if haveCombined {
				return combined
			}
			return RGBA{}
		case 1:
			return tex0
		case 2:
			return tex1
		case 3:
			return r.primColor
		case 4:
			return f.shade
		case 5:
			return r.envColor
		case 6:
			return RGBA{255, 255, 255, 255}
		case 7:
			n := noiseByte(f.x, f.y)
			return RGBA{n, n, n, n}
		}
		return RGBA{}
	}
	rgbMul := func(sel int, combined RGBA, haveCombined bool) RGBA {
		switch sel {
		case 0:
			if haveCombined {
				return combined
			}
			return RGBA{}
		case 1:
			return tex0
		case 2:
			return tex1
		case 3:
			return r.primColor
		case 4:
			return f.shade
		case 5:
			return r.envColor
		case 6:
			k := uint8(f.keyScale)
			return RGBA{k, k, k, k}
		case 7:
			if haveCombined {
				return gray(combined.A)
			}
			return RGBA{}
		case 8:
			return gray(tex0.A)
		case 9:
			return gray(tex1.A)
		case 10:
			return gray(r.primColor.A)
		case 11:
			return gray(f.shade.A)
		case 12:
			return gray(r.envColor.A)
		case 13:
			return gray(uint8(f.lodFrac))
		case 14:
			return gray(uint8(r.primLvlFrc))
		case 15:
			return gray(uint8(r.conv[5] << 1))
		}
		return RGBA{}
	}
	alphaIn := func(sel int, combined RGBA, haveCombined bool) int {
		switch sel {
		case 0:
			if haveCombined {
				return int(combined.A)
			}
			return 0
		case 1:
			return int(tex0.A)
		case 2:
			return int(tex1.A)
		case 3:
			return int(r.primColor.A)
		case 4:
			return int(f.shade.A)
		case 5:
			return int(r.envColor.A)
		case 6:
			return 255
		}
		return 0
	}
	alphaMul := func(sel int, combined RGBA, haveCombined bool) int {
		switch sel {
		case 0:
			return f.lodFrac
		case 1:
			return int(tex0.A)
		case 2:
			return int(tex1.A)
		case 3:
			return int(r.primColor.A)
		case 4:
			return int(f.shade.A)
		case 5:
			return int(r.envColor.A)
		case 6:
			return r.primLvlFrc
		}
		return 0
	}

	run := func(cycle int, combined RGBA, haveCombined bool) RGBA {
		subAR, mulR, subBR, addR, subAA, mulA, subBA, addA := fields(cycle)
		a := rgbIn(subAR, combined, haveCombined)
		b := rgbIn(subBR, combined, haveCombined)
		c := rgbMul(mulR, combined, haveCombined)
		d := rgbIn(addR, combined, haveCombined)
		aa := alphaIn(subAA, combined, haveCombined)
		ab := alphaIn(subBA, combined, haveCombined)
		ac := alphaMul(mulA, combined, haveCombined)
		ad := alphaIn(addA, combined, haveCombined)
		return RGBA{
			combChan(int(a.R), int(b.R), int(c.R), int(d.R)),
			combChan(int(a.G), int(b.G), int(c.G), int(d.G)),
			combChan(int(a.B), int(b.B), int(c.B), int(d.B)),
			combChan(aa, ab, ac, ad),
		}
	}

	c0 := run(0, RGBA{}, false)
	if r.omCycleType() != 1 {
		return c0, c0
	}
	c1 := run(1, c0, true)
	return c0, c1
}

// combChan is one combiner channel: clamp((A-B)*C/256
// + D, 0, 255) with floor semantics on the product.
func combChan(a, b, c, d int) uint8 {
	v := ((a-b)*c)>>8 + d
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func gray(v uint8) RGBA { return RGBA{v, v, v, v} }

// blendPass runs one blender pass: out = (P*A + M*B) /
// (A + B) per channel over the mux-selected inputs.
// When A + B is zero the result is P unchanged.
func (r *RDP) blendPass(cycle int, pixel RGBA, pixelAlpha int, mem RGBA, memCvg int, f *frag) RGBA {
	selC := func(sel int) RGBA {
		switch sel {
		case 0:
			return pixel
		case 1:
			return mem
		case 2:
			return r.blendColor
		case 3:
			return r.fogColor
		}
		return RGBA{}
	}
	p := selC(r.omBlendMux(cycle, 0))
	m := selC(r.omBlendMux(cycle, 1))
	var a int
	switch r.omBlendMux(cycle, 2) {
	case 0:
		a = pixelAlpha
	case 1:
		a = int(r.fogColor.A)
	case 2:
		a = int(f.shade.A)
	case 3:
		a = 0
	}
	var b int
	switch r.omBlendMux(cycle, 3) {
	case 0:
		b = 255 - a
	case 1:
		// Memory alpha: coverage for 16-bit images,
		// the stored alpha byte for 32-bit images.
		if r.colorImg.Size == Size16 {
			b = memCvg * 255 / 8
		} else {
			b = int(mem.A)
		}
	case 2:
		b = 255
	case 3:
		b = 0
	}
	if a+b == 0 {
		return p
	}
	mix := func(pc, mc uint8) uint8 {
		return uint8((int(pc)*a + int(mc)*b) / (a + b))
	}
	return RGBA{mix(p.R, m.R), mix(p.G, m.G), mix(p.B, m.B), mix(p.A, m.A)}
}

// noiseByte is the deterministic per-pixel noise used
// by the NOISE combiner input and the noise dither
// modes, so save/restore stays bit-identical (hardware
// noise comes from a free-running counter and is not
// reproducible even there).
func noiseByte(x, y int) uint8 {
	n := uint32(x)*374761393 + uint32(y)*668265263
	n = (n ^ (n >> 13)) * 1274126177
	return uint8((n >> 16) & 0xFF)
}

// readColor reads the destination pixel of the color
// image plus its coverage.
func (r *RDP) readColor(x, y int) (RGBA, int) {
	img := r.colorImg
	addr := img.Addr + uint32(y*img.Width+x)*uint32(texelBytes2(img.Size))/2
	switch img.Size {
	case Size16:
		v := r.dramRead16(addr)
		c := expandRGBA16(v)
		cvg := 8
		if idx := int(addr / 2); idx < len(r.hidden) {
			cvg = int(r.hidden[idx])
		}
		return c, cvg
	case Size32:
		v := r.dramRead32(addr)
		c := RGBA{uint8(v >> 24), uint8(v >> 16), uint8(v >> 8), uint8(v)}
		cvg := 8
		if r.omCvgDest() != 3 {
			cvg = int(v & 7)
			if cvg == 7 {
				cvg = 8
			}
		}
		return c, cvg
	case Size8:
		v := r.dramByte(addr)
		cvg := 8
		if idx := int(addr / 2); idx < len(r.hidden) {
			cvg = int(r.hidden[idx])
		}
		return RGBA{v, v, v, 255}, cvg
	}
	return RGBA{}, 8
}

// writeColor stores a pixel into the color image with
// its coverage, applying the programmed RGB dither when
// quantizing to 5-bit channels.
func (r *RDP) writeColor(x, y int, c RGBA, cvg int) {
	img := r.colorImg
	addr := img.Addr + uint32(y*img.Width+x)*uint32(texelBytes2(img.Size))/2
	switch img.Size {
	case Size16:
		d := r.ditherAdd(x, y)
		enc := func(v uint8) uint16 {
			return uint16(clamp255(int32(v)+d)) >> 3
		}
		a := uint16(0)
		if c.A >= 128 {
			a = 1
		}
		r.dramWrite16(addr, enc(c.R)<<11|enc(c.G)<<6|enc(c.B)<<1|a)
		r.storeCvg(x, y, cvg)
	case Size32:
		a := c.A
		if r.omCvgDest() != 3 {
			bits := uint8(cvg)
			if bits > 7 {
				bits = 7
			}
			a = a&^7 | bits
		}
		r.dramWrite32(addr, uint32(c.R)<<24|uint32(c.G)<<16|uint32(c.B)<<8|uint32(a))
	case Size8:
		r.dramWriteByte(addr, c.R)
		r.storeCvg(x, y, cvg)
	}
}

// storeCvg writes coverage alone (the color-on-coverage
// case and 16/8-bit pixels written by other paths).
func (r *RDP) storeCvg(x, y int, cvg int) {
	img := r.colorImg
	if img.Size == Size32 {
		addr := img.Addr + uint32(y*img.Width+x)*4
		v := r.dramRead32(addr)
		bits := uint32(cvg)
		if bits > 7 {
			bits = 7
		}
		r.dramWrite32(addr, v&^7|bits)
		return
	}
	addr := img.Addr + uint32(y*img.Width+x)*uint32(texelBytes2(img.Size))/2
	if idx := int(addr / 2); idx < len(r.hidden) {
		r.hidden[idx] = uint8(cvg)
	}
}

// writeFillPixel writes the packed fill color at one
// pixel, following the documented fill packing: for a
// 16-bit image the 32-bit fill word holds two adjacent
// pixels (even x takes the high half), for an 8-bit
// image four pixels (first pixel in the top byte), and
// for a 32-bit image the word is the pixel.
func (r *RDP) writeFillPixel(x, y int) {
	img := r.colorImg
	switch img.Size {
	case Size16:
		addr := img.Addr + uint32(y*img.Width+x)*2
		// Fill writes the full word-pair atomically in
		// hardware; here a single pixel write places
		// its half from the fill word, and r.fillSpan
		// covers both halves across the span.
		var v uint16
		if x%2 == 0 {
			v = uint16(r.fillColor >> 16)
		} else {
			v = uint16(r.fillColor)
		}
		r.dramWrite16(addr, v)
		if idx := int(addr / 2); idx < len(r.hidden) {
			r.hidden[idx] = 8
		}
	case Size32:
		addr := img.Addr + uint32(y*img.Width+x)*4
		r.dramWrite32(addr, r.fillColor)
	case Size8:
		addr := img.Addr + uint32(y*img.Width+x)
		shift := uint(24 - 8*(x%4))
		r.dramWriteByte(addr, byte(r.fillColor>>shift))
		if idx := int(addr / 2); idx < len(r.hidden) {
			r.hidden[idx] = 8
		}
	}
}

// ditherAdd returns the dither offset (in 8-bit channel
// units, 0..7 range scaled) for the RGB dither mode at
// a pixel: magic-square matrix for mode 0, Bayer for
// mode 1, noise for mode 2, none for mode 3. The offset
// is added before the 8 to 5 bit truncation by the
// caller. Only applied to 16-bit image writes.
func (r *RDP) ditherAdd(x, y int) int32 {
	switch r.omRGBDither() {
	case 0:
		return int32(magicSquare[y&3][x&3]) >> 2 // 0..15 -> 0..3
	case 1:
		return int32(bayer4[y&3][x&3]) >> 2
	case 2:
		return int32(noiseByte(x, y) >> 6) // 0..3
	}
	return 0
}

var bayer4 = [4][4]uint8{
	{0, 8, 2, 10},
	{12, 4, 14, 6},
	{3, 11, 1, 9},
	{15, 7, 13, 5},
}

var magicSquare = [4][4]uint8{
	{0, 14, 3, 13},
	{11, 5, 8, 6},
	{12, 2, 15, 1},
	{7, 9, 4, 10},
}

// dzTable decodes the 2-bit stored dz code to z18
// units.
var dzTable = [4]int32{16, 64, 256, 1024}

// readZ reads the stored depth at a pixel: the internal
// 18-bit z and the stored dz (decoded).
func (r *RDP) readZ(x, y int) (int32, int32) {
	addr := r.zAddr + uint32(y*r.colorImg.Width+x)*2
	w := r.dramRead16(addr)
	z := int32(w&0xFFFC) << 2
	return z, dzTable[w&3]
}

// writeZ stores a pixel depth with its dz compressed
// to the 2-bit code (the smallest code whose value is
// at least the dz, or the largest code).
func (r *RDP) writeZ(x, y int, z, dz int32) {
	addr := r.zAddr + uint32(y*r.colorImg.Width+x)*2
	code := 3
	for i, tv := range dzTable {
		if dz <= tv {
			code = i
			break
		}
	}
	w := uint16((z >> 2) & 0xFFFC)
	r.dramWrite16(addr, w|uint16(code))
}
