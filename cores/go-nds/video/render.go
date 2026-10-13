package video

// Per-scanline renderer. Colors are carried internally as
// rgb6 (three 6-bit channels packed r | g<<6 | b<<12) and
// expanded to XRGB8888 only at the framebuffer.
//
// Layer kinds per (BG mode, engine, BG number), per the
// documented mode tables:
//
//	mode 0: text, text, text, text
//	mode 1: text, text, text, affine
//	mode 2: text, text, affine, affine
//	mode 3: text, text, text, extended
//	mode 4: text, text, affine, extended
//	mode 5: text, text, extended, extended
//	mode 6: engine A: BG2 large bitmap only; engine B: as 5
//
// On engine A, a BG0 with DISPCNT bit 3 set is the 3D layer:
// the gx package fills it through the Line3D provider
// (DS-3); BG0Is3D exposes the condition.

const (
	kindNone = iota
	kindText
	kindAffine
	kindExtended
	kindLarge
)

func bgKind(mode uint32, e, bg int) int {
	if bg == 0 || bg == 1 {
		return kindText
	}
	switch mode {
	case 0:
		return kindText
	case 1:
		if bg == 2 {
			return kindText
		}
		return kindAffine
	case 2:
		return kindAffine
	case 3:
		if bg == 2 {
			return kindText
		}
		return kindExtended
	case 4:
		if bg == 2 {
			return kindAffine
		}
		return kindExtended
	case 5:
		return kindExtended
	case 6:
		if e == 0 {
			if bg == 2 {
				return kindLarge
			}
			return kindNone
		}
		return kindExtended
	}
	return kindNone
}

type px struct {
	c  uint32
	ok bool
}

type objPx struct {
	c    uint32
	ok   bool
	semi bool
	prio uint8
}

type lineCtx struct {
	v       *Video
	e       int
	base    int // IO offset of the engine's register page
	y       int
	dispcnt uint32
	bg      [4][Width]px
	bgOn    [4]bool
	obj     [Width]objPx
	objWin  [Width]bool
	line3D  [Width]uint32 // packed 3D provider output for this line
	alpha3D [Width]uint8  // per-pixel alpha of the 3D BG0 layer
	has3D   bool          // BG0 is the 3D layer on this line
}

func (v *Video) bgSpace(e int) *space {
	if e == 0 {
		return v.bgA
	}
	return v.bgB
}

func (v *Video) objSpace(e int) *space {
	if e == 0 {
		return v.objA
	}
	return v.objB
}

// stdPal reads a standard (non-extended) palette entry: BG at
// +0x000/+0x400 per engine, OBJ at +0x200/+0x600.
func (v *Video) stdPal(e int, obj bool, idx int) uint32 {
	base := e * 0x400
	if obj {
		base += 0x200
	}
	off := base + idx*2
	return rgb6From15(uint16(v.bus.Palette[off]) | uint16(v.bus.Palette[off+1])<<8)
}

// pal256 returns the 256-color palette entry for a BG layer,
// honoring the extended palette slot when BG extended
// palettes are enabled.
func (v *Video) pal256(lc *lineCtx, bg, idx int) uint32 {
	if lc.dispcnt&(1<<30) != 0 {
		return rgb6From15(lc.v.extBG[lc.e].get16(uint32(bg*0x2000 + idx*2)))
	}
	return lc.v.stdPal(lc.e, false, idx)
}

// RenderLine renders line y of both engines into the A and B
// framebuffers and records engine A's composed line (before
// master brightness) for display capture.
func (v *Video) RenderLine(y int) {
	v.maybeRebuildMaps()
	v.renderEngine(0, y, v.A)
	v.renderEngine(1, y, v.B)
}

