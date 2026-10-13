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

func (a *asm) rte() { a.emit16(0x4E73) }

// movemRegToAbs emits MOVEM.L <mask>, addr.l (register to memory,
// absolute long destination): opcode 0x48F9, mask word, address.
func (a *asm) movemRegToAbs(mask uint16, addr uint32) {
	a.emit16(0x48F9)
	a.emit16(mask)
	a.emit32(addr)
}

// movemAbsToReg emits MOVEM.L addr.l, <mask> (memory to register,
// absolute long source): opcode 0x4CF9, mask word, address.
func (a *asm) movemAbsToReg(mask uint16, addr uint32) {
	a.emit16(0x4CF9)
	a.emit16(mask)
	a.emit32(addr)
}

// tstbAbs emits TST.B addr.l (absolute long): opcode 0x4A39.
func (a *asm) tstbAbs(addr uint32) {
	a.emit16(0x4A39)
	a.emit32(addr)
}

// moveToCCRFromAbs emits MOVE addr.l, CCR (absolute long source):
// opcode 0x44F9.
func (a *asm) moveToCCRFromAbs(addr uint32) {
	a.emit16(0x44F9)
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

// cmpil emits CMPI.L #imm32, Dn.
func (a *asm) cmpil(v uint32, dn int) {
	a.emit16(0x0C80 | uint16(dn))
	a.emit32(v)
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
	// Copy sector 0 into Word RAM offset 0, so the main program in
	// the boot sector user bytes from offset 0x200 onward lands at
	// main window address 0x200200. For DDT 2 the destination latch
	// is a Word RAM offset, not a bus address.
	a.movebImmToAbs(0x10, 0xFF8003) // CDC destination = Word RAM
	a.moveq(0, 0)                   // D0 = LBA 0
	a.moveq(0, 2)                   // D2 = Word RAM offset 0
	a.bsr("readsec")
	a.movebImmToAbs(0x08, 0xFF8003) // CDC destination = PRG-RAM again
	a.movelAbsToDn(0x00020430, 0)   // D0 = IP start LBA (header offset 0x30)
	a.movelAbsToDn(0x00020434, 4)   // D4 = IP size in bytes (offset 0x34)
	a.movelImm(0x00026000, 2)       // D2 = IP destination address
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

// BIOS mailbox addresses in PRG-RAM (sub CPU absolute). The
// trampolines below save the caller register image here, and the
// Go BIOS services in bios.go read it and write the result block.
const (
	mbRequestAddr = 0x00025A00 // request byte: 0 idle, 1 pending
	mbFamilyAddr  = 0x00025A01 // family byte: 0 cdbios, 1 buram, 2 cdboot
	mbImageAddr   = 0x00025A04 // 15 longs: D0..D7, A0..A6
	mbResultAddr  = 0x00025A40 // 4 longs: D0, D1, A0, A1
	mbCCRAddr     = 0x00025A50 // result CCR word (low byte is the CCR)
)

// handlerStubCode assembles the BIOS handler block that lives
// right after the boot code and its signature string in the stub
// image: an RTE stub, an RTS stub, a fatal stub (sets the sub
// flag to 0xEE and spins, mirroring the boot fail path), and the
// three family trampolines for the jump table. A trampoline
// saves D0-D7/A0-A6 into the mailbox register image with MOVEM,
// stores its family byte, sets the request byte last, spins on
// the request byte until the Go service clears it, restores
// D0/D1/A0/A1 from the result block with MOVEM, loads the saved
// CCR word into CCR (MOVEM does not touch CCR, so the CCR load
// comes last), and returns. The block uses only relative branches
// and absolute mailbox addresses, so its bytes do not depend on
// where it is placed; base is still passed for label resolution.
// It returns the code and the absolute addresses of each stub.
func handlerStubCode(base int) (code []byte, rteAddr, rtsAddr, fatalAddr uint32, trampAddrs [3]uint32) {
	a := newAsm(base)
	a.mark("rte")
	a.rte()
	a.mark("rts")
	a.rts()
	a.mark("fatal")
	a.movebImmToAbs(0xEE, 0xFF800F) // sub flag: fatal stub entry
	a.branch(ccBRA, "fatal")
	spinNames := [3]string{"spin0", "spin1", "spin2"}
	trampNames := [3]string{"tramp0", "tramp1", "tramp2"}
	for fam := 0; fam < 3; fam++ {
		a.mark(trampNames[fam])
		a.movemRegToAbs(0x7FFF, mbImageAddr) // save D0-D7/A0-A6
		a.movebImmToAbs(uint8(fam), mbFamilyAddr)
		a.movebImmToAbs(0x01, mbRequestAddr) // request byte set last
		a.mark(spinNames[fam])
		a.tstbAbs(mbRequestAddr)
		a.branch(ccBNE, spinNames[fam])
		a.movemAbsToReg(0x0303, mbResultAddr) // restore D0/D1/A0/A1
		a.moveToCCRFromAbs(mbCCRAddr)
		a.rts()
	}
	code = a.finish()
	rteAddr = a.addr("rte")
	rtsAddr = a.addr("rts")
	fatalAddr = a.addr("fatal")
	for fam := 0; fam < 3; fam++ {
		trampAddrs[fam] = a.addr(trampNames[fam])
	}
	return code, rteAddr, rtsAddr, fatalAddr, trampAddrs
}

// JumpTableBase is the sub CPU address of the BIOS jump table in
// the stub image. The table holds 14 six byte JMP absolute long
// entries from 0x5EE0 to 0x5F33, and the RAM overlay window the
// Unit serves for it spans 0x5EE0 to 0x5F3F (jumpTableSize
// bytes). The _buram entry at 0x5F16 and the _cdbios entry at
// 0x5F22 are the documented hard addresses real software calls.
const JumpTableBase = 0x5EE0

const jumpTableSize = 0x60 // 96 bytes: 0x5EE0..0x5F3F

// BootStubImage builds the sub CPU boot stub image based at sub
// CPU address 0. The image is 0x6000 bytes: vectors at 0 (SSP =
// 0x000A0000, at the top of PRG-RAM) and 4 (PC = 0x00000200), an
// ASCII ID string at 0x100, the boot code at offset 0x200 with
// the 14 byte signature string SEGADISCSYSTEM embedded right
// after it, the BIOS handler block (RTE and RTS stubs, a fatal
// stub, and the three family trampolines) after the signature,
// and the BIOS jump table at 0x5EE0. Vectors 2..23 and 32..63
// point at the fatal stub, vector 24 (spurious) points at the
// RTE stub, and autovectors 25..31 point at the jump table
// level entries. Everything else is zero.
//
// The program is our own hand-written boot code, not a copy of
// any Sega BIOS: it programs the CDC for PRG-RAM destination,
// reads sector 0 into a PRG-RAM scratch page, verifies the
// SEGADISCSYSTEM signature from the sector against its embedded
// copy, copies sector 0 into Word RAM offset 0 with the CDC
// destination set to Word RAM and then restored to PRG-RAM,
// loads the IP (initial program) sectors named by the
// sector 0 header fields at offsets 0x30 (start LBA) and 0x34
// (size in bytes) to sub address 0x026000, sets the sub comm flag
// to 1 and comm status 0 to 0xB007, and jumps to the IP. On a
// signature mismatch it sets the sub flag to 0xEE and spins.
func BootStubImage() []byte {
	img := make([]byte, 0x6000)
	binary.BigEndian.PutUint32(img[0:], 0x000A0000)
	binary.BigEndian.PutUint32(img[4:], 0x00000200)
	copy(img[0x100:], "iTaK CD BIOS")

	measure := bootStubCode(0)
	sigAddr := uint32(0x200 + len(measure))
	handlerBase := int(sigAddr) + len("SEGADISCSYSTEM")
	hcode, rteAddr, rtsAddr, fatalAddr, trampAddrs := handlerStubCode(handlerBase)

	putVec := func(n int, addr uint32) {
		binary.BigEndian.PutUint32(img[n*4:], addr)
	}
	for v := 2; v <= 23; v++ {
		putVec(v, fatalAddr)
	}
	putVec(24, rteAddr) // spurious interrupt: just return
	levelEntries := [7]uint32{0x5EE0, 0x5EE6, 0x5EEC, 0x5EF2, 0x5EF8, 0x5EFE, 0x5F04}
	for i, entry := range levelEntries {
		putVec(25+i, entry) // autovectors 25..31: levels 1..7
	}
	for v := 32; v <= 63; v++ {
		putVec(v, fatalAddr)
	}

	code := bootStubCode(sigAddr)
	copy(img[0x200:], code)
	copy(img[sigAddr:], "SEGADISCSYSTEM")
	copy(img[handlerBase:], hcode)

	// Jump table at 0x5EE0: JMP absolute long entries.
	entries := [14]uint32{
		rteAddr,       // 0x5EE0 _level1
		rteAddr,       // 0x5EE6 _level2
		rteAddr,       // 0x5EEC _level3
		rteAddr,       // 0x5EF2 _level4
		rteAddr,       // 0x5EF8 _level5
		rteAddr,       // 0x5EFE _level6
		rteAddr,       // 0x5F04 _level7
		rtsAddr,       // 0x5F0A _usercall0
		rtsAddr,       // 0x5F10 _usercall1
		trampAddrs[1], // 0x5F16 _buram (hard address)
		rtsAddr,       // 0x5F1C _usercall2
		trampAddrs[0], // 0x5F22 _cdbios (hard address)
		trampAddrs[2], // 0x5F28 _cdboot
		rtsAddr,       // 0x5F2E _usercall3
	}
	for i, target := range entries {
		off := JumpTableBase + i*6
		img[off] = 0x4E
		img[off+1] = 0xF9
		binary.BigEndian.PutUint32(img[off+2:], target)
	}
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
// main comm flag at 0xA1200E, and then hands over to a disc main
// program when one is present, otherwise it idles.
//
// Disc main program convention for this synthesized stub: the
// main program lives at boot sector offset 0x200, which is the
// documented boot sector layout. The boot stub copies sector 0
// into Word RAM, so that program sits at main window address
// 0x200200. This stub requires the 8 byte marker ITAKMAIN at
// 0x200200 before jumping to the program entry at 0x200208, so
// discs without the marker keep the legacy idle behavior.
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
	a.movelAbsToDn(0x00200200, 0)   // D0 = first marker longword
	a.cmpil(0x4954414B, 0)          // "ITAK"
	a.branch(ccBNE, "idle")
	a.movelAbsToDn(0x00200204, 0) // D0 = second marker longword
	a.cmpil(0x4D41494E, 0)        // "MAIN"
	a.branch(ccBNE, "idle")
	a.jmpAbs(0x00200208) // disc main program entry
	a.mark("idle")
	a.branch(ccBRA, "idle")
	copy(rom[0x200:], a.finish())
	return rom
}
