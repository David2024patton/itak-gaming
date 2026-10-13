package scd

import "encoding/binary"

// This file implements the Sega CD CDD host interface and CD-DA
// playback for the scd package, following the reverse engineered
// CDD protocol documentation (CDD_inf.txt) and the behaviour of
// the Sega CD sub BIOS.
//
// Register model summary: the fader word lives at sub offsets
// 0x34 (high byte) and 0x35 (low byte), the HOCK control bit is
// bit2 of the byte written at sub offset 0x37, status nibbles
// RS0 to RS9 live at sub offsets 0x38 to 0x41, and command
// nibbles TC0 to TC9 live at sub offsets 0x42 to 0x4B. The Unit
// wiring that maps those offsets onto this type belongs to the
// coordinator; this type exposes byte and nibble methods only.
//
// Documented simplifications:
//   - Fades apply instantly, so the fader EFDT status bit always
//     reads 1 and the gain is the fader value in effect now.
//   - Commands apply when TC9 is written, but the response packet
//     is built into a pending slot at that moment and only becomes
//     visible in RS0 to RS9 at the next 75 Hz tick, which is also
//     when the interrupt pending flag sets.
//   - Scanning runs at 8x normal speed. The protocol document
//     does not publish a scan rate, so 8x is a model choice.
//   - Unknown commands 0x05, 0x0A, 0x0B, 0x0E and 0x0F, and any
//     other unlisted code, are treated as a NOP that returns a
//     plain status packet. Command 0x05 is unknown on real
//     hardware as far as the documentation records.
//   - Play and Seek targets with invalid BCD, an invalid MSF, or
//     an LBA outside the disc are rejected with a plain status
//     packet and no state change.

// CDDStateSize is the exact serialized size of a CDD. The layout
// is: state (1), display status (1), flags (1: bit0 status
// pending, bit1 status valid, bit2 interrupt pending), settle
// counter (1), RS nibbles packed (5), TC nibbles packed (5),
// pending packet nibbles packed (5), reserved (1), play LBA
// (4, big endian signed), play sample in sector (2), play sample
// accumulator (4), render LBA (4), render sample in sector (2),
// fader FD (2), fader misc bits (1), fader high byte latch (1),
// 75 Hz tick accumulator (4), data read LBA (4). Total 52 bytes.
const CDDStateSize = 52

// CDDState is the transport state of the CDD.
type CDDState int

const (
	CDDStopped CDDState = iota
	CDDPlaying
	CDDPaused
	CDDScanFF
	CDDScanFR
	CDDReading
	CDDTrayOpen
)

// CDDEvents reports what one AdvanceCycles call delivered.
// DataLBA is -1 and DataCount is 0 when no data sector was
// delivered. Otherwise DataLBA is the first delivered sector and
// DataCount is the number of consecutive sectors delivered.
type CDDEvents struct {
	DataLBA   int
	DataCount int
}

// CDD is the CD drive controller model. It is constructed with a
// disc, which may be nil for an empty drive, and it is driven by
// sub CPU cycle counts through AdvanceCycles.
type CDD struct {
	disc *Disc

	state   CDDState
	display uint8

	statusPending bool
	statusValid   bool
	irqPending    bool
	settle        uint8

	rs      [10]uint8
	tc      [10]uint8
	pending [10]uint8

	playLBA    int
	playSample int
	playAcc    int

	renderLBA    int
	renderSample int

	faderFD    uint16
	faderMisc  uint8
	faderLatch uint8

	tickAcc int
	readLBA int

	cacheValid bool
	cacheLBA   int
	cacheData  []byte
}

// NewCDD builds a CDD with the given disc attached. The disc may
// be nil, which models an empty drive. Power on state is stopped,
// the fader is at unity gain (FD 1024), and RS holds a valid
// checksum status packet reporting stopped (0x09) with a disc or
// no disc (0x0B) without one.
func NewCDD(d *Disc) *CDD {
	c := &CDD{
		disc:       d,
		state:      CDDStopped,
		display:    0x0B,
		faderFD:    1024,
		faderLatch: 0x40,
		cacheLBA:   -1,
	}
	if d != nil {
		c.display = 0x09
	}
	c.rs[0] = c.display
	setPacketChecksum(&c.rs)
	c.statusValid = true
	return c
}

