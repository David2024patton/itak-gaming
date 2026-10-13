// Package allegrex implements the Sony Allegrex CPU used in the PSP:
// a MIPS32 little endian core with a single precision FPU (CP1), the
// Allegrex SPECIAL2/SPECIAL3 extensions, and the COP2 VFPU.
//
// Execution model notes:
//   - PC/NPC pipeline pair: branches and jumps set NPC, so the delay
//     slot at PC+4 always executes exactly once before the target.
//   - No load delay. The Allegrex follows the MIPS32 interlock model:
//     a load writes its destination register during the load
//     instruction itself, so a back to back load/use pair sees the
//     new value with no stall and no stale read.
//   - Cycle counts are a deterministic approximation, documented in
//     exec.go: 1 cycle base, 2 for multiply family, 37 for divide,
//     12 for FPU divide and square root.
package allegrex

import "encoding/binary"

// Bus is the memory and device interface the CPU drives. Addresses
// are virtual equals physical in this core (no TLB), matching the
// PSP kernel segments the system package maps onto the bus.
type Bus interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, value uint8)
	Write16(addr uint32, value uint16)
	Write32(addr uint32, value uint32)
}

// Exception codes stored in Cause.ExcCode.
const (
	excInt  = 0  // interrupt
	excAdEL = 4  // address error, load or fetch
	excAdES = 5  // address error, store
	excSys  = 8  // syscall
	excBp   = 9  // breakpoint
	excRI   = 10 // reserved instruction
	excCpU  = 11 // coprocessor unusable
	excOv   = 12 // integer overflow
	excTr   = 13 // trap
	excFPE  = 15 // floating point (never raised in slice 1)
)

// Status register bit positions used by the core.
const (
	statusIE  = 1 << 0
	statusEXL = 1 << 1
	statusERL = 1 << 2
	statusBEV = 1 << 22
	statusCU1 = 1 << 29

	// StatusCU1 is the CP0 Status bit that enables the FPU. A
	// bare reset leaves it clear (CpU on COP1 use); the kernel
	// sets it in every thread context it creates, matching the
	// firmware, whose user threads run with the FPU enabled.
	StatusCU1 = statusCU1
)

// CPU is the full Allegrex processor state. All state is unexported;
// the system package drives it through Step and the accessors.
type CPU struct {
	bus Bus

	// SyscallHook, when non-nil, is consulted for every SYSCALL
	// instruction before the Sys exception is raised. It receives
	// the 20 bit syscall code from the instruction word. Returning
	// true means the hook handled the call (the result belongs in
	// $v0, set by the hook through SetReg) and execution continues
	// with the next instruction; returning false takes the normal
	// exception path. The hook is not part of the serialized state
	// and is never cleared by Reset or Deserialize: the kernel in
	// the kernel package installs it once per machine.
	SyscallHook func(c *CPU, code uint32) bool

	regs [32]uint32
	hi   uint32
	lo   uint32

	// Pipeline: pc is the instruction about to execute, nextPC is
	// the one after it. inDelaySlot reports whether pc sits in a
	// branch delay slot (drives Cause.BD and EPC selection).
	pc          uint32
	nextPC      uint32
	inDelaySlot bool

	cp0 [32][8]uint32 // CP0 registers by register and select

	fpr [32]uint32 // FPU registers, raw IEEE 754 bits
	fcr [32]uint32 // FPU control registers; index 31 is the FCSR

	icReg [32]uint32 // interrupt controller registers for MFIC/MTIC

	vregs     [128]uint32 // VFPU registers, raw bits
	ctrl      [32]uint32  // VFPU CFC2/CTC2 register storage
	mfvcStore [144]uint32 // MFVC/MTVC backing store
	rngState  [8]uint32   // VFPU random generator state, RCX0..7

	spfxImm uint32 // 19 bit source prefix for VS reads
	tpfxImm uint32 // 19 bit source prefix for VT reads
	dpfxImm uint32 // 12 bit destination prefix
	spfxOn  bool
	tpfxOn  bool
	dpfxOn  bool
	// pfxPoison is set while an op whose armed source prefix
	// selected an out of range lane executes; every source
	// read of that op returns zero. It is always false
	// between instructions, so it is not serialized.
	pfxPoison bool
	cc        uint32 // VFPU condition code

	llbit  bool
	lladdr uint32

	halted         bool
	countRemainder uint32 // 0 or 1, leftover half rate Count tick
	timerIRQ       bool   // latched timer interrupt, Cause IP bit 7
	extIP          uint32 // external interrupt lines, bits 0..7
	vfpuFaultCount int
	randomState    uint32 // current Random register value

	// Execution context for the instruction currently executing,
	// captured at Step entry so exception entry can compute EPC
	// and BD even after the pipeline has advanced.
	curPC    uint32
	curDelay bool
}

