package gekko

import "math/bits"

// Instruction field extraction. f1, f2, f3 are the three
// register fields; their meaning depends on the instruction
// class (rD/rA/rB for arithmetic, rS/rA for shifts and
// stores, rD/rA for loads).
func f1(w uint32) int { return int(w>>21) & 31 }
func f2(w uint32) int { return int(w>>16) & 31 }
func f3(w uint32) int { return int(w>>11) & 31 }
func xo(w uint32) int { return int(w>>1) & 0x3FF }
func simm(w uint32) int32 {
	return int32(int16(w))
}
func sprField(w uint32) int {
	f := int(w>>11) & 0x3FF
	return (f&0x1F)<<5 | f>>5
}

func (c *CPU) crField(f int) uint32 {
	return (c.CR >> uint(28-4*f)) & 0xF
}

func (c *CPU) setCRField(f int, nib uint32) {
	s := uint(28 - 4*f)
	c.CR = c.CR&^(0xF<<s) | (nib&0xF)<<s
}

func (c *CPU) crBit(bi int) uint32 {
	return (c.CR >> uint(31-bi)) & 1
}

// compareNibble returns the CR field value for a comparison
// outcome, with the SO bit copied from XER as the
// architecture specifies for record and compare forms.
func (c *CPU) compareNibble(lt, gt, eq bool) uint32 {
	var n uint32
	if lt {
		n |= 8
	}
	if gt {
		n |= 4
	}
	if eq {
		n |= 2
	}
	if c.XER&(1<<31) != 0 {
		n |= 1
	}
	return n
}

// recordCR0 sets CR field 0 from a signed result value and
// the current XER SO.
func (c *CPU) recordCR0(res uint32) {
	c.setCRField(0, c.compareNibble(int32(res) < 0, int32(res) > 0, res == 0))
}

// setOverflow applies the OE-form XER rule: OV takes the
// instruction's overflow, and SO latches to 1 if OV is set
// (SO is only cleared by software).
func (c *CPU) setOverflow(ov bool) {
	if ov {
		c.XER |= (1 << 31) | (1 << 30)
	} else {
		c.XER &^= 1 << 30
	}
}

func (c *CPU) setCarry(ca bool) {
	if ca {
		c.XER |= 1 << 29
	} else {
		c.XER &^= 1 << 29
	}
}

func (c *CPU) carry() uint32 {
	if c.XER&(1<<29) != 0 {
		return 1
	}
	return 0
}

// addC adds a + b + cin returning the result, the unsigned
// carry out, and the signed overflow.
func addC(a, b, cin uint32) (res, cout uint32, ov bool) {
	us := uint64(a) + uint64(b) + uint64(cin)
	ss := int64(int32(a)) + int64(int32(b)) + int64(cin)
	return uint32(us), uint32(us >> 32), ss > 0x7FFFFFFF || ss < -0x80000000
}

func (c *CPU) illegalAt() {
	panic(&faultInfo{vec: VecProgram, srr0: c.curIA, cause: SRR1ProgIllegal})
}

func (c *CPU) privAt() {
	panic(&faultInfo{vec: VecProgram, srr0: c.curIA, cause: SRR1ProgPrivileged})
}

func (c *CPU) fpUnavail() {
	panic(&faultInfo{vec: VecFPUnavail, srr0: c.curIA})
}

// rotMask builds an rlw mask: ones from MB through ME,
// wrapping when MB > ME.
func rotMask(mb, me uint32) uint32 {
	if mb <= me {
		return (0xFFFFFFFF >> mb) & (0xFFFFFFFF << (31 - me))
	}
	return (0xFFFFFFFF >> mb) | (0xFFFFFFFF << (31 - me))
}

