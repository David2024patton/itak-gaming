package pvr

import (
	"math"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/holly"
)

// ISP/TSP renderer for DC-2. Render consumes the primitive
// lists the TA built, rasterizes them into internal float
// buffers, and writes the result back into the VRAM
// framebuffer in the format FB_W_CTRL selects. The internal
// buffers hold RGBA color as floats in the range 0 to 1, a
// float depth per pixel (the depth value is the reciprocal
// of w, exactly as submitted in the vertex Z), and a signed
// modifier counter per pixel used by modifier volumes, shadow
// texture instructions, and two-volume parameter selection.
//
// Documented simplifications for this slice:
//   - The background plane is color only, even when the
//     texture bit in ISP_BACKGND_T is set.
//   - Trilinear filter modes sample one mip level (the LOD
//     level) with bilinear filtering.
//   - VQ textures are not mipmapped.
//   - Modifier volumes never write depth.
//   - Primitives are consumed in per-pixel submission order
//     within the hardware list order, which is pixel
//     identical to the hardware tile order for this model.

// texFmt8888 is the TCW pixel format code for ARGB8888,
// which has no named constant in ta.go.
const texFmt8888 = 7

// tspTexInstr extracts the TSP texture instruction field,
// bits 25:24: 0 replace, 1 modulate, 2 decal, 3 shadow.
func tspTexInstr(w uint32) int { return int((w >> 24) & 3) }

// frame is the internal render target for one Render call.
type frame struct {
	w, h  int
	color []float32 // RGBA per pixel
	depth []float32
	mod   []int32
}

// Render rasterizes the current TA scene and writes the
// finished frame into the VRAM framebuffer at FB_W_SOF1.
// When the FB_W_CTRL enable bit is clear the pipeline still
// runs into the internal buffers but the VRAM write is
// skipped, so Present (which reads VRAM) shows nothing new.
func (p *PVR) Render() {
	w, h := p.renderWidth(), p.renderHeight()
	fb := &frame{w: w, h: h,
		color: make([]float32, w*h*4),
		depth: make([]float32, w*h),
		mod:   make([]int32, w*h)}

	// Background: depth from ISP_BACKGND_D, color from
	// ISP_BACKGND_S (alpha bits 31:24, then R, G, B bytes).
	bgDepth := math.Float32frombits(p.reg(RegISPBackgndD))
	bg := p.reg(RegISPBackgndS)
	bgCol := [4]float32{
		float32((bg>>16)&0xFF) / 255,
		float32((bg>>8)&0xFF) / 255,
		float32(bg&0xFF) / 255,
		float32((bg>>24)&0xFF) / 255,
	}
	for i := range fb.depth {
		fb.depth[i] = bgDepth
		copy(fb.color[i*4:], bgCol[:])
	}

	alphaRef := float32((p.reg(RegTextControl)>>24)&0xFF) / 255
	regionBase := p.reg(RegRegionBase)
	tilesX := (w + 31) / 32

	// Hardware list order: opaque, opaque modifier,
	// punchthrough, translucent modifier, translucent.
	order := [5]int{ListOpaque, ListOpaqueMod, ListPunch, ListTransMod, ListTrans}
	for _, list := range order {
		for i := range p.ta.prims[list] {
			p.drawPrim(fb, &p.ta.prims[list][i], alphaRef, regionBase, tilesX)
		}
	}

	p.writeFrame(fb)
	p.renderCount++
	p.raise(holly.BankNormal, 1<<holly.EvtRenderDoneISP)
	p.raise(holly.BankNormal, 1<<holly.EvtRenderDoneTSP)
}

// paramSet is one ISP/TSP + TSP + TCW parameter triple of a
// primitive: set 0 is the primary triple, set 1 the second
// triple of a two-volume primitive.
type paramSet struct {
	isp, tsp, tcw uint32
}

func primSet(pr *Prim, idx int) paramSet {
	if idx == 1 {
		return paramSet{pr.ISPTSP2, pr.TSP2, pr.TCW2}
	}
	return paramSet{pr.ISPTSP, pr.TSP, pr.TCW}
}

// vertInterp holds the per-vertex values one triangle
// interpolates for one parameter set. The u, v and color
// channels are premultiplied by Z (which is 1/w) so the
// rasterizer can divide by the interpolated Z per pixel,
// the perspective-correct form the hardware uses.
type vertInterp struct {
	u, v   float32 // raw texture coordinates, for LOD
	uq, vq float32
	base   [4]float32 // RGBA, premultiplied by Z
	off    [4]float32 // RGBA, premultiplied by Z
}

