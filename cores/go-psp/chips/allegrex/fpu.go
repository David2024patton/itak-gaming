package allegrex

import (
	"math"
	"math/big"
)

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
// inputs are treated as signed zero before an ARITHMETIC operation,
// and denormal results are flushed to signed zero. Scalar compares
// are the exception: they see the true operand values (the
// pspautotests fpu_branch loop counts on 0 < denormal being true).
// Any NaN result is canonicalized to 0x7FC00000. FPU arithmetic
// never raises the FPE exception in slice 1; traps are masked and
// results follow IEEE 754 through the rules above.

const canonicalNaN = 0x7FC00000

// fcr31Mask is the set of FCSR bits a CTC1 write can change and
// a CFC1 read exposes: the rounding mode, flags, enables and
// cause fields (bits 0..16), FCC0 (bit 23) and FS (bit 24). The
// pspautotests fcr matrix pins the rest: writes to the FO and FN
// bits (22 and 21), the FCC1-7 slots (bits 25..31) and the
// unassigned bits are dropped, and FCC1-7 read back as zero even
// though the condition codes themselves work (they are kept in
// the same array bits internally, just never exposed).
const fcr31Mask = 0x0181FFFF

// fcsr returns the FPU control and status register (fcr31) as
// software reads it.
func (c *CPU) fcsr() uint32 { return c.fcr[31] & fcr31Mask }

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

// flushZero reports the FCSR FS bit: when set, the scalar FPU
// flushes denormal inputs and results to signed zero; when
// clear, denormals keep their true values. The pspautotests fpu
// matrix pins both halves (a min-normal times 0.5 yields the
// denormal with FS=0 and zero with FS=1). The VFPU always
// flushes and does not consult this bit.
func (c *CPU) flushZero() bool { return c.fcr[31]>>24&1 != 0 }

// fpIn reads a scalar FPU operand under the FS rule.
func (c *CPU) fpIn(bits uint32) float64 {
	if c.flushZero() {
		return fpInput(bits)
	}
	return float64(math.Float32frombits(bits))
}

// roundToSingle rounds x to a single precision bit pattern under
// the given MIPS rounding mode (0 RN, 1 RZ, 2 RP, 3 RM), flushing
// denormal results to signed zero and canonicalizing NaNs.
func roundToSingle(x float64, mode uint32) uint32 {
	return roundToSingleFlush(x, mode, true)
}

