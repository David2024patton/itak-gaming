package gx

import (
	"math"
	"sort"
)

// The rendering engine. At each VBlank it draws the display
// list into the frame buffers: rear plane first, then opaque
// polygons in submission order, then translucent polygons
// (Y-sorted unless the swap selected manual order), then the
// fog post-pass. Rasterization interpolates S, T, the vertex
// color, and 1/W linearly per span and divides per pixel
// (the documented perspective approach); depth interpolates
// linearly in X per span. Interpolation runs in float64; the
// depth buffer stores the rounded 24-bit result.
//
// Clipping is standard homogeneous clipping against the six
// view-volume planes (a stated simplification of the
// hardware's near-plane behavior). A polygon whose vertices
// all have W <= 0, or that still has a W <= 0 vertex after
// clipping, is not drawn.

// cvert is a clip-space vertex during clipping.
type cvert struct {
	x, y, z, w int32
	r, g, b    float64 // 6-bit color domain
	s, t       int32
}

// rvert is a screen-space vertex ready to rasterize.
type rvert struct {
	fx, fy float64 // pixel coordinates (top-down rows)
	depth  float64 // 24-bit depth value
	q      float64 // 1/W
	sq, tq float64 // texel-unit S and T times q
	rq, gq float64
	bq     float64
}

// render draws the current display list.
func (g *GX) render() {
	g.rebuildTex()
	disp3d := g.disp3dcnt()
	g.resetRear(disp3d)
	// Prepare polygons: cull, clip, project.
	type prepared struct {
		verts  []rvert
		poly   polygon
		maxY   float64
		wire   bool
		fullSz bool
	}
	var opaque, trans []prepared
	vp := g.viewport
	x1 := float64(vp & 0xFF)
	y1 := float64((vp >> 8) & 0xFF)
	x2 := float64((vp >> 16) & 0xFF)
	y2 := float64((vp >> 24) & 0xFF)
	vw, vh := x2-x1+1, y2-y1+1
	texOn := disp3d&1 != 0
	for _, pl := range g.display.polys {
		// Face culling by the clip-space winding.
		var area2 int64
		n := pl.n
		for i := 0; i < n; i++ {
			a := g.display.verts[pl.idx[i]]
			b := g.display.verts[pl.idx[(i+1)%n]]
			area2 += int64(a.x)*int64(b.y) - int64(b.x)*int64(a.y)
		}
		if area2 == 0 {
			continue // degenerate: occupies RAM, draws nothing
		}
		if area2 > 0 && pl.attr&(1<<7) == 0 {
			continue // front face (CCW) not enabled
		}
		if area2 < 0 && pl.attr&(1<<6) == 0 {
			continue // back face (CW) not enabled
		}
		// The 1-dot depth rule: a polygon no larger than one
		// pixel in each dimension is hidden when its first
		// vertex's W is behind DISP_1DOT_DEPTH, unless the
		// attr bit allows it.
		// (Extent is measured after projection, below.)
		cv := make([]cvert, 0, n+4)
		allNegW := true
		for i := 0; i < n; i++ {
			v := g.display.verts[pl.idx[i]]
			if v.w > 0 {
				allNegW = false
			}
			cv = append(cv, cvert{x: v.x, y: v.y, z: v.z, w: v.w,
				r: float64(v.r), g: float64(v.g), b: float64(v.b), s: v.s, t: v.t})
		}
		if allNegW {
			continue
		}
		// Far-plane crossing polygons are hidden whole when
		// the attr far-plane bit is clear.
		if pl.attr&(1<<12) == 0 {
			in, out := false, false
			for _, c := range cv {
				if int64(c.z) <= int64(c.w) {
					in = true
				} else {
					out = true
				}
			}
			if in && out {
				continue
			}
		}
		cv = clipPoly(cv)
		if len(cv) < 3 {
			continue
		}
		rv := make([]rvert, 0, len(cv))
		bad := false
		var minFx, maxFx, minFy, maxFy float64
		for i, c := range cv {
			if c.w <= 0 {
				bad = true
				break
			}
			sv := project(c, x1, y1, vw, vh, g.rndZBuf)
			rv = append(rv, sv)
			if i == 0 {
				minFx, maxFx, minFy, maxFy = sv.fx, sv.fx, sv.fy, sv.fy
			} else {
				minFx = math.Min(minFx, sv.fx)
				maxFx = math.Max(maxFx, sv.fx)
				minFy = math.Min(minFy, sv.fy)
				maxFy = math.Max(maxFy, sv.fy)
			}
		}
		if bad {
			continue
		}
		if maxFx-minFx <= 1 && maxFy-minFy <= 1 && pl.attr&(1<<13) == 0 {
			first := g.display.verts[pl.idx[0]]
			if int32(first.w>>9) > int32(g.dotDepth) {
				continue
			}
		}
		pp := prepared{verts: rv, poly: pl, maxY: maxFy,
			wire:   (pl.attr>>16)&31 == 0,
			fullSz: disp3d&0x30 != 0, // edge marking or anti-aliasing on
		}
		alpha := (pl.attr >> 16) & 31
		if alpha == 31 || pp.wire {
			opaque = append(opaque, pp)
		} else {
			pp.fullSz = true // translucent polygons render full size
			trans = append(trans, pp)
		}
	}
	for _, pp := range opaque {
		g.rasterize(pp.verts, pp.poly, disp3d, texOn, pp.wire, pp.fullSz)
	}
	if !g.rndManual {
		sort.SliceStable(trans, func(i, j int) bool { return trans[i].maxY < trans[j].maxY })
	}
	for _, pp := range trans {
		g.rasterize(pp.verts, pp.poly, disp3d, texOn, pp.wire, pp.fullSz)
	}
	g.applyFog(disp3d)
	// Compose the output buffer: rgb6 with alpha in bits
	// 18-22; fully transparent pixels read as 0.
	for i := range g.fb {
		if g.fbA[i] == 0 {
			g.fb[i] = 0
		} else {
			g.fb[i] = g.fbC[i] | uint32(g.fbA[i])<<18
		}
	}
}