// ---------------------------------------------------------------------------
// Fader, control, and packet registers
// ---------------------------------------------------------------------------

// WriteFaderByte writes one fader byte. off 0 is the sub offset
// 0x34 high byte, which is only latched, and off 1 is the sub
// offset 0x35 low byte, which commits the full fader word from
// the latch plus the low byte.
func (c *CDD) WriteFaderByte(off uint8, v uint8) {
	switch off {
	case 0:
		c.faderLatch = v
	case 1:
		word := uint16(c.faderLatch&0x7F)<<8 | uint16(v)
		c.faderFD = uint16(word>>4) & 0x7FF
		c.faderMisc = v & 0x0F
	}
}

// ReadFaderByte reads one fader byte, with the EFDT status bit
// (bit15 of the word, bit7 of the high byte) always set because
// fades apply instantly in this model.
func (c *CDD) ReadFaderByte(off uint8) uint8 {
	switch off {
	case 0:
		return 0x80 | uint8((c.faderFD>>4)&0x7F)
	case 1:
		return uint8(c.faderFD&0x0F)<<4 | (c.faderMisc & 0x0F)
	}
	return 0
}

// WriteControl writes the CDD control byte at sub offset 0x37.
// Bit2 is HOCK: writing it set hands the status transfer back and
// clears the status valid flag. Other bits are ignored.
func (c *CDD) WriteControl(v uint8) {
	if v&0x04 != 0 {
		c.statusValid = false
	}
}

// WriteCommandNibble writes command nibble idx (0 to 9), masked
// to four bits. Writing nibble 9, the checksum nibble, triggers
// command execution.
func (c *CDD) WriteCommandNibble(idx int, v uint8) {
	if idx < 0 || idx >= len(c.tc) {
		return
	}
	c.tc[idx] = v & 0x0F
	if idx == 9 {
		c.executeCommand()
	}
}

// StatusNibble returns status nibble idx (0 to 9), or 0 out of
// range.
func (c *CDD) StatusNibble(idx int) uint8 {
	if idx < 0 || idx >= len(c.rs) {
		return 0
	}
	return c.rs[idx]
}

// CommandNibble returns the stored command nibble idx (0 to 9),
// or 0 out of range.
func (c *CDD) CommandNibble(idx int) uint8 {
	if idx < 0 || idx >= len(c.tc) {
		return 0
	}
	return c.tc[idx]
}

// StatusValid reports whether RS0 to RS9 hold a status packet
// that has been delivered and not yet handed back with HOCK.
func (c *CDD) StatusValid() bool { return c.statusValid }

// TakeInterrupt returns and clears the interrupt pending flag.
// The flag sets at the 75 Hz tick that delivers a response
// packet.
func (c *CDD) TakeInterrupt() bool {
	v := c.irqPending
	c.irqPending = false
	return v
}

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

// State returns the current transport state.
func (c *CDD) State() CDDState { return c.state }

// DisplayStatus returns the current display status code, the
// value a Get status command reports in RS0.
func (c *CDD) DisplayStatus() uint8 { return c.display }

// PositionLBA returns the current play position as an LBA. At the
// end of the disc this is the total sector count.
func (c *CDD) PositionLBA() int { return c.playLBA }

// PositionSample returns the current play position within its
// sector, in stereo frames from 0 to 587.
func (c *CDD) PositionSample() int { return c.playSample }

// PositionMSF returns the current play position as MSF.
func (c *CDD) PositionMSF() (m, s, f int) { return LBAToMSF(c.playLBA) }

// FaderFD returns the current fader value, 0 to 2047, where 1024
// is unity gain.
func (c *CDD) FaderFD() int { return int(c.faderFD) }

// ---------------------------------------------------------------------------
// Disc helpers
// ---------------------------------------------------------------------------

func (c *CDD) totalSectors() int {
	if c.disc == nil {
		return 0
	}
	return c.disc.NumSectors()
}

func (c *CDD) trackAt(lba int) *Track {
	if c.disc == nil {
		return nil
	}
	return c.disc.trackForLBA(lba)
}

