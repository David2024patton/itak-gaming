// Package vi models the GameCube video interface (VI)
// register window, based at 0x0C002000 on the hardware
// page, from published VI register documentation. The
// register file is accessed in 16-bit lanes; 32-bit
// registers occupy two consecutive lanes, the lane at the
// lower offset holding the high half (the machine is
// big-endian).
//
// Timing model (deliberately simple and self-consistent,
// stated exactly): one machine frame is CyclesPerFrame
// CPU cycles (486 MHz / 60). The number of fields per
// frame is 1 when the VIDCR NIN bit is set (non-interlaced)
// and 2 otherwise. Lines per field are ACV + PRB + PSB,
// where ACV comes from VITV and PRB and PSB always come
// from the even-field vertical timing register VIVTE in
// this simplified model (the odd-field register VIVTO is
// stored but never used for timing). Lines per frame are
// linesPerField times fieldsPerFrame, and the total
// halflines per frame are 2 times linesPerFrame. The
// halfline counter is
//
//	CurrentHalfline = (cyclesIntoFrame * totalHalflines) / CyclesPerFrame
//
// where cyclesIntoFrame is the accumulated cycle count
// modulo CyclesPerFrame. The counter is monotonic within
// a frame and wraps at the frame boundary.
//
// Presentation is not part of this package: the frame
// buffer base registers (VITFBL, VITFBR, VIBFBL, VIBFBR)
// are stored state only, and actual presentation stays
// with the GX embedded frame buffer in package gpu.
package vi

import (
	"encoding/binary"
	"errors"
)

// CyclesPerFrame is the frame budget at the Gekko clock
// of 486 MHz and 60 frames per second.
const CyclesPerFrame = 8_100_000

// Register offsets within the VI window (base 0x0C002000).
const (
	regVITV      = 0x00 // vertical timing: ACV bits 0-9, EQ bits 10-14
	regVIDCR     = 0x02 // display control: ENB 0, RST 1, NIN 2, FMT bits 8-9
	regVIHTR0    = 0x04 // horizontal timing 0 (32-bit, stored)
	regVIHTR1    = 0x08 // horizontal timing 1 (32-bit, stored)
	regVIVTO     = 0x0C // odd field vertical timing: PSB bits 0-9, PRB bits 10-15
	regVIVTE     = 0x0E // even field vertical timing, same layout
	regVICURR    = 0x10 // current halfline (read-only, computed)
	regVIDI0     = 0x14 // display interrupt 0 (32-bit)
	regVIDI1     = 0x18 // display interrupt 1 (32-bit)
	regVIDI2     = 0x1C // display interrupt 2 (32-bit)
	regVIDI3     = 0x20 // display interrupt 3 (32-bit)
	regVIDIL0    = 0x24 // display interrupt location 0 (32-bit, stored)
	regVIDIL1    = 0x28 // display interrupt location 1 (32-bit, stored)
	regVIPICCONF = 0x2C // picture configuration (32-bit, stored)
	regVIHSR     = 0x30 // horizontal scaling (stored)
	regFilter0   = 0x34 // first of 12 filter coefficient words, through 0x4A
	regVITFBL    = 0x4C // top field frame buffer left (32-bit, stored)
	regVITFBR    = 0x50 // top field frame buffer right (32-bit, stored)
	regVIBFBL    = 0x54 // bottom field frame buffer left (32-bit, stored)
	regVIBFBR    = 0x58 // bottom field frame buffer right (32-bit, stored)
	regEnd       = 0x5C // one past the last register byte
)

// Display interrupt register bit fields (32-bit view).
const (
	dilTargetMask = 0x7FF          // bits 0-10: target line, in halflines
	dilDIS        = 1 << 16        // status: set by hardware when the interrupt fires
	dilDIE        = 1 << 28        // enable
	dilDISLaneBit = 1 << 0         // DIS within the high 16-bit lane
	dilDIELaneBit = 1 << (28 - 16) // DIE within the high 16-bit lane
)

// VI is the video interface state.
type VI struct {
	regs [regEnd / 2]uint16 // 16-bit lanes, offset/2 indexed

	totalCycles int64 // cycles accumulated by Advance, never reset except by Reset
}

// New returns a VI in its reset state.
func New() *VI {
	v := &VI{}
	v.Reset()
	return v
}

// Reset restores the documented 480i NTSC-like defaults:
// VITV with EQ 6 and ACV 240, VIDCR with ENB set and
// interlaced output (NIN clear), and both field vertical
// timing registers with PRB 15 and PSB 3. Every other
// register and the cycle accumulator are zeroed.
func (v *VI) Reset() {
	*v = VI{}
	v.regs[regVITV/2] = (6 << 10) | 240
	v.regs[regVIDCR/2] = 1 // ENB
	v.regs[regVIVTO/2] = (15 << 10) | 3
	v.regs[regVIVTE/2] = (15 << 10) | 3
}

// Read16 reads one 16-bit register lane at off. The
// VICURR lane returns the computed current halfline and
// offsets outside the register window read as zero.
func (v *VI) Read16(off uint32) uint16 {
	off &^= 1
	if off >= regEnd {
		return 0
	}
	if off == regVICURR {
		return uint16(v.CurrentHalfline())
	}
	return v.regs[off/2]
}

