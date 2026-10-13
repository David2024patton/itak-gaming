package periph

// DMA register offsets (relative to 0x04000000). Channel
// n occupies 12 bytes at 0xB0+0xC*n: source address word
// at +0, destination address word at +4, unit count
// halfword (CNT_L) at +8 and control halfword (CNT_H) at
// +0xA. Channels run 0xB0..0xDF overall.
const dmaBase = 0xB0

// DMA control (CNT_H) bits, in the terms used below.
const (
	dmaRepeat  = 1 << 9
	dmaIRQ     = 1 << 14
	dmaEnable  = 1 << 15
	dma32Bit   = 1 << 10
	dmaFIFOCha = 1 // first FIFO capable channel
	dmaFIFOChb = 2 // second FIFO capable channel
)

// dmaChan is one DMA channel: the registers as last
// written, plus the internal latches a transfer actually
// runs from. The latches load from the registers when the
// channel is enabled, and the count (and the destination,
// in reload mode) loads again when a repeat re-arms.
type dmaChan struct {
	sad   uint32 // source address register
	dad   uint32 // destination address register
	cntL  uint16 // unit count register, 0 means maximum
	cntH  uint16 // control register
	src   uint32 // internal source address
	dst   uint32 // internal destination address
	count uint32 // internal remaining units, 0 means idle
}

func (c *dmaChan) enabled() bool { return c.cntH&dmaEnable != 0 }

// timing returns the CNT_H start timing: 0 immediate,
// 1 VBlank, 2 HBlank, 3 special (FIFO for DMA1/DMA2).
func (c *dmaChan) timing() uint16 { return (c.cntH >> 12) & 3 }

// dstAdjust returns the CNT_H destination adjust mode:
// 0 increment, 1 decrement, 2 fixed, 3 increment with
// reload of the destination latch on repeat.
func (c *dmaChan) dstAdjust() uint16 { return (c.cntH >> 5) & 3 }

// srcAdjust returns the CNT_H source adjust mode:
// 0 increment, 1 decrement, 2 fixed. Mode 3 is prohibited
// on hardware and is treated as fixed here.
func (c *dmaChan) srcAdjust() uint16 { return (c.cntH >> 7) & 3 }

// effCount maps a CNT_L register value to the unit count
// it stands for: zero selects the channel maximum, 0x4000
// units for channels 0-2 and 0x10000 units for channel 3.
func effCount(ch int, cntL uint16) uint32 {
	if cntL != 0 {
		return uint32(cntL)
	}
	if ch == 3 {
		return 0x10000
	}
	return 0x4000
}

// adjustStep converts an adjust mode and unit size into
// the signed per unit address delta. Destination mode 3
// steps like increment during a transfer; its reload
// half lives in the repeat handling.
func adjustStep(mode uint16, size uint32) int32 {
	switch mode {
	case 0, 3:
		return int32(size)
	case 1:
		return -int32(size)
	}
	return 0
}

// FIFOSink receives sound FIFO samples from the special
// timing DMA channels. The audio hardware arrives in a
// later slice; the system wires a sink so DMA1 and DMA2
// can already deliver their bursts.
type FIFOSink interface {
	FIFOSample(channel int, value uint32)
}

// DMA is the four channel DMA controller. Transfers copy
// unit by unit through the Memory endpoint. When several
// channels are pending at one trigger, they run in
// channel order: channel 0 first, channel 3 last.
type DMA struct {
	mem  Memory
	irq  *IRQ
	sink FIFOSink
	ch   [4]dmaChan
}

// NewDMA builds a DMA controller bound to the transfer
// memory, the interrupt controller (for completion
// IRQs) and the FIFO sample sink (which may be nil, in
// which case FIFO bursts are dropped).
func NewDMA(mem Memory, irq *IRQ, sink FIFOSink) *DMA {
	return &DMA{mem: mem, irq: irq, sink: sink}
}

