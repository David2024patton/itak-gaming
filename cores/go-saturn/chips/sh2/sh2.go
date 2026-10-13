// Package sh2 implements the SH-2 CPU, the SH-2 subset of
// SuperH, as used in the Sega Saturn main processors.
//
// The core is a plain interpreter. One Step call executes
// one instruction; a delayed branch also executes its
// delay slot inside the same Step call. Cycle counts are
// the nominal counts from the programming manual states
// column, simplified as documented on Step.
//
// Serialized layout, SerializeSize bytes, big endian:
// bytes 0 to 63: Reg 0 to 15, four bytes each
// bytes 64 to 67: PC
// bytes 68 to 71: PR
// bytes 72 to 75: SR
// bytes 76 to 79: GBR
// bytes 80 to 83: VBR
// bytes 84 to 87: MACH
// bytes 88 to 91: MACL
// bytes 92 to 99: Cycles, eight bytes
// byte 100: Sleeping flag, 0 or 1
// byte 101: pending NMI flag, 0 or 1
// byte 102: pending interrupt: high bit set when valid,
// plus the level in the low four bits
// byte 103: pending interrupt vector
package sh2

import (
	"encoding/binary"
	"errors"
)

// SerializeSize is the exact size of a serialized CPU.
const SerializeSize = 104

// Bus is the memory and device space the CPU talks to.
// Read16 and Read32 return big endian assembled values:
// the bus, not the CPU, owns byte order assembly.
type Bus interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, v uint8)
	Write16(addr uint32, v uint16)
	Write32(addr uint32, v uint32)
}

// CPU is one SH-2 processor. The exported fields are the
// architecturally visible state.
type CPU struct {
	Reg  [16]uint32
	PC   uint32
	PR   uint32
	SR   uint32
	GBR  uint32
	VBR  uint32
	MACH uint32
	MACL uint32

	Cycles   uint64
	Sleeping bool

	bus        Bus
	pendNMI    bool
	pendValid  bool
	pendLevel  int
	pendVector int
	cur        uint32
}

// New returns a CPU attached to b, in the Go zero state.
// Call Reset to place it at a known execution point.
func New(b Bus) *CPU {
	c := &CPU{}
	c.bus = b
	return c
}

// Attach swaps the bus the CPU talks to.
func (c *CPU) Attach(b Bus) {
	c.bus = b
}

// Reset clears all CPU state, sets SR to 0x400000F0 (the
// MD bit plus interrupt mask 15), VBR to 0, PC to pc, and
// the stack pointer register Reg 15 to sp.
func (c *CPU) Reset(pc, sp uint32) {
	c.Reg = [16]uint32{}
	c.PR = 0
	c.GBR = 0
	c.MACH = 0
	c.MACL = 0
	c.Cycles = 0
	c.Sleeping = false
	c.pendNMI = false
	c.pendValid = false
	c.pendLevel = 0
	c.pendVector = 0
	c.SR = 0x400000F0
	c.VBR = 0
	c.PC = pc
	c.Reg[15] = sp
}

// RaiseNMI latches a pending non maskable interrupt. It
// is accepted at the next Step boundary regardless of
// the SR interrupt mask, with vector 11.
func (c *CPU) RaiseNMI() {
	c.pendNMI = true
}

// Interrupt latches a maskable interrupt request. It is
// accepted at a Step boundary when level is greater
// than the SR I field. Only the highest level pending
// request is kept. The return value reports whether
// this request is the one now latched.
func (c *CPU) Interrupt(level, vector int) bool {
	if level < 1 || level > 15 {
		return false
	}
	if vector < 0 || vector > 127 {
		return false
	}
	if c.pendValid && c.pendLevel >= level {
		return false
	}
	c.pendValid = true
	c.pendLevel = level
	c.pendVector = vector
	return true
}

// Serialize returns the CPU state in the layout given
// in the package comment, exactly SerializeSize bytes.
func (c *CPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	for i := 0; i < 16; i += 1 {
		binary.BigEndian.PutUint32(out[i*4:i*4+4], c.Reg[i])
	}
	binary.BigEndian.PutUint32(out[64:68], c.PC)
	binary.BigEndian.PutUint32(out[68:72], c.PR)
	binary.BigEndian.PutUint32(out[72:76], c.SR)
	binary.BigEndian.PutUint32(out[76:80], c.GBR)
	binary.BigEndian.PutUint32(out[80:84], c.VBR)
	binary.BigEndian.PutUint32(out[84:88], c.MACH)
	binary.BigEndian.PutUint32(out[88:92], c.MACL)
	binary.BigEndian.PutUint64(out[92:100], c.Cycles)
	if c.Sleeping {
		out[100] = 1
	}
	if c.pendNMI {
		out[101] = 1
	}
	if c.pendValid {
		out[102] = 0x80 | byte(c.pendLevel&0x0F)
	}
	out[103] = byte(c.pendVector & 0x7F)
	return out
}

