// Package video implements the two DS 2D engines (A and B),
// the VRAM bank assignment layer the engines fetch through,
// DISPSTAT and VCOUNT, display capture, and the display
// timing model the system drives.
//
// Timing model (stated in the README): a frame is 263 lines
// (VCOUNT 0-262), 192 of them visible. The ARM9 cycle budget
// splits as 1536 cycles of visible dots plus 594 cycles of
// horizontal blank per line, and the ARM7 budget as 768 plus
// 297, which reproduces the DS-1 frame budgets of 560,190 and
// 280,095 cycles exactly. Rendering happens per scanline from
// the register and VRAM state at line start, like the rest of
// this core family. The main-memory display FIFO is not
// modeled; display mode 3 reads main RAM at a latched address.
package video

import (
	"github.com/davidpatton/itak-gaming/cores/go-nds/bus"
)

// Screen geometry and timing.
const (
	Width        = 256
	Height       = 192
	LinesTotal   = 263
	CyclesLineA9 = 1536 // visible portion of a line, ARM9 cycles
	CyclesLineB9 = 594  // hblank portion
	CyclesLineA7 = 768
	CyclesLineB7 = 297
)

// space is one VRAM fetch space: an array of chunks aliased
// into the physical banks, indexed by address. Unassigned
// chunks read as 0.
type space struct {
	chunks [][]byte
	shift  uint
	size   uint32
}

func newSpace(chunks int, shift uint, size uint32) *space {
	return &space{chunks: make([][]byte, chunks), shift: shift, size: size}
}

func (s *space) get(addr uint32) byte {
	addr &= s.size - 1
	c := s.chunks[addr>>s.shift]
	if c == nil {
		return 0
	}
	return c[addr&((1<<s.shift)-1)]
}

func (s *space) get16(addr uint32) uint16 {
	return uint16(s.get(addr)) | uint16(s.get(addr+1))<<8
}

func (s *space) put(addr uint32, data []byte) {
	n := 1 << s.shift
	for off := 0; off+n <= len(data); off += n {
		s.chunks[(addr+uint32(off))>>s.shift] = data[off : off+n]
	}
}

func (s *space) clear() {
	for i := range s.chunks {
		s.chunks[i] = nil
	}
}

// Video owns both engines' render state.
type Video struct {
	bus *bus.Bus
	A   []uint32 // engine A framebuffer, XRGB8888
	B   []uint32 // engine B framebuffer, XRGB8888

	// Line3D, when set, fills one scanline of the 3D layer
	// for engine A BG0: rgb6 colors with the 5-bit alpha in
	// bits 18-22; a zero entry is fully transparent. The
	// system installs the gx package's provider (DS-3).
	Line3D func(y int, dst *[Width]uint32)

	bgA, bgB   *space
	objA, objB *space
	extBG      [2]*space
	extOBJ     [2]*space
	cntCache   [9]byte
	mapsDirty  bool

	vcount  int
	hblank  bool
	lyc     uint16
	dsEn    uint8 // DISPSTAT IRQ enables: vblank, hblank, vcount
	refX    [2][4]int32
	refY    [2][4]int32
	lineA   [Width]uint32 // engine A composed line (rgb6), for capture
	capOn   bool
	capLine int
	mainAdr uint32 // latched main-RAM display address
}

// New builds the video unit over the shared bus.
func New(b *bus.Bus) *Video {
	v := &Video{
		bus:       b,
		A:         make([]uint32, Width*Height),
		B:         make([]uint32, Width*Height),
		bgA:       newSpace(32, 14, 0x80000),
		bgB:       newSpace(8, 14, 0x20000),
		objA:      newSpace(16, 14, 0x40000),
		objB:      newSpace(16, 14, 0x40000),
		mapsDirty: true,
	}
	v.extBG[0] = newSpace(4, 13, 0x8000)
	v.extBG[1] = newSpace(4, 13, 0x8000)
	v.extOBJ[0] = newSpace(2, 13, 0x4000)
	v.extOBJ[1] = newSpace(2, 13, 0x4000)
	return v
}

// IO read helpers over the ARM9 IO page (both engine pages live
// there).
func (v *Video) rd16(off int) uint16 {
	return uint16(v.bus.IO9[off]) | uint16(v.bus.IO9[off+1])<<8
}
func (v *Video) rd32(off int) uint32 {
	return uint32(v.rd16(off)) | uint32(v.rd16(off+2))<<16
}
func (v *Video) wr16(off int, val uint16) {
	v.bus.IO9[off] = byte(val)
	v.bus.IO9[off+1] = byte(val >> 8)
}
func (v *Video) wr32(off int, val uint32) {
	v.wr16(off, uint16(val))
	v.wr16(off+2, uint16(val>>16))
}

// POWCNT1 (IO9 0x304): bit 0 LCDs on, bit 1 engine A 2D on,
// bit 9 engine B 2D on, bit 15 swap displays (engine A on the
// bottom screen).
func (v *Video) powcnt() uint16 { return v.rd16(0x304) }

