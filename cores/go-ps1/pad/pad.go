// Package pad implements the PlayStation controller and memory card
// ports (the JOY registers at 1F801040h to 1F80104Fh), built from the
// psx-spx "Controllers and Memory Cards" chapters.
//
// Serial model simplification: real hardware clocks one bit at a time
// at the baud rate set in JOY_BAUD, and the /ACK pulse and IRQ7 arrive
// roughly 100 cycles after a byte completes. This package simplifies
// that to immediate byte completion: a byte written to JOY_DATA is
// transferred at once, the response byte is placed in the RX FIFO
// immediately, JOY_STAT reports TX ready at all times, RX-not-empty
// reflects the FIFO, the /ACK level bit reflects whether the byte just
// transferred was acknowledged, and OnIRQ fires synchronously from the
// write when the JOY_CTRL interrupt settings request it. Everything
// above the byte layer (the pad command set, config mode, rumble
// mapping, and the memory card read/write/ID protocols with their XOR
// checksums and end bytes) follows the document byte for byte.
//
// One pad (port 1) and one memory card are modelled. The JOY_CTRL slot
// bit is stored but both slots reach the same devices, matching how
// the system package wires a single controller.
//
// Memory card notes from the document:
//   - The card holds 128 Kbytes in 1024 sectors of 128 bytes. Erased
//     (unformatted) storage reads as FFh, so a fresh card image is
//     FFh-filled. The image is raw: directory management is left to
//     games, exactly as on hardware.
//   - The FLAG byte returned with every command starts at 08h on
//     power-up and card insertion. Bit3 means the directory has not
//     been read yet; per the document it is cleared by writing, not by
//     reading. Bit2 reports a write error on the next command.
//   - A write whose checksum does not match the XOR of the address
//     and data bytes is NOT stored, and the transfer ends with the
//     BadChecksum end byte 4Eh. A write or read to a sector above
//     3FFh ends with FFh (bad sector) for writes, and reads answer
//     FFFFh as the confirmed address and abort without data.
//   - With no card inserted, the card answers nothing: every response
//     byte reads FFh and no /ACK (and therefore no IRQ) is produced.
package pad

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Register addresses (absolute).
const (
	addrData = 0x1F801040
	addrStat = 0x1F801044
	addrMode = 0x1F801048
	addrCtrl = 0x1F80104A
	addrBaud = 0x1F80104E
)

// JOY_CTRL bit masks used by this package.
const (
	ctrlTXEnable  = 1 << 0
	ctrlSelect    = 1 << 1 // /JOYn output low: port selected
	ctrlAck       = 1 << 4 // write 1: reset JOY_STAT bits 3 and 9
	ctrlReset     = 1 << 6 // write 1: reset most JOY registers to zero
	ctrlTXIRQEn   = 1 << 10
	ctrlRXIRQEn   = 1 << 11
	ctrlAckIRQEn  = 1 << 12
	ctrlSlotMask  = 1 << 13
	ctrlStoreMask = 0x3FFF &^ (ctrlAck | ctrlReset | 1<<7)
)

// Device selectors for the first byte of a transfer.
const (
	devNone byte = 0
	devPad  byte = 1
	devCard byte = 2
)

// Pad command bytes.
const (
	padCmdPoll   = 0x42
	padCmdConfig = 0x43
	padCmdLED    = 0x44
	padCmdInfo   = 0x45
	padCmdInfoA  = 0x46
	padCmdInfoC  = 0x47
	padCmdUnk48  = 0x48
	padCmdInfoB  = 0x4C
	padCmdRumble = 0x4D
)

// Memory card command bytes. The document names 52h "R" (read), 57h
// "W" (write), and 53h "S" (get ID). Some references name the get ID
// command 59h; both values are accepted here and answered identically.
const (
	cardCmdRead  = 0x52
	cardCmdWrite = 0x57
	cardCmdID    = 0x53
	cardCmdIDAlt = 0x59
)

const (
	cardSize    = 128 * 1024
	cardSectors = 1024
	sectorSize  = 128
)