// Deserialize restores a state produced by Serialize.
// It returns an error unless the length matches exactly.
func (c *CPU) Deserialize(data []byte) error {
	if len(data) != SerializeSize {
		return errors.New("sh2: state size mismatch")
	}
	for i := 0; i < 16; i += 1 {
		c.Reg[i] = binary.BigEndian.Uint32(data[i*4 : i*4+4])
	}
	c.PC = binary.BigEndian.Uint32(data[64:68])
	c.PR = binary.BigEndian.Uint32(data[68:72])
	c.SR = binary.BigEndian.Uint32(data[72:76])
	c.GBR = binary.BigEndian.Uint32(data[76:80])
	c.VBR = binary.BigEndian.Uint32(data[80:84])
	c.MACH = binary.BigEndian.Uint32(data[84:88])
	c.MACL = binary.BigEndian.Uint32(data[88:92])
	c.Cycles = binary.BigEndian.Uint64(data[92:100])
	c.Sleeping = data[100] != 0
	c.pendNMI = data[101] != 0
	c.pendValid = data[102]&0x80 != 0
	c.pendLevel = int(data[102] & 0x0F)
	c.pendVector = int(data[103] & 0x7F)
	return nil
}

// SR bit accessors. T is bit 0, S is bit 1, the I field
// is bits 4 to 7, Q is bit 8, M is bit 9, MD is bit 30.
// Writes to SR elsewhere in this file change only the
// bits named; an LDC to SR stores the value as given.

func (c *CPU) getT() int {
	if c.SR&0x00000001 != 0 {
		return 1
	}
	return 0
}

func (c *CPU) setT(v int) {
	if v != 0 {
		c.SR |= 0x00000001
	} else {
		c.SR &^= 0x00000001
	}
}

func (c *CPU) getS() int {
	if c.SR&0x00000002 != 0 {
		return 1
	}
	return 0
}

func (c *CPU) getQ() int {
	if c.SR&0x00000100 != 0 {
		return 1
	}
	return 0
}

func (c *CPU) setQ(v int) {
	if v != 0 {
		c.SR |= 0x00000100
	} else {
		c.SR &^= 0x00000100
	}
}

func (c *CPU) getM() int {
	if c.SR&0x00000200 != 0 {
		return 1
	}
	return 0
}

func (c *CPU) setQM(q, m int) {
	c.setQ(q)
	if m != 0 {
		c.SR |= 0x00000200
	} else {
		c.SR &^= 0x00000200
	}
}

func (c *CPU) getI() int {
	return int((c.SR >> 4) & 0x0F)
}

func (c *CPU) setI(level int) {
	c.SR &^= 0x000000F0
	c.SR |= uint32(level&0x0F) << 4
}

