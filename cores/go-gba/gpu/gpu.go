// Package gpu is the GBA picture processor, built from
// documented register and pixel behaviour. It works one
// scanline at a time: scroll values are latched at the
// start of a line, the whole line is then sampled, and
// the affine reference points advance by their per line
// steps once the line is done.
//
// Pixel selection keeps the top two candidates for each
// output pixel so that colour effects can blend the
// winner with whatever sits directly underneath it.
package gpu

// Screen dimensions in pixels.
const (
	Width  = 240
	Height = 160
)

// Serialized layout, all integers little endian:
//
//	0    96 bytes  stored register bytes, offsets 0x00 to 0x5F
//	96   16 bytes  latched scroll values, H then V for BG0 to BG3
//	112  16 bytes  live affine references, BG2 X/Y then BG3 X/Y
//	128  4 bytes   current line as int32
//	132  1 byte    flags, bit 0 hblank, bit 1 vblank
//	133  3 bytes   zero padding
//	136  rest      framebuffer, Width*Height uint32 pixels
const StateSize = 136 + Width*Height*4

// Layer identifiers, chosen so the identifier is also
// the bit position used by the window and blending
// registers: BG0 to BG3 are 0 to 3, OBJ is 4, and the
// backdrop is 5.
const (
	layerBG0 = 0
	layerOBJ = 4
	layerBD  = 5
)

// Register offsets, relative to the display IO base.
const (
	regDISPCNT  = 0x00
	regDISPSTAT = 0x04
	regVCOUNT   = 0x06
	regBG0CNT   = 0x08
	regMOSAIC   = 0x4C
	regBLDCNT   = 0x50
	regBLDALPHA = 0x52
	regBLDY     = 0x54
)

// GPU is one display unit. The palette, video RAM and
// sprite memory are slices owned by the caller (on real
// wiring, the bus backing stores); the GPU only reads
// them while rendering.
type GPU struct {
	palette []byte
	vram    []byte
	oam     []byte

	regs     [0x60]byte
	latchedH [4]uint16
	latchedV [4]uint16

	// Live affine reference points, held as signed
	// 28 bit values with 8 fractional bits.
	refX [2]int32
	refY [2]int32

	line   int
	hblank bool
	vblank bool

	frame []uint32
}

// New returns a GPU in power-on state reading from the
// given backing stores.
func New(palette, vram, oam []byte) *GPU {
	g := &GPU{palette: palette, vram: vram, oam: oam}
	g.frame = make([]uint32, Width*Height)
	g.Reset()
	return g
}

// Reset clears every register and internal counter and
// returns the framebuffer to opaque black. The external
// backing stores are not touched.
func (g *GPU) Reset() {
	for i := range g.regs {
		g.regs[i] = 0
	}
	for i := range g.latchedH {
		g.latchedH[i] = 0
		g.latchedV[i] = 0
	}
	for i := range g.refX {
		g.refX[i] = 0
		g.refY[i] = 0
	}
	g.line = 0
	g.hblank = false
	g.vblank = false
	if g.frame == nil {
		g.frame = make([]uint32, Width*Height)
	}
	for i := range g.frame {
		g.frame[i] = 0xFF000000
	}
}

// Framebuffer returns the GPU owned pixel buffer in
// XRGB8888 form.
func (g *GPU) Framebuffer() []uint32 { return g.frame }

// Line returns the current scanline number.
func (g *GPU) Line() int { return g.line }

// SetLine sets the current scanline number, which is
// what the VCOUNT register reports.
func (g *GPU) SetLine(line int) { g.line = line }

// SetHBlank drives the hblank status flag.
func (g *GPU) SetHBlank(on bool) { g.hblank = on }

// SetVBlank drives the vblank status flag.
func (g *GPU) SetVBlank(on bool) { g.vblank = on }

// VCountMatch reports whether the current line equals
// the compare value stored in the status register.
func (g *GPU) VCountMatch() bool {
	lyc := int((g.reg16(regDISPSTAT) >> 8) & 0xFF)
	return g.line == lyc
}

// Small register access helpers.

func (g *GPU) reg16(off uint32) uint16 {
	if off+1 >= uint32(len(g.regs)) {
		return 0
	}
	return uint16(g.regs[off]) | uint16(g.regs[off+1])<<8
}