// Controller is the JOY port block: registers, one digital/analog pad,
// and one memory card.
type Controller struct {
	// OnIRQ is fired when an acknowledge completes a byte transfer and
	// the JOY_CTRL interrupt settings request an IRQ for it. The
	// system package maps this to I_STAT bit 7.
	OnIRQ func()

	mode uint16
	ctrl uint16
	baud uint16

	rxFIFO  []byte
	statIRQ bool
	ackLow  bool

	// Host supplied pad inputs.
	buttons uint32 // bit set means pressed; see SetButtons
	rx, ry  uint8
	lx, ly  uint8

	// Pad state.
	analogMode       bool
	configMode       bool
	ledLock          bool
	motorMap         [6]byte // 4Dh mapping; FFh means unmapped
	rumbleConfigured bool
	motorSmall       bool
	motorBig         uint8

	// Card state.
	cardPresent bool
	cardFlag    byte // FLAG byte base value; starts at 08h
	cardErrNext bool // set bit2 of the next FLAG after a failed write
	card        []byte

	// In-flight transfer state.
	dev      byte
	cmd      byte
	pos      int // index of the byte currently being transferred
	total    int // total bytes in the transfer, address byte included
	curID    byte
	startCfg bool // transfer started in config mode
	iiVal    byte // config command index byte captured at pos 3
	motXX    byte // first motor byte, for the old one-motor method

	pendNewMap    [6]byte
	pendNewMapOK  bool
	pendMode      int8 // 0 none, 1 enter config, 2 exit config
	pendLED       int8 // 0 none, 1 set digital, 2 set analog
	pendLock      int8 // 0 none, 1 unlock, 2 lock
	lastSent      byte
	cardMSB       byte
	cardLSB       byte
	cardRecvChk   byte
	cardWriteOK   bool
	cardSectorBad bool
	cardWriteBuf  [sectorSize]byte
}

// New returns a Controller in its power-up state: digital mode, motors
// unmapped, card inserted and erased (FFh-filled), FLAG byte 08h.
func New() *Controller {
	c := &Controller{card: make([]byte, cardSize)}
	for i := range c.card {
		c.card[i] = 0xFF
	}
	c.cardPresent = true
	c.Reset()
	return c
}

// Reset resets the JOY registers and the protocol state machines, and
// returns the pad to digital non-config mode with rumble unmapped.
// The card image is preserved (a console reset does not erase the
// card), but the card FLAG byte returns to its power-up value 08h.
func (c *Controller) Reset() {
	c.mode, c.ctrl, c.baud = 0, 0, 0
	c.rxFIFO = c.rxFIFO[:0]
	c.statIRQ = false
	c.ackLow = false
	c.analogMode = false
	c.configMode = false
	c.ledLock = false
	for i := range c.motorMap {
		c.motorMap[i] = 0xFF
	}
	c.rumbleConfigured = false
	c.motorSmall = false
	c.motorBig = 0
	c.rx, c.ry, c.lx, c.ly = 0x80, 0x80, 0x80, 0x80
	c.cardFlag = 0x08
	c.cardErrNext = false
	c.resetTransfer()
}

func (c *Controller) resetTransfer() {
	c.dev = devNone
	c.cmd = 0
	c.pos = 0
	c.total = 0
	c.curID = 0
	c.startCfg = false
	c.iiVal = 0
	c.pendNewMapOK = false
	c.pendMode = 0
	c.pendLED = 0
	c.pendLock = 0
	c.cardWriteOK = false
	c.cardSectorBad = false
}

// SetButtons sets the pressed buttons. Bit layout: bit0 Select,
// bit1 L3, bit2 R3, bit3 Start, bit4 Up, bit5 Right, bit6 Down,
// bit7 Left, bit8 L2, bit9 R2, bit10 L1, bit11 R1, bit12 Triangle,
// bit13 Circle, bit14 Cross, bit15 Square. Set bits mean pressed;
// the wire format is active low and is derived from this mask.
func (c *Controller) SetButtons(mask uint32) { c.buttons = mask & 0xFFFF }

// SetAnalog sets the four stick axes as reported by analog polls:
// right stick X/Y, then left stick X/Y.
func (c *Controller) SetAnalog(rx, ry, lx, ly uint8) { c.rx, c.ry, c.lx, c.ly = rx, ry, lx, ly }