// project maps one clipped vertex to screen space. The
// viewport formula follows GBATEK: sx = X1 + (x+w)*(X2-X1+1)
// /(2w); the Y axis is converted from the documented
// bottom-up viewport to top-down rows. Depth is the 24-bit Z
// value by default, or the normalized W value when the swap
// selected W buffering.
func project(c cvert, x1, y1, vw, vh float64, wBuf bool) rvert {
	wf := float64(c.w)
	sx := x1 + (float64(c.x)+wf)*vw/(2*wf)
	syBottom := y1 + (float64(c.y)+wf)*vh/(2*wf)
	fy := float64(Height) - syBottom
	var depth float64
	if wBuf {
		depth = wf * 256
	} else {
		depth = (float64(c.z) + wf) * 16777215.0 / (2 * wf)
	}
	if depth < 0 {
		depth = 0
	}
	if depth > 0xFFFFFF {
		depth = 0xFFFFFF
	}
	q := 4096.0 / wf // 1/W in real terms (W = w / 4096)
	return rvert{
		fx: sx, fy: fy, depth: depth, q: q,
		sq: float64(c.s) / 16 * q,
		tq: float64(c.t) / 16 * q,
		rq: c.r * q, gq: c.g * q, bq: c.b * q,
	}
}

// clipPoly clips a polygon against the six view-volume
// planes, interpolating all attributes linearly in clip
// space.
func clipPoly(in []cvert) []cvert {
	out := in
	for plane := 0; plane < 6; plane++ {
		if len(out) == 0 {
			return out
		}
		next := make([]cvert, 0, len(out)+1)
		for i := range out {
			a := out[i]
			b := out[(i+1)%len(out)]
			da := planeDist([4]int32{a.x, a.y, a.z, a.w}, plane)
			db := planeDist([4]int32{b.x, b.y, b.z, b.w}, plane)
			if da >= 0 {
				next = append(next, a)
			}
			if (da >= 0) != (db >= 0) {
				t := float64(da) / float64(da-db)
				next = append(next, lerpCvert(a, b, t))
			}
		}
		out = next
	}
	return out
}

