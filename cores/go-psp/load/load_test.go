package load

import (
	"encoding/binary"
	"strings"
	"testing"
)

// mapMem is a byte slice backed Memory window for the tests.
type mapMem struct {
	base uint32
	data []byte
}

func newMapMem(base uint32, size int) *mapMem {
	return &mapMem{base: base, data: make([]byte, size)}
}

func (m *mapMem) off(addr uint32) int { return int(addr - m.base) }

func (m *mapMem) Read8(addr uint32) uint8 { return m.data[m.off(addr)] }

func (m *mapMem) Read16(addr uint32) uint16 {
	return binary.LittleEndian.Uint16(m.data[m.off(addr):])
}

func (m *mapMem) Read32(addr uint32) uint32 {
	return binary.LittleEndian.Uint32(m.data[m.off(addr):])
}

func (m *mapMem) Write8(addr uint32, v uint8) { m.data[m.off(addr)] = v }

func (m *mapMem) Write16(addr uint32, v uint16) {
	binary.LittleEndian.PutUint16(m.data[m.off(addr):], v)
}

func (m *mapMem) Write32(addr uint32, v uint32) {
	binary.LittleEndian.PutUint32(m.data[m.off(addr):], v)
}

// Test module layout in module address space (offsets from the
// segment base):
//
//	0x0000 .text: a few code words (tests patch some of them)
//	0x0200 .rodata.sceModuleInfo (64 bytes)
//	0x0300 .lib.stub: one SceLibraryStubTable (20 bytes)
//	0x0340 stub code block: two 8 byte stubs
//	0x0380 .rodata.sceResident: library name string
//	0x03A0 .rodata.sceNid: two function NIDs
//	0x0400 a data word the tests relocate
const (
	modTextOff   = 0x0000
	modInfoOff   = 0x0200
	modStubT_off = 0x0300
	modStubsOff  = 0x0340
	modNameOff   = 0x0380
	modNidOff    = 0x03A0
	modDataOff   = 0x0400
	modSegSize   = 0x1000
)

type reloc struct {
	off uint32
	typ uint32
	seg uint32
}

type elfOpts struct {
	typ    uint16 // ELF e_type
	link   uint32 // segment link address (vaddr base in the file)
	relocs []reloc
}

