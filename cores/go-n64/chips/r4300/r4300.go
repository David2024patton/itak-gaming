// Package r4300 is an original implementation of the NEC
// VR4300 CPU (MIPS III with 64-bit extensions) used in the
// Nintendo 64, written from the published MIPS64 architecture
// manuals and the VR4300 hardware manual. It implements the
// full integer instruction set, the CP1 floating point unit's
// core operations, CP0 with a 32-entry TLB, exceptions with
// correct vector selection, and branch delay slots.
//
// The CPU talks to the machine through the Bus interface in
// physical addresses: virtual translation (TLB plus the KSEG
// direct maps) happens inside the CPU, exactly as on the
// hardware, and every load, store, and instruction fetch is
// checked for alignment, segment permission, and TLB state
// before the bus is touched.
//
// Cycle counting is a flat published-style model: one cycle
// per simple instruction, two for loads and stores, and flat
// class totals for multiply, divide, and floating point
// operations. The Count register advances at half the cycle
// rate, as on the hardware. The README lists the totals.
package r4300

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

// Bus is the physical-address memory interface the CPU uses.
// Sizes are in bytes (1, 2, 4, or 8) and transfers are
// big-endian: Read returns the value right-justified, Write
// stores the low size bytes of val.
type Bus interface {
	Read(addr uint32, size int) uint64
	Write(addr uint32, size int, val uint64)
}

// CP0 register indices.
const (
	Cp0Index    = 0
	Cp0Random   = 1
	Cp0EntryLo0 = 2
	Cp0EntryLo1 = 3
	Cp0Context  = 4
	Cp0PageMask = 5
	Cp0Wired    = 6
	Cp0BadVAddr = 8
	Cp0Count    = 9
	Cp0EntryHi  = 10
	Cp0Compare  = 11
	Cp0Status   = 12
	Cp0Cause    = 13
	Cp0EPC      = 14
	Cp0PRId     = 15
	Cp0Config   = 16
	Cp0LLAddr   = 17
	Cp0WatchLo  = 18
	Cp0WatchHi  = 19
	Cp0XContext = 20
	Cp0ECC      = 26
	Cp0CacheErr = 27
	Cp0TagLo    = 28
	Cp0TagHi    = 29
	Cp0ErrorEPC = 30
)

// Status register bits.
const (
	StatusIE  = 1 << 0
	StatusEXL = 1 << 1
	StatusERL = 1 << 2
	StatusKSU = 3 << 3
	StatusUX  = 1 << 5
	StatusSX  = 1 << 6
	StatusKX  = 1 << 7
	StatusIM  = 0xFF << 8
	StatusDE  = 1 << 16
	StatusCE  = 1 << 17
	StatusCH  = 1 << 18
	StatusSR  = 1 << 20
	StatusFR  = 1 << 21
	StatusBEV = 1 << 22
	StatusTS  = 1 << 23
	StatusITS = 1 << 24
	StatusRE  = 1 << 25
	StatusRP  = 1 << 27
	StatusCU0 = 1 << 28
	StatusCU1 = 1 << 29
	StatusCU2 = 1 << 30
	StatusCU3 = 1 << 31
)

// Cause register fields.
const (
	CauseExcCode = 0x1F << 2
	CauseIP      = 0xFF << 8
	CauseIPSW    = 0x03 << 8 // software-writable IP0/IP1
	CauseIV      = 1 << 23
	CauseCE      = 3 << 28
	CauseBD      = 1 << 31
)

// Exception codes (Cause ExcCode values).
const (
	ExcInterrupt = 0
	ExcTLBMod    = 1
	ExcTLBL      = 2
	ExcTLBS      = 3
	ExcAdEL      = 4
	ExcAdES      = 5
	ExcBusFetch  = 6
	ExcBusData   = 7
	ExcSyscall   = 8
	ExcBreak     = 9
	ExcReserved  = 10
	ExcCpU       = 11
	ExcOverflow  = 12
	ExcTrap      = 13
	ExcFPE       = 15
)

