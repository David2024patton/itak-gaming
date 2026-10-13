package r4300

import "math/bits"

// Flat cycle totals by instruction class (see README).
const (
	cycLoadStore = 2
	cycMul       = 5
	cycDMul      = 8
	cycDiv       = 37
	cycDDiv      = 74
	cycTLB       = 2
	cycERET      = 3
)

// execute runs one decoded instruction at instrAddr and
// returns its flat cycle count. Exceptions raised here
// return the instruction to the exception vector instead
// (the count returned is then nominal).
func (c *CPU) execute(instr uint32, pc uint64, inDelay bool) int {
	op := instr >> 26
	rs := (instr >> 21) & 31
	rt := (instr >> 16) & 31
	imm := instr & 0xFFFF
	simm := uint64(int64(int16(imm)))
	target := instr & 0x3FFFFFF

	raise := func(code int) int {
		c.raiseException(exc{code: code}, pc, inDelay)
		return 1
	}
	// branch records a control transfer: the instruction at
	// PC (already advanced) is the delay slot.
	branch := func(t uint64) {
		c.NPC = t
		c.delaySlot = true
	}
	branchTarget := func() uint64 {
		return pc + 4 + (simm << 2)
	}

	switch op {
	case 0x00: // SPECIAL
		return c.execSpecial(instr, pc, inDelay)
	case 0x01: // REGIMM
		cond := false
		link := false
		trap := false
		isBranch := true
		sv := int64(c.Reg[rs])
		switch rt {
		case 0x00, 0x10: // BLTZ, BLTZAL
			cond = sv < 0
			link = rt == 0x10
		case 0x01, 0x11: // BGEZ, BGEZAL
			cond = sv >= 0
			link = rt == 0x11
		case 0x02, 0x12: // BLTZL, BLTZALL
			cond = sv < 0
			link = rt == 0x12
			if !cond {
				c.nullify = true
			} else {
				branch(branchTarget())
			}
			if link {
				c.Reg[31] = pc + 8
			}
			return 1
		case 0x03, 0x13: // BGEZL, BGEZALL
			cond = sv >= 0
			link = rt == 0x13
			if !cond {
				c.nullify = true
			} else {
				branch(branchTarget())
			}
			if link {
				c.Reg[31] = pc + 8
			}
			return 1
		case 0x08: // TGEI
			trap, isBranch = sv >= int64(simm), false
		case 0x09: // TGEIU
			trap, isBranch = c.Reg[rs] >= simm, false
		case 0x0A: // TLTI
			trap, isBranch = sv < int64(simm), false
		case 0x0B: // TLTIU
			trap, isBranch = c.Reg[rs] < simm, false
		case 0x0C: // TEQI
			trap, isBranch = c.Reg[rs] == simm, false
		case 0x0E: // TNEI
			trap, isBranch = c.Reg[rs] != simm, false
		default:
			return raise(ExcReserved)
		}
		if trap {
			return raise(ExcTrap)
		}
		if link {
			c.Reg[31] = pc + 8
		}
		if isBranch {
			if cond {
				branch(branchTarget())
			} else {
				c.delaySlot = true
			}
		}
		return 1
	case 0x02: // J
		branch((pc+4)&^uint64(0xFFFFFFF) | uint64(target)<<2)
		return 1
	case 0x03: // JAL
		c.Reg[31] = pc + 8
		branch((pc+4)&^uint64(0xFFFFFFF) | uint64(target)<<2)
		return 1
	case 0x04, 0x05, 0x06, 0x07: // BEQ BNE BLEZ BGTZ
		cond := false
		switch op {
		case 0x04:
			cond = c.Reg[rs] == c.Reg[rt]
		case 0x05:
			cond = c.Reg[rs] != c.Reg[rt]
		case 0x06:
			cond = int64(c.Reg[rs]) <= 0
		case 0x07:
			cond = int64(c.Reg[rs]) > 0
		}
		if cond {
			branch(branchTarget())
		} else {
			c.delaySlot = true
		}
		return 1
	case 0x14, 0x15, 0x16, 0x17: // BEQL BNEL BLEZL BGTZL
		cond := false
		switch op {
		case 0x14:
			cond = c.Reg[rs] == c.Reg[rt]
		case 0x15:
			cond = c.Reg[rs] != c.Reg[rt]
		case 0x16:
			cond = int64(c.Reg[rs]) <= 0
		case 0x17:
			cond = int64(c.Reg[rs]) > 0
		}
		if cond {
			branch(branchTarget())
		} else {
			c.nullify = true
		}
		return 1
	case 0x08: // ADDI
		r := int64(int32(c.Reg[rs])) + int64(int16(imm))
		if r != int64(int32(r)) {
			return raise(ExcOverflow)
		}
		c.Reg[rt] = uint64(r)
		return 1
	case 0x09: // ADDIU
		c.Reg[rt] = signExtend32(uint32(c.Reg[rs]) + uint32(int32(int16(imm))))
		return 1
	case 0x0A: // SLTI
		if int64(c.Reg[rs]) < int64(simm) {
			c.Reg[rt] = 1
		} else {
			c.Reg[rt] = 0
		}
		return 1
	case 0x0B: // SLTIU
		if c.Reg[rs] < simm {
			c.Reg[rt] = 1
		} else {
			c.Reg[rt] = 0
		}
		return 1
	case 0x0C: // ANDI
		c.Reg[rt] = c.Reg[rs] & uint64(imm)
		return 1
	case 0x0D: // ORI
		c.Reg[rt] = c.Reg[rs] | uint64(imm)
		return 1
	case 0x0E: // XORI
		c.Reg[rt] = c.Reg[rs] ^ uint64(imm)
		return 1
	case 0x0F: // LUI
		c.Reg[rt] = signExtend32(uint32(imm) << 16)
		return 1
	case 0x10: // COP0
		return c.execCOP0(instr, pc, inDelay)
	case 0x11: // COP1
		return c.execCOP1(instr, pc, inDelay)
	case 0x12, 0x13: // COP2, COP3: not implemented on the VR4300
		c.raiseException(exc{code: ExcCpU, ce: int(op & 3)}, pc, inDelay)
		return 1
	case 0x18: // DADDI
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		r := int64(c.Reg[rs]) + int64(simm)
		if (r^int64(c.Reg[rs]))&(r^int64(simm)) < 0 {
			return raise(ExcOverflow)
		}
		c.Reg[rt] = uint64(r)
		return 1
	case 0x19: // DADDIU
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rt] = c.Reg[rs] + simm
		return 1
	case 0x1A: // LDL
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		addr := c.Reg[rs] + simm
		v, ok := c.loadLeft(addr, 8, c.Reg[rt], pc, inDelay)
		if ok {
			c.Reg[rt] = v
		}
		return cycLoadStore
	case 0x1B: // LDR
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		addr := c.Reg[rs] + simm
		v, ok := c.loadRight(addr, 8, c.Reg[rt], pc, inDelay)
		if ok {
			c.Reg[rt] = v
		}
		return cycLoadStore
	case 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27: // LB LH LWL LW LBU LHU LWR LWU
		addr := c.Reg[rs] + simm
		return c.execLoad(op, addr, rt, pc, inDelay)
	case 0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E: // SB SH SWL SW SDL SD SWR
		addr := c.Reg[rs] + simm
		return c.execStore(op, addr, rt, pc, inDelay)
	case 0x2F: // CACHE: caches are not modeled
		return 1
	case 0x30: // LL
		addr := c.Reg[rs] + simm
		v, ok := c.readMem(addr, 4, pc, inDelay)
		if !ok {
			return cycLoadStore
		}
		if paddr, e := c.translate(addr, accRead); e == nil {
			c.LLAddr = paddr
			c.LLBit = true
		}
		c.Reg[rt] = signExtend32(uint32(v))
		return cycLoadStore
	case 0x31: // LWC1
		addr := c.Reg[rs] + simm
		if !c.cp1Usable() {
			c.raiseException(exc{code: ExcCpU, ce: 1}, pc, inDelay)
			return 1
		}
		v, ok := c.readMem(addr, 4, pc, inDelay)
		if ok {
			c.setSRaw(rt, uint32(v))
		}
		return cycLoadStore
	case 0x32, 0x3A: // LWC2, SWC2
		c.raiseException(exc{code: ExcCpU, ce: 2}, pc, inDelay)
		return 1
	case 0x33: // PREF: no-op on this core
		return 1
	case 0x34: // LLD
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		addr := c.Reg[rs] + simm
		v, ok := c.readMem(addr, 8, pc, inDelay)
		if !ok {
			return cycLoadStore
		}
		if paddr, e := c.translate(addr, accRead); e == nil {
			c.LLAddr = paddr
			c.LLBit = true
		}
		c.Reg[rt] = v
		return cycLoadStore
	case 0x35: // LDC1
		addr := c.Reg[rs] + simm
		if !c.cp1Usable() {
			c.raiseException(exc{code: ExcCpU, ce: 1}, pc, inDelay)
			return 1
		}
		v, ok := c.readMem(addr, 8, pc, inDelay)
		if ok {
			c.setDRaw(rt, v)
		}
		return cycLoadStore
	case 0x36, 0x3E: // LDC2, SDC2
		c.raiseException(exc{code: ExcCpU, ce: 2}, pc, inDelay)
		return 1
	case 0x37, 0x3F: // LDC3, SDC3
		c.raiseException(exc{code: ExcCpU, ce: 3}, pc, inDelay)
		return 1
	case 0x38: // SC
		addr := c.Reg[rs] + simm
		if addr&3 != 0 {
			c.raiseException(exc{code: ExcAdES, badAddr: addr, addrErr: true}, pc, inDelay)
			return cycLoadStore
		}
		if c.LLBit {
			if c.writeMem(addr, 4, uint64(uint32(c.Reg[rt])), pc, inDelay) {
				c.Reg[rt] = 1
			} else {
				c.Reg[rt] = 0
			}
		} else {
			c.Reg[rt] = 0
		}
		c.LLBit = false
		return cycLoadStore
	case 0x39: // SWC1
		addr := c.Reg[rs] + simm
		if !c.cp1Usable() {
			c.raiseException(exc{code: ExcCpU, ce: 1}, pc, inDelay)
			return 1
		}
		c.writeMem(addr, 4, uint64(c.getSRaw(rt)), pc, inDelay)
		return cycLoadStore
	case 0x3C: // SCD
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		addr := c.Reg[rs] + simm
		if addr&7 != 0 {
			c.raiseException(exc{code: ExcAdES, badAddr: addr, addrErr: true}, pc, inDelay)
			return cycLoadStore
		}
		if c.LLBit {
			if c.writeMem(addr, 8, c.Reg[rt], pc, inDelay) {
				c.Reg[rt] = 1
			} else {
				c.Reg[rt] = 0
			}
		} else {
			c.Reg[rt] = 0
		}
		c.LLBit = false
		return cycLoadStore
	case 0x3D: // SDC1
		addr := c.Reg[rs] + simm
		if !c.cp1Usable() {
			c.raiseException(exc{code: ExcCpU, ce: 1}, pc, inDelay)
			return 1
		}
		c.writeMem(addr, 8, c.getDRaw(rt), pc, inDelay)
		return cycLoadStore
	case 0x3B: // SWC3
		c.raiseException(exc{code: ExcCpU, ce: 3}, pc, inDelay)
		return 1
	}
	return raise(ExcReserved)
}

