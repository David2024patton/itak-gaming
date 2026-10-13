// Package dsp models the GameCube audio DSP as its own
// processor: 8 KB of instruction RAM, a zero filled
// instruction ROM window, 8 KB of data RAM, a 16 MiB
// ARAM array owned by the block, the CPU mailbox pair,
// the AR DMA engine that moves bytes between ARAM and
// MEM1, and the documented GCDSP primary and extended
// opcode tables.
//
// HLE stance: this project ships no DSP ROM dump, so
// IROM (0x8000-0x8FFF) and DROM (0x8000-0x87FF) read as
// zeros and the reset PC is 0x0000 in IRAM. Microcode is
// loaded with UploadIRAM; on real hardware there is no
// upload port and programs arrive through the mailbox
// and DMA driven boot microcode that we do not have.
//
// Stack assignment follows the published register map:
// ST0 is the call stack, ST1 the data stack, ST2 the
// loop address stack, ST3 the loop counter stack. Each
// stack is 32 entries deep and wraps on overflow, a
// documented simplification of the hardware behaviour.
package dsp

import (
	"encoding/binary"
	"errors"
)

// Mem is physical MEM1 as the AR DMA engine sees it:
// byte access at zero based physical addresses.
type Mem interface {
	Read8(addr uint32) uint8
	Write8(addr uint32, v uint8)
}

// Memory sizes in words unless noted.
const (
	iramWords = 4096
	dramWords = 4096
	aramSize  = 16 << 20
)

// MMIO offsets inside the DSP window (16 bit lanes).
const (
	mmMBDSPHigh = 0x00
	mmMBDSPLow  = 0x02
	mmMBCPUHigh = 0x04
	mmMBCPULow  = 0x06
	mmControl   = 0x0C

	mmARMode    = 0x20
	mmARRefresh = 0x22
	mmARAddrH   = 0x24
	mmARAddrL   = 0x26
	mmARMemH    = 0x28
	mmARMemL    = 0x2A
	mmARCntH    = 0x2C
	mmARCntL    = 0x2E
)

// Control register bits.
const (
	ctlDSPRST    = 1 << 0
	ctlDSPHALT   = 1 << 1
	ctlDSPINT    = 1 << 2
	ctlDSPINTMSK = 1 << 3
	ctlAIDINT    = 1 << 4
	ctlAIDINTMSK = 1 << 5
	ctlARINT     = 1 << 6
	ctlARINTMSK  = 1 << 7
	ctlStatus    = ctlDSPINT | ctlAIDINT | ctlARINT // write 1 to clear
	ctlMasks     = ctlDSPINTMSK | ctlAIDINTMSK | ctlARINTMSK
)

// SR bits used by the core.
const (
	srCarry   = 0x0001
	srOver    = 0x0002
	srZero    = 0x0004
	srSign    = 0x0008
	srOverStk = 0x0010
	srTopBits = 0x0020
	srLogicZ  = 0x0040
	srIntEn   = 0x0200
	srMailEn  = 0x0800
	srAccMode = 0x1000 // 40 bit accumulator mode when set
)

// DSP is the audio digital signal processor.
type DSP struct {
	mem  Mem
	iram []uint16
	dram []uint16
	aram []byte

	pc   uint16
	sr   uint16
	cr   uint16
	prod int64 // 48 bit, sign extended
	acc  [2]int64
	ax   [2]int32
	ar   [4]uint16
	ix   [4]int16
	wr   [4]uint16

	stack [4][32]uint16
	sp    [4]uint8 // next write index, circular
	depth [4]uint8 // live entries, saturates at 32

	halted bool
	ctrl   uint16 // stored control bits: reset, status, masks

	// Mailbox state. cpuIn is CPU to DSP mail, cpuOut
	// is DSP to CPU mail.
	cpuInH     uint16
	cpuInL     uint16
	cpuPending bool
	dspOutH    uint16
	cpuOutH    uint16
	cpuOutL    uint16
	cpuReady   bool

	// Multiply configuration set by the config opcodes.
	mulUnsigned bool // SET15
	prodTimes2  bool // M2

	arRefresh uint16
	arAddrH   uint16
	arAddrL   uint16
	arMemH    uint16
	arMemL    uint16
	arCntH    uint16
	arCntL    uint16
}

