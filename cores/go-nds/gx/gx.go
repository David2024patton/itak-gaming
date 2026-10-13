// Package gx implements the DS 3D geometry engine and
// rendering engine per GBATEK: the GXFIFO command port and the
// direct command ports, the matrix unit with its three
// stacks, per-vertex lighting with four lights, polygon
// assembly into the double-buffered polygon and vertex RAM,
// and the rendering engine that rasterizes the swapped list
// into the 256x192 layer the video package composites as
// engine A BG0.
//
// Execution model (stated): commands execute eagerly, as soon
// as their parameter set is complete, matching this core
// family's synchronous device style. The FIFO is modeled as
// the entry queue between the port parser and the executor;
// its GXSTAT level counts entries not yet consumed. A
// SWAP_BUFFERS command pauses the executor until the next
// VBlank (the hardware halts the geometry engine the same
// way), when the buffers exchange and the rendering engine
// draws the new display list. POS_TEST, VEC_TEST and BOX_TEST
// therefore complete instantly and the test-busy bit reads 0.
//
// Fixed point: matrix elements and clip coordinates are
// signed 32-bit with 12 fractional bits; multiplies
// accumulate in 64-bit and shift right by 12 (wrapping to 32
// bits), the documented matrix arithmetic shape.
package gx

import (
	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
)

// Screen geometry of the rendered layer.
const (
	Width  = 256
	Height = 192
)

// Polygon and vertex RAM capacities (GBATEK).
const (
	MaxPolys = 2048
	MaxVerts = 6144
)

// IO offsets (hook offset space, ARM9 page) claimed here.
const (
	offEdgeColor  = 0x330
	offAlphaRef   = 0x340
	offClearColor = 0x350
	offClearDepth = 0x354
	offFogColor   = 0x358
	offFogOffset  = 0x35C
	offFogTable   = 0x360
	offToonTable  = 0x380
	offGXFIFO     = 0x400
	offPorts      = 0x440
	offPortsEnd   = 0x600
	offGXSTAT     = 0x600
	offRAMCount   = 0x604
	offDotDepth   = 0x610
	offPOSResult  = 0x620
	offVECResult  = 0x630
	offClipResult = 0x640
	offVecMtxRes  = 0x680
	offEnd        = 0x6B0
	offDISP3DCNT  = 0x060
)

// paramCount gives the number of 32-bit parameters per
// documented command ID, or -1 for an invalid command (which
// the hardware ignores, fetching no parameters). GBATEK's
// invalid-command list omits 34h (SHININESS) even though its
// own command table documents it with 32 parameters; 34h is
// implemented as valid here.
func paramCount(cmd uint8) int {
	switch cmd {
	case 0x11, 0x15, 0x41: // MTX_PUSH, MTX_IDENTITY, END_VTXS
		return 0
	case 0x16, 0x18: // MTX_LOAD_4x4, MTX_MULT_4x4
		return 16
	case 0x17, 0x19: // MTX_LOAD_4x3, MTX_MULT_4x3
		return 12
	case 0x1A: // MTX_MULT_3x3
		return 9
	case 0x1B, 0x1C, 0x70: // MTX_SCALE, MTX_TRANS, BOX_TEST
		return 3
	case 0x23, 0x71: // VTX_16, POS_TEST
		return 2
	case 0x34: // SHININESS
		return 32
	case 0x10, 0x12, 0x13, 0x14, // matrix mode/pop/store/restore
		0x20, 0x21, 0x22, 0x24, 0x25, 0x26, 0x27, 0x28, // vertex group
		0x29, 0x2A, 0x2B, // polygon attr, teximage, pltt base
		0x30, 0x31, 0x32, 0x33, // lighting group
		0x40, 0x50, 0x60, 0x72: // begin, swap, viewport, vec test
		return 1
	}
	return -1
}

// fifoEntry is one 40-bit FIFO entry as the hardware stores
// it: a command byte plus one parameter word. A command with
// N parameters occupies N entries (a parameterless command
// occupies one entry with an unused parameter word).
type fifoEntry struct {
	cmd   uint8
	param uint32
}

