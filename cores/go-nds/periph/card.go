package periph

// ChipID is the value the card unit reports for the documented
// Get ROM Chip ID command. It is a synthetic constant of this
// module (real chip IDs belong to manufactured cartridges; ours
// follows the documented 32-bit format with the DS media type
// byte). The direct-boot block writes the same value into main
// RAM where games read their chip ID after boot.
const ChipID uint32 = 0x00001FC2

// Card is the minimal game card command model. The card is
// always in the plain (unencrypted command) state: our boot
// path never runs the KEY1/KEY2 encryption setup, and real
// games only encrypt after talking to the real BIOS, which
// this module does not have. Commands are therefore parsed
// plain, which is stated here and in the README rather than
// hidden.
//
// On a ROMCTRL write with the transfer-start bit set, the
// 8-byte command in ROMCMD is latched and a data block is
// prepared synchronously: command 0x00 and 0xB7 read ROM bytes
// at the 24-bit big-endian address in command bytes 1-3,
// command 0xB8 returns the chip ID repeated, command 0x3C (the
// KEY1 activation marker) and anything else return a zero
// block. Preparing a block raises the card-data-ready
// interrupt and fires armed card-timing DMA channels; reading
// the data port past the end raises transfer complete.
type Card struct {
	cmd     [8]byte
	romctrl uint32

	buf    []byte // prepared block, re-derived on deserialize
	pos    int
	srcOff uint32
	srcLen uint32
	kind   uint8 // 0 none, 1 ROM data, 2 chip ID, 3 zeros
	active bool
}

var cardBlockSizes = [8]uint32{0, 0x200, 0x400, 0x800, 0x1000, 0x2000, 0x4000, 0x10000}

// start latches the command and prepares the block.
func (c *Card) start(s *Set) {
	size := cardBlockSizes[(c.romctrl>>24)&7]
	c.pos = 0
	c.srcLen = size
	switch c.cmd[0] {
	case 0x00, 0xB7:
		c.kind = 1
		c.srcOff = uint32(c.cmd[1])<<16 | uint32(c.cmd[2])<<8 | uint32(c.cmd[3])
	case 0xB8:
		c.kind = 2
	default:
		c.kind = 3
	}
	c.rebuild(s.rom())
	c.active = size > 0
	if c.active {
		s.Raise(true, IRQCardData)
		s.TriggerDMA(TrigCard)
	}
}

// rebuild regenerates the prepared block from the ROM image.
func (c *Card) rebuild(rom []byte) {
	n := int(c.srcLen)
	c.buf = make([]byte, n)
	switch c.kind {
	case 1:
		for i := 0; i < n; i++ {
			if int(c.srcOff)+i < len(rom) {
				c.buf[i] = rom[c.srcOff+uint32(i)]
			}
		}
	case 2:
		id := ChipID
		for i := 0; i+4 <= n; i += 4 {
			c.buf[i] = byte(id)
			c.buf[i+1] = byte(id >> 8)
			c.buf[i+2] = byte(id >> 16)
			c.buf[i+3] = byte(id >> 24)
		}
	}
}

// readROMCTRL returns ROMCTRL with the live status bits: bit
// 23 is data-ready, bit 31 reads as busy during a transfer.
func (c *Card) readROMCTRL() uint32 {
	v := c.romctrl &^ (1<<23 | 1<<31)
	if c.active {
		v |= 1 << 31
		if c.pos < len(c.buf) {
			v |= 1 << 23
		}
	}
	return v
}

// readData serves one word from the data port at 0x04100010.
// The second return reports that this read drained the block,
// so the caller raises the transfer-complete interrupt.
func (c *Card) readData() (uint32, bool) {
	if !c.active || c.pos >= len(c.buf) {
		return 0, false
	}
	var v uint32
	for i := 0; i < 4 && c.pos+i < len(c.buf); i++ {
		v |= uint32(c.buf[c.pos+i]) << (8 * i)
	}
	c.pos += 4
	if c.pos >= len(c.buf) {
		c.active = false
		return v, true
	}
	return v, false
}

func (c *Card) readCmd(rel uint32, size int) uint32 {
	var v uint32
	for i := 0; i < size && int(rel)+i < 8; i++ {
		v |= uint32(c.cmd[rel+uint32(i)]) << (8 * i)
	}
	return v
}

func (c *Card) writeCmd(rel uint32, size int, val uint32) {
	for i := 0; i < size && int(rel)+i < 8; i++ {
		c.cmd[rel+uint32(i)] = byte(val >> (8 * i))
	}
}
