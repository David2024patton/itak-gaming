package arm7

// Thumb state instruction execution: the complete ARMv4T Thumb
// instruction set. Program counter reads in Thumb state see the
// stored R[15], which is the executing instruction address plus
// 4; PC relative addressing aligns that value down to a word
// boundary first.

// execThumb executes one Thumb instruction and returns cycles.
func (c *CPU) execThumb(op uint16) int {
	switch {
	case op>>13 == 0b000: // shifts, add, subtract
		if (op>>11)&3 == 3 {
			return c.thumbAddSub(op)
		}
		return c.thumbShiftImm(op)
	case op>>13 == 0b001: // move, compare, add, subtract immediate
		return c.thumbImm8(op)
	case op>>10 == 0b010000: // ALU operations
		return c.thumbALU(op)
	case op>>10 == 0b010001: // high register operations, BX
		return c.thumbHiReg(op)
	case op>>11 == 0b01001: // PC relative load
		rd := (op >> 8) & 7
		addr := (c.R[15] &^ 2) + uint32(op&0xFF)*4
		c.R[rd] = c.loadWord(addr)
		return 3
	case op>>12 == 0b0101: // load and store register offset groups
		return c.thumbLoadStoreReg(op)
	case op>>13 == 0b011: // load and store immediate offset
		return c.thumbLoadStoreImm(op)
	case op>>12 == 0b1000: // halfword immediate offset
		return c.thumbLoadStoreHalf(op)
	case op>>12 == 0b1001: // SP relative load and store
		rd := (op >> 8) & 7
		addr := c.R[13] + uint32(op&0xFF)*4
		if (op>>11)&1 == 1 {
			c.R[rd] = c.loadWord(addr)
			return 3
		}
		c.storeWord(addr, c.R[rd])
		return 2
	case op>>12 == 0b1010: // add PC or SP to register
		rd := (op >> 8) & 7
		var base uint32
		if (op>>11)&1 == 1 {
			base = c.R[13]
		} else {
			base = c.R[15] &^ 2
		}
		c.R[rd] = base + uint32(op&0xFF)*4
		return 1
	case op>>12 == 0b1011: // miscellaneous
		return c.thumbMisc(op)
	case op>>12 == 0b1100: // multiple load and store
		return c.thumbBlockTransfer(op)
	case op>>12 == 0b1101: // conditional branch and SWI
		if (op>>8)&0xF == 0xF {
			return c.swiException()
		}
		// Condition 1110 in this encoding is the always condition
		// on ARM7TDMI silicon (the architecture reserves it, but
		// the hardware condition evaluator treats it as AL), and
		// the corpus asserts branch always behaviour for it.
		if c.condPassed(uint32(op>>8) & 0xF) {
			off := int32(int8(op&0xFF)) * 2
			c.flush(uint32(int32(c.R[15]) + off))
			return 3
		}
		return 1
	case op>>11 == 0b11100: // unconditional branch
		off := int32(op&0x7FF) << 21 >> 21 * 2
		c.flush(uint32(int32(c.R[15]) + off))
		return 3
	case op>>11 == 0b11110: // BL, BLX prefix
		off := int32(op&0x7FF) << 21 >> 21 << 12
		c.R[14] = uint32(int32(c.R[15]) + off)
		return 1
	case op>>11 == 0b11101: // BLX suffix
		target := c.R[14] + uint32(op&0x7FF)*2
		c.R[14] = (c.R[15] - 2) | 1
		c.CPSR &^= cpsrT
		c.writePC(target)
		return 3
	case op>>11 == 0b11111: // BL suffix
		target := c.R[14] + uint32(op&0x7FF)*2
		c.R[14] = (c.R[15] - 2) | 1
		c.flush(target &^ 1)
		return 3
	}
	return c.undefinedException()
}

func (c *CPU) thumbShiftImm(op uint16) int {
	amt := (op >> 6) & 0x1F
	rm := (op >> 3) & 7
	rd := op & 7
	res, carry := shiftByImm(c.R[rm], uint32(op>>11)&3, uint32(amt), c.flagC())
	c.R[rd] = res
	c.setLogicalFlags(res, carry)
	return 1
}

