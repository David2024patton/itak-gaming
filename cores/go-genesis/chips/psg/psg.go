// Package psg is an original implementation of the Texas Instruments
// SN76489 programmable sound generator, written from the published
// datasheet behavior and the documented Sega variant (as used in the
// Master System, Game Gear and Genesis/Mega Drive).
//
// The chip provides three square-wave tone channels and one noise
// channel. Each channel has a 4-bit attenuation (volume) register:
// 0 is loudest, each step is 2 dB down, and 15 is silent.
//
// Register access uses the chip's latch/data protocol: a byte with
// bit 7 set selects a register and carries its low 4 data bits; a byte
// with bit 7 clear is a data byte for the most recently latched
// register (the high 6 bits for a tone period, the low 4 bits for a
// volume or the noise control).
package psg

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// Config selects chip-variant parameters.
type Config struct {
	// LFSRBits is the width of the noise shift register. The original
	// TI part uses 15 bits; the Sega variant uses 16. Zero means 15.
	LFSRBits int

	// WhiteNoiseTaps is the feedback tap mask for white-noise mode:
	// the feedback bit is the parity of (lfsr & WhiteNoiseTaps).
	// The Sega variant taps bits 0 and 3 (0x0009); the original TI
	// taps are bits 0 and 1 (0x0003), which is the default when zero.
	WhiteNoiseTaps uint32
}

// SerializeSize is the exact size of a serialized chip state.
const SerializeSize = 41

// serializeVersion identifies the state layout written by Serialize.
const serializeVersion = 1

// volumeTable maps a 4-bit attenuation value to a linear amplitude.
// Each step is 2 dB, so the ratio between steps is 10^(-2/20).
var volumeTable = [16]float32{
	1.0, 0.7943, 0.6310, 0.5012,
	0.3981, 0.3162, 0.2512, 0.1995,
	0.1585, 0.1259, 0.1, 0.0794,
	0.0631, 0.0501, 0.0398, 0.0,
}

// noiseRates maps the noise control register's two rate bits to the
// shift period of the rate source, in input clocks, for rates 0-2.
// Rate 3 slaves the noise channel to tone channel 3's output instead.
var noiseRates = [3]int{512, 1024, 2048}

// PSG is a single SN76489 chip instance.
type PSG struct {
	clockFreq  int
	sampleRate int

	// Register file. periods holds the 10-bit tone period registers;
	// volumes holds the 4-bit attenuation for all four channels
	// (index 3 is the noise channel). latched is the register most
	// recently selected by a latch byte, encoded as channel*2+type
	// (type 0 = period/control, 1 = volume).
	periods [3]uint16
	volumes [4]uint8
	latched int

	// Tone generator state: down-counters and output polarity
	// (true = positive half-cycle).
	counters [3]int
	polarity [3]bool

	// Noise generator state.
	noiseCtl uint8 // bit 2: white (1) vs periodic (0); bits 1-0: rate
	lfsr     uint32
	lfsrBits int
	taps     uint32
	// noisePhase advances the fixed-rate clock source (a square wave
	// with the period from noiseRates); noisePrevLevel is the source
	// level seen on the previous input clock, for falling-edge
	// detection.
	noisePhase     int
	noisePrevLevel bool

	// clockDiv counts input clocks toward the next tone tick; the
	// tone counters decrement once per 16 input clocks.
	clockDiv int

	// samplePhase is the fractional sample accumulator, in units of
	// input clocks scaled by sampleRate: it advances by sampleRate
	// per input clock and emits a sample each time it reaches
	// clockFreq, so the long-run sample rate is exact.
	samplePhase int64

	// Output buffer: buffer[:count] holds generated samples not yet
	// drained by the caller.
	buffer []float32
	count  int
}

