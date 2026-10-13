package gekko

import (
	"math"
	"math/big"
)

// This file executes the scalar floating point instructions:
// D and X form loads and stores, the opcode 59 single and
// opcode 63 double A forms, the opcode 63 X forms (rounding,
// conversion, moves, compares), and the FPSCR instructions.
// Paired singles live in ps.go.

// fpAvail gates every floating point and paired single
// instruction: with MSR[FP] clear the instruction raises the
// floating point unavailable exception.
func (c *CPU) fpAvail() {
	if c.MSR&MSRFP == 0 {
		c.fpUnavail()
	}
}

// cField extracts the frC operand field (instruction bits
// 21..25) of an A form.
func cField(w uint32) int { return int(w>>6) & 31 }

// aFormXO extracts the 5-bit extended opcode (bits 26..30)
// of an A form.
func aFormXO(w uint32) int { return int(w>>1) & 31 }

// load64 reads a doubleword through the existing load path
// so translation faults and DSI reporting are unchanged. In
// little-endian mode the two words swap roles because the
// per-word swap in load already reverses each word.
func (c *CPU) load64(ea uint32) uint64 {
	first := c.load(ea, 4)
	second := c.load(ea+4, 4)
	if c.MSR&MSRLE != 0 {
		// In little-endian mode the first word in memory
		// holds the low half of the doubleword.
		return uint64(second)<<32 | uint64(first)
	}
	return uint64(first)<<32 | uint64(second)
}

// store64 writes a doubleword, the mirror image of load64.
func (c *CPU) store64(ea uint32, v uint64) {
	hi := uint32(v >> 32)
	lo := uint32(v)
	if c.MSR&MSRLE != 0 {
		c.store(ea, 4, lo)
		c.store(ea+4, 4, hi)
		return
	}
	c.store(ea, 4, hi)
	c.store(ea+4, 4, lo)
}

func (c *CPU) fpAlign(ea uint32) {
	if ea&3 != 0 {
		c.alignFault(ea)
	}
}

// execFPLoadStoreD handles primary opcodes 48..55: lfs,
// lfsu, lfd, lfdu, stfs, stfsu, stfd, stfdu.
func (c *CPU) execFPLoadStoreD(w uint32) {
	c.fpAvail()
	op := w >> 26
	d := f1(w)
	a := f2(w)
	ea := c.base(a) + uint32(simm(w))
	c.fpAlign(ea)
	update := op&1 != 0
	switch op {
	case 48, 49: // lfs, lfsu
		v := c.load(ea, 4)
		if update {
			c.GPR[a] = ea
		}
		c.FPR[d] = storeSingleResult(uint64(v))
	case 50, 51: // lfd, lfdu
		v := c.load64(ea)
		if update {
			c.GPR[a] = ea
		}
		c.FPR[d] = v
	case 52, 53: // stfs, stfsu
		c.store(ea, 4, c.doubleToSingleBits(c.FPR[d]))
		if update {
			c.GPR[a] = ea
		}
	case 54, 55: // stfd, stfdu
		c.store64(ea, c.FPR[d])
		if update {
			c.GPR[a] = ea
		}
	}
}

// doubleToSingleBits converts an FPR double to the single
// bit pattern stfs stores, rounding once under the current
// FPSCR rounding mode. This is a store conversion, not an
// arithmetic operation, so it records no FPSCR status.
func (c *CPU) doubleToSingleBits(bits uint64) uint32 {
	return c.forceSingle(bits)
}

