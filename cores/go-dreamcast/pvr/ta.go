package pvr

import (
	"encoding/binary"
	"math"
	"math/bits"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/holly"
)

// TA (tile accelerator) parameter input. Parameters arrive as
// 32-byte packets at the TA FIFO (64 bytes for two-volume
// vertices and sprite vertices). The first word of a packet is
// the parameter control word (PCW):
//
//	bits 31:29  parameter type: 0 end of list, 1 user tile clip,
//	            2 control, 4 polygon or modifier volume header,
//	            5 sprite header, 7 vertex
//	bit  28     end of strip (vertex parameters)
//	bits 26:24  list type: 0 opaque, 1 opaque modifier, 2
//	            translucent, 3 translucent modifier, 4 punchthrough
//	bit  23     shadow (modifier volume semantics)
//	bit  22     two-volume: the header and its vertices carry a
//	            second ISP/TSP + TSP + TCW parameter set
//	bits 17:16  user clip mode: 0 off, 2 inside, 3 outside
//
// Polygon header packet layout (single volume): PCW, ISP/TSP
// instruction word, TSP word, TCW word, base color (packed
// ARGB8888), offset color, then two unused words. Two-volume
// headers replace the colors: PCW, set 0 (ISP/TSP, TSP, TCW),
// set 1 (ISP/TSP, TSP, TCW), one unused word; colors then live
// in the 64-byte vertices.
//
// Vertex packet layout (32 bytes, the standard textured type):
// PCW, X, Y, Z (floats; Z carries 1/w as on hardware), U, V
// (floats, or 16-bit floats in the low half of each word when
// the ISP/TSP word has UV16 set), base color, offset color.
// Two-volume vertices are 64 bytes: the same first 8 words, then
// U2, V2, base color 2, offset color 2, and four unused words.
//
// Only the packed color format is implemented; the floating
// point and intensity color formats are deferred (listed in the
// README) and such streams are read as packed.
//
// The TA also builds the hardware bookkeeping in VRAM: each
// completed primitive's parameter block (9 words) is stored at
// TA_ISP_BASE (TA_ITP_CURRENT tracks the write position), and an
// object pointer entry per covered tile is appended in the OPB
// area at TA_OL_BASE under the TA_ALLOC_CTRL per-list budgets.
// The renderer consumes the in-core primitive lists, which hold
// exactly the primitives those VRAM structures reference, in the
// same order.
const (
	pcwParaShift = 29
	pcwEOS       = 1 << 28
	pcwListShift = 24
	pcwShadow    = 1 << 23
	pcwVolume    = 1 << 22
	pcwClipShift = 16
)

const (
	paraEnd      = 0
	paraUserClip = 1
	paraControl  = 2
	paraPolygon  = 4
	paraSprite   = 5
	paraVertex   = 7
)

// Display list indices.
const (
	ListOpaque    = 0
	ListOpaqueMod = 1
	ListTrans     = 2
	ListTransMod  = 3
	ListPunch     = 4
)

// ISP/TSP instruction word fields.
const (
	ispZWriteDis = 1 << 3
	ispTextured  = 1 << 6
	ispOffset    = 1 << 7
	ispGouraud   = 1 << 8
	ispUV16      = 1 << 9
)

func ispDepthMode(w uint32) int   { return int(w & 7) }
func ispCullMode(w uint32) int    { return int((w >> 4) & 3) }
func ispFogCtrl(w uint32) int     { return int((w >> 20) & 3) }
func ispColorClamp(w uint32) bool { return w&(1<<22) != 0 }

