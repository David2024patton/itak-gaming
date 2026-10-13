package arm7tdmi

// execARM executes one ARM-state instruction (the word in the
// execute stage, located at R15-8) and returns its cycle count.
func (c *CPU) execARM(instr uint32) int {
	addr := c.R[15] - 8
	cond := instr >> 28
	if cond != 0xE && !c.condPassed(cond) {
		return 1
	}

	// Branch and exchange: 0001 0010 1111 1111 1111 0001.
	// Bit 0 of the operand selects the new state and is consumed.
	if instr&0x0FFFFFF0 == 0x012FFF10 {
		v := c.R[instr&0xF]
		if v&1 != 0 {
			c.CPSR |= BitT
		} else {
			c.CPSR &^= BitT
		}
		c.branch(v &^ 1)
		return 3
	}

	switch {
	case (instr>>26)&3 == 0 && instr&(1<<25) == 0 && (instr&0x90) == 0x90 && (instr>>5)&3 == 0:
		// Multiply, multiply-long, swap space (bit 7 and bit 4
		// set, shift field zero).
		switch {
		case (instr>>23)&0x1F == 0x1:
			return c.execMultiplyLong(instr)
		case (instr>>22)&0x3F == 0:
			return c.execMultiply(instr)
		case instr&0x0FB00FF0 == 0x01000090:
			return c.execSwap(instr)
		}
		return c.undefined(addr)

	case (instr>>26)&3 == 0 && instr&(1<<25) == 0 && (instr&0x90) == 0x90:
		// Halfword and signed data transfer (bits 6-5 nonzero).
		return c.execHalfword(instr, addr)

	case (instr>>26)&3 == 0:
		return c.execDataProc(instr, addr)

	case (instr>>26)&3 == 1:
		// Single data transfer. A register offset with bit 4 set
		// is undefined on ARMv4.
		if instr&(1<<25) != 0 && instr&(1<<4) != 0 {
			return c.undefined(addr)
		}
		return c.execSingle(instr, addr)

	case (instr>>25)&7 == 4:
		return c.execBlock(instr, addr)

	case (instr>>25)&7 == 5:
		// Branch, branch with link.
		off := int32(instr<<8) >> 8
		target := uint32(int32(c.R[15]) + off<<2)
		if instr&(1<<24) != 0 {
			c.R[14] = addr + 4
		}
		c.branch(target)
		return 3

	case (instr>>24)&0xF == 0xF:
		// Software interrupt: the system hook (BIOS HLE) gets
		// first refusal with the raw 24-bit comment field;
		// unhandled calls take the real exception.
		if c.SWIHook != nil {
			if n, ok := c.SWIHook(c, instr&0xFFFFFF); ok {
				return n
			}
		}
		return c.exception(0x08, ModeSVC, addr+4)
	}

	// Coprocessor space and everything left over has no
	// coprocessor to answer on the ARM7TDMI: undefined trap.
	return c.undefined(addr)
}

// writeReg writes an instruction result, refilling the pipeline
// when the destination is the PC.
func (c *CPU) writeReg(rd uint32, v uint32) {
	if rd == 15 {
		c.branch(v)
		return
	}
	c.R[rd] = v
}

func mulCycles(rs uint32) int {
	switch {
	case rs&0xFFFFFF00 == 0 || rs&0xFFFFFF00 == 0xFFFFFF00:
		return 1
	case rs&0xFFFF0000 == 0 || rs&0xFFFF0000 == 0xFFFF0000:
		return 2
	case rs&0xFF000000 == 0 || rs&0xFF000000 == 0xFF000000:
		return 3
	}
	return 4
}

