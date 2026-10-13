package sh2

// isDelayedBranch reports whether op is any delayed
// branch form or RTE, for the delay slot legality check.
func isDelayedBranch(op uint16) bool {
	top := op >> 12
	switch top {
	case 0x0A, 0x0B:
		return true
	case 0x08:
		sub := (op >> 8) & 0x0F
		return sub == 0x0D || sub == 0x0F
	case 0x00:
		if op == 0x000B || op == 0x002B {
			return true
		}
		low := op & 0x0F
		mid := (op >> 4) & 0x0F
		return low == 3 && (mid == 0 || mid == 2)
	case 0x04:
		lowByte := op & 0xFF
		return lowByte == 0x0B || lowByte == 0x2B
	}
	return false
}

// execSlot fetches and executes the delay slot of the
// branch instruction at addr. A slot that is itself a
// delayed branch or RTE raises the slot illegal
// instruction exception, vector 6, with the saved PC at
// the slot address, and the outer branch is abandoned.
func (c *CPU) execSlot(addr uint32) (int, bool) {
	slotAddr := addr + 2
	c.cur = slotAddr
	op := c.fetch(slotAddr)
	if isDelayedBranch(op) {
		c.enterException(6, slotAddr)
		return 2, true
	}
	return c.exec(op, slotAddr)
}

// delayed runs the delay slot, then jumps to target.
func (c *CPU) delayed(addr uint32, target uint32, cost int) (int, bool) {
	if _, bad := c.execSlot(addr); bad {
		return cost, true
	}
	c.PC = target
	return cost, false
}

// div1 performs one DIV1 step, Rm into Rn, following
// the operation table in the SH programming manual,
// stated here in words: the old Q is saved; Q takes the
// top bit of Rn; Rn shifts left by one with T entering
// at the bottom; when old Q equals M the divisor is
// subtracted, otherwise it is added; the new Q is the
// exclusive or of the borrow or carry flag from that
// addition or subtraction, that top bit, the old Q,
// and 1 when the operation was an addition; finally
// T is set when the new Q equals M.
func (c *CPU) div1(m, n int) {
	oldQ := c.getQ()
	mv := c.getM()
	qSign := 0
	if c.Reg[n]>>31 != 0 {
		qSign = 1
	}
	c.Reg[n] = c.Reg[n]<<1 | uint32(c.getT())
	before := c.Reg[n]
	sub := oldQ == mv
	var flag int
	if sub {
		c.Reg[n] = before - c.Reg[m]
		if c.Reg[n] > before {
			flag = 1
		}
	} else {
		c.Reg[n] = before + c.Reg[m]
		if c.Reg[n] < before {
			flag = 1
		}
	}
	add := 0
	if !sub {
		add = 1
	}
	newQ := flag ^ qSign ^ oldQ ^ add
	c.setQ(newQ)
	if newQ == mv {
		c.setT(1)
	} else {
		c.setT(0)
	}
}

// macAdd adds product to the MACH:MACL accumulator.
//
// Saturation rule used here, stated plainly: when the
// S bit is set, the incoming accumulator is read as a
// signed 48 bit value (the low 16 bits of MACH together
// with MACL, sign extended), the product is added, and
// the result is clamped to the signed 48 bit range,
// whose ends are the 64 bit patterns 0x00007FFFFFFFFFFF
// (high) and 0xFFFF800000000000 (low). A clamped high
// result therefore leaves MACH as 0x00007FFF and MACL
// as 0xFFFFFFFF; a clamped low result leaves MACH as
// 0xFFFF8000 and MACL as 0. When S is clear, the full
// 64 bit accumulator wraps normally.
func (c *CPU) macAdd(product int64) {
	if c.getS() == 1 {
		acc := int64(uint64(c.MACH&0x0000FFFF)<<32 | uint64(c.MACL))
		if acc&0x0000800000000000 != 0 {
			acc -= 0x0001000000000000
		}
		result := acc + product
		hi := int64(0x00007FFFFFFFFFFF)
		lo := int64(-1) << 47
		if result > hi {
			result = hi
		}
		if result < lo {
			result = lo
		}
		c.MACH = uint32(uint64(result) >> 32)
		c.MACL = uint32(result)
		return
	}
	acc := uint64(c.MACH)<<32 | uint64(c.MACL)
	acc += uint64(product)
	c.MACH = uint32(acc >> 32)
	c.MACL = uint32(acc)
}

