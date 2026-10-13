// Package z80 is an original implementation of the Zilog Z80 CPU,
// written from the behaviour documented in the Z80 CPU User Manual
// (instruction set, flag effects, T-state counts) and from publicly
// documented flag research for the undocumented flag bits (z80.info).
//
// The implementation is a straightforward fetch/decode/execute core:
// one Step runs exactly one instruction, one iteration of a repeating
// block instruction (with PC left pointing at the instruction, exactly
// as the hardware re-fetches it), or one 4 T-state HALT idle cycle.
package z80

import (
	"encoding/binary"
	"errors"
)

// Bus is the machine interface the CPU drives. Fetch is used for
// opcode bytes (the M1 cycles), Read/Write for operands and data,
// In/Out for I/O port cycles.
type Bus interface {
	Fetch(addr uint16) uint8
	Read(addr uint16) uint8
	Write(addr uint16, val uint8)
	In(port uint16) uint8
	Out(port uint16, val uint8)
}

// Flag bit positions inside the F register.
const (
	flagS  = 0x80 // sign
	flagZ  = 0x40 // zero
	flagY  = 0x20 // bit 5 of result (undocumented)
	flagH  = 0x10 // half carry
	flagX  = 0x08 // bit 3 of result (undocumented)
	flagPV = 0x04 // parity / signed overflow
	flagN  = 0x02 // last ALU op was a subtraction
	flagC  = 0x01 // carry
)

// Registers is a snapshot of the programmer-visible CPU state.
type Registers struct {
	AF, BC, DE, HL     uint16
	AFShadow, BCShadow uint16
	DEShadow, HLShadow uint16
	IX, IY, SP, PC     uint16
	I, R               uint8
	IFF1, IFF2         bool
	IM                 uint8
	Halted             bool
}

// CPU is the Z80 state. The zero value is not usable; call New.
type CPU struct {
	bus Bus

	a, f uint8
	b, c uint8
	d, e uint8
	h, l uint8

	// Shadow (alternate) register set, swapped by EX AF,AF' and EXX.
	a2, f2 uint8
	b2, c2 uint8
	d2, e2 uint8
	h2, l2 uint8

	ix, iy uint16
	sp, pc uint16

	i, r   uint8 // interrupt vector base, memory refresh counter
	iff1   bool
	iff2   bool
	im     uint8
	halted bool
	cycles uint64

	intLine    bool  // level-triggered INT input
	intData    uint8 // byte the interrupting device puts on the bus
	nmiPending bool  // latched NMI request (edge triggered on real hardware)
	inhibitInt bool  // set by EI: no acceptance until after the next instruction
}

// New returns a CPU in its reset state, attached to bus.
func New(bus Bus) *CPU {
	c := &CPU{bus: bus}
	c.Reset()
	return c
}

// Reset restores the power-on state documented in the manual:
// PC = 0, I = 0, R = 0, interrupts disabled, interrupt mode 0.
// AF and SP take the conventional 0xFFFF initial values.
func (c *CPU) Reset() {
	c.a, c.f = 0xFF, 0xFF
	c.b, c.c = 0, 0
	c.d, c.e = 0, 0
	c.h, c.l = 0, 0
	c.a2, c.f2 = 0xFF, 0xFF
	c.b2, c.c2 = 0, 0
	c.d2, c.e2 = 0, 0
	c.h2, c.l2 = 0, 0
	c.ix, c.iy = 0, 0
	c.sp = 0xFFFF
	c.pc = 0x0000
	c.i, c.r = 0, 0
	c.iff1, c.iff2 = false, false
	c.im = 0
	c.halted = false
	c.cycles = 0
	c.intLine = false
	c.intData = 0
	c.nmiPending = false
	c.inhibitInt = false
}

// Cycles returns the total number of T-states executed since reset
// (or since the state was loaded), including interrupt responses.
func (c *CPU) Cycles() uint64 { return c.cycles }

