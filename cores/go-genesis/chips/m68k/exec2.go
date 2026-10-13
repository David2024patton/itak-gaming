package m68k

// The two-operand lines (8, 9, B, C, D), multiply/divide, BCD, MOVEM,
// and the shift/rotate family. These lines share a three-way opmode
// split:
//
//	opmode 000-010: <ea> is the source, Dn the destination (size = opmode)
//	opmode 011/111: word/long variant instruction (DIV, MUL, SUBA, ...)
//	opmode 100-110: Dn is the source, <ea> the destination (size-100),
//	                except that EA modes 000/001 in this range encode
//	                the register-to-register / memory-to-memory forms
//	                (ADDX, SUBX, ABCD, SBCD, CMPM).

type aluKind int

const (
	aluOr aluKind = iota
	aluAnd
	aluAdd
	aluSub
	aluEor
	aluCmp
)

// applyALU performs a two-operand ALU operation with the manual's flag
// semantics and returns the result (unused for aluCmp).
func (c *CPU) applyALU(kind aluKind, a, b uint32, size int) uint32 {
	switch kind {
	case aluOr:
		r := (a | b) & sizeMask(size)
		c.flagsLogic(r, size)
		return r
	case aluAnd:
		r := (a & b) & sizeMask(size)
		c.flagsLogic(r, size)
		return r
	case aluEor:
		r := (a ^ b) & sizeMask(size)
		c.flagsLogic(r, size)
		return r
	case aluAdd:
		return c.flagsAdd(a, b, size, 0, false)
	case aluSub:
		return c.flagsSub(a, b, size, 0, false)
	case aluCmp:
		c.flagsCmp(a, b, size)
		return 0
	}
	return 0
}

// execALUToReg runs the <ea>,Dn form: the source is read, combined with
// Dn, and (except for compares) written back to Dn.
func (c *CPU) execALUToReg(op uint16, dstReg, size, mode, reg int, kind aluKind) int {
	if mode == 1 && size == szB {
		return c.illegal(op) // byte operations cannot use An as a source
	}
	if !eaReadable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, size)
	src := c.readSrc(o, size)
	res := c.applyALU(kind, c.d[dstReg], src, size)
	if kind != aluCmp {
		c.opWrite(operand{mode: 0, reg: dstReg}, size, res)
	}
	if size == szL {
		return 6 + o.cyc
	}
	return 4 + o.cyc
}

// execALUToMem runs the Dn,<ea> form: the memory (or, for EOR, register)
// destination is read, combined with Dn, and written back. For SUB the
// computation is <ea> - Dn; the destination supplies the first operand.
func (c *CPU) execALUToMem(op uint16, srcReg, size, mode, reg int, kind aluKind) int {
	if !eaDataAlterable(mode, reg) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, size)
	v := c.opRead(o, size)
	res := c.applyALU(kind, v, c.d[srcReg], size)
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

// --- line 8: OR, DIVU, DIVS, SBCD -------------------------------------

func (c *CPU) execLine8(op uint16) int {
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := eaFields(op)
	switch {
	case opmode == 3:
		return c.execDiv(op, reg, mode, r, false)
	case opmode == 7:
		return c.execDiv(op, reg, mode, r, true)
	case opmode >= 4 && mode <= 1:
		return c.execBCD(op, reg, mode, r, true) // SBCD
	case opmode <= 2:
		return c.execALUToReg(op, reg, opmode, mode, r, aluOr)
	default:
		return c.execALUToMem(op, reg, opmode-4, mode, r, aluOr)
	}
}

// --- lines 9/D: SUB/ADD families --------------------------------------

func (c *CPU) execLine9D(op uint16, isAdd bool) int {
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := eaFields(op)
	kind := aluSub
	if isAdd {
		kind = aluAdd
	}
	switch {
	case opmode == 3 || opmode == 7:
		// SUBA/ADDA: word form sign-extends the source; no flags.
		size := szW
		if opmode == 7 {
			size = szL
		}
		if !eaReadable(mode, r) {
			return c.illegal(op)
		}
		o := c.resolveEA(mode, r, size)
		v := c.readSrc(o, size)
		if size == szW {
			v = signExtend(v, szW)
			if isAdd {
				c.a[reg] += v
			} else {
				c.a[reg] -= v
			}
			return 8 + o.cyc
		}
		if isAdd {
			c.a[reg] += v
		} else {
			c.a[reg] -= v
		}
		return 6 + o.cyc
	case opmode >= 4 && mode <= 1:
		// SUBX/ADDX, register-to-register or memory-to-memory (both
		// memory operands use predecrement addressing).
		size := opmode - 4
		if (op>>3)&1 != 0 {
			so := c.resolveEA(4, r, size)
			sv := c.opRead(so, size)
			do := c.resolveEA(4, reg, size)
			dv := c.opRead(do, size)
			res := c.flagsX(kind, dv, sv, size)
			c.opWrite(do, size, res)
			if size == szL {
				return 30
			}
			return 18
		}
		res := c.flagsX(kind, c.d[reg], c.d[r], size)
		c.opWrite(operand{mode: 0, reg: reg}, size, res)
		if size == szL {
			return 8
		}
		return 4
	case opmode <= 2:
		return c.execALUToReg(op, reg, opmode, mode, r, kind)
	default:
		return c.execALUToMem(op, reg, opmode-4, mode, r, kind)
	}
}

