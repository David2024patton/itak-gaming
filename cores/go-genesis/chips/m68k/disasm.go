package m68k

import "fmt"

// Disassemble decodes the instruction at pc, reading instruction words
// through fetch. It returns the mnemonic text and the address of the
// next instruction. The common instruction forms are covered; anything
// unrecognized (or an encoding the decoder rejects) renders as
// "DC.W $XXXX" and advances one word.
func Disassemble(pc uint32, fetch func(addr uint32) uint16) (string, uint32) {
	op := fetch(pc)
	d := &disState{start: pc, cur: pc + 2, fetch: fetch}
	text := d.decode(op)
	if text == "" {
		return fmt.Sprintf("DC.W $%04X", op), pc + 2
	}
	return text, d.cur
}

type disState struct {
	start uint32
	cur   uint32
	fetch func(addr uint32) uint16
}

func (d *disState) w() uint16 {
	v := d.fetch(d.cur)
	d.cur += 2
	return v
}

func (d *disState) l() uint32 {
	hi := uint32(d.w())
	return hi<<16 | uint32(d.w())
}

var disSizeSuffix = []string{".B", ".W", ".L", ""}

var disCond = []string{"T", "F", "HI", "LS", "CC", "CS", "NE", "EQ",
	"VC", "VS", "PL", "MI", "GE", "LT", "GT", "LE"}

// hexDisp formats a signed displacement as $XX or -$XX.
func hexDisp(v int32) string {
	if v < 0 {
		return fmt.Sprintf("-$%X", -v)
	}
	return fmt.Sprintf("$%X", v)
}

// indexStr formats the register of an index extension word.
func indexStr(ext uint16) string {
	name := byte('D')
	if ext&0x8000 != 0 {
		name = 'A'
	}
	sz := "W"
	if ext&0x0800 != 0 {
		sz = "L"
	}
	return fmt.Sprintf("%c%d.%s", name, (ext>>12)&7, sz)
}

// ea formats an effective-address operand, consuming its extension
// words exactly as the executor's operand resolver does.
func (d *disState) ea(mode, reg, size int) string {
	switch mode {
	case 0:
		return fmt.Sprintf("D%d", reg)
	case 1:
		return fmt.Sprintf("A%d", reg)
	case 2:
		return fmt.Sprintf("(A%d)", reg)
	case 3:
		return fmt.Sprintf("(A%d)+", reg)
	case 4:
		return fmt.Sprintf("-(A%d)", reg)
	case 5:
		disp := int32(int16(d.w()))
		return fmt.Sprintf("%s(A%d)", hexDisp(disp), reg)
	case 6:
		ext := d.w()
		disp := int32(int8(uint8(ext & 0xFF)))
		return fmt.Sprintf("%s(A%d,%s)", hexDisp(disp), reg, indexStr(ext))
	case 7:
		switch reg {
		case 0:
			return fmt.Sprintf("$%04X", d.w())
		case 1:
			return fmt.Sprintf("$%08X", d.l())
		case 2:
			disp := int32(int16(d.w()))
			return fmt.Sprintf("%s(PC)", hexDisp(disp))
		case 3:
			ext := d.w()
			disp := int32(int8(uint8(ext & 0xFF)))
			return fmt.Sprintf("%s(PC,%s)", hexDisp(disp), indexStr(ext))
		case 4:
			switch size {
			case szB:
				return fmt.Sprintf("#$%02X", d.w()&0xFF)
			case szW:
				return fmt.Sprintf("#$%04X", d.w())
			case szL:
				return fmt.Sprintf("#$%08X", d.l())
			}
		}
	}
	return "?"
}

// imm formats a bare immediate operand (line-0 ops read it before the EA).
func (d *disState) imm(size int) string {
	switch size {
	case szB:
		return fmt.Sprintf("#$%02X", d.w()&0xFF)
	case szW:
		return fmt.Sprintf("#$%04X", d.w())
	case szL:
		return fmt.Sprintf("#$%08X", d.l())
	}
	return "#?"
}

