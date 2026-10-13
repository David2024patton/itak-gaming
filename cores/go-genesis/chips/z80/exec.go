package z80

// execute fetches and runs one instruction, including any prefix
// bytes, and returns its T-state cost.
func (c *CPU) execute() int {
	op := c.fetchOpcode()

	// DD/FD prefix handling: the last prefix seen wins; every prefix
	// before it is a wasted 4 T-state M1 cycle, as on hardware.
	prefix := byte(0)
	extra := 0
	for op == 0xDD || op == 0xFD {
		if prefix != 0 {
			extra += 4
		}
		prefix = op
		op = c.fetchOpcode()
	}

	if prefix != 0 {
		idx := &c.ix
		if prefix == 0xFD {
			idx = &c.iy
		}
		switch {
		case op == 0xCB:
			return c.execIndexedCB(idx) + extra
		case op == 0xED:
			// An index prefix in front of an ED instruction is
			// simply wasted.
			return c.execED() + extra + 4
		default:
			return c.execIndexed(op, idx) + extra
		}
	}
	return c.execOne(op)
}

// execOne dispatches a single already-fetched opcode byte with no
// index prefix in effect.
func (c *CPU) execOne(op byte) int {
	switch op {
	case 0xCB:
		return c.execCB()
	case 0xED:
		return c.execED()
	default:
		return c.execBase(op)
	}
}

