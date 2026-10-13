// Package si models the GameCube serial interface (SI),
// the controller bus at MMIO window base 0x0C006400,
// written from published SI documentation. Four controller
// channels are polled into input buffers, and a command
// transfer engine exchanges byte streams with the pad on
// one channel through the output buffer and the 128-byte
// I/O buffer window.
//
// Polling is simplified: hardware polls at vblank, here a
// write to the POLL register with enable bits polls at
// once, and the system calls PollFrame once per frame to
// re-poll while any channel stays enabled.
package si

import (
	"encoding/binary"
	"errors"
)

// Pad is one controller state, as reported by the provider.
// Buttons uses the system pad bit order (PadA is 0x0100 and
// so on); it is passed through unchanged in reports.
type Pad struct {
	Buttons            uint16
	TriggerL, TriggerR uint8
	StickX, StickY     uint8
	CStickX, CStickY   uint8
}

// Register offsets. Channel registers stride 0x0C.
const (
	regOutBuf = 0x00
	regInHi   = 0x04
	regInLo   = 0x08

	offPoll   = 0x30
	offComCSR = 0x34
	offStatus = 0x38
	offIOBuf  = 0x40
	ioBufSize = 128
)

// COMCSR bits.
const (
	comTSTART     = 1 << 0
	comCOMERR     = 1 << 28 // status, write 1 to clear
	comRDSTINTMSK = 1 << 30
	comRDSTINT    = 1 << 31 // status, write 1 to clear

	// Config bits stored from a COMCSR write: channel,
	// output length, input length and the interrupt mask.
	// Bit 28 overlaps the top input length bit in this
	// layout and also carries COMERR on reads.
	comCfgMask = 0x7F3F0006
)

// SI is the serial interface state.
type SI struct {
	provide func() [4]Pad

	// Connected marks which channels have a controller.
	// Channel 0 defaults to connected.
	Connected [4]bool

	outBuf [4]uint32
	inHi   [4]uint32
	inLo   [4]uint32

	poll     uint32
	comCfg   uint32 // stored COMCSR config bits
	comStage uint32 // staging for the in-progress COMCSR write
	comErr   bool
	rdstInt  bool
	rdst     [4]bool

	ioBuf [ioBufSize]byte
	motor [4]int
}

// New returns an SI in its reset state sampling pads from
// the provider, which is called at poll and transfer time.
func New(pads func() [4]Pad) *SI {
	s := &SI{provide: pads}
	s.Connected = [4]bool{true, false, false, false}
	s.Reset()
	return s
}

// Reset clears every register, the I/O buffer and the
// rumble motors. The Connected set is system wiring and
// survives a reset.
func (s *SI) Reset() {
	s.outBuf = [4]uint32{}
	s.inHi = [4]uint32{}
	s.inLo = [4]uint32{}
	s.poll = 0
	s.comCfg, s.comStage = 0, 0
	s.comErr, s.rdstInt = false, false
	s.rdst = [4]bool{}
	s.ioBuf = [ioBufSize]byte{}
	s.motor = [4]int{}
}

func (s *SI) pads() [4]Pad {
	if s.provide == nil {
		return [4]Pad{}
	}
	return s.provide()
}

// reg32 reads the 32-bit register containing off.
func (s *SI) reg32(off uint32) (uint32, bool) {
	switch {
	case off < 0x30:
		ch := off / 0x0C
		switch off % 0x0C &^ 3 {
		case regOutBuf:
			return s.outBuf[ch], true
		case regInHi:
			return s.inHi[ch], true
		case regInLo:
			return s.inLo[ch], true
		}
	case off&^3 == offPoll:
		return s.poll, true
	case off&^3 == offComCSR:
		v := s.comCfg
		if s.comErr {
			v |= comCOMERR
		}
		if s.rdstInt {
			v |= comRDSTINT
		}
		return v, true
	case off&^3 == offStatus:
		var v uint32
		for c := 0; c < 4; c++ {
			if s.rdst[c] {
				v |= 1 << uint(28+c)
			}
		}
		return v, true
	}
	return 0, false
}

// Read16 reads one 16-bit lane of a register, or two I/O
// buffer bytes big-endian inside the buffer window.
// Reading a channel INBUF_LO clears that channel RDST bit
// in STATUS.
func (s *SI) Read16(off uint32) uint16 {
	if off >= offIOBuf && off < offIOBuf+ioBufSize {
		i := off - offIOBuf
		var v uint16
		v = uint16(s.ioBuf[i]) << 8
		if i+1 < ioBufSize {
			v |= uint16(s.ioBuf[i+1])
		}
		return v
	}
	v, ok := s.reg32(off)
	if !ok {
		return 0
	}
	if off < 0x30 && off%0x0C&^3 == regInLo {
		s.rdst[off/0x0C] = false
	}
	if off&2 == 0 {
		return uint16(v >> 16)
	}
	return uint16(v)
}

