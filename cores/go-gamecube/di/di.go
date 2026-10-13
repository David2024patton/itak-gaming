// Package di models the GameCube disc interface: the
// status, cover, command buffer, configuration, DMA
// address and length, and control registers, plus the
// small synchronous command set games issue through the
// command buffers.
//
// The MMIO window base is 0x0C006000. All access here is
// through 16 bit lanes at offsets inside that window: a
// 32 bit register occupies two lanes, the high half at
// the even offset and the low half at the next offset.
// Side effects trigger on the low lane write.
//
// Commands run instantly and synchronously when TSTART
// is written: a simplification of the real drive, which
// seeks and reads over time, documented here because
// results (data in memory, TCINT, DILENGTH at zero) are
// already final when the DICR write returns.
package di

import (
	"encoding/binary"
	"errors"
)

// Source is the inserted disc as the drive sees it.
type Source interface {
	// Read copies disc bytes at byte offset off into
	// dst and returns how many bytes were copied: a
	// short count, or zero, past the end of the disc.
	Read(off uint32, dst []byte) int
	// Header returns the disc identity fields used by
	// the Identify command.
	Header() (sysID byte, game, company string, discNo, version byte)
}

// Mem is physical MEM1 as the DI sees it, for DMA.
type Mem interface {
	Read8(addr uint32) uint8
	Write8(addr uint32, v uint8)
}

// DISR bits.
const (
	disrBRK       = 1 << 0
	disrDEINT     = 1 << 1 // drive error interrupt, write 1 to clear
	disrDEINTMSK  = 1 << 2
	disrTCINT     = 1 << 3 // transfer complete interrupt, write 1 to clear
	disrTCINTMSK  = 1 << 4
	disrBRKINT    = 1 << 5 // break interrupt, write 1 to clear
	disrBRKINTMSK = 1 << 6
	disrW1C       = disrDEINT | disrTCINT | disrBRKINT
)

// DICVR bits.
const (
	dicvrCVR       = 1 << 0 // cover open, set by SetCover only
	dicvrCVRINT    = 1 << 1 // cover interrupt, write 1 to clear
	dicvrCVRINTMSK = 1 << 2
)

// DICR bits.
const (
	dicrTSTART = 1 << 0 // write 1 to execute, always reads 0
	dicrDMA    = 1 << 1
	dicrRW     = 1 << 2 // set means a write command
)

// Register offsets inside the MMIO window.
const (
	regDISR    = 0x00
	regDICVR   = 0x04
	regCMDBUF0 = 0x08
	regCMDBUF1 = 0x0C
	regCMDBUF2 = 0x10
	regDICFG   = 0x14
	regDIMAR   = 0x18
	regDILEN   = 0x1C
	regDICR    = 0x20

	readbackSize     = 32
	serializeVersion = 1
)

// DI is the disc interface device.
type DI struct {
	src Source
	mem Mem

	disr  uint32
	dicvr uint32 // the CVR bit mirrors coverOpen
	cmd   [3]uint32
	dicfg uint32
	dimar uint32
	dilen uint32 // DMA bytes remaining, zero when done
	dicr  uint32 // TSTART is never stored, so it reads 0

	coverOpen bool
	stopped   bool   // set by the stop motor command
	audioCfg  uint32 // last audio family command word
	readback  [readbackSize]byte
	readLen   uint32 // valid bytes in readback
}

// New returns a disc interface in its reset state,
// backed by the disc src (nil means no disc inserted)
// and MEM1 mem for DMA transfers.
func New(src Source, mem Mem) *DI {
	d := &DI{src: src, mem: mem}
	d.Reset()
	return d
}

// Reset restores the power on state: every register
// zero except DICFG, which resets to 1, the cover
// closed, the motor running and no audio configuration.
func (d *DI) Reset() {
	src, mem := d.src, d.mem
	*d = DI{src: src, mem: mem, dicfg: 1}
}

// SetCover records the cover state. CVR follows it, and
// any change raises CVRINT; repeating the same state
// changes nothing.
func (d *DI) SetCover(open bool) {
	if open == d.coverOpen {
		return
	}
	d.coverOpen = open
	if open {
		d.dicvr |= dicvrCVR
	} else {
		d.dicvr &^= dicvrCVR
	}
	d.dicvr |= dicvrCVRINT
}

// IRQAsserted reports whether any interrupt source is
// both pending and unmasked.
func (d *DI) IRQAsserted() bool {
	return (d.disr&disrDEINT != 0 && d.disr&disrDEINTMSK != 0) ||
		(d.disr&disrTCINT != 0 && d.disr&disrTCINTMSK != 0) ||
		(d.disr&disrBRKINT != 0 && d.disr&disrBRKINTMSK != 0) ||
		(d.dicvr&dicvrCVRINT != 0 && d.dicvr&dicvrCVRINTMSK != 0)
}