// execLoad handles the LB..LWU group.
func (c *CPU) execLoad(op uint32, addr uint64, rt uint32, pc uint64, inDelay bool) int {
	switch op {
	case 0x20: // LB
		if v, ok := c.readMem(addr, 1, pc, inDelay); ok {
			c.Reg[rt] = uint64(int64(int8(v)))
		}
	case 0x21: // LH
		if v, ok := c.readMem(addr, 2, pc, inDelay); ok {
			c.Reg[rt] = uint64(int64(int16(v)))
		}
	case 0x22: // LWL
		if v, ok := c.loadLeft(addr, 4, c.Reg[rt], pc, inDelay); ok {
			c.Reg[rt] = signExtend32(uint32(v))
		}
	case 0x23: // LW
		if v, ok := c.readMem(addr, 4, pc, inDelay); ok {
			c.Reg[rt] = signExtend32(uint32(v))
		}
	case 0x24: // LBU
		if v, ok := c.readMem(addr, 1, pc, inDelay); ok {
			c.Reg[rt] = v
		}
	case 0x25: // LHU
		if v, ok := c.readMem(addr, 2, pc, inDelay); ok {
			c.Reg[rt] = v
		}
	case 0x26: // LWR
		if v, ok := c.loadRight(addr, 4, c.Reg[rt], pc, inDelay); ok {
			c.Reg[rt] = signExtend32(uint32(v))
		}
	case 0x27: // LWU
		if v, ok := c.readMem(addr, 4, pc, inDelay); ok {
			c.Reg[rt] = uint64(uint32(v))
		}
	}
	return cycLoadStore
}