func lerpCvert(a, b cvert, t float64) cvert {
	li := func(x, y int32) int32 {
		return int32(float64(x) + (float64(y)-float64(x))*t + 0.5)
	}
	lf := func(x, y float64) float64 { return x + (y-x)*t }
	return cvert{
		x: li(a.x, b.x), y: li(a.y, b.y), z: li(a.z, b.z), w: li(a.w, b.w),
		r: lf(a.r, b.r), g: lf(a.g, b.g), b: lf(a.b, b.b),
		s: li(a.s, b.s), t: li(a.t, b.t),
	}
}

// resetRear initializes the frame buffers from the rear
// plane: the clear color/depth registers, or the clear image
// bitmaps (texture slots 2 and 3, scrolled by
// CLRIMAGE_OFFSET with wraparound) when DISP3DCNT bit 14
// selects the bitmap rear plane.
func (g *GX) resetRear(disp3d uint32) {
	cc := g.clearColor
	id := uint8((cc >> 24) & 0x3F)
	clearD := expandDepth(uint32(g.clearDepth))
	if disp3d&(1<<14) != 0 {
		xo := int(g.clrImgOff & 0xFF)
		yo := int((g.clrImgOff >> 8) & 0xFF)
		for y := 0; y < Height; y++ {
			for x := 0; x < Width; x++ {
				i := y*Width + x
				src := uint32(((y+yo)&0xFF)*256 + ((x + xo) & 0xFF))
				cv := g.texHalf(0x40000 + src*2)
				dv := g.texHalf(0x60000 + src*2)
				g.fbC[i] = rgb6c(expandGX(uint32(cv&31)),
					expandGX(uint32((cv>>5)&31)), expandGX(uint32((cv>>10)&31)))
				if cv&0x8000 != 0 {
					g.fbA[i] = 31
				} else {
					g.fbA[i] = 0
				}
				g.fbDepth[i] = expandDepth(uint32(dv & 0x7FFF))
				g.fbAttr[i] = uint8(dv >> 15)
				g.fbID[i] = id
			}
		}
		return
	}
	rgb := rgb6c(expandGX(cc&31), expandGX((cc>>5)&31), expandGX((cc>>10)&31))
	alpha := uint8((cc >> 16) & 31)
	fog := uint8((cc >> 15) & 1)
	for i := range g.fbC {
		g.fbC[i] = rgb
		g.fbA[i] = alpha
		g.fbDepth[i] = clearD
		g.fbAttr[i] = fog
		g.fbID[i] = id
	}
}

// expandDepth expands a 15-bit depth to 24 bits by the
// documented formula X*0x200 plus 0x1FF when X is 0x7FFF.
func expandDepth(x uint32) uint32 {
	return x*0x200 + ((x+1)>>15)*0x1FF
}

func rgb6c(r, g, b uint32) uint32 { return r | g<<6 | b<<12 }

