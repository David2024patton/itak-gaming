package enginetest

import (
	"encoding/binary"
	"strings"
)

// A minimal two-pass MIPS assembler and ET_EXEC ELF renderer
// for the enginetest guest, in the same shape as the PSP-3 gate
// builder (system/psp3_gate_test.go): one RWX segment holding
// code, syscall stubs, rodata, and the SceModuleInfo block the
// kernel loader resolves imports through.

const (
	rZero = 0
	rV0   = 2
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
	rT6   = 14
	rT7   = 15
	rS0   = 16
	rS1   = 17
	rS2   = 18
	rS3   = 19
	rS4   = 20
	rS5   = 21
	rS6   = 22
	rS7   = 23
	rSP   = 29
)

const (
	fxBranch = iota
	fxJump
	fxStub
	fxLaHi
	fxLaLo
)

type fixup struct {
	idx  int
	kind int
	lab  string
	lib  int
	fn   int
	base uint32
}

type asm struct {
	words  []uint32
	labels map[string]int
	fixups []fixup
}

func newAsm() *asm { return &asm{labels: map[string]int{}} }

func (a *asm) emit(w uint32) { a.words = append(a.words, w) }
func (a *asm) here(lab string) {
	if _, dup := a.labels[lab]; dup {
		panic("duplicate label " + lab)
	}
	a.labels[lab] = len(a.words)
}

func itype(op, rs, rt, imm int) uint32 {
	return uint32(op)<<26 | uint32(rs)<<21 | uint32(rt)<<16 | uint32(uint16(imm))
}
func rtype(rs, rt, rd, shamt, funct int) uint32 {
	return uint32(rs)<<21 | uint32(rt)<<16 | uint32(rd)<<11 | uint32(shamt)<<6 | uint32(funct)
}

func (a *asm) nop()                  { a.emit(0) }
func (a *asm) addiu(rt, rs, imm int) { a.emit(itype(0x09, rs, rt, imm)) }
func (a *asm) ori(rt, rs, imm int)   { a.emit(itype(0x0D, rs, rt, imm)) }
func (a *asm) xori(rt, rs, imm int)  { a.emit(itype(0x0E, rs, rt, imm)) }
func (a *asm) lui(rt, imm int)       { a.emit(itype(0x0F, 0, rt, imm)) }
func (a *asm) lw(rt, off, rs int)    { a.emit(itype(0x23, rs, rt, off)) }
func (a *asm) sw(rt, off, rs int)    { a.emit(itype(0x2B, rs, rt, off)) }
func (a *asm) lh(rt, off, rs int)    { a.emit(itype(0x21, rs, rt, off)) }
func (a *asm) sh(rt, off, rs int)    { a.emit(itype(0x29, rs, rt, off)) }
func (a *asm) addu(rd, rs, rt int)   { a.emit(rtype(rs, rt, rd, 0, 0x21)) }
func (a *asm) slt(rd, rs, rt int)    { a.emit(rtype(rs, rt, rd, 0, 0x2A)) }
func (a *asm) sll(rd, rt, sh int)    { a.emit(rtype(0, rt, rd, sh, 0x00)) }
func (a *asm) mfhi(rd int)           { a.emit(rtype(0, 0, rd, 0, 0x10)) }
func (a *asm) divu(rs, rt int)       { a.emit(rtype(rs, rt, 0, 0, 0x1B)) }
func (a *asm) move(rd, rs int)       { a.addu(rd, rs, rZero) }

func (a *asm) li(rd int, v int32) {
	if v >= -32768 && v <= 32767 {
		a.addiu(rd, rZero, int(v))
		return
	}
	uv := uint32(v)
	a.lui(rd, int(uv>>16))
	if uv&0xFFFF != 0 {
		a.ori(rd, rd, int(uv&0xFFFF))
	}
}