func (c *CPU) exec(w uint32, ia uint32) {
	switch w >> 26 {
	case 3: // twi
		to := uint32(f1(w))
		a := int32(c.GPR[f2(w)])
		b := simm(w)
		if trapFires(to, uint32(a), uint32(b)) {
			panic(&faultInfo{vec: VecProgram, srr0: ia, cause: SRR1ProgTrap})
		}
	case 4: // paired singles and dcbz_l (GCN-2, ps.go)
		c.execPS(w)
	case 48, 49, 50, 51, 52, 53, 54, 55: // FP loads and stores
		c.execFPLoadStoreD(w)
	case 56, 57, 60, 61: // paired single quantized loads and stores
		c.execPSQD(w)
	case 59: // floating point arithmetic, single (GCN-2)
		c.execFP59(w)
	case 63: // floating point arithmetic and system (GCN-2)
		c.execFP63(w)
	case 7: // mulli
		c.GPR[f1(w)] = uint32(int64(int32(c.GPR[f2(w)])) * int64(simm(w)))
	case 8: // subfic
		res, cout, _ := addC(^c.GPR[f2(w)], uint32(simm(w)), 1)
		c.GPR[f1(w)] = res
		c.setCarry(cout != 0)
	case 10: // cmpli
		bf := int(w>>23) & 7
		a := c.GPR[f2(w)]
		b := uint32(w & 0xFFFF)
		c.setCRField(bf, c.compareNibble(a < b, a > b, a == b))
	case 11: // cmpi
		bf := int(w>>23) & 7
		a := int32(c.GPR[f2(w)])
		b := simm(w)
		c.setCRField(bf, c.compareNibble(a < b, a > b, a == b))
	case 12, 13: // addic, addic.
		res, cout, _ := addC(c.GPR[f2(w)], uint32(simm(w)), 0)
		c.GPR[f1(w)] = res
		c.setCarry(cout != 0)
		if w>>26 == 13 {
			c.recordCR0(res)
		}
	case 14, 15: // addi, addis
		var base uint32
		if f2(w) != 0 {
			base = c.GPR[f2(w)]
		}
		imm := simm(w)
		if w>>26 == 15 {
			imm <<= 16
		}
		c.GPR[f1(w)] = base + uint32(imm)
	case 16: // bc
		c.branchCond(w, ia)
	case 17: // sc
		c.enterException(VecSyscall, ia+4, 0)
	case 18: // b
		li := int32(w & 0x03FFFFFC)
		if li&0x02000000 != 0 {
			li -= 0x04000000
		}
		var target uint32
		if w&2 != 0 {
			target = uint32(li)
		} else {
			target = ia + uint32(li)
		}
		if w&1 != 0 {
			c.LR = ia + 4
		}
		c.PC = target
	case 19:
		c.exec19(w, ia)
	case 20: // rlwimi
		s := c.GPR[f1(w)]
		sh := uint(f3(w))
		m := rotMask(uint32(w>>6)&31, uint32(w>>1)&31)
		r := bits.RotateLeft32(s, int(sh)) & m
		d := f2(w)
		c.GPR[d] = r | (c.GPR[d] &^ m)
		if w&1 != 0 {
			c.recordCR0(c.GPR[d])
		}
	case 21: // rlwinm
		s := c.GPR[f1(w)]
		sh := uint(f3(w))
		m := rotMask(uint32(w>>6)&31, uint32(w>>1)&31)
		res := bits.RotateLeft32(s, int(sh)) & m
		c.GPR[f2(w)] = res
		if w&1 != 0 {
			c.recordCR0(res)
		}
	case 23: // rlwnm
		s := c.GPR[f1(w)]
		sh := uint(c.GPR[f3(w)] & 31)
		m := rotMask(uint32(w>>6)&31, uint32(w>>1)&31)
		res := bits.RotateLeft32(s, int(sh)) & m
		c.GPR[f2(w)] = res
		if w&1 != 0 {
			c.recordCR0(res)
		}
	case 24: // ori
		c.GPR[f2(w)] = c.GPR[f1(w)] | (w & 0xFFFF)
	case 25: // oris
		c.GPR[f2(w)] = c.GPR[f1(w)] | (w&0xFFFF)<<16
	case 26: // xori
		c.GPR[f2(w)] = c.GPR[f1(w)] ^ (w & 0xFFFF)
	case 27: // xoris
		c.GPR[f2(w)] = c.GPR[f1(w)] ^ (w&0xFFFF)<<16
	case 28: // andi.
		res := c.GPR[f1(w)] & (w & 0xFFFF)
		c.GPR[f2(w)] = res
		c.recordCR0(res)
	case 29: // andis.
		res := c.GPR[f1(w)] & (w & 0xFFFF) << 16
		c.GPR[f2(w)] = res
		c.recordCR0(res)
	case 31:
		c.exec31(w, ia)
	case 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47:
		c.execLoadStoreD(w)
	default:
		c.illegalAt()
	}
}

