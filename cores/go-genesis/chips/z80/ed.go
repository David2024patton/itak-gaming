package z80

// execED executes an ED-prefixed opcode. The opcode byte itself is
// fetched here (an M1 cycle, bumping R). Undefined ED opcodes are
// two-byte NOPs costing 8 T-states.
func (c *CPU) execED() int {
	op := c.fetchOpcode()

	// Block group first: exact opcodes in the 0xA0-0xBB range.
	switch op {
	case 0xA0: // LDI
		return c.blockMove(1, false)
	case 0xA1: // CPI
		return c.blockCompare(1, false)
	case 0xA2: // INI
		return c.blockIn(1, false)
	case 0xA3: // OUTI
		return c.blockOut(1, false)
	case 0xA8: // LDD
		return c.blockMove(-1, false)
	case 0xA9: // CPD
		return c.blockCompare(-1, false)
	case 0xAA: // IND
		return c.blockIn(-1, false)
	case 0xAB: // OUTD
		return c.blockOut(-1, false)
	case 0xB0: // LDIR
		return c.blockMove(1, true)
	case 0xB1: // CPIR
		return c.blockCompare(1, true)
	case 0xB2: // INIR
		return c.blockIn(1, true)
	case 0xB3: // OTIR
		return c.blockOut(1, true)
	case 0xB8: // LDDR
		return c.blockMove(-1, true)
	case 0xB9: // CPDR
		return c.blockCompare(-1, true)
	case 0xBA: // INDR
		return c.blockIn(-1, true)
	case 0xBB: // OTDR
		return c.blockOut(-1, true)
	}

	if op < 0x40 || op > 0x7F {
		return 8 // undefined ED opcode: no operation
	}

	y := int(op>>3) & 7
	z := int(op) & 7
	p := int(op>>4) & 3
	q := int(op>>3) & 1
	switch z {
	case 0: // IN r,(C); op 0x70 is IN (HL),(C): value kept in memory
		v := c.bus.In(c.bc())
		// Documented flags: S/Z/Y/X from the value, H clear,
		// PV = parity, N clear, C preserved.
		c.f = c.f&flagC | szpFlags(v)
		if op == 0x70 {
			c.bus.Write(c.hl(), v)
		} else {
			c.setReg(y, v)
		}
		return 12
	case 1: // OUT (C),r; op 0x71 is OUT (C),0
		if op == 0x71 {
			c.bus.Out(c.bc(), 0)
		} else {
			c.bus.Out(c.bc(), c.reg(y))
		}
		return 12
	case 2: // SBC HL,rr / ADC HL,rr
		if q == 0 {
			c.setHL(c.sbc16(c.hl(), c.pair(p)))
		} else {
			c.setHL(c.adc16(c.hl(), c.pair(p)))
		}
		return 15
	case 3: // LD (nn),rr / LD rr,(nn)
		addr := c.operand16()
		if q == 0 {
			c.write16(addr, c.pair(p))
		} else {
			c.setPair(p, c.read16(addr))
		}
		return 20
	case 4: // NEG (all eight encodings)
		old := c.a
		res := -old
		c.a = res
		f := flagN | (szpFlags(res) &^ flagPV)
		if old&0x0F != 0 {
			f |= flagH
		}
		if old == 0x80 {
			f |= flagPV
		}
		if old != 0 {
			f |= flagC
		}
		c.f = f
		return 8
	case 5: // RETN (all encodings) / RETI (0x4D only)
		c.pc = c.pop()
		if op != 0x4D {
			// RETN restores IFF1 from IFF2; RETI does not.
			c.iff1 = c.iff2
		}
		return 14
	case 6: // IM 0 / 1 / 2
		switch y {
		case 0, 1, 4, 5:
			c.im = 0
		case 2, 6:
			c.im = 1
		case 3, 7:
			c.im = 2
		}
		return 8
	case 7:
		switch op {
		case 0x47: // LD I,A
			c.i = c.a
			return 9
		case 0x4F: // LD R,A
			c.r = c.a
			return 9
		case 0x57: // LD A,I
			c.a = c.i
			c.ldAIRFlags(c.i)
			return 9
		case 0x5F: // LD A,R
			c.a = c.r
			c.ldAIRFlags(c.r)
			return 9
		case 0x67: // RRD
			addr := c.hl()
			t := c.bus.Read(addr)
			c.bus.Write(addr, c.a<<4|(t>>4))
			c.a = (c.a & 0xF0) | (t & 0x0F)
			c.setSZP(c.a)
			return 18
		case 0x6F: // RLD
			addr := c.hl()
			t := c.bus.Read(addr)
			c.bus.Write(addr, (t<<4)|(c.a&0x0F))
			c.a = (c.a & 0xF0) | (t >> 4)
			c.setSZP(c.a)
			return 18
		}
		return 8
	}
	return 8
}

// ldAIRFlags sets the flags for LD A,I and LD A,R: S/Z/Y/X from the
// value, PV holding a copy of IFF2, H and N clear, C preserved.
func (c *CPU) ldAIRFlags(v uint8) {
	f := c.f&flagC | (v & (flagS | flagY | flagX))
	if v == 0 {
		f |= flagZ
	}
	if c.iff2 {
		f |= flagPV
	}
	c.f = f
}

// addW adds a signed direction (+1 or -1) to a 16-bit register.
func addW(v uint16, dir int) uint16 {
	return uint16(int32(v) + int32(dir))
}

// repeatYX applies the documented flag oddity of the repeating
// block move/compare instructions: while the instruction repeats,
// the Y/X bits are taken from the high byte of the address just
// past the instruction (the WZ register holds PC+2 for these).
func (c *CPU) repeatYX(f uint8) uint8 {
	hb := uint8((c.pc + 2) >> 8) // c.pc was rewound by 2 already
	return f&^(flagY|flagX) | hb&(flagY|flagX)
}

