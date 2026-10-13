// Package vi implements the Nintendo 64 Video Interface.
//
// The VI scans a framebuffer out of RDRAM on a fixed field
// cadence and raises an interrupt when the scan reaches a
// programmed half-line. This package models the register
// file, the half-line timing, the interrupt level, and the
// final picture sampling (Present) that turns the RDRAM
// framebuffer into an XRGB8888 frame for the host. RDP
// rendering into RDRAM is a separate package; the VI only
// reads what is already there.
//
// Model choices, stated plainly:
//
//   - A field lasts exactly 1,562,500 CPU cycles (the
//     93.75 MHz CPU divided by 60 Hz). That budget is
//     spread evenly over the field's half-lines with an
//     exact rational accumulator, so long runs have no
//     timing drift.
//   - The total half-line count is V_TOTAL plus one,
//     except that a V_TOTAL field of 0 selects the NTSC
//     default of 525 half-lines.
//   - An even half-line total means interlaced output:
//     the field flag toggles at every field wrap. An odd
//     total means progressive output: the field flag
//     never toggles. The SERRATE bit is stored but does
//     not change this model.
//   - In progressive mode the low bit of both the
//     interrupt target and the line counter is ignored
//     for the interrupt comparison, because a progressive
//     field has no distinct odd and even half-lines.
//   - When CTRL TYPE is 0 the VI is off: the counter is
//     frozen at 0, V_CURRENT reads 0, no interrupt fires,
//     and Present returns no frame.
//   - The interrupt is a level. OnInterrupt is called
//     with true when a field reaches the target half-line
//     and with false when a write to V_CURRENT clears
//     the pending flag.
package vi

import (
	"encoding/binary"
	"errors"
	"math"
)

// Register byte offsets from the VI base at 0x04400000.
const (
	RegCtrl     = 0x00
	RegOrigin   = 0x04
	RegWidth    = 0x08
	RegVIntr    = 0x0C
	RegVCurrent = 0x10
	RegBurst    = 0x14
	RegVTotal   = 0x18
	RegHTotal   = 0x1C
	RegHLeap    = 0x20
	RegHVideo   = 0x24
	RegVVideo   = 0x28
	RegVBurst   = 0x2C
	RegXScale   = 0x30
	RegYScale   = 0x34
)

// CTRL register bits and fields.
const (
	CtrlTypeMask     = 0x0003 // bits 1-0: 0 off, 2 is 16-bit, 3 is 32-bit
	CtrlGammaDither  = 1 << 2
	CtrlGamma        = 1 << 3
	CtrlDivot        = 1 << 4
	CtrlVBusClock    = 1 << 5
	CtrlSerrate      = 1 << 6
	CtrlTestMode     = 1 << 7
	CtrlAAModeMask   = 0x0300 // bits 9-8
	CtrlKillWE       = 1 << 11
	CtrlPixelAdvMask = 0xF000 // bits 15-12
	CtrlDedither     = 1 << 16
)

// cyclesPerField is the CPU cycle budget of one field:
// 93,750,000 cycles per second divided by 60 fields.
const cyclesPerField = 1562500

// defaultHalfLines is the half-line count used when the
// V_TOTAL field reads 0 (NTSC timing, 525 half-lines).
const defaultHalfLines = 525

// VI is the video interface device.
type VI struct {
	ctrl   uint32
	origin uint32 // stored masked to 24 bits
	width  uint32
	vIntr  uint32
	burst  uint32
	vTotal uint32
	hTotal uint32
	hLeap  uint32
	hVideo uint32
	vVideo uint32
	vBurst uint32
	xScale uint32
	yScale uint32

	counter uint32 // current half-line, 0 to totalHalf-1
	acc     uint64 // rational timing remainder, in units of half-line cycles
	fld     int    // current field: 0 or 1
	pending bool   // interrupt level currently asserted

	// OnInterrupt, when set, is called when the interrupt
	// level changes: true when a field reaches the target
	// half-line, false when a V_CURRENT write clears it.
	// The coordinator wires it to the MI VI interrupt bit.
	OnInterrupt func(active bool)
}

