// Package m68k is an original implementation of the Motorola 68000 CPU,
// written from the behavior documented in the M68000 Programmer's
// Reference Manual: the base instruction set, all 14 addressing modes,
// condition-code semantics, the exception model, and the manual's cycle
// count tables (implemented exactly where the manual is exact, and in
// the documented neighborhood elsewhere, which is what a line-budget
// scheduler in a game console core needs).
//
// This is the original 68000 only: no 68010+ instructions (MOVES, MOVEC,
// BKPT, RTD, long CHK, index scaling) exist here, and their encodings
// raise the illegal-instruction exception as they do on real silicon.
//
// Stack model: a[7] always holds the ACTIVE stack pointer; the inactive
// one lives in usp/ssp and is swapped in by setSupervisor whenever the
// S bit of SR changes, exactly as the hardware swaps A7 with USP/SSP.
package m68k

// Bus is the memory interface the CPU drives. Read32/Write32 move a full
// long word in CPU order; the bus owns the byte ordering of its memory.
type Bus interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, val uint8)
	Write16(addr uint32, val uint16)
	Write32(addr uint32, val uint32)
	Reset()
}

// Registers is a snapshot of the programmer-visible CPU state.
type Registers struct {
	D   [8]uint32
	A   [8]uint32
	PC  uint32
	SR  uint16
	USP uint32
	SSP uint32
	IR  uint16
}

// Status register bits.
const (
	srT    = 0x8000 // trace enable
	srS    = 0x2000 // supervisor state
	srMask = 0x0700 // interrupt priority mask (bits 10-8)
	srX    = 0x0010
	srN    = 0x0008
	srZ    = 0x0004
	srV    = 0x0002
	srC    = 0x0001
	srFull = 0xA71F // all bits that exist on a real 68000 SR
)

// Exception vector numbers used by this implementation.
const (
	vecAddressError = 3
	vecIllegal      = 4
	vecDivZero      = 5
	vecCHK          = 6
	vecTRAPV        = 7
	vecPrivilege    = 8
	vecTrace        = 9
	vecLineA        = 10
	vecLineF        = 11
)

// CPU is a Motorola 68000. The zero value is not usable; call New.
type CPU struct {
	bus Bus

	d   [8]uint32
	a   [8]uint32 // a[7] is the active stack pointer
	usp uint32    // user stack pointer (inactive while supervisor)
	ssp uint32    // supervisor stack pointer (inactive while user)
	pc  uint32
	sr  uint16
	ir  uint16 // last opcode fetched (the 68000's instruction register)

	// instrPC is the address of the instruction currently executing.
	// Exceptions raised because an instruction could not execute at
	// all (illegal encoding, line A and F, privilege violation) stack
	// this address, so a handler can inspect the offending word; only
	// exceptions raised by a completed instruction (TRAP, CHK, divide
	// by zero, ...) stack the following address.
	instrPC uint32

	cycles  uint64
	stopped bool // STOP executed; waiting for an acceptable interrupt
	halted  bool // double fault; the processor is dead until Reset

	// Pending (latched) interrupt request. A latched request survives
	// while the SR mask inhibits it and is serviced when the mask drops.
	pendPresent bool
	pendLevel   uint8
	pendVec     uint8
	pendVecOK   bool // an explicit vector was supplied

	tookException bool // set when the current step entered an exception
	inEntry       bool // inside exception entry (double-fault guard)
}

// New creates a CPU attached to bus. Call Reset to power it on.
func New(bus Bus) *CPU {
	return &CPU{bus: bus}
}

// Reset powers the CPU on: SR = 0x2700 (supervisor, mask 7, trace off),
// the supervisor stack pointer is loaded from vector 0 and the PC from
// vector 1, and all pending state is cleared.
//
// Note: CPU reset does not pulse the bus Reset line in this model; the
// surrounding system owns peripheral reset sequencing.
func (c *CPU) Reset() {
	c.d = [8]uint32{}
	c.a = [8]uint32{}
	c.sr = 0x2700
	c.a[7] = c.bus.Read32(0)
	c.ssp = c.a[7]
	c.usp = 0
	c.pc = c.bus.Read32(4)
	c.ir = 0
	c.cycles = 0
	c.stopped = false
	c.halted = false
	c.pendPresent = false
	c.tookException = false
	c.inEntry = false
}

