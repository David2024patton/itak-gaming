// Package gekko implements the integer CPU of the Nintendo
// GameCube and Wii: a PowerPC 750 derivative (IBM/Motorola
// Gekko on GameCube, Broadway on Wii), written from the
// PowerPC Microprocessor Family Programming Environments
// manual and the PowerPC 750 user manual.
//
// Slice GCN-1 scope: the complete integer instruction set
// (arithmetic, logical, rotate and shift, load and store in
// every form including multiple, string, and byte-reversed,
// reservation instructions, branches, condition register
// operations, system registers and exceptions), BAT address
// translation, the time base and decrementer, and full
// serialization.
//
// Slice GCN-2 adds the floating point unit and the paired
// single unit in fp.go and ps.go: scalar double and single
// arithmetic with exact rounding and FPSCR reporting, the
// FPSCR move instructions, paired single arithmetic, and
// the quantized paired loads and stores driven by the GQRs.
//
// FPR representation (mixed view, used throughout): an FPR
// is one uint64. A scalar double operand or result is the
// full uint64 read as an IEEE double. A scalar single result
// is stored widened to the double value of that single. A
// paired single value packs its PS0 lane in the high 32 bits
// and its PS1 lane in the low 32 bits, each lane an IEEE
// single. Which view an instruction uses is fixed by the
// instruction, exactly as on hardware, and code that reads
// an FPR through the wrong view gets the corresponding
// reinterpretation.
package gekko

import (
	"encoding/binary"
	"errors"
)

// MSR bit masks (32-bit register values).
const (
	MSRPOW uint32 = 1 << 18 // power management enable
	MSRILE uint32 = 1 << 16 // interrupt little-endian mode
	MSREE  uint32 = 1 << 15 // external interrupt enable
	MSRPR  uint32 = 1 << 14 // privilege level (1 = user)
	MSRFP  uint32 = 1 << 13 // floating point available
	MSRME  uint32 = 1 << 12 // machine check enable
	MSRFE0 uint32 = 1 << 11 // FP exception mode 0
	MSRSE  uint32 = 1 << 10 // single-step trace
	MSRBE  uint32 = 1 << 9  // branch trace
	MSRFE1 uint32 = 1 << 8  // FP exception mode 1
	MSRIP  uint32 = 1 << 7  // interrupt prefix (1 = 0xFFF00000)
	MSRIR  uint32 = 1 << 6  // instruction address translation
	MSRDR  uint32 = 1 << 5  // data address translation
	MSRPM  uint32 = 1 << 4  // performance monitor marked mode
	MSRRI  uint32 = 1 << 2  // recoverable interrupt
	MSRLE  uint32 = 1 << 0  // little-endian mode
)

// Exception vector offsets from the interrupt base.
const (
	VecReset        uint32 = 0x100
	VecMachineCheck uint32 = 0x200
	VecDSI          uint32 = 0x300
	VecISI          uint32 = 0x400
	VecExternal     uint32 = 0x500
	VecAlignment    uint32 = 0x600
	VecProgram      uint32 = 0x700
	VecFPUnavail    uint32 = 0x800
	VecDecrementer  uint32 = 0x900
	VecSyscall      uint32 = 0xC00
	VecTrace        uint32 = 0xD00
)

// SRR1 cause bits for the program exception.
const (
	SRR1ProgIllegal    uint32 = 1 << 19
	SRR1ProgPrivileged uint32 = 1 << 18
	SRR1ProgTrap       uint32 = 1 << 17
)

// DSISR cause bits (also used as SRR1 cause bits for ISI).
const (
	DSISRNoTranslation uint32 = 1 << 30
	DSISRProtection    uint32 = 1 << 27
	DSISRStore         uint32 = 1 << 25
)