// execMultiply runs MUL and MLA.
func (c *CPU) execMultiply(instr uint32) int {
	acc := instr&(1<<21) != 0
	set := instr&(1<<20) != 0
	rd := (instr >> 16) & 0xF
	rn := (instr >> 12) & 0xF
	rs := (instr >> 8) & 0xF
	rm := instr & 0xF
	lhs, rhs := c.memReg(rm), c.memReg(rs)
	var accum uint32
	res := lhs * rhs
	if acc {
		accum = c.memReg(rn)
		res += accum
	}
	if set {
		c.setNZ(res)
		full := tickMultiply(true, rhs)
		c.setCarry(mulCarry(full, false, false, lhs, rhs, accum, 0))
	}
	c.writeReg(rd, res)
	n := 1 + mulCycles(rhs)
	if acc {
		n++
	}
	return n
}

// execMultiplyLong runs UMULL, UMLAL, SMULL and SMLAL.
func (c *CPU) execMultiplyLong(instr uint32) int {
	signed := instr&(1<<22) != 0
	acc := instr&(1<<21) != 0
	set := instr&(1<<20) != 0
	rdHi := (instr >> 16) & 0xF
	rdLo := (instr >> 12) & 0xF
	rs := (instr >> 8) & 0xF
	rm := instr & 0xF
	lhs, rhs := c.memReg(rm), c.memReg(rs)
	var res uint64
	if signed {
		res = uint64(int64(int32(lhs)) * int64(int32(rhs)))
	} else {
		res = uint64(lhs) * uint64(rhs)
	}
	var accumLo, accumHi uint32
	if acc {
		accumLo = c.memReg(rdLo)
		accumHi = c.memReg(rdHi)
		res += uint64(accumHi)<<32 | uint64(accumLo)
	}
	if set {
		c.CPSR &^= FlagN | FlagZ
		if res>>63 != 0 {
			c.CPSR |= FlagN
		}
		if res == 0 {
			c.CPSR |= FlagZ
		}
		full := tickMultiply(signed, rhs)
		c.setCarry(mulCarry(full, true, signed, lhs, rhs, accumLo, accumHi))
	}
	c.R[rdLo] = uint32(res)
	c.R[rdHi] = uint32(res >> 32)
	if rdHi == 15 {
		c.branch(uint32(res >> 32))
	} else if rdLo == 15 {
		c.branch(uint32(res))
	}
	n := 2 + mulCycles(rhs)
	if acc {
		n++
	}
	return n
}

// execSwap runs SWP and SWPB.
func (c *CPU) execSwap(instr uint32) int {
	byteOp := instr&(1<<22) != 0
	rn := (instr >> 16) & 0xF
	rd := (instr >> 12) & 0xF
	rm := instr & 0xF
	// The swap address and the stored operand both read PC as
	// A+12 when they name R15 (the reference implementation
	// reads them after its pipeline increment).
	addr := c.memReg(rn)
	if byteOp {
		tmp := c.Bus.Read(addr, 1)
		c.Bus.Write(addr, 1, c.memReg(rm)&0xFF)
		c.writeReg(rd, tmp)
		return 4
	}
	tmp := c.load(addr, 4, false)
	c.Bus.Write(addr, 4, c.memReg(rm))
	c.writeReg(rd, tmp)
	return 4
}

// decodePSR handles MRS and MSR inside the data-processing space:
// the test-opcode encodings with S clear are the PSR transfers.
func (c *CPU) execPSR(instr uint32, opcode uint32) (int, bool) {
	sBit := instr&(1<<20) != 0
	if sBit {
		return 0, false
	}
	switch opcode {
	case 8, 10: // TST / CMP shape: MRS when Rn=1111 and low bits 0
		if (instr>>16)&0xF != 0xF || instr&0xFFF != 0 {
			return 0, false
		}
		rd := (instr >> 12) & 0xF
		var v uint32
		if opcode == 10 {
			v = c.curSPSR()
		} else {
			v = c.CPSR
		}
		// MRS writes its destination as a plain register, even
		// when Rd is R15: no pipeline refill follows (the
		// reference behavior advances sequentially).
		c.R[rd] = v
		return 1, true
	case 9, 11: // TEQ / CMN shape: MSR
		fieldMask := (instr >> 16) & 0xF
		var v uint32
		if instr&(1<<25) != 0 {
			rot := (instr >> 8) & 0xF
			v = ror32(instr&0xFF, uint(rot*2))
		} else {
			if instr&0xFF0 != 0 {
				return 0, false
			}
			v = c.R[instr&0xF]
		}
		c.msr(opcode == 11, fieldMask, v)
		return 1, true
	}
	return 0, false
}

