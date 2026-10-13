package gpu

import (
	"math"
)

// Clipping and rasterization.
//
// Triangles and quads are clipped with Sutherland-Hodgman
// against the canonical GX clip volume: -w <= x <= w,
// -w <= y <= w, and -w <= z <= 0 (GX projects depth so the
// near plane lands at z = 0 and the far plane at z = -w).
// All varyings (color, texcoord, clip position itself) are
// interpolated linearly in clip space at clip plane
// crossings. Lines and points use the documented trivial
// rule for this slice: a line or point is drawn only when
// every one of its vertices lies inside the clip volume,
// otherwise it is dropped whole.
//
// Screen mapping follows the XF viewport registers:
//   sx = xo + xs * (clipx / clipw)
//   sy = yo + ys * (clipy / clipw)
//   depth = clamp(zo + zs * (clipz / clipw), 0, 0xFFFFFF)
// where xs and ys are the viewport scale registers (ys is
// normally negative, which is what turns the projected +y
// axis downward), and xo, yo, zo are the origin registers.
//
// Front faces are the triangles whose signed screen area
// (y-down coordinates) is positive, the GX clockwise
// convention. The BP general mode register holds the cull
// mode at bits 14-15: 0 none, 1 cull front, 2 cull back,
// 3 cull all.
//
// Rasterization uses edge functions evaluated at integer
// pixel centers (x+0.5, y+0.5). The exact top-left tie rule
// used here: with the triangle wound to positive screen
// area, a pixel center lying exactly on an edge is covered
// when that edge is a top or left edge, where an edge from
// a to b is a top or left edge when it points upward
// (b.y < a.y), or is horizontal and points in negative x
// (b.y == a.y and b.x < a.x). The two triangles of a quad
// share their diagonal with opposite directions, so exactly
// one of them claims the tie pixels and a solid quad covers
// its full rectangle with no double-drawn seam.
//
// Interpolation is perspective correct: each varying is
// stored multiplied by 1/w and interpolated with the
// screen-space barycentric weights, then divided by the
// interpolated 1/w per pixel. Depth goes through the same
// path (the interpolated z/w feeds the viewport depth
// formula above).

type screenVertex struct {
	x, y       float64 // screen coordinates
	zndc       float64 // clip z / clip w
	invW       float64
	r, g, b, a float64 // color, already multiplied by invW below in prep
	s, t       float64
	// overW copies of the varyings:
	rw, gw, bw, aw, sw, tw, zw float64
}

func (g *GPU) toScreen(v pipeVertex) screenVertex {
	var sv screenVertex
	w := v.clip[3]
	if w == 0 {
		w = 1
	}
	invW := 1 / w
	xs := g.xfRegF(xfViewport + 0)
	ys := g.xfRegF(xfViewport + 1)
	xo := g.xfRegF(xfViewport + 3)
	yo := g.xfRegF(xfViewport + 4)
	sv.x = xo + xs*(v.clip[0]*invW)
	sv.y = yo + ys*(v.clip[1]*invW)
	sv.zndc = v.clip[2] * invW
	sv.invW = invW
	sv.r, sv.g, sv.b, sv.a = v.r, v.gg, v.b, v.a
	sv.s, sv.t = v.s, v.t
	sv.rw, sv.gw, sv.bw, sv.aw = v.r*invW, v.gg*invW, v.b*invW, v.a*invW
	sv.sw, sv.tw, sv.zw = v.s*invW, v.t*invW, sv.zndc*invW
	return sv
}

func (g *GPU) depthFromZndc(zndc float64) uint32 {
	zs := g.xfRegF(xfViewport + 2)
	zo := g.xfRegF(xfViewport + 5)
	d := zo + zs*zndc
	if d < 0 {
		d = 0
	}
	if d > 0xFFFFFF {
		d = 0xFFFFFF
	}
	return uint32(d + 0.5)
}

func lerpVertex(a, b pipeVertex, t float64) pipeVertex {
	var o pipeVertex
	for i := 0; i < 4; i++ {
		o.clip[i] = a.clip[i] + (b.clip[i]-a.clip[i])*t
	}
	o.r = a.r + (b.r-a.r)*t
	o.gg = a.gg + (b.gg-a.gg)*t
	o.b = a.b + (b.b-a.b)*t
	o.a = a.a + (b.a-a.a)*t
	o.s = a.s + (b.s-a.s)*t
	o.t = a.t + (b.t-a.t)*t
	for i := 0; i < 3; i++ {
		o.normal[i] = a.normal[i] + (b.normal[i]-a.normal[i])*t
	}
	return o
}

