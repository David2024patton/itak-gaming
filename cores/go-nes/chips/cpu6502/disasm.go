package cpu6502

import "fmt"

// Disassembly support, in the format the nestest golden log uses
// (the Nintendulator trace format). Kept beside the CPU because
// it must agree with the decoder about every opcode's length and
// addressing mode; a test cross-checks the two over the whole
// opcode space.

// disMode is a disassembly addressing mode. It mirrors addrMode
// but adds the implied, relative, and JMP-indirect shapes the
// execution grid handles outside decode.
type disMode int

const (
	dmImp disMode = iota
	dmAcc
	dmImm
	dmZP
	dmZPX
	dmZPY
	dmAbs
	dmAbsX
	dmAbsY
	dmIndX
	dmIndY
	dmInd
	dmRel
)

// disInfo describes one opcode for the disassembler.
type disInfo struct {
	name       string
	mode       disMode
	unofficial bool
}

// disLen returns the instruction length in bytes for a mode.
func disLen(m disMode) int {
	switch m {
	case dmImp, dmAcc:
		return 1
	case dmAbs, dmAbsX, dmAbsY, dmInd:
		return 3
	default:
		return 2
	}
}

var disSpecials = map[uint8]disInfo{
	0x00: {"BRK", dmImp, false},
	0x20: {"JSR", dmAbs, false},
	0x40: {"RTI", dmImp, false},
	0x60: {"RTS", dmImp, false},
	0x4C: {"JMP", dmAbs, false},
	0x6C: {"JMP", dmInd, false},
	0x08: {"PHP", dmImp, false},
	0x48: {"PHA", dmImp, false},
	0x28: {"PLP", dmImp, false},
	0x68: {"PLA", dmImp, false},
	0x18: {"CLC", dmImp, false},
	0x38: {"SEC", dmImp, false},
	0x58: {"CLI", dmImp, false},
	0x78: {"SEI", dmImp, false},
	0xB8: {"CLV", dmImp, false},
	0xD8: {"CLD", dmImp, false},
	0xF8: {"SED", dmImp, false},
	0x88: {"DEY", dmImp, false},
	0x8A: {"TXA", dmImp, false},
	0x98: {"TYA", dmImp, false},
	0x9A: {"TXS", dmImp, false},
	0xA8: {"TAY", dmImp, false},
	0xAA: {"TAX", dmImp, false},
	0xBA: {"TSX", dmImp, false},
	0xC8: {"INY", dmImp, false},
	0xCA: {"DEX", dmImp, false},
	0xE8: {"INX", dmImp, false},
	0xEA: {"NOP", dmImp, false},
	0x1A: {"NOP", dmImp, true},
	0x3A: {"NOP", dmImp, true},
	0x5A: {"NOP", dmImp, true},
	0x7A: {"NOP", dmImp, true},
	0xDA: {"NOP", dmImp, true},
	0xFA: {"NOP", dmImp, true},
	0x02: {"KIL", dmImp, true},
	0x12: {"KIL", dmImp, true},
	0x22: {"KIL", dmImp, true},
	0x32: {"KIL", dmImp, true},
	0x42: {"KIL", dmImp, true},
	0x52: {"KIL", dmImp, true},
	0x62: {"KIL", dmImp, true},
	0x72: {"KIL", dmImp, true},
	0x92: {"KIL", dmImp, true},
	0xB2: {"KIL", dmImp, true},
	0xD2: {"KIL", dmImp, true},
	0xF2: {"KIL", dmImp, true},
	0x80: {"NOP", dmImm, true},
	0x82: {"NOP", dmImm, true},
	0x89: {"NOP", dmImm, true},
	0xC2: {"NOP", dmImm, true},
	0xE2: {"NOP", dmImm, true},
}

var branchNames = [...]string{"BPL", "BMI", "BVC", "BVS", "BCC", "BCS", "BNE", "BEQ"}

