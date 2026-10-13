package m68ec000

// Instruction execution dispatch. exec runs one decoded
// opcode word; c.cur is the opcode address, c.ir the
// opcode, and c.pc already points past the opcode word.
// Exceptions that are part of normal execution (divide
// by zero, CHK, TRAPV, TRAP) call exception directly with
// c.pc as the saved PC, since effective address decode
// has consumed every extension word by then. Faults
// (illegal patterns, privilege violations, address
// errors) travel as the panic carriers recovered in
// stepInner.

func opSize(code int) int {
	switch code {
	case 0:
		return 1
	case 1:
		return 2
	case 2:
		return 4
	}
	return 0
}

// fetchImm consumes an immediate operand of the given
// size at the PC.
func (c *CPU) fetchImm(size int) uint32 {
	switch size {
	case 1:
		return uint32(c.fetchExt() & 0xFF)
	case 2:
		return uint32(c.fetchExt())
	default:
		hi := uint32(c.fetchExt())
		lo := uint32(c.fetchExt())
		return hi<<16 | lo
	}
}

func dnEA(reg int) ea { return ea{mode: 0, reg: reg & 7, dn: true} }

// decodeDest decodes the reversed destination field of
// a MOVE opcode (register in bits 11 to 9, mode in bits
// 8 to 6), consuming extension words at the PC. PC
// relative and immediate destinations are illegal.
func (c *CPU) decodeDest(op uint16, size int) ea {
	mode := int((op >> 6) & 7)
	reg := int((op >> 9) & 7)
	e := ea{mode: mode, reg: reg, cycles: eaCycles(mode, reg)}
	if size == 4 && mode >= 2 {
		e.cycles += 4
	}
	adj := size
	if size == 1 && reg == 7 {
		adj = 2
	}
	switch mode {
	case 0:
		e.dn = true
	case 1:
		e.an = true
	case 2:
		e.mem = true
		e.addr = c.a[reg]
	case 3:
		e.mem = true
		e.addr = c.a[reg]
		c.a[reg] += uint32(adj)
	case 4:
		e.mem = true
		c.a[reg] -= uint32(adj)
		e.addr = c.a[reg]
	case 5:
		disp := int32(int16(c.fetchExt()))
		e.mem = true
		e.addr = uint32(int32(c.a[reg]) + disp)
	case 6:
		ext := c.fetchExt()
		disp := int32(int8(ext))
		e.mem = true
		e.addr = uint32(int32(c.a[reg]) + disp + int32(c.indexValue(ext)))
	case 7:
		switch reg {
		case 0:
			e.mem = true
			e.addr = uint32(int32(int16(c.fetchExt())))
		case 1:
			e.mem = true
			hi := uint32(c.fetchExt())
			lo := uint32(c.fetchExt())
			e.addr = hi<<16 | lo
		default:
			panic(illegalError{instr: c.cur})
		}
	}
	return e
}

func (c *CPU) exec(op uint16) int {
	if op == 0x4AFC { // ILLEGAL
		panic(illegalError{instr: c.cur})
	}
	switch op >> 12 {
	case 0:
		return c.execGroup0(op)
	case 1:
		return c.execMove(op, 1)
	case 2:
		return c.execMove(op, 4)
	case 3:
		return c.execMove(op, 2)
	case 4:
		return c.execMisc(op)
	case 5:
		return c.execGroup5(op)
	case 6:
		return c.execBranch(op)
	case 7:
		return c.execMoveq(op)
	case 8:
		return c.execGroup8(op)
	case 9:
		return c.execAddSub(op, false)
	case 10:
		panic(illegalError{instr: c.cur, vec: 10}) // line A
	case 11:
		return c.execGroupB(op)
	case 12:
		return c.execGroupC(op)
	case 13:
		return c.execAddSub(op, true)
	case 14:
		return c.execShift(op)
	case 15:
		panic(illegalError{instr: c.cur, vec: 11}) // line F
	}
	panic(illegalError{instr: c.cur})
}

// execMove handles MOVE and MOVEA (destination mode 1).
func (c *CPU) execMove(op uint16, size int) int {
	if (op>>6)&7 == 1 && size == 1 {
		panic(illegalError{instr: c.cur}) // no byte MOVEA
	}
	src := c.decodeEA(op, size)
	val := c.read(src, size)
	dst := c.decodeDest(op, size)
	c.write(dst, val, size)
	if dst.an {
		return 4 + src.cycles // MOVEA sets no flags
	}
	c.logicFlags(val, size)
	return 4 + src.cycles + dst.cycles
}

func (c *CPU) execMoveq(op uint16) int {
	if op&0x0100 != 0 {
		panic(illegalError{instr: c.cur})
	}
	val := uint32(int32(int8(op)))
	c.d[(op>>9)&7] = val
	c.logicFlags(val, 4)
	return 4
}

// ---- group 0: immediate, bit operations, MOVEP ----

