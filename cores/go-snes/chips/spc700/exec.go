// Instruction execution for the SPC700: the 256-opcode
// dispatch, the addressing-mode access helpers, and the
// operations themselves.
//
// The opcode map is highly regular, and the dispatch keeps
// that structure visible: the six 8-bit ALU operations share
// one column pattern across their opcode rows, the six
// shift/rotate/increment/decrement operations share another,
// the TCALLs and the direct-page bit operations decode their
// operand (vector number, bit number) out of the opcode
// itself, and the rest are listed opcode by opcode.
//
// Cycle counts are the published per-opcode values. Stores
// perform the dummy read of their destination that the
// documentation describes (see the package comment) before
// writing; MOV dp,dp and MOV (X)+,A are the documented
// exceptions and do not.
package spc700

// aluKind decodes which of the six shared-column ALU
// operations an opcode belongs to, from the opcode's row:
// the rows are OR, AND, EOR, CMP, ADC, SBC in order.
func aluKind(op uint8) int {
	return int(op>>5) & 7
}

const (
	aluOR = iota
	aluAND
	aluEOR
	aluCMP
	aluADC
	aluSBC
)

// alu computes one 8-bit ALU operation on a with operand b
// and sets the flags the operation documents. CMP discards
// its result; the caller decides whether the result is
// stored (in A, or back to a memory destination for the
// memory forms).
func (c *CPU) alu(kind int, a, b uint8) uint8 {
	switch kind {
	case aluOR:
		r := a | b
		c.setNZ(r)
		return r
	case aluAND:
		r := a & b
		c.setNZ(r)
		return r
	case aluEOR:
		r := a ^ b
		c.setNZ(r)
		return r
	case aluCMP:
		c.cmp8(a, b)
		return a
	case aluADC:
		carry := 0
		if c.flag(FlagC) {
			carry = 1
		}
		t := int(a) + int(b) + carry
		r := uint8(t)
		c.setFlag(FlagC, t > 0xFF)
		c.setFlag(FlagH, (a&0x0F)+(b&0x0F)+uint8(carry) > 0x0F)
		c.setFlag(FlagV, (a^b)&0x80 == 0 && (a^r)&0x80 != 0)
		c.setNZ(r)
		return r
	case aluSBC:
		borrow := 1
		if c.flag(FlagC) {
			borrow = 0
		}
		t := int(a) - int(b) - borrow
		r := uint8(t)
		c.setFlag(FlagC, t >= 0)
		c.setFlag(FlagH, int(a&0x0F)-int(b&0x0F)-borrow >= 0)
		c.setFlag(FlagV, (a^b)&0x80 != 0 && (a^r)&0x80 != 0)
		c.setNZ(r)
		return r
	}
	return a
}

// cmp8 sets the flags CMP documents (N, Z, C from a-b; V
// and H are untouched) and returns nothing else.
func (c *CPU) cmp8(a, b uint8) {
	r := a - b
	c.setFlag(FlagC, a >= b)
	c.setNZ(r)
}

// shiftKind decodes the shared shift/increment pattern the
// same way aluKind decodes the ALU pattern.
func shiftKind(op uint8) int {
	return int(op>>5) & 7
}

const (
	shiftASL = iota
	shiftROL
	shiftLSR
	shiftROR
	shiftDEC
	shiftINC
)

// shift computes one shift, rotate, increment, or decrement
// on v and sets its documented flags (N, Z, and C for the
// shifts and rotates; N and Z only for INC and DEC).
func (c *CPU) shift(kind int, v uint8) uint8 {
	switch kind {
	case shiftASL:
		c.setFlag(FlagC, v&0x80 != 0)
		r := v << 1
		c.setNZ(r)
		return r
	case shiftROL:
		carry := uint8(0)
		if c.flag(FlagC) {
			carry = 1
		}
		c.setFlag(FlagC, v&0x80 != 0)
		r := v<<1 | carry
		c.setNZ(r)
		return r
	case shiftLSR:
		c.setFlag(FlagC, v&0x01 != 0)
		r := v >> 1
		c.setNZ(r)
		return r
	case shiftROR:
		carry := uint8(0)
		if c.flag(FlagC) {
			carry = 0x80
		}
		c.setFlag(FlagC, v&0x01 != 0)
		r := v>>1 | carry
		c.setNZ(r)
		return r
	case shiftDEC:
		r := v - 1
		c.setNZ(r)
		return r
	case shiftINC:
		r := v + 1
		c.setNZ(r)
		return r
	}
	return v
}

