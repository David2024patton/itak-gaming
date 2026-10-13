package rsp

import "encoding/binary"

// Step executes one instruction and returns 1 (the flat
// RSP cycle cost in this model). A halted RSP executes
// nothing and returns 0. There are no exceptions: BREAK
// sets the broke flag and halts, and any encoding outside
// the documented RSP subset executes as a NOP.
func (r *RSP) Step() int {
	if r.halted {
		return 0
	}
	pc := r.PC
	instr := binary.BigEndian.Uint32(r.IMEM[pc&0xFFF:])
	nextNPC := (r.NPC + 4) & 0xFFF
	r.exec(instr, pc, &nextNPC)
	r.PC = r.NPC
	r.NPC = nextNPC
	r.Reg[0] = 0
	return 1
}

// exec decodes and executes one instruction word. taken
// branches rewrite *nextNPC (the address after the delay
// slot), giving the standard MIPS delay-slot flow with a
// 12-bit IMEM wrap.
func (r *RSP) exec(instr uint32, pc uint32, nextNPC *uint32) {
	op := instr >> 26
	rs := (instr >> 21) & 0x1F
	rt := (instr >> 16) & 0x1F
	rd := (instr >> 11) & 0x1F
	sa := (instr >> 6) & 0x1F
	funct := instr & 0x3F
	imm := instr & 0xFFFF
	simm := int32(int16(imm))
	branch := func(cond bool) {
		if cond {
			*nextNPC = (pc + 4 + uint32(simm<<2)) & 0xFFF
		}
	}

	switch op {
	case 0x00: // SPECIAL
		a, b := r.Reg[rs], r.Reg[rt]
		switch funct {
		case 0x00: // SLL
			r.Reg[rd] = b << sa
		case 0x02: // SRL
			r.Reg[rd] = b >> sa
		case 0x03: // SRA
			r.Reg[rd] = uint32(int32(b) >> sa)
		case 0x04: // SLLV
			r.Reg[rd] = b << (a & 0x1F)
		case 0x06: // SRLV
			r.Reg[rd] = b >> (a & 0x1F)
		case 0x07: // SRAV
			r.Reg[rd] = uint32(int32(b) >> (a & 0x1F))
		case 0x08: // JR
			*nextNPC = a & 0xFFC
		case 0x09: // JALR
			r.Reg[rd] = (pc + 8) & 0xFFF
			*nextNPC = a & 0xFFC
		case 0x0D: // BREAK: set broke, halt, interrupt if enabled
			r.broke = true
			r.halted = true
			if r.intrOnBreak {
				r.setInterrupt(true)
			}
		case 0x0F: // SYNC: no-op on the RSP
		case 0x10: // MFHI
			r.Reg[rd] = r.HI
		case 0x11: // MTHI
			r.HI = a
		case 0x12: // MFLO
			r.Reg[rd] = r.LO
		case 0x13: // MTLO
			r.LO = a
		case 0x18: // MULT
			p := int64(int32(a)) * int64(int32(b))
			r.LO, r.HI = uint32(p), uint32(p>>32)
		case 0x19: // MULTU
			p := uint64(a) * uint64(b)
			r.LO, r.HI = uint32(p), uint32(p>>32)
		case 0x1A: // DIV
			r.div(int32(a), int32(b))
		case 0x1B: // DIVU
			r.divu(a, b)
		case 0x20, 0x21: // ADD, ADDU (no overflow trap on the RSP)
			r.Reg[rd] = a + b
		case 0x22, 0x23: // SUB, SUBU
			r.Reg[rd] = a - b
		case 0x24: // AND
			r.Reg[rd] = a & b
		case 0x25: // OR
			r.Reg[rd] = a | b
		case 0x26: // XOR
			r.Reg[rd] = a ^ b
		case 0x27: // NOR
			r.Reg[rd] = ^(a | b)
		case 0x2A: // SLT
			r.Reg[rd] = bool32(int32(a) < int32(b))
		case 0x2B: // SLTU
			r.Reg[rd] = bool32(a < b)
		}
	case 0x01: // REGIMM
		switch rt {
		case 0x00: // BLTZ
			branch(int32(r.Reg[rs]) < 0)
		case 0x01: // BGEZ
			branch(int32(r.Reg[rs]) >= 0)
		case 0x10: // BLTZAL (link written either way, MIPS I style)
			r.Reg[31] = (pc + 8) & 0xFFF
			branch(int32(r.Reg[rs]) < 0)
		case 0x11: // BGEZAL
			r.Reg[31] = (pc + 8) & 0xFFF
			branch(int32(r.Reg[rs]) >= 0)
		}
	case 0x02: // J
		*nextNPC = (instr & 0x3FFFFFF) << 2 & 0xFFC
	case 0x03: // JAL
		r.Reg[31] = (pc + 8) & 0xFFF
		*nextNPC = (instr & 0x3FFFFFF) << 2 & 0xFFC
	case 0x04: // BEQ
		branch(r.Reg[rs] == r.Reg[rt])
	case 0x05: // BNE
		branch(r.Reg[rs] != r.Reg[rt])
	case 0x06: // BLEZ
		branch(int32(r.Reg[rs]) <= 0)
	case 0x07: // BGTZ
		branch(int32(r.Reg[rs]) > 0)
	case 0x08, 0x09: // ADDI, ADDIU (identical on the RSP)
		r.Reg[rt] = r.Reg[rs] + uint32(simm)
	case 0x0A: // SLTI
		r.Reg[rt] = bool32(int32(r.Reg[rs]) < simm)
	case 0x0B: // SLTIU
		r.Reg[rt] = bool32(r.Reg[rs] < uint32(simm))
	case 0x0C: // ANDI
		r.Reg[rt] = r.Reg[rs] & imm
	case 0x0D: // ORI
		r.Reg[rt] = r.Reg[rs] | imm
	case 0x0E: // XORI
		r.Reg[rt] = r.Reg[rs] ^ imm
	case 0x0F: // LUI
		r.Reg[rt] = imm << 16
	case 0x10: // COP0: the SP register set
		switch rs {
		case 0: // MFC0
			r.Reg[rt] = r.ReadCP0(int(rd))
		case 4: // MTC0
			r.WriteCP0(int(rd), r.Reg[rt])
		}
	case 0x12: // COP2: vector unit moves and computation
		r.execCOP2(instr)
	case 0x20: // LB
		r.Reg[rt] = uint32(int32(int8(r.loadByte(r.Reg[rs], simm))))
	case 0x21: // LH
		r.Reg[rt] = uint32(int32(int16(r.loadHalf(r.Reg[rs], simm))))
	case 0x23: // LW
		r.Reg[rt] = r.loadWord(r.Reg[rs], simm)
	case 0x24: // LBU
		r.Reg[rt] = uint32(r.loadByte(r.Reg[rs], simm))
	case 0x25: // LHU
		r.Reg[rt] = uint32(r.loadHalf(r.Reg[rs], simm))
	case 0x22: // LWL
		r.loadWordLeft(rt, r.Reg[rs], simm, true)
	case 0x26: // LWR
		r.loadWordLeft(rt, r.Reg[rs], simm, false)
	case 0x28: // SB
		r.setDMEMByte(addrOf(r.Reg[rs], simm), byte(r.Reg[rt]))
	case 0x29: // SH
		r.storeHalf(r.Reg[rs], simm, uint16(r.Reg[rt]))
	case 0x2B: // SW
		r.storeWord(r.Reg[rs], simm, r.Reg[rt])
	case 0x2A: // SWL
		r.storeWordLeft(rt, r.Reg[rs], simm, true)
	case 0x2E: // SWR
		r.storeWordLeft(rt, r.Reg[rs], simm, false)
	case 0x32: // LWC2: vector loads
		r.execLoadStore(instr, true)
	case 0x3A: // SWC2: vector stores
		r.execLoadStore(instr, false)
	}
	// Every other encoding is a NOP on the RSP, including
	// COP1 (no floating point unit) and the 64-bit forms.
}

