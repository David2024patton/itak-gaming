// Package dma implements the PlayStation DMA
// controller from the psx-spx DMA documentation:
// seven channels (0 MDEC-in, 1 MDEC-out, 2 GPU,
// 3 CDROM, 4 SPU, 5 PIO, 6 OTC), the DPCR control
// register and the DICR interrupt register.
//
// Channel registers live at 0x1F801080 + n*0x10:
// MADR (base address), BCR (block control), CHCR
// (channel control). CHCR bits used here:
//
//	bit 0      transfer direction (0 = device to
//	           RAM, 1 = RAM to device)
//	bit 1      address step (0 = +4, 1 = -4)
//	bit 2      chopping enable
//	bits 9-10  sync mode (0 = all at once on start,
//	           1 = in blocks on request/trigger,
//	           2 = linked list)
//	bits 16-18 chopping DMA window size (2^n words)
//	bits 20-22 chopping CPU window size
//	bit 24     start/busy
//	bit 25     start/trigger for sync mode 1
//
// Transfers complete inside the call that starts
// them: this core has no DMA timing model (PS1-2
// states this in the core README). Chopping is
// honored structurally: a chopped transfer runs in
// bursts of the programmed DMA window size with the
// CPU windows in between, but because execution is
// immediate the moved data is identical; only the
// burst count is observable, through BurstCount.
//
// DICR follows the documentation precisely: channel
// IRQ flags (bits 24-30) are set on completion ONLY
// when the channel's IRQ enable (bits 16-22) is set;
// bit 15 is the bus error flag; bit 31 reads as
// b15 OR (master enable b23 AND any enabled flag);
// on the 0-to-1 transition of bit 31 the OnIRQ
// callback fires (the system latches I_STAT IRQ3).
// Flags are acknowledged by writing 1s to them.
package dma

// Channel indices in hardware order.
const (
	ChMDECIn  = 0
	ChMDECOut = 1
	ChGPU     = 2
	ChCDROM   = 3
	ChSPU     = 4
	ChPIO     = 5
	ChOTC     = 6

	Channels = 7
)

// Memory is the DMA engine's view of main memory
// (the system bus; DMA does not pass through CPU
// cache isolation).
type Memory interface {
	Read32(addr uint32) uint32
	Write32(addr uint32, v uint32)
}

// Devices are the channel endpoints. GP0Write feeds
// a word to the GPU command port and GPURead pulls
// a word from GPUREAD. CDReadWord pulls the next
// word from the CD data FIFO (ok=false when the
// FIFO cannot supply one). SPUReadWord/SPUWriteWord
// go through the SPU transfer port. MDECInWrite
// sinks a word into the MDEC stub and MDECOutRead
// sources one; the codec itself is PS1-3.
type Devices interface {
	GP0Write(w uint32)
	GPURead() uint32
	CDReadWord() (uint32, bool)
	SPUReadWord() uint32
	SPUWriteWord(w uint32)
	MDECInWrite(w uint32)
	MDECOutRead() uint32
}

// Channel holds one channel's registers.
type Channel struct {
	MADR uint32
	BCR  uint32
	CHCR uint32

	bursts int // bursts used by the last transfer
}

// Controller is the DMA controller.
type Controller struct {
	Ch   [Channels]Channel
	DPCR uint32

	dicrLow   uint32 // bits 0-14 (stored)
	forceIRQ  bool   // bit 15 (bus error flag storage name in docs: force/bus-error)
	irqEnable uint32 // bits 16-22
	masterEn  bool   // bit 23
	irqFlags  uint32 // bits 24-30
	irqLine   bool   // computed bit 31

	Mem Memory
	Dev Devices

	// OnIRQ fires on the 0-to-1 transition of the
	// DICR master flag (bit 31).
	OnIRQ func()
}

// New returns a controller in reset state: DPCR
// holds its documented reset value 0x07654321 and
// DICR is clear.
func New() *Controller {
	c := &Controller{}
	c.Reset()
	return c
}

// Reset restores the reset register values.
func (c *Controller) Reset() {
	c.Ch = [Channels]Channel{}
	c.DPCR = 0x07654321
	c.dicrLow = 0
	c.forceIRQ = false
	c.irqEnable = 0
	c.masterEn = false
	c.irqFlags = 0
	c.irqLine = false
}

// DICR returns the interrupt register as read by
// the CPU.
func (c *Controller) DICR() uint32 {
	v := c.dicrLow | (c.irqEnable << 16) | (c.irqFlags << 24)
	if c.forceIRQ {
		v |= 1 << 15
	}
	if c.masterEn {
		v |= 1 << 23
	}
	if c.irqLine {
		v |= 1 << 31
	}
	return v
}

// WriteDICR stores a CPU write to DICR: control
// bits are stored, flag bits written as 1 are
// acknowledged (cleared).
func (c *Controller) WriteDICR(v uint32) {
	c.dicrLow = v & 0x3FFF
	c.forceIRQ = v&(1<<15) != 0
	c.irqEnable = (v >> 16) & 0x7F
	c.masterEn = v&(1<<23) != 0
	ack := (v >> 24) & 0x7F
	c.irqFlags &^= ack
	c.updateLine()
}

