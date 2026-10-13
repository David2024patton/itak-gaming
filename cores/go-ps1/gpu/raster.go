package gpu

// Rasterization: pixel plotting with the drawing
// area clip, display-area rule, mask rules and
// semi-transparency; texture sampling; scan
// conversion for triangles and lines.

// prim carries the per-primitive rendering
// attributes shared by the polygon, line and
// rectangle paths.
type prim struct {
	textured bool
	semi     bool
	raw      bool
	page     texPage
	clutX    int
	clutY    int
	ditherOK bool // this primitive class dithers when enabled
}

// inDisplayArea reports whether a VRAM pixel falls
// inside the currently displayed rectangle (used by
// the GP0(E1) draw-to-display rule).
func (g *GPU) inDisplayArea(x, y int) bool {
	dx := x - g.dispX
	dy := y - g.dispY
	return dx >= 0 && dx < g.DisplayWidth() && dy >= 0 && dy < g.DisplayHeight()
}

// plot writes one shaded pixel (15-bit color in col,
// whose bit 15 is the source STP bit for textured
// primitives and 0 otherwise) at VRAM coordinates,
// applying clipping, the draw-to-display rule, the
// mask rules and, when blend is set, semi-transparent
// blending with the existing pixel per mode.
func (g *GPU) plot(x, y int, col uint16, blend bool, mode int) {
	if x < g.drawX1 || x > g.drawX2 || y < g.drawY1 || y > g.drawY2 {
		return
	}
	if x < 0 || x >= VRAMWidth || y < 0 || y >= VRAMHeight {
		return
	}
	// The draw-to-display prohibition protects the
	// area being scanned out; with the display
	// disabled there is no scanout to protect, so the
	// rule applies only while the display is on (see
	// the README model notes).
	if g.dispEnable && !g.drawToDisp && g.inDisplayArea(x, y) {
		return
	}
	idx := y*VRAMWidth + x
	dst := g.vram[idx]
	if g.maskCheck && dst&0x8000 != 0 {
		return
	}
	out := col & 0x7FFF
	if blend {
		out = blend15(dst&0x7FFF, out, mode)
	}
	if g.maskSet || col&0x8000 != 0 {
		out |= 0x8000
	}
	g.vram[idx] = out
}

// blend15 applies one semi-transparency mode to
// 5-bit channels: mode 0 = B/2 + F/2, mode 1 = B + F
// (clamped), mode 2 = B - F (clamped at 0),
// mode 3 = B + F/4 (clamped).
func blend15(backdrop, front uint16, mode int) uint16 {
	var out uint16
	for shift := 0; shift < 15; shift += 5 {
		b := int((backdrop >> shift) & 0x1F)
		f := int((front >> shift) & 0x1F)
		var v int
		switch mode {
		case 0:
			v = (b >> 1) + (f >> 1)
		case 1:
			v = b + f
			if v > 31 {
				v = 31
			}
		case 2:
			v = b - f
			if v < 0 {
				v = 0
			}
		default:
			v = b + (f >> 2)
			if v > 31 {
				v = 31
			}
		}
		out |= uint16(v) << shift
	}
	return out
}

// quantize converts an 8-bit-space shaded color to a
// 15-bit VRAM color, adding the dither matrix value
// first when dither applies.
func (g *GPU) quantize(c vertex, x, y int, dither bool) uint16 {
	r, gg, b := c.r, c.g, c.b
	if dither {
		d := ditherTable[y&3][x&3]
		r += d
		gg += d
		b += d
	}
	return uint16(clamp5(r)) | uint16(clamp5(gg))<<5 | uint16(clamp5(b))<<10
}

func clamp5(v int) int {
	v >>= 3
	if v < 0 {
		return 0
	}
	if v > 31 {
		return 31
	}
	return v
}

// applyWindow maps a texture coordinate through the
// GP0(E2) texture window in one axis.
func (g *GPU) applyWindow(t, mask, off int) int {
	if mask == 0 {
		return t
	}
	return (t & ((mask * 8) - 1)) | ((off & mask) * 8)
}

