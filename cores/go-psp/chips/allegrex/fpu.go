package allegrex

import "math"

// COP1 (FPU) implementation, single precision only.
//
// Arithmetic approach: Go float32 arithmetic rounds to nearest even
// only, and the Allegrex FCSR exposes all four MIPS rounding modes.
// Every operation is therefore computed in float64 and passed once
// through roundToSingle, which applies the FCSR rounding mode with
// a one ulp adjustment when the float64 value is not exactly
// representable in single precision. Caveat: division and square
// root are correctly rounded to float64 first and then rounded
// again to float32, a double rounding that can differ from true
// single precision rounding in rare edge cases; it is exact for
// the unit vectors this core is validated against.
//
// Denormal policy (flush to zero, matching the Allegrex): denormal
// inputs are treated as signed zero before an operation, and
// denormal results are flushed to signed zero. Any NaN result is
// canonicalized to 0x7FC00000. FPU arithmetic never raises the FPE
// exception in slice 1; traps are masked and results follow IEEE
// 754 through the rules above.

const canonicalNaN = 0x7FC00000

// fcsr returns the FPU control and status register (fcr31).
func (c *CPU) fcsr() uint32 { return c.fcr[31] }

func (c *CPU) roundingMode() uint32 { return c.fcr[31] & 3 }

// fccRead returns condition code bit cc: cc 0 lives at FCSR bit 23,
// cc 1..7 at FCSR bits 25..31.
func (c *CPU) fccRead(cc int) bool {
	if cc == 0 {
		return c.fcr[31]&(1<<23) != 0
	}
	return c.fcr[31]&(1<<uint(24+cc)) != 0
}

func (c *CPU) fccWrite(cc int, v bool) {
	if cc == 0 {
		if v {
			c.fcr[31] |= 1 << 23
		} else {
			c.fcr[31] &^= 1 << 23
		}
		return
	}
	if v {
		c.fcr[31] |= 1 << uint(24+cc)
	} else {
		c.fcr[31] &^= 1 << uint(24+cc)
	}
}

// fpInput converts raw register bits to the float64 operand value,
// flushing denormals to signed zero. NaN and Inf pass through.
func fpInput(bits uint32) float64 {
	exp := (bits >> 23) & 0xFF
	man := bits & 0x7FFFFF
	if exp == 0 && man != 0 {
		// Denormal input: signed zero.
		if bits>>31 != 0 {
			return math.Copysign(0, -1)
		}
		return 0
	}
	return float64(math.Float32frombits(bits))
}

func isNaNBits(bits uint32) bool {
	return (bits>>23)&0xFF == 0xFF && bits&0x7FFFFF != 0
}

// roundToSingle rounds x to a single precision bit pattern under
// the given MIPS rounding mode (0 RN, 1 RZ, 2 RP, 3 RM), flushing
// denormal results to signed zero and canonicalizing NaNs.
func roundToSingle(x float64, mode uint32) uint32 {
	if math.IsNaN(x) {
		return canonicalNaN
	}
	if math.IsInf(x, 0) {
		if x < 0 {
			return 0xFF800000
		}
		return 0x7F800000
	}
	if x == 0 {
		if math.Signbit(x) {
			return 0x80000000
		}
		return 0
	}
	f := float32(x) // round to nearest even baseline
	fb := math.Float32bits(f)
	fx := float64(f)
	switch mode {
	case 1: // RZ: truncate toward zero
		if (x > 0 && fx > x) || (x < 0 && fx < x) {
			fb = nextDownBits(fb, x)
		}
	case 2: // RP: toward plus infinity
		if fx < x {
			fb = nextUpBits(fb)
		}
	case 3: // RM: toward minus infinity
		if fx > x {
			fb = nextDownBits(fb, x)
		}
	}
	// Flush a denormal result to signed zero.
	if (fb>>23)&0xFF == 0 && fb&0x7FFFFF != 0 {
		return fb & 0x80000000
	}
	return fb
}

// nextUpBits returns the next float32 bit pattern above the given
// one in value. Sign magnitude encoding makes this bits+1 for
// positive values and bits-1 for negative ones.
func nextUpBits(b uint32) uint32 {
	if b == 0x7F800000 || b == 0xFF800000 {
		return b
	}
	if b>>31 == 0 {
		return b + 1
	}
	return b - 1
}

// nextDownBits returns the next float32 bit pattern below the given
// one in value. Stepping down from an infinity produced by overflow
// of a finite x lands on the largest finite value, which is the
// correct directed rounding result.
func nextDownBits(b uint32, x float64) uint32 {
	if b == 0x7F800000 { // +Inf from a finite x
		return 0x7F7FFFFF
	}
	if b == 0xFF800000 { // -Inf
		if x > -math.MaxFloat32*2 {
			return 0xFF7FFFFF
		}
		return b
	}
	if b>>31 == 0 {
		if b == 0 {
			return 0x80000001
		}
		return b - 1
	}
	return b + 1
}

