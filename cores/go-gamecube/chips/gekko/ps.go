package gekko

import (
	"math"
)

// Paired single support: arithmetic on the two packed
// single lanes of an FPR, the quantized loads and stores
// driven by the GQRs, and dcbz_l.
//
// GQR layout (PowerPC bit numbers, stored in a uint32 with
// bit 0 at value 1<<31):
//
//	bits 2..7   LD_SCALE (value bits 29..24, signed 6-bit)
//	bits 13..15 LD_TYPE  (value bits 18..16)
//	bits 18..23 ST_SCALE (value bits 13..8, signed 6-bit)
//	bits 29..31 ST_TYPE  (value bits 2..0)
//
// Type codes, from the Gekko manual's quantized data type
// table: 0 float (no conversion), 1..3 reserved, 4 unsigned
// 8-bit, 5 unsigned 16-bit, 6 signed 8-bit, 7 signed 16-bit.
// Reserved codes are treated as float pass-through in this
// core (boundedly undefined on hardware).
const (
	gqrTypeFloat = 0
	gqrTypeU8    = 4
	gqrTypeU16   = 5
	gqrTypeS8    = 6
	gqrTypeS16   = 7
)

func gqrLoadScale(g uint32) int  { return sign6(int(g>>24) & 0x3F) }
func gqrLoadType(g uint32) int   { return int(g>>16) & 7 }
func gqrStoreScale(g uint32) int { return sign6(int(g>>8) & 0x3F) }
func gqrStoreType(g uint32) int  { return int(g) & 7 }
func sign6(v int) int {
	if v&0x20 != 0 {
		return v - 64
	}
	return v
}

// gqrTypeSize is the memory size in bytes of one quantized
// element of the given type.
func gqrTypeSize(t int) int {
	switch t {
	case gqrTypeU8, gqrTypeS8:
		return 1
	case gqrTypeU16, gqrTypeS16:
		return 2
	default: // float and reserved
		return 4
	}
}

// psLaneBits returns one packed lane of a register: lane 0
// is PS0, the high word; lane 1 is PS1, the low word.
func (c *CPU) psLaneBits(reg, lane int) uint32 {
	if lane == 0 {
		return uint32(c.FPR[reg] >> 32)
	}
	return uint32(c.FPR[reg])
}

func (c *CPU) setPair(reg int, ps0, ps1 uint32) {
	c.FPR[reg] = uint64(ps0)<<32 | uint64(ps1)
}

// psLane returns one lane as an operand descriptor with the
// NI input flush applied in single terms.
func (c *CPU) psLane(reg, lane int) fpDesc {
	return descSingle(c.flushNISingleValue(c.psLaneBits(reg, lane)))
}

// dequantize converts one integer element read from memory
// to a single, following the manual formula F = I * 2^(-S)
// where S is the signed LD_SCALE: the scale is subtracted
// from the exponent. The product is exact in a single (the
// integer has at most 16 bits), as the scale test in
// ps_test.go pins for scale 5: I=32 gives exactly 1.0.
func dequantize(raw uint32, typ, scale int) uint32 {
	var iv int64
	switch typ {
	case gqrTypeU8:
		iv = int64(raw & 0xFF)
	case gqrTypeS8:
		iv = int64(int8(raw))
	case gqrTypeU16:
		iv = int64(raw & 0xFFFF)
	case gqrTypeS16:
		iv = int64(int16(raw))
	default:
		return raw // float and reserved pass through
	}
	f := float64(iv) * math.Ldexp(1, -scale)
	return math.Float32bits(float32(f))
}

