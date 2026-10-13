// Package rdp implements the Nintendo 64 Reality Display
// Processor: the command processor that consumes display
// lists from RDRAM (XBUS off) or from RSP DMEM (XBUS on),
// the TMEM texture memory with its tile descriptors, the
// color combiner and blender, and a software rasterizer
// that draws into the color and depth images in RDRAM.
// It is original code written from the published SGI RDP
// command summary and the public N64 programming
// documentation (command encodings, register layouts,
// and pipeline semantics are factual hardware data).
//
// Execution is synchronous: writing DP_END runs the list
// to completion immediately, so the busy bits in
// DP_STATUS always read clear and FULLSYNC raises the DP
// interrupt (through MI) as soon as it is reached. That
// simplification, and the other model choices (coverage,
// the three-texel texture filter, LOD scope, the depth
// store format), are documented in the README and in the
// comments at each site.
package rdp

import "encoding/binary"

// Image formats (Set Color/Texture Image, Set Tile).
const (
	FmtRGBA = 0
	FmtYUV  = 1
	FmtCI   = 2
	FmtIA   = 3
	FmtI    = 4
)

// Texel element sizes (the size field of image and tile
// commands): 0 = 4-bit, 1 = 8-bit, 2 = 16-bit, 3 = 32-bit.
const (
	Size4  = 0
	Size8  = 1
	Size16 = 2
	Size32 = 3
)

// DP register byte offsets from 0x04100000 (also the RSP
// CP0 registers 8..15 in the same order).
const (
	RegStart   = 0x00
	RegEnd     = 0x04
	RegCurrent = 0x08
	RegStatus  = 0x0C
	RegClock   = 0x10
	RegBufBusy = 0x14
	RegPipeBsy = 0x18
	RegTMem    = 0x1C
)

// DP_STATUS read bits.
const (
	StatusXBusDMEM  = 1 << 0
	StatusFreeze    = 1 << 1
	StatusFlush     = 1 << 2
	StatusGClkAlive = 1 << 3
	StatusEndValid  = 1 << 9
	StatusStartVal  = 1 << 10
)

// Command opcodes (bits 61..56 of the first word).
const (
	CmdNoOp         = 0x00
	CmdTriBase      = 0x08 // 0x08..0x0F: triangles
	CmdTexRect      = 0x24
	CmdTexRectFlip  = 0x25
	CmdSyncPipe     = 0x27
	CmdSyncTile     = 0x28
	CmdSyncFull     = 0x29
	CmdSetKeyGB     = 0x2A
	CmdSetKeyR      = 0x2B
	CmdSetConvert   = 0x2C
	CmdSetScissor   = 0x2D
	CmdSetPrimDepth = 0x2E
	CmdSetOtherMode = 0x2F
	CmdLoadTLUT     = 0x30
	CmdSyncLoad     = 0x31
	CmdSetTileSize  = 0x32
	CmdLoadBlock    = 0x33
	CmdLoadTile     = 0x34
	CmdSetTile      = 0x35
	CmdFillRect     = 0x36
	CmdSetFillColor = 0x37
	CmdSetFogColor  = 0x38
	CmdSetBlendCol  = 0x39
	CmdSetPrimColor = 0x3A
	CmdSetEnvColor  = 0x3B
	CmdSetCombine   = 0x3C
	CmdSetTexImage  = 0x3D
	CmdSetZImage    = 0x3E
	CmdSetColorImg  = 0x3F
)

// RGBA is one pixel in flight through the pipeline, one
// byte per channel.
type RGBA struct {
	R, G, B, A uint8
}

// Image describes one of the image registers (color,
// texture): format, texel size, width in pixels (the
// programmed width field plus one), and the RDRAM byte
// address of the top-left corner.
type Image struct {
	Format int
	Size   int
	Width  int
	Addr   uint32
}

