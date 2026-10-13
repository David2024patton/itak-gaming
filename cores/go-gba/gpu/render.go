package gpu

// One scanline of rendering: candidate gathering from
// the background and sprite layers, window masking,
// winner selection, colour effects and output packing.

// candidate is one layer offering a pixel. The colour is
// kept as a raw 15 bit value so effect maths can work
// in 5 bit channels.
type candidate struct {
	present bool
	col     uint16
	layer   int
	prio    int
	semi    bool
}

// candRank orders candidates that share a priority
// number: sprites first, then backgrounds in index
// order, and the backdrop below everything.
func candRank(c candidate) int {
	switch {
	case c.layer == layerOBJ:
		return 0
	case c.layer == layerBD:
		return 99
	default:
		return 1 + c.layer
	}
}

// candBetter reports whether a outranks b.
func candBetter(a, b candidate) bool {
	if a.prio != b.prio {
		return a.prio < b.prio
	}
	return candRank(a) < candRank(b)
}

type picker struct {
	best   candidate
	second candidate
}

func (p *picker) consider(c candidate) {
	if !c.present {
		return
	}
	if !p.best.present || candBetter(c, p.best) {
		p.second = p.best
		p.best = c
		return
	}
	if !p.second.present || candBetter(c, p.second) {
		p.second = c
	}
}

// Backing store reads, all bounds checked.

func (g *GPU) vram8(off int) (byte, bool) {
	if off < 0 || off >= len(g.vram) {
		return 0, false
	}
	return g.vram[off], true
}

func (g *GPU) vram16(off int) (uint16, bool) {
	lo, ok1 := g.vram8(off)
	hi, ok2 := g.vram8(off + 1)
	if !ok1 || !ok2 {
		return 0, false
	}
	return uint16(lo) | uint16(hi)<<8, true
}

func (g *GPU) palAt(byteOff int) uint16 {
	if byteOff < 0 || byteOff+1 >= len(g.palette) {
		if byteOff >= 0 && byteOff < len(g.palette) {
			return uint16(g.palette[byteOff])
		}
		return 0
	}
	return uint16(g.palette[byteOff]) | uint16(g.palette[byteOff+1])<<8
}

// bgPal returns the background palette colour for a
// palette index in the range 0 to 255.
func (g *GPU) bgPal(idx int) uint16 { return g.palAt(idx * 2) }

// objPal returns the sprite palette colour for a
// palette index in the range 0 to 255.
func (g *GPU) objPal(idx int) uint16 { return g.palAt(0x200 + idx*2) }

func (g *GPU) oam16(off int) uint16 {
	if off < 0 || off+1 >= len(g.oam) {
		return 0
	}
	return uint16(g.oam[off]) | uint16(g.oam[off+1])<<8
}

// Register convenience readers used while rendering.

func (g *GPU) dispcnt() uint16 { return g.reg16(regDISPCNT) }

func (g *GPU) bgcnt(bg int) uint16 {
	return g.reg16(uint32(regBG0CNT + bg*2))
}

// affineParam returns one of the four affine matrix
// values (indices 0 to 3 are PA, PB, PC, PD) for a
// background, as a signed 8.8 fixed point number.
func (g *GPU) affineParam(bg, idx int) int16 {
	base := uint32(0x20)
	if bg == 3 {
		base = 0x30
	}
	return int16(g.reg16(base + uint32(idx*2)))
}

// stepRefs advances both live affine reference points
// by their per line steps, wrapping to signed 28 bits.
func (g *GPU) stepRefs() {
	for i, bg := range []int{2, 3} {
		pb := int32(g.affineParam(bg, 1))
		pd := int32(g.affineParam(bg, 3))
		g.refX[i] = sign28(uint32(g.refX[i] + pb))
		g.refY[i] = sign28(uint32(g.refY[i] + pd))
	}
}

// Positive modulo for wrapping coordinates.
func wrapMod(v, n int) int {
	r := v % n
	if r < 0 {
		r += n
	}
	return r
}

// Text backgrounds.