// quantize converts one single lane to the integer bit
// pattern stored to memory, following the manual formula
// I = ROUND(F * 2^S) with S the signed ST_SCALE, where ROUND
// rounds toward zero and then clamps to the type range.
// NaN and +Inf convert to the positive clamp value and -Inf
// to the negative clamp value (zero for unsigned types), as
// the manual's overflow adjustment specifies.
func quantize(bits uint32, typ, scale int) uint32 {
	switch typ {
	case gqrTypeU8, gqrTypeU16, gqrTypeS8, gqrTypeS16:
	default:
		return bits // float and reserved pass through
	}
	var lo, hi int64
	switch typ {
	case gqrTypeU8:
		lo, hi = 0, 255
	case gqrTypeU16:
		lo, hi = 0, 65535
	case gqrTypeS8:
		lo, hi = -128, 127
	case gqrTypeS16:
		lo, hi = -32768, 32767
	}
	f := math.Float32frombits(bits)
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 1) {
		return uint32(hi)
	}
	if math.IsInf(float64(f), -1) {
		return uint32(lo) & 0xFFFF
	}
	scaled := math.Trunc(float64(f) * math.Ldexp(1, scale))
	if scaled >= float64(hi) {
		return uint32(hi)
	}
	if scaled <= float64(lo) {
		return uint32(lo) & 0xFFFF
	}
	return uint32(int64(scaled)) & 0xFFFF
}

// psqLoadOne loads and dequantizes one element at ea.
func (c *CPU) psqLoadOne(ea uint32, g uint32) uint32 {
	typ := gqrLoadType(g)
	sz := gqrTypeSize(typ)
	if sz > 1 && ea%uint32(sz) != 0 {
		c.alignFault(ea)
	}
	raw := c.load(ea, sz)
	return dequantize(raw, typ, gqrLoadScale(g))
}

// psqStoreOne quantizes and stores one lane at ea.
func (c *CPU) psqStoreOne(ea uint32, bits uint32, g uint32) {
	typ := gqrStoreType(g)
	sz := gqrTypeSize(typ)
	if sz > 1 && ea%uint32(sz) != 0 {
		c.alignFault(ea)
	}
	v := quantize(bits, typ, gqrStoreScale(g))
	c.store(ea, sz, v)
}

// execPSQ executes a quantized load or store given its
// effective address and fields. update writes EA back to rA
// after the transfer, except when rA is 0, which names the
// literal base 0 and has no update target.
func (c *CPU) execPSQ(op, reg, ra int, ea uint32, wBit, gi int, update bool) {
	g := c.GQR[gi&7]
	sz := uint32(gqrTypeSize(gqrLoadType(g)))
	if op == 60 || op == 61 {
		sz = uint32(gqrTypeSize(gqrStoreType(g)))
	}
	switch op {
	case 56, 57: // psq_l, psq_lu
		ps0 := c.psqLoadOne(ea, g)
		ps1 := math.Float32bits(1.0)
		if wBit == 0 {
			ps1 = c.psqLoadOne(ea+sz, g)
		}
		c.setPair(reg, ps0, ps1)
	case 60, 61: // psq_st, psq_stu
		c.psqStoreOne(ea, c.psLaneBits(reg, 0), g)
		if wBit == 0 {
			c.psqStoreOne(ea+sz, c.psLaneBits(reg, 1), g)
		}
	}
	if update && ra != 0 {
		c.GPR[ra] = ea
	}
}

// execPSQD handles the D-form quantized instructions:
// primary 56 psq_l, 57 psq_lu, 60 psq_st, 61 psq_stu. The
// displacement is a signed 12-bit field; the bits above it
// carry W (bit 16) and the 3-bit GQR index I (bits 17..19).
func (c *CPU) execPSQD(w uint32) {
	c.fpAvail()
	op := int(w >> 26)
	reg := f1(w)
	ra := f2(w)
	wBit := int(w>>15) & 1
	gi := int(w>>12) & 7
	disp := int(w & 0xFFF)
	if disp&0x800 != 0 {
		disp -= 0x1000
	}
	ea := c.base(ra) + uint32(int32(disp))
	c.execPSQ(op, reg, ra, ea, wBit, gi, op == 57 || op == 61)
}

