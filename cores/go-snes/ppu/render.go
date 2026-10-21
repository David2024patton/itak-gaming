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
	return 0xFF000000 | r<<16 | g<<8 | b
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
	// Tilemap entry bits: 15 flip Y, 14 flip X, 13 priority.
	if entry&0x4000 != 0 { // flip X
		px = tileW - 1 - px
	}
	if entry&0x8000 != 0 { // flip Y
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
	if entry&0x2000 != 0 { // priority bit
		rank = mi.objPrio[bg]
	}
	s := sample{layer: bg, opaque: true, rank: rank}
	if p.bgMode == 3 || p.bgMode == 4 {
		if bg == 0 && bpp == 8 && p.cgwsel&0x01 != 0 {
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

// objSpr is one sprite selected for a line, kept in
// evaluation order.
type objSpr struct {
	x, y     int
	tile     uint16
	pal      int
	prio     int
	flipX    bool
	flipY    bool
	wid, hgt int
}

// objLineB composes the OBJ layer per-pixel samples for
// one display line into out and returns how many opaque
// pixels it wrote: the first sprite (in evaluation
// order, with priority rotation applied) to cover a
// pixel wins it. The sprite structs live in PPU scratch
// arrays, so composing a line allocates nothing.
func (p *PPU) objLineB(mi *modeInfo, y, w int, out *lineBuf) int {
	clear(out.opaque[:w])
	start := 0
	if p.oamPrioRot {
		start = (p.oamAddr >> 2) & 0x7F
	}
	szTab := objSizes[p.objSize]
	nOn := 0
	for i := 0; i < 128; i++ {
		n := (start + i) & 0x7F
		o := n * 4
		hi := p.oam[512+n/4]
		sh := uint((n % 4) * 2)
		x0 := int(p.oam[o]) | int(hi>>sh&1)<<8
		if x0 >= 256 {
			x0 -= 512
		}
		y0 := int(p.oam[o+1])
		sizeBit := (hi >> (sh + 1)) & 1
		sz := szTab[sizeBit]
		sy := y - y0
		if sy < 0 {
			sy += 256
		}
		if sy >= sz[1] {
			continue
		}
		attr := p.oam[o+3]
		p.objOnLine[nOn] = objSpr{
			x: x0, y: y0,
			tile:  uint16(p.oam[o+2]) | uint16(attr&1)<<8,
			pal:   int((attr >> 1) & 0x07),
			prio:  int((attr >> 4) & 0x03),
			flipX: attr&0x40 != 0,
			flipY: attr&0x80 != 0,
			wid:   sz[0], hgt: sz[1],
		}
		nOn++
		if nOn == 33 {
			p.rangeOver = true
			nOn = 32
			break
		}
	}
	// Sprite tile budget: at most 34 8x8 tiles are fetched
	// per line; sprites past the budget are not drawn.
	tiles := 0
	nDraw := 0
	for i := 0; i < nOn; i++ {
		s := &p.objOnLine[i]
		end := s.x + s.wid
		if end > w {
			end = w
		}
		if end <= 0 || s.x >= w {
			continue // fully off-screen horizontally
		}
		// Tiles fetched span from the sprite left edge
		// to its last on-screen pixel.
		n := (end - s.x + 7) / 8
		tiles += n
		if tiles > 34 {
			p.timeOver = true
			break
		}
		p.objDraw[nDraw] = *s
		nDraw++
	}
	// Draw in evaluation order, skipping pixels an
	// earlier sprite already claimed: the first sprite
	// to cover a pixel keeps it, which is the same
	// result the reverse order overwrite produced, with
	// no pixel written twice. Each 8-pixel tile row is
	// decoded once into idxs; px runs in tile space and
	// sx maps it to the screen (mirrored under flipX).
	written := 0
	for i := 0; i < nDraw; i++ {
		s := &p.objDraw[i]
		ry := y - s.y
		if ry < 0 {
			ry += 256
		}
		if s.flipY {
			ry = s.hgt - 1 - ry
		}
		rank := int8(mi.objRank[s.prio])
		ciBase := 128 + s.pal*16
		for cx := 0; cx < s.wid; cx += 8 {
			tile := s.tile + uint16(ry/8)*16 + uint16(cx/8)
			base := p.objBase
			if tile&0x100 != 0 {
				base += p.objGap
			}
			word := (base + (tile&0xFF)*16 + uint16(ry%8)) & 0x7FFF
			w0 := p.vram[word]
			w1 := p.vram[(word+8)&0x7FFF]
			var idxs [8]uint8
			for j := 0; j < 8; j++ {
				bit := uint(7 - j)
				idxs[j] = uint8((w0>>bit)&1) | uint8((w0>>(bit+8))&1)<<1 |
					uint8((w1>>bit)&1)<<2 | uint8((w1>>(bit+8))&1)<<3
			}
			for j := 0; j < 8 && cx+j < s.wid; j++ {
				idx := idxs[j]
				if idx == 0 {
					continue
				}
				px := cx + j
				sx := px
				if s.flipX {
					sx = s.wid - 1 - px
				}
				dx := s.x + sx
				if dx < 0 || dx >= w || out.opaque[dx] {
					continue
				}
				out.color[dx] = p.cgram[ciBase+int(idx)]
				out.rank[dx] = rank
				out.layer[dx] = 4
				out.opaque[dx] = true
				written++
			}
		}
	}
	return written
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
	return windowEval(inv1, en1, inv2, en2, p.layerWindowLog(layer), &p.wh, x)
}

// windowEval is the window test with the layer select bits
// and combination logic already decoded: it reports
// whether x is inside the combined window region.
func windowEval(inv1, en1, inv2, en2 bool, log int, wh *[4]uint8, x int) bool {
	if !en1 && !en2 {
		return false
	}
	in1 := x >= int(wh[0]) && x <= int(wh[1])
	in2 := x >= int(wh[2]) && x <= int(wh[3])
	v1 := in1 != inv1
	v2 := in2 != inv2
	switch {
	case en1 && en2:
		switch log {
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
//
// CGWSEL ($2130) layout, per the SNES hardware
// documentation: bits 7-6 select the region where the
// main screen is forced to black and bits 5-4 select
// the region where the sub screen is forced
// transparent, both encoded 0 = nowhere, 1 = outside
// the color window, 2 = inside the color window,
// 3 = everywhere. Bit 1 selects the addend (0 = fixed
// color, 1 = sub screen); bit 0 is the direct color
// mode used by the 8bpp fetch path, not here. The
// color window itself comes from WOBJSEL bits 4-7;
// when it is not enabled, every pixel counts as
// outside it.
func (p *PPU) mathAt(main, sub sample, x int) uint16 {
	mainColor := main.color
	cw := p.colorWindowInside(x)
	inRegion := func(sel uint8) bool {
		switch sel {
		case 1:
			return !cw
		case 2:
			return cw
		case 3:
			return true
		}
		return false
	}
	// The main screen black region replaces the main
	// pixel with black before math. Color math can
	// still apply on the blacked pixel.
	blacked := inRegion(uint8(p.cgwsel >> 6))
	if blacked {
		mainColor = 0
	}
	if !main.opaque {
		// The backdrop: math applies per the backdrop bit.
		main.layer = 5
		if !blacked {
			mainColor = p.cgram[0]
		}
	}
	// Math enable for the winning main layer.
	layerBit := uint(main.layer)
	if main.layer >= 5 {
		layerBit = 5
	}
	if p.cgadsb&(1<<layerBit) == 0 {
		return mainColor
	}
	// The sub screen transparent region: where it
	// applies (and where no sub layer produced a
	// pixel) the sub screen counts as transparent, and
	// a sub screen addend falls back to the fixed
	// color.
	subTransparent := !sub.opaque || inRegion(uint8(p.cgwsel>>4&3))
	var add uint16
	if p.cgwsel&0x02 != 0 && !subTransparent {
		add = sub.color
	} else {
		add = uint16(p.fixedB)<<10 | uint16(p.fixedG)<<5 | uint16(p.fixedR)
	}
	// Halving divides the math result, not the addend,
	// and is suppressed when the main pixel was forced
	// black or when a sub screen addend fell back
	// because the sub screen was transparent.
	half := p.cgadsb&0x40 != 0 && !blacked && !(p.cgwsel&0x02 != 0 && subTransparent)
	subMode := p.cgadsb&0x80 != 0
	r := channelMath(int(mainColor&0x1F), int(add&0x1F), subMode, half)
	g := channelMath(int((mainColor>>5)&0x1F), int((add>>5)&0x1F), subMode, half)
	b := channelMath(int((mainColor>>10)&0x1F), int((add>>10)&0x1F), subMode, half)
	return uint16(b<<10 | g<<5 | r)
}

// channelMath runs one 5-bit channel through the
// add/subtract, optional halving, and clamp pipeline.
func channelMath(m, a int, sub, half bool) int {
	d := m + a
	if sub {
		d = m - a
	}
	if half {
		d >>= 1
	}
	if d < 0 {
		return 0
	}
	if d > 31 {
		return 31
	}
	return d
}

// RenderLine composes display line y (0-based) into the
// framebuffer. Lines past the 224-line framebuffer in
// overscan mode are counted by the machine timing but
// have nowhere to land, and are dropped here.
//
// Composition follows the documented priority tables for
// each background mode: every layer produces, per pixel, a
// color and a numeric rank, and the highest rank wins on
// each screen (main and sub are composed independently,
// then color math combines them). Layers render once into
// PPU scratch line buffers and merge into both screens.
func (p *PPU) RenderLine(y int) {
	if y < 0 || y >= Height {
		return
	}
	// The row is exactly one display line wide; every
	// write below indexes it by a display x. (The slice
	// used to run to the end of the framebuffer, which
	// made the forced blank fill below repaint the whole
	// rest of the frame on every blanked line.)
	row := p.frame[y*Width : y*Width+Width]
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
	p.lineStamp++
	if p.lineStamp == 0 {
		for i := range p.rowMemo {
			p.rowMemo[i].stamp = 0
		}
		p.lineStamp = 1
	}
	p.prepLineWindows(w, hires)
	main := &p.lbMain
	sub := &p.lbSub
	clear(main.opaque[:w])
	clear(sub.opaque[:w])
	extbg := p.bgMode == 7 && p.setINI&0x40 != 0
	if p.bgMode == 7 {
		if (p.tm|p.ts)&0x01 != 0 {
			if n := p.m7Line(y, w, false, &p.lbBG[0]); n > 0 {
				if p.tm&0x01 != 0 {
					mergeLine(main, &p.lbBG[0], w)
				}
				if p.ts&0x01 != 0 {
					mergeLine(sub, &p.lbBG[0], w)
				}
			}
		}
		if extbg && (p.tm|p.ts)&0x02 != 0 {
			if n := p.m7Line(y, w, true, &p.lbBG[1]); n > 0 {
				if p.tm&0x02 != 0 {
					mergeLine(main, &p.lbBG[1], w)
				}
				if p.ts&0x02 != 0 {
					mergeLine(sub, &p.lbBG[1], w)
				}
			}
		}
	} else {
		for bg := 0; bg < 4; bg++ {
			if mi.bpp[bg] == 0 || (p.tm|p.ts)&(1<<uint(bg)) == 0 {
				continue
			}
			onMain := p.tm&(1<<uint(bg)) != 0
			onSub := p.ts&(1<<uint(bg)) != 0
			switch {
			case onMain && onSub:
				if n := p.bgLine(&mi, bg, y, w, &p.lbBG[bg], false); n > 0 {
					mergeLine(main, &p.lbBG[bg], w)
					mergeLine(sub, &p.lbBG[bg], w)
				}
			case onMain:
				// One screen only: compose straight
				// into it, testing each pixel against
				// the current winner, and skip the
				// scratch buffer and merge pass.
				p.bgLine(&mi, bg, y, w, main, true)
			default:
				p.bgLine(&mi, bg, y, w, sub, true)
			}
		}
	}
	if (p.tm|p.ts)&0x10 != 0 {
		if n := p.objLineB(&mi, y, w, &p.lbObj); n > 0 {
			if p.tm&0x10 != 0 {
				mergeLine(main, &p.lbObj, w)
			}
			if p.ts&0x10 != 0 {
				mergeLine(sub, &p.lbObj, w)
			}
		}
	}
	// Window masks, per screen: where the winning layer
	// mask bit for that screen is set and the pixel is
	// inside the layer window, the winner drops out.
	if p.tmw != 0 {
		wm := p.winMask[:w]
		for x := 0; x < w; x++ {
			if main.opaque[x] {
				l := main.layer[x]
				if l <= 4 && p.tmw&(1<<uint(l)) != 0 && wm[x]&(1<<uint(l)) != 0 {
					main.opaque[x] = false
				}
			}
		}
	}
	if p.tsw != 0 {
		wm := p.winMask[:w]
		for x := 0; x < w; x++ {
			if sub.opaque[x] {
				l := sub.layer[x]
				if l <= 4 && p.tsw&(1<<uint(l)) != 0 && wm[x]&(1<<uint(l)) != 0 {
					sub.opaque[x] = false
				}
			}
		}
	}
	tab := p.expandTable()
	if !hires {
		if p.cgadsb == 0 && p.cgwsel&0xC0 == 0 {
			// Color math is off for every layer and
			// the main screen black region selector
			// is 0 (nowhere), so no pixel is blacked:
			// the output is the winning color or the
			// backdrop, expanded.
			backdrop := p.cgram[0]
			for x := 0; x < w; x++ {
				c := main.color[x]
				if !main.opaque[x] {
					c = backdrop
				}
				row[x] = tab[c]
			}
			return
		}
		if p.cgadsb == 0 {
			// Color math is off for every layer, so
			// mathLine reduces to the black region
			// test and the backdrop substitution.
			backdrop := p.cgram[0]
			for x := 0; x < w; x++ {
				c := main.color[x]
				if p.blackAt[x] {
					c = 0
				} else if !main.opaque[x] {
					c = backdrop
				}
				row[x] = tab[c]
			}
			return
		}
		if p.cgwsel&0x02 == 0 || p.ts == 0 {
			// The math addend is one fixed color for
			// the whole line: either CGWSEL selects
			// the fixed color outright, or no layer
			// is enabled on the sub screen, which
			// makes the sub screen transparent at
			// every pixel and the addend fall back
			// to the fixed color (halving is then
			// suppressed whenever the sub screen was
			// the selected addend).
			add := uint16(p.fixedB)<<10 | uint16(p.fixedG)<<5 | uint16(p.fixedR)
			halfOK := p.cgadsb&0x40 != 0 && p.cgwsel&0x02 == 0
			subMode := p.cgadsb&0x80 != 0
			backdrop := p.cgram[0]
			// Per channel math tables for the fixed
			// addend: lut[variant][value], variant 1
			// the halved math. Building 32 entries
			// per channel once replaces three
			// channelMath calls per pixel with three
			// table loads; the tables are built from
			// channelMath itself, so they agree with
			// the general path by construction.
			var lutR, lutG, lutB [2][32]uint16
			ar, ag, ab := int(add&0x1F), int((add>>5)&0x1F), int((add>>10)&0x1F)
			for v := 0; v < 32; v++ {
				lutR[0][v] = uint16(channelMath(v, ar, subMode, false))
				lutG[0][v] = uint16(channelMath(v, ag, subMode, false))
				lutB[0][v] = uint16(channelMath(v, ab, subMode, false))
				if halfOK {
					lutR[1][v] = uint16(channelMath(v, ar, subMode, true))
					lutG[1][v] = uint16(channelMath(v, ag, subMode, true))
					lutB[1][v] = uint16(channelMath(v, ab, subMode, true))
				}
			}
			var mathOn [6]bool
			for l := 0; l < 6; l++ {
				mathOn[l] = p.cgadsb&(1<<uint(l)) != 0
			}
			for x := 0; x < w; x++ {
				c := main.color[x]
				blacked := p.blackAt[x]
				if blacked {
					c = 0
				}
				layer := main.layer[x]
				if !main.opaque[x] {
					layer = 5
					if !blacked {
						c = backdrop
					}
				}
				if mathOn[layer] {
					h := 0
					if halfOK && !blacked {
						h = 1
					}
					c = lutB[h][(c>>10)&0x1F]<<10 | lutG[h][(c>>5)&0x1F]<<5 | lutR[h][c&0x1F]
				}
				row[x] = tab[c]
			}
			return
		}
		// General case: the sub screen supplies the
		// addend where it is opaque and outside its
		// transparent region. This is mathLine's body
		// inlined into the line loop, with the fixed
		// color packed once, so the per pixel cost
		// carries no function call. Pixels whose sub
		// screen is transparent fall back to the fixed
		// addend with halving suppressed, which is a
		// fixed function of the main color, so those
		// pixels go through per channel tables built
		// from channelMath; only true sub addend
		// pixels run the math inline.
		fixed := uint16(p.fixedB)<<10 | uint16(p.fixedG)<<5 | uint16(p.fixedR)
		backdrop := p.cgram[0]
		subMode := p.cgadsb&0x80 != 0
		halfBit := p.cgadsb&0x40 != 0
		var mathOn [6]bool
		for l := 0; l < 6; l++ {
			mathOn[l] = p.cgadsb&(1<<uint(l)) != 0
		}
		var fbR, fbG, fbB [32]uint16
		far, fag, fab := int(fixed&0x1F), int((fixed>>5)&0x1F), int((fixed>>10)&0x1F)
		for v := 0; v < 32; v++ {
			fbR[v] = uint16(channelMath(v, far, subMode, false))
			fbG[v] = uint16(channelMath(v, fag, subMode, false))
			fbB[v] = uint16(channelMath(v, fab, subMode, false))
		}
		for x := 0; x < w; x++ {
			c := main.color[x]
			blacked := p.blackAt[x]
			if blacked {
				c = 0
			}
			layer := main.layer[x]
			if !main.opaque[x] {
				layer = 5
				if !blacked {
					c = backdrop
				}
			}
			if mathOn[layer] {
				if !sub.opaque[x] || p.subRegAt[x] {
					c = fbB[(c>>10)&0x1F]<<10 | fbG[(c>>5)&0x1F]<<5 | fbR[c&0x1F]
				} else {
					add := sub.color[x]
					half := halfBit && !blacked
					r := channelMath(int(c&0x1F), int(add&0x1F), subMode, half)
					g := channelMath(int((c>>5)&0x1F), int((add>>5)&0x1F), subMode, half)
					b := channelMath(int((c>>10)&0x1F), int((add>>10)&0x1F), subMode, half)
					c = uint16(b<<10 | g<<5 | r)
				}
			}
			row[x] = tab[c]
		}
		return
	}
	// Hi-res: adjacent sub-pixel pairs are averaged after
	// color math (the documented 512-to-256 pairing).
	for x := 0; x < Width; x++ {
		c0 := p.mathLine(main, sub, 2*x)
		c1 := p.mathLine(main, sub, 2*x+1)
		row[x] = avgExpand(tab[c0], tab[c1])
	}
}

// avgExpand averages two XRGB8888 pixels per channel.
func avgExpand(a, b uint32) uint32 {
	r := ((a & 0xFF) + (b & 0xFF)) / 2
	g := (((a >> 8) & 0xFF) + ((b >> 8) & 0xFF)) / 2
	bl := (((a >> 16) & 0xFF) + ((b >> 16) & 0xFF)) / 2
	return 0xFF000000 | bl<<16 | g<<8 | r
}

// --- fast line composition ----------------------------------
//
// The line buffers and per-line window precomputation used
// by RenderLine. Background layers render tile by tile (a
// tile row is decoded once for its 8 or 16 pixels) into
// scratch buffers owned by the PPU, so composing a frame
// allocates nothing. Sampling paths that are per-pixel by
// nature (mosaic, offset-per-tile) fall back to bgSample,
// which remains the reference implementation of the
// sampling rules; both paths feed the same merge, window,
// and color math code.

// lineBuf is one layer or one screen worth of samples for
// a scanline, in parallel arrays: the winning color, rank,
// and layer index per pixel.
type lineBuf struct {
	color  [512]uint16
	rank   [512]int8
	layer  [512]int8
	opaque [512]bool
}

// expandTable returns the table mapping every 15-bit
// color to its expanded XRGB8888 pixel at the current
// brightness, rebuilding it when the brightness register
// has changed since the last build (in practice only
// during fades). The table is built from expand itself,
// so the fast path agrees with it by construction.
func (p *PPU) expandTable() *[32768]uint32 {
	if !p.expValid || p.expBright != p.brightness {
		for c := 0; c < 32768; c++ {
			p.expTab[c] = p.expand(uint16(c))
		}
		p.expBright = p.brightness
		p.expValid = true
	}
	return &p.expTab
}

// regionAt evaluates one CGWSEL region selector against
// the color window state at a pixel: 0 means nowhere, 1
// outside the window, 2 inside it, 3 everywhere.
func regionAt(sel uint8, cw bool) bool {
	switch sel {
	case 1:
		return !cw
	case 2:
		return cw
	case 3:
		return true
	}
	return false
}

// winTrueRun is a pre-filled span of true values, so
// window verdict runs fill with copy instead of a
// per-pixel store loop.
var winTrueRun [512]bool

// planeBits expands one byte into the 8 per pixel bit
// contributions of one bitplane: planeBits[plane][v][i]
// is bit (7-i) of v, shifted into plane's position in
// the pixel index (or zero). decodeRowU8 combines one
// table per plane instead of shifting bit by bit.
var planeBits [8][256][8]uint8

func init() {
	for i := range winTrueRun {
		winTrueRun[i] = true
	}
	for plane := 0; plane < 8; plane++ {
		for v := 0; v < 256; v++ {
			for i := 0; i < 8; i++ {
				if v>>(7-i)&1 != 0 {
					planeBits[plane][v][i] = 1 << uint(plane)
				}
			}
		}
	}
}

// fillWindowRuns fills dst[0..w) with the layer window
// verdict. The verdict can only change where x crosses a
// window edge (edges sit at wh values, and just past them),
// so it evaluates one point per run between consecutive
// edges and fills the run. Windows are defined in
// 256-space; hi-res sampling evaluates at x>>1, which maps
// each verdict to an adjacent pixel pair.
func fillWindowRuns(dst *[512]bool, inv1, en1, inv2, en2 bool, log int, wh *[4]uint8, w int, hires bool) {
	var pts [7]int
	pts[0] = 0
	pts[1] = 256
	pts[2] = int(wh[0])
	pts[3] = int(wh[1]) + 1
	pts[4] = int(wh[2])
	pts[5] = int(wh[3]) + 1
	pts[6] = 256
	// Insertion sort of the few candidate edges.
	for i := 1; i < len(pts); i++ {
		for j := i; j > 0 && pts[j] < pts[j-1]; j-- {
			pts[j], pts[j-1] = pts[j-1], pts[j]
		}
	}
	prev := -1
	for i := 0; i < len(pts); i++ {
		a := pts[i]
		if a < 0 {
			a = 0
		}
		if a > 256 {
			a = 256
		}
		if a == prev {
			continue
		}
		prev = a
		b := 256
		for k := i + 1; k < len(pts); k++ {
			c := pts[k]
			if c < 0 {
				c = 0
			}
			if c > 256 {
				c = 256
			}
			if c > a {
				b = c
				break
			}
		}
		v := windowEval(inv1, en1, inv2, en2, log, wh, a)
		lo, hi := a, b
		if hires {
			lo, hi = a*2, b*2
		}
		if lo > w {
			lo = w
		}
		if hi > w {
			hi = w
		}
		if v {
			copy(dst[lo:hi], winTrueRun[:hi-lo])
		} else {
			clear(dst[lo:hi])
		}
	}
}

// fillWindowMaskRuns is fillWindowRuns for the packed
// verdict byte: it walks the same edges and sets the
// layer's bit across the runs whose verdict is true.
// False runs write nothing: prepLineWindows clears the
// array per line and each layer owns exactly one bit.
func fillWindowMaskRuns(dst *[512]uint8, bit uint8, inv1, en1, inv2, en2 bool, log int, wh *[4]uint8, w int, hires bool) {
	var pts [7]int
	pts[0] = 0
	pts[1] = 256
	pts[2] = int(wh[0])
	pts[3] = int(wh[1]) + 1
	pts[4] = int(wh[2])
	pts[5] = int(wh[3]) + 1
	pts[6] = 256
	for i := 1; i < len(pts); i++ {
		for j := i; j > 0 && pts[j] < pts[j-1]; j-- {
			pts[j], pts[j-1] = pts[j-1], pts[j]
		}
	}
	prev := -1
	for i := 0; i < len(pts); i++ {
		a := pts[i]
		if a < 0 {
			a = 0
		}
		if a > 256 {
			a = 256
		}
		if a == prev {
			continue
		}
		prev = a
		b := 256
		for k := i + 1; k < len(pts); k++ {
			c := pts[k]
			if c < 0 {
				c = 0
			}
			if c > 256 {
				c = 256
			}
			if c > a {
				b = c
				break
			}
		}
		if !windowEval(inv1, en1, inv2, en2, log, wh, a) {
			continue
		}
		lo, hi := a, b
		if hires {
			lo, hi = a*2, b*2
		}
		if lo > w {
			lo = w
		}
		if hi > w {
			hi = w
		}
		for x := lo; x < hi; x++ {
			dst[x] |= bit
		}
	}
}

// prepLineWindows decodes the layers window settings once
// per line, evaluates the window regions, and derives the
// two color window regions color math needs (blackAt for
// the main screen, subRegAt for the sub screen). The
// background and OBJ verdicts land in winMask, one bit
// per layer in a per pixel byte, because the winner mask
// pass reads them by winning layer and a packed load
// beats gathering from six bool arrays; the color window
// verdict keeps its own array in winIn[5], which the
// region fills read. Only layers whose verdict can be
// read are evaluated: background and OBJ verdicts feed
// the winner masks (gated by TMW/TSW), the color window
// always feeds color math.
func (p *PPU) prepLineWindows(w int, hires bool) {
	clear(p.winMask[:w])
	for layer := 0; layer < 6; layer++ {
		if layer < 5 && (p.tmw|p.tsw)&(1<<uint(layer)) == 0 {
			continue // no mask reads this layer verdict
		}
		inv1, en1, inv2, en2 := p.layerWindowSel(layer)
		if layer == 5 {
			dst := &p.winIn[5]
			if !en1 && !en2 {
				// windowEval is false everywhere.
				clear(dst[:w])
				continue
			}
			fillWindowRuns(dst, inv1, en1, inv2, en2, p.layerWindowLog(layer), &p.wh, w, hires)
			continue
		}
		if !en1 && !en2 {
			continue // verdict false everywhere: the bit stays clear
		}
		fillWindowMaskRuns(&p.winMask, uint8(1<<uint(layer)), inv1, en1, inv2, en2, p.layerWindowLog(layer), &p.wh, w, hires)
	}
	blackSel := uint8(p.cgwsel >> 6)
	subSel := uint8(p.cgwsel >> 4 & 3)
	cw := &p.winIn[5]
	// Each region is filled by its selector: nowhere
	// clears, everywhere fills, inside copies the color
	// window verdicts, outside inverts them.
	fillRegion(p.blackAt[:w], blackSel, cw, w)
	fillRegion(p.subRegAt[:w], subSel, cw, w)
}

// fillRegion fills dst[0..w) with one CGWSEL region
// selector evaluated against the color window verdicts
// in cw: 0 means nowhere, 1 outside the window, 2 inside
// it, 3 everywhere.
func fillRegion(dst []bool, sel uint8, cw *[512]bool, w int) {
	switch sel {
	case 0:
		clear(dst)
	case 3:
		copy(dst, winTrueRun[:w])
	case 2:
		copy(dst, cw[:w])
	default:
		for x := 0; x < w; x++ {
			dst[x] = !cw[x]
		}
	}
}

// mergeLine folds src into dst with the composition rule:
// an opaque sample replaces the current winner only when
// its rank is strictly higher, so earlier layers keep ties.
func mergeLine(dst, src *lineBuf, w int) {
	sc, sr, sl, so := src.color[:w], src.rank[:w], src.layer[:w], src.opaque[:w]
	dc, dr, dl, do := dst.color[:w], dst.rank[:w], dst.layer[:w], dst.opaque[:w]
	for x := range so {
		if so[x] && (!do[x] || sr[x] > dr[x]) {
			dc[x] = sc[x]
			dr[x] = sr[x]
			dl[x] = sl[x]
			do[x] = true
		}
	}
}

// tileRowEntry is one decoded tile row in the per-line
// decode memo.
type tileRowEntry struct {
	key   uint32
	stamp uint32
	pix   [8]uint8
}

// tileRow returns the decoded 8-pixel row of a tile,
// decoding on a memo miss. The memo lives for one
// scanline (RenderLine bumps lineStamp per line): VRAM
// cannot change while a line composes, and a tile row
// repeats within a line whenever a flat area reuses the
// tile or another layer samples the same tile, so repeat
// lookups are free.
func (p *PPU) tileRow(b *bgLayer, tile uint16, row, bpp int) *[8]uint8 {
	key := uint32(b.chrBase)<<15 | uint32(tile)<<5 | uint32(row)<<2 | uint32(bpp>>2)
	if p.lastRowStamp == p.lineStamp && p.lastRowKey == key {
		return &p.lastRowPix
	}
	e := &p.rowMemo[(key^(key>>7))&127]
	if e.stamp != p.lineStamp || e.key != key {
		p.decodeRowU8(b, tile, row, bpp, &e.pix)
		e.key = key
		e.stamp = p.lineStamp
	}
	p.lastRowKey, p.lastRowStamp = key, p.lineStamp
	p.lastRowPix = e.pix
	return &p.lastRowPix
}

// decodeRowU8 decodes one 8-pixel tile row (row 0..7) at
// 2, 4 or 8 bits per pixel into dst. The planar layout
// puts one word per plane pair per row, the pairs spaced
// 8 words apart; a tile holds 4 words per bit of depth.
// Each plane's byte expands through planeBits, and the
// per pixel index is the OR of the plane contributions.
func (p *PPU) decodeRowU8(b *bgLayer, tile uint16, row, bpp int, dst *[8]uint8) {
	base := (b.chrBase + tile*uint16(4*bpp)) & 0x7FFF
	w0 := p.vram[(base+uint16(row))&0x7FFF]
	lo := &planeBits[0][w0&0xFF]
	hi := &planeBits[1][w0>>8]
	for i := 0; i < 8; i++ {
		dst[i] = lo[i] | hi[i]
	}
	if bpp < 4 {
		return
	}
	w1 := p.vram[(base+uint16(8+row))&0x7FFF]
	lo = &planeBits[2][w1&0xFF]
	hi = &planeBits[3][w1>>8]
	for i := 0; i < 8; i++ {
		dst[i] |= lo[i] | hi[i]
	}
	if bpp < 8 {
		return
	}
	w2 := p.vram[(base+uint16(16+row))&0x7FFF]
	w3 := p.vram[(base+uint16(24+row))&0x7FFF]
	lo2 := &planeBits[4][w2&0xFF]
	hi2 := &planeBits[5][w2>>8]
	lo3 := &planeBits[6][w3&0xFF]
	hi3 := &planeBits[7][w3>>8]
	for i := 0; i < 8; i++ {
		dst[i] |= lo2[i] | hi2[i] | lo3[i] | hi3[i]
	}
}

// emitSpan records one opaque background pixel into a
// tile span's slices (the span of a lineBuf being
// composed, cut to the span's length so the per pixel
// indexing is provably in range), resolving its color
// through CGRAM or the direct-color packing.
func (p *PPU) emitSpan(cols []uint16, rks []int8, lys []int8, ops []bool, i, idx int, rank int8, bg int, direct bool, ciBase int) {
	rks[i] = rank
	lys[i] = int8(bg)
	ops[i] = true
	if direct {
		// Direct color: the 8-bit index is the
		// color, in the documented BBGGGRRR packing
		// expanded to 15 bits (low bits zero).
		cols[i] = uint16(idx&0x07)<<2 | uint16((idx>>3)&0x07)<<7 | uint16((idx>>6)&0x03)<<13
	} else {
		cols[i] = p.cgram[(ciBase+idx)&0xFF]
	}
}

// bgLine renders background bg samples for display line y
// into out and returns how many opaque pixels it wrote,
// following bgSample exactly. The tile-walking fast path
// serves any layer whose sampling has no per-pixel
// variance this line; mosaic and offset-per-tile layers
// take the per-pixel reference path.
//
// In fused mode out is a screen buffer that already
// holds earlier layers' winners: the buffer is not
// cleared, and a pixel is written only when it beats
// the current winner, which is exactly the merge rule
// (strictly higher rank wins, ties keep the earlier
// layer). Layers render in the same order either way,
// so fused and merged composition agree pixel for
// pixel.
func (p *PPU) bgLine(mi *modeInfo, bg, y, w int, out *lineBuf, fused bool) int {
	if !fused {
		clear(out.opaque[:w])
	}
	if _, ok := p.optApplies(bg); ok || p.mosaicEnabled(bg) {
		cnt := 0
		for x := 0; x < w; x++ {
			s := p.bgSample(*mi, bg, x, y, w)
			if s.opaque && (!fused || !out.opaque[x] || int8(s.rank) > out.rank[x]) {
				out.color[x] = s.color
				out.rank[x] = int8(s.rank)
				out.layer[x] = int8(s.layer)
				out.opaque[x] = true
				cnt++
			}
		}
		return cnt
	}
	b := &p.bg[bg]
	bpp := mi.bpp[bg]
	hofs := int(b.hofs & 0x3FF)
	vofs := int(b.vofs & 0x7FF)
	tileW, tileH := 8, 8
	if b.tile16 || mi.hires {
		tileW = 16
	}
	if b.tile16 {
		tileH = 16
	}
	gy := y + vofs + 1 // the documented vertical +1
	ty := (gy / tileH) & (b.tilemapH - 1)
	py := gy % tileH
	// The tilemap row address for this line, split the
	// way tilemapEntry splits it: the row part is fixed
	// for the whole line, so per tile only the in-screen
	// column offset is added.
	tmTy := ty
	tmSy := 0
	if tmTy >= 32 {
		tmSy = 1
		tmTy -= 32
	}
	tmStride := 1
	if b.tilemapW == 64 {
		tmStride = 2
	}
	tmRowBase := b.tilemapBase + uint16(tmSy*tmStride*0x400+tmTy*32)
	tmWMask := b.tilemapW - 1
	direct := (p.bgMode == 3 || p.bgMode == 4) && bg == 0 && bpp == 8 && p.cgwsel&0x01 != 0
	mode0 := p.bgMode == 0
	cnt := 0
	x := 0
	gx := hofs
	for x < w {
		tx := (gx / tileW) & tmWMask
		sx := 0
		if tx >= 32 {
			sx = 1
			tx -= 32
		}
		entry := p.vram[(tmRowBase+uint16(sx*0x400+tx))&0x7FFF]
		rowInTile := py
		if entry&0x8000 != 0 { // flip Y
			rowInTile = tileH - 1 - py
		}
		tile := entry & 0x3FF
		row := rowInTile
		if tileH == 16 {
			tile += uint16(rowInTile/8) * 16
			row = rowInTile % 8
		}
		ra := *p.tileRow(b, tile, row, bpp)
		var rb [8]uint8
		if tileW == 16 {
			rb = *p.tileRow(b, tile+1, row, bpp)
		}
		pal := int((entry >> 10) & 0x07)
		ciBase := pal * (1 << uint(bpp))
		if mode0 {
			// Mode 0 gives each background its own
			// 32-color quarter of CGRAM.
			ciBase = bg*32 + pal*4
		}
		rank := int8(mi.objBase[bg])
		if entry&0x2000 != 0 { // priority bit
			rank = int8(mi.objPrio[bg])
		}
		flipX := entry&0x4000 != 0
		startPx := gx % tileW
		n := tileW - startPx
		if x+n > w {
			n = w - x
		}
		// Emit the part of the tile on screen. The span
		// is cut into its own slices first, so the per
		// pixel loop indexes by the span offset, which
		// the compiler can prove in range. The index
		// walk is specialized by tile width and flip
		// state, so the per-pixel path carries no
		// branches but the transparency test.
		cols := out.color[x : x+n]
		rks := out.rank[x : x+n]
		lys := out.layer[x : x+n]
		ops := out.opaque[x : x+n]
		switch {
		case tileW == 8 && !flipX:
			for i := 0; i < n; i++ {
				if idx := int(ra[startPx+i]); idx != 0 && (!fused || !ops[i] || rank > rks[i]) {
					p.emitSpan(cols, rks, lys, ops, i, idx, rank, bg, direct, ciBase)
					cnt++
				}
			}
		case tileW == 8:
			base := 7 - startPx
			for i := 0; i < n; i++ {
				if idx := int(ra[base-i]); idx != 0 && (!fused || !ops[i] || rank > rks[i]) {
					p.emitSpan(cols, rks, lys, ops, i, idx, rank, bg, direct, ciBase)
					cnt++
				}
			}
		case !flipX:
			for i := 0; i < n; i++ {
				pxv := startPx + i
				var idx int
				if pxv < 8 {
					idx = int(ra[pxv])
				} else {
					idx = int(rb[pxv-8])
				}
				if idx != 0 && (!fused || !ops[i] || rank > rks[i]) {
					p.emitSpan(cols, rks, lys, ops, i, idx, rank, bg, direct, ciBase)
					cnt++
				}
			}
		default:
			base := 15 - startPx
			for i := 0; i < n; i++ {
				pxv := base - i
				var idx int
				if pxv < 8 {
					idx = int(ra[pxv])
				} else {
					idx = int(rb[pxv-8])
				}
				if idx != 0 && (!fused || !ops[i] || rank > rks[i]) {
					p.emitSpan(cols, rks, lys, ops, i, idx, rank, bg, direct, ciBase)
					cnt++
				}
			}
		}
		x += n
		gx += n
	}
	return cnt
}

// m7Line renders the Mode 7 layer (or its EXTBG variant)
// for display line y into out and returns how many
// opaque pixels it wrote, sampling per pixel through
// m7Sample.
func (p *PPU) m7Line(y, w int, extbg bool, out *lineBuf) int {
	clear(out.opaque[:w])
	cnt := 0
	for x := 0; x < w; x++ {
		s := p.m7Sample(x, y, extbg)
		if s.opaque {
			out.color[x] = s.color
			out.rank[x] = int8(s.rank)
			out.layer[x] = int8(s.layer)
			out.opaque[x] = true
			cnt++
		}
	}
	return cnt
}

// mathLine decides the color-math outcome at pixel x from
// the composed main and sub winners. It is mathAt with the
// window regions precomputed by prepLineWindows.
func (p *PPU) mathLine(main, sub *lineBuf, x int) uint16 {
	mainColor := main.color[x]
	blacked := p.blackAt[x]
	if blacked {
		mainColor = 0
	}
	layer := main.layer[x]
	if !main.opaque[x] {
		// The backdrop: math applies per the backdrop bit.
		layer = 5
		if !blacked {
			mainColor = p.cgram[0]
		}
	}
	if p.cgadsb&(1<<uint(layer)) == 0 {
		return mainColor
	}
	// The sub screen transparent region: where it applies
	// (and where no sub layer produced a pixel) the sub
	// screen counts as transparent, and a sub screen
	// addend falls back to the fixed color.
	subTransparent := !sub.opaque[x] || p.subRegAt[x]
	var add uint16
	if p.cgwsel&0x02 != 0 && !subTransparent {
		add = sub.color[x]
	} else {
		add = uint16(p.fixedB)<<10 | uint16(p.fixedG)<<5 | uint16(p.fixedR)
	}
	// Halving divides the math result, not the addend, and
	// is suppressed when the main pixel was forced black
	// or when a sub screen addend fell back because the
	// sub screen was transparent.
	half := p.cgadsb&0x40 != 0 && !blacked && !(p.cgwsel&0x02 != 0 && subTransparent)
	subMode := p.cgadsb&0x80 != 0
	r := channelMath(int(mainColor&0x1F), int(add&0x1F), subMode, half)
	g := channelMath(int((mainColor>>5)&0x1F), int((add>>5)&0x1F), subMode, half)
	b := channelMath(int((mainColor>>10)&0x1F), int((add>>10)&0x1F), subMode, half)
	return uint16(b<<10 | g<<5 | r)
}
