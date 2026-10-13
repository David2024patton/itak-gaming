package cpu6502

// Instruction execution. The opcode space is decoded the way the
// hardware lays it out (the aaabbbcc bit fields), with an explicit
// special-case switch in exec for the instructions that live
// outside the grid: branches, jumps, returns, stack and register
// operations, the KIL jams, and the one-byte NOPs. The grid slots
// that look empty on a datasheet are not empty on the silicon:
// they hold the unofficial opcodes (the grid's third column is
// almost entirely unofficial), and real NES games use several of
// them, so they are implemented to their documented behavior from
// the NESdev wiki's unofficial opcode reference.
//
// Cycle counts fall out of the addressing helpers below: each one
// performs the same bus accesses in the same order as the
// hardware, including the dummy read at the unfixed address on
// indexed accesses (charged always for stores and read-modify-
// write, and on reads only when the index crosses a page, because
// without a crossing the unfixed address IS the target) and the
// dummy write of the old value in read-modify-write instructions.

// operation identifies an instruction's action.
type operation int

const (
	opInvalid operation = iota
	opORA
	opAND
	opEOR
	opADC
	opSTA
	opLDA
	opCMP
	opSBC
	opASL
	opROL
	opLSR
	opROR
	opSTX
	opLDX
	opDEC
	opINC
	opBIT
	opSTY
	opLDY
	opCPY
	opCPX
	// Unofficial operations.
	opSLO // ASL memory, then ORA
	opRLA // ROL memory, then AND
	opSRE // LSR memory, then EOR
	opRRA // ROR memory, then ADC
	opSAX // store A AND X
	opLAX // load A and X together
	opDCP // DEC memory, then CMP
	opISC // INC memory, then SBC (also called ISB)
	opNOP // the multi-byte NOPs: fetch and discard
	opANC // AND, then copy N to C
	opALR // AND, then LSR on A
	opARR // AND, then ROR on A, with the special V and C rules
	opXAA // A = (A OR magic) AND X AND immediate (unstable on hardware)
	opAXS // X = (A AND X) minus immediate, CMP-style flags (also SBX)
	opLXA // A and X = (A OR magic) AND immediate (also ATX)
	opAHX // store A AND X AND (address high byte + 1)
	opTAS // SP = A AND X, then store SP AND (high byte + 1)
	opSHX // store X AND (high byte + 1)
	opSHY // store Y AND (high byte + 1)
	opLAS // A, X, SP = memory AND SP
	opKIL // jam the CPU until reset
)

// addrMode identifies an addressing mode.
type addrMode int

const (
	modeInvalid addrMode = iota
	modeImm
	modeZP
	modeZPX
	modeZPY
	modeAbs
	modeAbsX
	modeAbsY
	modeIndX
	modeIndY
	modeAcc
)

// magic is the OR constant the unstable XAA and LXA opcodes pick
// up from the floating bus. The value varies between machines and
// moments on real hardware (it depends on the data bus state),
// which is why these two opcodes are called unstable. Blargg's
// instr_test-v5 checksum, calibrated on NES hardware, pins LXA
// down: with 0xFF the operation loads the operand outright and
// the checksum passes, while the often-quoted 0xEE fails it, so
// 0xFF is what this core uses. Games that rely on a different
// value are relying on luck either way.
const unstableMagic = 0xFF