// SetAnalogMode forces the pad's analog mode from the host side.
// Guest software normally enters analog mode through the config
// sequence (the 44h LED command); a frontend that always presents
// an analog pad sets it here instead. Poll responses then carry
// the 73h ID and the stick bytes, and the digital buttons still
// read normally.
func (c *Controller) SetAnalogMode(b bool) { c.analogMode = b }

// AnalogMode reports whether the pad answers polls as an analog
// pad (73h) or a digital pad (41h).
func (c *Controller) AnalogMode() bool { return c.analogMode }

// CardImage returns the live 128 KB card image. Mutations made by the
// card protocol are visible to the caller and vice versa.
func (c *Controller) CardImage() []byte { return c.card }

// SetCardPresent inserts or removes the memory card. Inserting a card
// restores the FLAG byte to its power-up value 08h, as the document
// describes for re-insertion.
func (c *Controller) SetCardPresent(b bool) {
	if b && !c.cardPresent {
		c.cardFlag = 0x08
		c.cardErrNext = false
	}
	c.cardPresent = b
	if !b && c.dev == devCard {
		c.resetTransfer()
	}
}

// Motors returns the last vibration state commanded through the poll
// bytes selected by the 4Dh rumble mapping (or the old one-motor
// method before any 4Dh mapping is installed): the small motor is a
// digital on/off, the big motor an analog level.
func (c *Controller) Motors() (small bool, big uint8) { return c.motorSmall, c.motorBig }

// ReadReg reads a JOY register. Only the documented access sizes are
// claimed: JOY_DATA byte reads (a 32-bit read returns the FIFO head in
// the low byte and previews of the next entries above it, popping
// only the head), JOY_STAT 32-bit, JOY_MODE/JOY_CTRL/JOY_BAUD 16-bit.
func (c *Controller) ReadReg(addr uint32, size int) (uint32, bool) {
	switch addr {
	case addrData:
		switch size {
		case 1:
			return uint32(c.popRX()), true
		case 4:
			var v uint32
			for i := 0; i < 4 && i < len(c.rxFIFO); i++ {
				v |= uint32(c.rxFIFO[i]) << (8 * i)
			}
			c.popRX()
			return v, true
		}
		return 0, false
	case addrStat:
		if size != 4 {
			return 0, false
		}
		return c.stat(), true
	case addrMode:
		if size != 2 {
			return 0, false
		}
		return uint32(c.mode), true
	case addrCtrl:
		if size != 2 {
			return 0, false
		}
		return uint32(c.ctrl), true
	case addrBaud:
		if size != 2 {
			return 0, false
		}
		return uint32(c.baud), true
	}
	return 0, false
}

// WriteReg writes a JOY register. JOY_DATA accepts byte writes and
// word writes (the low byte is sent, per the contract). JOY_MODE,
// JOY_CTRL, and JOY_BAUD accept halfword writes.
func (c *Controller) WriteReg(addr uint32, size int, v uint32) bool {
	switch addr {
	case addrData:
		if size != 1 && size != 4 {
			return false
		}
		c.writeData(byte(v))
		return true
	case addrMode:
		if size != 2 {
			return false
		}
		c.mode = uint16(v)
		return true
	case addrCtrl:
		if size != 2 {
			return false
		}
		c.writeCtrl(uint16(v))
		return true
	case addrBaud:
		if size != 2 {
			return false
		}
		c.baud = uint16(v)
		return true
	}
	return false
}

func (c *Controller) stat() uint32 {
	var s uint32
	s |= 1 << 0 // TX Ready Flag 1: ready (immediate completion model)
	if len(c.rxFIFO) > 0 {
		s |= 1 << 1 // RX FIFO Not Empty
	}
	s |= 1 << 2 // TX Ready Flag 2: finished
	// Bit3 RX parity error: never set (8-bit no-parity transfers).
	if c.ackLow {
		s |= 1 << 7 // /ACK Input Level: low
	}
	if c.statIRQ {
		s |= 1 << 9 // Interrupt Request (sticky until JOY_CTRL bit4)
	}
	// Bits 11-31 baudrate timer: reads zero in this model.
	return s
}

