// Package pif implements the N64 PIF (peripheral
// interface) as seen through the serial interface: the
// 64-byte PIF RAM holding Joybus frames for the four
// controller channels and the cartridge channel, the
// frame parsing and execution model, the Joybus command
// set for controllers, controller paks, and the
// cartridge EEPROM, and the two Joybus checksums. It is
// original code written from the published N64
// documentation (n64brew PIF-NUS and Joybus Protocol
// pages).
//
// The documented processing model has two phases. When
// the command byte (PIF RAM offset 0x3F) is written with
// bit 0x01 set, the PIF parses the frame in PIF RAM and
// records each channel's handshake. When the CPU then
// reads PIF RAM through an SI DMA, the PIF executes the
// handshakes (channels in reverse order, 4 down to 0)
// and the replies land in PIF RAM before the DMA copies
// it out. A frame can be executed repeatedly after one
// parse.
//
// Simplifications, stated plainly: the PIF boot security
// exchange with the CIC (including the 6105
// challenge/response) is modeled as passed. The command
// bits 0x20/0x40 (acquire and run the IPL2 checksum) are
// accepted and reported complete without reproducing
// the PIF ROM's checksum comparison, and bit 0x02 (the
// 6105 challenge) leaves the challenge bytes untouched.
// No PIF ROM code is embedded anywhere in this core.
package pif

// RAMSize is the size of PIF RAM in bytes.
const RAMSize = 0x40

// Command byte bits (PIF RAM offset 0x3F).
const (
	cmdJoybusFrame = 0x01 // parse the Joybus frame in RAM
	cmdChallenge   = 0x02 // 6105 challenge/response (modeled: no-op)
	cmdTerminate   = 0x08 // game signals boot termination
	cmdLockROM     = 0x10 // lock the PIF ROM (modeled: no-op)
	cmdAcquireSum  = 0x20 // CPU hands over its IPL2 checksum
	cmdRunSum      = 0x40 // PIF verifies the IPL2 checksum
	cmdDone        = 0x80 // PIF sets: boot data ready / sum acquired
)

// Escape codes in place of a handshake TX byte.
const (
	escSkipChannel = 0x00
	escReset       = 0xFD
	escEndOfFrame  = 0xFE
	escNop         = 0xFF
)

// RX byte error flags, set by the PIF after execution.
const (
	rxNoDevice = 0x80
	rxTimeout  = 0x40
)

// Joybus command bytes.
const (
	joyInfo        = 0x00
	joyInfoAlt     = 0xFF
	joyReadState   = 0x01
	joyPakRead     = 0x02
	joyPakWrite    = 0x03
	joyEEPromRead  = 0x04
	joyEEPromWrite = 0x05
	joyRTCInfo     = 0x06
	joyRTCRead     = 0x07
	joyRTCWrite    = 0x08
)

// PakKind identifies the accessory in a controller.
type PakKind int

const (
	PakNone PakKind = iota
	PakMemory
	PakRumble
)

// PakSize is the controller pak storage size.
const PakSize = 32 << 10

// CartChannel is the cartridge-side Joybus device on
// channel 4: the save EEPROM. The cart package's
// SaveStore satisfies it; RTC commands are answered by
// the PIF itself (no RTC is fitted in this core).
type CartChannel interface {
	EEPROMInfoID() (uint16, bool)
	EEPROMRead(block byte) ([8]byte, bool)
	EEPROMWrite(block byte, data [8]byte) bool
}

// channelState is the runtime state of one controller
// port.
type channelState struct {
	attached bool
	buttons  uint16
	stickX   int8
	stickY   int8

	pak     PakKind
	pakData [PakSize]byte
	// pakFresh is true while an inserted pak has not
	// yet been reported by an Info command (status 3).
	// While fresh, the pak behaves as absent, per the
	// documented Joybus rules.
	pakFresh bool
	rumbleOn bool
	// checksumError is set when the last pak access
	// carried a bad address checksum; the next Info
	// reply reports it in status bit 2.
	checksumError bool
}

