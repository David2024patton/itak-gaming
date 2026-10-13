// Package spc700 is an original implementation of the Sony
// SPC700 CPU, written from the published documentation for
// the chip: the SPC700 programming manual summaries and the
// fullsnes SPC700 section, which restate the manual's opcode
// table, flag definitions, and cycle counts.
//
// The SPC700 is the 8-bit CPU inside the SNES S-SMP audio
// subsystem. It has three 8-bit data registers (A, X, Y, with
// A and Y also usable as the 16-bit pair YA), an 8-bit stack
// pointer that always addresses page 1, a 16-bit program
// counter, and an 8-bit processor status word (PSW). One PSW
// bit, P, selects which page direct-page addressing uses:
// page 0 when P is clear, page 1 when P is set. Direct-page
// address arithmetic wraps inside the selected page, and a
// 16-bit direct-page read takes its high byte from the next
// address inside the same page, never from the page after it.
//
// Cycle counting follows the published per-opcode table (the
// same counts the Tom Harte single-step corpus records):
// every Step returns the documented cycle count of the one
// instruction it executed, taken branches costing their
// documented taken price. Bus accesses within an instruction
// are performed in the documented order and the conformance
// gate verifies the data-carrying access sequence (address,
// direction, and value) against the corpus traces exactly,
// including the dummy reads most store opcodes issue against
// their destination, the second read TSET1 and TCLR1 make of
// their target, and the dummy fetch TCALL makes of the byte
// after its opcode. What is not modeled is wait-state
// stretching through the S-SMP TEST register, since that
// register belongs to the SMP package that owns the bus.
//
// STOP and SLEEP halt the CPU. The published table gives
// their cycle count as unknown (the CPU simply stops), so
// this core charges a halted Step a flat 16 cycles, the
// slice the task contract for this package specifies, and
// changes no state; only Reset recovers from a halt.
//
// DIV YA,X reproduces the hardware divider rather than the
// mathematical one: the documented 17-bit shift algorithm
// whose 9-bit quotient lands in A with its overflow bit in
// V, valid for quotients up to 511, and whose remainder in
// the overflow range is the hardware's value, not the true
// modulo. Division by zero runs through the same algorithm
// with a zero divisor and produces the hardware's result
// instead of trapping.
package spc700

import (
	"encoding/binary"
	"errors"
)

// Bus is the machine interface the CPU drives: the flat
// 64 KB space of the S-SMP (ARAM plus the I/O ports the SMP
// package maps into it). Every address the CPU presents is
// a full 16-bit address in that space.
type Bus interface {
	Read(addr uint16) uint8
	Write(addr uint16, val uint8)
}

// Status word (PSW) bit masks, in the manual's order.
const (
	FlagC = 0x01 // carry
	FlagZ = 0x02 // zero
	FlagI = 0x04 // interrupt enable (no sources exist in the S-SMP)
	FlagH = 0x08 // half carry
	FlagB = 0x10 // break (set by BRK, clear at reset)
	FlagP = 0x20 // direct page select (0 = page 0, 1 = page 1)
	FlagV = 0x40 // overflow
	FlagN = 0x80 // negative
)

// SerializeSize is the fixed save-state size in bytes.
const SerializeSize = 24

const serializeVersion = 1

// Registers is a snapshot of the programmer-visible CPU
// state. P is the full status word, including the B and P
// bits, exactly as PUSH PSW would push it.
type Registers struct {
	PC uint16
	A  uint8
	X  uint8
	Y  uint8
	SP uint8
	P  uint8
}

// CPU is a single SPC700 instance.
type CPU struct {
	bus Bus

	pc     uint16
	a      uint8
	x      uint8
	y      uint8
	sp     uint8
	p      uint8
	cycles uint64 // SPC cycles since reset
	halted bool   // STOP or SLEEP executed; only Reset recovers
}

// New returns a CPU in its power-on state: registers zeroed.
// Call Reset (with a bus attached) to load the reset vector.
func New() *CPU {
	return &CPU{}
}