func (a *asm) beq(rs, rt int, lab string) {
	a.fixups = append(a.fixups, fixup{idx: len(a.words), kind: fxBranch, lab: lab, base: itype(0x04, rs, rt, 0)})
	a.emit(itype(0x04, rs, rt, 0))
}
func (a *asm) bne(rs, rt int, lab string) {
	a.fixups = append(a.fixups, fixup{idx: len(a.words), kind: fxBranch, lab: lab, base: itype(0x05, rs, rt, 0)})
	a.emit(itype(0x05, rs, rt, 0))
}
func (a *asm) j(lab string) {
	a.fixups = append(a.fixups, fixup{idx: len(a.words), kind: fxJump, lab: lab, base: uint32(0x02) << 26})
	a.emit(uint32(0x02) << 26)
}
func (a *asm) jalStub(lib, fn int) {
	a.fixups = append(a.fixups, fixup{idx: len(a.words), kind: fxStub, lib: lib, fn: fn, base: uint32(0x03) << 26})
	a.emit(uint32(0x03) << 26)
}
func (a *asm) la(rd int, lab string) {
	a.fixups = append(a.fixups,
		fixup{idx: len(a.words), kind: fxLaHi, lab: lab, base: itype(0x0F, 0, rd, 0)},
		fixup{idx: len(a.words) + 1, kind: fxLaLo, lab: lab, base: itype(0x09, rd, rd, 0)})
	a.emit(0)
	a.emit(0)
}

// --- ELF rendering ------------------------------------------------

type stubLib struct {
	name string
	nids []uint32
}

const (
	segBase  = 0x08804000 // ET_EXEC link base, inside the user partition
	textOff  = 0x40       // segment offset of .text
	segFOff  = 0x1000     // file offset of the segment
	moduleNm = "PSP4ENG"
)

type builder struct {
	libs    []stubLib
	code    *asm
	roOrder []string
	roData  map[string][]byte
}

func newBuilder(libs []stubLib) *builder {
	return &builder{libs: libs, code: newAsm(), roData: map[string][]byte{}}
}

func (b *builder) ro(label string, data []byte) {
	if _, dup := b.roData[label]; dup {
		panic("duplicate rodata label " + label)
	}
	b.roOrder = append(b.roOrder, label)
	b.roData[label] = data
}

func (b *builder) roStr(label, s string) { b.ro(label, append([]byte(s), 0)) }

func (b *builder) roWords(label string, words ...uint32) {
	buf := make([]byte, 4*len(words))
	for i, w := range words {
		binary.LittleEndian.PutUint32(buf[i*4:], w)
	}
	b.ro(label, buf)
}

// layout computes every address a build needs.
func (b *builder) layout() (stubAddr func(lib, fn int) uint32, roAddr map[string]uint32, infoOff, segSize int) {
	textWords := len(b.code.words)
	stubsOff := textOff + 4*textWords
	totalFuncs := 0
	for _, l := range b.libs {
		totalFuncs += len(l.nids)
	}
	roSegOff := (stubsOff + 8*totalFuncs + 15) &^ 15

	off := roSegOff
	roAddr = map[string]uint32{}
	for _, label := range b.roOrder {
		roAddr[label] = segBase + uint32(off)
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
		return segBase + uint32(stubsOff) + uint32(8*(n+fn))
	}
	roAddr["\x00stubTables"] = segBase + uint32(stubTablesOff)
	for i := range b.libs {
		roAddr["\x00name"+string(rune(i))] = segBase + uint32(libNameOff[i])
		roAddr["\x00nids"+string(rune(i))] = segBase + uint32(libNidOff[i])
	}
	return stubAddr, roAddr, infoOff, segSize
}

