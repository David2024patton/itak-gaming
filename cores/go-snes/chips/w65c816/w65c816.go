// Package w65c816 is an original implementation of the
// Western Design Center W65C816S CPU, written from the
// published datasheet and the programming documentation
// collected for the chip (Eyes and Lichty's "Programming the
// 65816" and the SNES development documentation that restates
// the datasheet's cycle tables).
//
// The 65816 is a 16-bit extension of the 6502: a 16-bit
// accumulator whose high byte is separately addressable, 16-bit
// index registers, a 24-bit address space split into a program
// bank and a data bank, a relocatable direct page, a 16-bit
// stack in native mode, and two processor widths (the M and X
// status flags) that most instructions obey. It powers on in
// emulation mode, where it behaves like a 6502 with its stack
// confined to page 1 and its widths forced to 8 bits, until
// software swaps the carry and emulation flags with XCE.
//
// Cycle counting mirrors the go-nes 6502 in this repository:
// every bus access the CPU makes takes exactly one cycle, in
// the documented order (including the dummy reads indexed
// modes perform), and internal cycles are charged where the
// datasheet's tables show them, so a machine hanging devices
// off the bus observes accesses in the hardware's order and
// count. Because SNES memory timing depends on what is being
// accessed, the bus also reports an access speed per address;
// the CPU accumulates both bus cycles (Cycles) and master
// clock cycles (MasterCycles) so DMA and HDMA engines in the
// system package can interleave against real time later.
//
// Interrupts poll the way the hardware polls them: the lines
// are sampled before every cycle the CPU spends, and the
// sample taken before an instruction's last cycle is the
// decision the next Step acts on. NMI is an edge latch with
// priority over the level-triggered IRQ, and a latched NMI
// hijacks a BRK or IRQ sequence at its vector fetch, as on
// the 6502. ABORT is exposed at the documented level this
// core promises: the line is sampled like IRQ, and when it
// is recognized the CPU takes the abort vector at the
// next instruction boundary, pushing the address of the
// instruction that was about to execute, rather than
// modeling the chip's cycle-by-cycle partial register
// state mid-instruction. The SNES never asserts
// ABORT (the pin is tied inactive on the console), so the
// approximation costs nothing there.
//
// Block moves (MVN and MVP) are interruptible the way the
// hardware makes them: each byte moves in a seven-cycle
// iteration that re-fetches the instruction, the CPU polls
// between bytes, and when an interrupt is recognized the
// move suspends with its progress in A, X, and Y exactly as
// the hardware leaves them and PC on the move's own opcode,
// the interrupt is taken, and the re-executed move
// continues after the handler returns. No move state lives
// outside the programmer-visible registers.
package w65c816

import (
	"encoding/binary"
	"errors"
)

// Speed is the access speed a bus reports for an address. On
// the SNES the same bus cycle lasts 6, 8, or 12 master clocks
// depending on the region accessed; the bare CPU does not
// care, but the console's DMA timing does, so the CPU tracks
// the weighted total alongside the plain cycle count.
type Speed uint8

const (
	// SpeedFast accesses complete in 6 master clocks (FastROM
	// in the high banks, and the CPU's internal cycles).
	SpeedFast Speed = iota
	// SpeedSlow accesses complete in 8 master clocks (Work
	// RAM, most ROM, most cartridge space).
	SpeedSlow
	// SpeedXSlow accesses complete in 12 master clocks (the
	// console's IO and controller region).
	SpeedXSlow
)

// MasterCycles returns how many master clocks an access at
// this speed occupies on the SNES.
func (s Speed) MasterCycles() uint64 {
	switch s {
	case SpeedFast:
		return 6
	case SpeedSlow:
		return 8
	case SpeedXSlow:
		return 12
	}
	return 8
}

// internalMasterCycles is the master-clock cost of a cycle
// the CPU spends internally.
const internalMasterCycles = 6

// Bus is the machine interface the CPU drives. Addresses are
// presented as a bank and a 16-bit offset, exactly as the
// chip's pins separate them. Every Read or Write is one bus
// cycle.
type Bus interface {
	Read(bank uint8, addr uint16) uint8
	Write(bank uint8, addr uint16, val uint8)
	// Speed reports the access speed of an address. The CPU
	// asks once per access, before performing it.
	Speed(bank uint8, addr uint16) Speed
}