func (v *Video) renderEngine(e, y int, out []uint32) {
	base := e * 0x1000
	row := out[y*Width : (y+1)*Width]
	pow := v.powcnt()
	dispcnt := v.rd32(base)
	engineOn := pow&1 != 0 && (e == 0 && pow&2 != 0 || e == 1 && pow&0x200 != 0)
	fill := func(c6 uint32) {
		w := xrgb(c6)
		for x := 0; x < Width; x++ {
			row[x] = w
			if e == 0 {
				v.lineA[x] = c6
			}
		}
	}
	if !engineOn {
		fill(0) // powered down: black
		return
	}
	mode := (dispcnt >> 16) & 3
	if dispcnt&0x80 != 0 || mode == 0 {
		fill(rgb6(63, 63, 63)) // forced blank / display off: white
		return
	}
	switch mode {
	case 2: // VRAM display: raw 15bpp from the selected bank
		bank := v.bus.VRAMBank(int(dispcnt>>18) & 3)
		for x := 0; x < Width; x++ {
			off := (y*Width + x) * 2
			c := rgb6From15(uint16(bank[off]) | uint16(bank[off+1])<<8)
			row[x] = xrgb(c)
			if e == 0 {
				v.lineA[x] = c
			}
		}
		return
	case 3: // main RAM display at the latched address
		for x := 0; x < Width; x++ {
			off := (v.mainAdr + uint32(y*Width+x)*2) & (4<<20 - 1)
			c := rgb6From15(uint16(v.bus.MainRAM[off]) | uint16(v.bus.MainRAM[off+1])<<8)
			row[x] = xrgb(c)
			if e == 0 {
				v.lineA[x] = c
			}
		}
		return
	}
	lc := &lineCtx{v: v, e: e, base: base, y: y, dispcnt: dispcnt}
	lc.renderLayers()
	comp := lc.composite()
	mb := v.rd16(base + 0x6C)
	f := int(mb & 0x3F)
	if f > 16 {
		f = 16
	}
	for x := 0; x < Width; x++ {
		c := comp[x]
		if e == 0 {
			v.lineA[x] = c
		}
		row[x] = xrgb(applyMaster(c, mb>>14&3, f))
	}
}

// applyMaster applies one MASTER_BRIGHT mode (1 up, 2 down)
// with the clamped factor to an rgb6 pixel.
func applyMaster(c uint32, mode uint16, f int) uint32 {
	if mode != 1 && mode != 2 {
		return c
	}
	ch := func(v uint32) uint32 {
		if mode == 1 {
			return v + (63-v)*uint32(f)>>4
		}
		return v - v*uint32(f)>>4
	}
	return rgb6(ch(c&63), ch(c>>6&63), ch(c>>12&63))
}

func (lc *lineCtx) renderLayers() {
	v := lc.v
	mode := lc.dispcnt & 7
	for bg := 0; bg < 4; bg++ {
		if lc.dispcnt&(1<<uint(8+bg)) == 0 {
			continue
		}
		if bg == 0 && lc.e == 0 && lc.dispcnt&8 != 0 {
			// The 3D layer: fill BG0 from the rendering
			// engine's line provider when one is installed;
			// without one it stays transparent.
			if v.Line3D != nil {
				lc.has3D = true
				v.Line3D(lc.y, &lc.line3D)
				for x := 0; x < Width; x++ {
					packed := lc.line3D[x]
					a := uint8(packed >> 18)
					if a == 0 {
						continue
					}
					lc.bg[0][x] = px{c: packed & 0x3FFFF, ok: true}
					lc.alpha3D[x] = a
				}
			}
			continue
		}
		switch bgKind(mode, lc.e, bg) {
		case kindText:
			v.renderTextBG(lc, bg)
		case kindAffine:
			v.renderAffineBG(lc, bg)
		case kindExtended:
			v.renderExtendedBG(lc, bg)
		case kindLarge:
			v.renderLargeBG(lc, bg)
		}
	}
	if lc.dispcnt&0x1000 != 0 {
		v.renderOBJ(lc)
	}
}

// mosaicH returns the horizontal mosaic size for a layer class
// (BG or OBJ) from the engine's MOSAIC register.
func (lc *lineCtx) mosaic(isOBJ bool) (int, int) {
	m := lc.v.rd16(lc.base + 0x4C)
	if isOBJ {
		return int(m>>8&0xF) + 1, int(m>>12&0xF) + 1
	}
	return int(m&0xF) + 1, int(m>>4&0xF) + 1
}

