// Package gpu implements the PlayStation GPU from
// the psx-spx GPU documentation: the GP0/GP1 command
// interface, the 1024x512 16-bit VRAM, immediate-mode
// software rasterization of every documented drawing
// primitive, semi-transparency, texture mapping with
// CLUT lookup, dithering, and conversion of the
// display area to an XRGB8888 framebuffer at the
// native display resolution.
//
// Rendering model (stated in the core README):
// commands render immediately in WriteGP0 order;
// there is no command FIFO delay or polygon timing
// model in this slice. The line walker is a standard
// integer Bresenham; psx-spx names the endpoints-
// inclusive behavior but does not pin the walker's
// subpixel phase, so exact mid-line pixel choice on
// near-diagonal lines is the one documented
// under-specification carried by this package.
// Everything else (scan conversion, edge exclusion,
// blending arithmetic, dither matrix, transfer
// parameter masking) follows the documentation as
// written.
package gpu

// VRAM dimensions in 16-bit entries.
const (
	VRAMWidth  = 1024
	VRAMHeight = 512
)

// Display size limits: width is one of 256, 320,
// 368, 512 or 640 by display mode; height is 240 or
// 480 (interlaced).
const (
	MaxWidth  = 640
	MaxHeight = 480
)

// ditherTable is the documented 4x4 dither matrix,
// indexed [y&3][x&3], added to 8-bit color channels
// before quantization to 5 bits.
var ditherTable = [4][4]int{
	{-4, 0, -3, 1},
	{2, -2, 3, -1},
	{-3, 1, -4, 0},
	{3, -1, 2, -2},
}

// texPage holds a texture page attribute set (from
// GP0(E1) or from a textured primitive's own texpage
// word).
type texPage struct {
	xBase    int // in halfwords: bits 0-3 times 64
	yBase    int // 0 or 256 (plus the 2 MB VRAM bit, ignored)
	semiMode int // bits 5-6 of E1
	colorFmt int // 0=4bit, 1=8bit, 2=15bit
}

// GPU is the graphics processor state.
type GPU struct {
	vram [VRAMWidth * VRAMHeight]uint16

	// Draw mode (GP0 E1) and environment.
	page       texPage
	dither     bool
	drawToDisp bool
	texDisable bool
	rectFlipX  bool
	rectFlipY  bool
	winMaskX   int
	winMaskY   int
	winOffX    int
	winOffY    int
	drawX1     int
	drawY1     int
	drawX2     int
	drawY2     int
	offX       int
	offY       int
	maskSet    bool
	maskCheck  bool
	texWindow  uint32 // raw E2 value, for GP1(10)

	// Display state (GP1).
	dispEnable bool
	dmaDir     int
	dispX      int
	dispY      int
	rangeX1    int
	rangeX2    int
	rangeY1    int
	rangeY2    int
	hres2      bool
	hres1      int
	vres480    bool
	pal        bool
	depth24    bool
	interlace  bool
	reverse    bool

	// Beam state, fed by the system timing model:
	// drives GPUSTAT bits 13 (field) and 31
	// (drawing odd lines).
	Field    int
	LineOdd  bool
	OddFrame bool // 480i frame parity: the field on display

	irq       bool // GPUSTAT bit 24
	readLatch uint32

	// Host-side diagnostic: GP1 command bytes with
	// no implemented case, kept across resets, never
	// serialized. (GP0 has no unknown commands: every
	// opcode byte names a documented command class.)
	unknownGP1  []uint8
	unknownGP1N int

	// GP0 command assembly.
	buf     [20]uint32
	bufLen  int
	bufNeed int
	cmd     uint8

	// Polyline capture.
	polyline    bool
	polyGouraud bool
	polyStage   int // 0 = group start word, 1 = vertex after a gouraud color
	polyPrevX   int
	polyPrevY   int
	polyPrevC   [3]int
	polyColor   [3]int

	// VRAM transfer state.
	xferActive bool
	xferRead   bool // true: VRAM to CPU
	xferX      int
	xferY      int
	xferW      int
	xferH      int
	xferCol    int
	xferRow    int
	xferHalf   bool   // low halfword of the current word consumed
	xferPend   uint16 // pending low halfword for CPU to VRAM writes
	xferHasLo  bool

	displayBuf []uint32
}

