package gpu

import (
	"encoding/binary"
	"math"
)

// Vertex description (VCD) layout. The low word carries, in
// order: the position matrix index presence bit (bit 0), the
// eight texture matrix index presence bits (bits 1-8), then
// two-bit attribute modes for position (bits 9-10), normal
// (11-12), color 0 (13-14) and color 1 (15-16). The high
// word carries the two-bit modes for texcoords 0-7 (bits
// 0-15). Modes: 0 none, 1 direct, 2 index8, 3 index16.
//
// Vertex attribute table (VAT), three words per format:
//   g0: position elements bit 0 (0 = 2 comps, 1 = 3), position
//       format bits 1-3, position frac bits 4-8, normal
//       elements bit 9 (0 = one normal, 1 = NBT triplet),
//       normal format bits 10-12, color 0 elements bit 13
//       (0 = RGB, 1 = RGBA), color 0 format bits 14-16,
//       color 1 elements bit 17, color 1 format bits 18-20,
//       texcoord 0 elements bit 21, format bits 22-24,
//       frac bits 25-29, byte dequantize bit 30, normal
//       three-index bit 31.
//   g1: texcoords 1-3 in the same 9-bit pattern starting at
//       bits 0, 9, 18, and texcoord 4 elements at bit 27 with
//       its format at bits 28-30 and the low frac bit at 31.
//   g2: texcoord 4 frac high bits at 0-3, texcoords 5-7 in
//       the 9-bit pattern starting at bits 4, 13, 22.
//
// Component formats: 0 u8, 1 s8, 2 u16, 3 s16, 4 f32.
// Color formats: 0 RGB565 (2 bytes), 1 RGB8 (3), 2 RGBX8 (4),
// 3 RGBA4 (2), 4 RGBA6 (3), 5 RGBA8 (4).
//
// Attribute array indices used for indexed fetches:
// position 0, normal 1, color 0 = 2, color 1 = 3,
// texcoord n = 4+n.

const (
	attrNone   = 0
	attrDirect = 1
	attrIndex8 = 2
	attrIndex  = 3 // index16
)

func compSize(format uint32) int {
	switch format {
	case 0, 1:
		return 1
	case 2, 3:
		return 2
	default:
		return 4
	}
}

func colorSize(format uint32) int {
	return [6]int{2, 3, 4, 2, 3, 4}[format%6]
}

type vatInfo struct {
	posElem   int // 2 or 3
	posFormat uint32
	posFrac   uint32
	nrmElem   int // 3 or 9 components stored
	nrmFormat uint32
	nrmIdx3   bool
	colElem   [2]bool // true = RGBA present
	colFormat [2]uint32
	texElem   [8]int // 1 or 2
	texFormat [8]uint32
	texFrac   [8]uint32
}

func (g *GPU) vatInfo(vatIdx int) vatInfo {
	v := g.vat[vatIdx&7]
	var vi vatInfo
	vi.posElem = 2 + int(v[0]&1)
	vi.posFormat = (v[0] >> 1) & 7
	vi.posFrac = (v[0] >> 4) & 0x1F
	vi.nrmElem = 3
	if v[0]&(1<<9) != 0 {
		vi.nrmElem = 9
	}
	vi.nrmFormat = (v[0] >> 10) & 7
	vi.colElem[0] = v[0]&(1<<13) != 0
	vi.colFormat[0] = (v[0] >> 14) & 7
	vi.colElem[1] = v[0]&(1<<17) != 0
	vi.colFormat[1] = (v[0] >> 18) & 7
	vi.nrmIdx3 = v[0]&(1<<31) != 0
	// Texcoord 0 is in g0, 1-4 straddle g1 and g2, 5-7 in g2.
	tex := func(i int) (int, uint32, uint32) {
		switch {
		case i == 0:
			return 1 + int((v[0]>>21)&1), (v[0] >> 22) & 7, (v[0] >> 25) & 0x1F
		case i >= 1 && i <= 3:
			s := uint(9 * (i - 1))
			return 1 + int((v[1]>>s)&1), (v[1] >> (s + 1)) & 7, (v[1] >> (s + 4)) & 0x1F
		case i == 4:
			frac := ((v[1] >> 31) & 1) | ((v[2] & 0xF) << 1)
			return 1 + int((v[1]>>27)&1), (v[1] >> 28) & 7, frac
		default:
			s := uint(9*(i-5) + 4)
			return 1 + int((v[2]>>s)&1), (v[2] >> (s + 1)) & 7, (v[2] >> (s + 4)) & 0x1F
		}
	}
	for i := 0; i < 8; i++ {
		vi.texElem[i], vi.texFormat[i], vi.texFrac[i] = tex(i)
	}
	return vi
}

