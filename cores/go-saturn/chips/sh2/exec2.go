package sh2

func (c *CPU) exec4(op uint16, addr uint32, n, m int) (int, bool) {
	lowByte := uint32(op & 0xFF)
	if lowByte&0x0F == 0x0F {
		// MAC.W @Rm+, @Rn+: both pointers advance by
		// 2; when m equals n the same register advances
		// twice and the second operand comes from the
		// advanced address.
		addrM := c.Reg[m]
		a := int32(int16(c.rd16(addrM)))
		addrN := c.Reg[n]
		if n == m {
			addrN = addrM + 2
			c.checkAlign(addrN, 2)
		}
		b := int32(int16(c.rd16(addrN)))
		c.Reg[m] += 2
		c.Reg[n] += 2
		c.macAdd(int64(a) * int64(b))
		return 3, false
	}
	switch lowByte {
	case 0x00:
		// SHLL Rn
		c.setT(int(c.Reg[n] >> 31))
		c.Reg[n] <<= 1
	case 0x10:
		// DT Rn
		c.Reg[n] -= 1
		c.setT(b2u(c.Reg[n] == 0))
	case 0x20:
		// SHAL Rn, same operation as SHLL.
		c.setT(int(c.Reg[n] >> 31))
		c.Reg[n] <<= 1
	case 0x01:
		// SHLR Rn
		c.setT(int(c.Reg[n] & 1))
		c.Reg[n] >>= 1
	case 0x11:
		// CMP/PZ Rn
		c.setT(b2u(int32(c.Reg[n]) >= 0))
	case 0x21:
		// SHAR Rn
		c.setT(int(c.Reg[n] & 1))
		c.Reg[n] = uint32(int32(c.Reg[n]) >> 1)
	case 0x04:
		// ROTL Rn
		bit := c.Reg[n] >> 31
		c.setT(int(bit))
		c.Reg[n] = c.Reg[n]<<1 | bit
	case 0x24:
		// ROTCL Rn
		bit := c.Reg[n] >> 31
		old := uint32(c.getT())
		c.Reg[n] = c.Reg[n]<<1 | old
		c.setT(int(bit))
	case 0x05:
		// ROTR Rn
		bit := c.Reg[n] & 1
		c.setT(int(bit))
		c.Reg[n] = c.Reg[n]>>1 | bit<<31
	case 0x15:
		// CMP/PL Rn
		c.setT(b2u(int32(c.Reg[n]) > 0))
	case 0x25:
		// ROTCR Rn
		bit := c.Reg[n] & 1
		old := uint32(c.getT())
		c.Reg[n] = c.Reg[n]>>1 | old<<31
		c.setT(int(bit))
	case 0x08:
		c.Reg[n] <<= 2
	case 0x18:
		c.Reg[n] <<= 8
	case 0x28:
		c.Reg[n] <<= 16
	case 0x09:
		c.Reg[n] >>= 2
	case 0x19:
		c.Reg[n] >>= 8
	case 0x29:
		c.Reg[n] >>= 16
	case 0x0B:
		// JSR @Rn
		target := c.Reg[n]
		c.PR = addr + 4
		return c.delayed(addr, target, 2)
	case 0x2B:
		// JMP @Rn
		return c.delayed(addr, c.Reg[n], 2)
	case 0x1B:
		// TAS.B @Rn
		dst := c.Reg[n]
		v := c.rd8(dst)
		c.setT(b2u(v == 0))
		c.wr8(dst, v|0x80)
		return 2, false
	case 0x02, 0x12, 0x22:
		// STS.L sys, @-Rn
		sel := lowByte >> 4
		dst := c.Reg[n] - 4
		c.checkAlign(dst, 4)
		c.wr32(dst, c.stsValue(sel))
		c.Reg[n] = dst
		return 2, false
	case 0x03, 0x13, 0x23:
		// STC.L sys, @-Rn
		sel := lowByte >> 4
		dst := c.Reg[n] - 4
		c.checkAlign(dst, 4)
		c.wr32(dst, c.stcValue(sel))
		c.Reg[n] = dst
		return 2, false
	case 0x06, 0x16, 0x26:
		// LDS.L @Rn+, sys
		sel := lowByte >> 4
		v := c.rd32(c.Reg[n])
		c.Reg[n] += 4
		c.ldsValue(sel, v)
		return 2, false
	case 0x07, 0x17, 0x27:
		// LDC.L @Rn+, sys; an SR load applies at once.
		sel := lowByte >> 4
		v := c.rd32(c.Reg[n])
		c.Reg[n] += 4
		c.ldcValue(sel, v)
		return 2, false
	case 0x0A, 0x1A, 0x2A:
		// LDS Rn, sys
		sel := lowByte >> 4
		c.ldsValue(sel, c.Reg[n])
		return 1, false
	case 0x0E, 0x1E, 0x2E:
		// LDC Rn, sys; an SR load applies at once.
		sel := lowByte >> 4
		c.ldcValue(sel, c.Reg[n])
		return 1, false
	default:
		return c.illegal(addr)
	}
	return 1, false
}