// New returns a GPU in its power-on state (the
// documented GP1(00) reset defaults).
func New() *GPU {
	g := &GPU{}
	g.Reset()
	return g
}

// Reset applies the documented GP1(00) reset
// defaults: display off, DMA off, display area at 0,
// horizontal range 0x200 to 0xC00, vertical range
// 0x10 to 0x100, display mode 0, and all GP0
// environment registers cleared. GPUSTAT then reads
// 0x14802000. VRAM contents are preserved, as on
// hardware.
func (g *GPU) Reset() {
	vram := g.vram
	unk, unkN := g.unknownGP1, g.unknownGP1N
	*g = GPU{vram: vram, unknownGP1: unk, unknownGP1N: unkN}
	g.dispEnable = false
	g.rangeX1 = 0x200
	g.rangeX2 = 0x200 + 256*10
	g.rangeY1 = 0x010
	g.rangeY2 = 0x010 + 240
	g.displayBuf = make([]uint32, MaxWidth*MaxHeight)
}

// VRAM exposes the framebuffer memory (row major,
// VRAMWidth entries per row).
func (g *GPU) VRAM() []uint16 { return g.vram[:] }

// VRAMAt reads one VRAM entry (coordinates wrap).
func (g *GPU) VRAMAt(x, y int) uint16 {
	return g.vram[(y&(VRAMHeight-1))*VRAMWidth+(x&(VRAMWidth-1))]
}

// SetVRAM writes one VRAM entry directly (test and
// transfer helper; coordinates wrap).
func (g *GPU) SetVRAM(x, y int, v uint16) {
	g.vram[(y&(VRAMHeight-1))*VRAMWidth+(x&(VRAMWidth-1))] = v
}

// IRQRequested reports GPUSTAT bit 24.
func (g *GPU) IRQRequested() bool { return g.irq }

// DisplayEnabled reports whether the display is on.
func (g *GPU) DisplayEnabled() bool { return g.dispEnable }

// PAL reports the display mode video standard.
func (g *GPU) PAL() bool { return g.pal }

// DisplayWidth returns the native horizontal
// resolution from the display mode.
func (g *GPU) DisplayWidth() int {
	if g.hres2 {
		return 368
	}
	switch g.hres1 {
	case 0:
		return 256
	case 1:
		return 320
	case 2:
		return 512
	default:
		return 640
	}
}

// DotDivider is the video clock divider producing
// the dotclock for the current horizontal
// resolution (256: /10, 320: /8, 368: /7, 512: /5,
// 640: /4).
func (g *GPU) DotDivider() int {
	switch g.DisplayWidth() {
	case 256:
		return 10
	case 320:
		return 8
	case 368:
		return 7
	case 512:
		return 5
	default:
		return 4
	}
}

// DisplayHeight returns 480 in interlaced vertical
// 480 mode, else 240.
func (g *GPU) DisplayHeight() int {
	if g.vres480 && g.interlace {
		return 480
	}
	return 240
}

// HDisplayRange returns the horizontal display range
// in video clock units (for the system timing model).
func (g *GPU) HDisplayRange() (int, int) { return g.rangeX1, g.rangeX2 }

// VDisplayRange returns the vertical display range
// in scanlines.
func (g *GPU) VDisplayRange() (int, int) { return g.rangeY1, g.rangeY2 }

// DisplayAreaStart returns the display area origin
// in VRAM halfwords/lines.
func (g *GPU) DisplayAreaStart() (int, int) { return g.dispX, g.dispY }