func (d *disState) decode(op uint16) string {
	switch op >> 12 {
	case 0x0:
		return d.decodeLine0(op)
	case 0x1:
		return d.decodeMove(op, szB)
	case 0x2:
		return d.decodeMove(op, szL)
	case 0x3:
		return d.decodeMove(op, szW)
	case 0x4:
		return d.decodeLine4(op)
	case 0x5:
		return d.decodeLine5(op)
	case 0x6:
		cond := int((op >> 8) & 0xF)
		base := d.start + 2
		var disp int32
		if op&0xFF == 0 {
			disp = int32(int16(d.w()))
		} else {
			disp = int32(int8(uint8(op & 0xFF)))
		}
		target := uint32(int32(base) + disp)
		switch cond {
		case 0:
			return fmt.Sprintf("BRA $%06X", target)
		case 1:
			return fmt.Sprintf("BSR $%06X", target)
		}
		return fmt.Sprintf("B%s $%06X", disCond[cond], target)
	case 0x7:
		if op&0x0100 != 0 {
			return ""
		}
		return fmt.Sprintf("MOVEQ #$%02X,D%d", op&0xFF, (op>>9)&7)
	case 0x8:
		return d.decodeLine8(op)
	case 0x9:
		return d.decodeLine9D(op, "SUB")
	case 0xA, 0xF:
		return "" // line A/F are exception stubs, not instructions
	case 0xB:
		return d.decodeLineB(op)
	case 0xC:
		return d.decodeLineC(op)
	case 0xD:
		return d.decodeLine9D(op, "ADD")
	case 0xE:
		return d.decodeShifts(op)
	}
	return ""
}

func (d *disState) decodeLine0(op uint16) string {
	hi := (op >> 8) & 0xF
	mode, reg := int((op>>3)&7), int(op&7)
	size := int((op >> 6) & 3)
	switch hi {
	case 0x0, 0x2, 0x4, 0x6, 0xA, 0xC:
		names := map[uint16]string{0x0: "ORI", 0x2: "ANDI", 0x4: "SUBI",
			0x6: "ADDI", 0xA: "EORI", 0xC: "CMPI"}
		if size == 3 {
			return ""
		}
		if mode == 7 && reg == 4 && (hi == 0x0 || hi == 0x2 || hi == 0xA) {
			switch size {
			case szB:
				v := d.w()
				return fmt.Sprintf("%s #$%02X,CCR", names[hi], v&0xFF)
			case szW:
				v := d.w()
				return fmt.Sprintf("%s #$%04X,SR", names[hi], v)
			}
			return ""
		}
		imm := d.imm(size)
		dst := d.ea(mode, reg, size)
		return fmt.Sprintf("%s%s %s,%s", names[hi], disSizeSuffix[size], imm, dst)
	case 0x8:
		bops := []string{"BTST", "BCHG", "BCLR", "BSET"}
		bop := bops[(op>>6)&3]
		bit := d.w() & 0xFF
		dst := d.ea(mode, reg, szB)
		return fmt.Sprintf("%s #$%02X,%s", bop, bit, dst)
	default:
		if mode == 1 {
			// MOVEP
			disp := int32(int16(d.w()))
			sz := ".W"
			if op&0x40 != 0 {
				sz = ".L"
			}
			ea := fmt.Sprintf("%s(A%d)", hexDisp(disp), reg)
			if op&0x80 != 0 {
				return fmt.Sprintf("MOVEP%s D%d,%s", sz, (op>>9)&7, ea)
			}
			return fmt.Sprintf("MOVEP%s %s,D%d", sz, ea, (op>>9)&7)
		}
		bops := []string{"BTST", "BCHG", "BCLR", "BSET"}
		dst := d.ea(mode, reg, szB)
		return fmt.Sprintf("%s D%d,%s", bops[(op>>6)&3], (op>>9)&7, dst)
	}
}

func (d *disState) decodeMove(op uint16, size int) string {
	dstReg := int((op >> 9) & 7)
	dstMode := int((op >> 6) & 7)
	srcMode, srcReg := int((op>>3)&7), int(op&7)
	src := d.ea(srcMode, srcReg, size)
	dst := d.ea(dstMode, dstReg, size)
	if dstMode == 1 {
		return fmt.Sprintf("MOVEA%s %s,A%d", disSizeSuffix[size], src, dstReg)
	}
	return fmt.Sprintf("MOVE%s %s,%s", disSizeSuffix[size], src, dst)
}

