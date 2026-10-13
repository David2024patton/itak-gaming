// Package armcore is the shared ARM execution core behind the DS
// module's two processors. One implementation is configured two
// ways: ARMv4T (the DS ARM7TDMI) and ARMv5TE (the DS ARM946E-S,
// "ARM9" here), matching how the two silicon blocks share one
// architecture lineage. The chips/arm7 and chips/arm9 packages
// expose the two configurations under their own names.
//
// The core is written from the published ARM architecture
// documentation and GBATEK. Memory access goes through the Bus
// interface so the same core runs against the DS system bus and
// against the transaction-backed memory used by the
// SingleStepTests ARM7TDMI conformance corpus.
package armcore

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

// CPSR/SPSR flag and control bits.
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

// Exception vectors, offsets from the vector base (0x00000000, or
// 0xFFFF0000 on the ARM9 when the CP15 control V bit is set).
const (
	VectorReset  = 0x00
	VectorUndef  = 0x04
	VectorSWI    = 0x08
	VectorPAbort = 0x0C
	VectorDAbort = 0x10
	VectorIRQ    = 0x18
	VectorFIQ    = 0x1C
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

// Config selects the architecture generation of a CPU instance.
type Config struct {
	// V5 enables the ARMv5TE additions: CLZ, the saturating
	// Q arithmetic, the signed halfword multiplies, LDRD/STRD,
	// BLX, BKPT, PLD handling, load-to-PC state selection from
	// bit 0, and the CP15 system control coprocessor.
	V5 bool
	// Name is "ARM7TDMI" or "ARM946E-S" for diagnostics.
	Name string
}

// cp15Index flattens a CP15 register address (CRn, CRm, opcode2)
// into the storage array.
func cp15Index(crn, crm, opc2 uint32) int {
	return int(crn<<7 | crm<<3 | opc2)
}

// CP15 register addresses used by this core, precomputed.
const (
	cp15ID       = 0 // c0, c0, 0: main ID register
	cp15Control  = 1 << 7
	cp15FSR      = 5 << 7      // c5, c0, 0: fault status
	cp15FAR      = 6 << 7      // c6, c0, 0: fault address
	cp15DTCM     = 9<<7 | 1<<3 // c9, c1, 0: data TCM base/size
	cp15ITCM     = 9<<7 | 1<<3 | 1
	cp15ControlV = 1 << 13 // high exception vectors
	cp15DTCMEn   = 1 << 16
	cp15ITCMEn   = 1 << 18
)

// CPU is the complete processor state for one configured core.
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
	Config Config

	R    [16]uint32
	RFIQ [7]uint32 // R8-R14 of FIQ mode
	RSVC [2]uint32 // R13-R14 of SVC mode
	RABT [2]uint32 // R13-R14 of ABT mode
	RIRQ [2]uint32 // R13-R14 of IRQ mode
	RUND [2]uint32 // R13-R14 of UND mode
	RUSR [2]uint32 // R13-R14 of USR/SYS modes

	CPSR uint32
	SPSR [SPSRCount]uint32

	// CP15 is the ARM9 system control register file, stored raw
	// (2048 words). It is meaningful only on the V5 configuration;
	// the ARM7 configuration never touches it. Cache and MPU
	// registers are storage only in this slice: no protection or
	// caching behavior is enforced from them.
	CP15 [2048]uint32

	// FSR and FAR mirror the ARM9 CP15 fault registers; they are
	// also kept as plain fields so aborts can be raised before
	// CP15 plumbing exists in a test harness.
	FSR uint32
	FAR uint32

	Pipe [2]uint32

	Bus Bus

	// Halted stops the core (used by the system to park a CPU
	// until its boot staging is complete).
	Halted bool

	// IRQLine and FIQLine are the interrupt input levels. They
	// are sampled at instruction boundaries.
	IRQLine bool
	FIQLine bool

	// Executing is the instruction word currently in the execute
	// stage. It is not serialized; it is reloaded from the bus on
	// Deserialize.
	Executing      uint32
	ExecutingValid bool

	// usr8_12 holds the user bank R8-R12 while FIQ mode is active
	// (they swap with the FIQ bank on mode changes). Serialized.
	usr8_12 [5]uint32

	// flushed marks that the last Step refilled the pipeline, so
	// the normal pipeline advance must be skipped.
	flushed bool

	// spsrDummy absorbs MSR-to-SPSR writes in modes that have no
	// SPSR (USR/SYS), where the write is architecturally ignored.
	spsrDummy uint32
}

// NewARM7 returns an ARM7TDMI (ARMv4T) CPU in its reset state.
func NewARM7(b Bus) *CPU {
	c := &CPU{Config: Config{V5: false, Name: "ARM7TDMI"}, Bus: b}
	c.Reset()
	return c
}

// NewARM9 returns an ARM946E-S (ARMv5TE) CPU in its reset state,
// with the DS power-on CP15 TCM configuration installed.
func NewARM9(b Bus) *CPU {
	c := &CPU{Config: Config{V5: true, Name: "ARM946E-S"}, Bus: b}
	c.Reset()
	return c
}