// Registers returns a copy of the programmer-visible state.
func (c *CPU) Registers() Registers {
	return Registers{
		AF:       uint16(c.a)<<8 | uint16(c.f),
		BC:       c.bc(),
		DE:       c.de(),
		HL:       c.hl(),
		AFShadow: uint16(c.a2)<<8 | uint16(c.f2),
		BCShadow: uint16(c.b2)<<8 | uint16(c.c2),
		DEShadow: uint16(c.d2)<<8 | uint16(c.e2),
		HLShadow: uint16(c.h2)<<8 | uint16(c.l2),
		IX:       c.ix,
		IY:       c.iy,
		SP:       c.sp,
		PC:       c.pc,
		I:        c.i,
		R:        c.r,
		IFF1:     c.iff1,
		IFF2:     c.iff2,
		IM:       c.im,
		Halted:   c.halted,
	}
}

// SetState replaces the programmer-visible state. Cycle count and
// interrupt line state are not part of Registers and are untouched.
func (c *CPU) SetState(reg Registers) {
	c.a, c.f = uint8(reg.AF>>8), uint8(reg.AF)
	c.setBC(reg.BC)
	c.setDE(reg.DE)
	c.setHL(reg.HL)
	c.a2, c.f2 = uint8(reg.AFShadow>>8), uint8(reg.AFShadow)
	c.b2, c.c2 = uint8(reg.BCShadow>>8), uint8(reg.BCShadow)
	c.d2, c.e2 = uint8(reg.DEShadow>>8), uint8(reg.DEShadow)
	c.h2, c.l2 = uint8(reg.HLShadow>>8), uint8(reg.HLShadow)
	c.ix, c.iy = reg.IX, reg.IY
	c.sp, c.pc = reg.SP, reg.PC
	c.i, c.r = reg.I, reg.R
	c.iff1, c.iff2 = reg.IFF1, reg.IFF2
	c.im = reg.IM
	c.halted = reg.Halted
}

// INT drives the maskable interrupt line. It is level triggered: the
// CPU samples it at the end of each instruction. data is the byte an
// interrupting device would place on the data bus (used by modes 0
// and 2).
func (c *CPU) INT(assert bool, data uint8) {
	c.intLine = assert
	c.intData = data
}

// NMI requests a non-maskable interrupt. The request is latched until
// it is serviced at the next instruction boundary.
func (c *CPU) NMI() { c.nmiPending = true }

// StepCycles executes whole instructions until at least budget
// T-states have been consumed and returns the total consumed.
func (c *CPU) StepCycles(budget int) int {
	total := 0
	for total < budget {
		total += c.Step()
	}
	return total
}

// Step executes one instruction, one iteration of a repeating block
// instruction, or one HALT idle cycle, then services any interrupt
// that is due. It returns the T-states consumed.
func (c *CPU) Step() int {
	if c.halted {
		switch {
		case c.nmiPending:
			c.halted = false
			t := c.acceptNMI()
			c.cycles += uint64(t)
			return t
		case c.intLine && c.iff1:
			c.halted = false
			t := c.acceptINT()
			c.cycles += uint64(t)
			return t
		case c.intLine:
			// A masked INT still wakes the CPU; execution resumes
			// with the instruction after HALT.
			c.halted = false
		default:
			// Idle in HALT: one refresh cycle, 4 T-states.
			c.incR()
			c.cycles += 4
			return 4
		}
	}

	t := c.execute()

	// Interrupt acceptance point: the end of the instruction. EI
	// inhibits acceptance until the end of the following instruction.
	switch {
	case c.inhibitInt:
		c.inhibitInt = false
	case c.nmiPending:
		t += c.acceptNMI()
	case c.intLine && c.iff1:
		t += c.acceptINT()
	}
	c.cycles += uint64(t)
	return t
}

// acceptNMI runs the non-maskable interrupt sequence: IFF1 is copied
// to IFF2, IFF1 is cleared, PC is pushed and control goes to 0x0066.
func (c *CPU) acceptNMI() int {
	c.nmiPending = false
	c.halted = false
	c.incR() // the interrupt acknowledge M1 cycle refreshes memory
	c.iff2 = c.iff1
	c.iff1 = false
	c.push(c.pc)
	c.pc = 0x0066
	return 11
}

// acceptINT runs the maskable interrupt sequence for the current
// interrupt mode. Both interrupt flip-flops are cleared.
func (c *CPU) acceptINT() int {
	c.halted = false
	c.incR() // acknowledge M1 cycle
	c.iff1 = false
	c.iff2 = false
	switch c.im {
	case 1:
		c.push(c.pc)
		c.pc = 0x0038
		return 13
	case 2:
		c.push(c.pc)
		vec := uint16(c.i)<<8 | uint16(c.intData)
		c.pc = c.read16(vec)
		return 19
	default: // mode 0: execute the byte supplied on the data bus
		b := c.intData
		if b&0xC7 == 0xC7 { // RST p, the usual mode-0 instruction
			c.push(c.pc)
			c.pc = uint16(b & 0x38)
			return 13
		}
		// Any other supplied opcode runs through the normal decoder;
		// the acknowledge adds 2 T-states on top of its cost.
		return c.execOne(b) + 2
	}
}