// Status register bit masks. Unlike the 6502, every bit of
// the 65816 status register is real storage: bit 5 is the M
// flag (accumulator and memory width) and bit 4 is the X flag
// (index width). In emulation mode the M and X flags are
// forced to 1 in effect whatever the stored bits say, and an
// interrupt or PHP push presents bit 4 as the 6502's B flag
// instead; see statusByte.
const (
	FlagC = 0x01 // carry
	FlagZ = 0x02 // zero
	FlagI = 0x04 // interrupt disable
	FlagD = 0x08 // decimal mode
	FlagX = 0x10 // index register width (1 = 8 bit)
	FlagM = 0x20 // accumulator/memory width (1 = 8 bit)
	FlagV = 0x40 // overflow
	FlagN = 0x80 // negative
)

// Vector addresses (all in bank 0).
const (
	vecNativeCOP   = 0xFFE4
	vecNativeBRK   = 0xFFE6
	vecNativeAbort = 0xFFE8
	vecNativeNMI   = 0xFFEA
	vecNativeIRQ   = 0xFFEE

	vecEmuCOP   = 0xFFF4
	vecEmuAbort = 0xFFF8
	vecEmuNMI   = 0xFFFA
	vecReset    = 0xFFFC
	vecEmuIRQ   = 0xFFFE // shared by IRQ and BRK in emulation mode
)

// SerializeSize is the fixed save-state size in bytes.
const SerializeSize = 96

const serializeVersion = 1

// Registers is a snapshot of the programmer-visible CPU
// state. A is the full 16-bit accumulator (the datasheet's
// C), with A>>8 the B accumulator. P is the stored status
// byte; in emulation mode its M and X bits are stored but
// inert (the effective widths are 8 bit).
type Registers struct {
	A, X, Y, S, D uint16
	PC            uint16
	DBR, PBR      uint8
	P             uint8
	E             bool
}

// CPU is a single W65C816S instance.
type CPU struct {
	bus Bus

	a, x, y, s, d uint16
	pc            uint16
	dbr, pbr      uint8
	p             uint8
	e             bool

	cycles uint64 // bus cycles since power-on
	master uint64 // master clocks since power-on

	nmiLine    bool
	nmiPending bool
	irqLine    bool
	abortLine  bool

	waiting bool // WAI executed, awaiting an interrupt input
	stopped bool // STP executed; only Reset recovers

	// Interrupt poll state: samples refresh before every
	// cycle, and the takes are the decision latched at the
	// end of each Step.
	smpNMI, smpIRQ, smpAbort bool
	takeNMI, takeIRQ         bool
	takeAbort                bool
	abortPC                  uint16 // PC of the instruction ABORT preempts
	abortPBR                 uint8

	// The most recent block move's shape, kept for state
	// inspection: a suspended move needs nothing else,
	// since its progress lives in A, X, Y, and PC.
	mvOp       uint8
	mvSrcBank  uint8
	mvDstBank  uint8
	mvOpcodePC uint16

	// budget, when positive, is a hard cycle stop used by
	// test harnesses: charge panics with budgetExhausted
	// once the cycle counter reaches it, which lets a
	// harness cut an execution at an exact cycle (the
	// single-step corpus truncates block-move traces at
	// 100 cycles, mid-iteration). Production code never
	// sets it.
	budget uint64
}

// New returns a CPU in its power-on state: emulation mode,
// registers zeroed. Call Reset (with a bus attached) to run
// the reset sequence.
func New() *CPU {
	return &CPU{e: true}
}

// Attach wires the CPU to a bus.
func (c *CPU) Attach(bus Bus) { c.bus = bus }