// TLBEntry is one decoded TLB entry. The raw CP0-format
// values are kept so TLBR can hand them back exactly.
type TLBEntry struct {
	PageMask uint64 // raw PageMask register value
	EntryHi  uint64 // raw EntryHi register value
	EntryLo0 uint64 // raw EntryLo0 register value
	EntryLo1 uint64 // raw EntryLo1 register value
}

// EntryLo bit fields.
const (
	entryLoG = 1 << 0
	entryLoV = 1 << 1
	entryLoD = 1 << 2
)

// Access kinds for translation.
const (
	accFetch = iota
	accRead
	accWrite
)

// Privilege modes.
const (
	modeKernel = iota
	modeSupervisor
	modeUser
)

// CPU is the VR4300 core. Registers are exported so tests and
// the system package can inspect and seed state.
type CPU struct {
	Reg [32]uint64
	HI  uint64
	LO  uint64
	PC  uint64 // address of the next instruction to execute
	NPC uint64 // address of the instruction after PC

	FPR  [32]uint64 // raw FPU register storage
	FCSR uint32     // FCR31: rounding mode, flags, enables, cause, conditions
	Cond [8]bool    // FPU condition bits (Cond[0] is FCC in FCSR bit 23)

	CP0 [32]uint64
	TLB [32]TLBEntry

	LLBit  bool
	LLAddr uint32

	Cycles uint64 // total cycles executed (flat model)

	bus       Bus
	delaySlot bool // the instruction at PC sits in a branch delay slot
	nullify   bool // next instruction is nullified (branch-likely not taken)
	countFrac uint64
	ipLine    uint8 // external interrupt lines driven by the machine (IP2..IP6)
	err       error
}

// New returns a CPU in its reset state, attached to bus.
func New(bus Bus) *CPU {
	c := &CPU{bus: bus}
	c.Reset()
	return c
}

// Reset applies the power-on state: the CPU starts at the
// reset vector in kernel mode with BEV and ERL set.
func (c *CPU) Reset() {
	for i := range c.Reg {
		c.Reg[i] = 0
	}
	c.HI, c.LO = 0, 0
	c.PC = 0xFFFFFFFFBFC00000
	c.NPC = c.PC + 4
	for i := range c.FPR {
		c.FPR[i] = 0
	}
	c.FCSR = 0
	for i := range c.Cond {
		c.Cond[i] = false
	}
	for i := range c.CP0 {
		c.CP0[i] = 0
	}
	// Power-on CP0 values per the VR4300 manual. Config's
	// cache-geometry fields are informational only; caches
	// are not modeled.
	c.CP0[Cp0Status] = StatusBEV | StatusERL | StatusCU0 | StatusRP
	c.CP0[Cp0PRId] = 0x00000B22
	c.CP0[Cp0Config] = 0x6E46303F
	c.CP0[Cp0Random] = 31
	for i := range c.TLB {
		c.TLB[i] = TLBEntry{}
	}
	c.LLBit = false
	c.LLAddr = 0
	c.Cycles = 0
	c.delaySlot = false
	c.nullify = false
	c.countFrac = 0
	c.err = nil
}

// SetBus attaches or replaces the memory bus.
func (c *CPU) SetBus(b Bus) { c.bus = b }

// SetInterruptLine drives the external interrupt input the
// machine connects to Cause IP2 (the RCP interrupt on the
// N64). Other IP bits are internal (software IP0/IP1 and the
// Count/Compare timer on IP7).
func (c *CPU) SetInterruptLine(active bool) {
	if active {
		c.ipLine |= 1 << 2
	} else {
		c.ipLine &^= 1 << 2
	}
}

// status is the CP0 Status register as a uint32.
func (c *CPU) status() uint32 { return uint32(c.CP0[Cp0Status]) }

// cause is the CP0 Cause register as a uint32.
func (c *CPU) cause() uint32 { return uint32(c.CP0[Cp0Cause]) }