func (c *Controller) writeCtrl(v uint16) {
	old := c.ctrl
	if v&ctrlReset != 0 {
		// Reset most JOY registers to zero. Pad and card device state
		// reached through completed transfers is left alone.
		c.mode, c.ctrl, c.baud = 0, 0, 0
		c.rxFIFO = c.rxFIFO[:0]
		c.statIRQ = false
		c.ackLow = false
		c.resetTransfer()
		return
	}
	if v&ctrlAck != 0 {
		c.statIRQ = false // also clears the parity bit, which is never set
	}
	c.ctrl = v & ctrlStoreMask
	// Deselecting the port, or switching slots mid-transfer, resets
	// the addressed device state machine.
	if old&ctrlSelect != 0 && v&ctrlSelect == 0 {
		c.resetTransfer()
	}
	if (old^v)&ctrlSlotMask != 0 && c.pos > 0 {
		c.resetTransfer()
	}
	if v&ctrlSelect == 0 && c.pos > 0 {
		c.resetTransfer()
	}
}

func (c *Controller) popRX() byte {
	if len(c.rxFIFO) == 0 {
		return 0xFF
	}
	b := c.rxFIFO[0]
	c.rxFIFO = c.rxFIFO[1:]
	return b
}

// writeData runs one byte transfer. Bytes are ignored when JOY_CTRL
// does not enable the transfer (TX enable and port select both set),
// as the document prescribes.
func (c *Controller) writeData(b byte) {
	if c.ctrl&ctrlTXEnable == 0 || c.ctrl&ctrlSelect == 0 {
		return
	}
	resp, ack := c.transferByte(b)
	// The response byte is stored in the RX FIFO (the port is
	// selected, which is the normal receive-enable condition).
	if len(c.rxFIFO) < 8 {
		c.rxFIFO = append(c.rxFIFO, resp)
	}
	c.ackLow = ack
	// Interrupt generation, per the JOY_CTRL mode bits: ACK interrupt
	// (bit12) when the byte was acknowledged, RX interrupt (bit11)
	// when the FIFO reaches the count selected by bits 8-9, TX
	// interrupt (bit10) when TX is ready after a transfer.
	fire := false
	if ack && c.ctrl&ctrlAckIRQEn != 0 {
		fire = true
	}
	if c.ctrl&ctrlRXIRQEn != 0 {
		need := 1 << ((c.ctrl >> 8) & 3) // 1, 2, 4, or 8 bytes
		if len(c.rxFIFO) >= need {
			fire = true
		}
	}
	if c.ctrl&ctrlTXIRQEn != 0 {
		fire = true
	}
	if fire {
		c.statIRQ = true
		if c.OnIRQ != nil {
			c.OnIRQ()
		}
	}
}

// transferByte processes one sent byte and returns the simultaneously
// received byte and whether the device acknowledged it. Per the
// document, the last byte of a transfer gets no /ACK.
func (c *Controller) transferByte(b byte) (resp byte, ack bool) {
	prev := c.lastSent
	c.lastSent = b
	if c.pos == 0 {
		switch b {
		case 0x01:
			c.dev = devPad
			c.pos = 1
			return 0xFF, true // Hi-Z dummy response, pad present
		case 0x81:
			if !c.cardPresent {
				return 0xFF, false // absent card answers nothing
			}
			c.dev = devCard
			c.pos = 1
			return 0xFF, true
		default:
			return 0xFF, false
		}
	}
	if c.dev == devPad {
		return c.padByte(b)
	}
	return c.cardByte(b, prev)
}

// finishTransfer applies deferred side effects and re-arms the state
// machine for the next address byte.
func (c *Controller) finishTransfer() {
	if c.pendMode == 1 {
		c.configMode = true
	} else if c.pendMode == 2 {
		c.configMode = false
	}
	if c.pendLED == 1 {
		c.analogMode = false
	} else if c.pendLED == 2 {
		c.analogMode = true
	}
	if c.pendLock == 1 {
		c.ledLock = false
	} else if c.pendLock == 2 {
		c.ledLock = true
	}
	if c.pendNewMapOK {
		c.motorMap = c.pendNewMap
		c.rumbleConfigured = true
	}
	c.resetTransfer()
}

