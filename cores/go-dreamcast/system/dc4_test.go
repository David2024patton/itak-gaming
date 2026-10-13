package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// DC-4 enginetest gate (the swap-class gate for the core).
//
// A synthetic disc carries an IP.BIN whose program is
// hand-assembled SH-4 (assembled by the small assembler in
// this file; every encoding is the documented one, and most
// are cross-checked against the DC-2 and DC-3 fixture words).
// The machine boots ONLY through BootDisc: the HLE BIOS
// installs the IP at 0x8C008000 and enters it at 0x8C008300,
// and the IP then does what a real IP does: it initializes
// the GD system through the BIOS syscall vectors, asks for
// the TOC, walks the ISO9660 root directory for the boot
// file named in its own header, loads that file sector by
// sector through the BIOS PIOREAD command to 0x8C010000, and
// jumps to it.
//
// The payload (the boot file) then exercises the whole
// machine: it uploads the DC-3 ARM7 sound driver and keys
// the looping PCM tone, polls the Maple controller on port 0
// through a Maple DMA table and records the condition
// words, renders the DC-2 textured quad scene through the
// TA FIFO, arms the vblank-in interrupt, and idles on SLEEP
// while its handler counts vblanks. Milestone words land in
// order at 0x8C300000. The gate asserts the milestones, the
// exact framebuffer probes, the tone period, the recorded
// Maple condition against the host SetButtons state, and a
// mid-run save restoring byte-identical continuation.
//
// The same disc is written under testroms/enginetest for the
// shared harness run through the built desktop .so.

// ----------------------------------------------------------------
// A small two-pass SH-4 assembler for the fixtures. Only the
// instruction forms the fixtures use are provided; encodings
// follow the SH-4 manual.
// ----------------------------------------------------------------

const (
	fixBra = iota
	fixBsr
	fixBf
	fixBt
	fixMovL
)

type sh4Fix struct {
	at    int // word index of the instruction to patch
	kind  int
	label string
}

type sh4Asm struct {
	base   uint32
	words  []uint16
	labels map[string]uint32
	fix    []sh4Fix
}

func newSH4Asm(base uint32) *sh4Asm {
	return &sh4Asm{base: base, labels: map[string]uint32{}}
}

func (a *sh4Asm) pc() uint32 { return a.base + 2*uint32(len(a.words)) }

func (a *sh4Asm) label(name string) { a.labels[name] = a.pc() }

func (a *sh4Asm) emit(ws ...uint16) { a.words = append(a.words, ws...) }

// long emits a 32 bit literal, padding to a 4 byte boundary
// with a NOP first when needed.
func (a *sh4Asm) long(v uint32) {
	if len(a.words)%2 != 0 {
		a.emit(0x0009)
	}
	a.emit(uint16(v), uint16(v>>16))
}

// pool emits a named 32 bit literal: the padding comes
// first, then the label, so the label names the literal
// itself and PC relative loads land exactly on it.
func (a *sh4Asm) pool(name string, v uint32) {
	if len(a.words)%2 != 0 {
		a.emit(0x0009)
	}
	a.label(name)
	a.emit(uint16(v), uint16(v>>16))
}

func (a *sh4Asm) branch(kind int, op uint16, label string) {
	a.fix = append(a.fix, sh4Fix{at: len(a.words), kind: kind, label: label})
	a.emit(op)
}

// bra and bsr also append a NOP delay slot automatically:
// their delay slots always execute, and no fixture wants a
// real instruction there (a result copy in a call's delay
// slot reads the pre-call register, a classic trap).
func (a *sh4Asm) bra(l string) { a.branch(fixBra, 0xA000, l); a.emit(0x0009) }
func (a *sh4Asm) bsr(l string) { a.branch(fixBsr, 0xB000, l); a.emit(0x0009) }

// bf and bt append a NOP delay slot automatically: on SH-4
// the delay slot of a conditional branch executes only when
// the branch is taken, so a real instruction parked there
// would run on exactly the wrong path. Callers that emit
// their own NOP after a bt or bf simply get two.
func (a *sh4Asm) bf(l string) { a.branch(fixBf, 0x8B00, l); a.emit(0x0009) }
func (a *sh4Asm) bt(l string) { a.branch(fixBt, 0x8900, l); a.emit(0x0009) }

// movl emits MOV.L @(label,PC),Rn with the displacement
// patched at assembly time.
func (a *sh4Asm) movl(rn int, label string) {
	a.fix = append(a.fix, sh4Fix{at: len(a.words), kind: fixMovL, label: label})
	a.emit(uint16(0xD000 | rn<<8))
}

// bytes finishes the assembly, resolving every fixup.
func (a *sh4Asm) bytes(t *testing.T) []byte {
	t.Helper()
	for _, f := range a.fix {
		target, ok := a.labels[f.label]
		if !ok {
			t.Fatalf("assembler: undefined label %q", f.label)
		}
		addr := a.base + 2*uint32(f.at)
		disp := int32(target) - int32(addr+4)
		switch f.kind {
		case fixBra, fixBsr:
			if disp%2 != 0 || disp < -4096 || disp > 4094 {
				t.Fatalf("assembler: branch to %q out of range (%d)", f.label, disp)
			}
			a.words[f.at] |= uint16((disp / 2) & 0xFFF)
		case fixBf, fixBt:
			if disp%2 != 0 || disp < -256 || disp > 254 {
				t.Fatalf("assembler: branch to %q out of range (%d)", f.label, disp)
			}
			a.words[f.at] |= uint16((disp / 2) & 0xFF)
		case fixMovL:
			d := int32(target) - int32((addr&^3)+4)
			if d < 0 || d%4 != 0 || d/4 > 255 {
				t.Fatalf("assembler: literal %q out of range (%d)", f.label, d)
			}
			a.words[f.at] |= uint16(d / 4)
		}
	}
	out := make([]byte, 2*len(a.words))
	for i, w := range a.words {
		binary.LittleEndian.PutUint16(out[2*i:], w)
	}
	return out
}