// ---------------------------------------------------------------------------
// Register pair helpers

func (c *CPU) bc() uint16         { return uint16(c.b)<<8 | uint16(c.c) }
func (c *CPU) de() uint16         { return uint16(c.d)<<8 | uint16(c.e) }
func (c *CPU) hl() uint16         { return uint16(c.h)<<8 | uint16(c.l) }
func (c *CPU) setBC(v uint16)     { c.b, c.c = uint8(v>>8), uint8(v) }
func (c *CPU) setDE(v uint16)     { c.d, c.e = uint8(v>>8), uint8(v) }
func (c *CPU) setHL(v uint16)     { c.h, c.l = uint8(v>>8), uint8(v) }
func pairVal(hi, lo uint8) uint16 { return uint16(hi)<<8 | uint16(lo) }

// pair returns register pair n of the LD rr / ADD group:
// 0=BC, 1=DE, 2=HL, 3=SP.
func (c *CPU) pair(n int) uint16 {
	switch n {
	case 0:
		return c.bc()
	case 1:
		return c.de()
	case 2:
		return c.hl()
	default:
		return c.sp
	}
}

func (c *CPU) setPair(n int, v uint16) {
	switch n {
	case 0:
		c.setBC(v)
	case 1:
		c.setDE(v)
	case 2:
		c.setHL(v)
	default:
		c.sp = v
	}
}

// stackPair returns register pair n of the PUSH/POP group:
// 0=BC, 1=DE, 2=HL, 3=AF.
func (c *CPU) stackPair(n int) uint16 {
	switch n {
	case 0:
		return c.bc()
	case 1:
		return c.de()
	case 2:
		return c.hl()
	default:
		return uint16(c.a)<<8 | uint16(c.f)
	}
}

func (c *CPU) setStackPair(n int, v uint16) {
	switch n {
	case 0:
		c.setBC(v)
	case 1:
		c.setDE(v)
	case 2:
		c.setHL(v)
	default:
		c.a, c.f = uint8(v>>8), uint8(v)
	}
}

// reg returns 8-bit register n in the standard encoding
// (0=B, 1=C, 2=D, 3=E, 4=H, 5=L, 7=A). Index 6 means (HL) and is
// always handled by the caller, never passed here.
func (c *CPU) reg(n int) uint8 {
	switch n {
	case 0:
		return c.b
	case 1:
		return c.c
	case 2:
		return c.d
	case 3:
		return c.e
	case 4:
		return c.h
	case 5:
		return c.l
	default:
		return c.a
	}
}

func (c *CPU) setReg(n int, v uint8) {
	switch n {
	case 0:
		c.b = v
	case 1:
		c.c = v
	case 2:
		c.d = v
	case 3:
		c.e = v
	case 4:
		c.h = v
	case 5:
		c.l = v
	case 7:
		c.a = v
	}
}

// ---------------------------------------------------------------------------
// Bus access helpers

// fetch reads the next instruction-stream byte at PC (no refresh).
func (c *CPU) fetch() uint8 {
	v := c.bus.Fetch(c.pc)
	c.pc++
	return v
}

// fetchOpcode reads an opcode byte: an M1 cycle, which also bumps
// the refresh register.
func (c *CPU) fetchOpcode() uint8 {
	c.incR()
	return c.fetch()
}

// operand reads an immediate byte (not an M1 cycle).
func (c *CPU) operand() uint8 {
	v := c.bus.Read(c.pc)
	c.pc++
	return v
}

// operand16 reads a little-endian immediate word.
func (c *CPU) operand16() uint16 {
	lo := c.operand()
	hi := c.operand()
	return uint16(hi)<<8 | uint16(lo)
}

func (c *CPU) read16(addr uint16) uint16 {
	lo := c.bus.Read(addr)
	hi := c.bus.Read(addr + 1)
	return uint16(hi)<<8 | uint16(lo)
}