func (c *CPU) exec6(addr uint32, n, m int, low uint32) (int, bool) {
	switch low {
	case 0, 1, 2:
		// MOV @Rm, Rn
		size := sizeOf(low)
		c.Reg[n] = c.loadSigned(c.Reg[m], size)
		return 2, false
	case 3:
		// MOV Rm, Rn
		c.Reg[n] = c.Reg[m]
		return 1, false
	case 4, 5, 6:
		// MOV @Rm+, Rn: destination is loaded first,
		// then the source pointer advances, matching
		// the manual operation order.
		size := sizeOf(low)
		v := c.loadSigned(c.Reg[m], size)
		c.Reg[n] = v
		c.Reg[m] += uint32(size)
		return 2, false
	case 8:
		// SWAP.B Rm, Rn
		v := c.Reg[m]
		c.Reg[n] = v&0xFFFF0000 | (v&0x000000FF)<<8 | (v>>8)&0x000000FF
		return 1, false
	case 9:
		// SWAP.W Rm, Rn
		v := c.Reg[m]
		c.Reg[n] = v<<16 | v>>16
		return 1, false
	case 7:
		// NOT Rm, Rn
		c.Reg[n] = ^c.Reg[m]
		return 1, false
	case 0x0A:
		// NEGC Rm, Rn: Rn = 0 - Rm - T, and T takes
		// the borrow, set unless Rm and T are both 0.
		tbit := uint32(c.getT())
		borrow := uint64(c.Reg[m]) + uint64(tbit)
		c.Reg[n] = 0 - c.Reg[m] - tbit
		c.setT(b2u(borrow != 0))
		return 1, false
	case 0x0B:
		// NEG Rm, Rn
		c.Reg[n] = 0 - c.Reg[m]
		return 1, false
	case 0x0C:
		// EXTU.B Rm, Rn
		c.Reg[n] = c.Reg[m] & 0xFF
		return 1, false
	case 0x0D:
		// EXTU.W Rm, Rn
		c.Reg[n] = c.Reg[m] & 0xFFFF
		return 1, false
	case 0x0E:
		// EXTS.B Rm, Rn
		c.Reg[n] = uint32(int32(int8(c.Reg[m])))
		return 1, false
	case 0x0F:
		// EXTS.W Rm, Rn
		c.Reg[n] = uint32(int32(int16(c.Reg[m])))
		return 1, false
	}
	return c.illegal(addr)
}