// Reset runs the hardware reset sequence: emulation mode is
// forced, the M, X, I, and D flags take their reset values,
// the direct page and both banks clear, the stack pointer is
// loaded with 0x01FF, and the program counter loads from the
// reset vector. Seven cycles pass: five internal and the two
// vector fetches.
func (c *CPU) Reset() {
	c.e = true
	c.p = FlagM | FlagX | FlagI // M, X set; D, C, Z, V, N clear
	c.d = 0
	c.dbr = 0
	c.pbr = 0
	c.s = 0x01FF
	c.nmiPending = false
	c.waiting = false
	c.stopped = false
	c.smpNMI, c.smpIRQ, c.smpAbort = false, false, false
	c.takeNMI, c.takeIRQ, c.takeAbort = false, false, false
	for i := 0; i < 5; i++ {
		c.io()
	}
	lo := c.read(0, vecReset)
	hi := c.read(0, vecReset+1)
	c.pc = uint16(hi)<<8 | uint16(lo)
}

// Regs returns a snapshot of the programmer-visible state.
// In emulation mode the exported stack pointer always
// shows 0x0100|(low byte), and the status register always
// shows the M and X bits set, as the hardware presents
// them and the corpus records them, whatever the internal
// storage currently holds.
func (c *CPU) Regs() Registers {
	s, p := c.s, c.p
	if c.e {
		s = 0x0100 | (c.s & 0x00FF)
		p = c.p | FlagM | FlagX
	}
	return Registers{
		A: c.a, X: c.x, Y: c.y, S: s, D: c.d, PC: c.pc,
		DBR: c.dbr, PBR: c.pbr, P: p, E: c.e,
	}
}

// SetRegs installs a register snapshot. The single-step
// conformance runner uses it to place the CPU in corpus
// states; in emulation mode the stack pointer is folded
// to 0x0100|(low byte), the form the corpus's counter
// starts from (see stackAddr), since corpus initial
// states carry random junk in the stored high byte.
func (c *CPU) SetRegs(r Registers) {
	c.a, c.x, c.y, c.s, c.d = r.A, r.X, r.Y, r.S, r.D
	c.pc = r.PC
	c.dbr, c.pbr = r.DBR, r.PBR
	c.p = r.P
	c.e = r.E
	if c.e {
		c.s = 0x0100 | (c.s & 0x00FF)
	}
	c.waiting = false
	c.stopped = false
	c.takeNMI, c.takeIRQ, c.takeAbort = false, false, false
	c.smpNMI, c.smpIRQ, c.smpAbort = false, false, false
	c.nmiPending = false
}

// PC returns the current program counter (within PBR).
func (c *CPU) PC() uint32 { return uint32(c.pbr)<<16 | uint32(c.pc) }

// Cycles returns the bus cycles executed since power-on,
// including reset, internal, and stall cycles.
func (c *CPU) Cycles() uint64 { return c.cycles }

// MasterCycles returns the master clocks those cycles
// occupy at the access speeds the bus reported.
func (c *CPU) MasterCycles() uint64 { return c.master }

// SetNMILine drives the NMI pin. The input is edge
// triggered: a low-to-high transition latches a pending NMI.
func (c *CPU) SetNMILine(high bool) {
	if high && !c.nmiLine {
		c.nmiPending = true
	}
	c.nmiLine = high
}

// SetIRQLine drives the IRQ pin, level triggered and masked
// by the I flag at the poll point.
func (c *CPU) SetIRQLine(asserted bool) { c.irqLine = asserted }

// SetAbortLine drives the ABORT pin, level triggered and
// not masked by any flag.
func (c *CPU) SetAbortLine(asserted bool) { c.abortLine = asserted }

// NMIPending reports whether an NMI edge is latched.
func (c *CPU) NMIPending() bool { return c.nmiPending }

// Waiting reports whether the CPU sits in WAI.
func (c *CPU) Waiting() bool { return c.waiting }

// Stopped reports whether STP has halted the CPU.
func (c *CPU) Stopped() bool { return c.stopped }

// m8 reports the effective accumulator/memory width:
// forced 8 bit in emulation mode.
func (c *CPU) m8() bool { return c.e || c.p&FlagM != 0 }

// x8 reports the effective index width.
func (c *CPU) x8() bool { return c.e || c.p&FlagX != 0 }