// currentTrack returns the track at the play position. A position
// at the very end of the disc reports the last track.
func (c *CDD) currentTrack() *Track {
	if c.disc == nil {
		return nil
	}
	if tr := c.disc.trackForLBA(c.playLBA); tr != nil {
		return tr
	}
	tracks := c.disc.tracks
	if len(tracks) == 0 {
		return nil
	}
	if c.playLBA >= c.totalSectors() {
		return &tracks[len(tracks)-1]
	}
	return nil
}

func (c *CDD) playTotalSamples() int {
	return c.playLBA*588 + c.playSample
}

func (c *CDD) renderTotalSamples() int {
	return c.renderLBA*588 + c.renderSample
}

func (c *CDD) setPlayTotalSamples(total int) {
	if total < 0 {
		total = 0
	}
	c.playLBA = total / 588
	c.playSample = total % 588
}

func (c *CDD) snapRenderToPlay() {
	c.renderLBA = c.playLBA
	c.renderSample = c.playSample
}

// ---------------------------------------------------------------------------
// Packet construction
// ---------------------------------------------------------------------------

// setPacketChecksum sets nibble 9 of a packet so that the sum of
// all ten nibbles is congruent to 0xF modulo 16.
func setPacketChecksum(p *[10]uint8) {
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(p[i])
	}
	p[9] = uint8((^sum) & 0xF)
}

// setPacketMSF writes m, s and f as BCD nibble pairs into packet
// nibbles 2 to 7.
func setPacketMSF(p *[10]uint8, m, s, f int) {
	p[2] = uint8(m / 10 % 10)
	p[3] = uint8(m % 10)
	p[4] = uint8(s / 10 % 10)
	p[5] = uint8(s % 10)
	p[6] = uint8(f / 10 % 10)
	p[7] = uint8(f % 10)
}

func setPacketMSFFromLBA(p *[10]uint8, lba int) {
	m, s, f := LBAToMSF(lba)
	setPacketMSF(p, m, s, f)
}

// statusPacket builds a plain status packet: RS0 is the display
// status, RS1 is 0, RS2 to RS7 are the current absolute position
// MSF, and RS8 is 0. With no disc the position fields are zero.
func (c *CDD) statusPacket() [10]uint8 {
	var p [10]uint8
	p[0] = c.display
	if c.disc != nil {
		setPacketMSFFromLBA(&p, c.playLBA)
	}
	setPacketChecksum(&p)
	return p
}

// queueResponse stores a response packet in the pending slot. It
// becomes visible in RS at the next 75 Hz tick.
func (c *CDD) queueResponse(p [10]uint8) {
	c.pending = p
	c.statusPending = true
	c.statusValid = false
}

// trackPacket builds the Play and Resume response: RS0 0x01, RS1
// 0x02, and the track number in BCD in RS2 and RS3.
func trackPacket(tr *Track) [10]uint8 {
	var p [10]uint8
	p[0] = 0x01
	p[1] = 0x02
	if tr != nil {
		p[2] = uint8(tr.Number / 10 % 10)
		p[3] = uint8(tr.Number % 10)
	}
	setPacketChecksum(&p)
	return p
}

// ---------------------------------------------------------------------------
// Command execution
// ---------------------------------------------------------------------------

// targetLBA decodes and validates the BCD MSF target in TC2 to
// TC7. It reports false for any nibble over 9, seconds at or over
// 60, frames at or over 75, or an LBA outside the disc tracks.
func (c *CDD) targetLBA() (int, bool) {
	for i := 2; i <= 7; i++ {
		if c.tc[i] > 9 {
			return 0, false
		}
	}
	m := int(c.tc[2])*10 + int(c.tc[3])
	s := int(c.tc[4])*10 + int(c.tc[5])
	f := int(c.tc[6])*10 + int(c.tc[7])
	if s >= 60 || f >= 75 {
		return 0, false
	}
	lba := MSFToLBA(m, s, f)
	if lba < 0 || lba >= c.totalSectors() || c.trackAt(lba) == nil {
		return 0, false
	}
	return lba, true
}

