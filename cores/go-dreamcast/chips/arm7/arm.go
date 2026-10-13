package arm7

// ARM state instruction execution. The decoder checks the fixed
// encodings (multiplies, PSR transfers, BX, swap, miscellaneous
// loads and stores) before the generic data processing space,
// following the ARMv4 allocation of the 000 encoding space.

// mulCarry is the carry flag the ARM7TDMI multiplier produces for
// the 32 bit multiplies: it is bit 31 of the multiplier operand
// (Rs) set with bit 30 clear, the deterministic behaviour of the
// hardware multiplier array across the normal operand range. The
// long multiplies produce the same signal from their multiplier
// operand.
// The carry flag a multiply leaves behind is not a product
// flag on this hardware: it is the carry out of the Booth
// multiplier's carry-save adder tree. The model below follows
// the published ARM7TDMI multiplier carry research (the Booth
// carry model worked out by zaydlang and calc84maniac, also
// used by the corpus generator): a multiplier whose high bytes
// are a pure sign extension terminates early and the flag comes
// from a radix-4 Booth carry-save walk of the low word, while a
// full length multiply takes a fixed carry for the short forms
// (multiplier bits 31 and 30 reading binary 10) and a reduced
// high word Booth computation for the long forms.

// mulFullLength reports whether the multiplier runs the full
// four beats with no early termination. Termination happens at
// a byte boundary when the remaining high bytes of the
// multiplier are all zero, or, for signed multiplies, all one.
func mulFullLength(signed bool, multiplier uint32) bool {
	mask := uint32(0xFFFFFF00)
	for {
		multiplier &= mask
		if multiplier == 0 {
			break
		}
		if signed && multiplier == mask {
			break
		}
		mask <<= 8
	}
	return mask == 0
}

// mulCarryShort computes the C flag for MUL and MLA.
func mulCarryShort(multiplicand, multiplier, accum uint32) bool {
	if mulFullLength(true, multiplier) {
		return (multiplier>>30)&3 == 2
	}
	return mulBoothCarryLo(multiplicand, multiplier, accum)
}

// mulCarryLong computes the C flag for the long multiplies.
func mulCarryLong(signed bool, multiplicand, multiplier, accumLo, accumHi uint32) bool {
	if mulFullLength(signed, multiplier) {
		return mulBoothCarryHi(signed, multiplicand, multiplier, accumHi)
	}
	return mulBoothCarryLo(multiplicand, multiplier, accumLo)
}

// mulBoothCarryLo models the carry out of the multiplier's
// carry-save adder for the low result word, stepping the
// multiplier in radix-4 Booth digits exactly as the hardware
// accumulates it. It is only reached for early terminated
// multipliers, whose Booth walk converges within three groups
// of four digits.
func mulBoothCarryLo(multiplicand, multiplier, accum uint32) bool {
	// Setting the low bit of the multiplicand makes Booth
	// negation invert the upper bits; the bit itself cannot
	// reach the carry.
	multiplicand |= 1

	booth := uint32(int32(multiplier<<31) >> 31)
	carry := multiplicand * booth
	sum := carry + accum

	shift := 29
	for {
		// Four Booth digits cover eight multiplier bits.
		for i := 0; i < 4; i++ {
			next := uint32(int32(multiplier<<uint(shift)) >> uint(shift))
			factor := next - booth
			booth = next
			addend := multiplicand * factor
			// Carry-save accumulation: the sum and the
			// new accumulator differ exactly by the
			// carry word.
			merged := accum ^ carry ^ addend
			sum += addend
			carry = sum - merged
			accum = merged
			shift -= 2
		}
		if booth == multiplier {
			break
		}
	}
	return carry>>31 != 0
}

