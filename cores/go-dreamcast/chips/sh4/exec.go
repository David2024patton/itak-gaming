package sh4

// Integer instruction execution. exec decodes and executes one
// 16-bit instruction at address pc. inDelay reports that the
// instruction occupies a delay slot; branch class instructions are
// filtered out before exec runs (see Step), and any remaining
// illegal encoding in a delay slot raises the slot illegal exception
// instead of the general one.
//
// On entry c.PC is advanced to the next sequential instruction;
// delayed branches instead arrange for the delay slot to execute
// next and for Step to continue at the branch target afterwards.

func sext8(v uint32) uint32  { return uint32(int32(int8(v))) }
func sext16(v uint32) uint32 { return uint32(int32(int16(v))) }

// delayedBranch arranges delay-slot execution: the next Step runs
// the instruction at pc+2, then continues at target if taken, or at
// pc+4 if not taken (the BT/S and BF/S not-taken case).
func (c *CPU) delayedBranch(pc uint32, target uint32, taken bool) {
	c.inDelay = true
	if taken {
		c.afterDelay = target
	} else {
		c.afterDelay = pc + 4
	}
	c.PC = pc + 2
}

// ctrlReg reads a control register by its LDC/STC code:
// 0 = SR, 1 = GBR, 2 = VBR, 3 = SSR, 4 = SPC.
func (c *CPU) ctrlReg(code uint32) (uint32, bool) {
	switch code {
	case 0:
		return c.SR, true
	case 1:
		return c.GBR, true
	case 2:
		return c.VBR, true
	case 3:
		return c.SSR, true
	case 4:
		return c.SPC, true
	}
	return 0, false
}

func (c *CPU) setCtrlReg(code uint32, v uint32) bool {
	switch code {
	case 0:
		c.setSR(v)
	case 1:
		c.GBR = v
	case 2:
		c.VBR = v
	case 3:
		c.SSR = v
	case 4:
		c.SPC = v
	default:
		return false
	}
	return true
}

// ctrlPrivileged reports whether a control register code requires
// privileged mode. GBR is the exception: it is user accessible.
func ctrlPrivileged(code uint32) bool { return code != 1 }

// sysReg reads a system register by its LDS/STS code:
// 0 = MACH, 1 = MACL, 2 = PR, 5 = FPUL, 6 = FPSCR.
func (c *CPU) sysReg(code uint32) (uint32, bool) {
	switch code {
	case 0:
		return c.MACH, true
	case 1:
		return c.MACL, true
	case 2:
		return c.PR, true
	case 5:
		return c.FPUL, true
	case 6:
		return c.FPSCR, true
	}
	return 0, false
}

func (c *CPU) setSysReg(code uint32, v uint32) bool {
	switch code {
	case 0:
		c.MACH = v
	case 1:
		c.MACL = v
	case 2:
		c.PR = v
	case 5:
		c.FPUL = v
	case 6:
		c.setFPSCR(v)
	default:
		return false
	}
	return true
}

// needPriv raises the illegal instruction exception when the CPU is
// in user mode (SR.MD = 0) and the instruction is privileged.
func (c *CPU) needPriv(inDelay bool, pc uint32) bool {
	if c.SR&srMD == 0 {
		c.illegal(inDelay, pc)
		return false
	}
	return true
}

