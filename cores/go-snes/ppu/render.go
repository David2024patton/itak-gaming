package ppu

// The scanline renderer. The machine calls RenderLine once
// per display line, after the CPU has run that line's worth
// of time; the renderer composes the line from the current
// register state (see the package comment for the
// line-granularity model and its one documented timing
// simplification).
//
// Composition follows the documented priority tables for
// each background mode: every layer produces, per pixel, a
// color and a numeric rank, and the highest rank wins on
// each screen (main and sub are composed independently,
// then color math combines them).

// sample is one layer's contribution at one pixel.
type sample struct {
	color  uint16 // 15-bit SNES color
	rank   int    // higher wins
	layer  int    // 0..3 backgrounds, 4 OBJ, 5 backdrop
	opaque bool
}

// expand turns a 15-bit color into an XRGB8888 pixel with
// the brightness scale applied (per channel:
// color*brightness/15, then the standard 5-to-8 bit
// replication expansion).
func (p *PPU) expand(c uint16) uint32 {
	r := uint32(c & 0x1F)
	g := uint32((c >> 5) & 0x1F)
	b := uint32((c >> 10) & 0x1F)
	br := uint32(p.brightness)
	r = r * br / 15
	g = g * br / 15
	b = b * br / 15
	r = (r << 3) | (r >> 2)
	g = (g << 3) | (g >> 2)
	b = (b << 3) | (b >> 2)
	return 0xFF000000 | b<<16 | g<<8 | r
}

// modeInfo describes one background mode's layers.
type modeInfo struct {
	bpp     [4]int // bits per pixel per BG, 0 = layer absent
	objBase [4]int // rank for non-priority tiles
	objPrio [4]int // rank for priority-bit tiles
	// objRanks: rank of OBJ samples by their 2-bit priority.
	objRank [4]int
	hires   bool
}

func (p *PPU) modeInfo() modeInfo {
	switch p.bgMode {
	case 0:
		return modeInfo{
			bpp:     [4]int{2, 2, 2, 2},
			objPrio: [4]int{12, 11, 9, 8},
			objBase: [4]int{6, 5, 3, 2},
			objRank: [4]int{4, 7, 10, 13},
		}
	case 1:
		bg3p, bg3b := 3, 1
		if p.bg3PrioBit {
			// BG3's priority tiles sit just below the
			// highest sprite priority.
			bg3p = 10
		}
		return modeInfo{
			bpp:     [4]int{4, 4, 2, 0},
			objPrio: [4]int{9, 8, bg3p, 0},
			objBase: [4]int{6, 5, bg3b, 0},
			objRank: [4]int{2, 4, 7, 11},
		}
	case 2:
		return modeInfo{
			bpp:     [4]int{4, 4, 0, 0},
			objPrio: [4]int{7, 5, 0, 0},
			objBase: [4]int{3, 1, 0, 0},
			objRank: [4]int{2, 4, 6, 8},
		}
	case 3:
		return modeInfo{
			bpp:     [4]int{8, 4, 0, 0},
			objPrio: [4]int{7, 5, 0, 0},
			objBase: [4]int{3, 1, 0, 0},
			objRank: [4]int{2, 4, 6, 8},
		}
	case 4:
		return modeInfo{
			bpp:     [4]int{8, 0, 0, 0},
			objPrio: [4]int{5, 0, 0, 0},
			objBase: [4]int{2, 0, 0, 0},
			objRank: [4]int{1, 3, 4, 6},
		}
	case 5:
		return modeInfo{
			bpp:     [4]int{4, 2, 0, 0},
			objPrio: [4]int{7, 5, 0, 0},
			objBase: [4]int{3, 1, 0, 0},
			objRank: [4]int{2, 4, 6, 8},
			hires:   true,
		}
	case 6:
		return modeInfo{
			bpp:     [4]int{4, 0, 0, 0},
			objPrio: [4]int{5, 0, 0, 0},
			objBase: [4]int{2, 0, 0, 0},
			objRank: [4]int{1, 3, 4, 6},
			hires:   true,
		}
	case 7:
		// Mode 7 and EXTBG layer ranks: OBJ3 7, EXTBG
		// priority tiles 6, OBJ2 5, BG1 4, OBJ1 3, EXTBG
		// plain tiles 2, OBJ0 1.
		return modeInfo{
			bpp:     [4]int{8, 0, 0, 0},
			objRank: [4]int{1, 3, 5, 7},
		}
	}
	return modeInfo{}
}