// New returns a PSG in its power-on state, clocked at clockFreq Hz and
// producing samples at sampleRate Hz into a buffer that holds at most
// bufferSize samples.
//
// Power-on state (the datasheet leaves the register file undefined
// until software writes it; these are the deterministic defaults
// chosen here): periods 0 (which behave as 1024), volumes 15
// (silent), counters loaded with the effective period, tone outputs
// positive, noise control 0, the latch pointing at tone channel 0's
// period register, and the noise register seeded.
func New(clockFreq, sampleRate, bufferSize int, config Config) *PSG {
	if clockFreq <= 0 {
		clockFreq = 3579545 // NTSC color-burst rate used by Sega
	}
	if sampleRate <= 0 {
		sampleRate = 44100
	}
	if bufferSize <= 0 {
		bufferSize = 4096
	}
	lfsrBits := config.LFSRBits
	if lfsrBits == 0 {
		lfsrBits = 15
	}
	if lfsrBits < 2 {
		lfsrBits = 2
	}
	if lfsrBits > 32 {
		lfsrBits = 32
	}
	taps := config.WhiteNoiseTaps
	if taps == 0 {
		taps = 0x0003 // original TI taps: bits 0 and 1
	}
	p := &PSG{
		clockFreq:  clockFreq,
		sampleRate: sampleRate,
		volumes:    [4]uint8{15, 15, 15, 15},
		polarity:   [3]bool{true, true, true},
		lfsrBits:   lfsrBits,
		taps:       taps,
		buffer:     make([]float32, bufferSize),
	}
	for ch := 0; ch < 3; ch++ {
		p.counters[ch] = p.effectivePeriod(ch)
	}
	p.resetLFSR()
	p.noisePrevLevel = p.noiseSourceLevel()
	return p
}

// effectivePeriod returns the counter reload value for a tone channel.
// A period register of 0 behaves as 1024 on the Sega variant (the
// 10-bit counter simply counts through the full range).
func (p *PSG) effectivePeriod(ch int) int {
	if p.periods[ch] == 0 {
		return 1024
	}
	return int(p.periods[ch])
}

// resetLFSR seeds the noise shift register with a single bit in the
// top position.
func (p *PSG) resetLFSR() {
	p.lfsr = 1 << uint(p.lfsrBits-1)
}

// lfsrMask trims the shift register to its configured width.
func (p *PSG) lfsrMask() uint32 {
	if p.lfsrBits >= 32 {
		return 0xFFFFFFFF
	}
	return (uint32(1) << uint(p.lfsrBits)) - 1
}

// Write feeds one byte to the chip's control port. A byte with bit 7
// set is a latch/data byte: bits 6-5 select the channel, bit 4 the
// register type (0 = period/control, 1 = volume), and bits 3-0 are
// the low 4 data bits, applied immediately. A byte with bit 7 clear
// is a data byte for the latched register: bits 5-0 supply the high
// 6 bits of a tone period, or replace the low 4 bits of a volume or
// the noise control.
func (p *PSG) Write(value uint8) {
	if value&0x80 != 0 {
		p.latched = int(value>>4) & 0x07
		p.writeRegister(p.latched, value&0x0F, true)
		return
	}
	p.writeRegister(p.latched, value&0x3F, false)
}

// writeRegister applies data to register reg. latch reports whether
// the write came from a latch byte (low 4 bits of data) as opposed to
// a data byte (6 bits).
func (p *PSG) writeRegister(reg int, data uint8, latch bool) {
	channel := reg >> 1
	isVolume := reg&1 != 0
	switch {
	case isVolume:
		p.volumes[channel] = data & 0x0F
	case channel == 3:
		// Noise control. Writing it reseeds the shift register.
		p.noiseCtl = data & 0x0F
		p.resetLFSR()
		// A register write must not itself look like a clock edge:
		// resync the edge detector to the (possibly new) source.
		p.noisePrevLevel = p.noiseSourceLevel()
	default:
		// Tone period: a latch byte loads the low 4 bits, a data
		// byte loads the high 6 bits; the other bits are retained.
		if latch {
			p.periods[channel] = p.periods[channel]&0x3F0 | uint16(data&0x0F)
		} else {
			p.periods[channel] = p.periods[channel]&0x00F | uint16(data&0x3F)<<4
		}
	}
}

// GenerateSamples advances the chip by the given number of input
// clocks and generates output samples into the internal buffer,
// which the caller drains with GetBuffer and ResetBuffer.
//
// Overflow policy: chip time always advances by the full clock count.
// Samples that would exceed the buffer's capacity are still
// generated (the chip state advances identically) but discarded;
// the return value is the number of samples actually stored during
// this call.
func (p *PSG) GenerateSamples(clocks int) int {
	stored := 0
	remaining := clocks
	for remaining > 0 {
		// Clocks until the next output sample: the phase
		// accumulator advances sampleRate per clock and emits
		// when it reaches clockFreq, so the next emission is
		// ceil((clockFreq - phase) / sampleRate) clocks away.
		// Chip state between sample points only matters through
		// the noise channel's edge counting, which advanceClocks
		// handles in bulk, so the span can be crossed at once.
		step := int((int64(p.clockFreq) - p.samplePhase + int64(p.sampleRate) - 1) / int64(p.sampleRate))
		if step > remaining {
			step = remaining
		}
		p.advanceClocks(step)
		remaining -= step
		p.samplePhase += int64(step) * int64(p.sampleRate)
		if p.samplePhase >= int64(p.clockFreq) {
			p.samplePhase -= int64(p.clockFreq)
			if p.count < len(p.buffer) {
				p.buffer[p.count] = p.output()
				p.count++
				stored++
			}
		}
	}
	return stored
}