// New returns a CPU attached to bus, reset with entry point 0.
func New(bus Bus) *CPU {
	c := &CPU{bus: bus}
	c.Reset(0)
	return c
}

// Reset zeroes all registers and state, sets PC to entry, and puts
// Status in its reset value BEV|ERL (0x00400004) with Cause clear.
func (c *CPU) Reset(entry uint32) {
	c.regs = [32]uint32{}
	c.hi, c.lo = 0, 0
	c.pc = entry
	c.nextPC = entry + 4
	c.inDelaySlot = false
	c.cp0 = [32][8]uint32{}
	c.cp0[12][0] = 0x00400004 // Status = BEV|ERL
	// Config (register 16) is left at 0 in slice 1; reads return the
	// stored value and the system package does not depend on it.
	c.fpr = [32]uint32{}
	c.fcr = [32]uint32{}
	c.vregs = [128]uint32{}
	c.ctrl = [32]uint32{}
	c.mfvcStore = [144]uint32{}
	c.rngState = [8]uint32{}
	// The prefix immediates power on at the identity encoding
	// 0xE4 (swizzle x,y,z,w, nothing else set); the corpus
	// prefix_ctrl test reads exactly that back through mfvc
	// $128..$130 before any prefix is written.
	c.spfxImm, c.tpfxImm, c.dpfxImm = 0xE4, 0xE4, 0xE4
	c.spfxOn, c.tpfxOn, c.dpfxOn = false, false, false
	c.cc = 0
	c.icReg = [32]uint32{}
	c.llbit = false
	c.lladdr = 0
	c.halted = false
	c.countRemainder = 0
	c.timerIRQ = false
	c.extIP = 0
	c.vfpuFaultCount = 0
	c.randomState = 31
	c.curPC = entry
	c.curDelay = false
}

// PC returns the address of the next instruction to execute.
func (c *CPU) PC() uint32 { return c.pc }

// SetPC redirects execution; the following instruction is taken as
// sequential and not in a delay slot.
func (c *CPU) SetPC(pc uint32) {
	c.pc = pc
	c.nextPC = pc + 4
	c.inDelaySlot = false
}

// Reg returns general purpose register i. Reg(0) is always 0.
func (c *CPU) Reg(i int) uint32 {
	if i <= 0 || i > 31 {
		return 0
	}
	return c.regs[i]
}

// SetReg writes general purpose register i; writes to register 0
// are ignored.
func (c *CPU) SetReg(i int, v uint32) {
	if i <= 0 || i > 31 {
		return
	}
	c.regs[i] = v
}

// setReg is the internal write path; every register write in the
// decoder funnels through here so r0 writes are discarded centrally.
func (c *CPU) setReg(i int, v uint32) {
	if i == 0 {
		return
	}
	c.regs[i] = v
}

// HI returns the HI result register.
func (c *CPU) HI() uint32 { return c.hi }

// LO returns the LO result register.
func (c *CPU) LO() uint32 { return c.lo }

