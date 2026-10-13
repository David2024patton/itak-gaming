package enginetest

// A tiny two pass SH-2 assembler, just large enough for
// the enginetest IP. Encodings follow the SH-2 hardware
// manual and were cross checked against the chips/sh2
// implementation in this module (in particular the load
// and store groups: MOV.L Rm,@Rn is 2nm2, the pre
// decrement store is 2nm6, MOV.L @Rm+,Rn is 6nm6).
//
// Branch and BSR displacements and PC relative literal
// loads are fixups resolved by Build. Literal loads bind
// to the next Pool flush after the load, so callers place
// Pool calls at dead fallthrough points (after an
// unconditional branch or an RTS) often enough that every
// literal stays within the 1020 byte forward reach of
// MOV.L @(disp,PC); Build reports any that do not.
//
// Delay slots are the caller's job: every branch, BSR,
// JMP and RTS emitted here must be followed by the
// instruction that belongs in its delay slot (a Nop in
// most of this program).

import "fmt"

type fixupKind int

const (
	fixBranch8 fixupKind = iota
	fixBranch12
	fixLiteral
)

type fixup struct {
	at    int // word index of the instruction
	kind  fixupKind
	label string
	lit   int // pending literal request index
}

type litRequest struct {
	value uint32
	addr  uint32 // resolved pool address, set at flush or Build
}

// Asm accumulates one SH-2 program at a fixed base
// address.
type Asm struct {
	base    uint32
	words   []uint16
	labels  map[string]uint32
	fixups  []fixup
	pending []litRequest
	poolSeq int
}

// NewAsm starts a program whose first word lands at base.
func NewAsm(base uint32) *Asm {
	return &Asm{
		base:   base,
		labels: make(map[string]uint32),
	}
}

func (a *Asm) pc() uint32 { return a.base + uint32(2*len(a.words)) }

// Emit appends one raw word.
func (a *Asm) Emit(w uint16) { a.words = append(a.words, w) }

// Label marks the current position with a name.
func (a *Asm) Label(name string) { a.labels[name] = a.pc() }

// Here returns the absolute address of the current
// position.
func (a *Asm) Here() uint32 { return a.pc() }

// Pool flushes the pending literal batch: every request
// not yet flushed is emitted at the next 4 byte boundary,
// deduplicated by value within the batch. The batch is
// preceded by a BRA over the data, so flushing is safe at
// fallthrough points as well as at dead points (after an
// unconditional branch or an RTS, where the BRA is simply
// unreachable). Calling Pool with nothing pending is a
// no-op.
func (a *Asm) Pool() {
	need := false
	for i := range a.pending {
		if a.pending[i].addr == 0 {
			need = true
			break
		}
	}
	if !need {
		return
	}
	if len(a.words)%2 != 0 {
		a.Emit(0x0009)
	}
	end := fmt.Sprintf("poolend%d", a.poolSeq)
	a.poolSeq++
	a.Bra(end)
	a.Nop()
	done := make(map[uint32]uint32)
	for i := range a.pending {
		if a.pending[i].addr != 0 {
			continue
		}
		if addr, ok := done[a.pending[i].value]; ok {
			a.pending[i].addr = addr
			continue
		}
		addr := a.pc()
		done[a.pending[i].value] = addr
		a.Emit(uint16(a.pending[i].value >> 16))
		a.Emit(uint16(a.pending[i].value))
		a.pending[i].addr = addr
	}
	a.Label(end)
}

// Words returns the assembled program so far (without
// resolving fixups); data builders use it for layout.
func (a *Asm) Words() []uint16 { return a.words }

// Build resolves every fixup and returns the finished
// program words. Any literal still pending is flushed at
// the end.
func (a *Asm) Build() ([]uint16, error) {
	a.Pool()
	// Requests made after the last manual Pool flush were
	// flushed by the Pool call above; their addresses are
	// recorded in the requests themselves.
	out := append([]uint16(nil), a.words...)
	for _, f := range a.fixups {
		insnAddr := a.base + uint32(2*f.at)
		switch f.kind {
		case fixBranch8, fixBranch12:
			target, ok := a.labels[f.label]
			if !ok {
				return nil, fmt.Errorf("asm: undefined label %q", f.label)
			}
			diff := int32(target) - int32(insnAddr+4)
			if diff%2 != 0 {
				return nil, fmt.Errorf("asm: branch to %q is not word aligned", f.label)
			}
			d := diff / 2
			if f.kind == fixBranch8 {
				if d < -128 || d > 127 {
					return nil, fmt.Errorf("asm: short branch to %q out of range (%d words)", f.label, d)
				}
				out[f.at] |= uint16(byte(d))
			} else {
				if d < -2048 || d > 2047 {
					return nil, fmt.Errorf("asm: branch to %q out of range (%d words)", f.label, d)
				}
				out[f.at] |= uint16(d) & 0x0FFF
			}
		case fixLiteral:
			req := a.pending[f.lit]
			if req.addr == 0 {
				return nil, fmt.Errorf("asm: literal %#x was never pooled", req.value)
			}
			base := (insnAddr + 4) &^ 3
			diff := int32(req.addr) - int32(base)
			if diff < 0 || diff%4 != 0 || diff/4 > 255 {
				return nil, fmt.Errorf("asm: literal %#x out of reach of its load (distance %d)", req.value, diff)
			}
			out[f.at] |= uint16(diff / 4)
		}
	}
	return out, nil
}