// execFPIndexed handles the opcode 31 floating point indexed
// loads and stores, reached from exec31.
func (c *CPU) execFPIndexed(w uint32, x int) {
	c.fpAvail()
	d := f1(w)
	a := f2(w)
	b := f3(w)
	ea := c.base(a) + c.GPR[b]
	c.fpAlign(ea)
	switch x {
	case 535, 567: // lfsx, lfsux
		v := c.load(ea, 4)
		if x == 567 {
			c.GPR[a] = ea
		}
		c.FPR[d] = storeSingleResult(uint64(v))
	case 599, 631: // lfdx, lfdux
		v := c.load64(ea)
		if x == 631 {
			c.GPR[a] = ea
		}
		c.FPR[d] = v
	case 663, 695: // stfsx, stfsux
		c.store(ea, 4, c.doubleToSingleBits(c.FPR[d]))
		if x == 695 {
			c.GPR[a] = ea
		}
	case 727, 759: // stfdx, stfdux
		c.store64(ea, c.FPR[d])
		if x == 759 {
			c.GPR[a] = ea
		}
	case 983: // stfiwx: store the low word of frS as an integer
		c.store(ea, 4, uint32(c.FPR[d]))
	}
}

// execFP59 handles opcode 59: single-precision A forms.
// Results are computed on single operands and stored
// widened to the double value of the single result.
func (c *CPU) execFP59(w uint32) {
	c.fpAvail()
	d := f1(w)
	a := f2(w)
	b := f3(w)
	cc := cField(w)
	rc := w&1 != 0
	xo := aFormXO(w)
	f := fmtSingle

	finish := func(bits uint64, st fpStatus) {
		c.FPR[d] = storeSingleResult(bits)
		c.applyFPStatus(st, rc)
	}

	switch xo {
	case 18, 20, 21: // fdivs, fsubs, fadds
		da, db := c.scalarSingle(a), c.scalarSingle(b)
		var bits uint64
		var st fpStatus
		switch xo {
		case 18:
			bits, st = c.divOp(da, db, f)
		case 20:
			bits, st = c.addDesc(da, db.negated(), f)
		default:
			bits, st = c.addDesc(da, db, f)
		}
		finish(bits, st)
	case 25: // fmuls: frA times frC
		da, dc := c.scalarSingle(a), c.scalarSingle(cc)
		bits, st := c.mulOp(da, dc, f)
		finish(bits, st)
	case 24: // fres: reciprocal of frB as a single
		db := c.scalarSingle(b)
		bits, st := c.recipOp(db, f)
		finish(bits, st)
	case 22: // fsqrts
		db := c.scalarSingle(b)
		bits, st := c.sqrtOp(db, f)
		finish(bits, st)
	case 28, 29, 30, 31: // fmsubs, fmadds, fnmsubs, fnmadds
		da, db, dc := c.scalarSingle(a), c.scalarSingle(b), c.scalarSingle(cc)
		negProd := xo == 30 || xo == 31
		negAdd := xo == 28 || xo == 31
		bits, st := c.fmaOp(da, dc, db, negProd, negAdd, f)
		finish(bits, st)
	default:
		c.illegalAt()
	}
}

