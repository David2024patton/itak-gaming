package arm7tdmi

// execThumb executes one Thumb-state instruction (the halfword in
// the execute stage, located at R15-4) and returns its cycle
// count.
func (c *CPU) execThumb(op uint16) int {
	addr := c.R[15] - 4

	switch {
	// Format 1 and 2: shifts by immediate, add/subtract.
	case op>>13 == 0 && (op>>11)&3 != 3:
		typ := uint32((op >> 11) & 3)
		amt := uint32((op >> 6) & 0x1F)
		rs := c.R[(op>>3)&7]
		res, carry := c.shifter(rs, typ, amt, false)
		c.R[op&7] = res
		c.setNZC(res, carry)
		return 1
	case op>>13 == 0:
		// Format 2: ADD/SUB register or 3-bit immediate. The
		// first operand is the register in bits 5-3; the second
		// operand (a register or the immediate) is in bits 8-6.
		sub := op&(1<<9) != 0
		a := c.R[(op>>3)&7]
		var b uint32
		if op&(1<<10) != 0 {
			b = uint32((op >> 6) & 7)
		} else {
			b = c.R[(op>>6)&7]
		}
		var res uint32
		if sub {
			res = c.subFlags(a, b, true)
		} else {
			res = c.addFlags(a, b, false)
		}
		c.R[op&7] = res
		return 1

	// Format 3: move/compare/add/subtract immediate.
	case op>>13 == 1:
		rd := (op >> 8) & 7
		imm := uint32(op & 0xFF)
		switch (op >> 11) & 3 {
		case 0: // MOV
			c.R[rd] = imm
			c.setNZ(imm)
		case 1: // CMP
			c.subFlags(c.R[rd], imm, true)
		case 2: // ADD
			c.R[rd] = c.addFlags(c.R[rd], imm, false)
		case 3: // SUB
			c.R[rd] = c.subFlags(c.R[rd], imm, true)
		}
		return 1

	// Format 4: ALU operations.
	case op>>12 == 4 && (op>>11)&1 == 0 && (op>>10)&1 == 0:
		return c.execThumbALU(op)

	// Format 5: high-register operations and BX.
	case op>>12 == 4 && (op>>11)&1 == 0:
		operation := (op >> 8) & 3
		rd := uint32(op & 7)
		rs := uint32((op >> 3) & 7)
		if op&(1<<7) != 0 {
			rd += 8
		}
		if op&(1<<6) != 0 {
			rs += 8
		}
		switch operation {
		case 0: // ADD
			res := c.R[rd] + c.R[rs]
			if rd == 15 {
				c.branch(res)
				return 3
			}
			c.R[rd] = res
		case 1: // CMP
			c.subFlags(c.R[rd], c.R[rs], true)
		case 2: // MOV
			if rd == 15 {
				c.branch(c.R[rs])
				return 3
			}
			c.R[rd] = c.R[rs]
		case 3: // BX (the bit-7 BLX register encoding does not
			// exist on ARM7TDMI; the reference implementation
			// executes it as a plain BX with no LR write)
			v := c.R[rs]
			if v&1 != 0 {
				c.CPSR |= BitT
			} else {
				c.CPSR &^= BitT
			}
			c.branch(v &^ 1)
			return 3
		}
		return 1

	// Format 6: PC-relative load.
	case op>>11 == 0b01001:
		rd := (op >> 8) & 7
		base := c.R[15] &^ 2
		v := c.load(base+uint32(op&0xFF)*4, 4, false)
		c.R[rd] = v
		return 3

	// Formats 7 and 8: load/store with register offset.
	case op>>12 == 5:
		rn := c.R[(op>>3)&7]
		ea := rn + c.R[(op>>6)&7]
		rd := uint32(op & 7)
		if op&(1<<9) == 0 {
			// Format 7, keyed by bits 11-10 (L, B).
			switch (op >> 10) & 3 {
			case 0:
				c.store(ea, 4, c.R[rd])
			case 1:
				c.store(ea, 1, c.R[rd])
			case 2:
				c.R[rd] = c.load(ea, 4, false)
				return 3
			case 3:
				c.R[rd] = c.load(ea, 1, false)
				return 3
			}
			return 2
		}
		// Format 8, keyed by bits 11-9.
		switch (op >> 9) & 7 {
		case 1:
			c.store(ea, 2, c.R[rd])
			return 2
		case 3:
			c.R[rd] = c.load(ea, 1, true)
		case 5:
			c.R[rd] = c.load(ea, 2, false)
		case 7:
			c.R[rd] = c.load(ea, 2, true)
		}
		return 3

	// Format 9: load/store with immediate offset.
	case op>>13 == 3:
		imm := uint32((op >> 6) & 0x1F)
		rn := c.R[(op>>3)&7]
		rd := uint32(op & 7)
		load := op&(1<<11) != 0
		if op&(1<<12) != 0 { // byte
			ea := rn + imm
			if load {
				c.R[rd] = c.load(ea, 1, false)
				return 3
			}
			c.store(ea, 1, c.R[rd])
			return 2
		}
		ea := rn + imm*4
		if load {
			c.R[rd] = c.load(ea, 4, false)
			return 3
		}
		c.store(ea, 4, c.R[rd])
		return 2

	// Format 10: halfword load/store with immediate offset.
	case op>>12 == 8:
		imm := uint32((op>>6)&0x1F) * 2
		ea := c.R[(op>>3)&7] + imm
		rd := uint32(op & 7)
		if op&(1<<11) != 0 {
			c.R[rd] = c.load(ea, 2, false)
			return 3
		}
		c.store(ea, 2, c.R[rd])
		return 2

	// Format 11: SP-relative load/store.
	case op>>12 == 9:
		rd := (op >> 8) & 7
		ea := c.R[13] + uint32(op&0xFF)*4
		if op&(1<<11) != 0 {
			c.R[rd] = c.load(ea, 4, false)
			return 3
		}
		c.store(ea, 4, c.R[rd])
		return 2

	// Format 12: load address.
	case op>>12 == 10:
		rd := (op >> 8) & 7
		var base uint32
		if op&(1<<11) != 0 {
			base = c.R[13]
		} else {
			base = c.R[15] &^ 2
		}
		c.R[rd] = base + uint32(op&0xFF)*4
		return 1

	// Format 13: add offset to stack pointer.
	case op&0xFF00 == 0xB000:
		imm := uint32(op&0x7F) * 4
		if op&(1<<7) != 0 {
			c.R[13] -= imm
		} else {
			c.R[13] += imm
		}
		return 1

	// Format 14: push and pop.
	case op&0xFE00 == 0xB400:
		list := uint32(op & 0xFF)
		if op&(1<<8) != 0 {
			list |= 1 << 14
		}
		return c.thumbPush(list)
	case op&0xFE00 == 0xBC00:
		list := uint32(op & 0xFF)
		if op&(1<<8) != 0 {
			list |= 1 << 15
		}
		return c.thumbPop(list)

	// Format 16: multiple load/store.
	case op>>12 == 12:
		rn := uint32((op >> 8) & 7)
		list := uint32(op & 0xFF)
		if op&(1<<11) != 0 {
			return c.thumbBlock(rn, list, true)
		}
		return c.thumbBlock(rn, list, false)

	// Format 15: conditional branch and SWI.
	case op>>12 == 13:
		cond := uint32((op >> 8) & 0xF)
		if cond == 0xF {
			// SWI in Thumb state: LR_svc is A+2.
			return c.exception(0x08, ModeSVC, addr+2)
		}
		// Condition 14, undefined in the Thumb encoding, is
		// evaluated as always by the reference implementation
		// (the shared condition table).
		if c.condPassed(cond) {
			off := int32(int8(op&0xFF)) * 2
			c.branch(uint32(int32(c.R[15]) + off))
			return 3
		}
		return 1

	// Format 17: unconditional branch.
	case op>>11 == 0b11100:
		off := (int32(op&0x7FF) << 21) >> 21 * 2
		c.branch(uint32(int32(c.R[15]) + off))
		return 3

	// Format 18: long branch prefix.
	case op>>11 == 0b11110:
		off := (int32(op&0x7FF) << 21) >> 21
		c.R[14] = uint32(int32(c.R[15]) + off<<12)
		return 1

	// Format 19: long branch suffix (BL), and the BLX suffix
	// encoding, which NBA also implements.
	case op>>11 == 0b11111 || op>>11 == 0b11101:
		target := c.R[14] + uint32(op&0x7FF)*2
		if op>>11 == 0b11101 {
			// BLX suffix: bit 0 of LR is part of the pair
			// encoding, not of the target.
			target = (c.R[14] &^ 1) + uint32(op&0x7FF)*2
			c.CPSR &^= BitT
		}
		c.R[14] = (addr + 2) | 1
		c.branch(target)
		return 3
	}

	return c.undefined(addr)
}

