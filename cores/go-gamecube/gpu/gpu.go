// Package gpu is a self-contained software renderer for the
// Flipper GX graphics processor used in the GameCube, written
// from published GX documentation (the YAGCD GX chapters and
// public GX register references). It consumes GX FIFO byte
// streams, fetches indexed vertex data and texture images from
// main memory through the Memory interface, and rasterizes
// into an embedded frame buffer (EFB) of 640x528 pixels with
// a 24-bit depth buffer.
//
// Scope for this slice (GCN-2): one texture stage fully
// sampled, the documented TEV stage 0 default combine (vertex
// color modulated by the texel), a scope-limited diffuse
// lighting path for light 0, the full per-pixel pipeline
// (alpha test, depth test, blending, logic ops, color and
// alpha update masks), and the BP-triggered EFB clear. Full
// multi-stage TEV, indirect textures, and bump mapping are
// out of scope; their registers are stored as state only.
package gpu

import (
	"encoding/binary"
	"errors"
	"math"
)

// Memory is the physical MEM1 interface the GPU reads
// through. Addresses are physical, 0x00000000-based; the
// adapter in the system package masks and validates them.
// Read16 and Read32 return big-endian assembled values.
type Memory interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
}

// EFB dimensions.
const (
	EFBWidth  = 640
	EFBHeight = 528
)

// TMEMSize is the texture memory size in bytes: 1 MiB.
const TMEMSize = 1 << 20

// fifoBufCap bounds the partial FIFO buffer kept in the save
// state. A maximal draw command (65535 vertices at the widest
// vertex layout this renderer decodes) fits inside it.
const fifoBufCap = 1 << 23

// BP register addresses in the FIFO BP load space. Values are
// 24-bit. Only the registers this renderer acts on are named;
// every BP write is also stored in the raw BP file.
const (
	bpGenMode       = 0x00 // general mode: cull mode at bits 14-15
	bpScissorTL     = 0x20 // scissor top-left: x bits 0-11, y bits 12-23
	bpScissorBR     = 0x21 // scissor bottom-right (inclusive), same layout
	bpScissorOffset = 0x22 // scissor offset, stored only
	bpLinePtWidth   = 0x23 // line width bits 0-7, point size bits 8-15
	bpZMode         = 0x40 // depth: enable 0, func 1-3, update 4
	bpBlendMode     = 0x41 // blend and logic control, see pixel.go
	bpConstAlpha    = 0x42 // constant alpha bits 0-7, enable bit 8
	bpPEControl     = 0x43 // pixel format bits 0-2, z format 3-4, early-z bit 6
	bpFieldMask     = 0x44 // stored only
	bpDrawDone      = 0x45 // stored only
	bpEFBTL         = 0x49 // EFB copy source top-left: x bits 0-9, y bits 10-19
	bpEFBBR         = 0x4A // EFB copy source bottom-right (inclusive)
	bpClearAR       = 0x4F // copy clear color: red bits 0-7, alpha bits 16-23
	bpClearGB       = 0x50 // copy clear color: green bits 0-7, blue bits 8-15
	bpClearZ        = 0x51 // copy clear depth, 24-bit
	bpTriggerCopy   = 0x52 // EFB copy trigger: clear bit 14
	bpLoadTLUT0     = 0x64 // TLUT load source: MEM1 address = value << 5
	bpLoadTLUT1     = 0x65 // TLUT load dest: TMEM offset = (value & 0x3FF) << 5,
	// entry count = ((value >> 10) & 0x7FF) + 1, triggers the load
	bpTexGroup0 = 0x80 // texture image groups, stride 8, see texture.go
	bpAlphaFunc = 0xF3 // alpha compare, see pixel.go
)

// PE pixel formats, in bpPEControl bits 0-2, as named in the
// task specification order.
const (
	pfbRGB565   = 0
	pfbRGB8     = 1
	pfbRGBA8    = 2
	pfbRGBA6Z24 = 3
)

// CP FIFO load register addresses (the byte after opcode
// 0x08). VAT group A registers live at 0x70 plus the VAT
// index (the low 3 bits select VAT 0-7), VAT group B at
// 0x80 plus the VAT index, and VAT group C at 0x90 plus
// the VAT index. Array bases live at 0xA0 plus the array
// index and array strides at 0xB0 plus the array index.
const (
	cpMatIndA   = 0x30
	cpMatIndB   = 0x40
	cpVCDLo     = 0x50
	cpVCDHi     = 0x60
	cpVATGroupA = 0x70
	cpVATGroupB = 0x80
	cpVATGroupC = 0x90
	cpArrayBase = 0xA0
	cpArrayStr  = 0xB0
)