// vcdMode returns the two-bit mode for attribute slot a of
// the VCD: 0 pos, 1 normal, 2 color0, 3 color1, 4+i tex i.
func (g *GPU) vcdMode(a int) uint32 {
	if a < 4 {
		return (g.vcdLo >> uint(9+2*a)) & 3
	}
	return (g.vcdHi >> uint(2*(a-4))) & 3
}

func modeStreamSize(mode uint32, directBytes int) int {
	switch mode {
	case attrDirect:
		return directBytes
	case attrIndex8:
		return 1
	case attrIndex:
		return 2
	}
	return 0
}

// vertexStreamSize is the number of FIFO bytes one vertex
// occupies under the current VCD and the given VAT.
func (g *GPU) vertexStreamSize(vatIdx int) int {
	vi := g.vatInfo(vatIdx)
	n := 0
	if g.vcdLo&1 != 0 {
		n++ // position matrix index byte
	}
	for i := 0; i < 8; i++ {
		if g.vcdLo&(1<<uint(1+i)) != 0 {
			n++ // texture matrix index byte
		}
	}
	n += modeStreamSize(g.vcdMode(0), compSize(vi.posFormat)*vi.posElem)
	nrmMode := g.vcdMode(1)
	if nrmMode == attrDirect {
		n += compSize(vi.nrmFormat) * vi.nrmElem
	} else if nrmMode != attrNone {
		if vi.nrmIdx3 {
			n += modeStreamSize(nrmMode, 0) * 3
		} else {
			n += modeStreamSize(nrmMode, 0)
		}
	}
	for c := 0; c < 2; c++ {
		n += modeStreamSize(g.vcdMode(2+c), colorSize(vi.colFormat[c]))
	}
	for i := 0; i < 8; i++ {
		n += modeStreamSize(g.vcdMode(4+i), compSize(vi.texFormat[i])*vi.texElem[i])
	}
	return n
}

// cursor walks one vertex record in the FIFO stream.
type cursor struct {
	b []byte
	p int
}

func (c *cursor) u8() uint32 {
	if c.p >= len(c.b) {
		return 0
	}
	v := uint32(c.b[c.p])
	c.p++
	return v
}

func (c *cursor) u16() uint32 {
	hi := c.u8()
	lo := c.u8()
	return hi<<8 | lo
}

func (c *cursor) bytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(c.u8())
	}
	return out
}

// scaleComp converts one raw component to a float. Integer
// formats are scaled by 2^-frac as documented; f32 ignores
// the fraction. Normals use the fixed documented scales
// instead: 2^-6 for byte formats and 2^-14 for short
// formats, with the raw bits read as signed values.
func scaleComp(raw uint32, format uint32, frac uint32, normal bool) float64 {
	if format == 4 {
		return float64(math.Float32frombits(raw))
	}
	if normal {
		switch format {
		case 0, 1:
			return float64(int8(uint8(raw))) / 64
		default:
			return float64(int16(uint16(raw))) / 16384
		}
	}
	var v float64
	switch format {
	case 0:
		v = float64(uint8(raw))
	case 1:
		v = float64(int8(uint8(raw)))
	case 2:
		v = float64(uint16(raw))
	case 3:
		v = float64(int16(uint16(raw)))
	}
	if frac > 0 {
		v *= math.Ldexp(1, -int(frac))
	}
	return v
}