// flagsX applies ADDX/SUBX arithmetic: the X flag is the carry/borrow
// in, X and C take the carry/borrow out, and Z is sticky (a zero result
// leaves it unchanged).
func (c *CPU) flagsX(kind aluKind, a, b uint32, size int) uint32 {
	if kind == aluAdd {
		return c.flagsAdd(a, b, size, c.xBit(), true)
	}
	return c.flagsSub(a, b, size, c.xBit(), true)
}

// --- line B: CMP, EOR, CMPA, CMPM --------------------------------------

func (c *CPU) execLineB(op uint16) int {
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := eaFields(op)
	switch {
	case opmode == 3 || opmode == 7:
		// CMPA: compare the full address register against the
		// (sign-extended for word) source; X is preserved.
		size := szW
		if opmode == 7 {
			size = szL
		}
		if !eaReadable(mode, r) {
			return c.illegal(op)
		}
		o := c.resolveEA(mode, r, size)
		v := c.readSrc(o, size)
		if size == szW {
			v = signExtend(v, szW)
		}
		c.flagsCmp(c.a[reg], v, szL)
		return 6 + o.cyc
	case opmode <= 2:
		return c.execALUToReg(op, reg, opmode, mode, r, aluCmp)
	case mode == 1 && opmode >= 4:
		// CMPM: compare (An)+ against (An)+.
		size := opmode - 4
		so := c.resolveEA(3, r, size)
		sv := c.readSrc(so, size)
		do := c.resolveEA(3, reg, size)
		dv := c.readSrc(do, size)
		c.flagsCmp(dv, sv, size)
		if size == szL {
			return 20
		}
		return 12
	default:
		return c.execALUToMem(op, reg, opmode-4, mode, r, aluEor)
	}
}

// --- line C: AND, MULU, MULS, ABCD, EXG --------------------------------

func (c *CPU) execLineC(op uint16) int {
	// EXG encodings live inside the AND opcode space; test them first.
	switch {
	case op&0xF1F8 == 0xC140: // EXG Dn,Dn
		a, b := int((op>>9)&7), int(op&7)
		c.d[a], c.d[b] = c.d[b], c.d[a]
		return 6
	case op&0xF1F8 == 0xC148: // EXG An,An
		a, b := int((op>>9)&7), int(op&7)
		c.a[a], c.a[b] = c.a[b], c.a[a]
		return 6
	case op&0xF1F8 == 0xC188: // EXG Dn,An
		a, b := int((op>>9)&7), int(op&7)
		c.d[a], c.a[b] = c.a[b], c.d[a]
		return 6
	}
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := eaFields(op)
	switch {
	case opmode == 3:
		return c.execMul(op, reg, mode, r, false)
	case opmode == 7:
		return c.execMul(op, reg, mode, r, true)
	case opmode >= 4 && mode <= 1:
		return c.execBCD(op, reg, mode, r, false) // ABCD
	case opmode <= 2:
		return c.execALUToReg(op, reg, opmode, mode, r, aluAnd)
	default:
		return c.execALUToMem(op, reg, opmode-4, mode, r, aluAnd)
	}
}

// --- multiply and divide ------------------------------------------------