func (c *CPU) exec(op uint16, pc uint32, inDelay bool) {
	n := int(op >> 8 & 0xF)
	m := int(op >> 4 & 0xF)
	low := op & 0xF
	imm := uint32(op & 0xFF)
	disp4 := uint32(op & 0xF)

	c.PC = pc + 2

	switch op >> 12 {
	case 0x0:
		c.execGroup0(op, pc, inDelay, n, m, low)
	case 0x1: // MOV.L Rm,@(disp,Rn)
		addr := c.R[n] + disp4*4
		if !c.write32(addr, c.R[m]) {
			c.enterMemFault(pc)
		}
	case 0x2:
		c.execGroup2(pc, inDelay, n, m, low)
	case 0x3:
		c.execGroup3(pc, inDelay, n, m, low)
	case 0x4:
		c.execGroup4(op, pc, inDelay, n, m, low)
	case 0x5: // MOV.L @(disp,Rm),Rn
		addr := c.R[m] + disp4*4
		v, ok := c.read32(addr)
		if !ok {
			c.enterMemFault(pc)
			return
		}
		c.R[n] = v
	case 0x6:
		c.execGroup6(pc, inDelay, n, m, low)
	case 0x7: // ADD #imm,Rn
		c.R[n] += sext8(imm)
	case 0x8:
		c.execGroup8(op, pc, inDelay)
	case 0x9: // MOV.W @(disp,PC),Rn
		addr := pc + 4 + imm*2
		v, ok := c.read16(addr)
		if !ok {
			c.enterMemFault(pc)
			return
		}
		c.R[n] = sext16(uint32(v))
	case 0xA: // BRA
		disp := int32(op & 0xFFF)
		if disp&0x800 != 0 {
			disp |= ^int32(0xFFF)
		}
		c.delayedBranch(pc, pc+4+uint32(disp*2), true)
	case 0xB: // BSR
		disp := int32(op & 0xFFF)
		if disp&0x800 != 0 {
			disp |= ^int32(0xFFF)
		}
		c.PR = pc + 4
		c.delayedBranch(pc, pc+4+uint32(disp*2), true)
	case 0xC:
		c.execGroupC(op, pc, inDelay)
	case 0xD: // MOV.L @(disp,PC),Rn
		addr := (pc & 0xFFFFFFFC) + 4 + imm*4
		v, ok := c.read32(addr)
		if !ok {
			c.enterMemFault(pc)
			return
		}
		c.R[n] = v
	case 0xE: // MOV #imm,Rn
		c.R[n] = sext8(imm)
	case 0xF:
		c.execFPU(op, pc, inDelay)
	}
}

