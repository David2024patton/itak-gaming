// Package holly is the Dreamcast system interrupt controller: the
// SB_ISTNRM, SB_ISTERR, and SB_ISTEXT status registers and the
// SB_IML2LVL, SB_IML4LVL, and SB_IML6LVL per-source level mask
// registers at 0xA05F6900, per the published Holly documentation.
//
// Three source banks collect events: normal (ISTNRM), error
// (ISTERR), and external (ISTEXT). Each bank has one mask word per
// delivery level (2, 4, 6); a set source bit whose mask bit is set
// at any level makes that bank a candidate at the highest such
// level. The winning level across the banks is presented to the
// SH-4 as an IRL-style interrupt: level 6 arrives as IRL9 with
// INTEVT 0x320, level 4 as IRL11 with INTEVT 0x360, and level 2 as
// IRL13 with INTEVT 0x3A0, the documented external interrupt event
// codes.
//
// Acknowledgment rules: ISTNRM and ISTERR are write-1-to-clear (a
// handler acknowledges the sources it serviced by writing their
// bits back). ISTEXT bits are owned by the external devices and
// are cleared through ClearExternal, never by a register write.
// ISTNRM reads additionally carry two summary bits: bit 31 set
// while any ISTEXT bit is set, bit 30 set while any ISTERR bit is
// set.
package holly

import "encoding/binary"

// Source banks.
const (
	BankNormal   = 0 // SB_ISTNRM sources
	BankError    = 1 // SB_ISTERR sources
	BankExternal = 2 // SB_ISTEXT sources
)

// ASIC event bit indices in the normal bank, from the published
// ASIC event table (the current KallistiOS numbering, which
// matches the hardware SB_ISTNRM bit assignments).
const (
	EvtRenderDoneVideo = 0  // PVR render finished, video stage
	EvtRenderDoneISP   = 1  // PVR render finished, ISP stage
	EvtRenderDoneTSP   = 2  // PVR render finished, TSP stage (render done)
	EvtVBlankIn        = 3  // SPG vblank-in line reached
	EvtVBlankOut       = 4  // SPG vblank-out line reached
	EvtHBlank          = 5  // SPG hblank position reached
	EvtYUVDone         = 6  // TA YUV conversion finished
	EvtOpaqueDone      = 7  // TA opaque list transfer finished
	EvtOpaqueModDone   = 8  // TA opaque modifier list transfer finished
	EvtTransDone       = 9  // TA translucent list transfer finished
	EvtTransModDone    = 10 // TA translucent modifier list transfer finished
	EvtPVRDMADone      = 19 // PVR DMA finished (no DMA this slice)
	EvtPunchDone       = 21 // TA punchthrough list transfer finished
)

// ASIC event bit indices in the error bank (the published table
// numbers error events from 0x200; the bank bit is the event code
// minus 0x200).
const (
	ErrISPOutOfMemory   = 0 // ISP ran out of parameter memory
	ErrStripHalt        = 1 // ISP strip halt
	ErrParamOutOfMemory = 2 // parameter memory exhausted
	ErrOPBOutOfMemory   = 3 // object pointer buffer exhausted
	ErrTAInputError     = 4 // TA input error
	ErrTAInputOverflow  = 5 // TA FIFO overflow
)

// Register offsets inside the controller window at physical
// 0x005F6900.
const (
	regISTNRM = 0x00
	regISTERR = 0x04
	regISTEXT = 0x08
	regIML2   = 0x10 // mask row for level 2, one word per bank
	regIML4   = 0x20 // mask row for level 4
	regIML6   = 0x30 // mask row for level 6
)

// levels are the delivery levels indexed by mask row (row 0 is
// IML2LVL, row 1 is IML4LVL, row 2 is IML6LVL), and intevt maps a
// delivery level to the INTEVT code presented to the CPU.
var (
	levels = [3]int{2, 4, 6}
	intevt = map[int]uint32{2: 0x3A0, 4: 0x360, 6: 0x320}
)

// Sink is the CPU-side interrupt endpoint the controller drives.
// RaiseInterrupt latches a request at the given level;
// WithdrawInterrupt drops an unaccepted request whose sources have
// all been acknowledged.
type Sink interface {
	RaiseInterrupt(code uint32, level int)
	WithdrawInterrupt()
}

// SerializeSize is the exact length of a serialized controller:
// three status words and nine mask words.
const SerializeSize = 48

// Controller is the Holly interrupt controller state.
type Controller struct {
	status [3]uint32    // pending source bits per bank
	mask   [3][3]uint32 // mask[row][bank], rows are levels 2, 4, 6
	sink   Sink
}