// execStore handles the SB..SWR group.
func (c *CPU) execStore(op uint32, addr uint64, rt uint32, pc uint64, inDelay bool) int {
	val := c.Reg[rt]
	switch op {
	case 0x28: // SB
		c.writeMem(addr, 1, val, pc, inDelay)
	case 0x29: // SH
		c.writeMem(addr, 2, val, pc, inDelay)
	case 0x2A: // SWL
		c.storeLeft(addr, 4, val, pc, inDelay)
	case 0x2B: // SW
		c.writeMem(addr, 4, val, pc, inDelay)
	case 0x2C: // SDL
		if !c.allow64Ops() {
			c.raiseException(exc{code: ExcReserved}, pc, inDelay)
			return cycLoadStore
		}
		c.storeLeft(addr, 8, val, pc, inDelay)
	case 0x2D: // SD
		if !c.allow64Ops() {
			c.raiseException(exc{code: ExcReserved}, pc, inDelay)
			return cycLoadStore
		}
		c.writeMem(addr, 8, val, pc, inDelay)
	case 0x2E: // SWR
		c.storeRight(addr, 4, val, pc, inDelay)
	}
	return cycLoadStore
}

// loadLeft implements LWL/LDL for a big-endian machine: bytes
// from addr to the end of its aligned unit fill the top of
// the result.
func (c *CPU) loadLeft(addr uint64, width int, old uint64, pc uint64, inDelay bool) (uint64, bool) {
	aligned := addr &^ uint64(width-1)
	paddr, e := c.translate(aligned, accRead)
	if e != nil {
		c.raiseException(*e, pc, inDelay)
		return 0, false
	}
	w := c.bus.Read(paddr, width)
	k := addr & uint64(width-1)
	shift := k * 8
	mask := uint64(0)
	if shift > 0 {
		mask = (uint64(1) << shift) - 1
	}
	return (w << shift) | (old & mask), true
}

