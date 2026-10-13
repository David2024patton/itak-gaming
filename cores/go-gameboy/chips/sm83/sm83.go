// Package sm83 is an original implementation of the Sharp SM83 CPU,
// the processor inside the Game Boy, written from the published
// instruction set documentation (the opcode and flag tables collected
// in the Game Boy Pan Docs and the published SM83 opcode references).
//
// The implementation is a straightforward fetch, decode, execute core:
// one Step runs exactly one instruction, one interrupt dispatch, or
// one idle M-cycle while halted or stopped, and returns the number of
// M-cycles consumed (1 M-cycle is 4 T-cycles at the Game Boy's base
// clock). Documented hardware quirks are modeled deliberately: the
// flags register's low nibble is hardwired to zero, EI enables
// interrupts only after the following instruction completes, RETI
// enables them immediately, the HALT bug makes the instruction after
// HALT fetch its opcode byte twice when an interrupt is pending with
// IME clear, and the accumulator rotates (RLCA, RRCA, RLA, RRA)
// always clear the zero flag even when the result is zero.
package sm83

import (
	"encoding/binary"
	"errors"
)

// Bus is the machine interface the CPU drives. Opcode fetches and data
// accesses both go through Read; the CPU does not distinguish them.
type Bus interface {
	Read(addr uint16) uint8
	Write(addr uint16, val uint8)
}

// Flag bit positions inside the F register. The low nibble of F is
// not stored anywhere in the hardware and always reads as zero.
const (
	flagZ = 0x80 // zero
	flagN = 0x40 // subtract (last ALU op was a subtraction)
	flagH = 0x20 // half carry
	flagC = 0x10 // carry
)

// Interrupt enable and interrupt flag register addresses.
const (
	addrIE = 0xFFFF
	addrIF = 0xFF0F
)

// SerializeSize is the fixed save-state size in bytes.
const SerializeSize = 26

const serializeVersion = 1

// Registers is a snapshot of the programmer-visible CPU state.
type Registers struct {
	A, F, B, C, D, E, H, L uint8
	SP, PC                 uint16
}

// CPU is a single SM83 instance.
type CPU struct {
	a, f, b, c, d, e, h, l uint8
	sp, pc                 uint16

	ime     bool // master interrupt enable
	eiDelay int  // instructions remaining before a pending EI takes effect
	halted  bool
	haltBug bool // next opcode fetch does not advance PC
	stopped bool

	cycles uint64 // total M-cycles executed since reset
}

// New returns a CPU in its power-on state: registers zero, PC zero,
// interrupts disabled. The machine applies the post-boot register
// values itself when it models a boot ROM hand-off.
func New() *CPU {
	return &CPU{}
}

// Reset returns the CPU to its power-on state.
func (c *CPU) Reset() {
	*c = CPU{}
}

// Regs returns a snapshot of the programmer-visible registers.
func (c *CPU) Regs() Registers {
	return Registers{
		A: c.a, F: c.f, B: c.b, C: c.c, D: c.d, E: c.e, H: c.h, L: c.l,
		SP: c.sp, PC: c.pc,
	}
}

// SetRegs installs a register snapshot. F is masked to its high
// nibble, matching the hardware.
func (c *CPU) SetRegs(r Registers) {
	c.a, c.f = r.A, r.F&0xF0
	c.b, c.c = r.B, r.C
	c.d, c.e = r.D, r.E
	c.h, c.l = r.H, r.L
	c.sp, c.pc = r.SP, r.PC
}

// Cycles returns the total M-cycles executed since reset.
func (c *CPU) Cycles() uint64 { return c.cycles }

// IME reports the master interrupt enable state.
func (c *CPU) IME() bool { return c.ime }

// Halted reports whether the CPU is in the HALT state.
func (c *CPU) Halted() bool { return c.halted }

// Stopped reports whether the CPU is in the STOP state.
func (c *CPU) Stopped() bool { return c.stopped }

