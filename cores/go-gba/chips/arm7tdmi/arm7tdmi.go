// Package arm7tdmi implements the ARM7TDMI CPU core used by the
// Game Boy Advance: full ARM (ARMv4T) and Thumb instruction sets,
// banked registers for all six exception modes, and cycle counts in
// the ARM7TDMI S/N/I cycle model as published in GBATEK and the
// ARM7TDMI technical reference manual.
//
// The core is written from the published architecture documentation.
// Memory access goes through the Bus interface so the same core runs
// against the GBA system bus and against the transaction-backed
// memory used by the Tom Harte (SingleStepTests) conformance corpus.
package arm7tdmi

// Processor modes, CPSR bits 4-0.
const (
	ModeUSR = 0x10
	ModeFIQ = 0x11
	ModeIRQ = 0x12
	ModeSVC = 0x13
	ModeABT = 0x17
	ModeUND = 0x1B
	ModeSYS = 0x1F
)

// CPSR/SPSR flag bits.
const (
	FlagN = 1 << 31
	FlagZ = 1 << 30
	FlagC = 1 << 29
	FlagV = 1 << 28
	FlagQ = 1 << 27

	BitI = 1 << 7
	BitF = 1 << 6
	BitT = 1 << 5
)

// SPSR slots, in the order the conformance corpus lists them.
const (
	SPSRFIQ = iota
	SPSRSVC
	SPSRABT
	SPSRIRQ
	SPSRUND
	SPSRCount
)

// Bus is the memory interface the CPU executes against. Read and
// Write are data accesses; Fetch is an instruction-stream read.
// Sizes are in bytes: 1, 2 or 4. Read returns the value zero
// extended to 32 bits (the CPU applies rotation and sign extension
// rules itself). Write passes the value in the low bits.
type Bus interface {
	Read(addr uint32, size int) uint32
	Write(addr uint32, size int, val uint32)
	Fetch(addr uint32, size int) uint32
}

// CPU is the complete ARM7TDMI processor state.
//
// R is the register file as seen in the current mode. The banked
// registers of the inactive modes live in the RFIQ/RSVC/RABT/RIRQ/
// RUND/RUSR fields; mode switches swap R8-R14 (and R8-R12 for FIQ)
// between R and the bank fields, so at any moment R plus the bank
// fields together hold every physical register exactly once, with
// one exception: when a mode is active its bank fields are stale
// copies and R is authoritative for that mode.
//
// Pipe holds the two fetched instruction words behind the
// instruction being executed: in ARM state Pipe[0] is the word in
// the decode stage (the next instruction) and Pipe[1] the word in
// the fetch stage; in Thumb state they hold halfwords, zero
// extended. The executing instruction sits at R[15]-8 in ARM state
// and R[15]-4 in Thumb state.
type CPU struct {
	R    [16]uint32
	RFIQ [7]uint32 // R8-R14 of FIQ mode
	RSVC [2]uint32 // R13-R14 of SVC mode
	RABT [2]uint32 // R13-R14 of ABT mode
	RIRQ [2]uint32 // R13-R14 of IRQ mode
	RUND [2]uint32 // R13-R14 of UND mode
	RUSR [2]uint32 // R13-R14 of USR/SYS modes

	CPSR uint32
	SPSR [SPSRCount]uint32

	Pipe [2]uint32

	Bus Bus

	// Halted is set by instructions that stop the core on real
	// hardware paths the GBA never takes out of (unused here; kept
	// in the serialized state for future slices).
	Halted bool

	// IRQLine is the level sensitive interrupt request input.
	// The system drives it from its interrupt controller: while
	// it is set and the CPSR I bit is clear, Step enters the IRQ
	// exception instead of executing the next instruction. It is
	// an external input, not processor state, so it is not
	// serialized.
	IRQLine bool

	// SWIHook, when non-nil, intercepts software interrupts
	// before the SWI exception is taken. It receives the raw SWI
	// comment field (the full 24-bit field in ARM state, the
	// 8-bit field in Thumb state) and reports the cycle count and
	// whether it handled the call. A hook that handles the call
	// is responsible for arranging the continuation, normally by
	// calling Jump with the return address (the SWI address + 4
	// in ARM state, + 2 in Thumb state), or by setting Halted for
	// the calls that wait. It is system wiring, not processor
	// state, so it is not serialized.
	SWIHook func(c *CPU, imm uint32) (int, bool)

	// Executing is the instruction word currently in the execute
	// stage. It is not serialized; it is reloaded from Pipe state
	// on Deserialize via ExecutingValid.
	Executing      uint32
	ExecutingValid bool

	// usr8_12 holds the user bank R8-R12 while FIQ mode is active
	// (they swap with the FIQ bank on mode changes). Serialized.
	usr8_12 [5]uint32

	// flushed marks that the last Step refilled the pipeline, so
	// the normal pipeline advance must be skipped.
	flushed bool

	// spsrDummy absorbs SPSR reads in modes that have no SPSR
	// (USR/SYS); reads there return the CPSR instead and this
	// field is never used. It exists so MSR-to-SPSR in such modes
	// has a harmless sink during development of test code.
	spsrDummy uint32
}

// New returns a CPU in its reset state, bound to bus b.
func New(b Bus) *CPU {
	c := &CPU{Bus: b}
	c.Reset()
	return c
}

// Mode returns the current processor mode bits.
func (c *CPU) Mode() uint32 { return c.CPSR & 0x1F }

// Thumb reports whether the core is in Thumb state.
func (c *CPU) Thumb() bool { return c.CPSR&BitT != 0 }