// div performs a signed divide with the common MIPS
// divide-by-zero convention (the architecture leaves the
// result undefined; the RSP documentation does not pin
// it either).
func (r *RSP) div(a, b int32) {
	if b == 0 {
		if a >= 0 {
			r.LO = 0xFFFFFFFF
		} else {
			r.LO = 1
		}
		r.HI = uint32(a)
		return
	}
	if a == -1<<31 && b == -1 {
		r.LO, r.HI = uint32(a), 0
		return
	}
	r.LO = uint32(a / b)
	r.HI = uint32(a % b)
}

// divu performs an unsigned divide (divide by zero keeps
// the same convention as the signed form).
func (r *RSP) divu(a, b uint32) {
	if b == 0 {
		r.LO = 0xFFFFFFFF
		r.HI = a
		return
	}
	r.LO = a / b
	r.HI = a % b
}

func bool32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// addrOf forms a DMEM effective address: base plus
// sign-extended offset, low 12 bits used.
func addrOf(base uint32, off int32) uint32 {
	return (base + uint32(off)) & 0xFFF
}

func (r *RSP) loadByte(base uint32, off int32) byte {
	return r.dmemByte(addrOf(base, off))
}

func (r *RSP) loadHalf(base uint32, off int32) uint16 {
	a := addrOf(base, off)
	return uint16(r.dmemByte(a))<<8 | uint16(r.dmemByte(a+1))
}