// Special purpose register numbers.
const (
	SPRXER   = 1
	SPRLR    = 8
	SPRCTR   = 9
	SPRDSISR = 18
	SPRDAR   = 19
	SPRDEC   = 22
	SPRSDR1  = 25
	SPRSRR0  = 26
	SPRSRR1  = 27
	SPRSPRG0 = 272
	SPRTBLRD = 268 // mftb/mfspr read of time base lower
	SPRTBURD = 269 // mftb/mfspr read of time base upper
	SPRTBLWR = 284 // mtspr write of time base lower
	SPRTBUWR = 285 // mtspr write of time base upper
	SPRPVR   = 287

	SPRIBAT0U = 528
	SPRDBAT0U = 536

	// GQR0 to GQR7. The task wiring for this slice is SPR
	// 896..903. The Gekko manual also names 912..919 for the
	// same registers, so both ranges are wired to the same
	// storage in GetSPR and SetSPR.
	SPRGQR0    = 896
	SPRGQR0Alt = 912
)

// PVR value for the GameCube Gekko (read-only).
const PVRGekko uint32 = 0x00083200

// Bus is the physical memory interface the CPU drives after
// address translation. Read16 and Read32 return big-endian
// values assembled from memory; the CPU applies little-endian
// swapping itself when MSR[LE] is set. Implementations must
// tolerate unaligned addresses (Gekko performs most unaligned
// accesses in hardware by splitting them; this core performs
// them as single bus transactions and does not take a
// second-page fault for a cross-page unaligned access).
type Bus interface {
	Read8(addr uint32) uint8
	Read16(addr uint32) uint16
	Read32(addr uint32) uint32
	Write8(addr uint32, v uint8)
	Write16(addr uint32, v uint16)
	Write32(addr uint32, v uint32)
}

// CPU is the Gekko core.
type CPU struct {
	GPR [32]uint32
	FPR [32]uint64 // see the mixed view note in the package comment
	CR  uint32
	XER uint32 // SO=1<<31, OV=1<<30, CA=1<<29, string byte count in low 7 bits
	LR  uint32
	CTR uint32
	PC  uint32
	MSR uint32

	// FPSCR is the floating point status and control
	// register. Bit constants use PowerPC bit numbering in
	// fp.go; the stored uint32 keeps the conventional layout
	// in which PowerPC bit 0 is the value bit 1<<31.
	FPSCR uint32

	// GQR holds the eight graphics quantization registers
	// used by the paired single quantized loads and stores.
	// Field layout is defined in ps.go.
	GQR [8]uint32

	SRR0  uint32
	SRR1  uint32
	DSISR uint32
	DAR   uint32
	SDR1  uint32
	SR    [16]uint32
	SPRG  [4]uint32

	// TB is the time base counter. It ticks once per four
	// CPU cycles (the Gekko bus clock ratio).
	TB uint64

	// Halted stops Step from doing anything (the system
	// starts the CPU halted after staging a DOL).
	Halted bool

	bus Bus

	dec        uint32
	decPending bool
	tbRem      uint32 // accumulated sub-tick cycles, 0..3
	irq        bool

	reservAddr  uint32
	reservValid bool

	ibat [4][2]uint32 // [i][0] = upper (BATU), [i][1] = lower (BATL)
	dbat [4][2]uint32

	extras [1024]uint32 // all non-dedicated SPR storage

	curIA uint32 // address of the instruction being executed
}

// New returns a CPU in its reset state driving bus.
func New(bus Bus) *CPU {
	c := &CPU{bus: bus}
	c.Reset()
	return c
}

// Reset restores the documented power-on state: exceptions
// prefixed to 0xFFF00000, translation off, interrupts off,
// PC at the reset vector. The wholesale struct replacement
// also clears FPSCR and all GQRs to zero.
func (c *CPU) Reset() {
	*c = CPU{
		bus: c.bus,
		MSR: MSRIP,
		PC:  0xFFF00000 + VecReset,
	}
}

// SetIRQ drives the external interrupt input line.
func (c *CPU) SetIRQ(on bool) { c.irq = on }

// IRQ reports the external interrupt input line.
func (c *CPU) IRQ() bool { return c.irq }

// Decrementer returns the current DEC value.
func (c *CPU) Decrementer() uint32 { return c.dec }

// faultInfo aborts the current instruction via panic and is
// recovered in Step, where it becomes exception entry.
type faultInfo struct {
	vec   uint32
	srr0  uint32
	cause uint32 // SRR1 cause bits
	dsisr uint32 // DSISR value, for DSI and alignment
	dar   uint32 // DAR value, for DSI and alignment
	setDA bool   // write DSISR and DAR on entry
}