// FReg returns the raw IEEE 754 bits of FPU register i.
func (c *CPU) FReg(i int) uint32 {
	if i < 0 || i > 31 {
		return 0
	}
	return c.fpr[i]
}

// SetFReg sets the raw bits of FPU register i.
func (c *CPU) SetFReg(i int, bits uint32) {
	if i < 0 || i > 31 {
		return
	}
	c.fpr[i] = bits
}

// CP0 reads a CP0 register by register number and select, applying
// the composed fields (Random, PRId, Cause interrupt bits).
func (c *CPU) CP0(reg, sel int) uint32 {
	if reg < 0 || reg > 31 || sel < 0 || sel > 7 {
		return 0
	}
	return c.readCP0(reg, sel)
}

// SetInterruptLines drives the external interrupt inputs. Bits 0..7
// map onto Cause.IP bits 8..15; bit 7 is shared with the timer latch
// and is the OR of both sources.
func (c *CPU) SetInterruptLines(ip uint32) { c.extIP = ip & 0xFF }

// Halted reports whether the CPU is stopped in HALT or WAIT waiting
// for an enabled interrupt.
func (c *CPU) Halted() bool { return c.halted }

// VFPUFaultCount returns how many genuinely unimplemented VFPU
// encodings have faulted.
func (c *CPU) VFPUFaultCount() int { return c.vfpuFaultCount }

// VSingle returns the raw bits of VFPU register i by linear index
// (0..127, m*16 + row*4 + col).
func (c *CPU) VSingle(i int) uint32 {
	if i < 0 || i > 127 {
		return 0
	}
	return c.vregs[i]
}

// SetVSingle sets the raw bits of VFPU register i by linear index.
func (c *CPU) SetVSingle(i int, bits uint32) {
	if i < 0 || i > 127 {
		return
	}
	c.vregs[i] = bits
}

// VFPUCC returns the VFPU condition code.
func (c *CPU) VFPUCC() uint32 { return c.cc }

// ipByte composes the Cause.IP byte: external lines, the two
// software writable bits stored in Cause, and the timer latch at
// bit 7.
func (c *CPU) ipByte() uint32 {
	ip := c.extIP
	ip |= (c.cp0[13][0] >> 8) & 0x03
	if c.timerIRQ {
		ip |= 0x80
	}
	return ip & 0xFF
}

// interruptPending reports whether an interrupt would be taken now:
// IE set, not in exception or error level, and an unmasked IP bit.
func (c *CPU) interruptPending() bool {
	st := c.cp0[12][0]
	if st&statusIE == 0 || st&statusEXL != 0 || st&statusERL != 0 {
		return false
	}
	im := (st >> 8) & 0xFF
	return c.ipByte()&im != 0
}

func (c *CPU) readCP0(reg, sel int) uint32 {
	switch {
	case reg == 1: // Random
		return c.randomState
	case reg == 13: // Cause: IP is composed, the rest is stored
		v := c.cp0[13][0] &^ (0xFF << 8)
		return v | c.ipByte()<<8
	case reg == 15 && sel == 0: // PRId is a constant
		return 0x00006542
	default:
		return c.cp0[reg][sel]
	}
}

func (c *CPU) writeCP0(reg, sel int, v uint32) {
	switch {
	case reg == 1: // Random is read only
	case reg == 11: // Compare: writing clears the timer latch
		c.cp0[reg][sel] = v
		c.timerIRQ = false
	case reg == 15 && sel == 0: // PRId writes are ignored
	default:
		c.cp0[reg][sel] = v
	}
}

