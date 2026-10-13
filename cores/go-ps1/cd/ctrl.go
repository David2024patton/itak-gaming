package cd

// The PS1-2 CD-ROM controller subset. The full CD
// controller (seek/play state machine, subchannel,
// error handling) is PS1-3; this controller covers
// the documented registers at 0x1F801800-0x1F801803
// and the small command set the DMA gate needs:
//
//	0x01 Getstat   status byte response, IRQ type 3
//	0x02 Setloc    BCD mm:ss:ff parameters, sets the
//	               pending read position
//	0x06 ReadN     start streaming sectors from the
//	               pending position into the data FIFO
//	0x09 Pause     stop streaming
//	0x0E Setmode   store the mode byte
//	0x1B ReadS     same streaming behavior as ReadN
//	               in this subset
//
// Command responses are pushed to the response FIFO
// and acknowledged with IRQ type 3; each streamed
// sector raises IRQ type 1 when its data is queued.
// Unknown commands raise IRQ type 5 (error) and set
// the error bit in the status byte. Sector data is
// the 2048-byte user data from the Reader; when the
// FIFO drains while streaming, the next sector is
// queued automatically, as ReadN does on hardware.
//
// The interrupt output follows the documented
// enable/flag registers: a type is recorded in the
// flag register when it occurs, and the OnIRQ
// callback fires when (flag AND enable) becomes
// nonzero. Writing the flag register acknowledges
// the written bits; writing bit 6 resets the
// controller's FIFOs and flags.

import "fmt"

// IRQ types recorded in the flag register.
const (
	irqDataReady = 1
	irqAck       = 3
	irqError     = 5
)

// Controller is the CD-ROM controller subset.
type Controller struct {
	disc Reader

	index    int
	params   []byte
	response []byte
	data     []byte

	irqEnable uint8
	irqFlag   uint8
	irqHot    bool // (flag AND enable) was nonzero at last check

	mode      uint8
	statError bool

	pendingLBA int  // Setloc target
	nextLBA    int  // next sector to stream
	streaming  bool // ReadN/ReadS active

	// OnIRQ, when set, is called on the 0-to-1
	// transition of (irqFlag AND irqEnable).
	OnIRQ func()
}

// NewController returns a controller with no disc.
func NewController() *Controller { return &Controller{} }

// SetDisc attaches the disc image sectors are
// streamed from (nil detaches).
func (c *Controller) SetDisc(r Reader) { c.disc = r }

// Reset returns the controller to power-on state.
// The attached disc is kept.
func (c *Controller) Reset() {
	disc := c.disc
	onIRQ := c.OnIRQ
	*c = Controller{disc: disc, OnIRQ: onIRQ}
}

// DataLen returns the number of bytes waiting in the
// data FIFO.
func (c *Controller) DataLen() int { return len(c.data) }

// Streaming reports whether a read stream is active.
func (c *Controller) Streaming() bool { return c.streaming }

// statusByte builds the Getstat/status response
// byte: bit 1 motor on (disc present), bit 4 shell
// open (no disc), bit 5 reading (streaming), bit 0
// error (latched until the next successful command).
func (c *Controller) statusByte() uint8 {
	var b uint8
	if c.statError {
		b |= 0x01
	}
	if c.disc == nil {
		b |= 0x10
	} else {
		b |= 0x02
	}
	if c.streaming {
		b |= 0x20
	}
	return b
}

// StatusRegister returns the value read from
// 0x1F801800.
func (c *Controller) StatusRegister() uint8 {
	var b uint8 = uint8(c.index & 3)
	if len(c.params) == 0 {
		b |= 0x08 // PRMEMPT: parameter FIFO empty
	}
	if len(c.params) < 16 {
		b |= 0x10 // PRMWRDY: parameter FIFO not full
	}
	if len(c.response) > 0 {
		b |= 0x20 // RSLRRDY: response ready
	}
	if len(c.data) > 0 {
		b |= 0x40 // DRQSTS: data FIFO not empty
	}
	return b
}

func (c *Controller) raiseIRQ(typ uint8) {
	c.irqFlag |= typ
	hot := c.irqFlag&c.irqEnable != 0
	if hot && !c.irqHot && c.OnIRQ != nil {
		c.OnIRQ()
	}
	c.irqHot = hot
}

// ReadReg reads one of the four controller
// registers (offset 0-3). The offset is the low two
// bits of the address.
func (c *Controller) ReadReg(off uint32) uint8 {
	switch off & 3 {
	case 0:
		return c.StatusRegister()
	case 1:
		if len(c.response) == 0 {
			return 0
		}
		v := c.response[0]
		c.response = c.response[1:]
		return v
	case 2:
		return c.popDataByte()
	case 3:
		if c.index == 1 {
			return c.irqFlag
		}
		return c.irqEnable
	}
	return 0
}

// WriteReg writes one of the four controller
// registers.
func (c *Controller) WriteReg(off uint32, v uint8) {
	switch off & 3 {
	case 0:
		c.index = int(v & 3)
	case 1:
		if c.index == 0 {
			c.command(v)
		}
	case 2:
		if c.index == 0 && len(c.params) < 16 {
			c.params = append(c.params, v)
		}
	case 3:
		if c.index == 1 {
			if v&0x40 != 0 {
				c.params = nil
				c.response = nil
				c.irqFlag = 0
				c.irqHot = false
			} else {
				c.irqFlag &^= v & 0x1F
				c.irqHot = c.irqFlag&c.irqEnable != 0
			}
		} else {
			c.irqEnable = v & 0x1F
			hot := c.irqFlag&c.irqEnable != 0
			if hot && !c.irqHot && c.OnIRQ != nil {
				c.OnIRQ()
			}
			c.irqHot = hot
		}
	}
}