// mulBoothCarryHi models the carry out of the high result word
// of a full length long multiply. Only the last three Booth
// digits can influence it, so both operands are reduced to the
// relevant high bits and the carry is propagated through marker
// bits, following the published model.
func mulBoothCarryHi(signed bool, multiplicand, multiplier, accumHi uint32) bool {
	if signed {
		multiplicand = uint32(int32(multiplicand) >> 6)
		multiplier = uint32(int32(multiplier) >> 26)
	} else {
		multiplicand >>= 6
		multiplier >>= 26
	}
	multiplicand |= 1

	carry := ^accumHi & 0x20000000
	accum := accumHi - 0x08000000

	booth0 := uint32(int32(multiplier<<27) >> 27)
	booth1 := uint32(int32(multiplier<<29) >> 29)
	booth2 := uint32(int32(multiplier<<31) >> 31)
	factor0 := multiplier - booth0
	factor1 := booth0 - booth1
	factor2 := booth1 - booth2

	addend := multiplicand * factor2
	accum -= addend & 0x10000000
	addend = multiplicand * factor1
	accum -= addend & 0x40000000

	sum := accum + (addend & 0x20000000)
	accum -= carry

	addend = multiplicand * factor0
	sum += addend & 0x40000000

	return (sum^accum)>>31 != 0
}

// shiftByImm applies an immediate specified shift (the data
// processing operand 2 form with a 5 bit amount). An amount of
// zero means shift by 32 for LSR and ASR, and RRX for ROR.
func shiftByImm(v, typ, amt uint32, carryIn bool) (uint32, bool) {
	switch typ {
	case 0: // LSL
		if amt == 0 {
			return v, carryIn
		}
		return v << amt, v>>(32-amt)&1 == 1
	case 1: // LSR
		if amt == 0 {
			return 0, v>>31&1 == 1
		}
		return v >> amt, v>>(amt-1)&1 == 1
	case 2: // ASR
		if amt == 0 {
			if v&0x80000000 != 0 {
				return 0xFFFFFFFF, true
			}
			return 0, false
		}
		return uint32(int32(v) >> amt), v>>(amt-1)&1 == 1
	}
	// ROR
	if amt == 0 { // RRX
		res := v >> 1
		if carryIn {
			res |= 0x80000000
		}
		return res, v&1 == 1
	}
	res := ror32(v, amt)
	return res, res&0x80000000 != 0
}

// shiftByReg applies a register specified shift. The amount is
// the low byte of the shift register; zero leaves the value and
// the carry untouched.
func shiftByReg(v, typ, amt uint32, carryIn bool) (uint32, bool) {
	if amt == 0 {
		return v, carryIn
	}
	switch typ {
	case 0: // LSL
		if amt < 32 {
			return v << amt, v>>(32-amt)&1 == 1
		}
		if amt == 32 {
			return 0, v&1 == 1
		}
		return 0, false
	case 1: // LSR
		if amt < 32 {
			return v >> amt, v>>(amt-1)&1 == 1
		}
		if amt == 32 {
			return 0, v>>31&1 == 1
		}
		return 0, false
	case 2: // ASR
		if amt < 32 {
			return uint32(int32(v) >> amt), v>>(amt-1)&1 == 1
		}
		if v&0x80000000 != 0 {
			return 0xFFFFFFFF, true
		}
		return 0, false
	}
	// ROR
	if amt&31 == 0 {
		return v, v>>31&1 == 1
	}
	res := ror32(v, amt&31)
	return res, res&0x80000000 != 0
}

// execARM executes one ARM state instruction and returns its
// cycle count.
func (c *CPU) execARM(op uint32) int {
	if !c.condPassed(op >> 28) {
		return 1
	}
	switch {
	case op>>24&0xF == 0xF: // SWI
		return c.swiException()
	case (op>>25)&7 == 0b110: // coprocessor load and store
		return c.undefinedException()
	case (op>>24)&0xF == 0b1110: // CDP, MRC, MCR
		return c.undefinedException()
	case (op>>25)&7 == 0b101: // branch
		return c.execBranch(op)
	case (op>>25)&7 == 0b100: // block transfer
		return c.execBlockTransfer(op)
	case (op>>26)&3 == 0b01: // single load and store
		if (op>>25)&1 == 1 && (op>>4)&1 == 1 {
			return c.undefinedException()
		}
		return c.execLoadStoreSingle(op)
	case (op>>25)&7 == 0b001: // data processing immediate, MSR immediate
		if op&0x0FB0F000 == 0x0320F000 {
			return c.execMSRImm(op)
		}
		return c.execDataProc(op)
	case (op>>25)&7 == 0b000:
		return c.execOpcodeSpace000(op)
	}
	return c.undefinedException()
}