// sampleInterrupts records the interrupt lines as they stand
// before a cycle is spent.
func (c *CPU) sampleInterrupts() {
	if !c.nmiPending && !c.irqLine && !c.abortLine {
		// The quiet fast path: no line is active and
		// no NMI is latched, so the sample is all
		// clear. This still runs at every access, so
		// a pulse spanning any access is seen by the
		// full path below.
		c.smpNMI = false
		c.smpIRQ = false
		c.smpAbort = false
		return
	}
	c.smpNMI = c.nmiPending
	c.smpIRQ = c.irqLine && c.p&FlagI == 0
	c.smpAbort = c.abortLine
}

// budgetExhausted is the panic charge raises when a test
// harness cycle budget runs out; see the budget field.
type budgetExhausted struct{}

// SetCycleBudget arms the hard cycle stop described on the
// budget field, as an absolute cycle count (the value of
// Cycles at which to stop); zero disarms it. Harness use
// only.
func (c *CPU) SetCycleBudget(cycles uint64) { c.budget = cycles }

// latchPoll moves the latest samples into the decisions the
// next Step acts on.
func (c *CPU) latchPoll() {
	c.takeNMI = c.smpNMI
	c.takeIRQ = c.smpIRQ
	if c.smpAbort && !c.takeAbort {
		// The abort is recognized at the boundary: the
		// return address pushed is the instruction that has
		// not executed yet (see the package comment).
		c.takeAbort = true
		c.abortPC = c.pc
		c.abortPBR = c.pbr
	}
}

// charge accounts for one bus cycle at a master-clock cost.
func (c *CPU) charge(master uint64) {
	c.cycles++
	c.master += master
	if c.budget > 0 && c.cycles >= c.budget {
		panic(budgetExhausted{})
	}
}

// read performs one bus read and charges one cycle at the
// speed the bus reports for the address.
func (c *CPU) read(bank uint8, addr uint16) uint8 {
	c.sampleInterrupts()
	sp := c.bus.Speed(bank, addr)
	v := c.bus.Read(bank, addr)
	c.charge(sp.MasterCycles())
	return v
}

// write performs one bus write and charges one cycle.
func (c *CPU) write(bank uint8, addr uint16, val uint8) {
	c.sampleInterrupts()
	sp := c.bus.Speed(bank, addr)
	c.bus.Write(bank, addr, val)
	c.charge(sp.MasterCycles())
}

// read24 reads at a full 24-bit address.
func (c *CPU) read24(addr uint32) uint8 {
	return c.read(uint8(addr>>16), uint16(addr))
}

// write24 writes at a full 24-bit address.
func (c *CPU) write24(addr uint32, val uint8) {
	c.write(uint8(addr>>16), uint16(addr), val)
}

// io charges one internal cycle: no bus access, 6 master
// clocks, interrupts still sampled (the hardware polls
// during internal cycles too).
func (c *CPU) io() {
	c.sampleInterrupts()
	c.charge(internalMasterCycles)
}

// dummy charges one bus cycle at the given address
// without transferring data: the dead cycles the hardware
// spends driving a partially fixed address while an index
// addition completes (indexed reads on a page cross,
// indexed stores, read-modify-write fixups). The
// single-step corpus records these cycles with no
// valid-access pins set, so this core makes no bus access
// for them either; the cycle still costs the target
// address's access speed.
func (c *CPU) dummy(bank uint8, addr uint16) {
	c.sampleInterrupts()
	c.charge(c.bus.Speed(bank, addr).MasterCycles())
}

// dummyWriter is the optional bus extension for the
// write half of a read-modify-write pair: hardware drives
// the original value onto the bus in a cycle the
// single-step corpus records as not valid, before the
// modified value's real write. A bus that implements it
// performs the write without counting it as a data
// transfer; other buses get a plain write.
type dummyWriter interface {
	WriteDummy(bank uint8, addr uint16, val uint8)
}

// writeDummy writes through the dummy channel (see
// dummyWriter), charging one cycle at the address's
// speed.
func (c *CPU) writeDummy(bank uint8, addr uint16, val uint8) {
	if dw, ok := c.bus.(dummyWriter); ok {
		c.sampleInterrupts()
		dw.WriteDummy(bank, addr, val)
		c.charge(c.bus.Speed(bank, addr).MasterCycles())
		return
	}
	c.write(bank, addr, val)
}

