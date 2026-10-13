// Package ge implements the PSP Graphics Engine (GE) display list
// processor for the from scratch Go PSP core.
//
// Scope of this slice: display lists are executed synchronously from
// memory. The processor covers the command state machine (including
// SIGNAL stalls, CALL/RET, and JUMP), vertex fetching for the documented
// vertex formats, a software rasterizer (points, lines, triangles,
// strips, fans, and sprites), texturing with CLUT and swizzled storage,
// depth / stencil / alpha / color tests, blending, logical ops, write
// masks, and a clear mode. Bezier, spline, and bounding box commands are
// counted but not drawn. There is NO backface culling and NO perspective
// correction in triangle interpolation in this slice; attributes are
// interpolated linearly in screen space.
//
// Interpretation notes, where the command model leaves room:
//
//   - Float parameters (viewport scale and center, offsets, texture
//     scale and offset) arrive in the 24 bit command argument as the top
//     24 bits of an IEEE 754 float32, the PSP "float24" packing: the
//     value is math.Float32frombits(arg << 8). A caller that wants 1.0
//     passes math.Float32bits(1.0) >> 8.
//   - Texture scale registers that were never written read as 1.0, which
//     matches the hardware reset state and makes plain UVs in [0,1]
//     usable without an explicit TEXSCALE command.
//   - RGBMASK and AMASK registers that were never written read as fully
//     open (0xFFFFFF and 0xFF), again the hardware reset state.
//   - Texture sampling uses the MAG filter selection (falling back to
//     the MIN selection if only that one requests linear), because this
//     slice has no mipmaps and draws at display scale.
package ge

import (
	"encoding/binary"
	"errors"
	"math"
)

// Mem is the memory view the GE works through. The machine bus
// (bus.Bus) satisfies it. Addresses are full bus addresses: the GE does
// its own base/origin arithmetic and hands raw addresses to Mem.
type Mem interface {
	Read8(uint32) uint8
	Read16(uint32) uint16
	Read32(uint32) uint32
	Write8(uint32, uint8)
	Write16(uint32, uint16)
	Write32(uint32, uint32)
}

// ListState is the execution state of the current display list.
type ListState int

const (
	StateReady     ListState = iota // no list submitted yet
	StateDrawing                    // transient, while Submit/Continue runs
	StateStalled                    // stopped after a SIGNAL command
	StateCompleted                  // END reached (or the safety cap hit)
)

// Opcodes used by name inside this package. Every opcode, listed or not,
// stores its argument into regs[op] and bumps its execution counter.
const (
	opNOP           = 0x00
	opVADDR         = 0x01
	opIADDR         = 0x02
	opPRIM          = 0x04
	opJUMP          = 0x08
	opCALL          = 0x0A
	opRET           = 0x0B
	opEND           = 0x0C
	opSIGNAL        = 0x0E
	opFINISH        = 0x0F
	opBASE          = 0x10
	opVTYPE         = 0x12
	opORIGIN        = 0x14
	opFRAMEBUFPTR   = 0x9C
	opZBUFPTR       = 0x9E
	opTEXADDR0      = 0xA0
	opCLUTADDR      = 0xB0
	opCLUTFORMAT    = 0xB1
	opCLUTLOAD      = 0xB2
	opFRAMEBUFWIDTH = 0x9D
)

// maxListWords is the safety cap on words executed per Submit/Continue
// run. A list that never reaches END or SIGNAL stops here and is
// reported Completed.
const maxListWords = 1 << 20

// maxCallDepth is the display list call stack depth. Deeper CALLs are
// executed (and counted) but do not push a return address.
const maxCallDepth = 8

// fbMaxHeight is the GE framebuffer height limit in rows.
const fbMaxHeight = 512

