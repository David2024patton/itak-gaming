package vdp2

// Color modes shared by every background layer.
const (
	col16 = iota
	col256
	col2048
	colRGB555
	colRGB888
)

// pix is one fetched layer dot before priority processing.
type pix struct {
	col      rgb
	dotColor int
	spPri    bool
	spCC     bool
}

// charCfg carries everything needed to fetch dots for one layer
// or one rotation parameter set.
type charCfg struct {
	colorMode int
	charSize  int // 0 is 1 by 1 cells, 1 is 2 by 2 cells
	bmen      bool
	bmsz      int
	pnb       bool // true is 1 word pattern names
	cnsm      bool
	spr       bool
	scc       bool
	splt      int
	scn       int
	planeSize int
	mapOffset int
	maps      [16]int
	craof     int
	tpon      bool
	// Bitmap pattern supplement fields.
	bmpPal int
	bmpr   bool
	bmcc   bool
}

func planeGrid(planeSize int) (int, int) {
	switch planeSize & 0x3 {
	case 1:
		return 2, 1
	case 2:
		return 1, 2
	case 3:
		return 2, 2
	default:
		return 1, 1
	}
}

func codeBits(colorMode int) int {
	switch colorMode {
	case col16:
		return 4
	case col256:
		return 8
	case col2048:
		return 11
	}
	return 0
}

// nbgCfg extracts the character configuration for NBG0 to NBG3,
// where idx runs 0 to 3 in that order.
func (v *VDP2) nbgCfg(idx int) charCfg {
	cfg := charCfg{}
	chctla := v.reg16(0x028)
	chctlb := v.reg16(0x02A)
	bgon := v.reg16(regBGON)
	switch idx {
	case 0:
		cfg.colorMode = int((chctla >> 4) & 0x7)
		cfg.bmsz = int((chctla >> 2) & 0x3)
		cfg.bmen = chctla&0x2 != 0
		cfg.charSize = int(chctla & 0x1)
		cfg.tpon = bgon&0x0100 != 0
		p := v.bmpnaFields(false)
		cfg.bmpPal, cfg.bmpr, cfg.bmcc = p.pal, p.pr, p.cc
	case 1:
		cfg.colorMode = int((chctla >> 12) & 0x3)
		cfg.bmsz = int((chctla >> 10) & 0x3)
		cfg.bmen = chctla&0x0200 != 0
		cfg.charSize = int((chctla >> 8) & 0x1)
		cfg.tpon = bgon&0x0200 != 0
		p := v.bmpnaFields(true)
		cfg.bmpPal, cfg.bmpr, cfg.bmcc = p.pal, p.pr, p.cc
	case 2:
		cfg.colorMode = col16
		if chctlb&0x2 != 0 {
			cfg.colorMode = col256
		}
		cfg.charSize = int(chctlb & 0x1)
		cfg.tpon = bgon&0x0400 != 0
	case 3:
		cfg.colorMode = col16
		if chctlb&0x20 != 0 {
			cfg.colorMode = col256
		}
		cfg.charSize = int((chctlb >> 4) & 0x1)
		cfg.tpon = bgon&0x0800 != 0
	}
	if cfg.colorMode > colRGB888 {
		cfg.colorMode = colRGB888
	}
	pnc := v.reg16(0x030 + idx*2)
	cfg.pnb = pnc&0x8000 != 0
	cfg.cnsm = pnc&0x4000 != 0
	cfg.spr = pnc&0x0200 != 0
	cfg.scc = pnc&0x0100 != 0
	cfg.splt = int((pnc >> 5) & 0x7)
	cfg.scn = int(pnc & 0x1F)
	plsz := v.reg16(0x03A)
	cfg.planeSize = int((plsz >> uint(idx*2)) & 0x3)
	mpofn := v.reg16(0x03C)
	cfg.mapOffset = int((mpofn >> uint(idx*4)) & 0x7)
	// Map register pairs: MPAB holds planes A and B, MPCD holds
	// planes C and D, each as 6 bit fields.
	ab := v.reg16(0x040 + idx*4)
	cd := v.reg16(0x042 + idx*4)
	cfg.maps[0] = int(ab & 0x3F)
	cfg.maps[1] = int((ab >> 8) & 0x3F)
	cfg.maps[2] = int(cd & 0x3F)
	cfg.maps[3] = int((cd >> 8) & 0x3F)
	cfg.craof = int((v.reg16(0x0E4) >> uint(idx*4)) & 0x7)
	return cfg
}

