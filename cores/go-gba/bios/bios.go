// Package bios implements the Game Boy Advance BIOS as a
// high level emulation: software interrupt calls are answered
// directly in Go by the Handler hook, following the calling
// conventions published in GBATEK, and the 16 KB BIOS window
// holds our own hand assembled boot stub and IRQ dispatcher
// for the paths that run real code (reset entry and hardware
// IRQ entry).
//
// No bytes here come from a Nintendo BIOS image. The boot
// stub and the dispatcher are original ARM code written for
// this core, the SWI routines are original Go written from
// the documented register level contracts, and the single
// published constant embedded is the BIOS checksum value,
// which games compare against (see BiosChecksum).
package bios

import (
	"encoding/binary"

	"github.com/davidpatton/itak-gaming/cores/go-gba/chips/arm7tdmi"
)

// Memory is the byte addressable store the BIOS routines
// work against. Read returns the addressed unit zero
// extended; Write stores the low bits of val. Sizes are
// 1, 2 or 4 bytes. The system bus satisfies this.
type Memory interface {
	Read(addr uint32, size int) uint32
	Write(addr uint32, size int, val uint32)
}

// Well known addresses in the IWRAM system area and the
// stacks the BIOS establishes there.
const (
	CheckWordAddr  = 0x03007FF8 // BIOS interrupt check word in IWRAM system area
	HandlerPtrAddr = 0x03007FFC // user IRQ handler pointer
	IRQStackTop    = 0x03007FA0
	SVCStackTop    = 0x03007FE0
	UserStackTop   = 0x03007F00
)

// BiosChecksum is the documented checksum of the standard
// BIOS (a value derived over its first 0xB0 words minus a
// fixed constant, as GBATEK describes). Games read it via
// SWI 0x0D to confirm a BIOS is present. Our BIOS is our
// own code, so no real checksum can be computed over it;
// this is the published value games expect, and that is
// the only reason it appears here.
const BiosChecksum = 0xBAAE187F

// Layout of the installed BIOS image. The vector words at
// the start branch into three blocks: the boot stub, the
// IRQ dispatcher, and a shared one instruction return stub
// used by the vectors this core does not service.
const (
	bootAddr       = 0x100
	dispatcherAddr = 0x200
	returnStubAddr = 0x300
)

// vectorBranches holds the exception vector words, one per
// 4 byte slot from 0x00 to 0x1C. Each is a plain ARM branch
// (condition always, opcode 0xEA) whose signed word offset
// reaches its target from the vector address plus 8.
var vectorBranches = []struct {
	off  uint32
	word uint32
}{
	{0x00, 0xEA00003E}, // reset: branch to the boot stub at 0x100
	{0x04, 0xEA0000BD}, // undefined instruction: branch to the return stub at 0x300
	{0x08, 0xEA0000BC}, // SWI: branch to the return stub (used only with no hook installed)
	{0x0C, 0xEA0000BB}, // prefetch abort: branch to the return stub
	{0x10, 0xEA0000BA}, // data abort: branch to the return stub
	{0x14, 0xEA0000B9}, // reserved: branch to the return stub
	{0x18, 0xEA000078}, // IRQ: branch to the dispatcher at 0x200
	{0x1C, 0xEA0000B7}, // FIQ: branch to the return stub
}

// bootStub is the boot code at 0x100, followed by its
// literal pool. Each word is annotated with the instruction
// it encodes, in our own words.
//
// The sequence sets the stack pointer for SVC mode (the
// reset mode), for IRQ mode and for SYS mode, lands in SYS
// mode with IRQs enabled and FIQs masked, clears the BIOS
// check word, and branches to the cartridge entry point.
var bootStub = []uint32{
	0xE59FD020, // load SP from the pool: SVC stack top
	0xE321F0D2, // write control byte 0xD2 to CPSR: IRQ mode, IRQ and FIQ masked
	0xE59FD01C, // load SP from the pool: IRQ stack top
	0xE321F0DF, // write control byte 0xDF to CPSR: SYS mode, IRQ and FIQ masked
	0xE59FD018, // load SP from the pool: user (SYS) stack top
	0xE321F05F, // write control byte 0x5F to CPSR: SYS mode, IRQ open, FIQ masked
	0xE3A00000, // put zero in R0
	0xE59F1010, // load R1 from the pool: check word address
	0xE5810000, // store R0 at the address in R1: clear the check word
	0xE59FF00C, // load PC from the pool: cartridge entry (too far for a branch)
	// Literal pool (data, not code):
	SVCStackTop,
	IRQStackTop,
	UserStackTop,
	CheckWordAddr,
	0x08000000, // cartridge entry point
}