// GE is one display list processor. State is fully inspectable through
// Reg, CmdCount, TotalCommands, State, and SignalInfo.
type GE struct {
	mem Mem

	regs   [256]uint32 // last argument written per opcode
	counts [256]uint64 // executions per opcode

	state          ListState
	SignalValue    uint32 // full argument of the last SIGNAL command
	SignalBehavior uint32 // (arg >> 16) & 0xFF of the last SIGNAL
	FinishedSeen   bool   // a FINISH command was seen in the current list

	pc         uint32 // address of the next list word to fetch
	stack      [maxCallDepth]uint32
	stackDepth int

	basePtr   uint32 // BASE: basePtr = arg << 8
	originPtr uint32 // ORIGIN: originPtr = arg << 8
	vertexPtr uint32 // VADDR: originPtr + arg
	indexPtr  uint32 // IADDR: originPtr + arg

	fbAddr   uint32 // FRAMEBUFPTR: 0x04000000 | arg
	zAddr    uint32 // ZBUFPTR: 0x04000000 | arg
	texAddr  uint32 // TEXADDR0: originPtr + arg
	clutAddr uint32 // CLUTADDR: originPtr + arg

	clut    [512]uint32 // RGBA8 packed palette, r | g<<8 | b<<16 | a<<24
	stencil []byte      // side plane, lazily allocated fbWidth*512 bytes

	// Display buffer state, set by SetDisplayBuffer. When DispSet is
	// false, DisplayPixel reads through the draw buffer state instead.
	DispAddr   uint32
	DispWidth  int
	DispFormat int
	DispSet    bool
}

// New returns a GE reading and writing through mem. The draw buffer
// defaults to the start of VRAM, which is where PSP framebuffers live.
func New(mem Mem) *GE {
	return &GE{
		mem:    mem,
		state:  StateReady,
		fbAddr: 0x04000000,
		zAddr:  0x04000000,
	}
}

// State reports the current list state.
func (g *GE) State() ListState { return g.state }

// SignalInfo returns the last SIGNAL argument, its behavior byte, and
// whether FINISH has been seen in the current list.
func (g *GE) SignalInfo() (value, behavior uint32, finished bool) {
	return g.SignalValue, g.SignalBehavior, g.FinishedSeen
}

// Reg returns the last argument stored for opcode op, or 0 if that
// opcode never executed.
func (g *GE) Reg(op int) uint32 {
	if op < 0 || op > 255 {
		return 0
	}
	return g.regs[op]
}

// CmdCount returns how many times opcode op has executed.
func (g *GE) CmdCount(op int) uint64 {
	if op < 0 || op > 255 {
		return 0
	}
	return g.counts[op]
}

// TotalCommands returns the total number of list words executed.
func (g *GE) TotalCommands() uint64 {
	var n uint64
	for _, c := range g.counts {
		n += c
	}
	return n
}

// Submit starts executing the display list at addr. Execution runs
// synchronously until END (StateCompleted), SIGNAL (StateStalled), or
// the safety cap. The call stack and FINISH flag are reset for the new
// list; register state carries over, the way it does on hardware.
func (g *GE) Submit(addr uint32) ListState {
	g.pc = addr
	g.stackDepth = 0
	g.FinishedSeen = false
	g.state = StateDrawing
	return g.run()
}

// Continue resumes a list stalled on SIGNAL, starting from the word
// after the SIGNAL. In any other state it is a no-op that returns the
// current state.
func (g *GE) Continue() ListState {
	if g.state != StateStalled {
		return g.state
	}
	g.state = StateDrawing
	return g.run()
}

// run is the fetch and execute loop shared by Submit and Continue.
func (g *GE) run() ListState {
	for steps := 0; steps < maxListWords; steps++ {
		if g.mem == nil {
			g.state = StateCompleted
			return g.state
		}
		word := g.mem.Read32(g.pc)
		op := int(word >> 24)
		arg := word & 0xFFFFFF
		// Every command stores its argument, even unlisted ones.
		g.regs[op] = arg
		g.counts[op]++
		next := g.pc + 4

		switch op {
		case opEND:
			g.pc = next
			g.state = StateCompleted
			return g.state
		case opSIGNAL:
			g.SignalValue = arg
			g.SignalBehavior = (arg >> 16) & 0xFF
			g.pc = next
			g.state = StateStalled
			return g.state
		case opFINISH:
			g.FinishedSeen = true
		case opJUMP:
			// Jump targets are base relative.
			g.pc = g.basePtr + arg
			continue
		case opCALL:
			if g.stackDepth < maxCallDepth {
				g.stack[g.stackDepth] = next
				g.stackDepth++
			}
			g.pc = g.basePtr + arg
			continue
		case opRET:
			if g.stackDepth == 0 {
				// Returning with an empty stack ends the list.
				g.pc = next
				g.state = StateCompleted
				return g.state
			}
			g.stackDepth--
			g.pc = g.stack[g.stackDepth]
			continue
		default:
			g.execute(op, arg)
		}
		g.pc = next
	}
	// Safety cap hit without END or SIGNAL.
	g.state = StateCompleted
	return g.state
}