func (c *CPU) thumbAddSub(op uint16) int {
	rd := op & 7
	rn := (op >> 3) & 7
	var b uint32
	if (op>>10)&1 == 1 {
		b = uint32((op >> 6) & 7)
	} else {
		b = c.R[(op>>6)&7]
	}
	if (op>>9)&1 == 1 {
		c.R[rd] = c.subFlags(c.R[rn], b, 1)
	} else {
		c.R[rd] = c.addFlags(c.R[rn], b, 0)
	}
	return 1
}

func (c *CPU) thumbImm8(op uint16) int {
	rd := (op >> 8) & 7
	imm := uint32(op & 0xFF)
	switch (op >> 11) & 3 {
	case 0: // MOV
		c.R[rd] = imm
		c.setNZ(imm)
	case 1: // CMP
		c.subFlags(c.R[rd], imm, 1)
	case 2: // ADD
		c.R[rd] = c.addFlags(c.R[rd], imm, 0)
	case 3: // SUB
		c.R[rd] = c.subFlags(c.R[rd], imm, 1)
	}
	return 1
}

// thumbALU implements the format 4 ALU group. Shift by register
// operations use the full low byte of the shift register, as in
// ARM state. The multiply here always clears the carry flag,
// which is what the corpus asserts for the Thumb form.
func (c *CPU) thumbALU(op uint16) int {
	aluOp := (op >> 6) & 0xF
	rd := op & 7
	rs := (op >> 3) & 7
	a := c.R[rd]
	b := c.R[rs]
	carryIn := uint32(0)
	if c.flagC() {
		carryIn = 1
	}
	switch aluOp {
	case 0x0: // AND
		res := a & b
		c.R[rd] = res
		c.setLogicalFlags(res, c.flagC())
	case 0x1: // EOR
		res := a ^ b
		c.R[rd] = res
		c.setLogicalFlags(res, c.flagC())
	case 0x2: // LSL by register
		res, carry := shiftByReg(a, 0, b&0xFF, c.flagC())
		c.R[rd] = res
		c.setLogicalFlags(res, carry)
	case 0x3: // LSR by register
		res, carry := shiftByReg(a, 1, b&0xFF, c.flagC())
		c.R[rd] = res
		c.setLogicalFlags(res, carry)
	case 0x4: // ASR by register
		res, carry := shiftByReg(a, 2, b&0xFF, c.flagC())
		c.R[rd] = res
		c.setLogicalFlags(res, carry)
	case 0x5: // ADC
		c.R[rd] = c.addFlags(a, b, carryIn)
	case 0x6: // SBC
		c.R[rd] = c.subFlags(a, b, carryIn)
	case 0x7: // ROR by register
		res, carry := shiftByReg(a, 3, b&0xFF, c.flagC())
		c.R[rd] = res
		c.setLogicalFlags(res, carry)
	case 0x8: // TST
		c.setLogicalFlags(a&b, c.flagC())
	case 0x9: // NEG
		c.R[rd] = c.subFlags(0, b, 1)
	case 0xA: // CMP
		c.subFlags(a, b, 1)
	case 0xB: // CMN
		c.addFlags(a, b, 0)
	case 0xC: // ORR
		res := a | b
		c.R[rd] = res
		c.setLogicalFlags(res, c.flagC())
	case 0xD: // MUL
		res := a * b
		c.R[rd] = res
		c.setNZ(res)
		// The carry is the multiplier carry out, as in the
		// ARM form, with the destination register operand in
		// the multiplier role of the model.
		c.setFlag(cpsrC, mulCarryShort(b, a, 0))
	case 0xE: // BIC
		res := a &^ b
		c.R[rd] = res
		c.setLogicalFlags(res, c.flagC())
	case 0xF: // MVN
		res := ^b
		c.R[rd] = res
		c.setLogicalFlags(res, c.flagC())
	}
	return 1
}