// GX is the geometry and rendering engine.
type GX struct {
	bus *bus.Bus

	// FIFOWords counts 32-bit words written to the GXFIFO
	// port (0x04000400 and its mirrors), the DS-3 successor
	// of the DS-2 counting sink.
	FIFOWords uint32

	// Port parser: packed command IDs waiting to be
	// decompressed into entries, and the command currently
	// being decompressed (its parameters arrive as GXFIFO
	// writes).
	idQueue   []uint8
	decCmd    uint8
	decRemain int

	// Entry queue between parser and executor, capped at the
	// documented FIFO+PIPE capacity. The hardware stalls the
	// writer when full; this model drops excess entries
	// instead (only reachable while the executor is paused
	// at a swap with a full frame of queued commands).
	queue []fifoEntry

	// Executor assembly state.
	execCmd    uint8
	execHave   int
	execParams [32]uint32
	executing  bool

	// GXSTAT fields.
	stackErr  bool
	irqMode   uint32 // GXSTAT bits 30-31
	boxResult bool

	// RE plain registers and tables (write-only on hardware;
	// reads here return 0 except ALPHA_TEST_REF).
	alphaRef   uint32
	clearColor uint32
	clearDepth uint32 // 15-bit value in bits 0-14
	clrImgOff  uint32 // CLRIMAGE_OFFSET
	fogColor   uint32
	fogOffset  uint32 // 15-bit
	fogTable   [32]uint8
	toonTable  [32]uint16
	edgeColor  [8]uint16
	dotDepth   uint32 // DISP_1DOT_DEPTH, 15-bit W value

	// Matrix unit.
	mtxMode int
	curProj mat
	curPos  mat
	curVec  mat
	curTex  mat
	projStk [1]mat
	posStk  [32]mat
	vecStk  [32]mat
	texStk  [1]mat
	spProj  int
	spPos   int // 6-bit
	spTex   int

	// Vertex pipeline state.
	curR, curG, curB uint8 // current vertex color, 6-bit channels
	curS, curT       int32 // current texcoord, raw 4-frac domain
	lastX, lastY     int32
	lastZ            int32

	// Lighting state.
	diffuse   uint32 // raw DIF_AMB parameter
	ambient   uint32
	specular  uint32 // raw SPE_EMI parameter
	emission  uint32
	shineOn   bool
	shineTbl  [128]uint8
	lightVec  [4][3]int32 // transformed at LIGHT_VECTOR, 9-frac
	lightHalf [4][3]int32
	lightCol  [4]uint32 // RGB15 in bits 0-14

	// Latched command registers.
	pendAttr   uint32 // last POLYGON_ATTR written
	normalAttr uint32 // attr in effect for lighting (latched at NORMAL)
	beginAttr  uint32 // attr in effect for the current primitive
	texParam   uint32 // last TEXIMAGE_PARAM written
	plttBase   uint32 // last PLTT_BASE written
	viewport   uint32 // last VIEWPORT written

	// Test results.
	posResult [4]int32
	vecResult [3]int32

	// Polygon assembly.
	primType  int
	primCount int // vertices fed into the current primitive group
	primOpen  bool
	build     listBuf
	display   listBuf

	// Swap state. curSwapBits govern the list being built;
	// renderBits were latched for the display list at its
	// SWAP_BUFFERS.
	swapPending bool
	curManual   bool
	curZBuf     bool
	rndManual   bool
	rndZBuf     bool

	// Rendering engine output: rgb6 color with the 5-bit
	// alpha in bits 18-22, per pixel; 0 means fully
	// transparent (nothing rendered there).
	fb []uint32

	// Rendering scratch buffers (color, alpha, 24-bit
	// depth, fog flag, polygon ID), fully rewritten by
	// every render, so they are not serialized.
	fbC     []uint32
	fbA     []uint8
	fbDepth []uint32
	fbAttr  []uint8
	fbID    []uint8

	// Texture fetch spaces, rebuilt when VRAMCNT changes.
	texImg   [0x80000]byte
	texPal   [0x18000]byte
	texCache [9]byte
	texValid bool
}

// New builds the engine over the shared bus.
func New(b *bus.Bus) *GX {
	g := &GX{bus: b, fb: make([]uint32, Width*Height),
		fbC: make([]uint32, Width*Height), fbA: make([]uint8, Width*Height),
		fbDepth: make([]uint32, Width*Height), fbAttr: make([]uint8, Width*Height),
		fbID: make([]uint8, Width*Height)}
	g.curProj = identityMat()
	g.curPos = identityMat()
	g.curVec = identityMat()
	g.curTex = identityMat()
	return g
}

// disp3dcnt reads DISP3DCNT (0x04000060) from the ARM9 IO
// page storage.
func (g *GX) disp3dcnt() uint32 {
	return uint32(g.bus.IO9[offDISP3DCNT]) | uint32(g.bus.IO9[offDISP3DCNT+1])<<8
}

// setOverflow sets or clears the polygon/vertex RAM overflow
// flag (DISP3DCNT bit 13) in IO storage.
func (g *GX) setOverflow() {
	g.bus.IO9[offDISP3DCNT+1] |= 0x20
}