func (c *CPU) write16(addr, v uint16) {
	c.bus.Write(addr, uint8(v))
	c.bus.Write(addr+1, uint8(v>>8))
}

func (c *CPU) push(v uint16) {
	c.sp--
	c.bus.Write(c.sp, uint8(v>>8))
	c.sp--
	c.bus.Write(c.sp, uint8(v))
}

func (c *CPU) pop() uint16 {
	v := c.read16(c.sp)
	c.sp += 2
	return v
}

// incR advances the refresh register: only the low 7 bits count,
// bit 7 is preserved, as documented for the refresh counter.
func (c *CPU) incR() {
	c.r = (c.r & 0x80) | ((c.r + 1) & 0x7F)
}

// ---------------------------------------------------------------------------
// Flag helpers

// parityEven reports whether v has an even number of set bits
// (the Z80 P/V flag is set for even parity).
func parityEven(v uint8) bool {
	v ^= v >> 4
	v ^= v >> 2
	v ^= v >> 1
	return v&1 == 0
}

// szpFlags builds the S, Z, Y, X and PV (parity) bits for an 8-bit
// result, in the flag positions.
func szpFlags(v uint8) uint8 {
	f := v & (flagS | flagY | flagX)
	if v == 0 {
		f |= flagZ
	}
	if parityEven(v) {
		f |= flagPV
	}
	return f
}

// setSZP sets S, Z, Y, X and PV from an 8-bit result and clears
// H and N. C is preserved.
func (c *CPU) setSZP(v uint8) {
	c.f = c.f&flagC | szpFlags(v)
}

// cond evaluates condition n of the jump/call/return groups:
// 0=NZ, 1=Z, 2=NC, 3=C, 4=PO, 5=PE, 6=P, 7=M.
func (c *CPU) cond(n int) bool {
	switch n & 7 {
	case 0:
		return c.f&flagZ == 0
	case 1:
		return c.f&flagZ != 0
	case 2:
		return c.f&flagC == 0
	case 3:
		return c.f&flagC != 0
	case 4:
		return c.f&flagPV == 0
	case 5:
		return c.f&flagPV != 0
	case 6:
		return c.f&flagS == 0
	default:
		return c.f&flagS != 0
	}
}

// ---------------------------------------------------------------------------
// 8-bit ALU primitives. Each leaves the documented flag pattern in F.

// add8 computes A + v (+ carry if adc) and sets all arithmetic flags.
func (c *CPU) add8(v uint8, adc bool) uint8 {
	a := c.a
	ci := 0
	if adc && c.f&flagC != 0 {
		ci = 1
	}
	sum := int(a) + int(v) + ci
	res := uint8(sum)
	f := szpFlags(res) &^ flagPV // PV means overflow here, not parity
	if (a&0x0F)+(v&0x0F)+uint8(ci) > 0x0F {
		f |= flagH
	}
	if (^(a ^ v) & (a ^ res) & 0x80) != 0 {
		f |= flagPV
	}
	if sum > 0xFF {
		f |= flagC
	}
	c.f = f
	return res
}

// sub8 computes A - v (- carry if sbc) and sets all arithmetic flags.
// For CP (cp=true) A is unchanged by the caller, and the undocumented
// Y/X bits come from the operand v rather than from the result.
func (c *CPU) sub8(v uint8, sbc bool, cp bool) uint8 {
	a := c.a
	ci := 0
	if sbc && c.f&flagC != 0 {
		ci = 1
	}
	diff := int(a) - int(v) - ci
	res := uint8(diff)
	f := flagN | (szpFlags(res) &^ flagPV)
	if int(a&0x0F)-int(v&0x0F)-ci < 0 {
		f |= flagH
	}
	if ((a ^ v) & (a ^ res) & 0x80) != 0 {
		f |= flagPV
	}
	if diff < 0 {
		f |= flagC
	}
	if cp {
		f = f&^(flagY|flagX) | v&(flagY|flagX)
	}
	c.f = f
	return res
}

