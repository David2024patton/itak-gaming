package system

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PSP-3 synthetic gates: hand assembled MIPS programs packaged as
// ET_EXEC ELFs (and one relocatable PRX) by the builders in this
// file, booted through the HLE kernel via LoadExecutable. The
// builders mirror the segment layout the loader's own tests prove
// (one PT_LOAD, module info located through its section).

// MIPS register numbers used by the gate programs.
const (
	rZero = 0
	rV0   = 2
	rV1   = 3
	rA0   = 4
	rA1   = 5
	rA2   = 6
	rA3   = 7
	rT0   = 8
	rT1   = 9
	rT2   = 10
	rT3   = 11
	rT4   = 12
	rT5   = 13
	rS0   = 16
	rS1   = 17
	rS2   = 18
	rS3   = 19
	rS4   = 20
	rS5   = 21
	rS6   = 22
	rS7   = 23
	rGP   = 28
	rSP   = 29
	rRA   = 31
)

// --- tiny two-pass assembler ------------------------------------

const (
	fxBranch = iota
	fxJump
	fxStub
	fxLaHi
	fxLaLo
	fxLaCodeHi
	fxLaCodeLo
)

type gateFix struct {
	idx  int
	kind int
	base uint32 // instruction word with the variable field zeroed
	lib  int
	fn   int
	lab  string
}

type gateAsm struct {
	words      []uint32
	labels     map[string]int
	fixups     []gateFix
	laPairs    [][2]int // word indexes of laRel lui/addiu pairs, for PRX relocs
	jalRelIdx  []int    // word indexes of jalRel jumps, for PRX relocs
	stubJalIdx []int    // word indexes of jalStub jumps, for PRX relocs
}

func newGateAsm() *gateAsm { return &gateAsm{labels: map[string]int{}} }

func (a *gateAsm) emit(w uint32) int {
	a.words = append(a.words, w)
	return len(a.words) - 1
}

func (a *gateAsm) here(name string) { a.labels[name] = len(a.words) }

func itype(op, rs, rt int, imm int) uint32 {
	return uint32(op)<<26 | uint32(rs)<<21 | uint32(rt)<<16 | uint32(uint16(imm))
}

func rtype(rs, rt, rd, sa, fn int) uint32 {
	return uint32(rs)<<21 | uint32(rt)<<16 | uint32(rd)<<11 | uint32(sa)<<6 | uint32(fn)
}

func (a *gateAsm) nop() { a.emit(0) }
func (a *gateAsm) addiu(rt, rs, imm int) {
	a.emit(itype(0x09, rs, rt, imm))
}
func (a *gateAsm) ori(rt, rs, imm int)  { a.emit(itype(0x0D, rs, rt, imm)) }
func (a *gateAsm) andi(rt, rs, imm int) { a.emit(itype(0x0C, rs, rt, imm)) }
func (a *gateAsm) lui(rt, imm int)      { a.emit(itype(0x0F, 0, rt, imm)) }
func (a *gateAsm) lw(rt, off, rs int)   { a.emit(itype(0x23, rs, rt, off)) }
func (a *gateAsm) sw(rt, off, rs int)   { a.emit(itype(0x2B, rs, rt, off)) }
func (a *gateAsm) lb(rt, off, rs int)   { a.emit(itype(0x20, rs, rt, off)) }
func (a *gateAsm) lbu(rt, off, rs int)  { a.emit(itype(0x24, rs, rt, off)) }
func (a *gateAsm) sb(rt, off, rs int)   { a.emit(itype(0x28, rs, rt, off)) }
func (a *gateAsm) addu(rd, rs, rt int)  { a.emit(rtype(rs, rt, rd, 0, 0x21)) }
func (a *gateAsm) subu(rd, rs, rt int)  { a.emit(rtype(rs, rt, rd, 0, 0x23)) }
func (a *gateAsm) and(rd, rs, rt int)   { a.emit(rtype(rs, rt, rd, 0, 0x24)) }
func (a *gateAsm) or(rd, rs, rt int)    { a.emit(rtype(rs, rt, rd, 0, 0x25)) }
func (a *gateAsm) slt(rd, rs, rt int)   { a.emit(rtype(rs, rt, rd, 0, 0x2A)) }
func (a *gateAsm) sll(rd, rt, sa int)   { a.emit(rtype(0, rt, rd, sa, 0x00)) }
func (a *gateAsm) srl(rd, rt, sa int)   { a.emit(rtype(0, rt, rd, sa, 0x02)) }
func (a *gateAsm) jr(rs int)            { a.emit(rtype(rs, 0, 0, 0, 0x08)) }
func (a *gateAsm) mflo(rd int)          { a.emit(rtype(0, 0, rd, 0, 0x12)) }
func (a *gateAsm) mult(rs, rt int)      { a.emit(rtype(rs, rt, 0, 0, 0x18)) }
func (a *gateAsm) divu(rs, rt int)      { a.emit(rtype(rs, rt, 0, 0, 0x1B)) }
func (a *gateAsm) move(rd, rs int)      { a.addu(rd, rs, rZero) }

// li loads a signed 32 bit constant in one or two words.
func (a *gateAsm) li(rd int, v int32) {
	if v >= -32768 && v <= 32767 {
		a.addiu(rd, rZero, int(v))
		return
	}
	a.lui(rd, int(uint32(v)>>16))
	a.ori(rd, rd, int(uint32(v)&0xFFFF))
}

func (a *gateAsm) branch(op, rs, rt int, label string) {
	idx := a.emit(itype(op, rs, rt, 0))
	a.fixups = append(a.fixups, gateFix{idx: idx, kind: fxBranch, base: itype(op, rs, rt, 0), lab: label})
}

func (a *gateAsm) beq(rs, rt int, label string) { a.branch(0x04, rs, rt, label) }
func (a *gateAsm) bne(rs, rt int, label string) { a.branch(0x05, rs, rt, label) }
func (a *gateAsm) blez(rs int, label string)    { a.branch(0x06, rs, 0, label) }
func (a *gateAsm) bgtz(rs int, label string)    { a.branch(0x07, rs, 0, label) }
func (a *gateAsm) bltz(rs int, label string)    { a.branch(0x01, rs, 0, label) }
func (a *gateAsm) bgez(rs int, label string)    { a.branch(0x01, rs, 1, label) }