// Instruction encoders. Register operands are plain numbers
// (R0 is 0, R15 is 15). For two-register forms the argument
// order matches the assembly syntax: eMov(dst, src) is
// MOV src,dst; eCmpEq(rn, rm) is CMP/EQ Rm,Rn (equality is
// symmetric, so the order only matters for reading).
func eMovImm(rn, imm int) uint16 { return uint16(0xE000 | rn<<8 | imm&0xFF) }
func eMov(dst, src int) uint16   { return uint16(0x6003 | dst<<8 | src<<4) }
func eAddImm(rn, imm int) uint16 { return uint16(0x7000 | rn<<8 | imm&0xFF) }
func eAdd(dst, src int) uint16   { return uint16(0x300C | dst<<8 | src<<4) }
func eSub(dst, src int) uint16   { return uint16(0x3008 | dst<<8 | src<<4) }
func eCmpEqImm(imm int) uint16   { return uint16(0x8800 | imm&0xFF) }
func eCmpEq(rn, rm int) uint16   { return uint16(0x3000 | rn<<8 | rm<<4) }
func eCmpHS(rn, rm int) uint16   { return uint16(0x3002 | rn<<8 | rm<<4) }
func eCmpGT(rn, rm int) uint16   { return uint16(0x3007 | rn<<8 | rm<<4) }
func eOr(rn, rm int) uint16      { return uint16(0x200B | rn<<8 | rm<<4) }
func eMovLld(d, rn, dst int) uint16 {
	return uint16(0x5000 | dst<<8 | rn<<4 | d&0xF) // MOV.L @(d*4,Rn),dst
}
func eMovLst(d, rn, src int) uint16 {
	return uint16(0x1000 | rn<<8 | src<<4 | d&0xF) // MOV.L src,@(d*4,Rn)
}
func eMovLAt(addr, dst int) uint16 { return uint16(0x6002 | dst<<8 | addr<<4) } // MOV.L @addr,dst
func eMovLSt(addr, src int) uint16 { return uint16(0x2002 | addr<<8 | src<<4) } // MOV.L src,@addr
func eMovLPush(addr, src int) uint16 {
	return uint16(0x2006 | addr<<8 | src<<4) // MOV.L src,@-addr
}
func eMovLPop(addr, dst int) uint16 {
	return uint16(0x6006 | dst<<8 | addr<<4) // MOV.L @addr+,dst
}
func eMovBAt(addr, dst int) uint16    { return uint16(0x6000 | dst<<8 | addr<<4) } // MOV.B @addr,dst
func eMovBAtInc(addr, dst int) uint16 { return uint16(0x6004 | dst<<8 | addr<<4) } // MOV.B @addr+,dst
func eExtUB(rn, rm int) uint16        { return uint16(0x600C | rn<<8 | rm<<4) }    // EXTU.B rm,rn
func eShll(rn int) uint16             { return uint16(0x4000 | rn<<8) }
func eShll2(rn int) uint16            { return uint16(0x4008 | rn<<8) }
func eShll8(rn int) uint16            { return uint16(0x4018 | rn<<8) }
func eShll16(rn int) uint16           { return uint16(0x4028 | rn<<8) }
func eDT(rn int) uint16               { return uint16(0x4010 | rn<<8) }
func eJmp(rn int) uint16              { return uint16(0x402B | rn<<8) }
func eJsr(rn int) uint16              { return uint16(0x400B | rn<<8) }
func eLdcSR(rm int) uint16            { return uint16(0x400E | rm<<8) }
func eLdcVBR(rm int) uint16           { return uint16(0x402E | rm<<8) }
func eStsPR(rn int) uint16            { return uint16(0x002A | rn<<8) }
func eLdsPR(rm int) uint16            { return uint16(0x402A | rm<<8) } // LDS Rm,PR (0x400A is MACH)

const (
	wNOP   = 0x0009
	wRTS   = 0x000B
	wRTE   = 0x002B
	wSLEEP = 0x001B
)

// pushPR saves PR on the stack (through R0 or R1, whichever
// the caller has free at the matching restore).
func pushPR(a *sh4Asm) {
	a.emit(eStsPR(0), eMovLPush(15, 0))
}

func popPR(a *sh4Asm) {
	a.emit(eMovLPop(15, 1), eLdsPR(1))
}

// ----------------------------------------------------------------
// The IP program, entered at 0x8C008300 inside the IP.BIN
// image at 0x8C008000. Image data areas: parameter block at
// image +0x2000, GD status block at +0x2010, TOC buffer at
// +0x2100, sector buffer at +0x4000, vector page at +0x5000
// (VBR = image +0x5000). Persistent registers: R12 = data
// area start FAD, R13 = boot filename length, R14 = image
// base. Subroutines clobber R0 to R8 only.
// ----------------------------------------------------------------