// alu runs one operation from the ALU group:
// 0=ADD, 1=ADC, 2=SUB, 3=SBC, 4=AND, 5=XOR, 6=OR, 7=CP.
func (c *CPU) alu(kind int, v uint8) {
	switch kind {
	case 0:
		c.a = c.add8(v, false)
	case 1:
		c.a = c.add8(v, true)
	case 2:
		c.a = c.sub8(v, false, false)
	case 3:
		c.a = c.sub8(v, true, false)
	case 4: // AND: H set, N and C clear, PV = parity
		c.a &= v
		c.f = szpFlags(c.a) | flagH
	case 5: // XOR
		c.a ^= v
		c.f = szpFlags(c.a)
	case 6: // OR
		c.a |= v
		c.f = szpFlags(c.a)
	case 7: // CP: result discarded
		c.sub8(v, false, true)
	}
}

// inc8 implements INC r flag behaviour: C is preserved, PV is set
// when the operand was 0x7F, H on a low-nibble carry, N cleared.
func (c *CPU) inc8(v uint8) uint8 {
	res := v + 1
	f := c.f & flagC
	f |= res & (flagS | flagY | flagX)
	if res == 0 {
		f |= flagZ
	}
	if v&0x0F == 0x0F {
		f |= flagH
	}
	if v == 0x7F {
		f |= flagPV
	}
	c.f = f
	return res
}

// dec8 implements DEC r flag behaviour: C is preserved, PV is set
// when the operand was 0x80, H on a low-nibble borrow, N set.
func (c *CPU) dec8(v uint8) uint8 {
	res := v - 1
	f := c.f&flagC | flagN
	f |= res & (flagS | flagY | flagX)
	if res == 0 {
		f |= flagZ
	}
	if v&0x0F == 0x00 {
		f |= flagH
	}
	if v == 0x80 {
		f |= flagPV
	}
	c.f = f
	return res
}

// ---------------------------------------------------------------------------
// 16-bit arithmetic primitives

// add16 implements ADD HL,rr / ADD IX,rr: only H, N (cleared), C and
// the undocumented Y/X bits (from the high byte of the result) move.
func (c *CPU) add16(a, b uint16) uint16 {
	sum := int(a) + int(b)
	res := uint16(sum)
	f := c.f & (flagS | flagZ | flagPV)
	if (a&0x0FFF)+(b&0x0FFF) > 0x0FFF {
		f |= flagH
	}
	if sum > 0xFFFF {
		f |= flagC
	}
	f |= uint8(res>>8) & (flagY | flagX)
	c.f = f
	return res
}

// adc16 implements ADC HL,rr: a full 16-bit add with carry.
func (c *CPU) adc16(a, b uint16) uint16 {
	ci := int(0)
	if c.f&flagC != 0 {
		ci = 1
	}
	sum := int(a) + int(b) + ci
	res := uint16(sum)
	var f uint8
	if res&0x8000 != 0 {
		f |= flagS
	}
	if res == 0 {
		f |= flagZ
	}
	f |= uint8(res>>8) & (flagY | flagX)
	if (a&0x0FFF)+(b&0x0FFF)+uint16(ci) > 0x0FFF {
		f |= flagH
	}
	if (^(a ^ b) & (a ^ res) & 0x8000) != 0 {
		f |= flagPV
	}
	if sum > 0xFFFF {
		f |= flagC
	}
	c.f = f
	return res
}

// sbc16 implements SBC HL,rr: a full 16-bit subtract with carry.
func (c *CPU) sbc16(a, b uint16) uint16 {
	ci := int(0)
	if c.f&flagC != 0 {
		ci = 1
	}
	diff := int(a) - int(b) - ci
	res := uint16(diff)
	var f uint8 = flagN
	if res&0x8000 != 0 {
		f |= flagS
	}
	if res == 0 {
		f |= flagZ
	}
	f |= uint8(res>>8) & (flagY | flagX)
	if int(a&0x0FFF)-int(b&0x0FFF)-ci < 0 {
		f |= flagH
	}
	if ((a ^ b) & (a ^ res) & 0x8000) != 0 {
		f |= flagPV
	}
	if diff < 0 {
		f |= flagC
	}
	c.f = f
	return res
}