// buildInterp prepares the interpolated values of one vertex
// for one parameter set. Gouraud primitives take colors from
// the vertex; flat primitives take the header colors.
// Two-volume primitives always take colors from the vertex,
// because their headers carry no colors, and sprites do the
// same, because the TA bakes the sprite vertex parameter's
// base and offset colors into every corner vertex.
func buildInterp(pr *Prim, v *Vertex, setIdx int, z float32) vertInterp {
	set := primSet(pr, setIdx)
	var u, vv float32
	var baseWord, offWord uint32
	if setIdx == 1 {
		u, vv = v.U2, v.V2
		baseWord, offWord = v.Base2, v.Offset2
	} else {
		u, vv = v.U, v.V
		baseWord, offWord = v.Base, v.Offset
	}
	if set.isp&ispGouraud == 0 && !pr.TwoVolume() && !pr.Sprite {
		baseWord, offWord = pr.BaseColor, pr.OffsetColor
	}
	vi := vertInterp{u: u, v: vv, uq: u * z, vq: vv * z}
	bc := unpackARGB(baseWord)
	oc := unpackARGB(offWord)
	for c := 0; c < 4; c++ {
		vi.base[c] = bc[c] * z
		vi.off[c] = oc[c] * z
	}
	return vi
}

// unpackARGB converts a packed ARGB8888 word to RGBA floats
// in the range 0 to 1.
func unpackARGB(word uint32) [4]float32 {
	return [4]float32{
		float32((word>>16)&0xFF) / 255,
		float32((word>>8)&0xFF) / 255,
		float32(word&0xFF) / 255,
		float32((word>>24)&0xFF) / 255,
	}
}

func clamp01(f float32) float32 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// depthPass applies one ISP depth compare mode: the new
// value q against the stored depth.
func depthPass(mode int, q, stored float32) bool {
	switch mode {
	case DepthNever:
		return false
	case DepthLess:
		return q < stored
	case DepthEqual:
		return q == stored
	case DepthLEqual:
		return q <= stored
	case DepthGreater:
		return q > stored
	case DepthNotEq:
		return q != stored
	case DepthGEqual:
		return q >= stored
	case DepthAlways:
		return true
	}
	return false
}

// edgeFn is the signed double area of the triangle a, b, p.
func edgeFn(ax, ay, bx, by, px, py float32) float32 {
	return (bx-ax)*(py-ay) - (by-ay)*(px-ax)
}

// drawPrim rasterizes every triangle of one primitive strip.
func (p *PVR) drawPrim(fb *frame, pr *Prim, alphaRef float32, regionBase uint32, tilesX int) {
	if len(pr.Verts) < 3 {
		return
	}
	isVolume := pr.List == ListOpaqueMod
	twoVol := pr.TwoVolume()
	for k := 0; k+2 < len(pr.Verts); k++ {
		vs := [3]*Vertex{&pr.Verts[k], &pr.Verts[k+1], &pr.Verts[k+2]}
		// Surface facing: signed area in submitted order,
		// flipped on odd strip indices.
		a2 := edgeFn(vs[0].X, vs[0].Y, vs[1].X, vs[1].Y, vs[2].X, vs[2].Y)
		if a2 == 0 {
			continue // degenerate: covers no pixels
		}
		facing := a2
		if k%2 == 1 {
			facing = -a2
		}
		switch ispCullMode(pr.ISPTSP) {
		case 1:
			if facing == 0 {
				continue
			}
		case 2:
			if facing < 0 {
				continue
			}
		case 3:
			if facing > 0 {
				continue
			}
		}

		var interp [2][3]vertInterp
		var lod [2]int
		for s := 0; s < 2; s++ {
			if s == 1 && !twoVol {
				break
			}
			for i := 0; i < 3; i++ {
				interp[s][i] = buildInterp(pr, vs[i], s, vs[i].Z)
			}
			set := primSet(pr, s)
			if set.isp&ispTextured != 0 {
				lod[s] = p.texLOD(set.tsp, set.tcw, vs, interp[s])
			}
		}

		minX := minf3(vs[0].X, vs[1].X, vs[2].X)
		maxX := maxf3(vs[0].X, vs[1].X, vs[2].X)
		minY := minf3(vs[0].Y, vs[1].Y, vs[2].Y)
		maxY := maxf3(vs[0].Y, vs[1].Y, vs[2].Y)
		x0 := clampInt(int(math.Floor(float64(minX))), 0, fb.w-1)
		x1 := clampInt(int(math.Floor(float64(maxX))), 0, fb.w-1)
		y0 := clampInt(int(math.Floor(float64(minY))), 0, fb.h-1)
		y1 := clampInt(int(math.Floor(float64(maxY))), 0, fb.h-1)

		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				cx, cy := float32(x)+0.5, float32(y)+0.5
				l0 := edgeFn(vs[1].X, vs[1].Y, vs[2].X, vs[2].Y, cx, cy) / a2
				l1 := edgeFn(vs[2].X, vs[2].Y, vs[0].X, vs[0].Y, cx, cy) / a2
				l2 := edgeFn(vs[0].X, vs[0].Y, vs[1].X, vs[1].Y, cx, cy) / a2
				if l0 < 0 || l1 < 0 || l2 < 0 {
					continue
				}
				idx := y*fb.w + x
				if regionDisabled(p, regionBase, tilesX, pr.List, x, y) {
					continue
				}
				q := l0*vs[0].Z + l1*vs[1].Z + l2*vs[2].Z

				if isVolume {
					// Modifier volume: no color output
					// and no depth write, ever. Pixels
					// passing the volume's own depth test
					// adjust the counter by facing.
					if !depthPass(ispDepthMode(pr.ISPTSP), q, fb.depth[idx]) {
						continue
					}
					if facing > 0 {
						fb.mod[idx]++
					} else if facing < 0 {
						fb.mod[idx] += -1
					}
					continue
				}
				p.shadePixel(fb, pr, idx, interp, lod, l0, l1, l2, q, alphaRef)
			}
		}
	}
}