func buildIPProgram(t *testing.T) []byte {
	t.Helper()
	a := newSH4Asm(IPEntryAddr)

	// ---- main ----
	a.label("main")
	a.movl(0, "cSR")
	a.emit(eLdcSR(0)) // SR = 0x40000000: privileged, IMASK 0, FPU on
	a.movl(15, "cSP") // SP = 0x8C00E000 (inside the IP image)
	a.movl(0, "cVBR") // VBR = image + 0x5000
	a.emit(eLdcVBR(0))
	a.movl(14, "cIB") // R14 = image base 0x8C008000

	// Boot filename length: the header field at image +0x60
	// is space padded; count bytes to the first space or NUL.
	a.emit(eMov(1, 14), eAddImm(1, 0x60))
	a.emit(eMovImm(13, 0))
	a.label("len_loop")
	a.emit(eMovBAt(1, 0), eCmpEqImm(0x20))
	a.bt("len_done")
	a.emit(eCmpEqImm(0))
	a.bt("len_done")
	a.emit(eAddImm(13, 1), eAddImm(1, 1))
	a.emit(eMov(0, 13), eCmpEqImm(16))
	a.bt("len_done")
	a.bra("len_loop")
	a.emit(wNOP)
	a.label("len_done")

	// GD command INIT (24) with a zeroed parameter block.
	a.movl(1, "cParams")
	a.emit(eMovImm(0, 0))
	for d := 0; d < 4; d++ {
		a.emit(eMovLst(d, 1, 0))
	}
	a.emit(eMovImm(4, 24), eMov(5, 1))
	a.bsr("gd_cmd")
	a.emit(wNOP)
	a.emit(eCmpEqImm(1))
	a.bt("init_ok")
	a.bra("fail")
	a.emit(wNOP)
	a.label("init_ok")

	// GD command GETTOC2 (19): params session 0, TOC buffer.
	a.movl(1, "cParams")
	a.emit(eMovImm(0, 0), eMovLst(0, 1, 0))
	a.movl(0, "cTOC")
	a.emit(eMovLst(1, 1, 0))
	a.emit(eMovImm(0, 0), eMovLst(2, 1, 0), eMovLst(3, 1, 0))
	a.emit(eMovImm(4, 19), eMov(5, 1))
	a.bsr("gd_cmd")
	a.emit(wNOP)
	a.emit(eCmpEqImm(1))
	a.bt("toc_ok")
	a.bra("fail")
	a.emit(wNOP)
	a.label("toc_ok")

	// Data area start FAD: TOC word [2 + 2*(sessions-1)].
	a.movl(1, "cTOC")
	a.emit(eMovLAt(1, 0))  // sessions
	a.emit(eAddImm(0, -1)) // sessions - 1
	a.emit(eShll(0))       // 2*(sessions-1)
	a.emit(eAddImm(0, 2))  // word index
	a.emit(eShll2(0))      // byte offset
	a.emit(eAdd(0, 1))     // address of the session start word
	a.emit(eMovLAt(0, 12)) // R12 = data start FAD

	// Read the PVD (relative sector 16) into the sector buffer.
	a.emit(eMov(4, 12), eAddImm(4, 16), eMovImm(5, 1))
	a.movl(6, "cSector")
	a.bsr("gd_read")
	a.emit(wNOP)
	a.emit(eCmpEqImm(1))
	a.bt("pvd_ok")
	a.bra("fail")
	a.emit(wNOP)
	a.label("pvd_ok")

	// Root directory record in the PVD at sector +156: extent
	// is the unaligned word at +158, size at +166.
	a.movl(1, "cRootRec")
	a.emit(eAddImm(1, 2))
	a.bsr("load32u")
	a.emit(eMov(9, 0)) // R9 = root extent (relative sector)
	a.movl(1, "cRootRec")
	a.emit(eAddImm(1, 10))
	a.bsr("load32u")
	a.emit(eMov(10, 0)) // R10 = root size in bytes

	// Walk the root directory sector by sector. Loop state
	// lives in R9 and R11: gd_cmd keeps its request id in
	// R8, so R8 is call-clobbered like R0 to R7.
	a.emit(eAdd(9, 12))  // R9 = current directory FAD
	a.emit(eMov(11, 10)) // R11 = bytes remaining
	a.label("dir_loop")
	a.emit(eMov(4, 9), eMovImm(5, 1))
	a.movl(6, "cSector")
	a.bsr("gd_read")
	a.emit(wNOP)
	a.emit(eCmpEqImm(1))
	a.bt("dir_read_ok")
	a.bra("fail")
	a.emit(wNOP)
	a.label("dir_read_ok")
	a.bsr("scan_dir")
	a.emit(wNOP)
	a.emit(eCmpEqImm(1))
	a.bt("found")
	a.emit(eAddImm(9, 1))
	a.movl(0, "c2048")
	a.emit(eSub(11, 0)) // remaining -= 2048
	a.emit(eMovImm(0, 0), eCmpGT(11, 0))
	a.bt("dir_loop")
	a.bra("fail")
	a.emit(wNOP)

	// Load the boot file: R9 = absolute FAD, R10 = size.
	a.label("found")
	a.emit(eMovImm(11, 0)) // sector index
	a.emit(eMovImm(12, 0)) // bytes loaded
	a.label("load_loop")
	a.emit(eCmpHS(12, 10)) // loaded >= size, unsigned
	a.bt("load_done")
	a.emit(eMov(4, 9), eAdd(4, 11), eMovImm(5, 1))
	a.emit(eMov(6, 11), eShll8(6), eShll2(6), eShll(6)) // dest offset = i * 2048
	a.movl(0, "cBoot")
	a.emit(eAdd(6, 0))
	a.bsr("gd_read")
	a.emit(wNOP)
	a.emit(eCmpEqImm(1))
	a.bt("load_ok")
	a.bra("fail")
	a.emit(wNOP)
	a.label("load_ok")
	a.emit(eAddImm(11, 1))
	a.movl(0, "c2048")
	a.emit(eAdd(12, 0))
	a.bra("load_loop")
	a.emit(wNOP)
	a.label("load_done")

	// Milestone 1: the IP loaded the program. Jump to it.
	a.movl(1, "cMile")
	a.emit(eMovImm(0, 1), eMovLSt(1, 0))
	a.movl(0, "cBoot")
	a.emit(eJmp(0))
	a.emit(wNOP)

	a.label("fail")
	a.movl(1, "cMile")
	a.movl(0, "cDead")
	a.emit(eMovLSt(1, 0))
	a.label("fail_spin")
	a.bra("fail_spin")
	a.emit(wNOP)

	// ---- gd_cmd: R4 = command, R5 = parameter block pointer.
	// Runs REQ_CMD, EXEC_SERVER, then polls GET_CMD_STAT
	// until the request completes or errors. Returns the
	// final status (1 complete, 2 error) in R0. ----
	a.label("gd_cmd")
	pushPR(a)
	// REQ_CMD: R7 = 0, R6 = 0.
	a.movl(1, "cVecBC")
	a.emit(eMovLAt(1, 1))
	a.emit(eMovImm(6, 0), eMovImm(7, 0), eJsr(1), wNOP)
	a.emit(eMov(8, 0)) // R8 = request id
	a.emit(eCmpEqImm(0))
	a.bt("gd_fail") // id 0 means the interface stayed busy
	// EXEC_SERVER: R7 = 2, R6 = 0.
	a.movl(1, "cVecBC")
	a.emit(eMovLAt(1, 1))
	a.emit(eMovImm(6, 0), eMovImm(7, 2), eJsr(1), wNOP)
	a.label("gd_poll")
	// GET_CMD_STAT: R4 = id, R5 = status block, R7 = 1.
	a.movl(1, "cVecBC")
	a.emit(eMovLAt(1, 1))
	a.emit(eMov(4, 8))
	a.movl(5, "cStatus")
	a.emit(eMovImm(6, 0), eMovImm(7, 1), eJsr(1), wNOP)
	a.emit(eCmpEqImm(0)) // 0 = still busy
	a.bt("gd_poll")
	a.emit(wNOP)
	// R0 already holds the final status.
	popPR(a)
	a.emit(wRTS, wNOP)
	a.label("gd_fail")
	a.emit(eMovImm(0, 2))
	popPR(a)
	a.emit(wRTS, wNOP)

	// ---- gd_read: R4 = FAD, R5 = sector count, R6 = dest.
	// Fills the parameter block and issues PIOREAD (16)
	// through gd_cmd. Returns gd_cmd's status in R0. ----
	a.label("gd_read")
	pushPR(a)
	a.movl(1, "cParams")
	a.emit(eMovLst(0, 1, 4), eMovLst(1, 1, 5), eMovLst(2, 1, 6))
	a.emit(eMovImm(0, 0), eMovLst(3, 1, 0))
	a.emit(eMovImm(4, 16), eMov(5, 1))
	a.bsr("gd_cmd")
	a.emit(wNOP)
	popPR(a)
	a.emit(wRTS, wNOP)

	// ---- load32u: R1 = unaligned address. Returns the
	// little-endian word there in R0. Clobbers R0, R1, R2. ----
	a.label("load32u")
	a.emit(eMovBAtInc(1, 0), eExtUB(0, 0))
	a.emit(eMovBAtInc(1, 2), eExtUB(2, 2), eShll8(2), eOr(0, 2))
	a.emit(eMovBAtInc(1, 2), eExtUB(2, 2), eShll16(2), eOr(0, 2))
	a.emit(eMovBAtInc(1, 2), eExtUB(2, 2), eShll16(2), eShll8(2), eOr(0, 2))
	a.emit(wRTS, wNOP)

	// ---- scan_dir: scans the sector in the sector buffer
	// for the boot filename (R13 = its length, the name
	// itself at R14 + 0x60). On a match returns R0 = 1 with
	// R9 = absolute FAD and R10 = size; else R0 = 0. ----
	a.label("scan_dir")
	pushPR(a)
	a.movl(1, "cSector")    // cursor
	a.movl(2, "cSectorEnd") // end of sector
	a.label("sd_loop")
	a.emit(eCmpHS(1, 2)) // cursor >= end
	a.bt("sd_notfound")
	a.emit(eMovBAt(1, 0), eExtUB(0, 0), eCmpEqImm(0))
	a.bt("sd_notfound") // zero record length: padding
	a.emit(eMov(3, 0))  // R3 = record length
	// Name length at cursor + 32.
	a.emit(eMov(4, 1), eAddImm(4, 32), eMovBAt(4, 5), eExtUB(5, 5))
	a.emit(eMov(0, 13), eCmpEq(5, 0)) // nameLen == H
	a.bt("sd_cmp")
	a.emit(eMov(0, 13), eAddImm(0, 2), eCmpEq(5, 0)) // nameLen == H + 2
	a.bt("sd_cmp2")
	a.bra("sd_next")
	a.emit(wNOP)
	a.label("sd_cmp2") // the ";1" version suffix must follow
	a.emit(eMov(4, 1), eAddImm(4, 33), eAdd(4, 13))
	a.emit(eMovBAt(4, 0), eExtUB(0, 0), eCmpEqImm(0x3B)) // ';'
	a.bf("sd_next")
	a.emit(eAddImm(4, 1))
	a.emit(eMovBAt(4, 0), eExtUB(0, 0), eCmpEqImm(0x31)) // '1'
	a.bf("sd_next")
	a.bra("sd_cmp")
	a.emit(wNOP)
	a.label("sd_cmp")
	a.emit(eMov(4, 1), eAddImm(4, 33)) // record name
	a.emit(eMov(5, 14), eAddImm(5, 0x60))
	a.emit(eMovImm(6, 0)) // index
	a.label("sd_cmp_loop")
	a.emit(eCmpEq(6, 13))
	a.bt("sd_match")
	a.emit(eMovBAt(4, 0), eExtUB(0, 0))
	a.emit(eMovBAt(5, 2), eExtUB(2, 2))
	a.emit(eCmpEq(2, 0))
	a.bf("sd_next")
	a.emit(eAddImm(4, 1), eAddImm(5, 1), eAddImm(6, 1))
	a.bra("sd_cmp_loop")
	a.emit(wNOP)
	a.label("sd_next")
	a.emit(eAdd(1, 3))
	a.bra("sd_loop")
	a.emit(wNOP)
	a.label("sd_match")
	a.emit(eMov(7, 1), eAddImm(1, 2))
	a.bsr("load32u")
	a.emit(wNOP)
	a.emit(eMov(9, 0), eAdd(9, 12)) // absolute FAD
	a.emit(eMov(1, 7), eAddImm(1, 10))
	a.bsr("load32u")
	a.emit(wNOP)
	a.emit(eMov(10, 0))
	a.emit(eMovImm(0, 1))
	popPR(a)
	a.emit(wRTS, wNOP)
	a.label("sd_notfound")
	a.emit(eMovImm(0, 0))
	popPR(a)
	a.emit(wRTS, wNOP)

	// ---- literal pool ----
	a.pool("cSR", 0x40000000)
	a.pool("cSP", 0x8C00E000)
	a.pool("cVBR", 0x8C00D000)
	a.pool("cIB", 0x8C008000)
	a.pool("cVecBC", 0x8C0000BC)
	a.pool("cParams", 0x8C00A000)
	a.pool("cStatus", 0x8C00A010)
	a.pool("cTOC", 0x8C00A100)
	a.pool("cSector", 0x8C00C000)
	a.pool("cSectorEnd", 0x8C00C800)
	a.pool("cRootRec", 0x8C00C09C)
	a.pool("cBoot", 0x8C010000)
	a.pool("cMile", 0x8C300000)
	a.pool("cDead", 0xDEAD0001)
	a.pool("c2048", 2048)

	return a.bytes(t)
}