// execThumbALU runs format 4, the register-register ALU group.
func (c *CPU) execThumbALU(op uint16) int {
	rd := uint32(op & 7)
	rs := uint32((op >> 3) & 7)
	a := c.R[rd]
	b := c.R[rs]
	switch (op >> 6) & 0xF {
	case 0: // AND
		c.R[rd] = a & b
		c.setNZ(a & b)
	case 1: // EOR
		c.R[rd] = a ^ b
		c.setNZ(a ^ b)
	case 2: // LSL
		res, carry := c.shifter(a, 0, b&0xFF, true)
		c.R[rd] = res
		c.setNZC(res, carry)
		return 2
	case 3: // LSR
		res, carry := c.shifter(a, 1, b&0xFF, true)
		c.R[rd] = res
		c.setNZC(res, carry)
		return 2
	case 4: // ASR
		res, carry := c.shifter(a, 2, b&0xFF, true)
		c.R[rd] = res
		c.setNZC(res, carry)
		return 2
	case 5: // ADC
		c.R[rd] = c.addFlags(a, b, c.carry())
	case 6: // SBC
		c.R[rd] = c.subFlags(a, b, c.carry())
	case 7: // ROR
		res, carry := c.shifter(a, 3, b&0xFF, true)
		c.R[rd] = res
		c.setNZC(res, carry)
		return 2
	case 8: // TST
		c.setNZ(a & b)
	case 9: // NEG
		c.R[rd] = c.subFlags(0, b, true)
	case 10: // CMP
		c.subFlags(a, b, true)
	case 11: // CMN
		c.addFlags(a, b, false)
	case 12: // ORR
		c.R[rd] = a | b
		c.setNZ(a | b)
	case 13: // MUL: in the Thumb form the booth multiplier is
		// the destination register's old value and the
		// multiplicand is the source register (the reference
		// implementation's operand order).
		res := a * b
		c.R[rd] = res
		c.setNZ(res)
		c.setCarry(mulCarry(tickMultiply(true, a), false, false, b, a, 0, 0))
		return 1 + mulCycles(a)
	case 14: // BIC
		c.R[rd] = a &^ b
		c.setNZ(a &^ b)
	case 15: // MVN
		c.R[rd] = ^b
		c.setNZ(^b)
	}
	return 1
}