// Stopped reports whether the stop motor command has
// stopped the drive motor.
func (d *DI) Stopped() bool { return d.stopped }

// AudioConfig returns the last audio family command
// word (top byte 0xE0, 0xE1 or 0xE2), or zero.
func (d *DI) AudioConfig() uint32 { return d.audioCfg }

// reg returns the register at a 32 bit base offset.
func (d *DI) reg(off uint32) uint32 {
	switch off {
	case regDISR:
		return d.disr
	case regDICVR:
		return d.dicvr
	case regCMDBUF0, regCMDBUF1, regCMDBUF2:
		return d.cmd[(off-regCMDBUF0)/4]
	case regDICFG:
		return d.dicfg
	case regDIMAR:
		return d.dimar
	case regDILEN:
		return d.dilen
	case regDICR:
		return d.dicr
	}
	return 0
}

// Read16 reads one 16 bit MMIO lane at off: the lane
// at the even offset of a register pair is its high
// half, the next lane its low half.
func (d *DI) Read16(off uint32) uint16 {
	v := d.reg(off &^ 2)
	if off&2 == 0 {
		return uint16(v >> 16)
	}
	return uint16(v)
}

// Write16 writes one 16 bit MMIO lane at off.
func (d *DI) Write16(off uint32, v uint16) {
	switch off {
	case regDISR:
		d.disr = d.disr&0x0000FFFF | uint32(v)<<16
	case regDISR + 2:
		// Interrupt bits are write 1 to clear; the rest
		// of the low half is stored as written.
		low := (uint32(v) &^ disrW1C) | (d.disr & disrW1C &^ uint32(v))
		d.disr = d.disr&0xFFFF0000 | low
	case regDICVR:
		d.dicvr = d.dicvr&0x0000FFFF | uint32(v)<<16
	case regDICVR + 2:
		// CVR is hardware state and ignores writes;
		// CVRINT is write 1 to clear.
		low := (uint32(v) &^ (dicvrCVR | dicvrCVRINT)) |
			(d.dicvr & dicvrCVR) |
			(d.dicvr & dicvrCVRINT &^ uint32(v))
		d.dicvr = d.dicvr&0xFFFF0000 | low
	case regCMDBUF0, regCMDBUF0 + 2, regCMDBUF1, regCMDBUF1 + 2, regCMDBUF2, regCMDBUF2 + 2:
		setLane(&d.cmd[(off&^2-regCMDBUF0)/4], off&2 != 0, v)
	case regDICFG, regDICFG + 2:
		setLane(&d.dicfg, off&2 != 0, v)
	case regDIMAR, regDIMAR + 2:
		setLane(&d.dimar, off&2 != 0, v)
	case regDILEN, regDILEN + 2:
		setLane(&d.dilen, off&2 != 0, v)
	case regDICR:
		d.dicr = d.dicr&0x0000FFFF | uint32(v)<<16
	case regDICR + 2:
		d.dicr = d.dicr&0xFFFF0000 | uint32(v)&^dicrTSTART
		if v&dicrTSTART != 0 {
			d.execute()
		}
	}
}

// setLane merges one 16 bit lane into a 32 bit register:
// low is true for the lane at the odd half offset.
func setLane(reg *uint32, low bool, v uint16) {
	if low {
		*reg = *reg&0xFFFF0000 | uint32(v)
	} else {
		*reg = *reg&0x0000FFFF | uint32(v)<<16
	}
}

// execute runs the command in the command buffers. See
// the package comment for the synchronous model.
func (d *DI) execute() {
	cmd0 := d.cmd[0]
	switch {
	case cmd0>>24 == 0xA8 && cmd0&0xFF == 0:
		d.cmdRead()
	case cmd0>>24 == 0xAD:
		d.cmdIdentify()
	case cmd0>>24 == 0xE3:
		// Stop motor: DEINT is the documented response
		// path for drive commands.
		d.stopped = true
		d.disr |= disrDEINT
	case cmd0>>24 == 0xE0 || cmd0>>24 == 0xE1 || cmd0>>24 == 0xE2:
		d.audioCfg = cmd0
		d.disr |= disrDEINT
	default:
		d.disr |= disrDEINT
	}
}

// dmaWrite copies data into MEM1 at DIMAR with its low
// 5 bits ignored, as DMA transfers require.
func (d *DI) dmaWrite(data []byte) {
	if d.mem == nil {
		return
	}
	base := d.dimar &^ 0x1F
	for i, b := range data {
		d.mem.Write8(base+uint32(i), b)
	}
}