// ----------------------------------------------------------------
// The payload (the boot file), loaded at 0x8C010000. Data:
// ARM driver image at +0x4000, PCM loop at +0x4200, twiddled
// texture at +0x4400, TA packet stream at +0x4600, vector
// page at +0x8000 with the vblank handler at +0x8600.
// ----------------------------------------------------------------

const (
	dc4MilePhys  = 0x0C300000
	dc4MileVA    = 0x8C300000
	dc4PayloadVA = 0x8C010000
)

func buildPayloadProgram(t *testing.T) []byte {
	t.Helper()
	a := newSH4Asm(dc4PayloadVA)

	a.label("main")
	a.movl(0, "cSR")
	a.emit(eLdcSR(0))
	a.movl(15, "cSP") // SP = 0x8C200000
	a.movl(0, "cVBR") // VBR = payload + 0x8000
	a.emit(eLdcVBR(0))

	// Sound: copy the ARM driver (69 words) to AICA RAM,
	// then the PCM loop (32 words) to AICA RAM + 0x1000,
	// then release the ARM7 reset.
	a.movl(1, "cAicaRAM")
	a.movl(2, "cDriver")
	a.emit(eMovImm(3, 69))
	a.label("cp1")
	a.emit(eMovLPop(2, 0), eMovLSt(1, 0), eAddImm(1, 4), eDT(3))
	a.bf("cp1")
	a.emit(wNOP)
	a.movl(1, "cPcmDst")
	a.movl(2, "cPCM")
	a.emit(eMovImm(3, 32))
	a.label("cp2")
	a.emit(eMovLPop(2, 0), eMovLSt(1, 0), eAddImm(1, 4), eDT(3))
	a.bf("cp2")
	a.emit(wNOP)
	a.movl(1, "cArmRst")
	a.emit(eMovImm(0, 0), eMovLSt(1, 0))
	// Milestone 2: sound started.
	a.movl(1, "cMile")
	a.emit(eMovImm(0, 2), eMovLst(1, 1, 0))

	// Maple: one table entry, port 0 Get Condition.
	a.movl(1, "cTable")
	a.movl(0, "cE0Hdr")
	a.emit(eMovLst(0, 1, 0))
	a.movl(0, "cRecvPhys")
	a.emit(eMovLst(1, 1, 0))
	a.movl(0, "cFrame0")
	a.emit(eMovLst(2, 1, 0))
	a.movl(0, "cFrame1")
	a.emit(eMovLst(3, 1, 0))
	a.movl(1, "cMDSTAR")
	a.movl(0, "cTablePhys")
	a.emit(eMovLSt(1, 0))
	a.movl(1, "cMDEN")
	a.emit(eMovImm(0, 1), eMovLSt(1, 0))
	a.movl(1, "cMDST")
	a.emit(eMovLSt(1, 0)) // R0 still holds 1: transfer runs
	// Record the four receive words at milestone +8..+11.
	a.movl(1, "cRecv")
	a.movl(2, "cMile")
	for i := 0; i < 4; i++ {
		a.emit(eMovLld(i, 1, 0), eMovLst(8+i, 2, 0))
	}
	// Milestone 3: the Maple poll landed.
	a.emit(eMovImm(0, 3), eMovLst(2, 2, 0))

	// PVR texture upload through the 64 bit VRAM window:
	// backing word k of the texture at VRAM 0x1000 goes to
	// window offset 2*(0x1000 + 4k).
	a.movl(1, "cTexWin")
	a.movl(2, "cTexture")
	a.emit(eMovImm(3, 32)) // 32 words = 128 bytes
	a.label("cp3")
	a.emit(eMovLPop(2, 0), eMovLSt(1, 0), eAddImm(1, 8), eDT(3))
	a.bf("cp3")
	a.emit(wNOP)

	// PVR registers. R1 = register base, R2 = base + 0x40,
	// R3 = base + 0x80.
	a.movl(1, "cPvrBase")
	a.emit(eMov(2, 1), eAddImm(2, 64))
	a.emit(eMov(3, 2), eAddImm(3, 64))
	a.emit(eMovImm(0, 3), eMovLst(2, 2, 0)) // FB_W_CTRL (0x48): enable, 565
	a.emit(eMovImm(0, 0), eMovLst(8, 2, 0)) // FB_W_SOF1 (0x60) = 0
	a.movl(0, "cFBRctrl")
	a.emit(eMovLst(1, 2, 0))                // FB_R_CTRL (0x44) = 0x800003
	a.emit(eMovImm(0, 0), eMovLst(4, 2, 0)) // FB_R_SOF1 (0x50) = 0
	a.movl(0, "cFBRsize")
	a.emit(eMovLst(7, 2, 0)) // FB_R_SIZE (0x5C) = 63<<10|31
	a.movl(0, "cClip")
	a.emit(eMovLst(10, 2, 0)) // FB_X_CLIP (0x68) = 63<<16
	a.emit(eMovLst(11, 2, 0)) // FB_Y_CLIP (0x6C) = 63<<16
	a.movl(0, "cBG")
	a.emit(eMovLst(6, 3, 0))                // ISP_BACKGND_S (0x98) = opaque black
	a.emit(eMovImm(0, 0), eMovLst(7, 3, 0)) // ISP_BACKGND_D (0x9C) = 0
	a.movl(4, "cTAISPB")
	a.movl(0, "cISPBase")
	a.emit(eMovLSt(4, 0)) // TA_ISP_BASE = 0x10000
	a.movl(4, "cTAInit")
	a.emit(eMovImm(0, 0), eMovLSt(4, 0)) // TA_LIST_INIT
	// TA packet stream: 40 words from payload data to the FIFO.
	a.movl(1, "cTAFIFO")
	a.movl(2, "cTAData")
	a.emit(eMovImm(3, 40))
	a.label("cp4")
	a.emit(eMovLPop(2, 0), eMovLSt(1, 0), eDT(3))
	a.bf("cp4")
	a.emit(wNOP)
	// STARTRENDER.
	a.movl(1, "cStartRender")
	a.emit(eMovImm(0, 0), eMovLSt(1, 0))
	// Milestone 4: the scene rendered.
	a.movl(1, "cMile")
	a.emit(eMovImm(0, 4), eMovLst(3, 1, 0))

	// Arm the vblank-in interrupt (Holly mask level 2, bit 3).
	a.movl(1, "cIML2NRM")
	a.emit(eMovImm(0, 8), eMovLSt(1, 0))
	// Idle: wait for the handler's vblank count, record
	// milestone 5, then sleep forever between vblanks.
	a.movl(1, "cMile")
	a.movl(2, "cMileCtr") // milestone word 16, past disp range
	a.label("wait_vb")
	a.emit(eMovLAt(2, 0))
	a.emit(eCmpEqImm(0))
	a.bt("wait_vb")
	a.emit(wNOP)
	a.emit(eMovImm(0, 5), eMovLst(4, 1, 0))
	a.label("idle")
	a.emit(wSLEEP)
	a.bra("idle")
	a.emit(wNOP)

	// ---- literal pool ----
	a.pool("cSR", 0x40000000)
	a.pool("cSP", 0x8C200000)
	a.pool("cVBR", 0x8C018000)
	a.pool("cAicaRAM", 0xA0800000)
	a.pool("cDriver", dc4PayloadVA+0x4000)
	a.pool("cPcmDst", 0xA0801000)
	a.pool("cPCM", dc4PayloadVA+0x4200)
	a.pool("cArmRst", 0xA0702C00)
	a.pool("cMile", dc4MileVA)
	a.pool("cTable", 0x8C210000)
	a.pool("cE0Hdr", 0x80000001) // last entry, port 0, 2 frame words
	a.pool("cRecvPhys", 0x0C220000)
	a.pool("cFrame0", 0x09200001) // Get Condition, port 0
	a.pool("cFrame1", 0x01000000) // controller function code
	a.pool("cMDSTAR", 0xA05F6C04)
	a.pool("cTablePhys", 0x0C210000)
	a.pool("cMDEN", 0xA05F6C14)
	a.pool("cMDST", 0xA05F6C18)
	a.pool("cRecv", 0x8C220000)
	a.pool("cTexWin", 0xA5002000) // 64 bit VRAM window at backing 0x1000
	a.pool("cTexture", dc4PayloadVA+0x4400)
	a.pool("cPvrBase", 0xA05F8000)
	a.pool("cFBRctrl", 0x800003)
	a.pool("cFBRsize", 63<<10|31)
	a.pool("cClip", 63<<16)
	a.pool("cBG", 0xFF000000)
	a.pool("cTAISPB", 0xA05F8128)
	a.pool("cISPBase", 0x10000)
	a.pool("cTAInit", 0xA05F8140)
	a.pool("cTAFIFO", 0xA0500000)
	a.pool("cTAData", dc4PayloadVA+0x4600)
	a.pool("cStartRender", 0xA05F8014)
	a.pool("cIML2NRM", 0xA05F6910)
	a.pool("cMileCtr", dc4MileVA+64)

	return a.bytes(t)
}