// sample fetches and shades one texel. It returns
// the 15-bit color (bit 15 = texel STP), whether the
// texel is drawn at all (texel 0x0000 is fully
// transparent), and the STP bit. Raw mode returns
// the texel unmodified. Blended mode modulates the
// texel by the vertex color in 8-bit space,
// saturating like the v2 GPU, and dithers when the
// draw-mode dither flag is on and the primitive
// class allows it.
func (g *GPU) sample(p prim, u, v int, shade vertex, x, y int) (uint16, bool, bool) {
	u = g.applyWindow(u, g.winMaskX, g.winOffX)
	v = g.applyWindow(v, g.winMaskY, g.winOffY)
	page := p.page
	var tex uint16
	switch page.colorFmt {
	case 0: // 4-bit CLUT
		w := g.VRAMAt(page.xBase+(u>>2), page.yBase+v)
		idx := (w >> (uint(u&3) * 4)) & 0xF
		tex = g.VRAMAt(p.clutX+int(idx), p.clutY)
	case 1: // 8-bit CLUT
		w := g.VRAMAt(page.xBase+(u>>1), page.yBase+v)
		idx := (w >> (uint(u&1) * 8)) & 0xFF
		tex = g.VRAMAt(p.clutX+int(idx), p.clutY)
	default: // 15-bit direct
		tex = g.VRAMAt(page.xBase+u, page.yBase+v)
	}
	if tex == 0 {
		return 0, false, false
	}
	stp := tex&0x8000 != 0
	if p.raw {
		return tex, true, stp
	}
	vr := modulate(int(tex&0x1F), shade.r)
	vg := modulate(int((tex>>5)&0x1F), shade.g)
	vb := modulate(int((tex>>10)&0x1F), shade.b)
	col := g.quantize(vertex{r: vr, g: vg, b: vb}, x, y, g.dither && p.ditherOK)
	if stp {
		col |= 0x8000
	}
	return col, true, stp
}

// modulate applies texture modulation: the 5-bit
// texel channel is expanded to 8 bits and multiplied
// by the 8-bit vertex color with 0x80 as neutral,
// saturating at 255.
func modulate(tex5, color8 int) int {
	t8 := (tex5 << 3) | (tex5 >> 2)
	out := (t8 * color8) >> 7
	if out > 255 {
		out = 255
	}
	return out
}

// ----------------------------------------------------------------
// Triangles
// ----------------------------------------------------------------

// edgeFn is the edge function for the directed edge
// a to b evaluated at (x,y).
func edgeFn(ax, ay, bx, by, x, y int64) int64 {
	return (bx-ax)*(y-ay) - (by-ay)*(x-ax)
}

// topLeft reports whether the directed edge a to b
// is a top or left edge under the hardware rule that
// lower-right polygon edges are not drawn: boundary
// pixels on top and left edges belong to the
// polygon, boundary pixels on bottom and right edges
// do not.
func topLeft(ax, ay, bx, by int64) bool {
	if by == ay {
		return bx > ax // top edge runs left to right
	}
	return by < ay // left edge runs bottom to top
}

