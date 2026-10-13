// Package r3000a is a from-scratch MIPS I R3000A CPU, the
// processor of the PlayStation, written from the MIPS I
// architecture manuals and the psx-spx hardware notes.
//
// The two pipeline behaviors that define this chip are
// modeled exactly:
//
//   - Load delay: a load (memory load, MFC0, MFC2) executed
//     by instruction N does not write its destination
//     register until instruction N+1 has executed. N+1
//     reads the old value. If N+1 writes the same
//     register with an ordinary write (an ALU result or
//     a link write), the pending load is cancelled and
//     never lands: the younger write wins, which is the
//     documented R3000 cancellation rule. A second load
//     to the same register does not cancel it: the
//     first load lands on schedule and the second takes
//     its place, so back-to-back loads deliver their
//     values one instruction apart. LWL and LWR merges
//     read the pending value when one targets their
//     register, because the merge happens late in the
//     pipeline; that is how unaligned load pairs
//     compose.
//   - Branch delay: the instruction after any branch or
//     jump always executes before control transfers. If
//     it faults, EPC points at the branch and Cause.BD
//     is set.
//
// Multiply and divide run in the async HI/LO unit: the
// instruction issues in one cycle and sets a busy count
// (12 cycles for multiply, 36 for divide, the documented
// R3000A figures). Independent instructions proceed;
// an MFHI/MFLO/MTHI/MTLO that arrives early stalls for
// the remaining count, which Step charges to it.
//
// Cache control: when SR.Isc is set, data loads and
// stores are redirected into an internal isolation
// store and never touch the bus; when SR.SwC is also
// set, instruction fetches come from that store too.
// Line, tag and writeback behavior of the real caches
// is not modeled in this slice (see the core README).
package r3000a

import (
	"encoding/binary"
	"fmt"
)

// Bus is the CPU's view of memory. Read16 and Read32
// return little-endian values. The CPU only issues
// aligned Read32 calls for word loads; unaligned access
// goes through the LWL/LWR/SWL/SWR merge paths or byte
// accesses.
type Bus interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, v uint8)
	Write16(addr uint32, v uint16)
	Write32(addr uint32, v uint32)
}

// Cop2 is the interface the CPU uses for the GTE. The
// system package wires chips/gte into it. Command is
// the COP2 instruction word; it executes the command
// and returns the cycles the GTE charges for it (the
// documented per-command execution time from the
// psx-spx GTE command table), which Step adds to the
// instruction's cost. That is this core's coprocessor
// stall model: command timing at instruction
// granularity, with no partial-overlap modeling.
type Cop2 interface {
	Data(reg uint32) uint32
	SetData(reg uint32, v uint32)
	Control(reg uint32) uint32
	SetControl(reg uint32, v uint32)
	Command(cmd uint32) int
}

// Exception codes for Cause.ExcCode.
const (
	ExcInt  = 0  // interrupt
	ExcAdEL = 4  // address error, load or fetch
	ExcAdES = 5  // address error, store
	ExcIBE  = 6  // instruction bus error
	ExcDBE  = 7  // data bus error
	ExcSys  = 8  // syscall
	ExcBp   = 9  // breakpoint
	ExcRI   = 10 // reserved instruction
	ExcCpU  = 11 // coprocessor unusable
	ExcOv   = 12 // arithmetic overflow
)

// CP0 register numbers used by this core.
const (
	cp0BadVAddr = 8
	cp0SR       = 12
	cp0Cause    = 13
	cp0EPC      = 14
	cp0PRId     = 15
)

// SR bits.
const (
	srIEc     = 0x00000001
	srStack   = 0x0000003F // KU/IE three-deep stack, bits 0-5
	srIMMask  = 0x0000FF00
	srIsc     = 0x00010000
	srSwC     = 0x00020000
	srBEV     = 0x00400000
	srWriteOK = 0xF7FFFFFF
)

// ResetVector is where execution starts after reset,
// in the BIOS window.
const ResetVector = 0xBFC00000

const (
	generalVectorBEV    = 0xBFC00180
	generalVectorNoBEV  = 0x80000000
	causeIPMask         = 0x0000FF00
	causeSWIP           = 0x00000300 // Cause bits 8-9, software writable
	externalIP2         = 0x00000400 // Cause bit 10, the PSX interrupt line
	multBusyCycles      = 12
	divBusyCycles       = 36
	isolationStoreBytes = 4096
)

// pendingLoad is one delayed load waiting out its delay
// slot.
type pendingLoad struct {
	reg   uint8
	value uint32
	valid bool
}

type raisedException struct {
	code uint32
	addr uint32 // faulting address for BadVAddr, else 0
	ce   uint32 // coprocessor number for ExcCpU
}