func bcd(b uint8) int {
	return int(b>>4)*10 + int(b&0x0F)
}

func (c *Controller) command(cmd uint8) {
	respond := func() {
		c.response = append(c.response, c.statusByte())
	}
	switch cmd {
	case 0x01: // Getstat
		respond()
		c.raiseIRQ(irqAck)
	case 0x02: // Setloc
		if len(c.params) >= 3 {
			m, s, f := bcd(c.params[0]), bcd(c.params[1]), bcd(c.params[2])
			c.pendingLBA = MSFToSector(m, s, f) - 150
			if c.pendingLBA < 0 {
				c.pendingLBA = 0
			}
			c.statError = false
		} else {
			c.statError = true
		}
		c.params = nil
		c.streaming = false
		respond()
		c.raiseIRQ(irqAck)
	case 0x06, 0x1B: // ReadN / ReadS
		c.params = nil
		respond()
		c.raiseIRQ(irqAck)
		c.nextLBA = c.pendingLBA
		c.streaming = true
		c.queueSector()
	case 0x09: // Pause
		c.streaming = false
		c.params = nil
		respond()
		c.raiseIRQ(irqAck)
	case 0x0E: // Setmode
		if len(c.params) >= 1 {
			c.mode = c.params[0]
		}
		c.params = nil
		respond()
		c.raiseIRQ(irqAck)
	default:
		c.params = nil
		c.statError = true
		respond()
		c.raiseIRQ(irqError)
	}
}

// queueSector loads the next stream sector's user
// data into the data FIFO and raises the data-ready
// IRQ. On failure the stream stops with an error.
func (c *Controller) queueSector() {
	if c.disc == nil {
		c.streaming = false
		c.statError = true
		c.raiseIRQ(irqError)
		return
	}
	sec, err := c.disc.ReadSector(c.nextLBA, false)
	if err != nil {
		c.streaming = false
		c.statError = true
		c.raiseIRQ(irqError)
		return
	}
	c.data = append(c.data[:0], sec...)
	c.nextLBA++
	c.raiseIRQ(irqDataReady)
}

// popDataByte removes one byte from the data FIFO,
// queueing the next sector when the FIFO drains
// during an active stream.
func (c *Controller) popDataByte() uint8 {
	if len(c.data) == 0 {
		return 0
	}
	v := c.data[0]
	c.data = c.data[1:]
	if len(c.data) == 0 && c.streaming {
		c.queueSector()
	}
	return v
}

// ReadWord removes one little-endian word from the
// data FIFO for the CDROM DMA channel. ok is false
// when fewer than 4 bytes are available and the
// stream could not refill.
func (c *Controller) ReadWord() (uint32, bool) {
	if len(c.data) < 4 {
		return 0, false
	}
	var v uint32
	for i := 0; i < 4; i++ {
		v |= uint32(c.popDataByte()) << (8 * i)
	}
	return v, true
}

// Mode returns the stored Setmode byte.
func (c *Controller) Mode() uint8 { return c.mode }

// CtrlStateSize is the serialized controller size:
// fixed fields plus bounded FIFOs.
const CtrlStateSize = 32 + 16 + 256 + 2352

// Serialize writes the controller state (the disc
// itself is not part of it; the system re-attaches
// the same Reader after a restore).
func (c *Controller) Serialize() []byte {
	out := make([]byte, CtrlStateSize)
	out[0] = uint8(c.index)
	out[1] = c.irqEnable
	out[2] = c.irqFlag
	out[3] = c.mode
	if c.irqHot {
		out[4] = 1
	}
	if c.statError {
		out[5] = 1
	}
	if c.streaming {
		out[6] = 1
	}
	put32(out[8:], uint32(int32(c.pendingLBA)))
	put32(out[12:], uint32(int32(c.nextLBA)))
	put32(out[16:], uint32(len(c.params)))
	put32(out[20:], uint32(len(c.response)))
	put32(out[24:], uint32(len(c.data)))
	copy(out[32:48], c.params)
	copy(out[48:48+256], c.response)
	copy(out[48+256:], c.data)
	return out
}

// Deserialize restores a state produced by Serialize.
func (c *Controller) Deserialize(in []byte) error {
	if len(in) != CtrlStateSize {
		return fmt.Errorf("cd: controller state size %d, want %d", len(in), CtrlStateSize)
	}
	c.index = int(in[0])
	c.irqEnable = in[1]
	c.irqFlag = in[2]
	c.mode = in[3]
	c.irqHot = in[4] != 0
	c.statError = in[5] != 0
	c.streaming = in[6] != 0
	c.pendingLBA = int(int32(get32(in[8:])))
	c.nextLBA = int(int32(get32(in[12:])))
	np := int(get32(in[16:]))
	nr := int(get32(in[20:]))
	nd := int(get32(in[24:]))
	if np > 16 || nr > 256 || nd > 2352 {
		return fmt.Errorf("cd: controller state has bad FIFO lengths %d/%d/%d", np, nr, nd)
	}
	c.params = append([]byte(nil), in[32:32+np]...)
	c.response = append([]byte(nil), in[48:48+nr]...)
	c.data = append([]byte(nil), in[48+256:48+256+nd]...)
	return nil
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func get32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