func (c *CPU) exec8(op uint16, addr uint32) (int, bool) {
	sub := (op >> 8) & 0x0F
	reg := int((op >> 4) & 0x0F)
	disp := uint32(op & 0x0F)
	imm := uint32(op & 0xFF)
	switch sub {
	case 0x00:
		// MOV.B R0, @(disp, Rn)
		c.store(c.Reg[reg]+disp, c.Reg[0], 1)
		return 2, false
	case 0x01:
		// MOV.W R0, @(disp, Rn)
		c.store(c.Reg[reg]+disp*2, c.Reg[0], 2)
		return 2, false
	case 0x04:
		// MOV.B @(disp, Rn), R0
		c.Reg[0] = c.loadSigned(c.Reg[reg]+disp, 1)
		return 2, false
	case 0x05:
		// MOV.W @(disp, Rn), R0
		c.Reg[0] = c.loadSigned(c.Reg[reg]+disp*2, 2)
		return 2, false
	case 0x08:
		// CMP/EQ #imm, R0
		c.setT(b2u(c.Reg[0] == sext8(imm)))
		return 1, false
	case 0x09, 0x0B:
		// BT, BF: non delayed conditional branches.
		target := uint32(int32(addr+4) + int32(int8(imm))*2)
		taken := (sub == 0x09 && c.getT() == 1) || (sub == 0x0B && c.getT() == 0)
		if taken {
			c.PC = target
			return 3, false
		}
		return 1, false
	case 0x0D, 0x0F:
		// BT/S, BF/S: delayed conditional branches. A
		// not taken form skips its delay slot entirely.
		target := uint32(int32(addr+4) + int32(int8(imm))*2)
		taken := (sub == 0x0D && c.getT() == 1) || (sub == 0x0F && c.getT() == 0)
		if taken {
			return c.delayed(addr, target, 2)
		}
		c.PC = addr + 4
		return 1, false
	}
	return c.illegal(addr)
}

func (c *CPU) execC(op uint16, addr uint32) (int, bool) {
	sub := (op >> 8) & 0x0F
	disp := uint32(op & 0xFF)
	imm := uint32(op & 0xFF)
	switch sub {
	case 0x00:
		c.store(c.GBR+disp, c.Reg[0], 1)
		return 2, false
	case 0x01:
		c.store(c.GBR+disp*2, c.Reg[0], 2)
		return 2, false
	case 0x02:
		c.store(c.GBR+disp*4, c.Reg[0], 4)
		return 2, false
	case 0x03:
		// TRAPA #imm. Rule used here: the trap number
		// is the immediate bits 7 to 2, and the vector
		// is 32 plus that number, so the handler pointer
		// is read at VBR plus vector times 4. TRAPA
		// with immediate 0x40 uses vector 48.
		vector := 32 + int((imm>>2)&0x3F)
		c.enterException(vector, addr+2)
		return 4, true
	case 0x04:
		c.Reg[0] = c.loadSigned(c.GBR+disp, 1)
		return 2, false
	case 0x05:
		c.Reg[0] = c.loadSigned(c.GBR+disp*2, 2)
		return 2, false
	case 0x06:
		c.Reg[0] = c.rd32(c.GBR + disp*4)
		return 2, false
	case 0x07:
		// MOVA @(disp, PC), R0
		base := (addr + 4) &^ 3
		c.Reg[0] = base + disp*4
		return 1, false
	case 0x08:
		// TST #imm, R0
		c.setT(b2u(c.Reg[0]&imm == 0))
		return 1, false
	case 0x09:
		c.Reg[0] &= imm
		return 1, false
	case 0x0A:
		c.Reg[0] ^= imm
		return 1, false
	case 0x0B:
		c.Reg[0] |= imm
		return 1, false
	case 0x0C:
		// TST.B #imm, @(R0, GBR)
		v := c.rd8(c.GBR + c.Reg[0])
		c.setT(b2u(uint32(v)&imm == 0))
		return 3, false
	case 0x0D:
		dst := c.GBR + c.Reg[0]
		c.wr8(dst, c.rd8(dst)&uint8(imm))
		return 3, false
	case 0x0E:
		dst := c.GBR + c.Reg[0]
		c.wr8(dst, c.rd8(dst)^uint8(imm))
		return 3, false
	case 0x0F:
		dst := c.GBR + c.Reg[0]
		c.wr8(dst, c.rd8(dst)|uint8(imm))
		return 3, false
	}
	return c.illegal(addr)
}
