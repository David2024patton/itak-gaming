package scd

// This file implements the Ricoh RF5C164 PCM engine of the Sega CD.
//
// The facts below come from the Ricoh RF5C68A datasheet summary and
// from the PCM chapter of the Mega CD hardware manual, as reproduced
// in public documentation.
//
// The chip plays eight channels from a shared 64 KB wave RAM and
// produces one mixed stereo output sample every 384 sub CPU clocks.
// The sub CPU clock is 12,500,000 Hz, so the native output rate is
// 12,500,000 / 384 = 32552.08 Hz.
//
// Wave RAM samples are 8 bit sign-magnitude values. Bit 7 is the
// sign (1 means positive and 0 means negative) and bits 6 to 0 are
// the magnitude, from 0 to 127. The byte 0xFF is not a sample at
// all: when a channel fetch reads 0xFF the channel jumps to its loop
// start address and fetches again there. If the byte at the loop
// start is also 0xFF the channel outputs silence for that tick.
// Because 0xFF is the loop marker, a positive 127 is not a legal
// sample, while a negative 127 (0x7F) is legal.
//
// This model does sample-and-hold resampling to 44,100 Hz in
// RenderFrame. The real chip does no interpolation, so a held native
// sample is repeated until the next chip tick produces a new one.
// The exact ratio accumulator keeps the long term rate exact.
//
// The datasheet restricts CPU access to wave RAM while the chip is
// sounding. That restriction is not modeled here: CPU wave RAM
// access through the bank window is unrestricted in the Unit, and
// this engine only reports the selected bank through WaveBank.

import "encoding/binary"

// PCMStateSize is the exact serialized size of a PCM engine, in
// bytes. The layout is 11 bytes per channel for the eight channels
// (88 bytes, channel major, in the field order ENV, PAN, FD low,
// FD high, loop start low, loop start high, start, and the four
// address counter bytes in big-endian order), followed by 19 global
// bytes: CTRL, the selected channel, the wave bank, the on and off
// byte, the seven mirror bytes for register offsets 9 to 15, the
// four resample accumulator bytes in big-endian order, and the held
// left and right outputs as big-endian signed words.
const PCMStateSize = 8*11 + 19

// Chip timing and output constants.
const (
	pcmSubClockHz   = 12500000 // sub CPU clock, in Hz
	pcmClocksPerOut = 384      // sub CPU clocks per native output sample
	pcmOutputHz     = 44100    // RenderFrame output rate, in Hz
)

// pcmChannel holds the register and playback state of one of the
// eight RF5C164 channels. The current sample byte is not part of the
// serialized state: it is refetched from wave RAM on restore, using
// the same loop marker rule as a normal fetch.
type pcmChannel struct {
	env  uint8  // offset 0x00: constant gain, called envelope in the manual
	pan  uint8  // offset 0x01: bits 7 to 4 left gain, bits 3 to 0 right gain
	fd   uint16 // offsets 0x02 and 0x03: 5.11 fixed point phase increment
	ls   uint16 // offsets 0x04 and 0x05: loop start address
	st   uint8  // offset 0x06: start address high byte, start is st << 8
	addr uint32 // 27 bit phase counter: 16 integer bits and 11 fraction bits
	cur  uint8  // current fetched sample byte, or 0 for a double loop marker
}

// PCM is the RF5C164 engine. It borrows the caller owned 64 KB wave
// RAM slice and keeps every other piece of state internally.
type PCM struct {
	wave     []byte
	channels [8]pcmChannel

	ctrl       uint8    // offset 0x07 raw byte: bit 7 master on, bit 6 MOD
	curChannel int      // channel selected by the last MOD 1 CTRL write
	waveBank   int      // wave bank selected by the last MOD 0 CTRL write
	onOff      uint8    // offset 0x08: bit i enables channel i
	mirror     [7]uint8 // offsets 0x09 to 0x0F: store and read back only

	resampleAccum uint32 // exact ratio accumulator for RenderFrame
	holdL         int16  // held mixed left output of the last chip tick
	holdR         int16  // held mixed right output of the last chip tick
}

