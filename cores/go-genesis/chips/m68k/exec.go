package m68k

// Instruction decode and execution. The 68000 opcode space is organized
// in 16 lines by the top nibble; the dispatcher mirrors that map, and
// each line decoder applies the manual's rules for its family,
// including which addressing modes are legal (an operand outside its
// instruction's class raises the illegal-instruction exception).

// execute runs one already-fetched opcode and returns its cycle count.
// Exceptions raised by the instruction itself (TRAP, CHK, divide by
// zero, privilege violation, ...) return their entry cost as the total.
func (c *CPU) execute(op uint16) int {
	switch op >> 12 {
	case 0x0:
		return c.execLine0(op)
	case 0x1:
		return c.execMove(op, szB)
	case 0x2:
		return c.execMove(op, szL)
	case 0x3:
		return c.execMove(op, szW)
	case 0x4:
		return c.execLine4(op)
	case 0x5:
		return c.execLine5(op)
	case 0x6:
		return c.execBranch(op)
	case 0x7:
		return c.execMoveQ(op)
	case 0x8:
		return c.execLine8(op)
	case 0x9:
		return c.execLine9D(op, false) // SUB family
	case 0xA:
		return c.enterExceptionAt(vecLineA, c.instrPC, 34)
	case 0xB:
		return c.execLineB(op)
	case 0xC:
		return c.execLineC(op)
	case 0xD:
		return c.execLine9D(op, true) // ADD family
	case 0xE:
		return c.execShifts(op)
	case 0xF:
		return c.enterExceptionAt(vecLineF, c.instrPC, 34)
	}
	return c.illegal(op)
}

func (c *CPU) illegal(op uint16) int { return c.enterExceptionAt(vecIllegal, c.instrPC, 34) }
func (c *CPU) privilege() int        { return c.enterExceptionAt(vecPrivilege, c.instrPC, 34) }
func (c *CPU) supervisor() bool      { return c.sr&srS != 0 }

// --- line 0: immediate ops, bit ops, MOVEP --------------------------

func (c *CPU) execLine0(op uint16) int {
	hi := (op >> 8) & 0xF // bits 11-8
	mode, reg := eaFields(op)
	size := int((op >> 6) & 3)
	switch hi {
	case 0x0, 0x2, 0xA: // ORI, ANDI, EORI
		if size == 3 {
			return c.illegal(op)
		}
		return c.execLogicImm(op, hi, mode, reg, size)
	case 0x4, 0x6, 0xC: // SUBI, ADDI, CMPI
		if size == 3 {
			return c.illegal(op)
		}
		return c.execArithImm(op, hi, mode, reg, size)
	case 0x8: // static bit ops: bit number from an extension word
		bop := int((op >> 6) & 3)
		bitNum := uint32(c.fetch16() & 0xFF)
		return c.execBitOp(op, bop, bitNum, mode, reg, true)
	default: // hi odd: bit 8 set -> MOVEP or dynamic bit op
		if mode == 1 {
			return c.execMoveP(op)
		}
		bop := int((op >> 6) & 3)
		bitNum := c.d[(op>>9)&7]
		return c.execBitOp(op, bop, bitNum, mode, reg, false)
	}
}