// branch applies a relative branch offset if cond holds,
// and reports whether the branch was taken (the taken and
// not-taken cycle counts differ).
func (c *CPU) branch(cond bool, off int8) bool {
	if cond {
		c.pc = uint16(int32(c.pc) + int32(off))
		return true
	}
	return false
}

// branchCycles is the shared cycle shape of the plain
// conditional branches and BRA's conditional cousins:
// 2 cycles not taken, 4 taken.
func branchCycles(taken bool) int {
	if taken {
		return 4
	}
	return 2
}

// bitAddr fetches the two operand bytes of an absolute bit
// operation and decodes them: a 13-bit address in the low
// bits and a bit number in the top three bits of the second
// byte.
func (c *CPU) bitAddr() (addr uint16, bit uint) {
	lo := c.fetch()
	hi := c.fetch()
	return uint16(lo) | uint16(hi&0x1F)<<8, uint(hi >> 5)
}

// exec executes one already-fetched opcode and returns its
// cycle count.
func (c *CPU) exec(op uint8) int {
	// The patterned opcode families first: TCALL n and the
	// direct-page bit operations encode their operand in
	// the opcode byte itself.
	switch {
	case op&0x0F == 0x01: // TCALL n, vector at $FFDE-2n
		// The documented access sequence reads the byte
		// after the opcode (a dummy fetch that does not
		// advance PC) before pushing the return address;
		// the single-step traces record it as a real read.
		_ = c.bus.Read(c.pc)
		c.push16(c.pc)
		vec := 0xFFDE - 2*uint16(op>>4)
		lo := c.bus.Read(vec)
		hi := c.bus.Read(vec + 1)
		c.pc = uint16(hi)<<8 | uint16(lo)
		return 8
	case op&0x1F == 0x02: // SET1 dp.bit
		off := c.fetch()
		addr := c.dpAddr(off)
		c.bus.Write(addr, c.bus.Read(addr)|(1<<(op>>5)))
		return 4
	case op&0x1F == 0x12: // CLR1 dp.bit
		off := c.fetch()
		addr := c.dpAddr(off)
		c.bus.Write(addr, c.bus.Read(addr)&^(1<<(op>>5)))
		return 4
	case op&0x1F == 0x03: // BBS dp.bit,rel
		off := c.fetch()
		v := c.bus.Read(c.dpAddr(off)) // the byte is read before the rel fetch
		rel := int8(c.fetch())
		if c.branch(v&(1<<(op>>5)) != 0, rel) {
			return 7
		}
		return 5
	case op&0x1F == 0x13: // BBC dp.bit,rel
		off := c.fetch()
		v := c.bus.Read(c.dpAddr(off))
		rel := int8(c.fetch())
		if c.branch(v&(1<<(op>>5)) == 0, rel) {
			return 7
		}
		return 5
	}

	switch op {
	// The six shared ALU columns: A destination forms.
	case 0x08, 0x28, 0x48, 0x68, 0x88, 0xA8: // op A,#imm
		c.a = c.alu(aluKind(op), c.a, c.fetch())
		return 2
	case 0x04, 0x24, 0x44, 0x64, 0x84, 0xA4: // op A,dp
		c.a = c.alu(aluKind(op), c.a, c.readDP(c.fetch()))
		return 3
	case 0x14, 0x34, 0x54, 0x74, 0x94, 0xB4: // op A,dp+X
		c.a = c.alu(aluKind(op), c.a, c.bus.Read(c.dpAddrIdx(c.fetch(), c.x)))
		return 4
	case 0x05, 0x25, 0x45, 0x65, 0x85, 0xA5: // op A,abs
		c.a = c.alu(aluKind(op), c.a, c.bus.Read(c.fetch16()))
		return 4
	case 0x15, 0x35, 0x55, 0x75, 0x95, 0xB5: // op A,abs+X
		c.a = c.alu(aluKind(op), c.a, c.bus.Read(c.fetch16()+uint16(c.x)))
		return 5
	case 0x16, 0x36, 0x56, 0x76, 0x96, 0xB6: // op A,abs+Y
		c.a = c.alu(aluKind(op), c.a, c.bus.Read(c.fetch16()+uint16(c.y)))
		return 5
	case 0x06, 0x26, 0x46, 0x66, 0x86, 0xA6: // op A,(X)
		c.a = c.alu(aluKind(op), c.a, c.bus.Read(c.dpAddr(c.x)))
		return 3
	case 0x07, 0x27, 0x47, 0x67, 0x87, 0xA7: // op A,[dp+X]
		c.a = c.alu(aluKind(op), c.a, c.bus.Read(c.ptrDP(c.fetch()+c.x)))
		return 6
	case 0x17, 0x37, 0x57, 0x77, 0x97, 0xB7: // op A,[dp]+Y
		c.a = c.alu(aluKind(op), c.a, c.bus.Read(c.ptrDP(c.fetch())+uint16(c.y)))
		return 6

	// The six shared ALU columns: memory destination forms.
	// The source operand is fetched and read first, then
	// the destination, in the byte order the manual lists.
	case 0x09, 0x29, 0x49, 0x69, 0x89, 0xA9: // op dp,dp
		// Access order per the documented sequence (and
		// the single-step traces): the source operand byte
		// is fetched and its value read before the
		// destination operand byte is fetched.
		sv := c.readDP(c.fetch())
		addr := c.dpAddr(c.fetch())
		r := c.alu(aluKind(op), c.bus.Read(addr), sv)
		if aluKind(op) != aluCMP {
			c.bus.Write(addr, r)
		}
		return 6
	case 0x18, 0x38, 0x58, 0x78, 0x98, 0xB8: // op dp,#imm
		imm := c.fetch()
		dst := c.fetch()
		addr := c.dpAddr(dst)
		r := c.alu(aluKind(op), c.bus.Read(addr), imm)
		if aluKind(op) != aluCMP {
			c.bus.Write(addr, r)
		}
		return 5
	case 0x19, 0x39, 0x59, 0x79, 0x99, 0xB9: // op (X),(Y)
		sv := c.bus.Read(c.dpAddr(c.y))
		addr := c.dpAddr(c.x)
		r := c.alu(aluKind(op), c.bus.Read(addr), sv)
		if aluKind(op) != aluCMP {
			c.bus.Write(addr, r)
		}
		return 5

	// The shared shift/rotate/increment/decrement columns.
	case 0x1C, 0x3C, 0x5C, 0x7C, 0x9C, 0xBC: // shift A
		c.a = c.shift(shiftKind(op), c.a)
		return 2
	case 0x0B, 0x2B, 0x4B, 0x6B, 0x8B, 0xAB: // shift dp
		addr := c.dpAddr(c.fetch())
		c.bus.Write(addr, c.shift(shiftKind(op), c.bus.Read(addr)))
		return 4
	case 0x1B, 0x3B, 0x5B, 0x7B, 0x9B, 0xBB: // shift dp+X
		addr := c.dpAddrIdx(c.fetch(), c.x)
		c.bus.Write(addr, c.shift(shiftKind(op), c.bus.Read(addr)))
		return 5
	case 0x0C, 0x2C, 0x4C, 0x6C, 0x8C, 0xAC: // shift abs
		addr := c.fetch16()
		c.bus.Write(addr, c.shift(shiftKind(op), c.bus.Read(addr)))
		return 5

	// Register increments and decrements (the X and Y forms
	// sit outside the shared columns).
	case 0x1D: // DEC X
		c.x--
		c.setNZ(c.x)
		return 2
	case 0x3D: // INC X
		c.x++
		c.setNZ(c.x)
		return 2
	case 0xDC: // DEC Y
		c.y--
		c.setNZ(c.y)
		return 2
	case 0xFC: // INC Y
		c.y++
		c.setNZ(c.y)
		return 2

	// Compares against X and Y.
	case 0xC8: // CMP X,#imm
		c.cmp8(c.x, c.fetch())
		return 2
	case 0x3E: // CMP X,dp
		c.cmp8(c.x, c.readDP(c.fetch()))
		return 3
	case 0x1E: // CMP X,abs
		c.cmp8(c.x, c.bus.Read(c.fetch16()))
		return 4
	case 0xAD: // CMP Y,#imm
		c.cmp8(c.y, c.fetch())
		return 2
	case 0x7E: // CMP Y,dp
		c.cmp8(c.y, c.readDP(c.fetch()))
		return 3
	case 0x5E: // CMP Y,abs
		c.cmp8(c.y, c.bus.Read(c.fetch16()))
		return 4

	// Register transfers and immediate loads.
	case 0xE8: // MOV A,#imm
		c.a = c.fetch()
		c.setNZ(c.a)
		return 2
	case 0xCD: // MOV X,#imm
		c.x = c.fetch()
		c.setNZ(c.x)
		return 2
	case 0x8D: // MOV Y,#imm
		c.y = c.fetch()
		c.setNZ(c.y)
		return 2
	case 0x7D: // MOV A,X
		c.a = c.x
		c.setNZ(c.a)
		return 2
	case 0x5D: // MOV X,A
		c.x = c.a
		c.setNZ(c.x)
		return 2
	case 0xDD: // MOV A,Y
		c.a = c.y
		c.setNZ(c.a)
		return 2
	case 0xFD: // MOV Y,A
		c.y = c.a
		c.setNZ(c.y)
		return 2
	case 0x9D: // MOV X,SP
		c.x = c.sp
		c.setNZ(c.x)
		return 2
	case 0xBD: // MOV SP,X (no flags)
		c.sp = c.x
		return 2

	// A loads from memory.
	case 0xE4: // MOV A,dp
		c.a = c.readDP(c.fetch())
		c.setNZ(c.a)
		return 3
	case 0xF4: // MOV A,dp+X
		c.a = c.bus.Read(c.dpAddrIdx(c.fetch(), c.x))
		c.setNZ(c.a)
		return 4
	case 0xE5: // MOV A,abs
		c.a = c.bus.Read(c.fetch16())
		c.setNZ(c.a)
		return 4
	case 0xF5: // MOV A,abs+X
		c.a = c.bus.Read(c.fetch16() + uint16(c.x))
		c.setNZ(c.a)
		return 5
	case 0xF6: // MOV A,abs+Y
		c.a = c.bus.Read(c.fetch16() + uint16(c.y))
		c.setNZ(c.a)
		return 5
	case 0xE6: // MOV A,(X)
		c.a = c.bus.Read(c.dpAddr(c.x))
		c.setNZ(c.a)
		return 3
	case 0xBF: // MOV A,(X)+
		c.a = c.bus.Read(c.dpAddr(c.x))
		c.x++
		c.setNZ(c.a)
		return 4
	case 0xF7: // MOV A,[dp]+Y
		c.a = c.bus.Read(c.ptrDP(c.fetch()) + uint16(c.y))
		c.setNZ(c.a)
		return 6
	case 0xE7: // MOV A,[dp+X]
		c.a = c.bus.Read(c.ptrDP(c.fetch() + c.x))
		c.setNZ(c.a)
		return 6

	// X and Y loads from memory.
	case 0xF8: // MOV X,dp
		c.x = c.readDP(c.fetch())
		c.setNZ(c.x)
		return 3
	case 0xF9: // MOV X,dp+Y
		c.x = c.bus.Read(c.dpAddrIdx(c.fetch(), c.y))
		c.setNZ(c.x)
		return 4
	case 0xE9: // MOV X,abs
		c.x = c.bus.Read(c.fetch16())
		c.setNZ(c.x)
		return 4
	case 0xEB: // MOV Y,dp
		c.y = c.readDP(c.fetch())
		c.setNZ(c.y)
		return 3
	case 0xFB: // MOV Y,dp+X
		c.y = c.bus.Read(c.dpAddrIdx(c.fetch(), c.x))
		c.setNZ(c.y)
		return 4
	case 0xEC: // MOV Y,abs
		c.y = c.bus.Read(c.fetch16())
		c.setNZ(c.y)
		return 4

	// Stores. Every one performs the documented dummy read
	// of its destination first, except MOV dp,dp and
	// MOV (X)+,A below.
	case 0x8F: // MOV dp,#imm (operand order: imm, then dp)
		imm := c.fetch()
		addr := c.dpAddr(c.fetch())
		_ = c.bus.Read(addr)
		c.bus.Write(addr, imm)
		return 5
	case 0xFA: // MOV dp,dp (operand order: source, then dest;
		// the source value is read before the dest byte is fetched)
		sv := c.readDP(c.fetch())
		c.bus.Write(c.dpAddr(c.fetch()), sv)
		return 5
	case 0xC4: // MOV dp,A
		addr := c.dpAddr(c.fetch())
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 4
	case 0xD8: // MOV dp,X
		addr := c.dpAddr(c.fetch())
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.x)
		return 4
	case 0xCB: // MOV dp,Y
		addr := c.dpAddr(c.fetch())
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.y)
		return 4
	case 0xD4: // MOV dp+X,A
		addr := c.dpAddrIdx(c.fetch(), c.x)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 5
	case 0xDB: // MOV dp+X,Y
		addr := c.dpAddrIdx(c.fetch(), c.x)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.y)
		return 5
	case 0xD9: // MOV dp+Y,X
		addr := c.dpAddrIdx(c.fetch(), c.y)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.x)
		return 5
	case 0xC5: // MOV abs,A
		addr := c.fetch16()
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 5
	case 0xC9: // MOV abs,X
		addr := c.fetch16()
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.x)
		return 5
	case 0xCC: // MOV abs,Y
		addr := c.fetch16()
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.y)
		return 5
	case 0xD5: // MOV abs+X,A
		addr := c.fetch16() + uint16(c.x)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 6
	case 0xD6: // MOV abs+Y,A
		addr := c.fetch16() + uint16(c.y)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 6
	case 0xC6: // MOV (X),A
		addr := c.dpAddr(c.x)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 4
	case 0xAF: // MOV (X)+,A (no dummy read)
		c.bus.Write(c.dpAddr(c.x), c.a)
		c.x++
		return 4
	case 0xD7: // MOV [dp]+Y,A
		addr := c.ptrDP(c.fetch()) + uint16(c.y)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 7
	case 0xC7: // MOV [dp+X],A
		addr := c.ptrDP(c.fetch() + c.x)
		_ = c.bus.Read(addr)
		c.bus.Write(addr, c.a)
		return 7

	// 16-bit operations on YA and direct-page words.
	case 0xBA: // MOVW YA,dp
		c.setYA(c.readDP16(c.fetch()))
		c.setNZ16(c.ya())
		return 5
	case 0xDA: // MOVW dp,YA (dummy read on the low byte only)
		off := c.fetch()
		_ = c.readDP(off)
		c.writeDP16(off, c.ya())
		return 5
	case 0x7A: // ADDW YA,dp
		w := c.readDP16(c.fetch())
		ya := c.ya()
		t := uint32(ya) + uint32(w)
		r := uint16(t)
		c.setFlag(FlagC, t > 0xFFFF)
		c.setFlag(FlagH, (ya&0x0FFF)+(w&0x0FFF) > 0x0FFF)
		c.setFlag(FlagV, (ya^w)&0x8000 == 0 && (ya^r)&0x8000 != 0)
		c.setNZ16(r)
		c.setYA(r)
		return 5
	case 0x9A: // SUBW YA,dp
		w := c.readDP16(c.fetch())
		ya := c.ya()
		r := ya - w
		c.setFlag(FlagC, ya >= w)
		c.setFlag(FlagH, (ya&0x0FFF) >= (w&0x0FFF))
		c.setFlag(FlagV, (ya^w)&0x8000 != 0 && (ya^r)&0x8000 != 0)
		c.setNZ16(r)
		c.setYA(r)
		return 5
	case 0x5A: // CMPW YA,dp
		w := c.readDP16(c.fetch())
		ya := c.ya()
		c.setFlag(FlagC, ya >= w)
		c.setNZ16(ya - w)
		return 4
	case 0x3A: // INCW dp
		off := c.fetch()
		lo, hi := c.incDecW(off, true)
		c.setNZ16(uint16(hi)<<8 | uint16(lo))
		return 6
	case 0x1A: // DECW dp
		off := c.fetch()
		lo, hi := c.incDecW(off, false)
		c.setNZ16(uint16(hi)<<8 | uint16(lo))
		return 6
	case 0xCF: // MUL YA: YA = Y*A, flags from the high byte
		t := uint16(c.y) * uint16(c.a)
		c.setYA(t)
		c.setNZ(c.y)
		return 9
	case 0x9E: // DIV YA,X: the hardware divider (see below)
		c.divide()
		return 12

	// Decimal adjust and nibble exchange.
	case 0xDF: // DAA
		a := c.a
		if c.flag(FlagC) || a > 0x99 {
			a += 0x60
			c.setFlag(FlagC, true)
		}
		if c.flag(FlagH) || (a&0x0F) > 9 {
			a += 0x06
		}
		c.a = a
		c.setNZ(a)
		return 3
	case 0xBE: // DAS
		a := c.a
		if !c.flag(FlagC) || a > 0x99 {
			a -= 0x60
			c.setFlag(FlagC, false)
		}
		if !c.flag(FlagH) || (a&0x0F) > 9 {
			a -= 0x06
		}
		c.a = a
		c.setNZ(a)
		return 3
	case 0x9F: // XCN A
		c.a = c.a<<4 | c.a>>4
		c.setNZ(c.a)
		return 5

	// Test-and-set / test-and-clear: N and Z come from a
	// CMP of A against the original memory value, taken
	// before the write; C, H, and V are untouched (the
	// single-step corpus pins this down exactly).
	case 0x0E: // TSET1 abs
		addr := c.fetch16()
		v := c.bus.Read(addr)
		c.setNZ(c.a - v)
		_ = c.bus.Read(addr) // the target is read twice (documented)
		c.bus.Write(addr, v|c.a)
		return 6
	case 0x4E: // TCLR1 abs
		addr := c.fetch16()
		v := c.bus.Read(addr)
		c.setNZ(c.a - v)
		_ = c.bus.Read(addr) // the target is read twice (documented)
		c.bus.Write(addr, v&^c.a)
		return 6

	// One-bit operations against the carry flag.
	case 0xAA: // MOV1 C,mem.bit
		addr, bit := c.bitAddr()
		c.setFlag(FlagC, c.bus.Read(addr)&(1<<bit) != 0)
		return 4
	case 0xCA: // MOV1 mem.bit,C
		addr, bit := c.bitAddr()
		v := c.bus.Read(addr)
		if c.flag(FlagC) {
			v |= 1 << bit
		} else {
			v &^= 1 << bit
		}
		c.bus.Write(addr, v)
		return 6
	case 0x0A: // OR1 C,mem.bit
		addr, bit := c.bitAddr()
		v := c.bus.Read(addr) // the read always happens, even when C already decides the result
		c.setFlag(FlagC, c.flag(FlagC) || v&(1<<bit) != 0)
		return 5
	case 0x2A: // OR1 C,/mem.bit (the bit is inverted first)
		addr, bit := c.bitAddr()
		v := c.bus.Read(addr)
		c.setFlag(FlagC, c.flag(FlagC) || v&(1<<bit) == 0)
		return 5
	case 0x4A: // AND1 C,mem.bit
		addr, bit := c.bitAddr()
		v := c.bus.Read(addr)
		c.setFlag(FlagC, c.flag(FlagC) && v&(1<<bit) != 0)
		return 4
	case 0x6A: // AND1 C,/mem.bit (the bit is inverted first)
		addr, bit := c.bitAddr()
		v := c.bus.Read(addr)
		c.setFlag(FlagC, c.flag(FlagC) && v&(1<<bit) == 0)
		return 4
	case 0x8A: // EOR1 C,mem.bit
		addr, bit := c.bitAddr()
		if c.bus.Read(addr)&(1<<bit) != 0 {
			c.p ^= FlagC
		}
		return 5
	case 0xEA: // NOT1 mem.bit
		addr, bit := c.bitAddr()
		c.bus.Write(addr, c.bus.Read(addr)^(1<<bit))
		return 5

	// Stack operations. POPs set no flags (POP PSW restores
	// the whole status word instead).
	case 0x0D: // PUSH PSW
		c.push(c.p)
		return 4
	case 0x2D: // PUSH A
		c.push(c.a)
		return 4
	case 0x4D: // PUSH X
		c.push(c.x)
		return 4
	case 0x6D: // PUSH Y
		c.push(c.y)
		return 4
	case 0x8E: // POP PSW
		c.p = c.pull()
		return 4
	case 0xAE: // POP A
		c.a = c.pull()
		return 4
	case 0xCE: // POP X
		c.x = c.pull()
		return 4
	case 0xEE: // POP Y
		c.y = c.pull()
		return 4

	// Conditional branches: 2 cycles not taken, 4 taken.
	case 0x10: // BPL
		return branchCycles(c.branch(!c.flag(FlagN), int8(c.fetch())))
	case 0x30: // BMI
		return branchCycles(c.branch(c.flag(FlagN), int8(c.fetch())))
	case 0x50: // BVC
		return branchCycles(c.branch(!c.flag(FlagV), int8(c.fetch())))
	case 0x70: // BVS
		return branchCycles(c.branch(c.flag(FlagV), int8(c.fetch())))
	case 0x90: // BCC
		return branchCycles(c.branch(!c.flag(FlagC), int8(c.fetch())))
	case 0xB0: // BCS
		return branchCycles(c.branch(c.flag(FlagC), int8(c.fetch())))
	case 0xD0: // BNE
		return branchCycles(c.branch(!c.flag(FlagZ), int8(c.fetch())))
	case 0xF0: // BEQ
		return branchCycles(c.branch(c.flag(FlagZ), int8(c.fetch())))
	case 0x2F: // BRA
		off := int8(c.fetch())
		c.pc = uint16(int32(c.pc) + int32(off))
		return 4

	// Compare-and-branch and decrement-and-branch: no
	// flags are affected by any of them.
	case 0x2E: // CBNE dp,rel
		v := c.readDP(c.fetch())
		rel := int8(c.fetch())
		if c.branch(c.a != v, rel) {
			return 7
		}
		return 5
	case 0xDE: // CBNE dp+X,rel
		v := c.bus.Read(c.dpAddrIdx(c.fetch(), c.x))
		rel := int8(c.fetch())
		if c.branch(c.a != v, rel) {
			return 8
		}
		return 6
	case 0x6E: // DBNZ dp,rel
		addr := c.dpAddr(c.fetch())
		v := c.bus.Read(addr) - 1
		c.bus.Write(addr, v)
		rel := int8(c.fetch())
		if c.branch(v != 0, rel) {
			return 7
		}
		return 5
	case 0xFE: // DBNZ Y,rel
		c.y--
		rel := int8(c.fetch())
		if c.branch(c.y != 0, rel) {
			return 6
		}
		return 4

	// Jumps, calls, and returns. Calls push the exact
	// return address (the address of the next instruction),
	// unlike the 6502 family.
	case 0x5F: // JMP abs
		c.pc = c.fetch16()
		return 3
	case 0x1F: // JMP [abs+X]
		base := c.fetch16() + uint16(c.x)
		lo := c.bus.Read(base)
		hi := c.bus.Read(base + 1)
		c.pc = uint16(hi)<<8 | uint16(lo)
		return 6
	case 0x3F: // CALL abs
		target := c.fetch16()
		c.push16(c.pc)
		c.pc = target
		return 8
	case 0x4F: // PCALL $FF00+operand
		target := 0xFF00 + uint16(c.fetch())
		c.push16(c.pc)
		c.pc = target
		return 6
	case 0x6F: // RET
		c.pc = c.pull16()
		return 5
	case 0x7F: // RET1 (RET from interrupt: PSW first, then PC)
		c.p = c.pull()
		c.pc = c.pull16()
		return 6
	case 0x0F: // BRK: push PC and PSW, vector through $FFDE,
		// set B, clear I (the same vector TCALL 0 uses).
		c.push16(c.pc)
		c.push(c.p)
		c.setFlag(FlagB, true)
		c.setFlag(FlagI, false)
		lo := c.bus.Read(0xFFDE)
		hi := c.bus.Read(0xFFDF)
		c.pc = uint16(hi)<<8 | uint16(lo)
		return 8

	// Flag and control operations.
	case 0x20: // CLRP
		c.setFlag(FlagP, false)
		return 2
	case 0x40: // SETP
		c.setFlag(FlagP, true)
		return 2
	case 0x60: // CLRC
		c.setFlag(FlagC, false)
		return 2
	case 0x80: // SETC
		c.setFlag(FlagC, true)
		return 2
	case 0xE0: // CLRV (clears V and H together)
		c.setFlag(FlagV, false)
		c.setFlag(FlagH, false)
		return 2
	case 0xED: // NOTC
		c.p ^= FlagC
		return 3
	case 0xA0: // EI
		c.setFlag(FlagI, true)
		return 3
	case 0xC0: // DI
		c.setFlag(FlagI, false)
		return 3
	case 0x00: // NOP
		return 2
	case 0xEF, 0xFF: // SLEEP, STOP
		c.halted = true
		return haltCycles
	}
	return 2 // unreachable: every opcode is covered above
}

