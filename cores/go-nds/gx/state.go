package gx

// Save state. The layout is fixed: header scalars, the RE
// registers and tables, the matrix unit (current matrices,
// stacks, pointers), the vertex and lighting state, the FIFO
// parser/executor state, the two polygon/vertex RAM buffers,
// and the rendered frame buffer. The depth/attribute/ID
// scratch buffers are not stored: every render rewrites them
// completely.

const (
	matBytes   = 64
	vertBytes  = 28 // 4 coords + 3 color + 1 pad + s,t
	polyBytes  = 32 // 4 indices + count + attr + tex + pltt
	queueBytes = 260 * 5
	fbBytes    = Width * Height * 4
	stackBytes = (1 + 32 + 32 + 1) * matBytes
	bufBytes   = 4 + MaxVerts*vertBytes + 4 + MaxPolys*polyBytes
)

// StateSize is the exact serialized size of the engine state.
const StateSize = 4 + 8 + // FIFOWords, flags, irqMode, mtxMode
	24 + 32 + 64 + 16 + 4 + // RE registers and tables
	4*matBytes + stackBytes + 4 + // matrices, stacks, pointers
	24 + // vertex color and texcoord state
	16 + 4 + 128 + 48 + 48 + 16 + // lighting
	24 + 16 + 12 + // latched registers and test results
	16 + // primitive and swap state
	17 + queueBytes + 128 + // FIFO and executor
	2*bufBytes +
	fbBytes

type sw struct {
	b []byte
	n int
}

func (w *sw) u8(v uint8)   { w.b[w.n] = v; w.n++ }
func (w *sw) u16(v uint16) { w.u8(uint8(v)); w.u8(uint8(v >> 8)) }
func (w *sw) u32(v uint32) { w.u16(uint16(v)); w.u16(uint16(v >> 16)) }
func (w *sw) flag(f bool) {
	if f {
		w.u8(1)
	} else {
		w.u8(0)
	}
}
func (w *sw) mat(m mat) {
	for _, v := range m {
		w.u32(uint32(v))
	}
}
func (w *sw) buf(l *listBuf) {
	w.u32(uint32(len(l.verts)))
	for i := 0; i < MaxVerts; i++ {
		var v vertex
		if i < len(l.verts) {
			v = l.verts[i]
		}
		w.u32(uint32(v.x))
		w.u32(uint32(v.y))
		w.u32(uint32(v.z))
		w.u32(uint32(v.w))
		w.u8(v.r)
		w.u8(v.g)
		w.u8(v.b)
		w.u8(0)
		w.u32(uint32(v.s))
		w.u32(uint32(v.t))
	}
	w.u32(uint32(len(l.polys)))
	for i := 0; i < MaxPolys; i++ {
		var p polygon
		if i < len(l.polys) {
			p = l.polys[i]
		}
		for _, ix := range p.idx {
			w.u32(uint32(ix))
		}
		w.u32(uint32(p.n))
		w.u32(p.attr)
		w.u32(p.tex)
		w.u32(p.pltt)
	}
}

type sr struct {
	b []byte
	n int
}

func (r *sr) u8() uint8 { v := r.b[r.n]; r.n++; return v }
func (r *sr) u16() uint16 {
	v := uint16(r.u8()) | uint16(r.u8())<<8
	return v
}
func (r *sr) u32() uint32 { v := uint32(r.u16()) | uint32(r.u16())<<16; return v }
func (r *sr) flag() bool  { return r.u8() != 0 }
func (r *sr) mat() mat {
	var m mat
	for i := range m {
		m[i] = int32(r.u32())
	}
	return m
}
func (r *sr) buf() listBuf {
	var l listBuf
	nv := int(r.u32())
	l.verts = make([]vertex, 0, nv)
	for i := 0; i < MaxVerts; i++ {
		v := vertex{x: int32(r.u32()), y: int32(r.u32()), z: int32(r.u32()), w: int32(r.u32())}
		v.r, v.g, v.b = r.u8(), r.u8(), r.u8()
		r.u8()
		v.s, v.t = int32(r.u32()), int32(r.u32())
		if i < nv {
			l.verts = append(l.verts, v)
		}
	}
	np := int(r.u32())
	l.polys = make([]polygon, 0, np)
	for i := 0; i < MaxPolys; i++ {
		var p polygon
		for j := range p.idx {
			p.idx[j] = int32(r.u32())
		}
		p.n = int(r.u32())
		p.attr, p.tex, p.pltt = r.u32(), r.u32(), r.u32()
		if i < np {
			l.polys = append(l.polys, p)
		}
	}
	return l
}