// New returns a VI in its power-on state: TYPE 0 (off),
// all registers zero, counter at half-line 0, field 0.
func New() *VI {
	return &VI{}
}

// totalHalf returns the number of half-lines in a field.
func (v *VI) totalHalf() int {
	f := int(v.vTotal & 0x3FF)
	if f == 0 {
		return defaultHalfLines
	}
	return f + 1
}

// interlaced reports whether the current timing is
// interlaced (an even half-line total).
func (v *VI) interlaced() bool {
	return v.totalHalf()%2 == 0
}

// framebufferType returns the CTRL TYPE field.
func (v *VI) framebufferType() uint32 {
	return v.ctrl & CtrlTypeMask
}

// ReadRegister reads the 32-bit register at the given byte
// offset. Plain registers read back what was written.
// V_CURRENT reads the computed counter value.
func (v *VI) ReadRegister(off uint32) uint32 {
	switch off {
	case RegCtrl:
		return v.ctrl
	case RegOrigin:
		return v.origin
	case RegWidth:
		return v.width
	case RegVIntr:
		return v.vIntr
	case RegVCurrent:
		return v.Current()
	case RegBurst:
		return v.burst
	case RegVTotal:
		return v.vTotal
	case RegHTotal:
		return v.hTotal
	case RegHLeap:
		return v.hLeap
	case RegHVideo:
		return v.hVideo
	case RegVVideo:
		return v.vVideo
	case RegVBurst:
		return v.vBurst
	case RegXScale:
		return v.xScale
	case RegYScale:
		return v.yScale
	}
	return 0
}

// WriteRegister writes the 32-bit register at the given
// byte offset. ORIGIN is masked to its 24-bit field;
// every other plain register is stored raw. Any write to
// V_CURRENT clears the interrupt, whatever the value.
func (v *VI) WriteRegister(off uint32, val uint32) {
	switch off {
	case RegCtrl:
		v.ctrl = val
	case RegOrigin:
		v.origin = val & 0xFFFFFF
	case RegWidth:
		v.width = val
	case RegVIntr:
		v.vIntr = val
	case RegVCurrent:
		v.pending = false
		if v.OnInterrupt != nil {
			v.OnInterrupt(false)
		}
	case RegBurst:
		v.burst = val
	case RegVTotal:
		v.vTotal = val
	case RegHTotal:
		v.hTotal = val
	case RegHLeap:
		v.hLeap = val
	case RegHVideo:
		v.hVideo = val
	case RegVVideo:
		v.vVideo = val
	case RegVBurst:
		v.vBurst = val
	case RegXScale:
		v.xScale = val
	case RegYScale:
		v.yScale = val
	}
}

// Current returns the V_CURRENT register value: bits 9-1
// hold the current line (half-line counter divided by 2)
// and bit 0 holds the field flag. When the VI is off the
// value reads 0.
func (v *VI) Current() uint32 {
	if v.framebufferType() == 0 {
		return 0
	}
	return ((v.counter >> 1) << 1) | uint32(v.fld)
}

// Field returns the current field flag, 0 or 1. In
// progressive mode it stays 0; in interlaced mode it
// toggles at every field wrap.
func (v *VI) Field() int {
	return v.fld
}

// Pending reports whether the VI interrupt is asserted.
// It is a test and integration helper; the level itself
// is delivered through OnInterrupt.
func (v *VI) Pending() bool {
	return v.pending
}