// privMode reports the current privilege mode.
func (c *CPU) privMode() int {
	st := c.status()
	if st&(StatusEXL|StatusERL) != 0 {
		return modeKernel
	}
	switch (st & StatusKSU) >> 3 {
	case 0:
		return modeKernel
	case 1:
		return modeSupervisor
	default:
		return modeUser
	}
}

// seg64 reports whether 64-bit addressing is enabled for the
// current privilege mode (KX/SX/UX).
func (c *CPU) seg64() bool {
	st := c.status()
	switch c.privMode() {
	case modeKernel:
		return st&StatusKX != 0
	case modeSupervisor:
		return st&StatusSX != 0
	default:
		return st&StatusUX != 0
	}
}

// allow64Ops reports whether 64-bit integer instructions may
// execute. In kernel mode the full MIPS III operation set is
// available; supervisor and user modes require SX/UX. This
// matches how the N64's kernel (which runs with KX clear)
// uses 64-bit operations.
func (c *CPU) allow64Ops() bool {
	switch c.privMode() {
	case modeKernel:
		return true
	case modeSupervisor:
		return c.status()&StatusSX != 0
	default:
		return c.status()&StatusUX != 0
	}
}

// signExtend32 sign-extends a 32-bit value into 64 bits.
func signExtend32(v uint32) uint64 { return uint64(int64(int32(v))) }

// exc is an exception being raised by an operation.
type exc struct {
	code    int
	ce      int
	badAddr uint64
	refill  bool // TLB miss (no matching entry), selects the refill vector
	addrErr bool // BadVAddr/Context update applies
}

var errHalt = errors.New("r4300: halted")

// raiseException enters an exception for the instruction at
// instrAddr. inDelay reports whether that instruction sits in
// a branch delay slot.
func (c *CPU) raiseException(e exc, instrAddr uint64, inDelay bool) {
	st := c.status()
	cause := c.cause() &^ (CauseExcCode | CauseCE | CauseBD)
	cause |= uint32(e.code&0x1F) << 2
	cause |= uint32(e.ce&3) << 28
	if e.addrErr || e.code == ExcTLBL || e.code == ExcTLBS || e.code == ExcTLBMod {
		c.CP0[Cp0BadVAddr] = e.badAddr
		// Context and XContext record the faulting VPN2.
		ctx := (c.CP0[Cp0Context] &^ 0x7FFFF0) | (uint64((e.badAddr>>13)&0x7FFFF) << 4)
		c.CP0[Cp0Context] = ctx
		xctx := (c.CP0[Cp0XContext] &^ 0x1FFFFFFF0) | (uint64((e.badAddr>>13)&0x1FFFFFFF) << 4)
		c.CP0[Cp0XContext] = xctx
		hi := (c.CP0[Cp0EntryHi] & 0xFF) | (e.badAddr &^ 0x1FFF)
		c.CP0[Cp0EntryHi] = hi
	}
	erl := st&StatusERL != 0
	exl := st&StatusEXL != 0
	if erl {
		c.CP0[Cp0ErrorEPC] = instrAddr
		if inDelay {
			c.CP0[Cp0ErrorEPC] = instrAddr - 4
		}
	} else if !exl {
		epc := instrAddr
		if inDelay {
			epc = instrAddr - 4
			cause |= CauseBD
		}
		c.CP0[Cp0EPC] = epc
	}
	// When EPC is not updated (EXL or ERL already set), BD
	// keeps its old value too.
	if erl || exl {
		cause = (cause &^ CauseBD) | (c.cause() & CauseBD)
	}
	c.CP0[Cp0Cause] = uint64(cause)
	if !erl {
		c.CP0[Cp0Status] = uint64(st | StatusEXL)
	}
	c.LLBit = false

	base := uint64(0xFFFFFFFF80000000)
	if st&StatusBEV != 0 {
		base = 0xFFFFFFFFBFC00000
	}
	var off uint64 = 0x180
	if e.refill && !exl && !erl {
		if c.seg64() {
			off = 0x080 // XTLB refill
		} else {
			off = 0x000 // TLB refill
		}
	} else if e.code == ExcInterrupt && cause&CauseIV != 0 && st&StatusBEV == 0 {
		off = 0x200 // special interrupt vector
	}
	vec := base + off
	c.PC = vec
	c.NPC = vec + 4
	c.delaySlot = false
	c.nullify = false
}