// --- Pad protocol ---

func (c *Controller) padID() byte {
	if c.analogMode {
		return 0x73
	}
	return 0x41
}

// pollTotal is the transfer length of a poll-shaped response with the
// current mode and rumble mapping. Mapping a motor onto a poll byte
// beyond the digital payload extends the transfer (and therefore the
// low nibble of the digital ID), as the 4Dh chapter describes.
func (c *Controller) pollTotal() int {
	if c.configMode || c.analogMode {
		return 9
	}
	total := 5
	for j := 0; j < 6; j++ {
		if c.motorMap[j] != 0xFF && j+4 > total {
			total = j + 4
		}
	}
	return total
}

func (c *Controller) padByte(b byte) (byte, bool) {
	pos := c.pos
	if pos == 1 {
		c.cmd = b
		c.startCfg = c.configMode
		switch {
		case c.configMode:
			c.total = 9
			c.curID = 0xF3
		case b == padCmdPoll || b == padCmdConfig:
			c.total = c.pollTotal()
			c.curID = c.padID()
			if !c.analogMode && c.total != 5 {
				c.curID = 0x40 | byte((c.total-3)/2)
			}
		default:
			// Unknown command in normal mode: answer the ID byte,
			// then end the transfer so the machine cannot hang.
			c.total = 2
			c.curID = c.padID()
		}
		c.pos = 2
		if c.total <= 2 {
			resp := c.curID
			c.finishTransfer()
			return resp, false
		}
		return c.curID, true
	}
	// Data positions: capture side effects first, then respond.
	c.padData(pos, b)
	resp := c.padResponse(pos)
	done := pos >= c.total-1
	c.pos++
	if done {
		c.finishTransfer()
		return resp, false
	}
	return resp, true
}

// padData records the side effects of a sent byte at position pos
// (pos 2 is the first byte after the command byte).
func (c *Controller) padData(pos int, b byte) {
	if pos == 3 {
		c.iiVal = b
	}
	switch c.cmd {
	case padCmdPoll:
		c.pollMotor(pos, b)
	case padCmdConfig:
		if c.startCfg {
			if pos == 3 {
				if b == 0 {
					c.pendMode = 2 // exit config mode
				} else if b == 1 {
					c.pendMode = 0 // stay in config mode
				}
			}
		} else {
			c.pollMotor(pos, b)
			if pos == 3 && b == 1 {
				c.pendMode = 1 // enter config mode
			}
		}
	case padCmdLED:
		if c.startCfg {
			if pos == 3 {
				if b == 0 {
					c.pendLED = 1
				} else if b == 1 {
					c.pendLED = 2
				}
			}
			if pos == 4 {
				eff := b
				if b > 3 {
					eff = b & 3 // 04h..FFh acts as Key AND 03h
				}
				if eff == 3 {
					c.pendLock = 2
				} else {
					c.pendLock = 1
				}
			}
		}
	case padCmdRumble:
		if c.startCfg && pos >= 3 && pos <= 8 {
			c.pendNewMap[pos-3] = b
			if pos == 8 {
				c.pendNewMapOK = true
			}
		}
	}
}

// pollMotor applies one sent poll byte to the rumble motors, using
// the 4Dh mapping once installed, and the old one-motor method before
// that (the byte at pos 3 in 40h..7Fh together with an odd byte at
// pos 4 switches the small motor).
func (c *Controller) pollMotor(pos int, b byte) {
	idx := pos - 3 // poll bytes at pos 3..8 map to aa..ff
	if idx < 0 || idx > 5 {
		return
	}
	if c.rumbleConfigured {
		switch c.motorMap[idx] {
		case 0x00:
			c.motorSmall = b&1 != 0
		case 0x01:
			c.motorBig = b
		}
		return
	}
	if pos == 3 {
		c.motXX = b
	}
	if pos == 4 {
		c.motorSmall = c.motXX >= 0x40 && c.motXX <= 0x7F && b&1 != 0
	}
}