// execDiv implements DIVU/DIVS: a 32-by-16 division producing a 16-bit
// quotient in the low word and the remainder in the high word of Dn. A
// zero divisor raises the divide-by-zero exception (vector 5). On
// quotient overflow the destination is left unchanged and V is set
// (N and Z are undefined by the manual in that case). The remainder
// takes the sign of the dividend, as Go's % does, matching the manual.
func (c *CPU) execDiv(op uint16, reg, mode, r int, signed bool) int {
	if !eaReadable(mode, r) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, r, szW)
	src := c.readSrc(o, szW)
	if signed {
		dvs := int32(int16(uint16(src)))
		if dvs == 0 {
			return c.enterException(vecDivZero, 40)
		}
		dvd := int32(c.d[reg])
		q := dvd / dvs
		rem := dvd % dvs
		if q > 32767 || q < -32768 {
			c.sr = (c.sr &^ srC) | srV
			return 100 + o.cyc
		}
		c.d[reg] = (uint32(uint16(rem)) << 16) | uint32(uint16(q))
		c.flagsLogic(uint32(uint16(q)), szW)
		return 120 + o.cyc
	}
	dvs := src & 0xFFFF
	if dvs == 0 {
		return c.enterException(vecDivZero, 40)
	}
	dvd := c.d[reg]
	q := dvd / dvs
	rem := dvd % dvs
	if q > 0xFFFF {
		c.sr = (c.sr &^ srC) | srV
		return 100 + o.cyc
	}
	c.d[reg] = (rem << 16) | q
	c.flagsLogic(q, szW)
	return 140 + o.cyc
}

// execMul implements MULU/MULS: a 16-by-16 multiply producing a 32-bit
// result in Dn, with N/Z from the full result and V/C cleared.
// Documented time is about 70 cycles plus the source EA fetch.
func (c *CPU) execMul(op uint16, reg, mode, r int, signed bool) int {
	if !eaReadable(mode, r) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, r, szW)
	src := c.readSrc(o, szW)
	var res uint32
	if signed {
		res = uint32(int32(int16(uint16(c.d[reg]))) * int32(int16(uint16(src))))
	} else {
		res = (c.d[reg] & 0xFFFF) * (src & 0xFFFF)
	}
	c.d[reg] = res
	c.flagsLogic(res, szL)
	return 70 + o.cyc
}

// --- BCD ------------------------------------------------------------------

// execBCD implements ABCD (isSub false) and SBCD (isSub true), byte
// only, in register-to-register and memory-to-memory (predecrement)
// forms.
func (c *CPU) execBCD(op uint16, reg, mode, r int, isSub bool) int {
	if (op>>3)&1 != 0 {
		so := c.resolveEA(4, r, szB)
		sv := c.opRead(so, szB)
		do := c.resolveEA(4, reg, szB)
		dv := c.opRead(do, szB)
		var res uint32
		if isSub {
			res = c.bcdSub(dv, sv)
		} else {
			res = c.bcdAdd(dv, sv)
		}
		c.opWrite(do, szB, res)
		return 18
	}
	var res uint32
	if isSub {
		res = c.bcdSub(c.d[reg], c.d[r])
	} else {
		res = c.bcdAdd(c.d[reg], c.d[r])
	}
	c.opWrite(operand{mode: 0, reg: reg}, szB, res)
	return 6
}

// The BCD model below was fitted against a full-space black-box
// probe of the reference implementation (every operand pair and both
// X states, 131072 cases per instruction, result and flags compared).
// The manual leaves the results for invalid BCD digits and the V flag
// undefined, but real code can observe both (DBcc reads V), so the
// silicon behavior is what is implemented:
//
//   - Corrections are applied to the binary sum as separate passes.
//     For ABCD the +0x60 pass and the carry flag are decided on the
//     RAW binary sum (before the +6 pass), not on the corrected sum.
//   - For SBCD the -0x60 result pass is decided on the raw
//     difference, but the carry flag is decided on the difference
//     after the -6 pass.
//   - V is the OR of the signed overflows of the correction passes
//     alone, each computed on byte values; the initial binary pass
//     does not contribute.

// bcdPassVAdd is the signed overflow of one byte addition pass u + k
// producing t.
func bcdPassVAdd(u, k, t uint32) bool {
	return (u^t)&(k^t)&0x80 != 0
}

// bcdPassVSub is the signed overflow of one byte subtraction pass
// u - k producing t.
func bcdPassVSub(u, k, t uint32) bool {
	return (u^k)&(u^t)&0x80 != 0
}

// bcdAdd computes a + b + X as packed BCD. Flags: X and C get the
// decimal carry, N is the result's top bit, Z is sticky (cleared by a
// nonzero result, otherwise unchanged, as for ADDX), V per the model
// above.
func (c *CPU) bcdAdd(a, b uint32) uint32 {
	a &= 0xFF
	b &= 0xFF
	x := uint32(c.xBit())
	raw := a + b + x
	res := raw & 0xFF
	v := false
	if (a&0xF)+(b&0xF)+x > 9 {
		before := res
		res = (res + 6) & 0xFF
		v = v || bcdPassVAdd(before, 6, res)
	}
	carry := false
	if raw > 0x99 {
		before := res
		res = (res + 0x60) & 0xFF
		v = v || bcdPassVAdd(before, 0x60, res)
		carry = true
	}
	c.setBCDFlags(res, carry, v)
	return res
}