// FIFOLevel returns the number of FIFO entries not yet
// consumed by the executor: queued entries plus the
// parameters buffered for the command being assembled.
func (g *GX) FIFOLevel() int {
	return len(g.queue) + g.execHave
}

// FIFOBusy reports the GXSTAT geometry-busy condition:
// entries pending, a command mid-assembly, or the executor
// paused at a swap.
func (g *GX) FIFOBusy() bool {
	return len(g.queue) > 0 || g.executing || g.swapPending || len(g.idQueue) > 0 || g.decRemain > 0
}

// FIFOIRQCondition reports whether the GXSTAT FIFO IRQ mode
// currently asserts the ARM9 geometry FIFO interrupt (mode 1:
// less than half full, mode 2: empty, mode 3: never, per
// GBATEK).
func (g *GX) FIFOIRQCondition() bool {
	switch g.irqMode {
	case 1:
		return g.FIFOLevel() < 128
	case 2:
		return g.FIFOLevel() == 0 && !g.FIFOBusy()
	}
	return false
}

// pushEntry appends one entry, dropping it when the queue is
// at the documented capacity (see the queue field comment).
func (g *GX) pushEntry(e fifoEntry) {
	if len(g.queue) >= 260 {
		return
	}
	g.queue = append(g.queue, e)
}

// writeFIFO handles one 32-bit write to the GXFIFO port.
func (g *GX) writeFIFO(val uint32) {
	g.FIFOWords++
	if g.decRemain > 0 {
		// The word is a parameter for the command being
		// decompressed.
		g.pushEntry(fifoEntry{g.decCmd, val})
		g.decRemain--
		if g.decRemain == 0 {
			g.drainIDs()
		}
		g.pump()
		return
	}
	// The word packs up to four command IDs (low byte first);
	// a zero byte is no command.
	for i := 0; i < 4; i++ {
		if id := uint8(val >> (8 * i)); id != 0 {
			g.idQueue = append(g.idQueue, id)
		}
	}
	g.drainIDs()
	g.pump()
}

// drainIDs decompresses queued command IDs into entries
// until one needs parameters from future writes.
func (g *GX) drainIDs() {
	for len(g.idQueue) > 0 && g.decRemain == 0 {
		id := g.idQueue[0]
		g.idQueue = g.idQueue[1:]
		n := paramCount(id)
		if n < 0 {
			continue // invalid command: ignored, no parameters
		}
		if n == 0 {
			g.pushEntry(fifoEntry{id, 0})
			continue
		}
		g.decCmd = id
		g.decRemain = n
	}
}

// writePort handles one write to a direct command port
// (0x04000440 onward): the value becomes an entry tagged with
// the port's command. For parameterless commands the value is
// a dummy.
func (g *GX) writePort(off uint32, val uint32) {
	cmd := uint8((off - offGXFIFO) >> 2)
	if paramCount(cmd) < 0 {
		return
	}
	g.pushEntry(fifoEntry{cmd, val})
	g.pump()
}

// pump runs the executor until the queue empties, a command
// is left mid-assembly, or a swap pauses execution.
func (g *GX) pump() {
	for len(g.queue) > 0 {
		if g.swapPending {
			return
		}
		e := g.queue[0]
		g.queue = g.queue[1:]
		if !g.executing {
			n := paramCount(e.cmd)
			if n < 0 {
				continue
			}
			g.execCmd = e.cmd
			g.execHave = 0
			if n == 0 {
				g.execute(e.cmd, g.execParams[:0])
				continue
			}
			g.executing = true
		}
		g.execParams[g.execHave] = e.param
		g.execHave++
		if g.execHave == paramCount(g.execCmd) {
			g.executing = false
			g.execute(g.execCmd, g.execParams[:g.execHave])
			g.execHave = 0
		}
	}
}