// TSP word fields.
func tspFilter(w uint32) int      { return int(w & 7) }
func tspMipBias(w uint32) int     { return int((w >> 3) & 7) }
func tspUClamp(w uint32) bool     { return w&(1<<9) != 0 }
func tspVClamp(w uint32) bool     { return w&(1<<10) != 0 }
func tspUFlip(w uint32) bool      { return w&(1<<11) != 0 }
func tspVFlip(w uint32) bool      { return w&(1<<12) != 0 }
func tspStride(w uint32) bool     { return w&(1<<13) != 0 }
func tspLinear(w uint32) bool     { return w&(1<<14) != 0 }
func tspDstBlend(w uint32) int    { return int((w >> 16) & 7) }
func tspSrcBlend(w uint32) int    { return int((w >> 19) & 7) }
func tspIgnoreTexA(w uint32) bool { return w&(1<<22) != 0 }
func tspUseAlpha(w uint32) bool   { return w&(1<<23) != 0 }
func tspUSize(w uint32) int       { return 8 << ((w >> 26) & 7) }
func tspVSize(w uint32) int       { return 8 << ((w >> 29) & 7) }

// TCW word fields. Texture pixel formats:
const (
	Tex1555 = 0
	Tex565  = 1
	Tex4444 = 2
	TexYUV  = 3
	TexBump = 4
	TexPal4 = 5
	TexPal8 = 6
)

func tcwAddr(w uint32) uint32    { return (w & 0x1FFFFF) * 8 }
func tcwPalSelect(w uint32) int  { return int((w >> 21) & 0x3F) }
func tcwPixelFmt(w uint32) int   { return int((w >> 27) & 7) }
func tcwVQ(w uint32) bool        { return w&(1<<30) != 0 }
func tcwMipMapped(w uint32) bool { return w&(1<<31) != 0 }

// Depth compare modes for the ISP/TSP depth field.
const (
	DepthNever   = 0
	DepthLess    = 1
	DepthEqual   = 2
	DepthLEqual  = 3
	DepthGreater = 4
	DepthNotEq   = 5
	DepthGEqual  = 6
	DepthAlways  = 7
)

// Blend functions for the TSP src/dst fields.
const (
	BlendZero         = 0
	BlendOne          = 1
	BlendDstColor     = 2
	BlendOneMinusDst  = 3
	BlendSrcAlpha     = 4
	BlendOneMinusSrcA = 5
	BlendDstAlpha     = 6
	BlendOneMinusDstA = 7
)

// Vertex is one TA vertex. Z carries the reciprocal of the
// projective W, exactly as submitted; colors are packed
// ARGB8888. The 2-suffixed fields are the second parameter set
// of a two-volume primitive.
type Vertex struct {
	X, Y, Z        float32
	U, V           float32
	Base, Offset   uint32
	U2, V2         float32
	Base2, Offset2 uint32
	EOS            bool
}

// Prim is one display-list primitive: a strip of vertices under
// one parameter header, in submission order.
type Prim struct {
	List    int
	Sprite  bool
	PCW     uint32
	ISPTSP  uint32
	TSP     uint32
	TCW     uint32
	ISPTSP2 uint32
	TSP2    uint32
	TCW2    uint32
	// BaseColor and OffsetColor are the header colors, used for
	// flat shading. Two-volume headers leave them zero.
	BaseColor   uint32
	OffsetColor uint32
	Verts       []Vertex
}

// TwoVolume reports whether the primitive carries a second
// parameter set.
func (pr *Prim) TwoVolume() bool { return pr.PCW&pcwVolume != 0 }

// ClipMode returns the PCW user clip mode (0 off, 2 inside,
// 3 outside).
func (pr *Prim) ClipMode() int { return int((pr.PCW >> pcwClipShift) & 3) }

// listDoneEvent maps a list index to its TA transfer-done ASIC
// event bit.
func listDoneEvent(list int) uint32 {
	switch list {
	case ListOpaque:
		return holly.EvtOpaqueDone
	case ListOpaqueMod:
		return holly.EvtOpaqueModDone
	case ListTrans:
		return holly.EvtTransDone
	case ListTransMod:
		return holly.EvtTransModDone
	case ListPunch:
		return holly.EvtPunchDone
	}
	return holly.EvtOpaqueDone
}

// Long parameter kinds for the 64-byte parameter accumulator.
const (
	longNone = iota
	longSprite
	longVertex
)

type yuvState struct {
	mbDone   int
	partial  [384]byte // one macroblock: U, V, then four Y blocks
	partialN int
}