// Advance runs the half-line timing for the given number
// of CPU cycles.
//
// The accumulator keeps the timing exact: each call adds
// cpuCycles times totalHalf to the numerator, converts
// whole multiples of cyclesPerField into half-line steps,
// and keeps the remainder. The counter steps one half-line
// at a time so the interrupt comparison sees every line.
//
// The interrupt fires when a step takes the counter onto
// the target: in interlaced mode the counter must equal
// the target, in progressive mode the counters divided by
// 2 must become equal where they were not equal on the
// previous half-line (so the paired half-line does not
// fire a second time). A target of 0 therefore fires at
// the field wrap. When the VI is off the cycles are
// discarded and the counter stays at 0.
func (v *VI) Advance(cpuCycles int) {
	if cpuCycles <= 0 {
		return
	}
	if v.framebufferType() == 0 {
		v.counter = 0
		v.acc = 0
		return
	}
	total := v.totalHalf()
	inter := total%2 == 0
	target := v.vIntr & 0x3FF

	matches := func(c uint32) bool {
		if inter {
			return c == target
		}
		return c>>1 == target>>1
	}

	v.acc += uint64(cpuCycles) * uint64(total)
	steps := v.acc / cyclesPerField
	v.acc -= steps * cyclesPerField

	for s := uint64(0); s < steps; s++ {
		prev := v.counter
		v.counter++
		if int(v.counter) >= total {
			v.counter = 0
			if inter {
				v.fld ^= 1
			}
		}
		if matches(v.counter) && !matches(prev) {
			v.pending = true
			if v.OnInterrupt != nil {
				v.OnInterrupt(true)
			}
		}
	}
}

// gammaTable maps a channel byte through the VI gamma
// curve (a square root), computed once with the formula
// byte(sqrt(c/255) * 255 + 0.5).
var gammaTable [256]byte

func init() {
	for c := 0; c < 256; c++ {
		gammaTable[c] = byte(math.Sqrt(float64(c)/255.0)*255.0 + 0.5)
	}
}

// Present samples the RDRAM framebuffer into an XRGB8888
// frame (alpha in the top byte) and returns it with its
// width and height. It returns nil, 0, 0 when the VI is
// off (TYPE 0), when TYPE is a reserved value, or when
// WIDTH is 0.
//
// Geometry comes from the video window registers: the
// width is H_END minus H_START from H_VIDEO (falling back
// to WIDTH when that is out of range) and the height is
// (V_END minus V_START) divided by 2 from V_VIDEO, since
// those values are in half-lines (falling back to 240).
// Width is capped at 640 and height at 480.
//
// Each output pixel maps back into the framebuffer
// through the X_SCALE and Y_SCALE registers, which hold a
// 2.10 fixed point scale and offset: the source coordinate
// is (offset + output * scale) shifted right by 10. In
// interlaced mode the source line is doubled and offset
// by the current field, so each field presents its own
// lines of the framebuffer. Pixels whose source x is
// outside WIDTH, or whose address falls outside RDRAM,
// read as opaque black.
func (v *VI) Present(dram []byte) (fb []uint32, w, h int) {
	typ := v.framebufferType()
	if typ != 2 && typ != 3 {
		return nil, 0, 0
	}
	if v.width == 0 {
		return nil, 0, 0
	}

	hStart := int((v.hVideo >> 16) & 0x3FF)
	hEnd := int(v.hVideo & 0x3FF)
	w = hEnd - hStart
	if w <= 0 || w > 640 {
		w = int(v.width)
	}
	if w > 640 {
		w = 640
	}
	if w <= 0 {
		return nil, 0, 0
	}

	vStart := int((v.vVideo >> 16) & 0x3FF)
	vEnd := int(v.vVideo & 0x3FF)
	h = (vEnd - vStart) / 2
	if h <= 0 || h > 480 {
		h = 240
	}
	if h > 480 {
		h = 480
	}

	xScale := int(v.xScale & 0xFFF)
	xOff := int((v.xScale >> 16) & 0xFFF)
	if xScale == 0 {
		xScale = 1024
	}
	yScale := int(v.yScale & 0xFFF)
	yOff := int((v.yScale >> 16) & 0xFFF)
	if yScale == 0 {
		yScale = 1024
	}

	bpp := 2
	if typ == 3 {
		bpp = 4
	}
	inter := v.interlaced()
	origin := int(v.origin & 0xFFFFFF)
	width := int(v.width)
	gamma := v.ctrl&CtrlGamma != 0

	fb = make([]uint32, w*h)
	for oy := 0; oy < h; oy++ {
		sy := (yOff + oy*yScale) >> 10
		srcLine := sy
		if inter {
			srcLine = sy*2 + v.fld
		}
		for ox := 0; ox < w; ox++ {
			sx := (xOff + ox*xScale) >> 10
			px := uint32(0xFF000000)
			if sx >= 0 && sx < width {
				addr := origin + (srcLine*width+sx)*bpp
				if addr >= 0 && addr+bpp <= len(dram) {
					px = v.fetchPixel(dram, addr, typ, gamma)
				}
			}
			fb[oy*w+ox] = px
		}
	}
	return fb, w, h
}