// Status builds GPUSTAT.
func (g *GPU) Status() uint32 {
	var s uint32
	s |= uint32(g.page.xBase/64) & 0xF
	if g.page.yBase&256 != 0 {
		s |= 1 << 4
	}
	s |= uint32(g.page.semiMode&3) << 5
	s |= uint32(g.page.colorFmt&3) << 7
	if g.dither {
		s |= 1 << 9
	}
	if g.drawToDisp {
		s |= 1 << 10
	}
	if g.maskSet {
		s |= 1 << 11
	}
	if g.maskCheck {
		s |= 1 << 12
	}
	// Bit 13: interlace field; on this GPU version it
	// reads 1 when interlace is off.
	if !g.interlace || g.Field == 1 {
		s |= 1 << 13
	}
	if g.reverse {
		s |= 1 << 14
	}
	if g.page.yBase&512 != 0 {
		s |= 1 << 15
	}
	if g.hres2 {
		s |= 1 << 16
	}
	s |= uint32(g.hres1&3) << 17
	if g.vres480 {
		s |= 1 << 19
	}
	if g.pal {
		s |= 1 << 20
	}
	if g.depth24 {
		s |= 1 << 21
	}
	if g.interlace {
		s |= 1 << 22
	}
	if !g.dispEnable {
		s |= 1 << 23
	}
	if g.irq {
		s |= 1 << 24
	}
	// Ready bits in the immediate model: the GPU is
	// ready to receive commands and DMA blocks unless
	// a VRAM to CPU transfer is pending readback
	// (bit 27 is set exactly while it is).
	pendingRead := g.xferActive && g.xferRead
	readyCmd := !pendingRead
	readyDMA := readyCmd
	if pendingRead {
		s |= 1 << 27
	}
	if readyCmd {
		s |= 1 << 26
	}
	if readyDMA {
		s |= 1 << 28
	}
	// Bit 25 (data request) follows the DMA
	// direction: off = always 0, FIFO = FIFO not full
	// (always in this model), CPU to GP0 = bit 28,
	// GPUREAD to CPU = bit 27.
	switch g.dmaDir {
	case 1:
		s |= 1 << 25
	case 2:
		if readyDMA {
			s |= 1 << 25
		}
	case 3:
		if pendingRead {
			s |= 1 << 25
		}
	}
	s |= uint32(g.dmaDir&3) << 29
	if g.LineOdd {
		s |= 1 << 31
	}
	return s
}

// UnknownGP1Commands returns the distinct GP1
// command bytes received that have no implemented
// case, in first-seen order. Host-side diagnostic
// only: the log survives resets and is not part of
// the serialized state.
func (g *GPU) UnknownGP1Commands() []uint8 {
	out := make([]uint8, len(g.unknownGP1))
	copy(out, g.unknownGP1)
	return out
}

// UnknownGP1Count returns the total number of
// unimplemented GP1 commands received, repeats
// included.
func (g *GPU) UnknownGP1Count() int { return g.unknownGP1N }

func (g *GPU) noteUnknownGP1(cmd uint8) {
	g.unknownGP1N++
	for _, u := range g.unknownGP1 {
		if u == cmd {
			return
		}
	}
	g.unknownGP1 = append(g.unknownGP1, cmd)
}

// WriteGP1 executes a GP1 command word.
func (g *GPU) WriteGP1(w uint32) {
	cmd := w >> 24
	param := w & 0xFFFFFF
	switch cmd {
	case 0x00: // reset
		vram := g.vram
		disp := g.displayBuf
		unk, unkN := g.unknownGP1, g.unknownGP1N
		*g = GPU{vram: vram, displayBuf: disp, unknownGP1: unk, unknownGP1N: unkN}
		g.dispEnable = false
		g.rangeX1 = 0x200
		g.rangeX2 = 0x200 + 256*10
		g.rangeY1 = 0x010
		g.rangeY2 = 0x010 + 240
	case 0x01: // reset command buffer
		g.bufLen = 0
		g.bufNeed = 0
		g.polyline = false
		g.xferActive = false
	case 0x02: // acknowledge interrupt
		g.irq = false
	case 0x03: // display enable (bit 0: 0 on, 1 off)
		g.dispEnable = param&1 == 0
	case 0x04: // DMA direction
		g.dmaDir = int(param & 3)
	case 0x05: // display area start
		g.dispX = int(param & 0x3FF)
		g.dispY = int((param >> 10) & 0x1FF)
	case 0x06: // horizontal display range
		g.rangeX1 = int(param & 0xFFF)
		g.rangeX2 = int((param >> 12) & 0xFFF)
	case 0x07: // vertical display range
		g.rangeY1 = int(param & 0x3FF)
		g.rangeY2 = int((param >> 10) & 0x3FF)
	case 0x08: // display mode
		g.hres1 = int(param & 3)
		g.vres480 = param&4 != 0
		g.pal = param&8 != 0
		g.depth24 = param&16 != 0
		g.interlace = param&32 != 0
		g.hres2 = param&64 != 0
		g.reverse = param&128 != 0
	case 0x09: // VRAM size (v2 register, stored only in hardware effect)
		// Accepted and ignored: our VRAM is always 1 MB.
	case 0x10: // read internal register
		switch param & 0xF {
		case 2:
			g.readLatch = g.texWindow
		case 3:
			g.readLatch = uint32(g.drawX1) | uint32(g.drawY1)<<10
		case 4:
			g.readLatch = uint32(g.drawX2) | uint32(g.drawY2)<<10
		case 5:
			g.readLatch = uint32(g.offX&0x7FF) | uint32(g.offY&0x7FF)<<11
		case 7:
			g.readLatch = 2 // GPU version (v2)
		}
	default:
		// Not a documented GP1 command for this
		// GPU: ignored, but recorded for the
		// host-side diagnostic log.
		g.noteUnknownGP1(uint8(cmd))
	}
}