type taState struct {
	fifo       [8]uint32
	fifoN      int
	flipEndian bool

	hdr      Prim
	hdrValid bool
	open     int // index of the open prim in prims[hdr.List], -1 = none
	prims    [5][]Prim

	ispBytes     uint32
	opbBytes     uint32
	clip         [4]uint8 // user tile clip: x0, y0, x1, y1 in tiles
	opbCounts    []uint16
	opbErrRaised bool

	longArmed   int
	longKind    int
	longPending bool
	longBuf     [16]uint32

	yuv yuvState
}

// listInit resets the TA to accept a fresh scene.
func (t *taState) listInit() {
	t.fifoN = 0
	t.flipEndian = false
	t.hdr = Prim{}
	t.hdrValid = false
	t.open = -1
	for i := range t.prims {
		t.prims[i] = t.prims[i][:0]
	}
	t.ispBytes = 0
	t.opbBytes = 0
	t.clip = [4]uint8{0, 0, 255, 255}
	t.opbCounts = nil
	t.opbErrRaised = false
	t.longArmed = longNone
	t.longKind = longNone
	t.longPending = false
}

// writeWord consumes one word of the TA FIFO stream.
func (t *taState) writeWord(p *PVR, v uint32) {
	if t.flipEndian {
		v = bits.ReverseBytes32(v)
	}
	t.fifo[t.fifoN] = v
	t.fifoN++
	if t.fifoN < 8 {
		return
	}
	t.fifoN = 0
	pkt := t.fifo
	if t.longPending {
		copy(t.longBuf[8:], pkt[:])
		t.longPending = false
		switch t.longKind {
		case longSprite:
			t.finishSprite(p)
		case longVertex:
			t.addVertex64(p, t.longBuf)
		}
		return
	}
	if t.longArmed != longNone {
		copy(t.longBuf[:], pkt[:])
		t.longKind = t.longArmed
		t.longArmed = longNone
		t.longPending = true
		return
	}
	t.packet(p, pkt)
}

// packet dispatches one assembled 32-byte parameter.
func (t *taState) packet(p *PVR, pkt [8]uint32) {
	pcw := pkt[0]
	switch int(pcw >> pcwParaShift) {
	case paraEnd:
		t.closePrim(p)
		p.raise(holly.BankNormal, 1<<listDoneEvent(int((pcw>>pcwListShift)&7)))
	case paraUserClip:
		t.clip = [4]uint8{uint8(pkt[1]), uint8(pkt[1] >> 8), uint8(pkt[1] >> 16), uint8(pkt[1] >> 24)}
	case paraControl:
		if pkt[1]&1 != 0 {
			t.flipEndian = true
		}
		if pkt[1]&2 != 0 {
			t.flipEndian = false
		}
	case paraPolygon:
		t.closePrim(p)
		pr := Prim{List: int((pcw >> pcwListShift) & 7), PCW: pcw,
			ISPTSP: pkt[1], TSP: pkt[2], TCW: pkt[3]}
		if pcw&pcwVolume != 0 {
			pr.ISPTSP2 = pkt[4]
			pr.TSP2 = pkt[5]
			pr.TCW2 = pkt[6]
		} else {
			pr.BaseColor = pkt[4]
			pr.OffsetColor = pkt[5]
		}
		t.hdr = pr
		t.hdrValid = true
	case paraSprite:
		t.closePrim(p)
		pr := Prim{List: int((pcw >> pcwListShift) & 7), PCW: pcw,
			ISPTSP: pkt[1], TSP: pkt[2], TCW: pkt[3],
			BaseColor: pkt[4], OffsetColor: pkt[5], Sprite: true}
		t.hdr = pr
		t.hdrValid = true
		t.longArmed = longSprite
	case paraVertex:
		if !t.hdrValid {
			p.raise(holly.BankError, 1<<holly.ErrTAInputError)
			return
		}
		if t.hdr.TwoVolume() {
			copy(t.longBuf[:], pkt[:])
			t.longKind = longVertex
			t.longPending = true
			return
		}
		t.addVertex(p, decodeVertex32(pkt, t.hdr.ISPTSP&ispUV16 != 0))
	}
}