func (a *gateAsm) jump(op int, label string) {
	idx := a.emit(uint32(op) << 26)
	a.fixups = append(a.fixups, gateFix{idx: idx, kind: fxJump, base: uint32(op) << 26, lab: label})
}

func (a *gateAsm) j(label string)   { a.jump(0x02, label) }
func (a *gateAsm) jal(label string) { a.jump(0x03, label) }

func (a *gateAsm) jalStub(lib, fn int) {
	idx := a.emit(0x03 << 26)
	a.fixups = append(a.fixups, gateFix{idx: idx, kind: fxStub, base: 0x03 << 26, lib: lib, fn: fn})
	a.stubJalIdx = append(a.stubJalIdx, idx)
}

// la loads a rodata item's address (lui + addiu pair).
func (a *gateAsm) la(rd int, roLabel string) {
	hi := a.emit(itype(0x0F, 0, rd, 0))
	a.fixups = append(a.fixups, gateFix{idx: hi, kind: fxLaHi, base: itype(0x0F, 0, rd, 0), lab: roLabel})
	lo := a.emit(itype(0x09, rd, rd, 0))
	a.fixups = append(a.fixups, gateFix{idx: lo, kind: fxLaLo, base: itype(0x09, rd, rd, 0), lab: roLabel})
}

// laCode loads a code label's address (lui + addiu pair).
func (a *gateAsm) laCode(rd int, codeLabel string) {
	hi := a.emit(itype(0x0F, 0, rd, 0))
	a.fixups = append(a.fixups, gateFix{idx: hi, kind: fxLaCodeHi, base: itype(0x0F, 0, rd, 0), lab: codeLabel})
	lo := a.emit(itype(0x09, rd, rd, 0))
	a.fixups = append(a.fixups, gateFix{idx: lo, kind: fxLaCodeLo, base: itype(0x09, rd, rd, 0), lab: codeLabel})
}

// laRel is la for a relocatable image: the pair is also recorded
// so the PRX builder can emit its HI16/LO16 relocations.
func (a *gateAsm) laRel(rd int, roLabel string) {
	hi := len(a.words)
	a.la(rd, roLabel)
	a.laPairs = append(a.laPairs, [2]int{hi, hi + 1})
}

// jalRel jumps to a code label and records the word so the PRX
// builder can emit its R_MIPS_26 relocation.
func (a *gateAsm) jalRel(codeLabel string) {
	a.jalRelIdx = append(a.jalRelIdx, len(a.words))
	a.jal(codeLabel)
}

// VFPU encoders for the gate (a) quad ops, from the documented
// field layout: opcode 0x18 for VV arith with G in bits 23..25,
// the size enum split across bits 7 and 15, operands as 7 bit
// fields. A quad register field is (matrix row index << 2) for
// matrix 0 rows.
func lvq(row, rs, off int) uint32 {
	return 0x36<<26 | uint32(rs)<<21 | uint32(row)<<16 | uint32((off>>2)&0x3FFF)<<2
}

func svq(row, rs, off int) uint32 {
	return 0x3E<<26 | uint32(rs)<<21 | uint32(row)<<16 | uint32((off>>2)&0x3FFF)<<2
}

// vaddq encodes vadd.q with raw 7 bit operand fields (the
// corpus layout: field 0 is C000, field 1 is C010, field 4
// is C100). An earlier form scaled the fields by 4, which
// matched the PSP-2 layout the corpus later refuted.
func vaddq(vd, vs, vt int) uint32 {
	return 0x60000000 | uint32(vt)<<16 | 1<<15 | uint32(vs)<<8 | 1<<7 | uint32(vd)
}

// --- ELF / PRX builder -------------------------------------------

type gateLib struct {
	name string
	nids []uint32
}

type gateBuilder struct {
	name     string
	libs     []gateLib
	code     *gateAsm
	roOrder  []string
	roData   map[string][]byte
	entryLab string // code label of the entry point; "" = first word

	extraRelocs [][2]uint32 // extra (segment offset, r_info) pairs for buildPRX
}

func newGateBuilder(name string, libs []gateLib) *gateBuilder {
	return &gateBuilder{name: name, libs: libs, code: newGateAsm(), roData: map[string][]byte{}}
}

func (b *gateBuilder) ro(label string, data []byte) {
	if _, dup := b.roData[label]; dup {
		panic("duplicate rodata label " + label)
	}
	b.roOrder = append(b.roOrder, label)
	b.roData[label] = data
}

func (b *gateBuilder) roStr(label, s string) { b.ro(label, append([]byte(s), 0)) }

func (b *gateBuilder) roWords(label string, words ...uint32) {
	buf := make([]byte, 4*len(words))
	for i, w := range words {
		binary.LittleEndian.PutUint32(buf[i*4:], w)
	}
	b.ro(label, buf)
}

const (
	gateSegBase = 0x08804000 // ET_EXEC link base, inside the user partition
	gateTextOff = 0x40       // segment offset of .text
)

// layout computes every address a build needs, given whether the
// image links at gateSegBase (ET_EXEC) or at 0 (PRX).
func (b *gateBuilder) layout(relocatable bool) (link uint32, stubAddr func(lib, fn int) uint32, roAddr map[string]uint32, infoOff, segSize int) {
	var base uint32
	if !relocatable {
		base = gateSegBase
	}
	textWords := len(b.code.words)
	stubsOff := gateTextOff + 4*textWords
	totalFuncs := 0
	for _, l := range b.libs {
		totalFuncs += len(l.nids)
	}
	roSegOff := (stubsOff + 8*totalFuncs + 15) &^ 15

	// Rodata order: user items, then per lib the name and NID
	// table, then the stub tables, then the module info.
	off := roSegOff
	roAddr = map[string]uint32{}
	for _, label := range b.roOrder {
		roAddr[label] = base + uint32(off)
		off += len(b.roData[label])
		off = (off + 3) &^ 3
	}
	libNameOff := make([]int, len(b.libs))
	libNidOff := make([]int, len(b.libs))
	for i, l := range b.libs {
		libNameOff[i] = off
		off += len(l.name) + 1
		off = (off + 3) &^ 3
		libNidOff[i] = off
		off += 4 * len(l.nids)
	}
	stubTablesOff := off
	off += 20 * len(b.libs)
	infoOff = off
	off += 0x40
	segSize = off

	stubAddr = func(lib, fn int) uint32 {
		n := 0
		for i := 0; i < lib; i++ {
			n += len(b.libs[i].nids)
		}
		return base + uint32(stubsOff) + uint32(8*(n+fn))
	}
	// Stash the table offsets in the map under synthetic keys.
	roAddr["\x00stubTables"] = base + uint32(stubTablesOff)
	for i := range b.libs {
		roAddr["\x00name"+string(rune(i))] = base + uint32(libNameOff[i])
		roAddr["\x00nids"+string(rune(i))] = base + uint32(libNidOff[i])
	}
	return base, stubAddr, roAddr, infoOff, segSize
}