// sysRead returns the STS and STC source value for a
// selector: for STS, 0 MACH, 1 MACL, 2 PR; for STC the
// caller passes a flag and the selector means 0 SR,
// 1 GBR, 2 VBR.
func (c *CPU) stsValue(sel uint32) uint32 {
	switch sel {
	case 0:
		return c.MACH
	case 1:
		return c.MACL
	default:
		return c.PR
	}
}

func (c *CPU) stcValue(sel uint32) uint32 {
	switch sel {
	case 0:
		return c.SR
	case 1:
		return c.GBR
	default:
		return c.VBR
	}
}

func (c *CPU) ldsValue(sel uint32, v uint32) {
	switch sel {
	case 0:
		c.MACH = v
	case 1:
		c.MACL = v
	default:
		c.PR = v
	}
}

func (c *CPU) ldcValue(sel uint32, v uint32) {
	switch sel {
	case 0:
		c.SR = v
	case 1:
		c.GBR = v
	default:
		c.VBR = v
	}
}

// exec runs one fetched instruction at address addr and
// returns its nominal cycle count plus a flag telling
// whether an exception was taken (so a caller executing
// a delay slot can abandon the outer branch).
func (c *CPU) exec(op uint16, addr uint32) (int, bool) {
	c.cur = addr
	c.PC = addr + 2
	n := int((op >> 8) & 0x0F)
	m := int((op >> 4) & 0x0F)
	low := uint32(op & 0x0F)
	top := op >> 12

	switch top {
	case 0x00:
		return c.exec0(op, addr, n, m, low)
	case 0x01:
		// MOV.L Rm, @(disp 4, Rn)
		disp := uint32(op & 0x0F)
		dst := c.Reg[n] + disp*4
		c.store(dst, c.Reg[m], 4)
		return 2, false
	case 0x02:
		return c.exec2(addr, n, m, low)
	case 0x03:
		return c.exec3(addr, n, m, low)
	case 0x04:
		return c.exec4(op, addr, n, m)
	case 0x05:
		// MOV.L @(disp 4, Rm), Rn
		disp := uint32(op & 0x0F)
		src := c.Reg[m] + disp*4
		c.Reg[n] = c.rd32(src)
		return 2, false
	case 0x06:
		return c.exec6(addr, n, m, low)
	case 0x07:
		// ADD #imm, Rn
		c.Reg[n] += sext8(uint32(op & 0xFF))
		return 1, false
	case 0x08:
		return c.exec8(op, addr)
	case 0x09:
		// MOV.W @(disp, PC), Rn
		base := addr + 4
		src := base + uint32(op&0xFF)*2
		c.Reg[n] = sext16(uint32(c.rd16(src)))
		return 2, false
	case 0x0A:
		target := uint32(int32(addr+4) + sext12(uint32(op&0x0FFF))*2)
		return c.delayed(addr, target, 2)
	case 0x0B:
		c.PR = addr + 4
		target := uint32(int32(addr+4) + sext12(uint32(op&0x0FFF))*2)
		return c.delayed(addr, target, 2)
	case 0x0C:
		return c.execC(op, addr)
	case 0x0D:
		// MOV.L @(disp, PC), Rn
		base := (addr + 4) &^ 3
		src := base + uint32(op&0xFF)*4
		c.Reg[n] = c.rd32(src)
		return 2, false
	case 0x0E:
		// MOV #imm, Rn
		c.Reg[n] = sext8(uint32(op & 0xFF))
		return 1, false
	}
	// Top nibble 0x0F is the floating point and SH-4 only
	// space: every encoding there is illegal on SH-2.
	return c.illegal(addr)
}