func (c *cursor) comp(format uint32) uint32 {
	switch compSize(format) {
	case 1:
		return c.u8()
	case 2:
		return c.u16()
	default:
		hi := c.u16()
		lo := c.u16()
		return hi<<16 | lo
	}
}

func (g *GPU) memComp(addr uint32, format uint32) uint32 {
	if g.mem == nil {
		return 0
	}
	switch compSize(format) {
	case 1:
		return uint32(g.mem.Read8(addr))
	case 2:
		return uint32(g.mem.Read16(addr))
	default:
		return g.mem.Read32(addr)
	}
}

// fetchNumeric reads count components of one attribute,
// either direct from the stream or indexed from memory.
// arrayIdx is the CP array used for indexed fetches.
func (g *GPU) fetchNumeric(cur *cursor, mode uint32, arrayIdx int, count int, format uint32, frac uint32, normal bool) []float64 {
	out := make([]float64, count)
	switch mode {
	case attrDirect:
		for i := range out {
			out[i] = scaleComp(cur.comp(format), format, frac, normal)
		}
	case attrIndex8, attrIndex:
		var idx uint32
		if mode == attrIndex8 {
			idx = cur.u8()
		} else {
			idx = cur.u16()
		}
		base := g.arrayBase[arrayIdx] + idx*g.arrayStride[arrayIdx]
		cs := uint32(compSize(format))
		for i := range out {
			out[i] = scaleComp(g.memComp(base+uint32(i)*cs, format), format, frac, normal)
		}
	}
	return out
}

// fetchNormal handles the three normal storage forms: a
// single direct or indexed normal, an indexed NBT triplet
// reached through one index, and the three-index form where
// the normal, binormal and tangent each carry their own
// index. Only the first normal is returned; the binormal
// and tangent are fetched (and consumed) but lighting in
// this slice does not use them.
func (g *GPU) fetchNormal(cur *cursor, vi vatInfo) (n [3]float64, ok bool) {
	mode := g.vcdMode(1)
	if mode == attrNone {
		return n, false
	}
	if mode == attrDirect {
		vals := g.fetchNumeric(cur, mode, 1, vi.nrmElem, vi.nrmFormat, 0, true)
		copy(n[:], vals[:min(3, len(vals))])
		return n, true
	}
	if vi.nrmIdx3 {
		for part := 0; part < 3; part++ {
			var idx uint32
			if mode == attrIndex8 {
				idx = cur.u8()
			} else {
				idx = cur.u16()
			}
			base := g.arrayBase[1] + idx*g.arrayStride[1]
			cs := uint32(compSize(vi.nrmFormat))
			for i := 0; i < 3; i++ {
				val := scaleComp(g.memComp(base+uint32(i)*cs, vi.nrmFormat), vi.nrmFormat, 0, true)
				if part == 0 {
					n[i] = val
				}
			}
		}
		return n, true
	}
	vals := g.fetchNumeric(cur, mode, 1, vi.nrmElem, vi.nrmFormat, 0, true)
	copy(n[:], vals[:min(3, len(vals))])
	return n, true
}

// decodeColor converts raw stored color bytes in one of the
// six documented color formats to 8-bit channels. Formats
// without alpha, and RGB formats of the alpha formats,
// produce alpha 255.
func decodeColor(format uint32, hasAlpha bool, b []byte) color8 {
	u16 := func() uint32 {
		if len(b) < 2 {
			return 0
		}
		return uint32(b[0])<<8 | uint32(b[1])
	}
	switch format % 6 {
	case 0: // RGB565
		w := u16()
		return color8{expand5(uint8(w >> 11)), expand6(uint8((w >> 5) & 0x3F)), expand5(uint8(w & 0x1F)), 255}
	case 1: // RGB8
		if len(b) < 3 {
			return color8{}
		}
		return color8{b[0], b[1], b[2], 255}
	case 2: // RGBX8
		if len(b) < 4 {
			return color8{}
		}
		return color8{b[0], b[1], b[2], 255}
	case 3: // RGBA4
		w := u16()
		a := uint8(255)
		if hasAlpha {
			a = expand4(uint8(w & 0xF))
		}
		return color8{expand4(uint8(w >> 12)), expand4(uint8((w >> 8) & 0xF)), expand4(uint8((w >> 4) & 0xF)), a}
	case 4: // RGBA6
		if len(b) < 3 {
			return color8{}
		}
		w := uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
		a := uint8(255)
		if hasAlpha {
			a = expand6(uint8(w & 0x3F))
		}
		return color8{expand6(uint8(w >> 18)), expand6(uint8((w >> 12) & 0x3F)), expand6(uint8((w >> 6) & 0x3F)), a}
	default: // RGBA8
		if len(b) < 4 {
			return color8{}
		}
		a := uint8(255)
		if hasAlpha {
			a = b[3]
		}
		return color8{b[0], b[1], b[2], a}
	}
}