// execute applies the side effects of one non-control command. Commands
// whose state is derived from regs on demand (texture size, filters,
// tests, blend setup, and so on) need no work here beyond the register
// store run() already did; they are listed in the switch only where a
// separate field has to move.
func (g *GE) execute(op int, arg uint32) {
	switch op {
	case opBASE:
		g.basePtr = arg << 8
	case opORIGIN:
		g.originPtr = arg << 8
	case opVADDR:
		g.vertexPtr = g.originPtr + arg
	case opIADDR:
		g.indexPtr = g.originPtr + arg
	case opFRAMEBUFPTR:
		g.fbAddr = 0x04000000 | arg
	case opZBUFPTR:
		g.zAddr = 0x04000000 | arg
	case opTEXADDR0:
		g.texAddr = g.originPtr + arg
	case opCLUTADDR:
		g.clutAddr = g.originPtr + arg
	case opCLUTLOAD:
		g.loadCLUT(arg)
	case opPRIM:
		g.prim(arg)
	}
}

// floatReg reads a float parameter register. Values arrive in the
// 24 bit argument as the top 24 bits of a float32 (see package doc).
func (g *GE) floatReg(op int) float64 {
	return float64(math.Float32frombits(g.regs[op] << 8))
}

// clutParams decodes the CLUTFORMAT register (0xB1).
func (g *GE) clutParams() (format, shift, mask, start int) {
	a := g.regs[opCLUTFORMAT]
	return int(a & 3), int((a >> 2) & 31), int((a >> 8) & 31), int((a >> 16) & 511)
}

// loadCLUT implements CLUTLOAD (0xB2): arg bits 5:0 are the number of
// 8-entry blocks to load; 0 means 64 blocks (the full 512 entries).
// Entries are decoded from clutAddr in the CLUTFORMAT pixel format into
// the palette as RGBA8 packed words, starting at palette entry 0.
func (g *GE) loadCLUT(arg uint32) {
	blocks := int(arg & 0x3F)
	if blocks == 0 {
		blocks = 64
	}
	entries := blocks * 8
	if entries > 512 {
		entries = 512
	}
	format, _, _, _ := g.clutParams()
	size := 4
	if format != 3 {
		size = 2
	}
	for i := 0; i < entries; i++ {
		addr := g.clutAddr + uint32(i*size)
		var c rgba8
		if format == 3 {
			c = unpack8888(g.mem.Read32(addr))
		} else {
			c = unpack16(g.mem.Read16(addr), format)
		}
		g.clut[i] = uint32(c.r) | uint32(c.g)<<8 | uint32(c.b)<<16 | uint32(c.a)<<24
	}
}

// SetDisplayBuffer selects the buffer DisplayPixel reads back. addr is a
// full bus address, width is in pixels, and format is a pixel format
// code (0=5650, 1=5551, 2=4444, 3=8888).
func (g *GE) SetDisplayBuffer(addr uint32, width, format int) {
	g.DispAddr = addr
	g.DispWidth = width
	g.DispFormat = format
	g.DispSet = true
}

// DisplayPixel reads one pixel from the display buffer, or from the
// draw buffer when no display buffer was set.
func (g *GE) DisplayPixel(x, y int) (r, gr, b, a uint8) {
	if g.DispSet {
		c := g.readPixelAt(g.DispAddr, g.DispWidth, g.DispFormat, x, y)
		return c.r, c.g, c.b, c.a
	}
	return g.FramebufferPixel(x, y)
}

// ReadDisplayPixel is an alias for DisplayPixel.
func (g *GE) ReadDisplayPixel(x, y int) (r, gr, b, a uint8) {
	return g.DisplayPixel(x, y)
}

// FramebufferPixel reads one pixel from the current draw buffer, using
// FRAMEBUFPTR, FRAMEBUFWIDTH, and PIXELFORMAT state.
func (g *GE) FramebufferPixel(x, y int) (r, gr, b, a uint8) {
	c := g.readPixelAt(g.fbAddr, g.fbWidth(), g.pixelFormat(), x, y)
	return c.r, c.g, c.b, c.a
}

// ConvertDisplay converts a width by height region of the display
// buffer (or draw buffer, when no display buffer is set) to a packed
// RGBA8 byte slice in row major order, 4 bytes per pixel.
func (g *GE) ConvertDisplay(width, height int) []byte {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	out := make([]byte, 0, width*height*4)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, gr, b, a := g.DisplayPixel(x, y)
			out = append(out, r, gr, b, a)
		}
	}
	return out
}