// New returns a DSP held in reset (DSPRST set), reading
// and writing MEM1 through mem for AR DMA. mem may be
// nil, in which case DMA reads return zeros and DMA
// writes to MEM1 are dropped.
func New(mem Mem) *DSP {
	d := &DSP{
		mem:  mem,
		iram: make([]uint16, iramWords),
		dram: make([]uint16, dramWords),
		aram: make([]byte, aramSize),
	}
	d.Reset()
	return d
}

// Reset restores the power on state and sets DSPRST, so
// the core stays halted until the host releases it by
// writing CONTROL with bit0 clear. Memories (IRAM, DRAM,
// ARAM) keep their contents; registers, stacks, flags,
// mailbox and DMA state are cleared.
func (d *DSP) Reset() {
	mem := d.mem
	iram, dram, aram := d.iram, d.dram, d.aram
	*d = DSP{mem: mem, iram: iram, dram: dram, aram: aram}
	for i := range d.wr {
		d.wr[i] = 0xFFFF // wrap disabled
	}
	d.ctrl = ctlDSPRST
}

// UploadIRAM writes words into instruction RAM starting
// at word address addr. This is the harness upload path
// chosen for this core: real hardware has no such port
// and microcode normally arrives through boot code, but
// the system and the tests need a direct way in. Words
// that land outside IRAM are dropped (IROM is read only
// and reads as zeros).
func (d *DSP) UploadIRAM(addr uint16, words []uint16) {
	for i, w := range words {
		a := uint32(addr) + uint32(i)
		if a < iramWords {
			d.iram[a] = w
		}
	}
}

// ARAM returns the 16 MiB auxiliary RAM slice owned by
// the DSP block.
func (d *DSP) ARAM() []byte { return d.aram }

// IRQAsserted reports whether any interrupt status bit
// is both pending and unmasked in CONTROL.
func (d *DSP) IRQAsserted() bool {
	return (d.ctrl&ctlDSPINT != 0 && d.ctrl&ctlDSPINTMSK != 0) ||
		(d.ctrl&ctlAIDINT != 0 && d.ctrl&ctlAIDINTMSK != 0) ||
		(d.ctrl&ctlARINT != 0 && d.ctrl&ctlARINTMSK != 0)
}

// SetAIDInt mirrors the audio interface interrupt into
// the AIDINT status bit of CONTROL.
func (d *DSP) SetAIDInt(on bool) {
	if on {
		d.ctrl |= ctlAIDINT
	} else {
		d.ctrl &^= ctlAIDINT
	}
}

// Read16 reads one 16 bit MMIO lane at off inside the
// DSP window.
func (d *DSP) Read16(off uint32) uint16 {
	switch off {
	case mmMBDSPHigh:
		v := d.cpuInH & 0x7FFF
		if d.cpuPending {
			v |= 0x8000
		}
		return v
	case mmMBDSPLow:
		return d.cpuInL
	case mmMBCPUHigh:
		v := d.cpuOutH & 0x7FFF
		if d.cpuReady {
			v |= 0x8000
		}
		return v
	case mmMBCPULow:
		v := d.cpuOutL
		d.cpuReady = false
		return v
	case mmControl:
		v := d.ctrl
		if d.halted {
			v |= ctlDSPHALT
		}
		return v
	case mmARMode:
		return 0
	case mmARRefresh:
		return d.arRefresh
	case mmARAddrH:
		return d.arAddrH
	case mmARAddrL:
		return d.arAddrL
	case mmARMemH:
		return d.arMemH
	case mmARMemL:
		return d.arMemL
	case mmARCntH:
		return d.arCntH
	case mmARCntL:
		return d.arCntL
	}
	return 0
}