func (g *GPU) fetchColor(cur *cursor, mode uint32, arrayIdx int, vi vatInfo, ch int) (color8, bool) {
	if mode == attrNone {
		return color8{}, false
	}
	format := vi.colFormat[ch]
	n := colorSize(format)
	var raw []byte
	switch mode {
	case attrDirect:
		raw = cur.bytes(n)
	case attrIndex8, attrIndex:
		var idx uint32
		if mode == attrIndex8 {
			idx = cur.u8()
		} else {
			idx = cur.u16()
		}
		base := g.arrayBase[arrayIdx] + idx*g.arrayStride[arrayIdx]
		raw = make([]byte, n)
		if g.mem != nil {
			for i := range raw {
				raw[i] = g.mem.Read8(base + uint32(i))
			}
		}
	}
	return decodeColor(format, vi.colElem[ch], raw), true
}

// rawVertex is one fetched, untransformed vertex.
type rawVertex struct {
	pos       [3]float64
	normal    [3]float64
	hasNormal bool
	col       [2]color8
	hasCol    [2]bool
	tex       [8][2]float64
	hasTex    [8]bool
	matIdx    uint32
	texMatIdx [8]uint32
}

// defMatIdx returns a default matrix index from the packed
// matrix index registers: A packs position at bits 0-5 and
// texcoords 0-2 at bits 8, 16, 24; B packs texcoords 3-6 at
// bits 0, 8, 16, 24. Texcoord 7 defaults to 0.
func (g *GPU) defMatIdx(which int) uint32 {
	if which == 0 {
		return g.matIndA & 0x3F
	}
	i := which - 1 // texcoord index
	if i < 3 {
		return (g.matIndA >> uint(8+8*i)) & 0x3F
	}
	if i < 7 {
		return (g.matIndB >> uint(8*(i-3))) & 0x3F
	}
	return 0
}

func (g *GPU) fetchVertex(rec []byte, vi vatInfo) rawVertex {
	cur := &cursor{b: rec}
	var v rawVertex
	v.matIdx = g.defMatIdx(0)
	for i := 0; i < 8; i++ {
		v.texMatIdx[i] = g.defMatIdx(1 + i)
	}
	if g.vcdLo&1 != 0 {
		v.matIdx = cur.u8() & 0x3F
	}
	for i := 0; i < 8; i++ {
		if g.vcdLo&(1<<uint(1+i)) != 0 {
			v.texMatIdx[i] = cur.u8() & 0x3F
		}
	}
	pos := g.fetchNumeric(cur, g.vcdMode(0), 0, vi.posElem, vi.posFormat, vi.posFrac, false)
	copy(v.pos[:], pos)
	if vi.posElem == 2 {
		v.pos[2] = 0
	}
	v.normal, v.hasNormal = g.fetchNormal(cur, vi)
	for c := 0; c < 2; c++ {
		v.col[c], v.hasCol[c] = g.fetchColor(cur, g.vcdMode(2+c), 2+c, vi, c)
	}
	for i := 0; i < 8; i++ {
		mode := g.vcdMode(4 + i)
		if mode == attrNone {
			continue
		}
		vals := g.fetchNumeric(cur, mode, 4+i, vi.texElem[i], vi.texFormat[i], vi.texFrac[i], false)
		v.tex[i][0] = vals[0]
		if vi.texElem[i] == 2 {
			v.tex[i][1] = vals[1]
		}
		v.hasTex[i] = true
	}
	return v
}

