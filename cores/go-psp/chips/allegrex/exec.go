package allegrex

// Integer instruction decode and execution. Cycle model
// (deterministic approximation): 1 cycle for everything except
// MULT/MULTU/MUL and the MADD/MSUB family at 2 cycles and
// DIV/DIVU at 37 cycles. Exception entry steps report 1 cycle.

func signExt16(v uint32) int32 { return int32(int16(v)) }

func (c *CPU) execute(instr uint32) int {
	op := instr >> 26
	rs := int((instr >> 21) & 31)
	rt := int((instr >> 16) & 31)
	rd := int((instr >> 11) & 31)
	sa := int((instr >> 6) & 31)
	fn := instr & 63
	imm := instr & 0xFFFF
	simm := signExt16(imm)

	switch op {
	case 0x00: // SPECIAL
		return c.execSpecial(instr, rs, rt, rd, sa, fn)
	case 0x01: // REGIMM
		return c.execRegimm(rs, rt, simm)
	case 0x02: // J
		c.branch(true, c.jumpTarget(instr))
	case 0x03: // JAL
		c.setReg(31, c.curPC+8)
		c.branch(true, c.jumpTarget(instr))
	case 0x04: // BEQ
		c.branch(c.regs[rs] == c.regs[rt], c.branchTarget(simm))
	case 0x05: // BNE
		c.branch(c.regs[rs] != c.regs[rt], c.branchTarget(simm))
	case 0x06: // BLEZ
		c.branch(int32(c.regs[rs]) <= 0, c.branchTarget(simm))
	case 0x07: // BGTZ
		c.branch(int32(c.regs[rs]) > 0, c.branchTarget(simm))
	case 0x14: // BEQL
		c.branchLikely(c.regs[rs] == c.regs[rt], c.branchTarget(simm))
	case 0x15: // BNEL
		c.branchLikely(c.regs[rs] != c.regs[rt], c.branchTarget(simm))
	case 0x16: // BLEZL
		c.branchLikely(int32(c.regs[rs]) <= 0, c.branchTarget(simm))
	case 0x17: // BGTZL
		c.branchLikely(int32(c.regs[rs]) > 0, c.branchTarget(simm))
	case 0x08: // ADDI
		sum := int64(int32(c.regs[rs])) + int64(simm)
		if sum > 0x7FFFFFFF || sum < -0x80000000 {
			c.raiseException(excOv, 0)
			return 1
		}
		c.setReg(rt, uint32(sum))
	case 0x09: // ADDIU
		c.setReg(rt, c.regs[rs]+uint32(simm))
	case 0x0A: // SLTI
		if int32(c.regs[rs]) < simm {
			c.setReg(rt, 1)
		} else {
			c.setReg(rt, 0)
		}
	case 0x0B: // SLTIU: immediate is sign extended, compare unsigned
		if c.regs[rs] < uint32(simm) {
			c.setReg(rt, 1)
		} else {
			c.setReg(rt, 0)
		}
	case 0x0C: // ANDI
		c.setReg(rt, c.regs[rs]&imm)
	case 0x0D: // ORI
		c.setReg(rt, c.regs[rs]|imm)
	case 0x0E: // XORI
		c.setReg(rt, c.regs[rs]^imm)
	case 0x0F: // LUI
		c.setReg(rt, imm<<16)
	case 0x10: // COP0
		return c.execCOP0(instr, rs, rt, rd)
	case 0x11: // COP1
		return c.execCOP1(instr)
	case 0x12: // COP2: the VFPU
		return c.execCOP2(instr)
	case 0x18: // VFPU VV arith group: vadd, vsub, vsbn, vdiv
		return c.execCompute(instr, c.execVVArith)
	case 0x19: // VFPU VV product group: vmul, vdot, vscl, vhdp, vcrs, vdet
		return c.execCompute(instr, c.execVVProd)
	case 0x1B: // VFPU VV compare group: vcmp, vmin, vmax, vsge, vslt
		return c.execCompute(instr, c.execVVCmpGrp)
	case 0x34: // VFPU unary, scaled conversions, vcmov
		return c.execCompute(instr, c.execVFPU34)
	case 0x37: // VFPU prefix instructions: vpfxs, vpfxt, vpfxd
		return c.execPrefixes(instr)
	case 0x3C: // VFPU matrix group
		return c.execCompute(instr, c.execMatrixGroup)
	case 0x32: // LV.S
		return c.execLVS(instr)
	case 0x35: // LVL.Q / LVR.Q
		return c.execLVLR(instr)
	case 0x36: // LV.Q
		return c.execLVQ(instr)
	case 0x3A: // SV.S
		return c.execSVS(instr)
	case 0x3D: // SVL.Q / SVR.Q
		return c.execSVLR(instr)
	case 0x3E: // SV.Q
		return c.execSVQ(instr)
	case 0x1C: // SPECIAL2
		return c.execSpecial2(instr, rs, rt, rd, fn)
	case 0x1F: // SPECIAL3
		return c.execSpecial3(instr, rs, rt, rd, sa, fn)
	case 0x3F:
		// Primary opcode 0x3F has no documented Allegrex
		// assignment in the published encoding references, but
		// recorded PSP software executes the word 0xFFFF0000
		// (three in a row inside the SDK printf float path
		// linked into every pspautotests vfpu binary) without
		// faulting or side effects: with this slot passed over,
		// the vrnd test runs to completion with correct output,
		// and with it faulting, execution dies at the first
		// word. Modelled as a no-op and flagged as an open
		// encoding question in the README.
		return 1
	case 0x2F: // CACHE: decoded, no operation, never faults
	default:
		return c.execLoadStore(op, rs, rt, simm)
	}
	return 1
}