// decode maps a grid opcode to its operation and addressing mode.
// exec handles the off-grid opcodes first, so decode only sees
// opcodes that follow the aaabbbcc layout.
func decode(op uint8) (operation, addrMode) {
	cc := op & 3
	aaa := (op >> 5) & 7
	bbb := (op >> 2) & 7

	gridModes := [...]addrMode{modeIndX, modeZP, modeImm, modeAbs, modeIndY, modeZPX, modeAbsY, modeAbsX}

	switch cc {
	case 1: // the ALU grid
		ops := [...]operation{opORA, opAND, opEOR, opADC, opSTA, opLDA, opCMP, opSBC}
		return ops[aaa], gridModes[bbb]

	case 3: // the unofficial grid (same layout, third column)
		if bbb == 2 {
			// The immediate column holds the one-off unofficial
			// immediate instructions, plus the duplicate SBC.
			imm := [...]operation{opANC, opANC, opALR, opARR, opXAA, opLXA, opAXS, opSBC}
			return imm[aaa], modeImm
		}
		ops := [...]operation{opSLO, opRLA, opSRE, opRRA, opSAX, opLAX, opDCP, opISC}
		o := ops[aaa]
		switch op {
		case 0x93: // the SAX (indirect),Y slot is AHX
			return opAHX, modeIndY
		case 0x9F: // the SAX absolute,X slot position holds AHX absolute,Y
			return opAHX, modeAbsY
		case 0x9B: // the SAX absolute-Y slot at bbb 6 is TAS
			return opTAS, modeAbsY
		case 0xBB: // the LAX absolute-Y slot at bbb 6 is LAS
			return opLAS, modeAbsY
		}
		m := gridModes[bbb]
		// SAX and LAX index zero page by Y where the rest of the
		// grid indexes by X.
		if (o == opSAX || o == opLAX) && m == modeZPX {
			m = modeZPY
		}
		// LAX absolute is likewise Y-indexed (0xBF), unlike the
		// cc=01 grid's absolute,X column.
		if o == opLAX && bbb == 7 {
			m = modeAbsY
		}
		return o, m

	case 2: // the shift and X grid
		ops := [...]operation{opASL, opROL, opLSR, opROR, opSTX, opLDX, opDEC, opINC}
		o := ops[aaa]
		switch bbb {
		case 0:
			return o, modeImm // only LDX reaches here; the rest are handled in exec
		case 1:
			return o, modeZP
		case 2:
			return o, modeAcc // the shifts; TAX and friends are handled in exec
		case 3:
			return o, modeAbs
		case 5:
			if o == opSTX || o == opLDX {
				return o, modeZPY
			}
			return o, modeZPX
		case 7:
			if o == opLDX {
				return o, modeAbsY
			}
			if o == opSTX {
				return opSHX, modeAbsY // 0x9E is SHX, not a store
			}
			return o, modeAbsX
		}
		return opKIL, modeInvalid // bbb 4 and 6 are the KIL and implied slots

	default: // cc 0: BIT, the stores and loads of Y, the compares
		switch bbb {
		case 0:
			switch aaa {
			case 5:
				return opLDY, modeImm
			case 6:
				return opCPY, modeImm
			case 7:
				return opCPX, modeImm
			}
		case 1:
			switch aaa {
			case 1:
				return opBIT, modeZP
			case 4:
				return opSTY, modeZP
			case 5:
				return opLDY, modeZP
			case 6:
				return opCPY, modeZP
			case 7:
				return opCPX, modeZP
			}
			return opNOP, modeZP
		case 3:
			switch aaa {
			case 1:
				return opBIT, modeAbs
			case 4:
				return opSTY, modeAbs
			case 5:
				return opLDY, modeAbs
			case 6:
				return opCPY, modeAbs
			case 7:
				return opCPX, modeAbs
			}
			return opNOP, modeAbs
		case 5:
			switch aaa {
			case 4:
				return opSTY, modeZPX
			case 5:
				return opLDY, modeZPX
			}
			return opNOP, modeZPX
		case 7:
			if aaa == 5 {
				return opLDY, modeAbsX
			}
			if op == 0x9C {
				return opSHY, modeAbsX
			}
			return opNOP, modeAbsX
		}
		return opKIL, modeInvalid
	}
}

// --- flag and ALU helpers ---

func (c *CPU) setFlag(mask uint8, on bool) {
	if on {
		c.p |= mask
	} else {
		c.p &^= mask
	}
}

func (c *CPU) setZN(v uint8) {
	c.setFlag(FlagZ, v == 0)
	c.setFlag(FlagN, v&0x80 != 0)
}