// executeCommand runs the command in TC once TC9 has been
// written. A command whose checksum fails is ignored entirely.
func (c *CDD) executeCommand() {
	sum := 0
	for i := 0; i < 10; i++ {
		sum += int(c.tc[i])
	}
	if sum&0xF != 0xF {
		return
	}
	code := c.tc[0]<<4 | c.tc[1]

	switch code {
	case 0x00: // Get status.
		c.queueResponse(c.statusPacket())

	case 0x01: // Stop.
		if c.state == CDDTrayOpen {
			c.queueResponse(c.statusPacket())
			return
		}
		c.state = CDDStopped
		if c.disc == nil {
			c.display = 0x0B
			c.settle = 0
		} else {
			c.display = 0x00
			c.settle = 1
		}
		c.queueResponse(c.statusPacket())

	case 0x02: // TOC info.
		c.queueResponse(c.tocPacket())

	case 0x03: // Read or Play.
		lba, ok := c.targetLBA()
		if c.state == CDDTrayOpen || c.disc == nil || !ok {
			c.queueResponse(c.statusPacket())
			return
		}
		tr := c.trackAt(lba)
		c.playLBA = lba
		c.playSample = 0
		c.playAcc = 0
		c.snapRenderToPlay()
		c.settle = 0
		c.display = 0x01
		if tr.Mode == TrackAudio {
			c.state = CDDPlaying
		} else {
			c.state = CDDReading
			c.readLBA = lba
		}
		c.queueResponse(trackPacket(tr))

	case 0x04: // Seek.
		lba, ok := c.targetLBA()
		if c.state == CDDTrayOpen || c.disc == nil || !ok {
			c.queueResponse(c.statusPacket())
			return
		}
		c.playLBA = lba
		c.playSample = 0
		c.playAcc = 0
		c.snapRenderToPlay()
		c.settle = 0
		c.state = CDDPaused
		c.display = 0x04
		var p [10]uint8
		p[0] = 0x02
		setPacketMSFFromLBA(&p, lba)
		setPacketChecksum(&p)
		c.queueResponse(p)

	case 0x06: // Pause.
		if c.disc == nil {
			var p [10]uint8
			p[0] = 0x00
			setPacketChecksum(&p)
			c.queueResponse(p)
			return
		}
		if c.state == CDDTrayOpen {
			c.queueResponse(c.statusPacket())
			return
		}
		if c.state == CDDPlaying || c.state == CDDScanFF || c.state == CDDScanFR || c.state == CDDReading {
			c.state = CDDPaused
			c.display = 0x04
		}
		p := c.statusPacket()
		p[0] = 0x04
		setPacketChecksum(&p)
		c.queueResponse(p)

	case 0x07: // Resume.
		if c.state != CDDPaused || c.disc == nil {
			c.queueResponse(c.statusPacket())
			return
		}
		tr := c.currentTrack()
		if tr == nil {
			c.queueResponse(c.statusPacket())
			return
		}
		c.settle = 0
		c.display = 0x01
		if tr.Mode == TrackAudio {
			c.state = CDDPlaying
		} else {
			c.state = CDDReading
			c.readLBA = c.playLBA
		}
		c.queueResponse(trackPacket(tr))

	case 0x08, 0x09: // Fast forward and fast reverse.
		if c.disc == nil || (c.state != CDDPlaying && c.state != CDDPaused) {
			c.queueResponse(c.statusPacket())
			return
		}
		if code == 0x08 {
			c.state = CDDScanFF
		} else {
			c.state = CDDScanFR
		}
		c.display = 0x03
		p := c.statusPacket()
		p[0] = 0x03
		setPacketChecksum(&p)
		c.queueResponse(p)

	case 0x0C: // Close tray.
		if c.state != CDDTrayOpen {
			c.queueResponse(c.statusPacket())
			return
		}
		c.state = CDDStopped
		c.display = 0x00
		c.settle = 1
		var p [10]uint8
		p[0] = 0x00
		if c.disc != nil {
			setPacketMSFFromLBA(&p, c.playLBA)
		}
		setPacketChecksum(&p)
		c.queueResponse(p)

	case 0x0D: // Open tray.
		c.state = CDDTrayOpen
		c.display = 0x05
		c.settle = 0
		c.snapRenderToPlay()
		var p [10]uint8
		p[0] = 0x05
		setPacketChecksum(&p)
		c.queueResponse(p)

	default:
		// 0x05, 0x0A, 0x0B, 0x0E, 0x0F and every other unlisted
		// code: NOP returning a plain status packet.
		c.queueResponse(c.statusPacket())
	}
}