func fault(info faultInfo) { panic(&info) }

// Step executes one instruction and returns the cycles it
// consumed. The cycle model is flat: one instruction is one
// cycle (the 750 is superscalar on hardware; this core does
// not model dispatch width). TB and DEC advance at one tick
// per four cycles.
func (c *CPU) Step() (n int) {
	if c.Halted {
		return 0
	}
	n = 1
	defer func() {
		if r := recover(); r != nil {
			if f, ok := r.(*faultInfo); ok {
				if f.setDA {
					c.DSISR = f.dsisr
					c.DAR = f.dar
				}
				c.enterException(f.vec, f.srr0, f.cause)
			} else {
				panic(r)
			}
		}
		c.tick(1)
	}()

	// Pending interrupts are sampled between instructions.
	if c.decPending && c.MSR&MSREE != 0 {
		c.decPending = false
		c.enterException(VecDecrementer, c.PC, 0)
		return 1
	}
	if c.irq && c.MSR&MSREE != 0 {
		c.enterException(VecExternal, c.PC, 0)
		return 1
	}

	ia := c.PC
	c.curIA = ia
	w := c.fetch(ia)
	c.PC = ia + 4
	c.exec(w, ia)
	return 1
}

// tick advances the time base and decrementer by cycles CPU
// cycles. TB and DEC tick once per four CPU cycles. When
// DEC decrements past zero the decrementer exception is
// requested; it is taken at the next Step boundary if
// MSR[EE] is set.
func (c *CPU) tick(cycles uint32) {
	c.tbRem += cycles
	for c.tbRem >= 4 {
		c.tbRem -= 4
		c.TB++
		if c.dec == 0 {
			c.decPending = true
		}
		c.dec--
	}
}

// enterException performs exception entry: SRR0 and SRR1
// are loaded, the reservation is cleared, MSR is replaced
// by its exception value (ME and IP preserved, LE takes
// ILE), and PC moves to the vector. In this core SRR1 saves
// the low half of MSR plus the cause bits; rfi restores
// exactly the architected field mask from it.
func (c *CPU) enterException(vec, srr0, cause uint32) {
	c.SRR0 = srr0
	c.SRR1 = (c.MSR & 0xFFFF) | cause
	old := c.MSR
	nm := old & (MSRME | MSRIP)
	if old&MSRILE != 0 {
		nm |= MSRLE
	}
	c.MSR = nm
	c.reservValid = false
	base := uint32(0)
	if old&MSRIP != 0 {
		base = 0xFFF00000
	}
	c.PC = base + vec
}

// Translate converts an effective address to a physical
// address. fetch selects instruction translation (MSR[IR]
// and the IBATs); otherwise data translation applies
// (MSR[DR] and the DBATs). store marks a write access for
// protection checks. ok is false when translation faults;
// the caller raises DSI or ISI. The translation scope is
// documented in TranslatePhys.
func (c *CPU) Translate(ea uint32, fetch, store bool) (pa uint32, ok bool) {
	pa, _, _, ok = c.translate(ea, fetch, store)
	return pa, ok
}

// translate implements the GCN-1 translation scope:
//
//   - Translation off (IR or DR clear): identity.
//   - Translation on: BAT array lookup with the documented
//     block sizes (128 KB to 256 MB in powers of two) and PP
//     protection qualified by the segment register keys. A
//     matching valid BAT wins.
//   - No page table walk in this slice: a BAT miss raises
//     the same translation exception the hardware raises
//     when its walk finds no PTE (DSISR/ISI translation
//     bit). GC software at boot and the SDK runtime
//     program BATs for the whole address space they use,
//     so this covers the boot path exactly.
func (c *CPU) translate(ea uint32, fetch, store bool) (pa uint32, prot, miss bool, ok bool) {
	if fetch {
		if c.MSR&MSRIR == 0 {
			return ea, false, false, true
		}
		return c.batLookup(c.ibat, ea, store)
	}
	if c.MSR&MSRDR == 0 {
		return ea, false, false, true
	}
	return c.batLookup(c.dbat, ea, store)
}