func (c *CPU) exec0(op uint16, addr uint32, n, m int, low uint32) (int, bool) {
	mid := uint32((op >> 4) & 0x0F)
	switch {
	case op == 0x0008:
		c.setT(0)
		return 1, false
	case op == 0x0018:
		c.setT(1)
		return 1, false
	case op == 0x0028:
		c.MACH = 0
		c.MACL = 0
		return 1, false
	case op == 0x0019:
		// DIV0U
		c.setQM(0, 0)
		c.setT(0)
		return 1, false
	case op == 0x000B:
		// RTS
		return c.delayed(addr, c.PR, 2)
	case op == 0x001B:
		// SLEEP
		c.Sleeping = true
		return 1, false
	case op == 0x002B:
		// RTE: pop the saved PC and SR, run the delay
		// slot, then restore PC and SR from the stack.
		savedPC := c.bus.Read32(c.Reg[15])
		savedSR := c.bus.Read32(c.Reg[15] + 4)
		c.Reg[15] += 8
		if _, bad := c.execSlot(addr); bad {
			return 2, true
		}
		c.PC = savedPC
		c.SR = savedSR
		return 2, false
	case low == 9 && mid == 2:
		// MOVT Rn: R0's encoding 0x0029 carries the
		// constant 2 in the middle nibble. The all zero
		// middle nibble form 0x0009 is NOP and changes no
		// register; an earlier slice treated it as MOVT
		// R0, which corrupts R0 across every NOP delay
		// slot and breaks real code, so SAT-4 corrects it
		// to the manual.
		c.Reg[n] = uint32(c.getT())
		return 1, false
	case op == 0x0009:
		// NOP
		return 1, false
	case low == 3 && mid == 0:
		// BSRF Rn
		target := c.Reg[n]
		c.PR = addr + 4
		return c.delayed(addr, target, 2)
	case low == 3 && mid == 2:
		// BRAF Rn
		return c.delayed(addr, c.Reg[n], 2)
	case low == 7:
		// MUL.L Rm, Rn
		c.MACL = uint32(int32(c.Reg[n]) * int32(c.Reg[m]))
		return 2, false
	case low == 15:
		// MAC.L @Rm+, @Rn+: both pointers advance by
		// 4; when m equals n the same register advances
		// twice and the second operand comes from the
		// advanced address. Both operands are read, with
		// their alignment checked, before either pointer
		// moves, so a bad access aborts cleanly.
		addrM := c.Reg[m]
		a := int32(c.rd32(addrM))
		addrN := c.Reg[n]
		if n == m {
			addrN = addrM + 4
		}
		b := int32(c.rd32(addrN))
		c.Reg[m] += 4
		c.Reg[n] += 4
		c.macAdd(int64(a) * int64(b))
		return 3, false
	case low == 4 || low == 5 || low == 6:
		// MOV Rm, @(R0, Rn), byte word long by low.
		size := sizeOf(low)
		dst := c.Reg[n] + c.Reg[0]
		c.store(dst, c.Reg[m], size)
		return 2, false
	case low == 12 || low == 13 || low == 14:
		// MOV @(R0, Rm), Rn, byte word long by low.
		size := sizeOf(low)
		src := c.Reg[m] + c.Reg[0]
		c.Reg[n] = c.loadSigned(src, size)
		return 2, false
	case low == 2 && mid <= 2:
		// STC: selector in mid, 0 SR, 1 GBR, 2 VBR.
		c.Reg[n] = c.stcValue(mid)
		return 1, false
	case low == 10 && mid <= 2:
		// STS: selector in mid, 0 MACH, 1 MACL, 2 PR.
		c.Reg[n] = c.stsValue(mid)
		return 1, false
	}
	return c.illegal(addr)
}