// build renders the module given the link base and an optional
// relocation list (segment offsets and r_info words).
func (b *gateBuilder) build(relocatable bool, relocs [][2]uint32) []byte {
	link, stubAddr, roAddr, infoOff, segSize := b.layout(relocatable)
	textAddr := func(idx int) uint32 { return link + gateTextOff + uint32(4*idx) }

	// Patch code fixups.
	words := append([]uint32{}, b.code.words...)
	for _, fx := range b.code.fixups {
		switch fx.kind {
		case fxBranch:
			target, ok := b.code.labels[fx.lab]
			if !ok {
				panic("unknown code label " + fx.lab)
			}
			off := target - (fx.idx + 1)
			words[fx.idx] = fx.base | uint32(uint16(off))
		case fxJump:
			target, ok := b.code.labels[fx.lab]
			if !ok {
				panic("unknown code label " + fx.lab)
			}
			words[fx.idx] = fx.base | ((textAddr(target) >> 2) & 0x3FFFFFF)
		case fxStub:
			words[fx.idx] = fx.base | ((stubAddr(fx.lib, fx.fn) >> 2) & 0x3FFFFFF)
		case fxLaHi:
			addr, ok := roAddr[fx.lab]
			if !ok {
				panic("unknown rodata label " + fx.lab)
			}
			words[fx.idx] = fx.base | (((addr + 0x8000) >> 16) & 0xFFFF)
		case fxLaLo:
			addr := roAddr[fx.lab]
			hi := (addr + 0x8000) >> 16
			lo := addr - (hi << 16)
			words[fx.idx] = fx.base | uint32(uint16(int32(lo)))
		case fxLaCodeHi:
			target, ok := b.code.labels[fx.lab]
			if !ok {
				panic("unknown code label " + fx.lab)
			}
			addr := textAddr(target)
			words[fx.idx] = fx.base | (((addr + 0x8000) >> 16) & 0xFFFF)
		case fxLaCodeLo:
			addr := textAddr(b.code.labels[fx.lab])
			hi := (addr + 0x8000) >> 16
			lo := addr - (hi << 16)
			words[fx.idx] = fx.base | uint32(uint16(int32(lo)))
		}
	}

	content := make([]byte, segSize)
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(content[off:], v) }
	for i, w := range words {
		put32(gateTextOff+4*i, w)
	}
	stubsOff := gateTextOff + 4*len(words)
	n := 0
	for _, l := range b.libs {
		for range l.nids {
			put32(stubsOff+8*n, 0x03E00008) // JR $ra
			put32(stubsOff+8*n+4, 0)        // delay slot: the kernel patches the SYSCALL here
			n++
		}
	}
	// User rodata items.
	for _, label := range b.roOrder {
		copy(content[int(roAddr[label]-link):], b.roData[label])
	}
	// Lib names, NID tables, stub tables.
	stubTablesAddr := roAddr["\x00stubTables"]
	for i, l := range b.libs {
		nameAddr := roAddr["\x00name"+string(rune(i))]
		nidAddr := roAddr["\x00nids"+string(rune(i))]
		copy(content[int(nameAddr-link):], l.name)
		for j, nid := range l.nids {
			put32(int(nidAddr-link)+4*j, nid)
		}
		tbl := int(stubTablesAddr-link) + 20*i
		put32(tbl+0, nameAddr)
		binary.LittleEndian.PutUint16(content[tbl+10:], uint16(len(l.nids)))
		content[tbl+8] = 5 // entrySize in words
		put32(tbl+12, nidAddr)
		put32(tbl+16, stubAddr(i, 0))
	}
	// Module info (SceModuleInfo).
	put32(infoOff+0x00, 0) // attribute: user module
	copy(content[infoOff+4:infoOff+31], b.name)
	put32(infoOff+0x20, link+0x900) // gp in module space
	put32(infoOff+0x2C, stubTablesAddr)
	put32(infoOff+0x30, stubTablesAddr+uint32(20*len(b.libs)))

	entry := textAddr(0)
	if b.entryLab != "" {
		entry = textAddr(b.code.labels[b.entryLab])
	}

	// Sections and string table.
	shstr := "\x00.text\x00.sceStub.text\x00.rodata.sceModuleInfo\x00.rel.text\x00.shstrtab\x00"
	nameText := strings.Index(shstr, ".text")
	nameStubT := strings.Index(shstr, ".sceStub.text")
	nameInfo := strings.Index(shstr, ".rodata.sceModuleInfo")
	nameRel := strings.Index(shstr, ".rel.text")
	nameShstr := strings.Index(shstr, ".shstrtab")

	const segFileOff = 0x1000
	shstrOff := segFileOff + segSize
	relOff := (shstrOff + len(shstr) + 3) &^ 3
	shnum := 5
	if len(relocs) > 0 {
		shnum = 6
	}
	shoff := (relOff + 8*len(relocs) + 3) &^ 3
	out := make([]byte, shoff+shnum*40)
	typ := uint16(2) // ET_EXEC
	if relocatable {
		typ = 0xFFA1 // ET_SCE_REL (PRX)
	}
	out[0], out[1], out[2], out[3] = 0x7F, 'E', 'L', 'F'
	out[4], out[5] = 1, 1
	binary.LittleEndian.PutUint16(out[16:], typ)
	binary.LittleEndian.PutUint16(out[18:], 8) // EM_MIPS
	binary.LittleEndian.PutUint32(out[20:], 1)
	binary.LittleEndian.PutUint32(out[24:], entry)
	binary.LittleEndian.PutUint32(out[28:], 52)
	binary.LittleEndian.PutUint32(out[32:], uint32(shoff))
	binary.LittleEndian.PutUint16(out[40:], 52)
	binary.LittleEndian.PutUint16(out[42:], 32)
	binary.LittleEndian.PutUint16(out[44:], 1)
	binary.LittleEndian.PutUint16(out[46:], 40)
	binary.LittleEndian.PutUint16(out[48:], uint16(shnum))
	binary.LittleEndian.PutUint16(out[50:], uint16(shnum-1))
	ph := out[52:]
	binary.LittleEndian.PutUint32(ph[0:], 1) // PT_LOAD
	binary.LittleEndian.PutUint32(ph[4:], segFileOff)
	binary.LittleEndian.PutUint32(ph[8:], link)
	binary.LittleEndian.PutUint32(ph[12:], link)
	binary.LittleEndian.PutUint32(ph[16:], uint32(segSize))
	binary.LittleEndian.PutUint32(ph[20:], uint32(segSize))
	binary.LittleEndian.PutUint32(ph[24:], 7)
	binary.LittleEndian.PutUint32(ph[28:], 0x1000)
	copy(out[segFileOff:], content)
	copy(out[shstrOff:], shstr)
	for i, r := range relocs {
		binary.LittleEndian.PutUint32(out[relOff+i*8:], r[0])
		binary.LittleEndian.PutUint32(out[relOff+i*8+4:], r[1])
	}
	sh := func(i int, name, typ, addr, off, size, entsize uint32) {
		bb := out[shoff+i*40:]
		binary.LittleEndian.PutUint32(bb[0:], name)
		binary.LittleEndian.PutUint32(bb[4:], typ)
		binary.LittleEndian.PutUint32(bb[12:], addr)
		binary.LittleEndian.PutUint32(bb[16:], off)
		binary.LittleEndian.PutUint32(bb[20:], size)
		binary.LittleEndian.PutUint32(bb[36:], entsize)
	}
	sh(1, uint32(nameText), 1, link+gateTextOff, segFileOff+gateTextOff, uint32(4*len(words)), 0)
	sh(2, uint32(nameStubT), 1, link+uint32(stubsOff), uint32(segFileOff+stubsOff), uint32(8*n), 0)
	sh(3, uint32(nameInfo), 1, link+uint32(infoOff), uint32(segFileOff+infoOff), 0x40, 0)
	if len(relocs) > 0 {
		sh(4, uint32(nameRel), 0x700000A0, 0, uint32(relOff), uint32(8*len(relocs)), 8)
		sh(5, uint32(nameShstr), 3, 0, uint32(shstrOff), uint32(len(shstr)), 0)
	} else {
		sh(4, uint32(nameShstr), 3, 0, uint32(shstrOff), uint32(len(shstr)), 0)
	}
	return out
}