// interruptPending reports whether an enabled interrupt is
// waiting (Cause IP against Status IM, IE set, EXL/ERL clear).
func (c *CPU) interruptPending() bool {
	st := c.status()
	if st&StatusIE == 0 || st&(StatusEXL|StatusERL) != 0 {
		return false
	}
	ip := uint32(c.cause()&CauseIP) | uint32(c.ipLine)<<8
	return ip&uint32(st&StatusIM) != 0
}

// tlbLookup translates through the TLB. On success it returns
// the physical address. On failure it returns the exception
// to raise.
func (c *CPU) tlbLookup(vaddr uint64, write bool, fetch bool) (uint32, *exc) {
	asid := uint8(c.CP0[Cp0EntryHi] & 0xFF)
	code := ExcTLBL
	if write {
		code = ExcTLBS
	}
	for i := range c.TLB {
		e := &c.TLB[i]
		mask := (e.PageMask >> 13) & 0xFFF
		shift := uint(bits.Len64(mask+1)) + 12 // log2(2*pageSize)
		if (vaddr >> shift) != (e.EntryHi >> shift) {
			continue
		}
		global := e.EntryLo0&entryLoG != 0 && e.EntryLo1&entryLoG != 0
		if !global && uint8(e.EntryHi&0xFF) != asid {
			continue
		}
		half := (vaddr >> (shift - 1)) & 1
		lo := e.EntryLo0
		if half == 1 {
			lo = e.EntryLo1
		}
		if lo&entryLoV == 0 {
			return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
		}
		if write && lo&entryLoD == 0 {
			return 0, &exc{code: ExcTLBMod, badAddr: vaddr, addrErr: true}
		}
		pageSize := (mask + 1) << 12
		pfn := (lo >> 6) & 0xFFFFFFFFF
		paddr := (pfn << 12) | (vaddr & (uint64(pageSize) - 1))
		return uint32(paddr), nil
	}
	return 0, &exc{code: code, badAddr: vaddr, refill: true, addrErr: true}
}

// translate converts a virtual address to a physical address
// for the given access kind, applying segment permissions.
func (c *CPU) translate(vaddr uint64, access int) (uint32, *exc) {
	write := access == accWrite
	code := ExcAdEL
	if write {
		code = ExcAdES
	}
	mode := c.privMode()
	if vaddr == signExtend32(uint32(vaddr)) {
		lo32 := uint32(vaddr)
		switch {
		case lo32 < 0x80000000: // kuseg: TLB mapped, all modes
			return c.tlbLookup(vaddr, write, access == accFetch)
		case lo32 < 0xA0000000: // kseg0: direct, kernel
			if mode != modeKernel {
				return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
			}
			return lo32 - 0x80000000, nil
		case lo32 < 0xC0000000: // kseg1: direct, kernel
			if mode != modeKernel {
				return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
			}
			return lo32 - 0xA0000000, nil
		case lo32 < 0xE0000000: // kseg2/ksseg: TLB, kernel or supervisor
			if mode == modeUser {
				return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
			}
			return c.tlbLookup(vaddr, write, access == accFetch)
		default: // kseg3: TLB, kernel
			if mode != modeKernel {
				return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
			}
			return c.tlbLookup(vaddr, write, access == accFetch)
		}
	}
	// True 64-bit segments require the mode's 64-bit enable.
	if !c.seg64() {
		return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
	}
	switch {
	case vaddr <= 0x0000FFFFFFFFFFFF: // xkuseg: TLB
		return c.tlbLookup(vaddr, write, access == accFetch)
	case vaddr >= 0x4000000000000000 && vaddr <= 0x4000FFFFFFFFFFFF: // xkseg: TLB, kernel
		if mode != modeKernel {
			return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
		}
		return c.tlbLookup(vaddr, write, access == accFetch)
	case vaddr >= 0x8000000000000000 && vaddr <= 0xBFFFFFFFFFFFFFFF: // xkphys: direct
		if mode != modeKernel {
			return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
		}
		return uint32(vaddr), nil
	}
	return 0, &exc{code: code, badAddr: vaddr, addrErr: true}
}

