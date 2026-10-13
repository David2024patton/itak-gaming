package gpu

// GP0 command processing and software rasterization.
// See the package comment for the rendering model and
// the one under-specified step (line walker phase).

// ----------------------------------------------------------------
// Command intake
// ----------------------------------------------------------------

// WriteGP0 feeds one word to the GP0 port: commands
// are assembled and executed immediately; CPU to
// VRAM transfer data words are consumed directly.
func (g *GPU) WriteGP0(w uint32) {
	if g.polyline {
		g.feedPolyline(w)
		return
	}
	if g.xferActive && !g.xferRead {
		g.feedTransfer(w)
		return
	}
	if g.bufLen == 0 {
		cmd := uint8(w >> 24)
		if immediateCommand(cmd) {
			g.execImmediate(w)
			return
		}
		g.cmd = cmd
		g.buf[0] = w
		g.bufLen = 1
		g.bufNeed = commandLength(cmd)
		if g.bufNeed <= 1 {
			g.execBuffered()
			g.bufLen = 0
		}
		return
	}
	// Polyline commands start streaming after their
	// first vertex word.
	if g.bufLen < len(g.buf) {
		g.buf[g.bufLen] = w
	}
	g.bufLen++
	if isPolylineCmd(g.cmd) && g.bufLen == 2 {
		g.startPolyline()
		return
	}
	if g.bufLen >= g.bufNeed {
		g.execBuffered()
		g.bufLen = 0
	}
}

// immediateCommand reports whether a command word is
// complete in one word: environment commands, the
// IRQ request command and 1-word misc commands.
func immediateCommand(cmd uint8) bool {
	if cmd >= 0xE0 {
		return true // environment block
	}
	if cmd == 0x1F {
		return true // interrupt request
	}
	if cmd < 0x20 {
		return cmd != 0x02 // misc, except fill (3 words)
	}
	return false
}

// commandLength returns the total word count of a
// buffered GP0 command, including the command word.
func commandLength(cmd uint8) int {
	switch {
	case cmd < 0x20: // fill rectangle (0x02)
		return 3
	case cmd < 0x40: // polygons
		verts := 3
		if cmd&0x08 != 0 {
			verts = 4
		}
		n := 1 + verts
		if cmd&0x10 != 0 { // gouraud: a color before each extra vertex
			n += verts - 1
		}
		if cmd&0x04 != 0 { // textured: a UV word per vertex
			n += verts
		}
		return n
	case cmd < 0x60: // lines
		if cmd&0x08 != 0 {
			return 2 // polyline: command + first vertex, then streams
		}
		if cmd&0x10 != 0 {
			return 4
		}
		return 3
	case cmd < 0x80: // rectangles
		n := 2
		if cmd&0x04 != 0 {
			n++
		}
		if (cmd>>3)&3 == 0 {
			n++ // variable size word
		}
		return n
	case cmd < 0xA0: // VRAM to VRAM
		return 4
	case cmd < 0xC0: // CPU to VRAM: 3 header words then data
		return 3
	default: // VRAM to CPU
		return 3
	}
}

func isPolylineCmd(cmd uint8) bool {
	return cmd >= 0x40 && cmd < 0x60 && cmd&0x08 != 0
}

// execImmediate runs a single-word command.
func (g *GPU) execImmediate(w uint32) {
	cmd := uint8(w >> 24)
	p := w & 0xFFFFFF
	switch {
	case cmd == 0x1F: // interrupt request
		g.irq = true
	case cmd >= 0xE0:
		g.execEnv(cmd, p)
	}
	// Other one-word misc commands (0x00 nop, 0x01
	// clear cache, 0x03-0x1E) have no visible effect.
}

// execEnv runs the GP0 environment commands E1-E6.
func (g *GPU) execEnv(cmd uint8, p uint32) {
	switch cmd {
	case 0xE1: // draw mode
		g.page.xBase = int(p&0xF) * 64
		g.page.yBase = int((p>>4)&1) * 256
		g.page.semiMode = int((p >> 5) & 3)
		g.page.colorFmt = int((p >> 7) & 3)
		g.dither = p&(1<<9) != 0
		g.drawToDisp = p&(1<<10) != 0
		if p&(1<<11) != 0 {
			g.page.yBase |= 512
		}
		g.texDisable = p&(1<<15) != 0
		g.rectFlipX = p&(1<<12) != 0
		g.rectFlipY = p&(1<<13) != 0
	case 0xE2: // texture window
		g.texWindow = p & 0xFFFFF
		g.winMaskX = int(p & 0x1F)
		g.winMaskY = int((p >> 5) & 0x1F)
		g.winOffX = int((p >> 10) & 0x1F)
		g.winOffY = int((p >> 15) & 0x1F)
	case 0xE3: // drawing area top-left
		g.drawX1 = int(p & 0x3FF)
		g.drawY1 = int((p >> 10) & 0x1FF)
	case 0xE4: // drawing area bottom-right
		g.drawX2 = int(p & 0x3FF)
		g.drawY2 = int((p >> 10) & 0x1FF)
	case 0xE5: // drawing offset
		g.offX = signExtend(int(p&0x7FF), 11)
		g.offY = signExtend(int((p>>11)&0x7FF), 11)
	case 0xE6: // mask bit
		g.maskSet = p&1 != 0
		g.maskCheck = p&2 != 0
	}
}

