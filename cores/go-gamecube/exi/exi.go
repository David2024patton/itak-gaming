// Package exi models the GameCube external interface (EXI),
// the serial bus at MMIO window base 0x0C006800, written from
// published EXI documentation. Three channels each carry a
// five-register block; transfers run synchronously when
// software writes TSTART in a channel control register.
//
// Devices modeled: the IPL ROM window on channel 0 device 0
// (reads return zero, this project ships no IPL bytes), the
// SRAM and RTC chip on channel 0 device 2, memory cards in
// slot A (channel 0 device 1) and slot B (channel 1
// device 1), and the AD16 chip on channel 2 device 0.
// Memory card images are persistent media held outside the
// save state; the system keeps them through CardImage.
package exi

import (
	"encoding/binary"
	"errors"
)

// Mem is the physical MEM1 interface used for EXI DMA
// transfers. Addresses are physical, zero based.
type Mem interface {
	Read8(addr uint32) uint8
	Write8(addr uint32, v uint8)
}

// Register offsets inside one channel block. Blocks sit at
// stride 0x14: channel bases 0x00, 0x14, 0x28.
const (
	regCSR    = 0x00
	regMAR    = 0x04
	regLength = 0x08
	regCR     = 0x0C
	regData   = 0x10
)

// CSR bits.
const (
	csrEXIINTMSK = 1 << 0
	csrEXIINT    = 1 << 1 // status, write 1 to clear
	csrTCINTMSK  = 1 << 2
	csrTCINT     = 1 << 3 // status, write 1 to clear
	csrROMDIS    = 1 << 13
)

// CR bits.
const (
	crTSTART = 1 << 0
	crDMA    = 1 << 1
)

// cpuHz is the Gekko clock the RTC counts against.
const cpuHz = 486_000_000

type chanRegs struct {
	csr    uint32
	mar    uint32
	length uint32
	cr     uint32
	data   uint32
}

type cardMedia struct {
	img   []byte
	awake bool
}

type devKind uint8

const (
	devIPL devKind = iota + 1
	devSRAM
	devCard
	devAD16
)

// Card session phases.
const (
	cardCmd = iota
	cardAddr
	cardReadData
	cardWriteData
	cardOutput
	cardSink
)

// session is the byte stream state of one selected device.
// It resets whenever the channel chip select moves away
// from the device or onto it.
type session struct {
	kind devKind
	card *cardMedia

	// SRAM and RTC session state.
	cmd    [4]byte
	cmdN   int
	sink   bool
	data   bool // command latched, data phase
	addr   uint32
	rtcBuf [4]byte

	// Memory card session state.
	phase    int
	cardCmdB byte
	addrN    int
	cardAddr uint32
	pageAddr uint32
	pageBuf  [512]byte
	pageN    int
	out      [2]byte
	outN     int
	outIdx   int

	// AD16 session state.
	adIdx int
}

// EXI is the external interface state.
type EXI struct {
	mem  Mem
	regs [3]chanRegs

	sram     [64]byte
	rtcBase  uint32
	rtcSecs  uint32 // whole seconds elapsed since the base was set
	rtcRem   uint32 // sub-second remainder, in CPU cycles
	cards    [2]*cardMedia
	sessions [3][3]*session
}

// New returns an EXI in its reset state. Both card slots
// start with a 16 Mbit (2 MiB) blank image.
func New(mem Mem) *EXI {
	e := &EXI{mem: mem}
	for i := range e.cards {
		img := make([]byte, 2<<20)
		for j := range img {
			img[j] = 0xFF
		}
		e.cards[i] = &cardMedia{img: img}
	}
	e.Reset()
	return e
}

// Reset clears the channel registers, the SRAM, the RTC
// counters and every device session, and puts the cards
// back to sleep. Card image contents are persistent media
// and survive a reset.
func (e *EXI) Reset() {
	e.regs = [3]chanRegs{}
	e.sram = [64]byte{}
	e.rtcBase, e.rtcSecs, e.rtcRem = 0, 0, 0
	for _, c := range e.cards {
		if c != nil {
			c.awake = false
		}
	}
	for ch := 0; ch < 3; ch++ {
		for d := 0; d < 3; d++ {
			if s := e.makeSession(ch, d); s != nil {
				e.sessions[ch][d] = s
			}
		}
	}
}