// Serialize writes the engine state into dst (at least
// StateSize bytes) and returns the byte count.
func (g *GX) Serialize(dst []byte) int {
	w := &sw{b: dst}
	w.u32(g.FIFOWords)
	w.flag(g.stackErr)
	w.flag(g.boxResult)
	w.u8(uint8(g.mtxMode))
	w.u8(0)
	w.u32(g.irqMode)
	w.u32(g.alphaRef)
	w.u32(g.clearColor)
	w.u32(g.clearDepth)
	w.u32(g.clrImgOff)
	w.u32(g.fogColor)
	w.u32(g.fogOffset)
	for _, v := range g.fogTable {
		w.u8(v)
	}
	for _, v := range g.toonTable {
		w.u16(v)
	}
	for _, v := range g.edgeColor {
		w.u16(v)
	}
	w.u32(g.dotDepth)
	w.mat(g.curProj)
	w.mat(g.curPos)
	w.mat(g.curVec)
	w.mat(g.curTex)
	for _, m := range g.projStk {
		w.mat(m)
	}
	for _, m := range g.posStk {
		w.mat(m)
	}
	for _, m := range g.vecStk {
		w.mat(m)
	}
	for _, m := range g.texStk {
		w.mat(m)
	}
	w.u8(uint8(g.spProj))
	w.u8(uint8(g.spPos))
	w.u8(uint8(g.spTex))
	w.u8(0)
	w.u8(g.curR)
	w.u8(g.curG)
	w.u8(g.curB)
	w.u8(0)
	w.u32(uint32(g.curS))
	w.u32(uint32(g.curT))
	w.u32(uint32(g.lastX))
	w.u32(uint32(g.lastY))
	w.u32(uint32(g.lastZ))
	w.u32(g.diffuse)
	w.u32(g.ambient)
	w.u32(g.specular)
	w.u32(g.emission)
	w.flag(g.shineOn)
	w.u8(0)
	w.u8(0)
	w.u8(0)
	for _, v := range g.shineTbl {
		w.u8(v)
	}
	for i := 0; i < 4; i++ {
		for _, v := range g.lightVec[i] {
			w.u32(uint32(v))
		}
	}
	for i := 0; i < 4; i++ {
		for _, v := range g.lightHalf[i] {
			w.u32(uint32(v))
		}
	}
	for _, v := range g.lightCol {
		w.u32(v)
	}
	w.u32(g.pendAttr)
	w.u32(g.normalAttr)
	w.u32(g.beginAttr)
	w.u32(g.texParam)
	w.u32(g.plttBase)
	w.u32(g.viewport)
	for _, v := range g.posResult {
		w.u32(uint32(v))
	}
	for _, v := range g.vecResult {
		w.u32(uint32(v))
	}
	w.u8(uint8(g.primType))
	w.u8(0)
	w.u16(0)
	w.u32(uint32(g.primCount))
	w.flag(g.primOpen)
	w.flag(g.swapPending)
	w.flag(g.curManual)
	w.flag(g.curZBuf)
	w.flag(g.rndManual)
	w.flag(g.rndZBuf)
	w.u8(0)
	w.u8(0)
	// FIFO and executor.
	w.u8(uint8(len(g.idQueue)))
	for i := 0; i < 8; i++ {
		if i < len(g.idQueue) {
			w.u8(g.idQueue[i])
		} else {
			w.u8(0)
		}
	}
	w.u8(g.decCmd)
	w.u8(uint8(g.decRemain))
	w.u8(g.execCmd)
	w.u8(uint8(g.execHave))
	w.flag(g.executing)
	w.u8(0)
	w.u16(uint16(len(g.queue)))
	for i := 0; i < 260; i++ {
		var e fifoEntry
		if i < len(g.queue) {
			e = g.queue[i]
		}
		w.u8(e.cmd)
		w.u32(e.param)
	}
	for _, v := range g.execParams {
		w.u32(v)
	}
	w.buf(&g.build)
	w.buf(&g.display)
	for _, v := range g.fb {
		w.u32(v)
	}
	return w.n
}

