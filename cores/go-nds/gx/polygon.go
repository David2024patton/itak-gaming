package gx

import "math"

// The vertex pipeline: COLOR, NORMAL, TEXCOORD and the VTX
// command forms feed the primitive assembler, which appends
// transformed vertices to vertex RAM and completed polygons
// to polygon RAM (the build half of the double buffer).
// Lighting is evaluated at NORMAL time per GBATEK's
// equations, in float64 with components in the 0..1 range
// (a stated numeric choice; results are rounded to 5-bit
// and expanded to the engine's 6-bit color domain like the
// COLOR command).

// vertex is one vertex-RAM entry: the clip coordinates
// computed at VTX time, the 6-bit vertex color, and the
// texcoord in the raw 4-fraction domain.
type vertex struct {
	x, y, z, w int32
	r, g, b    uint8
	s, t       int32
}

// polygon is one polygon-RAM entry: up to four vertex
// indices with the attribute, texture parameter, and
// palette base latched at BEGIN_VTXS.
type polygon struct {
	idx  [4]int32
	n    int
	attr uint32
	tex  uint32
	pltt uint32
}

// listBuf is one half of the double-buffered polygon and
// vertex RAM.
type listBuf struct {
	verts []vertex
	polys []polygon
}

// expandGX expands a 5-bit color channel to the geometry
// engine's 6-bit domain: 0 stays 0, anything else becomes
// 2*x+1 (GBATEK's texture-blending chapter expansion).
func expandGX(x uint32) uint32 {
	if x == 0 {
		return 0
	}
	return 2*x + 1
}

// setVertexColor sets the current vertex color from an
// RGB15 value, expanded to 6-bit channels.
func (g *GX) setVertexColor(rgb15 uint32) {
	g.curR = uint8(expandGX(rgb15 & 31))
	g.curG = uint8(expandGX((rgb15 >> 5) & 31))
	g.curB = uint8(expandGX((rgb15 >> 10) & 31))
}

// cmdColor implements COLOR.
func (g *GX) cmdColor(p uint32) { g.setVertexColor(p & 0x7FFF) }

// cmdTexcoord implements TEXCOORD, including the mode-1
// (texcoord source) texture transform, computed here with
// the documented (S, T, 1/16, 1/16) input vector against the
// first two columns of the texture matrix.
func (g *GX) cmdTexcoord(p uint32) {
	s := int32(int16(p))
	t := int32(int16(p >> 16))
	if g.texParam>>30 == 1 {
		m := g.curTex
		ns := (int64(s)*int64(m[0]) + int64(t)*int64(m[4]) + int64(m[8]) + int64(m[12])) >> 12
		nt := (int64(s)*int64(m[1]) + int64(t)*int64(m[5]) + int64(m[9]) + int64(m[13])) >> 12
		s, t = int32(int16(ns)), int32(int16(nt))
	}
	g.curS, g.curT = s, t
}

// cmdNormal implements NORMAL: the normal is transformed by
// the vector matrix, the vertex color is recomputed by the
// lighting model, and in texture transform mode 2 the
// texcoord is recomputed from the transformed normal.
func (g *GX) cmdNormal(p uint32) {
	g.normalAttr = g.pendAttr
	nx, ny, nz := xformDir(g.curVec, sext10(p), sext10(p>>10), sext10(p>>20))
	if g.texParam>>30 == 2 {
		m := g.curTex
		// Inputs in the 21-fraction domain: normal terms are
		// 9-frac by 12-frac; the last-row terms use the
		// current texcoord (4-frac by 12-frac, shifted up).
		s := (int64(nx)*int64(m[0]) + int64(ny)*int64(m[4]) + int64(nz)*int64(m[8]) +
			(int64(g.curS)*int64(m[12]))<<5) >> 17
		t := (int64(nx)*int64(m[1]) + int64(ny)*int64(m[5]) + int64(nz)*int64(m[9]) +
			(int64(g.curT)*int64(m[13]))<<5) >> 17
		g.curS, g.curT = int32(int16(s)), int32(int16(t))
	}
	g.computeLighting(nx, ny, nz)
}