// CPU is one R3000A.
type CPU struct {
	Regs   [32]uint32
	HI, LO uint32
	PC     uint32 // address of the next instruction to execute
	NextPC uint32 // address after PC (the branch target path)

	// CP0 registers with architectural behavior. Other
	// CP0 registers read as zero and ignore writes in
	// this slice.
	SR       uint32
	Cause    uint32
	EPC      uint32
	BadVAddr uint32

	// CP2 is the GTE, wired by the system. It may be
	// nil in CPU-only tests; COP2 instructions then
	// raise coprocessor unusable like a missing chip.
	CP2 Cop2

	bus Bus

	pending   pendingLoad // load executed by the previous instruction
	newLoad   pendingLoad // load executed by the current instruction
	curWrites uint32      // bitmask of GPRs the current instruction wrote with non-load writes
	busySet   int         // HI/LO busy count set by the current instruction, -1 for none
	hiLoBusy  int         // remaining HI/LO unit busy cycles
	delaySlot bool        // the instruction at PC sits in a branch delay slot

	isolation [isolationStoreBytes]byte

	cycles uint64
}

// StateSize is the fixed serialized size of the CPU.
const StateSize = 32*4 + // GPRs
	4*4 + // HI, LO, PC, NextPC
	4*4 + // SR, Cause, EPC, BadVAddr
	4 + // pending reg/valid/delaySlot pad
	4 + // pending value
	4 + // hiLoBusy
	8 + // cycles
	isolationStoreBytes

// New returns a CPU in reset state attached to bus.
func New(bus Bus) *CPU {
	c := &CPU{bus: bus}
	c.Reset()
	return c
}

// Reset applies the hardware reset state: PC at the
// BIOS reset vector, SR with BEV set and interrupts
// off, everything else zero.
func (c *CPU) Reset() {
	c.Regs = [32]uint32{}
	c.HI, c.LO = 0, 0
	c.PC = ResetVector
	c.NextPC = ResetVector + 4
	c.SR = srBEV
	c.Cause = 0
	c.EPC = 0
	c.BadVAddr = 0
	c.pending = pendingLoad{}
	c.newLoad = pendingLoad{}
	c.curWrites = 0
	c.busySet = -1
	c.hiLoBusy = 0
	c.delaySlot = false
	c.isolation = [isolationStoreBytes]byte{}
	c.cycles = 0
}

// Cycles returns the total cycles Step has charged.
func (c *CPU) Cycles() uint64 { return c.cycles }

// SetInterrupt drives the external interrupt line (the
// PSX interrupt controller output, Cause IP2). The
// interrupt is taken at the next instruction boundary
// if SR enables it.
func (c *CPU) SetInterrupt(on bool) {
	if on {
		c.Cause |= externalIP2
	} else {
		c.Cause &^= externalIP2
	}
}

func (c *CPU) interruptPending() bool {
	return c.SR&srIEc != 0 && c.SR&c.Cause&causeIPMask != 0
}

// commitPending lands the previous instruction's
// delayed load unconditionally. Used when the current
// instruction will not execute (exception or
// interrupt), where the older load has completed its
// delay in program order.
func (c *CPU) commitPending() {
	if c.pending.valid && c.pending.reg != 0 {
		c.Regs[c.pending.reg] = c.pending.value
	}
	c.pending = pendingLoad{}
}

// CommitPendingLoad lands the previous instruction's
// delayed load immediately. The system uses it when
// it intercepts execution outside the normal Step
// flow (the BIOS HLE vectors): a load sitting in the
// delay slot of the call has completed its delay in
// program order when the callee starts, exactly as
// enterException treats it.
func (c *CPU) CommitPendingLoad() { c.commitPending() }