// loadLatches copies the registers of channel n into its
// internal address and count latches.
func (d *DMA) loadLatches(n int) {
	c := &d.ch[n]
	c.src = c.sad
	c.dst = c.dad
	c.count = effCount(n, c.cntL)
}

// byteAt returns the register byte at an IO offset in
// 0xB0..0xDF: the address words, then the CNT_L and CNT_H
// halfwords of the owning channel. CNT_L reads return the
// stored register value; on hardware the count register
// is write only and reads return open bus values, so
// returning the register is a deliberate simplification
// of this core. Address registers read back as written.
// Other offsets read as zero.
func (d *DMA) byteAt(addr uint32) byte {
	if addr < dmaBase || addr > dmaBase+0x2F {
		return 0
	}
	rel := addr - dmaBase
	c := &d.ch[rel/0xC]
	r := rel % 0xC
	switch {
	case r < 4:
		return byte(c.sad >> (8 * r))
	case r < 8:
		return byte(c.dad >> (8 * (r - 4)))
	case r < 10:
		return byte(c.cntL >> (8 * (r - 8)))
	default:
		return byte(c.cntH >> (8 * (r - 10)))
	}
}

// writeByte applies one byte of a register write. Only
// the CNT_H high byte carries the enable bit, so the
// enable transition check lives there: when a write sets
// enable on a disabled channel, the internal latches load
// from the registers, and an immediate timing channel
// runs its transfer synchronously inside the write.
func (d *DMA) writeByte(addr uint32, b byte) {
	if addr < dmaBase || addr > dmaBase+0x2F {
		return
	}
	rel := addr - dmaBase
	n := int(rel / 0xC)
	c := &d.ch[n]
	r := rel % 0xC
	switch {
	case r < 4:
		s := 8 * r
		c.sad = (c.sad &^ (0xFF << s)) | uint32(b)<<s
	case r < 8:
		s := 8 * (r - 4)
		c.dad = (c.dad &^ (0xFF << s)) | uint32(b)<<s
	case r < 10:
		if r == 8 {
			c.cntL = (c.cntL & 0xFF00) | uint16(b)
		} else {
			c.cntL = (c.cntL & 0x00FF) | uint16(b)<<8
		}
	case r == 10:
		c.cntH = (c.cntH & 0xFF00) | uint16(b)
	default: // r == 11, the CNT_H high byte
		was := c.enabled()
		c.cntH = (c.cntH & 0x00FF) | uint16(b)<<8
		if !was && c.enabled() {
			d.loadLatches(n)
			if c.timing() == 0 {
				d.runTransfer(n)
			}
		}
	}
}

// ReadReg reads size bytes at off (0xB0..0xDF), composed
// little endian from the register values as last written.
func (d *DMA) ReadReg(off uint32, size int) uint32 {
	if !validSize(size) {
		return 0
	}
	return compose(off, size, d.byteAt)
}

// WriteReg writes size bytes at off (0xB0..0xDF),
// decomposed little endian. The write that raises the
// enable bit on a channel loads its latches and, for
// immediate timing, performs the whole transfer before
// returning (see writeByte).
func (d *DMA) WriteReg(off uint32, size int, val uint32) {
	if !validSize(size) {
		return
	}
	decompose(off, size, val, d.writeByte)
}

// runTransfer performs the pending transfer of channel n
// from its internal latches, unit by unit, then handles
// completion: the completion IRQ is raised when CNT_H
// asks for it; a repeat channel reloads its count (and
// its destination latch when the destination adjust is
// the reload mode) and stays enabled for the next
// trigger, while a one shot channel has its enable bit
// cleared.
func (d *DMA) runTransfer(n int) {
	c := &d.ch[n]
	if d.mem == nil || c.count == 0 {
		return
	}
	size := uint32(2)
	if c.cntH&dma32Bit != 0 {
		size = 4
	}
	srcStep := adjustStep(c.srcAdjust(), size)
	dstStep := adjustStep(c.dstAdjust(), size)
	if c.srcAdjust() == 3 {
		srcStep = 0 // prohibited source mode acts as fixed
	}
	for c.count > 0 {
		v := d.mem.Read(c.src, int(size))
		d.mem.Write(c.dst, int(size), v)
		c.src = uint32(int32(c.src) + srcStep)
		c.dst = uint32(int32(c.dst) + dstStep)
		c.count--
	}
	if c.cntH&dmaIRQ != 0 && d.irq != nil {
		d.irq.Raise(uint16(IRQDMA0 << n))
	}
	if c.cntH&dmaRepeat != 0 {
		c.count = effCount(n, c.cntL)
		if c.dstAdjust() == 3 {
			c.dst = c.dad
		}
	} else {
		c.cntH &^= dmaEnable
	}
}

