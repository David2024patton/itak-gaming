// Package cpu6502 is an original implementation of the MOS 6502
// CPU as the NES's Ricoh 2A03 runs it, written from the published
// datasheet behavior and the documented results collected on the
// NESdev wiki (including the unofficial opcode page).
//
// The Ricoh variant differs from a stock 6502 in one way that
// matters to software: the decimal flag still exists and can be
// set and cleared, but ADC and SBC always compute in binary. The
// interrupt lines also differ: the 2A03 adds an edge-triggered
// NMI input driven by the PPU alongside the level-triggered IRQ.
//
// Cycle counting is by bus access: every byte the CPU reads or
// writes takes exactly one cycle, and the addressing-mode helpers
// perform the hardware's dummy reads and dummy writes in the
// documented order, so the accesses a machine observes happen in
// the same order and at the same count as on hardware. Internal
// cycles are modeled as dummy reads, again like the hardware,
// which mostly spends them re-reading the byte at PC. A machine
// that hangs timing-sensitive devices off the bus (the PPU, the
// MMC3 IRQ counter, DMA) can therefore watch the accesses go by.
//
// Interrupts are polled at instruction boundaries, which matches
// the hardware's poll point closely enough for the documented
// software-visible behavior at this slice: NMI is an edge latch
// with priority over IRQ, IRQ is masked by the I flag, and BRK
// checks the NMI latch again when it reaches its vector fetch,
// which is how a mid-BRK NMI hijacks the sequence to the NMI
// vector while keeping BRK's pushed bytes. Devices on the bus can
// raise the latch mid-instruction through SetNMILine, because the
// vector fetch happens after the pushes.
package cpu6502

import (
	"encoding/binary"
	"errors"
)

// Bus is the machine interface the CPU drives. Every access is one
// CPU cycle, in order.
type Bus interface {
	Read(addr uint16) uint8
	Write(addr uint16, val uint8)
}

// Status register bit masks. Bit 5 (the "unused" bit) has no
// storage in the hardware: it reads as 1 in every byte pushed to
// the stack, and the B flag (bit 4) exists only in those pushed
// bytes; the live register keeps bit 5 set and bit 4 clear at all
// times.
const (
	FlagC = 0x01 // carry
	FlagZ = 0x02 // zero
	FlagI = 0x04 // interrupt disable
	FlagD = 0x08 // decimal (present but inert on the NES CPU)
	FlagB = 0x10 // break (stack copies only)
	FlagU = 0x20 // always 1 in pushed copies
	FlagV = 0x40 // overflow
	FlagN = 0x80 // negative
)

// Vector addresses.
const (
	VectorNMI   = 0xFFFA
	VectorReset = 0xFFFC
	VectorIRQ   = 0xFFFE // shared by IRQ and BRK
)

// SerializeSize is the fixed save-state size in bytes.
const SerializeSize = 21

const serializeVersion = 1

// Registers is a snapshot of the programmer-visible CPU state. P
// is the live status register as the CPU keeps it (bit 5 set, the
// B bit clear).
type Registers struct {
	A, X, Y, SP, P uint8
	PC             uint16
}

// CPU is a single 6502 instance.
type CPU struct {
	bus Bus

	a, x, y, sp, p uint8
	pc             uint16

	cycles uint64

	nmiLine    bool // current level of the NMI pin
	nmiPending bool // falling-to-rising edge latched, awaiting service
	irqLine    bool // current level of the IRQ pin (true asserts)

	stall  int  // pending RDY stall cycles (OAM DMA and friends)
	jammed bool // a KIL opcode has halted the CPU until reset
}

// New returns a CPU in its power-on state: registers zeroed,
// cycles zeroed, interrupt lines low. Call Reset (with a bus
// attached) to run the reset sequence.
func New() *CPU {
	return &CPU{}
}

// Attach wires the CPU to a bus. The machine owns this; the CPU
// never changes buses on its own.
func (c *CPU) Attach(bus Bus) { c.bus = bus }

// Reset runs the hardware reset sequence: seven cycles, the stack
// pointer drops by three without writes, the I flag sets, the
// pending NMI latch clears (the reset input suppresses it), and PC
// loads from the reset vector. A, X, and Y survive a reset on real
// hardware and survive here too; from power-on they are zero.
func (c *CPU) Reset() {
	// Three stack reads that decrement SP without storing, two
	// internal cycles, then the vector fetch: seven cycles in
	// all, modeled as reads like the hardware performs.
	for i := 0; i < 3; i++ {
		c.read(0x0100 | uint16(c.sp))
		c.sp--
	}
	c.read(c.pc)
	c.read(c.pc)
	c.p = (c.p | FlagI | FlagU) &^ FlagB
	c.nmiPending = false
	c.jammed = false
	lo := c.read(VectorReset)
	hi := c.read(VectorReset + 1)
	c.pc = uint16(hi)<<8 | uint16(lo)
}

// Regs returns a snapshot of the programmer-visible registers.
func (c *CPU) Regs() Registers {
	return Registers{A: c.a, X: c.x, Y: c.y, SP: c.sp, P: c.p, PC: c.pc}
}

// SetRegs installs a register snapshot. P is normalized to the
// live register's invariant (bit 5 set, B clear).
func (c *CPU) SetRegs(r Registers) {
	c.a, c.x, c.y, c.sp = r.A, r.X, r.Y, r.SP
	c.p = (r.P | FlagU) &^ FlagB
	c.pc = r.PC
}

// PC returns the current program counter.
func (c *CPU) PC() uint16 { return c.pc }

// Cycles returns the total cycles executed since power-on,
// including reset and stall cycles.
func (c *CPU) Cycles() uint64 { return c.cycles }