// Write16 writes one 16-bit lane of a register, or two I/O
// buffer bytes inside the buffer window. Side effects
// (POLL polling, COMCSR transfers) fire on the low lane
// write, once the full register value is assembled. INBUF
// and STATUS are read only.
func (s *SI) Write16(off uint32, v uint16) {
	if off >= offIOBuf && off < offIOBuf+ioBufSize {
		i := off - offIOBuf
		s.ioBuf[i] = byte(v >> 8)
		if i+1 < ioBufSize {
			s.ioBuf[i+1] = byte(v)
		}
		return
	}
	high := off&2 == 0
	switch {
	case off < 0x30:
		ch := off / 0x0C
		if off%0x0C&^3 != regOutBuf {
			return // INBUF writes are ignored
		}
		if high {
			s.outBuf[ch] = s.outBuf[ch]&0x0000FFFF | uint32(v)<<16
		} else {
			s.outBuf[ch] = s.outBuf[ch]&0xFFFF0000 | uint32(v)
		}
	case off&^3 == offPoll:
		if high {
			s.poll = s.poll&0x0000FFFF | uint32(v)<<16
		} else {
			s.poll = s.poll&0xFFFF0000 | uint32(v)
			if s.poll&0xF != 0 {
				s.pollEnabled()
			}
		}
	case off&^3 == offComCSR:
		if high {
			s.comStage = s.comStage&0x0000FFFF | uint32(v)<<16
		} else {
			s.comStage = s.comStage&0xFFFF0000 | uint32(v)
			s.applyComCSR(s.comStage)
		}
	}
}

// applyComCSR folds a completed COMCSR write in: the two
// status bits are write 1 to clear, config bits are
// stored, and TSTART runs a transfer at once.
func (s *SI) applyComCSR(written uint32) {
	if written&comRDSTINT != 0 {
		s.rdstInt = false
	}
	if written&comCOMERR != 0 {
		s.comErr = false
	}
	s.comCfg = written & comCfgMask
	s.comStage = s.comCfg
	if written&comTSTART != 0 {
		s.runTransfer()
	}
}

// packPad packs one pad report into an INBUF word pair.
// A disconnected channel answers with the error packet:
// the buttons word 0x8000 and zeros everywhere else.
func packPad(p Pad, connected bool) (hi, lo uint32) {
	if !connected {
		return 0x8000 << 16, 0
	}
	buttons := p.Buttons &^ 0x8000
	hi = uint32(buttons)<<16 | uint32(p.StickX)<<8 | uint32(p.StickY)
	lo = uint32(p.CStickX)<<24 | uint32(p.CStickY)<<16 |
		uint32(p.TriggerL)<<8 | uint32(p.TriggerR)
	return hi, lo
}

func (s *SI) pollChannel(c int, pads [4]Pad) {
	s.inHi[c], s.inLo[c] = packPad(pads[c], s.Connected[c])
	s.rdst[c] = true
}

func (s *SI) pollEnabled() {
	pads := s.pads()
	for c := 0; c < 4; c++ {
		if s.poll&(1<<uint(c)) != 0 {
			s.pollChannel(c, pads)
		}
	}
}

// PollFrame re-polls every enabled channel. The system
// calls it once per frame.
func (s *SI) PollFrame() {
	if s.poll&0xF != 0 {
		s.pollEnabled()
	}
}

// Motor returns the last rumble motor byte sent to a
// channel by the get status command (0 for none, or for
// an out of range channel).
func (s *SI) Motor(ch int) int {
	if ch < 0 || ch > 3 {
		return 0
	}
	return s.motor[ch]
}

// IRQAsserted reports the SI interrupt output.
func (s *SI) IRQAsserted() bool {
	return s.rdstInt && s.comCfg&comRDSTINTMSK != 0
}

