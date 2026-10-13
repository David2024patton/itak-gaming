package vdp1

// Command list processing and rasterization.
//
// Commands are 32 byte tables in VRAM. The walker starts at byte
// address 0 and follows the jump mode of each command. All texture
// and table byte addresses wrap at the VRAM end with the 0x7FFFF
// mask, byte by byte.

type pt struct {
	x, y int
}

// cmd holds one decoded command table.
type cmd struct {
	ctrl uint16
	link uint16
	pmod uint16
	colr uint16
	srca uint16
	size uint16
	raw  [4]pt // vertex words decoded as signed coordinates
	rawW int   // raw unsigned XB word, scaled sprite width
	rawH int   // raw unsigned YB word, scaled sprite height
	grda uint16
}

// decodeCoord decodes an 11 bit two complement vertex coordinate.
func decodeCoord(word uint16) int {
	val := int(word & 0x7FF)
	if val >= 0x400 {
		val -= 0x800
	}
	return val
}

func (v *VDP1) vramByte(addr int) byte {
	if len(v.vram) == 0 {
		return 0
	}
	a := addr & vramMask
	if a >= len(v.vram) {
		a %= len(v.vram)
	}
	return v.vram[a]
}

func (v *VDP1) vramWord(addr int) uint16 {
	hi := v.vramByte(addr)
	lo := v.vramByte(addr + 1)
	return uint16(hi)<<8 | uint16(lo)
}

func (v *VDP1) loadCmd(addr int) cmd {
	// The vertex slots hold raw words; XA and friends live at
	// consecutive word offsets from 0x0C to 0x1A.
	words := func(off int) uint16 { return v.vramWord(addr + off) }
	c := cmd{
		ctrl: words(0x00),
		link: words(0x02),
		pmod: words(0x04),
		colr: words(0x06),
		srca: words(0x08),
		size: words(0x0A),
		grda: words(0x1C),
	}
	for i := 0; i < 4; i += 1 {
		c.raw[i] = pt{
			x: decodeCoord(words(0x0C + i*4)),
			y: decodeCoord(words(0x0E + i*4)),
		}
	}
	c.rawW = int(words(0x10))
	c.rawH = int(words(0x12))
	return c
}