// XF register addresses at and above 0x1000. Transform
// memory occupies addresses below 0x1000 and is masked into
// the 1024-word file (see storeXF).
const (
	xfVtxSpecs    = 0x1008 // vertex specifications, stored
	xfNumChan     = 0x1009 // number of color channels, bits 0-3
	xfChan0Amb    = 0x100A
	xfChan1Amb    = 0x100B
	xfChan0Mat    = 0x100C
	xfChan1Mat    = 0x100D
	xfChan0Color  = 0x100E // color channel 0 control
	xfChan1Color  = 0x100F
	xfChan0Alpha  = 0x1010
	xfChan1Alpha  = 0x1011
	xfDualTex     = 0x1012 // dual texture (post matrix) enables, bit per stage
	xfMatIndA     = 0x1018
	xfMatIndB     = 0x1019
	xfViewport    = 0x101A // six floats: xs, ys, zs, xo, yo, zo
	xfProjection  = 0x1020 // six floats A..F, then a type word at 0x1026
	xfNumTexGens  = 0x103F
	xfTexMtxInfo0 = 0x1040 // plus stage
	xfPostMtxInfo = 0x1050 // plus stage: post matrix index bits 0-5
)

// Light blocks live in transform memory at 0x600 plus 16
// words per light (masked into the file, see storeXF).
const xfLights = 0x600

// GPU is the GX renderer state.
type GPU struct {
	mem Memory

	buf []byte // partial FIFO stream, buffered across WriteFIFO calls

	bp [256]uint32 // raw BP register file, 24-bit values

	cpRegs      [256]uint32 // raw CP load register file
	vcdLo       uint32
	vcdHi       uint32
	vat         [8][3]uint32
	arrayBase   [16]uint32
	arrayStride [16]uint32
	matIndA     uint32
	matIndB     uint32

	xfMem  [1024]uint32 // transform memory (masked addressing)
	xfRegs [128]uint32  // XF registers 0x1000-0x107F

	tmem     [TMEMSize]byte
	efbColor []uint32 // raw encoded color words, EFBWidth*EFBHeight
	efbDepth []uint32 // raw 24-bit depth per pixel

	mmio [64]uint16 // CP MMIO window, offsets 0x00-0x7F step 2

	tlutSrc uint32 // pending TLUT source byte address in MEM1
}

// New returns a GPU in its reset state reading through mem.
func New(mem Memory) *GPU {
	g := &GPU{mem: mem}
	g.efbColor = make([]uint32, EFBWidth*EFBHeight)
	g.efbDepth = make([]uint32, EFBWidth*EFBHeight)
	g.Reset()
	return g
}

// Reset restores the documented power-on defaults this
// renderer models: full-EFB scissor, depth test LEQUAL with
// update on, color and alpha updates on, no blending, pixel
// format RGB8, an identity modelview matrix in transform
// word 0, an identity orthographic projection, and a
// 640x480 viewport whose depth range spans the full 24-bit
// space (z scale 2^23, z offset 2^23). The EFB is cleared to
// black at maximum depth.
func (g *GPU) Reset() {
	mem := g.mem
	buf := g.buf[:0]
	*g = GPU{
		mem:      mem,
		buf:      buf,
		efbColor: g.efbColor,
		efbDepth: g.efbDepth,
	}
	if g.efbColor == nil {
		g.efbColor = make([]uint32, EFBWidth*EFBHeight)
		g.efbDepth = make([]uint32, EFBWidth*EFBHeight)
	}
	g.bp[bpScissorBR] = (527 << 12) | 639
	g.bp[bpZMode] = 1 | (3 << 1) | (1 << 4) // enable, LEQUAL, update
	g.bp[bpBlendMode] = (1 << 3) | (1 << 4) // color and alpha update
	g.bp[bpPEControl] = pfbRGB8
	g.bp[bpAlphaFunc] = 7 | (7 << 13) // both compares ALWAYS
	for i := range g.efbDepth {
		g.efbDepth[i] = 0xFFFFFF
	}
	// Identity modelview (3 rows of 4) at transform words 0-11.
	ident := [12]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0}
	for i, f := range ident {
		g.xfMem[i] = math.Float32bits(f)
	}
	// Identity orthographic projection: A=1, B=1, E=1, type=1.
	g.setXFReg(xfProjection+0, math.Float32bits(1))
	g.setXFReg(xfProjection+1, math.Float32bits(1))
	g.setXFReg(xfProjection+6, 1)
	// Viewport for 640x480: xs=320, ys=-240 (negative, so the
	// projected +y axis lands downward on screen), zs=2^23,
	// xo=320, yo=240, zo=2^23.
	vp := [6]float32{320, -240, 8388608, 320, 240, 8388608}
	for i, f := range vp {
		g.setXFReg(xfViewport+uint32(i), math.Float32bits(f))
	}
	g.xfRegs[xfNumChan-0x1000] = 1
}