// disOf returns the disassembly description of an opcode,
// following the same grid decode as the executor.
func disOf(op uint8) disInfo {
	if info, ok := disSpecials[op]; ok {
		return info
	}
	if op&0x1F == 0x10 {
		return disInfo{branchNames[(op>>5)&7], dmRel, false}
	}
	cc := op & 3
	aaa := (op >> 5) & 7
	bbb := (op >> 2) & 7
	gridModes := [...]disMode{dmIndX, dmZP, dmImm, dmAbs, dmIndY, dmZPX, dmAbsY, dmAbsX}
	switch cc {
	case 1:
		names := [...]string{"ORA", "AND", "EOR", "ADC", "STA", "LDA", "CMP", "SBC"}
		return disInfo{names[aaa], gridModes[bbb], false}
	case 3:
		if bbb == 2 {
			names := [...]string{"ANC", "ANC", "ALR", "ARR", "XAA", "LXA", "AXS", "SBC"}
			return disInfo{names[aaa], dmImm, true}
		}
		names := [...]string{"SLO", "RLA", "SRE", "RRA", "SAX", "LAX", "DCP", "ISB"}
		m := gridModes[bbb]
		if bbb == 7 && aaa == 5 {
			m = dmAbsY // LAX absolute is Y-indexed
		}
		switch op {
		case 0x93, 0x9F:
			return disInfo{"AHX", m, true}
		case 0x9B:
			return disInfo{"TAS", dmAbsY, true}
		case 0xBB:
			return disInfo{"LAS", dmAbsY, true}
		}
		if (aaa == 4 || aaa == 5) && m == dmZPX {
			m = dmZPY
		}
		return disInfo{names[aaa], m, true}
	case 2:
		names := [...]string{"ASL", "ROL", "LSR", "ROR", "STX", "LDX", "DEC", "INC"}
		switch bbb {
		case 0:
			return disInfo{names[aaa], dmImm, false}
		case 1:
			return disInfo{names[aaa], dmZP, false}
		case 2:
			return disInfo{names[aaa], dmAcc, false}
		case 3:
			return disInfo{names[aaa], dmAbs, false}
		case 5:
			if aaa == 4 || aaa == 5 {
				return disInfo{names[aaa], dmZPY, false}
			}
			return disInfo{names[aaa], dmZPX, false}
		case 7:
			if aaa == 5 {
				return disInfo{names[aaa], dmAbsY, false}
			}
			if aaa == 4 {
				return disInfo{"SHX", dmAbsY, true}
			}
			return disInfo{names[aaa], dmAbsX, false}
		}
		return disInfo{"KIL", dmImp, true}
	default: // cc 0
		switch bbb {
		case 0:
			names := [...]string{"", "", "", "", "", "LDY", "CPY", "CPX"}
			return disInfo{names[aaa], dmImm, false}
		case 1:
			switch aaa {
			case 1:
				return disInfo{"BIT", dmZP, false}
			case 4:
				return disInfo{"STY", dmZP, false}
			case 5:
				return disInfo{"LDY", dmZP, false}
			case 6:
				return disInfo{"CPY", dmZP, false}
			case 7:
				return disInfo{"CPX", dmZP, false}
			}
			return disInfo{"NOP", dmZP, true}
		case 3:
			switch aaa {
			case 1:
				return disInfo{"BIT", dmAbs, false}
			case 4:
				return disInfo{"STY", dmAbs, false}
			case 5:
				return disInfo{"LDY", dmAbs, false}
			case 6:
				return disInfo{"CPY", dmAbs, false}
			case 7:
				return disInfo{"CPX", dmAbs, false}
			}
			return disInfo{"NOP", dmAbs, true}
		case 5:
			switch aaa {
			case 4:
				return disInfo{"STY", dmZPX, false}
			case 5:
				return disInfo{"LDY", dmZPX, false}
			}
			return disInfo{"NOP", dmZPX, true}
		case 7:
			if aaa == 5 {
				return disInfo{"LDY", dmAbsX, false}
			}
			if op == 0x9C {
				return disInfo{"SHY", dmAbsX, true}
			}
			return disInfo{"NOP", dmAbsX, true}
		}
		return disInfo{"KIL", dmImp, true}
	}
}