func (c *CPU) jumpTarget(instr uint32) uint32 {
	return ((c.curPC + 4) & 0xF0000000) | ((instr & 0x03FFFFFF) << 2)
}

func (c *CPU) execSpecial(instr uint32, rs, rt, rd, sa int, fn uint32) int {
	switch fn {
	case 0x00: // SLL
		c.setReg(rd, c.regs[rt]<<uint(sa))
	case 0x02: // SRL, or ROTR when bit 21 (rs field) is 1
		if rs == 1 {
			c.setReg(rd, rotr(c.regs[rt], uint32(sa)))
		} else {
			c.setReg(rd, c.regs[rt]>>uint(sa))
		}
	case 0x03: // SRA
		c.setReg(rd, uint32(int32(c.regs[rt])>>uint(sa)))
	case 0x04: // SLLV
		c.setReg(rd, c.regs[rt]<<(c.regs[rs]&31))
	case 0x06: // SRLV, or ROTRV when bit 6 is set
		if (instr>>6)&1 == 1 {
			c.setReg(rd, rotr(c.regs[rt], c.regs[rs]&31))
		} else {
			c.setReg(rd, c.regs[rt]>>(c.regs[rs]&31))
		}
	case 0x07: // SRAV
		c.setReg(rd, uint32(int32(c.regs[rt])>>(c.regs[rs]&31)))
	case 0x08: // JR
		c.branch(true, c.regs[rs])
	case 0x09: // JALR
		target := c.regs[rs]
		c.setReg(rd, c.curPC+8)
		c.branch(true, target)
	case 0x0A: // MOVZ
		if c.regs[rt] == 0 {
			c.setReg(rd, c.regs[rs])
		}
	case 0x0B: // MOVN
		if c.regs[rt] != 0 {
			c.setReg(rd, c.regs[rs])
		}
	case 0x0C: // SYSCALL
		if c.SyscallHook != nil && c.SyscallHook(c, (instr>>6)&0xFFFFF) {
			return 1
		}
		c.raiseException(excSys, 0)
	case 0x0D: // BREAK
		c.raiseException(excBp, 0)
	case 0x0E: // SDBBP: software debug breakpoint
		c.raiseDebug(excBp)
	case 0x0F: // SYNC
	case 0x10: // MFHI
		c.setReg(rd, c.hi)
	case 0x11: // MTHI
		c.hi = c.regs[rs]
	case 0x12: // MFLO
		c.setReg(rd, c.lo)
	case 0x13: // MTLO
		c.lo = c.regs[rs]
	case 0x18: // MULT
		p := int64(int32(c.regs[rs])) * int64(int32(c.regs[rt]))
		c.hi, c.lo = uint32(uint64(p)>>32), uint32(p)
		return 2
	case 0x19: // MULTU
		p := uint64(c.regs[rs]) * uint64(c.regs[rt])
		c.hi, c.lo = uint32(p>>32), uint32(p)
		return 2
	case 0x1A: // DIV
		a, b := int32(c.regs[rs]), int32(c.regs[rt])
		if b == 0 {
			// Observed Allegrex divide by zero result.
			if a >= 0 {
				c.lo = 0xFFFFFFFF
			} else {
				c.lo = 1
			}
			c.hi = uint32(a)
		} else {
			// Go defines INT_MIN / -1 as INT_MIN with remainder 0,
			// matching MIPS (no trap).
			c.lo = uint32(a / b)
			c.hi = uint32(a % b)
		}
		return 37
	case 0x1B: // DIVU
		a, b := c.regs[rs], c.regs[rt]
		if b == 0 {
			// Observed Allegrex divide by zero result: the
			// quotient is 0xFFFF for dividends under 0x10000
			// and all ones above that.
			if a < 0x10000 {
				c.lo = 0x0000FFFF
			} else {
				c.lo = 0xFFFFFFFF
			}
			c.hi = a
		} else {
			c.lo = a / b
			c.hi = a % b
		}
		return 37
	case 0x16: // CLZ (Allegrex places CLZ/CLO in SPECIAL)
		c.setReg(rd, uint32(countLeading(c.regs[rs], false)))
	case 0x17: // CLO
		c.setReg(rd, uint32(countLeading(c.regs[rs], true)))
	case 0x1C: // MADD (Allegrex places the family in SPECIAL)
		return c.mulAcc(rs, rt, true, false)
	case 0x1D: // MADDU
		return c.mulAcc(rs, rt, false, false)
	case 0x2E: // MSUB
		return c.mulAcc(rs, rt, true, true)
	case 0x2F: // MSUBU
		return c.mulAcc(rs, rt, false, true)
	case 0x20: // ADD
		sum := int64(int32(c.regs[rs])) + int64(int32(c.regs[rt]))
		if sum > 0x7FFFFFFF || sum < -0x80000000 {
			c.raiseException(excOv, 0)
			return 1
		}
		c.setReg(rd, uint32(sum))
	case 0x21: // ADDU
		c.setReg(rd, c.regs[rs]+c.regs[rt])
	case 0x22: // SUB
		d := int64(int32(c.regs[rs])) - int64(int32(c.regs[rt]))
		if d > 0x7FFFFFFF || d < -0x80000000 {
			c.raiseException(excOv, 0)
			return 1
		}
		c.setReg(rd, uint32(d))
	case 0x23: // SUBU
		c.setReg(rd, c.regs[rs]-c.regs[rt])
	case 0x24: // AND
		c.setReg(rd, c.regs[rs]&c.regs[rt])
	case 0x25: // OR
		c.setReg(rd, c.regs[rs]|c.regs[rt])
	case 0x26: // XOR
		c.setReg(rd, c.regs[rs]^c.regs[rt])
	case 0x27: // NOR
		c.setReg(rd, ^(c.regs[rs] | c.regs[rt]))
	case 0x2C: // MAX (Allegrex addition to the SPECIAL table)
		if int32(c.regs[rs]) > int32(c.regs[rt]) {
			c.setReg(rd, c.regs[rs])
		} else {
			c.setReg(rd, c.regs[rt])
		}
	case 0x2D: // MIN (Allegrex addition)
		if int32(c.regs[rs]) < int32(c.regs[rt]) {
			c.setReg(rd, c.regs[rs])
		} else {
			c.setReg(rd, c.regs[rt])
		}
	case 0x2A: // SLT
		if int32(c.regs[rs]) < int32(c.regs[rt]) {
			c.setReg(rd, 1)
		} else {
			c.setReg(rd, 0)
		}
	case 0x2B: // SLTU
		if c.regs[rs] < c.regs[rt] {
			c.setReg(rd, 1)
		} else {
			c.setReg(rd, 0)
		}
	case 0x30, 0x31, 0x32, 0x33, 0x34, 0x36: // trap family
		if trapTaken(fn, c.regs[rs], c.regs[rt]) {
			c.raiseException(excTr, 0)
		}
	default:
		c.raiseException(excRI, 0)
	}
	return 1
}