// raiseException performs exception entry for the instruction at
// curPC. When ERL is clear and EXL is clear it saves EPC, ExcCode,
// CE and BD, then sets EXL. When ERL is set the saved context is
// left untouched (reset and error flows own it) but control still
// transfers to the vector. The interrupt vector gains an extra
// 0x80 offset when Cause.IV is set.
func (c *CPU) raiseException(code, ce uint32) {
	st := c.cp0[12][0]
	if st&statusERL == 0 {
		if st&statusEXL == 0 {
			epc := c.curPC
			bd := uint32(0)
			if c.curDelay {
				epc = c.curPC - 4
				bd = 1
			}
			c.cp0[14][0] = epc // EPC
			cause := c.cp0[13][0]
			cause &^= 0x7C // ExcCode bits 2..6
			cause |= (code & 0x1F) << 2
			cause &^= 0x3 << 28 // CE bits 28..29
			cause |= (ce & 0x3) << 28
			cause &^= 1 << 31 // BD bit 31
			cause |= bd << 31
			c.cp0[13][0] = cause
		}
		c.cp0[12][0] = st | statusEXL
	}
	base := uint32(0x80000180)
	if st&statusBEV != 0 {
		base = 0xBFC00380
	}
	if code == excInt && c.cp0[13][0]&(1<<23) != 0 { // Cause.IV
		base += 0x80
	}
	c.llbit = false
	c.setPCVector(base)
}

// raiseDebug performs debug exception entry for SDBBP: DEPC gets
// the faulting address (branch address when in a delay slot), the
// code is stored in Debug bits 10..6, and control goes to the
// debug vector. Simplification: Status is not modified.
func (c *CPU) raiseDebug(code uint32) {
	epc := c.curPC
	if c.curDelay {
		epc = c.curPC - 4
	}
	c.cp0[24][0] = epc // DEPC (register 24 is DEPC in this map)
	// Register 23 is Debug per the register map in the brief.
	dbg := c.cp0[23][0]
	dbg &^= 0x1F << 6
	dbg |= (code & 0x1F) << 6
	c.cp0[23][0] = dbg
	c.llbit = false
	c.setPCVector(0xBFC00480)
}

func (c *CPU) setPCVector(v uint32) {
	c.pc = v
	c.nextPC = v + 4
	c.inDelaySlot = false
}

// advanceCount moves the Count register at half the CPU rate using
// a serialized remainder, and latches the timer interrupt when
// Count reaches Compare.
func (c *CPU) advanceCount(cycles int) {
	total := c.countRemainder + uint32(cycles)
	inc := total / 2
	c.countRemainder = total % 2
	if inc == 0 {
		return
	}
	old := c.cp0[9][0]
	c.cp0[9][0] = old + inc
	cmp := c.cp0[11][0]
	if cmp != 0 {
		if delta := cmp - old; delta >= 1 && delta <= inc {
			c.timerIRQ = true
		}
	}
}

// tickRandom decrements the Random register once per Step, wrapping
// back to 31 when it would fall below Wired.
func (c *CPU) tickRandom() {
	wired := c.cp0[6][0]
	if wired > 31 {
		wired = 31
	}
	if c.randomState <= wired || c.randomState > 31 {
		c.randomState = 31
	} else {
		c.randomState--
	}
}

// Step executes one instruction, or one halted tick, and returns
// the cycles consumed (always at least 1).
func (c *CPU) Step() int {
	c.tickRandom()
	if c.halted {
		if c.interruptPending() {
			c.halted = false
			c.curPC = c.pc
			c.curDelay = c.inDelaySlot
			c.raiseException(excInt, 0)
		}
		c.advanceCount(1)
		return 1
	}
	// Interrupt check happens before the fetch of each instruction.
	if c.interruptPending() {
		c.curPC = c.pc
		c.curDelay = c.inDelaySlot
		c.raiseException(excInt, 0)
		c.advanceCount(1)
		return 1
	}
	c.curPC = c.pc
	c.curDelay = c.inDelaySlot
	if c.pc&3 != 0 {
		c.cp0[8][0] = c.pc // BadVAddr
		c.raiseException(excAdEL, 0)
		c.advanceCount(1)
		return 1
	}
	instr := c.bus.Read32(c.pc)
	// Advance the pipeline to the sequential successor. Branch and
	// jump execution rewrites nextPC; the instruction now sitting
	// at pc is the delay slot when the executed instruction was a
	// branch, which the branch handler flags via inDelaySlot.
	c.pc = c.nextPC
	c.nextPC = c.pc + 4
	c.inDelaySlot = false
	cycles := c.execute(instr)
	c.advanceCount(cycles)
	return cycles
}