// roundToSingleFlush is roundToSingle with the denormal result
// flush made conditional (the scalar FPU flushes only under FS).
func roundToSingleFlush(x float64, mode uint32, flush bool) uint32 {
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
	case 1: // RZ: truncate toward zero. From the round to
		// nearest baseline that means stepping down in value for
		// a positive result and UP in value for a negative one
		// (an earlier build stepped down for both, which the
		// roundmode int to float matrix catches at -16777219).
		if fb == 0x7F800000 {
			fb = 0x7F7FFFFF
		} else if fb == 0xFF800000 {
			fb = 0xFF7FFFFF
		} else if x > 0 && fx > x {
			fb = nextDownBits(fb, x)
		} else if x < 0 && fx < x {
			fb = nextUpBits(fb)
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
	// Flush a denormal result to signed zero when asked.
	if flush && (fb>>23)&0xFF == 0 && fb&0x7FFFFF != 0 {
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
	case 0x02: // CFC1: fcr0 reads the FPU implementation ID,
		// fcr31 reads the FCSR through the readback mask,
		// fcr25..28 read 0.
		if fs == 0 {
			c.setReg(rt, 0x00003351)
		} else if fs == 31 {
			c.setReg(rt, c.fcsr())
		} else {
			c.setReg(rt, c.fcr[fs])
		}
	case 0x04: // MTC1
		c.fpr[fs] = c.regs[rt]
	case 0x06: // CTC1: only fcr31 (the FCSR) is writable, and
		// only through the writable mask; the internal FCC1-7
		// bits survive a CTC1 write.
		if fs == 31 {
			c.fcr[31] = c.fcr[31]&^fcr31Mask | c.regs[rt]&fcr31Mask
		}
	case 0x08: // BC1F/BC1T and the likely forms
		cc := int((instr >> 18) & 7)
		// Encoding: bits 20..18 cc, bit 17 nd (the likely flag:
		// annul the delay slot when the branch is not taken),
		// bit 16 tf. PSP-1 had tf and nd swapped, which the
		// pspautotests fpu_branch matrix refutes (bc1t never
		// branched, bc1fl annulled the wrong way).
		tf := (instr>>16)&1 != 0
		likely := (instr>>17)&1 != 0
		taken := c.fccRead(cc) == tf
		if likely {
			c.branchLikely(taken, c.branchTarget(signExt16(instr&0xFFFF)))
		} else {
			c.branch(taken, c.branchTarget(signExt16(instr&0xFFFF)))
		}
	case 0x10: // fmt S
		return c.execFPUS(instr, rt, fs, fd, fn)
	case 0x14: // fmt W
		if fn == 0x20 { // CVT.S.W
			v := float64(int32(c.fpr[fs]))
			c.fpr[fd] = roundToSingleFlush(v, c.roundingMode(), c.flushZero())
			return 1
		}
		c.raiseException(excRI, 0)
	default:
		// Includes fmt D and COP1X style encodings: reserved here.
		c.raiseException(excRI, 0)
	}
	return 1
}

// nanOperand returns the quieted bits of a NaN operand and true,
// or false for a non-NaN. Scalar arithmetic propagates a NaN
// operand's own bit pattern (sign and payload kept, quiet bit
// set) instead of canonicalizing, the behavior the pspautotests
// roundmode arithmetic matrix records (ffc00000 in gives
// ffc00000 out; the signaling 7f800001 gives 7fc00001). Only a
// NaN GENERATED by an invalid operation on non-NaN inputs takes
// the canonical 0x7FC00000.
func nanOperand(bits uint32) (uint32, bool) {
	if isNaNBits(bits) {
		return bits | 0x00400000, true
	}
	return 0, false
}

// addSubExact rounds the exact sum or difference of two FPU
// operands (already FS adjusted to float64; aNeg flips the second
// operand's sign for subtraction) to single precision under mode.
// The float64 sum is used when it is exact (the usual case); when
// the float64 rounding leaves a residual, the exact value is
// rebuilt with big.Rat and rounded once. Directed modes need the
// exact value: 2^-126 minus 0.5 truncates to 0xBEFFFFFF on
// hardware, but the float64 difference rounds to exactly 0.5
// first and the truncation is lost (the roundmode matrix pins
// this line).
func addSubExact(a, b float64, neg bool, mode uint32, flush bool) (uint32, *big.Rat) {
	bb := b
	if neg {
		bb = -b
	}
	s := a + bb
	if s == 0 {
		// IEEE zero sign rules, which the roundmode matrix's
		// mode 3 section pins (1.1 - 1.1 is -0 under RM, +0 in
		// the other modes): an exact cancellation is +0 except
		// under round toward minus infinity; a sum of two zeros
		// keeps the common sign, and mixed zeros follow the
		// same RM rule.
		neg := false
		if a == 0 && bb == 0 {
			neg = math.Signbit(a) && math.Signbit(bb)
			if math.Signbit(a) != math.Signbit(bb) {
				neg = mode == 3
			}
		} else {
			neg = mode == 3
		}
		bits := uint32(0)
		if neg {
			bits = 0x80000000
		}
		return bits, new(big.Rat)
	}
	if math.IsInf(a, 0) || math.IsInf(bb, 0) || math.IsNaN(s) {
		// Infinite operands: the float64 result (an infinity,
		// or NaN for Inf against -Inf) is already final, and
		// has no exact rational form.
		return roundToSingleFlush(s, mode, flush), nil
	}
	// TwoSum residual: exact when s does not overflow, which
	// float32 range operands guarantee.
	bv := s - a
	err := (a - (s - bv)) + (bb - bv)
	if err == 0 || math.IsInf(s, 0) {
		return roundToSingleFlush(s, mode, flush), new(big.Rat).SetFloat64(s)
	}
	ra := new(big.Rat).SetFloat64(a)
	rb := new(big.Rat).SetFloat64(bb)
	ra.Add(ra, rb)
	return ratToSingle(ra, mode, flush), ra
}

// Condition bit positions shared by the FCSR Cause field (bits
// 12..16) and Flags field (bits 2..6): I, U, O, Z, V from bit 0.
const (
	condI = 1 << iota
	condU
	condO
	condZ
	condV
)

// setFPUConditions records one operation's IEEE conditions: the
// Cause field is replaced, the Flags field accumulates (sticky).
func (c *CPU) setFPUConditions(cond uint32) {
	c.fcr[31] = c.fcr[31]&^(0x1F<<12) | (cond&0x1F)<<12
	c.fcr[31] |= (cond & 0x1F) << 2
}

// fpuValueConditions derives the overflow, underflow and inexact
// conditions of a finite exact result x (as a float64; for add
// and sub the caller passes the float64 view of the exact
// rational, which only feeds the comparisons here) from the
// rounded result bits.
func fpuValueConditions(x float64, bits uint32) uint32 {
	var cond uint32
	if math.IsInf(x, 0) {
		return condO | condI
	}
	ax := math.Abs(x)
	if ax > math.MaxFloat32 {
		cond |= condO | condI
		return cond
	}
	res := math.Abs(float64(math.Float32frombits(bits)))
	if res != ax {
		cond |= condI
		if ax < 0x1p-126 {
			cond |= condU
		}
	}
	return cond
}

// ratToSingle rounds an exact rational to a float32 bit pattern
// under a MIPS rounding mode, with the FS denormal flush applied
// when flush is set. The rational must be finite and nonzero
// handling is done by the caller for zero (sign rules differ).
func ratToSingle(r *big.Rat, mode uint32, flush bool) uint32 {
	if r.Sign() == 0 {
		return 0
	}
	sign := uint32(0)
	num := new(big.Int).Set(r.Num())
	if r.Sign() < 0 {
		sign = 0x80000000
		num.Abs(num)
	}
	den := r.Denom()
	// Find e with 2^e <= |r| < 2^(e+1): start from bit lengths
	// and correct by one comparison.
	e := num.BitLen() - den.BitLen()
	// compare |r| against 2^e: num/den vs 2^e
	cmp := 0
	if e >= 0 {
		cmp = num.Cmp(new(big.Int).Lsh(den, uint(e)))
	} else {
		cmp = new(big.Int).Lsh(num, uint(-e)).Cmp(den)
	}
	if cmp < 0 {
		e--
	}
	// Round |r| / 2^(e-23) to an integer mantissa with remainder.
	roundTo := func(exp int) (uint64, *big.Int, *big.Int) {
		// value = num/den / 2^(exp-23); return quotient, rem num, rem den
		shift := exp - 23
		n := new(big.Int).Set(num)
		d := new(big.Int).Set(den)
		if shift >= 0 {
			d.Lsh(d, uint(shift))
		} else {
			n.Lsh(n, uint(-shift))
		}
		q, rem := new(big.Int).QuoRem(n, d, new(big.Int))
		return q.Uint64(), rem, d
	}
	applyRound := func(q uint64, rem, d *big.Int) uint64 {
		if rem.Sign() == 0 {
			return q
		}
		up := false
		switch mode {
		case 0: // RN, ties to even
			twice := new(big.Int).Lsh(rem, 1)
			c := twice.Cmp(d)
			up = c > 0 || (c == 0 && q&1 == 1)
		case 1: // RZ
			up = false
		case 2: // RP: magnitude up only for positive values
			up = sign == 0
		case 3: // RM: magnitude up only for negative values
			up = sign != 0
		}
		if up {
			q++
		}
		return q
	}
	if e >= -126 {
		q, rem, d := roundTo(e)
		q = applyRound(q, rem, d)
		if q == 1<<24 {
			q >>= 1
			e++
		}
		if e > 127 {
			// Overflow: infinity under RN, largest finite
			// under directed modes toward zero, per sign.
			switch mode {
			case 0:
				return sign | 0x7F800000
			case 1:
				return sign | 0x7F7FFFFF
			case 2:
				if sign == 0 {
					return 0x7F800000
				}
				return sign | 0x7F7FFFFF
			case 3:
				if sign != 0 {
					return sign | 0x7F800000
				}
				return sign | 0x7F7FFFFF
			}
		}
		return sign | uint32(e+127)<<23 | uint32(q&0x7FFFFF)
	}
	// Subnormal range: roundTo(-126) yields the mantissa at
	// 2^-149 granularity directly.
	q2, rem3, d3 := roundTo(-126)
	q2 = applyRound(q2, rem3, d3)
	if q2 == 0 {
		// Underflow to zero; directed modes away from zero
		// produce the smallest denormal.
		if (mode == 2 && sign == 0) || (mode == 3 && sign != 0) {
			if flush {
				return sign
			}
			return sign | 1
		}
		return sign
	}
	if flush {
		return sign
	}
	if q2 >= 1<<23 {
		// Rounded up into the smallest normal.
		return sign | 0x00800000
	}
	return sign | uint32(q2)
}

func (c *CPU) execFPUS(instr uint32, ft, fs, fd int, fn uint32) int {
	mode := c.roundingMode()
	switch fn {
	case 0x00, 0x01, 0x02, 0x03: // ADD.S SUB.S MUL.S DIV.S
		if nb, ok := nanOperand(c.fpr[fs]); ok {
			c.fpr[fd] = nb
			c.setFPUConditions(condV)
		} else if nb, ok := nanOperand(c.fpr[ft]); ok {
			c.fpr[fd] = nb
			c.setFPUConditions(condV)
		} else {
			a, b := c.fpIn(c.fpr[fs]), c.fpIn(c.fpr[ft])
			switch fn {
			case 0x00, 0x01:
				bits, exact := addSubExact(a, b, fn == 0x01, mode, c.flushZero())
				c.fpr[fd] = bits
				if exact == nil {
					// Infinite operand: NaN result means the
					// invalid Inf against -Inf form.
					if isNaNBits(bits) {
						c.setFPUConditions(condV)
					} else {
						c.setFPUConditions(0)
					}
					break
				}
				xf, _ := exact.Float64()
				c.setFPUConditions(fpuValueConditions(xf, bits))
			case 0x02:
				x := a * b
				bits := roundToSingleFlush(x, mode, c.flushZero())
				c.fpr[fd] = bits
				cond := fpuValueConditions(x, bits)
				if (a == 0 && math.IsInf(b, 0)) || (b == 0 && math.IsInf(a, 0)) {
					cond = condV
				}
				c.setFPUConditions(cond)
			case 0x03:
				x := a / b
				bits := roundToSingleFlush(x, mode, c.flushZero())
				c.fpr[fd] = bits
				cond := uint32(0)
				switch {
				case a == 0 && b == 0, math.IsInf(a, 0) && math.IsInf(b, 0):
					cond = condV
				case b == 0 && a != 0 && !math.IsInf(a, 0):
					cond = condZ
				default:
					cond = fpuValueConditions(x, bits)
				}
				c.setFPUConditions(cond)
			}
		}
		if fn == 0x03 {
			return 12
		}
	case 0x04: // SQRT.S
		if nb, ok := nanOperand(c.fpr[fs]); ok {
			c.fpr[fd] = nb
			c.setFPUConditions(condV)
		} else {
			a := c.fpIn(c.fpr[fs])
			x := math.Sqrt(a)
			bits := roundToSingleFlush(x, mode, c.flushZero())
			c.fpr[fd] = bits
			if a < 0 {
				c.setFPUConditions(condV)
			} else {
				c.setFPUConditions(fpuValueConditions(x, bits))
			}
		}
		return 12
	case 0x05: // ABS.S
		c.fpr[fd] = c.fpr[fs] & 0x7FFFFFFF
	case 0x06: // MOV.S
		c.fpr[fd] = c.fpr[fs]
	case 0x07: // NEG.S
		c.fpr[fd] = c.fpr[fs] ^ 0x80000000
	case 0x0C, 0x0D, 0x0E, 0x0F: // ROUND.W.S TRUNC.W.S CEIL.W.S FLOOR.W.S
		m := []uint32{0, 1, 2, 3}[fn-0x0C]
		c.fpr[fd] = cvtToWord(c.fpIn(c.fpr[fs]), m)
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
		c.fpr[fd] = cvtToWord(c.fpIn(c.fpr[fs]), mode)
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
// an explicit rounding mode. The conversion saturates by sign:
// results at or past the positive limit produce 0x7FFFFFFF and
// results past the negative limit produce 0x80000000. NaN of
// either sign produces 0x7FFFFFFF. That is the Allegrex behavior
// the pspautotests roundmode and fpu matrices record. An earlier
// build returned 0x80000000 for every invalid case on a
// "documented MIPS convention" claim the corpus refutes.
func cvtToWord(v float64, mode uint32) uint32 {
	if math.IsNaN(v) {
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
	if r >= 2147483648.0 {
		return 0x7FFFFFFF
	}
	if r < -2147483648.0 {
		return 0x80000000
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
	// Compares see operands under the same FS rule as
	// arithmetic; with FS clear (the thread default) denormal
	// inputs keep their true values. The pspautotests
	// fpu_branch loop pins this (c.lt.s of 0 against a positive
	// denormal is true on hardware, and its bc1tl loop counts 4;
	// an earlier build flushed compare inputs unconditionally
	// and the loop never branched).
	a := c.fpIn(aBits)
	b := c.fpIn(bBits)
	lt := a < b
	eq := a == b
	// The 4-bit condition selects predicate terms: bit 0 adds
	// "or unordered", bit 1 adds equality, bit 2 adds less-than.
	// That one formula covers all sixteen C.cond forms (F, UN,
	// EQ, UEQ, OLT, ULT, OLE, ULE and the signaling set SF, NGLE,
	// SEQ, NGL, LT, NGE, LE, NGT, whose value bits mirror the
	// quiet set). An earlier build gated the unordered term on
	// bit 3, so C.UN.S never reported unordered (and C.SF.S
	// wrongly did); the pspautotests fpu matrix caught it.
	res := (unord && cond&1 != 0) || (lt && cond&4 != 0) || (eq && cond&2 != 0)
	cc := int((instr >> 8) & 7)
	c.fccWrite(cc, res)
}