// execOpcodeSpace000 decodes the crowded 000 space.
func (c *CPU) execOpcodeSpace000(op uint32) int {
	switch {
	case op&0x0FFFFFF0 == 0x012FFF10: // BX
		return c.execBX(op, false)
	case op&0x0FFFFFF0 == 0x012FFF30: // BLX register
		return c.execBX(op, true)
	case op&0x0FBF0FFF == 0x010F0000: // MRS
		return c.execMRS(op)
	case op&0x0FB0FFF0 == 0x0120F000: // MSR register
		return c.execMSRReg(op)
	case op&0x0FC000F0 == 0x00000090: // MUL, MLA
		return c.execMultiply(op)
	case op&0x0F8000F0 == 0x00800090: // long multiplies
		return c.execMultiplyLong(op)
	case op&0x0FB00FF0 == 0x01000090: // SWP, SWPB
		return c.execSwap(op)
	case (op>>7)&1 == 1 && (op>>4)&1 == 1:
		// Miscellaneous loads and stores (halfword and signed).
		if (op>>5)&3 == 0 {
			return c.undefinedException()
		}
		return c.execLoadStoreHalf(op)
	}
	// Data processing with register operand 2.
	return c.execDataProc(op)
}

// execBX implements BX and the register form of BLX.
func (c *CPU) execBX(op uint32, link bool) int {
	v := c.R[op&0xF]
	if link {
		c.R[14] = c.R[15] - 4
	}
	if v&1 != 0 {
		c.CPSR |= cpsrT
	} else {
		c.CPSR &^= cpsrT
	}
	c.writePC(v)
	return 3
}

// execMRS implements MRS: transfer CPSR or SPSR to a register.
func (c *CPU) execMRS(op uint32) int {
	var v uint32
	if (op>>22)&1 == 1 {
		v = c.currentSPSR()
	} else {
		v = c.CPSR
	}
	rd := (op >> 12) & 0xF
	// MRS to the program counter is a plain register write in
	// the reference model: no pipeline refill, and the normal
	// sequential increment still applies on top of the value.
	c.R[rd] = v
	return 1
}

// msrFields expands the MSR field mask (opcode bits 19 to 16)
// into a byte lane mask over the PSR.
func msrFields(op uint32) uint32 {
	var mask uint32
	f := (op >> 16) & 0xF
	if f&1 != 0 {
		mask |= 0x000000FF
	}
	if f&2 != 0 {
		mask |= 0x0000FF00
	}
	if f&4 != 0 {
		mask |= 0x00FF0000
	}
	if f&8 != 0 {
		mask |= 0xFF000000
	}
	return mask
}

// execMSR applies an MSR write of operand under the field mask.
func (c *CPU) execMSR(op, operand uint32) int {
	mask := msrFields(op)
	if (op>>22)&1 == 1 {
		// SPSR of the current mode; user and system have none.
		if i := spsrIndex(c.Mode()); i >= 0 {
			c.SPSR[i] = (c.SPSR[i] &^ mask) | (operand & mask)
		}
		return 1
	}
	if c.Mode() == ModeUSR {
		// In user mode only the flags field of CPSR is writable.
		mask &= 0xFF000000
	}
	nc := (c.CPSR &^ mask) | (operand & mask)
	c.setCPSR(nc)
	return 1
}

func (c *CPU) execMSRReg(op uint32) int {
	return c.execMSR(op, c.reg(op&0xF, false))
}