// buildVblankHandler assembles the payload's interrupt
// handler at VBR + 0x600 = 0x8C018600: count the vblank at
// milestone word 16 and acknowledge ISTNRM bit 3.
func buildVblankHandler(t *testing.T) []byte {
	t.Helper()
	a := newSH4Asm(0x8C018600)
	a.emit(eMovLPush(15, 0), eMovLPush(15, 1))
	a.movl(0, "cCtr")
	a.emit(eMovLAt(0, 1), eAddImm(1, 1), eMovLSt(0, 1))
	a.movl(0, "cIstnrm")
	a.emit(eMovImm(1, 8), eMovLSt(0, 1))
	a.emit(eMovLPop(15, 1), eMovLPop(15, 0))
	a.emit(wRTE, wNOP)
	a.pool("cCtr", dc4MileVA+64) // milestone word 16
	a.pool("cIstnrm", 0xA05F6900)
	return a.bytes(t)
}

// ----------------------------------------------------------------
// Fixture builders: IP.BIN image, payload image, ISO, .gdi.
// ----------------------------------------------------------------

// padIPField returns s padded with spaces to width bytes.
func padIPField(s string, width int) string {
	for len(s) < width {
		s += " "
	}
	return s[:width]
}

// buildIPImage crafts the 32 KB IP.BIN: the documented
// metadata fields, the IP program at the entry offset 0x300,
// and the two VBR slots the IP's vector page needs (a spin
// at +0x100, an RTE at +0x600; the IP arms no interrupts, so
// both are pure safety nets).
func buildIPImage(t *testing.T) []byte {
	t.Helper()
	img := make([]byte, 0x8000)
	put := func(off int, s string, width int) {
		copy(img[off:off+width], padIPField(s, width))
	}
	put(0x00, "SEGA SEGAKATANA", 16)
	put(0x10, "ITAK MAKER", 16)
	put(0x20, "GD-ROM1/1", 16)
	put(0x30, "JUE", 8)
	put(0x38, "E000F10", 16)
	put(0x48, "T-12345", 10)
	put(0x52, "V1.000", 6)
	put(0x58, "20261013", 8)
	put(0x60, "1ST_READ.BIN", 16)
	put(0x70, "ITAK", 16)
	put(0x80, "ITAK GODC ENGINETEST", 128)
	copy(img[0x300:], buildIPProgram(t))
	binary.LittleEndian.PutUint16(img[0x5100:], 0xAFFE) // BRA -2: spin
	binary.LittleEndian.PutUint16(img[0x5102:], wNOP)
	binary.LittleEndian.PutUint16(img[0x5600:], wRTE)
	binary.LittleEndian.PutUint16(img[0x5602:], wNOP)
	return img
}