// drawTri scan-converts one triangle with barycentric
// interpolation of color and texture coordinates.
// Screen coordinates are the vertex coordinates plus
// the drawing offset.
func (g *GPU) drawTri(v0, v1, v2 vertex, p prim) {
	// Work in screen space.
	v0.x += g.offX
	v0.y += g.offY
	v1.x += g.offX
	v1.y += g.offY
	v2.x += g.offX
	v2.y += g.offY
	area := edgeFn(int64(v0.x), int64(v0.y), int64(v1.x), int64(v1.y), int64(v2.x), int64(v2.y))
	if area == 0 {
		return
	}
	if area < 0 {
		v1, v2 = v2, v1
		area = -area
	}
	// After the swap the winding gives positive edge
	// functions inside.
	x0 := minI(minI(v0.x, v1.x), v2.x)
	x1 := maxI(maxI(v0.x, v1.x), v2.x)
	y0 := minI(minI(v0.y, v1.y), v2.y)
	y1 := maxI(maxI(v0.y, v1.y), v2.y)
	// Clip the bounding box to the drawing area and
	// VRAM.
	x0 = maxI(x0, maxI(g.drawX1, 0))
	y0 = maxI(y0, maxI(g.drawY1, 0))
	x1 = minI(x1, minI(g.drawX2, VRAMWidth-1))
	y1 = minI(y1, minI(g.drawY2, VRAMHeight-1))
	if x0 > x1 || y0 > y1 {
		return
	}
	tl0 := topLeft(int64(v1.x), int64(v1.y), int64(v2.x), int64(v2.y))
	tl1 := topLeft(int64(v2.x), int64(v2.y), int64(v0.x), int64(v0.y))
	tl2 := topLeft(int64(v0.x), int64(v0.y), int64(v1.x), int64(v1.y))
	half := area / 2
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			w0 := edgeFn(int64(v1.x), int64(v1.y), int64(v2.x), int64(v2.y), int64(x), int64(y))
			w1 := edgeFn(int64(v2.x), int64(v2.y), int64(v0.x), int64(v0.y), int64(x), int64(y))
			w2 := edgeFn(int64(v0.x), int64(v0.y), int64(v1.x), int64(v1.y), int64(x), int64(y))
			if !inside(w0, tl0) || !inside(w1, tl1) || !inside(w2, tl2) {
				continue
			}
			c := vertex{
				r: int((w0*int64(v0.r) + w1*int64(v1.r) + w2*int64(v2.r) + half) / area),
				g: int((w0*int64(v0.g) + w1*int64(v1.g) + w2*int64(v2.g) + half) / area),
				b: int((w0*int64(v0.b) + w1*int64(v1.b) + w2*int64(v2.b) + half) / area),
			}
			if p.textured {
				u := int((w0*int64(v0.u) + w1*int64(v1.u) + w2*int64(v2.u) + half) / area)
				v := int((w0*int64(v0.v) + w1*int64(v1.v) + w2*int64(v2.v) + half) / area)
				col, ok, stp := g.sample(p, u, v, c, x, y)
				if !ok {
					continue
				}
				g.plot(x, y, col, p.semi && stp, p.page.semiMode)
			} else {
				col := g.quantize(c, x, y, g.dither && p.ditherOK)
				g.plot(x, y, col, p.semi, p.page.semiMode)
			}
		}
	}
}

func inside(w int64, topLeftEdge bool) bool {
	return w > 0 || (w == 0 && topLeftEdge)
}

// ----------------------------------------------------------------
// Lines
// ----------------------------------------------------------------

// drawLine rasterizes one line segment, both
// endpoints inclusive, interpolating color along the
// major axis. The walker is a 16.16 fixed point DDA
// on the minor axis; psx-spx documents the endpoint
// behavior and shading but not the walker's exact
// subpixel phase (see the package comment).
func (g *GPU) drawLine(a, b vertex, p prim) {
	ax, ay := a.x+g.offX, a.y+g.offY
	bx, by := b.x+g.offX, b.y+g.offY
	dx := bx - ax
	dy := by - ay
	steps := maxI(absI(dx), absI(dy))
	if steps == 0 {
		col := g.quantize(a, ax, ay, g.dither && p.ditherOK)
		g.plot(ax, ay, col, p.semi, p.page.semiMode)
		return
	}
	if steps > 1023 {
		return // documented span limit behavior for lines as for polygons
	}
	xAcc := int64(ax) << 16
	yAcc := int64(ay) << 16
	xStep := (int64(dx) << 16) / int64(steps)
	yStep := (int64(dy) << 16) / int64(steps)
	rStep := (int64(b.r-a.r) << 16) / int64(steps)
	ggStep := (int64(b.g-a.g) << 16) / int64(steps)
	bStep := (int64(b.b-a.b) << 16) / int64(steps)
	rAcc := int64(a.r) << 16
	gAcc := int64(a.g) << 16
	bAcc := int64(a.b) << 16
	for i := 0; i <= steps; i++ {
		x := int(xAcc >> 16)
		y := int(yAcc >> 16)
		c := vertex{r: int(rAcc >> 16), g: int(gAcc >> 16), b: int(bAcc >> 16)}
		col := g.quantize(c, x, y, g.dither && p.ditherOK)
		g.plot(x, y, col, p.semi, p.page.semiMode)
		xAcc += xStep
		yAcc += yStep
		rAcc += rStep
		gAcc += ggStep
		bAcc += bStep
	}
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func absI(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