// execLogicImm implements ORI/ANDI/EORI, including the immediate-to-CCR
// and immediate-to-SR forms (EA field 111100). The SR forms are
// privileged. kind: 0x0 ORI, 0x2 ANDI, 0xA EORI (bits 11-8 of the opcode).
func (c *CPU) execLogicImm(op uint16, kind uint16, mode, reg, size int) int {
	if mode == 7 && reg == 4 {
		// Immediate operand is the extension word itself; byte size
		// targets the CCR, word size targets the SR.
		switch size {
		case szB:
			v := c.fetch16() & 0xFF
			ccr := uint16(c.sr & 0xFF)
			switch kind {
			case 0x0:
				ccr |= v
			case 0x2:
				ccr &= v
			case 0xA:
				ccr ^= v
			}
			c.setCCR(ccr)
			return 20
		case szW:
			v := c.fetch16()
			if !c.supervisor() {
				return c.privilege()
			}
			var nv uint16
			switch kind {
			case 0x0:
				nv = c.sr | v
			case 0x2:
				nv = c.sr & v
			case 0xA:
				nv = c.sr ^ v
			}
			c.applySR(nv)
			return 20
		}
		return c.illegal(op)
	}
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	var imm uint32
	switch size {
	case szB:
		imm = uint32(c.fetch16() & 0xFF)
	case szW:
		imm = uint32(c.fetch16())
	case szL:
		imm = c.fetch32()
	}
	o := c.resolveEA(mode, reg, size)
	v := c.opRead(o, size)
	var res uint32
	switch kind {
	case 0x0:
		res = v | imm
	case 0x2:
		res = v & imm
	case 0xA:
		res = v ^ imm
	}
	res &= sizeMask(size)
	c.flagsLogic(res, size)
	c.opWrite(o, size, res)
	if mode == 0 {
		if size == szL {
			return 12
		}
		return 8
	}
	if size == szL {
		return 20 + o.cyc
	}
	return 12 + o.cyc
}

// execArithImm implements SUBI/ADDI/CMPI. kind: 0x4 SUBI, 0x6 ADDI,
// 0xC CMPI (bits 11-8 of the opcode).
func (c *CPU) execArithImm(op uint16, kind uint16, mode, reg, size int) int {
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	var imm uint32
	switch size {
	case szB:
		imm = uint32(c.fetch16() & 0xFF)
	case szW:
		imm = uint32(c.fetch16())
	case szL:
		imm = c.fetch32()
	}
	o := c.resolveEA(mode, reg, size)
	v := c.opRead(o, size)
	switch kind {
	case 0x4: // SUBI
		res := c.flagsSub(v, imm, size, 0, false)
		c.opWrite(o, size, res)
	case 0x6: // ADDI
		res := c.flagsAdd(v, imm, size, 0, false)
		c.opWrite(o, size, res)
	case 0xC: // CMPI
		c.flagsCmp(v, imm, size)
		c.commitPost(o)
	}
	if mode == 0 {
		if size == szL {
			return 12
		}
		return 8
	}
	if size == szL {
		return 20 + o.cyc
	}
	return 12 + o.cyc
}

// execBitOp implements BTST/BCHG/BCLR/BSET for both the static
// (immediate bit number) and dynamic (register bit number) forms.
// bop: 0 BTST, 1 BCHG, 2 BCLR, 3 BSET. Only Z is affected: it is set to
// the complement of the tested bit.
func (c *CPU) execBitOp(op uint16, bop int, bitNum uint32, mode, reg int, static bool) int {
	if mode == 0 {
		// Data register: the bit number is modulo 32.
		bit := bitNum & 31
		old := c.d[reg]
		c.sr &^= srZ
		if (old>>bit)&1 == 0 {
			c.sr |= srZ
		}
		switch bop {
		case 1:
			old ^= uint32(1) << bit
		case 2:
			old &^= uint32(1) << bit
		case 3:
			old |= uint32(1) << bit
		}
		if bop != 0 {
			c.d[reg] = old
		}
		// Documented: BTST 6 (dynamic) or 10 (static); others 8.
		if bop == 0 {
			if static {
				return 10
			}
			return 6
		}
		return 8
	}
	// Memory: byte operand, bit number modulo 8.
	if bop == 0 {
		if !eaReadable(mode, reg) || (mode == 7 && reg == 4) {
			return c.illegal(op)
		}
	} else if !eaAlterableMem(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, szB)
	bit := bitNum & 7
	if bop == 0 {
		v := c.readSrc(o, szB)
		c.sr &^= srZ
		if (v>>bit)&1 == 0 {
			c.sr |= srZ
		}
		// Documented: 4 + EA (dynamic) or 8 + EA (static).
		if static {
			return 8 + o.cyc
		}
		return 4 + o.cyc
	}
	v := c.opRead(o, szB)
	c.sr &^= srZ
	if (v>>bit)&1 == 0 {
		c.sr |= srZ
	}
	switch bop {
	case 1:
		v ^= uint32(1) << bit
	case 2:
		v &^= uint32(1) << bit
	case 3:
		v |= uint32(1) << bit
	}
	c.opWrite(o, szB, v)
	return 8 + o.cyc
}