func (c *Controller) updateLine() {
	line := c.forceIRQ || (c.masterEn && (c.irqFlags&c.irqEnable) != 0)
	if line && !c.irqLine && c.OnIRQ != nil {
		c.OnIRQ()
	}
	c.irqLine = line
}

// IRQLine returns the current DICR bit 31 value.
func (c *Controller) IRQLine() bool { return c.irqLine }

// ReadReg reads a DMA register by physical address
// (0x1F801080-0x1F8010FF).
func (c *Controller) ReadReg(addr uint32) uint32 {
	if addr == 0x1F8010F0 {
		return c.DPCR
	}
	if addr == 0x1F8010F4 {
		return c.DICR()
	}
	n, off := channelAddr(addr)
	if n < 0 {
		return 0
	}
	switch off {
	case 0:
		return c.Ch[n].MADR
	case 4:
		return c.Ch[n].BCR
	case 8:
		return c.Ch[n].CHCR
	}
	return 0
}

// WriteReg writes a DMA register; writing CHCR can
// start a transfer.
func (c *Controller) WriteReg(addr uint32, v uint32) {
	if addr == 0x1F8010F0 {
		c.DPCR = v
		return
	}
	if addr == 0x1F8010F4 {
		c.WriteDICR(v)
		return
	}
	n, off := channelAddr(addr)
	if n < 0 {
		return
	}
	switch off {
	case 0:
		c.Ch[n].MADR = v & 0x00FFFFFF
	case 4:
		c.Ch[n].BCR = v
	case 8:
		if n == ChOTC {
			// OTC CHCR has only bits 24, 28 and 30
			// writable; bit 1 (backward step) always
			// reads 1 and the rest read 0.
			c.Ch[n].CHCR = v & (1<<24 | 1<<28 | 1<<30)
		} else {
			c.Ch[n].CHCR = v
		}
		c.maybeStart(n)
	}
}

func channelAddr(addr uint32) (int, uint32) {
	if addr < 0x1F801080 || addr > 0x1F8010EC {
		return -1, 0
	}
	off := addr - 0x1F801080
	return int(off >> 4), off & 0xC
}

// channelEnabled reports the DPCR master enable for
// a channel (bit 3 of its nibble).
func (c *Controller) channelEnabled(n int) bool {
	return c.DPCR&(1<<uint(4*n+3)) != 0
}

// maybeStart runs a transfer if the CHCR write
// armed one and the channel is enabled in DPCR.
func (c *Controller) maybeStart(n int) {
	ch := &c.Ch[n]
	if ch.CHCR&(1<<24) == 0 || !c.channelEnabled(n) {
		return
	}
	sync := (ch.CHCR >> 9) & 3
	if n == ChOTC {
		c.runOTC(n)
		c.complete(n)
		return
	}
	switch sync {
	case 0:
		c.runBlock(n, c.sync0Words(n))
		c.complete(n)
	case 1:
		if ch.CHCR&(1<<25) == 0 {
			return // armed, waiting for the trigger bit
		}
		ch.CHCR &^= 1 << 25
		bs := int(ch.BCR & 0xFFFF)
		if bs == 0 {
			bs = 0x10000
		}
		bc := int(ch.BCR >> 16)
		if bc == 0 {
			bc = 1
		}
		c.runBlock(n, bs*bc)
		c.complete(n)
	case 2:
		if n == ChGPU {
			c.runLinkedList(n)
			c.complete(n)
		}
	}
}

// sync0Words is the sync mode 0 word count: the low
// halfword of BCR, with 0 meaning 0x10000.
func (c *Controller) sync0Words(n int) int {
	w := int(c.Ch[n].BCR & 0xFFFF)
	if w == 0 {
		w = 0x10000
	}
	return w
}

// complete finishes a channel: busy clears and the
// DICR flag is set if the channel IRQ is enabled
// (the documented rule: flags set only when
// enabled).
func (c *Controller) complete(n int) {
	c.Ch[n].CHCR &^= 1 << 24
	if c.irqEnable&(1<<uint(n)) != 0 {
		c.irqFlags |= 1 << uint(n)
	}
	c.updateLine()
}