// textPixel fetches the colour offered by text
// background bg at screen column x on the current line,
// reporting false for a transparent texel.
func (g *GPU) textPixel(bg, x int) (uint16, bool) {
	cnt := g.bgcnt(bg)
	charBase := int((cnt>>2)&3) * 0x4000
	screenBase := int((cnt>>8)&0x1F) * 0x800
	size := (cnt >> 14) & 3

	mapW, mapH := 256, 256
	switch size {
	case 1:
		mapW = 512
	case 2:
		mapH = 512
	case 3:
		mapW, mapH = 512, 512
	}

	mx := wrapMod(x+int(g.latchedH[bg]), mapW)
	my := wrapMod(g.line+int(g.latchedV[bg]), mapH)

	if cnt&(1<<6) != 0 {
		mosaic := g.reg16(regMOSAIC)
		hs := int((mosaic>>8)&0xF) + 1
		vs := int((mosaic>>12)&0xF) + 1
		mx -= mx % hs
		my -= my % vs
	}

	blocksPerRow := mapW / 256
	block := (mx >> 8) + (my>>8)*blocksPerRow
	bx := mx & 0xFF
	by := my & 0xFF
	entryOff := screenBase + block*0x800 + ((by>>3)*32+(bx>>3))*2
	entry, ok := g.vram16(entryOff)
	if !ok {
		return 0, false
	}
	tile := int(entry & 0x3FF)
	px := mx & 7
	py := my & 7
	if entry&(1<<10) != 0 {
		px = 7 - px
	}
	if entry&(1<<11) != 0 {
		py = 7 - py
	}
	if cnt&(1<<7) != 0 {
		off := charBase + tile*64 + py*8 + px
		idx, ok := g.vram8(off)
		if !ok || idx == 0 {
			return 0, false
		}
		return g.bgPal(int(idx)), true
	}
	off := charBase + tile*32 + py*4 + px/2
	b, ok := g.vram8(off)
	if !ok {
		return 0, false
	}
	var nib int
	if px&1 == 0 {
		nib = int(b & 0xF)
	} else {
		nib = int(b >> 4)
	}
	if nib == 0 {
		return 0, false
	}
	bank := int((entry >> 12) & 0xF)
	return g.bgPal(bank*16 + nib), true
}

// Affine backgrounds.

// affineTex computes the texture coordinates of screen
// column x for an affine background, using the live
// reference point and the per pixel steps PA and PC.
func (g *GPU) affineTex(bg, x int) (int, int) {
	i := bg - 2
	pa := int32(g.affineParam(bg, 0))
	pc := int32(g.affineParam(bg, 2))
	texX := int((g.refX[i] + pa*int32(x)) >> 8)
	texY := int((g.refY[i] + pc*int32(x)) >> 8)
	return texX, texY
}

func (g *GPU) affinePixel(bg, x int) (uint16, bool) {
	cnt := g.bgcnt(bg)
	charBase := int((cnt>>2)&3) * 0x4000
	screenBase := int((cnt>>8)&0x1F) * 0x800
	size := 128 << ((cnt >> 14) & 3)
	wrap := cnt&(1<<13) != 0

	texX, texY := g.affineTex(bg, x)
	if texX < 0 || texX >= size || texY < 0 || texY >= size {
		if !wrap {
			return 0, false
		}
		texX = wrapMod(texX, size)
		texY = wrapMod(texY, size)
	}
	tilesPerRow := size / 8
	entryOff := screenBase + (texY>>3)*tilesPerRow + (texX >> 3)
	tileByte, ok := g.vram8(entryOff)
	if !ok {
		return 0, false
	}
	off := charBase + int(tileByte)*64 + (texY&7)*8 + (texX & 7)
	idx, ok := g.vram8(off)
	if !ok || idx == 0 {
		return 0, false
	}
	return g.bgPal(int(idx)), true
}