// dispatcher is the IRQ entry code at 0x200, followed by
// its literal pool. It runs on the IRQ stack in IRQ mode.
//
// Flow, matching the documented BIOS dispatcher contract:
// save the caller saved registers, form the pending set
// (IE AND IF) from the interrupt controller halfwords,
// acknowledge by writing the pending set back to IF, OR
// the pending set into the BIOS check word, call the user
// handler pointer (if any) with LR set so a plain BX LR in
// the handler comes back here, restore the registers, and
// return to the interrupted code with SUBS PC, LR, #4.
var dispatcher = []uint32{
	0xE92D500F, // push R0, R1, R2, R3, R12 and LR onto the IRQ stack
	0xE59F0044, // load R0 from the pool: IE register address 0x04000200
	0xE1D010B0, // load halfword R1 from [R0]: the IE mask
	0xE1D020B2, // load halfword R2 from [R0, 2]: the IF flags
	0xE0011002, // AND R1 with R2: pending = IE AND IF
	0xE3510000, // compare pending with zero
	0x0A00000A, // if none pending, branch forward to the restore step
	0xE1C010B2, // store halfword R1 at [R0, 2]: write pending back to IF (acknowledge)
	0xE59F302C, // load R3 from the pool: check word address
	0xE5932000, // load word R2 from [R3]: current check word
	0xE1822001, // OR R2 with the pending set in R1
	0xE5832000, // store R2 at [R3]: updated check word
	0xE59F3020, // load R3 from the pool: handler pointer address
	0xE593C000, // load word R12 from [R3]: the user handler address
	0xE35C0000, // compare the handler address with zero
	0x0A000001, // if no handler, branch forward to the restore step
	0xE1A0E00F, // copy PC into LR: the handler returns here with BX LR
	0xE12FFF1C, // branch and exchange to the handler address in R12
	0xE8BD500F, // restore step: pop R0, R1, R2, R3, R12 and LR
	0xE25EF004, // subtract 4 from LR into PC, restoring CPSR from SPSR
	// Literal pool (data, not code):
	0x04000200, // IE register (IF is at this address plus 2)
	CheckWordAddr,
	HandlerPtrAddr,
}

// returnStub is the single instruction at 0x300 shared by
// the vectors this core does not service, and by the SWI
// vector when no SWI hook is installed: return to the
// saved link register, restoring the flags from the SPSR
// of the exception mode (the S bit on a PC destination
// does exactly that).
const returnStub = 0xE1B0F00E // move SPSR into CPSR while loading PC from LR

// Install writes our boot stub, IRQ dispatcher and return
// stub into the 16 KB BIOS store. The rest of the store is
// zeroed first so the image is deterministic.
func Install(store []byte) {
	for i := range store {
		store[i] = 0
	}
	put := func(off uint32, w uint32) {
		if int(off)+4 <= len(store) {
			binary.LittleEndian.PutUint32(store[off:], w)
		}
	}
	for _, v := range vectorBranches {
		put(v.off, v.word)
	}
	for i, w := range bootStub {
		put(bootAddr+uint32(4*i), w)
	}
	for i, w := range dispatcher {
		put(dispatcherAddr+uint32(4*i), w)
	}
	put(returnStubAddr, returnStub)
}

// Wait kinds recorded in the Handler state.
const (
	waitNone = iota
	waitHalt
	waitIntr
)

// Handler answers BIOS software interrupts. It is bound to
// a Memory at construction; install its Hook as the CPU SWI
// hook. The only mutable state is the wait state used by
// the Halt, Stop and IntrWait family, which Poll advances
// while the CPU sits halted.
type Handler struct {
	mem     Memory
	waiting bool
	kind    int
	mask    uint32 // IRQ bits an IntrWait is waiting on
	ret     uint32 // return address to resume at on wake
}

// New returns a Handler answering SWI calls against mem.
func New(mem Memory) *Handler {
	return &Handler{mem: mem}
}

// Waiting reports whether a wait call is currently holding
// the CPU halted.
func (h *Handler) Waiting() bool { return h.waiting }

// swiNumber extracts the BIOS function number from the raw
// SWI comment field. In Thumb state the field is 8 bits.
// In ARM state GBA calls carry the number either in the
// low byte (small comment values) or in bits 16 to 23 (the
// common SWI nn<<16 assembler form).
func swiNumber(c *arm7tdmi.CPU, imm uint32) uint32 {
	if c.Thumb() {
		return imm & 0xFF
	}
	if imm > 0xFF {
		return (imm >> 16) & 0xFF
	}
	return imm & 0xFF
}

