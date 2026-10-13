package sh4

// execGroup4 handles the 0100 opcode family, the most overloaded
// one: MAC.W, the dynamic shifts SHAD and SHLD, JMP and JSR, TAS.B,
// the unary shifts and rotates, CMP/PZ and CMP/PL, DT, and the whole
// LDC/STC/LDS/STS register-move family (including the .L memory
// forms and the Rn_BANK forms). The low nibble selects the sub
// family; the middle nibble is either a register number (MAC.W,
// SHAD, SHLD) or a system/control register code.

func (c *CPU) execGroup4(op uint16, pc uint32, inDelay bool, n, m int, low uint16) {
	switch low {
	case 0xF: // MAC.W @Rm+,@Rn+
		c.macW(pc, n, m)
		return
	case 0xC: // SHAD Rm,Rn
		c.shad(n, m, true)
		return
	case 0xD: // SHLD Rm,Rn
		c.shad(n, m, false)
		return
	}

	code := uint32(m)

	switch low {
	case 0x2: // STS.L sys,@-Rn, plus STC.L SGR/DBR encodings
		if m == 3 || m == 15 {
			if !c.needPriv(inDelay, pc) {
				return
			}
			v := c.SGR
			if m == 15 {
				v = c.DBR
			}
			c.R[n] -= 4
			c.storeSize(pc, c.R[n], v, 2)
			return
		}
		v, ok := c.sysReg(code)
		if !ok {
			c.illegal(inDelay, pc)
			return
		}
		c.R[n] -= 4
		c.storeSize(pc, c.R[n], v, 2)
	case 0x3: // STC.L ctrl,@-Rn or STC.L Rm_BANK,@-Rn
		if m >= 8 {
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.R[n] -= 4
			c.storeSize(pc, c.R[n], c.Bank[m&7], 2)
			return
		}
		if ctrlPrivileged(code) && !c.needPriv(inDelay, pc) {
			return
		}
		v, ok := c.ctrlReg(code)
		if !ok {
			c.illegal(inDelay, pc)
			return
		}
		c.R[n] -= 4
		c.storeSize(pc, c.R[n], v, 2)
	case 0x6: // LDS.L @Rm+,sys, plus LDC.L SGR/DBR encodings
		if m == 3 || m == 15 {
			// LDC.L @Rm+,SGR and LDC.L @Rm+,DBR: the pointer is
			// the register in bits 11..8 (n in this decode) and
			// the code sits in m, as in the other .L forms.
			if !c.needPriv(inDelay, pc) {
				return
			}
			v := c.loadSize(pc, c.R[n], 2)
			if c.excepted {
				return
			}
			c.R[n] += 4
			if m == 3 {
				c.SGR = v
			} else {
				c.DBR = v
			}
			return
		}
		v := c.loadSize(pc, c.R[n], 2)
		if c.excepted {
			return
		}
		c.R[n] += 4
		if !c.setSysReg(code, v) {
			c.illegal(inDelay, pc)
		}
	case 0x7: // LDC.L @Rm+,ctrl or LDC.L @Rm+,Rn_BANK
		if m >= 8 {
			if !c.needPriv(inDelay, pc) {
				return
			}
			v := c.loadSize(pc, c.R[n], 2)
			if c.excepted {
				return
			}
			c.R[n] += 4
			c.Bank[m&7] = v
			return
		}
		if ctrlPrivileged(code) && !c.needPriv(inDelay, pc) {
			return
		}
		v := c.loadSize(pc, c.R[n], 2)
		if c.excepted {
			return
		}
		c.R[n] += 4
		if !c.setCtrlReg(code, v) {
			c.illegal(inDelay, pc)
		}
	case 0xA: // LDS Rm,sys, plus LDC Rm,SGR / LDC Rm,DBR
		switch m {
		case 3:
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.SGR = c.R[n]
		case 15:
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.DBR = c.R[n]
		default:
			if !c.setSysReg(code, c.R[n]) {
				c.illegal(inDelay, pc)
			}
		}
	case 0xE: // LDC Rm,ctrl or LDC Rm,Rn_BANK
		if m >= 8 {
			if !c.needPriv(inDelay, pc) {
				return
			}
			c.Bank[m&7] = c.R[n]
			return
		}
		if ctrlPrivileged(code) && !c.needPriv(inDelay, pc) {
			return
		}
		if !c.setCtrlReg(code, c.R[n]) {
			c.illegal(inDelay, pc)
		}
	case 0xB:
		switch m {
		case 0x0: // JSR @Rn
			target := c.R[n]
			c.PR = pc + 4
			c.delayedBranch(pc, target, true)
		case 0x1: // TAS.B @Rn
			addr := c.R[n]
			v, ok := c.read8(addr)
			if !ok {
				c.enterMemFault(pc)
				return
			}
			c.setT(boolU32(v == 0))
			if !c.write8(addr, v|0x80) {
				c.enterMemFault(pc)
			}
		case 0x2: // JMP @Rn
			c.delayedBranch(pc, c.R[n], true)
		default:
			c.illegal(inDelay, pc)
		}
	default:
		c.execShiftUnary(pc, inDelay, n, m, low)
	}
}