// handshake is one parsed Joybus exchange in PIF RAM.
type handshake struct {
	off    int  // offset of the TX byte
	txLen  int  // bytes to transmit (bits 0..5 of TX)
	rxLen  int  // bytes to receive (bits 0..5 of RX)
	skip   bool // TX bit 7: skip this channel at execution
	device bool // whether a handshake was parsed at all
}

// PIF is the peripheral interface device.
type PIF struct {
	RAM [RAMSize]byte

	channels [4]channelState
	cart     CartChannel

	frame    [5]handshake // parsed frame, per channel
	parsed   bool
	bootDone bool
}

// New builds a PIF with a controller attached to every
// port, no paks inserted, and no cartridge device.
func New() *PIF {
	p := &PIF{}
	for i := range p.channels {
		p.channels[i].attached = true
	}
	return p
}

// SetAttached models plugging or unplugging a
// controller. An unplugged port answers every command
// with the no-device error.
func (p *PIF) SetAttached(port int, attached bool) {
	if port < 0 || port > 3 {
		return
	}
	p.channels[port].attached = attached
}

// AttachCart connects the cartridge channel device
// (channel 4). Passing nil detaches it.
func (p *PIF) AttachCart(c CartChannel) { p.cart = c }

// SetController records a controller port's live input
// state (button bit layout per the system package's
// Button constants, stick in the -80..80 range).
func (p *PIF) SetController(port int, buttons uint16, stickX, stickY int8) {
	if port < 0 || port > 3 {
		return
	}
	c := &p.channels[port]
	c.buttons = buttons
	c.stickX = stickX
	c.stickY = stickY
}

// InsertPak puts an accessory into a controller port.
// A memory pak's storage starts zeroed (games format
// it themselves). A freshly inserted pak reports
// status 3 on the next Info command and behaves as
// absent until then, per the documented rules.
func (p *PIF) InsertPak(port int, kind PakKind) {
	if port < 0 || port > 3 {
		return
	}
	c := &p.channels[port]
	c.pak = kind
	c.pakFresh = kind != PakNone
	c.rumbleOn = false
	if kind == PakMemory {
		c.pakData = [PakSize]byte{}
	}
}

// PakKind returns the accessory fitted in a port.
func (p *PIF) PakKind(port int) PakKind {
	if port < 0 || port > 3 {
		return PakNone
	}
	return p.channels[port].pak
}

// PakData returns a port's memory pak storage (the
// live array; the system uses it for save states).
func (p *PIF) PakData(port int) []byte {
	if port < 0 || port > 3 {
		return nil
	}
	return p.channels[port].pakData[:]
}

// RumbleOn reports a rumble pak's motor state.
func (p *PIF) RumbleOn(port int) bool {
	if port < 0 || port > 3 {
		return false
	}
	return p.channels[port].rumbleOn
}

// BootTerminated reports whether the game has sent the
// documented boot termination command (bit 0x08).
func (p *PIF) BootTerminated() bool { return p.bootDone }

// AfterWrite must be called after any host write into
// PIF RAM (SI DMA write or direct CPU write). When the
// command byte carries the joybus frame bit, the frame
// is parsed; the other documented command bits are
// serviced and cleared.
func (p *PIF) AfterWrite() {
	cmd := p.RAM[0x3F]
	if cmd&cmdJoybusFrame != 0 {
		p.parseFrame()
		p.RAM[0x3F] &^= cmdJoybusFrame
	}
	if cmd&cmdAcquireSum != 0 {
		// The CPU's 6-byte IPL2 checksum sits at offset
		// 0x32; the PIF takes it in and clears the area.
		for i := 0; i < 6; i++ {
			p.RAM[0x32+i] = 0
		}
		p.RAM[0x3F] &^= cmdAcquireSum
		p.RAM[0x3F] |= cmdDone
	}
	if cmd&cmdRunSum != 0 {
		// Modeled as passed (see the package comment).
		p.RAM[0x3F] &^= cmdRunSum
	}
	if cmd&cmdLockROM != 0 {
		p.RAM[0x3F] &^= cmdLockROM
	}
	if cmd&cmdChallenge != 0 {
		// Modeled: the challenge bytes are left as they
		// are and the bit is cleared.
		p.RAM[0x3F] &^= cmdChallenge
	}
	if cmd&cmdTerminate != 0 {
		p.bootDone = true
		p.RAM[0x3F] &^= cmdTerminate
	}
}