// bitmapPixel fetches BG2 in the direct and indexed
// bitmap modes (3, 4 and 5).
func (g *GPU) bitmapPixel(mode, x int) (uint16, bool) {
	cnt := g.bgcnt(2)
	wrap := cnt&(1<<13) != 0
	pageBase := 0
	if g.dispcnt()&(1<<4) != 0 {
		pageBase = 0xA000
	}
	texX, texY := g.affineTex(2, x)
	switch mode {
	case 3:
		if texX < 0 || texX >= 240 || texY < 0 || texY >= 160 {
			if !wrap {
				return 0, false
			}
			texX = wrapMod(texX, 240)
			texY = wrapMod(texY, 160)
		}
		col, ok := g.vram16((texY*240 + texX) * 2)
		if !ok {
			return 0, false
		}
		return col, true
	case 4:
		if texX < 0 || texX >= 240 || texY < 0 || texY >= 160 {
			if !wrap {
				return 0, false
			}
			texX = wrapMod(texX, 240)
			texY = wrapMod(texY, 160)
		}
		idx, ok := g.vram8(pageBase + texY*240 + texX)
		if !ok || idx == 0 {
			return 0, false
		}
		return g.bgPal(int(idx)), true
	case 5:
		if texX < 0 || texX >= 160 || texY < 0 || texY >= 128 {
			if !wrap {
				return 0, false
			}
			texX = wrapMod(texX, 160)
			texY = wrapMod(texY, 128)
		}
		col, ok := g.vram16(pageBase + (texY*160+texX)*2)
		if !ok {
			return 0, false
		}
		return col, true
	}
	return 0, false
}

// Sprites.

// Sprite sizes by shape then size index: width, height.
var objSizes = [3][4][2]int{
	{{8, 8}, {16, 16}, {32, 32}, {64, 64}}, // square
	{{16, 8}, {32, 8}, {32, 16}, {64, 32}}, // wide
	{{8, 16}, {8, 32}, {16, 32}, {32, 64}}, // tall
}

type objLine struct {
	pix    [Width]candidate
	winMsk [Width]bool
}

// objTilePixel fetches one source pixel of a sprite.
func (g *GPU) objTilePixel(tileNum, srcX, srcY, widthTiles int, color256 bool, bank int) (uint16, bool) {
	step := 1
	if color256 {
		step = 2
	}
	var tileNumber int
	if g.dispcnt()&(1<<6) != 0 {
		// One dimensional: tile numbers run on through
		// the sprite area row by row.
		tileNumber = tileNum + (srcY>>3)*widthTiles*step + (srcX>>3)*step
	} else {
		// Two dimensional: each sprite row starts 32
		// tile numbers after the previous one.
		tileNumber = tileNum + (srcY>>3)*32 + (srcX>>3)*step
	}
	base := 0x10000
	if mode := g.dispcnt() & 7; mode >= 3 && mode <= 5 {
		base = 0x14000
	}
	off := base + tileNumber*32
	tx := srcX & 7
	ty := srcY & 7
	if color256 {
		idx, ok := g.vram8(off + ty*8 + tx)
		if !ok || idx == 0 {
			return 0, false
		}
		return g.objPal(int(idx)), true
	}
	b, ok := g.vram8(off + ty*4 + tx/2)
	if !ok {
		return 0, false
	}
	var nib int
	if tx&1 == 0 {
		nib = int(b & 0xF)
	} else {
		nib = int(b >> 4)
	}
	if nib == 0 {
		return 0, false
	}
	return g.objPal(bank*16 + nib), true
}

