// Package sh4 implements a from-scratch SH-4 CPU core: the complete
// integer instruction set, the SH-4 FPU (single and double precision,
// including the graphics operations FIPR and FTRV), and a functional
// MMU built around a 64-entry unified TLB.
//
// The implementation is a plain interpreter. Step executes exactly one
// instruction (including correct delay-slot semantics) and always
// reports a cost of 1 cycle; this is a functional model, not a timing
// model.
//
// Simplifications, all documented at the point of use:
//   - No caches are simulated, so the cache instructions (PREF, OCBI,
//     OCBP, OCBWB, MOVCA.L) are architectural no-ops apart from the
//     MOVCA.L store itself.
//   - FPU arithmetic uses Go float32/float64 arithmetic (IEEE round to
//     nearest). FSRRA is computed as 1/sqrt(x) in float64 and rounded
//     to float32 rather than using the hardware approximation table.
//     FIPR and FTRV accumulate in float32, term by term, in index order.
//   - Interrupts are a simple model: RaiseInterrupt records INTEVT and,
//     when SR.BL is 0, vectors immediately to VBR+0x600. No interrupt
//     priority or level comparison against SR.IMASK is performed,
//     because the API carries no level.
package sh4

import "encoding/binary"

// PhysicalMemory is the byte-addressed physical memory the CPU talks
// to after address translation. All accesses are little endian.
type PhysicalMemory interface {
	Read8(pa uint32) uint8
	Read16(pa uint32) uint16
	Read32(pa uint32) uint32
	Read64(pa uint32) uint64
	Write8(pa uint32, v uint8)
	Write16(pa uint32, v uint16)
	Write32(pa uint32, v uint32)
	Write64(pa uint32, v uint64)
}

// TLBEntry is one unified TLB entry. VPN stores the virtual page base
// (the full virtual address masked down to the page size), and PPN
// stores the physical page base, already page aligned. Size selects
// the page size: 0 = 1KB, 1 = 4KB, 2 = 64KB, 3 = 1MB. PR holds the
// protection field: bit0 (value 1) means writable, bit1 (value 2)
// means accessible from user mode. D is the dirty bit: a write to a
// valid, writable, but clean page raises the initial page write
// exception. SH is the shared-page flag (recorded, not acted on).
type TLBEntry struct {
	VPN  uint32
	PPN  uint32
	Size int
	V    bool
	PR   int
	D    bool
	SH   bool
}

// SR bit positions.
const (
	srT     = 1 << 0
	srS     = 1 << 1
	srIMASK = 0xF << 4
	srQ     = 1 << 8
	srM     = 1 << 9
	srFD    = 1 << 15
	srBL    = 1 << 28
	srRB    = 1 << 29
	srMD    = 1 << 30
)

// FPSCR bit positions.
const (
	fpscrFR = 1 << 21
	fpscrSZ = 1 << 20
	fpscrPR = 1 << 19
)

// Exception event codes stored in EXPEVT.
const (
	excTLBMissRead  = 0x040
	excTLBMissWrite = 0x060
	excInitialWrite = 0x080
	excProtRead     = 0x0A0
	excProtWrite    = 0x0C0
	excAddrErrRead  = 0x0E0
	excAddrErrWrite = 0x100
	excFPU          = 0x120
	excTRAPA        = 0x160
	excIllegal      = 0x180
	excSlotIllegal  = 0x1A0
	excFPUDisabled  = 0x800
)