type bmpFields struct {
	pal int
	pr  bool
	cc  bool
}

// bmpnaFields reads BMPNA for NBG0 (low byte) or NBG1 (high byte).
func (v *VDP2) bmpnaFields(high bool) bmpFields {
	r := v.reg16(0x02C)
	if high {
		return bmpFields{pal: int((r >> 8) & 0x7), pr: r&0x2000 != 0, cc: r&0x1000 != 0}
	}
	return bmpFields{pal: int(r & 0x7), pr: r&0x20 != 0, cc: r&0x10 != 0}
}

// patternInfo is the decoded pattern name.
type patternInfo struct {
	charBase int
	palette  int
	spPri    bool
	spCC     bool
	flipH    bool
	flipV    bool
}

func (v *VDP2) decodePattern(cfg charCfg, w0, w1 uint16) patternInfo {
	if !cfg.pnb {
		// 2 word form: first word holds flips, special bits, and
		// the full 7 bit palette; second word holds the character.
		return patternInfo{
			charBase: int(w1 & 0x7FFF),
			palette:  int((w0 >> 5) & 0x7F),
			spPri:    w0&0x2000 != 0,
			spCC:     w0&0x1000 != 0,
			flipH:    w0&0x4000 != 0,
			flipV:    w0&0x8000 != 0,
		}
	}
	p := patternInfo{spPri: cfg.spr, spCC: cfg.scc}
	w := w0
	palData := func(cnsm1 bool) int {
		if cfg.colorMode == col16 {
			if cnsm1 {
				return int((w >> 12) & 0xF)
			}
			return int((w >> 10) & 0xF)
		}
		if cnsm1 {
			return int((w >> 12) & 0x7)
		}
		return int((w >> 10) & 0x7)
	}
	if cfg.charSize == 0 {
		if cfg.cnsm {
			p.charBase = ((cfg.scn & 0x1C) << 10) | int(w&0xFFF)
			p.palette = (cfg.splt << 4) | palData(true)
		} else {
			p.charBase = (cfg.scn << 10) | int(w&0x3FF)
			p.palette = (cfg.splt << 4) | palData(false)
			p.flipV = w&0x8000 != 0
			p.flipH = w&0x4000 != 0
		}
	} else {
		if cfg.cnsm {
			p.charBase = ((cfg.scn & 0x10) << 10) | (int((w>>2)&0xFFF) << 2)
			p.palette = (cfg.splt << 4) | palData(true)
		} else {
			p.charBase = ((cfg.scn & 0x1C) << 10) | (int((w>>2)&0x3FF) << 2)
			p.palette = (cfg.splt << 4) | palData(false)
			p.flipV = w&0x8000 != 0
			p.flipH = w&0x4000 != 0
		}
	}
	return p
}

// charDot reads one dot of one character. It returns the palette
// code for palette modes, or the direct color for RGB modes, plus
// the raw value used for transparency and special function tests.
func (v *VDP2) charDot(charNo, dotX, dotY int, cfg charCfg) (int, rgb, int) {
	base := charNo * 0x20
	switch cfg.colorMode {
	case col16:
		addr := base + dotY*4 + dotX/2
		b := v.vramByte(addr)
		code := int(b & 0xF)
		if dotX%2 == 0 {
			code = int(b >> 4)
		}
		return code, rgb{}, code
	case col256:
		code := int(v.vramByte(base + dotY*8 + dotX))
		return code, rgb{}, code
	case col2048:
		w := v.vramWord(base + (dotY*8+dotX)*2)
		return int(w & 0x7FF), rgb{}, int(w & 0x7FF)
	case colRGB555:
		w := v.vramWord(base + (dotY*8+dotX)*2)
		return 0, expand555(w & 0x7FFF), int(w & 0x7FFF)
	default: // colRGB888
		l := v.vramLong(base + (dotY*8+dotX)*4)
		c := rgb{int((l >> 16) & 0xFF), int((l >> 8) & 0xFF), int(l & 0xFF)}
		return 0, c, int(l & 0xFFFFFF)
	}
}

// assemble turns raw dot data into a pixel, or reports the dot as
// transparent. Palette modes look the color up in CRAM; RGB modes
// carry their color directly.
func (v *VDP2) assemble(cfg charCfg, code int, direct rgb, raw int, palette int, p patternInfo) (pix, bool) {
	if cfg.colorMode == colRGB555 || cfg.colorMode == colRGB888 {
		if raw == 0 && !cfg.tpon {
			return pix{}, false
		}
		return pix{col: direct, dotColor: raw, spPri: p.spPri, spCC: p.spCC}, true
	}
	dotColor := (palette << uint(codeBits(cfg.colorMode))) | code
	if dotColor == 0 && !cfg.tpon {
		return pix{}, false
	}
	return pix{
		col:      v.cramColorForDot(dotColor, cfg.craof),
		dotColor: dotColor,
		spPri:    p.spPri,
		spCC:     p.spCC,
	}, true
}