// buildObjLine resolves every sprite pixel that lands
// on the current line, and the sprite window mask.
func (g *GPU) buildObjLine() objLine {
	var out objLine
	mode := int(g.dispcnt() & 7)
	_ = mode
	mosaic := g.reg16(regMOSAIC)
	mosH := int(mosaic&0xF) + 1
	mosV := int((mosaic>>4)&0xF) + 1

	for i := 0; i < 128; i++ {
		base := i * 8
		if base+5 >= len(g.oam) {
			break
		}
		attr0 := g.oam16(base)
		attr1 := g.oam16(base + 2)
		attr2 := g.oam16(base + 4)

		affine := attr0&(1<<8) != 0
		flag9 := attr0&(1<<9) != 0
		if flag9 && !affine {
			continue // disabled sprite
		}
		shape := int((attr0 >> 14) & 3)
		if shape == 3 {
			continue
		}
		objMode := int((attr0 >> 10) & 3)
		color256 := attr0&(1<<13) != 0
		mosaicOn := attr0&(1<<12) != 0

		sizeIdx := int((attr1 >> 14) & 3)
		w := objSizes[shape][sizeIdx][0]
		h := objSizes[shape][sizeIdx][1]
		boundW, boundH := w, h
		if affine && flag9 {
			boundW, boundH = w*2, h*2 // double size box
		}

		y := int(attr0 & 0xFF)
		xCoord := int(attr1 & 0x1FF)
		relY := (g.line - y) & 0xFF
		if relY >= boundH {
			continue
		}

		tileNum := int(attr2 & 0x3FF)
		prio := int((attr2 >> 10) & 3)
		bank := int((attr2 >> 12) & 0xF)

		var pa, pb, pc, pd int
		if affine {
			set := int((attr1 >> 9) & 0x1F)
			pa = int(int16(g.oam16(set*0x20 + 0x06)))
			pb = int(int16(g.oam16(set*0x20 + 0x0E)))
			pc = int(int16(g.oam16(set*0x20 + 0x16)))
			pd = int(int16(g.oam16(set*0x20 + 0x1E)))
		}
		hflip := attr1&(1<<12) != 0
		vflip := attr1&(1<<13) != 0

		for px := 0; px < Width; px++ {
			relX := (px - xCoord) & 0x1FF
			if relX >= boundW {
				continue
			}
			var srcX, srcY int
			if affine {
				dx := relX - boundW/2
				dy := relY - boundH/2
				srcX = ((pa*dx + pb*dy) >> 8) + w/2
				srcY = ((pc*dx + pd*dy) >> 8) + h/2
				if srcX < 0 || srcX >= w || srcY < 0 || srcY >= h {
					continue
				}
			} else {
				srcX, srcY = relX, relY
				if hflip {
					srcX = w - 1 - srcX
				}
				if vflip {
					srcY = h - 1 - srcY
				}
			}
			if mosaicOn {
				srcX -= srcX % mosH
				srcY -= srcY % mosV
			}
			col, ok := g.objTilePixel(tileNum, srcX, srcY, w/8, color256, bank)
			if !ok {
				continue
			}
			if objMode == 2 {
				out.winMsk[px] = true
				continue
			}
			c := candidate{
				present: true,
				col:     col,
				layer:   layerOBJ,
				prio:    prio,
				semi:    objMode == 1,
			}
			if !out.pix[px].present || prio < out.pix[px].prio {
				out.pix[px] = c
			}
		}
	}
	return out
}

// Windows.

// windowMask returns the six enable and effect bits in
// force at a pixel: bits 0 to 3 enable BG0 to BG3, bit 4
// enables sprites, bit 5 enables colour effects.
func (g *GPU) windowMask(x int, objWin [Width]bool) uint16 {
	dc := g.dispcnt()
	if dc&0xE000 == 0 {
		return 0x3F // no window active: everything on
	}
	winIn := g.reg16(0x48)
	winOut := g.reg16(0x4A)
	if dc&(1<<13) != 0 && g.inWindow(0, x) {
		return winIn & 0x3F
	}
	if dc&(1<<14) != 0 && g.inWindow(1, x) {
		return (winIn >> 8) & 0x3F
	}
	if dc&(1<<15) != 0 && objWin[x] {
		return (winOut >> 8) & 0x3F
	}
	return winOut & 0x3F
}

// inWindow tests a pixel against one window rectangle,
// applying the dimension rules for empty or oversized
// rectangles: a right edge at or below the (clamped)
// left edge, or past the screen edge, extends to the
// screen edge, and the same for the bottom edge.
func (g *GPU) inWindow(which, x int) bool {
	hv := g.reg16(uint32(0x40 + which*2))
	vv := g.reg16(uint32(0x44 + which*2))
	left := int(hv >> 8)
	right := int(hv & 0xFF)
	top := int(vv >> 8)
	bottom := int(vv & 0xFF)
	if left > Width {
		left = Width
	}
	if top > Height {
		top = Height
	}
	if right <= left || right > Width {
		right = Width
	}
	if bottom <= top || bottom > Height {
		bottom = Height
	}
	return x >= left && x < right && g.line >= top && g.line < bottom
}

// Colour effects.

func clamp16(v int) int {
	if v > 16 {
		return 16
	}
	if v < 0 {
		return 0
	}
	return v
}

func channels(col uint16) (int, int, int) {
	return int(col & 31), int((col >> 5) & 31), int((col >> 10) & 31)
}