// rasterize fills one prepared convex polygon.
func (g *GX) rasterize(vs []rvert, pl polygon, disp3d uint32, texOn, wire, fullSz bool) {
	// Scanline range by the pixel-center rule. The normal
	// size rule excludes the bottom edge; full-size polygons
	// (translucent, edge-marked, anti-aliased) include it.
	minY, maxY := vs[0].fy, vs[0].fy
	for _, v := range vs[1:] {
		minY = math.Min(minY, v.fy)
		maxY = math.Max(maxY, v.fy)
	}
	yStart := int(math.Ceil(minY - 0.5))
	yEnd := int(math.Ceil(maxY - 0.5)) // exclusive
	if fullSz || wire {
		yEnd = int(math.Floor(maxY-0.5)) + 1
	}
	if yStart < 0 {
		yStart = 0
	}
	if yEnd > Height {
		yEnd = Height
	}
	type isect struct {
		x     float64
		attrs rvert
	}
	for y := yStart; y < yEnd; y++ {
		yc := float64(y) + 0.5
		var hits []isect
		for i := range vs {
			a := vs[i]
			b := vs[(i+1)%len(vs)]
			if (a.fy <= yc && yc < b.fy) || (b.fy <= yc && yc < a.fy) {
				t := (yc - a.fy) / (b.fy - a.fy)
				hits = append(hits, isect{
					x:     a.fx + (b.fx-a.fx)*t,
					attrs: lerpRvert(a, b, t),
				})
			}
		}
		if len(hits) < 2 {
			continue
		}
		lo, hi := hits[0], hits[0]
		for _, h := range hits[1:] {
			if h.x < lo.x {
				lo = h
			}
			if h.x > hi.x {
				hi = h
			}
		}
		xs := int(math.Ceil(lo.x - 0.5))
		xe := int(math.Ceil(hi.x - 0.5)) // exclusive
		if fullSz || wire {
			xe = int(math.Floor(hi.x-0.5)) + 1
		}
		if xs < 0 {
			xs = 0
		}
		if xe > Width {
			xe = Width
		}
		span := hi.x - lo.x
		for x := xs; x < xe; x++ {
			if wire && x != xs && x != xe-1 && y != yStart && y != yEnd-1 {
				continue
			}
			t := 0.0
			if span > 0 {
				t = (float64(x) + 0.5 - lo.x) / span
			}
			rv := lerpRvert(lo.attrs, hi.attrs, t)
			g.plot(x, y, rv, pl, disp3d, texOn, wire)
		}
	}
}

func lerpRvert(a, b rvert, t float64) rvert {
	l := func(x, y float64) float64 { return x + (y-x)*t }
	return rvert{
		depth: l(a.depth, b.depth), q: l(a.q, b.q),
		sq: l(a.sq, b.sq), tq: l(a.tq, b.tq),
		rq: l(a.rq, b.rq), gq: l(a.gq, b.gq), bq: l(a.bq, b.bq),
	}
}

// plot evaluates one pixel: texture sample and blend, alpha
// test, depth test, and the framebuffer write rules.
func (g *GX) plot(x, y int, rv rvert, pl polygon, disp3d uint32, texOn, wire bool) {
	i := y*Width + x
	depth := uint32(math.Floor(rv.depth + 0.5))
	old := g.fbDepth[i]
	diff := int64(depth) - int64(old)
	equalOK := pl.attr&(1<<14) != 0 && diff >= -0x200 && diff <= 0x200
	if diff >= 0 && !equalOK {
		return
	}
	// Vertex color at this pixel (perspective-divided).
	vc := vertex{
		r: clamp63(rv.rq / rv.q),
		g: clamp63(rv.gq / rv.q),
		b: clamp63(rv.bq / rv.q),
	}
	var tx texel
	if texOn {
		s := int(math.Floor(rv.sq / rv.q))
		t := int(math.Floor(rv.tq / rv.q))
		tx = g.sampleTexture(pl.tex, pl.pltt, s, t)
	}
	r, gg, b, a := g.blendTexture(tx, vc, pl.attr, disp3d)
	if wire {
		a = 31
	}
	if disp3d&4 != 0 && uint32(a) <= g.alphaRef {
		return // alpha test: draw only when alpha exceeds the reference
	}
	if a == 0 {
		return // fully transparent pixels update nothing
	}
	rgb := rgb6c(uint32(r), uint32(gg), uint32(b))
	translucent := a < 31
	if !translucent {
		g.fbC[i] = rgb
		g.fbA[i] = 31
		g.fbDepth[i] = depth
		g.fbAttr[i] = uint8((pl.attr >> 15) & 1)
		g.fbID[i] = uint8((pl.attr >> 24) & 0x3F)
		return
	}
	// Translucent write: alpha blending when enabled and
	// the old pixel is not fully transparent, else the
	// documented overwrite bypass.
	if disp3d&8 != 0 && g.fbA[i] > 0 {
		na := uint32(a)
		bl := func(p, o uint32) uint32 { return (p*(na+1) + o*(31-na)) / 32 }
		g.fbC[i] = rgb6c(
			bl(uint32(r), g.fbC[i]&63),
			bl(uint32(gg), g.fbC[i]>>6&63),
			bl(uint32(b), g.fbC[i]>>12&63))
		if a > g.fbA[i] {
			g.fbA[i] = a
		}
	} else {
		g.fbC[i] = rgb
		g.fbA[i] = a
	}
	if pl.attr&(1<<11) != 0 {
		g.fbDepth[i] = depth
		g.fbID[i] = uint8((pl.attr >> 24) & 0x3F)
	}
	// Fog flag: a translucent write can only clear it.
	g.fbAttr[i] &= uint8((pl.attr >> 15) & 1)
}