// loadRight implements LWR/LDR for a big-endian machine:
// bytes from the start of the aligned unit down to addr fill
// the bottom of the result.
func (c *CPU) loadRight(addr uint64, width int, old uint64, pc uint64, inDelay bool) (uint64, bool) {
	aligned := addr &^ uint64(width-1)
	paddr, e := c.translate(aligned, accRead)
	if e != nil {
		c.raiseException(*e, pc, inDelay)
		return 0, false
	}
	w := c.bus.Read(paddr, width)
	k := addr & uint64(width-1)
	shift := (uint64(width-1) - k) * 8
	bitsKept := (k + 1) * 8
	mask := ^uint64(0) << bitsKept
	return (w >> shift) | (old & mask), true
}

// storeLeft implements SWL/SDL byte by byte.
func (c *CPU) storeLeft(addr uint64, width int, val uint64, pc uint64, inDelay bool) bool {
	aligned := addr &^ uint64(width-1)
	paddr, e := c.translate(aligned, accWrite)
	if e != nil {
		c.raiseException(*e, pc, inDelay)
		return false
	}
	k := int(addr & uint64(width-1))
	for i := 0; i < width-k; i++ {
		b := byte(val >> uint(8*(width-1-i)))
		c.bus.Write(paddr+uint32(k+i), 1, uint64(b))
	}
	return true
}

// storeRight implements SWR/SDR byte by byte.
func (c *CPU) storeRight(addr uint64, width int, val uint64, pc uint64, inDelay bool) bool {
	aligned := addr &^ uint64(width-1)
	paddr, e := c.translate(aligned, accWrite)
	if e != nil {
		c.raiseException(*e, pc, inDelay)
		return false
	}
	k := int(addr & uint64(width-1))
	for j := 0; j <= k; j++ {
		b := byte(val >> uint(8*(k-j)))
		c.bus.Write(paddr+uint32(j), 1, uint64(b))
	}
	return true
}