func (e *EXI) makeSession(ch, d int) *session {
	switch {
	case ch == 0 && d == 0:
		return &session{kind: devIPL}
	case ch == 0 && d == 2:
		return &session{kind: devSRAM}
	case ch == 0 && d == 1:
		return &session{kind: devCard, card: e.cards[0]}
	case ch == 1 && d == 1:
		return &session{kind: devCard, card: e.cards[1]}
	case ch == 2 && d == 0:
		return &session{kind: devAD16}
	}
	return nil
}

func (e *EXI) resetSession(ch, d int) {
	if s := e.makeSession(ch, d); s != nil {
		// A fresh session drops any partial card page
		// buffer, which is exactly the discard rule.
		e.sessions[ch][d] = s
	}
}

// csDev returns the device ordinal selected by the CSR CS
// bits (bit 7 plus d selects device d), or -1.
func csDev(csr uint32) int {
	for d := 0; d < 3; d++ {
		if csr&(1<<uint(7+d)) != 0 {
			return d
		}
	}
	return -1
}

// Read16 reads one 16-bit lane of a channel register.
// 32-bit registers occupy two lanes; the high lane comes
// first.
func (e *EXI) Read16(off uint32) uint16 {
	v, ok := e.reg32(off)
	if !ok {
		return 0
	}
	if off&2 == 0 {
		return uint16(v >> 16)
	}
	return uint16(v)
}

func (e *EXI) reg32(off uint32) (uint32, bool) {
	if off >= 0x3C {
		return 0, false
	}
	ch := off / 0x14
	within := off % 0x14
	r := &e.regs[ch]
	switch within &^ 3 {
	case regCSR:
		return r.csr, true
	case regMAR:
		return r.mar, true
	case regLength:
		return r.length, true
	case regCR:
		return r.cr, true
	case regData:
		return r.data, true
	}
	return 0, false
}

// Write16 writes one 16-bit lane of a channel register.
// Side effects (chip select changes, transfer start) fire
// on the low lane write, once the full register value is
// assembled.
func (e *EXI) Write16(off uint32, v uint16) {
	if off >= 0x3C {
		return
	}
	ch := int(off / 0x14)
	within := off % 0x14
	r := &e.regs[ch]
	var target *uint32
	switch within &^ 3 {
	case regCSR:
		target = &r.csr
	case regMAR:
		target = &r.mar
	case regLength:
		target = &r.length
	case regCR:
		target = &r.cr
	case regData:
		target = &r.data
	default:
		return
	}
	if off&2 == 0 {
		*target = *target&0x0000FFFF | uint32(v)<<16
		return
	}
	oldCSR := r.csr
	*target = *target&0xFFFF0000 | uint32(v)
	switch within &^ 3 {
	case regCSR:
		e.applyCSR(ch, oldCSR, v)
	case regCR:
		if r.cr&crTSTART != 0 {
			e.runTransfer(ch)
		}
	}
}

// applyCSR folds a CSR low lane write into the stored CSR:
// mask, clock and chip select bits are stored, the two
// status bits are write 1 to clear, and a chip select
// change resets the device sessions it touches. The caller
// passes the CSR value from before the write assembled the
// new low lane: the lane write itself overwrites the
// status bits, so they can only be preserved (and the old
// chip select can only be known) from that saved value.
func (e *EXI) applyCSR(ch int, old uint32, writtenLow uint16) {
	r := &e.regs[ch]
	// Start from the old value and take only stored fields
	// from the newly assembled one.
	stored := uint32(csrEXIINTMSK | csrTCINTMSK | 0x7<<4 | 0x7<<7)
	if ch == 0 {
		stored |= csrROMDIS
	}
	next := old&^stored | r.csr&stored
	if writtenLow&csrEXIINT != 0 {
		next &^= csrEXIINT
	}
	if writtenLow&csrTCINT != 0 {
		next &^= csrTCINT
	}
	r.csr = next
	oldDev, newDev := csDev(old), csDev(next)
	if oldDev != newDev {
		if oldDev >= 0 {
			e.resetSession(ch, oldDev)
		}
		if newDev >= 0 {
			e.resetSession(ch, newDev)
		}
	}
}