func trapTaken(fn uint32, a, b uint32) bool {
	switch fn {
	case 0x30: // TGE
		return int32(a) >= int32(b)
	case 0x31: // TGEU
		return a >= b
	case 0x32: // TLT
		return int32(a) < int32(b)
	case 0x33: // TLTU
		return a < b
	case 0x34: // TEQ
		return a == b
	case 0x36: // TNE
		return a != b
	}
	return false
}

func rotr(v, n uint32) uint32 {
	n &= 31
	if n == 0 {
		return v
	}
	return (v >> n) | (v << (32 - n))
}

func (c *CPU) execRegimm(rs, rt int, simm int32) int {
	switch rt {
	case 0x00: // BLTZ
		c.branch(int32(c.regs[rs]) < 0, c.branchTarget(simm))
	case 0x01: // BGEZ
		c.branch(int32(c.regs[rs]) >= 0, c.branchTarget(simm))
	case 0x02: // BLTZL
		c.branchLikely(int32(c.regs[rs]) < 0, c.branchTarget(simm))
	case 0x03: // BGEZL
		c.branchLikely(int32(c.regs[rs]) >= 0, c.branchTarget(simm))
	case 0x10: // BLTZAL: link is written whether or not it branches
		c.setReg(31, c.curPC+8)
		c.branch(int32(c.regs[rs]) < 0, c.branchTarget(simm))
	case 0x11: // BGEZAL: link is written whether or not it branches
		c.setReg(31, c.curPC+8)
		c.branch(int32(c.regs[rs]) >= 0, c.branchTarget(simm))
	case 0x12: // BLTZALL: link is written whether or not it branches
		c.setReg(31, c.curPC+8)
		c.branchLikely(int32(c.regs[rs]) < 0, c.branchTarget(simm))
	case 0x13: // BGEZALL: link is written whether or not it branches
		c.setReg(31, c.curPC+8)
		c.branchLikely(int32(c.regs[rs]) >= 0, c.branchTarget(simm))
	default:
		c.raiseException(excRI, 0)
	}
	return 1
}