// execSpecial handles the SPECIAL opcode group.
func (c *CPU) execSpecial(instr uint32, pc uint64, inDelay bool) int {
	rs := (instr >> 21) & 31
	rt := (instr >> 16) & 31
	rd := (instr >> 11) & 31
	sa := (instr >> 6) & 31
	funct := instr & 63
	a := c.Reg[rs]
	b := c.Reg[rt]
	raise := func(code int) int {
		c.raiseException(exc{code: code}, pc, inDelay)
		return 1
	}
	switch funct {
	case 0x00: // SLL
		c.Reg[rd] = signExtend32(uint32(b) << sa)
	case 0x02: // SRL
		c.Reg[rd] = signExtend32(uint32(b) >> sa)
	case 0x03: // SRA
		c.Reg[rd] = signExtend32(uint32(int32(b) >> sa))
	case 0x04: // SLLV
		c.Reg[rd] = signExtend32(uint32(b) << (a & 31))
	case 0x06: // SRLV
		c.Reg[rd] = signExtend32(uint32(b) >> (a & 31))
	case 0x07: // SRAV
		c.Reg[rd] = signExtend32(uint32(int32(b) >> (a & 31)))
	case 0x08: // JR
		c.NPC = a
		c.delaySlot = true
	case 0x09: // JALR
		t := a
		c.Reg[rd] = pc + 8
		c.NPC = t
		c.delaySlot = true
	case 0x0C: // SYSCALL
		return raise(ExcSyscall)
	case 0x0D: // BREAK
		return raise(ExcBreak)
	case 0x0F: // SYNC
		return 1
	case 0x10: // MFHI
		c.Reg[rd] = c.HI
	case 0x11: // MTHI
		c.HI = a
	case 0x12: // MFLO
		c.Reg[rd] = c.LO
	case 0x13: // MTLO
		c.LO = a
	case 0x14: // DSLLV
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = b << (a & 63)
	case 0x16: // DSRLV
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = b >> (a & 63)
	case 0x17: // DSRAV
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = uint64(int64(b) >> (a & 63))
	case 0x18: // MULT
		p := int64(int32(a)) * int64(int32(b))
		c.LO = signExtend32(uint32(p))
		c.HI = signExtend32(uint32(p >> 32))
		return cycMul
	case 0x19: // MULTU
		p := uint64(uint32(a)) * uint64(uint32(b))
		c.LO = signExtend32(uint32(p))
		c.HI = signExtend32(uint32(p >> 32))
		return cycMul
	case 0x1A: // DIV
		x, y := int32(a), int32(b)
		if y == 0 {
			if x >= 0 {
				c.LO = signExtend32(0xFFFFFFFF)
			} else {
				c.LO = 1
			}
			c.HI = signExtend32(uint32(x))
		} else if x == -1<<31 && y == -1 {
			c.LO = signExtend32(uint32(x))
			c.HI = 0
		} else {
			c.LO = signExtend32(uint32(x / y))
			c.HI = signExtend32(uint32(x % y))
		}
		return cycDiv
	case 0x1B: // DIVU
		x, y := uint32(a), uint32(b)
		if y == 0 {
			c.LO = signExtend32(0xFFFFFFFF)
			c.HI = signExtend32(x)
		} else {
			c.LO = signExtend32(x / y)
			c.HI = signExtend32(x % y)
		}
		return cycDiv
	case 0x1C: // DMULT
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		hi, lo := mul64s(int64(a), int64(b))
		c.HI, c.LO = hi, lo
		return cycDMul
	case 0x1D: // DMULTU
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		hi, lo := bits.Mul64(a, b)
		c.HI, c.LO = hi, lo
		return cycDMul
	case 0x1E: // DDIV
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		x, y := int64(a), int64(b)
		if y == 0 {
			if x >= 0 {
				c.LO = ^uint64(0)
			} else {
				c.LO = 1
			}
			c.HI = uint64(x)
		} else if x == -1<<63 && y == -1 {
			c.LO = uint64(x)
			c.HI = 0
		} else {
			c.LO = uint64(x / y)
			c.HI = uint64(x % y)
		}
		return cycDDiv
	case 0x1F: // DDIVU
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		if b == 0 {
			c.LO = ^uint64(0)
			c.HI = a
		} else {
			c.LO = a / b
			c.HI = a % b
		}
		return cycDDiv
	case 0x20: // ADD
		r := int64(int32(a)) + int64(int32(b))
		if r != int64(int32(r)) {
			return raise(ExcOverflow)
		}
		c.Reg[rd] = uint64(r)
	case 0x21: // ADDU
		c.Reg[rd] = signExtend32(uint32(a) + uint32(b))
	case 0x22: // SUB
		r := int64(int32(a)) - int64(int32(b))
		if r != int64(int32(r)) {
			return raise(ExcOverflow)
		}
		c.Reg[rd] = uint64(r)
	case 0x23: // SUBU
		c.Reg[rd] = signExtend32(uint32(a) - uint32(b))
	case 0x24: // AND
		c.Reg[rd] = a & b
	case 0x25: // OR
		c.Reg[rd] = a | b
	case 0x26: // XOR
		c.Reg[rd] = a ^ b
	case 0x27: // NOR
		c.Reg[rd] = ^(a | b)
	case 0x2A: // SLT
		if int64(a) < int64(b) {
			c.Reg[rd] = 1
		} else {
			c.Reg[rd] = 0
		}
	case 0x2B: // SLTU
		if a < b {
			c.Reg[rd] = 1
		} else {
			c.Reg[rd] = 0
		}
	case 0x2C: // DADD
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		x, y := int64(a), int64(b)
		r := x + y
		if (r^x)&(r^y) < 0 {
			return raise(ExcOverflow)
		}
		c.Reg[rd] = uint64(r)
	case 0x2D: // DADDU
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = a + b
	case 0x2E: // DSUB
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		x, y := int64(a), int64(b)
		r := x - y
		if (x^y)&(r^x) < 0 {
			return raise(ExcOverflow)
		}
		c.Reg[rd] = uint64(r)
	case 0x2F: // DSUBU
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = a - b
	case 0x30: // TGE
		if int64(a) >= int64(b) {
			return raise(ExcTrap)
		}
	case 0x31: // TGEU
		if a >= b {
			return raise(ExcTrap)
		}
	case 0x32: // TLT
		if int64(a) < int64(b) {
			return raise(ExcTrap)
		}
	case 0x33: // TLTU
		if a < b {
			return raise(ExcTrap)
		}
	case 0x34: // TEQ
		if a == b {
			return raise(ExcTrap)
		}
	case 0x36: // TNE
		if a != b {
			return raise(ExcTrap)
		}
	case 0x38: // DSLL
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = b << sa
	case 0x3A: // DSRL
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = b >> sa
	case 0x3B: // DSRA
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = uint64(int64(b) >> sa)
	case 0x3C: // DSLL32
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = b << (sa + 32)
	case 0x3E: // DSRL32
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = b >> (sa + 32)
	case 0x3F: // DSRA32
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rd] = uint64(int64(b) >> (sa + 32))
	default:
		return raise(ExcReserved)
	}
	return 1
}