// IRQAsserted reports the EXI interrupt output: any
// channel with a pending, unmasked transfer-complete or
// device interrupt.
func (e *EXI) IRQAsserted() bool {
	for i := range e.regs {
		csr := e.regs[i].csr
		if csr&csrTCINT != 0 && csr&csrTCINTMSK != 0 {
			return true
		}
		if csr&csrEXIINT != 0 && csr&csrEXIINTMSK != 0 {
			return true
		}
	}
	return false
}

var tlenBytes = [4]int{1, 2, 4, 4}

// runTransfer executes one channel transfer synchronously
// and sets TCINT. In immediate mode the byte stream lives
// in the high bytes of the DATA register: for a one byte
// transfer the byte is DATA shifted right 24, for two bytes
// the top two bytes, for four bytes the whole word. In DMA
// mode the stream is LENGTH bytes of MEM1 at MAR. A read
// transfer clocks filler bytes out and captures the device
// bytes; with no device selected the captured bytes are
// all 0xFF and writes have no effect.
func (e *EXI) runTransfer(ch int) {
	r := &e.regs[ch]
	cr := r.cr
	dma := cr&crDMA != 0
	rw := (cr >> 2) & 3
	isRead := rw == 0 || rw == 3
	n := 0
	if dma {
		n = int(r.length)
	} else {
		n = tlenBytes[(cr>>4)&3]
	}
	var sess *session
	if d := csDev(r.csr); d >= 0 {
		sess = e.sessions[ch][d]
	}
	if isRead {
		in := make([]byte, n)
		for i := range in {
			if sess != nil {
				in[i] = e.devRead(sess)
			} else {
				in[i] = 0xFF
			}
		}
		if dma {
			if e.mem != nil {
				for i, b := range in {
					e.mem.Write8(r.mar+uint32(i), b)
				}
			}
		} else {
			var packed uint32
			for i, b := range in {
				packed |= uint32(b) << uint(24-8*i)
			}
			r.data = packed
		}
	} else {
		out := make([]byte, n)
		if dma {
			if e.mem != nil {
				for i := range out {
					out[i] = e.mem.Read8(r.mar + uint32(i))
				}
			}
		} else {
			for i := range out {
				out[i] = byte(r.data >> uint(24-8*i))
			}
		}
		if sess != nil {
			for _, b := range out {
				e.devWrite(sess, b)
			}
		}
	}
	r.cr &^= crTSTART
	r.csr |= csrTCINT
}

func (e *EXI) devWrite(s *session, b byte) {
	switch s.kind {
	case devIPL, devAD16:
		// Writes are swallowed.
	case devSRAM:
		e.sramWrite(s, b)
	case devCard:
		cardWrite(s, b)
	}
}

func (e *EXI) devRead(s *session) byte {
	switch s.kind {
	case devIPL:
		return 0x00
	case devAD16:
		v := byte(0x04)
		if s.adIdx&1 == 1 {
			v = 0x12
		}
		s.adIdx++
		return v
	case devSRAM:
		return e.sramRead(s)
	case devCard:
		return cardRead(s)
	}
	return 0xFF
}