// cellPixel fetches the dot at source coords (sx, sy) in cell mode.
func (v *VDP2) cellPixel(cfg charCfg, sx, sy int) (pix, bool) {
	cellX := floorDiv(sx, 8)
	cellY := floorDiv(sy, 8)
	dotX := sx - cellX*8
	dotY := sy - cellY*8
	gridW, gridH := planeGrid(cfg.planeSize)
	planeX := floorMod(floorDiv(cellX, 64), gridW)
	planeY := floorMod(floorDiv(cellY, 64), gridH)
	plane := planeY*gridW + planeX
	mapVal := cfg.maps[plane]
	pageBase := ((mapVal + (cfg.mapOffset << 6)) * 0x2000) & 0x7FFFF
	inX := floorMod(cellX, 64)
	inY := floorMod(cellY, 64)
	var entryIdx int
	if cfg.charSize == 0 {
		entryIdx = inY*64 + inX
	} else {
		entryIdx = (inY/2)*32 + (inX / 2)
	}
	entrySize := 4
	if cfg.pnb {
		entrySize = 2
	}
	addr := pageBase + entryIdx*entrySize
	w0 := v.vramWord(addr)
	w1 := uint16(0)
	if !cfg.pnb {
		w1 = v.vramWord(addr + 2)
	}
	p := v.decodePattern(cfg, w0, w1)
	charNo := p.charBase
	dx, dy := dotX, dotY
	if cfg.charSize == 1 {
		subX := cellX & 1
		subY := cellY & 1
		if p.flipH {
			subX = 1 - subX
			dx = 7 - dx
		}
		if p.flipV {
			subY = 1 - subY
			dy = 7 - dy
		}
		charNo = p.charBase + subY*2 + subX
	} else {
		if p.flipH {
			dx = 7 - dx
		}
		if p.flipV {
			dy = 7 - dy
		}
	}
	code, direct, raw := v.charDot(charNo, dx, dy, cfg)
	return v.assemble(cfg, code, direct, raw, p.palette, p)
}

var bitmapSizes = [4][2]int{{512, 256}, {512, 512}, {1024, 256}, {1024, 512}}

// bitmapPixel fetches the dot at source coords in bitmap mode.
// Simplification: bytes per dot are 1 for 16 and 256 color modes
// (the 16 color code is the low nibble of the byte), 2 for 2048
// and RGB555 modes, and 4 for RGB888 mode, matching the address
// formula in the specification.
func (v *VDP2) bitmapPixel(cfg charCfg, sx, sy int) (pix, bool) {
	size := bitmapSizes[cfg.bmsz&0x3]
	x := floorMod(sx, size[0])
	y := floorMod(sy, size[1])
	base := ((cfg.maps[0] + (cfg.mapOffset << 6)) * 0x2000) & 0x7FFFF
	p := patternInfo{spPri: cfg.bmpr, spCC: cfg.bmcc}
	addr := base + y*size[0] + x // dot index, scaled below per mode
	switch cfg.colorMode {
	case col16:
		code := int(v.vramByte(addr) & 0xF)
		return v.assemble(cfg, code, rgb{}, code, cfg.bmpPal<<4, p)
	case col256:
		code := int(v.vramByte(addr))
		return v.assemble(cfg, code, rgb{}, code, cfg.bmpPal<<4, p)
	case col2048:
		w := v.vramWord(base + (y*size[0]+x)*2)
		return v.assemble(cfg, int(w&0x7FF), rgb{}, int(w&0x7FF), cfg.bmpPal<<4, p)
	case colRGB555:
		w := v.vramWord(base + (y*size[0]+x)*2)
		return v.assemble(cfg, 0, expand555(w&0x7FFF), int(w&0x7FFF), 0, p)
	default:
		l := v.vramLong(base + (y*size[0]+x)*4)
		c := rgb{int((l >> 16) & 0xFF), int((l >> 8) & 0xFF), int(l & 0xFF)}
		return v.assemble(cfg, 0, c, int(l&0xFFFFFF), 0, p)
	}
}