// optSource returns the background index whose tilemap
// holds the offset-per-tile table for this mode, or -1.
func (p *PPU) optSource() int {
	switch p.bgMode {
	case 2:
		return 2 // BG3
	case 4, 6:
		return 1 // BG2
	}
	return -1
}

// optApplies reports whether offset-per-tile affects the
// given background in this mode.
func (p *PPU) optApplies(bg int) (src int, ok bool) {
	src = p.optSource()
	if src < 0 {
		return -1, false
	}
	switch p.bgMode {
	case 2:
		return src, bg == 0 || bg == 1
	case 4, 6:
		return src, bg == 0
	}
	return -1, false
}

// tilemapEntry reads one entry of a layer's tilemap at
// tile coordinates (already wrapped into the map size).
func (p *PPU) tilemapEntry(b *bgLayer, tx, ty int) uint16 {
	sx, sy := 0, 0
	if tx >= 32 {
		sx = 1
		tx -= 32
	}
	if ty >= 32 {
		sy = 1
		ty -= 32
	}
	stride := 1
	if b.tilemapW == 64 {
		stride = 2
	}
	addr := b.tilemapBase + uint16((sy*stride+sx)*0x400+ty*32+tx)
	return p.vram[addr&0x7FFF]
}

// fetchTilePixel returns the color index of the pixel at
// (px, py) inside tile tileNum of a layer with the given
// bit depth: planar decode, 8 pixels per row. A tile holds
// 4 words per bit of depth (8 rows of one word per plane
// pair), so 8, 16, or 32 words for 2, 4, or 8 bpp.
func (p *PPU) fetchTilePixel(b *bgLayer, tile uint16, px, py, bpp int) int {
	wordsPerTile := 4 * bpp
	base := (b.chrBase + tile*uint16(wordsPerTile)) & 0x7FFF
	bit := uint(7 - px)
	idx := 0
	for plane := 0; plane < bpp; plane += 2 {
		w := p.vram[(base+uint16(plane*4+py))&0x7FFF]
		idx |= int((w>>bit)&1) << plane
		idx |= int((w>>(bit+8))&1) << (plane + 1)
	}
	return idx
}