// SRAM and RTC chip protocol: the first four bytes written
// after selection are the command (0x20, address high,
// address low, ignored). After that, written bytes store
// and read bytes source at the latched address, which
// auto-increments. A bad command byte sinks the session:
// bytes are dropped and reads return zero.
func (e *EXI) sramWrite(s *session, b byte) {
	if s.sink {
		return
	}
	if !s.data {
		s.cmd[s.cmdN] = b
		s.cmdN++
		if s.cmdN == 4 {
			if s.cmd[0] != 0x20 {
				s.sink = true
				return
			}
			s.addr = uint32(s.cmd[1])<<8 | uint32(s.cmd[2])
			s.data = true
		}
		return
	}
	addr := s.addr
	s.addr++
	switch {
	case addr < 0x40:
		e.sram[addr] = b
	case addr >= 0x100 && addr < 0x104:
		pos := addr - 0x100
		s.rtcBuf[pos] = b
		if pos == 3 {
			e.rtcBase = binary.BigEndian.Uint32(s.rtcBuf[:])
			e.rtcSecs, e.rtcRem = 0, 0
		}
	}
}

func (e *EXI) sramRead(s *session) byte {
	if s.sink || !s.data {
		return 0
	}
	addr := s.addr
	s.addr++
	switch {
	case addr < 0x40:
		return e.sram[addr]
	case addr >= 0x100 && addr < 0x104:
		v := e.rtcBase + e.rtcSecs
		return byte(v >> uint(24-8*(addr-0x100)))
	}
	return 0
}

// SRAM returns the live 64-byte SRAM contents.
func (e *EXI) SRAM() []byte { return e.sram[:] }

// Advance moves the RTC forward by the given number of
// 486 MHz CPU cycles, keeping the sub-second remainder.
func (e *EXI) Advance(cpuCycles int) {
	if cpuCycles <= 0 {
		return
	}
	total := int64(e.rtcRem) + int64(cpuCycles)
	e.rtcSecs += uint32(total / cpuHz)
	e.rtcRem = uint32(total % cpuHz)
}

// SetRTC sets the RTC base counter (seconds since the
// GameCube epoch, 2000-01-01) and clears elapsed time.
func (e *EXI) SetRTC(seconds uint32) {
	e.rtcBase = seconds
	e.rtcSecs, e.rtcRem = 0, 0
}

// RTC returns the current RTC counter value.
func (e *EXI) RTC() uint32 { return e.rtcBase + e.rtcSecs }

// CardImage returns the live backing image of a card slot
// (0 is slot A, 1 is slot B). The image is persistent
// media: the system saves and restores it outside the
// save state.
func (e *EXI) CardImage(slot int) []byte {
	if slot < 0 || slot > 1 || e.cards[slot] == nil {
		return nil
	}
	return e.cards[slot].img
}

// SetCardSizeMBits resizes a card image. Allowed sizes are
// 4, 8, 16, 32 and 64 Mbit. Call before the card is used;
// the new image is blank (all 0xFF) beyond any prefix that
// fits from the old one.
func (e *EXI) SetCardSizeMBits(slot, mbits int) {
	if slot < 0 || slot > 1 {
		return
	}
	switch mbits {
	case 4, 8, 16, 32, 64:
	default:
		return
	}
	img := make([]byte, mbits<<17) // Mbit to bytes: mbits * 2^17
	for i := range img {
		img[i] = 0xFF
	}
	copy(img, e.cards[slot].img)
	e.cards[slot].img = img
}

func cardSizeMbits(c *cardMedia) byte {
	return byte(len(c.img) * 8 >> 20)
}