func (c *CPU) execMSRImm(op uint32) int {
	imm := op & 0xFF
	rot := ((op >> 8) & 0xF) * 2
	return c.execMSR(op, ror32(imm, rot))
}

// execMultiply implements MUL and MLA.
func (c *CPU) execMultiply(op uint32) int {
	rd := (op >> 16) & 0xF
	rn := (op >> 12) & 0xF
	rs := (op >> 8) & 0xF
	rm := op & 0xF
	// Source operands use the plus 12 program counter rule
	// (the corpus products show an R15 operand taken as the
	// instruction address plus 12).
	lhs := c.reg(rm, true)
	b := c.reg(rs, true)
	res := lhs * b
	var accum uint32
	if (op>>21)&1 == 1 {
		accum = c.reg(rn, true)
		res += accum
	}
	if (op>>20)&1 == 1 {
		c.setNZ(res)
		c.setFlag(cpsrC, mulCarryShort(lhs, b, accum))
	}
	if rd == 15 {
		c.flush(res)
		return 3
	}
	c.R[rd] = res
	// Operand size dependent timing in the GBATEK model.
	return 2
}

// execMultiplyLong implements UMULL, UMLAL, SMULL and SMLAL.
func (c *CPU) execMultiplyLong(op uint32) int {
	rdHi := (op >> 16) & 0xF
	rdLo := (op >> 12) & 0xF
	rs := (op >> 8) & 0xF
	rm := op & 0xF
	a, b := c.reg(rm, true), c.reg(rs, true)
	signed := (op>>22)&1 == 1
	var prod uint64
	if signed {
		prod = uint64(int64(int32(a)) * int64(int32(b)))
	} else {
		prod = uint64(a) * uint64(b)
	}
	var accumLo, accumHi uint32
	if (op>>21)&1 == 1 {
		accumLo = c.reg(rdLo, true)
		accumHi = c.reg(rdHi, true)
		prod += uint64(accumHi)<<32 | uint64(accumLo)
	}
	if (op>>20)&1 == 1 {
		c.setNZ64(prod)
		c.setFlag(cpsrC, mulCarryLong(signed, a, b, accumLo, accumHi))
	}
	c.R[rdLo] = uint32(prod)
	c.R[rdHi] = uint32(prod >> 32)
	if rdHi == 15 {
		c.flush(uint32(prod >> 32))
	} else if rdLo == 15 {
		c.flush(uint32(prod))
	}
	return 3
}