// tocPacket builds the response for command 0x02, selected by
// TC3. RS1 echoes TC3 in every TOC response.
func (c *CDD) tocPacket() [10]uint8 {
	sel := c.tc[3]
	var p [10]uint8
	p[1] = sel
	if c.state == CDDTrayOpen || c.disc == nil {
		if c.state == CDDTrayOpen {
			p[0] = 0x05
		} else {
			p[0] = 0x0B
		}
		setPacketChecksum(&p)
		return p
	}
	switch sel {
	case 0x00: // Absolute position.
		p = c.statusPacket()
		p[1] = sel
	case 0x01: // Position relative to the current track.
		p = c.statusPacket()
		p[1] = sel
		rel := 0
		if tr := c.currentTrack(); tr != nil {
			rel = c.playLBA - tr.StartLBA
			if rel < 0 {
				rel = 0
			}
		}
		m := rel / (75 * 60)
		rem := rel % (75 * 60)
		setPacketMSF(&p, m, rem/75, rem%75)
	case 0x02: // Current track number.
		p[0] = c.display
		if tr := c.currentTrack(); tr != nil {
			p[2] = uint8(tr.Number / 10 % 10)
			p[3] = uint8(tr.Number % 10)
		}
	case 0x03: // Total disc length.
		p[0] = 0x04
		setPacketMSFFromLBA(&p, c.totalSectors())
	case 0x04: // First and last track numbers.
		p[0] = 0x04
		tracks := c.disc.tracks
		if len(tracks) > 0 {
			first := tracks[0].Number
			last := tracks[len(tracks)-1].Number
			p[2] = uint8(first / 10 % 10)
			p[3] = uint8(first % 10)
			p[4] = uint8(last / 10 % 10)
			p[5] = uint8(last % 10)
		}
	case 0x05: // Start MSF of a named track.
		req := int(c.tc[4])*10 + int(c.tc[5])
		p[0] = c.display
		p[8] = uint8(req & 0xF)
		for i := range c.disc.tracks {
			tr := &c.disc.tracks[i]
			if tr.Number == req {
				setPacketMSFFromLBA(&p, tr.StartLBA)
				if tr.Mode != TrackAudio {
					p[6] |= 0x08
				}
				break
			}
		}
	default: // 0x06 to 0x0F: status code, zeroed fields.
		p[0] = c.display
	}
	setPacketChecksum(&p)
	return p
}

// ---------------------------------------------------------------------------
// Time: ticks and playback progression
// ---------------------------------------------------------------------------

// AdvanceCycles runs the CDD for the given number of sub CPU
// cycles. It delivers pending response packets and data sectors
// on the 75 Hz tick, and it advances CD-DA playback and scanning
// in absolute sample space using exact accumulators.
func (c *CDD) AdvanceCycles(cycles int) CDDEvents {
	ev := CDDEvents{DataLBA: -1}
	if cycles <= 0 {
		return ev
	}
	remaining := cycles
	for remaining > 0 {
		// Cycles until the next 75 Hz tick. The tick accumulator
		// counts cycles times 3 against a 500000 threshold, which
		// is exactly 12500000 cycles per second divided by 75.
		toTick := (500000 - c.tickAcc + 2) / 3
		if toTick < 1 {
			toTick = 1
		}
		chunk := remaining
		if chunk > toTick {
			chunk = toTick
		}
		c.advanceSamples(chunk)
		c.tickAcc += chunk * 3
		remaining -= chunk
		if c.tickAcc >= 500000 {
			c.tickAcc -= 500000
			c.tick(&ev)
		}
	}
	return ev
}