func (c *CPU) execGroup0(op uint16) int {
	if op&0xF138 == 0x0108 {
		return c.execMovep(op)
	}
	if op&0x0100 != 0 {
		return c.execBitDyn(op)
	}
	if op>>8 == 0x08 {
		return c.execBitStatic(op)
	}
	switch op >> 8 {
	case 0x00, 0x02, 0x04, 0x06, 0x0A, 0x0C:
		return c.execImmediate(op)
	}
	panic(illegalError{instr: c.cur})
}

// bitOp runs BTST, BCHG, BCLR, or BSET (kind 0 to 3).
// Only Z is affected: set when the tested bit was 0.
// Register operands take the bit number modulo 32,
// memory byte operands modulo 8.
func (c *CPU) bitOp(kind int, bitNum uint32, e ea) int {
	if e.an {
		panic(illegalError{instr: c.cur})
	}
	if e.dn {
		bit := bitNum % 32
		val := c.d[e.reg]
		c.setFlag(srZ, (val>>bit)&1 == 0)
		switch kind {
		case 1:
			val ^= 1 << bit
		case 2:
			val &^= 1 << bit
		case 3:
			val |= 1 << bit
		}
		if kind != 0 {
			c.d[e.reg] = val
		}
		return 6
	}
	bit := bitNum % 8
	val := c.read(e, 1)
	c.setFlag(srZ, (val>>bit)&1 == 0)
	switch kind {
	case 1:
		val ^= 1 << bit
	case 2:
		val &^= 1 << bit
	case 3:
		val |= 1 << bit
	}
	if kind != 0 {
		c.write(e, val, 1)
	}
	return 4 + e.cycles
}

func (c *CPU) execBitDyn(op uint16) int {
	kind := int((op >> 6) & 3)
	bitNum := c.d[(op>>9)&7]
	e := c.decodeEA(op, 1)
	return c.bitOp(kind, bitNum, e) + e.cycles
}

func (c *CPU) execBitStatic(op uint16) int {
	kind := int((op >> 6) & 3)
	bitNum := uint32(c.fetchExt() & 0xFF)
	e := c.decodeEA(op, 1)
	return c.bitOp(kind, bitNum, e) + e.cycles + 2
}

// execImmediate handles ORI, ANDI, SUBI, ADDI, EORI,
// and CMPI, including the CCR and SR destination forms
// of the logic operations.
func (c *CPU) execImmediate(op uint16) int {
	kind := op >> 8 // 0x00 ORI, 0x02 ANDI, 0x04 SUBI, 0x06 ADDI, 0x0A EORI, 0x0C CMPI
	sizeCode := int((op >> 6) & 3)
	if sizeCode == 3 {
		panic(illegalError{instr: c.cur})
	}
	size := opSize(sizeCode)
	if op&0x3F == 0x3C {
		// Destination field names CCR (byte) or SR (word).
		if kind != 0x00 && kind != 0x02 && kind != 0x0A {
			panic(illegalError{instr: c.cur})
		}
		if size == 1 {
			imm := c.fetchImm(1)
			res := logicImm(kind, uint32(c.sr&0xFF), imm)
			c.sr = (c.sr & 0xFF00) | uint16(res&0x1F)
			return 12
		}
		if size == 2 {
			if !c.supervisor() {
				panic(privError{instr: c.cur})
			}
			imm := c.fetchImm(2)
			c.setSR(uint16(logicImm(kind, uint32(c.sr), imm)))
			return 12
		}
		panic(illegalError{instr: c.cur})
	}
	imm := c.fetchImm(size)
	e := c.decodeEA(op, size)
	if e.imm || e.an {
		panic(illegalError{instr: c.cur})
	}
	dst := c.read(e, size)
	switch kind {
	case 0x00:
		res := dst | imm
		c.logicFlags(res, size)
		c.write(e, res, size)
	case 0x02:
		res := dst & imm
		c.logicFlags(res, size)
		c.write(e, res, size)
	case 0x0A:
		res := dst ^ imm
		c.logicFlags(res, size)
		c.write(e, res, size)
	case 0x06:
		res := c.addFlags(imm, dst, size, 0)
		c.write(e, res, size)
	case 0x04:
		res := c.subFlags(imm, dst, size, 0, true)
		c.write(e, res, size)
	case 0x0C:
		c.subFlags(imm, dst, size, 0, false)
	}
	return 8 + e.cycles
}

func logicImm(kind uint16, dst, imm uint32) uint32 {
	switch kind {
	case 0x00:
		return dst | imm
	case 0x02:
		return dst & imm
	default:
		return dst ^ imm
	}
}

// execMovep moves bytes between a data register and
// alternate memory bytes (even offsets from An plus a
// signed word displacement). Word transfers use the low
// word of the register and leave the high word intact;
// long transfers use the whole register. No flags.
func (c *CPU) execMovep(op uint16) int {
	opmode := int((op >> 6) & 7)
	dn := int((op >> 9) & 7)
	an := int(op & 7)
	disp := int32(int16(c.fetchExt()))
	addr := uint32(int32(c.a[an]) + disp)
	load := opmode&1 != 0
	long := opmode&2 != 0
	n := 2
	if long {
		n = 4
	}
	if load {
		var val uint32
		for i := 0; i < n; i++ {
			val = val<<8 | uint32(c.rd8(addr+uint32(2*i)))
		}
		if long {
			c.d[dn] = val
		} else {
			c.d[dn] = (c.d[dn] & 0xFFFF0000) | val
		}
	} else {
		val := c.d[dn]
		shift := uint(8 * (n - 1))
		for i := 0; i < n; i++ {
			c.wr8(addr+uint32(2*i), uint8(val>>shift))
			shift -= 8
		}
	}
	if long {
		return 24
	}
	return 16
}