func (c *CPU) execSpecial2(instr uint32, rs, rt, rd int, fn uint32) int {
	if instr == 0x70000000 { // HALT: this exact word, all fields zero
		c.halted = true
		return 1
	}
	switch fn {
	case 0x00: // MADD (standard MIPS32 placement; the Allegrex
		// encoding lives in SPECIAL, this form is kept as well)
		return c.mulAcc(rs, rt, true, false)
	case 0x01: // MADDU
		return c.mulAcc(rs, rt, false, false)
	case 0x02: // MUL: writes rd only, HI and LO untouched
		c.setReg(rd, c.regs[rs]*c.regs[rt])
		return 2
	case 0x04: // MSUB
		return c.mulAcc(rs, rt, true, true)
	case 0x05: // MSUBU
		return c.mulAcc(rs, rt, false, true)
	case 0x20: // CLZ
		c.setReg(rd, uint32(countLeading(c.regs[rs], false)))
		return 1
	case 0x21: // CLO
		c.setReg(rd, uint32(countLeading(c.regs[rs], true)))
		return 1
	case 0x24: // MFIC: rt is the GPR, rd selects the IC register
		c.setReg(rt, c.icReg[rd])
		return 1
	case 0x26: // MTIC
		c.icReg[rd] = c.regs[rt]
		return 1
	default:
		c.raiseException(excRI, 0)
		return 1
	}
}

// mulAcc performs the multiply accumulate family: the 64 bit
// HI:LO accumulator gains (add) or loses (sub) the product of the
// two operand registers, signed or unsigned.
func (c *CPU) mulAcc(rs, rt int, signed, sub bool) int {
	acc := (uint64(c.hi) << 32) | uint64(c.lo)
	if signed {
		p := int64(int32(c.regs[rs])) * int64(int32(c.regs[rt]))
		if sub {
			acc = uint64(int64(acc) - p)
		} else {
			acc = uint64(int64(acc) + p)
		}
	} else {
		p := uint64(c.regs[rs]) * uint64(c.regs[rt])
		if sub {
			acc -= p
		} else {
			acc += p
		}
	}
	c.hi, c.lo = uint32(acc>>32), uint32(acc)
	return 2
}