// mul64s multiplies two signed 64-bit values into a 128-bit
// product split as hi/lo.
func mul64s(a, b int64) (uint64, uint64) {
	ua, ub := uint64(a), uint64(b)
	if a < 0 {
		ua = -ua
	}
	if b < 0 {
		ub = -ub
	}
	hi, lo := bits.Mul64(ua, ub)
	if (a < 0) != (b < 0) {
		if lo != 0 {
			hi = -hi - 1
		} else {
			hi = -hi
		}
		lo = -lo
	}
	return hi, lo
}

// cp1Usable reports whether COP1 may execute (CU1 set, or
// kernel mode).
func (c *CPU) cp1Usable() bool {
	return c.status()&StatusCU1 != 0 || c.privMode() == modeKernel
}

// execCOP0 handles the COP0 opcode group.
func (c *CPU) execCOP0(instr uint32, pc uint64, inDelay bool) int {
	if c.privMode() != modeKernel {
		c.raiseException(exc{code: ExcCpU, ce: 0}, pc, inDelay)
		return 1
	}
	rs := (instr >> 21) & 31
	rt := (instr >> 16) & 31
	rd := (instr >> 11) & 31
	switch rs {
	case 0: // MFC0
		v := c.CP0[rd]
		if rd == Cp0Cause {
			v |= uint64(c.ipLine) << 8
		}
		c.Reg[rt] = signExtend32(uint32(v))
		return 1
	case 1: // DMFC0
		if !c.allow64Ops() {
			c.raiseException(exc{code: ExcReserved}, pc, inDelay)
			return 1
		}
		v := c.CP0[rd]
		if rd == Cp0Cause {
			v |= uint64(c.ipLine) << 8
		}
		c.Reg[rt] = v
		return 1
	case 4: // MTC0
		c.writeCP0(rd, uint64(uint32(c.Reg[rt])))
		return 1
	case 5: // DMTC0
		if !c.allow64Ops() {
			c.raiseException(exc{code: ExcReserved}, pc, inDelay)
			return 1
		}
		c.writeCP0(rd, c.Reg[rt])
		return 1
	case 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31: // CO functions
		funct := instr & 63
		switch funct {
		case 0x01: // TLBR
			e := c.TLB[c.CP0[Cp0Index]&31]
			c.CP0[Cp0PageMask] = e.PageMask
			c.CP0[Cp0EntryHi] = e.EntryHi
			c.CP0[Cp0EntryLo0] = e.EntryLo0
			c.CP0[Cp0EntryLo1] = e.EntryLo1
			return cycTLB
		case 0x02: // TLBWI
			c.TLB[c.CP0[Cp0Index]&31] = TLBEntry{
				PageMask: c.CP0[Cp0PageMask],
				EntryHi:  c.CP0[Cp0EntryHi],
				EntryLo0: c.CP0[Cp0EntryLo0],
				EntryLo1: c.CP0[Cp0EntryLo1],
			}
			return cycTLB
		case 0x06: // TLBWR
			c.TLB[c.CP0[Cp0Random]&31] = TLBEntry{
				PageMask: c.CP0[Cp0PageMask],
				EntryHi:  c.CP0[Cp0EntryHi],
				EntryLo0: c.CP0[Cp0EntryLo0],
				EntryLo1: c.CP0[Cp0EntryLo1],
			}
			return cycTLB
		case 0x08: // TLBP
			idx := c.tlbProbe(c.CP0[Cp0EntryHi])
			if idx < 0 {
				c.CP0[Cp0Index] |= 0x80000000
			} else {
				c.CP0[Cp0Index] = uint64(idx)
			}
			return cycTLB
		case 0x18: // ERET
			st := c.status()
			if st&StatusERL != 0 {
				c.PC = c.CP0[Cp0ErrorEPC]
				c.CP0[Cp0Status] = uint64(st &^ StatusERL)
			} else {
				c.PC = c.CP0[Cp0EPC]
				c.CP0[Cp0Status] = uint64(st &^ StatusEXL)
			}
			c.NPC = c.PC + 4
			c.delaySlot = false
			c.LLBit = false
			return cycERET
		}
		c.raiseException(exc{code: ExcReserved}, pc, inDelay)
		return 1
	}
	c.raiseException(exc{code: ExcReserved}, pc, inDelay)
	return 1
}

