package r3000a

// Instruction execution for the R3000A. Register writes
// go through setReg/setLoad so the load-delay and
// cancellation rules in Step see every write. Any
// instruction that can fault checks its fault condition
// before producing side effects, so a faulting
// instruction has no architectural effect.

func sext16(v uint32) uint32 {
	return uint32(int32(int16(v)))
}

func (c *CPU) raise(code, addr, ce uint32) *raisedException {
	return &raisedException{code: code, addr: addr, ce: ce}
}

func (c *CPU) execute(op uint32, instrPC uint32) (int, bool, *raisedException) {
	primary := op >> 26
	rs := (op >> 21) & 31
	rt := (op >> 16) & 31
	rd := (op >> 11) & 31
	shamt := (op >> 6) & 31
	funct := op & 63
	imm := op & 0xFFFF
	simm := sext16(imm)
	cycles := 1

	switch primary {
	case 0x00: // SPECIAL
		switch funct {
		case 0x00: // SLL
			c.setReg(rd, c.Regs[rt]<<shamt)
		case 0x02: // SRL
			c.setReg(rd, c.Regs[rt]>>shamt)
		case 0x03: // SRA
			c.setReg(rd, uint32(int32(c.Regs[rt])>>shamt))
		case 0x04: // SLLV
			c.setReg(rd, c.Regs[rt]<<(c.Regs[rs]&31))
		case 0x06: // SRLV
			c.setReg(rd, c.Regs[rt]>>(c.Regs[rs]&31))
		case 0x07: // SRAV
			c.setReg(rd, uint32(int32(c.Regs[rt])>>(c.Regs[rs]&31)))
		case 0x08: // JR
			c.NextPC = c.Regs[rs]
			return cycles, true, nil
		case 0x09: // JALR
			target := c.Regs[rs]
			c.setReg(rd, c.NextPC) // NextPC is instrPC+8 here
			c.NextPC = target
			return cycles, true, nil
		case 0x0C: // SYSCALL
			return cycles, false, c.raise(ExcSys, 0, 0)
		case 0x0D: // BREAK
			return cycles, false, c.raise(ExcBp, 0, 0)
		case 0x10: // MFHI
			cycles += c.stallHiLo()
			c.setReg(rd, c.HI)
		case 0x11: // MTHI
			cycles += c.stallHiLo()
			c.HI = c.Regs[rs]
		case 0x12: // MFLO
			cycles += c.stallHiLo()
			c.setReg(rd, c.LO)
		case 0x13: // MTLO
			cycles += c.stallHiLo()
			c.LO = c.Regs[rs]
		case 0x18: // MULT
			prod := int64(int32(c.Regs[rs])) * int64(int32(c.Regs[rt]))
			c.HI = uint32(uint64(prod) >> 32)
			c.LO = uint32(prod)
			c.busySet = multBusyCycles
		case 0x19: // MULTU
			prod := uint64(c.Regs[rs]) * uint64(c.Regs[rt])
			c.HI = uint32(prod >> 32)
			c.LO = uint32(prod)
			c.busySet = multBusyCycles
		case 0x1A: // DIV
			a := int32(c.Regs[rs])
			b := int32(c.Regs[rt])
			switch {
			case b == 0:
				// Documented R3000A divide-by-zero result:
				// remainder is the dividend, quotient is
				// -1 for a non-negative dividend, +1 for a
				// negative one.
				c.HI = uint32(a)
				if a < 0 {
					c.LO = 1
				} else {
					c.LO = 0xFFFFFFFF
				}
			case a == -1<<31 && b == -1:
				c.LO = uint32(a)
				c.HI = 0
			default:
				c.LO = uint32(a / b)
				c.HI = uint32(a % b)
			}
			c.busySet = divBusyCycles
		case 0x1B: // DIVU
			a := c.Regs[rs]
			b := c.Regs[rt]
			if b == 0 {
				c.HI = a
				c.LO = 0xFFFFFFFF
			} else {
				c.LO = a / b
				c.HI = a % b
			}
			c.busySet = divBusyCycles
		case 0x20: // ADD
			r := int64(int32(c.Regs[rs])) + int64(int32(c.Regs[rt]))
			if r > 0x7FFFFFFF || r < -0x80000000 {
				return cycles, false, c.raise(ExcOv, 0, 0)
			}
			c.setReg(rd, uint32(r))
		case 0x21: // ADDU
			c.setReg(rd, c.Regs[rs]+c.Regs[rt])
		case 0x22: // SUB
			r := int64(int32(c.Regs[rs])) - int64(int32(c.Regs[rt]))
			if r > 0x7FFFFFFF || r < -0x80000000 {
				return cycles, false, c.raise(ExcOv, 0, 0)
			}
			c.setReg(rd, uint32(r))
		case 0x23: // SUBU
			c.setReg(rd, c.Regs[rs]-c.Regs[rt])
		case 0x24: // AND
			c.setReg(rd, c.Regs[rs]&c.Regs[rt])
		case 0x25: // OR
			c.setReg(rd, c.Regs[rs]|c.Regs[rt])
		case 0x26: // XOR
			c.setReg(rd, c.Regs[rs]^c.Regs[rt])
		case 0x27: // NOR
			c.setReg(rd, ^(c.Regs[rs] | c.Regs[rt]))
		case 0x2A: // SLT
			if int32(c.Regs[rs]) < int32(c.Regs[rt]) {
				c.setReg(rd, 1)
			} else {
				c.setReg(rd, 0)
			}
		case 0x2B: // SLTU
			if c.Regs[rs] < c.Regs[rt] {
				c.setReg(rd, 1)
			} else {
				c.setReg(rd, 0)
			}
		default:
			return cycles, false, c.raise(ExcRI, 0, 0)
		}
		return cycles, false, nil

	case 0x01: // REGIMM branches
		v := int32(c.Regs[rs])
		target := c.PC + (simm << 2) // PC is instrPC+4 here
		switch rt {
		case 0x00: // BLTZ
			if v < 0 {
				c.NextPC = target
			}
		case 0x01: // BGEZ
			if v >= 0 {
				c.NextPC = target
			}
		case 0x10: // BLTZAL: link is unconditional on the R3000A
			c.setReg(31, c.NextPC)
			if v < 0 {
				c.NextPC = target
			}
		case 0x11: // BGEZAL: link is unconditional on the R3000A
			c.setReg(31, c.NextPC)
			if v >= 0 {
				c.NextPC = target
			}
		default:
			return cycles, false, c.raise(ExcRI, 0, 0)
		}
		return cycles, true, nil

	case 0x02: // J
		c.NextPC = (c.PC & 0xF0000000) | ((op & 0x03FFFFFF) << 2)
		return cycles, true, nil

	case 0x03: // JAL
		c.setReg(31, c.NextPC)
		c.NextPC = (c.PC & 0xF0000000) | ((op & 0x03FFFFFF) << 2)
		return cycles, true, nil

	case 0x04: // BEQ
		if c.Regs[rs] == c.Regs[rt] {
			c.NextPC = c.PC + (simm << 2)
		}
		return cycles, true, nil

	case 0x05: // BNE
		if c.Regs[rs] != c.Regs[rt] {
			c.NextPC = c.PC + (simm << 2)
		}
		return cycles, true, nil

	case 0x06: // BLEZ
		if rt != 0 {
			return cycles, false, c.raise(ExcRI, 0, 0)
		}
		if int32(c.Regs[rs]) <= 0 {
			c.NextPC = c.PC + (simm << 2)
		}
		return cycles, true, nil

	case 0x07: // BGTZ
		if rt != 0 {
			return cycles, false, c.raise(ExcRI, 0, 0)
		}
		if int32(c.Regs[rs]) > 0 {
			c.NextPC = c.PC + (simm << 2)
		}
		return cycles, true, nil

	case 0x08: // ADDI
		r := int64(int32(c.Regs[rs])) + int64(int32(simm))
		if r > 0x7FFFFFFF || r < -0x80000000 {
			return cycles, false, c.raise(ExcOv, 0, 0)
		}
		c.setReg(rt, uint32(r))
	case 0x09: // ADDIU
		c.setReg(rt, c.Regs[rs]+simm)
	case 0x0A: // SLTI
		if int32(c.Regs[rs]) < int32(simm) {
			c.setReg(rt, 1)
		} else {
			c.setReg(rt, 0)
		}
	case 0x0B: // SLTIU (immediate is sign-extended, compared unsigned)
		if c.Regs[rs] < simm {
			c.setReg(rt, 1)
		} else {
			c.setReg(rt, 0)
		}
	case 0x0C: // ANDI
		c.setReg(rt, c.Regs[rs]&imm)
	case 0x0D: // ORI
		c.setReg(rt, c.Regs[rs]|imm)
	case 0x0E: // XORI
		c.setReg(rt, c.Regs[rs]^imm)
	case 0x0F: // LUI
		c.setReg(rt, imm<<16)

	case 0x10: // COP0
		return c.execCOP0(op, rs, rt, rd, funct)
	case 0x11: // COP1: no FPU on the PSX
		return cycles, false, c.raise(ExcCpU, 0, 1)
	case 0x12: // COP2: the GTE
		return c.execCOP2(op, rs, rt, rd)
	case 0x13: // COP3: not present
		return cycles, false, c.raise(ExcCpU, 0, 3)

	case 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, // loads
		0x28, 0x29, 0x2A, 0x2B, 0x2E, // stores
		0x32, 0x3A: // LWC2 / SWC2
		return c.execLoadStore(primary, rs, rt, simm)

	case 0x30, 0x31, 0x33: // LWC0/1/3
		return cycles, false, c.raise(ExcCpU, 0, primary-0x30)
	case 0x38, 0x39, 0x3B: // SWC0/1/3
		return cycles, false, c.raise(ExcCpU, 0, primary-0x38)

	default:
		return cycles, false, c.raise(ExcRI, 0, 0)
	}
	return cycles, false, nil
}