func (c *CPU) thumbHiReg(op uint16) int {
	hiOp := (op >> 8) & 3
	rd := uint32(op&7) | uint32((op>>7)&1)<<3
	rs := uint32((op >> 3) & 0xF)
	if hiOp == 3 {
		// BX. The register form of BLX does not exist in ARMv4T;
		// bit 7 is ignored and the whole encoding is BX, which is
		// what the corpus asserts.
		v := c.R[rs]
		if v&1 != 0 {
			c.CPSR |= cpsrT
		} else {
			c.CPSR &^= cpsrT
		}
		c.writePC(v)
		return 3
	}
	src := c.R[rs]
	switch hiOp {
	case 0: // ADD
		res := c.R[rd] + src
		if rd == 15 {
			c.writePC(res)
			return 3
		}
		c.R[rd] = res
	case 1: // CMP
		c.subFlags(c.R[rd], src, 1)
	case 2: // MOV
		if rd == 15 {
			c.writePC(src)
			return 3
		}
		c.R[rd] = src
	}
	return 1
}

// thumbLoadStoreReg implements format 7 (register offset word and
// byte transfers) and format 7b (register offset halfword and
// signed transfers), selected by bit 9.
func (c *CPU) thumbLoadStoreReg(op uint16) int {
	ro := (op >> 6) & 7
	rb := (op >> 3) & 7
	rd := op & 7
	addr := c.R[rb] + c.R[ro]
	if (op>>9)&1 == 0 {
		load := (op>>11)&1 == 1
		isByte := (op>>10)&1 == 1
		if load {
			if isByte {
				c.R[rd] = c.loadByte(addr)
			} else {
				c.R[rd] = c.loadWord(addr)
			}
			return 3
		}
		if isByte {
			c.storeByte(addr, c.R[rd])
		} else {
			c.storeWord(addr, c.R[rd])
		}
		return 2
	}
	switch (op >> 10) & 3 {
	case 0: // STRH
		c.storeHalf(addr, c.R[rd])
		return 2
	case 1: // LDRSB
		c.R[rd] = c.loadByteSigned(addr)
	case 2: // LDRH
		c.R[rd] = c.loadHalf(addr)
	case 3: // LDRSH
		c.R[rd] = c.loadHalfSigned(addr)
	}
	return 3
}

func (c *CPU) thumbLoadStoreImm(op uint16) int {
	rb := (op >> 3) & 7
	rd := op & 7
	isByte := (op>>12)&1 == 1
	load := (op>>11)&1 == 1
	imm := uint32((op >> 6) & 0x1F)
	var addr uint32
	if isByte {
		addr = c.R[rb] + imm
	} else {
		addr = c.R[rb] + imm*4
	}
	if load {
		if isByte {
			c.R[rd] = c.loadByte(addr)
		} else {
			c.R[rd] = c.loadWord(addr)
		}
		return 3
	}
	if isByte {
		c.storeByte(addr, c.R[rd])
	} else {
		c.storeWord(addr, c.R[rd])
	}
	return 2
}

func (c *CPU) thumbLoadStoreHalf(op uint16) int {
	rb := (op >> 3) & 7
	rd := op & 7
	addr := c.R[rb] + uint32((op>>6)&0x1F)*2
	if (op>>11)&1 == 1 {
		c.R[rd] = c.loadHalf(addr)
		return 3
	}
	c.storeHalf(addr, c.R[rd])
	return 2
}

