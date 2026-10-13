// Package arm7 is a from scratch ARM7TDMI CPU core, written for the
// Dreamcast AICA sound processor (which embeds an ARM7DI variant run
// here as an ARM7TDMI without caches or MMU). It implements the full
// ARMv4T instruction set: ARM state data processing, multiplies, PSR
// transfers, single and block loads and stores, halfword and signed
// transfers, swaps, branches, and software interrupts, plus the
// complete Thumb state instruction set, with mode banking and the
// full exception model.
//
// The register file keeps the active mode view in R. Banked
// registers live in their own storage (RFIQ, RSVC, RABT, RIRQ, RUND,
// RUSR for the user and system stack and link registers, and usr8_12
// for the user R8 to R12 values shadowed while in FIQ mode). Mode
// switches swap the live view against that storage. R[15] holds the
// architectural program counter: the address of the executing
// instruction plus 8 in ARM state and plus 4 in Thumb state.
//
// The pipeline is modelled as three slots. Executing is the opcode
// in the execute slot, Pipe[0] is the decode slot, and Pipe[1] is
// the fetch slot. Each Step first fills the fetch slot from the bus
// at R[15], then executes the execute slot, then either shifts the
// pipeline (sequential flow) or refills it (any program counter
// write, branch, or exception). This matches the model the Tom
// Harte SingleStepTests ARM7TDMI corpus compares.
//
// This CPU has no MMU and the Bus interface carries no fault
// signal, so prefetch and data abort exceptions, while implemented,
// are unreachable with any bus that always completes its accesses.
package arm7

import "encoding/binary"

// Bus is the memory interface the CPU runs against. Accesses carry
// the effective address verbatim, including unaligned addresses;
// the CPU applies the ARM7TDMI rotation rules to returned data.
type Bus interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, v uint8)
	Write16(addr uint32, v uint16)
	Write32(addr uint32, v uint32)
}

// Processor modes, as encoded in CPSR bits 4 to 0.
const (
	ModeUSR = 0x10
	ModeFIQ = 0x11
	ModeIRQ = 0x12
	ModeSVC = 0x13
	ModeABT = 0x17
	ModeUND = 0x1B
	ModeSYS = 0x1F
)

// CPSR bit positions used by the core.
const (
	cpsrN     = 1 << 31
	cpsrZ     = 1 << 30
	cpsrC     = 1 << 29
	cpsrV     = 1 << 28
	cpsrI     = 1 << 7
	cpsrF     = 1 << 6
	cpsrT     = 1 << 5
	cpsrMode  = 0x1F
	flagMask  = 0xF0000000
	thumbFlag = cpsrT
)

// SPSR storage indices. The order matches the corpus state dump:
// fiq, svc, abt, irq, und.
const (
	spsrFIQ = iota
	spsrSVC
	spsrABT
	spsrIRQ
	spsrUND
)

// CPU is one ARM7TDMI core. The zero value is not valid; use New.
type CPU struct {
	Bus Bus

	// R is the active mode register view. R[15] is the program
	// counter in its architectural (lookahead) form.
	R [16]uint32
	// Banked register storage, in corpus layout. RFIQ holds the
	// FIQ R8 to R14 values while another mode is active (and is
	// stale while FIQ is active; the live values are in R then).
	RFIQ [7]uint32
	RSVC [2]uint32
	RABT [2]uint32
	RIRQ [2]uint32
	RUND [2]uint32
	// RUSR holds the user and system R13 and R14 while a
	// privileged mode is active.
	RUSR [2]uint32
	// usr8_12 holds the shared R8 to R12 values while FIQ mode is
	// active (FIQ has its own bank for those registers).
	usr8_12 [5]uint32

	CPSR uint32
	// SPSR holds the saved program status registers in the order
	// fiq, svc, abt, irq, und.
	SPSR [5]uint32

	// Executing is the opcode in the execute slot. Pipe[0] is the
	// decode slot and Pipe[1] is the fetch slot. In Thumb state the
	// slots hold zero extended halfwords.
	Executing uint32
	Pipe      [2]uint32

	irqLine bool
	fiqLine bool
	flushed bool
}