// execMoveP implements MOVEP: a register-to-memory transfer that moves
// a word or long one byte at a time to every other byte address,
// starting at (d16,An). Direction bit 7: 0 = memory to register,
// 1 = register to memory. Size bit 6: 0 = word, 1 = long.
func (c *CPU) execMoveP(op uint16) int {
	dreg := int((op >> 9) & 7)
	areg := int(op & 7)
	disp := int32(int16(c.fetch16()))
	addr := c.a[areg] + uint32(disp)
	n := 2
	if op&0x40 != 0 {
		n = 4
	}
	if op&0x80 != 0 {
		// Register to memory, most significant byte first.
		v := c.d[dreg]
		shift := uint((n - 1) * 8)
		for i := 0; i < n; i++ {
			c.wr8(addr+uint32(2*i), uint8(v>>shift))
			shift -= 8
		}
	} else {
		// Memory to register.
		var v uint32
		for i := 0; i < n; i++ {
			v = (v << 8) | uint32(c.rd8(addr+uint32(2*i)))
		}
		if n == 2 {
			c.d[dreg] = (c.d[dreg] &^ 0xFFFF) | v
		} else {
			c.d[dreg] = v
		}
	}
	if n == 4 {
		return 24
	}
	return 16
}

// --- MOVE / MOVEA ----------------------------------------------------

func (c *CPU) execMove(op uint16, size int) int {
	dstReg := int((op >> 9) & 7)
	dstMode := int((op >> 6) & 7)
	srcMode, srcReg := eaFields(op)
	if dstMode == 1 && size == szB {
		return c.illegal(op) // MOVE.B to an address register
	}
	if dstMode == 7 && dstReg > 1 {
		return c.illegal(op) // destination must be alterable memory
	}
	src := c.resolveEA(srcMode, srcReg, size)
	v := c.readSrc(src, size)
	dst := c.resolveEA(dstMode, dstReg, size)
	if dstMode == 1 {
		// MOVEA: no flags; a word source sign-extends.
		if size == szW {
			c.a[dstReg] = uint32(int32(int16(uint16(v))))
		} else {
			c.a[dstReg] = v
		}
		return 4 + src.cyc + dst.cyc
	}
	c.opWrite(dst, size, v)
	c.flagsLogic(v, size)
	return 4 + src.cyc + dst.cyc
}

// --- line 4: the miscellaneous group ---------------------------------