// execPS is the primary opcode 4 dispatch: paired single
// arithmetic and moves, the indexed quantized forms, the
// paired compares, and dcbz_l.
func (c *CPU) execPS(w uint32) {
	// dcbz_l is a cache instruction, not a floating point
	// one, so it is dispatched before the FP gate.
	if xo(w) == 1014 {
		ea := (c.base(f2(w)) + c.GPR[f3(w)]) &^ 31
		for i := uint32(0); i < 32; i += 4 {
			c.store(ea+i, 4, 0)
		}
		return
	}
	// The indexed quantized forms spend the low 6 bits of
	// the X field on their sub-opcode (6, 7, 38, 39) and
	// the four bits above it on W and I.
	if w&1 == 0 {
		switch (w >> 1) & 0x3F {
		case 6, 7, 38, 39: // psq_lx, psq_stx, psq_lux, psq_stux
			c.fpAvail()
			sub := (w >> 1) & 0x3F
			reg := f1(w)
			ra := f2(w)
			ea := c.base(ra) + c.GPR[f3(w)]
			wBit := int(w>>10) & 1
			gi := int(w>>7) & 7
			op := 56
			if sub == 7 || sub == 39 {
				op = 60
			}
			c.execPSQ(op, reg, ra, ea, wBit, gi, sub == 38 || sub == 39)
			return
		}
	}

	c.fpAvail()
	d := f1(w)
	a := f2(w)
	b := f3(w)
	cc := cField(w)
	rc := w&1 != 0
	f := fmtSingle

	// X forms on the full 10-bit extended opcode.
	switch xo(w) {
	case 0, 32, 64, 96: // ps_cmpu0, ps_cmpo0, ps_cmpu1, ps_cmpo1
		crfD := int(w>>23) & 7
		lane := 0
		if xo(w) >= 64 {
			lane = 1
		}
		ordered := xo(w) == 32 || xo(w) == 96
		c.fpComparePair(c.psLane(a, lane), c.psLane(b, lane), ordered, crfD)
		return
	case 40, 72, 136, 264: // ps_neg, ps_mr, ps_nabs, ps_abs
		p0, p1 := c.psLaneBits(b, 0), c.psLaneBits(b, 1)
		switch xo(w) {
		case 40:
			p0 ^= 1 << 31
			p1 ^= 1 << 31
		case 136:
			p0 |= 1 << 31
			p1 |= 1 << 31
		case 264:
			p0 &^= 1 << 31
			p1 &^= 1 << 31
		}
		c.setPair(d, p0, p1)
		if rc {
			c.recordCR1()
		}
		return
	case 528, 560, 592, 624: // ps_merge00, 01, 10, 11
		a0, a1 := c.psLaneBits(a, 0), c.psLaneBits(a, 1)
		b0, b1 := c.psLaneBits(b, 0), c.psLaneBits(b, 1)
		var p0, p1 uint32
		switch xo(w) {
		case 528:
			p0, p1 = a0, b0
		case 560:
			p0, p1 = a0, b1
		case 592:
			p0, p1 = a1, b0
		case 624:
			p0, p1 = a1, b1
		}
		c.setPair(d, p0, p1)
		if rc {
			c.recordCR1()
		}
		return
	}

	// A forms on the 5-bit extended opcode. All source lanes
	// are read before the destination is written, so a
	// destination that aliases a source behaves correctly.
	a0, a1 := c.psLane(a, 0), c.psLane(a, 1)
	b0, b1 := c.psLane(b, 0), c.psLane(b, 1)
	c0, c1 := c.psLane(cc, 0), c.psLane(cc, 1)
	rawA0, rawA1 := c.psLaneBits(a, 0), c.psLaneBits(a, 1)
	rawB0, rawB1 := c.psLaneBits(b, 0), c.psLaneBits(b, 1)
	rawC0, rawC1 := c.psLaneBits(cc, 0), c.psLaneBits(cc, 1)
	_ = rawA0
	_ = rawA1

	pair := func(r0 uint64, st0 fpStatus, r1 uint64, st1 fpStatus) {
		c.setPair(d, uint32(r0), uint32(r1))
		st0.merge(st1)
		c.applyFPStatus(st0, rc)
	}
	binPair := func(op func(x, y fpDesc) (uint64, fpStatus)) {
		r0, s0 := op(a0, b0)
		r1, s1 := op(a1, b1)
		pair(r0, s0, r1, s1)
	}

	switch aFormXO(w) {
	case 21: // ps_add
		binPair(func(x, y fpDesc) (uint64, fpStatus) { return c.addDesc(x, y, f) })
	case 20: // ps_sub
		binPair(func(x, y fpDesc) (uint64, fpStatus) { return c.addDesc(x, y.negated(), f) })
	case 18: // ps_div
		binPair(func(x, y fpDesc) (uint64, fpStatus) { return c.divOp(x, y, f) })
	case 25: // ps_mul
		r0, s0 := c.mulOp(a0, c0, f)
		r1, s1 := c.mulOp(a1, c1, f)
		pair(r0, s0, r1, s1)
	case 28, 29, 30, 31: // ps_msub, ps_madd, ps_nmsub, ps_nmadd
		xo := aFormXO(w)
		negProd := xo == 30 || xo == 31
		negAdd := xo == 28 || xo == 31
		r0, s0 := c.fmaOp(a0, c0, b0, negProd, negAdd, f)
		r1, s1 := c.fmaOp(a1, c1, b1, negProd, negAdd, f)
		pair(r0, s0, r1, s1)
	case 12, 13: // ps_muls0, ps_muls1
		cs := c0
		if aFormXO(w) == 13 {
			cs = c1
		}
		r0, s0 := c.mulOp(a0, cs, f)
		r1, s1 := c.mulOp(a1, cs, f)
		pair(r0, s0, r1, s1)
	case 14, 15: // ps_madds0, ps_madds1
		cs := c0
		if aFormXO(w) == 15 {
			cs = c1
		}
		r0, s0 := c.fmaOp(a0, cs, b0, false, false, f)
		r1, s1 := c.fmaOp(a1, cs, b1, false, false, f)
		pair(r0, s0, r1, s1)
	case 10, 11: // ps_sum0, ps_sum1
		// Manual operation: the sum is frA(ps0) + frB(ps1)
		// in both forms. ps_sum0 places it in ps0 and copies
		// frC(ps1) into ps1; ps_sum1 copies frC(ps0) into
		// ps0 and places the sum in ps1. FPRF follows the
		// lane that received the sum.
		r, s := c.addDesc(a0, b1, f)
		if aFormXO(w) == 10 {
			c.setPair(d, uint32(r), rawC1)
		} else {
			c.setPair(d, rawC0, uint32(r))
		}
		c.applyFPStatus(s, rc)
	case 23: // ps_sel: frA lane >= 0 selects frC lane, else frB lane
		sel := func(ad fpDesc, rb, rcBits uint32) uint32 {
			if !ad.kind.isNaN() && (!ad.neg || ad.kind.isZero()) {
				return rcBits
			}
			return rb
		}
		c.setPair(d, sel(a0, rawB0, rawC0), sel(a1, rawB1, rawC1))
		if rc {
			c.recordCR1()
		}
	case 24: // ps_res: exact reciprocal per lane
		r0, s0 := c.recipOp(b0, f)
		r1, s1 := c.recipOp(b1, f)
		pair(r0, s0, r1, s1)
	case 26: // ps_rsqrte: exact 1/sqrt per lane (see fp.go)
		r0, s0 := c.recipSqrtOp(b0, f)
		r1, s1 := c.recipSqrtOp(b1, f)
		pair(r0, s0, r1, s1)
	default:
		c.illegalAt()
	}
}

// fpComparePair is the paired compare core: same FPCC and
// CR rules as the scalar compares in fp_exec.go.
func (c *CPU) fpComparePair(da, db fpDesc, ordered bool, crfD int) {
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
		fa, fb := descValue(da), descValue(db)
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