// bgSample produces layer bg's sample at screen pixel
// (x, y) (0-based display line), in a sampling space of
// width w (256, or 512 in the hi-res modes).
func (p *PPU) bgSample(mi modeInfo, bg, x, y, w int) sample {
	bpp := mi.bpp[bg]
	if bpp == 0 {
		return sample{}
	}
	b := &p.bg[bg]

	// Mosaic: the sample comes from the top-left pixel of
	// the mosaic block this pixel belongs to.
	sx, sy := x, y
	if p.mosaicEnabled(bg) {
		sx = x - (x % p.mosaicSize)
		sy = y - (y % p.mosaicSize)
	}

	hofs := int(b.hofs & 0x3FF)
	vofs := int(b.vofs & 0x7FF)

	// Offset-per-tile replaces the scroll registers for
	// the affected columns.
	if src, ok := p.optApplies(bg); ok {
		if oh, ov, hasH, hasV := p.optOffsets(src, bg, sx, sy, w); hasH || hasV {
			if hasH {
				hofs = oh
			}
			if hasV {
				vofs = ov
			}
		}
	}

	tileW, tileH := 8, 8
	if b.tile16 || mi.hires {
		tileW = 16
	}
	if b.tile16 {
		tileH = 16
	}
	if mi.hires {
		// Hi-res modes sample a 512-wide picture through
		// 16-wide tiles; the sampling x is already in that
		// space. Scroll values are likewise in that space.
	}

	gx := sx + hofs
	gy := sy + vofs + 1 // the documented vertical +1

	tx := gx / tileW
	ty := gy / tileH
	px := gx % tileW
	py := gy % tileH
	if tx < 0 || ty < 0 {
		return sample{}
	}
	entry := p.tilemapEntry(b, tx&(b.tilemapW-1), ty&(b.tilemapH-1))
	tile := entry & 0x3FF
	pal := int((entry >> 10) & 0x07)
	// Tilemap entry bits: 15 priority, 14 flip Y, 13 flip X.
	if entry&0x2000 != 0 { // flip X
		px = tileW - 1 - px
	}
	if entry&0x4000 != 0 { // flip Y
		py = tileH - 1 - py
	}
	if tileW == 16 {
		tile += uint16(px / 8)
		px %= 8
	}
	if tileH == 16 {
		tile += uint16(py/8) * 16
		py %= 8
	}
	idx := p.fetchTilePixel(b, tile, px, py, bpp)
	if idx == 0 {
		return sample{}
	}
	rank := mi.objBase[bg]
	if entry&0x8000 != 0 {
		rank = mi.objPrio[bg]
	}
	s := sample{layer: bg, opaque: true, rank: rank}
	if p.bgMode == 3 || p.bgMode == 4 {
		if bg == 0 && bpp == 8 && p.cgwsel&0x40 != 0 {
			// Direct color: the 8-bit index is the color,
			// in the documented BBGGGRRR packing expanded
			// to 15 bits (low bits zero).
			s.color = uint16(idx&0x07)<<2 | uint16((idx>>3)&0x07)<<7 | uint16((idx>>6)&0x03)<<13
			return s
		}
	}
	ci := pal*(1<<uint(bpp)) + idx
	if p.bgMode == 0 {
		// Mode 0 gives each background its own 32-color
		// quarter of CGRAM.
		ci = bg*32 + pal*4 + idx
	}
	s.color = p.cgram[ci&0xFF]
	return s
}

// mosaicEnabled reports whether mosaic applies to a layer.
func (p *PPU) mosaicEnabled(bg int) bool {
	return p.mosaicSize > 1 && p.mosaicBG&(1<<uint(bg)) != 0
}

// optOffsets reads the offset-per-tile table (held in the
// source layer's tilemap) for the column holding screen
// pixel x on display line y, for target layer bg. The
// source layer's own scroll registers offset the lookup,
// as the documentation describes.
func (p *PPU) optOffsets(src, bg, x, y, w int) (hofs, vofs int, hasH, hasV bool) {
	sb := &p.bg[src]
	colW := 8
	if p.bg[bg].tile16 || p.modeInfo().hires {
		colW = 16
	}
	sh := int(sb.hofs & 0x3FF)
	sv := int(sb.vofs & 0x7FF)
	col := (x + sh) / colW
	row := (y + sv + 1) / 8
	entryH := p.tilemapEntry(sb, col&(sb.tilemapW-1), row&(sb.tilemapH-1))
	switch p.bgMode {
	case 4:
		// One entry per column: bit 15 set means it is a
		// vertical offset, clear means horizontal.
		if entryH&0x8000 != 0 {
			return 0, int(entryH & 0x3FF), false, true
		}
		return int(entryH & 0x3FF), 0, true, false
	case 2, 6:
		entryV := p.tilemapEntry(sb, col&(sb.tilemapW-1), (row+3)&(sb.tilemapH-1))
		var hFlag, vFlag uint16
		if bg == 0 {
			hFlag, vFlag = 0x8000, 0x4000
		} else {
			hFlag, vFlag = 0x4000, 0x8000
		}
		if entryH&hFlag != 0 {
			hofs, hasH = int(entryH&0x3FF), true
			if colW == 16 {
				hofs &^= 7
			}
		}
		if entryV&vFlag != 0 {
			vofs, hasV = int(entryV&0x3FF), true
		}
		return hofs, vofs, hasH, hasV
	}
	return 0, 0, false, false
}