func (c *CPU) exec2(addr uint32, n, m int, low uint32) (int, bool) {
	switch low {
	case 0, 1, 2:
		// MOV Rm, @Rn
		size := sizeOf(low)
		c.store(c.Reg[n], c.Reg[m], size)
		return 2, false
	case 4, 5, 6:
		// MOV Rm, @-Rn: the address register moves
		// first, the store follows, and the alignment
		// of the new address is checked before either
		// change so a bad access aborts cleanly.
		size := sizeOf(low)
		dst := c.Reg[n] - uint32(size)
		c.checkAlign(dst, size)
		c.Reg[n] = dst
		c.store(dst, c.Reg[m], size)
		return 2, false
	case 7:
		// DIV0S Rm, Rn
		q := 0
		if c.Reg[n]>>31 != 0 {
			q = 1
		}
		mv := 0
		if c.Reg[m]>>31 != 0 {
			mv = 1
		}
		c.setQM(q, mv)
		c.setT(q ^ mv)
		return 1, false
	case 8:
		c.setT(b2u(c.Reg[n]&c.Reg[m] == 0))
		return 1, false
	case 9:
		c.Reg[n] &= c.Reg[m]
		return 1, false
	case 10:
		c.Reg[n] ^= c.Reg[m]
		return 1, false
	case 11:
		c.Reg[n] |= c.Reg[m]
		return 1, false
	case 12:
		// CMP/STR: T set when any byte pair matches.
		a := c.Reg[n]
		b := c.Reg[m]
		hit := false
		for i := 0; i < 4; i += 1 {
			if (a>>(uint(i)*8))&0xFF == (b>>(uint(i)*8))&0xFF {
				hit = true
			}
		}
		c.setT(b2u(hit))
		return 1, false
	case 13:
		// XTRCT Rm, Rn
		c.Reg[n] = (c.Reg[m]&0x0000FFFF)<<16 | (c.Reg[n] >> 16)
		return 1, false
	case 14:
		// MULU.W Rm, Rn
		c.MACL = uint32(c.Reg[n]&0x0000FFFF) * uint32(c.Reg[m]&0x0000FFFF)
		return 1, false
	case 15:
		// MULS.W Rm, Rn
		c.MACL = uint32(int32(int16(c.Reg[n])) * int32(int16(c.Reg[m])))
		return 1, false
	}
	return c.illegal(addr)
}

func (c *CPU) exec3(addr uint32, n, m int, low uint32) (int, bool) {
	a := c.Reg[n]
	b := c.Reg[m]
	switch low {
	case 0:
		c.setT(b2u(a == b))
	case 2:
		c.setT(b2u(a >= b))
	case 3:
		c.setT(b2u(int32(a) >= int32(b)))
	case 4:
		c.div1(m, n)
	case 5:
		p := uint64(a) * uint64(b)
		c.MACH = uint32(p >> 32)
		c.MACL = uint32(p)
		return 2, false
	case 6:
		c.setT(b2u(a > b))
	case 7:
		c.setT(b2u(int32(a) > int32(b)))
	case 8:
		c.Reg[n] = a - b
	case 10:
		t := uint32(c.getT())
		res := a - b - t
		var borrow uint32
		if uint64(a) < uint64(b)+uint64(t) {
			borrow = 1
		}
		c.Reg[n] = res
		c.setT(int(borrow))
	case 11:
		res := a - b
		over := false
		if (a^b)&0x80000000 != 0 && (res^a)&0x80000000 != 0 {
			over = true
		}
		c.Reg[n] = res
		c.setT(b2u(over))
	case 12:
		c.Reg[n] = a + b
	case 13:
		p := int64(int32(a)) * int64(int32(b))
		c.MACH = uint32(uint64(p) >> 32)
		c.MACL = uint32(p)
		return 2, false
	case 14:
		t := uint32(c.getT())
		sum := uint64(a) + uint64(b) + uint64(t)
		c.Reg[n] = uint32(sum)
		c.setT(int(uint32(sum >> 32)))
	case 15:
		res := a + b
		over := false
		if (a^b)&0x80000000 == 0 && (res^a)&0x80000000 != 0 {
			over = true
		}
		c.Reg[n] = res
		c.setT(b2u(over))
	default:
		// Low nibbles 1 and 9 are not SH-2 instructions.
		return c.illegal(addr)
	}
	return 1, false
}