func (c *CPU) ora(v uint8) { c.a |= v; c.setZN(c.a) }
func (c *CPU) and(v uint8) { c.a &= v; c.setZN(c.a) }
func (c *CPU) eor(v uint8) { c.a ^= v; c.setZN(c.a) }

// adc adds with carry, always in binary: on the NES CPU the
// decimal flag is a flag like any other but the arithmetic unit
// never looks at it.
func (c *CPU) adc(v uint8) {
	carry := uint16(0)
	if c.p&FlagC != 0 {
		carry = 1
	}
	sum := uint16(c.a) + uint16(v) + carry
	c.setFlag(FlagV, (c.a^uint8(sum))&(v^uint8(sum))&0x80 != 0)
	c.setFlag(FlagC, sum > 0xFF)
	c.a = uint8(sum)
	c.setZN(c.a)
}

// sbc subtracts with borrow by adding the ones' complement, the
// hardware's own trick.
func (c *CPU) sbc(v uint8) { c.adc(v ^ 0xFF) }

func (c *CPU) cmp(reg, v uint8) {
	c.setFlag(FlagC, reg >= v)
	c.setZN(reg - v)
}

func (c *CPU) asl(v uint8) uint8 {
	c.setFlag(FlagC, v&0x80 != 0)
	v <<= 1
	c.setZN(v)
	return v
}

func (c *CPU) rol(v uint8) uint8 {
	old := c.p&FlagC != 0
	c.setFlag(FlagC, v&0x80 != 0)
	v <<= 1
	if old {
		v |= 1
	}
	c.setZN(v)
	return v
}

func (c *CPU) lsr(v uint8) uint8 {
	c.setFlag(FlagC, v&1 != 0)
	v >>= 1
	c.setZN(v)
	return v
}

func (c *CPU) ror(v uint8) uint8 {
	old := c.p&FlagC != 0
	c.setFlag(FlagC, v&1 != 0)
	v >>= 1
	if old {
		v |= 0x80
	}
	c.setZN(v)
	return v
}

// shiftFn returns the single-operand transform an operation
// applies to memory (or, for the accumulator forms, to A).
func (c *CPU) shiftFn(o operation) func(uint8) uint8 {
	switch o {
	case opASL, opSLO:
		return c.asl
	case opROL, opRLA:
		return c.rol
	case opLSR, opSRE:
		return c.lsr
	case opROR, opRRA:
		return c.ror
	case opINC, opISC:
		return func(v uint8) uint8 { v++; c.setZN(v); return v }
	case opDEC, opDCP:
		return func(v uint8) uint8 { v--; c.setZN(v); return v }
	}
	return func(v uint8) uint8 { return v }
}

// --- addressing helpers ---
//
// Each helper starts after the opcode fetch and performs the
// operand accesses in hardware order. Read-class indexed helpers
// return the value directly when the index did not cross a page
// (the dummy read at the unfixed address already fetched it) and
// report haveVal false when a crossing means the caller must read
// the fixed address itself.

// eaZP fetches a zero-page operand address.
func (c *CPU) eaZP() uint16 { return uint16(c.fetch()) }

// eaZPIdx fetches a zero-page address and indexes it. The add
// cycle reads the un-indexed zero-page address, and the sum wraps
// inside the page: zero-page indexing can never cross into page 1.
func (c *CPU) eaZPIdx(idx uint8) uint16 {
	t := c.fetch()
	c.dummy(uint16(t))
	return uint16(t + idx)
}

// eaAbs fetches a 16-bit operand address.
func (c *CPU) eaAbs() uint16 {
	lo := c.fetch()
	hi := c.fetch()
	return uint16(hi)<<8 | uint16(lo)
}

// indexed applies an index to a base address with the hardware's
// unfixed-address dummy read. For write-class accesses the dummy
// is charged unconditionally; for read-class accesses the dummy
// read is the data read when the page does not cross.
func (c *CPU) indexed(base uint16, idx uint8, writeClass bool) (addr uint16, val uint8, haveVal bool) {
	addr = base + uint16(idx)
	unfixed := (base & 0xFF00) | (addr & 0x00FF)
	v := c.read(unfixed)
	if writeClass || addr&0xFF00 != base&0xFF00 {
		return addr, 0, false
	}
	return addr, v, true
}