// Step executes one instruction (or takes a pending
// interrupt) and returns the cycles charged.
func (c *CPU) Step() int {
	if c.bus == nil {
		return 0
	}
	if c.interruptPending() {
		c.commitPending()
		c.enterException(ExcInt, c.PC, 0, 0, c.delaySlot)
		c.cycles++
		return 1
	}
	instrPC := c.PC
	if instrPC&3 != 0 {
		c.commitPending()
		c.enterException(ExcAdEL, instrPC, instrPC, 0, c.delaySlot)
		c.cycles++
		return 1
	}
	inDelay := c.delaySlot
	op := c.fetch(instrPC)
	c.PC = c.NextPC
	c.NextPC = c.PC + 4

	c.newLoad = pendingLoad{}
	c.curWrites = 0
	c.busySet = -1
	cycles, isCTI, exc := c.execute(op, instrPC)
	if cycles < 1 {
		cycles = 1
	}
	if exc != nil {
		// The faulting instruction has no architectural
		// effect; the older delayed load completes.
		c.commitPending()
		c.enterException(exc.code, instrPC, exc.addr, exc.ce, inDelay)
		c.cycles += uint64(cycles)
		return cycles
	}
	// Land the previous load unless this instruction
	// wrote the same register with a non-load write
	// (the R3000 cancellation rule). A fresh load to
	// the same register does not cancel it: the old
	// load lands here and the new one becomes pending.
	if c.pending.valid {
		mask := uint32(1) << c.pending.reg
		if c.curWrites&mask == 0 && c.pending.reg != 0 {
			c.Regs[c.pending.reg] = c.pending.value
		}
	}
	c.pending = c.newLoad
	c.delaySlot = isCTI
	if c.busySet >= 0 {
		c.hiLoBusy = c.busySet
	} else if c.hiLoBusy > 0 {
		c.hiLoBusy -= cycles
		if c.hiLoBusy < 0 {
			c.hiLoBusy = 0
		}
	}
	c.Regs[0] = 0
	c.cycles += uint64(cycles)
	return cycles
}

// enterException performs the R3000 exception entry:
// the KU/IE stack shifts, EPC and Cause are loaded
// (EPC points at the branch when the faulting
// instruction is in its delay slot, with Cause.BD
// set), and control goes to the general exception
// vector selected by SR.BEV.
func (c *CPU) enterException(code, instrPC, badAddr, ce uint32, inDelay bool) {
	epc := instrPC
	cause := c.Cause & causeIPMask
	cause |= code << 2
	if inDelay {
		epc = instrPC - 4
		cause |= 1 << 31 // BD
	}
	if code == ExcCpU {
		cause |= ce << 28
	}
	c.Cause = cause
	c.EPC = epc
	switch code {
	case ExcAdEL, ExcAdES, ExcIBE, ExcDBE:
		c.BadVAddr = badAddr
	}
	// Push the mode stack: old becomes previous,
	// previous becomes current's source, current is
	// kernel mode with interrupts disabled.
	c.SR = (c.SR &^ srStack) | ((c.SR & 0x0F) << 2)
	if c.SR&srBEV != 0 {
		c.PC = generalVectorBEV
	} else {
		c.PC = generalVectorNoBEV
	}
	c.NextPC = c.PC + 4
	c.delaySlot = false
	c.pending = pendingLoad{}
}

// fetch reads one instruction word, honoring the
// swapped-cache isolation mode.
func (c *CPU) fetch(addr uint32) uint32 {
	if c.SR&srIsc != 0 && c.SR&srSwC != 0 {
		return c.isoRead32(addr)
	}
	return c.bus.Read32(addr)
}

func (c *CPU) isoRead8(addr uint32) uint8 {
	return c.isolation[addr&(isolationStoreBytes-1)]
}

func (c *CPU) isoRead32(addr uint32) uint32 {
	var v uint32
	for i := 0; i < 4; i++ {
		v |= uint32(c.isoRead8(addr+uint32(i))) << (8 * i)
	}
	return v
}

func (c *CPU) isoWrite8(addr uint32, v uint8) {
	c.isolation[addr&(isolationStoreBytes-1)] = v
}

// readData8/16/32 and writeData8/16/32 route through
// the isolation store when SR.Isc is set, else the bus.
func (c *CPU) readData8(addr uint32) uint8 {
	if c.SR&srIsc != 0 {
		return c.isoRead8(addr)
	}
	return c.bus.Read8(addr)
}

func (c *CPU) readData16(addr uint32) uint16 {
	if c.SR&srIsc != 0 {
		return uint16(c.isoRead8(addr)) | uint16(c.isoRead8(addr+1))<<8
	}
	return c.bus.Read16(addr)
}

func (c *CPU) readData32(addr uint32) uint32 {
	if c.SR&srIsc != 0 {
		return c.isoRead32(addr)
	}
	return c.bus.Read32(addr)
}

func (c *CPU) writeData8(addr uint32, v uint8) {
	if c.SR&srIsc != 0 {
		c.isoWrite8(addr, v)
		return
	}
	c.bus.Write8(addr, v)
}

func (c *CPU) writeData16(addr uint32, v uint16) {
	if c.SR&srIsc != 0 {
		c.isoWrite8(addr, uint8(v))
		c.isoWrite8(addr+1, uint8(v>>8))
		return
	}
	c.bus.Write16(addr, v)
}

