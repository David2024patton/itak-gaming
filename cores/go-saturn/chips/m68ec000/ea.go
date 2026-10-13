package m68ec000

// Effective address decoding, operand access, and flag
// arithmetic. Sizes are 1, 2, or 4 throughout this file.

func sizeMask(size int) uint32 {
	switch size {
	case 1:
		return 0xFF
	case 2:
		return 0xFFFF
	default:
		return 0xFFFFFFFF
	}
}

func sizeBits(size int) uint {
	switch size {
	case 1:
		return 8
	case 2:
		return 16
	default:
		return 32
	}
}

func signBit(size int) uint32 {
	switch size {
	case 1:
		return 0x80
	case 2:
		return 0x8000
	default:
		return 0x80000000
	}
}

func sext(v uint32, size int) int32 {
	switch size {
	case 1:
		return int32(int8(v))
	case 2:
		return int32(int16(v))
	default:
		return int32(v)
	}
}

// ea is a decoded effective address. Exactly one of the
// dn, an, mem, or imm forms is set.
type ea struct {
	mode   int
	reg    int
	addr   uint32
	dn     bool
	an     bool
	mem    bool
	imm    bool
	immVal uint32
	cycles int
}

// eaCycles is the manual effective address adder for a
// byte or word operand; long operands add 4 for the
// memory forms, applied by decodeEA.
func eaCycles(mode, reg int) int {
	switch mode {
	case 0, 1:
		return 0
	case 2, 3:
		return 4
	case 4:
		return 6
	case 5:
		return 8
	case 6:
		return 10
	case 7:
		switch reg {
		case 0:
			return 8
		case 1:
			return 12
		case 2:
			return 8
		case 3:
			return 10
		case 4:
			return 4
		}
	}
	return 0
}

// indexValue evaluates the index register of an index
// extension word: bit 15 selects an address register,
// bits 14 to 12 the register, bit 11 the size (word,
// sign extended, or long).
func (c *CPU) indexValue(ext uint16) uint32 {
	idx := uint32(0)
	if ext&0x8000 != 0 {
		idx = c.a[(ext>>12)&7]
	} else {
		idx = c.d[(ext>>12)&7]
	}
	if ext&0x0800 == 0 {
		idx = uint32(int32(int16(idx)))
	}
	return idx
}

// decodeEA decodes the low six bits of op, consuming any
// extension words at the PC. It applies postincrement
// and predecrement address register updates. Byte
// accesses through the stack pointer adjust by 2 so the
// stack stays word aligned, per the manual.
func (c *CPU) decodeEA(op uint16, size int) ea {
	mode := int((op >> 3) & 7)
	reg := int(op & 7)
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
		case 2:
			base := c.pc
			disp := int32(int16(c.fetchExt()))
			e.mem = true
			e.addr = uint32(int32(base) + disp)
		case 3:
			base := c.pc
			ext := c.fetchExt()
			disp := int32(int8(ext))
			e.mem = true
			e.addr = uint32(int32(base) + disp + int32(c.indexValue(ext)))
		case 4:
			e.imm = true
			switch size {
			case 1:
				e.immVal = uint32(c.fetchExt() & 0xFF)
			case 2:
				e.immVal = uint32(c.fetchExt())
			case 4:
				hi := uint32(c.fetchExt())
				lo := uint32(c.fetchExt())
				e.immVal = hi<<16 | lo
			}
		default:
			panic(illegalError{instr: c.cur})
		}
	}
	return e
}