// decodeVertex32 reads one 32-byte vertex packet.
func decodeVertex32(pkt [8]uint32, uv16 bool) Vertex {
	v := Vertex{
		X:      math.Float32frombits(pkt[1]),
		Y:      math.Float32frombits(pkt[2]),
		Z:      math.Float32frombits(pkt[3]),
		Base:   pkt[6],
		Offset: pkt[7],
		EOS:    pkt[0]&pcwEOS != 0,
	}
	if uv16 {
		v.U = float16(pkt[4])
		v.V = float16(pkt[5])
	} else {
		v.U = math.Float32frombits(pkt[4])
		v.V = math.Float32frombits(pkt[5])
	}
	return v
}

// addVertex64 decodes one 64-byte two-volume vertex.
func (t *taState) addVertex64(p *PVR, w [16]uint32) {
	uv16 := t.hdr.ISPTSP&ispUV16 != 0
	v := Vertex{
		X:       math.Float32frombits(w[1]),
		Y:       math.Float32frombits(w[2]),
		Z:       math.Float32frombits(w[3]),
		Base:    w[6],
		Offset:  w[7],
		Base2:   w[10],
		Offset2: w[11],
		EOS:     w[0]&pcwEOS != 0,
	}
	if uv16 {
		v.U = float16(w[4])
		v.V = float16(w[5])
		v.U2 = float16(w[8])
		v.V2 = float16(w[9])
	} else {
		v.U = math.Float32frombits(w[4])
		v.V = math.Float32frombits(w[5])
		v.U2 = math.Float32frombits(w[8])
		v.V2 = math.Float32frombits(w[9])
	}
	t.addVertex(p, v)
}

// finishSprite builds the four corner vertices of a sprite from
// its 64-byte vertex parameter: corners A, B, C are explicit,
// the fourth is D = B + C - A. UV words pack two 16-bit floats
// each (U in the low half, V in the high half).
func (t *taState) finishSprite(p *PVR) {
	w := t.longBuf
	f := func(i int) float32 { return math.Float32frombits(w[i]) }
	ax, ay, az := f(1), f(2), f(3)
	bx, by, bz := f(4), f(5), f(6)
	cx, cy, cz := f(7), f(8), f(9)
	uv := func(i int) (float32, float32) {
		return float16(w[i]), float16(w[i] >> 16)
	}
	u0, v0 := uv(10)
	u1, v1 := uv(11)
	u2, v2 := uv(12)
	base, off := w[13], w[14]
	verts := []Vertex{
		{X: ax, Y: ay, Z: az, U: u0, V: v0, Base: base, Offset: off},
		{X: bx, Y: by, Z: bz, U: u1, V: v1, Base: base, Offset: off},
		{X: bx + cx - ax, Y: by + cy - ay, Z: bz + cz - az,
			U: u1 + u2 - u0, V: v1 + v2 - v0, Base: base, Offset: off},
		{X: cx, Y: cy, Z: cz, U: u2, V: v2, Base: base, Offset: off, EOS: true},
	}
	for _, v := range verts {
		t.addVertex(p, v)
	}
}

// addVertex appends a vertex to the open primitive, opening one
// from the current header when needed.
func (t *taState) addVertex(p *PVR, v Vertex) {
	if t.open < 0 {
		pr := t.hdr
		pr.Verts = nil
		t.prims[pr.List] = append(t.prims[pr.List], pr)
		t.open = len(t.prims[pr.List]) - 1
	}
	pr := &t.prims[t.hdr.List][t.open]
	pr.Verts = append(pr.Verts, v)
	if v.EOS {
		t.closePrim(p)
	}
}

