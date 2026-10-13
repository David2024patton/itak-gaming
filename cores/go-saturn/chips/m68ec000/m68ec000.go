// Package m68ec000 implements the Motorola 68000 integer CPU
// from the MC68000 User Manual. In the Saturn it is the SCSP
// sound CPU. This module is standalone: it depends only on
// the Bus interface below and the Go standard library.
//
// The core is a plain interpreter. One Step call executes
// one instruction, or takes one pending accepted interrupt.
// Cycle counts follow the User Manual instruction timing
// table for the common forms: a compact base cost per
// instruction class plus the manual effective address
// adders. Counts are deterministic; they are an
// approximation of the table, not a bus cycle simulation.
//
// Conformance: there is no Tom Harte 680x0 test corpus on
// this machine (it was searched for and not found), so the
// conformance gate is the hand derived vectors in
// m68ec000_test.go, worked out from the manual operation
// and flag tables. The tests are structured as plain
// register, flag, and memory assertions per instruction
// group so a corpus harness can be added later by driving
// the same public API: New, Reset, Step, D, A, SR, PC.
//
// The SR T bit is stored but trace exceptions are not
// taken. RESET is modeled in place: it invokes the
// optional ResetCallback field when set and otherwise
// does nothing besides consume its documented cycles.
// TAS is indivisible in this model because Step is the
// unit of interleaving.
//
// Serialized layout, SerializeSize bytes, big endian:
// bytes 0 to 31: D0 to D7, four bytes each
// bytes 32 to 63: A0 to A7, four bytes each. A7 is the
// active stack pointer, selected by the SR S bit.
// bytes 64 to 67: user stack pointer (the inactive A7
// when S is set, the active one when S is clear)
// bytes 68 to 71: supervisor stack pointer (same rule)
// bytes 72 to 75: PC
// bytes 76 to 77: SR
// byte 78: pending interrupt: high bit set when valid,
// plus the level in the low three bits
// byte 79: halted flag, 0 or 1
// bytes 80 to 87: Cycles, eight bytes
package m68ec000

import (
	"encoding/binary"
	"errors"
)

// SerializeSize is the exact size of a serialized CPU.
const SerializeSize = 88

// Bus is the memory space the CPU talks to. Read16 and
// Read32 return big endian assembled values: the bus, not
// the CPU, owns byte order assembly.
type Bus interface {
	Read8(uint32) uint8
	Read16(uint32) uint16
	Read32(uint32) uint32
	Write8(uint32, uint8)
	Write16(uint32, uint16)
	Write32(uint32, uint32)
}

// SR bit positions in the low byte (the CCR) and high byte.
const (
	srC uint16 = 0x0001
	srV uint16 = 0x0002
	srZ uint16 = 0x0004
	srN uint16 = 0x0008
	srX uint16 = 0x0010
	srS uint16 = 0x2000
	srT uint16 = 0x8000
)

// CPU is one 68000 processor.
type CPU struct {
	Cycles uint64

	// ResetCallback, when set, is invoked by the RESET
	// instruction. RESET is a supervisor only instruction.
	ResetCallback func()

	bus     Bus
	d       [8]uint32
	a       [8]uint32
	otherSP uint32
	pc      uint32
	sr      uint16

	pendValid bool
	pendLevel int
	halted    bool

	cur uint32
	ir  uint16
}

// New returns a CPU attached to b. It does not reset;
// the caller calls Reset.
func New(b Bus) *CPU {
	return &CPU{bus: b}
}

// Reset reads the initial supervisor stack pointer from
// bus address 0 and the initial PC from bus address 4,
// clears the registers, and sets SR to 0x2700:
// supervisor mode, interrupt mask 7, T clear.
func (c *CPU) Reset() {
	c.d = [8]uint32{}
	c.a = [8]uint32{}
	c.otherSP = 0
	c.Cycles = 0
	c.halted = false
	c.pendValid = false
	c.pendLevel = 0
	ssp := c.bus.Read32(0)
	c.pc = c.bus.Read32(4)
	c.sr = 0x2700
	c.a[7] = ssp
}

// PC returns the program counter.
func (c *CPU) PC() uint32 { return c.pc }

// D returns data register i, 0 to 7.
func (c *CPU) D(i int) uint32 { return c.d[i&7] }

// A returns address register i, 0 to 7. A7 is the active
// stack pointer.
func (c *CPU) A(i int) uint32 { return c.a[i&7] }

// SR returns the status register.
func (c *CPU) SR() uint16 { return c.sr }

// Halted reports whether a STOP instruction has halted
// the CPU. It wakes when an interrupt latch is accepted.
func (c *CPU) Halted() bool { return c.halted }