// trapFires evaluates a tw/twi TO field against two operands
// (compared both signed and unsigned per the TO bits).
func trapFires(to, a, b uint32) bool {
	sa, sb := int32(a), int32(b)
	return to&16 != 0 && sa < sb ||
		to&8 != 0 && sa > sb ||
		to&4 != 0 && sa == sb ||
		to&2 != 0 && a < b ||
		to&1 != 0 && a > b
}

// branchCondition evaluates BO/BI semantics, decrementing
// CTR when BO says to test it.
func (c *CPU) branchCondition(bo, bi uint32) bool {
	ctrOK := true
	if bo&0x04 == 0 {
		c.CTR--
		ctrOK = (c.CTR != 0) == (bo&0x02 == 0)
	}
	cond := true
	if bo&0x10 == 0 {
		cond = (c.crBit(int(bi)) == 1) == (bo&0x08 != 0)
	}
	return ctrOK && cond
}

func (c *CPU) branchCond(w uint32, ia uint32) {
	bo := uint32(f1(w))
	bi := uint32(f2(w))
	bd := int32(int16(w & 0xFFFC))
	taken := c.branchCondition(bo, bi)
	if w&1 != 0 {
		c.LR = ia + 4
	}
	if taken {
		if w&2 != 0 {
			c.PC = uint32(bd)
		} else {
			c.PC = ia + uint32(bd)
		}
	}
}

// exec19 handles primary opcode 19: CR logical operations,
// branch to LR and CTR, rfi, isync, mcrf.
func (c *CPU) exec19(w uint32, ia uint32) {
	switch xo(w) {
	case 0: // mcrf
		crd := int(w>>23) & 7
		crs := int(w>>18) & 7
		c.setCRField(crd, c.crField(crs))
	case 16: // bclr
		bo := uint32(f1(w))
		bi := uint32(f2(w))
		target := c.LR &^ 3
		if w&1 != 0 {
			c.LR = ia + 4
		}
		if c.branchCondition(bo, bi) {
			c.PC = target
		}
	case 50: // rfi
		if c.MSR&MSRPR != 0 {
			c.privAt()
		}
		const rfiMask = MSREE | MSRPR | MSRFP | MSRME | MSRFE0 |
			MSRSE | MSRBE | MSRFE1 | MSRIR | MSRDR | MSRPM | MSRRI | MSRLE
		c.MSR = (c.MSR &^ rfiMask) | (c.SRR1 & rfiMask)
		c.PC = c.SRR0
	case 150: // isync
		// Pipeline context sync: a no-op in this model.
	case 528: // bcctr
		bo := uint32(f1(w))
		bi := uint32(f2(w))
		target := c.CTR &^ 3
		if w&1 != 0 {
			c.LR = ia + 4
		}
		// bcctr never decrements or tests CTR; only the CR
		// condition applies.
		cond := true
		if bo&0x10 == 0 {
			cond = (c.crBit(int(bi)) == 1) == (bo&0x08 != 0)
		}
		if cond {
			c.PC = target
		}
	case 129, 193, 225, 257, 289, 33, 417, 449: // CR logical
		bt := int(f1(w))
		ba := (c.crBit(f2(w)) == 1)
		bb := (c.crBit(f3(w)) == 1)
		var r bool
		switch xo(w) {
		case 129: // crand
			r = ba && bb
		case 257: // crandc
			r = ba && !bb
		case 33: // crnor
			r = !(ba || bb)
		case 225: // crnand
			r = !(ba && bb)
		case 449: // cror
			r = ba || bb
		case 417: // crorc
			r = ba || !bb
		case 193: // crxor
			r = ba != bb
		case 289: // creqv
			r = ba == bb
		}
		if r {
			c.CR |= 1 << uint(31-bt)
		} else {
			c.CR &^= 1 << uint(31-bt)
		}
	default:
		c.illegalAt()
	}
}

// base returns the rA base value with the architected rule
// that rA = 0 means the literal value 0 for effective
// address and addi-class computations.
func (c *CPU) base(ra int) uint32 {
	if ra == 0 {
		return 0
	}
	return c.GPR[ra]
}