// SetNMILine drives the NMI pin. The input is edge triggered: a
// low-to-high transition latches a pending NMI that the CPU
// services at the next poll point. The latch survives the pin
// dropping again.
func (c *CPU) SetNMILine(high bool) {
	if high && !c.nmiLine {
		c.nmiPending = true
	}
	c.nmiLine = high
}

// SetIRQLine drives the IRQ pin. The input is level triggered and
// is honored only while the I flag is clear.
func (c *CPU) SetIRQLine(asserted bool) { c.irqLine = asserted }

// NMIPending reports whether an NMI edge is latched and awaiting
// service. Machines use it for diagnostics; the CPU clears it when
// the interrupt is taken or on reset.
func (c *CPU) NMIPending() bool { return c.nmiPending }

// Stall adds RDY stall cycles. A DMA engine (OAM DMA in NES-2)
// calls this when it seizes the bus: Step then burns the cycles
// one dummy read at a time before the next instruction, which is
// the state-level hook. Hardware stalls reads only and aligns to
// the clock phase; the NES-2 machine computes the 513 or 514 the
// hardware would spend and hands the total here.
func (c *CPU) Stall(n int) {
	if n > 0 {
		c.stall += n
	}
}

// Jammed reports whether a KIL opcode has halted the CPU. Only a
// reset recovers.
func (c *CPU) Jammed() bool { return c.jammed }

// read performs one bus read and charges one cycle.
func (c *CPU) read(addr uint16) uint8 {
	v := c.bus.Read(addr)
	c.cycles++
	return v
}

// write performs one bus write and charges one cycle.
func (c *CPU) write(addr uint16, val uint8) {
	c.bus.Write(addr, val)
	c.cycles++
}

// fetch reads the byte at PC and advances PC.
func (c *CPU) fetch() uint8 {
	v := c.read(c.pc)
	c.pc++
	return v
}

// dummy charges an internal cycle the way the hardware spends it:
// a read whose value is discarded, usually at PC.
func (c *CPU) dummy(addr uint16) { c.read(addr) }

func (c *CPU) push(v uint8) {
	c.write(0x0100|uint16(c.sp), v)
	c.sp--
}

func (c *CPU) pull() uint8 {
	c.sp++
	return c.read(0x0100 | uint16(c.sp))
}

// statusByte renders P the way an interrupt or BRK pushes it:
// bit 5 always set, B set only for BRK and PHP.
func (c *CPU) statusByte(brk bool) uint8 {
	p := c.p | FlagU
	if brk {
		p |= FlagB
	} else {
		p &^= FlagB
	}
	return p
}

// setP installs a pulled status byte, restoring the live
// register's invariant.
func (c *CPU) setP(v uint8) {
	c.p = (v | FlagU) &^ FlagB
}

// Step executes one instruction, one interrupt sequence, one
// stall cycle, or one jammed cycle, and returns the cycles it
// consumed.
func (c *CPU) Step() int {
	start := c.cycles
	if c.jammed {
		// The hardware jams reading the KIL opcode forever.
		c.read(c.pc)
		return int(c.cycles - start)
	}
	if c.stall > 0 {
		c.stall--
		c.dummy(c.pc)
		return int(c.cycles - start)
	}
	// Interrupt poll, taken before the next opcode fetch. NMI
	// wins over IRQ; IRQ is masked by I.
	if c.nmiPending {
		c.nmiPending = false
		c.interrupt(VectorNMI)
		return int(c.cycles - start)
	}
	if c.irqLine && c.p&FlagI == 0 {
		c.interrupt(VectorIRQ)
		return int(c.cycles - start)
	}
	op := c.fetch()
	c.exec(op)
	return int(c.cycles - start)
}

// interrupt runs the seven-cycle hardware interrupt sequence: two
// internal cycles with PC held (the fetched opcode is discarded),
// the return address and a B-clear status byte pushed, I set, and
// PC loaded from the vector. BRK shares the shape but pushes its
// own bytes; see execBRK.
func (c *CPU) interrupt(vector uint16) {
	c.dummy(c.pc)
	c.dummy(c.pc)
	c.push(uint8(c.pc >> 8))
	c.push(uint8(c.pc))
	c.push(c.statusByte(false))
	c.p |= FlagI
	lo := c.read(vector)
	hi := c.read(vector + 1)
	c.pc = uint16(hi)<<8 | uint16(lo)
}

// Serialize writes the CPU state in this package's fixed layout.
func (c *CPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	out[1] = c.a
	out[2] = c.x
	out[3] = c.y
	out[4] = c.sp
	out[5] = c.p
	binary.LittleEndian.PutUint16(out[6:], c.pc)
	binary.LittleEndian.PutUint64(out[8:], c.cycles)
	binary.LittleEndian.PutUint16(out[16:], uint16(c.stall))
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
	if c.jammed {
		lines |= 8
	}
	out[18] = lines
	// out[19], out[20] are reserved and stay zero in version 1.
	return out
}

// Deserialize restores a state written by Serialize.
func (c *CPU) Deserialize(in []byte) error {
	if len(in) != SerializeSize {
		return errors.New("cpu6502: bad state size")
	}
	if in[0] != serializeVersion {
		return errors.New("cpu6502: unsupported state version")
	}
	c.a, c.x, c.y, c.sp = in[1], in[2], in[3], in[4]
	c.setP(in[5])
	c.pc = binary.LittleEndian.Uint16(in[6:])
	c.cycles = binary.LittleEndian.Uint64(in[8:])
	c.stall = int(binary.LittleEndian.Uint16(in[16:]))
	lines := in[18]
	c.nmiLine = lines&1 != 0
	c.nmiPending = lines&2 != 0
	c.irqLine = lines&4 != 0
	c.jammed = lines&8 != 0
	return nil
}
