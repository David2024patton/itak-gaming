package sm83

// execute fetches and runs one instruction and returns its length in
// M-cycles. Branch instructions return the taken or not-taken cost as
// documented. The eleven unused opcodes (D3, DB, DD, E3, E4, EB, EC,
// ED, F4, FC, FD) jam the real hardware; here they execute as a
// one-cycle NOP so a stray jump cannot wedge the test rig. No
// conformance ROM executes them deliberately.
func (c *CPU) execute(bus Bus) int {
	op := c.fetchOp(bus)

	// LD r, r' block, with HALT at 0x76.
	if op >= 0x40 && op <= 0x7F {
		if op == 0x76 {
			return c.execHalt(bus)
		}
		dst := int(op>>3) & 7
		src := int(op) & 7
		c.setR(bus, dst, c.getR(bus, src))
		if dst == 6 || src == 6 {
			return 2
		}
		return 1
	}
	// ALU A, r block.
	if op >= 0x80 && op <= 0xBF {
		idx := int(op>>3) & 7
		src := int(op) & 7
		c.alu(idx, c.getR(bus, src))
		if src == 6 {
			return 2
		}
		return 1
	}

	switch {
	case op&0xC7 == 0x04: // INC r
		i := int(op>>3) & 7
		c.setR(bus, i, c.inc8(c.getR(bus, i)))
		if i == 6 {
			return 3
		}
		return 1
	case op&0xC7 == 0x05: // DEC r
		i := int(op>>3) & 7
		c.setR(bus, i, c.dec8(c.getR(bus, i)))
		if i == 6 {
			return 3
		}
		return 1
	case op&0xC7 == 0x06: // LD r, d8
		i := int(op>>3) & 7
		c.setR(bus, i, c.fetch8(bus))
		if i == 6 {
			return 3
		}
		return 2
	case op&0xCF == 0x01: // LD rp, d16
		c.setRP(int(op>>4)&3, c.fetch16(bus))
		return 3
	case op&0xCF == 0x03: // INC rp
		i := int(op>>4) & 3
		c.setRP(i, c.getRP(i)+1)
		return 2
	case op&0xCF == 0x0B: // DEC rp
		i := int(op>>4) & 3
		c.setRP(i, c.getRP(i)-1)
		return 2
	case op&0xCF == 0x09: // ADD HL, rp
		c.addHL(c.getRP(int(op>>4) & 3))
		return 2
	case op&0xE7 == 0x20: // JR cc, r8
		e := int8(c.fetch8(bus))
		if c.cond(int(op>>3) & 3) {
			c.pc = uint16(int32(int16(c.pc)) + int32(e))
			return 3
		}
		return 2
	case op&0xE7 == 0xC0: // RET cc (bit 5 is part of the pattern:
		// without it the LDH and ADD SP opcodes alias here)
		if c.cond(int(op>>3) & 3) {
			c.pc = c.pop16(bus)
			return 5
		}
		return 2
	case op&0xE7 == 0xC2: // JP cc, a16
		addr := c.fetch16(bus)
		if c.cond(int(op>>3) & 3) {
			c.pc = addr
			return 4
		}
		return 3
	case op&0xE7 == 0xC4: // CALL cc, a16
		addr := c.fetch16(bus)
		if c.cond(int(op>>3) & 3) {
			c.push16(bus, c.pc)
			c.pc = addr
			return 6
		}
		return 3
	case op&0xCF == 0xC1: // POP rp2
		c.setRP2(int(op>>4)&3, c.pop16(bus))
		return 3
	case op&0xCF == 0xC5: // PUSH rp2
		c.push16(bus, c.getRP2(int(op>>4)&3))
		return 4
	case op&0xC7 == 0xC6: // ALU A, d8
		c.alu(int(op>>3)&7, c.fetch8(bus))
		return 2
	case op&0xC7 == 0xC7: // RST vec
		c.push16(bus, c.pc)
		c.pc = uint16(op & 0x38)
		return 4
	}

	switch op {
	case 0x00: // NOP
		return 1
	case 0x02: // LD (BC), A
		bus.Write(c.bc(), c.a)
		return 2
	case 0x0A: // LD A, (BC)
		c.a = bus.Read(c.bc())
		return 2
	case 0x12: // LD (DE), A
		bus.Write(c.de(), c.a)
		return 2
	case 0x1A: // LD A, (DE)
		c.a = bus.Read(c.de())
		return 2
	case 0x08: // LD (a16), SP
		addr := c.fetch16(bus)
		bus.Write(addr, uint8(c.sp))
		bus.Write(addr+1, uint8(c.sp>>8))
		return 5
	case 0x07: // RLCA: zero flag always clears on the SM83 rotates
		cy := c.a >> 7
		c.a = c.a<<1 | cy
		c.setFlags(false, false, false, cy != 0)
		return 1
	case 0x0F: // RRCA
		cy := c.a & 1
		c.a = c.a>>1 | cy<<7
		c.setFlags(false, false, false, cy != 0)
		return 1
	case 0x17: // RLA
		var in uint8
		if c.f&flagC != 0 {
			in = 1
		}
		cy := c.a >> 7
		c.a = c.a<<1 | in
		c.setFlags(false, false, false, cy != 0)
		return 1
	case 0x1F: // RRA
		var in uint8
		if c.f&flagC != 0 {
			in = 0x80
		}
		cy := c.a & 1
		c.a = c.a>>1 | in
		c.setFlags(false, false, false, cy != 0)
		return 1
	case 0x10: // STOP: two-byte instruction. The hardware checks the
		// joypad wake condition before stopping: if any input line
		// already reads low (a button is pressed), STOP is skipped
		// and execution continues. Otherwise the CPU enters the
		// stopped state and Step idles until the machine wakes it.
		// This matters in practice: cartridge code that probes for
		// a CGB speed switch on plain DMG hardware reaches STOP
		// with the joypad lines in the pressed state.
		c.fetch8(bus) // padding byte
		if bus.Read(0xFF00)&0x0F == 0x0F {
			c.stopped = true
		}
		return 1
	case 0x18: // JR r8
		e := int8(c.fetch8(bus))
		c.pc = uint16(int32(int16(c.pc)) + int32(e))
		return 3
	case 0x22: // LD (HL+), A
		bus.Write(c.hl(), c.a)
		c.setHL(c.hl() + 1)
		return 2
	case 0x2A: // LD A, (HL+)
		c.a = bus.Read(c.hl())
		c.setHL(c.hl() + 1)
		return 2
	case 0x32: // LD (HL-), A
		bus.Write(c.hl(), c.a)
		c.setHL(c.hl() - 1)
		return 2
	case 0x3A: // LD A, (HL-)
		c.a = bus.Read(c.hl())
		c.setHL(c.hl() - 1)
		return 2
	case 0x27: // DAA
		c.daa()
		return 1
	case 0x2F: // CPL
		c.a = ^c.a
		c.f = (c.f & (flagZ | flagC)) | flagN | flagH
		return 1
	case 0x37: // SCF
		c.f = (c.f & flagZ) | flagC
		return 1
	case 0x3F: // CCF
		var cy uint8
		if c.f&flagC == 0 {
			cy = flagC
		}
		c.f = (c.f & flagZ) | cy
		return 1
	case 0xC3: // JP a16
		c.pc = c.fetch16(bus)
		return 4
	case 0xC9: // RET
		c.pc = c.pop16(bus)
		return 4
	case 0xCB: // CB prefix
		return c.executeCB(bus, c.fetch8(bus))
	case 0xCD: // CALL a16
		addr := c.fetch16(bus)
		c.push16(bus, c.pc)
		c.pc = addr
		return 6
	case 0xD9: // RETI: interrupts enable immediately, unlike EI
		c.pc = c.pop16(bus)
		c.ime = true
		c.eiDelay = 0
		return 4
	case 0xE0: // LDH (a8), A
		bus.Write(0xFF00+uint16(c.fetch8(bus)), c.a)
		return 3
	case 0xE2: // LD (C), A
		bus.Write(0xFF00+uint16(c.c), c.a)
		return 2
	case 0xE8: // ADD SP, r8
		c.sp = c.addSPe8(c.fetch8(bus))
		return 4
	case 0xE9: // JP (HL)
		c.pc = c.hl()
		return 1
	case 0xEA: // LD (a16), A
		bus.Write(c.fetch16(bus), c.a)
		return 4
	case 0xF0: // LDH A, (a8)
		c.a = bus.Read(0xFF00 + uint16(c.fetch8(bus)))
		return 3
	case 0xF2: // LD A, (C)
		c.a = bus.Read(0xFF00 + uint16(c.c))
		return 2
	case 0xF3: // DI
		c.ime = false
		c.eiDelay = 0
		return 1
	case 0xF8: // LD HL, SP+r8
		c.setHL(c.addSPe8(c.fetch8(bus)))
		return 3
	case 0xF9: // LD SP, HL
		c.sp = c.hl()
		return 2
	case 0xFA: // LD A, (a16)
		c.a = bus.Read(c.fetch16(bus))
		return 4
	case 0xFB: // EI: IME takes effect after the next instruction.
		// The enable is scheduled once: a second EI executed while
		// an enable is already pending does not postpone it (the
		// mooneye ei_sequence test pins this down: in a run of EIs,
		// the interrupt lands after the second one).
		if c.eiDelay == 0 {
			c.eiDelay = 2
		}
		return 1
	}
	// Unused opcodes: D3, DB, DD, E3, E4, EB, EC, ED, F4, FC, FD.
	return 1
}

// execHalt runs the HALT instruction. With an interrupt pending and
// IME clear the CPU does not halt; instead the HALT bug arms, and the
// next opcode fetch leaves PC in place so the following byte is read
// twice. With nothing pending the CPU halts until a request arrives.
func (c *CPU) execHalt(bus Bus) int {
	if c.pendingInterrupts(bus) != 0 && !c.ime {
		c.haltBug = true
		return 1
	}
	c.halted = true
	return 1
}