func (g *GPU) setReg16(off uint32, v uint16) {
	if off+1 >= uint32(len(g.regs)) {
		return
	}
	g.regs[off] = byte(v)
	g.regs[off+1] = byte(v >> 8)
}

func (g *GPU) reg32(off uint32) uint32 {
	return uint32(g.reg16(off)) | uint32(g.reg16(off+2))<<16
}

// sign28 interprets the low 28 bits of v as a signed
// value and returns it sign extended into an int32.
func sign28(v uint32) int32 {
	n := int32(v & 0x0FFFFFFF)
	if n&(1<<27) != 0 {
		n -= 1 << 28
	}
	return n
}

// refInfoFor maps any byte offset inside one of the
// 32 bit affine reference registers to that register:
// its base offset, which background (0 for BG2, 1 for
// BG3) and which coordinate (0 for X, 1 for Y).
func refInfoFor(off uint32) (base uint32, bg int, coord int, ok bool) {
	switch {
	case off >= 0x28 && off < 0x2C:
		return 0x28, 0, 0, true
	case off >= 0x2C && off < 0x30:
		return 0x2C, 0, 1, true
	case off >= 0x38 && off < 0x3C:
		return 0x38, 1, 0, true
	case off >= 0x3C && off < 0x40:
		return 0x3C, 1, 1, true
	}
	return 0, 0, 0, false
}

// syncRef reloads one live reference point from its
// stored 32 bit register value.
func (g *GPU) syncRef(bg, coord int) {
	var base uint32
	switch {
	case bg == 0 && coord == 0:
		base = 0x28
	case bg == 0:
		base = 0x2C
	case coord == 0:
		base = 0x38
	default:
		base = 0x3C
	}
	v := sign28(g.reg32(base))
	if coord == 0 {
		g.refX[bg] = v
	} else {
		g.refY[bg] = v
	}
}

// isRefHalf reports whether a 16 bit register offset is
// one half of an affine reference register.
func isRefHalf(off uint32) bool {
	switch off {
	case 0x28, 0x2A, 0x2C, 0x2E, 0x38, 0x3A, 0x3C, 0x3E:
		return true
	}
	return false
}

// read16 resolves one 16 bit register read, applying the
// read restrictions of the status, count, scroll and
// affine reference registers.
func (g *GPU) read16(off uint32) uint16 {
	if off >= 0x60 {
		return 0
	}
	switch {
	case off == regDISPSTAT:
		stored := g.reg16(off) & 0xFF38
		var live uint16
		if g.vblank {
			live |= 1 << 0
		}
		if g.hblank {
			live |= 1 << 1
		}
		if g.VCountMatch() {
			live |= 1 << 2
		}
		return stored | live
	case off == regVCOUNT:
		return uint16(g.line)
	case off >= 0x10 && off < 0x20:
		return 0 // scroll registers read back as zero
	case isRefHalf(off):
		return 0 // affine reference points read back as zero
	}
	return g.reg16(off)
}

// ReadReg reads size bytes (1, 2 or 4) at a register
// offset. Wide and byte reads are composed from the
// underlying 16 bit registers, little endian.
func (g *GPU) ReadReg(off uint32, size int) uint32 {
	switch size {
	case 1:
		v := uint32(g.read16(off &^ 1))
		if off&1 != 0 {
			return v >> 8
		}
		return v & 0xFF
	case 2:
		return uint32(g.read16(off))
	case 4:
		return uint32(g.read16(off)) | uint32(g.read16(off+2))<<16
	}
	return 0
}

func (g *GPU) write8(off uint32, val uint32) {
	if off >= 0x60 {
		return
	}
	aligned := off &^ 1
	if aligned == regVCOUNT {
		return
	}
	if aligned == regDISPSTAT {
		if off == regDISPSTAT {
			// Low byte: only the three IRQ enable bits
			// (bits 3 to 5) are stored; the flag bits
			// are driven by the timing setters.
			g.regs[off] = byte(val) & 0x38
		} else {
			// High byte is the line compare value.
			g.regs[off] = byte(val)
		}
		return
	}
	g.regs[off] = byte(val)
	if base, bg, coord, ok := refInfoFor(off); ok {
		_ = base
		g.syncRef(bg, coord)
	}
}