// closePrim finalizes the open primitive: its parameter block is
// stored in VRAM at TA_ISP_BASE and an OPB entry is appended for
// every tile it covers.
func (t *taState) closePrim(p *PVR) {
	if t.open < 0 {
		return
	}
	pr := &t.prims[t.hdr.List][t.open]
	t.open = -1
	if len(pr.Verts) == 0 {
		return
	}
	base := p.reg(RegTAISPBase)
	words := [9]uint32{pr.PCW, pr.ISPTSP, pr.TSP, pr.TCW,
		pr.ISPTSP2, pr.TSP2, pr.TCW2, pr.BaseColor, pr.OffsetColor}
	for i, w := range words {
		p.vramWrite32(base+t.ispBytes+uint32(4*i), w)
	}
	ispAddr := base + t.ispBytes
	t.ispBytes += 36
	if lim := p.reg(RegTAISPLimit); lim != 0 && base+t.ispBytes > lim && !t.opbErrRaised {
		t.opbErrRaised = true
		p.raise(holly.BankError, 1<<holly.ErrParamOutOfMemory)
	}
	t.writeOPB(p, pr, ispAddr)
}

// allocSizes decodes TA_ALLOC_CTRL into per-list OPB budgets in
// bytes: size code 0 = no allocation, else 4<<code bytes.
func allocSizes(ctrl uint32) [5]uint32 {
	var out [5]uint32
	for l := 0; l < 5; l++ {
		code := (ctrl >> (4 * l)) & 3
		if code != 0 {
			out[l] = 4 << code
		}
	}
	return out
}

// writeOPB appends the primitive's object pointer to the OPB
// chain of each tile its screen bounding box covers.
func (t *taState) writeOPB(p *PVR, pr *Prim, ispAddr uint32) {
	sizes := allocSizes(p.reg(RegTAAllocCtrl))
	if sizes[pr.List] == 0 {
		return
	}
	var total uint32
	off := uint32(0)
	for l := 0; l < 5; l++ {
		if l == pr.List {
			off = total
		}
		total += sizes[l]
	}
	w, h := p.renderWidth(), p.renderHeight()
	tilesX := (w + 31) / 32
	minX, minY := float32(w), float32(h)
	maxX, maxY := float32(0), float32(0)
	for _, v := range pr.Verts {
		if v.X < minX {
			minX = v.X
		}
		if v.X > maxX {
			maxX = v.X
		}
		if v.Y < minY {
			minY = v.Y
		}
		if v.Y > maxY {
			maxY = v.Y
		}
	}
	x0 := clampInt(int(minX)/32, 0, tilesX-1)
	x1 := clampInt(int(maxX)/32, 0, tilesX-1)
	y0 := clampInt(int(minY)/32, 0, (h+31)/32-1)
	y1 := clampInt(int(maxY)/32, 0, (h+31)/32-1)
	olBase := p.reg(RegTAOLBase)
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			tile := ty*tilesX + tx
			idx := tile*5 + pr.List
			for len(t.opbCounts) <= idx {
				t.opbCounts = append(t.opbCounts, 0)
			}
			n := uint32(t.opbCounts[idx])
			if (n+1)*4 > sizes[pr.List] {
				if !t.opbErrRaised {
					t.opbErrRaised = true
					p.raise(holly.BankError, 1<<holly.ErrOPBOutOfMemory)
				}
				continue
			}
			addr := olBase + uint32(tile)*total + off + n*4
			p.vramWrite32(addr, ispAddr)
			t.opbCounts[idx]++
			t.opbBytes += 4
		}
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// renderWidth and renderHeight give the render target size from
// the FB clip registers (640x480 when unprogrammed).
func (p *PVR) renderWidth() int {
	maxX := int((p.reg(RegFBXClip) >> 16) & 0x7FF)
	if maxX == 0 {
		return 640
	}
	return maxX + 1
}

func (p *PVR) renderHeight() int {
	maxY := int((p.reg(RegFBYClip) >> 16) & 0xFFF)
	if maxY == 0 {
		return 480
	}
	return maxY + 1
}

// ---------------------------------------------------------------------------
// YUV converter
//
// YUV data written to the YUV FIFO is grouped into macroblocks
// of 384 bytes: an 8x8 U block, an 8x8 V block, then four 8x8 Y
// blocks (top-left, top-right, bottom-left, bottom-right) for a
// 16x16 pixel macroblock. TA_YUV_TEX_CTRL gives the texture size
// in macroblocks (bits 10:0 wide, bits 26:16 high). Each finished
// macroblock is converted to a twiddled RGB565 texture at
// TA_YUV_TEX_BASE (a VRAM byte address) using the BT.601
// conversion; when the last macroblock lands, EvtYUVDone fires.