// Draw runs the command list now, starting at VRAM byte address 0.
// Draw state resets first: system clip is the full display, the user
// clip is invalid and the local coordinate is (0, 0).
//
// At completion CEF is set, BEF takes the previous CEF value, the
// draw end flag is raised, and LOPR and COPR hold the address of
// the last command processed.
func (v *VDP1) Draw() {
	prevCEF := v.reg(regEDSR) & 1
	// Clear CEF while drawing; BEF is left as it was until the
	// completion write below.
	v.setReg(regEDSR, v.reg(regEDSR)&0x0002)

	v.sysClip = rect{x1: 0, y1: 0, x2: v.width - 1, y2: v.height - 1}
	v.userClip = rect{}
	v.userValid = false
	v.localX = 0
	v.localY = 0

	addr := 0
	last := 0
	var stack []int
	stop := false
	for count := 0; count < maxCommands && !stop; count += 1 {
		last = addr
		v.setReg(regCOPR, uint16(addr))
		c := v.loadCmd(addr)
		if c.ctrl&0x8000 != 0 {
			// End command: process nothing, stop here. LOPR
			// was updated to this address above via last.
			break
		}
		jp := (c.ctrl >> 12) & 0x7
		skip := jp >= 4
		if !skip {
			v.execCmd(c)
		}
		linkAddr := int(c.link) * 8 & vramMask
		switch jp {
		case 0, 4: // next, skip next
			addr = (addr + 0x20) & vramMask
		case 1, 5: // assign, skip assign
			addr = linkAddr
		case 2, 6: // call, skip call
			stack = append(stack, (addr+0x20)&vramMask)
			addr = linkAddr
		case 3, 7: // return, skip return
			if len(stack) == 0 {
				stop = true
			} else {
				addr = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
		}
	}

	v.setReg(regLOPR, uint16(last))
	v.setReg(regCOPR, uint16(last))
	v.setReg(regEDSR, uint16(1|(prevCEF<<1)))
	v.setReg(regMODR, v.composeMODR())
	v.drawEnd = true
}

// execCmd executes one command. Jump handling lives in Draw.
func (v *VDP1) execCmd(c cmd) {
	sel := c.ctrl & 0xF
	switch sel {
	case 8: // user clipping set, absolute screen coordinates
		v.userClip = normRect(c.raw[0].x, c.raw[0].y, c.raw[2].x, c.raw[2].y)
		v.userValid = true
		return
	case 9: // system clipping set, absolute screen coordinates
		v.sysClip = normRect(c.raw[0].x, c.raw[0].y, c.raw[2].x, c.raw[2].y)
		return
	case 10: // local coordinate set
		v.localX = c.raw[0].x
		v.localY = c.raw[0].y
		return
	}

	// Drawing commands: vertices are relative to the local
	// coordinate.
	var pts [4]pt
	for i := 0; i < 4; i += 1 {
		pts[i] = pt{x: c.raw[i].x + v.localX, y: c.raw[i].y + v.localY}
	}
	// CMDSIZE width field holds (dots/8 minus 1) in bits 13-8.
	srcW := ((int(c.size>>8) & 0x3F) + 1) * 8
	srcH := int(c.size&0xFF) + 1

	switch sel {
	case 0: // normal sprite, texture sampled 1:1
		bbox := rect{x1: pts[0].x, y1: pts[0].y, x2: pts[0].x + srcW - 1, y2: pts[0].y + srcH - 1}
		ctx := v.makeCtx(c, bbox)
		v.drawSpriteRect(ctx, c, srcW, srcH, pts[0].x, pts[0].y, srcW, srcH)
	case 1: // scaled sprite
		v.execScaled(c, pts, srcW, srcH)
	case 2: // distorted sprite
		bbox := quadBBox(pts)
		ctx := v.makeCtx(c, bbox)
		v.rasterQuad(ctx, c, pts, true, srcW, srcH)
	case 4: // polygon
		bbox := quadBBox(pts)
		ctx := v.makeCtx(c, bbox)
		v.rasterQuad(ctx, c, pts, false, srcW, srcH)
	case 5: // polyline: edges A-B, B-C, C-D, D-A
		bbox := quadBBox(pts)
		ctx := v.makeCtx(c, bbox)
		v.drawLine(ctx, c, pts[0], pts[1], 0, 1)
		v.drawLine(ctx, c, pts[1], pts[2], 1, 2)
		v.drawLine(ctx, c, pts[2], pts[3], 2, 3)
		v.drawLine(ctx, c, pts[3], pts[0], 3, 0)
	case 6: // line: edge A-B only
		bbox := quadBBox([4]pt{pts[0], pts[1], pts[0], pts[1]})
		ctx := v.makeCtx(c, bbox)
		v.drawLine(ctx, c, pts[0], pts[1], 0, 1)
	default:
		// Other command values are ignored.
	}
}

// execScaled handles the scaled sprite command, both the zoom point
// equals 0 form (destination rectangle A to C) and the zoom point
// form (A is the zoom point, XB and YB are the size).
func (v *VDP1) execScaled(c cmd, pts [4]pt, srcW, srcH int) {
	zp := (c.ctrl >> 8) & 0xF
	if zp == 0 {
		destW := pts[2].x - pts[0].x + 1
		destH := pts[2].y - pts[0].y + 1
		if destW <= 0 || destH <= 0 {
			return
		}
		bbox := rect{x1: pts[0].x, y1: pts[0].y, x2: pts[2].x, y2: pts[2].y}
		ctx := v.makeCtx(c, bbox)
		v.drawSpriteRect(ctx, c, srcW, srcH, pts[0].x, pts[0].y, destW, destH)
		return
	}
	// XB and YB are unsigned word values here, not coordinates.
	// loadCmd kept them raw in rawW and rawH for exactly this.
	w, h := c.scaledSize()
	if w <= 0 || h <= 0 {
		return
	}
	anchorX := anchorOffset(int(zp&0x3), w)
	anchorY := anchorOffset(int((zp>>2)&0x3), h)
	ox := pts[0].x - anchorX
	oy := pts[0].y - anchorY
	bbox := rect{x1: ox, y1: oy, x2: ox + w - 1, y2: oy + h - 1}
	ctx := v.makeCtx(c, bbox)
	v.drawSpriteRect(ctx, c, srcW, srcH, ox, oy, w, h)
}

// anchorOffset maps an anchor selector to a pixel offset: 0 is the
// origin, 1 is half the size, 2 is the full size. Selector 3 is
// treated as the full size, a documented simplification.
func anchorOffset(sel, size int) int {
	switch sel {
	case 1:
		return size / 2
	case 2, 3:
		return size
	default:
		return 0
	}
}

func quadBBox(pts [4]pt) rect {
	r := rect{x1: pts[0].x, y1: pts[0].y, x2: pts[0].x, y2: pts[0].y}
	for i := 1; i < 4; i += 1 {
		if pts[i].x < r.x1 {
			r.x1 = pts[i].x
		}
		if pts[i].y < r.y1 {
			r.y1 = pts[i].y
		}
		if pts[i].x > r.x2 {
			r.x2 = pts[i].x
		}
		if pts[i].y > r.y2 {
			r.y2 = pts[i].y
		}
	}
	return r
}

// partCtx carries the per part drawing state derived from CMDPMOD
// and the current clip state.
type partCtx struct {
	calc      int
	colorMode int
	mesh      bool
	mon       bool
	ecd       bool
	spd       bool
	clip      bool
	cmod      int
	flipH     bool
	flipV     bool
	effSys    rect
	gour      [4][3]int
}

// makeCtx builds the part context. Pre clipping: when the Pclp bit
// is 0 (pre clipping enabled) and the part bounding box extends
// beyond the system clip rectangle, the effective system clip
// shrinks by 1 on the right and bottom for this part. That is a
// documented simplification of the hardware 1 pixel preclip rule.
func (v *VDP1) makeCtx(c cmd, bbox rect) partCtx {
	ctx := partCtx{
		calc:      int(c.pmod & 0x7),
		colorMode: int((c.pmod >> 3) & 0x7),
		mesh:      c.pmod&0x0100 != 0,
		mon:       c.pmod&0x8000 != 0,
		ecd:       c.pmod&0x0080 != 0,
		spd:       c.pmod&0x0040 != 0,
		clip:      c.pmod&0x0400 != 0,
		cmod:      int((c.pmod >> 9) & 1),
		flipH:     c.ctrl&0x0010 != 0,
		flipV:     c.ctrl&0x0020 != 0,
		effSys:    v.sysClip,
	}
	// CMDPMOD bit12 (HSS) is a documented no op here.
	if c.pmod&0x0800 == 0 {
		sc := v.sysClip
		if bbox.x1 < sc.x1 || bbox.y1 < sc.y1 || bbox.x2 > sc.x2 || bbox.y2 > sc.y2 {
			ctx.effSys.x2 -= 1
			ctx.effSys.y2 -= 1
		}
	}
	ctx.gour = v.gouraudTable(c.grda)
	return ctx
}

// clipPass applies the per dot clip tests in pipeline order:
// frame buffer bounds, system clip, then user clipping when the
// command enables it and a user rectangle is valid.
func (v *VDP1) clipPass(ctx partCtx, x, y int) bool {
	if x < 0 || x >= v.width || y < 0 || y >= v.height {
		return false
	}
	if !ctx.effSys.contains(x, y) {
		return false
	}
	if ctx.clip && v.userValid {
		inside := v.userClip.contains(x, y)
		if ctx.cmod == 0 && !inside {
			return false
		}
		if ctx.cmod == 1 && inside {
			return false
		}
	}
	return true
}

// gouraudTable reads the four RGB555 gouraud entries for corners
// A, B, C and D. The table byte address is (CMDGRDA and 0xFFF8)
// times 8.
func (v *VDP1) gouraudTable(grda uint16) [4][3]int {
	var out [4][3]int
	base := int(grda&0xFFF8) * 8
	for i := 0; i < 4; i += 1 {
		w := v.vramWord(base + i*2)
		out[i] = [3]int{
			int((w >> 10) & 0x1F),
			int((w >> 5) & 0x1F),
			int(w & 0x1F),
		}
	}
	return out
}

// scaledSize returns the raw XB and YB words of a scaled sprite
// command, which hold the destination size in the zoom point form.
// They are stored in the vertex slots as unsigned values alongside
// the decoded coordinates.
func (c cmd) scaledSize() (int, int) {
	return c.rawW, c.rawH
}

// fetch holds the result of one texture dot fetch.
type fetch struct {
	data        uint16
	end         bool
	transparent bool
	rgb         bool // gouraud applies to this data
}

// fetchTex samples one dot of the character pattern. u and vv are
// texture coordinates before flip handling; out of range values are
// clamped to the pattern edge, which matters for the distorted
// sprite right and bottom edges whose corner coordinate is srcW
// and srcH by definition.
func (v *VDP1) fetchTex(ctx partCtx, c cmd, srcW, srcH, uRaw, vvRaw int) fetch {
	if srcW <= 0 || srcH <= 0 {
		return fetch{transparent: true}
	}
	u := clampInt(uRaw, 0, srcW-1)
	vv := clampInt(vvRaw, 0, srcH-1)
	if ctx.flipH {
		u = srcW - 1 - u
	}
	if ctx.flipV {
		vv = srcH - 1 - vv
	}
	base := int(c.srca) * 8 & vramMask
	switch ctx.colorMode {
	case 0, 1:
		off := vv*(srcW/2) + u/2
		b := v.vramByte(base + off)
		var code uint16
		if u%2 == 0 {
			code = uint16(b >> 4)
		} else {
			code = uint16(b & 0xF)
		}
		if code == 0xF && !ctx.ecd {
			return fetch{end: true}
		}
		if code == 0 && !ctx.spd {
			return fetch{transparent: true}
		}
		if ctx.colorMode == 0 {
			return fetch{data: (c.colr & 0xFFF0) | code}
		}
		clutAddr := int(c.colr&0xFFF8) * 8
		data := v.vramWord(clutAddr + int(code)*2)
		// Gouraud applies to CLUT entries whose bit15 is 0 and
		// does not apply when bit15 is set; the entry is palette
		// bank data in that case. Documented rule from the spec.
		return fetch{data: data, rgb: data&0x8000 == 0}
	case 2, 3:
		bits := 6
		if ctx.colorMode == 3 {
			bits = 7
		}
		code := v.readBits(base, (vv*srcW+u)*bits, bits)
		allOnes := uint16((1 << bits) - 1)
		if code == allOnes && !ctx.ecd {
			return fetch{end: true}
		}
		if code == 0 && !ctx.spd {
			return fetch{transparent: true}
		}
		if ctx.colorMode == 2 {
			return fetch{data: (c.colr & 0xFFC0) | code}
		}
		return fetch{data: (c.colr & 0xFF80) | code}
	case 4:
		code := uint16(v.vramByte(base + vv*srcW + u))
		if code == 0xFF && !ctx.ecd {
			return fetch{end: true}
		}
		if code == 0 && !ctx.spd {
			return fetch{transparent: true}
		}
		return fetch{data: (c.colr & 0xFF00) | code}
	default: // color mode 5, RGB 32768 colors
		data := v.vramWord(base + (vv*srcW+u)*2)
		// Mode 5 has no end code; that is a documented rule.
		if data&0x7FFF == 0 && !ctx.spd {
			return fetch{transparent: true}
		}
		return fetch{data: data, rgb: true}
	}
}

// readBits reads n bits from a big endian bit stream starting at a
// bit offset from a pattern base byte address. Byte addresses wrap
// at the VRAM end.
func (v *VDP1) readBits(base, bitOff, n int) uint16 {
	var code uint16
	for i := 0; i < n; i += 1 {
		pos := bitOff + i
		b := v.vramByte(base + pos/8)
		bit := (b >> (7 - pos%8)) & 1
		code = code<<1 | uint16(bit)
	}
	return code
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func floorDiv(a, b int64) int64 {
	if b < 0 {
		a = -a
		b = -b
	}
	if a >= 0 {
		return a / b
	}
	return -((-a + b - 1) / b)
}

// finishDot runs the tail of the per dot pipeline: color
// calculation, then the mesh test, then the frame buffer write.
// The caller has already done clipping and texture fetch.
func (v *VDP1) finishDot(ctx partCtx, x, y int, data uint16, g [3]int, gouraudOK bool) {
	data = v.applyCalc(ctx, x, y, data, g, gouraudOK)
	if ctx.mesh && ((x^y)&1) == 0 {
		return
	}
	v.writeDrawPixel(x, y, data)
}

// applyCalc applies the CMDPMOD color calculation mode to one dot.
// Modes: 0 replace, 1 shadow, 2 half transparent, 3 replace
// (prohibited value, documented as replace), 4 gouraud only,
// 5 gouraud plus half transparent, 6 gouraud plus shadow,
// 7 replace.
func (v *VDP1) applyCalc(ctx partCtx, x, y int, data uint16, g [3]int, gouraudOK bool) uint16 {
	switch ctx.calc {
	case 1:
		return (data & 0x8000) | 0x7FFE
	case 2:
		return blendHalf(data, v.readDrawPixel(x, y))
	case 4:
		if gouraudOK {
			return addGouraud(data, g)
		}
		return data
	case 5:
		d := data
		if gouraudOK {
			d = addGouraud(d, g)
		}
		return blendHalf(d, v.readDrawPixel(x, y))
	case 6:
		d := data
		if gouraudOK {
			d = addGouraud(d, g)
		}
		return (d & 0x8000) | 0x7FFE
	default:
		return data
	}
}

// blendHalf averages the 5 bit channels of the new and old pixels.
// Bit15 comes from the new pixel.
func blendHalf(newData, oldData uint16) uint16 {
	r := ((newData >> 10) & 0x1F) + ((oldData >> 10) & 0x1F)
	g := ((newData >> 5) & 0x1F) + ((oldData >> 5) & 0x1F)
	b := (newData & 0x1F) + (oldData & 0x1F)
	return (newData & 0x8000) | uint16(r>>1)<<10 | uint16(g>>1)<<5 | uint16(b>>1)
}

// addGouraud adds a gouraud RGB555 value to the base data channels
// with clamping to 31. Bit15 of the data is preserved.
func addGouraud(data uint16, g [3]int) uint16 {
	r := clampInt(int((data>>10)&0x1F)+g[0], 0, 31)
	gg := clampInt(int((data>>5)&0x1F)+g[1], 0, 31)
	b := clampInt(int(data&0x1F)+g[2], 0, 31)
	return (data & 0x8000) | uint16(r)<<10 | uint16(gg)<<5 | uint16(b)
}

// bilinearGouraud interpolates the four corner gouraud values at
// destination fraction (dx, dy) inside a destW by destH rectangle.
// Fixed point with 16 bit fractions, floored at the end, so the
// center pixel of an odd sized rectangle gets the exact per
// channel average of the four corners when it divides evenly.
func bilinearGouraud(ctx partCtx, dx, dy, destW, destH int) [3]int {
	fxQ := 0
	if destW > 1 {
		fxQ = dx * 65536 / (destW - 1)
	}
	fyQ := 0
	if destH > 1 {
		fyQ = dy * 65536 / (destH - 1)
	}
	var out [3]int
	for ch := 0; ch < 3; ch += 1 {
		a := ctx.gour[0][ch]
		b := ctx.gour[1][ch]
		cc := ctx.gour[2][ch]
		d := ctx.gour[3][ch]
		topQ := a<<16 + fxQ*(b-a)
		botQ := d<<16 + fxQ*(cc-d)
		valQ := topQ + fyQ*(botQ-topQ)/65536
		out[ch] = int(floorDiv(int64(valQ), 65536))
	}
	return out
}

// drawSpriteRect rasterizes an axis aligned sprite (normal and
// scaled forms) into the destination rectangle.
func (v *VDP1) drawSpriteRect(ctx partCtx, c cmd, srcW, srcH, destX, destY, destW, destH int) {
	if srcW <= 0 || srcH <= 0 || destW <= 0 || destH <= 0 {
		return
	}
	for dy := 0; dy < destH; dy += 1 {
		y := destY + dy
		for dx := 0; dx < destW; dx += 1 {
			x := destX + dx
			if !v.clipPass(ctx, x, y) {
				continue
			}
			u := dx * srcW / destW
			vv := dy * srcH / destH
			f := v.fetchTex(ctx, c, srcW, srcH, u, vv)
			if f.end {
				// End code stops the rest of this row.
				break
			}
			if f.transparent {
				continue
			}
			data := f.data
			if ctx.mon {
				data |= 0x8000
			}
			g := bilinearGouraud(ctx, dx, dy, destW, destH)
			v.finishDot(ctx, x, y, data, g, f.rgb)
		}
	}
}

// spanHit is one scanline and edge intersection in fixed point.
// All values are scaled by 65536.
type spanHit struct {
	xF int64
	uF int64
	vF int64
	gF [3]int64
}

// rasterQuad draws a distorted sprite (textured) or a polygon
// (flat) using edge interpolation. For each scanline of the quad
// bounding box the intersections with the four edges are found;
// the leftmost and rightmost hits bound the span and texture
// coordinates and gouraud values interpolate across the span with
// fixed point math, floored at the end.
func (v *VDP1) rasterQuad(ctx partCtx, c cmd, pts [4]pt, textured bool, srcW, srcH int) {
	tex := [4]pt{{0, 0}, {srcW, 0}, {srcW, srcH}, {0, srcH}}
	bbox := quadBBox(pts)
	edges := [4][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 0}}
	for y := bbox.y1; y <= bbox.y2; y += 1 {
		var hits []spanHit
		for e := 0; e < 4; e += 1 {
			i := edges[e][0]
			j := edges[e][1]
			p := pts[i]
			q := pts[j]
			if p.y == q.y {
				if y == p.y {
					hits = append(hits, vertHit(p, tex[i], ctx.gour[i]))
					hits = append(hits, vertHit(q, tex[j], ctx.gour[j]))
				}
				continue
			}
			lo, hi := p.y, q.y
			if hi < lo {
				lo, hi = hi, lo
			}
			if y < lo || y > hi {
				continue
			}
			tQ := int64(y-p.y) * 65536 / int64(q.y-p.y)
			h := spanHit{
				xF: int64(p.x)<<16 + tQ*int64(q.x-p.x),
				uF: int64(tex[i].x)<<16 + tQ*int64(tex[j].x-tex[i].x),
				vF: int64(tex[i].y)<<16 + tQ*int64(tex[j].y-tex[i].y),
			}
			for ch := 0; ch < 3; ch += 1 {
				h.gF[ch] = int64(ctx.gour[i][ch])<<16 + tQ*int64(ctx.gour[j][ch]-ctx.gour[i][ch])
			}
			hits = append(hits, h)
		}
		if len(hits) == 0 {
			continue
		}
		left := hits[0]
		right := hits[0]
		for _, h := range hits {
			if h.xF < left.xF {
				left = h
			}
			if h.xF > right.xF {
				right = h
			}
		}
		xStart := int(floorDiv(left.xF, 65536))
		xEnd := int(floorDiv(right.xF, 65536))
		denom := right.xF - left.xF
		for x := xStart; x <= xEnd; x += 1 {
			if !v.clipPass(ctx, x, y) {
				continue
			}
			var u, vv int
			var g [3]int
			if denom == 0 {
				u = int(floorDiv(left.uF, 65536))
				vv = int(floorDiv(left.vF, 65536))
				for ch := 0; ch < 3; ch += 1 {
					g[ch] = int(floorDiv(left.gF[ch], 65536))
				}
			} else {
				num := (int64(x) << 16) - left.xF
				u = int(floorDiv(left.uF+num*(right.uF-left.uF)/denom, 65536))
				vv = int(floorDiv(left.vF+num*(right.vF-left.vF)/denom, 65536))
				for ch := 0; ch < 3; ch += 1 {
					g[ch] = int(floorDiv(left.gF[ch]+num*(right.gF[ch]-left.gF[ch])/denom, 65536))
				}
			}
			if textured {
				f := v.fetchTex(ctx, c, srcW, srcH, u, vv)
				if f.end {
					break
				}
				if f.transparent {
					continue
				}
				data := f.data
				if ctx.mon {
					data |= 0x8000
				}
				v.finishDot(ctx, x, y, data, g, f.rgb)
			} else {
				data := c.colr
				if ctx.mon {
					data |= 0x8000
				}
				// Non textured parts always take gouraud
				// as RGB data.
				v.finishDot(ctx, x, y, data, g, true)
			}
		}
	}
}

func vertHit(p pt, t pt, g [3]int) spanHit {
	h := spanHit{
		xF: int64(p.x) << 16,
		uF: int64(t.x) << 16,
		vF: int64(t.y) << 16,
	}
	for ch := 0; ch < 3; ch += 1 {
		h.gF[ch] = int64(g[ch]) << 16
	}
	return h
}

// drawLine draws one straight edge with the non textured color.
// Gouraud for a line is linear between the two endpoint corners
// given by gi and gj (A and B for the line command). Without
// gouraud color calculation the whole line uses CMDCOLR.
// Simplification: the line is stepped by max(abs(dx), abs(dy))
// with integer interpolation rather than hardware edge walking.
func (v *VDP1) drawLine(ctx partCtx, c cmd, p1, p2 pt, gi, gj int) {
	dx := p2.x - p1.x
	dy := p2.y - p1.y
	steps := absInt(dx)
	if absInt(dy) > steps {
		steps = absInt(dy)
	}
	for i := 0; i <= steps; i += 1 {
		x := p1.x
		y := p1.y
		if steps > 0 {
			x = p1.x + i*dx/steps
			y = p1.y + i*dy/steps
		}
		if !v.clipPass(ctx, x, y) {
			continue
		}
		data := c.colr
		if ctx.mon {
			data |= 0x8000
		}
		var g [3]int
		for ch := 0; ch < 3; ch += 1 {
			a := ctx.gour[gi][ch]
			b := ctx.gour[gj][ch]
			if steps == 0 {
				g[ch] = a
			} else {
				q := a<<16 + (i*65536/steps)*(b-a)
				g[ch] = int(floorDiv(int64(q), 65536))
			}
		}
		v.finishDot(ctx, x, y, data, g, true)
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