// ---- group 5: ADDQ, SUBQ, Scc, DBcc ----

func (c *CPU) execGroup5(op uint16) int {
	sizeCode := int((op >> 6) & 3)
	cond := int((op >> 8) & 0xF)
	if sizeCode == 3 {
		if (op>>3)&7 == 1 {
			// DBcc: extension word is the displacement,
			// taken from the address of that word.
			reg := int(op & 7)
			disp := int32(int16(c.fetchExt()))
			if c.condition(cond) {
				return 12
			}
			low := uint16(c.d[reg]) - 1
			c.d[reg] = (c.d[reg] & 0xFFFF0000) | uint32(low)
			if low != 0xFFFF {
				c.pc = uint32(int32(c.cur+2) + disp)
				return 10
			}
			return 14
		}
		// Scc: byte destination, 0xFF true, 0x00 false.
		e := c.decodeEA(op, 1)
		if e.an || e.imm {
			panic(illegalError{instr: c.cur})
		}
		var val uint32
		if c.condition(cond) {
			val = 0xFF
		}
		c.write(e, val, 1)
		return 4 + e.cycles
	}
	size := opSize(sizeCode)
	mode := int((op >> 3) & 7)
	if mode == 1 && size == 1 {
		panic(illegalError{instr: c.cur}) // no byte ADDQ/SUBQ to An
	}
	count := uint32((op >> 9) & 7)
	if count == 0 {
		count = 8
	}
	e := c.decodeEA(op, size)
	add := op&0x0100 == 0
	if e.an {
		// Address register quick arithmetic runs on the
		// full register and sets no flags.
		if add {
			c.a[e.reg] += count
		} else {
			c.a[e.reg] -= count
		}
		return 4
	}
	if e.imm {
		panic(illegalError{instr: c.cur})
	}
	dst := c.read(e, size)
	var res uint32
	if add {
		res = c.addFlags(count, dst, size, 0)
	} else {
		res = c.subFlags(count, dst, size, 0, true)
	}
	c.write(e, res, size)
	return 4 + e.cycles
}

// ---- group 6: BRA, BSR, Bcc ----

func (c *CPU) execBranch(op uint16) int {
	cond := int((op >> 8) & 0xF)
	disp8 := int8(op)
	var disp int32
	if disp8 == 0 {
		disp = int32(int16(c.fetchExt()))
	} else {
		// On the base 68000 a byte displacement of 0xFF
		// is a plain displacement of -1; the long
		// displacement form is a 68020 addition.
		disp = int32(disp8)
	}
	target := uint32(int32(c.cur+2) + disp)
	if cond == 1 { // BSR
		c.push32(c.pc) // past the opcode and any extension
		c.pc = target
		return 18
	}
	if cond == 0 || c.condition(cond) {
		c.pc = target
		return 10
	}
	return 8
}

// ---- BCD shared core (ABCD, SBCD) ----

// bcdFlags applies the BCD flag rule: N from the
// result, V from the helper, C and X both the decimal
// carry or borrow, and Z under the multi precision
// rule (cleared by a nonzero result, left unchanged by
// a zero result).
func (c *CPU) bcdFlags(res, carryOut uint32, v, oldZ bool) {
	c.setFlag(srN, res&0x80 != 0)
	c.setFlag(srV, v)
	c.setFlag(srC, carryOut != 0)
	c.setFlag(srX, carryOut != 0)
	if res != 0 {
		c.setFlag(srZ, false)
	} else {
		c.setFlag(srZ, oldZ)
	}
}

// execBCD runs ABCD (sub false) or SBCD (sub true). The
// register field in bits 11 to 9 is the destination,
// the low register field the source (assembler syntax
// lists the source first); bit 3 selects data registers
// or predecrement memory operands.
func (c *CPU) execBCD(op uint16, sub bool) int {
	dstReg := int((op >> 9) & 7)
	srcReg := int(op & 7)
	oldZ := c.sr&srZ != 0
	x := c.getX()
	var src, dst uint32
	var dstEA ea
	memForm := op&0x0008 != 0
	if memForm {
		sAdj, dAdj := 1, 1
		if srcReg == 7 {
			sAdj = 2
		}
		if dstReg == 7 {
			dAdj = 2
		}
		c.a[srcReg] -= uint32(sAdj)
		src = uint32(c.rd8(c.a[srcReg]))
		c.a[dstReg] -= uint32(dAdj)
		dstEA = ea{mem: true, addr: c.a[dstReg], mode: 4, reg: dstReg}
		dst = uint32(c.rd8(c.a[dstReg]))
	} else {
		src = c.d[srcReg] & 0xFF
		dst = c.d[dstReg] & 0xFF
		dstEA = dnEA(dstReg)
	}
	var res, carryOut uint32
	var v bool
	if sub {
		res, carryOut, v = bcdSub(dst, src, x)
	} else {
		res, carryOut, v = bcdAdd(dst, src, x)
	}
	c.bcdFlags(res, carryOut, v, oldZ)
	c.write(dstEA, res, 1)
	return 6
}

