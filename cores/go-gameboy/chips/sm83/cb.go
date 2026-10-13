package sm83

// executeCB runs one CB-prefixed instruction. The opcode splits into
// x (bits 7-6: operation class), y (bits 5-3: operation or bit
// index), and z (bits 2-0: operand register, 6 means (HL)).
//
// Cycles: register operands cost 2 M-cycles, (HL) operands cost 4,
// except BIT with (HL) which costs 3 because it only reads.
func (c *CPU) executeCB(bus Bus, op uint8) int {
	x := op >> 6
	y := int(op>>3) & 7
	z := int(op) & 7

	switch x {
	case 0: // rotates and shifts
		v := c.getR(bus, z)
		res, cy := c.cbShift(y, v)
		c.setR(bus, z, res)
		c.setFlags(res == 0, false, false, cy)
		if z == 6 {
			return 4
		}
		return 2
	case 1: // BIT: Z reports the bit is zero, N clears, H sets,
		// C is preserved (handled by not touching it below).
		v := c.getR(bus, z)
		cy := c.f&flagC != 0
		c.setFlags(v>>uint(y)&1 == 0, false, true, cy)
		if z == 6 {
			return 3
		}
		return 2
	case 2: // RES: no flags change
		c.setR(bus, z, c.getR(bus, z)&^(1<<uint(y)))
		if z == 6 {
			return 4
		}
		return 2
	default: // SET: no flags change
		c.setR(bus, z, c.getR(bus, z)|(1<<uint(y)))
		if z == 6 {
			return 4
		}
		return 2
	}
}

// cbShift performs the eight CB rotate and shift operations selected
// by y: 0 RLC, 1 RRC, 2 RL, 3 RR, 4 SLA, 5 SRA, 6 SWAP, 7 SRL.
// It returns the result and the bit that lands in the carry flag;
// SWAP reports no carry.
func (c *CPU) cbShift(y int, v uint8) (uint8, bool) {
	oldC := uint8(0)
	if c.f&flagC != 0 {
		oldC = 1
	}
	switch y {
	case 0: // RLC
		return v<<1 | v>>7, v>>7 != 0
	case 1: // RRC
		return v>>1 | v<<7, v&1 != 0
	case 2: // RL: through carry
		return v<<1 | oldC, v>>7 != 0
	case 3: // RR: through carry
		return v>>1 | oldC<<7, v&1 != 0
	case 4: // SLA
		return v << 1, v>>7 != 0
	case 5: // SRA: sign bit preserved
		return v>>1 | v&0x80, v&1 != 0
	case 6: // SWAP nibbles
		return v<<4 | v>>4, false
	default: // SRL
		return v >> 1, v&1 != 0
	}
}