// branchRawThumb refills the pipeline at addr like a Thumb
// branch, but keeps the raw target value in R15 (bit 0 not
// discarded). The empty-list LDMIA form loads the PC this way
// in the reference implementation.
func (c *CPU) branchRawThumb(addr uint32) {
	fa := addr &^ 1
	c.Executing = c.Bus.Fetch(fa, 2)
	c.Pipe[0] = c.Bus.Fetch(fa+2, 2)
	c.Pipe[1] = c.Bus.Fetch(fa+4, 2)
	c.R[15] = addr + 4
	c.ExecutingValid = true
	c.flushed = true
}

// thumbPush stores a register list (STMDB SP), always with
// writeback.
func (c *CPU) thumbPush(list uint32) int {
	if list == 0 {
		// Empty list quirk: the PC is stored and the stack
		// pointer moves by the full 0x40.
		c.R[13] -= 0x40
		c.Bus.Write(c.R[13], 4, c.R[15]+2)
		return 2
	}
	count := popcount16(list)
	start := c.R[13] - 4*uint32(count)
	cur := start
	for i := uint(0); i < 16; i++ {
		if list&(1<<i) == 0 {
			continue
		}
		c.Bus.Write(cur, 4, c.R[i])
		cur += 4
	}
	c.R[13] = start
	return count + 1
}