func (c *CPU) bc() uint16 { return uint16(c.b)<<8 | uint16(c.c) }
func (c *CPU) de() uint16 { return uint16(c.d)<<8 | uint16(c.e) }
func (c *CPU) hl() uint16 { return uint16(c.h)<<8 | uint16(c.l) }

func (c *CPU) setBC(v uint16) { c.b, c.c = uint8(v>>8), uint8(v) }
func (c *CPU) setDE(v uint16) { c.d, c.e = uint8(v>>8), uint8(v) }
func (c *CPU) setHL(v uint16) { c.h, c.l = uint8(v>>8), uint8(v) }

// getRP reads a 16-bit register pair by its opcode index:
// 0 BC, 1 DE, 2 HL, 3 SP.
func (c *CPU) getRP(i int) uint16 {
	switch i {
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

// setRP writes a 16-bit register pair by its opcode index.
func (c *CPU) setRP(i int, v uint16) {
	switch i {
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

// getRP2 reads a register pair for PUSH and POP by opcode index:
// 0 BC, 1 DE, 2 HL, 3 AF.
func (c *CPU) getRP2(i int) uint16 {
	if i == 3 {
		return uint16(c.a)<<8 | uint16(c.f)
	}
	return c.getRP(i)
}

// setRP2 writes a register pair for PUSH and POP. AF masks F to the
// high nibble, as the hardware does on POP AF.
func (c *CPU) setRP2(i int, v uint16) {
	if i == 3 {
		c.a, c.f = uint8(v>>8), uint8(v)&0xF0
		return
	}
	c.setRP(i, v)
}

// getR reads an 8-bit operand by opcode index:
// 0 B, 1 C, 2 D, 3 E, 4 H, 5 L, 6 (HL), 7 A.
func (c *CPU) getR(bus Bus, i int) uint8 {
	switch i {
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
	case 6:
		return bus.Read(c.hl())
	default:
		return c.a
	}
}

// setR writes an 8-bit operand by opcode index.
func (c *CPU) setR(bus Bus, i int, v uint8) {
	switch i {
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
	case 6:
		bus.Write(c.hl(), v)
	default:
		c.a = v
	}
}

// cond reports whether condition cc holds: 0 NZ, 1 Z, 2 NC, 3 C.
func (c *CPU) cond(cc int) bool {
	switch cc {
	case 0:
		return c.f&flagZ == 0
	case 1:
		return c.f&flagZ != 0
	case 2:
		return c.f&flagC == 0
	default:
		return c.f&flagC != 0
	}
}

func (c *CPU) setFlags(z, n, h, cy bool) {
	var f uint8
	if z {
		f |= flagZ
	}
	if n {
		f |= flagN
	}
	if h {
		f |= flagH
	}
	if cy {
		f |= flagC
	}
	c.f = f
}

// push16 writes a 16-bit value to the stack, high byte first.
func (c *CPU) push16(bus Bus, v uint16) {
	c.sp--
	bus.Write(c.sp, uint8(v>>8))
	c.sp--
	bus.Write(c.sp, uint8(v))
}

// pop16 reads a 16-bit value from the stack, low byte first.
func (c *CPU) pop16(bus Bus) uint16 {
	lo := bus.Read(c.sp)
	c.sp++
	hi := bus.Read(c.sp)
	c.sp++
	return uint16(hi)<<8 | uint16(lo)
}

// fetchOp reads the next opcode byte. Under the HALT bug the byte is
// read but PC does not advance, so the same byte is seen again by the
// following fetch.
func (c *CPU) fetchOp(bus Bus) uint8 {
	v := bus.Read(c.pc)
	if c.haltBug {
		c.haltBug = false
	} else {
		c.pc++
	}
	return v
}

// fetch8 reads an operand byte and advances PC.
func (c *CPU) fetch8(bus Bus) uint8 {
	v := bus.Read(c.pc)
	c.pc++
	return v
}

// fetch16 reads a 16-bit little-endian operand and advances PC.
func (c *CPU) fetch16(bus Bus) uint16 {
	lo := c.fetch8(bus)
	hi := c.fetch8(bus)
	return uint16(hi)<<8 | uint16(lo)
}

// add performs A = A + v + carryIn and sets all four flags.
func (c *CPU) add(v uint8, carryIn bool) {
	cin := uint8(0)
	if carryIn {
		cin = 1
	}
	sum := uint16(c.a) + uint16(v) + uint16(cin)
	half := (c.a&0x0F)+(v&0x0F)+cin > 0x0F
	c.a = uint8(sum)
	c.setFlags(c.a == 0, false, half, sum > 0xFF)
}

// sub performs A = A - v - carryIn and sets all four flags. For CP
// the caller discards the result.
func (c *CPU) sub(v uint8, carryIn bool) uint8 {
	cin := uint8(0)
	if carryIn {
		cin = 1
	}
	res := int(c.a) - int(v) - int(cin)
	half := int(c.a&0x0F)-int(v&0x0F)-int(cin) < 0
	out := uint8(res)
	c.setFlags(out == 0, true, half, res < 0)
	return out
}

// alu runs arithmetic/logic operation i (0 ADD, 1 ADC, 2 SUB, 3 SBC,
// 4 AND, 5 XOR, 6 OR, 7 CP) on A with operand v.
func (c *CPU) alu(i int, v uint8) {
	carry := c.f&flagC != 0
	switch i {
	case 0:
		c.add(v, false)
	case 1:
		c.add(v, carry)
	case 2:
		c.a = c.sub(v, false)
	case 3:
		c.a = c.sub(v, carry)
	case 4:
		c.a &= v
		c.setFlags(c.a == 0, false, true, false)
	case 5:
		c.a ^= v
		c.setFlags(c.a == 0, false, false, false)
	case 6:
		c.a |= v
		c.setFlags(c.a == 0, false, false, false)
	case 7:
		c.sub(v, false) // CP: result discarded, flags kept
	}
}

// inc8 increments an 8-bit value with INC flag rules: carry is
// preserved, N clears, H reports carry out of bit 3.
func (c *CPU) inc8(v uint8) uint8 {
	res := v + 1
	cy := c.f&flagC != 0
	c.setFlags(res == 0, false, v&0x0F == 0x0F, cy)
	return res
}

// dec8 decrements an 8-bit value with DEC flag rules: carry is
// preserved, N sets, H reports borrow into bit 4.
func (c *CPU) dec8(v uint8) uint8 {
	res := v - 1
	cy := c.f&flagC != 0
	c.setFlags(res == 0, true, v&0x0F == 0x00, cy)
	return res
}

// addHL performs HL = HL + v with ADD HL flag rules: Z is preserved,
// N clears, H reports carry out of bit 11, C out of bit 15.
func (c *CPU) addHL(v uint16) {
	hl := c.hl()
	sum := uint32(hl) + uint32(v)
	half := (hl&0x0FFF)+(v&0x0FFF) > 0x0FFF
	z := c.f&flagZ != 0
	c.setHL(uint16(sum))
	c.setFlags(z, false, half, sum > 0xFFFF)
}

// addSPe8 computes SP + signed byte offset with the ADD SP and
// LD HL,SP+e8 flag rules: Z and N always clear, H and C are computed
// from the low byte addition of the unsigned offset byte.
func (c *CPU) addSPe8(e uint8) uint16 {
	sp := c.sp
	half := (sp&0x0F)+(uint16(e)&0x0F) > 0x0F
	cy := (sp&0xFF)+(uint16(e)&0xFF) > 0xFF
	c.setFlags(false, false, half, cy)
	return uint16(int32(int16(sp)) + int32(int8(e)))
}

// daa adjusts A after a BCD addition or subtraction, following the
// published algorithm: corrections of 0x06 and 0x60 are applied based
// on the H and C flags and the operand ranges; N is preserved, H
// clears, Z follows the result, C may be set by the 0x60 correction.
func (c *CPU) daa() {
	a := int(c.a)
	adj := 0
	carry := c.f&flagC != 0
	sub := c.f&flagN != 0
	if c.f&flagH != 0 || (!sub && a&0x0F > 9) {
		adj |= 0x06
	}
	if carry || (!sub && a > 0x99) {
		adj |= 0x60
		carry = true
	}
	if sub {
		a -= adj
	} else {
		a += adj
	}
	c.a = uint8(a)
	c.setFlags(c.a == 0, sub, false, carry)
}

// pendingInterrupts returns the enabled and requested interrupt bits.
func (c *CPU) pendingInterrupts(bus Bus) uint8 {
	return bus.Read(addrIE) & bus.Read(addrIF) & 0x1F
}

// dispatch services the highest priority pending interrupt: IME
// clears, the request bit clears, PC is pushed, and control jumps to
// the interrupt vector. Costs 5 M-cycles on the hardware.
func (c *CPU) dispatch(bus Bus, pending uint8) int {
	bit := 0
	for pending>>uint(bit)&1 == 0 {
		bit++
	}
	bus.Write(addrIF, bus.Read(addrIF)&^(1<<uint(bit)))
	c.ime = false
	c.eiDelay = 0
	c.halted = false
	c.push16(bus, c.pc)
	c.pc = 0x40 + uint16(bit)*8
	c.cycles += 5
	return 5
}

// Step executes one instruction, one interrupt dispatch, or one idle
// M-cycle while halted or stopped, and returns the M-cycles consumed.
func (c *CPU) Step(bus Bus) int {
	if c.stopped {
		c.cycles++
		return 1
	}
	if c.halted {
		pending := c.pendingInterrupts(bus)
		if pending == 0 {
			c.cycles++
			return 1
		}
		c.halted = false
		if c.ime {
			return c.dispatch(bus, pending)
		}
		// IME clear: the CPU simply resumes executing. The HALT bug
		// does not apply here; it is decided when HALT executes.
	} else if c.ime {
		if pending := c.pendingInterrupts(bus); pending != 0 {
			return c.dispatch(bus, pending)
		}
	}
	n := c.execute(bus)
	// A pending EI takes effect once the instruction after EI has
	// completed. eiDelay is armed to 2 by EI itself so its own step
	// does not consume the delay.
	if c.eiDelay > 0 {
		c.eiDelay--
		if c.eiDelay == 0 {
			c.ime = true
		}
	}
	c.cycles += uint64(n)
	return n
}

// Serialize writes the full CPU state into buf, which must be at
// least SerializeSize bytes long.
func (c *CPU) Serialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("sm83: serialize buffer too small")
	}
	for i := 0; i < SerializeSize; i++ {
		buf[i] = 0
	}
	buf[0] = serializeVersion
	buf[1] = c.a
	buf[2] = c.f
	buf[3] = c.b
	buf[4] = c.c
	buf[5] = c.d
	buf[6] = c.e
	buf[7] = c.h
	buf[8] = c.l
	binary.LittleEndian.PutUint16(buf[9:], c.sp)
	binary.LittleEndian.PutUint16(buf[11:], c.pc)
	if c.ime {
		buf[13] = 1
	}
	buf[14] = uint8(c.eiDelay)
	if c.halted {
		buf[15] = 1
	}
	if c.haltBug {
		buf[16] = 1
	}
	if c.stopped {
		buf[17] = 1
	}
	binary.LittleEndian.PutUint64(buf[18:], c.cycles)
	return nil
}

// Deserialize restores a state written by Serialize.
func (c *CPU) Deserialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("sm83: deserialize buffer too small")
	}
	if buf[0] != serializeVersion {
		return errors.New("sm83: unsupported serialize version")
	}
	c.a, c.f = buf[1], buf[2]&0xF0
	c.b, c.c = buf[3], buf[4]
	c.d, c.e = buf[5], buf[6]
	c.h, c.l = buf[7], buf[8]
	c.sp = binary.LittleEndian.Uint16(buf[9:])
	c.pc = binary.LittleEndian.Uint16(buf[11:])
	c.ime = buf[13] != 0
	c.eiDelay = int(buf[14])
	c.halted = buf[15] != 0
	c.haltBug = buf[16] != 0
	c.stopped = buf[17] != 0
	c.cycles = binary.LittleEndian.Uint64(buf[18:])
	return nil
}