// New returns a CPU attached to bus b, in the post reset state.
func New(b Bus) *CPU {
	c := &CPU{Bus: b}
	c.Reset()
	return c
}

// Reset puts the CPU in the reset exception state: supervisor mode,
// IRQ and FIQ masked, ARM state, program counter at the reset
// vector 0x00000000, all other state zeroed.
func (c *CPU) Reset() {
	bus := c.Bus
	irq, fiq := c.irqLine, c.fiqLine
	*c = CPU{Bus: bus, irqLine: irq, fiqLine: fiq}
	c.CPSR = ModeSVC | cpsrI | cpsrF
	c.R[15] = 8
	if bus != nil {
		c.Executing = bus.Read32(0)
		c.Pipe[0] = bus.Read32(4)
	}
}

// Mode returns the current processor mode from CPSR.
func (c *CPU) Mode() uint32 { return c.CPSR & cpsrMode }

// hasSPSR reports whether the current mode owns a saved program
// status register (every mode except user and system).
func (c *CPU) hasSPSR() bool {
	m := c.Mode()
	return m != ModeUSR && m != ModeSYS
}

// Thumb reports whether the CPU is in Thumb state.
func (c *CPU) Thumb() bool { return c.CPSR&cpsrT != 0 }

// SetIRQ drives the level sensitive IRQ input.
func (c *CPU) SetIRQ(asserted bool) { c.irqLine = asserted }

// SetFIQ drives the level sensitive FIQ input.
func (c *CPU) SetFIQ(asserted bool) { c.fiqLine = asserted }

// Run executes instructions until at least cycles cycles have been
// consumed and returns the cycles actually consumed.
func (c *CPU) Run(cycles int) int {
	spent := 0
	for spent < cycles {
		spent += c.Step()
	}
	return spent
}

// Step executes one instruction (or takes a pending interrupt) and
// returns the cycles consumed, using GBATEK style cycle counts.
func (c *CPU) Step() int {
	if c.Bus == nil {
		return 1
	}
	// Fetch stage: the word or halfword at the architectural PC
	// enters the fetch slot. On a flush this value is discarded,
	// which the corpus transaction lists also show.
	if c.Thumb() {
		c.Pipe[1] = uint32(c.Bus.Read16(c.R[15]))
	} else {
		c.Pipe[1] = c.Bus.Read32(c.R[15])
	}
	c.flushed = false
	// The sequential increment uses the instruction size of the
	// state the step started in, even if the instruction itself
	// switches state (an MSR into Thumb state still advances the
	// program counter by the ARM instruction size).
	thumbAtStart := c.Thumb()

	// Interrupts are sampled between instructions. FIQ has
	// priority over IRQ.
	if c.fiqLine && c.CPSR&cpsrF == 0 {
		lr := c.R[15]
		if !c.Thumb() {
			lr -= 4
		}
		c.enterException(0x1C, lr, ModeFIQ, true)
		return 3
	}
	if c.irqLine && c.CPSR&cpsrI == 0 {
		lr := c.R[15]
		if !c.Thumb() {
			lr -= 4
		}
		c.enterException(0x18, lr, ModeIRQ, false)
		return 3
	}

	op := c.Executing
	var cycles int
	if c.Thumb() {
		cycles = c.execThumb(uint16(op))
	} else {
		cycles = c.execARM(op)
	}
	if !c.flushed {
		size := uint32(4)
		if thumbAtStart {
			size = 2
		}
		c.Executing = c.Pipe[0]
		c.Pipe[0] = c.Pipe[1]
		c.R[15] += size
	}
	return cycles
}

