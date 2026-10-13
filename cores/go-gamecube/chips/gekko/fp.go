package gekko

import (
	"math"
	"math/big"
)

// FPSCR bit constants. The comments give the PowerPC bit
// number (bit 0 is the most significant bit of the register);
// the constant value is the conventional uint32 mask, so
// PowerPC bit n has value 1<<(31-n).
const (
	fpscrFX     uint32 = 1 << 31 // bit 0: exception summary, sticky
	fpscrFEX    uint32 = 1 << 30 // bit 1: enabled exception summary
	fpscrVX     uint32 = 1 << 29 // bit 2: invalid operation summary
	fpscrOX     uint32 = 1 << 28 // bit 3: overflow, sticky
	fpscrUX     uint32 = 1 << 27 // bit 4: underflow, sticky
	fpscrZX     uint32 = 1 << 26 // bit 5: zero divide, sticky
	fpscrXX     uint32 = 1 << 25 // bit 6: inexact, sticky
	fpscrVXSNAN uint32 = 1 << 24 // bit 7: invalid, signaling NaN input
	fpscrVXISI  uint32 = 1 << 23 // bit 8: invalid, Inf minus Inf
	fpscrVXIDI  uint32 = 1 << 22 // bit 9: invalid, Inf over Inf
	fpscrVXZDZ  uint32 = 1 << 21 // bit 10: invalid, zero over zero
	fpscrVXIMZ  uint32 = 1 << 20 // bit 11: invalid, Inf times zero
	fpscrVXVC   uint32 = 1 << 19 // bit 12: invalid, compare with NaN
	fpscrFR     uint32 = 1 << 18 // bit 13: fraction rounded (per operation)
	fpscrFI     uint32 = 1 << 17 // bit 14: fraction inexact (per operation)
	// Bits 15..19 (value bits 16..12) are FPRF, the result
	// class field. Its low four bits, value bits 15..12, are
	// also the comparison FPCC nibble: less, greater, equal,
	// unordered, in that order from value bit 15 down.
	fpscrFPRFMask uint32 = 0x1F << 12
	fpscrVXSOFT   uint32 = 1 << 10 // bit 21: invalid, software request
	fpscrVXSQRT   uint32 = 1 << 9  // bit 22: invalid, square root
	fpscrVXCVI    uint32 = 1 << 8  // bit 23: invalid, integer conversion
	fpscrVE       uint32 = 1 << 7  // bit 24: invalid exception enable
	fpscrOE       uint32 = 1 << 6  // bit 25: overflow enable
	fpscrUE       uint32 = 1 << 5  // bit 26: underflow enable
	fpscrZE       uint32 = 1 << 4  // bit 27: zero divide enable
	fpscrXE       uint32 = 1 << 3  // bit 28: inexact enable
	fpscrNI       uint32 = 1 << 2  // bit 29: non-IEEE mode
	// Bits 30..31 (value bits 1..0) are RN, the rounding
	// mode: 0 nearest even, 1 toward zero, 2 toward +Inf,
	// 3 toward -Inf.
	fpscrRNMask uint32 = 3
)

// FPRF result class encodings (the 5-bit field value, before
// shifting into value bits 16..12). The field reads as a
// class bit followed by the less, greater, equal, unordered
// bits, which is why the patterns are not a plain code.
const (
	fprfQNaN    uint32 = 0x11 // 10001
	fprfNegInf  uint32 = 0x09 // 01001
	fprfNegNorm uint32 = 0x08 // 01000
	fprfNegDen  uint32 = 0x18 // 11000
	fprfNegZero uint32 = 0x12 // 10010
	fprfPosZero uint32 = 0x02 // 00010
	fprfPosDen  uint32 = 0x14 // 10100
	fprfPosNorm uint32 = 0x04 // 00100
	fprfPosInf  uint32 = 0x05 // 00101
)

// Canonical quiet NaN patterns produced by invalid
// operations and propagated from quiet NaN inputs.
const (
	qnanDoubleBits uint64 = 0x7FF8000000000000
	qnanSingleBits uint32 = 0x7FC00000
)