func (c *CPU) eaAbsIdx(idx uint8, writeClass bool) (uint16, uint8, bool) {
	return c.indexed(c.eaAbs(), idx, writeClass)
}

// eaIndX resolves (indirect,X): the add cycle reads the pointer's
// own zero-page address, and both pointer bytes wrap in page 0.
func (c *CPU) eaIndX() uint16 {
	t := c.fetch()
	c.dummy(uint16(t))
	p := t + c.x
	lo := c.read(uint16(p))
	hi := c.read(uint16(p + 1))
	return uint16(hi)<<8 | uint16(lo)
}

// eaIndYBase fetches the (indirect),Y pointer (wrapping in page 0)
// and returns the base address; the caller applies the index.
func (c *CPU) eaIndYBase() uint16 {
	t := c.fetch()
	lo := c.read(uint16(t))
	hi := c.read(uint16(t + 1))
	return uint16(hi)<<8 | uint16(lo)
}

func (c *CPU) eaIndY(idx uint8, writeClass bool) (uint16, uint8, bool) {
	return c.indexed(c.eaIndYBase(), idx, writeClass)
}

// eaJmpInd resolves the JMP (indirect) pointer, including the
// famous page-wrap bug: the high byte comes from the start of the
// pointer's own page, not the next page.
func (c *CPU) eaJmpInd() uint16 {
	p := c.eaAbs()
	lo := c.read(p)
	hi := c.read((p & 0xFF00) | uint16(uint8(p)+1))
	return uint16(hi)<<8 | uint16(lo)
}

// readVal finishes a read-class indexed access.
func (c *CPU) readVal(addr uint16, val uint8, haveVal bool) uint8 {
	if haveVal {
		return val
	}
	return c.read(addr)
}

// rmw runs a read-modify-write at addr: read the value, write the
// old value back (the dummy write the hardware performs), then
// write the transformed value.
func (c *CPU) rmw(addr uint16, f func(uint8) uint8) uint8 {
	v := c.read(addr)
	c.write(addr, v)
	nv := f(v)
	c.write(addr, nv)
	return nv
}

// branch executes a conditional branch opcode; the offset byte
// has already been read as part of the fetch sequence here. A
// taken branch costs one extra cycle reading the fall-through
// address with the old high byte, plus another when the target
// crosses into a different page.
func (c *CPU) branch(op uint8) {
	off := int8(c.fetch())
	var flag uint8
	switch (op >> 6) & 3 {
	case 0:
		flag = FlagN
	case 1:
		flag = FlagV
	case 2:
		flag = FlagC
	default:
		flag = FlagZ
	}
	taken := (c.p&flag != 0) == (op&0x20 != 0)
	if !taken {
		return
	}
	target := uint16(int32(c.pc) + int32(off))
	unfixed := (c.pc & 0xFF00) | (target & 0x00FF)
	c.dummy(unfixed)
	if unfixed != target {
		c.dummy(target)
	}
	c.pc = target
}

// indexedRaw performs the indexed dummy read and reports the
// effective address and whether the index crossed a page. The
// unstable stores use it directly: their stored value can replace
// the address's high byte on a crossing.
func (c *CPU) indexedRaw(base uint16, idx uint8) (addr uint16, crossed bool) {
	addr = base + uint16(idx)
	unfixed := (base & 0xFF00) | (addr & 0x00FF)
	c.read(unfixed)
	return addr, addr&0xFF00 != base&0xFF00
}

// eaWrite resolves an address for a store or read-modify-write,
// charging the write-class dummy reads.
func (c *CPU) eaWrite(m addrMode) uint16 {
	switch m {
	case modeZP:
		return c.eaZP()
	case modeZPX:
		return c.eaZPIdx(c.x)
	case modeZPY:
		return c.eaZPIdx(c.y)
	case modeAbs:
		return c.eaAbs()
	case modeAbsX:
		addr, _, _ := c.eaAbsIdx(c.x, true)
		return addr
	case modeAbsY:
		addr, _, _ := c.eaAbsIdx(c.y, true)
		return addr
	case modeIndX:
		return c.eaIndX()
	case modeIndY:
		addr, _, _ := c.eaIndY(c.y, true)
		return addr
	}
	return 0
}

