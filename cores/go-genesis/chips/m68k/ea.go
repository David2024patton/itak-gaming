package m68k

// Effective-address machinery. The 68000 has 14 addressing modes,
// encoded as a 3-bit mode plus a 3-bit register field:
//
//	000 Dn        data register direct      001 An  address register direct
//	010 (An)      register indirect         011 (An)+   indirect postincrement
//	100 -(An)     indirect predecrement     101 (d16,An)  indirect + displacement
//	110 (d8,An,Xn) indirect + index + disp  111 special: 000 (xxx).W, 001 (xxx).L,
//	                                             010 (d16,PC), 011 (d8,PC,Xn),
//	                                             100 #imm immediate
//
// An operand is resolved once: the address is computed (extension words
// consumed, predecrement applied) and a pending postincrement is
// recorded for the caller to commit after the access, which matches the
// hardware order (a read-modify-write to (An)+ stores to the address
// computed before the increment).

type operand struct {
	mode, reg int
	addr      uint32 // effective address for memory operands
	imm       uint32 // immediate value (mode 7, reg 4)
	post      int    // pending postincrement for the address register
	cyc       int    // documented EA calculation cycles at the operand size
}

// eaFetchCycles is the manual's effective-address calculation time
// table: (byte/word, long) per mode.
func eaFetchCycles(mode, reg int, size int) int {
	long := size == szL
	pick := func(w, l int) int {
		if long {
			return l
		}
		return w
	}
	switch mode {
	case 0, 1:
		return 0
	case 2, 3:
		return pick(4, 8)
	case 4:
		return pick(6, 10)
	case 5:
		return pick(8, 12)
	case 6:
		return pick(10, 14)
	case 7:
		switch reg {
		case 0:
			return pick(8, 12)
		case 1:
			return pick(12, 16)
		case 2:
			return pick(8, 12)
		case 3:
			return pick(10, 14)
		case 4:
			return pick(4, 8)
		}
	}
	return 0
}

// leaCycles is the manual's LEA timing per control mode (also used for
// PEA at +8 and JMP/JSR from their own tables below).
func leaCycles(mode, reg int) int {
	switch mode {
	case 2:
		return 4
	case 5:
		return 8
	case 6:
		return 12
	case 7:
		switch reg {
		case 0, 2: // (xxx).W, (d16,PC)
			return 8
		case 1, 3: // (xxx).L, (d8,PC,Xn)
			return 12
		}
	}
	return 0
}

func jmpCycles(mode, reg int) int {
	switch mode {
	case 2:
		return 8
	case 5:
		return 10
	case 6:
		return 14
	case 7:
		switch reg {
		case 0:
			return 10
		case 1:
			return 12
		case 2:
			return 10
		case 3:
			return 14
		}
	}
	return 0
}

// Addressing-class predicates, from the manual's per-instruction EA
// tables. Instructions reject operands outside their class with the
// illegal-instruction exception before any extension word is read (the
// class is fully determined by the opcode bits).
func eaReadable(mode, reg int) bool { return mode <= 6 || (mode == 7 && reg <= 4) }

func eaAlterableMem(mode, reg int) bool {
	return (mode >= 2 && mode <= 6) || (mode == 7 && reg <= 1)
}

func eaDataAlterable(mode, reg int) bool {
	return mode == 0 || eaAlterableMem(mode, reg)
}

func eaControl(mode, reg int) bool {
	return mode == 2 || mode == 5 || mode == 6 || (mode == 7 && reg <= 3)
}

// indexEA computes the register-indirect-with-index address. base is
// the address register (or PC) value; the extension word is read from
// the instruction stream at the current PC, which for the PC-relative
// form is also the displacement base the hardware uses. Index format
// (brief, the only form the 68000 has): D/A select, register, W/L
// select, three zero bits (no scaling on the 68000), signed 8-bit
// displacement.
func (c *CPU) indexEA(base uint32) uint32 {
	ext := c.fetch16()
	idxReg := (ext >> 12) & 7
	var idx uint32
	if ext&0x8000 != 0 {
		idx = c.a[idxReg]
	} else {
		idx = c.d[idxReg]
	}
	if ext&0x0800 == 0 {
		idx = uint32(int32(int16(uint16(idx)))) // word index, sign-extended
	}
	disp := int32(int8(uint8(ext & 0xFF)))
	return base + uint32(disp) + idx
}