func (c *CPU) writeData32(addr uint32, v uint32) {
	if c.SR&srIsc != 0 {
		for i := 0; i < 4; i++ {
			c.isoWrite8(addr+uint32(i), uint8(v>>(8*i)))
		}
		return
	}
	c.bus.Write32(addr, v)
}

// setReg writes a general register immediately and
// records the write for the load-cancellation rule.
func (c *CPU) setReg(reg uint32, v uint32) {
	if reg == 0 {
		return
	}
	c.Regs[reg] = v
	c.curWrites |= uint32(1) << reg
}

// setLoad schedules a delayed load. It deliberately
// does not mark curWrites: a load in the delay slot of
// another load to the same register does not cancel
// the older load (see the package comment).
func (c *CPU) setLoad(reg uint32, v uint32) {
	if reg == 0 {
		return
	}
	c.newLoad = pendingLoad{reg: uint8(reg), value: v, valid: true}
}

// stallHiLo charges the remaining HI/LO busy time to a
// HI/LO access and clears it.
func (c *CPU) stallHiLo() int {
	n := c.hiLoBusy
	c.hiLoBusy = 0
	return n
}

// readCP0 returns a CP0 register. PRId is the
// documented R3000A value 0x00000002.
func (c *CPU) readCP0(reg uint32) uint32 {
	switch reg {
	case cp0BadVAddr:
		return c.BadVAddr
	case cp0SR:
		return c.SR
	case cp0Cause:
		return c.Cause
	case cp0EPC:
		return c.EPC
	case cp0PRId:
		return 0x00000002
	}
	return 0
}

// writeCP0 writes a CP0 register with the documented
// writability rules: BadVAddr and PRId ignore writes,
// and Cause only accepts its two software interrupt
// bits from MTC0.
func (c *CPU) writeCP0(reg uint32, v uint32) {
	switch reg {
	case cp0SR:
		c.SR = v & srWriteOK
	case cp0Cause:
		c.Cause = (c.Cause &^ causeSWIP) | (v & causeSWIP)
	case cp0EPC:
		c.EPC = v
	case cp0BadVAddr, cp0PRId:
		// Read-only in practice; writes are dropped.
	}
}

// Serialize returns the full CPU state as a fixed-size
// block (see StateSize).
func (c *CPU) Serialize() []byte {
	out := make([]byte, StateSize)
	off := 0
	for _, r := range c.Regs {
		binary.LittleEndian.PutUint32(out[off:], r)
		off += 4
	}
	for _, v := range []uint32{c.HI, c.LO, c.PC, c.NextPC, c.SR, c.Cause, c.EPC, c.BadVAddr} {
		binary.LittleEndian.PutUint32(out[off:], v)
		off += 4
	}
	meta := byte(0)
	if c.pending.valid {
		meta = c.pending.reg
		out[off] = meta
		out[off+1] = 1
	}
	if c.delaySlot {
		out[off+2] = 1
	}
	off += 4
	binary.LittleEndian.PutUint32(out[off:], c.pending.value)
	off += 4
	binary.LittleEndian.PutUint32(out[off:], uint32(int32(c.hiLoBusy)))
	off += 4
	binary.LittleEndian.PutUint64(out[off:], c.cycles)
	off += 8
	copy(out[off:], c.isolation[:])
	return out
}

// Deserialize restores a state produced by Serialize.
func (c *CPU) Deserialize(in []byte) error {
	if len(in) != StateSize {
		return fmt.Errorf("r3000a: state size %d, want %d", len(in), StateSize)
	}
	off := 0
	for i := range c.Regs {
		c.Regs[i] = binary.LittleEndian.Uint32(in[off:])
		off += 4
	}
	vals := make([]uint32, 8)
	for i := range vals {
		vals[i] = binary.LittleEndian.Uint32(in[off:])
		off += 4
	}
	c.HI, c.LO, c.PC, c.NextPC = vals[0], vals[1], vals[2], vals[3]
	c.SR, c.Cause, c.EPC, c.BadVAddr = vals[4], vals[5], vals[6], vals[7]
	c.pending = pendingLoad{reg: in[off], valid: in[off+1] != 0}
	c.delaySlot = in[off+2] != 0
	off += 4
	c.pending.value = binary.LittleEndian.Uint32(in[off:])
	if !c.pending.valid {
		c.pending = pendingLoad{}
	}
	off += 4
	c.hiLoBusy = int(int32(binary.LittleEndian.Uint32(in[off:])))
	off += 4
	c.cycles = binary.LittleEndian.Uint64(in[off:])
	off += 8
	copy(c.isolation[:], in[off:off+isolationStoreBytes])
	c.newLoad = pendingLoad{}
	c.curWrites = 0
	c.busySet = -1
	c.Regs[0] = 0
	return nil
}