// Reset puts the CPU in the documented power-on state: SVC mode,
// ARM state, IRQ and FIQ disabled, PC at the reset vector 0x00,
// pipeline refilled from the vector.
func (c *CPU) Reset() {
	for i := range c.R {
		c.R[i] = 0
	}
	c.RFIQ = [7]uint32{}
	c.RSVC = [2]uint32{}
	c.RABT = [2]uint32{}
	c.RIRQ = [2]uint32{}
	c.RUND = [2]uint32{}
	c.RUSR = [2]uint32{}
	c.SPSR = [SPSRCount]uint32{}
	c.CPSR = ModeSVC | BitI | BitF
	c.usr8_12 = [5]uint32{}
	c.flushed = false
	c.spsrDummy = 0
	c.Halted = false
	c.R[15] = 0
	c.refill(0)
}

// refill loads the pipeline starting at the branch target addr
// and sets R15 so the unit at addr is in the execute position.
// In ARM state the R15 register keeps the target value verbatim
// (no low bits discarded, as the reference behavior shows); in
// Thumb state bit 0 is discarded. Instruction fetches are
// issued at the aligned address either way: word aligned in ARM
// state, halfword aligned in Thumb state.
func (c *CPU) refill(addr uint32) {
	if c.Thumb() {
		fa := addr &^ 1
		c.Executing = c.Bus.Fetch(fa, 2)
		c.Pipe[0] = c.Bus.Fetch(fa+2, 2)
		c.Pipe[1] = c.Bus.Fetch(fa+4, 2)
		c.R[15] = fa + 4
	} else {
		fa := addr &^ 3
		c.Executing = c.Bus.Fetch(fa, 4)
		c.Pipe[0] = c.Bus.Fetch(fa+4, 4)
		c.Pipe[1] = c.Bus.Fetch(fa+8, 4)
		c.R[15] = addr + 8
	}
	c.ExecutingValid = true
}

// StateSize is the serialized size of the CPU state in bytes.
const StateSize = (16+7+2+2+2+2+2)*4 + 4 + SPSRCount*4 + 2*4 + 5*4 + 4

// Serialize writes the full CPU state to dst, which must be at
// least StateSize bytes. It returns the number of bytes written.
func (c *CPU) Serialize(dst []byte) int {
	w := &stateWriter{b: dst}
	for _, v := range c.R {
		w.u32(v)
	}
	for _, v := range c.RFIQ {
		w.u32(v)
	}
	for _, v := range c.RSVC {
		w.u32(v)
	}
	for _, v := range c.RABT {
		w.u32(v)
	}
	for _, v := range c.RIRQ {
		w.u32(v)
	}
	for _, v := range c.RUND {
		w.u32(v)
	}
	for _, v := range c.RUSR {
		w.u32(v)
	}
	w.u32(c.CPSR)
	for _, v := range c.SPSR {
		w.u32(v)
	}
	w.u32(c.Pipe[0])
	w.u32(c.Pipe[1])
	for _, v := range c.usr8_12 {
		w.u32(v)
	}
	var flags uint32
	if c.Halted {
		flags |= 1
	}
	w.u32(flags)
	return w.n
}

// Deserialize restores a state written by Serialize. It reports
// false, leaving the CPU untouched, when src is too short.
func (c *CPU) Deserialize(src []byte) bool {
	if len(src) < StateSize {
		return false
	}
	r := &stateReader{b: src}
	for i := range c.R {
		c.R[i] = r.u32()
	}
	for i := range c.RFIQ {
		c.RFIQ[i] = r.u32()
	}
	for i := range c.RSVC {
		c.RSVC[i] = r.u32()
	}
	for i := range c.RABT {
		c.RABT[i] = r.u32()
	}
	for i := range c.RIRQ {
		c.RIRQ[i] = r.u32()
	}
	for i := range c.RUND {
		c.RUND[i] = r.u32()
	}
	for i := range c.RUSR {
		c.RUSR[i] = r.u32()
	}
	c.CPSR = r.u32()
	for i := range c.SPSR {
		c.SPSR[i] = r.u32()
	}
	c.Pipe[0] = r.u32()
	c.Pipe[1] = r.u32()
	for i := range c.usr8_12 {
		c.usr8_12[i] = r.u32()
	}
	flags := r.u32()
	c.Halted = flags&1 != 0
	// The executing instruction is the one behind Pipe in the
	// stream: at R15-8 (ARM) or R15-4 (Thumb), the word two units
	// back from the fetch stage. Re-fetch it from the bus so the
	// next Step executes the same instruction the saved core was
	// about to execute.
	if c.Thumb() {
		c.Executing = c.Bus.Fetch((c.R[15]-4)&^1, 2)
	} else {
		c.Executing = c.Bus.Fetch((c.R[15]-8)&^3, 4)
	}
	c.ExecutingValid = true
	return true
}

type stateWriter struct {
	b []byte
	n int
}

func (w *stateWriter) u32(v uint32) {
	w.b[w.n+0] = byte(v)
	w.b[w.n+1] = byte(v >> 8)
	w.b[w.n+2] = byte(v >> 16)
	w.b[w.n+3] = byte(v >> 24)
	w.n += 4
}

type stateReader struct {
	b []byte
	n int
}

func (r *stateReader) u32() uint32 {
	v := uint32(r.b[r.n]) | uint32(r.b[r.n+1])<<8 | uint32(r.b[r.n+2])<<16 | uint32(r.b[r.n+3])<<24
	r.n += 4
	return v
}