// parseFrame decodes the Joybus frame in PIF RAM into
// per-channel handshakes, following the documented
// escape codes. A handshake that would run past the
// end of RAM aborts the parse (remaining channels keep
// no handshake).
func (p *PIF) parseFrame() {
	for i := range p.frame {
		p.frame[i] = handshake{}
	}
	pos := 0
	for ch := 0; ch < 5; ch++ {
		if pos >= 0x3F {
			break
		}
		b := p.RAM[pos]
		switch b {
		case escEndOfFrame:
			p.parsed = true
			return
		case escSkipChannel, escReset:
			pos++
			continue
		case escNop:
			pos++
			ch-- // a nop does not consume a channel
			continue
		}
		txLen := int(b & 0x3F)
		if txLen == 0 || pos+1 >= RAMSize {
			p.parsed = true
			return
		}
		rxLen := int(p.RAM[pos+1] & 0x3F)
		if pos+2+txLen+rxLen > RAMSize {
			p.parsed = true
			return // incomplete handshake: parsing aborts
		}
		p.frame[ch] = handshake{
			off:    pos,
			txLen:  txLen,
			rxLen:  rxLen,
			skip:   b&0x80 != 0,
			device: true,
		}
		pos += 2 + txLen + rxLen
	}
	p.parsed = true
}

// Execute runs the parsed frame's handshakes against
// the channel devices, in reverse channel order as the
// real PIF does, writing replies into PIF RAM and the
// documented error flags into each RX byte. The SI
// calls it when the CPU DMAs PIF RAM out.
func (p *PIF) Execute() {
	if !p.parsed {
		return
	}
	for ch := 4; ch >= 0; ch-- {
		h := p.frame[ch]
		if !h.device || h.skip {
			continue
		}
		rxOff := h.off + 1
		// The PIF clears the RX flag bits before the
		// exchange, then sets error flags as needed.
		p.RAM[rxOff] &^= rxNoDevice | rxTimeout
		tx := p.RAM[h.off+2 : h.off+2+h.txLen]
		rx := p.RAM[h.off+2+h.txLen : h.off+2+h.txLen+h.rxLen]
		reply, outcome := p.dispatch(ch, tx)
		switch outcome {
		case outcomeNoDevice:
			p.RAM[rxOff] |= rxNoDevice
		case outcomeSilent:
			p.RAM[rxOff] |= rxTimeout
		default:
			if len(reply) < h.rxLen {
				p.RAM[rxOff] |= rxTimeout
			}
			copy(rx, reply)
		}
	}
}

// dispatch outcomes for one Joybus exchange.
const (
	outcomeAnswered = iota // the device replied
	outcomeSilent          // a device is there but did not reply (timeout)
	outcomeNoDevice        // nothing is attached to the channel
)

// dispatch routes one Joybus command to a channel's
// device.
func (p *PIF) dispatch(ch int, tx []byte) ([]byte, int) {
	if len(tx) == 0 {
		return nil, outcomeSilent
	}
	if ch == 4 {
		return p.dispatchCart(tx)
	}
	return p.dispatchController(ch, tx)
}

// controllerInfo builds the 3-byte Info reply for a
// controller channel: device 0x0500 plus the accessory
// status byte (1 inserted, 2 absent, 3 newly inserted,
// with bit 2 reporting a previous checksum error).
func (p *PIF) controllerInfo(ch int) []byte {
	c := &p.channels[ch]
	status := byte(0x02)
	if c.pak != PakNone {
		if c.pakFresh {
			status = 0x03
			c.pakFresh = false
		} else {
			status = 0x01
		}
	}
	if c.checksumError {
		status |= 0x04
		c.checksumError = false
	}
	return []byte{0x05, 0x00, status}
}