func (g *GPU) write16(off uint32, val uint32) {
	if off >= 0x60 {
		return
	}
	v := uint16(val)
	switch {
	case off == regVCOUNT:
		return
	case off == regDISPSTAT:
		g.setReg16(off, v&0xFF38)
		return
	}
	g.setReg16(off, v)
	if _, bg, coord, ok := refInfoFor(off); ok {
		g.syncRef(bg, coord)
	}
}

// WriteReg writes size bytes (1, 2 or 4) at a register
// offset. Wide writes decompose into 16 bit writes and
// byte writes touch only their own lane, including for
// the write-only scroll and reference registers, whose
// stored values still update.
func (g *GPU) WriteReg(off uint32, size int, val uint32) {
	switch size {
	case 1:
		g.write8(off, val&0xFF)
	case 2:
		if off&1 != 0 {
			g.write8(off, val&0xFF)
			g.write8(off+1, (val>>8)&0xFF)
			return
		}
		g.write16(off, val&0xFFFF)
	case 4:
		g.WriteReg(off, 2, val&0xFFFF)
		g.WriteReg(off+2, 2, (val>>16)&0xFFFF)
	}
}

// BeginLine latches the text scroll offsets for the
// current line, so changes written part way through a
// line only take effect on the next one.
func (g *GPU) BeginLine() {
	for bg := 0; bg < 4; bg++ {
		base := uint32(0x10 + bg*4)
		g.latchedH[bg] = g.reg16(base) & 0x1FF
		g.latchedV[bg] = g.reg16(base+2) & 0x1FF
	}
}

// Serialize writes the full GPU state (see the layout
// documented at StateSize) into dst and returns the
// number of bytes used. It returns 0 if dst is short.
func (g *GPU) Serialize(dst []byte) int {
	if len(dst) < StateSize {
		return 0
	}
	put16 := func(off int, v uint16) {
		dst[off] = byte(v)
		dst[off+1] = byte(v >> 8)
	}
	put32 := func(off int, v uint32) {
		dst[off] = byte(v)
		dst[off+1] = byte(v >> 8)
		dst[off+2] = byte(v >> 16)
		dst[off+3] = byte(v >> 24)
	}
	copy(dst[0:96], g.regs[:])
	off := 96
	for bg := 0; bg < 4; bg++ {
		put16(off, g.latchedH[bg])
		off += 2
	}
	for bg := 0; bg < 4; bg++ {
		put16(off, g.latchedV[bg])
		off += 2
	}
	for i := 0; i < 2; i++ {
		put32(off, uint32(g.refX[i]))
		off += 4
		put32(off, uint32(g.refY[i]))
		off += 4
	}
	put32(off, uint32(int32(g.line)))
	off += 4
	var flags byte
	if g.hblank {
		flags |= 1
	}
	if g.vblank {
		flags |= 2
	}
	dst[off] = flags
	off++
	dst[off] = 0
	dst[off+1] = 0
	dst[off+2] = 0
	off += 3
	for i, px := range g.frame {
		put32(off+i*4, px)
	}
	return StateSize
}

// Deserialize restores a state written by Serialize.
// It reports false when the input is too short.
func (g *GPU) Deserialize(src []byte) bool {
	if len(src) < StateSize {
		return false
	}
	get16 := func(off int) uint16 {
		return uint16(src[off]) | uint16(src[off+1])<<8
	}
	get32 := func(off int) uint32 {
		return uint32(src[off]) | uint32(src[off+1])<<8 |
			uint32(src[off+2])<<16 | uint32(src[off+3])<<24
	}
	copy(g.regs[:], src[0:96])
	off := 96
	for bg := 0; bg < 4; bg++ {
		g.latchedH[bg] = get16(off)
		off += 2
	}
	for bg := 0; bg < 4; bg++ {
		g.latchedV[bg] = get16(off)
		off += 2
	}
	for i := 0; i < 2; i++ {
		g.refX[i] = int32(get32(off))
		off += 4
		g.refY[i] = int32(get32(off))
		off += 4
	}
	g.line = int(int32(get32(off)))
	off += 4
	flags := src[off]
	off++
	g.hblank = flags&1 != 0
	g.vblank = flags&2 != 0
	off += 3
	if g.frame == nil {
		g.frame = make([]uint32, Width*Height)
	}
	for i := range g.frame {
		g.frame[i] = get32(off + i*4)
	}
	return true
}