// padResponse returns the pad byte sent back while the host clocks
// out the byte at position pos (pos 2 answers 5Ah, pos 3 and up are
// the payload).
func (c *Controller) padResponse(pos int) byte {
	if pos == 2 {
		return 0x5A
	}
	j := pos - 3 // payload index
	buttons := uint16(c.buttons)
	btnLo := byte(^buttons)
	btnHi := byte(^(buttons >> 8))
	pollPayload := []byte{btnLo, btnHi, c.rx, c.ry, c.lx, c.ly}
	if !c.startCfg && !c.analogMode && c.cmd == padCmdPoll {
		// Digital poll: payload past the buttons is padding, except
		// for bytes that exist only because a motor is mapped there.
		if j >= 2 {
			return 0x00
		}
	}
	switch c.cmd {
	case padCmdPoll:
		if j < len(pollPayload) {
			return pollPayload[j]
		}
		return 0
	case padCmdConfig:
		if c.startCfg {
			return 0 // config-mode exit/stay answers zero payload
		}
		if j < len(pollPayload) {
			return pollPayload[j]
		}
		return 0
	case padCmdLED:
		return 0 // payload includes the Err byte, 00h on this pad class
	case padCmdInfo:
		led := byte(0)
		if c.analogMode {
			led = 1
		}
		info := []byte{0x01, 0x02, led, 0x02, 0x01, 0x00}
		if j < len(info) {
			return info[j]
		}
		return 0
	case padCmdInfoA:
		var fnc, sub, siz, pwr byte
		switch c.iiVal {
		case 0:
			fnc, sub, siz, pwr = 0x01, 0x02, 0x00, 0x0A // small motor
		case 1:
			fnc, sub, siz, pwr = 0x01, 0x01, 0x01, 0x14 // big motor
		}
		info := []byte{0x00, 0x00, fnc, sub, siz, pwr}
		if j < len(info) {
			return info[j]
		}
		return 0
	case padCmdInfoC:
		var info []byte
		if c.iiVal == 0 {
			info = []byte{0x00, 0x00, 0x02, 0x00, 0x01, 0x00}
		} else {
			info = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		}
		if j < len(info) {
			return info[j]
		}
		return 0
	case padCmdUnk48:
		ee := byte(0)
		if c.iiVal <= 1 && j == 4 {
			ee = 0x01
		}
		return ee
	case padCmdInfoB:
		var cc, dd byte
		switch c.iiVal {
		case 0:
			cc, dd = 0x00, 0x04
		case 1:
			cc, dd = 0x00, 0x07
		}
		info := []byte{0x00, 0x00, cc, dd, 0x00, 0x00}
		if j < len(info) {
			return info[j]
		}
		return 0
	case padCmdRumble:
		// Answers the OLD mapping while the new one is being sent.
		if j < 6 {
			return c.motorMap[j]
		}
		return 0
	default:
		// Unused and unknown commands in config mode answer the
		// documented dummy pattern: ID F3h, 5Ah, then zero bytes.
		return 0
	}
}

// --- Memory card protocol ---

func (c *Controller) cardFlagByte() byte {
	f := c.cardFlag
	if c.cardErrNext {
		f |= 0x04
		c.cardErrNext = false
	}
	return f
}

func (c *Controller) cardSector() int {
	return int(c.cardMSB)<<8 | int(c.cardLSB)
}

func (c *Controller) cardChecksum() byte {
	chk := c.cardMSB ^ c.cardLSB
	off := c.cardSector() * sectorSize
	if c.cardSector() < cardSectors {
		for _, d := range c.card[off : off+sectorSize] {
			chk ^= d
		}
	}
	return chk
}