// execLoadStoreD handles the D-form loads and stores
// (primary opcodes 32-47).
func (c *CPU) execLoadStoreD(w uint32) {
	op := w >> 26
	d := f1(w)
	a := f2(w)
	ea := c.base(a) + uint32(simm(w))

	switch op {
	case 32, 33: // lwz, lwzu
		v := c.load(ea, 4)
		if op == 33 {
			c.GPR[a] = ea
		}
		c.GPR[d] = v
	case 34, 35: // lbz, lbzu
		v := c.load(ea, 1)
		if op == 35 {
			c.GPR[a] = ea
		}
		c.GPR[d] = v
	case 40, 41: // lhz, lhzu
		v := c.load(ea, 2)
		if op == 41 {
			c.GPR[a] = ea
		}
		c.GPR[d] = v
	case 42, 43: // lha, lhau
		v := int32(int16(c.load(ea, 2)))
		if op == 43 {
			c.GPR[a] = ea
		}
		c.GPR[d] = uint32(v)
	case 36, 37: // stw, stwu
		v := c.GPR[d]
		c.store(ea, 4, v)
		if op == 37 {
			c.GPR[a] = ea
		}
	case 38, 39: // stb, stbu
		v := c.GPR[d]
		c.store(ea, 1, v)
		if op == 39 {
			c.GPR[a] = ea
		}
	case 44, 45: // sth, sthu
		v := c.GPR[d]
		c.store(ea, 2, v)
		if op == 45 {
			c.GPR[a] = ea
		}
	case 46: // lmw
		if ea&3 != 0 {
			c.alignFault(ea)
		}
		for r := d; r < 32; r++ {
			c.GPR[r] = c.load(ea, 4)
			ea += 4
		}
	case 47: // stmw
		if ea&3 != 0 {
			c.alignFault(ea)
		}
		for r := d; r < 32; r++ {
			c.store(ea, 4, c.GPR[r])
			ea += 4
		}
	}
}