// fetch reads the byte at PBR:PC and advances PC within its
// bank. The advance happens before the read's cycle is
// charged, so a harness cycle budget cutting the execution
// inside this fetch leaves PC past the byte, the position
// the corpus's truncated states show.
func (c *CPU) fetch() uint8 {
	addr := c.pc
	c.pc++
	return c.read(c.pbr, addr)
}

// fetch16 fetches a little-endian 16-bit operand.
func (c *CPU) fetch16() uint16 {
	lo := c.fetch()
	hi := c.fetch()
	return uint16(hi)<<8 | uint16(lo)
}

// stackAddr is the bank-0 address the stack currently
// occupies.
//
// In emulation mode the single-step corpus's stack model
// is unusual, and this core follows it exactly: the
// pointer is a plain counter that is folded to
// 0x0100|(low byte) whenever a register snapshot is
// installed or emulation is entered, and then increments
// and decrements without masking. A push sequence that
// underflows 0x0100 continues into page 0 (a PHD from a
// low byte of 0x00 writes at 0x0100 then 0x00FF); a pull
// sequence crossing 0x01FF reads on into 0x0200 (a PLD
// from a low byte of 0xFE reads at 0x01FF then 0x0200).
// The exported S always reads 0x0100|(low byte). The
// datasheet describes the emulation stack as confined to
// page 1; the corpus is the gate for this core. In
// practice the difference needs a stack crossing the
// page 1 boundary in emulation mode, which real software
// never does. Interrupt entry pushes are the exception:
// they address the stack through the page-1 form on every
// access (see pushInt).
func (c *CPU) stackAddr() uint16 {
	return c.s
}

// decS moves the stack pointer down after a push.
func (c *CPU) decS() { c.s-- }

// incS moves the stack pointer up before a pull.
func (c *CPU) incS() { c.s++ }

// push writes a byte to the stack and decrements S.
func (c *CPU) push(v uint8) {
	c.write(0, c.stackAddr(), v)
	c.decS()
}

// push16 pushes a 16-bit value, high byte first.
func (c *CPU) push16(v uint16) {
	c.push(uint8(v >> 8))
	c.push(uint8(v))
}

// pushInt pushes a byte as part of an interrupt entry
// sequence. In emulation mode the corpus's interrupt
// microprogram addresses the stack with the pointer's
// high byte pinned to page 1 on every access (a push
// sequence crossing 0x0100 wraps to 0x01FF), where
// ordinary instruction pushes follow the nine-bit counter
// of stackAddr. Native mode is identical to push.
func (c *CPU) pushInt(v uint8) {
	if c.e {
		c.write(0, 0x0100|(c.s&0x00FF), v)
		c.s = 0x0100 | ((c.s - 1) & 0x00FF)
		return
	}
	c.push(v)
}

// pull increments S and reads the byte it lands on.
func (c *CPU) pull() uint8 {
	c.incS()
	return c.read(0, c.stackAddr())
}

// pullB pulls one byte the way the corpus's single-byte
// pull instructions (PLA, PLP, PLX, PLY) and the RTS/RTI
// sequences do in emulation mode: the increment wraps
// inside page 1 (0x01FF up to 0x0100). The 16-bit pulls
// (PLD, RTL) use plain pull and pull16 instead; the
// corpus traces pin the split down exactly.
func (c *CPU) pullB() uint8 {
	if c.e {
		c.s = 0x0100 | ((c.s + 1) & 0x00FF)
		return c.read(0, c.s)
	}
	return c.pull()
}

// pull16 pulls a 16-bit value, low byte first.
func (c *CPU) pull16() uint16 {
	lo := c.pull()
	hi := c.pull()
	return uint16(hi)<<8 | uint16(lo)
}

// statusByte renders P for a stack push. In native mode the
// stored byte goes out as it is. In emulation mode the push
// follows the 6502 convention: bit 5 reads 1 and bit 4 is
// the B flag, set for PHP and BRK, clear for IRQ and NMI.
func (c *CPU) statusByte(brk bool) uint8 {
	if !c.e {
		return c.p
	}
	p := c.p | FlagM // bit 5 (the M position) reads 1
	if brk {
		p |= FlagX // bit 4 position carries B in emulation pushes
	} else {
		p &^= FlagX
	}
	return p
}