// Attach wires the CPU to a bus.
func (c *CPU) Attach(b Bus) { c.bus = b }

// Reset loads the program counter from the reset vector at
// $FFFE/$FFFF, read through the bus, and clears the rest of
// the programmer-visible state: A, X, and Y are zero, the
// stack pointer is zero, the status word is zero (so the B
// flag reads clear, matching the manual's reset state, the
// direct page is page 0, and interrupts are disabled), the
// cycle counter restarts, and a halted CPU runs again.
//
// Real hardware leaves most registers undefined at reset
// and the IPL ROM immediately installs its own values (it
// sets SP itself before doing anything else), so a defined
// all-zero state costs real software nothing and keeps save
// states and tests deterministic. The vector fetch itself
// is not charged to the cycle counter.
func (c *CPU) Reset() {
	c.a, c.x, c.y = 0, 0, 0
	c.sp = 0
	c.p = 0
	c.cycles = 0
	c.halted = false
	lo := c.bus.Read(0xFFFE)
	hi := c.bus.Read(0xFFFF)
	c.pc = uint16(hi)<<8 | uint16(lo)
}

// Regs returns a snapshot of the programmer-visible state.
func (c *CPU) Regs() Registers {
	return Registers{PC: c.pc, A: c.a, X: c.x, Y: c.y, SP: c.sp, P: c.p}
}

// SetRegs installs a register snapshot. The single-step
// conformance runner uses it to place the CPU in corpus
// states; installing a snapshot also clears the halted
// state, since a snapshot describes a CPU that is about to
// execute.
func (c *CPU) SetRegs(r Registers) {
	c.pc = r.PC
	c.a, c.x, c.y = r.A, r.X, r.Y
	c.sp = r.SP
	c.p = r.P
	c.halted = false
}

// Cycles returns the total SPC cycles executed since
// reset, including the flat charge for halted Steps.
func (c *CPU) Cycles() uint64 { return c.cycles }

// Halted reports whether STOP or SLEEP has halted the CPU.
func (c *CPU) Halted() bool { return c.halted }

// haltCycles is the flat cycle charge for a Step taken
// while halted, and for the Step that executes STOP or
// SLEEP itself (see the package comment).
const haltCycles = 16

// Step executes one instruction and returns its cycle
// count from the published table. When the CPU is halted
// it returns haltCycles and changes nothing (the charge
// still accrues to Cycles, so a scheduler advancing other
// hardware against the returned count stays in step).
func (c *CPU) Step() int {
	if c.halted {
		c.cycles += haltCycles
		return haltCycles
	}
	op := c.fetch()
	n := c.exec(op)
	c.cycles += uint64(n)
	return n
}

// fetch reads the byte at PC and advances PC.
func (c *CPU) fetch() uint8 {
	v := c.bus.Read(c.pc)
	c.pc++
	return v
}

// fetch16 fetches a little-endian 16-bit operand.
func (c *CPU) fetch16() uint16 {
	lo := c.fetch()
	hi := c.fetch()
	return uint16(hi)<<8 | uint16(lo)
}

// dpBase is the base address of the current direct page.
func (c *CPU) dpBase() uint16 {
	if c.p&FlagP != 0 {
		return 0x0100
	}
	return 0x0000
}

// dpAddr is the address of a direct-page byte.
func (c *CPU) dpAddr(off uint8) uint16 {
	return c.dpBase() + uint16(off)
}

// dpAddrIdx is the address of a direct-page byte indexed by
// idx: the addition wraps inside the direct page, as the
// manual documents.
func (c *CPU) dpAddrIdx(off, idx uint8) uint16 {
	return c.dpBase() + uint16(off+idx)
}

// readDP reads a direct-page byte.
func (c *CPU) readDP(off uint8) uint8 {
	return c.bus.Read(c.dpAddr(off))
}