func signExtend(v, bits int) int {
	if v&(1<<(bits-1)) != 0 {
		return v - (1 << bits)
	}
	return v
}

// decodeVertex unpacks a vertex word: X in bits 0-10
// signed, Y in bits 16-26 signed.
func decodeVertex(w uint32) (int, int) {
	return signExtend(int(w&0x7FF), 11), signExtend(int((w>>16)&0x7FF), 11)
}

// decodeColor unpacks a 24-bit color word to 8-bit
// channels.
func decodeColor(w uint32) (int, int, int) {
	return int(w & 0xFF), int((w >> 8) & 0xFF), int((w >> 16) & 0xFF)
}

// execBuffered runs a fully assembled command.
func (g *GPU) execBuffered() {
	b := g.buf
	cmd := g.cmd
	switch {
	case cmd == 0x02:
		g.cmdFill(b)
	case cmd < 0x40:
		g.cmdPolygon(b)
	case cmd < 0x60:
		g.cmdLine(b)
	case cmd < 0x80:
		g.cmdRect(b)
	case cmd < 0xA0:
		g.cmdBlitVRAM(b)
	case cmd < 0xC0:
		g.startTransfer(b, false)
	default:
		g.startTransfer(b, true)
	}
}

// ----------------------------------------------------------------
// Primitive parameter decoding
// ----------------------------------------------------------------

type vertex struct {
	x, y    int
	r, g, b int
	u, v    int
}

// cmdPolygon decodes and draws a triangle or quad.
func (g *GPU) cmdPolygon(b [20]uint32) {
	cmd := g.cmd
	textured := cmd&0x04 != 0
	gouraud := cmd&0x10 != 0
	semi := cmd&0x02 != 0
	raw := cmd&0x01 != 0
	verts := 3
	if cmd&0x08 != 0 {
		verts = 4
	}
	vs := make([]vertex, verts)
	r, gg, bl := decodeColor(b[0])
	idx := 1
	var clutX, clutY int
	page := g.page
	for i := 0; i < verts; i++ {
		x, y := decodeVertex(b[idx])
		idx++
		vs[i].x, vs[i].y = x, y
		vs[i].r, vs[i].g, vs[i].b = r, gg, bl
		if textured {
			uvw := b[idx]
			idx++
			vs[i].u = int(uvw & 0xFF)
			vs[i].v = int((uvw >> 8) & 0xFF)
			if i == 0 {
				// The CLUT coordinate rides in the high
				// halfword of the first UV word.
				clutX = int((uvw>>16)&0x3F) * 16
				clutY = int((uvw >> 22) & 0x1FF)
			}
			if i == 1 {
				page = texPageFromWord(uvw, page)
			}
		}
		if gouraud && i < verts-1 {
			r, gg, bl = decodeColor(b[idx])
			idx++
		}
	}
	// Hardware span limits: primitives spanning more
	// than 1023 horizontally or 511 vertically are
	// not drawn.
	minX, maxX, minY, maxY := vs[0].x, vs[0].x, vs[0].y, vs[0].y
	for _, v := range vs[1:] {
		minX, maxX = minI(minX, v.x), maxI(maxX, v.x)
		minY, maxY = minI(minY, v.y), maxI(maxY, v.y)
	}
	if maxX-minX > 1023 || maxY-minY > 511 {
		return
	}
	p := prim{
		textured: textured && !g.texDisable,
		semi:     semi,
		raw:      raw,
		page:     page,
		clutX:    clutX,
		clutY:    clutY,
		// Dithering applies to gouraud-shaded and
		// texture-modulated polygons, never to
		// monochrome ones.
		ditherOK: textured || gouraud,
	}
	g.drawTri(vs[0], vs[1], vs[2], p)
	if verts == 4 {
		// Quads split as (1,2,3) and (2,3,4).
		g.drawTri(vs[1], vs[2], vs[3], p)
	}
}