// NewPCM builds an engine over wave, which the caller owns and which
// is expected to be the 64 KB wave RAM. The slice reference is kept,
// so caller writes to wave RAM are heard by the engine. Power-on
// state is all zero: the chip master is off, channel 0 is selected,
// wave bank 0 is selected, and no channel is enabled.
func NewPCM(wave []byte) *PCM {
	return &PCM{wave: wave}
}

// waveAt reads one wave RAM byte. Indexes outside the slice read as
// zero, which keeps a short or missing wave RAM silent instead of
// faulting. A full engine always runs over the full 64 KB.
func (p *PCM) waveAt(idx int) uint8 {
	if idx < 0 || idx >= len(p.wave) {
		return 0
	}
	return p.wave[idx]
}

// fetchCurrent refetches the current sample of ch from the wave RAM
// position its address counter points at. A 0xFF byte is the loop
// marker: the counter jumps to the loop start address and the fetch
// repeats there. A second 0xFF at the loop start means the current
// sample is silence, stored as zero, which decodes to a magnitude of
// zero on either sign.
func (p *PCM) fetchCurrent(ch *pcmChannel) {
	idx := int(ch.addr >> 11)
	b := p.waveAt(idx)
	if b != 0xFF {
		ch.cur = b
		return
	}
	ch.addr = uint32(ch.ls) << 11
	b = p.waveAt(int(ch.ls))
	if b == 0xFF {
		ch.cur = 0
		return
	}
	ch.cur = b
}

// holdAtStart parks ch at its start address, which is where an
// inactive channel counter constantly sits. The current sample is
// refreshed from the start position so a channel that turns on later
// starts from the start sample, but the loop marker jump is not
// applied here: the counter itself must stay at the start address
// for as long as the channel is inactive. A marker byte at the start
// position reads as silence.
func (p *PCM) holdAtStart(ch *pcmChannel) {
	ch.addr = uint32(ch.st) << 19
	b := p.waveAt(int(ch.st) << 8)
	if b == 0xFF {
		ch.cur = 0
		return
	}
	ch.cur = b
}

// masterOn reports whether the chip master bit (CTRL bit 7) is set.
func (p *PCM) masterOn() bool { return p.ctrl&0x80 != 0 }

// WriteReg writes one byte of the register page at offset off, which
// ranges from 0x00 to 0x0F. Offsets 0x00 to 0x06 write the currently
// selected channel. Offset 0x07 is CTRL, offset 0x08 is the channel
// on and off byte, and offsets 0x09 to 0x0F are mirror bytes that are
// stored and read back with no other effect. Offsets outside the
// page are ignored.
func (p *PCM) WriteReg(off uint8, v uint8) {
	switch {
	case off <= 0x06:
		ch := &p.channels[p.curChannel]
		switch off {
		case 0x00:
			ch.env = v
		case 0x01:
			ch.pan = v
		case 0x02:
			ch.fd = ch.fd&0xFF00 | uint16(v)
		case 0x03:
			ch.fd = ch.fd&0x00FF | uint16(v)<<8
		case 0x04:
			ch.ls = ch.ls&0xFF00 | uint16(v)
		case 0x05:
			ch.ls = ch.ls&0x00FF | uint16(v)<<8
		case 0x06:
			// Writing the start register reloads the address
			// counter and refetches the current sample at once,
			// whether or not the channel is currently on. This
			// is the channel retrigger.
			ch.st = v
			ch.addr = uint32(v) << 19
			p.fetchCurrent(ch)
		}
	case off == 0x07:
		p.ctrl = v
		if v&0x40 != 0 {
			// MOD 1: bits 2 to 0 select the current channel.
			// The wave bank selection is left untouched.
			p.curChannel = int(v & 0x07)
		} else {
			// MOD 0: bits 3 to 0 select the wave RAM bank shown
			// in the CPU window. The channel selection is left
			// untouched.
			p.waveBank = int(v & 0x0F)
		}
	case off == 0x08:
		p.onOff = v
	case off >= 0x09 && off <= 0x0F:
		p.mirror[off-0x09] = v
	}
}