// pipeVertex is a vertex after the transform stage, in clip
// space with its lit color and stage 0 texcoord.
type pipeVertex struct {
	clip        [4]float64
	r, gg, b, a float64 // 0-1
	s, t        float64
	normal      [3]float64 // view space, for reference
}

// modelviewMatrix returns the 3x4 modelview matrix selected
// by a position matrix index. Slots are 12 consecutive
// transform words (row major, 3 rows of 4); the fourth row
// is the implicit (0, 0, 0, 1).
func (g *GPU) modelviewMatrix(idx uint32) [12]float64 {
	var m [12]float64
	base := (idx & 0x3F) * 12
	for i := 0; i < 12; i++ {
		m[i] = float64(math.Float32frombits(g.xfMem[(base+uint32(i))&0x3FF]))
	}
	return m
}

// projectionMatrix builds the documented 4x4 projection
// from the six projection registers A-F and the type word:
// perspective when the type is 0, orthographic when 1.
//
// Perspective:                Orthographic:
//
//	A 0 C 0                     A 0 0 C
//	0 B D 0                     0 B 0 D
//	0 0 E F                     0 0 E F
//	0 0 -1 0                    0 0 0 1
func (g *GPU) projectionMatrix() [16]float64 {
	a := g.xfRegF(xfProjection + 0)
	b := g.xfRegF(xfProjection + 1)
	c := g.xfRegF(xfProjection + 2)
	d := g.xfRegF(xfProjection + 3)
	e := g.xfRegF(xfProjection + 4)
	f := g.xfRegF(xfProjection + 5)
	typ := g.xfReg(xfProjection + 6)
	var m [16]float64
	m[0] = a
	m[5] = b
	m[10] = e
	m[11] = f
	if typ == 1 {
		m[3] = c
		m[7] = d
		m[15] = 1
	} else {
		m[2] = c
		m[6] = d
		m[14] = -1
	}
	return m
}

func chanCtrlFields(v uint32) (matSrc, enable bool, lightMask uint32, ambSrc bool) {
	matSrc = v&1 != 0
	enable = v&(1<<1) != 0
	lightMask = ((v >> 2) & 0xF) | (((v >> 12) & 0xF) << 4)
	ambSrc = v&(1<<6) != 0
	return
}

func regColor(v uint32) color8 {
	// XF color registers pack RGBA with red in the top byte,
	// matching the big-endian FIFO word order.
	return color8{uint8(v >> 24), uint8(v >> 16), uint8(v >> 8), uint8(v)}
}