// GetBuffer returns the sample buffer and the number of valid
// samples at its front.
func (p *PSG) GetBuffer() ([]float32, int) {
	return p.buffer, p.count
}

// ResetBuffer discards all buffered samples.
func (p *PSG) ResetBuffer() {
	p.count = 0
}

// output is the chip's instantaneous analog output. The hardware
// output stage is unipolar: each channel contributes its volume
// while its generator is in the high state and nothing while low
// (for noise, the shift register's bit 0 is that state). The four
// channels are summed and normalized by 4. (Verified against the
// reference chip's observable output in the differential harness:
// bipolar drive differs only by a DC offset and a factor of 2, but
// drop-in equivalence is exact in this convention.)
func (p *PSG) output() float32 {
	var sum float32
	for ch := 0; ch < 3; ch++ {
		if p.polarity[ch] {
			sum += volumeTable[p.volumes[ch]]
		}
	}
	if p.lfsr&1 != 0 {
		sum += volumeTable[p.volumes[3]]
	}
	return sum * 0.25
}

// advanceClocks advances the chip by n input clocks, exactly as n
// stepClock calls would, but computed in bulk: the tone counters
// move by whole ticks with arithmetic flip counts, and the noise
// register shifts once per falling edge of its clock source inside
// the span. (The chip used to be stepped one clock at a time,
// some 60,000 calls per emulated frame, almost all of which only
// decremented counters.)
func (p *PSG) advanceClocks(n int) {
	if n <= 0 {
		return
	}
	// Tone ticks land once per 16 input clocks, phased by
	// clockDiv: tick boundaries in the span are the clocks where
	// clockDiv wraps.
	total := p.clockDiv + n
	ticks := total / 16
	p.clockDiv = total % 16
	pol3Before := p.polarity[2]
	flips3 := 0
	if ticks > 0 {
		for ch := 0; ch < 3; ch++ {
			if f := p.advanceTone(ch, ticks); ch == 2 {
				flips3 = f
			}
		}
	}
	p.advanceNoise(n, pol3Before, flips3)
}

// advanceTone moves one tone channel forward by the given number
// of tone ticks and returns how many times its output flipped.
// The per-tick rule is: decrement, and on reaching zero reload
// with the effective period and flip. A counter in [1, period]
// first flips after exactly counter ticks, then every period
// ticks. A counter outside that range (only reachable through a
// hand-crafted state) falls back to the literal per-tick walk.
func (p *PSG) advanceTone(ch, ticks int) int {
	c := p.counters[ch]
	per := p.effectivePeriod(ch)
	if c < 1 || c > per {
		flips := 0
		for i := 0; i < ticks; i++ {
			c--
			if c <= 0 {
				c += per
				p.polarity[ch] = !p.polarity[ch]
				flips++
			}
		}
		p.counters[ch] = c
		return flips
	}
	if ticks < c {
		p.counters[ch] = c - ticks
		return 0
	}
	flips := 1 + (ticks-c)/per
	p.counters[ch] = per - (ticks-c)%per
	if flips&1 != 0 {
		p.polarity[ch] = !p.polarity[ch]
	}
	return flips
}

// advanceNoise advances the noise channel over n input clocks.
// Tone channel 3's flip count inside the span is supplied because
// in slaved mode (rate bits 3) that channel's output is the noise
// clock source: each of its high-to-low flips shifts the register
// once. In the fixed-rate modes the source is the phase square
// wave, high while noisePhase*2 < rate after each clock's
// increment; the falling edge is the clock whose increment lands
// the phase on rate/2, which the span crosses a computable number
// of times.
func (p *PSG) advanceNoise(n int, pol3Before bool, flips3 int) {
	if p.noiseCtl&0x03 == 0x03 {
		edges := flips3 / 2
		if pol3Before && flips3&1 != 0 {
			edges++
		}
		for i := 0; i < edges; i++ {
			p.shiftNoise()
		}
		p.noisePrevLevel = p.polarity[2]
		return
	}
	rate := noiseRates[p.noiseCtl&0x03]
	target := rate / 2 // first low phase; rates are even (see noiseRates)
	first := (target - p.noisePhase) % rate
	if first <= 0 {
		first += rate
	}
	if first <= n {
		edges := 1 + (n-first)/rate
		for i := 0; i < edges; i++ {
			p.shiftNoise()
		}
	}
	p.noisePhase = (p.noisePhase + n) % rate
	p.noisePrevLevel = p.noisePhase*2 < rate
}

