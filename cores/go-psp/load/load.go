// Package load parses and loads PSP executables: static ET_EXEC
// ELFs and relocatable PRX modules (ET_SCE_EXEC), as produced by
// the open PSP toolchain. It implements the documented module
// format (the TyRaNiD "Naked PSP" overview and the pspsdk
// headers): program headers carry the segments, relocation
// sections of type 0x700000A0 carry Elf32_Rel entries in module
// address space, the .rodata.sceModuleInfo section describes the
// module, and the .lib.stub tables plus .rodata.sceResident and
// .rodata.sceNid carry the library imports.
//
// The loader only places bytes and applies relocations. Resolving
// imports (assigning syscall numbers and patching the stub code)
// is the kernel's job; the loader reports every import it found.
package load

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Memory is the byte addressed store the loader writes into. The
// system bus satisfies it.
type Memory interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, value uint8)
	Write16(addr uint32, value uint16)
	Write32(addr uint32, value uint32)
}

// Import is one library function (or variable) a module imports.
// StubAddr is the loaded address of the stub code for a function
// import: an 8 byte stub whose first word is JR $ra (0x03E00008)
// and whose second word, the jump delay slot, is where the kernel
// patches the SYSCALL instruction it assigns to this import. The
// stub table's funcTable field points directly at the stub code
// block (the .sceStub.text section), so the stub for function j
// sits at funcTable + 8*j; there is no pointer array. Variable
// imports carry StubAddr 0: they have no stub code.
type Import struct {
	Module   string
	NID      uint32
	StubAddr uint32
	IsVar    bool
}

// Module is a loaded executable image.
type Module struct {
	Name        string
	Attr        uint32
	GP          uint32 // final (relocated) $gp value from the module info
	Entry       uint32 // final entry address (module_start)
	Base        uint32 // load base (0 for fixed ET_EXEC images)
	Size        uint32 // resident size in bytes (segments extent)
	Relocatable bool
	Imports     []Import
}

// ELF constants used by the PSP formats.
const (
	etExec    = 2
	etSceExec = 0xFFA0
	etSceRel  = 0xFFA1
	ptLoad    = 1
	shtRel    = 9
	// shtPRXReloc is the PSP relocation section type.
	shtPRXReloc = 0x700000A0
)

// MIPS relocation types implemented (the documented PRX subset).
const (
	rNone    = 0
	r32      = 2
	r26      = 4
	rHi16    = 5
	rLo16    = 6
	rGPrel16 = 7
)

type phdr struct {
	typ, off, vaddr, paddr, filesz, memsz uint32
}

type shdr struct {
	name, typ, flags, addr, off, size, link, info, entsize uint32
}

type elfFile struct {
	data        []byte
	typ         uint16
	entry       uint32
	phdrs       []phdr
	shdrs       []shdr
	shstrndx    int
	relocatable bool
}

// Probe reports the resident size of the module image and whether
// it is relocatable, without loading anything. The caller uses
// the size to reserve a load base for a PRX module.
func Probe(data []byte) (size uint32, relocatable bool, err error) {
	f, err := parse(data)
	if err != nil {
		return 0, false, err
	}
	extent := segmentsExtent(f.phdrs)
	if extent == 0 {
		return 0, false, errors.New("load: no PT_LOAD segments")
	}
	return extent, f.relocatable, nil
}

// segmentsExtent is the resident size of the PT_LOAD segments: the
// span from the lowest segment vaddr to the highest segment end.
// For a relocatable module (vaddrs based at 0) it is simply the
// highest end; for a fixed ET_EXEC image it strips the link base.
func segmentsExtent(phdrs []phdr) uint32 {
	lo := uint32(0xFFFFFFFF)
	var hi uint32
	for _, p := range phdrs {
		if p.typ != ptLoad {
			continue
		}
		if p.vaddr < lo {
			lo = p.vaddr
		}
		if end := p.vaddr + p.memsz; end > hi {
			hi = end
		}
	}
	if lo == 0xFFFFFFFF {
		return 0
	}
	return hi - lo
}