// bcdSub computes a - b - X as packed BCD, with the flag rules of the
// model above. NBCD is bcdSub with a = 0.
func (c *CPU) bcdSub(a, b uint32) uint32 {
	a &= 0xFF
	b &= 0xFF
	x := int(c.xBit())
	raw := int(a) - int(b) - x
	res := uint32(raw) & 0xFF
	v := false
	afterLow := raw
	if (a & 0xF) < (b&0xF)+uint32(x) {
		before := res
		res = (res - 6) & 0xFF
		v = v || bcdPassVSub(before, 6, res)
		afterLow = raw - 6
	}
	carry := afterLow < 0
	if raw < 0 {
		before := res
		res = (res - 0x60) & 0xFF
		v = v || bcdPassVSub(before, 0x60, res)
	}
	c.setBCDFlags(res, carry, v)
	return res
}

func (c *CPU) setBCDFlags(res uint32, carry, v bool) {
	c.sr &^= srX | srN | srV | srC
	if carry {
		c.sr |= srX | srC
	}
	if v {
		c.sr |= srV
	}
	if res&0x80 != 0 {
		c.sr |= srN
	}
	if res != 0 {
		c.sr &^= srZ
	}
}

// --- MOVEM ------------------------------------------------------------------

// movemListReg maps a register-list mask bit (normal order) to a
// register: bits 0-7 are D0-D7, bits 8-15 are A0-A7. kind 0 = D, 1 = A.
func movemListReg(j int) (kind, idx int) {
	if j < 8 {
		return 0, j
	}
	return 1, j - 8
}

// movemRevReg maps a mask bit under predecrement addressing, where the
// mask is interpreted in reverse: bit 0 is A7 down to bit 7 = A0, then
// bit 8 is D7 down to bit 15 = D0.
func movemRevReg(j int) (kind, idx int) {
	if j < 8 {
		return 1, 7 - j
	}
	return 0, 15 - j
}

func regValue(snapD, snapA [8]uint32, kind, idx int) uint32 {
	if kind == 0 {
		return snapD[idx]
	}
	return snapA[idx]
}

// execMovem implements MOVEM in both directions. The mask word follows
// the opcode. Register-to-memory permits control modes and
// predecrement (with the reversed mask); memory-to-register permits
// control modes and postincrement. Word transfers to registers
// sign-extend. Documented times: 8 + 4/8 per register stored,
// 12 + 4/8 per register loaded.
func (c *CPU) execMovem(op uint16, memToReg bool) int {
	size := szW
	if op&0x40 != 0 {
		size = szL
	}
	mode, reg := eaFields(op)
	mask := c.fetch16()
	sb := sizeBytes(size)
	if !memToReg {
		// Snapshot first: under predecrement, a base register that is
		// itself in the list is stored with its original value.
		snapD, snapA := c.d, c.a
		if mode == 4 {
			addr := c.a[reg]
			n := 0
			for j := 0; j < 16; j++ {
				if mask>>uint(j)&1 == 0 {
					continue
				}
				addr -= uint32(sb)
				kind, idx := movemRevReg(j)
				c.writeMem(addr, size, regValue(snapD, snapA, kind, idx))
				n++
			}
			c.a[reg] = addr
			return 8 + n*sb*2
		}
		if !(mode == 2 || mode == 5 || mode == 6 || (mode == 7 && reg <= 1)) {
			return c.illegal(op)
		}
		o := c.resolveEA(mode, reg, size)
		addr := o.addr
		n := 0
		for j := 0; j < 16; j++ {
			if mask>>uint(j)&1 == 0 {
				continue
			}
			kind, idx := movemListReg(j)
			c.writeMem(addr, size, regValue(snapD, snapA, kind, idx))
			addr += uint32(sb)
			n++
		}
		return 8 + n*sb*2
	}
	if !(mode == 2 || mode == 3 || mode == 5 || mode == 6 || (mode == 7 && reg <= 1)) {
		return c.illegal(op)
	}
	o := c.resolveEA(mode, reg, size)
	addr := o.addr
	n := 0
	for j := 0; j < 16; j++ {
		if mask>>uint(j)&1 == 0 {
			continue
		}
		v := c.readMem(addr, size)
		kind, idx := movemListReg(j)
		if kind == 0 {
			c.d[idx] = signExtend(v, size)
		} else {
			c.a[idx] = signExtend(v, size)
		}
		addr += uint32(sb)
		n++
	}
	if mode == 3 {
		// Postincrement total; a base register in the list ends with
		// the final address, not the loaded value.
		c.a[reg] = addr
	}
	return 12 + n*sb*2
}