func (t *taState) yuvWrite(p *PVR, v uint32) {
	for i := 0; i < 4; i++ {
		t.yuv.partial[t.yuv.partialN] = uint8(v >> (8 * i))
		t.yuv.partialN++
		if t.yuv.partialN == len(t.yuv.partial) {
			t.yuv.partialN = 0
			t.convertMacroblock(p)
			t.yuv.mbDone++
			if total := yuvTotalMB(p); total > 0 && t.yuv.mbDone >= total {
				t.yuv.mbDone = 0
				p.raise(holly.BankNormal, 1<<holly.EvtYUVDone)
			}
		}
	}
}

// yuvTotalMB returns the macroblock count of the YUV texture
// being converted.
func yuvTotalMB(p *PVR) int {
	ctrl := p.reg(RegTAYUVTexCtrl)
	return int(ctrl&0x7FF) * int((ctrl>>16)&0x7FF)
}

// convertMacroblock converts the assembled macroblock into
// twiddled RGB565 texels in VRAM.
func (t *taState) convertMacroblock(p *PVR) {
	ctrl := p.reg(RegTAYUVTexCtrl)
	mbw := int(ctrl & 0x7FF)
	if mbw == 0 {
		return
	}
	mbx := t.yuv.mbDone % mbw
	mby := t.yuv.mbDone / mbw
	w := mbw * 16
	h := int((ctrl>>16)&0x7FF) * 16
	base := p.reg(RegTAYUVTexBase) & 0x7FFFFF
	d := t.yuv.partial
	ublock := d[0:64]
	vblock := d[64:128]
	yblock := func(bx, by int) []byte { return d[128+(by*2+bx)*64 : 128+(by*2+bx)*64+64] }
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			yb := yblock(x>>3, y>>3)
			yy := int(yb[(y&7)*8+(x&7)])
			uu := int(ublock[(y>>1)*8+(x>>1)])
			vv := int(vblock[(y>>1)*8+(x>>1)])
			r, g, b := yuvToRGB(yy, uu, vv)
			texel := uint32(r>>3)<<11 | uint32(g>>2)<<5 | uint32(b>>3)
			off := base + uint32(twiddleIndex(mbx*16+x, mby*16+y, w, h))*2
			binary.LittleEndian.PutUint16(p.vram[off&0x7FFFFF:], uint16(texel))
		}
	}
}

// yuvToRGB converts one pixel with the BT.601 coefficients the
// hardware documents (full-range studio swing form).
func yuvToRGB(y, u, v int) (int, int, int) {
	c := float64(y-16) * 1.164
	r := c + 1.596*float64(v-128)
	g := c - 0.813*float64(v-128) - 0.391*float64(u-128)
	b := c + 2.018*float64(u-128)
	return clamp255(r), clamp255(g), clamp255(b)
}

func clamp255(f float64) int {
	if f < 0 {
		return 0
	}
	if f > 255 {
		return 255
	}
	return int(f + 0.5)
}

// twiddleIndex returns the texel index of (x, y) inside a
// twiddled texture of w by h texels (both powers of two). The
// bits of y occupy the even positions and the bits of x the odd
// positions, the smaller dimension's bits first; the remaining
// bits of the larger dimension follow in order.
func twiddleIndex(x, y, w, h int) int {
	idx := 0
	bit := 0
	xb := bits.Len(uint(w - 1))
	yb := bits.Len(uint(h - 1))
	for i := 0; i < xb || i < yb; i++ {
		if i < yb {
			idx |= ((y >> i) & 1) << bit
			bit++
		}
		if i < xb {
			idx |= ((x >> i) & 1) << bit
			bit++
		}
	}
	return idx
}