// ---- group 8: OR, DIVU, DIVS, SBCD ----

func (c *CPU) execGroup8(op uint16) int {
	if op&0xF1F0 == 0x8100 {
		return c.execBCD(op, true) // SBCD
	}
	dn := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	switch opmode {
	case 3:
		return c.execDiv(op, dn, false)
	case 7:
		return c.execDiv(op, dn, true)
	case 0, 1, 2:
		size := opSize(opmode)
		e := c.decodeEA(op, size)
		res := c.d[dn]&sizeMask(size) | c.read(e, size)
		c.logicFlags(res, size)
		c.write(dnEA(dn), res, size)
		return 4 + e.cycles
	default: // 4, 5, 6: OR Dn, EA
		size := opSize(opmode & 3)
		e := c.decodeEA(op, size)
		if e.an || e.imm {
			panic(illegalError{instr: c.cur})
		}
		res := c.read(e, size) | c.d[dn]&sizeMask(size)
		c.logicFlags(res, size)
		c.write(e, res, size)
		return 4 + e.cycles
	}
}

// execDiv runs DIVU (signed false) or DIVS (signed
// true): a 32 bit dividend in Dn divided by a word
// source, packing the remainder in the high word and
// the quotient in the low word of Dn. Divide by zero
// takes vector 5 with the next instruction stacked. On
// overflow V is set and the destination register is
// left unchanged; that is the documented convention
// this core ships (the manual leaves the exact result
// undefined, and common emulators keep the register).
func (c *CPU) execDiv(op uint16, dn int, signed bool) int {
	e := c.decodeEA(op, 2)
	src := c.read(e, 2)
	if src == 0 {
		c.exception(5, c.pc)
		return 38
	}
	if !signed {
		dividend := uint64(c.d[dn])
		quot := dividend / uint64(src)
		rem := dividend % uint64(src)
		if quot > 0xFFFF {
			c.setFlag(srV, true)
			return 30
		}
		c.d[dn] = uint32(rem)<<16 | uint32(quot)
		c.setNZ(uint32(quot), 2)
		c.setFlag(srV, false)
		c.setFlag(srC, false)
		return 140 + e.cycles
	}
	dividend := int32(c.d[dn])
	divisor := int32(int16(src))
	quot := dividend / divisor
	rem := dividend % divisor
	if quot > 32767 || quot < -32768 {
		c.setFlag(srV, true)
		return 30
	}
	c.d[dn] = uint32(uint16(rem))<<16 | uint32(uint16(quot))
	c.setNZ(uint32(quot), 2)
	c.setFlag(srV, false)
	c.setFlag(srC, false)
	return 158 + e.cycles
}

// ---- groups 9 and D: SUB, ADD, SUBA, ADDA, SUBX, ADDX ----

func (c *CPU) execAddSub(op uint16, add bool) int {
	dn := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	if opmode == 3 || opmode == 7 {
		// ADDA / SUBA: word source sign extended, full
		// width arithmetic, no flags.
		size := 2
		if opmode == 7 {
			size = 4
		}
		e := c.decodeEA(op, size)
		src := c.read(e, size)
		if size == 2 {
			src = uint32(int32(int16(src)))
		}
		if add {
			c.a[dn] += src
		} else {
			c.a[dn] -= src
		}
		return 4 + e.cycles
	}
	xBase := uint16(0x9100)
	if add {
		xBase = 0xD100
	}
	if op&0xF130 == xBase {
		return c.execX(op, add)
	}
	size := opSize(opmode & 3)
	if opmode < 3 {
		e := c.decodeEA(op, size)
		src := c.read(e, size)
		var res uint32
		if add {
			res = c.addFlags(src, c.d[dn], size, 0)
		} else {
			res = c.subFlags(src, c.d[dn], size, 0, true)
		}
		c.write(dnEA(dn), res, size)
		return 4 + e.cycles
	}
	e := c.decodeEA(op, size)
	if e.an || e.imm {
		panic(illegalError{instr: c.cur})
	}
	dst := c.read(e, size)
	var res uint32
	if add {
		res = c.addFlags(c.d[dn], dst, size, 0)
	} else {
		res = c.subFlags(c.d[dn], dst, size, 0, true)
	}
	c.write(e, res, size)
	return 4 + e.cycles
}