func (c *CPU) execLine4(op uint16) int {
	bits86 := (op >> 6) & 7 // bits 8-6
	mode, reg := eaFields(op)
	if bits86 == 6 {
		return c.execChk(op, mode, reg)
	}
	if bits86 == 7 {
		// LEA
		dstReg := int((op >> 9) & 7)
		if !eaControl(mode, reg) {
			return c.illegal(op)
		}
		o := c.resolveEA(mode, reg, szW)
		c.a[dstReg] = o.addr
		return leaCycles(mode, reg)
	}
	fam := (op >> 8) & 0xF
	ss := int((op >> 6) & 3)
	switch fam {
	case 0x0:
		if ss == 3 {
			return c.execMoveFromSR(op, mode, reg)
		}
		return c.execUnary(op, ss, mode, reg, unaryNegx)
	case 0x2:
		return c.execUnary(op, ss, mode, reg, unaryClr)
	case 0x4:
		if ss == 3 {
			return c.execMoveToCCR(op, mode, reg)
		}
		return c.execUnary(op, ss, mode, reg, unaryNeg)
	case 0x6:
		if ss == 3 {
			return c.execMoveToSR(op, mode, reg)
		}
		return c.execUnary(op, ss, mode, reg, unaryNot)
	case 0x8:
		// Bit 7 set with a non-register EA is MOVEM (registers to
		// memory); with a register-direct EA the same bit pattern is
		// EXT.W/EXT.L, whose mode field is always 000.
		if op&0x0080 != 0 && mode != 0 {
			return c.execMovem(op, false)
		}
		switch ss {
		case 0:
			return c.execNBCD(op, mode, reg)
		case 1:
			if mode == 0 {
				// SWAP
				v := c.d[reg]
				v = (v << 16) | (v >> 16)
				c.d[reg] = v
				c.flagsLogic(v, szL)
				return 4
			}
			// PEA
			if !eaControl(mode, reg) {
				return c.illegal(op)
			}
			o := c.resolveEA(mode, reg, szW)
			c.push32(o.addr)
			return leaCycles(mode, reg) + 8
		case 2: // EXT.W
			if mode != 0 {
				return c.illegal(op)
			}
			v := uint32(int32(int8(uint8(c.d[reg]))))
			c.d[reg] = (c.d[reg] &^ 0xFFFF) | (v & 0xFFFF)
			c.flagsLogic(v, szW)
			return 4
		case 3: // EXT.L
			if mode != 0 {
				return c.illegal(op)
			}
			v := uint32(int32(int16(uint16(c.d[reg]))))
			c.d[reg] = v
			c.flagsLogic(v, szL)
			return 4
		}
	case 0xA:
		if ss == 3 {
			return c.execTAS(op, mode, reg)
		}
		return c.execTST(op, ss, mode, reg)
	case 0xC:
		if op&0x0080 != 0 {
			return c.execMovem(op, true)
		}
		return c.illegal(op)
	case 0xE:
		switch ss {
		case 1:
			return c.execLine4Control(op)
		case 2: // JSR
			if !eaControl(mode, reg) {
				return c.illegal(op)
			}
			o := c.resolveEA(mode, reg, szW)
			c.push32(c.pc)
			c.pc = o.addr
			return jmpCycles(mode, reg) + 8
		case 3: // JMP
			if !eaControl(mode, reg) {
				return c.illegal(op)
			}
			o := c.resolveEA(mode, reg, szW)
			c.pc = o.addr
			return jmpCycles(mode, reg)
		}
		return c.illegal(op)
	}
	return c.illegal(op)
}

// execLine4Control decodes the 0100 1110 01xx xxxx group: TRAP, LINK,
// UNLK, MOVE to/from USP, and the fixed control instructions.
func (c *CPU) execLine4Control(op uint16) int {
	switch {
	case op&0xFFF0 == 0x4E40: // TRAP #vector
		return c.enterException(32+int(op&0xF), 34)
	case op&0xFFF8 == 0x4E50: // LINK
		reg := int(op & 7)
		disp := int32(int16(c.fetch16()))
		c.push32(c.a[reg])
		c.a[reg] = c.a[7]
		c.a[7] += uint32(disp)
		return 16
	case op&0xFFF8 == 0x4E58: // UNLK
		reg := int(op & 7)
		c.a[7] = c.a[reg]
		c.a[reg] = c.pop32()
		return 12
	case op&0xFFF0 == 0x4E60: // MOVE USP (privileged)
		if !c.supervisor() {
			return c.privilege()
		}
		reg := int(op & 7)
		if op&0x8 != 0 {
			c.a[reg] = c.uspValue()
		} else {
			c.setUSPValue(c.a[reg])
		}
		return 4
	case op == 0x4E70: // RESET (privileged; the external reset line is not modeled)
		if !c.supervisor() {
			return c.privilege()
		}
		return 132
	case op == 0x4E71: // NOP
		return 4
	case op == 0x4E72: // STOP (privileged)
		imm := c.fetch16()
		if !c.supervisor() {
			return c.privilege()
		}
		c.applySR(imm)
		c.stopped = true
		return 4
	case op == 0x4E73: // RTE (privileged)
		if !c.supervisor() {
			return c.privilege()
		}
		newSR := c.rd16(c.a[7])
		newPC := c.rd32(c.a[7] + 2)
		c.a[7] += 6
		c.applySR(newSR)
		c.pc = newPC
		return 20
	case op == 0x4E75: // RTS
		c.pc = c.pop32()
		return 16
	case op == 0x4E76: // TRAPV
		if c.sr&srV != 0 {
			return c.enterException(vecTRAPV, 34)
		}
		return 4
	case op == 0x4E77: // RTR
		ccr := c.pop16()
		c.setCCR(ccr)
		c.pc = c.pop32()
		return 20
	}
	return c.illegal(op)
}