// buildPRX renders the module as a relocatable PRX (ET_SCE_REL,
// link base 0), emitting the relocations a toolchain would: the
// module info pointer fields, each stub table's pointer fields,
// every recorded la pair (HI16/LO16) and jal (R_MIPS_26), plus the
// builder's extraRelocs.
func (b *gateBuilder) buildPRX() []byte {
	_, _, roAddr, infoOff, _ := b.layout(true)
	const (
		r32   = 2
		r26   = 4
		rHi16 = 5
		rLo16 = 6
	)
	relocs := [][2]uint32{
		{uint32(infoOff) + 0x20, r32}, // gp
		{uint32(infoOff) + 0x2C, r32}, // stubTop
		{uint32(infoOff) + 0x30, r32}, // stubEnd
	}
	stubTablesOff := int(roAddr["\x00stubTables"])
	for i := range b.libs {
		tbl := uint32(stubTablesOff + 20*i)
		relocs = append(relocs,
			[2]uint32{tbl + 0, r32},  // name pointer
			[2]uint32{tbl + 12, r32}, // NID table pointer
			[2]uint32{tbl + 16, r32}) // func table pointer
	}
	for _, pair := range b.code.laPairs {
		relocs = append(relocs,
			[2]uint32{gateTextOff + uint32(4*pair[0]), rHi16},
			[2]uint32{gateTextOff + uint32(4*pair[1]), rLo16})
	}
	for _, idx := range b.code.jalRelIdx {
		relocs = append(relocs, [2]uint32{gateTextOff + uint32(4*idx), r26})
	}
	for _, idx := range b.code.stubJalIdx {
		relocs = append(relocs, [2]uint32{gateTextOff + uint32(4*idx), r26})
	}
	relocs = append(relocs, b.extraRelocs...)
	return b.build(true, relocs)
}

// roSegOff returns the segment offset of a rodata label in a
// relocatable (link base 0) layout.
func (b *gateBuilder) roSegOff(label string) uint32 {
	_, _, roAddr, _, _ := b.layout(true)
	addr, ok := roAddr[label]
	if !ok {
		panic("unknown rodata label " + label)
	}
	return addr
}

// runGate boots a built image on a fresh machine and runs frames
// until the guest exits, returning the machine and frames used.
func runGate(t *testing.T, img []byte, maxFrames int) (*Machine, int) {
	t.Helper()
	m := New()
	if err := m.LoadExecutable(img); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	for f := 1; f <= maxFrames; f++ {
		m.RunFrame()
		if m.Exited() {
			return m, f
		}
	}
	t.Fatalf("guest did not exit within %d frames; console so far: %q", maxFrames, m.ConsoleOutput())
	return m, maxFrames
}

// Library indexes shared by the gate programs.
var (
	libSysMem   = gateLib{"SysMemUserForUser", []uint32{0x13A5ABEF}}                                                // sceKernelPrintf
	libThread   = gateLib{"ThreadManForUser", nil}                                                                  // filled per gate
	libLoadExec = gateLib{"LoadExecForUser", []uint32{0x05572A5F}}                                                  // sceKernelExitGame
	libIO       = gateLib{"IoFileMgrForUser", []uint32{0x109F50BC, 0x6A638D83, 0x42EC03AC, 0x810C4BC3, 0xACE946E8}} // Open Read Write Close Getstat
	libDisplay  = gateLib{"sceDisplay", []uint32{0xEEDA2E54, 0x9C6EAAD7}}                                           // WaitVblankStart, GetVcount
)

// --- gate (a): hello, printf + IoWrite + a VFPU quad add --------