// fpFmt describes one IEEE binary format.
type fpFmt struct {
	prec  int // significand bits including the hidden bit
	emin  int // minimum normal exponent
	emax  int // maximum normal exponent
	bias  int
	total int // total bits, 32 or 64
	frac  int // stored fraction bits
}

var (
	fmtDouble = fpFmt{prec: 53, emin: -1022, emax: 1023, bias: 1023, total: 64, frac: 52}
	fmtSingle = fpFmt{prec: 24, emin: -126, emax: 127, bias: 127, total: 32, frac: 23}
)

// fpStatus accumulates the outcome of one operation (or, for
// paired singles, the merge of two lanes) before it is
// committed to FPSCR by applyFPStatus.
type fpStatus struct {
	invalid  uint32 // OR of the fpscrVX* detail masks raised
	overflow bool
	under    bool
	zerodiv  bool
	inexact  bool
	fr       bool
	fprf     uint32 // result class to write; skipped when noFPRF
	noFPRF   bool
}

func (s *fpStatus) merge(o fpStatus) {
	s.invalid |= o.invalid
	s.overflow = s.overflow || o.overflow
	s.under = s.under || o.under
	s.zerodiv = s.zerodiv || o.zerodiv
	s.inexact = s.inexact || o.inexact
	s.fr = s.fr || o.fr
}

// applyFPStatus commits a status to FPSCR: sticky detail
// and exception bits latch, VX is recomputed as the OR of
// every invalid detail bit, FI and FR and FPRF describe this
// operation, FX latches when this operation raised any
// exception, and FEX is recomputed from the summary bits
// gated by their enables. When rc is set, CR1 receives
// FPSCR bits 0..3 (FX, FEX, VX, OX) as the architecture
// specifies for floating point record forms.
func (c *CPU) applyFPStatus(st fpStatus, rc bool) {
	f := c.FPSCR
	raised := st.invalid != 0 || st.overflow || st.under || st.zerodiv || st.inexact
	f |= st.invalid
	if st.overflow {
		f |= fpscrOX
	}
	if st.under {
		f |= fpscrUX
	}
	if st.zerodiv {
		f |= fpscrZX
	}
	if st.inexact {
		f |= fpscrXX
	}
	// VX is a pure summary of the invalid detail bits.
	const detail = fpscrVXSNAN | fpscrVXISI | fpscrVXIDI | fpscrVXZDZ |
		fpscrVXIMZ | fpscrVXVC | fpscrVXSOFT | fpscrVXSQRT | fpscrVXCVI
	if f&detail != 0 {
		f |= fpscrVX
	} else {
		f &^= fpscrVX
	}
	if raised {
		f |= fpscrFX
	}
	// Per-operation bits.
	f &^= fpscrFI | fpscrFR
	if st.inexact {
		f |= fpscrFI
	}
	if st.fr {
		f |= fpscrFR
	}
	if !st.noFPRF {
		f = f&^fpscrFPRFMask | (st.fprf&0x1F)<<12
	}
	// Enabled exception summary.
	f &^= fpscrFEX
	if f&fpscrVX != 0 && f&fpscrVE != 0 ||
		f&fpscrOX != 0 && f&fpscrOE != 0 ||
		f&fpscrUX != 0 && f&fpscrUE != 0 ||
		f&fpscrZX != 0 && f&fpscrZE != 0 ||
		f&fpscrXX != 0 && f&fpscrXE != 0 {
		f |= fpscrFEX
	}
	c.FPSCR = f
	if rc {
		// CR1 takes FPSCR bits 0..3: the top nibble.
		c.setCRField(1, f>>28)
	}
}

// roundingMode returns the FPSCR RN field.
func (c *CPU) roundingMode() int { return int(c.FPSCR & fpscrRNMask) }

// classOf classifies a double bit pattern into class and
// sign for FPRF and for the operand dispatch helpers.
type fpClass int

const (
	classQNaN fpClass = iota
	classSNaN
	classPosInf
	classNegInf
	classPosNorm
	classNegNorm
	classPosDenorm
	classNegDenorm
	classPosZero
	classNegZero
)