// advanceSamples advances or retreats the play position for chunk
// sub CPU cycles when playing or scanning. Samples run at 44100
// per 12500000 cycles, accumulated exactly.
func (c *CDD) advanceSamples(chunk int) {
	if c.state != CDDPlaying && c.state != CDDScanFF && c.state != CDDScanFR {
		return
	}
	total := int64(c.playAcc) + int64(chunk)*441
	delta := total / 125000
	c.playAcc = int(total % 125000)
	if delta == 0 {
		return
	}
	cur := int64(c.playTotalSamples())
	var next int64
	switch c.state {
	case CDDScanFF:
		next = cur + delta*8
	case CDDScanFR:
		next = cur - delta*8
	default:
		next = cur + delta
	}
	end := int64(c.totalSectors()) * 588
	if c.state == CDDScanFR && next <= 0 {
		c.setPlayTotalSamples(0)
		c.stopAtBoundary()
		return
	}
	if next >= end {
		c.setPlayTotalSamples(int(end))
		c.stopAtBoundary()
		return
	}
	if next < 0 {
		next = 0
	}
	lba := int(next / 588)
	if tr := c.trackAt(lba); tr != nil && tr.Mode != TrackAudio {
		// Landed in a data track: clamp to its start and stop.
		c.setPlayTotalSamples(tr.StartLBA * 588)
		c.stopAtBoundary()
		c.snapRenderToPlay()
		return
	}
	c.setPlayTotalSamples(int(next))
}

// stopAtBoundary settles the transport into the stopped state at
// a playback boundary.
func (c *CDD) stopAtBoundary() {
	c.state = CDDStopped
	c.display = 0x09
	c.settle = 0
}

// tick runs one 75 Hz tick: settle a transient display status,
// deliver any pending response packet, and deliver one data
// sector when reading.
func (c *CDD) tick(ev *CDDEvents) {
	if c.settle > 0 {
		c.settle--
		if c.settle == 0 {
			if c.disc == nil {
				c.display = 0x0B
			} else {
				c.display = 0x09
			}
		}
	}
	if c.statusPending {
		c.rs = c.pending
		c.statusPending = false
		c.statusValid = true
		c.irqPending = true
	}
	if c.state != CDDReading {
		return
	}
	tr := c.trackAt(c.readLBA)
	if tr == nil || tr.Mode == TrackAudio {
		c.state = CDDStopped
		if c.disc == nil {
			c.display = 0x0B
		} else {
			c.display = 0x09
		}
		return
	}
	if ev.DataCount == 0 {
		ev.DataLBA = c.readLBA
	}
	ev.DataCount++
	delivered := c.readLBA
	last := tr.StartLBA + tr.Sectors - 1
	c.readLBA++
	c.playLBA = c.readLBA
	c.playSample = 0
	if delivered >= last {
		c.state = CDDStopped
		c.display = 0x09
	}
}

// ---------------------------------------------------------------------------
// CD-DA rendering
// ---------------------------------------------------------------------------

// RenderCDDA adds fader scaled CD-DA audio into a stereo int32
// accumulator holding len(acc)/2 frames. Rendering runs from a
// render position that trails the play position: it outputs the
// samples strictly behind the play position and then stays
// silent once caught up. In scanning states it outputs nothing
// and snaps the render position to the play position. Sectors
// that are not audio, or that fail to read, render as silence
// while the render position still advances.
func (c *CDD) RenderCDDA(acc []int32) {
	frames := len(acc) / 2
	if frames <= 0 {
		return
	}
	if c.state == CDDScanFF || c.state == CDDScanFR {
		c.snapRenderToPlay()
		return
	}
	if c.renderTotalSamples() > c.playTotalSamples() {
		c.snapRenderToPlay()
		return
	}
	out := 0
	for out < frames {
		behind := c.playTotalSamples() - c.renderTotalSamples()
		if behind <= 0 {
			return
		}
		take := behind
		if avail := 588 - c.renderSample; take > avail {
			take = avail
		}
		if rem := frames - out; take > rem {
			take = rem
		}
		data := c.audioSector(c.renderLBA)
		if data != nil {
			fd := int32(c.faderFD)
			for i := 0; i < take; i++ {
				src := (c.renderSample + i) * 4
				left := int32(int16(uint16(data[src]) | uint16(data[src+1])<<8))
				right := int32(int16(uint16(data[src+2]) | uint16(data[src+3])<<8))
				acc[out*2] += left * fd / 1024
				acc[out*2+1] += right * fd / 1024
				out++
			}
		} else {
			out += take
		}
		c.renderSample += take
		if c.renderSample >= 588 {
			c.renderSample = 0
			c.renderLBA++
		}
	}
}