// Cycles returns the total cycles consumed since Reset (plus any credit
// added with AddCycles).
func (c *CPU) Cycles() uint64 { return c.cycles }

// AddCycles advances the cycle counter without executing anything; the
// caller uses it to account for external bus-hold periods (for example
// DMA seizing the bus) and to restore a saved counter after SetState.
func (c *CPU) AddCycles(n uint64) { c.cycles += n }

// Halted reports whether the CPU has halted (double fault).
func (c *CPU) Halted() bool { return c.halted }

// Registers returns the live programmer-visible state. A[7] is the
// active stack pointer; the USP/SSP fields hold the user/supervisor
// stack pointers, with the active one mirroring A[7].
func (c *CPU) Registers() Registers {
	var r Registers
	r.D = c.d
	r.A = c.a
	if c.sr&srS != 0 {
		r.SSP = c.a[7]
		r.USP = c.usp
	} else {
		r.USP = c.a[7]
		r.SSP = c.ssp
	}
	r.PC = c.pc
	r.SR = c.sr
	r.IR = c.ir
	return r
}

// SetState installs a register snapshot for save-state restore. The
// contract (which the restore path depends on) is: D, SR, USP, SSP, PC
// and IR are copied; A[0..6] are copied; A[7] is NOT taken from the
// argument's A[7] but derived from the mode: SSP when SR bit 13
// (supervisor) is set, USP otherwise. Stopped and halted are cleared,
// pending interrupts are cleared, and the cycle counter is set to 0
// (the caller re-adds any saved count with AddCycles).
func (c *CPU) SetState(r Registers) {
	c.d = r.D
	for i := 0; i < 7; i++ {
		c.a[i] = r.A[i]
	}
	c.pc = r.PC
	c.sr = r.SR & srFull
	c.usp = r.USP
	c.ssp = r.SSP
	if c.sr&srS != 0 {
		c.a[7] = r.SSP
	} else {
		c.a[7] = r.USP
	}
	c.ir = r.IR
	c.stopped = false
	c.halted = false
	c.pendPresent = false
	c.tookException = false
	c.inEntry = false
	c.cycles = 0
}

// RequestInterrupt latches a pending interrupt at the given priority
// level (1-7). vector, when non-nil, is the exception vector number to
// use; nil requests an autovector interrupt (vector 24+level). A latched
// request is not dropped while the SR interrupt mask inhibits it: it is
// serviced at the first instruction boundary after the mask drops below
// the level. Level 7 is non-maskable. A newly arriving request replaces
// a latched one of equal or lower level.
func (c *CPU) RequestInterrupt(level uint8, vector *uint8) {
	if level == 0 || level > 7 || c.halted {
		return
	}
	if c.pendPresent && level < c.pendLevel {
		return
	}
	c.pendPresent = true
	c.pendLevel = level
	if vector != nil {
		c.pendVec = *vector
		c.pendVecOK = true
	} else {
		c.pendVecOK = false
	}
}

// Step executes one instruction, or one exception entry, and returns the
// cycles consumed. While the CPU is stopped (STOP) with no acceptable
// interrupt pending, Step advances time by 4 cycles without executing.
func (c *CPU) Step() int {
	if c.halted {
		c.cycles += 4
		return 4
	}
	if c.stopped {
		if c.intAcceptable() {
			c.stopped = false
			n := c.safeEntry(c.serviceInterrupt)
			c.cycles += uint64(n)
			return n
		}
		c.cycles += 4
		return 4
	}
	if c.intAcceptable() {
		n := c.safeEntry(c.serviceInterrupt)
		c.cycles += uint64(n)
		return n
	}
	return c.stepExec()
}

// StepCycles executes instructions until at least budget cycles have
// been consumed and returns the total consumed. A stopped or halted CPU
// consumes the whole budget as elapsed time.
func (c *CPU) StepCycles(budget int) int {
	if budget <= 0 {
		return 0
	}
	if c.halted || (c.stopped && !c.intAcceptable()) {
		c.cycles += uint64(budget)
		return budget
	}
	total := 0
	for total < budget {
		total += c.Step()
	}
	return total
}