// CPU is the complete architectural state of the SH-4 core.
//
// R is the active general-register view. R0 to R7 exist in two banks;
// Bank holds the inactive bank's R0 to R7. When the CPU is privileged
// (SR.MD = 1) and SR.RB = 1, bank 1 is active and R shows bank 1 while
// Bank holds bank 0; in every other mode R shows bank 0. The two are
// swapped whenever the active bank changes (LDC to SR, RTE, exception
// entry), so R always reflects architectural state at instruction
// boundaries.
//
// FR is the active floating-point register bank selected by
// FPSCR.FR; XFR is the inactive bank.
type CPU struct {
	R    [16]uint32
	Bank [8]uint32

	PC, PR, SR, GBR, VBR, SSR, SPC, SGR, DBR uint32

	MACH, MACL uint32

	FPUL  uint32
	FPSCR uint32
	FR    [16]uint32
	XFR   [16]uint32

	MMUCR, PTEH, PTEL, PTEA, TTB, TEA uint32

	EXPEVT, INTEVT, TRA, TTR uint32

	UTLB [64]TLBEntry

	Halted bool

	mem PhysicalMemory

	urc        int    // UTLB replacement counter for LDTLB
	inDelay    bool   // next instruction is a delay slot
	afterDelay uint32 // PC to continue at after the delay slot
	excepted   bool   // an exception was taken during exec
}

// New creates a CPU attached to mem and resets it.
func New(mem PhysicalMemory) *CPU {
	c := &CPU{mem: mem}
	c.Reset()
	return c
}

// Reset restores the power-on state: PC = 0xA0000000,
// SR = 0x700080F0 (MD, RB, BL, FD set, interrupt mask 15), VBR = 0,
// FPSCR = 0x00040001 (the documented SH-4
// power-on values), all other registers and the TLB zeroed, MMU
// address translation off, and the CPU running (not halted).
func (c *CPU) Reset() {
	mem := c.mem
	*c = CPU{mem: mem}
	c.PC = 0xA0000000
	c.SR = 0x700080F0
	c.FPSCR = 0x00040001
}

// Run executes instructions until the cycle budget is consumed. Each
// Step costs exactly 1 in this functional model.
func (c *CPU) Run(cycles int) {
	for i := 0; i < cycles; i++ {
		c.Step()
	}
}

// Step executes exactly one instruction, or takes the exception that
// the instruction raises, and returns 1. A halted CPU (after SLEEP)
// returns 1 without executing anything.
func (c *CPU) Step() int {
	if c.Halted || c.mem == nil {
		return 1
	}
	pc := c.PC
	c.excepted = false
	op, ok := c.read16(pc)
	if !ok {
		c.enterMemFault(pc)
		return 1
	}
	wasDelay := c.inDelay
	after := c.afterDelay
	c.inDelay = false
	if wasDelay && isDelaySlotForbidden(op) {
		c.enterException(excSlotIllegal, pc)
		return 1
	}
	c.exec(op, pc, wasDelay)
	if c.excepted {
		return 1
	}
	if wasDelay {
		c.PC = after
	}
	return 1
}