func TestPSP3GateHello(t *testing.T) {
	b := newGateBuilder("GATEA", []gateLib{libSysMem, libIO, libLoadExec})
	b.roStr("fmtA", "vfpu=%d,%d,%d,%d\n")
	b.roStr("msgA", "hello io\n")
	// Float bit patterns: A = 1.5, 2.25, -3.0, 4.0; B = 0.5, 0.75, 3.0, -4.0.
	b.roWords("vecA", 0x3FC00000, 0x40100000, 0xC0400000, 0x40800000)
	b.roWords("vecB", 0x3F000000, 0x3F400000, 0x40400000, 0xC0800000)
	b.roWords("vecOut", 0, 0, 0, 0)
	a := b.code
	a.addiu(rSP, rSP, -64)
	a.la(rT0, "vecA")
	a.emit(lvq(0, rT0, 0))
	a.la(rT0, "vecB")
	a.emit(lvq(1, rT0, 0))
	a.emit(vaddq(0, 0, 1))
	a.la(rT0, "vecOut")
	a.emit(svq(0, rT0, 0))
	a.lw(rA1, 0, rT0)
	a.lw(rA2, 4, rT0)
	a.lw(rA3, 8, rT0)
	a.lw(rT1, 12, rT0)
	a.sw(rT1, 16, rSP) // fourth printf argument rides the stack
	a.la(rA0, "fmtA")
	a.jalStub(0, 0) // sceKernelPrintf
	a.nop()
	// sceIoWrite(1, msgA, len)
	a.li(rA0, 1)
	a.la(rA1, "msgA")
	a.li(rA2, 9)
	a.jalStub(1, 2)
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0) // sceKernelExitGame
	a.nop()

	m, _ := runGate(t, b.build(false, nil), 10)
	// Sums are 2.0, 3.0, +0.0, +0.0 printed as their integer bit patterns.
	want := "vfpu=1073741824,1077936128,0,0\nhello io\n"
	if got := m.ConsoleOutput(); got != want {
		t.Fatalf("console = %q, want %q", got, want)
	}
}

// --- gate (b): threads + semaphore interleaving ----------------

func buildGateB() []byte {
	tm := gateLib{"ThreadManForUser", []uint32{
		0xD6DA4BA1, // 0 CreateSema
		0x4E3A1105, // 1 WaitSema
		0x3F53E640, // 2 SignalSema
		0x446D8DE6, // 3 CreateThread
		0xF475845D, // 4 StartThread
		0x278C0DF5, // 5 WaitThreadEnd
	}}
	b := newGateBuilder("GATEB", []gateLib{tm, libSysMem, libLoadExec})
	b.roStr("fmtStart", "start\n")
	b.roStr("fmtDone", "count=%d sum=%d len=%d\n")
	b.roStr("semName", "s")
	b.roStr("hiName", "whi")
	b.roStr("loName", "wlo")
	b.roWords("counter", 0)
	b.roWords("traceIdx", 0)
	b.roWords("semaSlot", 0)
	b.roWords("markerHi", 1)
	b.roWords("markerLo", 2)
	b.ro("trace", make([]byte, 40))
	a := b.code
	// main
	a.addiu(rSP, rSP, -64)
	a.la(rA0, "fmtStart")
	a.jalStub(1, 0)
	a.nop()
	// CreateSema("s", 0, 1, 1)
	a.la(rA0, "semName")
	a.li(rA1, 0)
	a.li(rA2, 1)
	a.li(rA3, 1)
	a.jalStub(0, 0)
	a.nop()
	a.la(rT0, "semaSlot")
	a.sw(rV0, 0, rT0)
	// CreateThread("whi", worker, 0x18, 0x4000, attr 0, opt 0)
	a.la(rA0, "hiName")
	a.laCode(rA1, "worker")
	a.li(rA2, 0x18)
	a.li(rA3, 0x4000)
	a.sw(rZero, 16, rSP)
	a.sw(rZero, 20, rSP)
	a.jalStub(0, 3)
	a.nop()
	a.move(rS7, rV0)
	// CreateThread("wlo", worker, 0x28, 0x4000, 0, 0)
	a.la(rA0, "loName")
	a.laCode(rA1, "worker")
	a.li(rA2, 0x28)
	a.li(rA3, 0x4000)
	a.sw(rZero, 16, rSP)
	a.sw(rZero, 20, rSP)
	a.jalStub(0, 3)
	a.nop()
	a.sw(rV0, 40, rSP)
	// StartThread(hi, 4, &markerHi); the higher priority worker
	// preempts main here and runs to completion first (see the
	// gate comment in the test).
	a.move(rA0, rS7)
	a.li(rA1, 4)
	a.la(rA2, "markerHi")
	a.jalStub(0, 4)
	a.nop()
	// StartThread(lo, 4, &markerLo)
	a.lw(rA0, 40, rSP)
	a.li(rA1, 4)
	a.la(rA2, "markerLo")
	a.jalStub(0, 4)
	a.nop()
	// WaitThreadEnd(hi), WaitThreadEnd(lo)
	a.move(rA0, rS7)
	a.li(rA1, 0)
	a.jalStub(0, 5)
	a.nop()
	a.lw(rA0, 40, rSP)
	a.li(rA1, 0)
	a.jalStub(0, 5)
	a.nop()
	// sum = Sigma trace[i] * (i+1); a1 = counter, a2 = sum, a3 = len.
	a.la(rT0, "counter")
	a.lw(rS0, 0, rT0)
	a.la(rT5, "trace")
	a.la(rT0, "traceIdx")
	a.lw(rS3, 0, rT0)
	a.li(rS1, 0) // sum
	a.li(rS2, 0) // i
	a.here("sumLoop")
	a.slt(rT0, rS2, rS3)
	a.beq(rT0, rZero, "sumDone")
	a.nop()
	a.sll(rT0, rS2, 2)
	a.addu(rT0, rT5, rT0)
	a.lw(rT1, 0, rT0)
	a.addiu(rT2, rS2, 1)
	a.mult(rT1, rT2)
	a.mflo(rT3)
	a.addu(rS1, rS1, rT3)
	a.addiu(rS2, rS2, 1)
	a.j("sumLoop")
	a.nop()
	a.here("sumDone")
	a.move(rA1, rS0)
	a.move(rA2, rS1)
	a.move(rA3, rS3)
	a.la(rA0, "fmtDone")
	a.jalStub(1, 0)
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0)
	a.nop()
	// worker: a1 points at the marker word delivered by StartThread.
	a.here("worker")
	a.move(rS2, rRA)  // stub calls clobber $ra; keep the exit address
	a.lw(rS0, 0, rA1) // marker (1 for hi, 2 for lo)
	a.li(rS1, 0)      // chunk counter
	a.here("chunk")
	// WaitSema(sema, 1, NULL)
	a.la(rT0, "semaSlot")
	a.lw(rA0, 0, rT0)
	a.li(rA1, 1)
	a.li(rA2, 0)
	a.jalStub(0, 1)
	a.nop()
	// counter += 200
	a.la(rT0, "counter")
	a.lw(rT1, 0, rT0)
	a.addiu(rT1, rT1, 200)
	a.sw(rT1, 0, rT0)
	// trace[traceIdx++] = marker
	a.la(rT0, "traceIdx")
	a.lw(rT1, 0, rT0)
	a.la(rT2, "trace")
	a.sll(rT3, rT1, 2)
	a.addu(rT2, rT2, rT3)
	a.sw(rS0, 0, rT2)
	a.addiu(rT1, rT1, 1)
	a.sw(rT1, 0, rT0)
	// SignalSema(sema, 1)
	a.la(rT0, "semaSlot")
	a.lw(rA0, 0, rT0)
	a.li(rA1, 1)
	a.jalStub(0, 2)
	a.nop()
	a.addiu(rS1, rS1, 1)
	a.li(rT0, 5)
	a.bne(rS1, rT0, "chunk")
	a.nop()
	a.li(rV0, 0)
	a.move(rRA, rS2)
	a.jr(rRA) // return through the $ra = 0 trampoline exits the thread
	a.nop()
	return b.build(false, nil)
}