// renderTextBG renders one text-mode background line (GBA
// semantics: 4bpp/8bpp tiles, screen block layout, flips,
// palette banks), with mosaic quantization of the fetch
// coordinates.
func (v *Video) renderTextBG(lc *lineCtx, bg int) {
	sp := v.bgSpace(lc.e)
	cnt := v.rd16(lc.base + 8 + 2*bg)
	charBase := uint32(cnt>>2&0x1F) * 0x4000
	scrBase := uint32(cnt>>8&0x1F) * 0x800
	wTiles := 32 << (cnt >> 14 & 1)
	sx := int(v.rd16(lc.base+0x10+4*bg) & 0x1FF)
	sy := int(v.rd16(lc.base+0x12+4*bg) & 0x1FF)
	mh, mv := 1, 1
	if cnt&0x40 != 0 {
		mh, mv = lc.mosaic(false)
	}
	fy := lc.y + sy
	if mv > 1 {
		fy -= fy % mv
	}
	tileY := fy >> 3
	blockRow := (tileY >> 5) * (wTiles >> 5)
	eightBit := cnt&0x80 != 0
	for x := 0; x < Width; x++ {
		fx := x + sx
		if mh > 1 {
			fx -= fx % mh
		}
		tileX := fx >> 3
		block := scrBase + uint32(blockRow+(tileX>>5))*0x800
		entry := sp.get16(block + uint32((tileY&31)*32+(tileX&31))*2)
		tile := uint32(entry & 0x3FF)
		pxx, pyy := fx&7, fy&7
		if entry&0x400 != 0 {
			pxx = 7 - pxx
		}
		if entry&0x800 != 0 {
			pyy = 7 - pyy
		}
		var idx int
		if eightBit {
			idx = int(sp.get(charBase + tile*64 + uint32(pyy*8+pxx)))
			if idx == 0 {
				continue
			}
			lc.bg[bg][x] = px{c: v.pal256(lc, bg, idx), ok: true}
		} else {
			b := sp.get(charBase + tile*32 + uint32(pyy*4+pxx/2))
			nib := int(b>>4) & 15
			if pxx&1 == 0 {
				nib = int(b & 15)
			}
			if nib == 0 {
				continue
			}
			lc.bg[bg][x] = px{c: v.stdPal(lc.e, false, int(entry>>12)*16+nib), ok: true}
		}
	}
}

// affineSample computes the source pixel for screen x of an
// affine layer: the reference point advanced by the matrix.
// ok=false means the sample falls outside the layer bounds
// with wrap disabled.
func (lc *lineCtx) affineSample(bg int, x int, sizePx int, wrap bool) (int, int, bool) {
	base := lc.base + 0x20 + (bg-2)*0x10
	pa := int32(int16(lc.v.rd16(base)))
	refX := lc.v.refX[lc.e][bg] + pa*int32(x)
	refY := lc.v.refY[lc.e][bg] + int32(int16(lc.v.rd16(base+4)))*int32(x)
	tx, ty := int(refX>>8), int(refY>>8)
	if wrap {
		tx &= sizePx - 1
		ty &= sizePx - 1
		return tx, ty, true
	}
	if tx < 0 || ty < 0 || tx >= sizePx || ty >= sizePx {
		return 0, 0, false
	}
	return tx, ty, true
}

// renderAffineBG renders one affine background line (8bpp
// tiles over a square map).
func (v *Video) renderAffineBG(lc *lineCtx, bg int) {
	sp := v.bgSpace(lc.e)
	cnt := v.rd16(lc.base + 8 + 2*bg)
	charBase := uint32(cnt>>2&0x1F) * 0x4000
	scrBase := uint32(cnt>>8&0x1F) * 0x800
	mapTiles := 16 << (cnt >> 14 & 3)
	wrap := cnt&0x2000 != 0
	for x := 0; x < Width; x++ {
		tx, ty, ok := lc.affineSample(bg, x, mapTiles*8, wrap)
		if !ok {
			continue
		}
		tile := sp.get(scrBase + uint32((ty>>3)*mapTiles+(tx>>3)))
		idx := int(sp.get(charBase + uint32(tile)*64 + uint32((ty&7)*8+(tx&7))))
		if idx == 0 {
			continue
		}
		lc.bg[bg][x] = px{c: v.pal256(lc, bg, idx), ok: true}
	}
}