// uspValue/sspValue read and write the inactive-pointer storage,
// treating the active A7 correctly when the CPU is in user mode.
func (c *CPU) uspValue() uint32 {
	if c.sr&srS == 0 {
		return c.a[7]
	}
	return c.usp
}

func (c *CPU) setUSPValue(v uint32) {
	if c.sr&srS == 0 {
		c.a[7] = v
	} else {
		c.usp = v
	}
}

// execChk implements CHK (word form, the only size on the 68000): if
// the low word of Dn is negative or greater than the source operand,
// the CHK exception (vector 6) is taken, with N set exactly when the
// register was negative. The manual leaves Z, V and C undefined for
// CHK; on the trap path they are cleared here so the stacked frame
// is deterministic (this matches the observable behavior of the
// reference implementation the core was differential-tested against).
func (c *CPU) execChk(op uint16, mode, reg int) int {
	dstReg := int((op >> 9) & 7)
	if !eaReadable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, szW)
	upper := int16(uint16(c.readSrc(o, szW)))
	dv := int16(uint16(c.d[dstReg]))
	if dv < 0 {
		c.sr = (c.sr | srN) &^ (srZ | srV | srC)
		return c.enterException(vecCHK, 44)
	}
	if dv > upper {
		c.sr &^= srN | srZ | srV | srC
		return c.enterException(vecCHK, 44)
	}
	// No trap: N is clear (the value is non-negative here by
	// construction), Z records equality with the bound, and V and C
	// are cleared; X is preserved. The manual leaves CHK's Z, V and C
	// undefined; a flag probe against the reference implementation
	// (grid over values, bounds and preset CCR) showed exactly this
	// rule, including in the stacked frame on the trap path.
	c.sr &^= srN | srZ | srV | srC
	if dv == upper {
		c.sr |= srZ
	}
	return 12 + o.cyc
}

type unaryKind int

const (
	unaryNegx unaryKind = iota
	unaryClr
	unaryNeg
	unaryNot
)

// execUnary implements NEGX, CLR, NEG and NOT against a data-alterable
// operand.
func (c *CPU) execUnary(op uint16, size int, mode, reg int, kind unaryKind) int {
	if size > szL {
		return c.illegal(op) // the ss == 11 encodings are the SR moves, handled by callers
	}
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, size)
	v := c.opRead(o, size)
	var res uint32
	switch kind {
	case unaryNegx:
		res = c.flagsSub(0, v, size, c.xBit(), true)
	case unaryClr:
		res = 0
		c.sr = (c.sr &^ (srN | srV | srC)) | srZ
	case unaryNeg:
		res = c.flagsSub(0, v, size, 0, false)
	case unaryNot:
		res = (^v) & sizeMask(size)
		c.flagsLogic(res, size)
	}
	c.opWrite(o, size, res)
	if mode == 0 {
		if size == szL {
			return 6
		}
		return 4
	}
	if size == szL {
		return 12 + o.cyc
	}
	return 8 + o.cyc
}

func (c *CPU) execMoveFromSR(op uint16, mode, reg int) int {
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, szW)
	c.opWrite(o, szW, uint32(c.sr))
	if mode == 0 {
		return 6
	}
	return 8 + o.cyc
}

func (c *CPU) execMoveToCCR(op uint16, mode, reg int) int {
	if !eaReadable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, szW)
	v := c.readSrc(o, szW)
	c.setCCR(uint16(v))
	return 12 + o.cyc
}

func (c *CPU) execMoveToSR(op uint16, mode, reg int) int {
	if !eaReadable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, szW)
	v := c.readSrc(o, szW)
	if !c.supervisor() {
		return c.privilege()
	}
	c.applySR(uint16(v))
	return 12 + o.cyc
}

// execNBCD implements NBCD: 0 - operand - X in BCD.
func (c *CPU) execNBCD(op uint16, mode, reg int) int {
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, szB)
	v := c.opRead(o, szB)
	res := c.bcdSub(0, v)
	c.opWrite(o, szB, res)
	if mode == 0 {
		return 6
	}
	return 8 + o.cyc
}