func (c *CPU) batLookup(bats [4][2]uint32, ea uint32, store bool) (uint32, bool, bool, bool) {
	user := c.MSR&MSRPR != 0
	sr := c.SR[ea>>28]
	ks := sr&(1<<30) != 0
	kp := sr&(1<<29) != 0
	for i := 0; i < 4; i++ {
		batu := bats[i][0]
		batl := bats[i][1]
		vs := batu&(1<<1) != 0
		vp := batu&1 != 0
		if user {
			if !vp {
				continue
			}
		} else if !vs {
			continue
		}
		bl := (batu >> 2) & 0x7FF
		size := (bl + 1) << 17 // 128 KB units
		mask := ^(size - 1)
		if ea&mask != batu&0xFFFF8000&mask {
			continue
		}
		pp := batl & 3
		// Key gate: a set key bit for the current privilege
		// level denies all access through this BAT.
		if (!user && ks) || (user && kp) {
			return 0, true, false, false
		}
		if pp == 0 || (store && pp != 2) {
			return 0, true, false, false
		}
		pa := (batl & 0xFFFF8000) | (ea & (size - 1))
		return pa, false, false, true
	}
	return 0, false, true, false
}

// fetch reads the instruction word at ea, raising ISI on a
// translation fault.
func (c *CPU) fetch(ea uint32) uint32 {
	pa, prot, _, ok := c.translate(ea, true, false)
	if !ok {
		cause := uint32(DSISRNoTranslation)
		if prot {
			cause = DSISRProtection
		}
		panic(&faultInfo{vec: VecISI, srr0: ea, cause: cause})
	}
	w := c.bus.Read32(pa)
	if c.MSR&MSRLE != 0 {
		w = swap32(w)
	}
	return w
}

// load reads size bytes (1, 2, or 4) at effective address
// ea as an unsigned big-endian value, raising DSI on a
// translation fault.
func (c *CPU) load(ea uint32, size int) uint32 {
	pa, prot, _, ok := c.translate(ea, false, false)
	if !ok {
		panic(&faultInfo{
			vec: VecDSI, srr0: c.curIA, setDA: true,
			dsisr: dsisrCause(prot, false), dar: ea,
		})
	}
	var v uint32
	switch size {
	case 1:
		v = uint32(c.bus.Read8(pa))
	case 2:
		v = uint32(c.bus.Read16(pa))
	default:
		v = c.bus.Read32(pa)
	}
	if c.MSR&MSRLE != 0 {
		switch size {
		case 2:
			v = uint32(swap16(uint16(v)))
		case 4:
			v = swap32(v)
		}
	}
	return v
}

// store writes the low size bytes of v at effective address
// ea, raising DSI on a translation fault.
func (c *CPU) store(ea uint32, size int, v uint32) {
	pa, prot, _, ok := c.translate(ea, false, true)
	if !ok {
		panic(&faultInfo{
			vec: VecDSI, srr0: c.curIA, setDA: true,
			dsisr: dsisrCause(prot, true), dar: ea,
		})
	}
	if c.MSR&MSRLE != 0 {
		switch size {
		case 2:
			v = uint32(swap16(uint16(v)))
		case 4:
			v = swap32(v)
		}
	}
	switch size {
	case 1:
		c.bus.Write8(pa, uint8(v))
	case 2:
		c.bus.Write16(pa, uint16(v))
	default:
		c.bus.Write32(pa, v)
	}
}

func dsisrCause(prot, store bool) uint32 {
	d := uint32(DSISRNoTranslation)
	if prot {
		d = DSISRProtection
	}
	if store {
		d |= DSISRStore
	}
	return d
}

// alignFault raises the alignment exception for ea.
func (c *CPU) alignFault(ea uint32) {
	panic(&faultInfo{
		vec: VecAlignment, srr0: c.curIA, setDA: true,
		dsisr: 0, dar: ea,
	})
}

func swap16(v uint16) uint16 { return v<<8 | v>>8 }

func swap32(v uint32) uint32 {
	return v<<24 | (v&0xFF00)<<8 | (v>>8)&0xFF00 | v>>24
}