// Tile is one of the eight TMEM tile descriptors.
// SL/TL/SH/TH are kept in the raw 10.2 units the
// commands carry (texel = raw / 4).
type Tile struct {
	Format  int
	Size    int
	Line    int // TMEM 64-bit words per tile line
	TMem    int // tile base in TMEM, in 64-bit words
	Palette int
	ClampT  bool
	MirrorT bool
	MaskT   int
	ShiftT  int
	ClampS  bool
	MirrorS bool
	MaskS   int
	ShiftS  int

	SL, TL, SH, TH int
}

// keyChan holds one chroma-key channel's coefficients.
type keyChan struct {
	Width  int // s7.4 fixed (12 bits)
	Center int
	Scale  int
}

// RDP is the display processor state.
type RDP struct {
	dram []byte // attached RDRAM
	dmem []byte // attached RSP DMEM (the XBUS source)

	// DP command registers.
	start      uint32
	end        uint32
	current    uint32
	xbus       bool // false: fetch from RDRAM; true: DMEM
	freeze     bool
	flush      bool
	startValid bool
	endValid   bool
	clock      uint32 // activity counter: words fetched

	// OnInterrupt drives the MI DP bit when a FULLSYNC
	// command executes. It is a level: FULLSYNC raises
	// it, MI_MODE bit 11 (on the bus) clears it.
	OnInterrupt func(active bool)

	colorImg Image
	texImg   Image
	zAddr    uint32

	tiles [8]Tile
	tmem  [4096]byte

	otherModes uint64 // Set Other Modes register (bits 55..0)
	combineReg uint64 // Set Combine Mode register (bits 55..0)

	primColor  RGBA
	envColor   RGBA
	blendColor RGBA
	fogColor   RGBA
	primMinLvl int // Set Prim Color: minimum LOD clamp (0..31)
	primLvlFrc int // Set Prim Color: LOD fraction (0..255)

	fillColor uint32
	primZ     uint16
	primDZ    uint16

	conv [6]int32 // Set Convert coefficients (signed 1.7)

	keyR  keyChan
	keyGB keyGBRegs

	sciX0, sciY0 int // scissor box, 10.2 raw units
	sciX1, sciY1 int
	sciField     bool // scissor field enable
	sciOdd       bool // keep odd lines when set

	// hidden holds the per-pixel coverage bits for
	// 16-bit color images, which live in a separate
	// on-chip store on hardware rather than in RDRAM.
	// Indexed by RDRAM byte address / 2. Values 0..8.
	hidden []uint8
}

type keyGBRegs struct {
	WidthG, WidthB   int
	CenterG, CenterB int
	ScaleG, ScaleB   int
}

// New returns an RDP in its power-on state. The
// scissor box starts at the full 10.2 coordinate range
// so lists that never program it draw unclipped.
func New() *RDP {
	return &RDP{sciX1: 0xFFF, sciY1: 0xFFF}
}

// AttachDRAM gives the RDP its RDRAM backing store and
// sizes the hidden coverage store to match (one entry
// per 16-bit pixel slot).
func (r *RDP) AttachDRAM(dram []byte) {
	r.dram = dram
	r.hidden = make([]uint8, len(dram)/2)
}

// AttachDMEM gives the command processor the RSP DMEM
// slice used as the XBUS command source.
func (r *RDP) AttachDMEM(dmem []byte) { r.dmem = dmem }

// ReadRegister reads a DP register by byte offset from
// 0x04100000.
func (r *RDP) ReadRegister(off uint32) uint32 {
	switch off {
	case RegStart:
		return r.start
	case RegEnd:
		return r.end
	case RegCurrent:
		return r.current
	case RegStatus:
		var s uint32 = StatusGClkAlive
		if r.xbus {
			s |= StatusXBusDMEM
		}
		if r.freeze {
			s |= StatusFreeze
		}
		if r.flush {
			s |= StatusFlush
		}
		if r.endValid {
			s |= StatusEndValid
		}
		if r.startValid {
			s |= StatusStartVal
		}
		// TMEM busy, pipeline busy, command busy, buffer
		// ready, and DMA busy all read 0: execution is
		// synchronous in this model.
		return s
	case RegClock:
		return r.clock
	}
	return 0 // buffer busy, pipe busy, and TMEM counters
}