// fetch returns the opcode at addr in the current state size.
func (c *CPU) fetch(addr uint32) uint32 {
	if c.Thumb() {
		return uint32(c.Bus.Read16(addr))
	}
	return c.Bus.Read32(addr)
}

// flush refills the pipeline with the instruction at addr, which
// becomes the new execute slot, and marks the step as flushed so
// Step does not also shift the pipeline. The caller must have the
// CPSR T bit in its final state before calling flush. The
// execution address keeps any low bits the writing instruction
// produced (they are visible in R[15]), but the refill fetches
// themselves are taken from the aligned address, which is the
// behaviour the corpus transaction lists show.
func (c *CPU) flush(addr uint32) {
	if c.Thumb() {
		c.R[15] = addr + 4
		fa := addr &^ 1
		c.Executing = c.fetch(fa)
		c.Pipe[0] = c.fetch(fa + 2)
	} else {
		c.R[15] = addr + 8
		fa := addr &^ 3
		c.Executing = c.fetch(fa)
		c.Pipe[0] = c.fetch(fa + 4)
	}
	c.flushed = true
}

// spsrIndex maps a mode to its SPSR storage slot, or -1 for the
// modes that have no SPSR (user, system, and invalid encodings).
func spsrIndex(mode uint32) int {
	switch mode {
	case ModeFIQ:
		return spsrFIQ
	case ModeSVC:
		return spsrSVC
	case ModeABT:
		return spsrABT
	case ModeIRQ:
		return spsrIRQ
	case ModeUND:
		return spsrUND
	}
	return -1
}

// currentSPSR reads the SPSR of the current mode. In user and
// system mode, which have no SPSR, it reads back the CPSR, which
// is the behaviour the corpus asserts for MRS in those modes.
func (c *CPU) currentSPSR() uint32 {
	if i := spsrIndex(c.Mode()); i >= 0 {
		return c.SPSR[i]
	}
	return c.CPSR
}

// setCurrentSPSR writes the SPSR of the current mode. Writes in
// user and system mode go nowhere.
func (c *CPU) setCurrentSPSR(v uint32) {
	if i := spsrIndex(c.Mode()); i >= 0 {
		c.SPSR[i] = v
	}
}

// switchBanks swaps the live register view between two modes: the
// outgoing mode banked registers are stored, then the incoming
// mode banked registers are loaded. Modes outside the six defined
// ones share the user bank, matching how the reference model
// treats stray MSR written mode values.
func (c *CPU) switchBanks(oldMode, newMode uint32) {
	if oldMode == newMode {
		return
	}
	switch oldMode {
	case ModeFIQ:
		copy(c.RFIQ[0:5], c.R[8:13])
		c.RFIQ[5], c.RFIQ[6] = c.R[13], c.R[14]
		copy(c.R[8:13], c.usr8_12[:])
	case ModeSVC:
		c.RSVC[0], c.RSVC[1] = c.R[13], c.R[14]
	case ModeABT:
		c.RABT[0], c.RABT[1] = c.R[13], c.R[14]
	case ModeIRQ:
		c.RIRQ[0], c.RIRQ[1] = c.R[13], c.R[14]
	case ModeUND:
		c.RUND[0], c.RUND[1] = c.R[13], c.R[14]
	default: // user, system, invalid
		c.RUSR[0], c.RUSR[1] = c.R[13], c.R[14]
	}
	switch newMode {
	case ModeFIQ:
		copy(c.usr8_12[:], c.R[8:13])
		copy(c.R[8:13], c.RFIQ[0:5])
		c.R[13], c.R[14] = c.RFIQ[5], c.RFIQ[6]
	case ModeSVC:
		c.R[13], c.R[14] = c.RSVC[0], c.RSVC[1]
	case ModeABT:
		c.R[13], c.R[14] = c.RABT[0], c.RABT[1]
	case ModeIRQ:
		c.R[13], c.R[14] = c.RIRQ[0], c.RIRQ[1]
	case ModeUND:
		c.R[13], c.R[14] = c.RUND[0], c.RUND[1]
	default:
		c.R[13], c.R[14] = c.RUSR[0], c.RUSR[1]
	}
}