func (r *RSP) loadWord(base uint32, off int32) uint32 {
	a := addrOf(base, off)
	return uint32(r.dmemByte(a))<<24 | uint32(r.dmemByte(a+1))<<16 |
		uint32(r.dmemByte(a+2))<<8 | uint32(r.dmemByte(a+3))
}

func (r *RSP) storeHalf(base uint32, off int32, v uint16) {
	a := addrOf(base, off)
	r.setDMEMByte(a, byte(v>>8))
	r.setDMEMByte(a+1, byte(v))
}

func (r *RSP) storeWord(base uint32, off int32, v uint32) {
	a := addrOf(base, off)
	r.setDMEMByte(a, byte(v>>24))
	r.setDMEMByte(a+1, byte(v>>16))
	r.setDMEMByte(a+2, byte(v>>8))
	r.setDMEMByte(a+3, byte(v))
}

// regByte views a scalar register as big-endian bytes,
// index 0 most significant.
func regByte(v uint32, i int) byte { return byte(v >> (8 * uint(3-i))) }

func setRegByte(v uint32, i int, b byte) uint32 {
	sh := 8 * uint(3-i)
	return v&^(0xFF<<sh) | uint32(b)<<sh
}

// loadWordLeft implements the big-endian LWL (left=true)
// and LWR (left=false) byte merges into register rt.
func (r *RSP) loadWordLeft(rt, base uint32, off int32, left bool) {
	addr := addrOf(base, off)
	v := r.Reg[rt]
	if left {
		n := 4 - int(addr&3)
		for k := 0; k < n; k++ {
			v = setRegByte(v, k, r.dmemByte(addr+uint32(k)))
		}
	} else {
		n := int(addr&3) + 1
		for k := 0; k < n; k++ {
			v = setRegByte(v, 4-n+k, r.dmemByte(addr-uint32(n-1)+uint32(k)))
		}
	}
	r.Reg[rt] = v
}

// storeWordLeft implements the big-endian SWL/SWR byte
// merges from register rt.
func (r *RSP) storeWordLeft(rt, base uint32, off int32, left bool) {
	addr := addrOf(base, off)
	v := r.Reg[rt]
	if left {
		n := 4 - int(addr&3)
		for k := 0; k < n; k++ {
			r.setDMEMByte(addr+uint32(k), regByte(v, k))
		}
	} else {
		n := int(addr&3) + 1
		for k := 0; k < n; k++ {
			r.setDMEMByte(addr-uint32(n-1)+uint32(k), regByte(v, 4-n+k))
		}
	}
}