// execCOP0 handles MFC0, MTC0, BC0 and RFE.
func (c *CPU) execCOP0(op, rs, rt, rd, funct uint32) (int, bool, *raisedException) {
	switch {
	case rs == 0x00: // MFC0: a delayed load, like memory loads
		c.setLoad(rt, c.readCP0(rd))
		return 2, false, nil
	case rs == 0x04: // MTC0
		c.writeCP0(rd, c.Regs[rt])
		return 1, false, nil
	case rs == 0x08: // BC0F/BC0T: the CP0 condition input is tied false on the PSX
		return 1, true, nil
	case rs >= 0x10 && funct == 0x10: // RFE
		c.SR = (c.SR &^ 0x0F) | ((c.SR >> 2) & 0x0F)
		return 1, false, nil
	}
	return 1, false, c.raise(ExcRI, 0, 0)
}

// execCOP2 handles the GTE register moves and command
// issue. Register reads are delayed loads, matching
// the load-delay family.
func (c *CPU) execCOP2(op, rs, rt, rd uint32) (int, bool, *raisedException) {
	if c.CP2 == nil {
		return 1, false, c.raise(ExcCpU, 0, 2)
	}
	switch {
	case rs == 0x00: // MFC2
		c.setLoad(rt, c.CP2.Data(rd))
		return 2, false, nil
	case rs == 0x02: // CFC2
		c.setLoad(rt, c.CP2.Control(rd))
		return 2, false, nil
	case rs == 0x04: // MTC2
		c.CP2.SetData(rd, c.Regs[rt])
		return 1, false, nil
	case rs == 0x06: // CTC2
		c.CP2.SetControl(rd, c.Regs[rt])
		return 1, false, nil
	case rs == 0x08: // BC2F/BC2T: the GTE raises no branch condition
		return 1, true, nil
	case rs >= 0x10: // GTE command: the GTE charges
		// its documented execution cycles.
		cyc := c.CP2.Command(op)
		if cyc < 1 {
			cyc = 1
		}
		return cyc, false, nil
	}
	return 1, false, c.raise(ExcRI, 0, 0)
}