// execute dispatches one fully assembled command.
func (g *GX) execute(cmd uint8, p []uint32) {
	switch {
	case cmd >= 0x10 && cmd <= 0x1C:
		g.execMatrix(cmd, p)
	case cmd == 0x20:
		g.cmdColor(p[0])
	case cmd == 0x21:
		g.cmdNormal(p[0])
	case cmd == 0x22:
		g.cmdTexcoord(p[0])
	case cmd >= 0x23 && cmd <= 0x28:
		g.cmdVertex(cmd, p)
	case cmd == 0x29:
		g.pendAttr = p[0]
	case cmd == 0x2A:
		g.texParam = p[0]
	case cmd == 0x2B:
		g.plttBase = p[0]
	case cmd == 0x30:
		g.diffuse = p[0] & 0x7FFF
		g.ambient = (p[0] >> 16) & 0x7FFF
		if p[0]&0x8000 != 0 {
			// The diffuse color doubles as the vertex color.
			g.setVertexColor(g.diffuse)
		}
	case cmd == 0x31:
		g.specular = p[0] & 0x7FFF
		g.emission = (p[0] >> 16) & 0x7FFF
		g.shineOn = p[0]&0x8000 != 0
	case cmd == 0x32:
		g.cmdLightVector(p[0])
	case cmd == 0x33:
		g.lightCol[(p[0]>>30)&3] = p[0] & 0x7FFF
	case cmd == 0x34:
		for i := 0; i < 32; i++ {
			for k := 0; k < 4; k++ {
				g.shineTbl[i*4+k] = uint8(p[i] >> (8 * k))
			}
		}
	case cmd == 0x40:
		g.beginVtxs(p[0])
	case cmd == 0x41:
		g.primOpen = false
	case cmd == 0x50:
		g.rndManual = g.curManual
		g.rndZBuf = g.curZBuf
		g.curManual = p[0]&1 != 0
		g.curZBuf = p[0]&2 != 0
		g.swapPending = true
	case cmd == 0x60:
		g.viewport = p[0]
	case cmd == 0x70:
		g.cmdBoxTest(p)
	case cmd == 0x71:
		g.cmdPosTest(p)
	case cmd == 0x72:
		g.cmdVecTest(p[0])
	}
}

// OnVBlank performs the deferred buffer swap (when a
// SWAP_BUFFERS is pending) and renders the display list into
// the frame buffer. The system calls it at line 192. Without
// a pending swap the same display list is rendered again, so
// rear-plane register changes still appear.
func (g *GX) OnVBlank() {
	if g.swapPending {
		g.display, g.build = g.build, listBuf{}
		g.swapPending = false
	}
	g.render()
	g.pump()
}

// ReadIO implements the read side of the claimed ARM9
// offsets: 0x320 (RDLINES_COUNT, reads 0 in this model), the
// RE register block 0x330-0x3BF, the command port range
// (write-only, reads 0), and the status/result block
// 0x600-0x6AF.
func (g *GX) ReadIO(off uint32, size int) (uint32, bool) {
	switch {
	case off == 0x320:
		return 0, true
	case off >= offEdgeColor && off < offToonTable+0x40:
		if off >= offAlphaRef && off < offAlphaRef+4 {
			return getLanes(g.alphaRef, off, size), true
		}
		return 0, true
	case off >= offGXFIFO && off < offPortsEnd:
		return 0, true
	case off == offGXSTAT || off == offGXSTAT+2:
		return getLanes(g.gxstat(), off, size), true
	case off == offRAMCount || off == offRAMCount+2:
		return getLanes(g.ramCount(), off, size), true
	case off == offDotDepth || off == offDotDepth+2:
		return 0, true
	case off >= offPOSResult && off < offPOSResult+16:
		i := (off - offPOSResult) / 4
		return getLanes(uint32(g.posResult[i]), off, size), true
	case off >= offVECResult && off < offVECResult+12:
		i := (off - offVECResult) / 4
		return getLanes(uint32(g.vecResult[i]), off, size), true
	case off >= offClipResult && off < offClipResult+64:
		i := (off - offClipResult) / 4
		clip := g.clipMatrix()
		return getLanes(uint32(clip[i]), off, size), true
	case off >= offVecMtxRes && off < offVecMtxRes+36:
		i := (off - offVecMtxRes) / 4
		vm := [9]int32{g.curVec[0], g.curVec[1], g.curVec[2],
			g.curVec[4], g.curVec[5], g.curVec[6],
			g.curVec[8], g.curVec[9], g.curVec[10]}
		return getLanes(uint32(vm[i]), off, size), true
	}
	return 0, false
}

// WriteIO implements the write side of the claimed offsets.
func (g *GX) WriteIO(off uint32, size int, val uint32) bool {
	switch {
	case off >= offEdgeColor && off < offToonTable+0x40:
		g.writeREReg(off, size, val)
		return true
	case off >= offGXFIFO && off < offPorts:
		if size == 4 {
			g.writeFIFO(val)
		}
		return true
	case off >= offPorts && off < offPortsEnd:
		if size == 4 && off&3 == 0 {
			g.writePort(off, val)
		}
		return true
	case off == offGXSTAT || off == offGXSTAT+2:
		g.writeGXSTAT(off, size, val)
		return true
	case off == offDotDepth || off == offDotDepth+2:
		if size == 4 || off == offDotDepth {
			g.dotDepth = putLanes(g.dotDepth, off, size, val) & 0x7FFF
		}
		return true
	case off >= offPOSResult && off < offEnd:
		return true // result registers are read-only
	}
	return false
}