// Deserialize restores the engine state and returns the byte
// count consumed.
func (g *GX) Deserialize(src []byte) int {
	r := &sr{b: src}
	g.FIFOWords = r.u32()
	g.stackErr = r.flag()
	g.boxResult = r.flag()
	g.mtxMode = int(r.u8())
	r.u8()
	g.irqMode = r.u32()
	g.alphaRef = r.u32()
	g.clearColor = r.u32()
	g.clearDepth = r.u32()
	g.clrImgOff = r.u32()
	g.fogColor = r.u32()
	g.fogOffset = r.u32()
	for i := range g.fogTable {
		g.fogTable[i] = r.u8()
	}
	for i := range g.toonTable {
		g.toonTable[i] = r.u16()
	}
	for i := range g.edgeColor {
		g.edgeColor[i] = r.u16()
	}
	g.dotDepth = r.u32()
	g.curProj = r.mat()
	g.curPos = r.mat()
	g.curVec = r.mat()
	g.curTex = r.mat()
	for i := range g.projStk {
		g.projStk[i] = r.mat()
	}
	for i := range g.posStk {
		g.posStk[i] = r.mat()
	}
	for i := range g.vecStk {
		g.vecStk[i] = r.mat()
	}
	for i := range g.texStk {
		g.texStk[i] = r.mat()
	}
	g.spProj = int(r.u8())
	g.spPos = int(r.u8())
	g.spTex = int(r.u8())
	r.u8()
	g.curR, g.curG, g.curB = r.u8(), r.u8(), r.u8()
	r.u8()
	g.curS = int32(r.u32())
	g.curT = int32(r.u32())
	g.lastX = int32(r.u32())
	g.lastY = int32(r.u32())
	g.lastZ = int32(r.u32())
	g.diffuse = r.u32()
	g.ambient = r.u32()
	g.specular = r.u32()
	g.emission = r.u32()
	g.shineOn = r.flag()
	r.u8()
	r.u8()
	r.u8()
	for i := range g.shineTbl {
		g.shineTbl[i] = r.u8()
	}
	for i := 0; i < 4; i++ {
		for j := range g.lightVec[i] {
			g.lightVec[i][j] = int32(r.u32())
		}
	}
	for i := 0; i < 4; i++ {
		for j := range g.lightHalf[i] {
			g.lightHalf[i][j] = int32(r.u32())
		}
	}
	for i := range g.lightCol {
		g.lightCol[i] = r.u32()
	}
	g.pendAttr = r.u32()
	g.normalAttr = r.u32()
	g.beginAttr = r.u32()
	g.texParam = r.u32()
	g.plttBase = r.u32()
	g.viewport = r.u32()
	for i := range g.posResult {
		g.posResult[i] = int32(r.u32())
	}
	for i := range g.vecResult {
		g.vecResult[i] = int32(r.u32())
	}
	g.primType = int(r.u8())
	r.u8()
	r.u16()
	g.primCount = int(r.u32())
	g.primOpen = r.flag()
	g.swapPending = r.flag()
	g.curManual = r.flag()
	g.curZBuf = r.flag()
	g.rndManual = r.flag()
	g.rndZBuf = r.flag()
	r.u8()
	r.u8()
	nID := int(r.u8())
	g.idQueue = g.idQueue[:0]
	for i := 0; i < 8; i++ {
		v := r.u8()
		if i < nID {
			g.idQueue = append(g.idQueue, v)
		}
	}
	g.decCmd = r.u8()
	g.decRemain = int(r.u8())
	g.execCmd = r.u8()
	g.execHave = int(r.u8())
	g.executing = r.flag()
	r.u8()
	nq := int(r.u16())
	g.queue = g.queue[:0]
	for i := 0; i < 260; i++ {
		e := fifoEntry{cmd: r.u8(), param: r.u32()}
		if i < nq {
			g.queue = append(g.queue, e)
		}
	}
	for i := range g.execParams {
		g.execParams[i] = r.u32()
	}
	g.build = r.buf()
	g.display = r.buf()
	for i := range g.fb {
		g.fb[i] = r.u32()
	}
	g.texValid = false
	return r.n
}