// blockMove performs one LDI/LDD iteration; with repeat set it
// performs one LDIR/LDDR iteration, rewinding PC while BC != 0.
// Flag behaviour: H and N clear, PV set while BC != 0 after the
// decrement, and Y/X from (A + transferred byte): Y is its bit 1,
// X its bit 3. S, Z and C are preserved.
func (c *CPU) blockMove(dir int, repeat bool) int {
	v := c.bus.Read(c.hl())
	c.bus.Write(c.de(), v)
	c.setHL(addW(c.hl(), dir))
	c.setDE(addW(c.de(), dir))
	bc := c.bc() - 1
	c.setBC(bc)

	f := c.f & (flagS | flagZ | flagC)
	if bc != 0 {
		f |= flagPV
	}
	n := c.a + v
	f |= (n & 0x02) << 4 // bit 1 -> Y
	f |= n & 0x08        // bit 3 -> X
	if repeat && bc != 0 {
		c.pc -= 2
		c.f = c.repeatYX(f)
		return 21
	}
	c.f = f
	return 16
}

// blockCompare performs one CPI/CPD iteration; with repeat set it
// performs one CPIR/CPDR iteration, rewinding PC while BC != 0 and
// no match was found. Flags are those of CP A,(HL) except that C is
// preserved, PV reports BC != 0, and Y/X come from the difference
// adjusted by the half borrow (bit 1 and bit 3 of it).
func (c *CPU) blockCompare(dir int, repeat bool) int {
	v := c.bus.Read(c.hl())
	c.setHL(addW(c.hl(), dir))
	bc := c.bc() - 1
	c.setBC(bc)

	diff := int(c.a) - int(v)
	res := uint8(diff)
	halfBorrow := (c.a & 0x0F) < (v & 0x0F)
	f := c.f&flagC | flagN
	if res&0x80 != 0 {
		f |= flagS
	}
	if res == 0 {
		f |= flagZ
	}
	if halfBorrow {
		f |= flagH
	}
	w := res
	if halfBorrow {
		w--
	}
	f |= (w & 0x02) << 4 // bit 1 -> Y
	f |= w & 0x08        // bit 3 -> X
	if bc != 0 {
		f |= flagPV
	}
	if repeat && bc != 0 && res != 0 {
		c.pc -= 2
		c.f = c.repeatYX(f)
		return 21
	}
	c.f = f
	return 16
}

// blockIn performs one INI/IND iteration; with repeat set it
// performs one INIR/INDR iteration. The port is addressed by the
// value of BC before B is decremented.
//
// Flag behaviour (documented on z80.info): S/Z/Y/X are those of B
// after the decrement; N is bit 7 of the byte read; with
// k = byte + (C + direction), H and C are both set when k > 255 and
// PV is the parity of (k & 7) XOR B.
func (c *CPU) blockIn(dir int, repeat bool) int {
	v := c.bus.In(c.bc()) // old BC
	c.bus.Write(c.hl(), v)
	c.b--
	c.setHL(addW(c.hl(), dir))

	k := int(v) + int(uint8(int16(c.c)+int16(dir)))
	f := c.blockIOFlags(v, k)
	c.f = f
	if repeat && c.b != 0 {
		c.pc -= 2
		c.repeatIOFixup(k > 0xFF, v&0x80 != 0)
		return 21
	}
	return 16
}

// blockOut performs one OUTI/OUTD iteration; with repeat set it
// performs one OTIR/OTDR iteration. B is decremented before the
// port write, so the port is addressed by the new BC.
//
// Flag behaviour mirrors blockIn, except k = byte + L, with L taken
// after HL has been updated.
func (c *CPU) blockOut(dir int, repeat bool) int {
	v := c.bus.Read(c.hl())
	c.b--
	c.bus.Out(c.bc(), v) // new BC
	c.setHL(addW(c.hl(), dir))

	k := int(v) + int(c.l) // L after the HL update
	f := c.blockIOFlags(v, k)
	c.f = f
	if repeat && c.b != 0 {
		c.pc -= 2
		c.repeatIOFixup(k > 0xFF, v&0x80 != 0)
		return 21
	}
	return 16
}

// blockIOFlags builds the flag byte for one block I/O iteration:
// S/Z/Y/X from B (already decremented), N from bit 7 of the
// transferred byte, H and C from k overflowing a byte, PV the
// parity of (k & 7) XOR B.
func (c *CPU) blockIOFlags(v uint8, k int) uint8 {
	f := c.b & (flagS | flagY | flagX)
	if c.b == 0 {
		f |= flagZ
	}
	if v&0x80 != 0 {
		f |= flagN
	}
	if k > 0xFF {
		f |= flagH | flagC
	}
	if parityEven(uint8(k&7) ^ c.b) {
		f |= flagPV
	}
	return f
}

// repeatIOFixup applies the documented extra flag adjustment that
// happens when a block I/O instruction repeats: H and PV are
// recomputed from B alone, or from B+1 / B-1 when the transfer
// raised a carry, depending on the N flag the transfer left.
func (c *CPU) repeatIOFixup(carry, nflag bool) {
	var pvVal uint8
	var h bool
	switch {
	case !carry:
		pvVal = c.b
	case !nflag:
		pvVal = c.b + 1
		h = c.b&0x0F == 0x0F
	default:
		pvVal = c.b - 1
		h = c.b&0x0F == 0x00
	}
	c.f &^= flagH | flagPV
	if h {
		c.f |= flagH
	}
	if parityEven(pvVal) {
		c.f |= flagPV
	}
}