// --- shifts and rotates (line E) -------------------------------------------

func (c *CPU) execShifts(op uint16) int {
	if (op>>6)&3 == 3 {
		// Memory form: word only, shift count of one.
		typ := int((op >> 9) & 3)
		dir := int((op >> 8) & 1)
		mode, reg := eaFields(op)
		if !eaAlterableMem(mode, reg) {
			return c.illegal(op)
		}
		o := c.resolveEA(mode, reg, szW)
		v := c.opRead(o, szW)
		res := c.shiftOp(typ, dir, szW, v, 1)
		c.opWrite(o, szW, res)
		return 8 + o.cyc
	}
	cntField := int((op >> 9) & 7)
	dir := int((op >> 8) & 1)
	size := int((op >> 6) & 3)
	typ := int((op >> 3) & 3)
	reg := int(op & 7)
	var count int
	if op&0x20 != 0 {
		count = int(c.d[cntField] & 63) // register counts are modulo 64
	} else {
		count = cntField
		if count == 0 {
			count = 8
		}
	}
	v := c.d[reg] & sizeMask(size)
	res := c.shiftOp(typ, dir, size, v, count)
	c.opWrite(operand{mode: 0, reg: reg}, size, res)
	if size == szL {
		return 8 + 2*count
	}
	return 6 + 2*count
}

// shiftOp performs one arithmetic/logical shift or rotate. typ: 0 AS,
// 1 LS, 2 ROX (through X), 3 RO. dir: 0 right, 1 left.
//
// Flag rules per the manual: N and Z come from the result; C holds the
// last bit shifted out and is cleared when the count is zero; X holds
// the last bit shifted out for AS/LS/ROX and is unaffected for RO and
// when the count is zero; V is set for ASL if the sign bit changes at
// any point during the shift, and is cleared for all other forms.
func (c *CPU) shiftOp(typ, dir, size int, v uint32, count int) uint32 {
	bits := sizeBytes(size) * 8
	mask := sizeMask(size)
	sb := signBit(size)
	v &= mask
	res := v
	carry := uint32(0)
	shifted := false
	vSet := false
	xIn := uint32(0)
	if c.sr&srX != 0 {
		xIn = 1
	}
	if dir == 1 { // left
		switch typ {
		case 0, 1: // ASL, LSL
			for i := 0; i < count; i++ {
				before := res & sb
				carry = 0
				if before != 0 {
					carry = 1
				}
				res = (res << 1) & mask
				if typ == 0 && (res&sb) != before {
					vSet = true
				}
				shifted = true
			}
		case 2: // ROXL
			x := xIn
			for i := 0; i < count; i++ {
				nx := uint32(0)
				if res&sb != 0 {
					nx = 1
				}
				res = ((res << 1) | x) & mask
				x = nx
				carry = nx
				shifted = true
			}
		case 3: // ROL
			for i := 0; i < count; i++ {
				carry = 0
				if res&sb != 0 {
					carry = 1
				}
				res = ((res << 1) | carry) & mask
				shifted = true
			}
		}
	} else { // right
		switch typ {
		case 0: // ASR
			for i := 0; i < count; i++ {
				carry = res & 1
				res = (res >> 1) | (res & sb)
				shifted = true
			}
		case 1: // LSR
			for i := 0; i < count; i++ {
				carry = res & 1
				res >>= 1
				shifted = true
			}
		case 2: // ROXR
			x := xIn
			for i := 0; i < count; i++ {
				nx := res & 1
				res = (res >> 1) | (x << uint(bits-1))
				x = nx
				carry = nx
				shifted = true
			}
		case 3: // ROR
			for i := 0; i < count; i++ {
				carry = res & 1
				res = (res >> 1) | (carry << uint(bits-1))
				shifted = true
			}
		}
	}
	c.sr &^= srN | srZ | srV | srC
	if res&sb != 0 {
		c.sr |= srN
	}
	if res == 0 {
		c.sr |= srZ
	}
	if vSet {
		c.sr |= srV
	}
	if shifted && carry != 0 {
		c.sr |= srC
	}
	if typ != 3 && shifted {
		// AS/LS/ROX load X with the last bit out.
		if carry != 0 {
			c.sr |= srX
		} else {
			c.sr &^= srX
		}
	}
	return res
}