// trigger runs every enabled channel whose start timing
// is want and whose internal count is loaded, in channel
// order.
func (d *DMA) trigger(want uint16) {
	for n := range d.ch {
		c := &d.ch[n]
		if c.enabled() && c.timing() == want && c.count > 0 {
			d.runTransfer(n)
		}
	}
}

// OnVBlank delivers the vertical blank trigger: enabled
// channels with VBlank timing run their pending
// transfer.
func (d *DMA) OnVBlank() { d.trigger(1) }

// OnHBlank delivers the horizontal blank trigger:
// enabled channels with HBlank timing run their pending
// transfer.
func (d *DMA) OnHBlank() { d.trigger(2) }

// OnFIFO delivers one sound FIFO request for a channel.
// Only DMA1 and DMA2 have FIFO wiring, and only in
// special timing while enabled: each request moves
// exactly four 32 bit words from the internal source
// (advanced by the source adjust mode) to the sink. The
// destination is the fixed FIFO register, so it never
// moves; no completion IRQ fires per burst and the
// channel stays armed for the next request. Anything else
// (DMA0 or DMA3, a disabled channel, a channel in another
// timing) does nothing.
func (d *DMA) OnFIFO(channel int) {
	if channel != dmaFIFOCha && channel != dmaFIFOChb {
		return
	}
	c := &d.ch[channel]
	if !c.enabled() || c.timing() != 3 || d.mem == nil || d.sink == nil {
		return
	}
	step := adjustStep(c.srcAdjust(), 4)
	if c.srcAdjust() == 3 {
		step = 0
	}
	for i := 0; i < 4; i++ {
		v := d.mem.Read(c.src, 4)
		d.sink.FIFOSample(channel, v)
		c.src = uint32(int32(c.src) + step)
	}
}

// DMAStateSize is the serialized size of the DMA state:
// 24 bytes per channel times 4 channels.
//
// Layout, per channel at 24n: source register word at 0,
// destination register word at 4, CNT_L halfword at 8,
// CNT_H halfword at 10, internal source word at 12,
// internal destination word at 16, internal count word at
// 20, all little endian.
const DMAStateSize = 4 * 24

// Serialize writes the DMA state to dst, which must be
// at least DMAStateSize bytes, and returns the number of
// bytes written.
func (d *DMA) Serialize(dst []byte) int {
	for n := range d.ch {
		c := &d.ch[n]
		o := 24 * n
		put32(dst, o+0, c.sad)
		put32(dst, o+4, c.dad)
		put16(dst, o+8, c.cntL)
		put16(dst, o+10, c.cntH)
		put32(dst, o+12, c.src)
		put32(dst, o+16, c.dst)
		put32(dst, o+20, c.count)
	}
	return DMAStateSize
}

// Deserialize restores a state written by Serialize. It
// reports false, leaving the state untouched, when src is
// too short.
func (d *DMA) Deserialize(src []byte) bool {
	if len(src) < DMAStateSize {
		return false
	}
	for n := range d.ch {
		c := &d.ch[n]
		o := 24 * n
		c.sad = get32(src, o+0)
		c.dad = get32(src, o+4)
		c.cntL = get16(src, o+8)
		c.cntH = get16(src, o+10)
		c.src = get32(src, o+12)
		c.dst = get32(src, o+16)
		c.count = get32(src, o+20)
	}
	return true
}