// exec31 handles primary opcode 31: the X and XO form
// integer instructions, load and store indexed, string and
// multiple helpers, synchronization, cache management, and
// the system register moves.
func (c *CPU) exec31(w uint32, ia uint32) {
	x := xo(w)
	d := f1(w) // rD or rS
	a := f2(w) // rA
	b := f3(w) // rB
	rc := w&1 != 0
	oe := w&0x400 != 0

	// The extended opcode field is ten bits, but the
	// arithmetic forms spend the top bit on OE: an encoding
	// of base|512 is the same instruction with OE set. Fold
	// those back to the base opcode for dispatch. No other
	// instruction's opcode collides with the folded values.
	key := x
	if oe {
		switch x & 0x1FF {
		case 266, 10, 138, 202, 234, 40, 8, 136, 200, 232, 104, 235, 491, 459:
			key = x & 0x1FF
		}
	}

	switch key {
	// Compare and trap.
	case 0, 32: // cmp, cmpl
		bf := int(w>>23) & 7
		if x == 0 {
			av, bv := int32(c.GPR[a]), int32(c.GPR[b])
			c.setCRField(bf, c.compareNibble(av < bv, av > bv, av == bv))
		} else {
			av, bv := c.GPR[a], c.GPR[b]
			c.setCRField(bf, c.compareNibble(av < bv, av > bv, av == bv))
		}
	case 4: // tw
		if trapFires(uint32(d), c.GPR[a], c.GPR[b]) {
			panic(&faultInfo{vec: VecProgram, srr0: ia, cause: SRR1ProgTrap})
		}

	// Add family. subf computes rB - rA; the carry forms
	// follow the architected add-with-complement encoding.
	case 266, 10, 138, 202, 234: // add, addc, adde, addze, addme
		av, bv := c.GPR[a], c.GPR[b]
		var cin uint32
		switch x {
		case 138: // adde
			cin = c.carry()
		case 202: // addze
			bv = 0
			cin = c.carry()
		case 234: // addme
			bv = 0xFFFFFFFF
			cin = c.carry()
		}
		res, cout, ov := addC(av, bv, cin)
		c.GPR[d] = res
		if x != 266 {
			// Only the carry forms write CA; plain add
			// leaves it alone even with OE set.
			c.setCarry(cout != 0)
		}
		if oe {
			c.setOverflow(ov)
		}
		if rc {
			c.recordCR0(res)
		}
	case 40, 8, 136, 200, 232: // subf, subfc, subfe, subfze, subfme
		av, bv := c.GPR[a], c.GPR[b]
		var cin uint32 = 1
		switch x {
		case 136: // subfe
			cin = c.carry()
		case 200: // subfze
			bv = 0
			cin = c.carry()
		case 232: // subfme
			bv = 0xFFFFFFFF
			cin = c.carry()
		}
		res, cout, _ := addC(^av, bv, cin)
		c.GPR[d] = res
		c.setCarry(cout != 0)
		if oe {
			// Signed overflow of the subtraction the form
			// encodes: bv - av - (1 - cin), where cin is
			// the carry input captured above (1 for the
			// plain forms, the old CA for the e forms).
			c.setOverflow(subOvExtra(bv, av, cin))
		}
		if rc {
			c.recordCR0(res)
		}
	case 104: // neg
		av := c.GPR[a]
		res := ^av + 1
		c.GPR[d] = res
		if oe {
			c.setOverflow(av == 0x80000000)
		}
		if rc {
			c.recordCR0(res)
		}
	case 235, 75, 11: // mullw, mulhw, mulhwu
		av, bv := int64(int32(c.GPR[a])), int64(int32(c.GPR[b]))
		var res uint32
		switch x {
		case 235:
			p := av * bv
			res = uint32(p)
			if oe {
				c.setOverflow(p != int64(int32(p)))
			}
		case 75:
			res = uint32(av * bv >> 32)
		case 11:
			res = uint32(uint64(c.GPR[a]) * uint64(c.GPR[b]) >> 32)
		}
		c.GPR[d] = res
		if rc {
			c.recordCR0(res)
		}
	case 491, 459: // divw, divwu
		var res uint32
		var ov bool
		if x == 491 {
			av, bv := int32(c.GPR[a]), int32(c.GPR[b])
			if bv == 0 || (av == -0x80000000 && bv == -1) {
				res, ov = 0, true
			} else {
				res = uint32(av / bv)
			}
		} else {
			if c.GPR[b] == 0 {
				res, ov = 0, true
			} else {
				res = c.GPR[a] / c.GPR[b]
			}
		}
		c.GPR[d] = res
		if oe {
			c.setOverflow(ov)
		}
		if rc {
			c.recordCR0(res)
		}

	// Logical.
	case 28, 60, 444, 412, 316, 476, 124, 284: // and, andc, or, orc, xor, nand, nor, eqv
		av, bv := c.GPR[a], c.GPR[b]
		var res uint32
		switch x {
		case 28:
			res = av & bv
		case 60:
			res = av &^ bv
		case 444:
			res = av | bv
		case 412:
			res = av | ^bv
		case 316:
			res = av ^ bv
		case 476:
			res = ^(av & bv)
		case 124:
			res = ^(av | bv)
		case 284:
			res = ^(av ^ bv)
		}
		c.GPR[d] = res
		if rc {
			c.recordCR0(res)
		}

	// Shifts, sign extension, count leading zeros. In the
	// X form the source is the f1 field and the destination
	// is the f2 field.
	case 24, 536: // slw, srw
		n := c.GPR[b] & 0x3F
		var res uint32
		if n < 32 {
			if x == 24 {
				res = c.GPR[d] << n
			} else {
				res = c.GPR[d] >> n
			}
		}
		c.GPR[a] = res
		if rc {
			c.recordCR0(res)
		}
	case 792, 824: // sraw, srawi
		var n uint32
		if x == 792 {
			n = c.GPR[b] & 0x3F
		} else {
			n = uint32(b)
		}
		src := c.GPR[d]
		var res uint32
		var ca bool
		switch {
		case n == 0:
			res = src
		case n < 32:
			res = uint32(int32(src) >> n)
			ca = int32(src) < 0 && src&((1<<n)-1) != 0
		default:
			if int32(src) < 0 {
				res = 0xFFFFFFFF
				ca = true
			}
		}
		c.GPR[a] = res
		c.setCarry(ca)
		if rc {
			c.recordCR0(res)
		}
	case 922: // extsh
		res := uint32(int32(int16(c.GPR[d])))
		c.GPR[a] = res
		if rc {
			c.recordCR0(res)
		}
	case 954: // extsb
		res := uint32(int32(int8(c.GPR[d])))
		c.GPR[a] = res
		if rc {
			c.recordCR0(res)
		}
	case 26: // cntlzw
		res := uint32(bits.LeadingZeros32(c.GPR[d]))
		c.GPR[a] = res
		if rc {
			c.recordCR0(res)
		}

	// System registers.
	case 19: // mfcr
		c.GPR[d] = c.CR
	case 144: // mtcrf
		fxm := (w >> 12) & 0xFF
		src := c.GPR[d]
		for i := 0; i < 8; i++ {
			if fxm&(1<<uint(7-i)) != 0 {
				c.setCRField(i, (src>>uint(28-4*i))&0xF)
			}
		}
	case 512: // mcrxr
		bf := int(w>>23) & 7
		c.setCRField(bf, c.XER>>28)
		c.XER &= 0x0FFFFFFF
	case 83: // mfmsr
		c.GPR[d] = c.MSR
	case 146: // mtmsr
		if c.MSR&MSRPR != 0 {
			c.privAt()
		}
		const defined = MSRPOW | MSRILE | MSREE | MSRPR | MSRFP |
			MSRME | MSRFE0 | MSRSE | MSRBE | MSRFE1 | MSRIP |
			MSRIR | MSRDR | MSRPM | MSRRI | MSRLE
		c.MSR = c.GPR[d] & defined
	case 339, 371: // mfspr, mftb
		spr := sprField(w)
		if c.MSR&MSRPR != 0 && sprPrivileged(spr) {
			c.privAt()
		}
		c.GPR[d] = c.GetSPR(spr)
	case 467: // mtspr
		spr := sprField(w)
		if c.MSR&MSRPR != 0 && sprPrivileged(spr) {
			c.privAt()
		}
		c.SetSPR(spr, c.GPR[d])
	case 595: // mfsr
		c.GPR[d] = c.SR[a&0xF]
	case 210: // mtsr
		if c.MSR&MSRPR != 0 {
			c.privAt()
		}
		c.SR[a&0xF] = c.GPR[d]
	case 659: // mfsrin
		c.GPR[d] = c.SR[c.GPR[b]>>28]
	case 242: // mtsrin
		if c.MSR&MSRPR != 0 {
			c.privAt()
		}
		c.SR[c.GPR[b]>>28] = c.GPR[d]

	// Floating point indexed loads and stores (GCN-2).
	case 535, 567, 599, 631, 663, 695, 727, 759, 983: // lfsx, lfsux, lfdx, lfdux, stfsx, stfsux, stfdx, stfdux, stfiwx
		c.execFPIndexed(w, x)

	// Synchronization and cache management. dcbz has a real
	// effect (it zeroes the block); the rest are ordering
	// or cache hints with no state in this model.
	case 598, 854: // sync, eieio
	case 54, 86, 278, 246, 982: // dcbst, dcbf, dcbt, dcbtst, icbi
	case 470: // dcbi
		if c.MSR&MSRPR != 0 {
			c.privAt()
		}
	case 1014: // dcbz
		ea := (c.base(a) + c.GPR[b]) &^ 31
		for i := uint32(0); i < 32; i += 4 {
			c.store(ea+i, 4, 0)
		}
	case 306, 370: // tlbie, tlbia
		if c.MSR&MSRPR != 0 {
			c.privAt()
		}
		// No TLB in this model: BAT translation is
		// combinational, so invalidation is a no-op.

	default:
		c.execLoadStoreX(w, x, ia)
	}
}