// audioSector returns the raw sector bytes for an audio track
// LBA, or nil for silence. The last fetched sector is cached; the
// cache is not serialized.
func (c *CDD) audioSector(lba int) []byte {
	if c.cacheValid && c.cacheLBA == lba {
		return c.cacheData
	}
	var data []byte
	if tr := c.trackAt(lba); tr != nil && tr.Mode == TrackAudio && c.disc != nil {
		if raw, err := c.disc.ReadAudio(lba); err == nil {
			data = raw
		}
	}
	c.cacheValid = true
	c.cacheLBA = lba
	c.cacheData = data
	return data
}

// ---------------------------------------------------------------------------
// Save states
// ---------------------------------------------------------------------------

func packNibbles(dst []byte, nib *[10]uint8) {
	for i := 0; i < 5; i++ {
		dst[i] = (nib[i*2] & 0x0F) | (nib[i*2+1]&0x0F)<<4
	}
}

func unpackNibbles(src []byte, nib *[10]uint8) {
	for i := 0; i < 5; i++ {
		nib[i*2] = src[i] & 0x0F
		nib[i*2+1] = src[i] >> 4 & 0x0F
	}
}

// SerializeState writes exactly CDDStateSize bytes into buf. A
// buffer smaller than CDDStateSize is left untouched.
func (c *CDD) SerializeState(buf []byte) {
	if len(buf) < CDDStateSize {
		return
	}
	buf = buf[:CDDStateSize]
	for i := range buf {
		buf[i] = 0
	}
	buf[0] = byte(c.state)
	buf[1] = c.display
	var flags uint8
	if c.statusPending {
		flags |= 1 << 0
	}
	if c.statusValid {
		flags |= 1 << 1
	}
	if c.irqPending {
		flags |= 1 << 2
	}
	buf[2] = flags
	buf[3] = c.settle
	packNibbles(buf[4:9], &c.rs)
	packNibbles(buf[9:14], &c.tc)
	packNibbles(buf[14:19], &c.pending)
	// buf[19] is reserved.
	binary.BigEndian.PutUint32(buf[20:24], uint32(int32(c.playLBA)))
	binary.BigEndian.PutUint16(buf[24:26], uint16(c.playSample))
	binary.BigEndian.PutUint32(buf[26:30], uint32(c.playAcc))
	binary.BigEndian.PutUint32(buf[30:34], uint32(int32(c.renderLBA)))
	binary.BigEndian.PutUint16(buf[34:36], uint16(c.renderSample))
	binary.BigEndian.PutUint16(buf[36:38], c.faderFD)
	buf[38] = c.faderMisc
	buf[39] = c.faderLatch
	binary.BigEndian.PutUint32(buf[40:44], uint32(c.tickAcc))
	binary.BigEndian.PutUint32(buf[44:48], uint32(int32(c.readLBA)))
}

// DeserializeState restores state written by SerializeState. The
// disc pointer is not part of the state and is kept. The audio
// sector cache is invalidated, since disc reads are pure and a
// refetch is deterministic.
func (c *CDD) DeserializeState(buf []byte) {
	if len(buf) < CDDStateSize {
		return
	}
	buf = buf[:CDDStateSize]
	c.state = CDDState(buf[0])
	c.display = buf[1]
	flags := buf[2]
	c.statusPending = flags&(1<<0) != 0
	c.statusValid = flags&(1<<1) != 0
	c.irqPending = flags&(1<<2) != 0
	c.settle = buf[3]
	unpackNibbles(buf[4:9], &c.rs)
	unpackNibbles(buf[9:14], &c.tc)
	unpackNibbles(buf[14:19], &c.pending)
	c.playLBA = int(int32(binary.BigEndian.Uint32(buf[20:24])))
	c.playSample = int(binary.BigEndian.Uint16(buf[24:26]))
	c.playAcc = int(binary.BigEndian.Uint32(buf[26:30]))
	c.renderLBA = int(int32(binary.BigEndian.Uint32(buf[30:34])))
	c.renderSample = int(binary.BigEndian.Uint16(buf[34:36]))
	c.faderFD = binary.BigEndian.Uint16(buf[36:38])
	c.faderMisc = buf[38]
	c.faderLatch = buf[39]
	c.tickAcc = int(binary.BigEndian.Uint32(buf[40:44]))
	c.readLBA = int(int32(binary.BigEndian.Uint32(buf[44:48])))
	c.cacheValid = false
	c.cacheLBA = -1
	c.cacheData = nil
}