// execX runs ADDX (add true) or SUBX (add false), in
// register or predecrement memory form, under the multi
// precision Z rule.
func (c *CPU) execX(op uint16, add bool) int {
	size := opSize(int((op >> 6) & 3))
	// Bits 11 to 9 name the destination register, the
	// low field the source, as with ADDX Dy, Dx syntax.
	dstReg := int((op >> 9) & 7)
	srcReg := int(op & 7)
	oldZ := c.sr&srZ != 0
	x := c.getX()
	var src, dst uint32
	var dstEA ea
	if op&0x0008 != 0 {
		adj := size
		if size == 1 {
			if srcReg == 7 {
				c.a[srcReg] -= 2
			} else {
				c.a[srcReg] -= 1
			}
			if dstReg == 7 {
				c.a[dstReg] -= 2
			} else {
				c.a[dstReg] -= 1
			}
		} else {
			c.a[srcReg] -= uint32(adj)
			c.a[dstReg] -= uint32(adj)
		}
		src = c.readMem(c.a[srcReg], size)
		dstEA = ea{mem: true, addr: c.a[dstReg], mode: 4, reg: dstReg}
		dst = c.readMem(c.a[dstReg], size)
	} else {
		src = c.d[srcReg] & sizeMask(size)
		dst = c.d[dstReg] & sizeMask(size)
		dstEA = dnEA(dstReg)
	}
	var res uint32
	if add {
		res = c.addFlags(src, dst, size, x)
	} else {
		res = c.subFlags(src, dst, size, x, true)
	}
	if res&sizeMask(size) == 0 {
		c.setFlag(srZ, oldZ) // multi precision Z rule
	}
	c.write(dstEA, res, size)
	return 4
}

// ---- group B: CMP, CMPA, CMPM, EOR ----

func (c *CPU) execGroupB(op uint16) int {
	dn := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	if op&0xF138 == 0xB108 {
		// CMPM: (Ay)+ compared against (Ax)+.
		size := opSize(opmode & 3)
		srcReg := int(op & 7)
		sAdj, dAdj := size, size
		if size == 1 {
			if srcReg == 7 {
				sAdj = 2
			}
			if dn == 7 {
				dAdj = 2
			}
		}
		src := c.readMem(c.a[srcReg], size)
		c.a[srcReg] += uint32(sAdj)
		dst := c.readMem(c.a[dn], size)
		c.a[dn] += uint32(dAdj)
		c.subFlags(src, dst, size, 0, false)
		return 8
	}
	if opmode == 3 || opmode == 7 {
		// CMPA: word source sign extended, compared at
		// full width, X untouched.
		size := 2
		if opmode == 7 {
			size = 4
		}
		e := c.decodeEA(op, size)
		src := c.read(e, size)
		if size == 2 {
			src = uint32(int32(int16(src)))
		}
		c.subFlags(src, c.a[dn], 4, 0, false)
		return 4 + e.cycles
	}
	size := opSize(opmode & 3)
	if opmode < 3 {
		e := c.decodeEA(op, size)
		c.subFlags(c.read(e, size), c.d[dn], size, 0, false)
		return 4 + e.cycles
	}
	// EOR Dn, EA
	e := c.decodeEA(op, size)
	if e.an || e.imm {
		panic(illegalError{instr: c.cur})
	}
	res := c.read(e, size) ^ c.d[dn]&sizeMask(size)
	c.logicFlags(res, size)
	c.write(e, res, size)
	return 4 + e.cycles
}

// ---- group C: AND, MULU, MULS, ABCD, EXG ----

func (c *CPU) execGroupC(op uint16) int {
	if op&0xF1F0 == 0xC100 {
		return c.execBCD(op, false) // ABCD
	}
	dn := int((op >> 9) & 7)
	if op&0x0100 != 0 {
		switch (op >> 3) & 0x1F {
		case 0x08: // EXG Dx, Dy
			other := int(op & 7)
			c.d[dn], c.d[other] = c.d[other], c.d[dn]
			return 6
		case 0x09: // EXG Ax, Ay
			other := int(op & 7)
			c.a[dn], c.a[other] = c.a[other], c.a[dn]
			return 6
		case 0x11: // EXG Dx, Ay
			other := int(op & 7)
			c.d[dn], c.a[other] = c.a[other], c.d[dn]
			return 6
		}
	}
	opmode := int((op >> 6) & 7)
	switch opmode {
	case 3, 7: // MULU, MULS: word source times low word of Dn
		e := c.decodeEA(op, 2)
		src := c.read(e, 2)
		var res uint32
		if opmode == 3 {
			res = (c.d[dn] & 0xFFFF) * src
		} else {
			res = uint32(int32(int16(c.d[dn])) * int32(int16(src)))
		}
		c.d[dn] = res
		c.logicFlags(res, 4)
		return 38 + e.cycles
	case 0, 1, 2:
		size := opSize(opmode)
		e := c.decodeEA(op, size)
		res := c.d[dn] & sizeMask(size) & c.read(e, size)
		c.logicFlags(res, size)
		c.write(dnEA(dn), res, size)
		return 4 + e.cycles
	default: // 4, 5, 6: AND Dn, EA
		size := opSize(opmode & 3)
		e := c.decodeEA(op, size)
		if e.an || e.imm {
			panic(illegalError{instr: c.cur})
		}
		res := c.read(e, size) & c.d[dn] & sizeMask(size)
		c.logicFlags(res, size)
		c.write(e, res, size)
		return 4 + e.cycles
	}
}