// execFP63 handles opcode 63: double-precision A forms and
// all the X forms (moves, rounding and conversion, compares,
// FPSCR access).
func (c *CPU) execFP63(w uint32) {
	c.fpAvail()
	d := f1(w)
	a := f2(w)
	b := f3(w)
	cc := cField(w)
	rc := w&1 != 0
	f := fmtDouble

	// X forms carry a 10-bit extended opcode in bits
	// 21..30. Several share their low 5 bits with A-form
	// codes, so the X dispatch runs first on the full
	// 10-bit value; anything it does not claim falls to the
	// A-form dispatch on the low 5 bits.
	switch xo(w) {
	case 0, 32: // fcmpu, fcmpo
		c.execFPCompare(w, xo(w) == 32)
		return
	case 12: // frsp
		c.execFRSP(w)
		return
	case 14, 15: // fctiw, fctiwz
		c.execFCTIW(w, xo(w) == 15)
		return
	case 38: // mtfsb1
		c.execMTFSB(w, true)
		return
	case 40: // fneg
		c.FPR[d] = c.FPR[b] ^ (1 << 63)
		if rc {
			c.recordCR1()
		}
		return
	case 64: // mcrfs (not in the GCN-2 gate list; implemented
		// with its architected read-and-clear of the source
		// field's exception bits)
		crfD := int(w>>23) & 7
		crfS := int(w>>18) & 7
		nib := (c.FPSCR >> uint(28-4*crfS)) & 0xF
		c.setCRField(crfD, nib)
		// Clear the exception bits of the source field,
		// keeping the summary handling consistent with the
		// manual: fields other than FX's field clear fully.
		mask := uint32(0xF) << uint(28-4*crfS)
		c.FPSCR &^= mask & (fpscrFEX | fpscrVX | fpscrOX | fpscrUX |
			fpscrZX | fpscrXX | fpscrVXSNAN | fpscrVXISI | fpscrVXIDI |
			fpscrVXZDZ | fpscrVXIMZ | fpscrVXVC | fpscrVXSOFT |
			fpscrVXSQRT | fpscrVXCVI)
		return
	case 70: // mtfsb0
		c.execMTFSB(w, false)
		return
	case 72: // fmr
		c.FPR[d] = c.FPR[b]
		if rc {
			c.recordCR1()
		}
		return
	case 134: // mtfsfi
		crfD := int(w>>23) & 7
		imm := (w >> 12) & 0xF
		shift := uint(28 - 4*crfD)
		c.FPSCR = c.FPSCR&^(0xF<<shift) | imm<<shift
		if rc {
			c.recordCR1()
		}
		return
	case 136: // fnabs
		c.FPR[d] = c.FPR[b] | (1 << 63)
		if rc {
			c.recordCR1()
		}
		return
	case 264: // fabs
		c.FPR[d] = c.FPR[b] &^ (1 << 63)
		if rc {
			c.recordCR1()
		}
		return
	case 583: // mffs: FPSCR into the low word of frD
		c.FPR[d] = uint64(c.FPSCR)
		if rc {
			c.recordCR1()
		}
		return
	case 711: // mtfsf
		c.execMTFSF(w)
		return
	}

	// A forms, dispatched on the 5-bit extended opcode.
	switch aFormXO(w) {
	case 18, 20, 21: // fdiv, fsub, fadd
		da, db := c.scalarDouble(a), c.scalarDouble(b)
		var bits uint64
		var st fpStatus
		switch aFormXO(w) {
		case 18:
			bits, st = c.divOp(da, db, f)
		case 20:
			bits, st = c.addDesc(da, db.negated(), f)
		default:
			bits, st = c.addDesc(da, db, f)
		}
		c.FPR[d] = bits
		c.applyFPStatus(st, rc)
	case 25: // fmul: frA times frC
		da, dc := c.scalarDouble(a), c.scalarDouble(cc)
		bits, st := c.mulOp(da, dc, f)
		c.FPR[d] = bits
		c.applyFPStatus(st, rc)
	case 23: // fsel: frA >= 0 selects frC, else frB (NaN selects frB)
		da := c.scalarDouble(a)
		selC := !da.kind.isNaN() && (!da.neg || da.kind.isZero())
		var src uint64
		if selC {
			src = c.FPR[cc]
		} else {
			src = c.FPR[b]
		}
		c.FPR[d] = src
		if rc {
			c.recordCR1()
		}
	case 26: // frsqrte: correctly rounded 1/sqrt(frB)
		db := c.scalarDouble(b)
		bits, st := c.recipSqrtOp(db, f)
		c.FPR[d] = bits
		c.applyFPStatus(st, rc)
	case 22: // fsqrt
		db := c.scalarDouble(b)
		bits, st := c.sqrtOp(db, f)
		c.FPR[d] = bits
		c.applyFPStatus(st, rc)
	case 28, 29, 30, 31: // fmsub, fmadd, fnmsub, fnmadd
		da, db, dc := c.scalarDouble(a), c.scalarDouble(b), c.scalarDouble(cc)
		xo := aFormXO(w)
		negProd := xo == 30 || xo == 31
		negAdd := xo == 28 || xo == 31
		bits, st := c.fmaOp(da, dc, db, negProd, negAdd, f)
		c.FPR[d] = bits
		c.applyFPStatus(st, rc)
	default:
		c.illegalAt()
	}
}