// readOperand fetches the operand value for a read-class
// instruction in the given mode.
func (c *CPU) readOperand(m addrMode) uint8 {
	switch m {
	case modeImm:
		return c.fetch()
	case modeZP:
		return c.read(c.eaZP())
	case modeZPX:
		return c.read(c.eaZPIdx(c.x))
	case modeZPY:
		return c.read(c.eaZPIdx(c.y))
	case modeAbs:
		return c.read(c.eaAbs())
	case modeAbsX:
		addr, v, have := c.eaAbsIdx(c.x, false)
		return c.readVal(addr, v, have)
	case modeAbsY:
		addr, v, have := c.eaAbsIdx(c.y, false)
		return c.readVal(addr, v, have)
	case modeIndX:
		return c.read(c.eaIndX())
	case modeIndY:
		addr, v, have := c.eaIndY(c.y, false)
		return c.readVal(addr, v, have)
	}
	return 0
}

// applyRead applies a read-class operation to a fetched value.
func (c *CPU) applyRead(o operation, v uint8) {
	switch o {
	case opORA:
		c.ora(v)
	case opAND:
		c.and(v)
	case opEOR:
		c.eor(v)
	case opADC:
		c.adc(v)
	case opSBC:
		c.sbc(v)
	case opCMP:
		c.cmp(c.a, v)
	case opLDA:
		c.a = v
		c.setZN(v)
	case opLAX:
		c.a = v
		c.x = v
		c.setZN(v)
	case opLDX:
		c.x = v
		c.setZN(v)
	case opLDY:
		c.y = v
		c.setZN(v)
	case opCPX:
		c.cmp(c.x, v)
	case opCPY:
		c.cmp(c.y, v)
	case opBIT:
		c.setFlag(FlagZ, c.a&v == 0)
		c.setFlag(FlagN, v&0x80 != 0)
		c.setFlag(FlagV, v&0x40 != 0)
	case opLAS:
		nv := v & c.sp
		c.a, c.x, c.sp = nv, nv, nv
		c.setZN(nv)
	case opNOP:
		// The operand was fetched and discarded.
	}
}

// storeVal returns the byte a store operation writes.
func (c *CPU) storeVal(o operation) uint8 {
	switch o {
	case opSTX:
		return c.x
	case opSTY:
		return c.y
	case opSAX:
		return c.a & c.x
	default: // opSTA
		return c.a
	}
}

// rmwPost applies the second half of a composite unofficial
// read-modify-write: the ALU operation on the value just written.
func (c *CPU) rmwPost(o operation, nv uint8) {
	switch o {
	case opSLO:
		c.ora(nv)
	case opRLA:
		c.and(nv)
	case opSRE:
		c.eor(nv)
	case opRRA:
		c.adc(nv)
	case opDCP:
		c.cmp(c.a, nv)
	case opISC:
		c.sbc(nv)
	}
}

// applyUnstableStore executes AHX, TAS, SHX, and SHY: the stored
// byte is ANDed with the base address's high byte plus one, and
// when the index crosses a page the address's high byte is
// replaced by the stored byte (the carry into the high byte is
// ANDed the same way). TAS also updates SP first.
func (c *CPU) applyUnstableStore(o operation, m addrMode) {
	var base uint16
	var idx uint8
	switch m {
	case modeAbsX:
		base, idx = c.eaAbs(), c.x
	case modeAbsY:
		base, idx = c.eaAbs(), c.y
	case modeIndY:
		base, idx = c.eaIndYBase(), c.y
	}
	addr, crossed := c.indexedRaw(base, idx)
	h := uint8(base>>8) + 1
	var v uint8
	switch o {
	case opAHX:
		v = c.a & c.x & h
	case opSHX:
		v = c.x & h
	case opSHY:
		v = c.y & h
	case opTAS:
		c.sp = c.a & c.x
		v = c.sp & h
	}
	if crossed {
		addr = (addr & 0x00FF) | (uint16(v) << 8)
	}
	c.write(addr, v)
}