func reg(r int) uint16 {
	if r < 0 || r > 15 {
		panic("asm: register out of range")
	}
	return uint16(r)
}

// Nop emits NOP (also the standard delay slot filler).
func (a *Asm) Nop() { a.Emit(0x0009) }

// Rts emits RTS; the caller supplies the delay slot.
func (a *Asm) Rts() { a.Emit(0x000B) }

// MovImm emits MOV #imm,Rn. The immediate is sign
// extended from 8 bits.
func (a *Asm) MovImm(rn int, imm int) {
	if imm < -128 || imm > 127 {
		panic("asm: MOV immediate out of range")
	}
	a.Emit(0xE000 | reg(rn)<<8 | uint16(byte(imm)))
}

// MovReg emits MOV Rm,Rn.
func (a *Asm) MovReg(rn, rm int) { a.Emit(0x6003 | reg(rn)<<8 | reg(rm)<<4) }

// LoadL emits MOV.L @Rm,Rn.
func (a *Asm) LoadL(rn, rm int) { a.Emit(0x6002 | reg(rn)<<8 | reg(rm)<<4) }

// LoadW emits MOV.W @Rm,Rn (sign extended).
func (a *Asm) LoadW(rn, rm int) { a.Emit(0x6001 | reg(rn)<<8 | reg(rm)<<4) }

// LoadB emits MOV.B @Rm,Rn (sign extended).
func (a *Asm) LoadB(rn, rm int) { a.Emit(0x6000 | reg(rn)<<8 | reg(rm)<<4) }

// LoadLInc emits MOV.L @Rm+,Rn.
func (a *Asm) LoadLInc(rn, rm int) { a.Emit(0x6006 | reg(rn)<<8 | reg(rm)<<4) }

// LoadWInc emits MOV.W @Rm+,Rn.
func (a *Asm) LoadWInc(rn, rm int) { a.Emit(0x6005 | reg(rn)<<8 | reg(rm)<<4) }

// LoadBInc emits MOV.B @Rm+,Rn.
func (a *Asm) LoadBInc(rn, rm int) { a.Emit(0x6004 | reg(rn)<<8 | reg(rm)<<4) }

// StoreL emits MOV.L Rm,@Rn (Rn is the address register).
func (a *Asm) StoreL(rn, rm int) { a.Emit(0x2002 | reg(rn)<<8 | reg(rm)<<4) }

// StoreW emits MOV.W Rm,@Rn.
func (a *Asm) StoreW(rn, rm int) { a.Emit(0x2001 | reg(rn)<<8 | reg(rm)<<4) }

// StoreB emits MOV.B Rm,@Rn.
func (a *Asm) StoreB(rn, rm int) { a.Emit(0x2000 | reg(rn)<<8 | reg(rm)<<4) }

// StoreLDisp emits MOV.L Rm,@(disp,Rn); disp is a byte
// offset, a multiple of 4 in 0..60.
func (a *Asm) StoreLDisp(rn, rm int, disp int) {
	if disp < 0 || disp > 60 || disp%4 != 0 {
		panic("asm: bad long store displacement")
	}
	a.Emit(0x1000 | reg(rn)<<8 | reg(rm)<<4 | uint16(disp/4))
}

// LoadLDisp emits MOV.L @(disp,Rm),Rn.
func (a *Asm) LoadLDisp(rn, rm int, disp int) {
	if disp < 0 || disp > 60 || disp%4 != 0 {
		panic("asm: bad long load displacement")
	}
	a.Emit(0x5000 | reg(rn)<<8 | reg(rm)<<4 | uint16(disp/4))
}

// LoadLit emits MOV.L @(disp,PC),Rn for the constant v,
// resolved against the next Pool flush.
func (a *Asm) LoadLit(rn int, v uint32) {
	idx := -1
	for i := range a.pending {
		if a.pending[i].value == v && a.pending[i].addr == 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.pending = append(a.pending, litRequest{value: v})
		idx = len(a.pending) - 1
	}
	a.fixups = append(a.fixups, fixup{at: len(a.words), kind: fixLiteral, lit: idx})
	a.Emit(0xD000 | reg(rn)<<8)
}

