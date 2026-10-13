package scd

import "encoding/binary"

// This file builds the two hand-written boot programs for the Sega
// CD unit. Neither contains any Sega BIOS code: both are original
// programs written for this core with the tiny two-pass 68000
// assembler below.
//
// Encoding note: the absolute destination forms of MOVE use the
// absolute long destination encodings 0x13FC (MOVE.B immediate),
// 0x33FC (MOVE.W immediate) and 0x33C0 (MOVE.W from Dn), followed
// by a full 32 bit address. The absolute short forms (0x11FC,
// 0x31FC, 0x31C0) sign extend a 16 bit address and cannot reach the
// main CPU gate array window at 0xA12000, and the IP test program
// in scd_test.go uses the matching absolute long form (0x33FC with
// a 32 bit address) for its comm status write. The repository
// system test documents the same encoding: 0x13FC is move.b #imm,
// addr.l, where the mode 7 destination register field 001 selects
// absolute long.

// Branch condition codes used by the assembler.
const (
	ccBRA = 0
	ccBNE = 6
	ccBEQ = 7
	ccBGT = 14
	ccBLE = 15
)

// asmFixup is one pending branch displacement word.
type asmFixup struct {
	pos   int    // buffer offset of the displacement word
	label string // target label name
}

// asm is a minimal two-pass 68000 assembler: a byte buffer, a
// label map and a fixup list. Label addresses and branch
// displacements use absolute addresses: the assembler is created
// with the address its first emitted byte will run at, and a
// branch displacement is target minus (address of the opcode word
// plus 2), stored as the word after the opcode word (the 16 bit
// displacement form, selected by a zero low byte in the opcode).
type asm struct {
	base   int
	buf    []byte
	labels map[string]int
	fixups []asmFixup
}

func newAsm(base int) *asm {
	return &asm{base: base, labels: make(map[string]int)}
}

// mark records the current position as a label.
func (a *asm) mark(name string) {
	a.labels[name] = a.base + len(a.buf)
}

// addr returns the absolute address a label will have, or 0 if it
// has not been marked yet. Callers that need a forward address
// assemble twice (measure, then rebuild with the known value).
func (a *asm) addr(name string) uint32 {
	return uint32(a.labels[name])
}

func (a *asm) emit16(v uint16) {
	a.buf = append(a.buf, byte(v>>8), byte(v))
}