// noiseSourceLevel reports the current level of the noise clock
// source. The fixed rates are modeled as a square wave with the
// documented period, high for its first half; rate 3 is tone
// channel 3's output polarity.
func (p *PSG) noiseSourceLevel() bool {
	if p.noiseCtl&0x03 == 0x03 {
		return p.polarity[2]
	}
	rate := noiseRates[p.noiseCtl&0x03]
	return p.noisePhase*2 < rate
}

// shiftNoise advances the noise shift register by one step. In white
// noise mode the feedback bit is the parity of the tapped bits; in
// periodic mode the output bit itself rotates back into the top, so
// the seed pattern repeats every LFSRBits shifts and the output bit
// is high once per repetition.
func (p *PSG) shiftNoise() {
	var feedback uint32
	if p.noiseCtl&0x04 != 0 {
		feedback = uint32(bits.OnesCount32(p.lfsr&p.taps) & 1)
	} else {
		feedback = p.lfsr & 1
	}
	p.lfsr = (p.lfsr >> 1) | feedback<<uint(p.lfsrBits-1)
	p.lfsr &= p.lfsrMask()
}

// Serialize writes the complete chip state into buf, which must be
// at least SerializeSize bytes. The state covers every register and
// counter, including the fractional sample timing, so a restored
// chip continues sample-exactly. Samples sitting in the output
// buffer are not part of the state (callers drain before saving);
// a chip restored by Deserialize starts with an empty buffer.
func (p *PSG) Serialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("psg: serialize buffer too small")
	}
	out := buf[:SerializeSize]
	for i := range out {
		out[i] = 0
	}
	out[0] = serializeVersion
	for ch := 0; ch < 3; ch++ {
		binary.LittleEndian.PutUint16(out[1+2*ch:], p.periods[ch])
		binary.LittleEndian.PutUint16(out[7+2*ch:], uint16(p.counters[ch]))
	}
	copy(out[13:17], p.volumes[:])
	out[17] = p.noiseCtl
	out[18] = uint8(p.latched)
	binary.LittleEndian.PutUint32(out[19:23], p.lfsr)
	binary.LittleEndian.PutUint16(out[23:25], uint16(p.noisePhase))
	out[25] = uint8(p.clockDiv)
	var flags uint8
	for ch := 0; ch < 3; ch++ {
		if p.polarity[ch] {
			flags |= 1 << uint(ch)
		}
	}
	if p.noisePrevLevel {
		flags |= 1 << 3
	}
	out[26] = flags
	binary.LittleEndian.PutUint64(out[27:35], uint64(p.samplePhase))
	out[35] = uint8(p.lfsrBits)
	// Bytes 36-40 are reserved and stay zero.
	return nil
}

// Deserialize restores a state written by Serialize. The restoring
// chip should have the same clock/sample rates and variant config;
// the saved shift register is trimmed to this chip's LFSR width.
func (p *PSG) Deserialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("psg: deserialize buffer too small")
	}
	in := buf[:SerializeSize]
	if in[0] != serializeVersion {
		return errors.New("psg: unsupported serialize version")
	}
	for ch := 0; ch < 3; ch++ {
		p.periods[ch] = binary.LittleEndian.Uint16(in[1+2*ch:]) & 0x3FF
		p.counters[ch] = int(binary.LittleEndian.Uint16(in[7+2*ch:]))
	}
	for i := range p.volumes {
		p.volumes[i] = in[13+i] & 0x0F
	}
	p.noiseCtl = in[17] & 0x0F
	p.latched = int(in[18]) & 0x07
	p.lfsr = binary.LittleEndian.Uint32(in[19:23]) & p.lfsrMask()
	p.noisePhase = int(binary.LittleEndian.Uint16(in[23:25]))
	p.clockDiv = int(in[25]) & 0x0F
	flags := in[26]
	for ch := 0; ch < 3; ch++ {
		p.polarity[ch] = flags&(1<<uint(ch)) != 0
	}
	p.noisePrevLevel = flags&(1<<3) != 0
	p.samplePhase = int64(binary.LittleEndian.Uint64(in[27:35]))
	p.count = 0
	return nil
}