// texPageFromWord extracts the texpage attribute
// carried in a textured primitive's second UV word.
func texPageFromWord(uvw uint32, cur texPage) texPage {
	tp := int((uvw >> 16) & 0xFFFF)
	out := cur
	out.xBase = int(tp&0xF) * 64
	out.yBase = int((tp>>4)&1) * 256
	out.semiMode = int((tp >> 5) & 3)
	out.colorFmt = int((tp >> 7) & 3)
	if tp&(1<<11) != 0 {
		out.yBase |= 512
	}
	return out
}

func (g *GPU) cmdLine(b [20]uint32) {
	gouraud := g.cmd&0x10 != 0
	semi := g.cmd&0x02 != 0
	r, gg, bl := decodeColor(b[0])
	x0, y0 := decodeVertex(b[1])
	x1, y1 := decodeVertex(b[2])
	r1, g1, b1 := r, gg, bl
	if gouraud {
		r1, g1, b1 = decodeColor(b[2])
		x1, y1 = decodeVertex(b[3])
	}
	p := prim{semi: semi, ditherOK: true}
	g.drawLine(
		vertex{x: x0, y: y0, r: r, g: gg, b: bl},
		vertex{x: x1, y: y1, r: r1, g: g1, b: b1}, p)
}

func (g *GPU) startPolyline() {
	g.polyline = true
	g.polyGouraud = g.cmd&0x10 != 0
	g.polyStage = 0
	r, gg, bl := decodeColor(g.buf[0])
	x, y := decodeVertex(g.buf[1])
	g.polyPrevX, g.polyPrevY = x, y
	g.polyPrevC = [3]int{r, gg, bl}
	g.bufLen = 0
}

// feedPolyline consumes one streaming polyline word.
func (g *GPU) feedPolyline(w uint32) {
	terminator := w&0xF000F000 == 0x50005000
	switch g.polyStage {
	case 0: // a group-start word: color (gouraud) or vertex
		if terminator {
			g.polyline = false
			return
		}
		if g.polyGouraud {
			r, gg, bl := decodeColor(w)
			g.polyColor = [3]int{r, gg, bl}
			g.polyStage = 1
			return
		}
		x, y := decodeVertex(w)
		g.lineTo(x, y, g.polyPrevC)
	case 1: // vertex word after a gouraud color
		x, y := decodeVertex(w)
		g.lineTo(x, y, g.polyColor)
		g.polyStage = 0
	}
}

// lineTo draws one polyline segment from the
// previous vertex to (x,y) with end color c.
func (g *GPU) lineTo(x, y int, c [3]int) {
	semi := g.cmd&0x02 != 0
	p := prim{semi: semi, ditherOK: true}
	g.drawLine(
		vertex{x: g.polyPrevX, y: g.polyPrevY, r: g.polyPrevC[0], g: g.polyPrevC[1], b: g.polyPrevC[2]},
		vertex{x: x, y: y, r: c[0], g: c[1], b: c[2]}, p)
	g.polyPrevX, g.polyPrevY = x, y
	g.polyPrevC = c
}

func (g *GPU) cmdRect(b [20]uint32) {
	cmd := g.cmd
	textured := cmd&0x04 != 0
	semi := cmd&0x02 != 0
	raw := cmd&0x01 != 0
	sizeCode := (cmd >> 3) & 3
	r, gg, bl := decodeColor(b[0])
	vx, vy := decodeVertex(b[1])
	idx := 2
	u0, v0, clutX, clutY := 0, 0, 0, 0
	if textured {
		uvw := b[idx]
		idx++
		u0 = int(uvw & 0xFF)
		v0 = int((uvw >> 8) & 0xFF)
		// The CLUT coordinate rides in the high
		// halfword of the UV word.
		clutX = int((uvw>>16)&0x3F) * 16
		clutY = int((uvw >> 22) & 0x1FF)
	}
	w, h := 1, 1
	switch sizeCode {
	case 0:
		w = int(b[idx] & 0x3FF)
		if w == 0 {
			w = 1024
		}
		h = int((b[idx] >> 16) & 0x1FF)
		if h == 0 {
			h = 512
		}
	case 2:
		w, h = 8, 8
	case 3:
		w, h = 16, 16
	}
	if w > 1024 || h > 512 {
		return
	}
	p := prim{
		textured: textured && !g.texDisable,
		semi:     semi,
		raw:      raw,
		page:     g.page,
		clutX:    clutX,
		clutY:    clutY,
		ditherOK: false, // rectangles are never dithered
	}
	sx := vx + g.offX
	sy := vy + g.offY
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			x, y := sx+dx, sy+dy
			if !p.textured {
				g.plot(x, y, g.quantize(vertex{r: r, g: gg, b: bl}, x, y, false), semi, g.page.semiMode)
				continue
			}
			tu, tv := dx, dy
			if g.rectFlipX {
				tu = w - 1 - dx
			}
			if g.rectFlipY {
				tv = h - 1 - dy
			}
			col, ok, stp := g.sample(p, (u0+tu)&0xFF, (v0+tv)&0xFF,
				vertex{r: r, g: gg, b: bl}, x, y)
			if !ok {
				continue
			}
			g.plot(x, y, col, semi && stp, p.page.semiMode)
		}
	}
}