func classDouble(bits uint64) fpClass {
	exp := (bits >> 52) & 0x7FF
	frac := bits & (1<<52 - 1)
	neg := bits>>63 != 0
	switch {
	case exp == 0x7FF && frac != 0:
		if frac&(1<<51) != 0 {
			return classQNaN
		}
		return classSNaN
	case exp == 0x7FF:
		if neg {
			return classNegInf
		}
		return classPosInf
	case exp == 0 && frac == 0:
		if neg {
			return classNegZero
		}
		return classPosZero
	case exp == 0:
		if neg {
			return classNegDenorm
		}
		return classPosDenorm
	default:
		if neg {
			return classNegNorm
		}
		return classPosNorm
	}
}

func classSingle(bits uint32) fpClass {
	exp := (bits >> 23) & 0xFF
	frac := bits & (1<<23 - 1)
	neg := bits>>31 != 0
	switch {
	case exp == 0xFF && frac != 0:
		if frac&(1<<22) != 0 {
			return classQNaN
		}
		return classSNaN
	case exp == 0xFF:
		if neg {
			return classNegInf
		}
		return classPosInf
	case exp == 0 && frac == 0:
		if neg {
			return classNegZero
		}
		return classPosZero
	case exp == 0:
		if neg {
			return classNegDenorm
		}
		return classPosDenorm
	default:
		if neg {
			return classNegNorm
		}
		return classPosNorm
	}
}

func (cl fpClass) isNaN() bool  { return cl == classQNaN || cl == classSNaN }
func (cl fpClass) isInf() bool  { return cl == classPosInf || cl == classNegInf }
func (cl fpClass) isZero() bool { return cl == classPosZero || cl == classNegZero }
func (cl fpClass) neg() bool {
	switch cl {
	case classNegInf, classNegNorm, classNegDenorm, classNegZero:
		return true
	}
	return false
}

// fprfOf maps a class to its FPRF encoding.
func fprfOf(cl fpClass) uint32 {
	switch cl {
	case classQNaN, classSNaN:
		return fprfQNaN
	case classPosInf:
		return fprfPosInf
	case classNegInf:
		return fprfNegInf
	case classPosNorm:
		return fprfPosNorm
	case classNegNorm:
		return fprfNegNorm
	case classPosDenorm:
		return fprfPosDen
	case classNegDenorm:
		return fprfNegDen
	case classPosZero:
		return fprfPosZero
	default:
		return fprfNegZero
	}
}

// flushNI applies the FPSCR NI (non-IEEE) input rule: a
// denormal input is read as a signed zero. The double view
// is used for scalar operands and the single view for single
// and paired operands, whose denormals are normals when
// widened to a double.
func (c *CPU) flushNIDouble(bits uint64) uint64 {
	if c.FPSCR&fpscrNI == 0 {
		return bits
	}
	if cl := classDouble(bits); cl == classPosDenorm || cl == classNegDenorm {
		return bits & (1 << 63)
	}
	return bits
}

func (c *CPU) flushNISingleValue(bits uint32) uint32 {
	if c.FPSCR&fpscrNI == 0 {
		return bits
	}
	if cl := classSingle(bits); cl == classPosDenorm || cl == classNegDenorm {
		return bits & (1 << 31)
	}
	return bits
}

// ratOf converts a finite float64 to its exact rational
// value. Callers guarantee finiteness.
func ratOf(f float64) *big.Rat {
	return new(big.Rat).SetFloat64(f)
}

func pow2Rat(e int) *big.Rat {
	if e >= 0 {
		return new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(e)))
	}
	return new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), uint(-e)))
}

// ratExp returns e with 2^e <= a < 2^(e+1) for a positive a.
func ratExp(a *big.Rat) int {
	e := a.Num().BitLen() - a.Denom().BitLen()
	// Correct the estimate by at most one step either way.
	for a.Cmp(pow2Rat(e)) < 0 {
		e--
	}
	for a.Cmp(pow2Rat(e+1)) >= 0 {
		e++
	}
	return e
}

// roundOutcome is a rounded value plus its exactness facts.
type roundOutcome struct {
	bits     uint64 // format bits; singles use the low 32
	inexact  bool
	fr       bool // rounded magnitude exceeds the exact magnitude
	overflow bool
	under    bool // inexact result in the subnormal or zero range
}