// --- sprites -------------------------------------------------------------

var objSizes = [8][2][2]int{
	{{8, 8}, {16, 16}},
	{{8, 8}, {32, 32}},
	{{8, 8}, {64, 64}},
	{{16, 16}, {32, 32}},
	{{16, 16}, {64, 64}},
	{{32, 32}, {64, 64}},
	{{16, 32}, {32, 64}},
	{{16, 32}, {32, 32}},
}

// objLine precomputes the OBJ layer's per-pixel sample for
// one display line: the first sprite (in evaluation order,
// with priority rotation applied) to cover a pixel wins it.
func (p *PPU) objLine(mi modeInfo, y, w int) []sample {
	out := make([]sample, w)
	type spr struct {
		x, y     int
		tile     uint16
		pal      int
		prio     int
		flipX    bool
		flipY    bool
		wid, hgt int
	}
	start := 0
	if p.oamPrioRot {
		start = (p.oamAddr >> 2) & 0x7F
	}
	var onLine []spr
	for i := 0; i < 128; i++ {
		n := (start + i) & 0x7F
		o := n * 4
		x0 := int(p.oam[o]) | int(p.oam[512+n/4]>>uint((n%4)*2)&1)<<8
		if x0 >= 256 {
			x0 -= 512
		}
		y0 := int(p.oam[o+1])
		sizeBit := (p.oam[512+n/4] >> uint((n%4)*2+1)) & 1
		sz := objSizes[p.objSize][sizeBit]
		sy := y - y0
		if sy < 0 {
			sy += 256
		}
		if sy >= sz[1] {
			continue
		}
		attr := p.oam[o+3]
		s := spr{
			x: x0, y: y0,
			tile:  uint16(p.oam[o+2]) | uint16(attr&1)<<8,
			pal:   int((attr >> 1) & 0x07),
			prio:  int((attr >> 4) & 0x03),
			flipX: attr&0x40 != 0,
			flipY: attr&0x80 != 0,
			wid:   sz[0], hgt: sz[1],
		}
		onLine = append(onLine, s)
		if len(onLine) == 33 {
			p.rangeOver = true
			onLine = onLine[:32]
			break
		}
	}
	// Sprite tile budget: at most 34 8x8 tiles are fetched
	// per line; sprites past the budget are not drawn.
	tiles := 0
	var draw []spr
	for _, s := range onLine {
		end := s.x + s.wid
		if end > w {
			end = w
		}
		if end <= 0 || s.x >= w {
			continue // fully off-screen horizontally
		}
		// Tiles fetched span from the sprite's left edge
		// to its last on-screen pixel.
		n := (end - s.x + 7) / 8
		tiles += n
		if tiles > 34 {
			p.timeOver = true
			break
		}
		draw = append(draw, s)
	}
	// Draw from the last evaluated to the first, so the
	// first sprite to cover a pixel keeps it.
	for i := len(draw) - 1; i >= 0; i-- {
		s := draw[i]
		ry := y - s.y
		if ry < 0 {
			ry += 256
		}
		if s.flipY {
			ry = s.hgt - 1 - ry
		}
		for sx := 0; sx < s.wid; sx++ {
			dx := s.x + sx
			if dx < 0 || dx >= w {
				continue
			}
			px := sx
			if s.flipX {
				px = s.wid - 1 - sx
			}
			tile := s.tile + uint16(ry/8)*16 + uint16(px/8)
			base := p.objBase
			if tile&0x100 != 0 {
				base += p.objGap
			}
			word := (base + (tile&0xFF)*16 + uint16(ry%8)) & 0x7FFF
			w0 := p.vram[word]
			w1 := p.vram[(word+8)&0x7FFF]
			bit := uint(7 - (px % 8))
			idx := int((w0>>bit)&1) | int((w0>>(bit+8))&1)<<1 |
				int((w1>>bit)&1)<<2 | int((w1>>(bit+8))&1)<<3
			if idx == 0 {
				continue
			}
			out[dx] = sample{
				color:  p.cgram[128+s.pal*16+idx],
				rank:   mi.objRank[s.prio],
				layer:  4,
				opaque: true,
			}
		}
	}
	return out
}