// isDelaySlotForbidden reports whether op belongs to the class of
// instructions that may not occupy a delay slot: anything that can
// modify the PC (branches, jumps, returns, RTE) plus TRAPA.
func isDelaySlotForbidden(op uint16) bool {
	switch op >> 12 {
	case 0xA, 0xB: // BRA, BSR
		return true
	case 0x8:
		switch op >> 8 {
		case 0x89, 0x8B, 0x8D, 0x8F: // BT, BF, BT/S, BF/S
			return true
		}
	case 0xC:
		if op>>8 == 0xC3 { // TRAPA
			return true
		}
	case 0x0:
		low := op & 0xF
		mid := (op >> 4) & 0xF
		if low == 0x3 && (mid == 0x0 || mid == 0x2) { // BSRF, BRAF
			return true
		}
		if op == 0x000B || op == 0x002B { // RTS, RTE
			return true
		}
	case 0x4:
		if op&0xF == 0xB {
			mid := (op >> 4) & 0xF
			if mid == 0x0 || mid == 0x2 { // JSR, JMP
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Register bank and flag helpers

// bank1Active reports whether bank 1 is the active R0 to R7 bank,
// which is exactly when the CPU is privileged and SR.RB is set.
func (c *CPU) bank1Active() bool {
	return c.SR&srMD != 0 && c.SR&srRB != 0
}

func (c *CPU) swapBanks() {
	for i := 0; i < 8; i++ {
		c.R[i], c.Bank[i] = c.Bank[i], c.R[i]
	}
}

// setSR stores a new SR value, swapping the R0 to R7 banks if the
// active bank changes as a result.
func (c *CPU) setSR(v uint32) {
	old := c.bank1Active()
	c.SR = v
	if c.bank1Active() != old {
		c.swapBanks()
	}
}

// setFPSCR stores a new FPSCR value, swapping the FPU register banks
// if FPSCR.FR changes.
func (c *CPU) setFPSCR(v uint32) {
	old := c.FPSCR & fpscrFR
	c.FPSCR = v
	if v&fpscrFR != old {
		c.FR, c.XFR = c.XFR, c.FR
	}
}

func (c *CPU) getT() uint32 {
	if c.SR&srT != 0 {
		return 1
	}
	return 0
}

func (c *CPU) setT(v uint32) {
	if v&1 != 0 {
		c.SR |= srT
	} else {
		c.SR &^= srT
	}
}

func (c *CPU) setS(v uint32) {
	if v&1 != 0 {
		c.SR |= srS
	} else {
		c.SR &^= srS
	}
}

func (c *CPU) getQ() uint32 {
	if c.SR&srQ != 0 {
		return 1
	}
	return 0
}

func (c *CPU) getM() uint32 {
	if c.SR&srM != 0 {
		return 1
	}
	return 0
}

func (c *CPU) setQM(q, m uint32) {
	c.SR &^= srQ | srM
	if q&1 != 0 {
		c.SR |= srQ
	}
	if m&1 != 0 {
		c.SR |= srM
	}
}

// ---------------------------------------------------------------------------
// Exceptions

// enterException performs the standard SH-4 exception sequence: the
// current SR, return PC, and R15 are saved to SSR, SPC, and SGR,
// SR.MD, SR.RB, and SR.BL are set (swapping register banks if that
// changes the active bank), and control transfers to VBR+0x100, or
// VBR+0x600 for TLB miss exceptions.
func (c *CPU) enterException(code uint32, returnPC uint32) {
	c.EXPEVT = code
	c.SSR = c.SR
	c.SPC = returnPC
	c.SGR = c.R[15]
	c.setSR(c.SR | srMD | srRB | srBL)
	off := uint32(0x100)
	if code == excTLBMissRead || code == excTLBMissWrite {
		off = 0x600
	}
	c.PC = c.VBR + off
	c.inDelay = false
	c.excepted = true
}

// enterMemFault vectors the translation fault that TranslateVA just
// recorded in EXPEVT and TEA.
func (c *CPU) enterMemFault(returnPC uint32) {
	c.enterException(c.EXPEVT, returnPC)
}

// illegal raises the general illegal instruction exception, or the
// slot illegal instruction exception when the opcode sits in a delay
// slot.
func (c *CPU) illegal(inDelay bool, pc uint32) {
	if inDelay {
		c.enterException(excSlotIllegal, pc)
	} else {
		c.enterException(excIllegal, pc)
	}
}

// RaiseInterrupt records an interrupt event in INTEVT. Simple model:
// if SR.BL is set the interrupt is blocked and stays recorded but is
// not taken; otherwise the CPU vectors immediately, using the
// exception register-save sequence and the interrupt vector at
// VBR+0x600. EXPEVT is not touched (interrupts report through
// INTEVT). No level comparison against SR.IMASK is performed because
// this API carries no interrupt level.
func (c *CPU) RaiseInterrupt(code uint32) {
	c.INTEVT = code
	if c.SR&srBL != 0 {
		return
	}
	c.SSR = c.SR
	c.SPC = c.PC
	c.SGR = c.R[15]
	c.setSR(c.SR | srMD | srRB | srBL)
	c.PC = c.VBR + 0x600
	c.inDelay = false
}

// ---------------------------------------------------------------------------
// MMU

// pageMask returns the address mask for a TLB page-size code:
// bits above the page offset are set.
func pageMask(size int) uint32 {
	switch size {
	case 0:
		return ^uint32(0x3FF) // 1KB
	case 1:
		return ^uint32(0xFFF) // 4KB
	case 2:
		return ^uint32(0xFFFF) // 64KB
	default:
		return ^uint32(0xFFFFF) // 1MB
	}
}

// TranslateVA translates a virtual address to a physical address.
//
// The P1 (0x80000000-0x9FFFFFFF), P2 (0xA0000000-0xBFFFFFFF), and P4
// (0xE0000000-0xFFFFFFFF) areas are never translated. The P0
// (0x00000000-0x7FFFFFFF) and P3 (0xC0000000-0xDFFFFFFF) areas go
// through the UTLB when MMUCR.AT is 1, and map directly
// (pa = va & 0x1FFFFFFF) when it is 0. User mode (SR.MD = 0) may only
// access P0; touching any other area is an address error even when
// translation is off.
//
// On a fault TranslateVA returns ok = false and records the fault in
// TEA and EXPEVT (TLB miss, TLB protection, initial page write, or
// address error, using the read or write variant selected by
// isWrite). It does not vector the CPU; instruction execution does
// that through enterMemFault so tests can inspect the recorded state.
func (c *CPU) TranslateVA(va uint32, isWrite bool) (uint32, bool) {
	fail := func(code uint32) (uint32, bool) {
		c.TEA = va
		c.EXPEVT = code
		return 0, false
	}
	if c.SR&srMD == 0 && va >= 0x80000000 {
		if isWrite {
			return fail(excAddrErrWrite)
		}
		return fail(excAddrErrRead)
	}
	if c.MMUCR&1 == 0 {
		return va & 0x1FFFFFFF, true
	}
	switch va >> 29 {
	case 4, 5, 7: // P1, P2, P4: never translated
		return va & 0x1FFFFFFF, true
	}
	// P0 or P3: UTLB lookup. The first entry whose masked VPN matches
	// is the candidate, whether or not it is valid.
	for i := range c.UTLB {
		e := &c.UTLB[i]
		mask := pageMask(e.Size)
		if va&mask != e.VPN&mask {
			continue
		}
		if !e.V {
			if isWrite {
				return fail(excTLBMissWrite)
			}
			return fail(excTLBMissRead)
		}
		if c.SR&srMD == 0 && e.PR&2 == 0 {
			if isWrite {
				return fail(excProtWrite)
			}
			return fail(excProtRead)
		}
		if isWrite && e.PR&1 == 0 {
			return fail(excProtWrite)
		}
		if isWrite && !e.D {
			return fail(excInitialWrite)
		}
		return e.PPN&mask | va&^mask, true
	}
	if isWrite {
		return fail(excTLBMissWrite)
	}
	return fail(excTLBMissRead)
}

// loadTLB implements LDTLB: the UTLB entry at the replacement counter
// is loaded from PTEH, PTEL, and PTEA, and the counter advances
// modulo 64. The PTEL layout used here (a documented simplification
// of the hardware layout) is: PPN = PTEL & 0xFFFFFC00, V = bit 8,
// PR = bits 6..5, SZ = bits 4..3 (0 = 1KB, 1 = 4KB, 2 = 64KB,
// 3 = 1MB), D = bit 2 (the manual's bit 9 and bit 1 positions are
// also honored), SH = bit 0 (bit 1 also honored). The VPN comes from
// PTEH masked to the page size; the ASID in PTEH is ignored. PTEA is
// not needed at these page sizes and is ignored.
func (c *CPU) loadTLB() {
	sz := int(c.PTEL >> 3 & 3)
	mask := pageMask(sz)
	e := TLBEntry{
		VPN:  c.PTEH & mask,
		PPN:  c.PTEL & 0xFFFFFC00 & mask,
		Size: sz,
		V:    c.PTEL&(1<<8) != 0,
		PR:   int(c.PTEL >> 5 & 3),
		D:    c.PTEL&(1<<9|1<<2|1<<1) != 0,
		SH:   c.PTEL&(1<<1|1<<0) != 0,
	}
	c.UTLB[c.urc&63] = e
	c.urc = (c.urc + 1) & 63
}

// ---------------------------------------------------------------------------
// Memory accessors used by instruction execution. Each returns
// ok = false after recording the fault in TEA and EXPEVT (through
// TranslateVA or the alignment check); the caller vectors the fault
// with enterMemFault. Word, longword, and quadword accesses must be
// naturally aligned; a misaligned access is an address error.

func (c *CPU) addrErr(va uint32, isWrite bool) {
	c.TEA = va
	if isWrite {
		c.EXPEVT = excAddrErrWrite
	} else {
		c.EXPEVT = excAddrErrRead
	}
}

func (c *CPU) read8(va uint32) (uint8, bool) {
	pa, ok := c.TranslateVA(va, false)
	if !ok {
		return 0, false
	}
	return c.mem.Read8(pa), true
}

func (c *CPU) read16(va uint32) (uint16, bool) {
	if va&1 != 0 {
		c.addrErr(va, false)
		return 0, false
	}
	pa, ok := c.TranslateVA(va, false)
	if !ok {
		return 0, false
	}
	return c.mem.Read16(pa), true
}

func (c *CPU) read32(va uint32) (uint32, bool) {
	if va&3 != 0 {
		c.addrErr(va, false)
		return 0, false
	}
	pa, ok := c.TranslateVA(va, false)
	if !ok {
		return 0, false
	}
	return c.mem.Read32(pa), true
}

// read64 reads a quadword as two longwords so that the alignment
// requirement is 4 bytes, matching how FMOV double transfers are
// used in practice.
func (c *CPU) read64(va uint32) (uint64, bool) {
	lo, ok := c.read32(va)
	if !ok {
		return 0, false
	}
	hi, ok := c.read32(va + 4)
	if !ok {
		return 0, false
	}
	return uint64(hi)<<32 | uint64(lo), true
}

func (c *CPU) write8(va uint32, v uint8) bool {
	pa, ok := c.TranslateVA(va, true)
	if !ok {
		return false
	}
	c.mem.Write8(pa, v)
	return true
}

func (c *CPU) write16(va uint32, v uint16) bool {
	if va&1 != 0 {
		c.addrErr(va, true)
		return false
	}
	pa, ok := c.TranslateVA(va, true)
	if !ok {
		return false
	}
	c.mem.Write16(pa, v)
	return true
}

func (c *CPU) write32(va uint32, v uint32) bool {
	if va&3 != 0 {
		c.addrErr(va, true)
		return false
	}
	pa, ok := c.TranslateVA(va, true)
	if !ok {
		return false
	}
	c.mem.Write32(pa, v)
	return true
}

func (c *CPU) write64(va uint32, v uint64) bool {
	if !c.write32(va, uint32(v)) {
		return false
	}
	return c.write32(va+4, uint32(v>>32))
}

// ---------------------------------------------------------------------------
// Serialization
//
// Fixed little-endian layout, SerializeSize bytes total:
//
//	0     R[16]                       64
//	64    Bank[8]                     32
//	96    PC,PR,SR,GBR,VBR,SSR,SPC,SGR,DBR   36
//	132   MACH,MACL                   8
//	140   FPUL,FPSCR                  8
//	148   FR[16]                      64
//	212   XFR[16]                     64
//	276   MMUCR,PTEH,PTEL,PTEA,TTB,TEA  24
//	300   EXPEVT,INTEVT,TRA,TTR       16
//	316   UTLB[64], 20 bytes each     1280
//	      (VPN, PPN, Size, PR, flag byte: bit0 V, bit1 D, bit2 SH)
//	1596  Halted byte + padding       4
//	1600  LDTLB replacement counter   4
//	1604  inDelay byte + padding      4
//	1608  afterDelay                  4

// SerializeSize is the exact length of a serialized CPU state.
const SerializeSize = 1612

// Serialize writes the full CPU state to a fresh byte slice of
// SerializeSize bytes.
func (c *CPU) Serialize() []byte {
	b := make([]byte, SerializeSize)
	put := func(off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	off := 0
	for _, v := range c.R {
		put(off, v)
		off += 4
	}
	for _, v := range c.Bank {
		put(off, v)
		off += 4
	}
	for _, v := range []uint32{c.PC, c.PR, c.SR, c.GBR, c.VBR, c.SSR, c.SPC, c.SGR, c.DBR} {
		put(off, v)
		off += 4
	}
	put(off, c.MACH)
	put(off+4, c.MACL)
	off += 8
	put(off, c.FPUL)
	put(off+4, c.FPSCR)
	off += 8
	for _, v := range c.FR {
		put(off, v)
		off += 4
	}
	for _, v := range c.XFR {
		put(off, v)
		off += 4
	}
	for _, v := range []uint32{c.MMUCR, c.PTEH, c.PTEL, c.PTEA, c.TTB, c.TEA} {
		put(off, v)
		off += 4
	}
	for _, v := range []uint32{c.EXPEVT, c.INTEVT, c.TRA, c.TTR} {
		put(off, v)
		off += 4
	}
	for i := range c.UTLB {
		e := &c.UTLB[i]
		put(off, e.VPN)
		put(off+4, e.PPN)
		put(off+8, uint32(e.Size))
		put(off+12, uint32(e.PR))
		var flags byte
		if e.V {
			flags |= 1
		}
		if e.D {
			flags |= 2
		}
		if e.SH {
			flags |= 4
		}
		b[off+16] = flags
		off += 20
	}
	if c.Halted {
		b[off] = 1
	}
	off += 4
	put(off, uint32(c.urc))
	off += 4
	if c.inDelay {
		b[off] = 1
	}
	off += 4
	put(off, c.afterDelay)
	return b
}

// Deserialize restores a state produced by Serialize. The length
// must match SerializeSize exactly; on a mismatch the CPU is left
// untouched and false is returned. Register banks and FPSCR are
// stored as active/inactive views, so they are restored verbatim
// with no bank swapping.
func (c *CPU) Deserialize(b []byte) bool {
	if len(b) != SerializeSize {
		return false
	}
	get := func(off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
	off := 0
	for i := range c.R {
		c.R[i] = get(off)
		off += 4
	}
	for i := range c.Bank {
		c.Bank[i] = get(off)
		off += 4
	}
	regs := []*uint32{&c.PC, &c.PR, &c.SR, &c.GBR, &c.VBR, &c.SSR, &c.SPC, &c.SGR, &c.DBR}
	for _, p := range regs {
		*p = get(off)
		off += 4
	}
	c.MACH = get(off)
	c.MACL = get(off + 4)
	off += 8
	c.FPUL = get(off)
	c.FPSCR = get(off + 4)
	off += 8
	for i := range c.FR {
		c.FR[i] = get(off)
		off += 4
	}
	for i := range c.XFR {
		c.XFR[i] = get(off)
		off += 4
	}
	mmu := []*uint32{&c.MMUCR, &c.PTEH, &c.PTEL, &c.PTEA, &c.TTB, &c.TEA}
	for _, p := range mmu {
		*p = get(off)
		off += 4
	}
	evt := []*uint32{&c.EXPEVT, &c.INTEVT, &c.TRA, &c.TTR}
	for _, p := range evt {
		*p = get(off)
		off += 4
	}
	for i := range c.UTLB {
		e := &c.UTLB[i]
		e.VPN = get(off)
		e.PPN = get(off + 4)
		e.Size = int(get(off + 8))
		e.PR = int(get(off + 12))
		flags := b[off+16]
		e.V = flags&1 != 0
		e.D = flags&2 != 0
		e.SH = flags&4 != 0
		off += 20
	}
	c.Halted = b[off] != 0
	off += 4
	c.urc = int(get(off))
	off += 4
	c.inDelay = b[off] != 0
	off += 4
	c.afterDelay = get(off)
	return true
}
