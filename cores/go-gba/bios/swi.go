package bios

import (
	"math"

	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
)

// softReset (SWI 0x00) restarts execution. The documented
// flag byte at 0x03007FFA selects the restart target: when
// it is zero, control goes back through the BIOS reset
// entry, which reruns the boot stub stack setup and enters
// the cartridge; when it is nonzero, control goes directly
// to the word stored at the handler pointer address with
// its low bit cleared.
//
// Choice made here, per the calling convention: in the
// zero flag case we Jump to the reset vector 0x00 so the
// boot stub re-establishes every stack exactly as at power
// on. In the flag case we Jump straight to the stored
// target, since the caller has already set up its own
// context there.
func (h *Handler) softReset(c *arm7tdmi.CPU) int {
	if h.rd8(0x03007FFA) != 0 {
		target := h.rd32(HandlerPtrAddr) &^ 1
		c.Jump(target)
		return 3
	}
	c.Jump(0x00)
	return 3
}

// clearRange zeroes the half open range [start, end) in
// word sized writes. Every range RegisterRamReset clears
// is word aligned with a word multiple length.
func (h *Handler) clearRange(start, end uint32) {
	for addr := start; addr < end; addr += 4 {
		h.wr32(addr, 0)
	}
}

// registerRamReset (SWI 0x01) clears the memory areas and
// IO ranges selected by the flag bits in R0. The top 0x200
// bytes of IWRAM hold the BIOS system area (stacks, the
// check word, the handler pointer) and are preserved.
func (h *Handler) registerRamReset(c *arm7tdmi.CPU, ret uint32) int {
	flags := c.R[0]
	work := 0
	if flags&(1<<0) != 0 {
		h.clearRange(0x02000000, 0x02040000) // EWRAM
		work += 0x10000
	}
	if flags&(1<<1) != 0 {
		h.clearRange(0x03000000, 0x03007E00) // IWRAM below the system area
		work += 0x1F80
	}
	if flags&(1<<2) != 0 {
		h.clearRange(0x05000000, 0x05000400) // palette RAM
		work += 0x100
	}
	if flags&(1<<3) != 0 {
		h.clearRange(0x06000000, 0x06018000) // VRAM
		work += 0x6000
	}
	if flags&(1<<4) != 0 {
		h.clearRange(0x07000000, 0x07000400) // OAM
		work += 0x100
	}
	if flags&(1<<5) != 0 {
		h.clearRange(0x04000100, 0x04000160) // SIO IO range
		work += 0x18
	}
	if flags&(1<<6) != 0 {
		h.clearRange(0x04000060, 0x040000B0) // sound IO range
		work += 0x14
	}
	if flags&(1<<7) != 0 {
		h.clearRange(0x04000000, 0x04000400) // all IO registers
		work += 0x100
	}
	c.Jump(ret)
	return 3 + work
}

// intrWait (SWI 0x04, and SWI 0x05 in its fixed form)
// waits for one of the masked IRQ bits to appear in the
// BIOS check word. The dispatcher ORs acknowledged IRQ
// flags into the check word as interrupts fire.
//
// If discard is false and a masked bit is already present,
// those bits are consumed and the call returns at once.
// If discard is true, any masked bits already present are
// cleared first and the call always blocks. While blocked
// the CPU is halted; Poll performs the wake side.
func (h *Handler) intrWait(c *arm7tdmi.CPU, discard bool, mask, ret uint32) int {
	word := h.rd32(CheckWordAddr)
	if !discard && word&mask != 0 {
		h.wr32(CheckWordAddr, word&^mask)
		c.Jump(ret)
		return 3
	}
	if discard {
		h.wr32(CheckWordAddr, word&^mask)
	}
	h.enterWait(c, waitIntr, mask, ret)
	return 3
}

// div performs the shared Div and DivArm math: the
// quotient truncates toward zero, the remainder takes the
// sign of the numerator, and R3 receives the absolute
// value of the quotient. Division by zero follows the
// documented BIOS behaviour: the quotient is 1 when the
// numerator is nonnegative and -1 otherwise, the remainder
// is the numerator, and R3 is 1.
func (h *Handler) div(c *arm7tdmi.CPU, num, den int32, ret uint32) int {
	var q, r int64
	n64, d64 := int64(num), int64(den)
	if den == 0 {
		if num >= 0 {
			q = 1
		} else {
			q = -1
		}
		r = n64
	} else {
		// int64 arithmetic gives the overflow case (the
		// most negative value divided by -1) a well defined
		// quotient of 2^31, whose low 32 bits are the value
		// the hardware style result carries.
		q = n64 / d64
		r = n64 - q*d64
	}
	absq := q
	if absq < 0 {
		absq = -absq
	}
	c.R[0] = uint32(int32(q))
	c.R[1] = uint32(int32(r))
	c.R[3] = uint32(absq)
	c.Jump(ret)
	return 10
}