// dc4Texture builds the 8x8 RGB565 texture in twiddled
// order, the same texel pattern the DC-2 gate pins: red,
// green, and blue corners over white.
func dc4Texture() []byte {
	texel := func(x, y int) uint16 {
		switch {
		case x == 0 && y == 0:
			return 0xF800
		case x == 1 && y == 0:
			return 0x07E0
		case x == 0 && y == 1:
			return 0x001F
		}
		return 0xFFFF
	}
	img := make([]byte, 8*8*2)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			binary.LittleEndian.PutUint16(img[twiddle(x, y, 8, 8)*2:], texel(x, y))
		}
	}
	return img
}

// dc4TAStream builds the payload's TA packet stream: the
// DC-2 gate's polygon header and four vertices verbatim,
// plus the end of list packet.
func dc4TAStream() []uint32 {
	f32 := math.Float32bits
	out := []uint32{
		4 << 29,              // polygon header, opaque list
		1<<6 | 7,             // ISP/TSP: textured, depth always
		1 | 1<<24,            // TSP: bilinear, modulate
		1<<27 | (0x1000 / 8), // TCW: 565 texture at VRAM 0x1000
		0xFFFFFFFF, 0, 0, 0,  // base color white
	}
	vtx := func(x, y, u, v float32, eos bool) {
		pcw := uint32(7 << 29)
		if eos {
			pcw |= 1 << 28
		}
		out = append(out, pcw, f32(x), f32(y), f32(1), f32(u), f32(v), 0xFFFFFFFF, 0)
	}
	vtx(0.5, 0.5, 0, 0, false)
	vtx(64.5, 0.5, 1, 0, false)
	vtx(0.5, 64.5, 0, 1, false)
	vtx(64.5, 64.5, 1, 1, true)
	out = append(out, make([]uint32, 8)...) // end of list
	return out
}

// buildPayloadImage assembles the boot file image.
func buildPayloadImage(t *testing.T) []byte {
	t.Helper()
	img := make([]byte, 0x8700)
	copy(img[0:], buildPayloadProgram(t))
	for i, w := range armDriverWords {
		binary.LittleEndian.PutUint32(img[0x4000+4*i:], w)
	}
	for i := 0; i < 64; i++ {
		binary.LittleEndian.PutUint16(img[0x4200+2*i:], uint16(dc3PCM(i)))
	}
	copy(img[0x4400:], dc4Texture())
	for i, w := range dc4TAStream() {
		binary.LittleEndian.PutUint32(img[0x4600+4*i:], w)
	}
	copy(img[0x8600:], buildVblankHandler(t))
	return img
}

// dc4DirRecord builds one ISO9660 directory record, the
// layout the gd fixtures use.
func dc4DirRecord(extent, size uint32, isDir bool, name []byte) []byte {
	recLen := 33 + len(name)
	if len(name)%2 == 0 {
		recLen++
	}
	rec := make([]byte, recLen)
	rec[0] = byte(recLen)
	binary.LittleEndian.PutUint32(rec[2:6], extent)
	binary.BigEndian.PutUint32(rec[6:10], extent)
	binary.LittleEndian.PutUint32(rec[10:14], size)
	binary.BigEndian.PutUint32(rec[14:18], size)
	copy(rec[18:25], []byte{126, 10, 13, 12, 0, 0, 0})
	if isDir {
		rec[25] = 0x02
	}
	binary.LittleEndian.PutUint16(rec[28:30], 1)
	binary.BigEndian.PutUint16(rec[30:32], 1)
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}