// float16 decodes an IEEE half-precision value held in the low
// 16 bits of w. Denormals flush to signed zero (documented).
func float16(w uint32) float32 {
	h := uint16(w)
	sign := uint32(h>>15) << 31
	exp := (h >> 10) & 0x1F
	frac := uint32(h & 0x3FF)
	switch {
	case exp == 0:
		return math.Float32frombits(sign)
	case exp == 0x1F:
		return math.Float32frombits(sign | 0x7F800000 | frac<<13)
	}
	return math.Float32frombits(sign | uint32(exp+112)<<23 | frac<<13)
}

// ---------------------------------------------------------------------------
// TA serialization
//
// Fixed part (taFixedSize bytes), then the variable part: five
// prim counts, the prims, the OPB count length, and the OPB
// counts.

const taFixedSize = 556

// primHeaderSize is the serialized size of one prim header.
const primHeaderSize = 44

// primVertSize is the serialized size of one vertex.
const primVertSize = 48

func (t *taState) serializeSize() int {
	n := taFixedSize + 20
	for l := 0; l < 5; l++ {
		for i := range t.prims[l] {
			n += primHeaderSize + primVertSize*len(t.prims[l][i].Verts)
		}
	}
	n += 4 + 2*len(t.opbCounts)
	return n
}

// taSerializedSize computes the TA section length implied by the
// counts inside a serialized TA section, or -1 if it is
// malformed.
func taSerializedSize(b []byte) int {
	if len(b) < taFixedSize+20 {
		return -1
	}
	off := taFixedSize
	var counts [5]int
	for l := 0; l < 5; l++ {
		counts[l] = int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
	}
	for l := 0; l < 5; l++ {
		for i := 0; i < counts[l]; i++ {
			if off+primHeaderSize > len(b) {
				return -1
			}
			nv := int(binary.LittleEndian.Uint32(b[off+40:]))
			off += primHeaderSize + primVertSize*nv
			if off > len(b) {
				return -1
			}
		}
	}
	if off+4 > len(b) {
		return -1
	}
	nopc := int(binary.LittleEndian.Uint32(b[off:]))
	return off + 4 + 2*nopc
}

func (t *taState) serialize(b []byte) []byte {
	tmp := make([]byte, 4)
	put := func(v uint32) {
		binary.LittleEndian.PutUint32(tmp, v)
		b = append(b, tmp...)
	}
	for _, w := range t.fifo {
		put(w)
	}
	put(uint32(t.fifoN))
	var flags uint32
	if t.hdrValid {
		flags |= 1
	}
	if t.flipEndian {
		flags |= 2
	}
	if t.opbErrRaised {
		flags |= 4
	}
	if t.longPending {
		flags |= 8
	}
	flags |= uint32(t.longKind&3) << 8
	flags |= uint32(t.longArmed&3) << 16
	put(flags)
	b = serializePrim(b, &t.hdr)
	put(t.ispBytes)
	put(t.opbBytes)
	put(uint32(t.clip[0]) | uint32(t.clip[1])<<8 | uint32(t.clip[2])<<16 | uint32(t.clip[3])<<24)
	for _, w := range t.longBuf {
		put(w)
	}
	put(uint32(t.yuv.mbDone))
	put(uint32(t.yuv.partialN))
	b = append(b, t.yuv.partial[:]...)
	open := t.open
	put(uint32(int32(open)))
	for l := 0; l < 5; l++ {
		put(uint32(len(t.prims[l])))
	}
	for l := 0; l < 5; l++ {
		for i := range t.prims[l] {
			b = serializePrim(b, &t.prims[l][i])
		}
	}
	put(uint32(len(t.opbCounts)))
	for _, c := range t.opbCounts {
		b = append(b, uint8(c), uint8(c>>8))
	}
	return b
}