// ReadReg reads one byte of the register page at offset off.
// Offsets 0x00 to 0x06 return the currently selected channel values,
// offset 0x07 returns the raw CTRL byte, offset 0x08 returns the on
// and off byte, and offsets 0x09 to 0x0F return the mirror bytes.
// Offsets outside the page read as zero.
func (p *PCM) ReadReg(off uint8) uint8 {
	switch {
	case off <= 0x06:
		ch := &p.channels[p.curChannel]
		switch off {
		case 0x00:
			return ch.env
		case 0x01:
			return ch.pan
		case 0x02:
			return uint8(ch.fd)
		case 0x03:
			return uint8(ch.fd >> 8)
		case 0x04:
			return uint8(ch.ls)
		case 0x05:
			return uint8(ch.ls >> 8)
		case 0x06:
			return ch.st
		}
	case off == 0x07:
		return p.ctrl
	case off == 0x08:
		return p.onOff
	case off >= 0x09 && off <= 0x0F:
		return p.mirror[off-0x09]
	}
	return 0
}

// WaveBank returns the wave RAM bank, from 0 to 15, that the CPU
// window currently shows. It is set by MOD 0 CTRL writes and is 0 at
// power-on.
func (p *PCM) WaveBank() int { return p.waveBank }

// Selected returns the currently selected channel, from 0 to 7. It is
// set by MOD 1 CTRL writes and is 0 at power-on.
func (p *PCM) Selected() int { return p.curChannel }

// OnOff returns the channel on and off byte: bit i enables channel i.
func (p *PCM) OnOff() uint8 { return p.onOff }

// Ctrl returns the raw CTRL byte last written to offset 0x07.
func (p *PCM) Ctrl() uint8 { return p.ctrl }

// ChannelAddr returns the 27 bit address counter of channel ch, or
// zero when ch is out of range. The sample index into wave RAM is
// the counter shifted right by 11.
func (p *PCM) ChannelAddr(ch int) uint32 {
	if ch < 0 || ch >= len(p.channels) {
		return 0
	}
	return p.channels[ch].addr & 0x7FFFFFF
}

// ChannelSample returns the current fetched sample byte of channel
// ch, or zero when ch is out of range. A channel whose fetch found
// the loop marker twice in a row reports zero, for silence.
func (p *PCM) ChannelSample(ch int) uint8 {
	if ch < 0 || ch >= len(p.channels) {
		return 0
	}
	return p.channels[ch].cur
}

// Tick advances the engine by one chip tick and recomputes the held
// mixed output. With the chip master off the held output is zero, no
// channel advances, and every channel counter is parked at its start
// address, because the master off state makes every channel
// inactive. With the master on, an enabled channel advances its
// address counter by its FD increment, wraps in 27 bits, and
// refetches its current sample with the loop marker rule. A disabled
// channel is parked at its start address and contributes nothing.
func (p *PCM) Tick() {
	if !p.masterOn() {
		for i := range p.channels {
			p.holdAtStart(&p.channels[i])
		}
		p.holdL = 0
		p.holdR = 0
		return
	}
	var sumL, sumR int
	for i := range p.channels {
		ch := &p.channels[i]
		if p.onOff&(1<<uint(i)) == 0 {
			p.holdAtStart(ch)
			continue
		}
		ch.addr = (ch.addr + uint32(ch.fd)) & 0x7FFFFFF
		p.fetchCurrent(ch)
		mag := int(ch.cur & 0x7F)
		if mag == 0 {
			continue
		}
		sign := 1
		if ch.cur&0x80 == 0 {
			sign = -1
		}
		env := int(ch.env)
		sumL += sign * ((mag * env * int(ch.pan>>4)) >> 5)
		sumR += sign * ((mag * env * int(ch.pan&0x0F)) >> 5)
	}
	p.holdL = clampInt16(sumL)
	p.holdR = clampInt16(sumR)
}

