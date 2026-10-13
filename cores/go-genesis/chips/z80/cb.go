package z80

// rotShift applies the CB rotate/shift operation selected by y
// (0=RLC, 1=RRC, 2=RL, 3=RR, 4=SLA, 5=SRA, 6=SLL, 7=SRL) to v,
// setting the shift group flags: S/Z/Y/X and parity from the
// result, H and N clear, C holding the bit shifted out.
func (c *CPU) rotShift(y int, v uint8) uint8 {
	oldC := c.f & flagC
	var res, out uint8
	switch y {
	case 0: // RLC
		out = v >> 7
		res = v<<1 | out
	case 1: // RRC
		out = v & 1
		res = v>>1 | out<<7
	case 2: // RL
		out = v >> 7
		res = v<<1 | oldC
	case 3: // RR
		out = v & 1
		res = v>>1 | oldC<<7
	case 4: // SLA
		out = v >> 7
		res = v << 1
	case 5: // SRA
		out = v & 1
		res = v>>1 | (v & 0x80)
	case 6: // SLL (documented undocumented shift: a 1 enters bit 0)
		out = v >> 7
		res = v<<1 | 1
	default: // SRL
		out = v & 1
		res = v >> 1
	}
	c.f = szpFlags(res) | out
	return res
}

// bitTest sets the BIT group flags for bit y of v. yxSource supplies
// the undocumented Y/X bits: the operand itself for register forms,
// or the high byte of the effective address (the WZ register) for
// memory forms. PV mirrors Z, S is set only for bit 7 when set,
// H is set, N clear, C preserved.
func (c *CPU) bitTest(y int, v uint8, yxSource uint8) {
	f := c.f&flagC | flagH
	if v&(1<<uint(y)) == 0 {
		f |= flagZ | flagPV
	}
	if y == 7 && v&0x80 != 0 {
		f |= flagS
	}
	f |= yxSource & (flagY | flagX)
	c.f = f
}

// execCB executes a CB-prefixed opcode. The opcode byte itself is
// fetched here (an M1 cycle, bumping R).
func (c *CPU) execCB() int {
	op := c.fetchOpcode()
	y := int(op>>3) & 7
	z := int(op) & 7
	switch op >> 6 {
	case 0: // rotates and shifts
		if z == 6 {
			hl := c.hl()
			c.bus.Write(hl, c.rotShift(y, c.bus.Read(hl)))
			return 15
		}
		c.setReg(z, c.rotShift(y, c.reg(z)))
		return 8
	case 1: // BIT
		if z == 6 {
			// For (HL), the Y/X bits come from the high byte of
			// the address (WZ = HL).
			c.bitTest(y, c.bus.Read(c.hl()), c.h)
			return 12
		}
		v := c.reg(z)
		c.bitTest(y, v, v)
		return 8
	case 2: // RES
		mask := ^(1 << uint(y))
		if z == 6 {
			hl := c.hl()
			c.bus.Write(hl, c.bus.Read(hl)&uint8(mask))
			return 15
		}
		c.setReg(z, c.reg(z)&uint8(mask))
		return 8
	default: // SET
		mask := uint8(1 << uint(y))
		if z == 6 {
			hl := c.hl()
			c.bus.Write(hl, c.bus.Read(hl)|mask)
			return 15
		}
		c.setReg(z, c.reg(z)|mask)
		return 8
	}
}

// execIndexedCB executes a DDCB/FDCB sequence: the displacement byte
// comes first (a plain read), then the opcode (an M1 cycle). For the
// shift and RES/SET groups, an operand field other than (HL) also
// copies the result into that register, as documented; BIT only
// tests the memory byte. Timing is 23 T-states, 20 for BIT.
func (c *CPU) execIndexedCB(idx *uint16) int {
	d := int8(c.operand())
	op := c.fetchOpcode()
	addr := uint16(int32(*idx) + int32(d))
	y := int(op>>3) & 7
	z := int(op) & 7
	switch op >> 6 {
	case 0: // rotates and shifts on (IX+d), with register copy
		res := c.rotShift(y, c.bus.Read(addr))
		c.bus.Write(addr, res)
		if z != 6 {
			c.setReg(z, res)
		}
		return 23
	case 1: // BIT: no write-back, no register copy
		c.bitTest(y, c.bus.Read(addr), uint8(addr>>8))
		return 20
	case 2: // RES
		res := c.bus.Read(addr) & ^(1 << uint(y))
		c.bus.Write(addr, res)
		if z != 6 {
			c.setReg(z, res)
		}
		return 23
	default: // SET
		res := c.bus.Read(addr) | (1 << uint(y))
		c.bus.Write(addr, res)
		if z != 6 {
			c.setReg(z, res)
		}
		return 23
	}
}