func buildTestELF(t *testing.T, o elfOpts) []byte {
	t.Helper()
	content := make([]byte, modSegSize)
	put32 := func(off uint32, v uint32) { binary.LittleEndian.PutUint32(content[off:], v) }
	// .text words: [0] a JAL placeholder, [1] a LUI, [2] an ADDIU.
	put32(modTextOff+0, 0x0C000100) // jal +0x400 (26 bit field 0x100)
	put32(modTextOff+4, 0x3C080002) // lui $t0, 0x0002 (pair encodes 0x18ABC)
	put32(modTextOff+8, 0x25088ABC) // addiu $t0, $t0, 0x8ABC (sign extends)
	put32(modDataOff, 0x00000500)   // data word pointing at module offset 0x500
	// Module info.
	info := content[modInfoOff:]
	binary.LittleEndian.PutUint32(info[0:], 0) // attribute
	copy(info[4:31], "TESTMOD")
	put32(modInfoOff+0x20, 0x00000900)             // gp (module space)
	put32(modInfoOff+0x2C, o.link+modStubT_off)    // stubTop
	put32(modInfoOff+0x30, o.link+modStubT_off+20) // stubBtm
	// One stub table.
	st := content[modStubT_off:]
	put32(modStubT_off+0, o.link+modNameOff)   // name pointer
	binary.LittleEndian.PutUint16(st[4:], 0)   // version
	binary.LittleEndian.PutUint16(st[6:], 0)   // attribute
	st[8] = 5                                  // entrySize in words
	st[9] = 0                                  // varCount
	binary.LittleEndian.PutUint16(st[10:], 2)  // funcCount
	put32(modStubT_off+12, o.link+modNidOff)   // nidTable
	put32(modStubT_off+16, o.link+modStubsOff) // funcTable: the stub code block
	// Stub code: {JR $ra, 0} twice.
	put32(modStubsOff+0, 0x03E00008)
	put32(modStubsOff+4, 0)
	put32(modStubsOff+8, 0x03E00008)
	put32(modStubsOff+12, 0)
	copy(content[modNameOff:], "ThreadManForUser")
	put32(modNidOff+0, 0x446D8DE6)
	put32(modNidOff+4, 0xF475845D)

	// Sections: null, .rodata.sceModuleInfo, one .rel section per
	// reloc group (a single one here), .shstrtab.
	shstr := "\x00.rodata.sceModuleInfo\x00.rel.text\x00.shstrtab\x00"
	nameInfo := strings.Index(shstr, ".rodata.sceModuleInfo")
	nameRel := strings.Index(shstr, ".rel.text")
	nameShstr := strings.Index(shstr, ".shstrtab")

	segFileOff := 0x1000
	shstrOff := segFileOff + modSegSize
	relOff := shstrOff + len(shstr)
	shoff := (relOff + len(o.relocs)*8 + 3) &^ 3

	out := make([]byte, shoff+4*40)
	// ELF header.
	out[0], out[1], out[2], out[3] = 0x7F, 'E', 'L', 'F'
	out[4], out[5] = 1, 1
	binary.LittleEndian.PutUint16(out[16:], o.typ)
	binary.LittleEndian.PutUint16(out[18:], 8) // EM_MIPS
	binary.LittleEndian.PutUint32(out[20:], 1)
	binary.LittleEndian.PutUint32(out[24:], o.link+0x40) // entry
	binary.LittleEndian.PutUint32(out[28:], 52)          // phoff
	binary.LittleEndian.PutUint32(out[32:], uint32(shoff))
	binary.LittleEndian.PutUint16(out[40:], 52)
	binary.LittleEndian.PutUint16(out[42:], 32)
	binary.LittleEndian.PutUint16(out[44:], 1) // phnum
	binary.LittleEndian.PutUint16(out[46:], 40)
	binary.LittleEndian.PutUint16(out[48:], 4) // shnum
	binary.LittleEndian.PutUint16(out[50:], 3) // shstrndx
	// Program header.
	ph := out[52:]
	binary.LittleEndian.PutUint32(ph[0:], 1) // PT_LOAD
	binary.LittleEndian.PutUint32(ph[4:], uint32(segFileOff))
	binary.LittleEndian.PutUint32(ph[8:], o.link)
	binary.LittleEndian.PutUint32(ph[12:], o.link)
	binary.LittleEndian.PutUint32(ph[16:], modSegSize)
	binary.LittleEndian.PutUint32(ph[20:], modSegSize)
	copy(out[segFileOff:], content)
	copy(out[shstrOff:], shstr)
	for i, r := range o.relocs {
		binary.LittleEndian.PutUint32(out[relOff+i*8:], r.off)
		binary.LittleEndian.PutUint32(out[relOff+i*8+4:], r.typ|(r.seg<<8))
	}
	sh := func(i int, name, typ, addr, off, size, entsize uint32) {
		b := out[shoff+i*40:]
		binary.LittleEndian.PutUint32(b[0:], name)
		binary.LittleEndian.PutUint32(b[4:], typ)
		binary.LittleEndian.PutUint32(b[12:], addr)
		binary.LittleEndian.PutUint32(b[16:], off)
		binary.LittleEndian.PutUint32(b[20:], size)
		binary.LittleEndian.PutUint32(b[36:], entsize)
	}
	sh(1, uint32(nameInfo), 1, o.link+modInfoOff, uint32(segFileOff+modInfoOff), 64, 0)
	sh(2, uint32(nameRel), shtPRXReloc, 0, uint32(relOff), uint32(len(o.relocs)*8), 8)
	sh(3, uint32(nameShstr), 3, 0, uint32(shstrOff), uint32(len(shstr)), 0)
	return out
}