// buildDC4ISO builds the data area image: the IP.BIN boot
// area in sectors 0 to 15, the PVD at 16, the root directory
// at 20, and the payload at 22.
func buildDC4ISO(t *testing.T) []byte {
	t.Helper()
	payload := buildPayloadImage(t)
	payloadSectors := (len(payload) + 2047) / 2048
	total := 22 + payloadSectors
	img := make([]byte, total*2048)
	copy(img, buildIPImage(t))

	pvd := make([]byte, 2048)
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	pvd[6] = 1
	for i := 8; i < 72; i++ {
		pvd[i] = ' '
	}
	copy(pvd[8:], "SEGA")
	copy(pvd[40:], "ITAKGODC")
	binary.LittleEndian.PutUint32(pvd[80:84], uint32(total))
	binary.BigEndian.PutUint32(pvd[84:88], uint32(total))
	binary.LittleEndian.PutUint16(pvd[112:114], 2048)
	binary.BigEndian.PutUint16(pvd[114:116], 2048)
	copy(pvd[156:], dc4DirRecord(20, 2048, true, []byte{0x00}))
	copy(img[16*2048:], pvd)

	root := make([]byte, 0, 2048)
	root = append(root, dc4DirRecord(20, 2048, true, []byte{0x00})...)
	root = append(root, dc4DirRecord(20, 2048, true, []byte{0x01})...)
	root = append(root, dc4DirRecord(22, uint32(len(payload)), false, []byte("1ST_READ.BIN;1"))...)
	copy(img[20*2048:], root)

	copy(img[22*2048:], payload)
	return img
}

// wrap2352DC4 wraps 2048 byte sectors into raw 2352 byte
// Mode 1 sectors (12 byte sync, 4 byte header, data, 288
// bytes of filler), the gd fixture layout.
func wrap2352DC4(iso []byte) []byte {
	sectors := len(iso) / 2048
	out := make([]byte, 0, sectors*2352)
	sync := []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}
	for i := 0; i < sectors; i++ {
		out = append(out, sync...)
		total := i + 150
		out = append(out, byte(total/4500), byte((total/75)%60), byte(total%75), 0x01)
		out = append(out, iso[i*2048:(i+1)*2048]...)
		out = append(out, make([]byte, 288)...)
	}
	return out
}