// execGroup0 handles the 0000 opcode family: indexed MOV forms,
// MUL.L, MAC.L, STC/STS, MOVT, and the fixed system instructions.
func (c *CPU) execGroup0(op uint16, pc uint32, inDelay bool, n, m int, low uint16) {
	switch low {
	case 0x3:
		switch m {
		case 0x0: // BSRF Rn
			target := pc + 4 + c.R[n]
			c.PR = pc + 4
			c.delayedBranch(pc, target, true)
		case 0x2: // BRAF Rn
			c.delayedBranch(pc, pc+4+c.R[n], true)
		case 0x8: // PREF @Rn: cache prefetch, no-op without caches
		case 0x9, 0xA, 0xB: // OCBI, OCBP, OCBWB @Rn: no-ops without caches
		case 0xC: // MOVCA.L R0,@Rn
			if !c.write32(c.R[n], c.R[0]) {
				c.enterMemFault(pc)
			}
		default:
			c.illegal(inDelay, pc)
		}
	case 0x4, 0x5, 0x6: // MOV.B/W/L Rm,@(R0,Rn)
		addr := c.R[n] + c.R[0]
		c.storeSize(pc, addr, c.R[m], int(low-0x4))
	case 0xC, 0xD, 0xE: // MOV.B/W/L @(R0,Rm),Rn
		addr := c.R[m] + c.R[0]
		v := c.loadSize(pc, addr, int(low-0xC))
		if c.excepted {
			return
		}
		c.R[n] = v
	case 0x7: // MUL.L Rm,Rn
		c.MACL = c.R[n] * c.R[m]
	case 0xF: // MAC.L @Rm+,@Rn+
		c.macL(pc, n, m)
	case 0x2: // STC ctrl,Rn or STC Rm_BANK,Rn
		if m >= 8 { // bank form: source is the inactive bank
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.R[n] = c.Bank[m&7]
			return
		}
		code := uint32(m)
		if ctrlPrivileged(code) && !c.needPriv(inDelay, pc) {
			return
		}
		v, ok := c.ctrlReg(code)
		if !ok {
			c.illegal(inDelay, pc)
			return
		}
		c.R[n] = v
	case 0xA: // STS sys,Rn, plus STC SGR/DBR special encodings
		switch m {
		case 0x3: // STC SGR,Rn
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.R[n] = c.SGR
		case 0xF: // STC DBR,Rn
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.R[n] = c.DBR
		default:
			v, ok := c.sysReg(uint32(m))
			if !ok {
				c.illegal(inDelay, pc)
				return
			}
			c.R[n] = v
		}
	case 0x9:
		if m == 2 { // MOVT Rn
			c.R[n] = c.getT()
		} else if op == 0x0019 { // DIV0U
			c.setQM(0, 0)
			c.setT(0)
		} else if op == 0x0009 { // NOP
		} else {
			c.illegal(inDelay, pc)
		}
	case 0x8:
		if n != 0 {
			c.illegal(inDelay, pc)
			return
		}
		switch m {
		case 0x0: // CLRT
			c.setT(0)
		case 0x1: // SETT
			c.setT(1)
		case 0x2: // CLRMAC
			c.MACH, c.MACL = 0, 0
		case 0x3: // LDTLB
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.loadTLB()
		case 0x4: // CLRS
			c.setS(0)
		case 0x5: // SETS
			c.setS(1)
		default:
			c.illegal(inDelay, pc)
		}
	case 0xB:
		if n != 0 {
			c.illegal(inDelay, pc)
			return
		}
		switch m {
		case 0x0: // RTS
			c.delayedBranch(pc, c.PR, true)
		case 0x1: // SLEEP
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.Halted = true
		case 0x2: // RTE
			if !c.needPriv(inDelay, pc) {
				return
			}
			target := c.SPC
			c.setSR(c.SSR)
			c.delayedBranch(pc, target, true)
		default:
			c.illegal(inDelay, pc)
		}
	default:
		c.illegal(inDelay, pc)
	}
}

// loadSize loads a byte (size 0, sign extended), word (size 1, sign
// extended), or longword (size 2) and vectors a fault itself.
func (c *CPU) loadSize(pc uint32, addr uint32, size int) uint32 {
	switch size {
	case 0:
		v, ok := c.read8(addr)
		if !ok {
			c.enterMemFault(pc)
			return 0
		}
		return sext8(uint32(v))
	case 1:
		v, ok := c.read16(addr)
		if !ok {
			c.enterMemFault(pc)
			return 0
		}
		return sext16(uint32(v))
	default:
		v, ok := c.read32(addr)
		if !ok {
			c.enterMemFault(pc)
			return 0
		}
		return v
	}
}

// storeSize stores the low bytes of v at addr; size is 0, 1, or 2
// for byte, word, or longword.
func (c *CPU) storeSize(pc uint32, addr uint32, v uint32, size int) {
	var ok bool
	switch size {
	case 0:
		ok = c.write8(addr, uint8(v))
	case 1:
		ok = c.write16(addr, uint16(v))
	default:
		ok = c.write32(addr, v)
	}
	if !ok {
		c.enterMemFault(pc)
	}
}