func parse(data []byte) (*elfFile, error) {
	if len(data) < 52 {
		return nil, errors.New("load: file too short for an ELF header")
	}
	if data[0] != 0x7F || data[1] != 'E' || data[2] != 'L' || data[3] != 'F' {
		return nil, errors.New("load: not an ELF file")
	}
	if data[4] != 1 || data[5] != 1 {
		return nil, errors.New("load: not a 32 bit little endian ELF")
	}
	typ := binary.LittleEndian.Uint16(data[16:])
	machine := binary.LittleEndian.Uint16(data[18:])
	if machine != 8 { // EM_MIPS
		return nil, fmt.Errorf("load: ELF machine %d, want MIPS (8)", machine)
	}
	f := &elfFile{data: data, typ: typ, shstrndx: int(binary.LittleEndian.Uint16(data[50:]))}
	switch typ {
	case etExec:
		f.relocatable = false
	case 1, etSceExec, etSceRel:
		f.relocatable = true
	default:
		return nil, fmt.Errorf("load: unsupported ELF type %#x", typ)
	}
	f.entry = binary.LittleEndian.Uint32(data[24:])
	phoff := binary.LittleEndian.Uint32(data[28:])
	shoff := binary.LittleEndian.Uint32(data[32:])
	phnum := int(binary.LittleEndian.Uint16(data[44:]))
	shnum := int(binary.LittleEndian.Uint16(data[48:]))
	for i := 0; i < phnum; i++ {
		off := int(phoff) + i*32
		if off+32 > len(data) {
			return nil, errors.New("load: program header out of range")
		}
		b := data[off:]
		f.phdrs = append(f.phdrs, phdr{
			typ:    binary.LittleEndian.Uint32(b[0:]),
			off:    binary.LittleEndian.Uint32(b[4:]),
			vaddr:  binary.LittleEndian.Uint32(b[8:]),
			paddr:  binary.LittleEndian.Uint32(b[12:]),
			filesz: binary.LittleEndian.Uint32(b[16:]),
			memsz:  binary.LittleEndian.Uint32(b[20:]),
		})
	}
	for i := 0; i < shnum; i++ {
		off := int(shoff) + i*40
		if off+40 > len(data) {
			return nil, errors.New("load: section header out of range")
		}
		b := data[off:]
		f.shdrs = append(f.shdrs, shdr{
			name:    binary.LittleEndian.Uint32(b[0:]),
			typ:     binary.LittleEndian.Uint32(b[4:]),
			flags:   binary.LittleEndian.Uint32(b[8:]),
			addr:    binary.LittleEndian.Uint32(b[12:]),
			off:     binary.LittleEndian.Uint32(b[16:]),
			size:    binary.LittleEndian.Uint32(b[20:]),
			link:    binary.LittleEndian.Uint32(b[24:]),
			info:    binary.LittleEndian.Uint32(b[28:]),
			entsize: binary.LittleEndian.Uint32(b[36:]),
		})
	}
	return f, nil
}

func (f *elfFile) sectionByName(want string) *shdr {
	if f.shstrndx >= len(f.shdrs) {
		return nil
	}
	strSec := f.shdrs[f.shstrndx]
	for i := range f.shdrs {
		start := int(strSec.off + f.shdrs[i].name)
		if start < 0 || start >= len(f.data) {
			continue
		}
		end := start
		for end < len(f.data) && f.data[end] != 0 {
			end++
		}
		if string(f.data[start:end]) == want {
			return &f.shdrs[i]
		}
	}
	return nil
}