// stepExec fetches and executes one instruction, converting internal
// faults (address error, illegal encoding) into exception entries.
func (c *CPU) stepExec() (ret int) {
	defer func() {
		if r := recover(); r != nil {
			switch f := r.(type) {
			case addrError:
				if c.inEntry {
					c.halted = true
					c.cycles += 4
					ret = 4
					return
				}
				n := c.safeEntry(func() int { return c.enterAddrError(f) })
				c.cycles += uint64(n)
				ret = n
			case illegalEncoding:
				if c.inEntry {
					c.halted = true
					c.cycles += 4
					ret = 4
					return
				}
				n := c.safeEntry(func() int { return c.enterExceptionAt(vecIllegal, c.instrPC, 34) })
				c.cycles += uint64(n)
				ret = n
			default:
				panic(r)
			}
		}
	}()
	trace := c.sr&srT != 0
	c.instrPC = c.pc
	op := c.fetch16()
	c.ir = op
	c.tookException = false
	n := c.execute(op)
	c.cycles += uint64(n)
	ret = n
	if trace && !c.tookException && !c.stopped {
		m := c.safeEntry(func() int { return c.enterException(vecTrace, 34) })
		c.cycles += uint64(m)
		ret += m
	}
	return ret
}

// safeEntry runs an exception-entry routine, converting an address error
// raised inside the entry itself (a fault while stacking, i.e. a double
// fault) into the halted state instead of a panic escape.
func (c *CPU) safeEntry(entry func() int) (ret int) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(addrError); ok {
				c.inEntry = false
				c.halted = true
				ret = 4
				return
			}
			panic(r)
		}
	}()
	return entry()
}

// intAcceptable reports whether the latched interrupt may be serviced:
// its level must exceed the SR mask, except level 7, which is
// non-maskable.
func (c *CPU) intAcceptable() bool {
	if !c.pendPresent || c.halted {
		return false
	}
	if c.pendLevel == 7 {
		return true
	}
	return c.pendLevel > uint8((c.sr>>8)&7)
}

// serviceInterrupt performs interrupt entry: the PC and SR are stacked
// on the supervisor stack, S is set, trace is cleared, the mask is
// raised to the interrupt level, and the PC is loaded from the
// interrupt's vector (the requested vector, or the autovector).
// Documented cost: 44 cycles.
func (c *CPU) serviceInterrupt() int {
	level := c.pendLevel
	vec := int(24 + level)
	if c.pendVecOK {
		vec = int(c.pendVec)
	}
	c.pendPresent = false
	oldSR := c.sr
	c.setSupervisor(true)
	c.sr = (oldSR | srS) &^ srT & srFull
	c.sr = (c.sr &^ srMask) | uint16(level)<<8
	c.inEntry = true
	c.push32(c.pc)
	c.push16(oldSR)
	c.inEntry = false
	c.pc = c.bus.Read32(uint32(vec) * 4)
	c.tookException = true
	return 44
}

// enterException performs a software exception entry for an exception
// raised by a completed instruction (TRAP, CHK, divide by zero, ...):
// the address of the next instruction and the SR are stacked on the
// supervisor stack, S is set, trace is cleared, the interrupt mask is
// left unchanged, and the PC is loaded from the vector.
func (c *CPU) enterException(vector int, cycles int) int {
	return c.enterExceptionAt(vector, c.pc, cycles)
}

// enterExceptionAt is enterException with a caller-chosen stacked PC.
// Per the manual, exceptions for instructions that never executed
// (illegal instruction, line A and line F emulation, privilege
// violation) stack the address of the offending instruction itself,
// so a handler can decode the word that caused the exception.
func (c *CPU) enterExceptionAt(vector int, stackPC uint32, cycles int) int {
	c.tookException = true
	oldSR := c.sr
	c.setSupervisor(true)
	c.sr = (oldSR | srS) &^ srT & srFull
	c.inEntry = true
	c.push32(stackPC)
	c.push16(oldSR)
	c.inEntry = false
	c.pc = c.bus.Read32(uint32(vector) * 4)
	return cycles
}

// addrError is an internal fault: a word or long access to an odd
// address. Real hardware raises the address-error exception (vector 3).
type addrError struct {
	addr  uint32
	write bool
}

// illegalEncoding is an internal fault: an opcode bit pattern that the
// decode rejected before any operand fetch (an unassigned EA mode, a
// PC-relative destination, ...). Real hardware raises vector 4.
type illegalEncoding struct{}