// macL implements MAC.L @Rm+,@Rn+. When Rm and Rn are the same
// register, two consecutive longwords are read and the register is
// post-incremented twice, per the manual. With SR.S = 1 the 64-bit
// sum is saturated to a signed 48-bit range (with the manual's
// overflow rule: a sum that overflows 64 bits saturates to
// 2^47-1 or -2^47 according to the sign of the accumulator).
func (c *CPU) macL(pc uint32, n, m int) {
	var vN, vM uint32
	var ok bool
	if m == n {
		if vN, ok = c.read32(c.R[n]); !ok {
			c.enterMemFault(pc)
			return
		}
		if vM, ok = c.read32(c.R[n] + 4); !ok {
			c.enterMemFault(pc)
			return
		}
		c.R[n] += 8
	} else {
		if vN, ok = c.read32(c.R[n]); !ok {
			c.enterMemFault(pc)
			return
		}
		if vM, ok = c.read32(c.R[m]); !ok {
			c.enterMemFault(pc)
			return
		}
		c.R[n] += 4
		c.R[m] += 4
	}
	prod := int64(int32(vN)) * int64(int32(vM))
	acc := int64(uint64(c.MACH)<<32 | uint64(c.MACL))
	sum := acc + prod
	if c.SR&srS != 0 {
		const max48 = int64(1)<<47 - 1
		const min48 = -int64(1) << 47
		if (prod > 0 && sum < acc) || (prod < 0 && sum > acc) {
			if acc >= 0 {
				sum = max48
			} else {
				sum = min48
			}
		} else if sum > max48 {
			sum = max48
		} else if sum < min48 {
			sum = min48
		}
	}
	c.MACH = uint32(sum >> 32)
	c.MACL = uint32(sum)
}

// macW implements MAC.W @Rm+,@Rn+ with the same addressing rules as
// MAC.L. With SR.S = 1 only MACL accumulates, the 33-bit sum is
// saturated to the signed 32-bit range, and on saturation MACH is
// set to 1 (otherwise MACH is unchanged), per the manual.
func (c *CPU) macW(pc uint32, n, m int) {
	var vN, vM uint16
	var ok bool
	if m == n {
		if vN, ok = c.read16(c.R[n]); !ok {
			c.enterMemFault(pc)
			return
		}
		if vM, ok = c.read16(c.R[n] + 2); !ok {
			c.enterMemFault(pc)
			return
		}
		c.R[n] += 4
	} else {
		if vN, ok = c.read16(c.R[n]); !ok {
			c.enterMemFault(pc)
			return
		}
		if vM, ok = c.read16(c.R[m]); !ok {
			c.enterMemFault(pc)
			return
		}
		c.R[n] += 2
		c.R[m] += 2
	}
	prod := int64(int32(int16(vN))) * int64(int32(int16(vM)))
	if c.SR&srS != 0 {
		sum := int64(int32(c.MACL)) + prod
		sat := sum
		if sat > 0x7FFFFFFF {
			sat = 0x7FFFFFFF
		} else if sat < -0x80000000 {
			sat = -0x80000000
		}
		if sat != sum {
			c.MACH = 1
		}
		c.MACL = uint32(int32(sat))
		return
	}
	acc := int64(uint64(c.MACH)<<32|uint64(c.MACL)) + prod
	c.MACH = uint32(acc >> 32)
	c.MACL = uint32(acc)
}

// execGroup2 handles 0010 opcodes: register-indirect stores,
// pre-decrement stores, and register-register logic and multiply ops.
func (c *CPU) execGroup2(pc uint32, inDelay bool, n, m int, low uint16) {
	switch low {
	case 0x0, 0x1, 0x2: // MOV.B/W/L Rm,@Rn
		c.storeSize(pc, c.R[n], c.R[m], int(low))
	case 0x4, 0x5, 0x6: // MOV.B/W/L Rm,@-Rn
		size := int(low - 0x4)
		c.R[n] -= uint32(1 << size)
		c.storeSize(pc, c.R[n], c.R[m], size)
	case 0x7: // DIV0S Rm,Rn
		q := c.R[n] >> 31
		mm := c.R[m] >> 31
		c.setQM(q, mm)
		c.setT(q ^ mm)
	case 0x8: // TST Rm,Rn
		if c.R[n]&c.R[m] == 0 {
			c.setT(1)
		} else {
			c.setT(0)
		}
	case 0x9: // AND Rm,Rn
		c.R[n] &= c.R[m]
	case 0xA: // XOR Rm,Rn
		c.R[n] ^= c.R[m]
	case 0xB: // OR Rm,Rn
		c.R[n] |= c.R[m]
	case 0xC: // CMP/STR Rm,Rn
		x := c.R[n] ^ c.R[m]
		t := uint32(0)
		for i := 0; i < 4; i++ {
			if byte(x>>uint(8*i)) == 0 {
				t = 1
			}
		}
		c.setT(t)
	case 0xD: // XTRCT Rm,Rn
		c.R[n] = c.R[m]&0xFFFF<<16 | c.R[n]>>16
	case 0xE: // MULU.W Rm,Rn
		c.MACL = uint32(c.R[n]&0xFFFF) * uint32(c.R[m]&0xFFFF)
	case 0xF: // MULS.W Rm,Rn
		c.MACL = uint32(int32(int16(c.R[n])) * int32(int16(c.R[m])))
	default:
		c.illegal(inDelay, pc)
	}
}