// computeLighting evaluates the documented per-vertex
// lighting: the color starts at the emission, and each
// enabled light adds specular, diffuse, and ambient terms.
// Light vectors and half vectors were transformed when
// LIGHT_VECTOR executed; the half vector uses the fixed
// line of sight (0, 0, -1), which the hardware never rotates
// by the projection matrix.
func (g *GX) computeLighting(nx, ny, nz int32) {
	n := [3]float64{float64(nx) / 512, float64(ny) / 512, float64(nz) / 512}
	mat5 := func(rgb uint32) [3]float64 {
		return [3]float64{
			float64(rgb&31) / 31,
			float64((rgb>>5)&31) / 31,
			float64((rgb>>10)&31) / 31,
		}
	}
	dif := mat5(g.diffuse)
	amb := mat5(g.ambient)
	spec := mat5(g.specular)
	col := mat5(g.emission)
	for i := 0; i < 4; i++ {
		if g.normalAttr&(1<<uint(i)) == 0 {
			continue
		}
		lc := mat5(g.lightCol[i])
		lv := g.lightVec[i]
		hv := g.lightHalf[i]
		d := -(float64(lv[0])*n[0] + float64(lv[1])*n[1] + float64(lv[2])*n[2]) / 512
		if d < 0 {
			d = 0
		}
		s := -(float64(hv[0])*n[0] + float64(hv[1])*n[1] + float64(hv[2])*n[2]) / 512
		if s < 0 {
			s = 0
		}
		s *= s
		if g.shineOn {
			idx := int(s * 128)
			if idx > 127 {
				idx = 127
			}
			s = float64(g.shineTbl[idx]) / 255
		}
		for c := 0; c < 3; c++ {
			col[c] += spec[c]*lc[c]*s + dif[c]*lc[c]*d + amb[c]*lc[c]
		}
	}
	g.curR = uint8(expandGX(round31(col[0])))
	g.curG = uint8(expandGX(round31(col[1])))
	g.curB = uint8(expandGX(round31(col[2])))
}

// round31 clamps a 0..1 float to a rounded 5-bit value.
func round31(f float64) uint32 {
	if f <= 0 {
		return 0
	}
	if f >= 1 {
		return 31
	}
	return uint32(math.Floor(f*31 + 0.5))
}

// cmdLightVector implements LIGHT_VECTOR: the packed
// direction (10-bit fields, 9 fractional bits) is transformed
// by the current vector matrix when the command executes;
// the half vector against the fixed line of sight (0, 0, -1)
// is derived from the transformed vector.
func (g *GX) cmdLightVector(p uint32) {
	i := (p >> 30) & 3
	x, y, z := xformDir(g.curVec, sext10(p), sext10(p>>10), sext10(p>>20))
	g.lightVec[i] = [3]int32{x, y, z}
	g.lightHalf[i] = [3]int32{x / 2, y / 2, (z - 512) / 2}
}