// renderExtendedBG renders one extended background line:
// affine-extended tile mode (BGxCNT bit 2 clear) or bitmap
// mode (bit 2 set), per the documented decode in the header
// of this slice's design notes.
func (v *Video) renderExtendedBG(lc *lineCtx, bg int) {
	sp := v.bgSpace(lc.e)
	cnt := v.rd16(lc.base + 8 + 2*bg)
	wrap := cnt&0x2000 != 0
	if cnt&4 == 0 {
		// Affine-extended tiles: 16-bit map entries (tile
		// 0-13, H flip 14, V flip 15), 8bpp tile data.
		charBase := uint32(cnt>>2&0x1F) * 0x4000
		scrBase := uint32(cnt>>8&0x1F) * 0x800
		mapTiles := 16 << (cnt >> 14 & 3)
		for x := 0; x < Width; x++ {
			tx, ty, ok := lc.affineSample(bg, x, mapTiles*8, wrap)
			if !ok {
				continue
			}
			entry := sp.get16(scrBase + uint32((ty>>3)*mapTiles+(tx>>3))*2)
			pxx, pyy := tx&7, ty&7
			if entry&0x4000 != 0 {
				pxx = 7 - pxx
			}
			if entry&0x8000 != 0 {
				pyy = 7 - pyy
			}
			idx := int(sp.get(charBase + uint32(entry&0x3FFF)*64 + uint32(pyy*8+pxx)))
			if idx == 0 {
				continue
			}
			lc.bg[bg][x] = px{c: v.pal256(lc, bg, idx), ok: true}
		}
		return
	}
	// Bitmap mode.
	sizes := [4][2]int{{128, 128}, {256, 256}, {512, 256}, {512, 512}}
	w, h := sizes[cnt>>14&3][0], sizes[cnt>>14&3][1]
	dataBase := uint32(cnt>>8&0x1F) * 0x4000
	direct := cnt&0x80 != 0
	for x := 0; x < Width; x++ {
		tx, ty, ok := lc.affineSampleWH(bg, x, w, h, wrap)
		if !ok {
			continue
		}
		if direct {
			w16 := sp.get16(dataBase + uint32(ty*w+tx)*2)
			if w16&0x8000 == 0 {
				continue
			}
			lc.bg[bg][x] = px{c: rgb6From15(w16), ok: true}
		} else {
			idx := int(sp.get(dataBase + uint32(ty*w+tx)))
			if idx == 0 {
				continue
			}
			lc.bg[bg][x] = px{c: rgb6From15(v.extBG[lc.e].get16(uint32(bg*0x2000 + idx*2))), ok: true}
		}
	}
}

// affineSampleWH is affineSample for rectangular layers.
func (lc *lineCtx) affineSampleWH(bg, x, w, h int, wrap bool) (int, int, bool) {
	base := lc.base + 0x20 + (bg-2)*0x10
	pa := int32(int16(lc.v.rd16(base)))
	refX := lc.v.refX[lc.e][bg] + pa*int32(x)
	refY := lc.v.refY[lc.e][bg] + int32(int16(lc.v.rd16(base+4)))*int32(x)
	tx, ty := int(refX>>8), int(refY>>8)
	if wrap {
		tx = ((tx % w) + w) % w
		ty = ((ty % h) + h) % h
		return tx, ty, true
	}
	if tx < 0 || ty < 0 || tx >= w || ty >= h {
		return 0, 0, false
	}
	return tx, ty, true
}

// renderLargeBG renders engine A's mode-6 BG2 large bitmap
// (direct color, affine parameters apply, data base 0 of the
// BG A space; a documented simplification of the hardware's
// base-offset selection).
func (v *Video) renderLargeBG(lc *lineCtx, bg int) {
	sp := v.bgSpace(lc.e)
	cnt := v.rd16(lc.base + 8 + 2*bg)
	sizes := [4][2]int{{512, 1024}, {1024, 512}, {512, 512}, {1024, 1024}}
	w, h := sizes[cnt>>14&3][0], sizes[cnt>>14&3][1]
	wrap := cnt&0x2000 != 0
	for x := 0; x < Width; x++ {
		tx, ty, ok := lc.affineSampleWH(bg, x, w, h, wrap)
		if !ok {
			continue
		}
		w16 := sp.get16(uint32(ty*w+tx) * 2)
		if w16&0x8000 == 0 {
			continue
		}
		lc.bg[bg][x] = px{c: rgb6From15(w16), ok: true}
	}
}