// enterAddrError stacks a plausible 68000 group-0 frame: below the
// normal SR/PC frame it places a status word, the instruction register,
// and the access address (the extra information the 68000 stacks for bus
// and address errors). Games should never take this exception; the frame
// layout is a documented simplification.
func (c *CPU) enterAddrError(ae addrError) int {
	c.tookException = true
	oldSR := c.sr
	c.setSupervisor(true)
	c.sr = (oldSR | srS) &^ srT & srFull
	var stat uint16
	if ae.write {
		stat |= 0x10 // read/write indicator, simplified
	}
	c.inEntry = true
	c.push32(ae.addr) // access address
	c.push16(c.ir)    // instruction register
	c.push16(stat)    // status information word
	c.push32(c.instrPC)
	c.push16(oldSR)
	c.inEntry = false
	c.pc = c.bus.Read32(uint32(vecAddressError) * 4)
	return 50
}

// setSupervisor swaps the stack pointers so that a[7] is the stack
// pointer for the requested state. It only swaps the pointers; callers
// manage the S bit of SR themselves.
func (c *CPU) setSupervisor(on bool) {
	cur := c.sr&srS != 0
	if cur == on {
		return
	}
	if on {
		c.usp = c.a[7]
		c.a[7] = c.ssp
	} else {
		c.ssp = c.a[7]
		c.a[7] = c.usp
	}
}

// applySR installs a complete new SR value (MOVE to SR, RTE, STOP),
// swapping stack pointers if the S bit changes.
func (c *CPU) applySR(v uint16) {
	v &= srFull
	c.setSupervisor(v&srS != 0)
	c.sr = v
}

// setCCR installs a new condition-code byte (low byte of SR), preserving
// the rest of SR. Only the flag bits that exist are taken.
func (c *CPU) setCCR(v uint16) {
	c.sr = (c.sr & 0xFF00) | (v & 0x1F)
}

// --- memory access -------------------------------------------------
// Word and long accesses to odd addresses raise the address-error
// exception on the 68000; the helpers below panic with addrError and
// Step converts the fault into an exception entry.

func (c *CPU) rd8(addr uint32) uint8 { return c.bus.Read8(addr) }

func (c *CPU) rd16(addr uint32) uint16 {
	if addr&1 != 0 {
		panic(addrError{addr: addr})
	}
	return c.bus.Read16(addr)
}

func (c *CPU) rd32(addr uint32) uint32 {
	if addr&1 != 0 {
		panic(addrError{addr: addr})
	}
	return c.bus.Read32(addr)
}

func (c *CPU) wr8(addr uint32, v uint8) { c.bus.Write8(addr, v) }

func (c *CPU) wr16(addr uint32, v uint16) {
	if addr&1 != 0 {
		panic(addrError{addr: addr, write: true})
	}
	c.bus.Write16(addr, v)
}

func (c *CPU) wr32(addr uint32, v uint32) {
	if addr&1 != 0 {
		panic(addrError{addr: addr, write: true})
	}
	c.bus.Write32(addr, v)
}

// fetch16 reads the next instruction-stream word and advances the PC.
func (c *CPU) fetch16() uint16 {
	v := c.rd16(c.pc)
	c.pc += 2
	return v
}

// fetch32 reads the next instruction-stream long word.
func (c *CPU) fetch32() uint32 {
	hi := uint32(c.fetch16())
	return hi<<16 | uint32(c.fetch16())
}

// push16/push32 store on the active stack (predecrement);
// pop16/pop32 load from it (postincrement).
func (c *CPU) push16(v uint16) {
	c.a[7] -= 2
	c.wr16(c.a[7], v)
}

func (c *CPU) push32(v uint32) {
	c.a[7] -= 4
	c.wr32(c.a[7], v)
}

func (c *CPU) pop16() uint16 {
	v := c.rd16(c.a[7])
	c.a[7] += 2
	return v
}

func (c *CPU) pop32() uint32 {
	v := c.rd32(c.a[7])
	c.a[7] += 4
	return v
}

// --- condition codes -----------------------------------------------

// Operand size codes used throughout the decoder.
const (
	szB = 0 // byte
	szW = 1 // word
	szL = 2 // long
)

func sizeBytes(size int) int { return 1 << uint(size) }

func sizeMask(size int) uint32 {
	if size == szL {
		return 0xFFFFFFFF
	}
	return (uint32(1) << uint(sizeBytes(size)*8)) - 1
}

func signBit(size int) uint32 { return uint32(1) << uint(sizeBytes(size)*8-1) }