// swiReturn computes the SWI instruction address and the
// return address (the next instruction) from the pipeline
// convention: in ARM state R15 is the SWI address plus 8
// and instructions are 4 bytes; in Thumb state R15 is the
// SWI address plus 4 and instructions are 2 bytes.
func swiReturn(c *arm7tdmi.CPU) (swiAddr, ret uint32) {
	if c.Thumb() {
		swiAddr = c.R[15] - 4
		return swiAddr, swiAddr + 2
	}
	swiAddr = c.R[15] - 8
	return swiAddr, swiAddr + 4
}

// Hook is the CPU SWI hook. It dispatches on the BIOS
// function number, completes handled calls by jumping to
// the return address (or by halting, for the wait calls),
// and reports false for unknown numbers so the CPU takes
// the real SWI exception instead.
func (h *Handler) Hook(c *arm7tdmi.CPU, imm uint32) (int, bool) {
	num := swiNumber(c, imm)
	_, ret := swiReturn(c)
	switch num {
	case 0x00:
		return h.softReset(c), true
	case 0x01:
		return h.registerRamReset(c, ret), true
	case 0x02, 0x03:
		// Halt (0x02) and Stop (0x03). Stop on real hardware
		// also powers down the clock and waits for a narrower
		// set of wake sources; this core models it exactly
		// like Halt, which is the documented simplification
		// for this slice.
		h.enterWait(c, waitHalt, 0, ret)
		return 3, true
	case 0x04:
		return h.intrWait(c, c.R[0] != 0, c.R[1], ret), true
	case 0x05:
		// VBlankIntrWait is IntrWait with the discard flag
		// set and the mask fixed to the vblank IRQ bit, so
		// it always waits for the next vblank.
		return h.intrWait(c, true, 1<<0, ret), true
	case 0x06:
		return h.div(c, int32(c.R[0]), int32(c.R[1]), ret), true
	case 0x07:
		// DivArm takes the same inputs swapped.
		return h.div(c, int32(c.R[1]), int32(c.R[0]), ret), true
	case 0x08:
		c.R[0] = isqrt(c.R[0])
		c.Jump(ret)
		return 3, true
	case 0x09:
		c.R[0] = arcTan(int32(c.R[0]))
		c.Jump(ret)
		return 3, true
	case 0x0A:
		c.R[0] = arcTan2(int32(c.R[0]), int32(c.R[1]))
		c.Jump(ret)
		return 3, true
	case 0x0B:
		return h.cpuSet(c, ret), true
	case 0x0C:
		return h.cpuFastSet(c, ret), true
	case 0x0D:
		c.R[0] = BiosChecksum
		c.Jump(ret)
		return 3, true
	case 0x0E:
		return h.bgAffineSet(c, ret), true
	case 0x0F:
		return h.objAffineSet(c, ret), true
	case 0x10:
		return h.bitUnpack(c, ret), true
	case 0x11, 0x12:
		return h.lz77(c, ret), true
	case 0x13:
		return h.huffUnComp(c, ret), true
	case 0x14, 0x15:
		return h.rlUnComp(c, ret), true
	case 0x16, 0x17:
		return h.diffUnFilter(c, ret, false), true
	case 0x18:
		return h.diffUnFilter(c, ret, true), true
	}
	return 0, false
}

// enterWait records a wait and halts the CPU.
func (h *Handler) enterWait(c *arm7tdmi.CPU, kind int, mask, ret uint32) {
	h.waiting = true
	h.kind = kind
	h.mask = mask
	h.ret = ret
	c.Halted = true
}

// wake clears the wait state, unhalts the CPU and resumes
// it at the recorded return address.
func (h *Handler) wake(c *arm7tdmi.CPU) {
	ret := h.ret
	h.waiting = false
	h.kind = waitNone
	h.mask = 0
	c.Halted = false
	c.Jump(ret)
}

// finish clears the wait bookkeeping for the case where
// the CPU already resumed on its own: it woke to take the
// interrupt, the dispatcher ran and returned it to exactly
// the wait's resume address, so no jump is needed here.
func (h *Handler) finish() {
	h.waiting = false
	h.kind = waitNone
	h.mask = 0
}

// executingAddr returns the address of the instruction in
// the CPU's execute stage, by the pipeline convention
// swiReturn uses: R15 reads as the executing address plus
// 8 in ARM state and plus 4 in Thumb state.
func executingAddr(c *arm7tdmi.CPU) uint32 {
	if c.Thumb() {
		return c.R[15] - 4
	}
	return c.R[15] - 8
}