// setCPSR installs a complete new CPSR value, swapping register
// banks if the mode changes. Flag only updates should use the
// flag helpers instead. The mode field is installed with bit 4
// forced set: mode values written by software pass through the
// reference model mode decoder, which maps the low four bits
// onto the mode set that all have bit 4 set, and the corpus
// asserts the forced bit in the resulting CPSR.
func (c *CPU) setCPSR(v uint32) {
	v = (v &^ cpsrMode) | ((v & cpsrMode) | 0x10)
	c.switchBanks(c.Mode(), v&cpsrMode)
	c.CPSR = v
}

// restoreCPSRFromSPSR performs the exception return CPSR restore:
// the CPSR is replaced by the current mode SPSR.
func (c *CPU) restoreCPSRFromSPSR() {
	c.setCPSR(c.currentSPSR())
}

// enterException performs exception entry: the CPSR is saved into
// the target mode SPSR, banks are swapped, the target mode is
// entered with IRQ masked (and FIQ masked for FIQ and reset), the
// state is forced to ARM, the link register receives lr, and the
// pipeline is refilled at the vector.
func (c *CPU) enterException(vector, lr, mode uint32, maskFIQ bool) {
	old := c.CPSR
	if i := spsrIndex(mode); i >= 0 {
		c.SPSR[i] = old
	}
	c.switchBanks(c.Mode(), mode)
	nc := (old &^ cpsrMode) | mode
	nc |= cpsrI
	nc &^= cpsrT
	if maskFIQ {
		nc |= cpsrF
	}
	c.CPSR = nc
	c.R[14] = lr
	c.flush(vector)
}

// undefinedException raises the undefined instruction exception
// for the instruction at the current execute slot. In ARM state
// the link register is the next instruction address; in Thumb
// state it is the same convention at halfword granularity. Both
// reduce to R[15] minus the instruction size doubled, which the
// callers express directly.
func (c *CPU) undefinedException() int {
	lr := c.R[15] - 4
	if c.Thumb() {
		lr = c.R[15] - 2
	}
	c.enterException(0x04, lr, ModeUND, false)
	return 3
}

// swiException raises the software interrupt exception.
func (c *CPU) swiException() int {
	lr := c.R[15] - 4
	if c.Thumb() {
		lr = c.R[15] - 2
	}
	c.enterException(0x08, lr, ModeSVC, false)
	return 3
}

// Flag helpers. The condition flags live in CPSR bits 31 to 28 and
// are preserved verbatim outside the bits an instruction defines.

func (c *CPU) flagN() bool { return c.CPSR&cpsrN != 0 }
func (c *CPU) flagZ() bool { return c.CPSR&cpsrZ != 0 }
func (c *CPU) flagC() bool { return c.CPSR&cpsrC != 0 }
func (c *CPU) flagV() bool { return c.CPSR&cpsrV != 0 }

func (c *CPU) setFlag(bit uint32, on bool) {
	if on {
		c.CPSR |= bit
	} else {
		c.CPSR &^= bit
	}
}

// setNZ sets the N and Z flags from a 32 bit result.
func (c *CPU) setNZ(res uint32) {
	c.setFlag(cpsrN, res&0x80000000 != 0)
	c.setFlag(cpsrZ, res == 0)
}

// setNZ64 sets the N and Z flags from a 64 bit result.
func (c *CPU) setNZ64(res uint64) {
	c.setFlag(cpsrN, res&0x8000000000000000 != 0)
	c.setFlag(cpsrZ, res == 0)
}

// setLogicalFlags applies the flag result of a logical or shift
// operation: N and Z from the result, C from the shifter, V kept.
func (c *CPU) setLogicalFlags(res uint32, carry bool) {
	c.setNZ(res)
	c.setFlag(cpsrC, carry)
}