// CP MMIO window. Offsets are the documented CP window
// offsets: status at 0x00, control at 0x02, clear at 0x04,
// then the FIFO pointer registers (base, end, high and low
// watermarks, read-write distance or count, write pointer,
// read pointer, breakpoint) as stored 16-bit halves at
// offsets 0x10 through 0x2F. All values are stored; the
// status read forces the read-idle bit (bit 2) on and the
// command-idle bit (bit 3) on whenever no partial FIFO
// command is buffered. A write to the clear register clears
// every status bit whose bit is set in the written value.

// ReadMMIO reads one 16-bit CP MMIO register.
func (g *GPU) ReadMMIO(offset uint32) uint16 {
	idx := (offset & 0x7F) >> 1
	if idx >= 64 {
		return 0
	}
	v := g.mmio[idx]
	if offset&0x7F == 0x00 {
		v |= 1 << 2 // read idle
		if len(g.buf) == 0 {
			v |= 1 << 3 // command idle
		}
	}
	return v
}

// WriteMMIO writes one 16-bit CP MMIO register.
func (g *GPU) WriteMMIO(offset uint32, v uint16) {
	idx := (offset & 0x7F) >> 1
	if idx >= 64 {
		return
	}
	if offset&0x7F == 0x04 {
		g.mmio[0] &^= v // clear register: 1 bits clear status bits
	}
	g.mmio[idx] = v
}

// Pixel returns the decoded EFB color at EFB coordinates,
// honoring the current PE pixel format.
func (g *GPU) Pixel(x, y int) (r, gg, b, a uint8) {
	if x < 0 || y < 0 || x >= EFBWidth || y >= EFBHeight {
		return 0, 0, 0, 0
	}
	return decodeEFB(g.bp[bpPEControl]&7, g.efbColor[y*EFBWidth+x])
}

// Depth returns the raw 24-bit depth at EFB coordinates.
func (g *GPU) Depth(x, y int) uint32 {
	if x < 0 || y < 0 || x >= EFBWidth || y >= EFBHeight {
		return 0
	}
	return g.efbDepth[y*EFBWidth+x] & 0xFFFFFF
}

// Present fills dst, a 640x480 XRGB8888 slice, from the EFB
// using the BP copy source rectangle (origin from bpEFBTL,
// inclusive bottom-right from bpEFBBR), scaled to fill the
// whole destination with nearest sampling. If no copy
// rectangle was ever programmed, the full 640x480 area of
// the EFB is used. A programmed but degenerate rectangle
// presents black.
func (g *GPU) Present(dst []uint32) {
	if len(dst) < 640*480 {
		return
	}
	tlx := int(g.bp[bpEFBTL] & 0x3FF)
	tly := int((g.bp[bpEFBTL] >> 10) & 0x3FF)
	brx := int(g.bp[bpEFBBR] & 0x3FF)
	bry := int((g.bp[bpEFBBR] >> 10) & 0x3FF)
	if tlx == 0 && tly == 0 && brx == 0 && bry == 0 {
		brx, bry = 639, 479
	}
	w := brx - tlx + 1
	h := bry - tly + 1
	for i := range dst[:640*480] {
		dst[i] = 0xFF000000
	}
	if w <= 0 || h <= 0 {
		return
	}
	for dy := 0; dy < 480; dy++ {
		sy := tly + dy*h/480
		if sy < 0 || sy >= EFBHeight {
			continue
		}
		for dx := 0; dx < 640; dx++ {
			sx := tlx + dx*w/640
			if sx < 0 || sx >= EFBWidth {
				continue
			}
			r, gg, b, _ := g.Pixel(sx, sy)
			dst[dy*640+dx] = 0xFF000000 | uint32(r)<<16 | uint32(gg)<<8 | uint32(b)
		}
	}
}

// setXFReg stores one XF register at or above 0x1000 and
// applies the matrix-index mirror registers.
func (g *GPU) setXFReg(addr uint32, v uint32) {
	if addr >= 0x1000 && addr < 0x1080 {
		g.xfRegs[addr-0x1000] = v
	}
	switch addr {
	case xfMatIndA:
		g.matIndA = v
	case xfMatIndB:
		g.matIndB = v
	}
}

// storeXF stores one word from an XF load. Transform memory
// addresses below 0x1000 are masked into the 1024-word file
// (so the documented regions land at: position matrices
// 0x000, post matrices 0x500 masked to 0x100, lights 0x600
// masked to 0x200); registers at 0x1000 and above go to the
// register file. Matrix slots in this renderer are 12
// consecutive words (3 rows of 4).
func (g *GPU) storeXF(addr uint32, v uint32) {
	if addr < 0x1000 {
		g.xfMem[addr&0x3FF] = v
		return
	}
	g.setXFReg(addr, v)
}