// Poll advances the wait state. The system calls it
// whenever a wait is outstanding, whether or not the CPU
// is currently halted, because a wait completes through
// the real IRQ path and Poll has to watch both sides of
// the dispatch:
//
//   - While the CPU sits halted, no dispatcher has run
//     yet. A Halt wakes when any of the IF flag bits 0 to
//     13 is pending (IME and IE gating is the documented
//     simplification: Halt wakes on any pending flag) and
//     the IRQ itself is then taken on the next step, so
//     the user handler still runs before the waiting
//     code. An IntrWait can complete here only if its
//     check word bits are already present.
//   - An accepted interrupt wakes the halted CPU (see
//     arm7tdmi.Step) and the BIOS dispatcher records the
//     acknowledged flags in the check word, calls the
//     user handler, and returns to the wait's resume
//     address. If the awaited condition now holds, Poll
//     consumes it (for IntrWait, clearing its bits from
//     the check word) and the wait is over; the CPU is
//     already positioned at the resume address. If it
//     does not hold (the wake was an unrelated interrupt),
//     IntrWait halts again at the resume address, exactly
//     the BIOS IntrWait loop; a plain Halt needs no such
//     re-check because any accepted interrupt ends it.
//   - While the CPU is inside the IRQ dispatcher (IRQ
//     mode) Poll leaves everything alone: the dispatcher
//     is still deciding the wait's outcome.
func (h *Handler) Poll(c *arm7tdmi.CPU) {
	if !h.waiting {
		return
	}
	if c.CPSR&0x1F == arm7tdmi.ModeIRQ {
		return
	}
	switch h.kind {
	case waitHalt:
		if c.Halted {
			if h.mem.Read(0x04000202, 2)&0x3FFF != 0 {
				h.wake(c)
			}
			return
		}
		// The CPU woke to service an interrupt and its
		// handler has returned: the halt is over.
		h.finish()
	case waitIntr:
		word := h.mem.Read(CheckWordAddr, 4)
		if word&h.mask != 0 {
			h.mem.Write(CheckWordAddr, 4, word&^h.mask)
			if c.Halted {
				h.wake(c)
			} else {
				h.finish()
			}
			return
		}
		if !c.Halted && executingAddr(c) == h.ret {
			// An unrelated interrupt woke the CPU and its
			// dispatcher run did not set the awaited
			// bits: halt again at the resume address.
			c.Halted = true
		}
	}
}

// HandlerStateSize is the serialized size of the Handler
// wait state in bytes: a flags word (bit 0 waiting, bits 8
// to 15 the wait kind), the IntrWait mask, the return
// address, and one reserved word kept zero.
const HandlerStateSize = 16

// Serialize writes the wait state to dst, which must be at
// least HandlerStateSize bytes, and returns the byte count.
func (h *Handler) Serialize(dst []byte) int {
	if len(dst) < HandlerStateSize {
		return 0
	}
	var flags uint32
	if h.waiting {
		flags |= 1
	}
	flags |= uint32(h.kind) << 8
	binary.LittleEndian.PutUint32(dst[0:], flags)
	binary.LittleEndian.PutUint32(dst[4:], h.mask)
	binary.LittleEndian.PutUint32(dst[8:], h.ret)
	binary.LittleEndian.PutUint32(dst[12:], 0)
	return HandlerStateSize
}

// Deserialize restores a state written by Serialize. It
// reports false, leaving the Handler untouched, when src
// is too short.
func (h *Handler) Deserialize(src []byte) bool {
	if len(src) < HandlerStateSize {
		return false
	}
	flags := binary.LittleEndian.Uint32(src[0:])
	h.waiting = flags&1 != 0
	h.kind = int((flags >> 8) & 0xFF)
	h.mask = binary.LittleEndian.Uint32(src[4:])
	h.ret = binary.LittleEndian.Uint32(src[8:])
	return true
}

// Small memory helpers used by the SWI routines.

func (h *Handler) rd8(addr uint32) uint32  { return h.mem.Read(addr, 1) }
func (h *Handler) rd16(addr uint32) uint32 { return h.mem.Read(addr, 2) }
func (h *Handler) rd32(addr uint32) uint32 { return h.mem.Read(addr, 4) }

func (h *Handler) wr8(addr uint32, v uint32)  { h.mem.Write(addr, 1, v) }
func (h *Handler) wr16(addr uint32, v uint32) { h.mem.Write(addr, 2, v) }
func (h *Handler) wr32(addr uint32, v uint32) { h.mem.Write(addr, 4, v) }