// Memory card byte protocol. The card starts asleep: only
// the ID command (and the status read) work until the ID
// has been read, which wakes the card. While asleep, other
// write commands sink the session and reads return 0xFF.
// The first byte written after selection is the command.
func cardWrite(s *session, b byte) {
	c := s.card
	switch s.phase {
	case cardSink:
		return
	case cardCmd:
		if !c.awake && b != 0x00 && b != 0x81 {
			s.phase = cardSink
			return
		}
		switch b {
		case 0x00: // read ID; reading it wakes the card
			c.awake = true
			s.out = [2]byte{cardSizeMbits(c), 0x00}
			s.outN, s.outIdx = 2, 0
			s.phase = cardOutput
		case 0x81: // read status
			s.out[0] = 0x00
			if c.awake {
				s.out[0] = 0x40
			}
			s.outN, s.outIdx = 1, 0
			s.phase = cardOutput
		case 0x83: // clear status: no data, no effect
		case 0x52, 0xF2, 0x89: // read, write page, erase sector
			s.cardCmdB = b
			s.cardAddr, s.addrN = 0, 0
			s.phase = cardAddr
		default:
			s.phase = cardSink
		}
	case cardAddr:
		s.cardAddr = s.cardAddr<<8 | uint32(b)
		s.addrN++
		if s.addrN < 4 {
			return
		}
		switch s.cardCmdB {
		case 0x89:
			base := s.cardAddr &^ 0x1FFF // 8 KiB sector
			for i := uint32(0); i < 0x2000 && base+i < uint32(len(c.img)); i++ {
				c.img[base+i] = 0xFF
			}
			s.phase = cardCmd
		case 0x52:
			s.phase = cardReadData
		case 0xF2:
			s.pageAddr = s.cardAddr &^ 511 // pages are 512 aligned
			s.pageN = 0
			s.phase = cardWriteData
		}
	case cardWriteData:
		s.pageBuf[s.pageN] = b
		s.pageN++
		if s.pageN == 512 {
			copy(c.img[s.pageAddr:], s.pageBuf[:])
			s.pageN = 0
			s.pageAddr += 512
			if s.pageAddr >= uint32(len(c.img)) {
				s.pageAddr = 0
			}
		}
	default:
		// Bytes written during a read or output phase are
		// ignored.
	}
}

func cardRead(s *session) byte {
	c := s.card
	switch s.phase {
	case cardOutput:
		if s.outIdx < s.outN {
			v := s.out[s.outIdx]
			s.outIdx++
			return v
		}
		return 0xFF
	case cardReadData:
		v := c.img[s.cardAddr%uint32(len(c.img))]
		s.cardAddr++
		return v
	}
	return 0xFF
}

// SerializeSize is the fixed save state size: a version
// byte, the three channel register blocks, the SRAM, and
// the RTC base, elapsed seconds and sub-second remainder.
// Card images are not part of the state.
const SerializeSize = 1 + 3*5*4 + 64 + 4 + 4 + 4

// ErrBadState reports a state block of the wrong size or
// version.
var ErrBadState = errors.New("exi: bad save state")

const serializeVersion = 1

// Serialize writes the EXI state covered by SerializeSize.
func (e *EXI) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	off := 1
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(out[off:], v)
		off += 4
	}
	for i := range e.regs {
		r := &e.regs[i]
		put32(r.csr)
		put32(r.mar)
		put32(r.length)
		put32(r.cr)
		put32(r.data)
	}
	off += copy(out[off:], e.sram[:])
	put32(e.rtcBase)
	put32(e.rtcSecs)
	put32(e.rtcRem)
	if off != SerializeSize {
		panic("exi: serialize size mismatch")
	}
	return out
}

// Deserialize restores a state written by Serialize.
// Device sessions restart fresh and the cards go back to
// sleep; card images are untouched.
func (e *EXI) Deserialize(in []byte) error {
	if len(in) != SerializeSize || in[0] != serializeVersion {
		return ErrBadState
	}
	off := 1
	get32 := func() uint32 {
		v := binary.BigEndian.Uint32(in[off:])
		off += 4
		return v
	}
	for i := range e.regs {
		r := &e.regs[i]
		r.csr = get32()
		r.mar = get32()
		r.length = get32()
		r.cr = get32()
		r.data = get32()
	}
	off += copy(e.sram[:], in[off:])
	e.rtcBase = get32()
	e.rtcSecs = get32()
	e.rtcRem = get32()
	for _, c := range e.cards {
		if c != nil {
			c.awake = false
		}
	}
	for ch := 0; ch < 3; ch++ {
		for d := 0; d < 3; d++ {
			e.resetSession(ch, d)
		}
	}
	return nil
}