// AddImm emits ADD #imm,Rn (sign extended 8 bit).
func (a *Asm) AddImm(rn int, imm int) {
	if imm < -128 || imm > 127 {
		panic("asm: ADD immediate out of range")
	}
	a.Emit(0x7000 | reg(rn)<<8 | uint16(byte(imm)))
}

// AddReg emits ADD Rm,Rn.
func (a *Asm) AddReg(rn, rm int) { a.Emit(0x300C | reg(rn)<<8 | reg(rm)<<4) }

// SubReg emits SUB Rm,Rn.
func (a *Asm) SubReg(rn, rm int) { a.Emit(0x3008 | reg(rn)<<8 | reg(rm)<<4) }

// CmpEqImm emits CMP/EQ #imm,R0.
func (a *Asm) CmpEqImm(imm int) {
	if imm < -128 || imm > 127 {
		panic("asm: CMP immediate out of range")
	}
	a.Emit(0x8800 | uint16(byte(imm)))
}

// CmpEq emits CMP/EQ Rm,Rn.
func (a *Asm) CmpEq(rn, rm int) { a.Emit(0x3000 | reg(rn)<<8 | reg(rm)<<4) }

// CmpHS emits CMP/HS Rm,Rn (unsigned Rn >= Rm).
func (a *Asm) CmpHS(rn, rm int) { a.Emit(0x3002 | reg(rn)<<8 | reg(rm)<<4) }

// CmpGE emits CMP/GE Rm,Rn (signed Rn >= Rm).
func (a *Asm) CmpGE(rn, rm int) { a.Emit(0x3003 | reg(rn)<<8 | reg(rm)<<4) }

// TstImm emits TST #imm,R0: T is set when R0 AND imm is
// zero.
func (a *Asm) TstImm(imm int) { a.Emit(0xC800 | uint16(byte(imm))) }

// TstReg emits TST Rm,Rn: T is set when Rn AND Rm is zero.
func (a *Asm) TstReg(rn, rm int) { a.Emit(0x2008 | reg(rn)<<8 | reg(rm)<<4) }

// AndImm emits AND #imm,R0.
func (a *Asm) AndImm(imm int) { a.Emit(0xC900 | uint16(byte(imm))) }

// OrReg emits OR Rm,Rn.
func (a *Asm) OrReg(rn, rm int) { a.Emit(0x200B | reg(rn)<<8 | reg(rm)<<4) }

// Bt emits BT label (8 bit displacement).
func (a *Asm) Bt(label string) {
	a.fixups = append(a.fixups, fixup{at: len(a.words), kind: fixBranch8, label: label})
	a.Emit(0x8900)
}

// Bf emits BF label (8 bit displacement).
func (a *Asm) Bf(label string) {
	a.fixups = append(a.fixups, fixup{at: len(a.words), kind: fixBranch8, label: label})
	a.Emit(0x8B00)
}

// Bra emits BRA label (12 bit displacement).
func (a *Asm) Bra(label string) {
	a.fixups = append(a.fixups, fixup{at: len(a.words), kind: fixBranch12, label: label})
	a.Emit(0xA000)
}

// Bsr emits BSR label (12 bit displacement).
func (a *Asm) Bsr(label string) {
	a.fixups = append(a.fixups, fixup{at: len(a.words), kind: fixBranch12, label: label})
	a.Emit(0xB000)
}

// Dt emits DT Rn: Rn decrements, T is set when it reaches
// zero.
func (a *Asm) Dt(rn int) { a.Emit(0x4010 | reg(rn)<<8) }

// ExtuB emits EXTU.B Rm,Rn.
func (a *Asm) ExtuB(rn, rm int) { a.Emit(0x600C | reg(rn)<<8 | reg(rm)<<4) }

// ExtuW emits EXTU.W Rm,Rn.
func (a *Asm) ExtuW(rn, rm int) { a.Emit(0x600D | reg(rn)<<8 | reg(rm)<<4) }

// Shll emits SHLL Rn.
func (a *Asm) Shll(rn int) { a.Emit(0x4000 | reg(rn)<<8) }

// Shlr emits SHLR Rn.
func (a *Asm) Shlr(rn int) { a.Emit(0x4001 | reg(rn)<<8) }

// Shll2 emits SHLL2 Rn.
func (a *Asm) Shll2(rn int) { a.Emit(0x4008 | reg(rn)<<8) }

// Shlr2 emits SHLR2 Rn.
func (a *Asm) Shlr2(rn int) { a.Emit(0x4009 | reg(rn)<<8) }

// Shll8 emits SHLL8 Rn.
func (a *Asm) Shll8(rn int) { a.Emit(0x4018 | reg(rn)<<8) }

// Shlr8 emits SHLR8 Rn.
func (a *Asm) Shlr8(rn int) { a.Emit(0x4019 | reg(rn)<<8) }

// Shll16 emits SHLL16 Rn.
func (a *Asm) Shll16(rn int) { a.Emit(0x4028 | reg(rn)<<8) }