// fetchPixel decodes one framebuffer pixel at a byte
// address in RDRAM into XRGB8888.
func (v *VI) fetchPixel(dram []byte, addr int, typ uint32, gamma bool) uint32 {
	var r, g, b, a uint32
	if typ == 2 {
		p := uint32(binary.BigEndian.Uint16(dram[addr:]))
		r5 := (p >> 11) & 31
		g5 := (p >> 6) & 31
		b5 := (p >> 1) & 31
		r = (r5 << 3) | (r5 >> 2)
		g = (g5 << 3) | (g5 >> 2)
		b = (b5 << 3) | (b5 >> 2)
		if p&1 != 0 {
			a = 255
		}
	} else {
		r = uint32(dram[addr])
		g = uint32(dram[addr+1])
		b = uint32(dram[addr+2])
		a = uint32(dram[addr+3])
	}
	if gamma {
		r = uint32(gammaTable[r])
		g = uint32(gammaTable[g])
		b = uint32(gammaTable[b])
	}
	return a<<24 | r<<16 | g<<8 | b
}

// SerializeSize is the fixed byte size of a serialized VI.
//
// Layout, all big-endian:
//
//	 0: 13 register words: CTRL, ORIGIN, WIDTH, V_INTR,
//	    BURST, V_TOTAL, H_TOTAL, H_LEAP, H_VIDEO, V_VIDEO,
//	    V_BURST, X_SCALE, Y_SCALE (52 bytes)
//	52: half-line counter, uint32
//	56: timing accumulator, uint64
//	64: field flag byte
//	65: interrupt pending byte
//	66: 2 pad bytes
const SerializeSize = 13*4 + 4 + 8 + 4

// Serialize writes the VI state into dst, which must be
// at least SerializeSize bytes, and returns the number of
// bytes written. The layout is documented on
// SerializeSize. The OnInterrupt callback is not part of
// the state; the coordinator rewires it after a restore.
func (v *VI) Serialize(dst []byte) int {
	regs := [13]uint32{
		v.ctrl, v.origin, v.width, v.vIntr, v.burst,
		v.vTotal, v.hTotal, v.hLeap, v.hVideo, v.vVideo,
		v.vBurst, v.xScale, v.yScale,
	}
	for i, r := range regs {
		binary.BigEndian.PutUint32(dst[i*4:], r)
	}
	binary.BigEndian.PutUint32(dst[52:], v.counter)
	binary.BigEndian.PutUint64(dst[56:], v.acc)
	dst[64] = byte(v.fld)
	if v.pending {
		dst[65] = 1
	} else {
		dst[65] = 0
	}
	dst[66] = 0
	dst[67] = 0
	return SerializeSize
}

// Deserialize restores a state written by Serialize.
func (v *VI) Deserialize(in []byte) error {
	if len(in) < SerializeSize {
		return errors.New("vi: state too short")
	}
	regs := make([]uint32, 13)
	for i := range regs {
		regs[i] = binary.BigEndian.Uint32(in[i*4:])
	}
	v.ctrl = regs[0]
	v.origin = regs[1]
	v.width = regs[2]
	v.vIntr = regs[3]
	v.burst = regs[4]
	v.vTotal = regs[5]
	v.hTotal = regs[6]
	v.hLeap = regs[7]
	v.hVideo = regs[8]
	v.vVideo = regs[9]
	v.vBurst = regs[10]
	v.xScale = regs[11]
	v.yScale = regs[12]
	v.counter = binary.BigEndian.Uint32(in[52:])
	v.acc = binary.BigEndian.Uint64(in[56:])
	v.fld = int(in[64] & 1)
	v.pending = in[65] != 0
	return nil
}