// shade computes the fragment source color for one vertex.
//
// Lighting scope (a deliberate simplification, per the
// slice plan): when color channel 0 is configured for
// lighting (its XF channel control enable bit set), light 0
// is in the channel light mask, and the vertex carries a
// normal, the color is
//
//	clamp(ambient + material * diffuse)
//
// per channel, where diffuse = max(0, dot(n, lightDir)), n
// is the normal transformed by the modelview upper 3x3 and
// normalized, and lightDir is the normalized light 0
// position read from its transform memory block: the stored
// light position is treated as a directional vector, which
// is the documented encoding for a light at infinity. The
// material and ambient terms come from the channel material
// and ambient registers, or from the vertex color itself
// when the control register selects the vertex as the
// material or ambient source. Specular, attenuation, and
// lights 1-7 are not evaluated.
//
// In every other configuration the raw vertex color is
// used, or the channel material color when the vertex has
// no color attribute.
func (g *GPU) shade(v rawVertex, mv [12]float64) (r, gg, b, a float64) {
	base := color8{0, 0, 0, 255}
	ch := 0
	if v.hasCol[0] {
		base = v.col[0]
	} else if v.hasCol[1] {
		base = v.col[1]
		ch = 1
	} else {
		base = regColor(g.xfReg(xfChan0Mat))
	}
	ctrl := g.xfReg(xfChan0Color + uint32(ch))
	actrl := g.xfReg(xfChan0Alpha + uint32(ch))
	matSrc, enable, lightMask, ambSrc := chanCtrlFields(ctrl)
	_, aEnable, aLightMask, aAmbSrc := chanCtrlFields(actrl)
	if v.hasNormal && lightMask&1 != 0 && (enable || (aEnable && aLightMask&1 != 0)) {
		// View-space normal from the modelview upper 3x3.
		n := [3]float64{
			mv[0]*v.normal[0] + mv[1]*v.normal[1] + mv[2]*v.normal[2],
			mv[4]*v.normal[0] + mv[5]*v.normal[1] + mv[6]*v.normal[2],
			mv[8]*v.normal[0] + mv[9]*v.normal[1] + mv[10]*v.normal[2],
		}
		n = normalize3(n)
		// Light 0 block: color at word 3, position at words
		// 10-12 of the 16-word block at xfLights + 0.
		lbase := uint32(xfLights)
		lp := [3]float64{
			float64(math.Float32frombits(g.xfWord(lbase + 10))),
			float64(math.Float32frombits(g.xfWord(lbase + 11))),
			float64(math.Float32frombits(g.xfWord(lbase + 12))),
		}
		if lp == [3]float64{} {
			lp = [3]float64{
				float64(math.Float32frombits(g.xfWord(lbase + 13))),
				float64(math.Float32frombits(g.xfWord(lbase + 14))),
				float64(math.Float32frombits(g.xfWord(lbase + 15))),
			}
		}
		ld := normalize3(lp)
		diff := n[0]*ld[0] + n[1]*ld[1] + n[2]*ld[2]
		if diff < 0 {
			diff = 0
		}
		amb := regColor(g.xfReg(xfChan0Amb + uint32(ch)))
		mat := regColor(g.xfReg(xfChan0Mat + uint32(ch)))
		if ambSrc {
			amb = base
		}
		if matSrc {
			mat = base
		}
		calc := func(ambC, matC uint8) float64 {
			out := float64(ambC)/255 + float64(matC)/255*diff
			if out > 1 {
				out = 1
			}
			return out
		}
		if enable && lightMask&1 != 0 {
			r = calc(amb.r, mat.r)
			gg = calc(amb.g, mat.g)
			b = calc(amb.b, mat.b)
		} else {
			r, gg, b = float64(base.r)/255, float64(base.g)/255, float64(base.b)/255
		}
		aAmb, aMat := amb, mat
		if aAmbSrc {
			aAmb = base
		}
		if g.xfReg(xfChan0Alpha+uint32(ch))&1 != 0 {
			aMat = base
		}
		if aEnable && aLightMask&1 != 0 {
			a = calc(aAmb.a, aMat.a)
		} else {
			a = float64(base.a) / 255
		}
		return r, gg, b, a
	}
	return float64(base.r) / 255, float64(base.g) / 255,
		float64(base.b) / 255, float64(base.a) / 255
}

func normalize3(v [3]float64) [3]float64 {
	l := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if l == 0 {
		return v
	}
	return [3]float64{v[0] / l, v[1] / l, v[2] / l}
}