func (p *PIF) dispatchController(ch int, tx []byte) ([]byte, int) {
	c := &p.channels[ch]
	if !c.attached {
		return nil, outcomeNoDevice
	}
	switch tx[0] {
	case joyInfo, joyInfoAlt:
		return p.controllerInfo(ch), outcomeAnswered
	case joyReadState:
		return []byte{
			byte(c.buttons >> 8), byte(c.buttons),
			byte(c.stickX), byte(c.stickY),
		}, outcomeAnswered
	case joyPakRead, joyPakWrite:
		return p.pakAccess(c, tx)
	}
	return nil, outcomeSilent // unknown command: the device stays silent
}

// pakAccess serves controller pak read and write
// commands. The address is the 16-bit value in tx[1..2]
// whose low 5 bits carry the address checksum; the
// data area is 32 bytes. An absent or still-fresh pak
// behaves as documented: reads return zero-filled data
// with a complemented CRC, writes answer only the
// complemented CRC.
func (p *PIF) pakAccess(c *channelState, tx []byte) ([]byte, int) {
	isWrite := tx[0] == joyPakWrite
	if len(tx) < 3 || (isWrite && len(tx) < 35) {
		return nil, outcomeSilent
	}
	addr := uint16(tx[1])<<8 | uint16(tx[2])
	if uint16(AddressChecksum(addr)) != addr&0x1F {
		c.checksumError = true
		return nil, outcomeSilent // bad address checksum: no reply
	}
	base := int(addr & 0xFFE0)
	absent := c.pak == PakNone || c.pakFresh
	if isWrite {
		data := tx[3:35]
		crc := DataCRC(data)
		if absent {
			return []byte{^crc}, outcomeAnswered
		}
		if c.pak == PakMemory {
			copy(c.pakData[base&0x7FFF:], data)
		} else if c.pak == PakRumble && base == 0x8000 {
			c.rumbleOn = data[0]&1 != 0
		}
		return []byte{crc}, outcomeAnswered
	}
	// Read.
	if absent {
		out := make([]byte, 33)
		out[32] = 0xFF // CRC of 32 zero bytes is 0; complemented
		return out, outcomeAnswered
	}
	out := make([]byte, 33)
	if c.pak == PakMemory {
		copy(out, c.pakData[base&0x7FFF:])
	}
	out[32] = DataCRC(out[:32])
	return out, outcomeAnswered
}

// dispatchCart serves channel 4: the cartridge EEPROM
// (and the RTC commands, which have no device here).
func (p *PIF) dispatchCart(tx []byte) ([]byte, int) {
	switch tx[0] {
	case joyInfo, joyInfoAlt:
		if p.cart != nil {
			if id, ok := p.cart.EEPROMInfoID(); ok {
				return []byte{byte(id >> 8), byte(id), 0x00}, outcomeAnswered
			}
		}
		return []byte{0x00, 0x00, 0x00}, outcomeAnswered
	case joyEEPromRead:
		if p.cart == nil || len(tx) < 2 {
			return nil, outcomeSilent
		}
		blk, ok := p.cart.EEPROMRead(tx[1])
		if !ok {
			return nil, outcomeSilent
		}
		return blk[:], outcomeAnswered
	case joyEEPromWrite:
		if p.cart == nil || len(tx) < 10 {
			return nil, outcomeSilent
		}
		var blk [8]byte
		copy(blk[:], tx[2:10])
		if !p.cart.EEPROMWrite(tx[1], blk) {
			return nil, outcomeSilent
		}
		return []byte{0x00}, outcomeAnswered
	case joyRTCInfo:
		// No RTC is fitted: the documented answer is
		// three zero bytes.
		return []byte{0x00, 0x00, 0x00}, outcomeAnswered
	}
	return nil, outcomeSilent
}