// --- Mode 7 --------------------------------------------------------------

// m7Sample produces the Mode 7 layer's sample at (x, y).
// The affine math follows the documented model: coordinates
// relative to the center, the two 16-bit products summed
// and wrapped to 26 bits signed before the shift, then the
// center added back.
func (p *PPU) m7Sample(x, y int, extbg bool) sample {
	sx, sy := x, y
	if p.m7FlipX {
		sx = Width - 1 - x
	}
	if p.m7FlipY {
		sy = Height - 1 - y
	}
	cx := sign13(p.m7x)
	cy := sign13(p.m7y)
	dx := sx + sign13(p.bg[0].hofs&0x1FFF) - cx
	dy := sy + sign13(p.bg[0].vofs&0x1FFF) - cy
	tx := int((wrap26(int32(p.m7a)*int32(dx)+int32(p.m7b)*int32(dy)) >> 8)) + cx
	ty := int((wrap26(int32(p.m7c)*int32(dx)+int32(p.m7d)*int32(dy)) >> 8)) + cy
	if tx < 0 || tx >= 1024 || ty < 0 || ty >= 1024 {
		switch p.m7Over {
		case 1: // outside is transparent
			return sample{}
		case 2: // outside samples tile 0
			tx &= 7
			ty &= 7
		case 3: // outside repeats tiles 0..7
			tx &= 63
			ty &= 63
		default: // 0: the map wraps
			tx &= 1023
			ty &= 1023
		}
	}
	entry := p.vram[(ty>>3)*128+(tx>>3)] & 0xFF
	w := p.vram[(entry*64+uint16((ty&7)*8+(tx&7)))&0x7FFF]
	pix := uint8(w >> 8)
	if extbg {
		if pix&0x7F == 0 {
			return sample{}
		}
		rank := 2
		if entry&0x80 != 0 {
			rank = 6
		}
		return sample{color: p.cgram[128+(pix&0x7F)], rank: rank, layer: 1, opaque: true}
	}
	if pix == 0 {
		return sample{}
	}
	return sample{color: p.cgram[pix], rank: 4, layer: 0, opaque: true}
}

func sign13(v uint16) int {
	if v&0x1000 != 0 {
		return int(v) - 0x2000
	}
	return int(v)
}

// wrap26 wraps a value to 26 bits signed, the documented
// truncation of the Mode 7 product sum.
func wrap26(v int32) int32 {
	v &= (1 << 26) - 1
	if v&(1<<25) != 0 {
		v -= 1 << 26
	}
	return v
}

// --- windows and color math ---------------------------------------------

// layerWindowSel returns the invert/enable bits for a
// layer (0..3 BG, 4 OBJ, 5 color window).
func (p *PPU) layerWindowSel(layer int) (inv1, en1, inv2, en2 bool) {
	var sel uint8
	var shift uint
	switch layer {
	case 0, 1:
		sel, shift = p.w12sel, uint(layer)*4
	case 2, 3:
		sel, shift = p.w34sel, uint(layer-2)*4
	case 4:
		sel, shift = p.wobjsel, 0
	case 5:
		sel, shift = p.wobjsel, 4
	}
	inv1 = sel&(1<<shift) != 0
	en1 = sel&(2<<shift) != 0
	inv2 = sel&(4<<shift) != 0
	en2 = sel&(8<<shift) != 0
	return
}

// layerWindowLog returns the 2-bit window combination
// logic for a layer.
func (p *PPU) layerWindowLog(layer int) int {
	switch layer {
	case 0, 1, 2, 3:
		return int((p.wbglog >> (uint(layer) * 2)) & 3)
	case 4:
		return int(p.wobjlog & 3)
	case 5:
		return int((p.wobjlog >> 2) & 3)
	}
	return 0
}