func clamp63(f float64) uint8 {
	if f <= 0 {
		return 0
	}
	if f >= 63 {
		return 63
	}
	return uint8(math.Floor(f + 0.5))
}

// applyFog runs the fog post-pass over flagged pixels: the
// density is interpolated between the documented depth
// boundaries (FOG_OFFSET plus FOG_STEP multiples, FOG_STEP
// = 0x400 shifted right by the DISP3DCNT fog shift) in the
// pixel depth's top 15 bits, then blended toward FOG_COLOR
// by (fog*density + color*(128-density))/128, density 127
// counting as 128. DISP3DCNT bit 6 selects alpha-only fog.
func (g *GX) applyFog(disp3d uint32) {
	if disp3d&(1<<7) == 0 {
		return
	}
	shift := (disp3d >> 8) & 0xF
	step := 0x400 >> shift
	fr := expandGX(g.fogColor & 31)
	fg := expandGX((g.fogColor >> 5) & 31)
	fb := expandGX((g.fogColor >> 10) & 31)
	fa := (g.fogColor >> 16) & 31
	alphaOnly := disp3d&(1<<6) != 0
	blend := func(c, f, density uint32) uint32 {
		return (f*density + c*(128-density)) / 128
	}
	for i := range g.fbC {
		if g.fbAttr[i]&1 == 0 {
			continue
		}
		d := int64(g.fbDepth[i] >> 9)
		var density uint32
		switch {
		case step == 0:
			if d <= int64(g.fogOffset) {
				density = uint32(g.fogTable[0])
			} else {
				density = uint32(g.fogTable[31])
			}
		case d <= int64(g.fogOffset)+int64(step):
			density = uint32(g.fogTable[0])
		default:
			// Find the boundary pair containing d.
			rel := d - int64(g.fogOffset)
			n := int(rel / int64(step)) // boundary index (1-based segment)
			if n > 31 {
				density = uint32(g.fogTable[31])
			} else {
				lo := int64(g.fogOffset) + int64(step)*int64(n)
				frac := d - lo
				d0 := int64(g.fogTable[n-1])
				d1 := int64(g.fogTable[n])
				density = uint32(d0 + (d1-d0)*frac/int64(step))
			}
		}
		if density == 127 {
			density = 128
		}
		if !alphaOnly {
			g.fbC[i] = rgb6c(
				blend(g.fbC[i]&63, fr, density),
				blend(g.fbC[i]>>6&63, fg, density),
				blend(g.fbC[i]>>12&63, fb, density))
		}
		g.fbA[i] = uint8(blend(uint32(g.fbA[i]), fa, density))
	}
}

// Line3D is the video package's 3D layer provider: it fills
// dst with one scanline of the rendered layer (rgb6 with the
// alpha in bits 18-22, 0 = transparent). The horizontal BG0
// scroll applies with the documented 512-pixel wrap (the
// second 256 pixels are transparent), and POWCNT1 bit 2
// (rendering engine enable) gates the output.
func (g *GX) Line3D(y int, dst *[Width]uint32) {
	for i := range dst {
		dst[i] = 0
	}
	pow := uint32(g.bus.IO9[0x304]) | uint32(g.bus.IO9[0x305])<<8
	if pow&4 == 0 {
		return
	}
	scroll := int(g.bus.IO9[0x10]) | int(g.bus.IO9[0x11]&1)<<8
	for x := 0; x < Width; x++ {
		sx := x + scroll
		if sx >= 512 {
			sx -= 512
		}
		if sx >= Width {
			continue
		}
		dst[x] = g.fb[y*Width+sx]
	}
}