// ReadData services GPUREAD: VRAM to CPU transfer
// words first, then the GP1(10) latch.
func (g *GPU) ReadData() uint32 {
	if g.xferActive && g.xferRead {
		lo := g.xferNextHalf()
		hi := g.xferNextHalf()
		return uint32(lo) | uint32(hi)<<16
	}
	return g.readLatch
}

// xferNextHalf returns the next halfword of a VRAM
// to CPU transfer and advances its cursor; when the
// last halfword is consumed the transfer ends.
func (g *GPU) xferNextHalf() uint16 {
	v := g.VRAMAt(g.xferX+g.xferCol, g.xferY+g.xferRow)
	g.xferCol++
	if g.xferCol >= g.xferW {
		g.xferCol = 0
		g.xferRow++
		if g.xferRow >= g.xferH {
			g.xferActive = false
		}
	}
	return v
}

// Framebuffer renders the display area into an
// XRGB8888 slice at native resolution and returns
// it along with its dimensions. In interlaced 480
// mode the full 480 lines are returned, starting at
// the display area offset by the current field
// parity (the simplification stated in the README).
// A disabled display returns black.
func (g *GPU) Framebuffer() ([]uint32, int, int) {
	w, h := g.DisplayWidth(), g.DisplayHeight()
	out := g.displayBuf[:w*h]
	for i := range out {
		out[i] = 0xFF000000
	}
	if !g.dispEnable {
		return out, w, h
	}
	yOff := 0
	if g.interlace && g.vres480 && g.OddFrame {
		yOff = 1
	}
	if g.depth24 {
		g.blit24(out, w, h, yOff)
	} else {
		g.blit15(out, w, h, yOff)
	}
	return out, w, h
}

func (g *GPU) blit15(out []uint32, w, h, yOff int) {
	for y := 0; y < h; y++ {
		sy := (g.dispY + yOff + y) & (VRAMHeight - 1)
		row := sy * VRAMWidth
		for x := 0; x < w; x++ {
			sx := (g.dispX + x) & (VRAMWidth - 1)
			out[y*w+x] = expand15(g.vram[row+sx])
		}
	}
}

func expand15(v uint16) uint32 {
	r := uint32(v & 0x1F)
	gr := uint32((v >> 5) & 0x1F)
	b := uint32((v >> 10) & 0x1F)
	r = (r << 3) | (r >> 2)
	gr = (gr << 3) | (gr >> 2)
	b = (b << 3) | (b >> 2)
	return 0xFF000000 | r<<16 | gr<<8 | b
}

func (g *GPU) blit24(out []uint32, w, h, yOff int) {
	for y := 0; y < h; y++ {
		sy := (g.dispY + yOff + y) & (VRAMHeight - 1)
		base := sy * VRAMWidth * 2 // byte offset of the VRAM row
		for x := 0; x < w; x++ {
			off := base + g.dispX*2 + x*3
			r := g.vramByte(off)
			gr := g.vramByte(off + 1)
			b := g.vramByte(off + 2)
			out[y*w+x] = 0xFF000000 | uint32(r)<<16 | uint32(gr)<<8 | uint32(b)
		}
	}
}