// DataCRC is the Joybus data checksum: CRC-8 with
// polynomial 0x85 and initial value 0x00, computed
// MSB-first, as documented for the pak read/write
// commands.
func DataCRC(data []byte) byte {
	crc := byte(0)
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ 0x85
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// addressChecksumTable holds the documented per-bit
// XOR values for address bits 15 down to 5.
var addressChecksumTable = [11]byte{
	0x01, 0x1A, 0x0D, 0x1C, 0x0E, 0x07, 0x19, 0x16, 0x0B, 0x1F, 0x15,
}

// AddressChecksum computes the 5-bit Joybus address
// checksum over the top 11 bits of addr (not a CRC: a
// positional XOR, per the documentation).
func AddressChecksum(addr uint16) byte {
	var sum byte
	for i := 0; i < 11; i++ {
		bit := 15 - i
		if addr&(1<<uint(bit)) != 0 {
			sum ^= addressChecksumTable[i]
		}
	}
	return sum & 0x1F
}

// StateSize is the fixed byte size of the PIF runtime
// block in a system save state: the four paks' storage,
// per-channel accessory runtime flags, the global flags
// byte, and the parsed frame (so a state saved between
// a frame's parse and its execution restores exactly).
//
// Layout: 4 x 32768 bytes of pak data, then per channel
// one flags byte (bits 0..1 pak kind, bit 2 fresh, bit
// 3 rumble on, bit 4 checksum error, bit 5 detached),
// then the parsed and boot-done flags byte, then per
// channel four frame bytes (PIF RAM offset, TX length,
// RX length, flags: bit 0 parsed, bit 1 skip).
const StateSize = 4*PakSize + 4 + 1 + 5*4

// Serialize writes the PIF runtime state (PIF RAM
// itself is serialized by the bus owner; here: pak
// storage and accessory flags) into out.
func (p *PIF) Serialize(out []byte) {
	for ch := 0; ch < 4; ch++ {
		copy(out[ch*PakSize:], p.channels[ch].pakData[:])
		var flags byte
		flags |= byte(p.channels[ch].pak) & 0x03
		if p.channels[ch].pakFresh {
			flags |= 0x04
		}
		if p.channels[ch].rumbleOn {
			flags |= 0x08
		}
		if p.channels[ch].checksumError {
			flags |= 0x10
		}
		if !p.channels[ch].attached {
			flags |= 0x20
		}
		out[4*PakSize+ch] = flags
	}
	var f byte
	if p.parsed {
		f |= 0x01
	}
	if p.bootDone {
		f |= 0x02
	}
	out[4*PakSize+4] = f
	for ch := 0; ch < 5; ch++ {
		h := p.frame[ch]
		base := 4*PakSize + 5 + ch*4
		out[base] = byte(h.off)
		out[base+1] = byte(h.txLen)
		out[base+2] = byte(h.rxLen)
		var hf byte
		if h.device {
			hf |= 0x01
		}
		if h.skip {
			hf |= 0x02
		}
		out[base+3] = hf
	}
}

// Deserialize restores the PIF runtime state,
// including the parsed frame.
func (p *PIF) Deserialize(in []byte) {
	for ch := 0; ch < 4; ch++ {
		copy(p.channels[ch].pakData[:], in[ch*PakSize:])
		flags := in[4*PakSize+ch]
		p.channels[ch].pak = PakKind(flags & 0x03)
		p.channels[ch].pakFresh = flags&0x04 != 0
		p.channels[ch].rumbleOn = flags&0x08 != 0
		p.channels[ch].checksumError = flags&0x10 != 0
		p.channels[ch].attached = flags&0x20 == 0
	}
	f := in[4*PakSize+4]
	p.parsed = f&0x01 != 0
	p.bootDone = f&0x02 != 0
	for ch := 0; ch < 5; ch++ {
		base := 4*PakSize + 5 + ch*4
		hf := in[base+3]
		p.frame[ch] = handshake{
			off:    int(in[base]),
			txLen:  int(in[base+1]),
			rxLen:  int(in[base+2]),
			device: hf&0x01 != 0,
			skip:   hf&0x02 != 0,
		}
	}
}