// clampInt16 clamps v to the signed 16 bit range.
func clampInt16(v int) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// RenderFrame returns one stereo output frame at 44,100 Hz. The
// exact ratio accumulator adds the sub CPU clock rate on every call
// and runs one chip tick for every 384 times 44,100 accumulated
// units, so the long term tick rate is exactly the native chip rate
// and no drift builds up. The returned sample is the held output of
// the most recent chip tick, repeated unchanged between ticks,
// because the chip itself does no interpolation.
func (p *PCM) RenderFrame() (l, r int16) {
	p.resampleAccum += pcmSubClockHz
	for p.resampleAccum >= pcmClocksPerOut*pcmOutputHz {
		p.resampleAccum -= pcmClocksPerOut * pcmOutputHz
		p.Tick()
	}
	return p.holdL, p.holdR
}

// SerializeState writes exactly PCMStateSize bytes into buf, in the
// layout documented on PCMStateSize. The caller supplies a buffer of
// at least that size.
func (p *PCM) SerializeState(buf []byte) {
	off := 0
	for i := range p.channels {
		ch := &p.channels[i]
		buf[off+0] = ch.env
		buf[off+1] = ch.pan
		buf[off+2] = uint8(ch.fd)
		buf[off+3] = uint8(ch.fd >> 8)
		buf[off+4] = uint8(ch.ls)
		buf[off+5] = uint8(ch.ls >> 8)
		buf[off+6] = ch.st
		binary.BigEndian.PutUint32(buf[off+7:off+11], ch.addr&0x7FFFFFF)
		off += 11
	}
	buf[off+0] = p.ctrl
	buf[off+1] = uint8(p.curChannel)
	buf[off+2] = uint8(p.waveBank)
	buf[off+3] = p.onOff
	copy(buf[off+4:off+11], p.mirror[:])
	binary.BigEndian.PutUint32(buf[off+11:off+15], p.resampleAccum)
	binary.BigEndian.PutUint16(buf[off+15:off+17], uint16(p.holdL))
	binary.BigEndian.PutUint16(buf[off+17:off+19], uint16(p.holdR))
}

// DeserializeState restores state written by SerializeState. After
// the registers and counters are restored, every channel current
// sample is refetched from wave RAM with the same loop marker rule a
// normal fetch uses, so a restored engine renders identically to the
// engine that was serialized.
func (p *PCM) DeserializeState(buf []byte) {
	off := 0
	for i := range p.channels {
		ch := &p.channels[i]
		ch.env = buf[off+0]
		ch.pan = buf[off+1]
		ch.fd = uint16(buf[off+2]) | uint16(buf[off+3])<<8
		ch.ls = uint16(buf[off+4]) | uint16(buf[off+5])<<8
		ch.st = buf[off+6]
		ch.addr = binary.BigEndian.Uint32(buf[off+7:off+11]) & 0x7FFFFFF
		off += 11
	}
	p.ctrl = buf[off+0]
	p.curChannel = int(buf[off+1]) & 0x07
	p.waveBank = int(buf[off+2]) & 0x0F
	p.onOff = buf[off+3]
	copy(p.mirror[:], buf[off+4:off+11])
	p.resampleAccum = binary.BigEndian.Uint32(buf[off+11 : off+15])
	p.holdL = int16(binary.BigEndian.Uint16(buf[off+15 : off+17]))
	p.holdR = int16(binary.BigEndian.Uint16(buf[off+17 : off+19]))
	for i := range p.channels {
		p.fetchCurrent(&p.channels[i])
	}
}