// branch applies a branch decision made by the instruction at
// curPC. The delay slot (already at pc) is flagged as in a delay
// slot whether or not the branch is taken; a taken branch retargets
// nextPC.
func (c *CPU) branch(taken bool, target uint32) {
	c.inDelaySlot = true
	if taken {
		c.nextPC = target
	}
}

// branchLikely applies a branch likely decision (BEQL, BNEL,
// BLEZL, BGTZL, BLTZL, BGEZL, BLTZALL, BGEZALL). A taken branch
// behaves exactly like branch. A branch that is not taken annuls
// its delay slot: the slot instruction leaves no architectural
// trace. At execute time pc holds the slot address and nextPC the
// instruction after it, so the annulment advances the pipeline
// past the slot; the saved state stays consistent at every step
// boundary with no extra flag. The annulled fetch costs no cycle
// in this model, a documented deviation from hardware.
func (c *CPU) branchLikely(taken bool, target uint32) {
	if taken {
		c.branch(true, target)
		return
	}
	c.pc = c.nextPC
	c.nextPC = c.pc + 4
	c.inDelaySlot = false
}

// branchTarget computes a PC relative branch target.
func (c *CPU) branchTarget(off int32) uint32 {
	return uint32(int32(c.curPC+4) + off<<2)
}

// Serialize layout: every field is one little endian uint32, in
// this exact order, with no header. The size is constant.
//
//	GPRs 0..31, HI, LO, PC, nextPC, inDelaySlot,
//	cp0[32][8] in register then select order,
//	fpr 0..31, fcr 0..31 (index 31 is the FCSR),
//	icReg 0..31, llbit, lladdr, halted, countRemainder,
//	timerIRQ, vfpuFaultCount, randomState, extIP,
//	vregs 0..127, ctrl 0..31, mfvcStore 0..143, rngState 0..7,
//	spfxImm, tpfxImm, dpfxImm, spfxOn, tpfxOn, dpfxOn, cc.
const serializeWords = 32 + 2 + 2 + 1 + 32*8 + 32 + 32 + 32 + 1 + 1 + 1 + 1 + 1 + 1 + 1 + 1 + 128 + 32 + 144 + 8 + 3 + 3 + 1

// SerializeSize is the fixed byte length of a serialized CPU.
const serializeSize = serializeWords * 4

// Serialize returns the full CPU state in the fixed layout above.
func (c *CPU) Serialize() []byte {
	out := make([]byte, serializeSize)
	w := make([]uint32, 0, serializeWords)
	w = append(w, c.regs[:]...)
	w = append(w, c.hi, c.lo, c.pc, c.nextPC)
	w = append(w, boolWord(c.inDelaySlot))
	for r := 0; r < 32; r++ {
		for s := 0; s < 8; s++ {
			w = append(w, c.cp0[r][s])
		}
	}
	w = append(w, c.fpr[:]...)
	w = append(w, c.fcr[:]...)
	w = append(w, c.icReg[:]...)
	w = append(w, boolWord(c.llbit), c.lladdr, boolWord(c.halted),
		c.countRemainder, boolWord(c.timerIRQ), uint32(c.vfpuFaultCount),
		c.randomState, c.extIP)
	w = append(w, c.vregs[:]...)
	w = append(w, c.ctrl[:]...)
	w = append(w, c.mfvcStore[:]...)
	w = append(w, c.rngState[:]...)
	w = append(w, c.spfxImm, c.tpfxImm, c.dpfxImm)
	w = append(w, boolWord(c.spfxOn), boolWord(c.tpfxOn), boolWord(c.dpfxOn))
	w = append(w, c.cc)
	for i, v := range w {
		binary.LittleEndian.PutUint32(out[i*4:], v)
	}
	return out
}