// WriteRegister writes a DP register by byte offset.
func (r *RDP) WriteRegister(off uint32, val uint32) {
	switch off {
	case RegStart:
		r.start = val & 0xFFFFFF
		r.startValid = true
	case RegEnd:
		r.end = val & 0xFFFFFF
		r.endValid = true
		r.maybeExecute()
	case RegStatus:
		r.writeStatus(val)
	}
}

// writeStatus applies the documented DP_STATUS write
// pairs: each function has a clear bit and a set bit.
func (r *RDP) writeStatus(val uint32) {
	if val&(1<<0) != 0 {
		r.xbus = false
	}
	if val&(1<<1) != 0 {
		r.xbus = true
	}
	if val&(1<<2) != 0 {
		wasFrozen := r.freeze
		r.freeze = false
		if wasFrozen {
			r.maybeExecute()
		}
	}
	if val&(1<<3) != 0 {
		r.freeze = true
	}
	if val&(1<<4) != 0 {
		r.flush = false
	}
	if val&(1<<5) != 0 {
		r.flush = true
	}
	// Bits 6..8 clear the TMEM, pipe, and command
	// counters (only the clock is modeled, by bit 9).
	if val&(1<<9) != 0 {
		r.clock = 0
	}
}

// ReadCPURegister reads a DP register in RSP CP0 index
// form (registers 8..15 map to offsets 0x00..0x1C).
func (r *RDP) ReadCPURegister(reg int) uint32 {
	if reg < 8 || reg > 15 {
		return 0
	}
	return r.ReadRegister(uint32(reg-8) * 4)
}

// WriteCPURegister writes a DP register in RSP CP0
// index form.
func (r *RDP) WriteCPURegister(reg int, val uint32) {
	if reg < 8 || reg > 15 {
		return
	}
	r.WriteRegister(uint32(reg-8)*4, val)
}

// maybeExecute runs the pending list when both address
// registers are valid and the RDP is not frozen.
func (r *RDP) maybeExecute() {
	if r.freeze || !r.startValid || !r.endValid {
		return
	}
	r.execute(r.start, r.end)
}

// fetchWord reads one 64-bit command word from the
// active source at a byte address, wrapping inside the
// source (24-bit RDRAM space, 12-bit DMEM space).
func (r *RDP) fetchWord(addr uint32) uint64 {
	var v uint64
	if r.xbus {
		base := addr & 0xFFF
		for i := 0; i < 8; i++ {
			v = v<<8 | uint64(r.dmem[(base+uint32(i))&0xFFF])
		}
		return v
	}
	base := addr & 0xFFFFFF
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(r.dramByte(base+uint32(i)))
	}
	return v
}

// addrMask is the wrap mask of the active source.
func (r *RDP) addrMask() uint32 {
	if r.xbus {
		return 0xFFF
	}
	return 0xFFFFFF
}

// execute processes the command stream from start to
// end. Command lengths depend on the opcode; a stream
// that ends mid-command stops quietly (the truncated
// tail is treated as consumed), matching the undefined
// hardware behavior with a safe outcome.
func (r *RDP) execute(start, end uint32) {
	mask := r.addrMask()
	addr := start & mask
	end &= mask
	r.current = addr
	guard := 0
	for addr != end {
		guard++
		if guard > 1<<22 {
			break // runaway list protection
		}
		op := int(r.peekOp(addr))
		n := cmdWords(op, r, addr)
		if n <= 0 {
			n = 1
		}
		// Verify the whole command fits before end.
		fits := true
		a := addr
		for i := 1; i < n; i++ {
			a = (a + 8) & mask
			if a == end {
				fits = false
				break
			}
		}
		if !fits {
			break
		}
		words := make([]uint64, n)
		for i := 0; i < n; i++ {
			words[i] = r.fetchWord(addr)
			addr = (addr + 8) & mask
		}
		r.clock += uint32(n)
		r.dispatch(op, words)
		r.current = addr
	}
	r.current = end
}