// execDataProc implements the 16 ALU operations with immediate,
// immediate shifted, and register shifted operand 2 forms.
func (c *CPU) execDataProc(op uint32) int {
	opcode := (op >> 21) & 0xF
	sBit := (op>>20)&1 == 1
	rn := (op >> 16) & 0xF
	rd := (op >> 12) & 0xF

	cycles := 1
	var op2 uint32
	var shCarry bool
	regShift := false
	if (op>>25)&1 == 1 {
		imm := op & 0xFF
		rot := ((op >> 8) & 0xF) * 2
		op2 = ror32(imm, rot)
		shCarry = c.flagC()
		if rot != 0 {
			shCarry = op2>>31&1 == 1
		}
	} else if (op>>4)&1 == 1 {
		regShift = true
		cycles++
		// The shift amount register is read without the plus
		// 12 program counter rule (the corpus shows an R15
		// amount taken from the plain register value).
		amt := c.reg((op>>8)&0xF, false) & 0xFF
		op2, shCarry = shiftByReg(c.reg(op&0xF, true), (op>>5)&3, amt, c.flagC())
	} else {
		amt := (op >> 7) & 0x1F
		op2, shCarry = shiftByImm(c.reg(op&0xF, false), (op>>5)&3, amt, c.flagC())
	}
	a := c.reg(rn, regShift)
	carryIn := uint32(0)
	if c.flagC() {
		carryIn = 1
	}

	isTest := opcode >= 0x8 && opcode <= 0xB
	// Arithmetic helpers set flags as a side effect; when the
	// instruction does not call for flag updates (S clear and not
	// a test operation) the saved flags are restored afterwards.
	setFlags := sBit || isTest
	savedFlags := c.CPSR & flagMask
	var res uint32
	arith := false
	switch opcode {
	case 0x0, 0x8: // AND, TST
		res = a & op2
	case 0x1, 0x9: // EOR, TEQ
		res = a ^ op2
	case 0x2, 0xA: // SUB, CMP
		res = c.subFlags(a, op2, 1)
		arith = true
	case 0x3: // RSB
		res = c.subFlags(op2, a, 1)
		arith = true
	case 0x4, 0xB: // ADD, CMN
		res = c.addFlags(a, op2, 0)
		arith = true
	case 0x5: // ADC
		res = c.addFlags(a, op2, carryIn)
		arith = true
	case 0x6: // SBC
		res = c.subFlags(a, op2, carryIn)
		arith = true
	case 0x7: // RSC
		res = c.subFlags(op2, a, carryIn)
		arith = true
	case 0xC: // ORR
		res = a | op2
	case 0xD: // MOV
		res = op2
	case 0xE: // BIC
		res = a &^ op2
	case 0xF: // MVN
		res = ^op2
	}
	if arith && !setFlags {
		c.CPSR = (c.CPSR &^ flagMask) | savedFlags
	}
	if !arith && setFlags {
		// Logical operations take the shifter carry. The
		// exception return form below overwrites the flags
		// with the SPSR where it applies.
		c.setLogicalFlags(res, shCarry)
	}
	if isTest {
		if rd == 15 && sBit {
			// A test operation with Rd = 15 and S set
			// restores the CPSR from the SPSR in the
			// reference model, but does not write the
			// program counter: execution continues with
			// the next sequential instruction.
			c.restoreCPSRFromSPSR()
		}
		return cycles
	}
	if rd == 15 && sBit {
		// The exception return form: writing the program
		// counter with the S bit set branches to the result,
		// and in modes that own an SPSR the CPSR is restored
		// from it. In user and system modes there is no SPSR
		// to restore, so the flag updates above stand (the
		// corpus USR mode cases show the branch with normal
		// flag results).
		if c.hasSPSR() {
			c.restoreCPSRFromSPSR()
		}
		c.flush(res)
		return 3
	}
	if rd == 15 {
		// A data processing program counter write is not
		// masked: the result value, low bits included, becomes
		// the new execution address.
		c.flush(res)
		return 3
	}
	c.R[rd] = res
	return cycles
}

// execBranch implements B and BL.
func (c *CPU) execBranch(op uint32) int {
	if (op>>24)&1 == 1 {
		c.R[14] = c.R[15] - 4
	}
	off := int32(op&0xFFFFFF) << 8 >> 8 // sign extend 24 bits
	target := uint32(int32(c.R[15]) + off*4)
	c.flush(target)
	return 3
}

// addressMode computes the transfer address and the writeback
// value for the single and halfword load and store address modes.
// It returns the address to access, the value to write back, and
// whether writeback applies.
func (c *CPU) addressMode(op uint32, offset uint32) (addr, wb uint32, doWB bool) {
	rn := (op >> 16) & 0xF
	base := c.reg(rn, false)
	pre := (op>>24)&1 == 1
	up := (op>>23)&1 == 1
	wBit := (op>>21)&1 == 1
	adj := base + offset
	if !up {
		adj = base - offset
	}
	if pre {
		return adj, adj, wBit
	}
	return base, adj, true
}

// applyWriteback stores a writeback value to the base register.
// A base of R15 is written as a plain register (the addressing
// forms that write back to the program counter are unpredictable
// on hardware; the corpus model keeps the value in the register
// file without a pipeline refill).
func (c *CPU) applyWriteback(op uint32, wb uint32) {
	rn := (op >> 16) & 0xF
	if rn == 15 {
		// A writeback to the program counter refills the
		// pipeline in the reference model, targeting the
		// writeback value plus 4 (the corpus final states
		// show the resulting PC at the writeback value
		// plus 12 in R[15] terms).
		c.flush(wb + 4)
		return
	}
	c.R[rn] = wb
}