// writeDC4GDI writes the harness disc set into dir: a
// two-track .gdi (a short audio track at LBA 0 and the data
// track at LBA 45000, the standard GD layout) and returns
// the .gdi path.
func writeDC4GDI(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	iso := buildDC4ISO(t)
	audio := make([]byte, 10*2352)
	if err := os.WriteFile(filepath.Join(dir, "track01.bin"), audio, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "track03.bin"), wrap2352DC4(iso), 0o644); err != nil {
		t.Fatal(err)
	}
	gdi := "2\n1 0 0 2352 track01.bin 0\n3 45000 4 2352 track03.bin 0\n"
	p := filepath.Join(dir, "game.gdi")
	if err := os.WriteFile(p, []byte(gdi), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// dc4Pad is the host pad state the gate holds while booting:
// A and Start pressed, left trigger pulled to 200, stick at
// (30, 200). The expected Maple condition words are the ones
// the DC-3 gate pins for exactly this state.
var dc4Pad = PadState{Mask: BtnA | BtnStart, TriggerL: 200, JoyX: 30, JoyY: 200}

var dc4WantCond = [4]uint32{0x07002003, 0x01000000, 0xFFF300C8, 0x8080C81E}

// bootDC4 loads and boots a disc image, then runs frames
// until every milestone stage has been seen or the budget
// runs out. It returns the machine and the frame index at
// which each of the five stages was first observed (-1 when
// a stage never landed).
func bootDC4(t *testing.T, path string, maxFrames int) (*Machine, [5]int) {
	t.Helper()
	m := New()
	m.SetButtons(dc4Pad)
	if err := m.LoadDisc(path); err != nil {
		t.Fatalf("LoadDisc(%s): %v", path, err)
	}
	if err := m.BootDisc(); err != nil {
		t.Fatalf("BootDisc(%s): %v", path, err)
	}
	if !m.Booted() {
		t.Fatal("Booted() = false after a successful BootDisc")
	}
	var first [5]int
	for i := range first {
		first[i] = -1
	}
	for frame := 0; frame < maxFrames; frame++ {
		m.RunFrame()
		done := true
		for s := 0; s < 5; s++ {
			if first[s] < 0 {
				if m.Bus.Read32(dc4MilePhys+uint32(4*s)) == uint32(s+1) {
					first[s] = frame
				} else {
					done = false
				}
			}
		}
		if done {
			// A few more frames so the vblank counter and
			// the tone both accumulate past their gates.
			for i := 0; i < 3; i++ {
				m.RunFrame()
			}
			return m, first
		}
	}
	return m, first
}

// checkDC4Milestones asserts the ordered milestone stages.
func checkDC4Milestones(t *testing.T, m *Machine, first [5]int) {
	t.Helper()
	for s := 0; s < 5; s++ {
		if first[s] < 0 {
			word := m.Bus.Read32(dc4MilePhys + uint32(4*s))
			t.Errorf("milestone stage %d never landed (word = %#08x, PC = %#08x)",
				s+1, word, m.CPU.PC)
		}
	}
	for s := 1; s < 5; s++ {
		if first[s] >= 0 && first[s-1] >= 0 && first[s] < first[s-1] {
			t.Errorf("milestones out of order: stage %d at frame %d, stage %d at frame %d",
				s+1, first[s], s, first[s-1])
		}
	}
	// The recorded Maple condition matches the host pad.
	for i, want := range dc4WantCond {
		if got := m.Bus.Read32(dc4MilePhys + uint32(4*(8+i))); got != want {
			t.Errorf("recorded condition word %d = %#08x, want %#08x", i, got, want)
		}
	}
	// The boot used only implemented BIOS calls.
	if calls := m.UnimplementedBIOSCalls(); len(calls) != 0 {
		t.Errorf("unimplemented BIOS calls during boot: %v", calls)
	}
}

// checkDC4Scene asserts the exact rendered probes.
func checkDC4Scene(t *testing.T, m *Machine) {
	t.Helper()
	if got := m.PVR.RenderCount(); got < 1 {
		t.Errorf("RenderCount = %d, want at least 1", got)
	}
	fb16 := func(x, y int) uint16 {
		return m.Bus.Read16(0x04000000 + uint32((y*64+x)*2))
	}
	for _, c := range []struct {
		x, y int
		want uint16
		what string
	}{
		{4, 4, 0xF800, "texel (0,0) red"},
		{12, 4, 0x07E0, "texel (1,0) green"},
		{8, 4, 0x8400, "bilinear midpoint red/green"},
		{4, 12, 0x001F, "texel (0,1) blue"},
		{60, 60, 0xFFFF, "texel (7,7) white"},
	} {
		if got := fb16(c.x, c.y); got != c.want {
			t.Errorf("fb(%d,%d) = %#04x, want %#04x (%s)", c.x, c.y, got, c.want, c.what)
		}
	}
	if got := m.Framebuffer()[4*Width+4]; got != 0xFFFF0000 {
		t.Errorf("presented (4,4) = %#08x, want 0xFFFF0000 (red texel)", got)
	}
	if got := m.Framebuffer()[4*Width+8]; got != 0xFF848200 {
		t.Errorf("presented (8,4) = %#08x, want 0xFF848200 (bilinear midpoint)", got)
	}
	if got := m.Framebuffer()[100*Width+100]; got != 0xFF000000 {
		t.Errorf("presented (100,100) = %#08x, want 0xFF000000 (border)", got)
	}
	// The vblank handler kept counting after milestone 5.
	if got := m.Bus.Read32(dc4MilePhys + 64); got < 2 {
		t.Errorf("vblank count = %d, want at least 2", got)
	}
}

// checkDC4Tone asserts the looping PCM tone in the drained
// audio: one full period verbatim, and the zero crossing
// spacing measuring the 64 sample period within tolerance.
func checkDC4Tone(t *testing.T, m *Machine) {
	t.Helper()
	audio := m.AudioBuffer()
	if len(audio) < 1200 {
		t.Fatalf("drained audio = %d samples, want several frames of tone", len(audio))
	}
	found := -1
	for i := 0; i+128 <= len(audio); i++ {
		if audio[i][0] == dc3PCM(0) && audio[i][1] == dc3PCM(0) {
			ok := true
			for j := 0; j < 128; j++ {
				if audio[i+j][0] != dc3PCM(j%64) || audio[i+j][1] != dc3PCM(j%64) {
					ok = false
					break
				}
			}
			if ok {
				found = i
				break
			}
		}
	}
	if found < 0 {
		t.Fatalf("PCM loop period not found verbatim in %d drained samples", len(audio))
	}
	// Measure the period from rising zero crossings of the
	// left channel after the key-on transient.
	var edges []int
	for i := 1; i < len(audio); i++ {
		if audio[i-1][0] < 0 && audio[i][0] >= 0 {
			edges = append(edges, i)
		}
	}
	if len(edges) < 8 {
		t.Fatalf("only %d rising edges in the tone, want at least 8", len(edges))
	}
	total := edges[len(edges)-1] - edges[0]
	period := float64(total) / float64(len(edges)-1)
	if period < 62 || period > 66 {
		t.Errorf("measured tone period = %.2f samples, want 64 +/- 2 (689 Hz at 44100 Hz)", period)
	}
	t.Logf("tone: period found verbatim at %d, measured period %.2f samples over %d edges",
		found, period, len(edges))
}

func TestDC4Enginetest(t *testing.T) {
	// The gate disc, plus the persistent copy the shared
	// harness runs through the built .so.
	gdiPath := writeDC4GDI(t, t.TempDir())
	writeDC4GDI(t, filepath.Join("..", "testroms", "enginetest"))

	m, first := bootDC4(t, gdiPath, 40)
	checkDC4Milestones(t, m, first)
	t.Logf("milestone frames: %v", first)
	checkDC4Scene(t, m)
	checkDC4Tone(t, m)

	// State size: the fresh v4 state is the DC-3 v3 size
	// plus exactly the BIOS block.
	if got, want := New().StateSize(), 28162473+biosStateSize; got != want {
		t.Errorf("fresh state size = %d, want %d (v3 size + BIOS block)", got, want)
	}

	// Mid-run save, byte-identical continuation.
	state := m.Serialize()
	m.RunFrame()
	m.RunFrame()
	wantFB := append([]uint32(nil), m.Framebuffer()...)
	wantAudio := audioBytes(m.AudioBuffer())
	wantState := m.Serialize()

	m2 := New()
	if !m2.Deserialize(state) {
		t.Fatal("Deserialize of the mid-run state failed")
	}
	m2.RunFrame()
	m2.RunFrame()
	if got := m2.Framebuffer(); !bytes.Equal(u32Bytes(wantFB), u32Bytes(got)) {
		t.Error("restored framebuffer differs from the original continuation")
	}
	if got := audioBytes(m2.AudioBuffer()); !bytes.Equal(wantAudio, got) {
		t.Error("restored audio differs from the original continuation")
	}
	if got := m2.Serialize(); !bytes.Equal(wantState, got) {
		t.Error("restored state bytes differ after two more frames")
	}
}

// u32Bytes flattens a framebuffer for byte comparison.
func u32Bytes(fb []uint32) []byte {
	b := make([]byte, 4*len(fb))
	for i, px := range fb {
		binary.LittleEndian.PutUint32(b[4*i:], px)
	}
	return b
}

func TestDC4BootISO(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "game.iso")
	if err := os.WriteFile(p, buildDC4ISO(t), 0o644); err != nil {
		t.Fatal(err)
	}
	m, first := bootDC4(t, p, 40)
	checkDC4Milestones(t, m, first)
	checkDC4Scene(t, m)
}

func TestDC4BootFailures(t *testing.T) {
	// No disc at all.
	m := New()
	if err := m.BootDisc(); err == nil {
		t.Error("BootDisc with no disc returned nil error")
	}
	// A zeroed image: the IP.BIN has no SEGA header.
	dir := t.TempDir()
	zero := filepath.Join(dir, "zero.iso")
	if err := os.WriteFile(zero, make([]byte, 25*2048), 0o644); err != nil {
		t.Fatal(err)
	}
	m2 := New()
	if err := m2.LoadDisc(zero); err != nil {
		t.Fatalf("LoadDisc(zero.iso): %v", err)
	}
	if err := m2.BootDisc(); err == nil {
		t.Error("BootDisc on a zeroed IP.BIN returned nil error")
	}
	if m2.Booted() {
		t.Error("Booted() = true after a failed BootDisc")
	}
	// A truncated image: the boot area cannot be read.
	short := filepath.Join(dir, "short.iso")
	if err := os.WriteFile(short, make([]byte, 4*2048), 0o644); err != nil {
		t.Fatal(err)
	}
	m3 := New()
	if err := m3.LoadDisc(short); err != nil {
		t.Fatalf("LoadDisc(short.iso): %v", err)
	}
	if err := m3.BootDisc(); err == nil {
		t.Error("BootDisc on a truncated image returned nil error")
	}
	// A missing file fails at load.
	m4 := New()
	if err := m4.LoadDisc(filepath.Join(dir, "missing.gdi")); err == nil {
		t.Error("LoadDisc on a missing file returned nil error")
	}
}