// Deserialize restores a state produced by Serialize. It returns an
// error unless the length matches the fixed layout exactly.
func (c *CPU) Deserialize(data []byte) error {
	if len(data) != serializeSize {
		return errBadStateSize
	}
	rd := func(i int) uint32 { return binary.LittleEndian.Uint32(data[i*4:]) }
	i := 0
	next := func() uint32 { v := rd(i); i++; return v }
	for r := 0; r < 32; r++ {
		c.regs[r] = next()
	}
	c.regs[0] = 0
	c.hi = next()
	c.lo = next()
	c.pc = next()
	c.nextPC = next()
	c.inDelaySlot = next() != 0
	for r := 0; r < 32; r++ {
		for s := 0; s < 8; s++ {
			c.cp0[r][s] = next()
		}
	}
	for r := 0; r < 32; r++ {
		c.fpr[r] = next()
	}
	for r := 0; r < 32; r++ {
		c.fcr[r] = next()
	}
	for r := 0; r < 32; r++ {
		c.icReg[r] = next()
	}
	c.llbit = next() != 0
	c.lladdr = next()
	c.halted = next() != 0
	c.countRemainder = next()
	c.timerIRQ = next() != 0
	c.vfpuFaultCount = int(next())
	c.randomState = next()
	c.extIP = next()
	for r := 0; r < 128; r++ {
		c.vregs[r] = next()
	}
	for r := 0; r < 32; r++ {
		c.ctrl[r] = next()
	}
	for r := 0; r < 144; r++ {
		c.mfvcStore[r] = next()
	}
	for r := 0; r < 8; r++ {
		c.rngState[r] = next()
	}
	c.spfxImm = next()
	c.tpfxImm = next()
	c.dpfxImm = next()
	c.spfxOn = next() != 0
	c.tpfxOn = next() != 0
	c.dpfxOn = next() != 0
	c.cc = next()
	c.curPC = c.pc
	c.curDelay = c.inDelaySlot
	return nil
}

func boolWord(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

type stateError string

func (e stateError) Error() string { return string(e) }

const errBadStateSize = stateError("allegrex: serialized state has wrong length")

// ThreadStatus is the CP0 Status value a PSP thread context
// starts with: the FPU enabled (CU1), user mode, interrupts
// enabled, and the exception vector in RAM (BEV clear, ERL and
// EXL clear), so guest exception handlers installed at
// 0x80000180 receive faults. A bare CPU reset instead carries
// the boot value BEV|ERL; the pspautotests framework handler
// (which skips faulting data words embedded in code streams)
// only works with the thread value, which is how the corpus
// pinned this down.
const ThreadStatus = StatusCU1 | 0x11

// SetSR replaces the CP0 Status register. The kernel uses it
// when building a new thread's context.
func (c *CPU) SetSR(v uint32) { c.cp0[12][0] = v }

// ClearLL drops any outstanding LL reservation. The kernel calls
// it at the points where hardware loses the reservation: syscall
// dispatch (the SYSCALL exception breaks it) and thread switches
// (the return from the kernel through ERET breaks it). The
// pspautotests llsc case measures exactly this behavior.
func (c *CPU) ClearLL() { c.llbit = false }

// DefaultFCSR is the FPU control register value a PSP thread
// starts with: rounding mode nearest, the overflow, divide by
// zero and invalid operation enables set (0x00000E00, recorded by
// the pspautotests fcr test's initial readback).
const DefaultFCSR = 0x00000E00

// SetFCSR sets the FPU control and status register. The kernel
// uses it when building a new thread's context.
func (c *CPU) SetFCSR(v uint32) { c.fcr[31] = v }