// isqrt returns floor(sqrt(v)) for an unsigned 32 bit v.
// The float64 estimate is corrected with 64 bit products
// so values near the top of the range (where the float
// root can round up to 65536) land exactly.
func isqrt(v uint32) uint32 {
	r := uint64(math.Sqrt(float64(v)))
	for (r+1)*(r+1) <= uint64(v) {
		r++
	}
	for r*r > uint64(v) {
		r--
	}
	return uint32(r)
}

// angleScale converts radians to the BIOS angle unit: an
// unsigned 16 bit value where 0x10000 is a full turn. The
// result is rounded to the nearest unit and wrapped into
// [0, 0x10000).
func angleScale(radians float64) uint32 {
	scaled := int64(math.Round(radians * (0x10000 / (2 * math.Pi))))
	m := scaled % 0x10000
	if m < 0 {
		m += 0x10000
	}
	return uint32(m)
}

// arcTan (SWI 0x09) takes a tangent in signed 1.14 fixed
// point and returns its angle. The input is a full s32 but
// arctan saturates smoothly, so no clamping is needed
// beyond the natural range of math.Atan.
func arcTan(tan32 int32) uint32 {
	return angleScale(math.Atan(float64(tan32) / 16384.0))
}

// arcTan2 (SWI 0x0A) takes x in R0 and y in R1, both
// signed 1.14 fixed point, and returns the angle of the
// vector (x, y). The fixed point scale cancels in the
// ratio but is applied anyway so the zero vector behaves
// exactly like math.Atan2(0, 0).
func arcTan2(x32, y32 int32) uint32 {
	x := float64(x32) / 16384.0
	y := float64(y32) / 16384.0
	return angleScale(math.Atan2(y, x))
}

// cpuSetWork performs a CpuSet style transfer: count
// units of unitSize bytes (2 or 4) from src to dst. In
// copy mode each unit is read once from the advancing
// source; in fill mode the first unit is read once and
// written to every destination unit.
func (h *Handler) cpuSetWork(src, dst, count uint32, unitSize int, fill bool) {
	if unitSize == 4 {
		if fill {
			v := h.rd32(src)
			for i := uint32(0); i < count; i++ {
				h.wr32(dst+4*i, v)
			}
			return
		}
		for i := uint32(0); i < count; i++ {
			h.wr32(dst+4*i, h.rd32(src+4*i))
		}
		return
	}
	if fill {
		v := h.rd16(src)
		for i := uint32(0); i < count; i++ {
			h.wr16(dst+2*i, v)
		}
		return
	}
	for i := uint32(0); i < count; i++ {
		h.wr16(dst+2*i, h.rd16(src+2*i))
	}
}

// cpuSet (SWI 0x0B): R2 control bits 0 to 20 are the unit
// count, bit 24 selects fill over copy, bit 26 selects
// 32 bit units over 16 bit units.
func (h *Handler) cpuSet(c *arm7tdmi.CPU, ret uint32) int {
	control := c.R[2]
	count := control & 0x1FFFFF
	unitSize := 2
	if control&(1<<26) != 0 {
		unitSize = 4
	}
	h.cpuSetWork(c.R[0], c.R[1], count, unitSize, control&(1<<24) != 0)
	c.Jump(ret)
	return 3 + int(count)
}

// cpuFastSet (SWI 0x0C) has the same control layout but
// always moves 32 bit units, and the unit count is rounded
// up to a multiple of 8.
func (h *Handler) cpuFastSet(c *arm7tdmi.CPU, ret uint32) int {
	control := c.R[2]
	count := (control&0x1FFFFF + 7) &^ 7
	h.cpuSetWork(c.R[0], c.R[1], count, 4, control&(1<<24) != 0)
	c.Jump(ret)
	return 3 + int(count)
}