// thumbMisc implements the 1011 group: ADD SP with a signed
// immediate, PUSH, and POP. Other encodings in the group are
// undefined in ARMv4T.
func (c *CPU) thumbMisc(op uint16) int {
	switch {
	case op&0xFF00 == 0xB000: // ADD SP, signed immediate times 4
		imm := uint32(op&0x7F) * 4
		if (op>>7)&1 == 1 {
			c.R[13] -= imm
		} else {
			c.R[13] += imm
		}
		return 1
	case op&0xFE00 == 0xB400: // PUSH
		list := op & 0xFF
		count := 0
		for i := 0; i < 8; i++ {
			if list>>i&1 == 1 {
				count++
			}
		}
		withLR := (op>>8)&1 == 1
		if withLR {
			count++
		}
		if count == 0 {
			// Empty list: only the program counter
			// transfers, and the stack pointer still moves
			// by the full sixteen register frame.
			sp := c.R[13] - 64
			c.Bus.Write32(sp, c.R[15]+2)
			c.R[13] = sp
			return 3
		}
		sp := c.R[13] - 4*uint32(count)
		addr := sp
		for i := 0; i < 8; i++ {
			if list>>i&1 == 1 {
				c.Bus.Write32(addr, c.R[i])
				addr += 4
			}
		}
		if withLR {
			c.Bus.Write32(addr, c.R[14])
		}
		c.R[13] = sp
		return 2 + count
	case op&0xFE00 == 0xBC00: // POP
		list := op & 0xFF
		count := 0
		for i := 0; i < 8; i++ {
			if list>>i&1 == 1 {
				count++
			}
		}
		withPC := (op>>8)&1 == 1
		if withPC {
			count++
		}
		if count == 0 {
			// Empty list: only the program counter
			// transfers, and the stack pointer still moves
			// by the full sixteen register frame. The
			// loaded value goes to flush unmasked: flush
			// keeps its low bits visible in R[15] (the
			// corpus compares them) and aligns only the
			// refill fetches.
			pc := c.Bus.Read32(c.R[13])
			c.R[13] += 64
			c.flush(pc)
			return 5
		}
		addr := c.R[13]
		for i := 0; i < 8; i++ {
			if list>>i&1 == 1 {
				c.R[i] = c.Bus.Read32(addr)
				addr += 4
			}
		}
		var pc uint32
		if withPC {
			pc = c.Bus.Read32(addr)
		}
		c.R[13] += 4 * uint32(count)
		if withPC {
			c.flush(pc &^ 1)
			return 5
		}
		return 2 + count
	}
	return c.undefinedException()
}

// thumbBlockTransfer implements the Thumb LDMIA and STMIA forms.
// The base register is always written back; an empty list counts
// as sixteen registers for the writeback and transfers only the
// program counter, as in the ARM form (the corpus transaction
// lists show the single transfer).
func (c *CPU) thumbBlockTransfer(op uint16) int {
	rb := uint32((op >> 8) & 7)
	list := op & 0xFF
	load := (op>>11)&1 == 1
	count := 0
	for i := 0; i < 8; i++ {
		if list>>i&1 == 1 {
			count++
		}
	}
	effCount := count
	if list == 0 {
		effCount = 16
	}
	base := c.R[rb]
	wb := base + 4*uint32(effCount)
	addr := base
	cycles := 2
	if list == 0 {
		// Empty list: only the program counter transfers.
		c.R[rb] = wb
		if load {
			v := c.Bus.Read32(base)
			c.flush(v)
		} else {
			// A stored Thumb program counter reads as the
			// instruction address plus 6.
			c.Bus.Write32(base, c.R[15]+2)
		}
		return cycles + 2
	}
	if load {
		var rbLoaded uint32
		rbInList := list>>rb&1 == 1
		for i := 0; i < 8; i++ {
			if list>>i&1 == 1 {
				v := c.Bus.Read32(addr)
				if uint32(i) == rb {
					rbLoaded = v
				}
				c.R[i] = v
				addr += 4
				cycles++
			}
		}
		c.R[rb] = wb
		if rbInList {
			c.R[rb] = rbLoaded
		}
		return cycles
	}
	first := -1
	for i := 0; i < 8; i++ {
		if list>>i&1 == 1 {
			first = i
			break
		}
	}
	for i := 0; i < 8; i++ {
		if list>>i&1 == 1 {
			v := c.R[i]
			if uint32(i) == rb && i != first {
				v = wb
			}
			c.Bus.Write32(addr, v)
			addr += 4
			cycles++
		}
	}
	c.R[rb] = wb
	return cycles
}