// sprPrivileged reports whether an SPR is supervisor-only.
// The architected rule: SPR bit 4 of the ten-bit number
// (value 0x10) marks the privileged half of the space.
func sprPrivileged(spr int) bool { return spr&0x10 != 0 }

// GetSPR reads a special purpose register, applying the
// dedicated storage for the registers this core models.
func (c *CPU) GetSPR(spr int) uint32 {
	switch spr {
	case SPRXER:
		return c.XER
	case SPRLR:
		return c.LR
	case SPRCTR:
		return c.CTR
	case SPRDSISR:
		return c.DSISR
	case SPRDAR:
		return c.DAR
	case SPRDEC:
		return c.dec
	case SPRSDR1:
		return c.SDR1
	case SPRSRR0:
		return c.SRR0
	case SPRSRR1:
		return c.SRR1
	case SPRSPRG0 + 0, SPRSPRG0 + 1, SPRSPRG0 + 2, SPRSPRG0 + 3:
		return c.SPRG[spr-SPRSPRG0]
	case SPRTBLRD:
		return uint32(c.TB)
	case SPRTBURD:
		return uint32(c.TB >> 32)
	case SPRPVR:
		return PVRGekko
	}
	if spr >= SPRGQR0 && spr < SPRGQR0+8 {
		return c.GQR[spr-SPRGQR0]
	}
	if spr >= SPRGQR0Alt && spr < SPRGQR0Alt+8 {
		return c.GQR[spr-SPRGQR0Alt]
	}
	if spr >= SPRIBAT0U && spr < SPRIBAT0U+8 {
		n := spr - SPRIBAT0U
		return c.ibat[n/2][n%2]
	}
	if spr >= SPRDBAT0U && spr < SPRDBAT0U+8 {
		n := spr - SPRDBAT0U
		return c.dbat[n/2][n%2]
	}
	return c.extras[spr&0x3FF]
}

// SetSPR writes a special purpose register. Writes to the
// read-only PVR and to the time base read numbers are
// ignored, as on hardware.
func (c *CPU) SetSPR(spr int, v uint32) {
	switch spr {
	case SPRXER:
		c.XER = v
	case SPRLR:
		c.LR = v
	case SPRCTR:
		c.CTR = v
	case SPRDSISR:
		c.DSISR = v
	case SPRDAR:
		c.DAR = v
	case SPRDEC:
		c.dec = v
		c.decPending = false
	case SPRSDR1:
		c.SDR1 = v
	case SPRSRR0:
		c.SRR0 = v
	case SPRSRR1:
		c.SRR1 = v
	case SPRSPRG0 + 0, SPRSPRG0 + 1, SPRSPRG0 + 2, SPRSPRG0 + 3:
		c.SPRG[spr-SPRSPRG0] = v
	case SPRTBLRD, SPRTBURD, SPRPVR:
		// Read-only through the SPR interface.
	case SPRTBLWR:
		c.TB = (c.TB & 0xFFFFFFFF00000000) | uint64(v)
	case SPRTBUWR:
		c.TB = (c.TB & 0x00000000FFFFFFFF) | uint64(v)<<32
	default:
		if spr >= SPRGQR0 && spr < SPRGQR0+8 {
			c.GQR[spr-SPRGQR0] = v
			return
		}
		if spr >= SPRGQR0Alt && spr < SPRGQR0Alt+8 {
			c.GQR[spr-SPRGQR0Alt] = v
			return
		}
		if spr >= SPRIBAT0U && spr < SPRIBAT0U+8 {
			n := spr - SPRIBAT0U
			c.ibat[n/2][n%2] = v
			return
		}
		if spr >= SPRDBAT0U && spr < SPRDBAT0U+8 {
			n := spr - SPRDBAT0U
			c.dbat[n/2][n%2] = v
			return
		}
		c.extras[spr&0x3FF] = v
	}
}