// execBase executes one unprefixed opcode (not CB/ED/DD/FD) and
// returns its documented T-state cost.
func (c *CPU) execBase(op byte) int {
	// Pattern-coded groups first.
	switch {
	case op&0xCF == 0x01: // LD rr,nn
		c.setPair(int(op>>4)&3, c.operand16())
		return 10
	case op&0xCF == 0x03: // INC rr
		n := int(op>>4) & 3
		c.setPair(n, c.pair(n)+1)
		return 6
	case op&0xCF == 0x0B: // DEC rr
		n := int(op>>4) & 3
		c.setPair(n, c.pair(n)-1)
		return 6
	case op&0xCF == 0x09: // ADD HL,rr
		c.setHL(c.add16(c.hl(), c.pair(int(op>>4)&3)))
		return 11
	case op&0xC7 == 0x04: // INC r / INC (HL)
		if idx := int(op>>3) & 7; idx == 6 {
			hl := c.hl()
			c.bus.Write(hl, c.inc8(c.bus.Read(hl)))
			return 11
		} else {
			c.setReg(idx, c.inc8(c.reg(idx)))
			return 4
		}
	case op&0xC7 == 0x05: // DEC r / DEC (HL)
		if idx := int(op>>3) & 7; idx == 6 {
			hl := c.hl()
			c.bus.Write(hl, c.dec8(c.bus.Read(hl)))
			return 11
		} else {
			c.setReg(idx, c.dec8(c.reg(idx)))
			return 4
		}
	case op&0xC7 == 0x06: // LD r,n / LD (HL),n
		v := c.operand()
		if idx := int(op>>3) & 7; idx == 6 {
			c.bus.Write(c.hl(), v)
			return 10
		} else {
			c.setReg(idx, v)
			return 7
		}
	case op == 0x76: // HALT (the (HL),(HL) slot of the LD matrix)
		c.halted = true
		return 4
	case op >= 0x40 && op <= 0x7F: // LD r,r'
		dst, src := int(op>>3)&7, int(op)&7
		var v uint8
		if src == 6 {
			v = c.bus.Read(c.hl())
		} else {
			v = c.reg(src)
		}
		if dst == 6 {
			c.bus.Write(c.hl(), v)
		} else {
			c.setReg(dst, v)
		}
		if src == 6 || dst == 6 {
			return 7
		}
		return 4
	case op >= 0x80 && op <= 0xBF: // ALU A,r / ALU A,(HL)
		src := int(op) & 7
		if src == 6 {
			c.alu(int(op>>3)&7, c.bus.Read(c.hl()))
			return 7
		}
		c.alu(int(op>>3)&7, c.reg(src))
		return 4
	case op&0xC7 == 0xC0: // RET cc
		if c.cond(int(op>>3) & 7) {
			c.pc = c.pop()
			return 11
		}
		return 5
	case op&0xCF == 0xC1: // POP rr
		c.setStackPair(int(op>>4)&3, c.pop())
		return 10
	case op&0xC7 == 0xC2: // JP cc,nn
		addr := c.operand16()
		if c.cond(int(op>>3) & 7) {
			c.pc = addr
		}
		return 10
	case op&0xC7 == 0xC4: // CALL cc,nn
		addr := c.operand16()
		if c.cond(int(op>>3) & 7) {
			c.push(c.pc)
			c.pc = addr
			return 17
		}
		return 10
	case op&0xCF == 0xC5: // PUSH rr
		c.push(c.stackPair(int(op>>4) & 3))
		return 11
	case op&0xC7 == 0xC6: // ALU A,n
		c.alu(int(op>>3)&7, c.operand())
		return 7
	case op&0xC7 == 0xC7: // RST p
		c.push(c.pc)
		c.pc = uint16(op & 0x38)
		return 11
	}

	// Discrete opcodes.
	switch op {
	case 0x00: // NOP
		return 4
	case 0x02: // LD (BC),A
		c.bus.Write(c.bc(), c.a)
		return 7
	case 0x12: // LD (DE),A
		c.bus.Write(c.de(), c.a)
		return 7
	case 0x0A: // LD A,(BC)
		c.a = c.bus.Read(c.bc())
		return 7
	case 0x1A: // LD A,(DE)
		c.a = c.bus.Read(c.de())
		return 7
	case 0x07: // RLCA
		out := c.a >> 7
		c.a = c.a<<1 | out
		c.f = c.f&(flagS|flagZ|flagPV) | out | (c.a & (flagY | flagX))
		return 4
	case 0x0F: // RRCA
		out := c.a & 1
		c.a = c.a>>1 | out<<7
		c.f = c.f&(flagS|flagZ|flagPV) | out | (c.a & (flagY | flagX))
		return 4
	case 0x17: // RLA
		out := c.a >> 7
		c.a = c.a<<1 | (c.f & flagC)
		c.f = c.f&(flagS|flagZ|flagPV) | out | (c.a & (flagY | flagX))
		return 4
	case 0x1F: // RRA
		out := c.a & 1
		c.a = c.a>>1 | (c.f&flagC)<<7
		c.f = c.f&(flagS|flagZ|flagPV) | out | (c.a & (flagY | flagX))
		return 4
	case 0x08: // EX AF,AF'
		c.a, c.a2 = c.a2, c.a
		c.f, c.f2 = c.f2, c.f
		return 4
	case 0x10: // DJNZ e
		off := int8(c.operand())
		c.b--
		if c.b != 0 {
			c.pc = uint16(int32(c.pc) + int32(off))
			return 13
		}
		return 8
	case 0x18: // JR e
		off := int8(c.operand())
		c.pc = uint16(int32(c.pc) + int32(off))
		return 12
	case 0x20, 0x28, 0x30, 0x38: // JR NZ/Z/NC/C,e
		off := int8(c.operand())
		cc := int(op>>3) & 3 // NZ, Z, NC, C are conditions 0..3
		if c.cond(cc) {
			c.pc = uint16(int32(c.pc) + int32(off))
			return 12
		}
		return 7
	case 0x22: // LD (nn),HL
		c.write16(c.operand16(), c.hl())
		return 16
	case 0x2A: // LD HL,(nn)
		c.setHL(c.read16(c.operand16()))
		return 16
	case 0x32: // LD (nn),A
		c.bus.Write(c.operand16(), c.a)
		return 13
	case 0x3A: // LD A,(nn)
		c.a = c.bus.Read(c.operand16())
		return 13
	case 0x27: // DAA
		c.daa()
		return 4
	case 0x2F: // CPL
		c.a = ^c.a
		c.f |= flagH | flagN
		c.f = c.f&^(flagY|flagX) | (c.a & (flagY | flagX))
		return 4
	case 0x37: // SCF
		// Y/X come from A OR the previous F (the documented quirk the
		// z80test research established for SCF/CCF; found by differential
		// testing, where copying from A alone diverged).
		c.f = c.f&(flagS|flagZ|flagPV) | flagC | ((c.f | c.a) & (flagY | flagX))
		return 4
	case 0x3F: // CCF
		oldC := c.f & flagC
		f := c.f&(flagS|flagZ|flagPV) | ((c.f | c.a) & (flagY | flagX))
		if oldC != 0 {
			f |= flagH // the old carry becomes the half carry
		} else {
			f |= flagC
		}
		c.f = f
		return 4
	case 0xC3: // JP nn
		c.pc = c.operand16()
		return 10
	case 0xC9: // RET
		c.pc = c.pop()
		return 10
	case 0xCD: // CALL nn
		addr := c.operand16()
		c.push(c.pc)
		c.pc = addr
		return 17
	case 0xD3: // OUT (n),A
		c.bus.Out(uint16(c.a)<<8|uint16(c.operand()), c.a)
		return 11
	case 0xDB: // IN A,(n)
		c.a = c.bus.In(uint16(c.a)<<8 | uint16(c.operand()))
		return 11
	case 0xD9: // EXX
		c.b, c.b2 = c.b2, c.b
		c.c, c.c2 = c.c2, c.c
		c.d, c.d2 = c.d2, c.d
		c.e, c.e2 = c.e2, c.e
		c.h, c.h2 = c.h2, c.h
		c.l, c.l2 = c.l2, c.l
		return 4
	case 0xE3: // EX (SP),HL
		v := c.read16(c.sp)
		c.write16(c.sp, c.hl())
		c.setHL(v)
		return 19
	case 0xE9: // JP (HL)
		c.pc = c.hl()
		return 4
	case 0xEB: // EX DE,HL
		c.d, c.h = c.h, c.d
		c.e, c.l = c.l, c.e
		return 4
	case 0xF3: // DI
		c.iff1, c.iff2 = false, false
		c.inhibitInt = false
		return 4
	case 0xF9: // LD SP,HL
		c.sp = c.hl()
		return 6
	case 0xFB: // EI
		c.iff1, c.iff2 = true, true
		c.inhibitInt = true
		return 4
	}
	// Any opcode not decoded above cannot occur: the pattern groups
	// and the discrete table cover the whole base map. Treat a miss
	// as a NOP rather than crashing the machine.
	return 4
}