// execGroup3 handles 0011 opcodes: arithmetic, compares, DIV1, and
// the double-length multiplies.
func (c *CPU) execGroup3(pc uint32, inDelay bool, n, m int, low uint16) {
	a, b := c.R[n], c.R[m]
	switch low {
	case 0x0: // CMP/EQ Rm,Rn
		c.setT(boolU32(a == b))
	case 0x2: // CMP/HS Rm,Rn (unsigned >=)
		c.setT(boolU32(a >= b))
	case 0x3: // CMP/GE Rm,Rn (signed >=)
		c.setT(boolU32(int32(a) >= int32(b)))
	case 0x6: // CMP/HI Rm,Rn (unsigned >)
		c.setT(boolU32(a > b))
	case 0x7: // CMP/GT Rm,Rn (signed >)
		c.setT(boolU32(int32(a) > int32(b)))
	case 0x4: // DIV1 Rm,Rn
		c.div1(n, m)
	case 0x5: // DMULU.L Rm,Rn
		p := uint64(a) * uint64(b)
		c.MACL = uint32(p)
		c.MACH = uint32(p >> 32)
	case 0xD: // DMULS.L Rm,Rn
		p := int64(int32(a)) * int64(int32(b))
		c.MACL = uint32(p)
		c.MACH = uint32(p >> 32)
	case 0x8: // SUB Rm,Rn
		c.R[n] = a - b
	case 0xA: // SUBC Rm,Rn
		sub := uint64(b) + uint64(c.getT())
		res := uint64(a) - sub
		c.R[n] = uint32(res)
		if uint64(a) < sub {
			c.setT(1)
		} else {
			c.setT(0)
		}
	case 0xB: // SUBV Rm,Rn
		res := int64(int32(a)) - int64(int32(b))
		c.R[n] = uint32(int32(res))
		if res > 0x7FFFFFFF || res < -0x80000000 {
			c.setT(1)
		} else {
			c.setT(0)
		}
	case 0xC: // ADD Rm,Rn
		c.R[n] = a + b
	case 0xE: // ADDC Rm,Rn
		sum := uint64(a) + uint64(b) + uint64(c.getT())
		c.R[n] = uint32(sum)
		c.setT(uint32(sum >> 32 & 1))
	case 0xF: // ADDV Rm,Rn
		res := int64(int32(a)) + int64(int32(b))
		c.R[n] = uint32(int32(res))
		if res > 0x7FFFFFFF || res < -0x80000000 {
			c.setT(1)
		} else {
			c.setT(0)
		}
	default:
		c.illegal(inDelay, pc)
	}
}