// peekOp reads the opcode of the command at addr.
func (r *RDP) peekOp(addr uint32) uint32 {
	return uint32(r.fetchWord(addr) >> 56)
}

// cmdWords returns the length in words of the command
// with the given opcode at addr. Triangle lengths depend
// on the attribute bits in the opcode itself.
func cmdWords(op int, r *RDP, addr uint32) int {
	switch {
	case op >= CmdTriBase && op <= 0x0F:
		n := 4 // edge coefficients
		if op&0x04 != 0 {
			n += 8 // shade coefficients
		}
		if op&0x02 != 0 {
			n += 8 // texture coefficients
		}
		if op&0x01 != 0 {
			n += 2 // z coefficients
		}
		return n
	case op == CmdTexRect || op == CmdTexRectFlip:
		return 2
	}
	return 1
}

// dispatch executes one decoded command.
func (r *RDP) dispatch(op int, w []uint64) {
	switch {
	case op >= CmdTriBase && op <= 0x0F:
		r.cmdTriangle(op, w)
	case op == CmdNoOp, op == CmdSyncPipe, op == CmdSyncTile, op == CmdSyncLoad:
		// Ordering barriers only; synchronous execution
		// makes them no-ops.
	case op == CmdSyncFull:
		if r.OnInterrupt != nil {
			r.OnInterrupt(true)
		}
	case op == CmdTexRect || op == CmdTexRectFlip:
		r.cmdTexRect(op, w)
	default:
		r.dispatchState(op, w[0])
	}
}

// dramByte reads RDRAM with power-of-two wraparound.
func (r *RDP) dramByte(addr uint32) byte {
	if len(r.dram) == 0 {
		return 0
	}
	return r.dram[addr&(uint32(len(r.dram))-1)]
}

// dramWriteByte writes RDRAM with wraparound.
func (r *RDP) dramWriteByte(addr uint32, v byte) {
	if len(r.dram) == 0 {
		return
	}
	r.dram[addr&(uint32(len(r.dram))-1)] = v
}

// dramRead16 reads a big-endian halfword from RDRAM.
func (r *RDP) dramRead16(addr uint32) uint16 {
	return uint16(r.dramByte(addr))<<8 | uint16(r.dramByte(addr+1))
}

// dramWrite16 writes a big-endian halfword to RDRAM.
func (r *RDP) dramWrite16(addr uint32, v uint16) {
	r.dramWriteByte(addr, byte(v>>8))
	r.dramWriteByte(addr+1, byte(v))
}

// dramRead32 reads a big-endian word from RDRAM.
func (r *RDP) dramRead32(addr uint32) uint32 {
	return uint32(r.dramRead16(addr))<<16 | uint32(r.dramRead16(addr+2))
}

// dramWrite32 writes a big-endian word to RDRAM.
func (r *RDP) dramWrite32(addr uint32, v uint32) {
	r.dramWrite16(addr, uint16(v>>16))
	r.dramWrite16(addr+2, uint16(v))
}

// SerializeSize reports the byte size of a serialized
// RDP for the attached RDRAM size (the hidden coverage
// store scales with RDRAM).
func (r *RDP) SerializeSize() int {
	return 20 + // DP registers and clock
		36 + // image registers
		8*17*4 + // tiles
		8 + 8 + // other modes, combine
		16 + 8 + 4 + 8 + // colors, prim LOD, fill, prim depth
		24 + 36 + // convert, keys
		20 + // scissor
		4096 + // TMEM
		len(r.hidden)
}