// movemList renders a MOVEM register mask as a compact list. rev
// selects the predecrement interpretation of the mask bits.
func movemList(mask uint16, rev bool) string {
	var present [16]bool // index: 0-7 = D0-D7, 8-15 = A0-A7
	for j := 0; j < 16; j++ {
		if mask>>uint(j)&1 == 0 {
			continue
		}
		var kind, idx int
		if rev {
			kind, idx = movemRevReg(j)
		} else {
			kind, idx = movemListReg(j)
		}
		if kind == 0 {
			present[idx] = true
		} else {
			present[8+idx] = true
		}
	}
	name := func(i int) string {
		if i < 8 {
			return fmt.Sprintf("D%d", i)
		}
		return fmt.Sprintf("A%d", i-8)
	}
	out := ""
	for base := 0; base < 16; base += 8 {
		i := base
		for i < base+8 {
			if !present[i] {
				i++
				continue
			}
			j := i
			for j+1 < base+8 && present[j+1] {
				j++
			}
			if out != "" {
				out += "/"
			}
			if j > i {
				out += name(i) + "-" + name(j)
			} else {
				out += name(i)
			}
			i = j + 1
		}
	}
	return out
}

func (d *disState) decodeLine4(op uint16) string {
	bits86 := (op >> 6) & 7
	mode, reg := int((op>>3)&7), int(op&7)
	if bits86 == 6 {
		src := d.ea(mode, reg, szW)
		return fmt.Sprintf("CHK.W %s,D%d", src, (op>>9)&7)
	}
	if bits86 == 7 {
		src := d.ea(mode, reg, szW)
		return fmt.Sprintf("LEA %s,A%d", src, (op>>9)&7)
	}
	fam := (op >> 8) & 0xF
	ss := int((op >> 6) & 3)
	switch fam {
	case 0x0:
		if ss == 3 {
			dst := d.ea(mode, reg, szW)
			return fmt.Sprintf("MOVE SR,%s", dst)
		}
		return fmt.Sprintf("NEGX%s %s", disSizeSuffix[ss], d.ea(mode, reg, ss))
	case 0x2:
		return fmt.Sprintf("CLR%s %s", disSizeSuffix[ss], d.ea(mode, reg, ss))
	case 0x4:
		if ss == 3 {
			src := d.ea(mode, reg, szW)
			return fmt.Sprintf("MOVE %s,CCR", src)
		}
		return fmt.Sprintf("NEG%s %s", disSizeSuffix[ss], d.ea(mode, reg, ss))
	case 0x6:
		if ss == 3 {
			src := d.ea(mode, reg, szW)
			return fmt.Sprintf("MOVE %s,SR", src)
		}
		return fmt.Sprintf("NOT%s %s", disSizeSuffix[ss], d.ea(mode, reg, ss))
	case 0x8:
		if op&0x0080 != 0 && mode != 0 {
			mask := d.w()
			dst := d.ea(mode, reg, szW)
			sz := ".W"
			if op&0x40 != 0 {
				sz = ".L"
			}
			return fmt.Sprintf("MOVEM%s %s,%s", sz, movemList(mask, mode == 4), dst)
		}
		switch ss {
		case 0:
			return fmt.Sprintf("NBCD %s", d.ea(mode, reg, szB))
		case 1:
			if mode == 0 {
				return fmt.Sprintf("SWAP D%d", reg)
			}
			return fmt.Sprintf("PEA %s", d.ea(mode, reg, szW))
		case 2:
			return fmt.Sprintf("EXT.W D%d", reg)
		case 3:
			return fmt.Sprintf("EXT.L D%d", reg)
		}
	case 0xA:
		if ss == 3 {
			return fmt.Sprintf("TAS %s", d.ea(mode, reg, szB))
		}
		return fmt.Sprintf("TST%s %s", disSizeSuffix[ss], d.ea(mode, reg, ss))
	case 0xC:
		if op&0x0080 != 0 {
			mask := d.w()
			src := d.ea(mode, reg, szW)
			sz := ".W"
			if op&0x40 != 0 {
				sz = ".L"
			}
			return fmt.Sprintf("MOVEM%s %s,%s", sz, src, movemList(mask, false))
		}
		return ""
	case 0xE:
		switch ss {
		case 1:
			return d.decodeLine4Control(op)
		case 2:
			return fmt.Sprintf("JSR %s", d.ea(mode, reg, szW))
		case 3:
			return fmt.Sprintf("JMP %s", d.ea(mode, reg, szW))
		}
		return ""
	}
	return ""
}