// ---- group 4: the miscellaneous family ----

func (c *CPU) execMisc(op uint16) int {
	switch {
	case op&0xFFC0 == 0x40C0: // MOVE from SR
		e := c.decodeEA(op, 2)
		if e.imm {
			panic(illegalError{instr: c.cur})
		}
		c.write(e, uint32(c.sr), 2)
		return 4 + e.cycles
	case op&0xFFC0 == 0x44C0: // MOVE to CCR
		e := c.decodeEA(op, 2)
		v := c.read(e, 2)
		c.sr = (c.sr & 0xFF00) | uint16(v&0x1F)
		return 8 + e.cycles
	case op&0xFFC0 == 0x46C0: // MOVE to SR
		if !c.supervisor() {
			panic(privError{instr: c.cur})
		}
		e := c.decodeEA(op, 2)
		c.setSR(uint16(c.read(e, 2)))
		return 8 + e.cycles
	case op&0xF1C0 == 0x41C0: // LEA
		addr := c.controlAddr(op)
		c.a[(op>>9)&7] = addr
		return 4
	case op&0xF1C0 == 0x4180: // CHK
		dn := int((op >> 9) & 7)
		e := c.decodeEA(op, 2)
		upper := int32(int16(c.read(e, 2)))
		val := int32(int16(c.d[dn]))
		if val < 0 || val > upper {
			c.setFlag(srN, val < 0)
			c.exception(6, c.pc)
			return 40
		}
		return 10 + e.cycles
	}
	switch op >> 8 {
	case 0x40, 0x42, 0x44, 0x46:
		return c.execUnary(op)
	case 0x48, 0x4C:
		return c.execGroup48(op)
	case 0x4A:
		return c.execTest(op)
	case 0x4E:
		return c.execControl(op)
	}
	panic(illegalError{instr: c.cur})
}

// execUnary handles NEGX (0x40), CLR (0x42), NEG
// (0x44), and NOT (0x46) at byte, word, and long sizes.
func (c *CPU) execUnary(op uint16) int {
	sizeCode := int((op >> 6) & 3)
	if sizeCode == 3 {
		panic(illegalError{instr: c.cur})
	}
	size := opSize(sizeCode)
	e := c.decodeEA(op, size)
	if e.an || e.imm {
		panic(illegalError{instr: c.cur})
	}
	switch op >> 8 {
	case 0x42: // CLR
		c.logicFlags(0, size)
		c.write(e, 0, size)
	case 0x46: // NOT
		res := ^c.read(e, size) & sizeMask(size)
		c.logicFlags(res, size)
		c.write(e, res, size)
	case 0x44: // NEG
		res := c.subFlags(c.read(e, size), 0, size, 0, true)
		c.write(e, res, size)
	case 0x40: // NEGX, multi precision Z rule
		oldZ := c.sr&srZ != 0
		res := c.subFlags(c.read(e, size), 0, size, c.getX(), true)
		if res&sizeMask(size) == 0 {
			c.setFlag(srZ, oldZ)
		}
		c.write(e, res, size)
	}
	return 4 + e.cycles
}

// execGroup48 handles the 0x48 and 0x4C high bytes:
// NBCD, SWAP, EXT, PEA, and MOVEM in both directions.
func (c *CPU) execGroup48(op uint16) int {
	mode := int((op >> 3) & 7)
	if op&0xFB80 == 0x4880 {
		if mode == 0 {
			// EXT: the EA field is a data register.
			reg := int(op & 7)
			if op&0x0040 == 0 { // EXT.W: byte to word
				res := uint32(int32(int8(c.d[reg])))
				c.logicFlags(res, 2)
				c.write(dnEA(reg), res, 2)
			} else { // EXT.L: word to long
				res := uint32(int32(int16(c.d[reg])))
				c.logicFlags(res, 4)
				c.d[reg] = res
			}
			return 4
		}
		return c.execMovem(op)
	}
	if op>>8 == 0x4C {
		panic(illegalError{instr: c.cur})
	}
	switch (op >> 6) & 3 {
	case 0: // NBCD
		e := c.decodeEA(op, 1)
		if e.an || e.imm {
			panic(illegalError{instr: c.cur})
		}
		oldZ := c.sr&srZ != 0
		res, borrow, v := bcdSub(0, c.read(e, 1), c.getX())
		c.bcdFlags(res, borrow, v, oldZ)
		c.write(e, res, 1)
		return 4 + e.cycles
	case 1:
		if mode == 0 { // SWAP
			reg := int(op & 7)
			d := c.d[reg]
			res := d<<16 | d>>16
			c.d[reg] = res
			c.logicFlags(res, 4)
			return 4
		}
		// PEA
		addr := c.controlAddr(op)
		c.push32(addr)
		return 12
	}
	panic(illegalError{instr: c.cur})
}