// roundRatTo rounds the exact signed rational q to format f
// under rounding mode mode (the FPSCR RN encoding). zeroSign
// supplies the sign for an exact zero input, whose sign the
// rational cannot carry.
func roundRatTo(q *big.Rat, zeroSign bool, f fpFmt, mode int) roundOutcome {
	var sign uint64
	if q.Sign() < 0 || q.Sign() == 0 && zeroSign {
		sign = 1
	}
	signBit := sign << uint(f.total-1)
	if q.Sign() == 0 {
		return roundOutcome{bits: signBit}
	}
	abs := new(big.Rat).Abs(new(big.Rat).Set(q))
	e := ratExp(abs)
	effE := e
	if effE < f.emin {
		effE = f.emin
	}
	shift := effE - f.prec + 1
	// scaled = abs / 2^shift, a positive rational. n is its
	// floor and rem/den the leftover fraction.
	scaled := new(big.Rat).Set(abs)
	if shift >= 0 {
		scaled.Quo(scaled, pow2Rat(shift))
	} else {
		scaled.Mul(scaled, pow2Rat(-shift))
	}
	n := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	rem := new(big.Int).Rem(scaled.Num(), scaled.Denom())
	inexact := rem.Sign() != 0
	inc := false
	if inexact {
		switch mode {
		case 0: // nearest, ties to even
			twice := new(big.Int).Lsh(rem, 1)
			switch twice.Cmp(scaled.Denom()) {
			case 1:
				inc = true
			case 0:
				inc = n.Bit(0) != 0
			}
		case 1: // toward zero: never increment the magnitude
		case 2: // toward +Inf
			inc = sign == 0
		case 3: // toward -Inf
			inc = sign != 0
		}
	}
	if inc {
		n.Add(n, big.NewInt(1))
	}
	out := roundOutcome{inexact: inexact, fr: inc}
	// A mantissa that rounded up to a full extra bit moves
	// to the next exponent (normal range only).
	if effE == e && n.BitLen() > f.prec {
		n.Rsh(n, 1)
		e++
		effE++
	}
	if e > f.emax {
		out.overflow = true
		out.inexact = true
		out.under = false
		toInf := false
		switch mode {
		case 0:
			toInf = true
		case 2:
			toInf = sign == 0
		case 3:
			toInf = sign != 0
		}
		if toInf {
			out.bits = signBit | uint64(2*f.bias+1)<<uint(f.frac)
			out.fr = true
		} else {
			// Largest finite value of the format.
			out.bits = maxFiniteBits(f, sign != 0)
		}
		return out
	}
	if n.Sign() == 0 {
		// A nonzero value rounded to zero: underflow.
		out.bits = signBit
		out.under = inexact
		return out
	}
	half := new(big.Int).Lsh(big.NewInt(1), uint(f.prec-1))
	if effE == f.emin && e < f.emin {
		// Subnormal quantum: n counts steps of
		// 2^(emin-prec+1). Reaching half is the smallest
		// normal, which is not an underflow result.
		if n.Cmp(half) >= 0 {
			out.bits = signBit | 1<<uint(f.frac) | (n.Uint64() - half.Uint64())
		} else {
			out.bits = signBit | n.Uint64()
			out.under = inexact
		}
		return out
	}
	expField := uint64(e + f.bias)
	frac := n.Uint64() - half.Uint64()
	out.bits = signBit | expField<<uint(f.frac) | frac
	return out
}

// maxFiniteBits is a helper for tests and overflow paths.
func maxFiniteBits(f fpFmt, neg bool) uint64 {
	var sign uint64
	if neg {
		sign = 1 << uint(f.total-1)
	}
	return sign | uint64(f.emax+f.bias)<<uint(f.frac) | (uint64(1)<<uint(f.frac) - 1)
}

// infBits returns the infinity pattern of a format.
func infBits(f fpFmt, neg bool) uint64 {
	var sign uint64
	if neg {
		sign = 1 << uint(f.total-1)
	}
	return sign | uint64(2*f.bias+1)<<uint(f.frac)
}