// addFlags computes x plus y plus carryIn (0 or 1), sets NZCV as
// an ARM arithmetic operation does, and returns the result. The
// overflow bit uses the carry into bit 31 versus the carry out of
// bit 31, which stays correct with a carry input.
func (c *CPU) addFlags(x, y, carryIn uint32) uint32 {
	full := uint64(x) + uint64(y) + uint64(carryIn)
	res := uint32(full)
	c31 := ((x&0x7FFFFFFF)+(y&0x7FFFFFFF)+carryIn)>>31&1 == 1
	c32 := full>>32&1 == 1
	c.setNZ(res)
	c.setFlag(cpsrC, c32)
	c.setFlag(cpsrV, c31 != c32)
	return res
}

// subFlags computes x minus y minus (1 minus carryIn) by way of
// addition with the complemented operand, as the hardware does.
func (c *CPU) subFlags(x, y, carryIn uint32) uint32 {
	return c.addFlags(x, ^y, carryIn)
}

// condPassed evaluates an ARM condition field against the flags.
// Condition 0xF is the never condition in ARMv4.
func (c *CPU) condPassed(cond uint32) bool {
	switch cond {
	case 0x0:
		return c.flagZ()
	case 0x1:
		return !c.flagZ()
	case 0x2:
		return c.flagC()
	case 0x3:
		return !c.flagC()
	case 0x4:
		return c.flagN()
	case 0x5:
		return !c.flagN()
	case 0x6:
		return c.flagV()
	case 0x7:
		return !c.flagV()
	case 0x8:
		return c.flagC() && !c.flagZ()
	case 0x9:
		return !c.flagC() || c.flagZ()
	case 0xA:
		return c.flagN() == c.flagV()
	case 0xB:
		return c.flagN() != c.flagV()
	case 0xC:
		return !c.flagZ() && c.flagN() == c.flagV()
	case 0xD:
		return c.flagZ() || c.flagN() != c.flagV()
	case 0xE:
		return true
	}
	return false
}

// ror32 rotates a 32 bit value right. s must be in 0 to 31.
func ror32(v, s uint32) uint32 {
	if s == 0 {
		return v
	}
	return v>>s | v<<(32-s)
}

// reg reads a register for an operand fetch. When pcPlus is set
// and the register is the program counter, the value is the
// executing instruction address plus 12 (the stored R[15] plus 4);
// this is the rule for register specified shifts, for stored PC
// values, and for swap source operands. Otherwise R[15] reads as
// its stored lookahead value.
func (c *CPU) reg(n uint32, pcPlus bool) uint32 {
	if n == 15 && pcPlus {
		return c.R[15] + 4
	}
	return c.R[n]
}

// writePC writes a new program counter value from an instruction
// that branches without changing the exception state (BX and BLX
// handle their own state switch before calling flush). Only bit
// 0 is masked off: the corpus shows bit 1 of the target surviving
// into the program counter in both states on this hardware.
func (c *CPU) writePC(v uint32) {
	c.flush(v &^ 1)
}

// Data access helpers shared by ARM and Thumb state. They encode
// the ARM7TDMI unaligned access behaviour exactly as the corpus
// asserts it: word loads rotate the returned word right by the
// address low bits times 8; halfword loads from an odd address
// return the halfword rotated right by 8 in a full 32 bit lane
// (the low byte wraps into the top byte); signed halfword loads
// from an odd address return the sign extended high byte of the
// returned halfword. Stores pass the effective address through.

func (c *CPU) loadWord(addr uint32) uint32 {
	return ror32(c.Bus.Read32(addr), (addr&3)*8)
}

func (c *CPU) loadHalf(addr uint32) uint32 {
	v := uint32(c.Bus.Read16(addr))
	if addr&1 != 0 {
		return ror32(v, 8)
	}
	return v
}