func (d *disState) decodeLine4Control(op uint16) string {
	switch {
	case op&0xFFF0 == 0x4E40:
		return fmt.Sprintf("TRAP #%d", op&0xF)
	case op&0xFFF8 == 0x4E50:
		disp := d.w()
		return fmt.Sprintf("LINK A%d,#$%04X", op&7, disp)
	case op&0xFFF8 == 0x4E58:
		return fmt.Sprintf("UNLK A%d", op&7)
	case op&0xFFF0 == 0x4E60:
		if op&0x8 != 0 {
			return fmt.Sprintf("MOVE.L USP,A%d", op&7)
		}
		return fmt.Sprintf("MOVE.L A%d,USP", op&7)
	case op == 0x4E70:
		return "RESET"
	case op == 0x4E71:
		return "NOP"
	case op == 0x4E72:
		return fmt.Sprintf("STOP #$%04X", d.w())
	case op == 0x4E73:
		return "RTE"
	case op == 0x4E75:
		return "RTS"
	case op == 0x4E76:
		return "TRAPV"
	case op == 0x4E77:
		return "RTR"
	}
	return ""
}

func (d *disState) decodeLine5(op uint16) string {
	mode, reg := int((op>>3)&7), int(op&7)
	cond := int((op >> 8) & 0xF)
	if (op>>6)&3 == 3 {
		if mode == 1 {
			disp := int32(int16(d.w()))
			target := uint32(int32(d.start+2) + disp)
			return fmt.Sprintf("DB%s D%d,$%06X", disCond[cond], reg, target)
		}
		return fmt.Sprintf("S%s %s", disCond[cond], d.ea(mode, reg, szB))
	}
	data := (op >> 9) & 7
	if data == 0 {
		data = 8
	}
	name := "ADDQ"
	if op&0x0100 != 0 {
		name = "SUBQ"
	}
	size := int((op >> 6) & 3)
	return fmt.Sprintf("%s%s #%d,%s", name, disSizeSuffix[size], data, d.ea(mode, reg, size))
}

// twoOperand formats a line 8/9/B/C/D instruction in its <ea>,Dn form.
func (d *disState) twoReg(op uint16, name string, size int) string {
	mode, reg := int((op>>3)&7), int(op&7)
	src := d.ea(mode, reg, size)
	return fmt.Sprintf("%s%s %s,D%d", name, disSizeSuffix[size], src, (op>>9)&7)
}

// twoMem formats the Dn,<ea> (destination EA) form.
func (d *disState) twoMem(op uint16, name string, size int) string {
	mode, reg := int((op>>3)&7), int(op&7)
	dst := d.ea(mode, reg, size)
	return fmt.Sprintf("%s%s D%d,%s", name, disSizeSuffix[size], (op>>9)&7, dst)
}

// bcdOperands formats the register or predecrement operand pair used by
// ABCD/SBCD/ADDX/SUBX (source first).
func bcdOperands(op uint16) string {
	src := int(op & 7)
	dst := int((op >> 9) & 7)
	if (op>>3)&1 != 0 {
		return fmt.Sprintf("-(A%d),-(A%d)", src, dst)
	}
	return fmt.Sprintf("D%d,D%d", src, dst)
}