func (c *Controller) cardByte(b byte, prev byte) (byte, bool) {
	pos := c.pos
	if pos == 1 {
		c.cmd = b
		switch b {
		case cardCmdRead:
			c.total = 140
		case cardCmdWrite:
			c.total = 138
		case cardCmdID, cardCmdIDAlt:
			c.total = 10
		default:
			// Invalid command: answer FLAG, then abort the transfer.
			resp := c.cardFlagByte()
			c.resetTransfer()
			return resp, false
		}
		resp := c.cardFlagByte()
		c.pos = 2
		return resp, true
	}
	var resp byte
	done := false
	switch c.cmd {
	case cardCmdRead:
		switch {
		case pos == 2:
			resp = 0x5A
		case pos == 3:
			resp = 0x5D
		case pos == 4:
			c.cardMSB = b
			resp = 0x00
		case pos == 5:
			c.cardLSB = b
			c.cardSectorBad = c.cardSector() >= cardSectors
			resp = prev // (pre): echoes the MSB
		case pos == 6:
			resp = 0x5C
		case pos == 7:
			resp = 0x5D
		case pos == 8:
			if c.cardSectorBad {
				resp = 0xFF
			} else {
				resp = c.cardMSB
			}
		case pos == 9:
			if c.cardSectorBad {
				resp = 0xFF
				done = true // FFFFh confirmed address, then abort
			} else {
				resp = c.cardLSB
			}
		case pos >= 10 && pos <= 137:
			off := c.cardSector()*sectorSize + (pos - 10)
			resp = c.card[off]
		case pos == 138:
			resp = c.cardChecksum()
		case pos == 139:
			resp = 0x47 // "G": good
			done = true
		}
	case cardCmdWrite:
		switch {
		case pos == 2:
			resp = 0x5A
		case pos == 3:
			resp = 0x5D
		case pos == 4:
			c.cardMSB = b
			resp = 0x00
		case pos == 5:
			c.cardLSB = b
			c.cardSectorBad = c.cardSector() >= cardSectors
			resp = prev
		case pos >= 6 && pos <= 133:
			c.cardWriteBuf[pos-6] = b
			resp = prev
		case pos == 134:
			c.cardRecvChk = b
			resp = prev
			// Verify before storing: the checksum covers the
			// address bytes and all 128 data bytes.
			chk := c.cardMSB ^ c.cardLSB
			for _, d := range c.cardWriteBuf {
				chk ^= d
			}
			c.cardWriteOK = chk == b && !c.cardSectorBad
			if c.cardWriteOK {
				off := c.cardSector() * sectorSize
				copy(c.card[off:off+sectorSize], c.cardWriteBuf[:])
			}
		case pos == 135:
			resp = 0x5C
		case pos == 136:
			resp = 0x5D
		case pos == 137:
			switch {
			case c.cardSectorBad:
				resp = 0xFF // bad sector
			case !c.cardWriteOK:
				resp = 0x4E // "N": bad checksum
			default:
				resp = 0x47 // "G": good
			}
			// Any completed write clears FLAG bit3 (the document's
			// dummy-write trick relies on this); a checksum failure
			// raises bit2 on the next command instead.
			c.cardFlag &^= 0x08
			if !c.cardWriteOK && !c.cardSectorBad {
				c.cardErrNext = true
			}
			done = true
		}
	case cardCmdID, cardCmdIDAlt:
		switch pos {
		case 2:
			resp = 0x5A
		case 3:
			resp = 0x5D
		case 4:
			resp = 0x5C
		case 5:
			resp = 0x5D
		case 6:
			resp = 0x04
		case 7, 8:
			resp = 0x00
		case 9:
			resp = 0x80
			done = true
		}
	}
	c.pos++
	if done {
		c.resetTransfer()
		return resp, false
	}
	return resp, true
}

// --- Serialization ---

// StateSize is the fixed serialized size: a 512-byte header holding
// the JOY registers, protocol state, pad mode and config state, and
// the in-flight transfer, followed by the full 128 KB card image.
const StateSize = 512 + cardSize

const stateMagic = "PS1PAD01"

var errBadState = errors.New("pad: bad serialized state")