func regByIndex(c *CPU, idx int) uint32 {
	if idx < 8 {
		return c.d[idx]
	}
	return c.a[idx-8]
}

func setRegByIndex(c *CPU, idx int, val uint32, size int) {
	if size == 2 {
		val = uint32(int32(int16(val)))
	}
	if idx < 8 {
		c.d[idx] = val
	} else {
		c.a[idx-8] = val
	}
}

// execMovem runs MOVEM in both directions and sizes.
// The register list mask is the extension word right
// after the opcode, before any effective address
// extension words. For a predecrement destination the
// mask bits are read in reverse order (bit 0 names A7)
// and registers are stored from the highest named
// register down, so the memory image ends up in the
// same ascending register order a postincrement load
// with the normal mask expects.
func (c *CPU) execMovem(op uint16) int {
	load := op&0x0400 != 0
	size := 2
	if op&0x0040 != 0 {
		size = 4
	}
	mask := c.fetchExt()
	mode := int((op >> 3) & 7)
	reg := int(op & 7)
	var addr uint32
	switch mode {
	case 2:
		addr = c.a[reg]
	case 3:
		if !load {
			panic(illegalError{instr: c.cur})
		}
		addr = c.a[reg]
	case 4:
		if load {
			panic(illegalError{instr: c.cur})
		}
		addr = c.a[reg]
	case 5:
		addr = uint32(int32(c.a[reg]) + int32(int16(c.fetchExt())))
	case 6:
		ext := c.fetchExt()
		addr = uint32(int32(c.a[reg]) + int32(int8(ext)) + int32(c.indexValue(ext)))
	case 7:
		switch reg {
		case 0:
			addr = uint32(int32(int16(c.fetchExt())))
		case 1:
			hi := uint32(c.fetchExt())
			lo := uint32(c.fetchExt())
			addr = hi<<16 | lo
		default:
			panic(illegalError{instr: c.cur})
		}
	default:
		panic(illegalError{instr: c.cur})
	}
	count := 0
	if !load && mode == 4 {
		for bit := 0; bit < 16; bit++ {
			if mask>>bit&1 == 0 {
				continue
			}
			count++
			addr -= uint32(size)
			c.writeMem(addr, regByIndex(c, 15-bit), size)
		}
		c.a[reg] = addr
		return 8 + count*8
	}
	for bit := 0; bit < 16; bit++ {
		if mask>>bit&1 == 0 {
			continue
		}
		count++
		if load {
			setRegByIndex(c, bit, c.readMem(addr, size), size)
		} else {
			c.writeMem(addr, regByIndex(c, bit), size)
		}
		addr += uint32(size)
	}
	if load && mode == 3 {
		c.a[reg] = addr
	}
	return 8 + count*8
}

// execTest handles TST (sizes in bits 7 to 6) and TAS
// (size field 3). TAS reads the byte, sets N and Z from
// the original value, then sets bit 7 and writes back;
// in this model the read modify write is indivisible
// because Step is the unit of interleaving.
func (c *CPU) execTest(op uint16) int {
	if op&0xFFC0 == 0x4AC0 { // TAS
		e := c.decodeEA(op, 1)
		if e.an || e.imm {
			panic(illegalError{instr: c.cur})
		}
		val := c.read(e, 1)
		c.logicFlags(val, 1)
		c.write(e, val|0x80, 1)
		return 4 + e.cycles
	}
	sizeCode := int((op >> 6) & 3)
	if sizeCode == 3 {
		panic(illegalError{instr: c.cur})
	}
	size := opSize(sizeCode)
	e := c.decodeEA(op, size)
	if e.an {
		panic(illegalError{instr: c.cur})
	}
	c.logicFlags(c.read(e, size), size)
	return 4 + e.cycles
}