// xfWord reads one transform memory word through the same
// masked addressing as storeXF.
func (g *GPU) xfWord(addr uint32) uint32 { return g.xfMem[addr&0x3FF] }

func (g *GPU) xfReg(addr uint32) uint32 {
	if addr >= 0x1000 && addr < 0x1080 {
		return g.xfRegs[addr-0x1000]
	}
	return 0
}

func (g *GPU) xfRegF(addr uint32) float64 {
	return float64(math.Float32frombits(g.xfReg(addr)))
}

// SerializeSize is the fixed save-state size: a version
// byte, the BP and CP files, CP decoded state, the MMIO
// window, XF memory and registers, TMEM, the raw EFB color
// and depth planes, and the partial FIFO buffer.
const SerializeSize = 1 +
	256*4 + // BP
	256*4 + // CP raw
	16*4 + 16*4 + 4 + 4 + 8*3*4 + // array bases, strides, VCD, VAT
	64*2 + // MMIO
	1024*4 + 128*4 + // XF
	TMEMSize +
	EFBWidth*EFBHeight*4*2 +
	4 + fifoBufCap + 4 // FIFO length, FIFO bytes, TLUT source

// ErrBadState reports a state block of the wrong size or
// version.
var ErrBadState = errors.New("gpu: bad save state")

const serializeVersion = 1

// Serialize writes the complete GPU state.
func (g *GPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	off := 1
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(out[off:], v)
		off += 4
	}
	for _, v := range g.bp {
		put32(v)
	}
	for _, v := range g.cpRegs {
		put32(v)
	}
	for _, v := range g.arrayBase {
		put32(v)
	}
	for _, v := range g.arrayStride {
		put32(v)
	}
	put32(g.vcdLo)
	put32(g.vcdHi)
	for i := 0; i < 8; i++ {
		for j := 0; j < 3; j++ {
			put32(g.vat[i][j])
		}
	}
	for _, v := range g.mmio {
		binary.BigEndian.PutUint16(out[off:], v)
		off += 2
	}
	for _, v := range g.xfMem {
		put32(v)
	}
	for _, v := range g.xfRegs {
		put32(v)
	}
	off += copy(out[off:], g.tmem[:])
	for _, v := range g.efbColor {
		put32(v)
	}
	for _, v := range g.efbDepth {
		put32(v)
	}
	n := len(g.buf)
	if n > fifoBufCap {
		n = fifoBufCap
	}
	put32(uint32(n))
	off += copy(out[off:], g.buf[:n])
	off += fifoBufCap - n
	put32(g.tlutSrc)
	if off != SerializeSize {
		panic("gpu: serialize size mismatch")
	}
	return out
}

// Deserialize restores a state written by Serialize.
func (g *GPU) Deserialize(in []byte) error {
	if len(in) != SerializeSize || in[0] != serializeVersion {
		return ErrBadState
	}
	off := 1
	get32 := func() uint32 {
		v := binary.BigEndian.Uint32(in[off:])
		off += 4
		return v
	}
	for i := range g.bp {
		g.bp[i] = get32()
	}
	for i := range g.cpRegs {
		g.cpRegs[i] = get32()
	}
	for i := range g.arrayBase {
		g.arrayBase[i] = get32()
	}
	for i := range g.arrayStride {
		g.arrayStride[i] = get32()
	}
	g.vcdLo = get32()
	g.vcdHi = get32()
	for i := 0; i < 8; i++ {
		for j := 0; j < 3; j++ {
			g.vat[i][j] = get32()
		}
	}
	for i := range g.mmio {
		g.mmio[i] = binary.BigEndian.Uint16(in[off:])
		off += 2
	}
	for i := range g.xfMem {
		g.xfMem[i] = get32()
	}
	for i := range g.xfRegs {
		g.xfRegs[i] = get32()
	}
	// The matrix index registers mirror two XF registers;
	// resync them from the restored register file.
	g.matIndA = g.xfReg(xfMatIndA)
	g.matIndB = g.xfReg(xfMatIndB)
	if g.matIndA == 0 {
		g.matIndA = g.cpRegs[cpMatIndA]
	}
	if g.matIndB == 0 {
		g.matIndB = g.cpRegs[cpMatIndB]
	}
	off += copy(g.tmem[:], in[off:])
	if g.efbColor == nil {
		g.efbColor = make([]uint32, EFBWidth*EFBHeight)
		g.efbDepth = make([]uint32, EFBWidth*EFBHeight)
	}
	for i := range g.efbColor {
		g.efbColor[i] = get32()
	}
	for i := range g.efbDepth {
		g.efbDepth[i] = get32()
	}
	n := int(get32())
	if n > fifoBufCap {
		return ErrBadState
	}
	g.buf = append(g.buf[:0], in[off:off+n]...)
	off += fifoBufCap
	g.tlutSrc = get32()
	return nil
}