// New returns a controller presenting interrupts to sink. All
// sources and masks start clear, so nothing is presented until
// software programs the IMLxLVL masks.
func New(sink Sink) *Controller {
	return &Controller{sink: sink}
}

// Raise sets source bits in a bank, as a device event does, and
// re-evaluates what is presented to the CPU.
func (c *Controller) Raise(bank int, bits uint32) {
	if bank < 0 || bank > 2 || bits == 0 {
		return
	}
	c.status[bank] |= bits
	c.update()
}

// ClearExternal clears ISTEXT source bits. External sources are
// acknowledged by their device, not by a write to the status
// register.
func (c *Controller) ClearExternal(bits uint32) {
	c.status[BankExternal] &^= bits
	c.update()
}

// Status returns the raw pending bits of a bank, for tests and
// debugging.
func (c *Controller) Status(bank int) uint32 {
	if bank < 0 || bank > 2 {
		return 0
	}
	return c.status[bank]
}

// Level returns the delivery level currently presented to the
// CPU (0 when no masked source is pending).
func (c *Controller) Level() int {
	win := 0
	for bank := 0; bank < 3; bank++ {
		for row := 0; row < 3; row++ {
			if c.status[bank]&c.mask[row][bank] != 0 && levels[row] > win {
				win = levels[row]
			}
		}
	}
	return win
}

// update re-presents the winning level to the CPU. The previous
// request is withdrawn first: the controller output is a level
// signal, so when the winning source set changes (a higher source
// acknowledged, leaving a lower one), the CPU must see the new
// winner, not the stale latch.
func (c *Controller) update() {
	if c.sink == nil {
		return
	}
	c.sink.WithdrawInterrupt()
	if lvl := c.Level(); lvl > 0 {
		c.sink.RaiseInterrupt(intevt[lvl], lvl)
	}
}

// Read32 reads one controller register by full physical address.
func (c *Controller) Read32(pa uint32) uint32 {
	switch pa & 0x3F {
	case regISTNRM:
		v := c.status[BankNormal]
		if c.status[BankExternal] != 0 {
			v |= 1 << 31 // external summary
		}
		if c.status[BankError] != 0 {
			v |= 1 << 30 // error summary
		}
		return v
	case regISTERR:
		return c.status[BankError]
	case regISTEXT:
		return c.status[BankExternal]
	}
	off := pa & 0x3F
	if row, ok := maskRow(off); ok {
		return c.mask[row][int((off&0xF)>>2)]
	}
	return 0
}

// Write32 writes one controller register by full physical address.
// ISTNRM and ISTERR are write-1-to-clear; ISTEXT writes are
// ignored; mask rows store plainly.
func (c *Controller) Write32(pa uint32, v uint32) {
	switch pa & 0x3F {
	case regISTNRM:
		// The summary bits are read-only; mask them out of the
		// clear value so a read-modify-write cannot clear by
		// accident through them (they carry no source bits).
		c.status[BankNormal] &^= v & 0x3FFFFFFF
		c.update()
	case regISTERR:
		c.status[BankError] &^= v
		c.update()
	case regISTEXT:
		// Owned by the external devices; writes do nothing.
	default:
		if row, ok := maskRow(pa & 0x3F); ok {
			c.mask[row][int((pa&0xF)>>2)] = v
			c.update()
		}
	}
}

// maskRow maps a register offset to a mask row index.
func maskRow(off uint32) (int, bool) {
	switch {
	case off >= regIML2 && off < regIML2+12:
		return 0, true
	case off >= regIML4 && off < regIML4+12:
		return 1, true
	case off >= regIML6 && off < regIML6+12:
		return 2, true
	}
	return 0, false
}

// Serialize writes the controller state: three status words then
// the nine mask words in row-major order.
func (c *Controller) Serialize() []byte {
	b := make([]byte, SerializeSize)
	for i := 0; i < 3; i++ {
		binary.LittleEndian.PutUint32(b[i*4:], c.status[i])
	}
	off := 12
	for row := 0; row < 3; row++ {
		for bank := 0; bank < 3; bank++ {
			binary.LittleEndian.PutUint32(b[off:], c.mask[row][bank])
			off += 4
		}
	}
	return b
}

// Deserialize restores a state written by Serialize and
// re-presents the winning level.
func (c *Controller) Deserialize(b []byte) bool {
	if len(b) != SerializeSize {
		return false
	}
	for i := 0; i < 3; i++ {
		c.status[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	off := 12
	for row := 0; row < 3; row++ {
		for bank := 0; bank < 3; bank++ {
			c.mask[row][bank] = binary.LittleEndian.Uint32(b[off:])
			off += 4
		}
	}
	c.update()
	return true
}