// execFPCompare executes fcmpu and fcmpo: the target CR
// field and the FPSCR FPCC (the comparison nibble inside
// FPRF) both receive less, greater, equal, unordered.
func (c *CPU) execFPCompare(w uint32, ordered bool) {
	crfD := int(w>>23) & 7
	a, b := f2(w), f3(w)
	da, db := c.scalarDouble(a), c.scalarDouble(b)
	var st fpStatus
	unordered := da.kind.isNaN() || db.kind.isNaN()
	if da.kind == classSNaN || db.kind == classSNaN {
		st.invalid |= fpscrVXSNAN
	} else if unordered && ordered {
		st.invalid |= fpscrVXVC
	}
	var nib uint32
	switch {
	case unordered:
		nib = 1
		st.fprf = fprfQNaN
	default:
		fa := descValue(da)
		fb := descValue(db)
		switch {
		case fa < fb:
			nib = 8
			st.fprf = fprfNegNorm
		case fa > fb:
			nib = 4
			st.fprf = fprfPosNorm
		default:
			nib = 2
			st.fprf = fprfPosZero
		}
	}
	c.applyFPStatus(st, false)
	c.setCRField(crfD, nib)
}

// descValue returns a comparable float64 for a non-NaN
// descriptor (infinities and zeros included).
func descValue(d fpDesc) float64 {
	switch {
	case d.kind.isInf():
		if d.neg {
			return math.Inf(-1)
		}
		return math.Inf(1)
	case d.kind.isZero():
		if d.neg {
			return math.Copysign(0, -1)
		}
		return 0
	}
	f, _ := d.rat.Float64()
	return f
}

// execFRSP rounds frB to single precision and stores the
// widened result, as frsp does.
func (c *CPU) execFRSP(w uint32) {
	d, b := f1(w), f3(w)
	rc := w&1 != 0
	src := c.FPR[b]
	var st fpStatus
	switch cl := classDouble(src); {
	case cl == classSNaN:
		st.invalid |= fpscrVXSNAN
		st.fprf = fprfQNaN
		c.FPR[d] = qnanDoubleBits
	case cl.isNaN():
		st.fprf = fprfQNaN
		c.FPR[d] = qnanDoubleBits
	case cl.isInf():
		st.fprf = fprfOf(cl)
		c.FPR[d] = infBits(fmtDouble, cl.neg())
	case cl.isZero():
		st.fprf = fprfOf(cl)
		c.FPR[d] = zeroResult(fmtDouble, cl.neg())
	default:
		flushed := c.flushNIDouble(src)
		out := roundRatTo(ratOf(math.Float64frombits(flushed)), false, fmtSingle, c.roundingMode())
		st.inexact = out.inexact
		st.fr = out.fr
		st.overflow = out.overflow
		st.under = out.under
		res := storeSingleResult(out.bits)
		if c.FPSCR&fpscrNI != 0 {
			if cl2 := classSingle(uint32(out.bits)); cl2 == classPosDenorm || cl2 == classNegDenorm {
				res = zeroResult(fmtDouble, cl2.neg())
				st.under = true
				st.inexact = true
			}
		}
		st.fprf = fprfOf(classDouble(res))
		c.FPR[d] = res
	}
	c.applyFPStatus(st, rc)
}