func TestPSP3GateThreads(t *testing.T) {
	// Scheduler model trace: StartThread(hi, priority 0x18)
	// unblocks a thread that outranks main (0x20), so the
	// post-syscall reschedule runs hi first. Hi never blocks (the
	// semaphore is free at each take) and finishes all 5 chunks,
	// appending 1 five times. Main then starts lo (0x28), blocks in
	// WaitThreadEnd(lo), and lo appends 2 five times.
	// count = 10 chunks * 200 = 2000.
	// sum = 1*(1+2+3+4+5) + 2*(6+7+8+9+10) = 15 + 80 = 95.
	m, _ := runGate(t, buildGateB(), 10)
	want := "start\ncount=2000 sum=95 len=10\n"
	if got := m.ConsoleOutput(); got != want {
		t.Fatalf("console = %q, want %q", got, want)
	}
}

// --- gate (c): delay + vblank wake order -------------------------

func TestPSP3GateDelayVblank(t *testing.T) {
	tm := gateLib{"ThreadManForUser", []uint32{0xCEADEB47}} // DelayThread
	b := newGateBuilder("GATEC", []gateLib{tm, libDisplay, libSysMem, libLoadExec})
	b.roStr("fT0", "t0\n")
	b.roStr("fDelayed", "delayed\n")
	b.roStr("fVblank", "vblank %d\n")
	a := b.code
	a.la(rA0, "fT0")
	a.jalStub(2, 0)
	a.nop()
	a.li(rA0, 1000) // 1000 microseconds
	a.jalStub(0, 0)
	a.nop()
	a.la(rA0, "fDelayed")
	a.jalStub(2, 0)
	a.nop()
	a.jalStub(1, 0) // WaitVblankStart
	a.nop()
	a.jalStub(1, 1) // GetVcount
	a.nop()
	a.move(rA1, rV0)
	a.la(rA0, "fVblank")
	a.jalStub(2, 0)
	a.nop()
	a.jalStub(1, 0) // WaitVblankStart again
	a.nop()
	a.jalStub(1, 1)
	a.nop()
	a.move(rA1, rV0)
	a.la(rA0, "fVblank")
	a.jalStub(2, 0)
	a.nop()
	a.li(rA0, 0)
	a.jalStub(3, 0)
	a.nop()

	m := New()
	if err := m.LoadExecutable(b.build(false, nil)); err != nil {
		t.Fatal(err)
	}
	m.RunFrame()
	if m.Exited() {
		t.Fatal("exited during frame 1")
	}
	if got := m.ConsoleOutput(); got != "t0\ndelayed\n" {
		t.Fatalf("after frame 1 console = %q", got)
	}
	m.RunFrame()
	if m.Exited() {
		t.Fatal("exited during frame 2")
	}
	if got := m.ConsoleOutput(); got != "t0\ndelayed\nvblank 1\n" {
		t.Fatalf("after frame 2 console = %q", got)
	}
	m.RunFrame()
	if !m.Exited() {
		t.Fatal("not exited after frame 3")
	}
	if got := m.ConsoleOutput(); got != "t0\ndelayed\nvblank 1\nvblank 2\n" {
		t.Fatalf("final console = %q", got)
	}
}

// --- gate (d): UMD file IO through the ISO9660 VFS ---------------

// gateDataByte is the DATA.BIN fixture pattern for gate (d).
func gateDataByte(i int) byte { return byte((i*7 + 3) & 0xFF) }