// Mode returns the current processor mode bits.
func (c *CPU) Mode() uint32 { return c.CPSR & 0x1F }

// Thumb reports whether the core is in Thumb state.
func (c *CPU) Thumb() bool { return c.CPSR&BitT != 0 }

// VectorBase returns the exception vector base for this CPU: the
// high vector address on the ARM9 when CP15 control bit 13 is
// set, otherwise zero.
func (c *CPU) VectorBase() uint32 {
	if c.Config.V5 && c.CP15[cp15Control]&cp15ControlV != 0 {
		return 0xFFFF0000
	}
	return 0
}

// Reset puts the CPU in the documented power-on state: SVC mode,
// ARM state, IRQ and FIQ disabled, PC at the reset vector,
// pipeline refilled from the vector. On the ARM9 it also installs
// the DS CP15 defaults: instruction TCM 32 KB at 0x00000000 and
// data TCM 16 KB at 0x00800000, both enabled (the state DS
// software expects to find and reprogram).
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
	c.CP15 = [2048]uint32{}
	c.FSR, c.FAR = 0, 0
	c.CPSR = ModeSVC | BitI | BitF
	c.usr8_12 = [5]uint32{}
	c.flushed = false
	c.spsrDummy = 0
	c.Halted = false
	c.IRQLine, c.FIQLine = false, false
	if c.Config.V5 {
		c.CP15[cp15ID] = 0x41059461 // ARM946E-S ID
		c.CP15[cp15Control] = cp15DTCMEn | cp15ITCMEn
		c.CP15[cp15DTCM] = 0x00800000 | 5<<1 // base 0x00800000, 16 KB
		c.CP15[cp15ITCM] = 6 << 1            // base 0x00000000, 32 KB
	}
	c.R[15] = 0
	c.refill(c.VectorBase())
}

// Jump refills the pipeline at addr in the current state. The
// system uses it to start a CPU at a cartridge entry point after
// boot staging.
func (c *CPU) Jump(addr uint32) {
	c.refill(addr)
	c.Halted = false
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

// TCMRegion decodes one TCM configuration register value into
// base and size in bytes. The size field encodes the region size
// as 512 bytes shifted left by the field value.
func TCMRegion(reg uint32) (base, size uint32) {
	base = reg & 0xFFFFF000
	field := (reg >> 1) & 0x1F
	if field > 15 {
		field = 15
	}
	size = 512 << field
	return base, size
}

// DTCMRegion returns the configured data TCM window and whether
// the data TCM is enabled in the CP15 control register.
func (c *CPU) DTCMRegion() (base, size uint32, enabled bool) {
	base, size = TCMRegion(c.CP15[cp15DTCM])
	return base, size, c.CP15[cp15Control]&cp15DTCMEn != 0
}

// ITCMRegion returns the configured instruction TCM window and
// whether the instruction TCM is enabled.
func (c *CPU) ITCMRegion() (base, size uint32, enabled bool) {
	base, size = TCMRegion(c.CP15[cp15ITCM])
	return base, size, c.CP15[cp15Control]&cp15ITCMEn != 0
}

// CP15Read returns the stored CP15 register at the given address.
// Reads of the fault registers return the live FSR/FAR fields.
func (c *CPU) CP15Read(crn, crm, opc2 uint32) uint32 {
	switch cp15Index(crn, crm, opc2) {
	case cp15FSR:
		return c.FSR
	case cp15FAR:
		return c.FAR
	}
	return c.CP15[cp15Index(crn, crm, opc2)]
}

// CP15Write stores a CP15 register. Writes to the fault registers
// update the live FSR/FAR fields as well as the storage.
func (c *CPU) CP15Write(crn, crm, opc2, val uint32) {
	switch cp15Index(crn, crm, opc2) {
	case cp15FSR:
		c.FSR = val
	case cp15FAR:
		c.FAR = val
	}
	c.CP15[cp15Index(crn, crm, opc2)] = val
}

// StateSize is the serialized size of the CPU state in bytes.
const StateSize = (16 + 7 + 2 + 2 + 2 + 2 + 2 + 1 + SPSRCount + 2 + 5 + 1 + 2048 + 2) * 4

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
	if c.IRQLine {
		flags |= 2
	}
	if c.FIQLine {
		flags |= 4
	}
	w.u32(flags)
	for _, v := range c.CP15 {
		w.u32(v)
	}
	w.u32(c.FSR)
	w.u32(c.FAR)
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
	c.IRQLine = flags&2 != 0
	c.FIQLine = flags&4 != 0
	for i := range c.CP15 {
		c.CP15[i] = r.u32()
	}
	c.FSR = r.u32()
	c.FAR = r.u32()
	// The executing instruction is the one behind Pipe in the
	// stream: at R15-8 (ARM) or R15-4 (Thumb). Re-fetch it from
	// the bus so the next Step executes the same instruction the
	// saved core was about to execute.
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