// execDataProc runs the ARM data-processing group, including the
// PSR transfers that share its encoding space.
func (c *CPU) execDataProc(instr uint32, addr uint32) int {
	opcode := (instr >> 21) & 0xF
	if n, ok := c.execPSR(instr, opcode); ok {
		return n
	}
	sBit := instr&(1<<20) != 0
	rn := (instr >> 16) & 0xF
	rd := (instr >> 12) & 0xF
	isTest := opcode >= 8 && opcode <= 11

	// Operand 2.
	var op2 uint32
	var shiftCarry bool
	regShift := false
	if instr&(1<<25) != 0 {
		rot := (instr >> 8) & 0xF
		op2 = ror32(instr&0xFF, uint(rot*2))
		if rot == 0 {
			shiftCarry = c.carry()
		} else {
			shiftCarry = op2>>31 != 0
		}
	} else if instr&(1<<4) != 0 {
		regShift = true
		rs := (instr >> 8) & 0xF
		// The shift-amount register reads PC as the plain A+8
		// in the reference implementation (no +12 quirk for
		// Rs), while Rn and Rm do take the +4 adjustment.
		rsVal := c.R[rs]
		rmVal := c.R[instr&0xF]
		if instr&0xF == 15 {
			rmVal += 4
		}
		op2, shiftCarry = c.shifter(rmVal, (instr>>5)&3, rsVal&0xFF, true)
	} else {
		// Immediate shift: an Rm of R15 reads the plain A+8
		// already held in R15, which c.R[instr&0xF] provides.
		amt := (instr >> 7) & 0x1F
		op2, shiftCarry = c.shifter(c.R[instr&0xF], (instr>>5)&3, amt, false)
	}

	rnVal := c.R[rn]
	if rn == 15 && regShift {
		rnVal += 4
	}

	setFlags := sBit || isTest
	var res uint32
	logical := false
	switch opcode {
	case 0, 8: // AND, TST
		res = rnVal & op2
		logical = true
	case 1, 9: // EOR, TEQ
		res = rnVal ^ op2
		logical = true
	case 2, 10: // SUB, CMP
		if setFlags {
			res = c.subFlags(rnVal, op2, true)
		} else {
			res = rnVal - op2
		}
	case 3: // RSB
		if setFlags {
			res = c.subFlags(op2, rnVal, true)
		} else {
			res = op2 - rnVal
		}
	case 4, 11: // ADD, CMN
		if setFlags {
			res = c.addFlags(rnVal, op2, false)
		} else {
			res = rnVal + op2
		}
	case 5: // ADC
		if setFlags {
			res = c.addFlags(rnVal, op2, c.carry())
		} else {
			res = rnVal + op2
			if c.carry() {
				res++
			}
		}
	case 6: // SBC
		if setFlags {
			res = c.subFlags(rnVal, op2, c.carry())
		} else {
			res = rnVal - op2
			if !c.carry() {
				res--
			}
		}
	case 7: // RSC
		if setFlags {
			res = c.subFlags(op2, rnVal, c.carry())
		} else {
			res = op2 - rnVal
			if !c.carry() {
				res--
			}
		}
	case 12: // ORR
		res = rnVal | op2
		logical = true
	case 13: // MOV
		res = op2
		logical = true
	case 14: // BIC
		res = rnVal &^ op2
		logical = true
	case 15: // MVN
		res = ^op2
		logical = true
	}
	if logical && setFlags {
		c.setNZC(res, shiftCarry)
	}

	if isTest {
		// A test-opcode encoding whose Rd field is R15 with S
		// set restores the CPSR from the SPSR after the flag
		// computation, in the reference implementation. In
		// modes without an SPSR the restore reads back the
		// CPSR itself, so the computed flags stand.
		if rd == 15 && sBit {
			c.setCPSR(c.curSPSR())
		}
		return 1 + regShiftCycles(regShift, false)
	}
	if rd == 15 {
		if sBit {
			c.setCPSR(c.curSPSR())
		}
		c.branch(res)
		return 1 + regShiftCycles(regShift, true)
	}
	c.R[rd] = res
	return 1 + regShiftCycles(regShift, false)
}