// setNZ sets N and Z from a result of the given size, touching nothing
// else.
func (c *CPU) setNZ(v uint32, size int) {
	v &= sizeMask(size)
	c.sr &^= srN | srZ
	if v == 0 {
		c.sr |= srZ
	}
	if v&signBit(size) != 0 {
		c.sr |= srN
	}
}

// flagsLogic applies the standard logical-instruction flags: N and Z
// from the result, V and C cleared, X preserved.
func (c *CPU) flagsLogic(v uint32, size int) {
	c.setNZ(v, size)
	c.sr &^= srV | srC
}

// flagsAdd computes a + b + xIn (xIn is 0 or 1) at the given size and
// sets X, N, Z, V, C per the manual. With stickyZ (ADDX), a zero result
// leaves Z unchanged instead of setting it. Returns the masked result.
func (c *CPU) flagsAdd(a, b uint32, size int, xIn uint8, stickyZ bool) uint32 {
	mask := sizeMask(size)
	sb := signBit(size)
	a &= mask
	b &= mask
	bEff := (b + uint32(xIn)) & mask
	full := uint64(a) + uint64(b) + uint64(xIn)
	res := uint32(full) & mask
	var f uint16
	if full > uint64(mask) {
		f |= srC | srX
	}
	if (a^res)&(bEff^res)&sb != 0 {
		f |= srV
	}
	if res&sb != 0 {
		f |= srN
	}
	if res == 0 && (!stickyZ || c.sr&srZ != 0) {
		f |= srZ
	}
	c.sr = (c.sr &^ (srX | srN | srZ | srV | srC)) | f
	return res
}

// flagsSub computes a - b - xIn (xIn is 0 or 1) at the given size and
// sets X (to the borrow), N, Z, V, C per the manual. With stickyZ
// (SUBX), a zero result leaves Z unchanged. Returns the masked result.
func (c *CPU) flagsSub(a, b uint32, size int, xIn uint8, stickyZ bool) uint32 {
	mask := sizeMask(size)
	sb := signBit(size)
	a &= mask
	b &= mask
	diff := int64(a) - int64(b) - int64(xIn)
	res := uint32(diff) & mask
	var f uint16
	if diff < 0 {
		f |= srC | srX
	}
	if (a^b)&sb != 0 && (res^a)&sb != 0 {
		f |= srV
	}
	if res&sb != 0 {
		f |= srN
	}
	if res == 0 && (!stickyZ || c.sr&srZ != 0) {
		f |= srZ
	}
	c.sr = (c.sr &^ (srX | srN | srZ | srV | srC)) | f
	return res
}

// flagsCmp applies subtraction flags (for CMP/CMPA/CMPM) while
// preserving X, which compares never affect.
func (c *CPU) flagsCmp(a, b uint32, size int) {
	x := c.sr & srX
	c.flagsSub(a, b, size, 0, false)
	c.sr = (c.sr &^ srX) | x
}

// condTrue evaluates a 68000 condition-code field (Bcc/Scc/DBcc).
func (c *CPU) condTrue(cond int) bool {
	switch cond {
	case 0: // T
		return true
	case 1: // F
		return false
	case 2: // HI: !C && !Z
		return c.sr&(srC|srZ) == 0
	case 3: // LS: C || Z
		return c.sr&(srC|srZ) != 0
	case 4: // CC: !C
		return c.sr&srC == 0
	case 5: // CS: C
		return c.sr&srC != 0
	case 6: // NE: !Z
		return c.sr&srZ == 0
	case 7: // EQ: Z
		return c.sr&srZ != 0
	case 8: // VC: !V
		return c.sr&srV == 0
	case 9: // VS: V
		return c.sr&srV != 0
	case 10: // PL: !N
		return c.sr&srN == 0
	case 11: // MI: N
		return c.sr&srN != 0
	case 12: // GE: N == V
		return (c.sr&srN != 0) == (c.sr&srV != 0)
	case 13: // LT: N != V
		return (c.sr&srN != 0) != (c.sr&srV != 0)
	case 14: // GT: !Z && N == V
		return c.sr&srZ == 0 && (c.sr&srN != 0) == (c.sr&srV != 0)
	case 15: // LE: Z || N != V
		return c.sr&srZ != 0 || (c.sr&srN != 0) != (c.sr&srV != 0)
	}
	return false
}

// xBit returns the current X flag as 0 or 1.
func (c *CPU) xBit() uint8 {
	if c.sr&srX != 0 {
		return 1
	}
	return 0
}