// writeREReg merges a write into the RE register block:
// byte-lane tables (edge colors, fog table, toon table) and
// the plain registers between them.
func (g *GX) writeREReg(off uint32, size int, val uint32) {
	// bytewise merges one written unit into a halfword table.
	bytewise := func(tbl []uint16, base uint32) {
		for i := 0; i < size; i++ {
			pos := off - base + uint32(i)
			b := uint16(byte(val >> (8 * i)))
			if pos%2 == 0 {
				tbl[pos/2] = tbl[pos/2]&0xFF00 | b
			} else {
				tbl[pos/2] = tbl[pos/2]&0x00FF | b<<8
			}
		}
	}
	switch {
	case off >= offEdgeColor && off < offEdgeColor+16:
		bytewise(g.edgeColor[:], offEdgeColor)
	case off >= offAlphaRef && off < offAlphaRef+4:
		g.alphaRef = putLanes(g.alphaRef, off, size, val) & 0x1F
	case off >= offClearColor && off < offClearColor+4:
		g.clearColor = putLanes(g.clearColor, off, size, val)
	case off >= offClearDepth && off < offClearDepth+2:
		g.clearDepth = putLanes(g.clearDepth, off, size, val) & 0x7FFF
		if size == 4 {
			// The high half of a 32-bit write is
			// CLRIMAGE_OFFSET.
			g.clrImgOff = val >> 16
		}
	case off >= offClearDepth+2 && off < offClearDepth+4:
		g.clrImgOff = putLanes(g.clrImgOff, off, size, val)
	case off >= offFogColor && off < offFogColor+4:
		g.fogColor = putLanes(g.fogColor, off, size, val)
	case off >= offFogOffset && off < offFogOffset+4:
		g.fogOffset = putLanes(g.fogOffset, off, size, val) & 0x7FFF
	case off >= offFogTable && off < offFogTable+32:
		for i := 0; i < size; i++ {
			g.fogTable[off-offFogTable+uint32(i)] = byte(val >> (8 * i))
		}
	case off >= offToonTable && off < offToonTable+64:
		bytewise(g.toonTable[:], offToonTable)
	}
}

// writeGXSTAT handles GXSTAT writes: bits 30-31 set the FIFO
// IRQ mode; writing bit 15 set clears the stack error flag
// and resets the projection (and, per GBATEK's "probably",
// texture) stack pointer.
func (g *GX) writeGXSTAT(off uint32, size int, val uint32) {
	merged := putLanes(g.irqMode<<30, off, size, val)
	g.irqMode = merged >> 30
	if merged&(1<<15) != 0 {
		g.stackErr = false
		g.spProj = 0
		g.spTex = 0
	}
}

// gxstat composes the GXSTAT register.
func (g *GX) gxstat() uint32 {
	var s uint32
	if g.boxResult {
		s |= 1 << 1
	}
	s |= uint32(g.spPos&31) << 8
	s |= uint32(g.spProj&1) << 13
	if g.stackErr {
		s |= 1 << 15
	}
	lvl := g.FIFOLevel()
	s |= uint32(lvl) << 16
	if lvl < 128 {
		s |= 1 << 25
	}
	if lvl == 0 && !g.FIFOBusy() {
		s |= 1 << 26
	}
	if g.FIFOBusy() {
		s |= 1 << 27
	}
	s |= g.irqMode << 30
	return s
}

// ramCount composes RAM_COUNT from the build buffer: polygon
// count in bits 0-11, vertex count in bits 16-28.
func (g *GX) ramCount() uint32 {
	return uint32(len(g.build.polys)) | uint32(len(g.build.verts))<<16
}

// getLanes extracts the unit of the given size at the byte
// lane of off from a 32-bit value (same convention as the
// system hook).
func getLanes(v uint32, off uint32, size int) uint32 {
	switch size {
	case 1:
		return (v >> ((off & 3) * 8)) & 0xFF
	case 2:
		return (v >> ((off & 2) * 8)) & 0xFFFF
	}
	return v
}

// putLanes merges a written unit into a 32-bit value.
func putLanes(v uint32, off uint32, size int, val uint32) uint32 {
	switch size {
	case 1:
		sh := (off & 3) * 8
		return v&^(0xFF<<sh) | (val&0xFF)<<sh
	case 2:
		sh := (off & 2) * 8
		return v&^(0xFFFF<<sh) | (val&0xFFFF)<<sh
	}
	return val
}