func (d *disState) decodeLine8(op uint16) string {
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := int((op>>3)&7), int(op&7)
	switch {
	case opmode == 3:
		return fmt.Sprintf("DIVU.W %s,D%d", d.ea(mode, r, szW), reg)
	case opmode == 7:
		return fmt.Sprintf("DIVS.W %s,D%d", d.ea(mode, r, szW), reg)
	case opmode >= 4 && mode <= 1:
		return fmt.Sprintf("SBCD %s", bcdOperands(op))
	case opmode <= 2:
		return d.twoReg(op, "OR", opmode)
	default:
		return d.twoMem(op, "OR", opmode-4)
	}
}

func (d *disState) decodeLine9D(op uint16, name string) string {
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := int((op>>3)&7), int(op&7)
	switch {
	case opmode == 3:
		return fmt.Sprintf("%sA.W %s,A%d", name, d.ea(mode, r, szW), reg)
	case opmode == 7:
		return fmt.Sprintf("%sA.L %s,A%d", name, d.ea(mode, r, szL), reg)
	case opmode >= 4 && mode <= 1:
		return fmt.Sprintf("%sX%s %s", name, disSizeSuffix[opmode-4], bcdOperands(op))
	case opmode <= 2:
		return d.twoReg(op, name, opmode)
	default:
		return d.twoMem(op, name, opmode-4)
	}
}

func (d *disState) decodeLineB(op uint16) string {
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := int((op>>3)&7), int(op&7)
	switch {
	case opmode == 3:
		return fmt.Sprintf("CMPA.W %s,A%d", d.ea(mode, r, szW), reg)
	case opmode == 7:
		return fmt.Sprintf("CMPA.L %s,A%d", d.ea(mode, r, szL), reg)
	case opmode <= 2:
		return d.twoReg(op, "CMP", opmode)
	case mode == 1 && opmode >= 4:
		return fmt.Sprintf("CMPM%s (A%d)+,(A%d)+", disSizeSuffix[opmode-4], r, reg)
	default:
		return d.twoMem(op, "EOR", opmode-4)
	}
}

func (d *disState) decodeLineC(op uint16) string {
	switch {
	case op&0xF1F8 == 0xC140:
		return fmt.Sprintf("EXG D%d,D%d", (op>>9)&7, op&7)
	case op&0xF1F8 == 0xC148:
		return fmt.Sprintf("EXG A%d,A%d", (op>>9)&7, op&7)
	case op&0xF1F8 == 0xC188:
		return fmt.Sprintf("EXG D%d,A%d", (op>>9)&7, op&7)
	}
	reg := int((op >> 9) & 7)
	opmode := int((op >> 6) & 7)
	mode, r := int((op>>3)&7), int(op&7)
	switch {
	case opmode == 3:
		return fmt.Sprintf("MULU.W %s,D%d", d.ea(mode, r, szW), reg)
	case opmode == 7:
		return fmt.Sprintf("MULS.W %s,D%d", d.ea(mode, r, szW), reg)
	case opmode >= 4 && mode <= 1:
		return fmt.Sprintf("ABCD %s", bcdOperands(op))
	case opmode <= 2:
		return d.twoReg(op, "AND", opmode)
	default:
		return d.twoMem(op, "AND", opmode-4)
	}
}

func (d *disState) decodeShifts(op uint16) string {
	names := [4][2]string{
		{"ASR", "ASL"},
		{"LSR", "LSL"},
		{"ROXR", "ROXL"},
		{"ROR", "ROL"},
	}
	if (op>>6)&3 == 3 {
		typ := int((op >> 9) & 3)
		dir := int((op >> 8) & 1)
		mode, reg := int((op>>3)&7), int(op&7)
		return fmt.Sprintf("%s %s", names[typ][dir], d.ea(mode, reg, szW))
	}
	cntField := int((op >> 9) & 7)
	dir := int((op >> 8) & 1)
	size := int((op >> 6) & 3)
	typ := int((op >> 3) & 3)
	reg := int(op & 7)
	var cnt string
	if op&0x20 != 0 {
		cnt = fmt.Sprintf("D%d", cntField)
	} else {
		n := cntField
		if n == 0 {
			n = 8
		}
		cnt = fmt.Sprintf("#%d", n)
	}
	return fmt.Sprintf("%s%s %s,D%d", names[typ][dir], disSizeSuffix[size], cnt, reg)
}