// sqrtRat returns the square root of a positive finite
// rational rounded to format f. The root is computed with a
// 512-bit big.Float, whose value is then an exact rational
// and goes through the same exact rounding path as every
// other operation. 512 bits is far beyond the separation
// any 53-bit input can have from a rounding midpoint of the
// target format (midpoints squared differ from the input by
// a nonzero rational with a denominator bounded near
// 2^220), so the final rounding is the correctly rounded
// root.
func sqrtRat(x *big.Rat, f fpFmt, mode int) roundOutcome {
	bf := new(big.Float).SetPrec(512).SetRat(x)
	root := new(big.Float).SetPrec(512).Sqrt(bf)
	r, _ := root.Rat(nil)
	return roundRatTo(r, false, f, mode)
}

// recipSqrtRat returns 1/sqrt(x) for positive finite x with
// the same high precision argument as sqrtRat.
func recipSqrtRat(x *big.Rat, f fpFmt, mode int) roundOutcome {
	bf := new(big.Float).SetPrec(512).SetRat(x)
	root := new(big.Float).SetPrec(512).Sqrt(bf)
	inv := new(big.Float).SetPrec(512).Quo(new(big.Float).SetPrec(512).SetInt64(1), root)
	r, _ := inv.Rat(nil)
	return roundRatTo(r, false, f, mode)
}

// fpDesc is one floating point operand or unrounded
// intermediate reduced to sign, class, and (for finite
// values) an exact rational. Zero and infinity carry no
// rational; their sign lives in neg.
type fpDesc struct {
	kind fpClass
	neg  bool
	rat  *big.Rat // finite values only, signed
}

func descDouble(bits uint64) fpDesc {
	cl := classDouble(bits)
	d := fpDesc{kind: cl, neg: cl.neg()}
	if !cl.isNaN() && !cl.isInf() && !cl.isZero() {
		d.rat = ratOf(math.Float64frombits(bits))
	} else if cl.isZero() {
		d.rat = big.NewRat(0, 1)
	}
	return d
}

func descSingle(bits uint32) fpDesc {
	cl := classSingle(bits)
	d := fpDesc{kind: cl, neg: cl.neg()}
	if !cl.isNaN() && !cl.isInf() && !cl.isZero() {
		d.rat = ratOf(float64(math.Float32frombits(bits)))
	} else if cl.isZero() {
		d.rat = big.NewRat(0, 1)
	}
	return d
}

func (d fpDesc) negated() fpDesc {
	d.neg = !d.neg
	switch d.kind {
	case classPosInf:
		d.kind = classNegInf
	case classNegInf:
		d.kind = classPosInf
	case classPosNorm:
		d.kind = classNegNorm
	case classNegNorm:
		d.kind = classPosNorm
	case classPosDenorm:
		d.kind = classNegDenorm
	case classNegDenorm:
		d.kind = classPosDenorm
	case classPosZero:
		d.kind = classNegZero
	case classNegZero:
		d.kind = classPosZero
	}
	if d.rat != nil {
		d.rat = new(big.Rat).Neg(d.rat)
	}
	return d
}

func qnanResult(f fpFmt) uint64 {
	if f.total == 32 {
		return uint64(qnanSingleBits)
	}
	return qnanDoubleBits
}

func infResult(f fpFmt, neg bool) uint64 { return infBits(f, neg) }

func zeroResult(f fpFmt, neg bool) uint64 {
	if neg {
		return 1 << uint(f.total-1)
	}
	return 0
}

// roundedFromRat rounds a finite exact rational into a
// result bit pattern plus status, applying the NI result
// flush (a denormal result becomes a signed zero) when the
// CPU is in non-IEEE mode.
func (c *CPU) roundedFromRat(q *big.Rat, zeroSign bool, f fpFmt) (uint64, fpStatus) {
	out := roundRatTo(q, zeroSign, f, c.roundingMode())
	var st fpStatus
	st.inexact = out.inexact
	st.fr = out.fr
	st.overflow = out.overflow
	st.under = out.under
	bits := out.bits
	if c.FPSCR&fpscrNI != 0 {
		var cl fpClass
		if f.total == 32 {
			cl = classSingle(uint32(bits))
		} else {
			cl = classDouble(bits)
		}
		if cl == classPosDenorm || cl == classNegDenorm {
			bits = zeroResult(f, cl.neg())
			st.under = true
			st.inexact = true
		}
	}
	// Classify the final pattern for FPRF.
	if f.total == 32 {
		st.fprf = fprfOf(classSingle(uint32(bits)))
	} else {
		st.fprf = fprfOf(classDouble(bits))
	}
	return bits, st
}