// cmdVertex implements the VTX command forms. Coordinates
// enter in the 12-fraction domain: VTX_16 fields are used as
// read, VTX_10 fields (6 fractional bits) shift left by 6,
// VTX_DIFF deltas (9 fractional bits) shift left by 3 and add
// to the previous vertex.
func (g *GX) cmdVertex(cmd uint8, p []uint32) {
	var x, y, z int32
	switch cmd {
	case 0x23: // VTX_16
		x = int32(int16(p[0]))
		y = int32(int16(p[0] >> 16))
		z = int32(int16(p[1]))
	case 0x24: // VTX_10
		x = sext10(p[0]) << 6
		y = sext10(p[0]>>10) << 6
		z = sext10(p[0]>>20) << 6
	case 0x25: // VTX_XY
		x = int32(int16(p[0]))
		y = int32(int16(p[0] >> 16))
		z = g.lastZ
	case 0x26: // VTX_XZ
		x = int32(int16(p[0]))
		z = int32(int16(p[0] >> 16))
		y = g.lastY
	case 0x27: // VTX_YZ
		y = int32(int16(p[0]))
		z = int32(int16(p[0] >> 16))
		x = g.lastX
	case 0x28: // VTX_DIFF
		x = g.lastX + sext10(p[0])<<3
		y = g.lastY + sext10(p[0]>>10)<<3
		z = g.lastZ + sext10(p[0]>>20)<<3
	}
	g.lastX, g.lastY, g.lastZ = x, y, z
	s, t := g.curS, g.curT
	if g.texParam>>30 == 3 {
		// Vertex-source texture transform: the input vector
		// is the vertex position with the last row replaced
		// by the current texcoord. Terms are computed in
		// the 24-fraction domain.
		m := g.curTex
		ns := (int64(x)*int64(m[0]) + int64(y)*int64(m[4]) + int64(z)*int64(m[8]) +
			(int64(g.curS)*int64(m[12]))<<8) >> 20
		nt := (int64(x)*int64(m[1]) + int64(y)*int64(m[5]) + int64(z)*int64(m[9]) +
			(int64(g.curT)*int64(m[13]))<<8) >> 20
		s, t = int32(int16(ns)), int32(int16(nt))
	}
	cx, cy, cz, cw := xform(g.clipMatrix(), x, y, z, 0x1000)
	v := vertex{x: cx, y: cy, z: cz, w: cw,
		r: g.curR, g: g.curG, b: g.curB, s: s, t: t}
	g.pushVertex(v)
}

// beginVtxs implements BEGIN_VTXS: the pending polygon
// attribute, texture parameter, and palette base latch for
// the new primitive.
func (g *GX) beginVtxs(param uint32) {
	g.beginAttr = g.pendAttr
	g.primType = int(param & 3)
	g.primCount = 0
	g.primOpen = true
}

// pushVertex appends one transformed vertex to vertex RAM
// and feeds the primitive assembler. Vertices beyond the RAM
// capacity are dropped and set the overflow flag.
func (g *GX) pushVertex(v vertex) {
	if len(g.build.verts) >= MaxVerts {
		g.setOverflow()
		return
	}
	idx := int32(len(g.build.verts))
	g.build.verts = append(g.build.verts, v)
	if g.primOpen {
		g.assemble(idx)
	}
}

// assemble folds one new vertex index into the current
// primitive, emitting a polygon whenever a group completes.
// Strips reuse the trailing vertices of the previous group;
// in a triangle strip the winding alternates so all
// triangles keep the same facing.
func (g *GX) assemble(idx int32) {
	g.primCount++
	verts := g.build.verts
	recent := func(k int) int32 {
		return int32(len(verts)) - 1 - int32(k)
	}
	emit := func(idxs ...int32) {
		if len(g.build.polys) >= MaxPolys {
			g.setOverflow()
			return
		}
		var pl polygon
		pl.n = len(idxs)
		copy(pl.idx[:], idxs)
		pl.attr = g.beginAttr
		pl.tex = g.texParam
		pl.pltt = g.plttBase
		g.build.polys = append(g.build.polys, pl)
	}
	switch g.primType {
	case 0: // separate triangles
		if g.primCount%3 == 0 {
			emit(recent(2), recent(1), recent(0))
		}
	case 1: // separate quads
		if g.primCount%4 == 0 {
			emit(recent(3), recent(2), recent(1), recent(0))
		}
	case 2: // triangle strip
		if g.primCount >= 3 {
			if g.primCount%2 == 1 {
				emit(recent(2), recent(1), recent(0))
			} else {
				emit(recent(1), recent(2), recent(0))
			}
		}
	case 3: // quad strip: pairs of vertices per quad
		if g.primCount >= 4 && g.primCount%2 == 0 {
			emit(recent(3), recent(2), recent(1), recent(0))
		}
	}
}