// runTransfer executes the COMCSR transfer on the selected
// channel. The output stream is the channel OUTBUF word
// followed by I/O buffer bytes, OUTLNGTH bytes in total
// (zero means the OUTBUF word alone, 4). The response is
// truncated or zero padded to INLNGTH bytes and written
// into the I/O buffer at offset 0. Status-class responses
// (origin and get status, plus the disconnected error
// packet) are also packed into the channel INBUF pair.
func (s *SI) runTransfer() {
	ch := int(s.comCfg>>1) & 3
	outLen := int(s.comCfg>>16) & 0x3F
	if outLen == 0 {
		outLen = 4
	}
	inLen := int(s.comCfg>>24) & 0x3F

	out := make([]byte, 0, outLen)
	var word [4]byte
	binary.BigEndian.PutUint32(word[:], s.outBuf[ch])
	for i := 0; i < outLen; i++ {
		if i < 4 {
			out = append(out, word[i])
		} else if i-4 < ioBufSize {
			out = append(out, s.ioBuf[i-4])
		} else {
			out = append(out, 0)
		}
	}
	cmd := out[0]

	connected := s.Connected[ch]
	pad := s.pads()[ch]

	var resp []byte
	statusClass := false
	if !connected {
		resp = []byte{0x80, 0x00, 0, 0, 0, 0, 0, 0}
		statusClass = true
	} else {
		switch cmd {
		case 0x00, 0xFF: // info or reset: type ID 0x0900 plus status
			resp = []byte{0x09, 0x00, 0x00}
		case 0x41: // origin
			resp = statusPacket(pad)
			resp = append(resp, 0, 0)
			statusClass = true
		case 0x42: // get status; second output byte is rumble
			if len(out) >= 2 {
				s.motor[ch] = int(out[1])
			}
			resp = statusPacket(pad)
			statusClass = true
		default:
			resp = nil // unknown commands answer with zeros
		}
	}
	for i := 0; i < inLen && i < ioBufSize; i++ {
		var b byte
		if i < len(resp) {
			b = resp[i]
		}
		s.ioBuf[i] = b
	}
	if statusClass && inLen > 0 {
		var pkt [8]byte
		copy(pkt[:], resp)
		s.inHi[ch] = binary.BigEndian.Uint32(pkt[0:4])
		s.inLo[ch] = binary.BigEndian.Uint32(pkt[4:8])
	}
	s.comErr = !connected
	s.rdstInt = true
	s.rdst[ch] = true
}

// statusPacket builds the eight-byte status response:
// buttons big-endian, then the six analog channels.
func statusPacket(p Pad) []byte {
	buttons := p.Buttons &^ 0x8000
	return []byte{
		byte(buttons >> 8), byte(buttons),
		p.StickX, p.StickY, p.CStickX, p.CStickY,
		p.TriggerL, p.TriggerR,
	}
}

// SerializeSize is the fixed save state size: a version
// byte, the channel register triples, POLL and the COMCSR
// config, the status flags, the I/O buffer, the motors and
// the Connected set.
const SerializeSize = 1 + 4*3*4 + 4 + 4 + 1 + 1 + ioBufSize + 4 + 4

// ErrBadState reports a state block of the wrong size or
// version.
var ErrBadState = errors.New("si: bad save state")

const serializeVersion = 1

// Serialize writes the SI state covered by SerializeSize.
func (s *SI) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	off := 1
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(out[off:], v)
		off += 4
	}
	for c := 0; c < 4; c++ {
		put32(s.outBuf[c])
		put32(s.inHi[c])
		put32(s.inLo[c])
	}
	put32(s.poll)
	put32(s.comCfg)
	var flags byte
	if s.comErr {
		flags |= 1
	}
	if s.rdstInt {
		flags |= 2
	}
	out[off] = flags
	off++
	var rdst byte
	for c := 0; c < 4; c++ {
		if s.rdst[c] {
			rdst |= 1 << uint(c)
		}
	}
	out[off] = rdst
	off++
	off += copy(out[off:], s.ioBuf[:])
	for c := 0; c < 4; c++ {
		out[off] = byte(s.motor[c])
		off++
	}
	for c := 0; c < 4; c++ {
		if s.Connected[c] {
			out[off] = 1
		}
		off++
	}
	if off != SerializeSize {
		panic("si: serialize size mismatch")
	}
	return out
}

// Deserialize restores a state written by Serialize. The
// pad provider is wiring and is not part of the state.
func (s *SI) Deserialize(in []byte) error {
	if len(in) != SerializeSize || in[0] != serializeVersion {
		return ErrBadState
	}
	off := 1
	get32 := func() uint32 {
		v := binary.BigEndian.Uint32(in[off:])
		off += 4
		return v
	}
	for c := 0; c < 4; c++ {
		s.outBuf[c] = get32()
		s.inHi[c] = get32()
		s.inLo[c] = get32()
	}
	s.poll = get32()
	s.comCfg = get32()
	s.comStage = s.comCfg
	flags := in[off]
	off++
	s.comErr = flags&1 != 0
	s.rdstInt = flags&2 != 0
	rdst := in[off]
	off++
	for c := 0; c < 4; c++ {
		s.rdst[c] = rdst&(1<<uint(c)) != 0
	}
	off += copy(s.ioBuf[:], in[off:])
	for c := 0; c < 4; c++ {
		s.motor[c] = int(in[off])
		off++
	}
	for c := 0; c < 4; c++ {
		s.Connected[c] = in[off] != 0
		off++
	}
	return nil
}