// Load copies the module into mem. For a relocatable module the
// segments land at base + p_vaddr and all internal pointers are
// relocated into that address range; for ET_EXEC the segments
// land at their linked addresses and base is ignored (pass 0).
// Relocations are applied in place, then the module info and
// import tables are read back from memory, so every pointer the
// returned Module carries is a final loaded address.
func Load(mem Memory, data []byte, base uint32) (*Module, error) {
	f, err := parse(data)
	if err != nil {
		return nil, err
	}
	if !f.relocatable {
		base = 0
	}
	extent := segmentsExtent(f.phdrs)
	segBases := make([]uint32, len(f.phdrs))
	for i, p := range f.phdrs {
		if p.typ != ptLoad {
			continue
		}
		dest := base + p.vaddr
		segBases[i] = dest
		if int64(p.off)+int64(p.filesz) > int64(len(data)) {
			return nil, errors.New("load: segment file range out of bounds")
		}
		for j := uint32(0); j < p.filesz; j++ {
			mem.Write8(dest+j, data[p.off+j])
		}
		for j := p.filesz; j < p.memsz; j++ {
			mem.Write8(dest+j, 0)
		}
	}
	if extent == 0 {
		return nil, errors.New("load: no PT_LOAD segments")
	}
	m := &Module{Base: base, Size: extent, Relocatable: f.relocatable}
	m.Entry = f.entry
	if f.relocatable {
		m.Entry = base + f.entry
		if err := f.applyRelocations(mem, base, segBases); err != nil {
			return nil, err
		}
	}
	if err := f.readModuleInfo(mem, base, m); err != nil {
		return nil, err
	}
	return m, nil
}

// applyRelocations walks every PSP relocation section and patches
// the loaded image. Per the documented PRX format, r_info packs
// three fields: the MIPS relocation type in bits 0..7, the OFS_BASE
// segment index in bits 8..15 (r_offset is an offset from that
// segment, so the patch target is segBases[OFS_BASE] + r_offset),
// and the ADDR_BASE segment index in bits 16..23 (the stored value
// is an address inside that segment, so the addend base added to
// it is segBases[ADDR_BASE]). Single segment modules, which is
// what the toolchain emits for user programs, have both indices 0
// and a segment vaddr of 0, reducing both to the load base.
func (f *elfFile) applyRelocations(mem Memory, base uint32, segBases []uint32) error {
	for _, s := range f.shdrs {
		if s.typ != shtPRXReloc && s.typ != shtRel {
			continue
		}
		if s.entsize != 8 && s.entsize != 0 {
			return fmt.Errorf("load: relocation entry size %d, want 8", s.entsize)
		}
		type hiPend struct {
			addr uint32
			seg  uint32 // ADDR_BASE of the HI16 entry
		}
		var pending []hiPend
		n := int(s.size / 8)
		for i := 0; i < n; i++ {
			off := int(s.off) + i*8
			if off+8 > len(f.data) {
				return errors.New("load: relocation entry out of range")
			}
			rOff := binary.LittleEndian.Uint32(f.data[off:])
			rInfo := binary.LittleEndian.Uint32(f.data[off+4:])
			rtyp := rInfo & 0xFF
			ofsSeg := (rInfo >> 8) & 0xFF
			addrSeg := (rInfo >> 16) & 0xFF
			if int(ofsSeg) >= len(segBases) || int(addrSeg) >= len(segBases) {
				return fmt.Errorf("load: relocation segment %d/%d out of range", ofsSeg, addrSeg)
			}
			addr := segBases[ofsSeg] + rOff
			addBase := segBases[addrSeg]
			switch rtyp {
			case rNone:
			case r32:
				mem.Write32(addr, mem.Read32(addr)+addBase)
			case r26:
				w := mem.Read32(addr)
				target := ((w & 0x3FFFFFF) << 2) + addBase
				mem.Write32(addr, (w&0xFC000000)|((target>>2)&0x3FFFFFF))
			case rHi16:
				pending = append(pending, hiPend{addr, addrSeg})
			case rLo16:
				w := mem.Read32(addr)
				alo := int32(int16(w & 0xFFFF))
				var ahi int32
				if len(pending) > 0 {
					ahi = int32(mem.Read32(pending[0].addr) & 0xFFFF)
					addBase = segBases[pending[0].seg]
				}
				t := uint32((ahi<<16)+alo) + addBase
				lo := t & 0xFFFF
				mem.Write32(addr, (w&0xFFFF0000)|lo)
				hi := (t - uint32(int32(int16(lo)))) >> 16
				for _, p := range pending {
					hw := mem.Read32(p.addr)
					mem.Write32(p.addr, (hw&0xFFFF0000)|(hi&0xFFFF))
				}
				pending = nil
			case rGPrel16:
				// GP relative offsets are invariant under a
				// uniform base shift: both the target and $gp
				// move by the same base, and the module info gp
				// the loader relocates carries the same shift.
				// The entry is validated and left unchanged.
			default:
				return fmt.Errorf("load: unsupported relocation type %d at offset %#x", rtyp, rOff)
			}
		}
		// A HI16 with no following LO16 in its section is paired
		// with a zero low half. Compiler output always pairs them
		// inside one section; this is only a guard.
		for _, p := range pending {
			w := mem.Read32(p.addr)
			t := uint32(int32(w&0xFFFF)<<16) + segBases[p.seg]
			lo := t & 0xFFFF
			hi := (t - uint32(int32(int16(lo)))) >> 16
			mem.Write32(p.addr, (w&0xFFFF0000)|(hi&0xFFFF))
		}
	}
	return nil
}