// cmdFill draws the fill rectangle (GP0 0x02): the
// documented parameter masking applies and the mask
// settings and drawing area do not.
func (g *GPU) cmdFill(b [20]uint32) {
	c := b[0] & 0xFFFFFF
	col := uint16((c>>19)&0x1F)<<10 | uint16((c>>11)&0x1F)<<5 | uint16((c>>3)&0x1F)
	x := int(b[1]&0xFFFF) & 0x3F0
	y := int(b[1]>>16) & 0x1FF
	w := (int(b[2]&0xFFFF) & 0x3FF)
	w = (w + 0xF) &^ 0xF
	h := int(b[2]>>16) & 0x1FF
	if w == 0 || h == 0 {
		return
	}
	for yy := 0; yy < h; yy++ {
		row := ((y + yy) & (VRAMHeight - 1)) * VRAMWidth
		for xx := 0; xx < w; xx++ {
			g.vram[row+((x+xx)&(VRAMWidth-1))] = col
		}
	}
}

// cmdBlitVRAM performs a VRAM to VRAM copy.
func (g *GPU) cmdBlitVRAM(b [20]uint32) {
	sx, sy := blitCoord(b[1])
	dx, dy := blitCoord(b[2])
	w, h := blitSize(b[3])
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			src := g.VRAMAt(sx+xx, sy+yy)
			dst := g.VRAMAt(dx+xx, dy+yy)
			if g.maskCheck && dst&0x8000 != 0 {
				continue
			}
			v := src & 0x7FFF
			if g.maskSet || src&0x8000 != 0 {
				v |= 0x8000
			}
			g.vram[((dy+yy)&(VRAMHeight-1))*VRAMWidth+((dx+xx)&(VRAMWidth-1))] = v
		}
	}
}

func blitCoord(w uint32) (int, int) {
	return int(w & 0x3FF), int((w >> 16) & 0x1FF)
}

// blitSize applies the documented size rule: a
// stored size of 0 means the full width/height.
func blitSize(w uint32) (int, int) {
	sx := int(w & 0xFFFF)
	sy := int(w >> 16)
	return ((sx - 1) & 0x3FF) + 1, ((sy - 1) & 0x1FF) + 1
}

// startTransfer arms a CPU to VRAM or VRAM to CPU
// transfer from its parameter words.
func (g *GPU) startTransfer(b [20]uint32, read bool) {
	x, y := blitCoord(b[1])
	w, h := blitSize(b[2])
	g.xferActive = true
	g.xferRead = read
	g.xferX, g.xferY = x, y
	g.xferW, g.xferH = w, h
	g.xferCol, g.xferRow = 0, 0
}

// feedTransfer consumes one word of CPU to VRAM
// data: two halfwords, low first, honoring the mask
// rules; the transfer ends after W*H halfwords.
func (g *GPU) feedTransfer(w uint32) {
	for _, half := range [2]uint16{uint16(w), uint16(w >> 16)} {
		if !g.xferActive {
			return
		}
		x := g.xferX + g.xferCol
		y := g.xferY + g.xferRow
		dst := g.VRAMAt(x, y)
		if !(g.maskCheck && dst&0x8000 != 0) {
			v := half & 0x7FFF
			if g.maskSet || half&0x8000 != 0 {
				v |= 0x8000
			}
			g.vram[(y&(VRAMHeight-1))*VRAMWidth+(x&(VRAMWidth-1))] = v
		}
		g.xferCol++
		if g.xferCol >= g.xferW {
			g.xferCol = 0
			g.xferRow++
			if g.xferRow >= g.xferH {
				g.xferActive = false
			}
		}
	}
}