// Write16 writes one 16-bit register lane at off. Writes
// to VICURR are ignored, as are offsets outside the
// register window. For the display interrupt registers,
// the high lane carries the DIS status bit: writing that
// lane with the DIS bit clear clears a pending status,
// and writing it set leaves the status unchanged (only
// hardware sets DIS).
func (v *VI) Write16(off uint32, val uint16) {
	off &^= 1
	if off >= regEnd || off == regVICURR {
		return
	}
	if isVIDIHighLane(off) {
		old := v.regs[off/2]
		dis := old & dilDISLaneBit
		if val&dilDISLaneBit == 0 {
			dis = 0
		}
		v.regs[off/2] = (val &^ dilDISLaneBit) | dis
		return
	}
	v.regs[off/2] = val
}

// isVIDIHighLane reports whether off is the high lane of
// one of the four display interrupt registers.
func isVIDIHighLane(off uint32) bool {
	switch off {
	case regVIDI0, regVIDI1, regVIDI2, regVIDI3:
		return true
	}
	return false
}

// reg32 reads a 32-bit register from its two lanes.
func (v *VI) reg32(off uint32) uint32 {
	return uint32(v.regs[off/2])<<16 | uint32(v.regs[off/2+1])
}

// fieldsPerFrame returns 1 for non-interlaced output
// (VIDCR NIN set) and 2 otherwise.
func (v *VI) fieldsPerFrame() int {
	if v.regs[regVIDCR/2]&(1<<2) != 0 {
		return 1
	}
	return 2
}

// linesPerField returns ACV + PRB + PSB, with PRB and PSB
// taken from the even-field register VIVTE (see the
// package timing model).
func (v *VI) linesPerField() int {
	acv := int(v.regs[regVITV/2] & 0x3FF)
	vte := v.regs[regVIVTE/2]
	psb := int(vte & 0x3FF)
	prb := int((vte >> 10) & 0x3F)
	return acv + prb + psb
}

// totalHalflines returns the halflines in one frame:
// 2 * linesPerField * fieldsPerFrame.
func (v *VI) totalHalflines() int {
	return 2 * v.linesPerField() * v.fieldsPerFrame()
}

// CurrentHalfline returns the current halfline position
// within the frame, in 0..totalHalflines-1.
func (v *VI) CurrentHalfline() int {
	total := v.totalHalflines()
	if total <= 0 {
		return 0
	}
	into := v.totalCycles % CyclesPerFrame
	return int(into * int64(total) / CyclesPerFrame)
}

// Advance accumulates cpuCycles and fires any enabled
// display interrupt whose target halfline the counter
// crossed during the advance, including across the frame
// wrap. An advance of a full frame or more crosses every
// target. Firing sets the register DIS bit.
func (v *VI) Advance(cpuCycles int) {
	if cpuCycles <= 0 {
		return
	}
	prev := v.CurrentHalfline()
	v.totalCycles += int64(cpuCycles)
	cur := v.CurrentHalfline()
	fullFrame := cpuCycles >= CyclesPerFrame
	for i := 0; i < 4; i++ {
		off := uint32(regVIDI0 + 4*i)
		r := v.reg32(off)
		if r&dilDIE == 0 || r&dilDIS != 0 {
			continue
		}
		target := int(r & dilTargetMask)
		crossed := false
		switch {
		case fullFrame:
			crossed = true
		case cur >= prev:
			crossed = prev < target && target <= cur
		default: // wrapped at the frame boundary
			crossed = target > prev || target <= cur
		}
		if crossed {
			v.regs[off/2] |= dilDISLaneBit
		}
	}
}

// Pending reports whether any display interrupt register
// has both its status (DIS) and enable (DIE) bits set.
func (v *VI) Pending() bool {
	for i := 0; i < 4; i++ {
		r := v.reg32(uint32(regVIDI0 + 4*i))
		if r&dilDIS != 0 && r&dilDIE != 0 {
			return true
		}
	}
	return false
}

// SerializeSize is the fixed save-state size: a version
// byte, the register lanes, and the cycle accumulator.
const SerializeSize = 1 + regEnd + 8

const serializeVersion = 1

// ErrBadState reports a state block of the wrong size or
// version.
var ErrBadState = errors.New("vi: bad save state")

// Serialize writes the complete VI state.
func (v *VI) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	off := 1
	for _, lane := range v.regs {
		binary.BigEndian.PutUint16(out[off:], lane)
		off += 2
	}
	binary.BigEndian.PutUint64(out[off:], uint64(v.totalCycles))
	off += 8
	if off != SerializeSize {
		panic("vi: serialize size mismatch")
	}
	return out
}

// Deserialize restores a state written by Serialize.
func (v *VI) Deserialize(in []byte) error {
	if len(in) != SerializeSize || in[0] != serializeVersion {
		return ErrBadState
	}
	off := 1
	for i := range v.regs {
		v.regs[i] = binary.BigEndian.Uint16(in[off:])
		off += 2
	}
	v.totalCycles = int64(binary.BigEndian.Uint64(in[off:]))
	return nil
}