// Serialize encodes the full GE state deterministically. Layout, all
// words little endian:
//
//	regs[256], clut[512],
//	vertexPtr, indexPtr, basePtr, originPtr, fbAddr, zAddr, texAddr,
//	clutAddr, state, SignalValue, SignalBehavior, FinishedSeen (word),
//	listPC, stack depth, stack[8],
//	DispAddr, DispWidth, DispFormat, DispSet (word),
//	stencil presence word, then if present: stencil length word + bytes.
//
// Command counters are diagnostics and are not serialized; a restored
// GE starts its counters at zero.
func (g *GE) Serialize() []byte {
	size := 256*4 + 512*4 + 8*4 + 5*4 + 4 + 8*4 + 4*4 + 4
	if g.stencil != nil {
		size += 4 + len(g.stencil)
	}
	buf := make([]byte, 0, size)
	put := func(v uint32) { buf = binary.LittleEndian.AppendUint32(buf, v) }
	boolWord := func(b bool) uint32 {
		if b {
			return 1
		}
		return 0
	}
	for _, v := range g.regs {
		put(v)
	}
	for _, v := range g.clut {
		put(v)
	}
	put(g.vertexPtr)
	put(g.indexPtr)
	put(g.basePtr)
	put(g.originPtr)
	put(g.fbAddr)
	put(g.zAddr)
	put(g.texAddr)
	put(g.clutAddr)
	put(uint32(g.state))
	put(g.SignalValue)
	put(g.SignalBehavior)
	put(boolWord(g.FinishedSeen))
	put(g.pc)
	put(uint32(g.stackDepth))
	for _, v := range g.stack {
		put(v)
	}
	put(g.DispAddr)
	put(uint32(int32(g.DispWidth)))
	put(uint32(int32(g.DispFormat)))
	put(boolWord(g.DispSet))
	if g.stencil != nil {
		put(1)
		put(uint32(len(g.stencil)))
		buf = append(buf, g.stencil...)
	} else {
		put(0)
	}
	return buf
}

// Deserialize restores state produced by Serialize. The total length
// must match the layout exactly, including the stencil length field.
func (g *GE) Deserialize(data []byte) error {
	// Fixed part: everything except optional stencil bytes and the
	// stencil length word that only exists when a plane is present.
	const fixedWords = 256 + 512 + 8 + 5 + 1 + 8 + 4 + 1
	if len(data) < fixedWords*4 {
		return errors.New("ge: serialized state too short")
	}
	rd := &reader{data: data}
	for i := range g.regs {
		g.regs[i] = rd.u32()
	}
	for i := range g.clut {
		g.clut[i] = rd.u32()
	}
	g.vertexPtr = rd.u32()
	g.indexPtr = rd.u32()
	g.basePtr = rd.u32()
	g.originPtr = rd.u32()
	g.fbAddr = rd.u32()
	g.zAddr = rd.u32()
	g.texAddr = rd.u32()
	g.clutAddr = rd.u32()
	g.state = ListState(rd.u32())
	g.SignalValue = rd.u32()
	g.SignalBehavior = rd.u32()
	g.FinishedSeen = rd.u32() != 0
	g.pc = rd.u32()
	depth := int(rd.u32())
	if depth < 0 || depth > maxCallDepth {
		return errors.New("ge: call stack depth out of range")
	}
	g.stackDepth = depth
	for i := range g.stack {
		g.stack[i] = rd.u32()
	}
	g.DispAddr = rd.u32()
	g.DispWidth = int(int32(rd.u32()))
	g.DispFormat = int(int32(rd.u32()))
	g.DispSet = rd.u32() != 0
	presence := rd.u32()
	switch presence {
	case 0:
		if rd.off != len(data) {
			return errors.New("ge: serialized length mismatch")
		}
		g.stencil = nil
	case 1:
		if len(data)-rd.off < 4 {
			return errors.New("ge: stencil length missing")
		}
		n := int(rd.u32())
		if n < 0 || rd.off+n != len(data) {
			return errors.New("ge: serialized length mismatch")
		}
		g.stencil = make([]byte, n)
		copy(g.stencil, data[rd.off:rd.off+n])
	default:
		return errors.New("ge: bad stencil presence word")
	}
	for i := range g.counts {
		g.counts[i] = 0
	}
	return nil
}

// reader is a tiny little endian cursor for Deserialize.
type reader struct {
	data []byte
	off  int
}

func (r *reader) u32() uint32 {
	v := binary.LittleEndian.Uint32(r.data[r.off:])
	r.off += 4
	return v
}