// controlAddr decodes an effective address for the
// control modes used by LEA, PEA, JSR, JMP, and MOVEM:
// modes 2, 5, 6, and absolute, plus PC relative for the
// read only users. No postincrement or predecrement is
// applied. It returns the address only.
func (c *CPU) controlAddr(op uint16) uint32 {
	mode := int((op >> 3) & 7)
	reg := int(op & 7)
	switch mode {
	case 2:
		return c.a[reg]
	case 5:
		disp := int32(int16(c.fetchExt()))
		return uint32(int32(c.a[reg]) + disp)
	case 6:
		ext := c.fetchExt()
		disp := int32(int8(ext))
		return uint32(int32(c.a[reg]) + disp + int32(c.indexValue(ext)))
	case 7:
		switch reg {
		case 0:
			return uint32(int32(int16(c.fetchExt())))
		case 1:
			hi := uint32(c.fetchExt())
			lo := uint32(c.fetchExt())
			return hi<<16 | lo
		case 2:
			base := c.pc
			disp := int32(int16(c.fetchExt()))
			return uint32(int32(base) + disp)
		case 3:
			base := c.pc
			ext := c.fetchExt()
			disp := int32(int8(ext))
			return uint32(int32(base) + disp + int32(c.indexValue(ext)))
		}
	}
	panic(illegalError{instr: c.cur})
}

// read returns the operand value, truncated to size.
func (c *CPU) read(e ea, size int) uint32 {
	switch {
	case e.dn:
		return c.d[e.reg] & sizeMask(size)
	case e.an:
		return c.a[e.reg] & sizeMask(size)
	case e.imm:
		return e.immVal & sizeMask(size)
	default:
		return c.readMem(e.addr, size)
	}
}

func (c *CPU) readMem(addr uint32, size int) uint32 {
	switch size {
	case 1:
		return uint32(c.rd8(addr))
	case 2:
		return uint32(c.rd16(addr))
	default:
		return c.rd32(addr)
	}
}

func (c *CPU) writeMem(addr uint32, v uint32, size int) {
	switch size {
	case 1:
		c.wr8(addr, uint8(v))
	case 2:
		c.wr16(addr, uint16(v))
	default:
		c.wr32(addr, v)
	}
}

// write stores v at the operand. Data register byte and
// word writes preserve the upper bits; address register
// writes are always full 32 bit with word results sign
// extended, per the manual partial register rules.
func (c *CPU) write(e ea, v uint32, size int) {
	switch {
	case e.dn:
		m := sizeMask(size)
		c.d[e.reg] = (c.d[e.reg] &^ m) | (v & m)
	case e.an:
		if size == 2 {
			c.a[e.reg] = uint32(int32(int16(v)))
		} else {
			c.a[e.reg] = v
		}
	case e.mem:
		c.writeMem(e.addr, v, size)
	default:
		panic(illegalError{instr: c.cur})
	}
}

// Flag helpers. The CCR is the low byte of SR.

func (c *CPU) setFlag(f uint16, on bool) {
	if on {
		c.sr |= f
	} else {
		c.sr &^= f
	}
}

func (c *CPU) getX() uint32 {
	if c.sr&srX != 0 {
		return 1
	}
	return 0
}

// setNZ sets N and Z from a truncated result.
func (c *CPU) setNZ(res uint32, size int) {
	c.setFlag(srN, res&signBit(size) != 0)
	c.setFlag(srZ, res&sizeMask(size) == 0)
}

// logicFlags is the flag rule for logic, move, and most
// unary operations: N and Z from the result, V and C
// cleared, X unchanged.
func (c *CPU) logicFlags(res uint32, size int) {
	c.setNZ(res, size)
	c.setFlag(srV, false)
	c.setFlag(srC, false)
}

// addFlags computes dst plus src plus xIn at size and
// sets XNZVC for an addition. It returns the result.
func (c *CPU) addFlags(src, dst uint32, size int, xIn uint32) uint32 {
	m := sizeMask(size)
	s := src & m
	d := dst & m
	total := uint64(d) + uint64(s) + uint64(xIn)
	res := uint32(total) & m
	sb := signBit(size)
	// Overflow: same sign operands, different sign result.
	v := (d^res)&(s^res)&sb != 0
	c.setNZ(res, size)
	c.setFlag(srV, v)
	carry := total > uint64(m)
	c.setFlag(srC, carry)
	c.setFlag(srX, carry)
	return res
}