// windowInside evaluates the effective window state for a
// layer at screen x (in 256-space): true means the pixel
// is inside the combined window region.
func (p *PPU) windowInside(layer, x int) bool {
	inv1, en1, inv2, en2 := p.layerWindowSel(layer)
	if !en1 && !en2 {
		return false
	}
	in1 := x >= int(p.wh[0]) && x <= int(p.wh[1])
	in2 := x >= int(p.wh[2]) && x <= int(p.wh[3])
	v1 := in1 != inv1
	v2 := in2 != inv2
	switch {
	case en1 && en2:
		switch p.layerWindowLog(layer) {
		case 0:
			return v1 || v2
		case 1:
			return v1 && v2
		case 2:
			return v1 != v2
		case 3:
			return v1 == v2
		}
	case en1:
		return v1
	default:
		return v2
	}
	return false
}

// applyScreenWindows masks out layer samples on one screen
// where the layer's window mask for that screen is set and
// the pixel is inside the layer's window.
func (p *PPU) maskWindows(s *sample, layer, x int, isSub bool) {
	var mask uint8
	if isSub {
		mask = p.tsw
	} else {
		mask = p.tmw
	}
	if layer <= 4 && mask&(1<<uint(layer)) != 0 && p.windowInside(layer, x) {
		s.opaque = false
	}
}

// colorWindowInside evaluates the color window at x.
func (p *PPU) colorWindowInside(x int) bool { return p.windowInside(5, x) }

// mathAt decides the color-math outcome at a pixel.
// Returns the resulting 15-bit color.
func (p *PPU) mathAt(main, sub sample, x int) uint16 {
	mainColor := main.color
	cw := p.colorWindowInside(x)
	// Clip to black comes first: the main pixel is black
	// (and no math happens on it).
	switch (p.cgwsel >> 0) & 3 {
	case 1:
		if !cw {
			return 0
		}
	case 2:
		if cw {
			return 0
		}
	case 3:
		return 0
	}
	if !main.opaque {
		// The backdrop: math applies per the backdrop bit.
		main.layer = 5
		mainColor = p.cgram[0]
	}
	// Math enable for the winning main layer.
	layerBit := uint(main.layer)
	if main.layer >= 5 {
		layerBit = 5
	}
	if p.cgadsb&(1<<layerBit) == 0 {
		return mainColor
	}
	switch (p.cgwsel >> 2) & 3 {
	case 1:
		if !cw {
			return mainColor
		}
	case 2:
		if cw {
			return mainColor
		}
	case 3:
		return mainColor
	}
	var add uint16
	if sub.opaque {
		add = sub.color
	} else if p.cgwsel&0x80 != 0 {
		// The subscreen backdrop is the addend instead of
		// the fixed color.
		add = p.cgram[0]
	} else {
		add = uint16(p.fixedB)<<10 | uint16(p.fixedG)<<5 | uint16(p.fixedR)
	}
	if p.cgadsb&0x40 != 0 { // halve the addend, per channel
		add = ((add & 0x1F) >> 1) | (((add >> 5) & 0x1F) >> 1 << 5) | (((add >> 10) & 0x1F) >> 1 << 10)
	}
	subMode := p.cgadsb&0x80 != 0
	r := channelMath(uint(mainColor&0x1F), uint(add&0x1F), subMode)
	g := channelMath(uint((mainColor>>5)&0x1F), uint((add>>5)&0x1F), subMode)
	b := channelMath(uint((mainColor>>10)&0x1F), uint((add>>10)&0x1F), subMode)
	return uint16(b<<10 | g<<5 | r)
}

func channelMath(m, a uint, sub bool) uint {
	if sub {
		if m < a {
			return 0
		}
		return m - a
	}
	if m+a > 31 {
		return 31
	}
	return m + a
}