// Interrupt latches an autovectored interrupt request,
// levels 1 to 7. It is accepted between instructions
// when the level is greater than the SR interrupt mask.
// Level 7 is always accepted, once per latch (edge
// model). The vector taken is 24 plus the level. Only
// the highest latched level is kept.
func (c *CPU) Interrupt(level int) {
	if level < 1 || level > 7 {
		return
	}
	if c.pendValid && c.pendLevel >= level {
		return
	}
	c.pendValid = true
	c.pendLevel = level
}

func (c *CPU) supervisor() bool { return c.sr&srS != 0 }

func (c *CPU) mask() int { return int((c.sr >> 8) & 7) }

// setSR stores a new SR, swapping the stack pointers
// when the S bit changes, per the manual USP and SSP
// split.
func (c *CPU) setSR(v uint16) {
	oldS := c.sr & srS
	newS := v & srS
	c.sr = v
	if oldS != newS {
		c.a[7], c.otherSP = c.otherSP, c.a[7]
	}
}

// usp and ssp report the two stack pointers regardless
// of which one is active.
func (c *CPU) usp() uint32 {
	if c.supervisor() {
		return c.otherSP
	}
	return c.a[7]
}

func (c *CPU) ssp() uint32 {
	if c.supervisor() {
		return c.a[7]
	}
	return c.otherSP
}

// Serialize returns the CPU state in the layout given in
// the package comment, exactly SerializeSize bytes.
func (c *CPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint32(out[i*4:i*4+4], c.d[i])
		binary.BigEndian.PutUint32(out[32+i*4:32+i*4+4], c.a[i])
	}
	binary.BigEndian.PutUint32(out[64:68], c.usp())
	binary.BigEndian.PutUint32(out[68:72], c.ssp())
	binary.BigEndian.PutUint32(out[72:76], c.pc)
	binary.BigEndian.PutUint16(out[76:78], c.sr)
	if c.pendValid {
		out[78] = 0x80 | byte(c.pendLevel&7)
	}
	if c.halted {
		out[79] = 1
	}
	binary.BigEndian.PutUint64(out[80:88], c.Cycles)
	return out
}

// Deserialize restores a state produced by Serialize.
func (c *CPU) Deserialize(data []byte) error {
	if len(data) != SerializeSize {
		return errors.New("m68ec000: state size mismatch")
	}
	for i := 0; i < 8; i++ {
		c.d[i] = binary.BigEndian.Uint32(data[i*4 : i*4+4])
		c.a[i] = binary.BigEndian.Uint32(data[32+i*4 : 32+i*4+4])
	}
	usp := binary.BigEndian.Uint32(data[64:68])
	ssp := binary.BigEndian.Uint32(data[68:72])
	c.pc = binary.BigEndian.Uint32(data[72:76])
	c.sr = binary.BigEndian.Uint16(data[76:78])
	// A7 in the blob is the active pointer. Rebuild the
	// inactive pointer from the stored pair.
	if c.supervisor() {
		c.a[7] = ssp
		c.otherSP = usp
	} else {
		c.a[7] = usp
		c.otherSP = ssp
	}
	c.pendValid = data[78]&0x80 != 0
	c.pendLevel = int(data[78] & 7)
	c.halted = data[79] != 0
	c.Cycles = binary.BigEndian.Uint64(data[80:88])
	return nil
}

// Exception panic carriers. The memory helpers panic with
// addrError on an odd word or long access; Step recovers
// and builds the address error frame. illegalError and
// privError carry the faulting instruction address.
type addrError struct {
	addr       uint32
	instr      uint32
	pc         uint32
	ir         uint16
	read       bool
	instrFetch bool
}

// illegalError carries the exception vector to take: 4
// for a plain illegal instruction, 10 for line A, and
// 11 for line F. A zero vector means 4.
type illegalError struct {
	instr uint32
	vec   int
}
type privError struct{ instr uint32 }

func (c *CPU) rd8(addr uint32) uint8 { return c.bus.Read8(addr) }

func (c *CPU) rd16(addr uint32) uint16 {
	if addr&1 != 0 {
		panic(addrError{addr: addr, instr: c.cur, pc: c.pc, ir: c.ir, read: true})
	}
	return c.bus.Read16(addr)
}

func (c *CPU) rd32(addr uint32) uint32 {
	if addr&1 != 0 {
		panic(addrError{addr: addr, instr: c.cur, pc: c.pc, ir: c.ir, read: true})
	}
	return c.bus.Read32(addr)
}

func (c *CPU) wr8(addr uint32, v uint8) { c.bus.Write8(addr, v) }

func (c *CPU) wr16(addr uint32, v uint16) {
	if addr&1 != 0 {
		panic(addrError{addr: addr, instr: c.cur, pc: c.pc, ir: c.ir, read: false})
	}
	c.bus.Write16(addr, v)
}