func regShiftCycles(regShift bool, rdPC bool) int {
	n := 0
	if regShift {
		n++
	}
	if rdPC {
		n += 2
	}
	return n
}

// addrMode computes the effective address and writeback value for
// the P/U/W addressing forms shared by the single and halfword
// transfers. It returns the address to access, the value to write
// back (when doWB), and whether writeback applies.
func addrMode(base, offset uint32, pre, up, w bool) (ea, wb uint32, doWB bool) {
	if pre {
		if up {
			ea = base + offset
		} else {
			ea = base - offset
		}
		return ea, ea, w
	}
	if up {
		wb = base + offset
	} else {
		wb = base - offset
	}
	return base, wb, true
}

// execSingle runs LDR and STR (word and byte forms).
func (c *CPU) execSingle(instr uint32, addr uint32) int {
	pre := instr&(1<<24) != 0
	up := instr&(1<<23) != 0
	byteOp := instr&(1<<22) != 0
	w := instr&(1<<21) != 0
	load := instr&(1<<20) != 0
	rn := (instr >> 16) & 0xF
	rd := (instr >> 12) & 0xF

	var offset uint32
	if instr&(1<<25) != 0 {
		amt := (instr >> 7) & 0x1F
		offset, _ = c.shifter(c.R[instr&0xF], (instr>>5)&3, amt, false)
	} else {
		offset = instr & 0xFFF
	}
	ea, wb, doWB := addrMode(c.R[rn], offset, pre, up, w)

	// A writeback into R15 refills the pipeline at the
	// written-back value plus one unit (the reference
	// implementation computes it from the post-increment PC),
	// unless a load into R15 follows and wins.
	writeback := func() {
		if !doWB {
			return
		}
		if rn == 15 {
			c.branch(wb + 4)
		} else {
			c.R[rn] = wb
		}
	}

	if load {
		var v uint32
		if byteOp {
			v = c.load(ea, 1, false)
		} else {
			v = c.load(ea, 4, false)
		}
		if rn != rd {
			writeback()
		}
		if rd == 15 {
			c.branch(v)
			return 5
		}
		c.R[rd] = v
		return 3
	}
	data := c.R[rd]
	if rd == 15 {
		data = c.R[15] + 4 // STR of PC stores A+12
	}
	if byteOp {
		c.store(ea, 1, data)
	} else {
		c.store(ea, 4, data)
	}
	writeback()
	return 2
}

// execHalfword runs the halfword and signed transfer group.
// Note the offset-kind bit (22) is inverted relative to the
// single transfers: set means an immediate offset, clear means a
// register offset.
func (c *CPU) execHalfword(instr uint32, addr uint32) int {
	pre := instr&(1<<24) != 0
	up := instr&(1<<23) != 0
	regOff := instr&(1<<22) == 0
	w := instr&(1<<21) != 0
	load := instr&(1<<20) != 0
	rn := (instr >> 16) & 0xF
	rd := (instr >> 12) & 0xF
	sh := (instr >> 5) & 3

	if !load && sh != 1 {
		return c.undefined(addr)
	}
	var offset uint32
	if regOff {
		offset = c.R[instr&0xF]
	} else {
		offset = ((instr>>8)&0xF)<<4 | (instr & 0xF)
	}
	ea, wb, doWB := addrMode(c.R[rn], offset, pre, up, w)

	if load {
		var v uint32
		switch sh {
		case 1:
			v = c.load(ea, 2, false)
		case 2:
			v = c.load(ea, 1, true)
		case 3:
			v = c.load(ea, 2, true)
		}
		if doWB && rn != rd {
			if rn == 15 {
				c.branch(wb + 4)
			} else {
				c.R[rn] = wb
			}
		}
		if rd == 15 {
			c.branch(v)
			return 5
		}
		c.R[rd] = v
		return 3
	}
	data := c.R[rd]
	if rd == 15 {
		data = c.R[15] + 4
	}
	c.store(ea, 2, data)
	if doWB {
		if rn == 15 {
			c.branch(wb + 4)
		} else {
			c.R[rn] = wb
		}
	}
	return 2
}