// thumbPop loads a register list (LDMIA SP), always with
// writeback; a loaded PC branches.
func (c *CPU) thumbPop(list uint32) int {
	if list == 0 {
		// Empty list quirk: the PC is loaded (raw, bit 0
		// kept) and the stack pointer moves by 0x40.
		v := c.Bus.Read(c.R[13], 4)
		c.R[13] += 0x40
		c.branchRawThumb(v)
		return 5
	}
	count := popcount16(list)
	cur := c.R[13]
	var pcVal uint32
	pcSet := false
	for i := uint(0); i < 16; i++ {
		if list&(1<<i) == 0 {
			continue
		}
		v := c.Bus.Read(cur, 4)
		cur += 4
		if i == 15 {
			pcVal, pcSet = v, true
		} else {
			c.R[i] = v
		}
	}
	c.R[13] += 4 * uint32(count)
	n := count + 2
	if pcSet {
		c.branch(pcVal)
		n += 2
	}
	return n
}

// thumbBlock runs the Thumb LDMIA/STMIA forms (format 16), which
// always write back. A load whose base is in the list keeps the
// loaded value and suppresses writeback; a store whose base is in
// the list stores the old base when first and the new base
// otherwise. An empty list transfers R15 with a 0x40 writeback,
// as on ARM.
func (c *CPU) thumbBlock(rn uint32, list uint32, load bool) int {
	base := c.R[rn]
	empty := list == 0
	if empty {
		list = 1 << 15
	}
	count := popcount16(list)
	wb := base + 4*uint32(count)
	if empty {
		wb = base + 0x40
	}
	if load {
		var pcVal uint32
		pcSet := false
		cur := base
		for i := uint(0); i < 16; i++ {
			if list&(1<<i) == 0 {
				continue
			}
			v := c.Bus.Read(cur, 4)
			cur += 4
			if i == 15 {
				pcVal, pcSet = v, true
			} else {
				c.R[i] = v
			}
		}
		if list&(1<<rn) == 0 {
			c.R[rn] = wb
		}
		n := count + 2
		if pcSet {
			if empty {
				// The empty-list LDMIA loads the PC raw.
				c.branchRawThumb(pcVal)
			} else {
				c.branch(pcVal)
			}
			n += 2
		}
		return n
	}
	idx := 0
	rnIdx := -1
	for i := uint(0); i < 16; i++ {
		if list&(1<<i) != 0 {
			if uint32(i) == rn {
				rnIdx = idx
			}
			idx++
		}
	}
	cur := base
	idx = 0
	for i := uint(0); i < 16; i++ {
		if list&(1<<i) == 0 {
			continue
		}
		v := c.R[i]
		if uint32(i) == rn && rnIdx > 0 {
			v = wb
		}
		if i == 15 {
			v = c.R[15] + 2 // a stored PC reads as A+6 in Thumb state
		}
		c.Bus.Write(cur, 4, v)
		cur += 4
		idx++
	}
	c.R[rn] = wb
	return count + 1
}

func popcount16(list uint32) int {
	n := 0
	for i := uint(0); i < 16; i++ {
		if list&(1<<i) != 0 {
			n++
		}
	}
	return n
}