// setP installs a pulled status byte. If the X flag is now
// set (8-bit index), the index registers' high bytes clear,
// as the hardware clears them whenever 8-bit index width is
// in force.
func (c *CPU) setP(v uint8) {
	c.p = v
	if c.x8() {
		c.x &= 0x00FF
		c.y &= 0x00FF
	}
}

// Step executes one instruction, one interrupt sequence,
// one block-move resumption, or one idle cycle (WAI/STP),
// and returns the bus cycles it consumed.
func (c *CPU) Step() int {
	start := c.cycles
	if c.stopped {
		c.io()
		c.latchPoll()
		return int(c.cycles - start)
	}
	if c.waiting {
		c.io()
		// WAI ends when an interrupt input is active, even
		// one the I flag masks; a masked IRQ just wakes the
		// CPU, an unmasked one (or an NMI) is then taken
		// through the ordinary poll decisions.
		if c.nmiPending || c.irqLine {
			c.waiting = false
		}
		c.latchPoll()
		return int(c.cycles - start)
	}
	if c.takeAbort {
		c.takeAbort = false
		c.hardwareInterrupt(c.interruptVector(true, false), true, false)
		c.latchPoll()
		return int(c.cycles - start)
	}
	if c.takeNMI {
		c.takeNMI = false
		c.nmiPending = false
		c.hardwareInterrupt(c.interruptVector(false, true), false, false)
		c.latchPoll()
		return int(c.cycles - start)
	}
	if c.takeIRQ {
		c.takeIRQ = false
		c.hardwareInterrupt(c.interruptVector(false, false), false, true)
		c.latchPoll()
		return int(c.cycles - start)
	}
	op := c.fetch()
	c.exec(op)
	c.latchPoll()
	return int(c.cycles - start)
}

// hardwareInterrupt runs the NMI, IRQ, or ABORT sequence
// to the given vector. Native mode pushes PBR, PC, and P
// and reads the native vectors (8 cycles); emulation mode
// pushes PC and a B-clear P and reads the emulation vectors
// (7 cycles). When irq is true an NMI latched after the
// sequence began hijacks the entry to the NMI vector, as
// the hardware does for IRQ (and BRK) entries.
func (c *CPU) hardwareInterrupt(vec uint16, abort, irq bool) {
	c.io()
	c.io()
	if c.e {
		ret := c.pc
		if abort {
			ret = c.abortPC
		}
		c.pushInt(uint8(ret >> 8))
		c.pushInt(uint8(ret))
		c.pushInt(c.statusByte(false))
	} else {
		ret, retBank := c.pc, c.pbr
		if abort {
			ret, retBank = c.abortPC, c.abortPBR
		}
		c.pushInt(retBank)
		c.pushInt(uint8(ret >> 8))
		c.pushInt(uint8(ret))
		c.pushInt(c.p)
	}
	c.p |= FlagI
	c.p &^= FlagD
	if irq && c.nmiPending {
		c.nmiPending = false
		vec = c.interruptVector(false, true)
	}
	lo := c.read(0, vec)
	hi := c.read(0, vec+1)
	c.pbr = 0
	c.pc = uint16(hi)<<8 | uint16(lo)
}

// interruptVector picks a vector address. nmi selects the
// NMI vector over the IRQ one; abort selects ABORT over
// both.
func (c *CPU) interruptVector(abort, nmi bool) uint16 {
	if c.e {
		switch {
		case abort:
			return vecEmuAbort
		case nmi:
			return vecEmuNMI
		default:
			return vecEmuIRQ
		}
	}
	switch {
	case abort:
		return vecNativeAbort
	case nmi:
		return vecNativeNMI
	default:
		return vecNativeIRQ
	}
}