func TestLoadExecImage(t *testing.T) {
	data := buildTestELF(t, elfOpts{typ: etExec, link: 0x08800000})
	size, relocatable, err := Probe(data)
	if err != nil {
		t.Fatal(err)
	}
	if relocatable || size != modSegSize {
		t.Fatalf("Probe: size %#x relocatable %v, want %#x false", size, relocatable, modSegSize)
	}
	mem := newMapMem(0x08800000, modSegSize)
	m, err := Load(mem, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "TESTMOD" || m.Entry != 0x08800040 || m.GP != 0x900 || m.Relocatable {
		t.Fatalf("module: name %q entry %#x gp %#x reloc %v", m.Name, m.Entry, m.GP, m.Relocatable)
	}
	if len(m.Imports) != 2 {
		t.Fatalf("imports: %d, want 2", len(m.Imports))
	}
	want0 := Import{Module: "ThreadManForUser", NID: 0x446D8DE6, StubAddr: 0x08800340}
	want1 := Import{Module: "ThreadManForUser", NID: 0xF475845D, StubAddr: 0x08800348}
	if m.Imports[0] != want0 || m.Imports[1] != want1 {
		t.Fatalf("imports: %+v / %+v", m.Imports[0], m.Imports[1])
	}
	// The stub code itself must sit untouched at StubAddr: JR $ra
	// in word 0 and a zero delay slot word the kernel will patch.
	if w := mem.Read32(m.Imports[0].StubAddr); w != 0x03E00008 {
		t.Fatalf("stub word 0 = %#x, want JR $ra", w)
	}
	if w := mem.Read32(m.Imports[0].StubAddr + 4); w != 0 {
		t.Fatalf("stub word 1 = %#x, want 0 placeholder", w)
	}
}

func TestLoadPRXRelocations(t *testing.T) {
	const base = 0x08900000
	relocs := []reloc{
		{off: modInfoOff + 0x20, typ: r32}, // gp field
		{off: modInfoOff + 0x2C, typ: r32}, // stubTop
		{off: modInfoOff + 0x30, typ: r32}, // stubBtm
		{off: modStubT_off + 0, typ: r32},  // stub table name ptr
		{off: modStubT_off + 12, typ: r32}, // nidTable ptr
		{off: modStubT_off + 16, typ: r32}, // funcTable ptr
		{off: modTextOff + 0, typ: r26},    // jal target
		{off: modTextOff + 4, typ: rHi16},  // lui half of the pair
		{off: modTextOff + 8, typ: rLo16},  // addiu half of the pair
		{off: modDataOff, typ: r32},        // data pointer
	}
	data := buildTestELF(t, elfOpts{typ: etSceExec, link: 0, relocs: relocs})
	size, relocatable, err := Probe(data)
	if err != nil {
		t.Fatal(err)
	}
	if !relocatable || size != modSegSize {
		t.Fatalf("Probe: size %#x relocatable %v", size, relocatable)
	}
	mem := newMapMem(base, modSegSize)
	m, err := Load(mem, data, base)
	if err != nil {
		t.Fatal(err)
	}
	if m.Entry != base+0x40 || m.GP != base+0x900 {
		t.Fatalf("module: entry %#x gp %#x, want %#x / %#x", m.Entry, m.GP, base+0x40, base+0x900)
	}
	if len(m.Imports) != 2 {
		t.Fatalf("imports: %d, want 2", len(m.Imports))
	}
	if m.Imports[0].StubAddr != base+modStubsOff || m.Imports[1].StubAddr != base+modStubsOff+8 {
		t.Fatalf("stub addrs: %#x %#x", m.Imports[0].StubAddr, m.Imports[1].StubAddr)
	}
	if m.Imports[0].Module != "ThreadManForUser" || m.Imports[0].NID != 0x446D8DE6 {
		t.Fatalf("import 0: %+v", m.Imports[0])
	}
	// R_MIPS_26: jal field becomes (0x400 + base) >> 2.
	if w := mem.Read32(base + modTextOff); w != 0x0C000000|((0x400+base)>>2) {
		t.Fatalf("jal word = %#x, want %#x", w, 0x0C000000|((0x400+base)>>2))
	}
	// R_MIPS_HI16/LO16 pair: AHL = 0x18ABC, so the relocated
	// address is base + 0x18ABC = 0x08918ABC. The low half 0x8ABC
	// has the sign bit set, so the high half must absorb the
	// carry: LUI imm = 0x0892, ADDIU imm stays 0x8ABC.
	if w := mem.Read32(base + modTextOff + 4); w != 0x3C080000|0x0892 {
		t.Fatalf("lui word = %#x, want imm 0x0892", w)
	}
	if w := mem.Read32(base + modTextOff + 8); w != 0x25088ABC {
		t.Fatalf("addiu word = %#x, want imm 0x8ABC", w)
	}
	// R_MIPS_32 data word.
	if w := mem.Read32(base + modDataOff); w != base+0x500 {
		t.Fatalf("data word = %#x, want %#x", w, base+0x500)
	}
}

func TestLoadUnsupportedRelocation(t *testing.T) {
	data := buildTestELF(t, elfOpts{typ: etSceExec, link: 0, relocs: []reloc{{off: 0, typ: 9}}})
	mem := newMapMem(0x08900000, modSegSize)
	_, err := Load(mem, data, 0x08900000)
	if err == nil || !strings.Contains(err.Error(), "relocation type 9") {
		t.Fatalf("err = %v, want a relocation type 9 error", err)
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	mem := newMapMem(0x08800000, modSegSize)
	if _, err := Load(mem, []byte("not an elf at all, just bytes"), 0); err == nil {
		t.Fatal("non ELF accepted")
	}
	data := buildTestELF(t, elfOpts{typ: etExec, link: 0x08800000})
	data[18] = 62 // EM_X86_64
	if _, err := Load(mem, data, 0); err == nil {
		t.Fatal("non MIPS ELF accepted")
	}
	data = buildTestELF(t, elfOpts{typ: 0x1234, link: 0x08800000})
	if _, err := Load(mem, data, 0); err == nil {
		t.Fatal("unknown ELF type accepted")
	}
}