// Write16 writes one 16 bit MMIO lane at off.
func (d *DSP) Write16(off uint32, v uint16) {
	switch off {
	case mmMBDSPHigh:
		d.cpuInH = v & 0x7FFF
	case mmMBDSPLow:
		d.cpuInL = v
		d.cpuPending = true
	case mmMBCPUHigh, mmMBCPULow:
		// The CPU mailbox output lanes are read only
		// from the CPU side.
	case mmControl:
		d.writeControl(v)
	case mmARMode:
		// Read only, always zero on this core.
	case mmARRefresh:
		d.arRefresh = v
	case mmARAddrH:
		d.arAddrH = v
	case mmARAddrL:
		d.arAddrL = v
	case mmARMemH:
		d.arMemH = v
	case mmARMemL:
		d.arMemL = v
	case mmARCntH:
		d.arCntH = v
	case mmARCntL:
		d.arCntL = v
		d.runARDMA()
	}
}

// writeControl applies the CONTROL register write rules:
// status bits are write 1 to clear, mask bits are stored,
// DSPHALT clears when written 0, and a 0 to 1 transition
// of DSPRST performs the reset transition (PC, SR, stacks
// and flags cleared, memories kept).
func (d *DSP) writeControl(v uint16) {
	if v&ctlDSPHALT == 0 {
		d.halted = false
	}
	if v&ctlDSPRST != 0 && d.ctrl&ctlDSPRST == 0 {
		ctrl := d.ctrl
		d.Reset()
		// The same write still stores its mask bits.
		d.ctrl = ctlDSPRST | (v & ctlMasks)
		_ = ctrl
		return
	}
	d.ctrl = (d.ctrl &^ ctlMasks) | (v & ctlMasks)
	if v&ctlStatus != 0 {
		d.ctrl &^= v & ctlStatus
	}
	d.ctrl = (d.ctrl &^ ctlDSPRST) | (v & ctlDSPRST)
}

// runARDMA executes one AR DMA transfer atomically: the
// whole count moves before the call returns, ARINT is
// set on completion and the count registers read back
// zero. Real hardware moves the data over many cycles;
// the atomic model keeps the transfer indivisible with
// respect to DSP and CPU execution.
func (d *DSP) runARDMA() {
	aramAddr := (uint32(d.arAddrH)<<16 | uint32(d.arAddrL)) &^ 0x1F
	memAddr := (uint32(d.arMemH)<<16 | uint32(d.arMemL)) &^ 0x1F
	count := ((uint32(d.arCntH&0x7FFF) << 16) | uint32(d.arCntL)) &^ 0x1F
	toMEM := d.arCntH&0x8000 != 0
	d.arCntH, d.arCntL = 0, 0
	for i := uint32(0); i < count; i++ {
		off := int((aramAddr + i) % aramSize)
		if toMEM {
			if d.mem != nil {
				d.mem.Write8(memAddr+i, d.aram[off])
			}
		} else {
			var b byte
			if d.mem != nil {
				b = d.mem.Read8(memAddr + i)
			}
			d.aram[off] = b
		}
	}
	d.ctrl |= ctlARINT
}

// SerializeSize is the exact save state size: a version
// byte, the register file, the four stacks, mailbox and
// control state, the AR DMA registers, IRAM, DRAM and
// the full ARAM.
const SerializeSize = 1 +
	2 + 2 + 2 + 8 + 16 + 8 + 8 + 8 + 8 + // PC, SR, CR, PROD, ACC, AX, AR, IX, WR
	4*32*2 + 4 + 4 + // stacks, write indices, depths
	2 + 2 + 1 + 2 + 2 + 2 + 1 + // mailbox
	2 + 1 + 1 + 1 + // control, halted, multiply config
	2 + 2 + 2 + 2 + 2 + 2 + 2 + // AR refresh, address, memory, count lanes
	iramWords*2 + dramWords*2 + aramSize

const serializeVersion = 1

// ErrBadState reports a state block of the wrong size
// or version.
var ErrBadState = errors.New("dsp: bad save state")