// transform runs one raw vertex through the modelview and
// projection matrices and the stage 0 texgen.
func (g *GPU) transform(v rawVertex) pipeVertex {
	mv := g.modelviewMatrix(v.matIdx)
	vx := mv[0]*v.pos[0] + mv[1]*v.pos[1] + mv[2]*v.pos[2] + mv[3]
	vy := mv[4]*v.pos[0] + mv[5]*v.pos[1] + mv[6]*v.pos[2] + mv[7]
	vz := mv[8]*v.pos[0] + mv[9]*v.pos[1] + mv[10]*v.pos[2] + mv[11]
	p := g.projectionMatrix()
	var pv pipeVertex
	pv.clip[0] = p[0]*vx + p[1]*vy + p[2]*vz + p[3]
	pv.clip[1] = p[4]*vx + p[5]*vy + p[6]*vz + p[7]
	pv.clip[2] = p[8]*vx + p[9]*vy + p[10]*vz + p[11]
	pv.clip[3] = p[12]*vx + p[13]*vy + p[14]*vz + p[15]
	pv.r, pv.gg, pv.b, pv.a = g.shade(v, mv)
	if v.hasTex[0] {
		pv.s, pv.t = v.tex[0][0], v.tex[0][1]
	}
	// Stage 0 texture post matrix: when the dual texture
	// register enables the post matrix for stage 0, the
	// (s, t, 1) vector is multiplied by the post matrix
	// selected in the stage 0 post matrix info register.
	if g.xfReg(xfDualTex)&1 != 0 {
		idx := g.xfReg(xfPostMtxInfo) & 0x3F
		base := (uint32(0x500) + idx*12) & 0x3FF
		var pm [12]float64
		for i := 0; i < 12; i++ {
			pm[i] = float64(math.Float32frombits(g.xfMem[(base+uint32(i))&0x3FF]))
		}
		s := pm[0]*pv.s + pm[1]*pv.t + pm[3]
		t := pm[4]*pv.s + pm[5]*pv.t + pm[7]
		pv.s, pv.t = s, t
	}
	pv.normal = v.normal
	return pv
}

// Draw command opcodes: the top bit is set, bits 3-5 select
// the primitive, and the low three bits select the VAT.
//
//	0x80 quads, 0x90 triangles, 0x98 triangle strip,
//	0xA0 triangle fan, 0xA8 lines, 0xB0 line strip,
//	0xB8 points (each plus the VAT index).
const (
	primQuads     = 0x10
	primTriangles = 0x12
	primStrip     = 0x13
	primFan       = 0x14
	primLines     = 0x15
	primLineStrip = 0x16
	primPoints    = 0x17
)

// draw executes one draw command: a header byte, a
// big-endian u16 vertex count, then the vertex records.
func (g *GPU) draw(cmd []byte) {
	vatIdx := int(cmd[0] & 7)
	prim := int(cmd[0]&0xF8) >> 3 // 0x10 quads .. 0x17 points
	count := int(binary.BigEndian.Uint16(cmd[1:]))
	vs := g.vertexStreamSize(vatIdx)
	vi := g.vatInfo(vatIdx)
	verts := make([]pipeVertex, 0, count)
	for i := 0; i < count; i++ {
		off := 3 + i*vs
		if off+vs > len(cmd) {
			break
		}
		verts = append(verts, g.transform(g.fetchVertex(cmd[off:off+vs], vi)))
	}
	switch prim {
	case primQuads:
		// Documented GX quad decomposition: each group of
		// four vertices becomes triangles (0,1,2) and
		// (0,2,3).
		for i := 0; i+3 < len(verts); i += 4 {
			g.tri(verts[i], verts[i+1], verts[i+2])
			g.tri(verts[i], verts[i+2], verts[i+3])
		}
	case primTriangles:
		for i := 0; i+2 < len(verts); i += 3 {
			g.tri(verts[i], verts[i+1], verts[i+2])
		}
	case primStrip:
		for i := 0; i+2 < len(verts); i++ {
			if i%2 == 0 {
				g.tri(verts[i], verts[i+1], verts[i+2])
			} else {
				// Alternating winding correction.
				g.tri(verts[i+1], verts[i], verts[i+2])
			}
		}
	case primFan:
		for i := 1; i+1 < len(verts); i++ {
			g.tri(verts[0], verts[i], verts[i+1])
		}
	case primLines:
		for i := 0; i+1 < len(verts); i += 2 {
			g.line(verts[i], verts[i+1])
		}
	case primLineStrip:
		for i := 0; i+1 < len(verts); i++ {
			g.line(verts[i], verts[i+1])
		}
	case primPoints:
		for _, v := range verts {
			g.point(v)
		}
	}
}