func boolU32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// div1 performs one DIV1 step: Rn is the partial-remainder register,
// Rm the divisor, and the SR Q, M, and T bits carry the state set up
// by DIV0S or DIV0U. Q records the sign of the 33-bit partial
// remainder (whose low half is Rn); each step shifts the remainder
// left, brings the previous quotient bit in through T, adds or
// subtracts the divisor depending on whether the old remainder sign
// equals M, and produces the next quotient bit in T. The full
// division sequence built on this (ROTCL on the dividend register
// interleaved with DIV1, one final ROTCL, and the closing remainder
// correction) is exercised in the tests.
func (c *CPU) div1(n, m int) {
	oldQ := c.getQ()
	mBit := c.getM()
	q := c.R[n] >> 31
	shifted := c.R[n]<<1 | c.getT()
	var res, carry uint32
	if oldQ == mBit {
		res = shifted - c.R[m]
		if res > shifted {
			carry = 1
		}
	} else {
		res = shifted + c.R[m]
		if res < shifted {
			carry = 1
		}
	}
	qn := q ^ mBit ^ carry
	c.R[n] = res
	c.setQM(qn, mBit)
	c.setT(1 - (qn ^ mBit))
}

// execGroup6 handles 0110 opcodes: register-indirect loads,
// post-increment loads, register MOV, and unary operations.
func (c *CPU) execGroup6(pc uint32, inDelay bool, n, m int, low uint16) {
	switch low {
	case 0x0, 0x1, 0x2: // MOV.B/W/L @Rm,Rn
		v := c.loadSize(pc, c.R[m], int(low))
		if c.excepted {
			return
		}
		c.R[n] = v
	case 0x3: // MOV Rm,Rn
		c.R[n] = c.R[m]
	case 0x4, 0x5, 0x6: // MOV.B/W/L @Rm+,Rn
		size := int(low - 0x4)
		addr := c.R[m]
		v := c.loadSize(pc, addr, size)
		if c.excepted {
			return
		}
		c.R[m] = addr + uint32(1<<size)
		c.R[n] = v
	case 0x7: // NOT Rm,Rn
		c.R[n] = ^c.R[m]
	case 0x8: // SWAP.B Rm,Rn
		v := c.R[m]
		c.R[n] = v&0xFFFF0000 | (v&0xFF)<<8 | (v>>8)&0xFF
	case 0x9: // SWAP.W Rm,Rn
		v := c.R[m]
		c.R[n] = v<<16 | v>>16
	case 0xA: // NEGC Rm,Rn
		sub := uint64(c.R[m]) + uint64(c.getT())
		c.R[n] = uint32(-int64(sub))
		if sub != 0 {
			c.setT(1)
		} else {
			c.setT(0)
		}
	case 0xB: // NEG Rm,Rn
		c.R[n] = -c.R[m]
	case 0xC: // EXTU.B Rm,Rn
		c.R[n] = c.R[m] & 0xFF
	case 0xD: // EXTU.W Rm,Rn
		c.R[n] = c.R[m] & 0xFFFF
	case 0xE: // EXTS.B Rm,Rn
		c.R[n] = sext8(c.R[m])
	case 0xF: // EXTS.W Rm,Rn
		c.R[n] = sext16(c.R[m])
	default:
		c.illegal(inDelay, pc)
	}
}