func (a *asm) emit32(v uint32) {
	a.buf = append(a.buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// finish resolves all branch fixups and returns the code bytes.
func (a *asm) finish() []byte {
	for _, f := range a.fixups {
		target, ok := a.labels[f.label]
		if !ok {
			panic("scd: assembler fixup to unknown label " + f.label)
		}
		// The displacement word sits at base+f.pos, which is also
		// the address of the opcode word plus 2.
		disp := target - (a.base + f.pos)
		binary.BigEndian.PutUint16(a.buf[f.pos:f.pos+2], uint16(int16(disp)))
	}
	return a.buf
}

// branch emits an opcode word with a zero low byte (16 bit
// displacement form) and records a fixup for the word after it.
func (a *asm) branchWord(opcode uint16, label string) {
	a.emit16(opcode)
	a.fixups = append(a.fixups, asmFixup{pos: len(a.buf), label: label})
	a.emit16(0)
}

func (a *asm) nop() { a.emit16(0x4E71) }
func (a *asm) rts() { a.emit16(0x4E75) }

func (a *asm) moveq(v int, dn int) {
	a.emit16(0x7000 | uint16(dn)<<9 | uint16(v&0xFF))
}

func (a *asm) movelImm(v uint32, dn int) {
	a.emit16(0x203C | uint16(dn)<<9)
	a.emit32(v)
}

func (a *asm) moveaImm(v uint32, an int) {
	a.emit16(0x207C | uint16(an)<<9)
	a.emit32(v)
}

func (a *asm) leaAbs(addr uint32, an int) {
	a.emit16(0x41F9 | uint16(an)<<9)
	a.emit32(addr)
}

// movebImmToAbs emits MOVE.B #v, addr.l (absolute long form).
func (a *asm) movebImmToAbs(v uint8, addr uint32) {
	a.emit16(0x13FC)
	a.emit16(uint16(v))
	a.emit32(addr)
}

// movewImmToAbs emits MOVE.W #v, addr.l (absolute long form).
func (a *asm) movewImmToAbs(v uint16, addr uint32) {
	a.emit16(0x33FC)
	a.emit16(v)
	a.emit32(addr)
}

func (a *asm) movelDnToAbs(dn int, addr uint32) {
	a.emit16(0x23C0 | uint16(dn))
	a.emit32(addr)
}

func (a *asm) movelAbsToDn(addr uint32, dn int) {
	a.emit16(0x2039 | uint16(dn)<<9)
	a.emit32(addr)
}

func (a *asm) movebAbsToDn(addr uint32, dn int) {
	a.emit16(0x1039 | uint16(dn)<<9)
	a.emit32(addr)
}

func (a *asm) movebPostIncToDn(an int, dn int) {
	a.emit16(0x1000 | uint16(dn)<<9 | 0x18 | uint16(an))
}

// cmpbDn emits CMP.B srcDn, dstDn (computes dst minus src).
func (a *asm) cmpbDn(srcDn int, dstDn int) {
	a.emit16(0xB000 | uint16(dstDn)<<9 | uint16(srcDn))
}

// btstDn emits BTST #bit, Dn (static bit number form).
func (a *asm) btstDn(bit int, dn int) {
	a.emit16(0x0800 | uint16(dn))
	a.emit16(uint16(bit))
}

func (a *asm) tstL(dn int) { a.emit16(0x4A80 | uint16(dn)) }

func (a *asm) addqL(n int, dn int) {
	a.emit16(0x5080 | uint16(n&7)<<9 | uint16(dn))
}

func (a *asm) addiL(v uint32, dn int) {
	a.emit16(0x0680 | uint16(dn))
	a.emit32(v)
}

func (a *asm) subiL(v uint32, dn int) {
	a.emit16(0x0480 | uint16(dn))
	a.emit32(v)
}

func (a *asm) branch(cond int, label string) {
	a.branchWord(0x6000|uint16(cond)<<8|0x00, label)
}

func (a *asm) bsr(label string) {
	a.branchWord(0x6100, label)
}

func (a *asm) dbra(dn int, label string) {
	a.branchWord(0x51C8|uint16(dn), label)
}

func (a *asm) jmpAbs(addr uint32) {
	a.emit16(0x4EF9)
	a.emit32(addr)
}

func (a *asm) movewAbsToDn(addr uint32, dn int) {
	a.emit16(0x3039 | uint16(dn)<<9)
	a.emit32(addr)
}

func (a *asm) cmpiw(v uint16, dn int) {
	a.emit16(0x0C40 | uint16(dn))
	a.emit16(v)
}

func (a *asm) notw(dn int) { a.emit16(0x4640 | uint16(dn)) }

// movewDnToAbs emits MOVE.W Dn, addr.l (absolute long form).
func (a *asm) movewDnToAbs(dn int, addr uint32) {
	a.emit16(0x33C0 | uint16(dn))
	a.emit32(addr)
}

// bootStubCode assembles the sub CPU boot program. sigAddr is the
// absolute address of the embedded signature string; it is a
// parameter because the string sits right after the code, so its
// address depends on the code length. The code length does not
// depend on sigAddr, so callers assemble once with 0 to measure,
// then again with the true value.
func bootStubCode(sigAddr uint32) []byte {
	a := newAsm(0x200)

	a.mark("start")
	a.movebImmToAbs(0x08, 0xFF8003) // CDC destination = PRG-RAM, H-INT off
	a.moveq(0, 0)                   // D0 = LBA 0
	a.movelImm(0x00020400, 2)       // D2 = PRG-RAM scratch destination
	a.bsr("readsec")
	a.leaAbs(0x00020400, 0) // A0 = scratch (sector 0 image)
	a.leaAbs(sigAddr, 1)    // A1 = embedded signature
	a.moveq(13, 3)          // D3 = 14 bytes minus 1, for DBRA
	a.mark("chk")
	a.movebPostIncToDn(0, 0) // D0 = next scratch byte
	a.movebPostIncToDn(1, 1) // D1 = next signature byte
	a.cmpbDn(1, 0)
	a.branch(ccBNE, "fail")
	a.dbra(3, "chk")
	a.movelAbsToDn(0x00020430, 0) // D0 = IP start LBA (header offset 0x30)
	a.movelAbsToDn(0x00020434, 4) // D4 = IP size in bytes (offset 0x34)
	a.movelImm(0x00026000, 2)     // D2 = IP destination address
	a.mark("load")
	a.tstL(4)
	a.branch(ccBLE, "done")
	a.bsr("readsec")
	a.addqL(1, 0)     // next LBA
	a.addiL(0x800, 2) // next destination sector
	a.subiL(0x800, 4) // bytes remaining
	a.branch(ccBRA, "load")
	a.mark("done")
	a.movebImmToAbs(0x01, 0xFF800F)   // sub flag: boot OK
	a.movewImmToAbs(0xB007, 0xFF8020) // comm status 0: boot signature
	a.jmpAbs(0x00026000)              // enter the IP
	a.mark("fail")
	a.movebImmToAbs(0xEE, 0xFF800F) // sub flag: signature mismatch
	a.branch(ccBRA, "fail")
	a.mark("readsec")
	a.movelDnToAbs(0, 0xFF8038)       // CDC LBA latch = D0
	a.movelDnToAbs(2, 0xFF803E)       // CDC destination latch = D2
	a.movewImmToAbs(0x0001, 0xFF803C) // CDC sector count = 1
	a.movebImmToAbs(0x01, 0xFF8036)   // CDC control: start transfer
	a.mark("poll")
	a.movebAbsToDn(0xFF8037, 1) // D1 = CDC status
	a.btstDn(1, 1)              // test transfer complete bit
	a.branch(ccBEQ, "poll")
	a.rts()
	return a.finish()
}

// BootStubImage builds the sub CPU boot stub image based at sub
// CPU address 0. The image is 0x1000 bytes: vectors at 0 (SSP =
// 0x000A0000, at the top of PRG-RAM) and 4 (PC = 0x00000200), code
// at offset 0x200, and the 14 byte signature string SEGADISCSYSTEM
// embedded right after the code. Everything else is zero.
//
// The program is our own hand-written boot code, not a copy of
// any Sega BIOS: it programs the CDC for PRG-RAM destination,
// reads sector 0 into a PRG-RAM scratch page, verifies the
// SEGADISCSYSTEM signature from the sector against its embedded
// copy, loads the IP (initial program) sectors named by the
// sector 0 header fields at offsets 0x30 (start LBA) and 0x34
// (size in bytes) to sub address 0x026000, sets the sub comm flag
// to 1 and comm status 0 to 0xB007, and jumps to the IP. On a
// signature mismatch it sets the sub flag to 0xEE and spins.
func BootStubImage() []byte {
	img := make([]byte, 0x1000)
	binary.BigEndian.PutUint32(img[0:], 0x000A0000)
	binary.BigEndian.PutUint32(img[4:], 0x00000200)
	measure := bootStubCode(0)
	sigAddr := uint32(0x200 + len(measure))
	code := bootStubCode(sigAddr)
	copy(img[0x200:], code)
	copy(img[sigAddr:], "SEGADISCSYSTEM")
	return img
}

// MainStubROM builds a 0x10000 byte Genesis cartridge image for
// CD-only mode. Vectors: SSP = 0x00FFFE00 at offset 0 and PC =
// 0x00000200 at offset 4. The header at 0x100 holds the 16 bytes
// SEGA GENESIS followed by spaces, which is what the cartridge
// loader checks for. The code at 0x200 releases the sub CPU
// (gate array RES = 1 at 0xA12000), waits for comm status 0 at
// 0xA12020 to read 0x600D (the running IP), echoes the inverted
// comm status 1 word into comm command 0 at 0xA12010, sets the
// main comm flag at 0xA1200E, and idles.
func MainStubROM() []byte {
	rom := make([]byte, 0x10000)
	binary.BigEndian.PutUint32(rom[0:], 0x00FFFE00)
	binary.BigEndian.PutUint32(rom[4:], 0x00000200)
	copy(rom[0x100:], "SEGA GENESIS    ")

	a := newAsm(0x200)
	a.mark("start")
	a.movebImmToAbs(0x01, 0xA12000) // RES = 1, BRQ = 0: release sub CPU
	a.mark("wait")
	a.movewAbsToDn(0xA12020, 0) // D0 = comm status 0
	a.cmpiw(0x600D, 0)
	a.branch(ccBNE, "wait")
	a.movewAbsToDn(0xA12022, 0) // D0 = comm status 1
	a.notw(0)
	a.movewDnToAbs(0, 0xA12010)     // comm command 0 = inverted status 1
	a.movebImmToAbs(0x01, 0xA1200E) // main comm flag = 1
	a.mark("idle")
	a.branch(ccBRA, "idle")
	copy(rom[0x200:], a.finish())
	return rom
}