// readMem reads size bytes at a virtual address.
func (c *CPU) readMem(vaddr uint64, size int, instrAddr uint64, inDelay bool) (uint64, bool) {
	if size > 1 && vaddr&(uint64(size)-1) != 0 {
		c.raiseException(exc{code: ExcAdEL, badAddr: vaddr, addrErr: true}, instrAddr, inDelay)
		return 0, false
	}
	paddr, e := c.translate(vaddr, accRead)
	if e != nil {
		c.raiseException(*e, instrAddr, inDelay)
		return 0, false
	}
	return c.bus.Read(paddr, size), true
}

// writeMem writes size bytes at a virtual address.
func (c *CPU) writeMem(vaddr uint64, size int, val uint64, instrAddr uint64, inDelay bool) bool {
	if size > 1 && vaddr&(uint64(size)-1) != 0 {
		c.raiseException(exc{code: ExcAdES, badAddr: vaddr, addrErr: true}, instrAddr, inDelay)
		return false
	}
	paddr, e := c.translate(vaddr, accWrite)
	if e != nil {
		c.raiseException(*e, instrAddr, inDelay)
		return false
	}
	c.bus.Write(paddr, size, val)
	return true
}

// tick advances the Count/Compare timer by the cycles an
// instruction consumed and decrements Random.
func (c *CPU) tick(cycles int) {
	c.Cycles += uint64(cycles)
	c.countFrac += uint64(cycles)
	delta := c.countFrac / 2
	c.countFrac %= 2
	if delta > 0 {
		prev := uint64(uint32(c.CP0[Cp0Count]))
		next := prev + delta
		cmp := uint64(uint32(c.CP0[Cp0Compare]))
		c.CP0[Cp0Count] = uint64(uint32(next))
		if cmp != 0 && prev < cmp && next >= cmp {
			c.CP0[Cp0Cause] |= 1 << 15 // IP7 timer interrupt
		} else if cmp == 0 && uint32(next) == 0 && prev != 0 {
			// Compare of 0 matches on wrap.
		}
	}
	// Random decrements once per instruction (flat model),
	// wrapping between Wired and 31.
	r := int32(c.CP0[Cp0Random])
	wired := int32(c.CP0[Cp0Wired])
	r--
	if r < wired || r > 31 {
		r = 31
	}
	c.CP0[Cp0Random] = uint64(uint32(r))
}

// Step executes one instruction (or takes a pending
// interrupt) and returns the flat cycle count consumed.
func (c *CPU) Step() int {
	if c.nullify {
		// Branch-likely not taken: the delay slot instruction
		// is nullified, execution resumes after it.
		c.nullify = false
		c.PC = c.NPC
		c.NPC = c.PC + 4
		c.delaySlot = false
	}
	if c.interruptPending() {
		c.raiseException(exc{code: ExcInterrupt}, c.PC, c.delaySlot)
		c.tick(1)
		return 1
	}
	instrAddr := c.PC
	inDelay := c.delaySlot
	c.PC = c.NPC
	c.NPC = c.PC + 4
	c.delaySlot = false

	if instrAddr&3 != 0 {
		c.raiseException(exc{code: ExcAdEL, badAddr: instrAddr, addrErr: true}, instrAddr, inDelay)
		c.tick(1)
		return 1
	}
	paddr, e := c.translate(instrAddr, accFetch)
	if e != nil {
		c.raiseException(*e, instrAddr, inDelay)
		c.tick(1)
		return 1
	}
	instr := uint32(c.bus.Read(paddr, 4))
	cycles := c.execute(instr, instrAddr, inDelay)
	c.Reg[0] = 0
	c.tick(cycles)
	return cycles
}