// execTAS implements TAS: test a byte (N/Z from it, V and C cleared)
// and set its high bit. The official ILLEGAL opcode (0x4AFC) shares the
// TAS encoding with an immediate operand and raises vector 4.
func (c *CPU) execTAS(op uint16, mode, reg int) int {
	if mode == 7 && reg == 4 {
		return c.illegal(op)
	}
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, szB)
	v := c.opRead(o, szB)
	c.flagsLogic(v, szB)
	c.opWrite(o, szB, v|0x80)
	if mode == 0 {
		return 4
	}
	return 10 + o.cyc
}

// execTST implements TST. On the base 68000 the operand class is data
// registers and alterable memory (no address register, PC-relative, or
// immediate forms).
func (c *CPU) execTST(op uint16, size int, mode, reg int) int {
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, size)
	v := c.readSrc(o, size)
	c.flagsLogic(v, size)
	return 4 + o.cyc
}

// --- line 5: ADDQ/SUBQ, Scc, DBcc ------------------------------------

func (c *CPU) execLine5(op uint16) int {
	mode, reg := eaFields(op)
	cond := int((op >> 8) & 0xF)
	if (op>>6)&3 == 3 {
		if mode == 1 {
			// DBcc: if the condition is false, decrement the low word
			// of Dn and branch unless it reached -1.
			base := c.pc
			disp := int32(int16(c.fetch16()))
			if c.condTrue(cond) {
				return 12
			}
			low := uint16(c.d[reg]) - 1
			c.d[reg] = (c.d[reg] &^ 0xFFFF) | uint32(low)
			if low != 0xFFFF {
				c.pc = uint32(int32(base) + disp)
				return 10
			}
			return 14
		}
		// Scc
		if !eaDataAlterable(mode, reg) {
			return c.illegal(op)
		}
		o := c.resolveEA(mode, reg, szB)
		var v uint32
		if c.condTrue(cond) {
			v = 0xFF
		}
		c.opWrite(o, szB, v)
		if mode == 0 {
			if v != 0 {
				return 4
			}
			return 6
		}
		return 8 + o.cyc
	}
	// ADDQ/SUBQ
	data := uint32((op >> 9) & 7)
	if data == 0 {
		data = 8
	}
	size := int((op >> 6) & 3)
	sub := op&0x0100 != 0
	if mode == 1 {
		// Quick ops to An: no flags, word and long only.
		if size == szB {
			return c.illegal(op)
		}
		if sub {
			c.a[reg] -= data
		} else {
			c.a[reg] += data
		}
		if size == szL {
			return 8
		}
		return 4
	}
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, size)
	v := c.opRead(o, size)
	var res uint32
	if sub {
		res = c.flagsSub(v, data, size, 0, false)
	} else {
		res = c.flagsAdd(v, data, size, 0, false)
	}
	c.opWrite(o, size, res)
	if mode == 0 {
		if size == szL {
			return 8
		}
		return 4
	}
	if size == szL {
		return 12 + o.cyc
	}
	return 8 + o.cyc
}

// --- line 6: branches -------------------------------------------------

func (c *CPU) execBranch(op uint16) int {
	cond := int((op >> 8) & 0xF)
	base := c.pc // displacement base: the word after the opcode
	disp8 := int8(uint8(op & 0xFF))
	var disp int32
	notTaken := 8
	if disp8 == 0 {
		disp = int32(int16(c.fetch16()))
		notTaken = 12
	} else {
		disp = int32(disp8)
	}
	if cond == 1 {
		// BSR: push the return address (after the whole instruction).
		c.push32(c.pc)
		c.pc = uint32(int32(base) + disp)
		return 18
	}
	if c.condTrue(cond) {
		c.pc = uint32(int32(base) + disp)
		return 10
	}
	return notTaken
}

// --- line 7: MOVEQ ----------------------------------------------------

func (c *CPU) execMoveQ(op uint16) int {
	if op&0x0100 != 0 {
		return c.illegal(op)
	}
	reg := int((op >> 9) & 7)
	v := uint32(int32(int8(uint8(op & 0xFF))))
	c.d[reg] = v
	c.flagsLogic(v, szL)
	return 4
}