// Swapped reports the POWCNT1 display assignment.
func (v *Video) Swapped() bool { return v.powcnt()&0x8000 != 0 }

// BG0Is3D reports whether engine A's BG0 is selected as the 3D
// layer (DISPCNT bit 3). The gx package renders the layer
// through the Line3D provider.
func (v *Video) BG0Is3D() bool { return v.rd32(0)&8 != 0 }

// DISPSTAT returns the composed DISPSTAT halfword: live flags
// in bits 0-2 (VBlank, HBlank, VCount match), the stored IRQ
// enables in bits 3-5, and the 9-bit LYC in bits 8-15 plus
// bit 7.
func (v *Video) DISPSTAT() uint16 {
	var s uint16
	if v.vcount >= Height {
		s |= 1
	}
	if v.hblank {
		s |= 2
	}
	if uint16(v.vcount) == v.lyc {
		s |= 4
	}
	s |= uint16(v.dsEn&7) << 3
	s |= (v.lyc & 0x100) >> 1
	s |= (v.lyc & 0xFF) << 8
	return s
}

// SetDISPSTAT stores the writable DISPSTAT fields (IRQ enables
// and LYC) from a CPU write.
func (v *Video) SetDISPSTAT(val uint16) {
	v.dsEn = uint8(val >> 3 & 7)
	v.lyc = (val>>8)&0xFF | (val&0x80)<<1
}

// IRQEnabled reports whether a DISPSTAT source (0 VBlank,
// 1 HBlank, 2 VCount) has its IRQ enable set.
func (v *Video) IRQEnabled(kind int) bool { return v.dsEn>>uint(kind)&1 != 0 }

// LYC returns the VCount compare value.
func (v *Video) LYC() int { return int(v.lyc) }

// SetLine sets the current scanline (VCOUNT).
func (v *Video) SetLine(n int) { v.vcount = n }

// VCount returns the current scanline.
func (v *Video) VCount() int { return v.vcount }

// SetHBlank sets the hblank phase flag.
func (v *Video) SetHBlank(b bool) { v.hblank = b }

// SetMainDispAddr latches the main-RAM display address written
// to DISP_MMEM_FIFO (0x04000068).
func (v *Video) SetMainDispAddr(a uint32) { v.mainAdr = a }

// LatchAffine reloads both engines' affine reference points
// from their registers. The system calls it at the start of
// VBlank (line 192).
func (v *Video) LatchAffine() {
	for e := 0; e < 2; e++ {
		for bg := 2; bg < 4; bg++ {
			x, y := v.regRef(e, bg)
			v.refX[e][bg], v.refY[e][bg] = x, y
		}
	}
}

// regRef reads an affine BG's reference point registers as
// sign-extended 28-bit fixed-point values (8 fractional bits).
func (v *Video) regRef(e, bg int) (int32, int32) {
	base := e*0x1000 + 0x20 + (bg-2)*0x10
	x := int32(v.rd32(base + 8))
	y := int32(v.rd32(base + 12))
	if x&0x08000000 != 0 {
		x |= ^0x0FFFFFFF
	}
	if y&0x08000000 != 0 {
		y |= ^0x0FFFFFFF
	}
	return x, y
}

// AdvanceAffine steps both engines' affine reference points by
// their vertical parameters after a rendered line.
func (v *Video) AdvanceAffine() {
	for e := 0; e < 2; e++ {
		for bg := 2; bg < 4; bg++ {
			base := e*0x1000 + 0x20 + (bg-2)*0x10
			v.refX[e][bg] += int32(int16(v.rd16(base + 2))) // PB (dmx)
			v.refY[e][bg] += int32(int16(v.rd16(base + 6))) // PD (dmy)
		}
	}
}

// BeginFrame arms display capture if DISPCAPCNT enable is set.
func (v *Video) BeginFrame() {
	cap := v.rd32(0x064)
	if cap&(1<<31) != 0 && !v.capOn {
		v.capOn = true
		v.capLine = 0
		v.wr32(0x064, cap|(1<<29)) // busy
	}
}

// rgb6 packs three 6-bit channels.
func rgb6(r, g, b uint32) uint32 { return r | g<<6 | b<<12 }

// rgb6From15 converts a 15-bit palette color to rgb6 by the
// documented 5-to-6 expansion (c<<1)|(c>>4).
func rgb6From15(c uint16) uint32 {
	r := uint32(c & 31)
	g := uint32(c >> 5 & 31)
	b := uint32(c >> 10 & 31)
	return rgb6(r<<1|r>>4, g<<1|g>>4, b<<1|b>>4)
}

// xrgb expands an rgb6 pixel to XRGB8888 (6-to-8 by
// (c<<2)|(c>>4) per channel).
func xrgb(c uint32) uint32 {
	r := c & 63
	g := c >> 6 & 63
	b := c >> 12 & 63
	return 0xFF000000 | (r<<2|r>>4)<<16 | (g<<2|g>>4)<<8 | (b<<2 | b>>4)
}