// Serialize writes the complete DSP state.
func (d *DSP) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	off := 1
	put16 := func(v uint16) {
		binary.BigEndian.PutUint16(out[off:], v)
		off += 2
	}
	put64 := func(v uint64) {
		binary.BigEndian.PutUint64(out[off:], v)
		off += 8
	}
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(out[off:], v)
		off += 4
	}
	put16(d.pc)
	put16(d.sr)
	put16(d.cr)
	put64(uint64(d.prod))
	for _, a := range d.acc {
		put64(uint64(a))
	}
	for _, a := range d.ax {
		put32(uint32(a))
	}
	for _, v := range d.ar {
		put16(v)
	}
	for _, v := range d.ix {
		put16(uint16(v))
	}
	for _, v := range d.wr {
		put16(v)
	}
	for s := range d.stack {
		for _, v := range d.stack[s] {
			put16(v)
		}
	}
	for _, v := range d.sp {
		out[off] = v
		off++
	}
	for _, v := range d.depth {
		out[off] = v
		off++
	}
	put16(d.cpuInH)
	put16(d.cpuInL)
	if d.cpuPending {
		out[off] = 1
	}
	off++
	put16(d.dspOutH)
	put16(d.cpuOutH)
	put16(d.cpuOutL)
	if d.cpuReady {
		out[off] = 1
	}
	off++
	put16(d.ctrl)
	if d.halted {
		out[off] = 1
	}
	off++
	if d.mulUnsigned {
		out[off] = 1
	}
	off++
	if d.prodTimes2 {
		out[off] = 1
	}
	off++
	put16(d.arRefresh)
	put16(d.arAddrH)
	put16(d.arAddrL)
	put16(d.arMemH)
	put16(d.arMemL)
	put16(d.arCntH)
	put16(d.arCntL)
	for _, v := range d.iram {
		put16(v)
	}
	for _, v := range d.dram {
		put16(v)
	}
	off += copy(out[off:], d.aram)
	if off != SerializeSize {
		panic("dsp: serialize size mismatch")
	}
	return out
}

// Deserialize restores a state written by Serialize.
func (d *DSP) Deserialize(in []byte) error {
	if len(in) != SerializeSize || in[0] != serializeVersion {
		return ErrBadState
	}
	off := 1
	get16 := func() uint16 {
		v := binary.BigEndian.Uint16(in[off:])
		off += 2
		return v
	}
	get64 := func() uint64 {
		v := binary.BigEndian.Uint64(in[off:])
		off += 8
		return v
	}
	get32 := func() uint32 {
		v := binary.BigEndian.Uint32(in[off:])
		off += 4
		return v
	}
	d.pc = get16()
	d.sr = get16()
	d.cr = get16()
	d.prod = int64(get64())
	for i := range d.acc {
		d.acc[i] = int64(get64())
	}
	for i := range d.ax {
		d.ax[i] = int32(get32())
	}
	for i := range d.ar {
		d.ar[i] = get16()
	}
	for i := range d.ix {
		d.ix[i] = int16(get16())
	}
	for i := range d.wr {
		d.wr[i] = get16()
	}
	for s := range d.stack {
		for i := range d.stack[s] {
			d.stack[s][i] = get16()
		}
	}
	for i := range d.sp {
		d.sp[i] = in[off]
		off++
	}
	for i := range d.depth {
		d.depth[i] = in[off]
		off++
	}
	d.cpuInH = get16()
	d.cpuInL = get16()
	d.cpuPending = in[off] != 0
	off++
	d.dspOutH = get16()
	d.cpuOutH = get16()
	d.cpuOutL = get16()
	d.cpuReady = in[off] != 0
	off++
	d.ctrl = get16()
	d.halted = in[off] != 0
	off++
	d.mulUnsigned = in[off] != 0
	off++
	d.prodTimes2 = in[off] != 0
	off++
	d.arRefresh = get16()
	d.arAddrH = get16()
	d.arAddrL = get16()
	d.arMemH = get16()
	d.arMemL = get16()
	d.arCntH = get16()
	d.arCntL = get16()
	for i := range d.iram {
		d.iram[i] = get16()
	}
	for i := range d.dram {
		d.dram[i] = get16()
	}
	copy(d.aram, in[off:])
	return nil
}