func countLeading(v uint32, ones bool) int {
	n := 0
	for i := 31; i >= 0; i-- {
		bit := (v >> uint(i)) & 1
		if (bit == 1) == ones {
			n++
		} else {
			break
		}
	}
	return n
}

func (c *CPU) execSpecial3(instr uint32, rs, rt, rd, sa int, fn uint32) int {
	switch fn {
	case 0x00: // EXT: rd field holds size-1, sa holds pos
		size := rd + 1
		var mask uint32
		if size >= 32 {
			mask = 0xFFFFFFFF
		} else {
			mask = (uint32(1) << uint(size)) - 1
		}
		c.setReg(rt, (c.regs[rs]>>uint(sa))&mask)
	case 0x04: // INS: rd field holds pos+size-1, sa holds pos
		size := rd - sa + 1
		var mask uint32
		if size >= 32 {
			mask = 0xFFFFFFFF
		} else {
			mask = ((uint32(1) << uint(size)) - 1) << uint(sa)
		}
		v := (c.regs[rs] << uint(sa)) & mask
		c.setReg(rt, (c.regs[rt]&^mask)|v)
	case 0x20: // BSHFL
		switch sa {
		case 2: // WSBH: swap bytes in each halfword
			v := c.regs[rt]
			v = ((v & 0x00FF00FF) << 8) | ((v & 0xFF00FF00) >> 8)
			c.setReg(rd, v)
		case 3: // WSBW: swap all four bytes (byte reverse)
			v := c.regs[rt]
			v = (v << 24) | ((v & 0xFF00) << 8) | ((v >> 8) & 0xFF00) | (v >> 24)
			c.setReg(rd, v)
		case 16: // SEB: sign extend byte
			c.setReg(rd, uint32(int32(int8(c.regs[rt]))))
		case 20: // BITREV: reverse all 32 bits
			v := c.regs[rt]
			var r uint32
			for i := 0; i < 32; i++ {
				if v&(1<<uint(i)) != 0 {
					r |= 1 << uint(31-i)
				}
			}
			c.setReg(rd, r)
		case 24: // SEH: sign extend halfword
			c.setReg(rd, uint32(int32(int16(c.regs[rt]))))
		default:
			c.raiseException(excRI, 0)
		}
	default:
		c.raiseException(excRI, 0)
	}
	return 1
}

func (c *CPU) execCOP0(instr uint32, rs, rt, rd int) int {
	sel := int(instr & 7)
	switch {
	case rs == 0x00: // MFC0
		c.setReg(rt, c.readCP0(rd, sel))
	case rs == 0x04: // MTC0
		c.writeCP0(rd, sel, c.regs[rt])
	case rs >= 0x10: // CO function field
		switch instr & 0x3F {
		case 0x01, 0x02, 0x06, 0x08: // TLBR, TLBWI, TLBWR, TLBP
			// No TLB exists in this core; these decode as no-ops.
		case 0x18: // ERET
			if c.cp0[12][0]&statusERL != 0 {
				c.cp0[12][0] &^= statusERL
				c.setPCVector(c.cp0[30][0]) // ErrorEPC
			} else {
				c.cp0[12][0] &^= statusEXL
				c.setPCVector(c.cp0[14][0]) // EPC
			}
			c.llbit = false
		case 0x1F: // DERET: simplification, jump to DEPC only
			c.setPCVector(c.cp0[24][0])
		case 0x20: // WAIT: like HALT, sleep until an enabled interrupt
			c.halted = true
		default:
			c.raiseException(excRI, 0)
		}
	default:
		c.raiseException(excRI, 0)
	}
	return 1
}

// addrError raises the given address error with BadVAddr set and
// performs no bus access.
func (c *CPU) addrError(code uint32, addr uint32) int {
	c.cp0[8][0] = addr
	c.raiseException(code, 0)
	return 1
}