// transferWord moves one word between memory at
// addr and channel n's device, honoring CHCR
// direction (bit 0: 1 = RAM to device).
func (c *Controller) transferWord(n int, addr uint32) {
	fromRAM := c.Ch[n].CHCR&1 != 0
	switch n {
	case ChGPU:
		if fromRAM {
			c.Dev.GP0Write(c.Mem.Read32(addr))
		} else {
			c.Mem.Write32(addr, c.Dev.GPURead())
		}
	case ChCDROM:
		if fromRAM {
			_ = c.Mem.Read32(addr) // CD has no data-in path in this subset; words are dropped
		} else {
			w, ok := c.Dev.CDReadWord()
			if !ok {
				w = 0 // FIFO dry: hardware would stall; we pad (see README)
			}
			c.Mem.Write32(addr, w)
		}
	case ChSPU:
		if fromRAM {
			c.Dev.SPUWriteWord(c.Mem.Read32(addr))
		} else {
			c.Mem.Write32(addr, c.Dev.SPUReadWord())
		}
	case ChMDECIn:
		if fromRAM {
			c.Dev.MDECInWrite(c.Mem.Read32(addr))
		}
	case ChMDECOut:
		if !fromRAM {
			c.Mem.Write32(addr, c.Dev.MDECOutRead())
		}
	case ChPIO:
		if fromRAM {
			_ = c.Mem.Read32(addr) // PIO unused: sink
		} else {
			c.Mem.Write32(addr, 0)
		}
	}
}

// runBlock moves words words for channel n from its
// MADR with the programmed address step, in bursts
// when chopping is enabled.
func (c *Controller) runBlock(n int, words int) {
	ch := &c.Ch[n]
	step := int32(4)
	if ch.CHCR&0x2 != 0 {
		step = -4
	}
	burst := words
	if ch.CHCR&0x4 != 0 {
		burst = 1 << ((ch.CHCR >> 16) & 7)
	}
	ch.bursts = 0
	addr := ch.MADR
	done := 0
	for done < words {
		nb := burst
		if nb > words-done {
			nb = words - done
		}
		for i := 0; i < nb; i++ {
			c.transferWord(n, addr)
			addr = uint32(int32(addr) + step)
			done++
		}
		ch.bursts++
	}
	ch.MADR = addr & 0x00FFFFFF
}

// runLinkedList executes a GPU linked list: each
// node starts with a header word (top byte = number
// of data words that follow, low 24 bits = address
// of the next node); the list ends when the next
// address is 0xFFFFFF. A generous node cap guards
// against corrupt lists looping forever.
func (c *Controller) runLinkedList(n int) {
	ch := &c.Ch[n]
	addr := ch.MADR
	for nodes := 0; nodes < (1 << 20); nodes++ {
		header := c.Mem.Read32(addr)
		count := int(header >> 24)
		next := header & 0x00FFFFFF
		for i := 0; i < count; i++ {
			addr += 4
			c.Dev.GP0Write(c.Mem.Read32(addr))
		}
		if next == 0x00FFFFFF {
			break
		}
		addr = next
	}
	ch.MADR = addr & 0x00FFFFFF
}

// runOTC builds the ordering table for channel 6:
// BCR holds the entry count; starting at MADR (the
// LAST entry) and stepping down, each entry receives
// the address of the previous entry, and the first
// (lowest) entry receives the terminator 0x00FFFFFF.
// This reproduces the documented DMA6 example table
// word for word.
func (c *Controller) runOTC(n int) {
	ch := &c.Ch[n]
	count := int(ch.BCR & 0xFFFF)
	if count == 0 {
		count = int(ch.BCR >> 16)
	}
	addr := ch.MADR
	for i := 0; i < count; i++ {
		var v uint32
		if i == count-1 {
			v = 0x00FFFFFF
		} else {
			v = (addr - 4) & 0x00FFFFFF
		}
		c.Mem.Write32(addr, v)
		addr -= 4
	}
	ch.MADR = addr & 0x00FFFFFF
}

// BurstCount reports how many bursts the channel's
// last transfer used (1 unless chopping split it).
func (c *Controller) BurstCount(n int) int { return c.Ch[n].bursts }

// StateSize is the serialized controller size.
const StateSize = Channels*16 + 16

// Serialize writes the controller state.
func (c *Controller) Serialize() []byte {
	out := make([]byte, StateSize)
	for i := range c.Ch {
		o := out[i*16:]
		put32(o[0:], c.Ch[i].MADR)
		put32(o[4:], c.Ch[i].BCR)
		put32(o[8:], c.Ch[i].CHCR)
	}
	o := out[Channels*16:]
	put32(o[0:], c.DPCR)
	put32(o[4:], c.DICR())
	return out
}

// Deserialize restores a state produced by Serialize.
func (c *Controller) Deserialize(in []byte) {
	for i := range c.Ch {
		o := in[i*16:]
		c.Ch[i].MADR = get32(o[0:])
		c.Ch[i].BCR = get32(o[4:])
		c.Ch[i].CHCR = get32(o[8:])
		c.Ch[i].bursts = 0
	}
	o := in[Channels*16:]
	c.DPCR = get32(o[0:])
	dicr := get32(o[4:])
	c.dicrLow = dicr & 0x3FFF
	c.forceIRQ = dicr&(1<<15) != 0
	c.irqEnable = (dicr >> 16) & 0x7F
	c.masterEn = dicr&(1<<23) != 0
	c.irqFlags = (dicr >> 24) & 0x7F
	c.irqLine = dicr&(1<<31) != 0
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func get32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