// clipPlanes are (axis, sign, limit) triples encoding the
// six planes as sign*coord <= limit*w in homogeneous form:
// distance = limit*w - sign*coord must stay >= 0... we use
// the equivalent signed distance functions directly below.
func clipPolygon(in []pipeVertex) []pipeVertex {
	type plane struct {
		dist func(v pipeVertex) float64
	}
	planes := []plane{
		{func(v pipeVertex) float64 { return v.clip[3] + v.clip[0] }}, // x >= -w
		{func(v pipeVertex) float64 { return v.clip[3] - v.clip[0] }}, // x <= w
		{func(v pipeVertex) float64 { return v.clip[3] + v.clip[1] }}, // y >= -w
		{func(v pipeVertex) float64 { return v.clip[3] - v.clip[1] }}, // y <= w
		{func(v pipeVertex) float64 { return v.clip[3] + v.clip[2] }}, // z >= -w
		{func(v pipeVertex) float64 { return -v.clip[2] }},            // z <= 0
	}
	poly := in
	for _, p := range planes {
		if len(poly) == 0 {
			return poly
		}
		out := make([]pipeVertex, 0, len(poly)+1)
		for i := range poly {
			cur := poly[i]
			prev := poly[(i+len(poly)-1)%len(poly)]
			dc := p.dist(cur)
			dp := p.dist(prev)
			curIn := dc >= 0
			prevIn := dp >= 0
			if curIn != prevIn {
				t := dp / (dp - dc)
				out = append(out, lerpVertex(prev, cur, t))
			}
			if curIn {
				out = append(out, cur)
			}
		}
		poly = out
	}
	return poly
}

func insideClip(v pipeVertex) bool {
	w := v.clip[3]
	return w > 0 &&
		v.clip[0] >= -w && v.clip[0] <= w &&
		v.clip[1] >= -w && v.clip[1] <= w &&
		v.clip[2] >= -w && v.clip[2] <= 0
}

func signedArea(a, b, c screenVertex) float64 {
	return (b.x-a.x)*(c.y-a.y) - (c.x-a.x)*(b.y-a.y)
}

// shadeFragment applies the single-stage TEV combine for
// stage 0: when the stage 0 texture is enabled, the
// fragment color is the vertex color modulated by the texel
// component-wise (alpha likewise); otherwise it is the
// plain vertex color. Full multi-stage TEV is out of scope.
func (g *GPU) shadeFragment(sv screenVertex, invW float64) color8 {
	r := sv.rw / invW
	gg := sv.gw / invW
	b := sv.bw / invW
	a := sv.aw / invW
	if ti := g.stage0Texture(); ti.enabled {
		s := sv.sw / invW
		t := sv.tw / invW
		tex := g.sampleTexture(ti, s, t)
		r *= float64(tex.r) / 255
		gg *= float64(tex.g) / 255
		b *= float64(tex.b) / 255
		a *= float64(tex.a) / 255
	}
	return color8{
		clamp255(r * 255), clamp255(gg * 255),
		clamp255(b * 255), clamp255(a * 255),
	}
}

func (g *GPU) plotScreen(sv screenVertex) {
	x := int(math.Floor(sv.x + 0.5))
	y := int(math.Floor(sv.y + 0.5))
	invW := sv.invW
	if invW == 0 {
		invW = 1
	}
	src := g.shadeFragment(sv, invW)
	g.plotFragment(x, y, src, g.depthFromZndc(sv.zndc))
}

// tri clips and rasterizes one triangle.
func (g *GPU) tri(a, b, c pipeVertex) {
	sa, sb, sc := g.toScreen(a), g.toScreen(b), g.toScreen(c)
	area := signedArea(sa, sb, sc)
	cull := (g.bp[bpGenMode] >> 14) & 3
	switch cull {
	case 1: // cull front (positive area)
		if area > 0 {
			return
		}
	case 2: // cull back
		if area < 0 {
			return
		}
	case 3:
		return
	}
	if area == 0 {
		return
	}
	poly := clipPolygon([]pipeVertex{a, b, c})
	if len(poly) < 3 {
		return
	}
	sp := make([]screenVertex, len(poly))
	for i, v := range poly {
		sp[i] = g.toScreen(v)
	}
	for i := 1; i+1 < len(sp); i++ {
		g.rasterTri(sp[0], sp[i], sp[i+1])
	}
}

func isTopLeft(ax, ay, bx, by float64) bool {
	if by == ay {
		return bx < ax
	}
	return by < ay
}