// serializePrim appends one prim (header and vertices).
func serializePrim(b []byte, pr *Prim) []byte {
	tmp := make([]byte, 4)
	put := func(v uint32) {
		binary.LittleEndian.PutUint32(tmp, v)
		b = append(b, tmp...)
	}
	b = append(b, uint8(pr.List))
	var fl uint8
	if pr.TwoVolume() {
		fl |= 1
	}
	if pr.Sprite {
		fl |= 2
	}
	b = append(b, fl, 0, 0)
	put(pr.PCW)
	put(pr.ISPTSP)
	put(pr.TSP)
	put(pr.TCW)
	put(pr.ISPTSP2)
	put(pr.TSP2)
	put(pr.TCW2)
	put(pr.BaseColor)
	put(pr.OffsetColor)
	put(uint32(len(pr.Verts)))
	for _, v := range pr.Verts {
		put(math.Float32bits(v.X))
		put(math.Float32bits(v.Y))
		put(math.Float32bits(v.Z))
		put(math.Float32bits(v.U))
		put(math.Float32bits(v.V))
		put(v.Base)
		put(v.Offset)
		put(math.Float32bits(v.U2))
		put(math.Float32bits(v.V2))
		put(v.Base2)
		put(v.Offset2)
		var vf uint32
		if v.EOS {
			vf = 1
		}
		put(vf)
	}
	return b
}

func (t *taState) deserialize(r *reader) bool {
	for i := range t.fifo {
		t.fifo[i] = r.u32()
	}
	t.fifoN = int(r.u32())
	flags := r.u32()
	t.hdrValid = flags&1 != 0
	t.flipEndian = flags&2 != 0
	t.opbErrRaised = flags&4 != 0
	t.longPending = flags&8 != 0
	t.longKind = int((flags >> 8) & 3)
	t.longArmed = int((flags >> 16) & 3)
	if !deserializePrim(r, &t.hdr) {
		return false
	}
	t.ispBytes = r.u32()
	t.opbBytes = r.u32()
	clip := r.u32()
	t.clip = [4]uint8{uint8(clip), uint8(clip >> 8), uint8(clip >> 16), uint8(clip >> 24)}
	for i := range t.longBuf {
		t.longBuf[i] = r.u32()
	}
	t.yuv.mbDone = int(r.u32())
	t.yuv.partialN = int(r.u32())
	copy(t.yuv.partial[:], r.bytes(384))
	t.open = int(int32(r.u32()))
	var counts [5]int
	for l := 0; l < 5; l++ {
		counts[l] = int(r.u32())
	}
	for l := 0; l < 5; l++ {
		t.prims[l] = t.prims[l][:0]
		for i := 0; i < counts[l]; i++ {
			var pr Prim
			if !deserializePrim(r, &pr) {
				return false
			}
			t.prims[l] = append(t.prims[l], pr)
		}
	}
	nopc := int(r.u32())
	t.opbCounts = make([]uint16, nopc)
	for i := range t.opbCounts {
		t.opbCounts[i] = r.u16()
	}
	return true
}

// deserializePrim reads one prim written by serializePrim.
func deserializePrim(r *reader, pr *Prim) bool {
	pr.List = int(r.u8())
	fl := r.u8()
	pr.Sprite = fl&2 != 0
	r.u8()
	r.u8()
	pr.PCW = r.u32()
	pr.ISPTSP = r.u32()
	pr.TSP = r.u32()
	pr.TCW = r.u32()
	pr.ISPTSP2 = r.u32()
	pr.TSP2 = r.u32()
	pr.TCW2 = r.u32()
	pr.BaseColor = r.u32()
	pr.OffsetColor = r.u32()
	nv := int(r.u32())
	pr.Verts = make([]Vertex, nv)
	for i := range pr.Verts {
		v := &pr.Verts[i]
		v.X = math.Float32frombits(r.u32())
		v.Y = math.Float32frombits(r.u32())
		v.Z = math.Float32frombits(r.u32())
		v.U = math.Float32frombits(r.u32())
		v.V = math.Float32frombits(r.u32())
		v.Base = r.u32()
		v.Offset = r.u32()
		v.U2 = math.Float32frombits(r.u32())
		v.V2 = math.Float32frombits(r.u32())
		v.Base2 = r.u32()
		v.Offset2 = r.u32()
		v.EOS = r.u32()&1 != 0
	}
	return true
}