// apply executes a grid operation once its mode is known.
func (c *CPU) apply(o operation, m addrMode) {
	switch o {
	// The unofficial immediate one-offs.
	case opANC:
		v := c.fetch()
		c.a &= v
		c.setZN(c.a)
		c.setFlag(FlagC, c.p&FlagN != 0)
	case opALR:
		v := c.fetch()
		c.a = c.lsr(c.a & v)
	case opARR:
		v := c.fetch()
		t := c.a & v
		r := t >> 1
		if c.p&FlagC != 0 {
			r |= 0x80
		}
		c.a = r
		c.setZN(r)
		// ARR's documented flags: C is bit 6 of the result, V is
		// bit 6 XOR bit 5.
		c.setFlag(FlagC, r&0x40 != 0)
		c.setFlag(FlagV, (r&0x40 != 0) != (r&0x20 != 0))
	case opXAA:
		v := c.fetch()
		c.a = (c.a | unstableMagic) & c.x & v
		c.setZN(c.a)
	case opLXA:
		v := c.fetch()
		c.a = (c.a | unstableMagic) & v
		c.x = c.a
		c.setZN(c.a)
	case opAXS:
		v := c.fetch()
		t := uint16(c.a & c.x)
		c.setFlag(FlagC, t >= uint16(v))
		c.x = uint8(t - uint16(v))
		c.setZN(c.x)

	case opAHX, opTAS, opSHX, opSHY:
		c.applyUnstableStore(o, m)

	case opSTA, opSTX, opSTY, opSAX:
		c.write(c.eaWrite(m), c.storeVal(o))

	case opASL, opROL, opLSR, opROR, opDEC, opINC,
		opSLO, opRLA, opSRE, opRRA, opDCP, opISC:
		if m == modeAcc {
			c.dummy(c.pc)
			c.a = c.shiftFn(o)(c.a)
			return
		}
		addr := c.eaWrite(m)
		nv := c.rmw(addr, c.shiftFn(o))
		c.rmwPost(o, nv)

	default: // read class, including opNOP (fetch and discard)
		c.applyRead(o, c.readOperand(m))
	}
}

// execBRK runs the BRK instruction: the padding byte is read and
// skipped, PC (pointing past it) and a B-set status byte are
// pushed, and the vector is fetched. An NMI latched by vector
// time hijacks the sequence to the NMI vector while keeping BRK's
// pushed bytes; the IRQ input shares BRK's vector anyway, so only
// NMI changes the outcome.
func (c *CPU) execBRK() {
	c.dummy(c.pc)
	c.pc++
	c.push(uint8(c.pc >> 8))
	c.push(uint8(c.pc))
	c.push(c.statusByte(true))
	c.p |= FlagI
	vector := uint16(VectorIRQ)
	if c.nmiPending {
		c.nmiPending = false
		vector = VectorNMI
	}
	lo := c.read(vector)
	hi := c.read(vector + 1)
	c.pc = uint16(hi)<<8 | uint16(lo)
}