// execLoadStoreSingle implements LDR, STR, LDRB and STRB with
// immediate and register offsets.
func (c *CPU) execLoadStoreSingle(op uint32) int {
	var offset uint32
	if (op>>25)&1 == 0 {
		offset = op & 0xFFF
	} else {
		o, _ := shiftByImm(c.reg(op&0xF, false), (op>>5)&3, (op>>7)&0x1F, c.flagC())
		offset = o
	}
	addr, wb, doWB := c.addressMode(op, offset)
	rd := (op >> 12) & 0xF
	isByte := (op>>22)&1 == 1
	load := (op>>20)&1 == 1
	if load {
		var v uint32
		if isByte {
			v = c.loadByte(addr)
		} else {
			v = c.loadWord(addr)
		}
		if doWB {
			c.applyWriteback(op, wb)
		}
		if rd == 15 {
			c.flush(v)
			return 5
		}
		c.R[rd] = v
		return 3
	}
	v := c.reg(rd, true)
	if isByte {
		c.storeByte(addr, v)
	} else {
		c.storeWord(addr, v)
	}
	if doWB {
		c.applyWriteback(op, wb)
	}
	return 2
}

// execLoadStoreHalf implements the halfword and signed transfer
// group: STRH, LDRH, LDRSB and LDRSH.
func (c *CPU) execLoadStoreHalf(op uint32) int {
	var offset uint32
	if (op>>22)&1 == 1 {
		offset = ((op>>8)&0xF)<<4 | (op & 0xF)
	} else {
		offset = c.reg(op&0xF, false)
	}
	addr, wb, doWB := c.addressMode(op, offset)
	rd := (op >> 12) & 0xF
	load := (op>>20)&1 == 1
	sh := (op >> 5) & 3
	if !load {
		// Only STRH is a store in this group in ARMv4.
		if sh != 1 {
			return c.undefinedException()
		}
		v := c.reg(rd, true)
		c.storeHalf(addr, v)
		if doWB {
			c.applyWriteback(op, wb)
		}
		return 2
	}
	var v uint32
	switch sh {
	case 1:
		v = c.loadHalf(addr)
	case 2:
		v = c.loadByteSigned(addr)
	case 3:
		v = c.loadHalfSigned(addr)
	}
	if doWB {
		c.applyWriteback(op, wb)
	}
	if rd == 15 {
		c.flush(v)
		return 5
	}
	c.R[rd] = v
	return 3
}

// execSwap implements SWP and SWPB. The base register is read
// with the plus 12 program counter rule (the corpus shows a base
// of R15 addressing the instruction address plus 12), and a
// destination of R15 is a plain register write, as with MRS.
func (c *CPU) execSwap(op uint32) int {
	rn := (op >> 16) & 0xF
	rd := (op >> 12) & 0xF
	addr := c.reg(rn, true)
	src := c.reg(op&0xF, true)
	if (op>>22)&1 == 1 {
		v := c.loadByte(addr)
		c.storeByte(addr, src)
		if rd == 15 {
			c.flush(v)
			return 5
		}
		c.R[rd] = v
		return 4
	}
	v := c.loadWord(addr)
	c.storeWord(addr, src)
	if rd == 15 {
		c.flush(v)
		return 5
	}
	c.R[rd] = v
	return 4
}

// userReg reads a register from the user bank view, used by the
// caret (user register) form of block transfers.
func (c *CPU) userReg(i int) uint32 {
	switch {
	case i < 8 || i == 15:
		return c.R[i]
	case i < 13:
		if c.Mode() == ModeFIQ {
			return c.usr8_12[i-8]
		}
		return c.R[i]
	default:
		if c.Mode() == ModeUSR || c.Mode() == ModeSYS {
			return c.R[i]
		}
		return c.RUSR[i-13]
	}
}