// applyEffects runs the blending and brightness maths
// for the winning pixel. second may hold the backdrop
// as a stand-in runner-up.
func (g *GPU) applyEffects(best, second candidate) (int, int, int) {
	r, gg, b := channels(best.col)
	bld := g.reg16(regBLDCNT)
	mode := int((bld >> 6) & 3)
	alpha := g.reg16(regBLDALPHA)
	eva := clamp16(int(alpha & 0x1F))
	evb := clamp16(int((alpha >> 8) & 0x1F))
	evy := clamp16(int(g.reg16(regBLDY) & 0x1F))

	blend := func() (int, int, int) {
		sr, sg, sb := channels(second.col)
		mix := func(top, under int) int {
			v := (top*eva + under*evb) / 16
			if v > 31 {
				v = 31
			}
			return v
		}
		return mix(r, sr), mix(gg, sg), mix(b, sb)
	}

	firstTarget := best.layer >= 0 && best.layer <= 5 &&
		(bld>>uint(best.layer))&1 != 0
	secondTarget := second.present &&
		(bld>>(8+uint(second.layer)))&1 != 0

	// A semi-transparent sprite blends with a second
	// target underneath it whatever the mode bits say.
	if best.semi && best.layer == layerOBJ && secondTarget {
		return blend()
	}
	switch mode {
	case 1:
		if firstTarget && secondTarget {
			return blend()
		}
	case 2:
		if firstTarget {
			up := func(c int) int { return c + (31-c)*evy/16 }
			return up(r), up(gg), up(b)
		}
	case 3:
		if firstTarget {
			down := func(c int) int { return c - c*evy/16 }
			return down(r), down(gg), down(b)
		}
	}
	return r, gg, b
}

func packPixel(r, gg, b int) uint32 {
	r8 := uint32((r << 3) | (r >> 2))
	g8 := uint32((gg << 3) | (gg >> 2))
	b8 := uint32((b << 3) | (b >> 2))
	return 0xFF000000 | r8<<16 | g8<<8 | b8
}

// RenderLine renders the current line when it is
// visible, then advances the affine reference points by
// one line step. The step happens even for blanked and
// off screen lines.
func (g *GPU) RenderLine() {
	defer g.stepRefs()
	if g.line < 0 || g.line >= Height {
		return
	}
	row := g.line * Width
	dc := g.dispcnt()
	if dc&(1<<7) != 0 {
		for x := 0; x < Width; x++ {
			g.frame[row+x] = 0xFFFFFFFF
		}
		return
	}
	mode := int(dc & 7)
	obj := g.buildObjLine()

	for x := 0; x < Width; x++ {
		mask := g.windowMask(x, obj.winMsk)
		var pick picker
		for bg := 0; bg < 4; bg++ {
			if dc&(1<<uint(8+bg)) == 0 {
				continue
			}
			if mask&(1<<uint(bg)) == 0 {
				continue
			}
			var col uint16
			var ok bool
			switch {
			case mode == 0 && bg <= 3:
				col, ok = g.textPixel(bg, x)
			case mode == 1 && bg <= 1:
				col, ok = g.textPixel(bg, x)
			case mode == 1 && bg == 2:
				col, ok = g.affinePixel(bg, x)
			case mode == 2 && (bg == 2 || bg == 3):
				col, ok = g.affinePixel(bg, x)
			case (mode == 3 || mode == 4 || mode == 5) && bg == 2:
				col, ok = g.bitmapPixel(mode, x)
			}
			if !ok {
				continue
			}
			pick.consider(candidate{
				present: true,
				col:     col,
				layer:   bg,
				prio:    int(g.bgcnt(bg) & 3),
			})
		}
		if dc&(1<<12) != 0 && mask&(1<<4) != 0 && obj.pix[x].present {
			pick.consider(obj.pix[x])
		}
		backdrop := candidate{
			present: true,
			col:     g.palAt(0),
			layer:   layerBD,
			prio:    99,
		}
		if !pick.best.present {
			pick.best = backdrop
			pick.second = candidate{}
		} else if !pick.second.present {
			pick.second = backdrop
		}
		r, gg, b := channels(pick.best.col)
		if mask&(1<<5) != 0 {
			r, gg, b = g.applyEffects(pick.best, pick.second)
		}
		g.frame[row+x] = packPixel(r, gg, b)
	}
}