// rasterTri fills one screen-space triangle whose vertices
// are already clipped.
func (g *GPU) rasterTri(a, b, c screenVertex) {
	area := signedArea(a, b, c)
	if area == 0 {
		return
	}
	if area < 0 {
		b, c = c, b
		area = -area
	}
	minX := int(math.Floor(math.Min(a.x, math.Min(b.x, c.x))))
	maxX := int(math.Floor(math.Max(a.x, math.Max(b.x, c.x))))
	minY := int(math.Floor(math.Min(a.y, math.Min(b.y, c.y))))
	maxY := int(math.Floor(math.Max(a.y, math.Max(b.y, c.y))))
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX >= EFBWidth {
		maxX = EFBWidth - 1
	}
	if maxY >= EFBHeight {
		maxY = EFBHeight - 1
	}
	edge := func(p0, p1 screenVertex, px, py float64) float64 {
		return (p1.x-p0.x)*(py-p0.y) - (p1.y-p0.y)*(px-p0.x)
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px := float64(x) + 0.5
			py := float64(y) + 0.5
			e0 := edge(b, c, px, py) // opposite a
			e1 := edge(c, a, px, py) // opposite b
			e2 := edge(a, b, px, py) // opposite c
			inside := func(e float64, p0, p1 screenVertex) bool {
				if e > 0 {
					return true
				}
				if e < 0 {
					return false
				}
				return isTopLeft(p0.x, p0.y, p1.x, p1.y)
			}
			if !inside(e0, b, c) || !inside(e1, c, a) || !inside(e2, a, b) {
				continue
			}
			l0 := e0 / area
			l1 := e1 / area
			l2 := e2 / area
			var sv screenVertex
			sv.x, sv.y = px, py
			sv.invW = l0*a.invW + l1*b.invW + l2*c.invW
			if sv.invW == 0 {
				continue
			}
			sv.rw = l0*a.rw + l1*b.rw + l2*c.rw
			sv.gw = l0*a.gw + l1*b.gw + l2*c.gw
			sv.bw = l0*a.bw + l1*b.bw + l2*c.bw
			sv.aw = l0*a.aw + l1*b.aw + l2*c.aw
			sv.sw = l0*a.sw + l1*b.sw + l2*c.sw
			sv.tw = l0*a.tw + l1*b.tw + l2*c.tw
			zndc := (l0*a.zw + l1*b.zw + l2*c.zw) / sv.invW
			sv.zndc = zndc
			src := g.shadeFragment(sv, sv.invW)
			g.plotFragment(x, y, src, g.depthFromZndc(zndc))
		}
	}
}

// line draws a 1-pixel Bresenham segment with interpolated
// color, depth tested per pixel. Both endpoints must lie
// inside the clip volume (the trivial line rule documented
// at the top of this file).
func (g *GPU) line(a, b pipeVertex) {
	if !insideClip(a) || !insideClip(b) {
		return
	}
	sa, sb := g.toScreen(a), g.toScreen(b)
	x0 := int(math.Floor(sa.x + 0.5))
	y0 := int(math.Floor(sa.y + 0.5))
	x1 := int(math.Floor(sb.x + 0.5))
	y1 := int(math.Floor(sb.y + 0.5))
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	steps := dx
	if -dy > steps {
		steps = -dy
	}
	if steps == 0 {
		steps = 1
	}
	for i := 0; ; i++ {
		t := float64(i) / float64(steps)
		var sv screenVertex
		sv.x, sv.y = float64(x0), float64(y0)
		sv.invW = sa.invW + (sb.invW-sa.invW)*t
		sv.rw = sa.rw + (sb.rw-sa.rw)*t
		sv.gw = sa.gw + (sb.gw-sa.gw)*t
		sv.bw = sa.bw + (sb.bw-sa.bw)*t
		sv.aw = sa.aw + (sb.aw-sa.aw)*t
		sv.sw = sa.sw + (sb.sw-sa.sw)*t
		sv.tw = sa.tw + (sb.tw-sa.tw)*t
		sv.zndc = sa.zndc + (sb.zndc-sa.zndc)*t
		src := g.shadeFragment(sv, sv.invW)
		g.plotFragment(x0, y0, src, g.depthFromZndc(sv.zndc))
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
		if i > 4096 {
			break
		}
	}
}

// point draws a single pixel at the rounded screen position.
// The BP line and point width register is stored, but point
// size 1 rendering is what this slice implements.
func (g *GPU) point(v pipeVertex) {
	if !insideClip(v) {
		return
	}
	g.plotScreen(g.toScreen(v))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