// cmdRead runs the read command: CMDBUF1 shifted left
// by 2 is the disc offset and CMDBUF2 is the length.
func (d *DI) cmdRead() {
	if d.dicr&dicrRW != 0 {
		// A write command to a read only drive is an
		// error.
		d.disr |= disrDEINT
		return
	}
	if d.src == nil {
		d.disr |= disrDEINT
		return
	}
	off := d.cmd[1] << 2
	length := d.cmd[2]
	if d.dicr&dicrDMA == 0 {
		// Non DMA reads land in a small internal
		// readback buffer and leave the command buffers
		// unchanged, a documented simplification: games
		// use DMA reads.
		n := length
		if n > readbackSize {
			n = readbackSize
		}
		d.readback = [readbackSize]byte{}
		d.readLen = n
		got := d.src.Read(off, d.readback[:n])
		_ = got
		d.disr |= disrTCINT
		return
	}
	d.dilen = length
	buf := make([]byte, 4096)
	done := uint32(0)
	for done < length {
		chunk := length - done
		if chunk > uint32(len(buf)) {
			chunk = uint32(len(buf))
		}
		n := d.src.Read(off+done, buf[:chunk])
		if n < 0 {
			n = 0
		}
		if n > int(chunk) {
			n = int(chunk)
		}
		// A short read past the end of the disc pads
		// with zeros so the transfer still completes.
		for i := n; i < int(chunk); i++ {
			buf[i] = 0
		}
		if d.mem != nil {
			base := (d.dimar &^ 0x1F) + done
			for i := uint32(0); i < chunk; i++ {
				d.mem.Write8(base+i, buf[i])
			}
		}
		done += chunk
		d.dilen -= chunk
	}
	d.disr |= disrTCINT
}

// cmdIdentify runs the identify command, which builds
// the 32 byte drive info block from the disc header:
// bytes 0 to 3 zero, the game code at bytes 4 to 5, the
// company code at bytes 6 to 7, the disc number at byte
// 8, the version at byte 9, the system ID at byte 10
// and zeros elsewhere.
func (d *DI) cmdIdentify() {
	if d.src == nil {
		d.disr |= disrDEINT
		return
	}
	sysID, game, company, discNo, version := d.src.Header()
	var block [readbackSize]byte
	copy(block[4:6], game)
	copy(block[6:8], company)
	block[8] = discNo
	block[9] = version
	block[10] = sysID
	// The transfer length: the preset DILENGTH when one
	// was loaded, else CMDBUF2 when nonzero, else the
	// full 32 byte block, and never past the block.
	n := uint32(readbackSize)
	switch {
	case d.dilen != 0 && d.dilen < n:
		n = d.dilen
	case d.dilen == 0 && d.cmd[2] != 0 && d.cmd[2] < n:
		n = d.cmd[2]
	}
	if d.dicr&dicrDMA != 0 {
		d.dmaWrite(block[:n])
		switch {
		case d.dilen != 0:
			d.dilen -= n
		case d.cmd[2] != 0:
			d.dilen = d.cmd[2] - n
		}
	} else {
		d.readback = block
		d.readLen = n
	}
	d.disr |= disrTCINT
}

// SerializeSize is the fixed save state size: a version
// byte, the ten 32 bit registers, the drive flags and
// the readback buffer.
const SerializeSize = 1 +
	10*4 + // DISR, DICVR, CMDBUF0 to 2, DICFG, DIMAR, DILENGTH, DICR, audio config
	1 + 1 + // cover open, stopped
	4 + // readback length
	readbackSize

// ErrBadState reports a state block of the wrong size
// or version.
var ErrBadState = errors.New("di: bad save state")

// Serialize writes the complete DI state.
func (d *DI) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	off := 1
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(out[off:], v)
		off += 4
	}
	put32(d.disr)
	put32(d.dicvr)
	put32(d.cmd[0])
	put32(d.cmd[1])
	put32(d.cmd[2])
	put32(d.dicfg)
	put32(d.dimar)
	put32(d.dilen)
	put32(d.dicr)
	put32(d.audioCfg)
	if d.coverOpen {
		out[off] = 1
	}
	off++
	if d.stopped {
		out[off] = 1
	}
	off++
	put32(d.readLen)
	off += copy(out[off:], d.readback[:])
	if off != SerializeSize {
		panic("di: serialize size mismatch")
	}
	return out
}

// Deserialize restores a state written by Serialize.
func (d *DI) Deserialize(in []byte) error {
	if len(in) != SerializeSize || in[0] != serializeVersion {
		return ErrBadState
	}
	off := 1
	get32 := func() uint32 {
		v := binary.BigEndian.Uint32(in[off:])
		off += 4
		return v
	}
	d.disr = get32()
	d.dicvr = get32()
	d.cmd[0] = get32()
	d.cmd[1] = get32()
	d.cmd[2] = get32()
	d.dicfg = get32()
	d.dimar = get32()
	d.dilen = get32()
	d.dicr = get32()
	d.audioCfg = get32()
	d.coverOpen = in[off] != 0
	off++
	d.stopped = in[off] != 0
	off++
	d.readLen = get32()
	if d.readLen > readbackSize {
		return ErrBadState
	}
	copy(d.readback[:], in[off:])
	return nil
}