// subOvExtra reports signed overflow for bv - av - borrow,
// where the encoded addition was ^av + bv + cin with
// borrow = 1 - cin.
func subOvExtra(bv, av, cin uint32) bool {
	ss := int64(int32(bv)) - int64(int32(av)) - int64(1-cin)
	return ss > 0x7FFFFFFF || ss < -0x80000000
}

// execLoadStoreX handles the opcode-31 indexed load and
// store forms, the byte-reversed forms, the string forms,
// and the reservation pair.
func (c *CPU) execLoadStoreX(w uint32, x int, ia uint32) {
	d := f1(w)
	a := f2(w)
	b := f3(w)
	ea := c.base(a) + c.GPR[b]

	loadInto := func(v uint32, update bool) {
		if update {
			c.GPR[a] = ea
		}
		c.GPR[d] = v
	}

	switch x {
	case 23, 55: // lwzx, lwzux
		loadInto(c.load(ea, 4), x == 55)
	case 87, 119: // lbzx, lbzux
		loadInto(c.load(ea, 1), x == 119)
	case 279, 311: // lhzx, lhzux
		loadInto(c.load(ea, 2), x == 311)
	case 343, 375: // lhax, lhaux
		loadInto(uint32(int32(int16(c.load(ea, 2)))), x == 375)
	case 151, 183: // stwx, stwux
		v := c.GPR[d]
		c.store(ea, 4, v)
		if x == 183 {
			c.GPR[a] = ea
		}
	case 215, 247: // stbx, stbux
		v := c.GPR[d]
		c.store(ea, 1, v)
		if x == 247 {
			c.GPR[a] = ea
		}
	case 407, 439: // sthx, sthux
		v := c.GPR[d]
		c.store(ea, 2, v)
		if x == 439 {
			c.GPR[a] = ea
		}
	case 534: // lwbrx
		v := c.load(ea, 4)
		c.GPR[d] = swap32(v)
	case 790: // lhbrx
		v := c.load(ea, 2)
		c.GPR[d] = uint32(swap16(uint16(v)))
	case 662: // stwbrx
		c.store(ea, 4, swap32(c.GPR[d]))
	case 918: // sthbrx
		c.store(ea, 2, uint32(swap16(uint16(c.GPR[d]))))
	case 20: // lwarx
		if ea&3 != 0 {
			c.alignFault(ea)
		}
		pa, prot, _, ok := c.translate(ea, false, false)
		if !ok {
			panic(&faultInfo{
				vec: VecDSI, srr0: ia, setDA: true,
				dsisr: dsisrCause(prot, false), dar: ea,
			})
		}
		v := c.bus.Read32(pa)
		if c.MSR&MSRLE != 0 {
			v = swap32(v)
		}
		c.GPR[d] = v
		c.reservAddr = pa
		c.reservValid = true
	case 150: // stwcx.
		if ea&3 != 0 {
			c.alignFault(ea)
		}
		pa, prot, _, ok := c.translate(ea, false, true)
		if !ok {
			panic(&faultInfo{
				vec: VecDSI, srr0: ia, setDA: true,
				dsisr: dsisrCause(prot, true), dar: ea,
			})
		}
		var nib uint32
		if c.XER&(1<<31) != 0 {
			nib = 1
		}
		if c.reservValid && c.reservAddr == pa {
			v := c.GPR[d]
			if c.MSR&MSRLE != 0 {
				v = swap32(v)
			}
			c.bus.Write32(pa, v)
			nib |= 2 // EQ
		}
		c.reservValid = false
		c.setCRField(0, nib)
	case 597: // lswi
		n := int(uint32(b))
		if n == 0 {
			n = 32
		}
		c.loadString(d, c.base(a), n)
	case 533: // lswx
		n := int(c.XER & 0x7F)
		if n > 0 {
			c.loadString(d, ea, n)
		}
	case 725: // stswi
		n := int(uint32(b))
		if n == 0 {
			n = 32
		}
		c.storeString(d, c.base(a), n)
	case 661: // stswx
		n := int(c.XER & 0x7F)
		if n > 0 {
			c.storeString(d, ea, n)
		}
	default:
		c.illegalAt()
	}
}

// loadString loads n bytes starting at ea into consecutive
// registers beginning at rd (wrapping r31 to r0), packing
// bytes big-endian: the first byte is the most significant
// byte of the first register. A final partial register has
// its remaining low bytes zero-filled; the architecture
// leaves those bytes undefined and this core documents the
// choice in the README.
func (c *CPU) loadString(rd int, ea uint32, n int) {
	reg := rd
	for n > 0 {
		chunk := n
		if chunk > 4 {
			chunk = 4
		}
		var v uint32
		for i := 0; i < chunk; i++ {
			v |= c.load(ea, 1) << uint(24-8*i)
			ea++
		}
		c.GPR[reg] = v
		reg = (reg + 1) & 31
		n -= chunk
	}
}

// storeString stores n bytes beginning at ea from
// consecutive registers starting at rs (wrapping r31 to
// r0), most significant byte first.
func (c *CPU) storeString(rs int, ea uint32, n int) {
	reg := rs
	for n > 0 {
		chunk := n
		if chunk > 4 {
			chunk = 4
		}
		v := c.GPR[reg]
		for i := 0; i < chunk; i++ {
			c.store(ea, 1, v>>uint(24-8*i))
			ea++
		}
		reg = (reg + 1) & 31
		n -= chunk
	}
}