// execControl handles the 0x4E high byte: TRAP, LINK,
// UNLK, MOVE USP, RESET, NOP, STOP, RTE, RTS, TRAPV,
// RTR, JSR, and JMP.
func (c *CPU) execControl(op uint16) int {
	switch {
	case op&0xFFF0 == 0x4E40: // TRAP
		c.exception(32+int(op&0xF), c.pc)
		return 34
	case op&0xFFF8 == 0x4E50: // LINK
		reg := int(op & 7)
		disp := int32(int16(c.fetchExt()))
		c.push32(c.a[reg])
		c.a[reg] = c.a[7]
		c.a[7] = uint32(int32(c.a[7]) + disp)
		return 16
	case op&0xFFF8 == 0x4E58: // UNLK
		reg := int(op & 7)
		c.a[7] = c.a[reg]
		c.a[reg] = c.pop32()
		return 12
	case op&0xFFF0 == 0x4E60: // MOVE USP, supervisor only
		if !c.supervisor() {
			panic(privError{instr: c.cur})
		}
		reg := int(op & 7)
		if op&0x0008 != 0 {
			c.a[reg] = c.otherSP // USP to An; S is set here
		} else {
			c.otherSP = c.a[reg] // An to USP
		}
		return 4
	case op == 0x4E70: // RESET
		if !c.supervisor() {
			panic(privError{instr: c.cur})
		}
		if c.ResetCallback != nil {
			c.ResetCallback()
		}
		return 132
	case op == 0x4E71: // NOP
		return 4
	case op == 0x4E72: // STOP
		if !c.supervisor() {
			panic(privError{instr: c.cur})
		}
		imm := c.fetchExt()
		c.setSR(imm)
		c.halted = true
		return 4
	case op == 0x4E73: // RTE
		if !c.supervisor() {
			// Privilege is checked before anything is
			// popped, per the manual.
			panic(privError{instr: c.cur})
		}
		// On the base 68000 an RTE from a group 0 frame
		// (address or bus error) is documented as not
		// reliably restartable; this core pops the
		// plain 6 byte SR and PC frame regardless of
		// frame type, and handlers are expected to
		// reset or loop after a group 0 exception.
		newSR := c.pop16()
		newPC := c.pop32()
		c.setSR(newSR)
		c.pc = newPC
		return 20
	case op == 0x4E75: // RTS
		c.pc = c.pop32()
		return 16
	case op == 0x4E76: // TRAPV
		if c.sr&srV != 0 {
			c.exception(7, c.pc)
			return 34
		}
		return 4
	case op == 0x4E77: // RTR
		ccr := c.pop16()
		newPC := c.pop32()
		c.sr = (c.sr & 0xFF00) | (ccr & 0x1F)
		c.pc = newPC
		return 20
	case op&0xFFC0 == 0x4E80: // JSR
		addr := c.controlAddr(op)
		c.push32(c.pc) // past every extension word
		c.pc = addr
		return 16
	case op&0xFFC0 == 0x4EC0: // JMP
		c.pc = c.controlAddr(op)
		return 8
	}
	panic(illegalError{instr: c.cur})
}

// ---- group E: shifts and rotates ----

// doShift shifts or rotates val at size by count,
// setting flags per the manual tables. kind is 0
// arithmetic, 1 logical, 2 rotate through X, 3 rotate.
// dir is 0 right, 1 left. For a register count of 0,
// X is unchanged, C is cleared, and V is cleared; this
// falls out of the loop form below except for X on the
// non ROX kinds, which the caller of the loop restores.
func (c *CPU) doShift(kind, dir, count int, val uint32, size int) uint32 {
	mask := sizeMask(size)
	res := val & mask
	if count == 0 {
		c.setNZ(res, size)
		c.setFlag(srV, false)
		c.setFlag(srC, false)
		return res
	}
	bits := int(sizeBits(size))
	sb := signBit(size)
	oldX := c.getX()
	x := oldX
	var lastOut uint32
	vSet := false
	for i := 0; i < count; i++ {
		if dir == 1 {
			out := (res >> (bits - 1)) & 1
			lastOut = out
			signBefore := res & sb
			switch kind {
			case 0, 1: // AS, LS
				res = (res << 1) & mask
				if kind == 0 && (res&sb) != signBefore {
					vSet = true
				}
			case 2: // ROX
				res = ((res << 1) | x) & mask
				x = out
			case 3: // RO
				res = ((res << 1) | out) & mask
			}
		} else {
			out := res & 1
			lastOut = out
			switch kind {
			case 0: // AS: sign bit replicates
				res = (res >> 1) | (res & sb)
			case 1: // LS
				res >>= 1
			case 2: // ROX
				res = (res >> 1) | (x << (bits - 1))
				x = out
			case 3: // RO
				res = (res >> 1) | (out << (bits - 1))
			}
		}
	}
	c.setNZ(res, size)
	c.setFlag(srC, lastOut != 0)
	if kind == 2 {
		c.setFlag(srX, x != 0)
	} else {
		c.setFlag(srX, lastOut != 0)
	}
	// V is set only by ASL, when the sign bit changed
	// value at any point during the shift.
	c.setFlag(srV, kind == 0 && dir == 1 && vSet)
	_ = oldX
	return res
}

func (c *CPU) execShift(op uint16) int {
	sizeCode := int((op >> 6) & 3)
	dir := int((op >> 8) & 1)
	if sizeCode == 3 {
		// Memory form: word only, count of one, the
		// kind in bits 11 to 9 as type plus direction:
		// 0 AS, 1 LS, 2 ROX, 3 RO with bit 8 the way.
		kind := int((op >> 9) & 3)
		e := c.decodeEA(op, 2)
		if !e.mem {
			panic(illegalError{instr: c.cur})
		}
		res := c.doShift(kind, dir, 1, c.readMem(e.addr, 2), 2)
		c.writeMem(e.addr, res, 2)
		return 8 + e.cycles
	}
	size := opSize(sizeCode)
	kind := int((op >> 3) & 3)
	reg := int(op & 7)
	var count int
	if op&0x0020 != 0 {
		count = int(c.d[(op>>9)&7] % 64)
	} else {
		count = int((op >> 9) & 7)
		if count == 0 {
			count = 8
		}
	}
	res := c.doShift(kind, dir, count, c.d[reg], size)
	c.write(dnEA(reg), res, size)
	return 6 + 2*count
}