// shad implements SHAD (arithmetic) and SHLD (logical): a positive
// Rm shifts Rn left, a negative Rm shifts Rn right, in both cases by
// the magnitude of Rm modulo 32.
func (c *CPU) shad(n, m int, arithmetic bool) {
	cnt := int32(c.R[m])
	if cnt >= 0 {
		c.R[n] <<= uint32(cnt) & 31
		return
	}
	s := uint32(-cnt) & 31
	if arithmetic {
		c.R[n] = uint32(int32(c.R[n]) >> s)
	} else {
		c.R[n] >>= s
	}
}

// execShiftUnary handles the single-register shift, rotate, and test
// instructions of the 0100 family, selected by (code, low) pairs.
func (c *CPU) execShiftUnary(pc uint32, inDelay bool, n, m int, low uint16) {
	v := c.R[n]
	switch {
	case m == 0 && low == 0x0: // SHLL Rn
		c.setT(v >> 31)
		c.R[n] = v << 1
	case m == 0 && low == 0x1: // SHLR Rn
		c.setT(v & 1)
		c.R[n] = v >> 1
	case m == 0 && low == 0x4: // ROTL Rn
		t := v >> 31
		c.R[n] = v<<1 | t
		c.setT(t)
	case m == 0 && low == 0x5: // ROTR Rn
		t := v & 1
		c.R[n] = v>>1 | t<<31
		c.setT(t)
	case m == 0 && low == 0x8: // SHLL2 Rn
		c.R[n] = v << 2
	case m == 0 && low == 0x9: // SHLR2 Rn
		c.R[n] = v >> 2
	case m == 1 && low == 0x0: // DT Rn
		v--
		c.R[n] = v
		c.setT(boolU32(v == 0))
	case m == 1 && low == 0x1: // CMP/PZ Rn
		c.setT(boolU32(int32(v) >= 0))
	case m == 1 && low == 0x5: // CMP/PL Rn
		c.setT(boolU32(int32(v) > 0))
	case m == 1 && low == 0x8: // SHLL8 Rn
		c.R[n] = v << 8
	case m == 1 && low == 0x9: // SHLR8 Rn
		c.R[n] = v >> 8
	case m == 2 && low == 0x0: // SHAL Rn
		c.setT(v >> 31)
		c.R[n] = v << 1
	case m == 2 && low == 0x1: // SHAR Rn
		c.setT(v & 1)
		c.R[n] = uint32(int32(v) >> 1)
	case m == 2 && low == 0x4: // ROTCL Rn
		t := v >> 31
		c.R[n] = v<<1 | c.getT()
		c.setT(t)
	case m == 2 && low == 0x5: // ROTCR Rn
		t := v & 1
		c.R[n] = v>>1 | c.getT()<<31
		c.setT(t)
	case m == 2 && low == 0x8: // SHLL16 Rn
		c.R[n] = v << 16
	case m == 2 && low == 0x9: // SHLR16 Rn
		c.R[n] = v >> 16
	default:
		c.illegal(inDelay, pc)
	}
}