// subFlags computes dst minus src minus xIn at size and
// sets flags for a subtraction. setX is false for CMP,
// which leaves X unchanged.
func (c *CPU) subFlags(src, dst uint32, size int, xIn uint32, setX bool) uint32 {
	m := sizeMask(size)
	s := src & m
	d := dst & m
	total := int64(d) - int64(s) - int64(xIn)
	res := uint32(total) & m
	sb := signBit(size)
	v := (d^s)&(d^res)&sb != 0
	c.setNZ(res, size)
	c.setFlag(srV, v)
	borrow := total < 0
	c.setFlag(srC, borrow)
	if setX {
		c.setFlag(srX, borrow)
	}
	return res
}

// bcdV is the overflow rule shipped for ABCD and SBCD.
// V is set when two operands with bit 7 clear produce a
// result with bit 7 set and no decimal carry out, or
// when two operands with bit 7 set produce a result
// with bit 7 clear and a decimal carry (borrow, for
// SBCD) out. This symmetric model is the stated rule
// for this core; the caller supplies the decimal carry
// or borrow as carryOut. N is bit 7 of the result and
// the multi precision Z rule is applied by the caller,
// not here.
func bcdV(a, b, res, carryOut uint32) bool {
	if carryOut == 0 {
		return (res&0x80) != 0 && (a&0x80) == 0 && (b&0x80) == 0
	}
	return (res&0x80) == 0 && (a&0x80) != 0 && (b&0x80) != 0
}

// bcdAdd adds two packed BCD bytes plus the X flag:
// binary addition digit by digit with the +6 correction
// on any digit over 9. It returns the corrected result,
// the decimal carry out, and V per bcdV.
func bcdAdd(a, b uint32, x uint32) (res uint32, carry uint32, v bool) {
	lo := (a & 0x0F) + (b & 0x0F) + x
	if lo > 9 {
		lo += 6
	}
	hi := (a >> 4) + (b >> 4) + (lo >> 4)
	if hi > 9 {
		hi += 6
		carry = 1
	}
	res = ((hi & 0x0F) << 4) | (lo & 0x0F)
	v = bcdV(a, b, res, carry)
	return res, carry, v
}

// bcdSub subtracts b and X from a in packed BCD, per
// SBCD: digitwise with the 6 correction on borrow. It
// returns the corrected result, the decimal borrow out,
// and V per bcdV with the borrow as the carry term.
func bcdSub(a, b uint32, x uint32) (res uint32, borrow uint32, v bool) {
	lo := int(a&0x0F) - int(b&0x0F) - int(x)
	loBorrow := 0
	if lo < 0 {
		lo -= 6
		loBorrow = 1
	}
	hi := int(a>>4) - int(b>>4) - loBorrow
	if hi < 0 {
		hi -= 6
		borrow = 1
	}
	res = uint32((hi&0x0F)<<4 | (lo & 0x0F))
	v = bcdV(a, b, res, borrow)
	return res, borrow, v
}

// condition evaluates a 68000 condition code field.
func (c *CPU) condition(cond int) bool {
	n := c.sr&srN != 0
	z := c.sr&srZ != 0
	v := c.sr&srV != 0
	carry := c.sr&srC != 0
	switch cond {
	case 0:
		return true
	case 1:
		return false
	case 2:
		return !carry && !z
	case 3:
		return carry || z
	case 4:
		return !carry
	case 5:
		return carry
	case 6:
		return !z
	case 7:
		return z
	case 8:
		return !v
	case 9:
		return v
	case 10:
		return !n
	case 11:
		return n
	case 12:
		return n == v
	case 13:
		return n != v
	case 14:
		return !z && (n == v)
	case 15:
		return z || (n != v)
	}
	return false
}