// Serialize writes the RDP state into dst (at least
// SerializeSize bytes) and returns the byte count.
func (r *RDP) Serialize(dst []byte) int {
	w := &stateWriter{b: dst}
	w.u32(r.start)
	w.u32(r.end)
	w.u32(r.current)
	var fl uint32
	if r.xbus {
		fl |= 1 << 0
	}
	if r.freeze {
		fl |= 1 << 1
	}
	if r.flush {
		fl |= 1 << 2
	}
	if r.startValid {
		fl |= 1 << 3
	}
	if r.endValid {
		fl |= 1 << 4
	}
	w.u32(fl)
	w.u32(r.clock)
	writeImg := func(im Image) {
		w.u32(uint32(im.Format))
		w.u32(uint32(im.Size))
		w.u32(uint32(im.Width))
		w.u32(im.Addr)
	}
	writeImg(r.colorImg)
	writeImg(r.texImg)
	w.u32(r.zAddr)
	for i := range r.tiles {
		t := &r.tiles[i]
		vals := [17]uint32{
			uint32(t.Format), uint32(t.Size), uint32(t.Line), uint32(t.TMem),
			uint32(t.Palette), boolU(t.ClampT), boolU(t.MirrorT), uint32(t.MaskT),
			uint32(t.ShiftT), boolU(t.ClampS), boolU(t.MirrorS), uint32(t.MaskS),
			uint32(t.ShiftS), uint32(int32(t.SL)), uint32(int32(t.TL)),
			uint32(int32(t.SH)), uint32(int32(t.TH)),
		}
		for _, v := range vals {
			w.u32(v)
		}
	}
	w.u64(r.otherModes)
	w.u64(r.combineReg)
	writeRGBA := func(c RGBA) {
		w.u8(c.R)
		w.u8(c.G)
		w.u8(c.B)
		w.u8(c.A)
	}
	writeRGBA(r.primColor)
	writeRGBA(r.envColor)
	writeRGBA(r.blendColor)
	writeRGBA(r.fogColor)
	w.u32(uint32(r.primMinLvl))
	w.u32(uint32(r.primLvlFrc))
	w.u32(r.fillColor)
	w.u32(uint32(r.primZ))
	w.u32(uint32(r.primDZ))
	for _, k := range r.conv {
		w.u32(uint32(k))
	}
	w.u32(uint32(r.keyR.Width))
	w.u32(uint32(r.keyR.Center))
	w.u32(uint32(r.keyR.Scale))
	w.u32(uint32(r.keyGB.WidthG))
	w.u32(uint32(r.keyGB.WidthB))
	w.u32(uint32(r.keyGB.CenterG))
	w.u32(uint32(r.keyGB.ScaleG))
	w.u32(uint32(r.keyGB.CenterB))
	w.u32(uint32(r.keyGB.ScaleB))
	w.u32(uint32(int32(r.sciX0)))
	w.u32(uint32(int32(r.sciY0)))
	w.u32(uint32(int32(r.sciX1)))
	w.u32(uint32(int32(r.sciY1)))
	var sf uint32
	if r.sciField {
		sf |= 1
	}
	if r.sciOdd {
		sf |= 2
	}
	w.u32(sf)
	w.bytes(r.tmem[:])
	w.bytes(r.hidden)
	return w.n
}