// vramByte reads one byte of VRAM by byte offset,
// wrapping at 1 MB.
func (g *GPU) vramByte(off int) uint8 {
	off &= (VRAMWidth * VRAMHeight * 2) - 1
	hw := g.vram[off>>1]
	if off&1 == 0 {
		return uint8(hw)
	}
	return uint8(hw >> 8)
}

func packColor(c [3]int) uint32 {
	return uint32(c[0]) | uint32(c[1])<<8 | uint32(c[2])<<16
}

func unpackColor(w uint32) [3]int {
	return [3]int{int(w & 0xFF), int((w >> 8) & 0xFF), int((w >> 16) & 0xFF)}
}

// StateSize is the serialized size of the GPU:
// VRAM plus a fixed field block.
const StateSize = VRAMWidth*VRAMHeight*2 + 2048

// Serialize writes the GPU state.
func (g *GPU) Serialize() []byte {
	out := make([]byte, StateSize)
	for i, v := range g.vram {
		out[2*i] = byte(v)
		out[2*i+1] = byte(v >> 8)
	}
	o := out[VRAMWidth*VRAMHeight*2:]
	put16 := func(off int, v uint16) { o[off], o[off+1] = byte(v), byte(v>>8) }
	put32 := func(off int, v uint32) {
		o[off], o[off+1], o[off+2], o[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	put32(0, g.texWindow)
	put32(4, uint32(g.page.xBase)|uint32(g.page.yBase)<<16)
	o[8] = byte(g.page.semiMode) | byte(g.page.colorFmt)<<2
	flags := byte(0)
	set := func(bit byte, on bool) {
		if on {
			flags |= 1 << bit
		}
	}
	set(0, g.dither)
	set(1, g.drawToDisp)
	set(2, g.texDisable)
	set(3, g.rectFlipX)
	set(4, g.rectFlipY)
	set(5, g.maskSet)
	set(6, g.maskCheck)
	set(7, g.dispEnable)
	o[9] = flags
	put16(10, uint16(g.winMaskX)|uint16(g.winMaskY)<<8)
	put16(12, uint16(g.winOffX)|uint16(g.winOffY)<<8)
	put16(14, uint16(g.drawX1))
	put16(16, uint16(g.drawY1))
	put16(18, uint16(g.drawX2))
	put16(20, uint16(g.drawY2))
	put16(22, uint16(g.offX))
	put16(24, uint16(g.offY))
	put16(26, uint16(g.dispX))
	put16(28, uint16(g.dispY))
	put16(30, uint16(g.rangeX1))
	put16(32, uint16(g.rangeX2))
	put16(34, uint16(g.rangeY1))
	put16(36, uint16(g.rangeY2))
	o[38] = byte(g.dmaDir) | byte(g.hres1)<<2
	f2 := byte(0)
	set2 := func(bit byte, on bool) {
		if on {
			f2 |= 1 << bit
		}
	}
	set2(0, g.hres2)
	set2(1, g.vres480)
	set2(2, g.pal)
	set2(3, g.depth24)
	set2(4, g.interlace)
	set2(5, g.reverse)
	set2(6, g.irq)
	set2(7, g.OddFrame)
	o[39] = f2
	put32(40, g.readLatch)
	o[44] = byte(g.bufLen)
	o[45] = byte(g.bufNeed)
	o[46] = g.cmd
	for i := 0; i < g.bufLen && i < len(g.buf); i++ {
		put32(48+i*4, g.buf[i])
	}
	o[128] = 0
	if g.polyline {
		o[128] |= 1
	}
	if g.polyGouraud {
		o[128] |= 2
	}
	o[129] = byte(g.polyStage)
	put16(130, uint16(g.polyPrevX))
	put16(132, uint16(g.polyPrevY))
	put32(134, packColor(g.polyPrevC))
	put32(156, packColor(g.polyColor))
	o[138] = 0
	if g.xferActive {
		o[138] |= 1
	}
	if g.xferRead {
		o[138] |= 2
	}
	if g.xferHasLo {
		o[138] |= 4
	}
	put16(140, uint16(g.xferX))
	put16(142, uint16(g.xferY))
	put16(144, uint16(g.xferW))
	put16(146, uint16(g.xferH))
	put16(148, uint16(g.xferCol))
	put16(150, uint16(g.xferRow))
	put16(152, g.xferPend)
	o[154] = byte(g.Field)
	if g.LineOdd {
		o[155] = 1
	}
	return out
}

// Deserialize restores a state produced by Serialize.
func (g *GPU) Deserialize(in []byte) {
	for i := range g.vram {
		g.vram[i] = uint16(in[2*i]) | uint16(in[2*i+1])<<8
	}
	o := in[VRAMWidth*VRAMHeight*2:]
	get16 := func(off int) uint16 { return uint16(o[off]) | uint16(o[off+1])<<8 }
	get32 := func(off int) uint32 {
		return uint32(o[off]) | uint32(o[off+1])<<8 | uint32(o[off+2])<<16 | uint32(o[off+3])<<24
	}
	g.texWindow = get32(0)
	p := get32(4)
	g.page.xBase = int(p & 0xFFFF)
	g.page.yBase = int(p >> 16)
	g.page.semiMode = int(o[8] & 3)
	g.page.colorFmt = int((o[8] >> 2) & 3)
	flags := o[9]
	g.dither = flags&1 != 0
	g.drawToDisp = flags&2 != 0
	g.texDisable = flags&4 != 0
	g.rectFlipX = flags&8 != 0
	g.rectFlipY = flags&16 != 0
	g.maskSet = flags&32 != 0
	g.maskCheck = flags&64 != 0
	g.dispEnable = flags&128 != 0
	m := get16(10)
	g.winMaskX, g.winMaskY = int(m&0xFF), int(m>>8)
	m = get16(12)
	g.winOffX, g.winOffY = int(m&0xFF), int(m>>8)
	g.drawX1 = int(get16(14))
	g.drawY1 = int(get16(16))
	g.drawX2 = int(get16(18))
	g.drawY2 = int(get16(20))
	g.offX = int(int16(get16(22)))
	g.offY = int(int16(get16(24)))
	g.dispX = int(get16(26))
	g.dispY = int(get16(28))
	g.rangeX1 = int(get16(30))
	g.rangeX2 = int(get16(32))
	g.rangeY1 = int(get16(34))
	g.rangeY2 = int(get16(36))
	g.dmaDir = int(o[38] & 3)
	g.hres1 = int((o[38] >> 2) & 3)
	f2 := o[39]
	g.hres2 = f2&1 != 0
	g.vres480 = f2&2 != 0
	g.pal = f2&4 != 0
	g.depth24 = f2&8 != 0
	g.interlace = f2&16 != 0
	g.reverse = f2&32 != 0
	g.irq = f2&64 != 0
	g.OddFrame = f2&128 != 0
	g.readLatch = get32(40)
	g.bufLen = int(o[44])
	g.bufNeed = int(o[45])
	g.cmd = o[46]
	for i := range g.buf {
		g.buf[i] = 0
	}
	for i := 0; i < g.bufLen && i < len(g.buf); i++ {
		g.buf[i] = get32(48 + i*4)
	}
	g.polyline = o[128]&1 != 0
	g.polyGouraud = o[128]&2 != 0
	g.polyStage = int(o[129])
	g.polyPrevX = int(int16(get16(130)))
	g.polyPrevY = int(int16(get16(132)))
	g.polyPrevC = unpackColor(get32(134))
	g.polyColor = unpackColor(get32(156))
	g.xferActive = o[138]&1 != 0
	g.xferRead = o[138]&2 != 0
	g.xferHasLo = o[138]&4 != 0
	g.xferX = int(get16(140))
	g.xferY = int(get16(142))
	g.xferW = int(get16(144))
	g.xferH = int(get16(146))
	g.xferCol = int(get16(148))
	g.xferRow = int(get16(150))
	g.xferPend = get16(152)
	g.Field = int(o[154])
	g.LineOdd = o[155] != 0
}