// Serialize returns the full controller state, exactly StateSize
// bytes, including the card image.
func (c *Controller) Serialize() []byte {
	out := make([]byte, StateSize)
	copy(out[0:8], stateMagic)
	binary.LittleEndian.PutUint16(out[8:10], c.mode)
	binary.LittleEndian.PutUint16(out[10:12], c.ctrl)
	binary.LittleEndian.PutUint16(out[12:14], c.baud)
	binary.LittleEndian.PutUint32(out[16:20], c.buttons)
	out[20], out[21], out[22], out[23] = c.rx, c.ry, c.lx, c.ly
	var fl byte
	if c.configMode {
		fl |= 1 << 0
	}
	if c.analogMode {
		fl |= 1 << 1
	}
	if c.ledLock {
		fl |= 1 << 2
	}
	if c.cardPresent {
		fl |= 1 << 3
	}
	if c.statIRQ {
		fl |= 1 << 4
	}
	if c.ackLow {
		fl |= 1 << 5
	}
	if c.motorSmall {
		fl |= 1 << 6
	}
	out[24] = fl
	if c.rumbleConfigured {
		out[25] = 1
	}
	out[26] = c.motorBig
	out[27] = c.cardFlag
	copy(out[28:34], c.motorMap[:])
	out[34] = c.dev
	out[35] = c.cmd
	binary.LittleEndian.PutUint16(out[36:38], uint16(c.pos))
	binary.LittleEndian.PutUint16(out[38:40], uint16(c.total))
	out[40] = c.curID
	if c.startCfg {
		out[41] = 1
	}
	out[42] = c.iiVal
	out[43] = c.motXX
	out[44] = c.lastSent
	out[45] = c.cardMSB
	out[46] = c.cardLSB
	out[47] = c.cardRecvChk
	out[48] = byte(len(c.rxFIFO))
	copy(out[49:57], c.rxFIFO)
	copy(out[57:185], c.cardWriteBuf[:])
	copy(out[185:191], c.pendNewMap[:])
	if c.pendNewMapOK {
		out[191] = 1
	}
	out[192] = byte(c.pendMode)
	out[193] = byte(c.pendLED)
	out[194] = byte(c.pendLock)
	if c.cardWriteOK {
		out[195] = 1
	}
	if c.cardSectorBad {
		out[196] = 1
	}
	if c.cardErrNext {
		out[197] = 1
	}
	copy(out[512:512+cardSize], c.card)
	return out
}

// Deserialize restores a state produced by Serialize.
func (c *Controller) Deserialize(in []byte) error {
	if len(in) != StateSize {
		return fmt.Errorf("pad: state size %d, want %d", len(in), StateSize)
	}
	if string(in[0:8]) != stateMagic {
		return errBadState
	}
	c.mode = binary.LittleEndian.Uint16(in[8:10])
	c.ctrl = binary.LittleEndian.Uint16(in[10:12])
	c.baud = binary.LittleEndian.Uint16(in[12:14])
	c.buttons = binary.LittleEndian.Uint32(in[16:20])
	c.rx, c.ry, c.lx, c.ly = in[20], in[21], in[22], in[23]
	fl := in[24]
	c.configMode = fl&(1<<0) != 0
	c.analogMode = fl&(1<<1) != 0
	c.ledLock = fl&(1<<2) != 0
	c.cardPresent = fl&(1<<3) != 0
	c.statIRQ = fl&(1<<4) != 0
	c.ackLow = fl&(1<<5) != 0
	c.motorSmall = fl&(1<<6) != 0
	c.rumbleConfigured = in[25] != 0
	c.motorBig = in[26]
	c.cardFlag = in[27]
	copy(c.motorMap[:], in[28:34])
	c.dev = in[34]
	c.cmd = in[35]
	c.pos = int(binary.LittleEndian.Uint16(in[36:38]))
	c.total = int(binary.LittleEndian.Uint16(in[38:40]))
	c.curID = in[40]
	c.startCfg = in[41] != 0
	c.iiVal = in[42]
	c.motXX = in[43]
	c.lastSent = in[44]
	c.cardMSB = in[45]
	c.cardLSB = in[46]
	c.cardRecvChk = in[47]
	n := int(in[48])
	if n > 8 {
		n = 8
	}
	c.rxFIFO = append(c.rxFIFO[:0], in[49:49+n]...)
	copy(c.cardWriteBuf[:], in[57:185])
	copy(c.pendNewMap[:], in[185:191])
	c.pendNewMapOK = in[191] != 0
	c.pendMode = int8(in[192])
	c.pendLED = int8(in[193])
	c.pendLock = int8(in[194])
	c.cardWriteOK = in[195] != 0
	c.cardSectorBad = in[196] != 0
	c.cardErrNext = in[197] != 0
	copy(c.card, in[512:512+cardSize])
	return nil
}
