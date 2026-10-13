package z80

// Indexed (DD/FD) instruction execution. idx points at the index
// register in use (IX for 0xDD, IY for 0xFD); its halves are
// reachable as the IXh/IXl (IYh/IYl) pseudo-registers wherever the
// base instruction set would use H or L, except when the instruction
// also uses the (IX+d) memory operand, where real H and L apply.
// Opcodes the prefix does not modify fall back to the base
// instruction with a 4 T-state prefix penalty.

// idxAddr computes the effective address IX+d / IY+d, fetching the
// signed displacement byte.
func (c *CPU) idxAddr(idx *uint16) uint16 {
	d := int8(c.operand())
	return uint16(int32(*idx) + int32(d))
}

// idxHalf reads the high (hi=true) or low half of the index register.
func idxHalf(idx *uint16, hi bool) uint8 {
	if hi {
		return uint8(*idx >> 8)
	}
	return uint8(*idx)
}

// setIdxHalf writes one half of the index register.
func setIdxHalf(idx *uint16, hi bool, v uint8) {
	if hi {
		*idx = (*idx & 0x00FF) | uint16(v)<<8
	} else {
		*idx = (*idx & 0xFF00) | uint16(v)
	}
}

// idxRegVal reads operand n in indexed mode: 4 and 5 select the
// index halves, everything else is a plain register. Never called
// with n == 6.
func (c *CPU) idxRegVal(idx *uint16, n int) uint8 {
	switch n {
	case 4:
		return idxHalf(idx, true)
	case 5:
		return idxHalf(idx, false)
	default:
		return c.reg(n)
	}
}

// setIdxReg writes operand n in indexed mode (see idxRegVal).
func (c *CPU) setIdxReg(idx *uint16, n int, v uint8) {
	switch n {
	case 4:
		setIdxHalf(idx, true, v)
	case 5:
		setIdxHalf(idx, false, v)
	default:
		c.setReg(n, v)
	}
}

// execIndexed executes one opcode with a DD/FD prefix in effect and
// returns its documented T-state cost (which includes the prefix).
func (c *CPU) execIndexed(op byte, idx *uint16) int {
	switch {
	case op&0xCF == 0x09: // ADD idx,rr (rr == idx for the 0x29 form)
		var v uint16
		if n := int(op>>4) & 3; n == 2 {
			v = *idx
		} else {
			v = c.pair(n)
		}
		*idx = c.add16(*idx, v)
		return 15
	case op == 0x21: // LD idx,nn
		*idx = c.operand16()
		return 14
	case op == 0x22: // LD (nn),idx
		c.write16(c.operand16(), *idx)
		return 20
	case op == 0x23: // INC idx
		*idx++
		return 10
	case op == 0x2A: // LD idx,(nn)
		*idx = c.read16(c.operand16())
		return 20
	case op == 0x2B: // DEC idx
		*idx--
		return 10
	case op&0xC7 == 0x04 || op&0xC7 == 0x05: // INC/DEC half or (idx+d)
		n := int(op>>3) & 7
		apply := c.inc8
		if op&0xC7 == 0x05 {
			apply = c.dec8
		}
		switch n {
		case 4:
			setIdxHalf(idx, true, apply(idxHalf(idx, true)))
			return 8
		case 5:
			setIdxHalf(idx, false, apply(idxHalf(idx, false)))
			return 8
		case 6:
			addr := c.idxAddr(idx)
			c.bus.Write(addr, apply(c.bus.Read(addr)))
			return 23
		default:
			return c.execBase(op) + 4
		}
	case op&0xC7 == 0x06: // LD half,n / LD (idx+d),n
		n := int(op>>3) & 7
		switch n {
		case 4:
			setIdxHalf(idx, true, c.operand())
			return 11
		case 5:
			setIdxHalf(idx, false, c.operand())
			return 11
		case 6:
			addr := c.idxAddr(idx)
			c.bus.Write(addr, c.operand())
			return 19
		default:
			return c.execBase(op) + 4
		}
	case op == 0x76: // HALT (prefix does not change it)
		c.halted = true
		return 8
	case op >= 0x40 && op <= 0x7F: // LD r,r' with index rules
		dst, src := int(op>>3)&7, int(op)&7
		if src == 6 { // LD r,(idx+d): the register is a plain one
			c.setReg(dst, c.bus.Read(c.idxAddr(idx)))
			return 19
		}
		if dst == 6 { // LD (idx+d),r: likewise
			c.bus.Write(c.idxAddr(idx), c.reg(src))
			return 19
		}
		c.setIdxReg(idx, dst, c.idxRegVal(idx, src))
		return 8
	case op >= 0x80 && op <= 0xBF: // ALU A,operand
		src := int(op) & 7
		kind := int(op>>3) & 7
		switch src {
		case 6:
			c.alu(kind, c.bus.Read(c.idxAddr(idx)))
			return 19
		case 4:
			c.alu(kind, idxHalf(idx, true))
			return 8
		case 5:
			c.alu(kind, idxHalf(idx, false))
			return 8
		default:
			c.alu(kind, c.reg(src))
			return 8
		}
	case op == 0xE1: // POP idx
		*idx = c.pop()
		return 14
	case op == 0xE3: // EX (SP),idx
		v := c.read16(c.sp)
		c.write16(c.sp, *idx)
		*idx = v
		return 23
	case op == 0xE5: // PUSH idx
		c.push(*idx)
		return 15
	case op == 0xE9: // JP (idx)
		c.pc = *idx
		return 8
	case op == 0xF9: // LD SP,idx
		c.sp = *idx
		return 10
	default:
		// The prefix does not modify this instruction.
		return c.execBase(op) + 4
	}
}