// RenderLine composes display line y (0-based) into the
// framebuffer. Lines past the 224-line framebuffer in
// overscan mode are counted by the machine's timing but
// have nowhere to land, and are dropped here.
func (p *PPU) RenderLine(y int) {
	if y < 0 || y >= Height {
		return
	}
	row := p.frame[y*Width:]
	if p.forcedBlank {
		for i := range row {
			row[i] = 0xFF000000
		}
		return
	}
	mi := p.modeInfo()
	w := Width
	hires := mi.hires || p.setINI&0x10 != 0 // hi-res modes or pseudo-hires
	if hires {
		w = 512
	}
	obj := p.objLine(mi, y, w)
	extbg := p.bgMode == 7 && p.setINI&0x40 != 0
	main := make([]sample, w)
	sub := make([]sample, w)
	for x := 0; x < w; x++ {
		wx := x
		if hires {
			wx = x >> 1
		}
		// Main screen.
		var best sample
		if p.bgMode == 7 {
			if p.tm&0x01 != 0 {
				if s := p.m7Sample(x, y, false); s.opaque {
					best = s
				}
			}
			if extbg && p.tm&0x02 != 0 {
				s2 := p.m7Sample(x, y, true)
				if s2.opaque && (!best.opaque || s2.rank > best.rank) {
					best = s2
				}
			}
		} else {
			for bg := 0; bg < 4; bg++ {
				if mi.bpp[bg] == 0 || p.tm&(1<<uint(bg)) == 0 {
					continue
				}
				s := p.bgSample(mi, bg, x, y, w)
				if s.opaque && (!best.opaque || s.rank > best.rank) {
					best = s
				}
			}
		}
		if p.tm&0x10 != 0 {
			if s := obj[x]; s.opaque && (!best.opaque || s.rank > best.rank) {
				best = s
			}
		}
		main[x] = best
		// Sub screen (no OBJ window masks or TM bits: its
		// own TS enable set).
		var sb sample
		if p.bgMode == 7 {
			if p.ts&0x01 != 0 {
				if s := p.m7Sample(x, y, false); s.opaque {
					sb = s
				}
			}
			if extbg && p.ts&0x02 != 0 {
				s2 := p.m7Sample(x, y, true)
				if s2.opaque && (!sb.opaque || s2.rank > sb.rank) {
					sb = s2
				}
			}
		} else {
			for bg := 0; bg < 4; bg++ {
				if mi.bpp[bg] == 0 || p.ts&(1<<uint(bg)) == 0 {
					continue
				}
				s := p.bgSample(mi, bg, x, y, w)
				if s.opaque && (!sb.opaque || s.rank > sb.rank) {
					sb = s
				}
			}
		}
		if p.ts&0x10 != 0 {
			if s := obj[x]; s.opaque && (!sb.opaque || s.rank > sb.rank) {
				sb = s
			}
		}
		sub[x] = sb
		// Window masks, per screen.
		if main[x].opaque {
			m := main[x]
			p.maskWindows(&m, m.layer, wx, false)
			main[x] = m
		}
		if sub[x].opaque {
			m := sub[x]
			p.maskWindows(&m, m.layer, wx, true)
			sub[x] = m
		}
	}
	if !hires {
		for x := 0; x < w; x++ {
			c := p.mathAt(main[x], sub[x], x)
			row[x] = p.expand(c)
		}
		return
	}
	// Hi-res: adjacent sub-pixel pairs are averaged after
	// color math (the documented 512-to-256 pairing).
	for x := 0; x < Width; x++ {
		c0 := p.mathAt(main[2*x], sub[2*x], x)
		c1 := p.mathAt(main[2*x+1], sub[2*x+1], x)
		row[x] = avgExpand(p.expand(c0), p.expand(c1))
	}
}

// avgExpand averages two XRGB8888 pixels per channel.
func avgExpand(a, b uint32) uint32 {
	r := ((a & 0xFF) + (b & 0xFF)) / 2
	g := (((a >> 8) & 0xFF) + ((b >> 8) & 0xFF)) / 2
	bl := (((a >> 16) & 0xFF) + ((b >> 16) & 0xFF)) / 2
	return 0xFF000000 | bl<<16 | g<<8 | r
}