// resolveEA computes an operand at the given size (szB/szW/szL),
// consuming extension words and applying predecrement. It panics with
// illegalEncoding for the nonexistent special modes (111 reg 5+).
func (c *CPU) resolveEA(mode, reg, size int) operand {
	o := operand{mode: mode, reg: reg, cyc: eaFetchCycles(mode, reg, size)}
	switch mode {
	case 0, 1:
		// Register direct: no address.
	case 2:
		o.addr = c.a[reg]
	case 3:
		o.addr = c.a[reg]
		n := sizeBytes(size)
		if reg == 7 && size == szB {
			n = 2 // the stack-pointer byte quirk: A7 always moves by 2
		}
		o.post = n
	case 4:
		n := sizeBytes(size)
		if reg == 7 && size == szB {
			n = 2
		}
		c.a[reg] -= uint32(n)
		o.addr = c.a[reg]
	case 5:
		d := int32(int16(c.fetch16()))
		o.addr = c.a[reg] + uint32(d)
	case 6:
		o.addr = c.indexEA(c.a[reg])
	case 7:
		switch reg {
		case 0: // (xxx).W, sign-extended
			o.addr = uint32(int32(int16(c.fetch16())))
		case 1: // (xxx).L
			o.addr = c.fetch32()
		case 2: // (d16,PC): displacement base is the extension word's address
			extAddr := c.pc
			d := int32(int16(c.fetch16()))
			o.addr = extAddr + uint32(d)
		case 3: // (d8,PC,Xn)
			o.addr = c.indexEA(c.pc)
		case 4: // #imm
			switch size {
			case szB:
				o.imm = uint32(c.fetch16() & 0xFF)
			case szW:
				o.imm = uint32(c.fetch16())
			case szL:
				o.imm = c.fetch32()
			}
		default:
			panic(illegalEncoding{})
		}
	}
	return o
}

// readMem reads a byte/word/long operand from memory.
func (c *CPU) readMem(addr uint32, size int) uint32 {
	switch size {
	case szB:
		return uint32(c.rd8(addr))
	case szW:
		return uint32(c.rd16(addr))
	default:
		return c.rd32(addr)
	}
}

// writeMem writes a byte/word/long operand to memory.
func (c *CPU) writeMem(addr uint32, size int, v uint32) {
	switch size {
	case szB:
		c.wr8(addr, uint8(v))
	case szW:
		c.wr16(addr, uint16(v))
	default:
		c.wr32(addr, v)
	}
}

// opRead reads an operand's value at the given size. Register-direct
// reads return the register masked to the operand size. The pending
// postincrement is NOT applied; use readSrc for source operands, or
// commitPost after a read-modify-write completes.
func (c *CPU) opRead(o operand, size int) uint32 {
	if o.mode == 0 {
		return c.d[o.reg] & sizeMask(size)
	}
	if o.mode == 1 {
		return c.a[o.reg] & sizeMask(size)
	}
	if o.mode == 7 && o.reg == 4 {
		return o.imm
	}
	return c.readMem(o.addr, size)
}

// readSrc reads a source operand and applies its postincrement,
// matching the hardware order for operand fetch.
func (c *CPU) readSrc(o operand, size int) uint32 {
	v := c.opRead(o, size)
	c.commitPost(o)
	return v
}

// opWrite writes a value to an operand at the given size and applies
// its postincrement. Data-register writes merge at byte/word size;
// address-register word writes sign-extend into the full register, as
// the hardware does.
func (c *CPU) opWrite(o operand, size int, v uint32) {
	switch o.mode {
	case 0:
		switch size {
		case szB:
			c.d[o.reg] = (c.d[o.reg] &^ 0xFF) | (v & 0xFF)
		case szW:
			c.d[o.reg] = (c.d[o.reg] &^ 0xFFFF) | (v & 0xFFFF)
		case szL:
			c.d[o.reg] = v
		}
	case 1:
		switch size {
		case szW:
			c.a[o.reg] = uint32(int32(int16(uint16(v))))
		case szL:
			c.a[o.reg] = v
		}
	default:
		c.writeMem(o.addr, size, v)
	}
	c.commitPost(o)
}

// commitPost applies a resolved operand's pending postincrement.
func (c *CPU) commitPost(o operand) {
	if o.post != 0 {
		c.a[o.reg] += uint32(o.post)
	}
}

// signExtend widens a value of the given size to 32 bits, signed.
func signExtend(v uint32, size int) uint32 {
	switch size {
	case szB:
		return uint32(int32(int8(uint8(v))))
	case szW:
		return uint32(int32(int16(uint16(v))))
	default:
		return v
	}
}

// eaFields splits an opcode's low 6 bits into mode and register.
func eaFields(op uint16) (mode, reg int) {
	return int((op >> 3) & 7), int(op & 7)
}