// buildGateISO writes a minimal ISO9660 image holding one root
// file, DATA.BIN (256 pattern bytes), and returns its path.
func buildGateISO(t *testing.T) string {
	t.Helper()
	const sec = 2048
	data := make([]byte, 256)
	for i := range data {
		data[i] = gateDataByte(i)
	}
	img := make([]byte, 24*sec)
	pvd := img[16*sec:]
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	for i := 40; i < 72; i++ {
		pvd[i] = ' '
	}
	copy(pvd[40:], "PSP_GATE_UMD")
	both32(pvd[80:], 24)
	both16(pvd[120:], 1)
	both16(pvd[124:], 1)
	both16(pvd[128:], sec)
	both32(pvd[132:], 10)
	binary.LittleEndian.PutUint32(pvd[140:], 19)
	binary.BigEndian.PutUint32(pvd[148:], 19)
	copy(pvd[156:], dirRecord([]byte{0}, 20, sec, true))
	root := img[20*sec:]
	off := 0
	for _, r := range [][]byte{
		dirRecord([]byte{0}, 20, sec, true),
		dirRecord([]byte{1}, 20, sec, true),
		dirRecord([]byte("DATA.BIN;1"), 23, 256, false),
	} {
		copy(root[off:], r)
		off += len(r)
	}
	copy(img[23*sec:], data)
	path := filepath.Join(t.TempDir(), "gate.iso")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPSP3GateUMD(t *testing.T) {
	b := newGateBuilder("GATED", []gateLib{libIO, libSysMem, libLoadExec})
	b.roStr("pathD", "umd0:/DATA.BIN")
	b.roStr("fmtD", "umd size=%d sum=%d\n")
	b.ro("statD", make([]byte, 96))
	b.ro("bufD", make([]byte, 256))
	a := b.code
	a.addiu(rSP, rSP, -64)
	a.la(rA0, "pathD")
	a.li(rA1, 1) // PSP_O_RDONLY
	a.li(rA2, 0)
	a.jalStub(0, 0) // sceIoOpen
	a.nop()
	a.move(rS0, rV0) // fd
	a.la(rA0, "pathD")
	a.la(rA1, "statD")
	a.jalStub(0, 4) // sceIoGetstat
	a.nop()
	a.la(rT0, "statD")
	a.lw(rS1, 8, rT0) // st_size
	a.move(rA0, rS0)
	a.la(rA1, "bufD")
	a.li(rA2, 256)
	a.jalStub(0, 1) // sceIoRead
	a.nop()
	a.move(rA0, rS0)
	a.jalStub(0, 3) // sceIoClose
	a.nop()
	// sum the bytes the guest read back through the ISO.
	a.li(rS2, 0)
	a.li(rS3, 0)
	a.la(rS4, "bufD")
	a.here("sumLoop")
	a.lbu(rT0, 0, rS4)
	a.addu(rS2, rS2, rT0)
	a.addiu(rS4, rS4, 1)
	a.addiu(rS3, rS3, 1)
	a.li(rT0, 256)
	a.bne(rS3, rT0, "sumLoop")
	a.nop()
	a.la(rA0, "fmtD")
	a.move(rA1, rS1)
	a.move(rA2, rS2)
	a.jalStub(1, 0) // sceKernelPrintf
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0) // sceKernelExitGame
	a.nop()

	m := New()
	if err := m.LoadISO(buildGateISO(t)); err != nil {
		t.Fatalf("LoadISO: %v", err)
	}
	if err := m.LoadExecutable(b.build(false, nil)); err != nil {
		t.Fatalf("LoadExecutable: %v", err)
	}
	for f := 0; f < 10 && !m.Exited(); f++ {
		m.RunFrame()
	}
	if !m.Exited() {
		t.Fatalf("guest did not exit; console %q", m.ConsoleOutput())
	}
	sum := 0
	for i := 0; i < 256; i++ {
		sum += int(gateDataByte(i))
	}
	want := fmt.Sprintf("umd size=256 sum=%d\n", sum)
	if got := m.ConsoleOutput(); got != want {
		t.Fatalf("console = %q, want %q", got, want)
	}
}

// --- gate (e): relocatable PRX -----------------------------------

func TestPSP3GatePRX(t *testing.T) {
	b := newGateBuilder("GATEE", []gateLib{libSysMem, libLoadExec})
	b.roStr("msgE", "relocated hello")
	a := b.code
	a.addiu(rSP, rSP, -64)
	a.jalRel("helper") // v0 = 77, through an R_MIPS_26 relocation
	a.nop()
	a.move(rS0, rV0)
	a.laRel(rA0, "fmtE")
	a.laRel(rA1, "ptrE")
	a.lw(rA1, 0, rA1) // the message address, via an R_MIPS_32 word
	a.move(rA2, rS0)
	a.jalStub(0, 0) // sceKernelPrintf
	a.nop()
	a.li(rA0, 0)
	a.jalStub(1, 0) // sceKernelExitGame
	a.nop()
	a.here("helper")
	a.li(rV0, 77)
	a.jr(rRA)
	a.nop()
	// The message offset is fixed once the code above is emitted
	// and msgE is the first rodata item; later items cannot move
	// it. ptrE starts as that link offset and is relocated by an
	// R_MIPS_32 entry the builder adds here.
	msgOff := b.roSegOff("msgE")
	b.roStr("fmtE", "prx says %s and %d\n")
	b.roWords("ptrE", msgOff)
	b.extraRelocs = append(b.extraRelocs, [2]uint32{b.roSegOff("ptrE"), 2})

	m, _ := runGate(t, b.buildPRX(), 10)
	if got, want := m.ConsoleOutput(), "prx says relocated hello and 77\n"; got != want {
		t.Fatalf("console = %q, want %q", got, want)
	}
	if base := m.Kernel().ModuleBase(); base != 0x08804000 {
		t.Fatalf("ModuleBase = %#x, want 0x08804000 (partition base plus the 16 KB reserve)", base)
	}
	if ptr := m.Bus.Read32(0x08804000 + b.roSegOff("ptrE")); ptr != 0x08804000+msgOff {
		t.Fatalf("relocated pointer = %#x, want %#x", ptr, 0x08804000+msgOff)
	}
}

// --- gate (f): determinism across a mid-run save -----------------

func TestPSP3GateDeterminism(t *testing.T) {
	img := buildGateB()

	mA := New()
	if err := mA.LoadExecutable(img); err != nil {
		t.Fatal(err)
	}
	mA.RunCycles(120)
	if mA.Exited() {
		t.Fatal("gate B finished before the save point; pick a smaller budget")
	}
	saved := mA.Serialize()

	// A second fresh machine at the same cycle count must produce
	// byte-identical state.
	mB := New()
	if err := mB.LoadExecutable(img); err != nil {
		t.Fatal(err)
	}
	mB.RunCycles(120)
	if !bytes.Equal(saved, mB.Serialize()) {
		t.Fatal("two fresh machines disagree on state at cycle 120")
	}

	finish := func(m *Machine) {
		for f := 0; f < 10 && !m.Exited(); f++ {
			m.RunFrame()
		}
		if !m.Exited() {
			t.Fatal("machine did not exit")
		}
	}
	finish(mA)

	mC := New()
	if err := mC.LoadExecutable(img); err != nil {
		t.Fatal(err)
	}
	if err := mC.Deserialize(saved); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	finish(mC)

	if mC.ConsoleOutput() != mA.ConsoleOutput() {
		t.Fatalf("console after restore = %q, want %q", mC.ConsoleOutput(), mA.ConsoleOutput())
	}
	if !bytes.Equal(mC.Serialize(), mA.Serialize()) {
		t.Fatal("final state after restore differs from the straight run")
	}
}

// --- gate (g): unknown NID ----------------------------------------