// softwareInterrupt runs BRK and COP: the program counter
// skips the signature byte that follows the opcode, then
// the sequence is the hardware one with a B-set status in
// emulation mode and the BRK or COP vector.
func (c *CPU) softwareInterrupt(cop bool) {
	c.fetch() // the signature byte is read and skipped
	if c.e {
		vec := uint16(vecEmuIRQ)
		if cop {
			vec = vecEmuCOP
		}
		c.pushInt(uint8(c.pc >> 8))
		c.pushInt(uint8(c.pc))
		c.pushInt(c.statusByte(true))
		c.p |= FlagI
		c.p &^= FlagD
		if c.nmiPending {
			c.nmiPending = false
			vec = vecEmuNMI
		}
		lo := c.read(0, vec)
		hi := c.read(0, vec+1)
		c.pbr = 0
		c.pc = uint16(hi)<<8 | uint16(lo)
		return
	}
	vec := uint16(vecNativeBRK)
	if cop {
		vec = vecNativeCOP
	}
	c.pushInt(c.pbr)
	c.pushInt(uint8(c.pc >> 8))
	c.pushInt(uint8(c.pc))
	c.pushInt(c.p)
	c.p |= FlagI
	c.p &^= FlagD
	if c.nmiPending {
		c.nmiPending = false
		vec = vecNativeNMI
	}
	lo := c.read(0, vec)
	hi := c.read(0, vec+1)
	c.pbr = 0
	c.pc = uint16(hi)<<8 | uint16(lo)
}

// Serialize writes the CPU state in this package's fixed
// layout.
func (c *CPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	put := func(off int, v uint16) { binary.LittleEndian.PutUint16(out[off:], v) }
	put(1, c.a)
	put(3, c.x)
	put(5, c.y)
	put(7, c.s)
	put(9, c.d)
	put(11, c.pc)
	out[13] = c.dbr
	out[14] = c.pbr
	out[15] = c.p
	if c.e {
		out[16] = 1
	}
	binary.LittleEndian.PutUint64(out[17:], c.cycles)
	binary.LittleEndian.PutUint64(out[25:], c.master)
	var lines uint8
	if c.nmiLine {
		lines |= 1
	}
	if c.nmiPending {
		lines |= 2
	}
	if c.irqLine {
		lines |= 4
	}
	if c.abortLine {
		lines |= 8
	}
	if c.waiting {
		lines |= 16
	}
	if c.stopped {
		lines |= 32
	}
	out[33] = lines
	var poll uint8
	if c.smpNMI {
		poll |= 1
	}
	if c.smpIRQ {
		poll |= 2
	}
	if c.smpAbort {
		poll |= 4
	}
	if c.takeNMI {
		poll |= 8
	}
	if c.takeIRQ {
		poll |= 16
	}
	if c.takeAbort {
		poll |= 32
	}
	out[34] = poll
	put(35, c.abortPC)
	out[37] = c.abortPBR
	out[38] = c.mvOp
	out[39] = c.mvSrcBank
	out[40] = c.mvDstBank
	// out[41] is reserved (a removed in-flight flag); the
	// layout does not change within a state version.
	put(42, c.mvOpcodePC)
	return out
}

// Deserialize restores a state written by Serialize.
func (c *CPU) Deserialize(in []byte) error {
	if len(in) != SerializeSize {
		return errors.New("w65c816: bad state size")
	}
	if in[0] != serializeVersion {
		return errors.New("w65c816: unsupported state version")
	}
	get := func(off int) uint16 { return binary.LittleEndian.Uint16(in[off:]) }
	c.a, c.x, c.y, c.s, c.d = get(1), get(3), get(5), get(7), get(9)
	c.pc = get(11)
	c.dbr, c.pbr = in[13], in[14]
	c.p = in[15]
	c.e = in[16] != 0
	c.cycles = binary.LittleEndian.Uint64(in[17:])
	c.master = binary.LittleEndian.Uint64(in[25:])
	lines := in[33]
	c.nmiLine = lines&1 != 0
	c.nmiPending = lines&2 != 0
	c.irqLine = lines&4 != 0
	c.abortLine = lines&8 != 0
	c.waiting = lines&16 != 0
	c.stopped = lines&32 != 0
	poll := in[34]
	c.smpNMI = poll&1 != 0
	c.smpIRQ = poll&2 != 0
	c.smpAbort = poll&4 != 0
	c.takeNMI = poll&8 != 0
	c.takeIRQ = poll&16 != 0
	c.takeAbort = poll&32 != 0
	c.abortPC = get(35)
	c.abortPBR = in[37]
	c.mvOp, c.mvSrcBank, c.mvDstBank = in[38], in[39], in[40]
	c.mvOpcodePC = get(42)
	return nil
}