// mergeBase returns the value an LWL/LWR merge starts
// from: the pending load's value when a load to the
// same register is in its delay slot (the merge reads
// late, after that load lands), else the register.
func (c *CPU) mergeBase(rt uint32) uint32 {
	if c.pending.valid && uint32(c.pending.reg) == rt {
		return c.pending.value
	}
	return c.Regs[rt]
}

// execLoadStore implements every integer load and
// store, including the unaligned merge instructions.
func (c *CPU) execLoadStore(primary, rs, rt, simm uint32) (int, bool, *raisedException) {
	addr := c.Regs[rs] + simm
	const cycles = 2
	switch primary {
	case 0x20: // LB
		v := c.readData8(addr)
		c.setLoad(rt, uint32(int32(int8(v))))
	case 0x24: // LBU
		c.setLoad(rt, uint32(c.readData8(addr)))
	case 0x21: // LH
		if addr&1 != 0 {
			return cycles, false, c.raise(ExcAdEL, addr, 0)
		}
		c.setLoad(rt, uint32(int32(int16(c.readData16(addr)))))
	case 0x25: // LHU
		if addr&1 != 0 {
			return cycles, false, c.raise(ExcAdEL, addr, 0)
		}
		c.setLoad(rt, uint32(c.readData16(addr)))
	case 0x23: // LW
		if addr&3 != 0 {
			return cycles, false, c.raise(ExcAdEL, addr, 0)
		}
		c.setLoad(rt, c.readData32(addr))
	case 0x22: // LWL (little-endian merge, documented shifts)
		word := c.readData32(addr &^ 3)
		old := c.mergeBase(rt)
		var v uint32
		switch addr & 3 {
		case 0:
			v = (old & 0x00FFFFFF) | (word << 24)
		case 1:
			v = (old & 0x0000FFFF) | (word << 16)
		case 2:
			v = (old & 0x000000FF) | (word << 8)
		case 3:
			v = word
		}
		c.setLoad(rt, v)
	case 0x26: // LWR
		word := c.readData32(addr &^ 3)
		old := c.mergeBase(rt)
		var v uint32
		switch addr & 3 {
		case 0:
			v = word
		case 1:
			v = (old & 0xFF000000) | (word >> 8)
		case 2:
			v = (old & 0xFFFF0000) | (word >> 16)
		case 3:
			v = (old & 0xFFFFFF00) | (word >> 24)
		}
		c.setLoad(rt, v)
	case 0x28: // SB
		c.writeData8(addr, uint8(c.Regs[rt]))
	case 0x29: // SH
		if addr&1 != 0 {
			return cycles, false, c.raise(ExcAdES, addr, 0)
		}
		c.writeData16(addr, uint16(c.Regs[rt]))
	case 0x2B: // SW
		if addr&3 != 0 {
			return cycles, false, c.raise(ExcAdES, addr, 0)
		}
		c.writeData32(addr, c.Regs[rt])
	case 0x2A: // SWL (read-modify-write of the aligned word)
		a := addr &^ 3
		word := c.readData32(a)
		v := c.Regs[rt]
		var merged uint32
		switch addr & 3 {
		case 0:
			merged = (word & 0xFFFFFF00) | (v >> 24)
		case 1:
			merged = (word & 0xFFFF0000) | (v >> 16)
		case 2:
			merged = (word & 0xFF000000) | (v >> 8)
		case 3:
			merged = v
		}
		c.writeData32(a, merged)
	case 0x2E: // SWR
		a := addr &^ 3
		word := c.readData32(a)
		v := c.Regs[rt]
		var merged uint32
		switch addr & 3 {
		case 0:
			merged = v
		case 1:
			merged = (word & 0x000000FF) | (v << 8)
		case 2:
			merged = (word & 0x0000FFFF) | (v << 16)
		case 3:
			merged = (word & 0x00FFFFFF) | (v << 24)
		}
		c.writeData32(a, merged)
	case 0x32: // LWC2
		if c.CP2 == nil {
			return cycles, false, c.raise(ExcCpU, 0, 2)
		}
		if addr&3 != 0 {
			return cycles, false, c.raise(ExcAdEL, addr, 0)
		}
		c.CP2.SetData(rt, c.readData32(addr))
	case 0x3A: // SWC2
		if c.CP2 == nil {
			return cycles, false, c.raise(ExcCpU, 0, 2)
		}
		if addr&3 != 0 {
			return cycles, false, c.raise(ExcAdES, addr, 0)
		}
		c.writeData32(addr, c.CP2.Data(rt))
	}
	return cycles, false, nil
}