// setUserReg writes a register in the user bank view.
func (c *CPU) setUserReg(i int, v uint32) {
	switch {
	case i < 8 || i == 15:
		c.R[i] = v
	case i < 13:
		if c.Mode() == ModeFIQ {
			c.usr8_12[i-8] = v
		} else {
			c.R[i] = v
		}
	default:
		if c.Mode() == ModeUSR || c.Mode() == ModeSYS {
			c.R[i] = v
		} else {
			c.RUSR[i-13] = v
		}
	}
}

// execBlockTransfer implements LDM and STM in all four address
// modes, including the caret forms and the empty list quirk (an
// empty register list transfers only the program counter but
// counts as sixteen registers for addressing and writeback).
func (c *CPU) execBlockTransfer(op uint32) int {
	load := (op>>20)&1 == 1
	sBit := (op>>22)&1 == 1
	wBit := (op>>21)&1 == 1
	rn := (op >> 16) & 0xF
	list := op & 0xFFFF
	count := 0
	for i := 0; i < 16; i++ {
		if list>>i&1 == 1 {
			count++
		}
	}
	if list == 0 {
		count = 16
	}
	base := c.R[rn]
	up := (op>>23)&1 == 1
	pre := (op>>24)&1 == 1
	var start, wb uint32
	if up {
		wb = base + 4*uint32(count)
		start = base
		if pre {
			start = base + 4
		}
	} else {
		wb = base - 4*uint32(count)
		start = wb
		if !pre {
			start = wb + 4
		}
	}
	first := -1
	for i := 0; i < 16; i++ {
		if list>>i&1 == 1 {
			first = i
			break
		}
	}
	// The caret form transfers user bank registers, except that a
	// load with the program counter in the list (the exception
	// return form) loads the current bank and restores the CPSR.
	userRegs := sBit && !(load && list>>15&1 == 1)
	addr := start
	var loadedPC uint32
	pcLoaded := false
	var rnLoaded uint32
	rnInList := rn < 16 && list>>rn&1 == 1
	cycles := 2
	for i := 0; i < 16; i++ {
		transfer := list>>i&1 == 1 || (list == 0 && i == 15)
		if !transfer {
			continue
		}
		cycles++
		if load {
			v := c.Bus.Read32(addr)
			switch {
			case i == 15:
				loadedPC = v
				pcLoaded = true
			case userRegs:
				if uint32(i) == rn {
					rnLoaded = v
				}
				c.setUserReg(i, v)
			default:
				if uint32(i) == rn {
					rnLoaded = v
				}
				c.R[i] = v
			}
		} else {
			var v uint32
			switch {
			case wBit && uint32(i) == rn && rnInList && i != first:
				// A base register in the list that is not
				// first is stored with the writeback
				// value when writeback is enabled, and
				// with its original value otherwise
				// (the corpus store data shows this).
				v = wb
			case sBit && i != 15:
				v = c.userReg(i)
			case i == 15:
				v = c.R[15] + 4
			default:
				v = c.R[i]
			}
			c.Bus.Write32(addr, v)
		}
		addr += 4
	}
	if wBit {
		switch {
		case userRegs && rn != 15:
			// The caret form routes the writeback to the
			// user bank base register in the reference
			// model (a caret store in FIQ mode leaves the
			// active stack pointer untouched and updates
			// the user one). A loaded base register still
			// wins over the writeback value.
			if load && rnInList {
				c.setUserReg(int(rn), rnLoaded)
			} else {
				c.setUserReg(int(rn), wb)
			}
		case rn == 15:
			// Base writeback to the program counter
			// refills the pipeline at the writeback value.
			c.flush(wb)
		default:
			c.R[rn] = wb
			if load && rnInList && rn != 15 {
				c.R[rn] = rnLoaded
			}
		}
	}
	if pcLoaded {
		if sBit {
			c.restoreCPSRFromSPSR()
		}
		// A block transfer program counter load is not masked:
		// the corpus shows the loaded value, low bits included,
		// becoming the new execution address.
		c.flush(loadedPC)
		return cycles + 2
	}
	return cycles
}