// execGroup8 handles the 1000 family selected by the second byte:
// displacement byte and word stores and loads through R0, CMP/EQ
// with an immediate, and the short conditional branches.
func (c *CPU) execGroup8(op uint16, pc uint32, inDelay bool) {
	imm := uint32(op & 0xFF)
	reg := int(op >> 4 & 0xF)
	disp := uint32(op & 0xF)
	switch op >> 8 {
	case 0x80: // MOV.B R0,@(disp,Rn)
		c.storeSize(pc, c.R[reg]+disp, c.R[0], 0)
	case 0x81: // MOV.W R0,@(disp,Rn)
		c.storeSize(pc, c.R[reg]+disp*2, c.R[0], 1)
	case 0x84: // MOV.B @(disp,Rm),R0
		v := c.loadSize(pc, c.R[reg]+disp, 0)
		if !c.excepted {
			c.R[0] = v
		}
	case 0x85: // MOV.W @(disp,Rm),R0
		v := c.loadSize(pc, c.R[reg]+disp*2, 1)
		if !c.excepted {
			c.R[0] = v
		}
	case 0x88: // CMP/EQ #imm,R0
		c.setT(boolU32(c.R[0] == sext8(imm)))
	case 0x89, 0x8B: // BT, BF: delay slot executes only if taken
		taken := (op>>8 == 0x89) == (c.getT() == 1)
		if taken {
			target := pc + 4 + uint32(int32(int8(imm))*2)
			c.delayedBranch(pc, target, true)
		} else {
			c.PC = pc + 4 // skip the delay slot entirely
		}
	case 0x8D, 0x8F: // BT/S, BF/S: delay slot always executes
		taken := (op>>8 == 0x8D) == (c.getT() == 1)
		target := pc + 4 + uint32(int32(int8(imm))*2)
		c.delayedBranch(pc, target, taken)
	default:
		c.illegal(inDelay, pc)
	}
}

// execGroupC handles the 1100 family selected by the second byte:
// GBR-relative transfers, MOVA, immediate logic ops, the GBR byte
// logic ops, and TRAPA.
func (c *CPU) execGroupC(op uint16, pc uint32, inDelay bool) {
	imm := uint32(op & 0xFF)
	gbrAddr := func(scale uint32) uint32 { return c.GBR + imm*scale }
	switch op >> 8 {
	case 0xC0: // MOV.B R0,@(disp,GBR)
		c.storeSize(pc, gbrAddr(1), c.R[0], 0)
	case 0xC1: // MOV.W R0,@(disp,GBR)
		c.storeSize(pc, gbrAddr(2), c.R[0], 1)
	case 0xC2: // MOV.L R0,@(disp,GBR)
		c.storeSize(pc, gbrAddr(4), c.R[0], 2)
	case 0xC4: // MOV.B @(disp,GBR),R0
		v := c.loadSize(pc, gbrAddr(1), 0)
		if !c.excepted {
			c.R[0] = v
		}
	case 0xC5: // MOV.W @(disp,GBR),R0
		v := c.loadSize(pc, gbrAddr(2), 1)
		if !c.excepted {
			c.R[0] = v
		}
	case 0xC6: // MOV.L @(disp,GBR),R0
		v := c.loadSize(pc, gbrAddr(4), 2)
		if !c.excepted {
			c.R[0] = v
		}
	case 0xC7: // MOVA @(disp,PC),R0
		c.R[0] = (pc & 0xFFFFFFFC) + 4 + imm*4
	case 0xC8: // TST #imm,R0
		c.setT(boolU32(c.R[0]&imm == 0))
	case 0xC9: // AND #imm,R0
		c.R[0] &= imm
	case 0xCA: // XOR #imm,R0
		c.R[0] ^= imm
	case 0xCB: // OR #imm,R0
		c.R[0] |= imm
	case 0xCC, 0xCD, 0xCE, 0xCF: // TST.B / AND.B / XOR.B / OR.B #imm,@(R0,GBR)
		addr := c.GBR + c.R[0]
		v, ok := c.read8(addr)
		if !ok {
			c.enterMemFault(pc)
			return
		}
		switch op >> 8 {
		case 0xCC:
			c.setT(boolU32(uint32(v)&imm == 0))
		case 0xCD:
			if !c.write8(addr, uint8(uint32(v)&imm)) {
				c.enterMemFault(pc)
			}
		case 0xCE:
			if !c.write8(addr, uint8(uint32(v)^imm)) {
				c.enterMemFault(pc)
			}
		case 0xCF:
			if !c.write8(addr, uint8(uint32(v)|imm)) {
				c.enterMemFault(pc)
			}
		}
	case 0xC3: // TRAPA #imm
		c.TRA = imm << 2
		c.enterException(excTRAPA, pc+2)
	default:
		c.illegal(inDelay, pc)
	}
}