// daa implements the decimal adjust accumulator operation using the
// documented correction table: 0x06 fixes the low digit, 0x60 the
// high digit, selected by the H/C flags and the nibble values left
// by the preceding addition or subtraction.
func (c *CPU) daa() {
	a := c.a
	var corr uint8
	carry := c.f&flagC != 0
	if c.f&flagH != 0 || (a&0x0F) > 9 {
		corr |= 0x06
	}
	if carry || a > 0x99 {
		corr |= 0x60
		carry = true
	} else {
		carry = false
	}
	var res uint8
	var half bool
	if c.f&flagN != 0 {
		res = a - corr
		half = (a & 0x0F) < (corr & 0x0F)
	} else {
		res = a + corr
		half = (a&0x0F)+(corr&0x0F) > 0x0F
	}
	c.a = res
	f := szpFlags(res) | (c.f & flagN)
	if half {
		f |= flagH
	}
	if carry {
		f |= flagC
	}
	c.f = f
}

// ---------------------------------------------------------------------------
// Serialization
//
// Layout (37 of the 47 bytes are used, the rest stay zero):
//
//	[0]     version (1)
//	[1:25]  AF, BC, DE, HL, AF', BC', DE', HL', IX, IY, SP, PC (LE16)
//	[25]    I
//	[26]    R
//	[27]    bit flags: IFF1, IFF2, halted, INT line, NMI pending,
//	        EI inhibit, IM in bits 6-7
//	[28]    INT bus data byte
//	[29:37] cycle counter (LE64)

// SerializeSize is the fixed save-state size in bytes.
const SerializeSize = 47

const serializeVersion = 1

// Serialize writes the full CPU state (including cycle counter and
// interrupt line state) into buf, which must be at least
// SerializeSize bytes long.
func (c *CPU) Serialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("z80: serialize buffer too small")
	}
	for i := 0; i < SerializeSize; i++ {
		buf[i] = 0
	}
	buf[0] = serializeVersion
	pairs := []uint16{
		uint16(c.a)<<8 | uint16(c.f), c.bc(), c.de(), c.hl(),
		uint16(c.a2)<<8 | uint16(c.f2),
		uint16(c.b2)<<8 | uint16(c.c2),
		uint16(c.d2)<<8 | uint16(c.e2),
		uint16(c.h2)<<8 | uint16(c.l2),
		c.ix, c.iy, c.sp, c.pc,
	}
	for i, p := range pairs {
		binary.LittleEndian.PutUint16(buf[1+i*2:], p)
	}
	buf[25] = c.i
	buf[26] = c.r
	var bits uint8
	if c.iff1 {
		bits |= 1 << 0
	}
	if c.iff2 {
		bits |= 1 << 1
	}
	if c.halted {
		bits |= 1 << 2
	}
	if c.intLine {
		bits |= 1 << 3
	}
	if c.nmiPending {
		bits |= 1 << 4
	}
	if c.inhibitInt {
		bits |= 1 << 5
	}
	bits |= (c.im & 3) << 6
	buf[27] = bits
	buf[28] = c.intData
	binary.LittleEndian.PutUint64(buf[29:], c.cycles)
	return nil
}

// Deserialize restores a state written by Serialize.
func (c *CPU) Deserialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("z80: deserialize buffer too small")
	}
	if buf[0] != serializeVersion {
		return errors.New("z80: unsupported serialize version")
	}
	pairAt := func(i int) uint16 { return binary.LittleEndian.Uint16(buf[1+i*2:]) }
	c.a, c.f = uint8(pairAt(0)>>8), uint8(pairAt(0))
	c.setBC(pairAt(1))
	c.setDE(pairAt(2))
	c.setHL(pairAt(3))
	c.a2, c.f2 = uint8(pairAt(4)>>8), uint8(pairAt(4))
	c.b2, c.c2 = uint8(pairAt(5)>>8), uint8(pairAt(5))
	c.d2, c.e2 = uint8(pairAt(6)>>8), uint8(pairAt(6))
	c.h2, c.l2 = uint8(pairAt(7)>>8), uint8(pairAt(7))
	c.ix = pairAt(8)
	c.iy = pairAt(9)
	c.sp = pairAt(10)
	c.pc = pairAt(11)
	c.i = buf[25]
	c.r = buf[26]
	bits := buf[27]
	c.iff1 = bits&(1<<0) != 0
	c.iff2 = bits&(1<<1) != 0
	c.halted = bits&(1<<2) != 0
	c.intLine = bits&(1<<3) != 0
	c.nmiPending = bits&(1<<4) != 0
	c.inhibitInt = bits&(1<<5) != 0
	c.im = (bits >> 6) & 3
	c.intData = buf[28]
	c.cycles = binary.LittleEndian.Uint64(buf[29:])
	return nil
}