func TestPSP3GateUnknownNID(t *testing.T) {
	fake := gateLib{"FakeLib", []uint32{0xDEADBEEF}}
	b := newGateBuilder("GATEG", []gateLib{fake, libSysMem, libLoadExec})
	b.roStr("fmtG", "err=%x end\n")
	a := b.code
	a.addiu(rSP, rSP, -64)
	a.jalStub(0, 0) // the nonsense import
	a.nop()
	a.move(rS0, rV0)
	a.la(rA0, "fmtG")
	a.move(rA1, rS0)
	a.jalStub(1, 0) // sceKernelPrintf
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0) // sceKernelExitGame
	a.nop()

	m, _ := runGate(t, b.build(false, nil), 10)
	if got, want := m.ConsoleOutput(), "err=80020001 end\n"; got != want {
		t.Fatalf("console = %q, want %q", got, want)
	}
	uc := m.Kernel().UnknownCalls()
	if len(uc) != 1 || uc[0].Module != "FakeLib" || uc[0].NID != 0xDEADBEEF || uc[0].Count != 1 {
		t.Fatalf("UnknownCalls = %+v, want one {FakeLib 0xDEADBEEF 1}", uc)
	}
}

// --- gate (h): mutex + event flag semantics ------------------------

func TestPSP3GateSyncObjects(t *testing.T) {
	tm := gateLib{"ThreadManForUser", []uint32{
		0xB7D098C6, // 0 CreateMutex
		0xB011B11F, // 1 LockMutex
		0x6B30100F, // 2 UnlockMutex
		0x55C20A00, // 3 CreateEventFlag
		0x402FCF22, // 4 WaitEventFlag
		0x1FB15A32, // 5 SetEventFlag
		0x30FD48F0, // 6 PollEventFlag
		0xA66B0120, // 7 ReferEventFlagStatus
	}}
	b := newGateBuilder("GATEH", []gateLib{tm, libSysMem, libLoadExec})
	b.roStr("mxName", "mx")
	b.roStr("evName", "ev")
	b.roStr("fmtMx", "mx %x %x %x\n")
	b.roStr("fmtEv", "ev %x %x %x\n")
	b.roStr("fmtPat", "pat %x res %d\n")
	b.roWords("zeroWord", 0)
	b.roWords("resWord", 0)
	b.ro("evfStat", make([]byte, 52))
	a := b.code
	a.addiu(rSP, rSP, -80)
	// Recursive mutex: lock twice, unlock twice, third unlock errors.
	a.la(rA0, "mxName")
	a.li(rA1, 0x400) // recursive attribute
	a.li(rA2, 0)     // starts unlocked
	a.jalStub(0, 0)  // CreateMutex
	a.nop()
	a.move(rS0, rV0)
	a.move(rA0, rS0)
	a.li(rA1, 1)
	a.li(rA2, 0)
	a.jalStub(0, 1) // LockMutex
	a.nop()
	a.move(rS1, rV0)
	a.move(rA0, rS0)
	a.li(rA1, 1)
	a.li(rA2, 0)
	a.jalStub(0, 1) // LockMutex again (recursive)
	a.nop()
	a.move(rA0, rS0)
	a.jalStub(0, 2) // UnlockMutex
	a.nop()
	a.move(rA0, rS0)
	a.jalStub(0, 2) // UnlockMutex
	a.nop()
	a.move(rS2, rV0)
	a.move(rA0, rS0)
	a.jalStub(0, 2) // UnlockMutex a third time: not locked
	a.nop()
	a.move(rS3, rV0)
	a.la(rA0, "fmtMx")
	a.move(rA1, rS1)
	a.move(rA2, rS2)
	a.move(rA3, rS3)
	a.jalStub(1, 0) // printf
	a.nop()
	// Event flag.
	a.la(rA0, "evName")
	a.li(rA1, 0)
	a.li(rA2, 0)    // initial pattern 0
	a.jalStub(0, 3) // CreateEventFlag
	a.nop()
	a.move(rS0, rV0)
	// Wait for bit 0 (AND) with a zero timeout word: polls, times out.
	a.move(rA0, rS0)
	a.li(rA1, 1)
	a.li(rA2, 0) // AND, no clear
	a.la(rA3, "resWord")
	a.la(rT0, "zeroWord")
	a.sw(rT0, 16, rSP) // fifth argument: timeout pointer
	a.jalStub(0, 4)    // WaitEventFlag
	a.nop()
	a.move(rS1, rV0)
	// Set bit 1 only, then poll for bits 0+1 (AND): not satisfied.
	a.move(rA0, rS0)
	a.li(rA1, 2)
	a.jalStub(0, 5) // SetEventFlag
	a.nop()
	a.move(rA0, rS0)
	a.li(rA1, 3)
	a.li(rA2, 0)
	a.la(rA3, "resWord")
	a.jalStub(0, 6) // PollEventFlag
	a.nop()
	a.move(rS2, rV0)
	// Set bit 0 too; the AND wait with CLEAR_PAT succeeds.
	a.move(rA0, rS0)
	a.li(rA1, 1)
	a.jalStub(0, 5) // SetEventFlag
	a.nop()
	a.move(rA0, rS0)
	a.li(rA1, 3)
	a.li(rA2, 0x20) // AND | WAIT_CLEAR_PAT
	a.la(rA3, "resWord")
	a.sw(rZero, 16, rSP) // no timeout
	a.jalStub(0, 4)      // WaitEventFlag
	a.nop()
	a.move(rS3, rV0)
	a.la(rA0, "fmtEv")
	a.move(rA1, rS1)
	a.move(rA2, rS2)
	a.move(rA3, rS3)
	a.jalStub(1, 0) // printf
	a.nop()
	// Refer: the pattern field (+44) must read 0 after the clear.
	a.move(rA0, rS0)
	a.la(rA1, "evfStat")
	a.jalStub(0, 7) // ReferEventFlagStatus
	a.nop()
	a.la(rT0, "evfStat")
	a.lw(rA1, 44, rT0)
	a.la(rT0, "resWord")
	a.lw(rA2, 0, rT0)
	a.la(rA0, "fmtPat")
	a.jalStub(1, 0) // printf
	a.nop()
	a.li(rA0, 0)
	a.jalStub(2, 0) // sceKernelExitGame
	a.nop()

	m, _ := runGate(t, b.build(false, nil), 10)
	want := "mx 0 0 800201c5\nev 800201a8 800201af 0\npat 0 res 3\n"
	if got := m.ConsoleOutput(); got != want {
		t.Fatalf("console = %q, want %q", got, want)
	}
}