func (c *CPU) execCOP1(instr uint32) int {
	if c.cp0[12][0]&statusCU1 == 0 {
		c.raiseException(excCpU, 1)
		return 1
	}
	rs := int((instr >> 21) & 31) // fmt field
	rt := int((instr >> 16) & 31) // ft, or GPR for moves
	fs := int((instr >> 11) & 31)
	fd := int((instr >> 6) & 31)
	fn := instr & 63

	switch rs {
	case 0x00: // MFC1
		c.setReg(rt, c.fpr[fs])
	case 0x02: // CFC1: fcr0 reads 0, fcr31 reads the FCSR
		c.setReg(rt, c.fcr[fs])
	case 0x04: // MTC1
		c.fpr[fs] = c.regs[rt]
	case 0x06: // CTC1: only fcr31 (the FCSR) is writable
		if fs == 31 {
			c.fcr[31] = c.regs[rt]
		}
	case 0x08, 0x09: // BC1F/BC1T, and likely variants
		cc := int((instr >> 18) & 7)
		// Encoding: bits 20..18 cc, bit 17 tf, bit 16 nd (likely
		// style nullify flag). The likely form annuls the delay
		// slot when the branch is not taken.
		tf := (instr>>17)&1 != 0
		likely := rs == 0x09 || (instr>>16)&1 != 0
		taken := c.fccRead(cc) == tf
		if likely && !taken {
			// Annul: skip the delay slot entirely.
			c.pc = c.nextPC
			c.nextPC = c.pc + 4
			c.inDelaySlot = false
			return 1
		}
		c.branch(taken, c.branchTarget(signExt16(instr&0xFFFF)))
	case 0x10: // fmt S
		return c.execFPUS(instr, rt, fs, fd, fn)
	case 0x14: // fmt W
		if fn == 0x20 { // CVT.S.W
			v := float64(int32(c.fpr[fs]))
			c.fpr[fd] = roundToSingle(v, c.roundingMode())
			return 1
		}
		c.raiseException(excRI, 0)
	default:
		// Includes fmt D and COP1X style encodings: reserved here.
		c.raiseException(excRI, 0)
	}
	return 1
}

func (c *CPU) execFPUS(instr uint32, ft, fs, fd int, fn uint32) int {
	mode := c.roundingMode()
	switch fn {
	case 0x00: // ADD.S
		c.fpr[fd] = roundToSingle(fpInput(c.fpr[fs])+fpInput(c.fpr[ft]), mode)
	case 0x01: // SUB.S
		c.fpr[fd] = roundToSingle(fpInput(c.fpr[fs])-fpInput(c.fpr[ft]), mode)
	case 0x02: // MUL.S
		c.fpr[fd] = roundToSingle(fpInput(c.fpr[fs])*fpInput(c.fpr[ft]), mode)
	case 0x03: // DIV.S
		c.fpr[fd] = roundToSingle(fpInput(c.fpr[fs])/fpInput(c.fpr[ft]), mode)
		return 12
	case 0x04: // SQRT.S
		c.fpr[fd] = roundToSingle(math.Sqrt(fpInput(c.fpr[fs])), mode)
		return 12
	case 0x05: // ABS.S
		c.fpr[fd] = c.fpr[fs] & 0x7FFFFFFF
	case 0x06: // MOV.S
		c.fpr[fd] = c.fpr[fs]
	case 0x07: // NEG.S
		c.fpr[fd] = c.fpr[fs] ^ 0x80000000
	case 0x0C, 0x0D, 0x0E, 0x0F: // ROUND.W.S TRUNC.W.S CEIL.W.S FLOOR.W.S
		m := []uint32{0, 1, 2, 3}[fn-0x0C]
		c.fpr[fd] = cvtToWord(c.fpr[fs], m)
	case 0x10, 0x11: // MOVF.S / MOVT.S
		// Slice encoding: cc is taken from the ft field bits
		// 20..18 (the same slot BC1 uses), fn bit 0 is tf, fd is
		// the destination and fs the source.
		cc := int((instr >> 18) & 7)
		want := fn&1 != 0
		if c.fccRead(cc) == want {
			c.fpr[fd] = c.fpr[fs]
		}
	case 0x24: // CVT.W.S uses the FCSR rounding mode
		c.fpr[fd] = cvtToWord(c.fpr[fs], mode)
	default:
		if fn >= 0x30 { // C.cond.S
			c.execCompare(instr, ft, fs, fn&0xF)
			return 1
		}
		// MOVZ.S/MOVN.S and everything else: reserved here.
		c.raiseException(excRI, 0)
	}
	return 1
}

// cvtToWord converts a float register to an int32 bit pattern under
// an explicit rounding mode. NaN and out of range values produce
// 0x7FFFFFFF, the documented MIPS convention (including negative
// out of range).
func cvtToWord(bits uint32, mode uint32) uint32 {
	v := fpInput(bits)
	if math.IsNaN(v) || v >= 2147483648.0 || v < -2147483648.0 {
		return 0x7FFFFFFF
	}
	var r float64
	switch mode {
	case 0: // RN, ties to even
		r = math.RoundToEven(v)
	case 1: // RZ
		r = math.Trunc(v)
	case 2: // RP
		r = math.Ceil(v)
	case 3: // RM
		r = math.Floor(v)
	}
	return uint32(int32(r))
}

// execCompare implements C.cond.S. The cond bits are
// [orUnordered, less, equal, signaling] from bit 3 down to bit 0.
// The signaling bit only selects exception behavior, which is
// masked in slice 1, so it changes nothing numerically.
func (c *CPU) execCompare(instr uint32, ft, fs int, cond uint32) {
	aBits, bBits := c.fpr[fs], c.fpr[ft]
	unord := isNaNBits(aBits) || isNaNBits(bBits)
	a, b := fpInput(aBits), fpInput(bBits)
	lt := a < b
	eq := a == b
	res := (unord && cond&8 != 0) || (lt && cond&4 != 0) || (eq && cond&2 != 0)
	cc := int((instr >> 8) & 7)
	c.fccWrite(cc, res)
}