// SceModuleInfo field offsets (pspsdk pspmoduleinfo.h): the name
// is 27 bytes at +4, gp at +0x20, the export table range at
// +0x24/+0x28, the stub table range at +0x2C/+0x30.
// SceLibraryStubTable is 20 bytes: name u32 @0, version u16 @4,
// attribute u16 @6, entrySize u8 @8 (in words; tables advance by
// entrySize*4 when it is nonzero), varCount u8 @9, funcCount
// u16 @10, nidTable u32 @12, funcTable u32 @16. Function imports
// occupy the first funcCount entries of the NID table; variable
// import NIDs follow them. funcTable points at the stub code
// block itself: stub j is the 8 bytes at funcTable + 8*j.
func (f *elfFile) readModuleInfo(mem Memory, base uint32, m *Module) error {
	var infoAddr uint32
	if s := f.sectionByName(".rodata.sceModuleInfo"); s != nil {
		infoAddr = base + s.addr
	} else if len(f.phdrs) > 0 && f.phdrs[0].paddr >= f.phdrs[0].off && f.phdrs[0].paddr != 0 {
		// Fallback seen in toolchain output: the first segment's
		// p_paddr is the file offset of the module info.
		p := f.phdrs[0]
		infoAddr = base + p.vaddr + (p.paddr - p.off)
	} else {
		return errors.New("load: no .rodata.sceModuleInfo section")
	}
	m.Attr = mem.Read32(infoAddr)
	nameBytes := make([]byte, 0, 27)
	for i := 0; i < 27; i++ {
		b := mem.Read8(infoAddr + 4 + uint32(i))
		if b == 0 {
			break
		}
		nameBytes = append(nameBytes, b)
	}
	m.Name = string(nameBytes)
	m.GP = mem.Read32(infoAddr + 0x20)
	stubTop := mem.Read32(infoAddr + 0x2C)
	stubEnd := mem.Read32(infoAddr + 0x30)
	if stubEnd < stubTop || stubEnd-stubTop > 1<<16 {
		return fmt.Errorf("load: bad stub table range %#x..%#x", stubTop, stubEnd)
	}
	for a := stubTop; a+20 <= stubEnd; {
		namePtr := mem.Read32(a + 0)
		entrySize := int(mem.Read8(a + 8))
		varCount := int(mem.Read8(a + 9))
		funcCount := int(mem.Read16(a + 10))
		nidPtr := mem.Read32(a + 12)
		funcPtr := mem.Read32(a + 16)
		libName := readCString(mem, namePtr)
		for j := 0; j < funcCount; j++ {
			m.Imports = append(m.Imports, Import{
				Module:   libName,
				NID:      mem.Read32(nidPtr + uint32(4*j)),
				StubAddr: funcPtr + uint32(8*j),
			})
		}
		for j := 0; j < varCount; j++ {
			m.Imports = append(m.Imports, Import{
				Module: libName,
				NID:    mem.Read32(nidPtr + uint32(4*(funcCount+j))),
				IsVar:  true,
			})
		}
		step := 20
		if entrySize > 0 {
			step = entrySize * 4
		}
		a += uint32(step)
	}
	return nil
}

func readCString(mem Memory, addr uint32) string {
	var sb strings.Builder
	for i := 0; i < 256; i++ {
		b := mem.Read8(addr + uint32(i))
		if b == 0 {
			break
		}
		sb.WriteByte(b)
	}
	return sb.String()
}