func (c *CPU) loadHalfSigned(addr uint32) uint32 {
	v := uint32(c.Bus.Read16(addr))
	if addr&1 != 0 {
		return uint32(int32(int8(v >> 8)))
	}
	return uint32(int32(int16(v)))
}

func (c *CPU) loadByte(addr uint32) uint32 {
	return uint32(c.Bus.Read8(addr))
}

func (c *CPU) loadByteSigned(addr uint32) uint32 {
	return uint32(int32(int8(c.Bus.Read8(addr))))
}

func (c *CPU) storeWord(addr, v uint32) { c.Bus.Write32(addr, v) }
func (c *CPU) storeHalf(addr, v uint32) { c.Bus.Write16(addr, uint16(v)) }
func (c *CPU) storeByte(addr, v uint32) { c.Bus.Write8(addr, uint8(v)) }

// SerializeSize is the fixed byte size of a serialized CPU state.
const SerializeSize = 192

// Serialize writes the complete CPU state into dst, which must be
// at least SerializeSize bytes, and returns SerializeSize. The
// layout is: R (64 bytes), RFIQ (28), RSVC, RABT, RIRQ, RUND (8
// each), RUSR (8), user R8 to R12 shadow (20), CPSR (4), SPSR
// array (20), Executing (4), Pipe (8), and one flags byte (bit 0
// IRQ line, bit 1 FIQ line) padded to the fixed size. The bus
// pointer is not part of the state.
func (c *CPU) Serialize(dst []byte) int {
	_ = dst[SerializeSize-1]
	put := func(off int, v uint32) { binary.LittleEndian.PutUint32(dst[off:], v) }
	off := 0
	for i := 0; i < 16; i++ {
		put(off, c.R[i])
		off += 4
	}
	for i := 0; i < 7; i++ {
		put(off, c.RFIQ[i])
		off += 4
	}
	for _, b := range [][2]uint32{c.RSVC, c.RABT, c.RIRQ, c.RUND, c.RUSR} {
		put(off, b[0])
		put(off+4, b[1])
		off += 8
	}
	for i := 0; i < 5; i++ {
		put(off, c.usr8_12[i])
		off += 4
	}
	put(off, c.CPSR)
	off += 4
	for i := 0; i < 5; i++ {
		put(off, c.SPSR[i])
		off += 4
	}
	put(off, c.Executing)
	off += 4
	put(off, c.Pipe[0])
	put(off+4, c.Pipe[1])
	off += 8
	flags := byte(0)
	if c.irqLine {
		flags |= 1
	}
	if c.fiqLine {
		flags |= 2
	}
	dst[off] = flags
	return SerializeSize
}

// Deserialize restores a state written by Serialize. It returns
// false, leaving the CPU untouched, when src is too short.
func (c *CPU) Deserialize(src []byte) bool {
	if len(src) < SerializeSize {
		return false
	}
	get := func(off int) uint32 { return binary.LittleEndian.Uint32(src[off:]) }
	off := 0
	for i := 0; i < 16; i++ {
		c.R[i] = get(off)
		off += 4
	}
	for i := 0; i < 7; i++ {
		c.RFIQ[i] = get(off)
		off += 4
	}
	banks := []*[2]uint32{&c.RSVC, &c.RABT, &c.RIRQ, &c.RUND, &c.RUSR}
	for _, b := range banks {
		b[0] = get(off)
		b[1] = get(off + 4)
		off += 8
	}
	for i := 0; i < 5; i++ {
		c.usr8_12[i] = get(off)
		off += 4
	}
	c.CPSR = get(off)
	off += 4
	for i := 0; i < 5; i++ {
		c.SPSR[i] = get(off)
		off += 4
	}
	c.Executing = get(off)
	off += 4
	c.Pipe[0] = get(off)
	c.Pipe[1] = get(off + 4)
	off += 8
	c.irqLine = src[off]&1 != 0
	c.fiqLine = src[off]&2 != 0
	c.flushed = false
	return true
}