// readDP16 reads a 16-bit direct-page value; the high byte
// comes from the next address inside the same direct page,
// wrapping at the page end, as the manual documents.
func (c *CPU) readDP16(off uint8) uint16 {
	base := c.dpBase()
	lo := c.bus.Read(base + uint16(off))
	hi := c.bus.Read(base + uint16(off+1))
	return uint16(hi)<<8 | uint16(lo)
}

// writeDP16 writes a 16-bit direct-page value with the same
// in-page wrap on its high byte as readDP16.
func (c *CPU) writeDP16(off uint8, v uint16) {
	base := c.dpBase()
	c.bus.Write(base+uint16(off), uint8(v))
	c.bus.Write(base+uint16(off+1), uint8(v>>8))
}

// ptrDP reads the 16-bit pointer stored at a direct-page
// address (its high byte wraps inside the page).
func (c *CPU) ptrDP(off uint8) uint16 {
	return c.readDP16(off)
}

// push writes a byte to the stack in page 1 and decrements
// SP; SP wraps inside the page, as the manual documents.
func (c *CPU) push(v uint8) {
	c.bus.Write(0x0100+uint16(c.sp), v)
	c.sp--
}

// push16 pushes a 16-bit value, high byte first, so a
// matching pull16 reads it back low byte first.
func (c *CPU) push16(v uint16) {
	c.push(uint8(v >> 8))
	c.push(uint8(v))
}

// pull increments SP and reads the byte it lands on.
func (c *CPU) pull() uint8 {
	c.sp++
	return c.bus.Read(0x0100 + uint16(c.sp))
}

// pull16 pulls a 16-bit value, low byte first.
func (c *CPU) pull16() uint16 {
	lo := c.pull()
	hi := c.pull()
	return uint16(hi)<<8 | uint16(lo)
}

// setFlag sets or clears one status bit.
func (c *CPU) setFlag(f uint8, on bool) {
	if on {
		c.p |= f
	} else {
		c.p &^= f
	}
}

// flag reports one status bit.
func (c *CPU) flag(f uint8) bool { return c.p&f != 0 }

// setNZ sets N and Z from an 8-bit result.
func (c *CPU) setNZ(v uint8) {
	c.setFlag(FlagN, v&0x80 != 0)
	c.setFlag(FlagZ, v == 0)
}

// setNZ16 sets N and Z from a 16-bit result: N is bit 15
// and Z reports the whole word, as MOVW and the word ALU
// operations document.
func (c *CPU) setNZ16(v uint16) {
	c.setFlag(FlagN, v&0x8000 != 0)
	c.setFlag(FlagZ, v == 0)
}

// ya is the 16-bit YA pair (Y high, A low).
func (c *CPU) ya() uint16 {
	return uint16(c.y)<<8 | uint16(c.a)
}

// setYA stores a 16-bit value into the YA pair.
func (c *CPU) setYA(v uint16) {
	c.a = uint8(v)
	c.y = uint8(v >> 8)
}

// Serialize writes the CPU state in this package's fixed
// layout: version, registers, cycle counter, and the halted
// flag. There is no other internal state: an SPC700 between
// instructions holds nothing in flight.
func (c *CPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	binary.LittleEndian.PutUint16(out[1:], c.pc)
	out[3] = c.a
	out[4] = c.x
	out[5] = c.y
	out[6] = c.sp
	out[7] = c.p
	binary.LittleEndian.PutUint64(out[8:], c.cycles)
	if c.halted {
		out[16] = 1
	}
	return out
}

// Deserialize restores a state written by Serialize.
func (c *CPU) Deserialize(in []byte) error {
	if len(in) != SerializeSize {
		return errors.New("spc700: bad state size")
	}
	if in[0] != serializeVersion {
		return errors.New("spc700: unsupported state version")
	}
	c.pc = binary.LittleEndian.Uint16(in[1:])
	c.a, c.x, c.y = in[3], in[4], in[5]
	c.sp = in[6]
	c.p = in[7]
	c.cycles = binary.LittleEndian.Uint64(in[8:])
	c.halted = in[16] != 0
	return nil
}