// Deserialize restores a state written by Serialize.
func (r *RDP) Deserialize(in []byte) error {
	if len(in) < r.SerializeSize() {
		return errShortState
	}
	rd := &stateReader{b: in}
	r.start = rd.u32()
	r.end = rd.u32()
	r.current = rd.u32()
	fl := rd.u32()
	r.xbus = fl&(1<<0) != 0
	r.freeze = fl&(1<<1) != 0
	r.flush = fl&(1<<2) != 0
	r.startValid = fl&(1<<3) != 0
	r.endValid = fl&(1<<4) != 0
	r.clock = rd.u32()
	readImg := func() Image {
		return Image{Format: int(rd.u32()), Size: int(rd.u32()),
			Width: int(rd.u32()), Addr: rd.u32()}
	}
	r.colorImg = readImg()
	r.texImg = readImg()
	r.zAddr = rd.u32()
	for i := range r.tiles {
		v := make([]uint32, 17)
		for j := range v {
			v[j] = rd.u32()
		}
		r.tiles[i] = Tile{
			Format: int(v[0]), Size: int(v[1]), Line: int(v[2]), TMem: int(v[3]),
			Palette: int(v[4]), ClampT: v[5] != 0, MirrorT: v[6] != 0,
			MaskT: int(v[7]), ShiftT: int(v[8]), ClampS: v[9] != 0,
			MirrorS: v[10] != 0, MaskS: int(v[11]), ShiftS: int(v[12]),
			SL: int(int32(v[13])), TL: int(int32(v[14])),
			SH: int(int32(v[15])), TH: int(int32(v[16])),
		}
	}
	r.otherModes = rd.u64()
	r.combineReg = rd.u64()
	readRGBA := func() RGBA {
		return RGBA{rd.u8(), rd.u8(), rd.u8(), rd.u8()}
	}
	r.primColor = readRGBA()
	r.envColor = readRGBA()
	r.blendColor = readRGBA()
	r.fogColor = readRGBA()
	r.primMinLvl = int(rd.u32())
	r.primLvlFrc = int(rd.u32())
	r.fillColor = rd.u32()
	r.primZ = uint16(rd.u32())
	r.primDZ = uint16(rd.u32())
	for i := range r.conv {
		r.conv[i] = int32(rd.u32())
	}
	r.keyR = keyChan{Width: int(rd.u32()), Center: int(rd.u32()), Scale: int(rd.u32())}
	r.keyGB = keyGBRegs{
		WidthG: int(rd.u32()), WidthB: int(rd.u32()),
		CenterG: int(rd.u32()), ScaleG: int(rd.u32()),
		CenterB: int(rd.u32()), ScaleB: int(rd.u32()),
	}
	r.sciX0 = int(int32(rd.u32()))
	r.sciY0 = int(int32(rd.u32()))
	r.sciX1 = int(int32(rd.u32()))
	r.sciY1 = int(int32(rd.u32()))
	sf := rd.u32()
	r.sciField = sf&1 != 0
	r.sciOdd = sf&2 != 0
	copy(r.tmem[:], rd.bytes(4096))
	copy(r.hidden, rd.bytes(len(r.hidden)))
	return nil
}

func boolU(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

var errShortState = errShort{}

type errShort struct{}

func (errShort) Error() string { return "rdp: state too short" }

// stateWriter appends fixed-width big-endian values.
type stateWriter struct {
	b []byte
	n int
}

func (w *stateWriter) u8(v uint8)   { w.b[w.n] = v; w.n++ }
func (w *stateWriter) u32(v uint32) { binary.BigEndian.PutUint32(w.b[w.n:], v); w.n += 4 }
func (w *stateWriter) u64(v uint64) { binary.BigEndian.PutUint64(w.b[w.n:], v); w.n += 8 }
func (w *stateWriter) bytes(p []byte) {
	copy(w.b[w.n:], p)
	w.n += len(p)
}

// stateReader reads fixed-width big-endian values.
type stateReader struct {
	b []byte
	n int
}

func (s *stateReader) u8() uint8   { v := s.b[s.n]; s.n++; return v }
func (s *stateReader) u32() uint32 { v := binary.BigEndian.Uint32(s.b[s.n:]); s.n += 4; return v }
func (s *stateReader) u64() uint64 { v := binary.BigEndian.Uint64(s.b[s.n:]); s.n += 8; return v }
func (s *stateReader) bytes(n int) []byte {
	v := s.b[s.n : s.n+n]
	s.n += n
	return v
}