// SerializeSize is the fixed byte size of a serialized CPU.
const SerializeSize = 32*8 + 8 + 8 + 8 + 8 + // GPRs, HI, LO, PC, NPC
	32*8 + 4 + 4 + // FPRs, FCSR, cond mask
	32*8 + // CP0
	32*4*8 + // TLB
	8 + 8 + 4 + 4 // Cycles, countFrac, LLAddr, flags

// Serialize writes the complete CPU state into dst, which
// must be at least SerializeSize bytes.
func (c *CPU) Serialize(dst []byte) int {
	w := &stateWriter{b: dst}
	for _, r := range c.Reg {
		w.u64(r)
	}
	w.u64(c.HI)
	w.u64(c.LO)
	w.u64(c.PC)
	w.u64(c.NPC)
	for _, r := range c.FPR {
		w.u64(r)
	}
	w.u32(c.FCSR)
	var cond uint32
	for i, b := range c.Cond {
		if b {
			cond |= 1 << uint(i)
		}
	}
	w.u32(cond)
	for _, r := range c.CP0 {
		w.u64(r)
	}
	for _, t := range c.TLB {
		w.u64(t.PageMask)
		w.u64(t.EntryHi)
		w.u64(t.EntryLo0)
		w.u64(t.EntryLo1)
	}
	w.u64(c.Cycles)
	w.u64(c.countFrac)
	w.u32(c.LLAddr)
	var flags uint32
	if c.LLBit {
		flags |= 1
	}
	if c.delaySlot {
		flags |= 2
	}
	if c.nullify {
		flags |= 4
	}
	w.u32(flags)
	return w.off
}

// Deserialize restores a CPU state written by Serialize.
func (c *CPU) Deserialize(src []byte) error {
	if len(src) < SerializeSize {
		return fmt.Errorf("r4300: state too short: %d", len(src))
	}
	r := &stateReader{b: src}
	for i := range c.Reg {
		c.Reg[i] = r.u64()
	}
	c.HI = r.u64()
	c.LO = r.u64()
	c.PC = r.u64()
	c.NPC = r.u64()
	for i := range c.FPR {
		c.FPR[i] = r.u64()
	}
	c.FCSR = r.u32()
	cond := r.u32()
	for i := range c.Cond {
		c.Cond[i] = cond&(1<<uint(i)) != 0
	}
	for i := range c.CP0 {
		c.CP0[i] = r.u64()
	}
	for i := range c.TLB {
		c.TLB[i].PageMask = r.u64()
		c.TLB[i].EntryHi = r.u64()
		c.TLB[i].EntryLo0 = r.u64()
		c.TLB[i].EntryLo1 = r.u64()
	}
	c.Cycles = r.u64()
	c.countFrac = r.u64()
	c.LLAddr = r.u32()
	flags := r.u32()
	c.LLBit = flags&1 != 0
	c.delaySlot = flags&2 != 0
	c.nullify = flags&4 != 0
	return r.err
}

type stateWriter struct {
	b   []byte
	off int
}

func (w *stateWriter) u64(v uint64) {
	binary.LittleEndian.PutUint64(w.b[w.off:], v)
	w.off += 8
}

func (w *stateWriter) u32(v uint32) {
	binary.LittleEndian.PutUint32(w.b[w.off:], v)
	w.off += 4
}

type stateReader struct {
	b   []byte
	off int
	err error
}

func (r *stateReader) u64() uint64 {
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

func (r *stateReader) u32() uint32 {
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}