// SerializeSize is the fixed save-state size of the CPU:
// GPRs 128, FPRs 256, twelve registers 48, TB 8, segment
// registers 64, SPRGs 16, IBATs 32, DBATs 32, the generic
// SPR file 4096, reservation address 4, flags 4, and the
// GCN-2 tail: FPSCR 4 plus the eight GQRs 32. The GCN-2
// tail is appended after the flags word so every GCN-1
// field keeps its offset; only the total size grew, by 36.
const SerializeSize = 128 + 256 + 48 + 8 + 64 + 16 + 32 + 32 + 4096 + 4 + 4 + 4 + 32

// Serialize writes the complete CPU state.
func (c *CPU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	off := 0
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(out[off:], v)
		off += 4
	}
	for _, r := range c.GPR {
		put32(r)
	}
	for _, f := range c.FPR {
		binary.BigEndian.PutUint64(out[off:], f)
		off += 8
	}
	put32(c.CR)
	put32(c.XER)
	put32(c.LR)
	put32(c.CTR)
	put32(c.PC)
	put32(c.MSR)
	put32(c.SRR0)
	put32(c.SRR1)
	put32(c.DSISR)
	put32(c.DAR)
	put32(c.SDR1)
	put32(c.dec)
	binary.BigEndian.PutUint64(out[off:], c.TB)
	off += 8
	for _, s := range c.SR {
		put32(s)
	}
	for _, s := range c.SPRG {
		put32(s)
	}
	for i := 0; i < 4; i++ {
		put32(c.ibat[i][0])
		put32(c.ibat[i][1])
	}
	for i := 0; i < 4; i++ {
		put32(c.dbat[i][0])
		put32(c.dbat[i][1])
	}
	for _, e := range c.extras {
		put32(e)
	}
	put32(c.reservAddr)
	var flags uint32
	if c.Halted {
		flags |= 1
	}
	if c.irq {
		flags |= 2
	}
	if c.decPending {
		flags |= 4
	}
	if c.reservValid {
		flags |= 8
	}
	flags |= c.tbRem << 8
	put32(flags)
	// GCN-2 appended tail: FPSCR, then GQR0 through GQR7.
	put32(c.FPSCR)
	for _, g := range c.GQR {
		put32(g)
	}
	if off != SerializeSize {
		panic("gekko: serialize size mismatch")
	}
	return out
}

// ErrStateSize reports a CPU state block of the wrong size.
var ErrStateSize = errors.New("gekko: bad CPU state size")

// Deserialize restores a state written by Serialize.
func (c *CPU) Deserialize(in []byte) error {
	if len(in) != SerializeSize {
		return ErrStateSize
	}
	off := 0
	get32 := func() uint32 {
		v := binary.BigEndian.Uint32(in[off:])
		off += 4
		return v
	}
	for i := range c.GPR {
		c.GPR[i] = get32()
	}
	for i := range c.FPR {
		c.FPR[i] = binary.BigEndian.Uint64(in[off:])
		off += 8
	}
	c.CR = get32()
	c.XER = get32()
	c.LR = get32()
	c.CTR = get32()
	c.PC = get32()
	c.MSR = get32()
	c.SRR0 = get32()
	c.SRR1 = get32()
	c.DSISR = get32()
	c.DAR = get32()
	c.SDR1 = get32()
	c.dec = get32()
	c.TB = binary.BigEndian.Uint64(in[off:])
	off += 8
	for i := range c.SR {
		c.SR[i] = get32()
	}
	for i := range c.SPRG {
		c.SPRG[i] = get32()
	}
	for i := 0; i < 4; i++ {
		c.ibat[i][0] = get32()
		c.ibat[i][1] = get32()
	}
	for i := 0; i < 4; i++ {
		c.dbat[i][0] = get32()
		c.dbat[i][1] = get32()
	}
	for i := range c.extras {
		c.extras[i] = get32()
	}
	c.reservAddr = get32()
	flags := get32()
	c.Halted = flags&1 != 0
	c.irq = flags&2 != 0
	c.decPending = flags&4 != 0
	c.reservValid = flags&8 != 0
	c.tbRem = (flags >> 8) & 3
	// GCN-2 appended tail: FPSCR, then GQR0 through GQR7.
	c.FPSCR = get32()
	for i := range c.GQR {
		c.GQR[i] = get32()
	}
	return nil
}