func (c *CPU) wr32(addr uint32, v uint32) {
	if addr&1 != 0 {
		panic(addrError{addr: addr, instr: c.cur, pc: c.pc, ir: c.ir, read: false})
	}
	c.bus.Write32(addr, v)
}

func (c *CPU) fetch16(addr uint32) uint16 {
	if addr&1 != 0 {
		panic(addrError{addr: addr, instr: addr, pc: addr, ir: 0, read: true, instrFetch: true})
	}
	return c.bus.Read16(addr)
}

// fetchExt consumes one extension word at the PC.
func (c *CPU) fetchExt() uint16 {
	w := c.fetch16(c.pc)
	c.pc += 2
	return w
}

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

// exception takes a normal exception: it stacks SR then
// PC (so SR sits at the stack pointer and PC above it,
// matching RTE and RTR unwinding), switches to
// supervisor mode, clears T, and fetches the vector from
// the vector table through the bus. savedPC is the PC
// value the manual stacks for the exception in question.
func (c *CPU) exception(vector int, savedPC uint32) {
	oldSR := c.sr
	if !c.supervisor() {
		c.setSR(c.sr | srS)
	}
	c.push32(savedPC)
	c.push16(oldSR)
	c.sr &^= srT
	c.pc = c.bus.Read32(uint32(vector) * 4)
}

// addressException builds the 68000 address error frame.
// Per the MC68000 User Manual exception processing
// section (Group 0 exceptions, the bus and address error
// stacking order), the frame is 14 bytes and, from the
// stack pointer upward, holds: the status register, the
// program counter, the instruction register, the access
// (fault) address, and a trailing status word. The
// trailing status word carries the function code in bits
// 2 to 0, an instruction or not flag in bit 4 (set for a
// data access, clear for an instruction fetch), and a
// read or write flag in bit 5 (set for a read). The
// stacked program counter is the addrError pc field: for
// a data access it is the PC at the moment of the fault
// (past the opcode and the extension words consumed so
// far, which for the common single effective address
// cases is the next instruction, the value the manual
// stacks for data access errors); for an instruction
// fetch it is the faulting fetch address itself.
func (c *CPU) addressException(e addrError) {
	oldSR := c.sr
	if !c.supervisor() {
		c.setSR(c.sr | srS)
	}
	var fc uint16 = 5 // supervisor data
	if e.instrFetch {
		fc = 6 // supervisor program
	}
	if oldSR&srS == 0 {
		if e.instrFetch {
			fc = 2 // user program
		} else {
			fc = 1 // user data
		}
	}
	ssw := fc
	if !e.instrFetch {
		ssw |= 0x10
	}
	if e.read {
		ssw |= 0x20
	}
	// Push in reverse so the memory order above holds.
	c.push16(ssw)
	c.push32(e.addr)
	c.push16(e.ir)
	c.push32(e.pc)
	c.push16(oldSR)
	c.sr &^= srT
	c.pc = c.bus.Read32(3 * 4) // vector 3, address error
}

// Step executes one instruction, or takes a pending
// accepted interrupt, and returns the cycles consumed,
// which are also added to Cycles. While halted by STOP,
// Step returns 4 without fetching until an interrupt
// latch is accepted, which wakes the CPU and is taken.
func (c *CPU) Step() int {
	n := c.stepInner()
	c.Cycles += uint64(n)
	return n
}

func (c *CPU) stepInner() (cycles int) {
	cycles = 4
	defer func() {
		if r := recover(); r != nil {
			switch e := r.(type) {
			case addrError:
				c.halted = false
				c.addressException(e)
				cycles = 50
			case illegalError:
				c.halted = false
				vec := e.vec
				if vec == 0 {
					vec = 4
				}
				c.exception(vec, e.instr+2)
				cycles = 34
			case privError:
				c.halted = false
				// The stacked PC is past the opcode word
				// (e.instr + 2), matching the illegal
				// instruction path above.
				c.exception(8, e.instr+2)
				cycles = 34
			default:
				panic(r)
			}
		}
	}()
	// Interrupt acceptance at the instruction boundary.
	if c.pendValid && (c.pendLevel == 7 || c.pendLevel > c.mask()) {
		level := c.pendLevel
		c.pendValid = false
		c.halted = false
		savedPC := c.pc
		oldSR := c.sr
		if !c.supervisor() {
			c.setSR(c.sr | srS)
		}
		c.push32(savedPC)
		c.push16(oldSR)
		c.sr &^= srT
		c.sr = (c.sr &^ 0x0700) | uint16(level)<<8
		c.pc = c.bus.Read32(uint32(24+level) * 4)
		return 44
	}
	if c.halted {
		return 4
	}
	c.cur = c.pc
	c.ir = c.fetch16(c.pc)
	c.pc += 2
	return c.exec(c.ir)
}