// build renders the ET_EXEC image.
func (b *builder) build() []byte {
	stubAddr, roAddr, infoOff, segSize := b.layout()
	textAddr := func(idx int) uint32 { return segBase + textOff + uint32(4*idx) }

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
		}
	}

	content := make([]byte, segSize)
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(content[off:], v) }
	for i, w := range words {
		put32(textOff+4*i, w)
	}
	stubsOff := textOff + 4*len(words)
	n := 0
	for _, l := range b.libs {
		for range l.nids {
			put32(stubsOff+8*n, 0x03E00008) // JR $ra
			put32(stubsOff+8*n+4, 0)        // delay slot: the kernel patches the SYSCALL here
			n++
		}
	}
	for _, label := range b.roOrder {
		copy(content[int(roAddr[label]-segBase):], b.roData[label])
	}
	stubTablesAddr := roAddr["\x00stubTables"]
	for i, l := range b.libs {
		nameAddr := roAddr["\x00name"+string(rune(i))]
		nidAddr := roAddr["\x00nids"+string(rune(i))]
		copy(content[int(nameAddr-segBase):], l.name)
		for j, nid := range l.nids {
			put32(int(nidAddr-segBase)+4*j, nid)
		}
		tbl := int(stubTablesAddr-segBase) + 20*i
		put32(tbl+0, nameAddr)
		binary.LittleEndian.PutUint16(content[tbl+10:], uint16(len(l.nids)))
		content[tbl+8] = 5 // entrySize in words
		put32(tbl+12, nidAddr)
		put32(tbl+16, stubAddr(i, 0))
	}
	put32(infoOff+0x00, 0) // attribute: user module
	copy(content[infoOff+4:infoOff+31], moduleNm)
	put32(infoOff+0x20, segBase+0x900) // gp in module space
	put32(infoOff+0x2C, stubTablesAddr)
	put32(infoOff+0x30, stubTablesAddr+uint32(20*len(b.libs)))

	shstr := "\x00.text\x00.sceStub.text\x00.rodata.sceModuleInfo\x00.shstrtab\x00"
	nameText := strings.Index(shstr, ".text")
	nameStubT := strings.Index(shstr, ".sceStub.text")
	nameInfo := strings.Index(shstr, ".rodata.sceModuleInfo")
	nameShstr := strings.Index(shstr, ".shstrtab")

	shstrOff := segFOff + segSize
	shoff := (shstrOff + len(shstr) + 3) &^ 3
	const shnum = 5
	out := make([]byte, shoff+shnum*40)
	out[0], out[1], out[2], out[3] = 0x7F, 'E', 'L', 'F'
	out[4], out[5] = 1, 1
	binary.LittleEndian.PutUint16(out[16:], 2) // ET_EXEC
	binary.LittleEndian.PutUint16(out[18:], 8) // EM_MIPS
	binary.LittleEndian.PutUint32(out[20:], 1)
	binary.LittleEndian.PutUint32(out[24:], segBase+textOff) // entry = first word
	binary.LittleEndian.PutUint32(out[28:], 52)
	binary.LittleEndian.PutUint32(out[32:], uint32(shoff))
	binary.LittleEndian.PutUint16(out[40:], 52)
	binary.LittleEndian.PutUint16(out[42:], 32)
	binary.LittleEndian.PutUint16(out[44:], 1)
	binary.LittleEndian.PutUint16(out[46:], 40)
	binary.LittleEndian.PutUint16(out[48:], shnum)
	binary.LittleEndian.PutUint16(out[50:], shnum-1)
	ph := out[52:]
	binary.LittleEndian.PutUint32(ph[0:], 1) // PT_LOAD
	binary.LittleEndian.PutUint32(ph[4:], segFOff)
	binary.LittleEndian.PutUint32(ph[8:], segBase)
	binary.LittleEndian.PutUint32(ph[12:], segBase)
	binary.LittleEndian.PutUint32(ph[16:], uint32(segSize))
	binary.LittleEndian.PutUint32(ph[20:], uint32(segSize))
	binary.LittleEndian.PutUint32(ph[24:], 7)
	binary.LittleEndian.PutUint32(ph[28:], 0x1000)
	copy(out[segFOff:], content)
	copy(out[shstrOff:], shstr)
	sh := func(i int, name, typ, addr, off, size uint32) {
		bb := out[shoff+i*40:]
		binary.LittleEndian.PutUint32(bb[0:], name)
		binary.LittleEndian.PutUint32(bb[4:], typ)
		binary.LittleEndian.PutUint32(bb[12:], addr)
		binary.LittleEndian.PutUint32(bb[16:], off)
		binary.LittleEndian.PutUint32(bb[20:], size)
	}
	sh(1, uint32(nameText), 1, segBase+textOff, segFOff+textOff, uint32(4*len(words)))
	sh(2, uint32(nameStubT), 1, segBase+uint32(stubsOff), uint32(segFOff+stubsOff), uint32(8*n))
	sh(3, uint32(nameInfo), 1, segBase+uint32(infoOff), uint32(segFOff+infoOff), 0x40)
	sh(4, uint32(nameShstr), 3, 0, uint32(shstrOff), uint32(len(shstr)))
	return out
}