// incDecW runs INCW and DECW at a direct-page address and
// returns the resulting low and high bytes. The access
// sequence is byte-interleaved, as the documented order
// (and the single-step traces) show: the low byte is read,
// adjusted, and written back before the high byte is read,
// adjusted by the carry or borrow out of the low byte, and
// written back. The high byte's address wraps inside the
// direct page, as with every 16-bit direct-page access.
func (c *CPU) incDecW(off uint8, inc bool) (lo, hi uint8) {
	base := c.dpBase()
	loAddr := base + uint16(off)
	hiAddr := base + uint16(off+1)
	lo = c.bus.Read(loAddr)
	if inc {
		lo++
	} else {
		lo--
	}
	c.bus.Write(loAddr, lo)
	hi = c.bus.Read(hiAddr)
	if inc {
		if lo == 0 {
			hi++
		}
	} else {
		if lo == 0xFF {
			hi--
		}
	}
	c.bus.Write(hiAddr, hi)
	return lo, hi
}

// divide runs DIV YA,X as the hardware divider does it, in
// the form the documentation gives: a 17-bit working value
// holding YA is rotated left nine times against the divisor
// shifted to bits 9 and up, conditionally setting the low
// bit and subtracting the divisor, after which the working
// value's bits 16..9 are the remainder (returned in Y), bit
// 8 is the quotient's overflow bit (returned in V), and
// bits 7..0 are the quotient (returned in A). For quotients
// that fit in 9 bits this is exact division; past that the
// result is the hardware's, not the mathematical one. A
// zero divisor flows through the same steps. The H flag is
// not a half carry here: the manual sets it from the low
// nibbles of the original operands, (Y&15) >= (X&15). N and
// Z come from the final A, and C is untouched.
func (c *CPU) divide() {
	c.setFlag(FlagH, (c.y&0x0F) >= (c.x&0x0F))
	yva := uint32(c.ya())
	divisor := uint32(c.x) << 9
	for i := 0; i < 9; i++ {
		yva = ((yva << 1) & 0x1FFFF) | ((yva >> 16) & 1)
		if yva >= divisor {
			yva ^= 1
		}
		if yva&1 != 0 {
			yva = (yva - divisor) & 0x1FFFF
		}
	}
	c.setFlag(FlagV, yva&0x100 != 0)
	c.a = uint8(yva)
	c.y = uint8(yva >> 9)
	c.setNZ(c.a)
}