// blockStart returns the address of the first (lowest-address)
// transfer of a block of count words from base, for the four
// addressing modes: increment after, increment before, decrement
// after, decrement before.
func blockStart(base uint32, count int, pre, up bool) uint32 {
	switch {
	case up && !pre:
		return base
	case up && pre:
		return base + 4
	case !up && !pre:
		return base - 4*uint32(count-1)
	default:
		return base - 4*uint32(count)
	}
}

// execBlock runs LDM and STM, all four addressing modes. The
// structure follows the reference implementation: with the S bit
// set the CPU switches to the user register view for the whole
// transfer, except for an S-bit LDM that also loads the PC,
// which transfers into the current view and then restores the
// CPSR from the SPSR. The base writeback is applied when the
// first register of the list is reached, before that register
// is itself transferred. An empty register list transfers R15
// only, with a writeback offset of 0x40.
func (c *CPU) execBlock(instr uint32, addr uint32) int {
	pre := instr&(1<<24) != 0
	up := instr&(1<<23) != 0
	sBit := instr&(1<<22) != 0
	w := instr&(1<<21) != 0
	load := instr&(1<<20) != 0
	rn := (instr >> 16) & 0xF
	list := uint32(instr & 0xFFFF)
	base := c.R[rn]

	mode := c.Mode()
	transferPC := list&(1<<15) != 0
	switchMode := sBit && (!load || !transferPC) && mode != ModeUSR && mode != ModeSYS

	empty := list == 0
	if empty {
		list = 1 << 15
		transferPC = true
	}
	count := 0
	first := 16
	for i := uint(0); i < 16; i++ {
		if list&(1<<i) != 0 {
			count++
			if first == 16 {
				first = int(i)
			}
		}
	}
	bytes := uint32(count) * 4
	if empty {
		bytes = 0x40
	}
	var wb uint32
	if up {
		wb = base + bytes
	} else {
		wb = base - bytes
	}
	start := blockStart(base, int(bytes/4), pre, up)

	origCPSR := c.CPSR
	if switchMode {
		c.setCPSR(origCPSR&^0x1F | ModeUSR)
	}

	var pcVal uint32
	pcSet := false
	wbApplied := false
	applyWB := func() {
		wbApplied = true
		if rn != 15 {
			c.R[rn] = wb
		}
	}
	cur := start
	for i := 0; i < 16; i++ {
		if list&(1<<uint(i)) == 0 {
			continue
		}
		if load {
			v := c.Bus.Read(cur, 4)
			if w && i == first {
				// Writeback lands before the destination
				// register is written, so a base register
				// in the list ends with the loaded value.
				applyWB()
			}
			if i == 15 {
				pcVal, pcSet = v, true
			} else {
				c.R[i] = v
			}
		} else {
			var v uint32
			switch {
			case i == 15 && rn == 15 && w && wbApplied:
				v = wb // base is PC and writeback already applied
			case i == 15:
				v = c.R[15] + 4 // STM stores PC as A+12
			default:
				v = c.R[i]
			}
			c.Bus.Write(cur, 4, v)
			if w && i == first {
				// Writeback lands after the first register
				// is stored, so a base register heading the
				// list is stored with its original value.
				applyWB()
			}
		}
		cur += 4
	}

	if switchMode {
		c.setCPSR(origCPSR)
	}
	if load && pcSet {
		if sBit {
			c.setCPSR(c.curSPSR())
		}
		c.branch(pcVal)
		return count + 4
	}
	if w && rn == 15 {
		c.branch(wb)
	}
	if load {
		return count + 2
	}
	return count + 1
}