// execFCTIW converts frB to a 32-bit signed integer word.
// Result representation (this core's documented choice, the
// 750 manual leaves the high word undefined): the low 32
// bits hold the saturated integer and the high 32 bits are
// the sign extension of that integer, all copies of its sign
// bit. Saturation follows the manual: above the range gives
// 0x7fffffff, below the range, infinities of either sign
// beyond the range, and NaN give the 0x80000000 low word
// (NaN converts to the most negative word on PowerPC), so
// -Inf, NaN, and under-range values all produce the full
// pattern 0xffffffff80000000.
func (c *CPU) execFCTIW(w uint32, trunc bool) {
	d, b := f1(w), f3(w)
	rc := w&1 != 0
	src := c.scalarDouble(b)
	var st fpStatus
	st.noFPRF = true
	var val int32
	switch {
	case src.kind == classSNaN:
		st.invalid |= fpscrVXSNAN | fpscrVXCVI
		val = math.MinInt32
	case src.kind.isNaN():
		st.invalid |= fpscrVXCVI
		val = math.MinInt32
	case src.kind == classPosInf:
		st.invalid |= fpscrVXCVI
		val = math.MaxInt32
	case src.kind == classNegInf:
		st.invalid |= fpscrVXCVI
		val = math.MinInt32
	default:
		mode := c.roundingMode()
		if trunc {
			mode = 1
		}
		iv, inexact, fr := roundRatToInt(src.rat, mode)
		st.inexact = inexact
		st.fr = fr
		switch {
		case iv.Cmp(big.NewInt(math.MaxInt32)) > 0:
			st.invalid |= fpscrVXCVI
			val = math.MaxInt32
		case iv.Cmp(big.NewInt(math.MinInt32)) < 0:
			st.invalid |= fpscrVXCVI
			val = math.MinInt32
		default:
			val = int32(iv.Int64())
		}
	}
	c.FPR[d] = uint64(int64(val))
	c.applyFPStatus(st, rc)
}

// roundRatToInt rounds a rational to an integer under a
// FPSCR rounding mode encoding, reporting whether the value
// changed and whether the magnitude grew.
func roundRatToInt(q *big.Rat, mode int) (*big.Int, bool, bool) {
	num := q.Num()
	den := q.Denom()
	quo := new(big.Int).Quo(num, den) // truncates toward zero
	rem := new(big.Int).Rem(num, den)
	inexact := rem.Sign() != 0
	fr := false
	if inexact {
		neg := q.Sign() < 0
		inc := false // increment the magnitude
		switch mode {
		case 0:
			absRem := new(big.Int).Abs(rem)
			twice := new(big.Int).Lsh(absRem, 1)
			switch twice.Cmp(den) {
			case 1:
				inc = true
			case 0:
				inc = new(big.Int).Abs(quo).Bit(0) != 0
			}
		case 1: // toward zero
		case 2: // toward +Inf: increment the value if positive remainder
			if !neg {
				quo.Add(quo, big.NewInt(1))
				fr = true
			}
		case 3: // toward -Inf
			if neg {
				quo.Sub(quo, big.NewInt(1))
				fr = true
			}
		}
		if inc {
			if neg {
				quo.Sub(quo, big.NewInt(1))
			} else {
				quo.Add(quo, big.NewInt(1))
			}
			fr = true
		}
	}
	return quo, inexact, fr
}

// execMTFSB implements mtfsb0 and mtfsb1. The RN bits (30
// and 31 in PowerPC numbering) are never altered, per the
// manual. No other restriction on mtfsb1 could be verified
// in the Gekko manual, so all other bits are written
// literally; the write is a software move and does not
// recompute the summary bits.
func (c *CPU) execMTFSB(w uint32, set bool) {
	crbD := f1(w)
	if crbD == 30 || crbD == 31 {
		return
	}
	mask := uint32(1) << uint(31-crbD)
	if set {
		c.FPSCR |= mask
	} else {
		c.FPSCR &^= mask
	}
	if w&1 != 0 {
		c.recordCR1()
	}
}

// execMTFSF implements mtfsf: the FM field (instruction
// bits 6..13 in PowerPC numbering, with bit 6 the L bit)
// selects which FPSCR nibbles are copied from the low word
// of frB. FM's most significant bit (instruction bit 7)
// corresponds to FPSCR field 0. With L set, the whole
// register is copied regardless of FM.
func (c *CPU) execMTFSF(w uint32) {
	b := f3(w)
	src := uint32(c.FPR[b])
	l := (w >> 25) & 1
	fm := (w >> 17) & 0xFF
	if l != 0 {
		c.FPSCR = src
	} else {
		for i := 0; i < 8; i++ {
			if fm&(0x80>>uint(i)) != 0 {
				shift := uint(28 - 4*i)
				c.FPSCR = c.FPSCR&^(0xF<<shift) | src&(0xF<<shift)
			}
		}
	}
	if w&1 != 0 {
		c.recordCR1()
	}
}