// addDesc adds two descriptors exactly and rounds once.
func (c *CPU) addDesc(a, b fpDesc, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	if a.kind == classSNaN || b.kind == classSNaN {
		st.invalid |= fpscrVXSNAN
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if a.kind.isNaN() || b.kind.isNaN() {
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if a.kind.isInf() || b.kind.isInf() {
		if a.kind.isInf() && b.kind.isInf() && a.neg != b.neg {
			st.invalid |= fpscrVXISI
			st.fprf = fprfQNaN
			return qnanResult(f), st
		}
		inf := a
		if b.kind.isInf() {
			inf = b
		}
		st.fprf = fprfOf(inf.kind)
		return infResult(f, inf.neg), st
	}
	if a.kind.isZero() && b.kind.isZero() {
		neg := a.neg && b.neg
		if a.neg != b.neg && c.roundingMode() == 3 {
			neg = true
		}
		st.fprf = fprfOf(classPosZero)
		if neg {
			st.fprf = fprfNegZero
		}
		return zeroResult(f, neg), st
	}
	q := new(big.Rat).Add(a.rat, b.rat)
	zeroSign := false
	if q.Sign() == 0 {
		// Exact cancellation of nonzero values: +0, or
		// -0 when rounding toward -Inf.
		zeroSign = c.roundingMode() == 3
	}
	return c.roundedFromRat(q, zeroSign, f)
}

// mulDesc builds the unrounded product descriptor and
// reports the 0 times Inf invalid case separately, so FMA
// can reuse it without rounding the product.
func mulDesc(a, b fpDesc) (fpDesc, bool) {
	neg := a.neg != b.neg
	if a.kind.isInf() || b.kind.isInf() {
		other := a
		if a.kind.isInf() {
			other = b
		}
		if other.kind.isZero() {
			return fpDesc{}, true
		}
		kind := classPosInf
		if neg {
			kind = classNegInf
		}
		return fpDesc{kind: kind, neg: neg}, false
	}
	if a.kind.isZero() || b.kind.isZero() {
		kind := classPosZero
		if neg {
			kind = classNegZero
		}
		return fpDesc{kind: kind, neg: neg, rat: big.NewRat(0, 1)}, false
	}
	kind := classPosNorm
	if neg {
		kind = classNegNorm
	}
	return fpDesc{kind: kind, neg: neg, rat: new(big.Rat).Mul(a.rat, b.rat)}, false
}

func (c *CPU) mulOp(a, b fpDesc, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	if a.kind == classSNaN || b.kind == classSNaN {
		st.invalid |= fpscrVXSNAN
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if a.kind.isNaN() || b.kind.isNaN() {
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	p, invalid := mulDesc(a, b)
	if invalid {
		st.invalid |= fpscrVXIMZ
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if p.kind.isInf() {
		st.fprf = fprfOf(p.kind)
		return infResult(f, p.neg), st
	}
	if p.kind.isZero() {
		st.fprf = fprfOf(p.kind)
		return zeroResult(f, p.neg), st
	}
	return c.roundedFromRat(p.rat, p.neg, f)
}

func (c *CPU) divOp(a, b fpDesc, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	if a.kind == classSNaN || b.kind == classSNaN {
		st.invalid |= fpscrVXSNAN
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if a.kind.isNaN() || b.kind.isNaN() {
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	neg := a.neg != b.neg
	switch {
	case a.kind.isInf() && b.kind.isInf():
		st.invalid |= fpscrVXIDI
		st.fprf = fprfQNaN
		return qnanResult(f), st
	case a.kind.isZero() && b.kind.isZero():
		st.invalid |= fpscrVXZDZ
		st.fprf = fprfQNaN
		return qnanResult(f), st
	case b.kind.isZero():
		// Finite nonzero (or Inf) over zero.
		st.zerodiv = true
		st.fprf = fprfOf(classPosInf)
		if neg {
			st.fprf = fprfNegInf
		}
		return infResult(f, neg), st
	case a.kind.isInf():
		st.fprf = fprfOf(classPosInf)
		if neg {
			st.fprf = fprfNegInf
		}
		return infResult(f, neg), st
	case b.kind.isInf() || a.kind.isZero():
		st.fprf = fprfOf(classPosZero)
		if neg {
			st.fprf = fprfNegZero
		}
		return zeroResult(f, neg), st
	}
	q := new(big.Rat).Quo(a.rat, b.rat)
	return c.roundedFromRat(q, neg, f)
}

// fmaOp computes signP*(a*c) + signB*b as one operation with
// a single rounding: the product is never rounded before the
// addition. Negations are folded into the descriptors, so
// the fmsub and fnmadd family members share this path.
func (c *CPU) fmaOp(a, cc, b fpDesc, negProd, negAdd bool, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	for _, d := range []fpDesc{a, cc, b} {
		if d.kind == classSNaN {
			st.invalid |= fpscrVXSNAN
			st.fprf = fprfQNaN
			return qnanResult(f), st
		}
	}
	if a.kind.isNaN() || cc.kind.isNaN() || b.kind.isNaN() {
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	p, invalid := mulDesc(a, cc)
	if invalid {
		st.invalid |= fpscrVXIMZ
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if negProd {
		p = p.negated()
	}
	add := b
	if negAdd {
		add = add.negated()
	}
	return c.addDescMerged(p, add, f, st)
}

// addDescMerged is addDesc with a preloaded status (FMA uses
// it after the product descriptor has been formed).
func (c *CPU) addDescMerged(a, b fpDesc, f fpFmt, st fpStatus) (uint64, fpStatus) {
	if a.kind.isInf() || b.kind.isInf() {
		if a.kind.isInf() && b.kind.isInf() && a.neg != b.neg {
			st.invalid |= fpscrVXISI
			st.fprf = fprfQNaN
			return qnanResult(f), st
		}
		inf := a
		if b.kind.isInf() {
			inf = b
		}
		st.fprf = fprfOf(inf.kind)
		return infResult(f, inf.neg), st
	}
	if a.kind.isZero() && b.kind.isZero() {
		neg := a.neg && b.neg
		if a.neg != b.neg && c.roundingMode() == 3 {
			neg = true
		}
		st.fprf = fprfOf(classPosZero)
		if neg {
			st.fprf = fprfNegZero
		}
		return zeroResult(f, neg), st
	}
	q := new(big.Rat).Add(a.rat, b.rat)
	zeroSign := false
	if q.Sign() == 0 {
		zeroSign = c.roundingMode() == 3
	}
	bits, st2 := c.roundedFromRat(q, zeroSign, f)
	st2.invalid |= st.invalid
	return bits, st2
}

func (c *CPU) sqrtOp(a fpDesc, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	if a.kind == classSNaN {
		st.invalid |= fpscrVXSNAN
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if a.kind.isNaN() {
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	switch {
	case a.kind == classNegInf || a.kind == classNegNorm || a.kind == classNegDenorm:
		st.invalid |= fpscrVXSQRT
		st.fprf = fprfQNaN
		return qnanResult(f), st
	case a.kind == classPosInf:
		st.fprf = fprfPosInf
		return infResult(f, false), st
	case a.kind.isZero():
		st.fprf = fprfOf(a.kind)
		return zeroResult(f, a.neg), st
	}
	out := sqrtRat(a.rat, f, c.roundingMode())
	return c.finishSqrtOut(out, f)
}

func (c *CPU) finishSqrtOut(out roundOutcome, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	st.inexact = out.inexact
	st.fr = out.fr
	st.overflow = out.overflow
	st.under = out.under
	bits := out.bits
	if c.FPSCR&fpscrNI != 0 {
		var cl fpClass
		if f.total == 32 {
			cl = classSingle(uint32(bits))
		} else {
			cl = classDouble(bits)
		}
		if cl == classPosDenorm || cl == classNegDenorm {
			bits = zeroResult(f, cl.neg())
			st.under = true
			st.inexact = true
		}
	}
	if f.total == 32 {
		st.fprf = fprfOf(classSingle(uint32(bits)))
	} else {
		st.fprf = fprfOf(classDouble(bits))
	}
	return bits, st
}

// recipOp is the exact reciprocal, used by fres and ps_res:
// the result is the correctly rounded 1/x in the target
// format, not a table estimate.
func (c *CPU) recipOp(a fpDesc, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	if a.kind == classSNaN {
		st.invalid |= fpscrVXSNAN
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if a.kind.isNaN() {
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	switch {
	case a.kind.isZero():
		st.zerodiv = true
		st.fprf = fprfOf(classPosInf)
		if a.neg {
			st.fprf = fprfNegInf
		}
		return infResult(f, a.neg), st
	case a.kind.isInf():
		st.fprf = fprfOf(classPosZero)
		if a.neg {
			st.fprf = fprfNegZero
		}
		return zeroResult(f, a.neg), st
	}
	q := new(big.Rat).Quo(big.NewRat(1, 1), a.rat)
	return c.roundedFromRat(q, a.neg, f)
}

// recipSqrtOp is 1/sqrt(x), correctly rounded through the
// high precision path in recipSqrtRat.
func (c *CPU) recipSqrtOp(a fpDesc, f fpFmt) (uint64, fpStatus) {
	var st fpStatus
	if a.kind == classSNaN {
		st.invalid |= fpscrVXSNAN
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	if a.kind.isNaN() {
		st.fprf = fprfQNaN
		return qnanResult(f), st
	}
	switch {
	case a.kind == classNegInf || a.kind == classNegNorm || a.kind == classNegDenorm:
		st.invalid |= fpscrVXSQRT
		st.fprf = fprfQNaN
		return qnanResult(f), st
	case a.kind == classPosInf:
		st.fprf = fprfPosZero
		return zeroResult(f, false), st
	case a.kind.isZero():
		st.zerodiv = true
		st.fprf = fprfPosInf
		if a.neg {
			st.fprf = fprfNegInf
		}
		return infResult(f, a.neg), st
	}
	out := recipSqrtRat(a.rat, f, c.roundingMode())
	return c.finishSqrtOut(out, f)
}

// forceSingle converts a double FPR bit pattern to the
// single value a single-precision instruction operates on.
// Finite values round once through the exact path under the
// current rounding mode; operand pre-rounding records no
// FPSCR status of its own (documented simplification: on
// hardware the same conversion is part of the instruction).
func (c *CPU) forceSingle(bits uint64) uint32 {
	switch cl := classDouble(bits); {
	case cl == classQNaN || cl == classSNaN:
		return qnanSingleBits
	case cl.isInf():
		return uint32(infBits(fmtSingle, cl.neg()))
	case cl.isZero():
		return uint32(zeroResult(fmtSingle, cl.neg()))
	}
	out := roundRatTo(ratOf(math.Float64frombits(bits)), false, fmtSingle, c.roundingMode())
	return uint32(out.bits)
}

// scalarSingle returns the single-precision operand of a
// register: the NI flush in single terms, then the canonical
// single the FPR holds.
func (c *CPU) scalarSingle(reg int) fpDesc {
	b := c.forceSingle(c.FPR[reg])
	b = c.flushNISingleValue(b)
	return descSingle(b)
}

// scalarDouble returns the double-precision operand of a
// register with the NI input flush applied.
func (c *CPU) scalarDouble(reg int) fpDesc {
	return descDouble(c.flushNIDouble(c.FPR[reg]))
}

// storeSingleResult widens a single result bit pattern into
// the FPR double convention: FPR bits are the exact double
// value of the single.
func storeSingleResult(bits uint64) uint64 {
	s := uint32(bits)
	switch cl := classSingle(s); {
	case cl.isNaN():
		return qnanDoubleBits
	case cl.isInf():
		return infBits(fmtDouble, cl.neg())
	}
	return math.Float64bits(float64(math.Float32frombits(s)))
}

// recordCR1FromFPSCR applies the record rule for moves and
// other instructions that do not themselves change FPSCR:
// CR1 receives FPSCR bits 0..3.
func (c *CPU) recordCR1() {
	c.setCRField(1, c.FPSCR>>28)
}
