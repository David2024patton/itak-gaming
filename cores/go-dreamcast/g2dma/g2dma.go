// Package g2dma is the Dreamcast G2 bus DMA controller: the four
// channel register blocks at 0x005F7800 (AICA, Ext1, Ext2, Dev)
// from the published system documentation.
//
// The AICA channel is fully implemented: with the enable bit set
// and the CPU trigger selected (SB_ADTSEL = 0), writing 1 to
// SB_ADST copies SB_ADLEN bytes between system memory (SB_ADSTAR)
// and the AICA address space (SB_ADSTAG), direction per SB_ADDIR
// (0 = system memory to AICA space, 1 = AICA space to system
// memory). The copy runs through the physical bus, so AICA
// register addresses and wave RAM addresses both work as targets.
// Completion raises the documented ISTNRM AICA DMA end event
// (normal bank bit 15) and the AICA-side DMA end interrupt source.
// The transfer is synchronous in this model: SB_ADST never reads
// back busy for the AICA channel, and the readback counters show
// the final addresses with the length counter at 0.
//
// The Ext1, Ext2, and Dev channels address devices that are not
// present in this machine (modem, LAN adapter, expansion device).
// On hardware a DMA started toward an absent device never
// completes, so these channels model exactly that: their
// registers store, a start with the enable bit set raises the
// busy state forever, and no completion event ever fires.
// Clearing the enable bit drops the busy state in this model.
// Interrupt-triggered transfers (SB_ADTSEL values other than 0,
// whose trigger masks live in registers outside this window) are
// not modeled: a start write with a nonzero trigger select does
// nothing.
package g2dma

import "encoding/binary"

// Memory is the physical bus access the transfers run through.
type Memory interface {
	Read8(pa uint32) uint8
	Write8(pa uint32, v uint8)
}

// AICAEnd is the AICA-side DMA end notification.
type AICAEnd interface {
	NotifyG2DMAEnd()
}

// EventSink receives the Holly normal-bank AICA DMA done event.
type EventSink interface {
	RaiseNormal(bits uint32)
}

// evtAICADMADone is the ISTNRM bit for G2 AICA DMA completion.
const evtAICADMADone = 1 << 15

// Channel indices.
const (
	ChAICA = 0
	ChExt1 = 1
	ChExt2 = 2
	ChDev  = 3
)

// Per-channel register indices inside a channel block.
const (
	regSTAG = 0 // device-side start address
	regSTAR = 1 // system memory start address
	regLEN  = 2 // byte length
	regDIR  = 3 // direction
	regTSEL = 4 // trigger select
	regEN   = 5 // enable
	regST   = 6 // start / busy status
	regSUSP = 7 // suspend (stored only)
)

// SerializeSize is the exact serialized size: four channels of
// eight registers, four channels of three readback counters,
// the four timeout registers, the address-protection register,
// and one word of busy flags. 50 words in total.
const SerializeSize = (4*8 + 4*3 + 4 + 1 + 1) * 4

// Controller is the G2 DMA register file and transfer engine.
type Controller struct {
	mem  Memory
	aica AICAEnd
	sink EventSink
	regs [4][8]uint32 // channel registers (ST is not stored here)
	cnt  [4][3]uint32 // readback counters: STAGD, STARD, LEND
	busy [4]bool      // ext-channel busy state (AICA completes at once)
	glob [4]uint32    // SB_G2DSTO, SB_G2TRTO, SB_G2MDMTO, SB_G2MDMW
	apro uint32       // SB_G2APRO (write-only on hardware)
}

// New returns a controller moving data through mem, reporting
// AICA DMA completion to aica and raising ISTNRM events on sink.
func New(mem Memory, aica AICAEnd, sink EventSink) *Controller {
	return &Controller{mem: mem, aica: aica, sink: sink}
}