// writeCP0 stores a value into a CP0 register, applying the
// documented write restrictions.
func (c *CPU) writeCP0(rd uint32, val uint64) {
	switch rd {
	case Cp0PRId, Cp0Random, Cp0BadVAddr:
		// Read-only.
	case Cp0Cause:
		old := c.CP0[Cp0Cause]
		c.CP0[Cp0Cause] = (old &^ (CauseIPSW | CauseIV)) | (val & (CauseIPSW | CauseIV))
	case Cp0Count:
		c.CP0[Cp0Count] = uint64(uint32(val))
		c.countFrac = 0
	case Cp0Compare:
		c.CP0[Cp0Compare] = uint64(uint32(val))
		c.CP0[Cp0Cause] &^= 1 << 15 // writing Compare clears IP7
	case Cp0Wired:
		c.CP0[Cp0Wired] = val & 31
		c.CP0[Cp0Random] = 31
	case Cp0Index:
		c.CP0[Cp0Index] = val & 0x8000003F
	case Cp0Config:
		// Read-only on the VR4300.
	default:
		c.CP0[rd] = val
	}
}

// tlbProbe searches the TLB for an entry matching hi's
// VPN2/ASID, using each entry's own page mask.
func (c *CPU) tlbProbe(hi uint64) int {
	asid := uint8(hi & 0xFF)
	for i := range c.TLB {
		e := &c.TLB[i]
		mask := (e.PageMask >> 13) & 0xFFF
		shift := uint(bits.Len64(mask+1)) + 12
		if (hi >> shift) != (e.EntryHi >> shift) {
			continue
		}
		global := e.EntryLo0&entryLoG != 0 && e.EntryLo1&entryLoG != 0
		if !global && uint8(e.EntryHi&0xFF) != asid {
			continue
		}
		return i
	}
	return -1
}