// scrollSource computes the source dot coords for NBG0 and NBG1,
// applying base scroll, line scroll, zoom, and vertical cell
// scroll. All intermediate math is in 1 over 256 fixed point.
// NBG2 and NBG3 use the integer path in fetchNBG instead.
func (v *VDP2) scrollSource(idx, ex, ey int) (int, int) {
	base := 0x070 + idx*0x10
	scrollReg := func(off int) int {
		intPart := signExtendWord(v.reg16(off)&0x7FF, 11)
		return intPart*256 + int(v.reg16(off+2)>>8)
	}
	scrollX := scrollReg(base)
	scrollY := scrollReg(base + 4)
	incX := int(v.reg16(base+8)&0x7)*256 + int(v.reg16(base+10)>>8)
	incY := int(v.reg16(base+12)&0x7)*256 + int(v.reg16(base+14)>>8)

	scrctl := v.reg16(0x09A)
	field := uint16(0)
	if idx == 0 {
		field = scrctl & 0x3F
	} else {
		field = (scrctl >> 8) & 0x3F
	}
	lss := int((field >> 4) & 0x3)
	lscx := field&0x2 != 0
	lscy := field&0x4 != 0
	lzmx := field&0x8 != 0
	vcsc := field&0x1 != 0

	if lscx || lscy || lzmx {
		u := v.reg16(0x0A0 + idx*4)
		table := (int(u&0x7) << 16) | int(v.reg16(0x0A2+idx*4)&0xFFFE)
		entry := ey >> uint(lss)
		// Walk the enabled fields in X, Y, zoom order. Each field
		// is two words, so one entry spans field count times 4
		// bytes.
		pos := 0
		entryBase := table + entry*countLineFields(field)*4
		readPair := func() (uint16, uint16) {
			a := v.vramWord(entryBase + pos*2)
			b := v.vramWord(entryBase + pos*2 + 2)
			pos += 2
			return a, b
		}
		if lscx {
			iw, fw := readPair()
			scrollX = signExtendWord(iw&0x7FF, 11)*256 + int(fw>>8)
		}
		if lscy {
			iw, fw := readPair()
			scrollY = signExtendWord(iw&0x7FF, 11)*256 + int(fw>>8)
		}
		if lzmx {
			iw, fw := readPair()
			z := int(iw&0x7)*256 + int(fw>>8)
			incX = z
			incY = z
		}
	}

	zmctl := v.reg16(0x098)
	var zmField uint16
	if idx == 0 {
		zmField = zmctl & 0x3
	} else {
		zmField = (zmctl >> 8) & 0x3
	}
	if zmField&0x2 != 0 {
		incX = 4 * 256
		incY = 4 * 256
	} else if zmField&0x1 != 0 {
		incX = 2 * 256
		incY = 2 * 256
	}

	srcX := scrollX + ex*incX
	srcY := scrollY + ey*incY

	if vcsc {
		u := v.reg16(0x09C)
		table := (int(u&0x7) << 16) | int(v.reg16(0x09E)&0xFFFE)
		col := ex >> 3
		iw := v.vramWord(table + col*4)
		fw := v.vramWord(table + col*4 + 2)
		// The vertical cell scroll integer word is treated as a
		// plain signed 16 bit integer part.
		srcY += int(int16(iw))*256 + int(fw>>8)
	}

	return floorDiv(srcX, 256), floorDiv(srcY, 256)
}

func countLineFields(field uint16) int {
	n := 0
	if field&0x2 != 0 {
		n++
	}
	if field&0x4 != 0 {
		n++
	}
	if field&0x8 != 0 {
		n++
	}
	return n
}

// fetchNBG fetches one dot of NBG layer idx at screen evaluation
// coords (ex, ey). Mosaic is applied by the caller by passing the
// snapped evaluation coords, so the scroll state used here is the
// state of the sample line, as documented in the specification.
func (v *VDP2) fetchNBG(idx, ex, ey int) (pix, bool) {
	cfg := v.nbgCfg(idx)
	var sx, sy int
	if idx < 2 {
		sx, sy = v.scrollSource(idx, ex, ey)
	} else {
		off := 0x090
		if idx == 3 {
			off = 0x094
		}
		scx := signExtendWord(v.reg16(off)&0x7FF, 11)
		scy := signExtendWord(v.reg16(off+2)&0x7FF, 11)
		sx = scx + ex
		sy = scy + ey
	}
	if cfg.bmen {
		return v.bitmapPixel(cfg, sx, sy)
	}
	return v.cellPixel(cfg, sx, sy)
}