// Read32 reads one register by full physical address.
func (c *Controller) Read32(pa uint32) uint32 {
	off := pa - 0x005F7800
	switch {
	case off < 0x80: // channel blocks
		ch := int(off >> 5)
		r := int((off >> 2) & 7)
		if r == regST {
			if c.busy[ch] {
				return 1
			}
			return 0
		}
		return c.regs[ch][r]
	case off == 0x80: // SB_G2ID
		return 0
	case off >= 0x90 && off < 0xA0:
		return c.glob[(off-0x90)>>2]
	case off == 0xBC: // SB_G2APRO is write-only
		return 0
	case off >= 0xC0 && off < 0x100:
		rel := off - 0xC0
		return c.cnt[rel>>4][(rel>>2)&3]
	}
	return 0
}

// Write32 writes one register by full physical address.
func (c *Controller) Write32(pa uint32, v uint32) {
	off := pa - 0x005F7800
	switch {
	case off < 0x80:
		ch := int(off >> 5)
		r := int((off >> 2) & 7)
		switch r {
		case regST:
			c.start(ch, v)
		case regEN:
			c.regs[ch][regEN] = v
			if v&1 == 0 {
				c.busy[ch] = false
			}
		default:
			c.regs[ch][r] = v
		}
	case off >= 0x90 && off < 0xA0:
		c.glob[(off-0x90)>>2] = v
	case off == 0xBC:
		c.apro = v
	}
}

// start handles a write to a channel ST register.
func (c *Controller) start(ch int, v uint32) {
	if v&1 == 0 || c.regs[ch][regEN]&1 == 0 {
		return
	}
	if ch != ChAICA {
		// Absent device: the transfer never completes.
		c.busy[ch] = true
		return
	}
	if c.regs[ch][regTSEL] != 0 {
		// Interrupt-triggered AICA DMA is not modeled.
		return
	}
	c.runAICA()
}

// runAICA executes the AICA channel transfer synchronously.
func (c *Controller) runAICA() {
	stag := c.regs[ChAICA][regSTAG]
	star := c.regs[ChAICA][regSTAR]
	n := c.regs[ChAICA][regLEN]
	src, dst := star, stag
	if c.regs[ChAICA][regDIR]&1 == 1 {
		src, dst = stag, star
	}
	for i := uint32(0); i < n; i++ {
		c.mem.Write8(dst+i, c.mem.Read8(src+i))
	}
	c.cnt[ChAICA][0] = stag + n
	c.cnt[ChAICA][1] = star + n
	c.cnt[ChAICA][2] = 0
	if c.sink != nil {
		c.sink.RaiseNormal(evtAICADMADone)
	}
	if c.aica != nil {
		c.aica.NotifyG2DMAEnd()
	}
}

// Serialize writes the full register and counter state.
func (c *Controller) Serialize(dst []byte) int {
	if len(dst) < SerializeSize {
		return 0
	}
	off := 0
	put := func(v uint32) {
		binary.LittleEndian.PutUint32(dst[off:], v)
		off += 4
	}
	for ch := 0; ch < 4; ch++ {
		for r := 0; r < 8; r++ {
			put(c.regs[ch][r])
		}
	}
	for ch := 0; ch < 4; ch++ {
		for i := 0; i < 3; i++ {
			put(c.cnt[ch][i])
		}
	}
	for i := 0; i < 4; i++ {
		put(c.glob[i])
	}
	put(c.apro)
	var flags uint32
	for ch := 0; ch < 4; ch++ {
		if c.busy[ch] {
			flags |= 1 << ch
		}
	}
	put(flags)
	return off
}

// Deserialize restores a state written by Serialize.
func (c *Controller) Deserialize(src []byte) bool {
	if len(src) < SerializeSize {
		return false
	}
	off := 0
	get := func() uint32 {
		v := binary.LittleEndian.Uint32(src[off:])
		off += 4
		return v
	}
	for ch := 0; ch < 4; ch++ {
		for r := 0; r < 8; r++ {
			c.regs[ch][r] = get()
		}
	}
	for ch := 0; ch < 4; ch++ {
		for i := 0; i < 3; i++ {
			c.cnt[ch][i] = get()
		}
	}
	for i := 0; i < 4; i++ {
		c.glob[i] = get()
	}
	c.apro = get()
	flags := get()
	for ch := 0; ch < 4; ch++ {
		c.busy[ch] = flags&(1<<ch) != 0
	}
	return true
}