// exec runs one instruction whose opcode byte has been fetched.
func (c *CPU) exec(op uint8) {
	switch op {
	case 0x00: // BRK
		c.execBRK()
		return
	case 0x20: // JSR
		lo := c.fetch()
		c.dummy(0x0100 | uint16(c.sp))
		c.push(uint8(c.pc >> 8))
		c.push(uint8(c.pc))
		hi := c.read(c.pc)
		c.pc = uint16(hi)<<8 | uint16(lo)
		return
	case 0x40: // RTI
		c.dummy(c.pc)
		c.dummy(0x0100 | uint16(c.sp))
		c.setP(c.pull())
		lo := c.pull()
		hi := c.pull()
		c.pc = uint16(hi)<<8 | uint16(lo)
		return
	case 0x60: // RTS
		c.dummy(c.pc)
		c.dummy(0x0100 | uint16(c.sp))
		lo := c.pull()
		hi := c.pull()
		c.pc = uint16(hi)<<8 | uint16(lo)
		c.dummy(c.pc)
		c.pc++
		return
	case 0x4C: // JMP absolute
		c.pc = c.eaAbs()
		return
	case 0x6C: // JMP indirect
		c.pc = c.eaJmpInd()
		return

	// Stack operations.
	case 0x08: // PHP
		c.dummy(c.pc)
		c.push(c.statusByte(true))
		return
	case 0x48: // PHA
		c.dummy(c.pc)
		c.push(c.a)
		return
	case 0x28: // PLP
		c.dummy(c.pc)
		c.dummy(0x0100 | uint16(c.sp))
		c.setP(c.pull())
		return
	case 0x68: // PLA
		c.dummy(c.pc)
		c.dummy(0x0100 | uint16(c.sp))
		c.a = c.pull()
		c.setZN(c.a)
		return

	// Flag operations.
	case 0x18:
		c.dummy(c.pc)
		c.setFlag(FlagC, false)
		return
	case 0x38:
		c.dummy(c.pc)
		c.setFlag(FlagC, true)
		return
	case 0x58:
		c.dummy(c.pc)
		c.setFlag(FlagI, false)
		return
	case 0x78:
		c.dummy(c.pc)
		c.setFlag(FlagI, true)
		return
	case 0xB8:
		c.dummy(c.pc)
		c.setFlag(FlagV, false)
		return
	case 0xD8:
		c.dummy(c.pc)
		c.setFlag(FlagD, false)
		return
	case 0xF8:
		c.dummy(c.pc)
		c.setFlag(FlagD, true)
		return

	// Register transfers and increments.
	case 0x88: // DEY
		c.dummy(c.pc)
		c.y--
		c.setZN(c.y)
		return
	case 0x8A: // TXA
		c.dummy(c.pc)
		c.a = c.x
		c.setZN(c.a)
		return
	case 0x98: // TYA
		c.dummy(c.pc)
		c.a = c.y
		c.setZN(c.a)
		return
	case 0x9A: // TXS (no flags)
		c.dummy(c.pc)
		c.sp = c.x
		return
	case 0xA8: // TAY
		c.dummy(c.pc)
		c.y = c.a
		c.setZN(c.y)
		return
	case 0xAA: // TAX
		c.dummy(c.pc)
		c.x = c.a
		c.setZN(c.x)
		return
	case 0xBA: // TSX
		c.dummy(c.pc)
		c.x = c.sp
		c.setZN(c.x)
		return
	case 0xC8: // INY
		c.dummy(c.pc)
		c.y++
		c.setZN(c.y)
		return
	case 0xCA: // DEX
		c.dummy(c.pc)
		c.x--
		c.setZN(c.x)
		return
	case 0xE8: // INX
		c.dummy(c.pc)
		c.x++
		c.setZN(c.x)
		return

	// NOP, official and the unofficial one-byte forms.
	case 0xEA, 0x1A, 0x3A, 0x5A, 0x7A, 0xDA, 0xFA:
		c.dummy(c.pc)
		return

	// KIL: the CPU jams until reset. PC steps back onto the
	// opcode so the Step loop's jam reads hit it, like the bus
	// does on hardware.
	case 0x02, 0x12, 0x22, 0x32, 0x42, 0x52,
		0x62, 0x72, 0x92, 0xB2, 0xD2, 0xF2:
		c.jammed = true
		c.pc--
		return

	// DOP: the two-byte NOPs in the immediate slots the grid
	// cannot use (STA immediate, ASL immediate, and friends).
	case 0x80, 0x82, 0x89, 0xC2, 0xE2:
		c.fetch()
		return
	}

	if op&0x1F == 0x10 { // the branch column
		c.branch(op)
		return
	}
	o, m := decode(op)
	c.apply(o, m)
}