func (c *CPU) execLoadStore(op uint32, rs, rt int, simm int32) int {
	addr := c.regs[rs] + uint32(simm)
	switch op {
	case 0x20: // LB
		c.setReg(rt, uint32(int32(int8(c.bus.Read8(addr)))))
	case 0x24: // LBU
		c.setReg(rt, uint32(c.bus.Read8(addr)))
	case 0x21: // LH
		if addr&1 != 0 {
			return c.addrError(excAdEL, addr)
		}
		c.setReg(rt, uint32(int32(int16(c.bus.Read16(addr)))))
	case 0x25: // LHU
		if addr&1 != 0 {
			return c.addrError(excAdEL, addr)
		}
		c.setReg(rt, uint32(c.bus.Read16(addr)))
	case 0x23, 0x30: // LW, LL
		if addr&3 != 0 {
			return c.addrError(excAdEL, addr)
		}
		c.setReg(rt, c.bus.Read32(addr))
		if op == 0x30 {
			c.llbit = true
			c.lladdr = addr
		}
	case 0x22: // LWL: the aligned word shifts into the high end
		// of rt; the low (3-a) bytes survive. (Shift form, pinned
		// against the pspautotests lsu matrix: the byte loop this
		// replaces merged in big-endian order.)
		w := c.bus.Read32(addr &^ 3)
		shift := uint(8 * (3 - (addr & 3)))
		keep := uint32((uint64(1) << shift) - 1)
		c.setReg(rt, (w<<shift)|(c.regs[rt]&keep))
	case 0x26: // LWR
		w := c.bus.Read32(addr &^ 3)
		a := addr & 3
		v := c.regs[rt]
		for j := uint32(0); j <= 3-a; j++ {
			v = putByte(v, j, getByte(w, a+j))
		}
		c.setReg(rt, v)
	case 0x28: // SB
		c.bus.Write8(addr, uint8(c.regs[rt]))
	case 0x29: // SH
		if addr&1 != 0 {
			return c.addrError(excAdES, addr)
		}
		c.bus.Write16(addr, uint16(c.regs[rt]))
	case 0x2B: // SW
		if addr&3 != 0 {
			return c.addrError(excAdES, addr)
		}
		c.bus.Write32(addr, c.regs[rt])
	case 0x2A: // SWL: read modify write of the aligned word; the
		// low (a+1) bytes of the word take rt's high bytes (the
		// mirror of LWL, same shift).
		aligned := addr &^ 3
		w := c.bus.Read32(aligned)
		a := addr & 3
		shift := uint(8 * (3 - a))
		mask := uint32((uint64(1) << (8 * (a + 1))) - 1)
		w = (w &^ mask) | ((c.regs[rt] >> shift) & mask)
		c.bus.Write32(aligned, w)
	case 0x2E: // SWR
		aligned := addr &^ 3
		w := c.bus.Read32(aligned)
		a := addr & 3
		for j := uint32(0); j <= 3-a; j++ {
			w = putByte(w, a+j, getByte(c.regs[rt], j))
		}
		c.bus.Write32(aligned, w)
	case 0x38: // SC: succeeds while the LL reservation stands.
		// The reservation is NOT consumed by a successful SC on
		// this hardware: the pspautotests llsc matrix has a
		// second SC to the reserved address succeed as well.
		// Syscalls, exceptions and thread switches are what
		// break it (see ClearLL).
		if addr&3 != 0 {
			return c.addrError(excAdES, addr)
		}
		v := c.regs[rt]
		if c.llbit {
			c.bus.Write32(addr, v)
			c.setReg(rt, 1)
		} else {
			c.setReg(rt, 0)
		}
	case 0x31: // LWC1
		if addr&3 != 0 {
			return c.addrError(excAdEL, addr)
		}
		c.fpr[rt] = c.bus.Read32(addr)
	case 0x39: // SWC1
		if addr&3 != 0 {
			return c.addrError(excAdES, addr)
		}
		c.bus.Write32(addr, c.fpr[rt])
	default:
		// Includes LDC1 (0x35) and SDC1 (0x3D): the Allegrex FPU
		// has no double precision, so they are reserved. COP2
		// loads and stores are handled in execute before here.
		c.raiseException(excRI, 0)
	}
	return 1
}

// getByte returns byte j of w, where byte 0 is the least significant.
func getByte(w, j uint32) uint32 { return (w >> (8 * j)) & 0xFF }

func putByte(w, j, b uint32) uint32 {
	w &^= 0xFF << (8 * j)
	return w | (b&0xFF)<<(8*j)
}