func b2u(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sext8(v uint32) uint32 {
	return uint32(int32(int8(v)))
}

func sext16(v uint32) uint32 {
	return uint32(int32(int16(v)))
}

func sext12(v uint32) int32 {
	x := int32(v & 0x0FFF)
	if x&0x0800 != 0 {
		x -= 0x1000
	}
	return x
}

// addrError is panicked by the memory helpers when an
// access breaks the alignment rules. Step recovers it
// and raises the CPU address error exception, vector 7,
// with the saved PC set to instr, the address of the
// instruction that made the bad access.
type addrError struct {
	instr uint32
}

func (c *CPU) checkAlign(addr uint32, size int) {
	if size == 2 && addr&1 != 0 {
		panic(addrError{instr: c.cur})
	}
	if size == 4 && addr&3 != 0 {
		panic(addrError{instr: c.cur})
	}
}

func (c *CPU) rd8(addr uint32) uint8 {
	return c.bus.Read8(addr)
}

func (c *CPU) rd16(addr uint32) uint16 {
	c.checkAlign(addr, 2)
	return c.bus.Read16(addr)
}

func (c *CPU) rd32(addr uint32) uint32 {
	c.checkAlign(addr, 4)
	return c.bus.Read32(addr)
}

func (c *CPU) wr8(addr uint32, v uint8) {
	c.bus.Write8(addr, v)
}

func (c *CPU) wr16(addr uint32, v uint16) {
	c.checkAlign(addr, 2)
	c.bus.Write16(addr, v)
}

func (c *CPU) wr32(addr uint32, v uint32) {
	c.checkAlign(addr, 4)
	c.bus.Write32(addr, v)
}

func (c *CPU) fetch(addr uint32) uint16 {
	if addr&1 != 0 {
		panic(addrError{instr: addr})
	}
	return c.bus.Read16(addr)
}

// loadSigned reads a byte, word, or long from memory and
// sign extends byte and word results into a register
// value. size is 1, 2, or 4.
func (c *CPU) loadSigned(addr uint32, size int) uint32 {
	switch size {
	case 1:
		return sext8(uint32(c.rd8(addr)))
	case 2:
		return sext16(uint32(c.rd16(addr)))
	default:
		return c.rd32(addr)
	}
}

// store writes the low byte, word, or long of v using
// the matching bus width. size is 1, 2, or 4.
func (c *CPU) store(addr uint32, v uint32, size int) {
	switch size {
	case 1:
		c.wr8(addr, uint8(v))
	case 2:
		c.wr16(addr, uint16(v))
	default:
		c.wr32(addr, v)
	}
}

func sizeOf(low uint32) int {
	switch low {
	case 0, 4, 12:
		return 1
	case 1, 5, 13:
		return 2
	default:
		return 4
	}
}

// enterException pushes SR then the saved PC onto the
// stack (so the saved PC sits at Reg 15 and the saved SR
// at Reg 15 plus 4, matching RTE), then loads the PC
// from the vector slot at VBR plus vector times 4. The
// saved SR is the SR value from before any later change.
func (c *CPU) enterException(vector int, savedPC uint32) {
	sp := c.Reg[15]
	sp -= 4
	c.Reg[15] = sp
	c.bus.Write32(sp, c.SR)
	sp -= 4
	c.Reg[15] = sp
	c.bus.Write32(sp, savedPC)
	c.PC = c.bus.Read32(c.VBR + uint32(vector)*4)
}

// enterInterrupt is enterException plus the rule that
// an accepted interrupt sets the SR I field to the
// accepted level; the stack still holds the old SR.
func (c *CPU) enterInterrupt(vector int, savedPC uint32, level int) {
	c.enterException(vector, savedPC)
	c.setI(level)
}

// illegal raises the general illegal instruction
// exception, vector 4, saved PC at the instruction.
func (c *CPU) illegal(addr uint32) (int, bool) {
	c.enterException(4, addr)
	return 1, true
}

// Step executes one instruction and returns its nominal
// cycle count, which is also added to Cycles.
//
// Nominal counts used here: most register and immediate
// ops 1; loads and stores 2; the byte forms at GBR for
// the immediate logic group 3; MULU.W and MULS.W 1;
// MUL.L 2; DMULS.L and DMULU.L 2; MAC.W and MAC.L 3;
// DIV steps 1; BF and BT cost 3 taken and 1 not taken;
// BF/S and BT/S cost 2 taken and 1 not taken (a not
// taken form skips its delay slot); other delayed
// branches cost 2, including their delay slot in this
// simple model; TRAPA 4. An interrupt accepted at the
// Step boundary costs 4 and the handler instruction
// itself runs on a later Step. While Sleeping, Step
// returns 1 without fetching until a wakeup is accepted.
func (c *CPU) Step() int {
	n := c.stepInner()
	c.Cycles += uint64(n)
	return n
}

func (c *CPU) stepInner() (cycles int) {
	cycles = 1
	defer func() {
		if r := recover(); r != nil {
			if ae, ok := r.(addrError); ok {
				c.Sleeping = false
				c.enterException(7, ae.instr)
				cycles = 1
				return
			}
			panic(r)
		}
	}()
	if c.pendNMI {
		c.pendNMI = false
		c.Sleeping = false
		c.enterInterrupt(11, c.PC, 15)
		return 4
	}
	if c.pendValid && c.pendLevel > c.getI() {
		level := c.pendLevel
		vector := c.pendVector
		c.pendValid = false
		c.Sleeping = false
		c.enterInterrupt(vector, c.PC, level)
		return 4
	}
	if c.Sleeping {
		return 1
	}
	addr := c.PC
	c.cur = addr
	op := c.fetch(addr)
	n, _ := c.exec(op, addr)
	return n
}