func minf3(a, b, c float32) float32 {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func maxf3(a, b, c float32) float32 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

// regionDisabled reports whether the region array disables
// this list inside the tile holding pixel (x, y). The array
// entry for a tile is 6 words at RegionBase + tile*24, and
// word 0 bits 4:0 hold one disable bit per list.
func regionDisabled(p *PVR, regionBase uint32, tilesX, list, x, y int) bool {
	if regionBase == 0 {
		return false
	}
	tile := (y/32)*tilesX + x/32
	word := p.vramWord(regionBase + uint32(tile*24))
	return (word>>uint(list))&1 != 0
}

// shadePixel runs depth, texturing, punchthrough, and
// blending for one covered pixel of a shaded primitive.
func (p *PVR) shadePixel(fb *frame, pr *Prim, idx int, interp [2][3]vertInterp, lod [2]int,
	l0, l1, l2, q float32, alphaRef float32) {
	if q == 0 {
		return // no projective divide is possible
	}
	odd := fb.mod[idx]&1 != 0
	setIdx := 0
	if pr.TwoVolume() {
		if odd {
			setIdx = 1
		}
	} else if pr.List == ListTransMod && odd {
		// Single-volume translucent modifier primitives
		// shade only where the counter is even.
		return
	}
	set := primSet(pr, setIdx)
	if !depthPass(ispDepthMode(set.isp), q, fb.depth[idx]) {
		return
	}
	iv := interp[setIdx]
	u := (l0*iv[0].uq + l1*iv[1].uq + l2*iv[2].uq) / q
	v := (l0*iv[0].vq + l1*iv[1].vq + l2*iv[2].vq) / q
	var base, off [4]float32
	for c := 0; c < 4; c++ {
		base[c] = (l0*iv[0].base[c] + l1*iv[1].base[c] + l2*iv[2].base[c]) / q
		off[c] = (l0*iv[0].off[c] + l1*iv[1].off[c] + l2*iv[2].off[c]) / q
	}

	src := base
	texA := base[3]
	if set.isp&ispTextured != 0 {
		tex := p.sampleTexture(set.tsp, set.tcw, u, v, lod[setIdx])
		texA = tex[3]
		if pr.List == ListPunch && texA < alphaRef {
			return // discarded: no depth or color write
		}
		switch tspTexInstr(set.tsp) {
		case 0: // replace
			src = tex
			if tspIgnoreTexA(set.tsp) {
				src[3] = base[3]
			}
		case 1: // modulate
			for c := 0; c < 3; c++ {
				src[c] = tex[c] * base[c]
			}
			if tspIgnoreTexA(set.tsp) {
				src[3] = base[3]
			} else {
				src[3] = tex[3] * base[3]
			}
		case 2: // decal
			for c := 0; c < 3; c++ {
				src[c] = tex[c]*tex[3] + base[c]*(1-tex[3])
			}
			src[3] = base[3]
		case 3: // shadow: modulate, then scale by the
			// shadow scale where the counter is odd
			for c := 0; c < 3; c++ {
				src[c] = tex[c] * base[c]
			}
			if tspIgnoreTexA(set.tsp) {
				src[3] = base[3]
			} else {
				src[3] = tex[3] * base[3]
			}
			if odd {
				scale := math.Float32frombits(p.reg(RegFPUShadScale))
				for c := 0; c < 3; c++ {
					src[c] *= scale
				}
			}
		}
	} else if pr.List == ListPunch && texA < alphaRef {
		return
	}
	if set.isp&ispOffset != 0 {
		for c := 0; c < 3; c++ {
			src[c] += off[c]
		}
	}
	for c := 0; c < 4; c++ {
		src[c] = clamp01(src[c])
	}

	if set.isp&ispZWriteDis == 0 {
		fb.depth[idx] = q
	}
	dst := [4]float32{fb.color[idx*4], fb.color[idx*4+1], fb.color[idx*4+2], fb.color[idx*4+3]}
	if pr.List == ListTrans || pr.List == ListTransMod || tspUseAlpha(set.tsp) {
		sf := tspSrcBlend(set.tsp)
		df := tspDstBlend(set.tsp)
		for c := 0; c < 4; c++ {
			dst[c] = src[c]*blendFactor(sf, c, src, dst) + dst[c]*blendFactor(df, c, src, dst)
		}
	} else {
		dst = src
	}
	copy(fb.color[idx*4:], dst[:])
}

// blendFactor evaluates one TSP blend function for channel
// c (0 R, 1 G, 2 B, 3 A) of the src and dst colors.
func blendFactor(code, c int, src, dst [4]float32) float32 {
	switch code {
	case BlendZero:
		return 0
	case BlendOne:
		return 1
	case BlendDstColor:
		return dst[c]
	case BlendOneMinusDst:
		return 1 - dst[c]
	case BlendSrcAlpha:
		return src[3]
	case BlendOneMinusSrcA:
		return 1 - src[3]
	case BlendDstAlpha:
		return dst[3]
	case BlendOneMinusDstA:
		return 1 - dst[3]
	}
	return 0
}

// Texture sampling

// texInfo describes one mip level of one texture for the
// duration of a sample.
type texInfo struct {
	tsp, tcw uint32
	fmt      int
	w, h     int    // level dimensions in texels
	base     uint32 // VRAM byte offset of the level data
	vq       bool
}

// bppBits returns the storage size of one texel.
func bppBits(fmt int) int {
	switch fmt {
	case TexPal4:
		return 4
	case TexPal8:
		return 8
	case texFmt8888:
		return 32
	}
	return 16
}

// mipMaxLevel returns the index of the smallest mip level.
func mipMaxLevel(w, h int) int {
	l := 0
	for (w>>l) > 1 || (h>>l) > 1 {
		l++
	}
	return l
}

// texLOD picks the mip level for one triangle. The footprint
// is the texel-space step per pixel, from screen-space
// derivatives of the texture coordinates formed by finite
// differences over the triangle vertices, and the level is
// round(log2(footprint)) adjusted by the TSP mip bias field
// (bias minus 4, matching the signed hardware bias form).
func (p *PVR) texLOD(tsp, tcw uint32, vs [3]*Vertex, iv [3]vertInterp) int {
	if !tcwMipMapped(tcw) {
		return 0
	}
	tw, th := tspUSize(tsp), tspVSize(tsp)
	maxLevel := mipMaxLevel(tw, th)
	det := (vs[1].X-vs[0].X)*(vs[2].Y-vs[0].Y) - (vs[2].X-vs[0].X)*(vs[1].Y-vs[0].Y)
	if det == 0 {
		return 0
	}
	deriv := func(f0, f1, f2 float32) (float32, float32) {
		dx := ((f1-f0)*(vs[2].Y-vs[0].Y) - (f2-f0)*(vs[1].Y-vs[0].Y)) / det
		dy := ((f2-f0)*(vs[1].X-vs[0].X) - (f1-f0)*(vs[2].X-vs[0].X)) / det
		return dx, dy
	}
	dudx, dudy := deriv(iv[0].u, iv[1].u, iv[2].u)
	dvdx, dvdy := deriv(iv[0].v, iv[1].v, iv[2].v)
	foot := math.Max(math.Abs(float64(dudx))*float64(tw), math.Abs(float64(dvdx))*float64(th))
	foot = math.Max(foot, math.Abs(float64(dudy))*float64(tw))
	foot = math.Max(foot, math.Abs(float64(dvdy))*float64(th))
	if foot < 1 {
		foot = 1
	}
	lod := int(math.Round(math.Log2(foot))) + tspMipBias(tsp) - 4
	if lod < 0 {
		lod = 0
	}
	if lod > maxLevel {
		lod = maxLevel
	}
	return lod
}

// texLevel builds the texInfo for one LOD level. Mipmapped
// textures store the smallest levels first in VRAM and the
// largest (level 0) last, so a level's offset accumulates
// the texel counts of every smaller level.
func (p *PVR) texLevel(tsp, tcw uint32, lod int) texInfo {
	tw, th := tspUSize(tsp), tspVSize(tsp)
	ti := texInfo{tsp: tsp, tcw: tcw, fmt: tcwPixelFmt(tcw), vq: tcwVQ(tcw)}
	base := tcwAddr(tcw)
	if ti.vq {
		// VQ textures are not mipmapped in this
		// implementation: the codebook and index data sit
		// at the texture base at full size.
		ti.w, ti.h, ti.base = tw, th, base
		return ti
	}
	if !tcwMipMapped(tcw) {
		ti.w, ti.h, ti.base = tw, th, base
		return ti
	}
	maxLevel := mipMaxLevel(tw, th)
	if lod > maxLevel {
		lod = maxLevel
	}
	smaller := 0
	for l := lod + 1; l <= maxLevel; l++ {
		smaller += maxInt(1, tw>>l) * maxInt(1, th>>l)
	}
	ti.w = maxInt(1, tw>>lod)
	ti.h = maxInt(1, th>>lod)
	ti.base = base + uint32(smaller*bppBits(ti.fmt)/8)
	return ti
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// sampleTexture samples one texture at (u, v) under the TSP
// filter mode, returning RGBA floats in the range 0 to 1.
func (p *PVR) sampleTexture(tsp, tcw uint32, u, v float32, lod int) [4]float32 {
	ti := p.texLevel(tsp, tcw, lod)
	filter := tspFilter(tsp)
	if filter == 0 {
		// Point sampling: nearest texel.
		x := wrapCoord(int(math.Floor(float64(u)*float64(ti.w))), ti.w, tspUClamp(tsp), tspUFlip(tsp))
		y := wrapCoord(int(math.Floor(float64(v)*float64(ti.h))), ti.h, tspVClamp(tsp), tspVFlip(tsp))
		return p.fetchTexel(&ti, x, y)
	}
	// Bilinear. Trilinear modes 2 and 3 also land here,
	// sampling the single LOD level (documented above).
	s := float64(u)*float64(ti.w) - 0.5
	t := float64(v)*float64(ti.h) - 0.5
	x0 := int(math.Floor(s))
	y0 := int(math.Floor(t))
	fx := float32(s - float64(x0))
	fy := float32(t - float64(y0))
	c00 := p.fetchTexel(&ti, wrapCoord(x0, ti.w, tspUClamp(tsp), tspUFlip(tsp)), wrapCoord(y0, ti.h, tspVClamp(tsp), tspVFlip(tsp)))
	c10 := p.fetchTexel(&ti, wrapCoord(x0+1, ti.w, tspUClamp(tsp), tspUFlip(tsp)), wrapCoord(y0, ti.h, tspVClamp(tsp), tspVFlip(tsp)))
	c01 := p.fetchTexel(&ti, wrapCoord(x0, ti.w, tspUClamp(tsp), tspUFlip(tsp)), wrapCoord(y0+1, ti.h, tspVClamp(tsp), tspVFlip(tsp)))
	c11 := p.fetchTexel(&ti, wrapCoord(x0+1, ti.w, tspUClamp(tsp), tspUFlip(tsp)), wrapCoord(y0+1, ti.h, tspVClamp(tsp), tspVFlip(tsp)))
	var out [4]float32
	for c := 0; c < 4; c++ {
		top := c00[c]*(1-fx) + c10[c]*fx
		bot := c01[c]*(1-fx) + c11[c]*fx
		out[c] = top*(1-fy) + bot*fy
	}
	return out
}

// wrapCoord applies one texture axis mode to a texel
// coordinate: clamp to the edge, mirror with period 2*size,
// or repeat with period size.
func wrapCoord(c, size int, clampMode, flipMode bool) int {
	if size <= 0 {
		return 0
	}
	if clampMode {
		return clampInt(c, 0, size-1)
	}
	if flipMode {
		m := c % (2 * size)
		if m < 0 {
			m += 2 * size
		}
		if m >= size {
			m = 2*size - 1 - m
		}
		return m
	}
	m := c % size
	if m < 0 {
		m += size
	}
	return m
}

// fetchTexel reads one texel of the level at integer
// coordinates already passed through the axis modes.
func (p *PVR) fetchTexel(ti *texInfo, x, y int) [4]float32 {
	if ti.vq {
		return p.fetchVQTexel(ti, x, y)
	}
	var idx int
	if tspLinear(ti.tsp) {
		stride := ti.w
		if tspStride(ti.tsp) {
			bytes := int((p.reg(RegTextControl)>>1)&0x3F) * 32
			if texels := bytes * 8 / bppBits(ti.fmt); texels > 0 {
				stride = texels
			}
		}
		idx = y*stride + x
	} else {
		idx = twiddleIndex(x, y, ti.w, ti.h)
	}
	switch ti.fmt {
	case TexPal4:
		b := p.vramByte(ti.base + uint32(idx/2))
		nib := int(b & 0xF)
		if idx%2 == 1 {
			nib = int(b >> 4)
		}
		return p.paletteColor(tcwPalSelect(ti.tcw)*16 + nib)
	case TexPal8:
		b := p.vramByte(ti.base + uint32(idx))
		palIdx := ((tcwPalSelect(ti.tcw) & 0x30) << 4) + int(b)
		return p.paletteColor(palIdx)
	case TexYUV:
		// One 32-bit word holds two texels: low byte Y0,
		// then U, then Y1, then V. Even x uses Y0, odd x Y1.
		word := p.vramWord(ti.base + uint32((idx/2)*4))
		yy := int(word & 0xFF)
		if x%2 == 1 {
			yy = int((word >> 16) & 0xFF)
		}
		r, g, b := yuvToRGB(yy, int((word>>8)&0xFF), int((word>>24)&0xFF))
		return [4]float32{float32(r) / 255, float32(g) / 255, float32(b) / 255, 1}
	}
	return p.decodeTexelAt(ti.base+uint32(idx*bppBits(ti.fmt)/8), ti.fmt)
}

// fetchVQTexel reads one texel of a VQ compressed texture.
// The codebook holds 256 entries of 4 texels at the texture
// base; index data follows, one byte per 2x2 block, with the
// block coordinates twiddled over the half-size grid. The
// texel order inside a block is (0,0), (1,0), (0,1), (1,1).
func (p *PVR) fetchVQTexel(ti *texInfo, x, y int) [4]float32 {
	texelBytes := bppBits(ti.fmt) / 8
	entryBytes := 4 * texelBytes
	codebookBytes := 256 * entryBytes
	bw := maxInt(1, ti.w/2)
	bh := maxInt(1, ti.h/2)
	bidx := twiddleIndex(x/2, y/2, bw, bh)
	code := int(p.vramByte(ti.base + uint32(codebookBytes+bidx)))
	pos := (y&1)*2 + (x & 1)
	off := ti.base + uint32(code*entryBytes+pos*texelBytes)
	return p.decodeTexelAt(off, ti.fmt)
}

// decodeTexelAt decodes one raw texel at a VRAM byte offset
// in a non-palette pixel format.
func (p *PVR) decodeTexelAt(off uint32, fmt int) [4]float32 {
	switch fmt {
	case Tex565:
		return decode565(uint16(p.vramWord16(off)))
	case Tex4444:
		return decode4444(uint16(p.vramWord16(off)))
	case texFmt8888:
		return decode8888(p.vramWord(off))
	case TexPal4, TexPal8:
		// Palette formats store indices, not colors; the
		// fetch paths resolve them before this point. A
		// zero color is returned for direct calls.
		return [4]float32{}
	}
	// Tex1555, and TexBump treated as Tex1555 in this slice.
	return decode1555(uint16(p.vramWord16(off)))
}

// paletteColor resolves one palette RAM entry to RGBA under
// the PAL_RAM_CTRL format: 0 = 1555, 1 = 565, 2 = 4444,
// 3 = 8888 (entry word as ARGB).
func (p *PVR) paletteColor(idx int) [4]float32 {
	if idx < 0 || idx >= len(p.palette) {
		return [4]float32{}
	}
	entry := p.palette[idx]
	switch p.reg(RegPALRamCtrl) & 3 {
	case 1:
		return decode565(uint16(entry))
	case 2:
		return decode4444(uint16(entry))
	case 3:
		return decode8888(entry)
	}
	return decode1555(uint16(entry))
}

func decode1555(v uint16) [4]float32 {
	a := float32(0)
	if v&0x8000 != 0 {
		a = 1
	}
	return [4]float32{
		float32((v>>10)&0x1F) / 31,
		float32((v>>5)&0x1F) / 31,
		float32(v&0x1F) / 31,
		a,
	}
}

func decode565(v uint16) [4]float32 {
	return [4]float32{
		float32((v>>11)&0x1F) / 31,
		float32((v>>5)&0x3F) / 63,
		float32(v&0x1F) / 31,
		1,
	}
}

func decode4444(v uint16) [4]float32 {
	return [4]float32{
		float32((v>>8)&0xF) / 15,
		float32((v>>4)&0xF) / 15,
		float32(v&0xF) / 15,
		float32((v>>12)&0xF) / 15,
	}
}

func decode8888(word uint32) [4]float32 {
	return unpackARGB(word)
}

// vramByte reads one byte of VRAM with the offset masked to
// the 8 MB space.
func (p *PVR) vramByte(off uint32) byte {
	return p.vram[off&0x7FFFFF]
}

// vramWord16 reads one 16-bit value from VRAM, byte by byte
// so offsets near the end of the space cannot overrun.
func (p *PVR) vramWord16(off uint32) uint32 {
	return uint32(p.vramByte(off)) | uint32(p.vramByte(off+1))<<8
}

// vramWord reads one 32-bit value from VRAM, byte by byte.
func (p *PVR) vramWord(off uint32) uint32 {
	return uint32(p.vramByte(off)) | uint32(p.vramByte(off+1))<<8 |
		uint32(p.vramByte(off+2))<<16 | uint32(p.vramByte(off+3))<<24
}

// vramPutByte writes one byte of VRAM with the offset masked
// to the 8 MB space.
func (p *PVR) vramPutByte(off uint32, b byte) {
	p.vram[off&0x7FFFFF] = b
}

// Framebuffer write-back

// bayer4 is the 4x4 ordered dither matrix.
var bayer4 = [4][4]int{
	{0, 8, 2, 10},
	{12, 4, 14, 6},
	{3, 11, 1, 9},
	{15, 7, 13, 5},
}

// quantize rounds a 0 to 1 channel to an integer with the
// given maximum, round to nearest.
func quantize(f float32, max int) int {
	n := int(clamp01(f)*float32(max) + 0.5)
	if n > max {
		n = max
	}
	return n
}

// writeFrame stores the internal frame into VRAM at
// FB_W_SOF1 in the FB_W_CTRL format, clipped by the
// FB_X_CLIP and FB_Y_CLIP scissor. A clip max of 0 disables
// the scissor on that axis.
func (p *PVR) writeFrame(fb *frame) {
	ctrl := p.reg(RegFBWCTRL)
	if ctrl&fbEnable == 0 {
		return
	}
	format := int((ctrl & fbDepth) >> 1)
	bpp := 2
	switch format {
	case Fmt0888:
		bpp = 3
	case Fmt8888:
		bpp = 4
	}
	stride := fb.w * bpp
	if ls := p.reg(RegFBWLineStride); ls != 0 {
		stride = int((ls&0x3FF)+1) * 8
	}
	xClip := p.reg(RegFBXClip)
	yClip := p.reg(RegFBYClip)
	xMin, xMax := int(xClip&0x7FF), int((xClip>>16)&0x7FF)
	yMin, yMax := int(yClip&0xFFF), int((yClip>>16)&0xFFF)
	if xMax == 0 {
		xMax = fb.w - 1
	}
	if yMax == 0 {
		yMax = fb.h - 1
	}
	base := p.reg(RegFBWSOF1)
	dither := ctrl&fbDither != 0
	for y := 0; y < fb.h; y++ {
		if y < yMin || y > yMax {
			continue
		}
		for x := 0; x < fb.w; x++ {
			if x < xMin || x > xMax {
				continue
			}
			idx := (y*fb.w + x) * 4
			r, g, b, a := fb.color[idx], fb.color[idx+1], fb.color[idx+2], fb.color[idx+3]
			off := base + uint32(y*stride+x*bpp)
			switch format {
			case Fmt0565:
				dr, dg, db := ditherOffsets(dither, x, y)
				val := uint16(quantize(r+dr, 31))<<11 |
					uint16(quantize(g+dg, 63))<<5 |
					uint16(quantize(b+db, 31))
				p.vramPutByte(off, byte(val))
				p.vramPutByte(off+1, byte(val>>8))
			case Fmt0555:
				dr, dg, db := ditherOffsets(dither, x, y)
				var val uint16
				if a >= 0.5 {
					val = 1 << 15
				}
				val |= uint16(quantize(r+dr, 31))<<10 |
					uint16(quantize(g+dg, 31))<<5 |
					uint16(quantize(b+db, 31))
				p.vramPutByte(off, byte(val))
				p.vramPutByte(off+1, byte(val>>8))
			case Fmt0888:
				p.vramPutByte(off, byte(quantize(b, 255)))
				p.vramPutByte(off+1, byte(quantize(g, 255)))
				p.vramPutByte(off+2, byte(quantize(r, 255)))
			case Fmt8888:
				word := uint32(quantize(a, 255))<<24 |
					uint32(quantize(r, 255))<<16 |
					uint32(quantize(g, 255))<<8 |
					uint32(quantize(b, 255))
				for i := 0; i < 4; i++ {
					p.vramPutByte(off+uint32(i), byte(word>>(8*i)))
				}
			}
		}
	}
}

// ditherOffsets returns the ordered dither offsets for the
// 5-bit and 6-bit channels at one pixel: the returned values
// are (red and blue offset, green offset, blue offset), all
// zero when dither is off. The offset is (m/16 - 0.5) scaled
// by one quantization step of the channel.
func ditherOffsets(on bool, x, y int) (float32, float32, float32) {
	if !on {
		return 0, 0, 0
	}
	m := float32(bayer4[y&3][x&3])
	o5 := (m/16 - 0.5) * (1.0 / 31)
	o6 := (m/16 - 0.5) * (1.0 / 63)
	return o5, o6, o5
}

// Display output

// Present fills out (w by h XRGB8888 pixels) with the
// displayed frame read back from VRAM under FB_R_CTRL and
// FB_R_SIZE. When the display is disabled, or outside the
// source rectangle, pixels take the border color.
func (p *PVR) Present(out []uint32, w, h int) {
	border := 0xFF000000 | (p.reg(RegBorderCol) & 0xFFFFFF)
	for i := range out {
		out[i] = border
	}
	ctrl := p.reg(RegFBRCTRL)
	if ctrl&fbEnable == 0 {
		return
	}
	size := p.reg(RegFBRSize)
	widthWords := int(size&0x3FF) + 1
	height := int((size>>10)&0x3FF) + 1
	moduloWords := int((size >> 20) & 0x3FF)
	strideWords := widthWords + moduloWords
	format := int((ctrl & fbDepth) >> 1)
	srcW := widthWords * 2
	switch format {
	case Fmt8888:
		srcW = widthWords
	case Fmt0888:
		srcW = widthWords * 4 / 3
	}
	scale := p.reg(RegScalerCtl)&1 != 0
	presW := srcW
	if scale {
		// The scaler halves the presented width by
		// sampling every other source pixel.
		presW = srcW / 2
	}
	interlace := p.reg(RegSPGControl)&spgInterlace != 0
	sof1 := p.reg(RegFBRSOF1)
	sof2 := p.reg(RegFBRSOF2)
	for y := 0; y < h && y < height; y++ {
		base := sof1
		line := y
		if interlace {
			line = y / 2
			if y%2 == 1 {
				base = sof2
			}
		}
		for x := 0; x < w && x < presW; x++ {
			sx := x
			if scale {
				sx = x * 2
			}
			out[y*w+x] = p.readDisplayPixel(base, uint32(line*strideWords*4), sx, format)
		}
	}
}

// readDisplayPixel converts one framebuffer pixel at a byte
// line offset plus a pixel index to XRGB8888, with the top
// byte always FF.
func (p *PVR) readDisplayPixel(base, lineOff uint32, x, format int) uint32 {
	switch format {
	case Fmt0565:
		v := uint16(p.vramWord16(base + lineOff + uint32(x*2)))
		return 0xFF000000 | uint32(expand5(int((v>>11)&0x1F)))<<16 |
			uint32(expand6(int((v>>5)&0x3F)))<<8 | uint32(expand5(int(v&0x1F)))
	case Fmt0888:
		off := base + lineOff + uint32(x*3)
		b := uint32(p.vramByte(off))
		g := uint32(p.vramByte(off + 1))
		r := uint32(p.vramByte(off + 2))
		return 0xFF000000 | r<<16 | g<<8 | b
	case Fmt8888:
		word := p.vramWord(base + lineOff + uint32(x*4))
		return 0xFF000000 | (word & 0xFFFFFF)
	}
	// Fmt0555.
	v := uint16(p.vramWord16(base + lineOff + uint32(x*2)))
	return 0xFF000000 | uint32(expand5(int((v>>10)&0x1F)))<<16 |
		uint32(expand5(int((v>>5)&0x1F)))<<8 | uint32(expand5(int(v&0x1F)))
}

// expand5 replicates a 5-bit channel into 8 bits.
func expand5(v int) int { return (v << 3) | (v >> 2) }

// expand6 replicates a 6-bit channel into 8 bits.
func expand6(v int) int { return (v << 2) | (v >> 4) }