// FormatTrace renders one nestest golden-log line for the
// instruction at pc, using peek for side-effect-free memory reads
// and the CPU state as it stands BEFORE the instruction executes
// (r is that state; cycles is the cycle count at the same
// moment). It also returns the address whose value fills the
// line's trailing "= XX" annotation, so a comparer can tell a
// real divergence from an annotation about stub IO space.
func FormatTrace(peek func(uint16) uint8, pc uint16, r Registers, cycles uint64) (line string, annAddr uint16, hasAnn bool) {
	op := peek(pc)
	info := disOf(op)
	b1 := peek(pc + 1)
	b2 := peek(pc + 2)

	var bytesField string
	switch disLen(info.mode) {
	case 1:
		bytesField = fmt.Sprintf("%02X", op)
	case 2:
		bytesField = fmt.Sprintf("%02X %02X", op, b1)
	default:
		bytesField = fmt.Sprintf("%02X %02X %02X", op, b1, b2)
	}

	// The golden format reserves the first operand column for
	// the unofficial marker: unofficial mnemonics start with *,
	// official ones with a space.
	name := " " + info.name
	if info.unofficial {
		name = "*" + info.name
	}
	zpPtr := func(t uint8) uint16 {
		lo := peek(uint16(t))
		hi := peek(uint16(t + 1))
		return uint16(hi)<<8 | uint16(lo)
	}
	absAddr := uint16(b2)<<8 | uint16(b1)
	var operand string
	switch info.mode {
	case dmImp:
		operand = name
	case dmAcc:
		operand = name + " A"
	case dmImm:
		operand = fmt.Sprintf("%s #$%02X", name, b1)
	case dmZP:
		operand = fmt.Sprintf("%s $%02X = %02X", name, b1, peek(uint16(b1)))
		annAddr, hasAnn = uint16(b1), true
	case dmZPX:
		eff := b1 + r.X
		operand = fmt.Sprintf("%s $%02X,X @ %02X = %02X", name, b1, eff, peek(uint16(eff)))
		annAddr, hasAnn = uint16(eff), true
	case dmZPY:
		eff := b1 + r.Y
		operand = fmt.Sprintf("%s $%02X,Y @ %02X = %02X", name, b1, eff, peek(uint16(eff)))
		annAddr, hasAnn = uint16(eff), true
	case dmAbs:
		if info.name == "JMP" || info.name == "JSR" {
			operand = fmt.Sprintf("%s $%04X", name, absAddr)
		} else {
			operand = fmt.Sprintf("%s $%04X = %02X", name, absAddr, peek(absAddr))
			annAddr, hasAnn = absAddr, true
		}
	case dmAbsX:
		eff := absAddr + uint16(r.X)
		operand = fmt.Sprintf("%s $%04X,X @ %04X = %02X", name, absAddr, eff, peek(eff))
		annAddr, hasAnn = eff, true
	case dmAbsY:
		eff := absAddr + uint16(r.Y)
		operand = fmt.Sprintf("%s $%04X,Y @ %04X = %02X", name, absAddr, eff, peek(eff))
		annAddr, hasAnn = eff, true
	case dmIndX:
		zp := b1 + r.X
		ptr := zpPtr(zp)
		operand = fmt.Sprintf("%s ($%02X,X) @ %02X = %04X = %02X", name, b1, zp, ptr, peek(ptr))
		annAddr, hasAnn = ptr, true
	case dmIndY:
		ptr := zpPtr(b1)
		eff := ptr + uint16(r.Y)
		operand = fmt.Sprintf("%s ($%02X),Y = %04X @ %04X = %02X", name, b1, ptr, eff, peek(eff))
		annAddr, hasAnn = eff, true
	case dmInd:
		lo := peek(absAddr)
		hi := peek((absAddr & 0xFF00) | uint16(uint8(absAddr)+1))
		operand = fmt.Sprintf("%s ($%04X) = %04X", name, absAddr, uint16(hi)<<8|uint16(lo))
	case dmRel:
		target := uint16(int32(pc) + 2 + int32(int8(b1)))
		operand = fmt.Sprintf("%s $%04X", name, target)
	}

	ppu := cycles * 3
	line = fmt.Sprintf("%04X  %-9s%-32s A:%02X X:%02X Y:%02X P:%02X SP:%02X PPU:%3d,%3d CYC:%d",
		pc, bytesField, operand, r.A, r.X, r.Y, r.P, r.SP,
		(ppu/341)%262, ppu%341, cycles)
	return line, annAddr, hasAnn
}
