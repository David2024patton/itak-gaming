// Package sound is an original implementation of the Game Boy (DMG)
// audio processing unit, written from the published hardware
// documentation (the Pan Docs audio chapters).
//
// Four channels: two square waves (channel 1 with a frequency
// sweep), a wave channel reading a 32-entry 4-bit sample table, and
// a noise channel driven by a 15-bit LFSR. A 512 Hz frame sequencer
// clocks the length counters, the sweep, and the envelopes. The
// mixer routes each channel to the left (SO2) and right (SO1)
// outputs under NR51, scales by the NR50 side volumes, and the
// result is box-filtered down to the host sample rate.
//
// The frame sequencer is not timed by this package: on hardware it
// is clocked by a falling edge of the system divider's bit 12, so
// the system calls StepFrameSequencer at those edges and the APU's
// length, sweep, and envelope timing inherits the divider's exact
// phase (including the quirk that a DIV write dropping bit 12
// clocks the sequencer).
package sound

import (
	"encoding/binary"
	"errors"
	"math"
)

// SampleRate is the host output rate the APU renders to.
const SampleRate = 44100

// clockHz is the Game Boy master clock; channel timers and the
// sample resampler count in these T-cycles (dots).
const clockHz = 4194304

// Register addresses.
const (
	regNR10 = 0xFF10
	regNR52 = 0xFF26
	regWave = 0xFF30
)

// dutyPatterns are the four square waveforms, one bit per step,
// MSB first in step order.
var dutyPatterns = [4]uint8{0x01, 0x03, 0x0F, 0x3F}

// noiseDivisors maps NR43's divisor code to the LFSR timer base
// period in dots (before the shift clock is applied).
var noiseDivisors = [8]int{8, 16, 32, 48, 64, 80, 96, 112}

type square struct {
	enabled bool
	dacOn   bool
	freq    uint16 // 11-bit frequency value
	timer   int    // dots until the next duty step
	dutyPos int
	duty    int // 0-3

	length   int
	lengthOn bool

	vol      int // live envelope volume
	envInit  int // NRx2 initial volume, loaded at trigger
	envTimer int
	envPace  int
	envUp    bool

	// Sweep (channel 1 only).
	shadow     uint16
	sweepTimer int
	sweepPace  int
	sweepSlope int
	sweepNeg   bool
	sweepOn    bool
}

type wave struct {
	enabled  bool
	dacOn    bool
	freq     uint16
	timer    int
	pos      int // 0-31 nibble position
	length   int
	lengthOn bool
	volCode  int // NR32 volume code 0-3
}

type noise struct {
	enabled  bool
	dacOn    bool
	lfsr     uint16
	timer    int
	length   int
	lengthOn bool

	vol      int // live envelope volume
	envInit  int // NR42 initial volume, loaded at trigger
	envTimer int
	envPace  int
	envUp    bool
}

// APU is one DMG audio processing unit.
type APU struct {
	ch1 square
	ch2 square
	ch3 wave
	ch4 noise

	// Register shadows for read-back of the readable registers.
	nr10, nr11, nr12, nr21, nr22 uint8
	nr30, nr32                   uint8
	nr42, nr43                   uint8
	nr50, nr51                   uint8
	len1, len2, len4             uint8 // NRx1 length fields as written
	len3                         uint8
	ctrl1, ctrl2, ctrl3, ctrl4   uint8 // NRx4 as written (bit 6 read back)

	waveRAM [16]byte

	power  bool
	fsStep int // next frame sequencer step to run, 0-7

	// Sample generation: every dot adds the mixed output to the
	// accumulators; the phase accumulator emits one output sample
	// per SampleRate-th of a second of dots, box-filtered over the
	// dots it spans.
	phase float64
	sumL  float64
	sumR  float64
	sumN  int
	out   []int16 // pending samples (interleaved stereo)
}

// New returns an APU in its power-on state: powered, all channels
// off, frame sequencer at step 0.
func New() *APU {
	s := &APU{power: true}
	s.ch4.lfsr = 0x7FFF
	return s
}

// Trigger helper: the length counter quirk. When a channel's
// length enable rises in an NRx4 write and the next frame
// sequencer step is a length-clock step, the counter is clocked
// immediately; if that empties it, the channel turns off.
func (s *APU) lenEnableEdge(length *int, lengthOn *bool, enabled *bool, newOn bool) {
	if newOn && !*lengthOn && s.nextStepClocksLength() {
		if *length > 0 {
			*length--
			if *length == 0 {
				*enabled = false
			}
		}
	}
	*lengthOn = newOn
}

func (s *APU) nextStepClocksLength() bool {
	// Length counters clock on sequencer steps 0, 2, 4, 6.
	return s.fsStep%2 == 0
}

// Write handles a write to the APU register area 0xFF10-0xFF3F.
// While powered off, writes to NR10-NR51 are ignored (wave RAM
// stays writable); NR52 is the power switch itself.
func (s *APU) Write(addr uint16, val uint8) {
	if addr >= regWave && addr < regWave+16 {
		// DMG wave RAM quirk: while the wave channel is playing,
		// writes are ignored (reads return 0xFF; see Read).
		if !s.ch3.enabled {
			s.waveRAM[addr-regWave] = val
		}
		return
	}
	if addr == regNR52 {
		s.writePower(val)
		return
	}
	if !s.power {
		return
	}
	switch addr {
	// Channel 1.
	case 0xFF10:
		s.nr10 = val
		s.ch1.sweepPace = int(val>>4) & 7
		s.ch1.sweepNeg = val&0x08 != 0
		s.ch1.sweepSlope = int(val) & 7
	case 0xFF11:
		s.nr11 = val
		s.ch1.duty = int(val >> 6)
		s.len1 = val & 0x3F
		s.ch1.length = 64 - int(s.len1)
	case 0xFF12:
		s.nr12 = val
		s.ch1.dacOn = val&0xF8 != 0
		if !s.ch1.dacOn {
			s.ch1.enabled = false
		}
		s.ch1.envInit = int(val >> 4)
		s.ch1.envUp = val&0x08 != 0
		s.ch1.envPace = int(val) & 7
	case 0xFF13:
		s.ch1.freq = s.ch1.freq&0x700 | uint16(val)
	case 0xFF14:
		s.ctrl1 = val
		s.ch1.freq = s.ch1.freq&0xFF | uint16(val&7)<<8
		s.lenEnableEdge(&s.ch1.length, &s.ch1.lengthOn, &s.ch1.enabled, val&0x40 != 0)
		if val&0x80 != 0 {
			s.triggerSquare(&s.ch1, true)
		}
	// Channel 2.
	case 0xFF16:
		s.nr21 = val
		s.ch2.duty = int(val >> 6)
		s.len2 = val & 0x3F
		s.ch2.length = 64 - int(s.len2)
	case 0xFF17:
		s.nr22 = val
		s.ch2.dacOn = val&0xF8 != 0
		if !s.ch2.dacOn {
			s.ch2.enabled = false
		}
		s.ch2.envInit = int(val >> 4)
		s.ch2.envUp = val&0x08 != 0
		s.ch2.envPace = int(val) & 7
	case 0xFF18:
		s.ch2.freq = s.ch2.freq&0x700 | uint16(val)
	case 0xFF19:
		s.ctrl2 = val
		s.ch2.freq = s.ch2.freq&0xFF | uint16(val&7)<<8
		s.lenEnableEdge(&s.ch2.length, &s.ch2.lengthOn, &s.ch2.enabled, val&0x40 != 0)
		if val&0x80 != 0 {
			s.triggerSquare(&s.ch2, false)
		}
	// Channel 3.
	case 0xFF1A:
		s.nr30 = val
		s.ch3.dacOn = val&0x80 != 0
		if !s.ch3.dacOn {
			s.ch3.enabled = false
		}
	case 0xFF1B:
		s.len3 = val
		s.ch3.length = 256 - int(val)
	case 0xFF1C:
		s.nr32 = val
		s.ch3.volCode = int(val>>5) & 3
	case 0xFF1D:
		s.ch3.freq = s.ch3.freq&0x700 | uint16(val)
	case 0xFF1E:
		s.ctrl3 = val
		s.ch3.freq = s.ch3.freq&0xFF | uint16(val&7)<<8
		s.lenEnableEdge(&s.ch3.length, &s.ch3.lengthOn, &s.ch3.enabled, val&0x40 != 0)
		if val&0x80 != 0 {
			s.triggerWave()
		}
	// Channel 4.
	case 0xFF20:
		s.len4 = val & 0x3F
		s.ch4.length = 64 - int(s.len4)
	case 0xFF21:
		s.nr42 = val
		s.ch4.dacOn = val&0xF8 != 0
		if !s.ch4.dacOn {
			s.ch4.enabled = false
		}
		s.ch4.envInit = int(val >> 4)
		s.ch4.envUp = val&0x08 != 0
		s.ch4.envPace = int(val) & 7
	case 0xFF22:
		s.nr43 = val
	case 0xFF23:
		s.ctrl4 = val
		s.lenEnableEdge(&s.ch4.length, &s.ch4.lengthOn, &s.ch4.enabled, val&0x40 != 0)
		if val&0x80 != 0 {
			s.triggerNoise()
		}
	// Master.
	case 0xFF24:
		s.nr50 = val
	case 0xFF25:
		s.nr51 = val
	}
}

// writePower handles NR52's bit 7. Powering off clears every sound
// register and disables all channels, and resets the frame
// sequencer to step 0; the length counters keep their values,
// which the documentation calls out explicitly (a channel powered
// back on with a surviving counter still times out on it once
// re-enabled). Wave RAM is not touched.
func (s *APU) writePower(val uint8) {
	on := val&0x80 != 0
	if on == s.power {
		return
	}
	s.power = on
	if !on {
		l1, l2, l3, l4 := s.ch1.length, s.ch2.length, s.ch3.length, s.ch4.length
		wr := s.waveRAM
		*s = APU{power: false, out: s.out[:0]}
		s.ch1.length, s.ch2.length, s.ch3.length, s.ch4.length = l1, l2, l3, l4
		s.waveRAM = wr
		s.ch4.lfsr = 0x7FFF
	}
}

// triggerSquare starts a square channel: length reloads if empty,
// the envelope restarts, and (channel 1) the sweep restarts with
// its immediate overflow check.
func (s *APU) triggerSquare(ch *square, swept bool) {
	if !ch.dacOn {
		ch.enabled = false
		return
	}
	ch.enabled = true
	if ch.length == 0 {
		ch.length = 64
		if ch.lengthOn && s.nextStepClocksLength() {
			ch.length--
		}
	}
	ch.timer = (2048 - int(ch.freq)) * 4
	// The envelope restarts from the register's initial volume.
	ch.vol = ch.envInit
	ch.envTimer = ch.envPace
	if swept {
		ch.shadow = ch.freq
		ch.sweepTimer = ch.sweepPace
		if ch.sweepTimer == 0 {
			ch.sweepTimer = 8
		}
		ch.sweepOn = ch.sweepPace != 0 || ch.sweepSlope != 0
		if ch.sweepSlope != 0 {
			if ch.calcSweep() > 2047 {
				ch.enabled = false
			}
		}
	}
}

// calcSweep computes the frequency the sweep would move to.
func (ch *square) calcSweep() uint16 {
	delta := ch.shadow >> uint(ch.sweepSlope)
	if ch.sweepNeg {
		return ch.shadow - delta
	}
	return ch.shadow + delta
}

func (s *APU) triggerWave() {
	if !s.ch3.dacOn {
		s.ch3.enabled = false
		return
	}
	s.ch3.enabled = true
	if s.ch3.length == 0 {
		s.ch3.length = 256
		if s.ch3.lengthOn && s.nextStepClocksLength() {
			s.ch3.length--
		}
	}
	s.ch3.timer = (2048 - int(s.ch3.freq)) * 2
	s.ch3.pos = 0
}

func (s *APU) triggerNoise() {
	if !s.ch4.dacOn {
		s.ch4.enabled = false
		return
	}
	s.ch4.enabled = true
	if s.ch4.length == 0 {
		s.ch4.length = 64
		if s.ch4.lengthOn && s.nextStepClocksLength() {
			s.ch4.length--
		}
	}
	s.ch4.lfsr = 0x7FFF
	s.ch4.timer = noisePeriod(s.nr43)
	s.ch4.vol = s.ch4.envInit
	s.ch4.envTimer = s.ch4.envPace
}

// noisePeriod converts NR43 to the LFSR timer period in dots.
func noisePeriod(nr43 uint8) int {
	code := int(nr43) & 7
	shift := int(nr43>>4) & 0xF
	return noiseDivisors[code] << uint(shift)
}

// Read handles a read from the APU register area, applying the
// documented read-back masks (write-only fields read as 1s).
func (s *APU) Read(addr uint16) uint8 {
	if addr >= regWave && addr < regWave+16 {
		// DMG wave RAM quirk: while the wave channel is playing,
		// reads return 0xFF.
		if s.ch3.enabled {
			return 0xFF
		}
		return s.waveRAM[addr-regWave]
	}
	switch addr {
	case 0xFF10:
		return s.nr10 | 0x80
	case 0xFF11:
		return s.nr11&0xC0 | 0x3F
	case 0xFF12:
		return s.nr12
	case 0xFF13:
		return 0xFF
	case 0xFF14:
		return s.ctrl1&0x40 | 0xBF
	case 0xFF16:
		return s.nr21&0xC0 | 0x3F
	case 0xFF17:
		return s.nr22
	case 0xFF18:
		return 0xFF
	case 0xFF19:
		return s.ctrl2&0x40 | 0xBF
	case 0xFF1A:
		return s.nr30&0x80 | 0x7F
	case 0xFF1B:
		return 0xFF
	case 0xFF1C:
		return s.nr32&0x60 | 0x9F
	case 0xFF1D:
		return 0xFF
	case 0xFF1E:
		return s.ctrl3&0x40 | 0xBF
	case 0xFF20:
		return 0xFF
	case 0xFF21:
		return s.nr42
	case 0xFF22:
		return s.nr43
	case 0xFF23:
		return s.ctrl4&0x40 | 0xBF
	case 0xFF24:
		return s.nr50
	case 0xFF25:
		return s.nr51
	case regNR52:
		v := uint8(0x70)
		if s.power {
			v |= 0x80
		}
		if s.ch1.enabled {
			v |= 1
		}
		if s.ch2.enabled {
			v |= 2
		}
		if s.ch3.enabled {
			v |= 4
		}
		if s.ch4.enabled {
			v |= 8
		}
		return v
	}
	return 0xFF
}

// StepFrameSequencer runs one 512 Hz frame sequencer step: length
// counters on steps 0/2/4/6, sweep on 2/6, envelopes on step 7. The
// system calls it on each falling edge of divider bit 12.
func (s *APU) StepFrameSequencer() {
	if !s.power {
		return
	}
	step := s.fsStep
	s.fsStep = (s.fsStep + 1) & 7
	switch step {
	case 0, 2, 4, 6:
		s.clockLengths()
	}
	switch step {
	case 2, 6:
		s.clockSweep()
	case 7:
		s.clockEnvelopes()
	}
}

func (s *APU) clockLengths() {
	clock := func(length *int, lengthOn bool, enabled *bool) {
		if lengthOn && *length > 0 {
			*length--
			if *length == 0 {
				*enabled = false
			}
		}
	}
	clock(&s.ch1.length, s.ch1.lengthOn, &s.ch1.enabled)
	clock(&s.ch2.length, s.ch2.lengthOn, &s.ch2.enabled)
	clock(&s.ch3.length, s.ch3.lengthOn, &s.ch3.enabled)
	clock(&s.ch4.length, s.ch4.lengthOn, &s.ch4.enabled)
}

func (s *APU) clockSweep() {
	ch := &s.ch1
	if !ch.sweepOn || ch.sweepPace == 0 {
		return
	}
	if ch.sweepTimer > 0 {
		ch.sweepTimer--
	}
	if ch.sweepTimer != 0 {
		return
	}
	ch.sweepTimer = ch.sweepPace
	if !ch.enabled {
		return
	}
	next := ch.calcSweep()
	if next > 2047 {
		ch.enabled = false
		return
	}
	ch.freq = next
	ch.shadow = next
	if ch.calcSweep() > 2047 {
		ch.enabled = false
	}
}

func (s *APU) clockEnvelopes() {
	env := func(chVol *int, timer *int, pace int, up bool, dacOn bool) {
		if pace == 0 || !dacOn {
			return
		}
		if *timer > 0 {
			*timer--
		}
		if *timer == 0 {
			*timer = pace
			if up && *chVol < 15 {
				*chVol++
			}
			if !up && *chVol > 0 {
				*chVol--
			}
		}
	}
	env(&s.ch1.vol, &s.ch1.envTimer, s.ch1.envPace, s.ch1.envUp, s.ch1.dacOn)
	env(&s.ch2.vol, &s.ch2.envTimer, s.ch2.envPace, s.ch2.envUp, s.ch2.dacOn)
	env(&s.ch4.vol, &s.ch4.envTimer, s.ch4.envPace, s.ch4.envUp, s.ch4.dacOn)
}

// Tick advances the channel timers by the given number of dots and
// generates output samples into the pending buffer.
func (s *APU) Tick(dots int) {
	for i := 0; i < dots; i++ {
		s.tickChannels()
		s.mixDot()
	}
}

func (s *APU) tickChannels() {
	if s.ch1.enabled {
		s.ch1.timer--
		if s.ch1.timer <= 0 {
			s.ch1.timer += (2048 - int(s.ch1.freq)) * 4
			s.ch1.dutyPos = (s.ch1.dutyPos + 1) & 7
		}
	}
	if s.ch2.enabled {
		s.ch2.timer--
		if s.ch2.timer <= 0 {
			s.ch2.timer += (2048 - int(s.ch2.freq)) * 4
			s.ch2.dutyPos = (s.ch2.dutyPos + 1) & 7
		}
	}
	if s.ch3.enabled {
		s.ch3.timer--
		if s.ch3.timer <= 0 {
			s.ch3.timer += (2048 - int(s.ch3.freq)) * 2
			s.ch3.pos = (s.ch3.pos + 1) & 31
		}
	}
	if s.ch4.enabled {
		s.ch4.timer--
		if s.ch4.timer <= 0 {
			s.ch4.timer += noisePeriod(s.nr43)
			// Clock the LFSR: the new bit is the XOR of bits 0
			// and 1, entering at bit 14 (and bit 6 in the
			// 7-bit width mode).
			nb := (s.ch4.lfsr ^ (s.ch4.lfsr >> 1)) & 1
			s.ch4.lfsr = s.ch4.lfsr>>1 | nb<<14
			if s.nr43&0x08 != 0 {
				s.ch4.lfsr = s.ch4.lfsr&^(1<<6) | nb<<6
			}
		}
	}
}

// channelOut returns the current per-channel DAC output in
// [0, 1], before panning and side volume.
func (s *APU) channelOut(ch int) float64 {
	switch ch {
	case 0:
		if !s.ch1.enabled || !s.ch1.dacOn {
			return 0
		}
		if dutyPatterns[s.ch1.duty]>>uint(7-s.ch1.dutyPos)&1 != 0 {
			return float64(s.ch1.vol) / 15
		}
		return 0
	case 1:
		if !s.ch2.enabled || !s.ch2.dacOn {
			return 0
		}
		if dutyPatterns[s.ch2.duty]>>uint(7-s.ch2.dutyPos)&1 != 0 {
			return float64(s.ch2.vol) / 15
		}
		return 0
	case 2:
		if !s.ch3.enabled || !s.ch3.dacOn {
			return 0
		}
		nib := s.waveRAM[s.ch3.pos/2]
		if s.ch3.pos%2 == 0 {
			nib >>= 4
		} else {
			nib &= 0x0F
		}
		switch s.ch3.volCode {
		case 1:
			return float64(nib) / 15
		case 2:
			return float64(nib) / 30
		case 3:
			return float64(nib) / 60
		}
		return 0
	case 3:
		if !s.ch4.enabled || !s.ch4.dacOn {
			return 0
		}
		if s.ch4.lfsr&1 == 0 {
			return float64(s.ch4.vol) / 15
		}
		return 0
	}
	return 0
}

// DebugChannelSample returns channel ch's output as a centered
// waveform in [-1, 1], for measurement tests: it is the raw DAC
// output with its midpoint subtracted, so zero crossings of the
// return value are the channel's cycles.
func (s *APU) DebugChannelSample(ch int) float64 {
	// Peak amplitude per channel is 1 in channelOut units; center
	// the [0, peak] waveform on 0.
	switch ch {
	case 0:
		return s.channelOut(0)*2 - float64(s.ch1.vol)/15
	case 1:
		return s.channelOut(1)*2 - float64(s.ch2.vol)/15
	case 2:
		return s.channelOut(2)*2 - 0.5
	case 3:
		return s.channelOut(3)*2 - float64(s.ch4.vol)/15
	}
	return 0
}

// DebugLength returns channel ch's length counter (test support).
func (s *APU) DebugLength(ch int) int {
	switch ch {
	case 0:
		return s.ch1.length
	case 1:
		return s.ch2.length
	case 2:
		return s.ch3.length
	case 3:
		return s.ch4.length
	}
	return 0
}

// DebugEnabled reports whether channel ch is currently on.
func (s *APU) DebugEnabled(ch int) bool {
	switch ch {
	case 0:
		return s.ch1.enabled
	case 1:
		return s.ch2.enabled
	case 2:
		return s.ch3.enabled
	case 3:
		return s.ch4.enabled
	}
	return false
}

// mixDot accumulates one dot of mixed stereo output and emits a
// finished sample whenever the phase accumulator crosses a whole
// output sample period.
func (s *APU) mixDot() {
	var l, r float64
	if s.power {
		volL := float64(((s.nr50>>4)&7)+1) / 8
		volR := float64((s.nr50&7)+1) / 8
		for ch := 0; ch < 4; ch++ {
			o := s.channelOut(ch)
			if o == 0 {
				continue
			}
			if s.nr51>>(uint(ch)+4)&1 != 0 { // SO2: left
				l += o
			}
			if s.nr51>>uint(ch)&1 != 0 { // SO1: right
				r += o
			}
		}
		l *= volL / 4
		r *= volR / 4
	}
	s.sumL += l
	s.sumR += r
	s.sumN++
	s.phase += float64(SampleRate) / clockHz
	if s.phase >= 1 {
		s.phase -= 1
		n := float64(s.sumN)
		s.out = append(s.out,
			clamp16(s.sumL/n*32767), clamp16(s.sumR/n*32767))
		s.sumL, s.sumR, s.sumN = 0, 0, 0
	}
}

func clamp16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// Produce drains up to n pending interleaved stereo samples. With
// n <= 0 it drains everything pending.
func (s *APU) Produce(n int) []int16 {
	if n <= 0 || n > len(s.out) {
		n = len(s.out)
	}
	out := make([]int16, n)
	copy(out, s.out[:n])
	s.out = append(s.out[:0], s.out[n:]...)
	return out
}

// Pending returns how many samples are waiting in the buffer.
func (s *APU) Pending() int { return len(s.out) / 2 }

// --- save states ---------------------------------------------------------

// SerializeSize is the fixed size of the APU state.
const SerializeSize = 512

const serializeVersion = 1

// Serialize writes the full APU state into buf: registers, wave
// RAM, every channel's dynamic state, the frame sequencer step,
// and the sample generator accumulators (they drive the render
// ledger, so a restore must resume them exactly).
func (s *APU) Serialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("sound: serialize buffer too small")
	}
	for i := 0; i < SerializeSize; i++ {
		buf[i] = 0
	}
	buf[0] = serializeVersion
	buf[1] = s.nr10
	buf[2] = s.nr11
	buf[3] = s.nr12
	buf[4] = s.nr21
	buf[5] = s.nr22
	buf[6] = s.nr30
	buf[7] = s.nr32
	buf[8] = s.nr42
	buf[9] = s.nr43
	buf[10] = s.nr50
	buf[11] = s.nr51
	buf[12] = s.len1
	buf[13] = s.len2
	buf[14] = s.len3
	buf[15] = s.len4
	buf[16] = s.ctrl1
	buf[17] = s.ctrl2
	buf[18] = s.ctrl3
	buf[19] = s.ctrl4
	if s.power {
		buf[20] = 1
	}
	buf[21] = uint8(s.fsStep)
	copy(buf[22:38], s.waveRAM[:])

	off := 38
	putSquare := func(ch *square) {
		b := buf[off:]
		if ch.enabled {
			b[0] = 1
		}
		if ch.dacOn {
			b[1] = 1
		}
		binary.LittleEndian.PutUint16(b[2:], ch.freq)
		binary.LittleEndian.PutUint32(b[4:], uint32(int32(ch.timer)))
		b[8] = uint8(ch.dutyPos)
		b[9] = uint8(ch.duty)
		binary.LittleEndian.PutUint16(b[10:], uint16(ch.length))
		if ch.lengthOn {
			b[12] = 1
		}
		b[13] = uint8(ch.vol)
		binary.LittleEndian.PutUint16(b[14:], uint16(ch.envTimer))
		b[16] = uint8(ch.envPace)
		if ch.envUp {
			b[17] = 1
		}
		binary.LittleEndian.PutUint16(b[18:], ch.shadow)
		binary.LittleEndian.PutUint16(b[20:], uint16(ch.sweepTimer))
		b[22] = uint8(ch.sweepPace)
		b[23] = uint8(ch.sweepSlope)
		if ch.sweepNeg {
			b[24] = 1
		}
		if ch.sweepOn {
			b[25] = 1
		}
		off += 26
	}
	putSquare(&s.ch1)
	putSquare(&s.ch2)
	{
		b := buf[off:]
		if s.ch3.enabled {
			b[0] = 1
		}
		if s.ch3.dacOn {
			b[1] = 1
		}
		binary.LittleEndian.PutUint16(b[2:], s.ch3.freq)
		binary.LittleEndian.PutUint32(b[4:], uint32(int32(s.ch3.timer)))
		b[8] = uint8(s.ch3.pos)
		binary.LittleEndian.PutUint16(b[9:], uint16(s.ch3.length))
		if s.ch3.lengthOn {
			b[11] = 1
		}
		b[12] = uint8(s.ch3.volCode)
		off += 13
	}
	{
		b := buf[off:]
		if s.ch4.enabled {
			b[0] = 1
		}
		if s.ch4.dacOn {
			b[1] = 1
		}
		binary.LittleEndian.PutUint16(b[2:], s.ch4.lfsr)
		binary.LittleEndian.PutUint32(b[4:], uint32(int32(s.ch4.timer)))
		binary.LittleEndian.PutUint16(b[8:], uint16(s.ch4.length))
		if s.ch4.lengthOn {
			b[10] = 1
		}
		b[11] = uint8(s.ch4.vol)
		binary.LittleEndian.PutUint16(b[12:], uint16(s.ch4.envTimer))
		b[14] = uint8(s.ch4.envPace)
		if s.ch4.envUp {
			b[15] = 1
		}
		off += 16
	}
	binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(s.phase))
	binary.LittleEndian.PutUint64(buf[off+8:], math.Float64bits(s.sumL))
	binary.LittleEndian.PutUint64(buf[off+16:], math.Float64bits(s.sumR))
	binary.LittleEndian.PutUint32(buf[off+24:], uint32(s.sumN))
	return nil
}

// Deserialize restores a state written by Serialize.
func (s *APU) Deserialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("sound: deserialize buffer too small")
	}
	if buf[0] != serializeVersion {
		return errors.New("sound: unsupported serialize version")
	}
	out := s.out[:0]
	*s = APU{out: out}
	s.nr10, s.nr11, s.nr12 = buf[1], buf[2], buf[3]
	s.nr21, s.nr22 = buf[4], buf[5]
	s.nr30, s.nr32 = buf[6], buf[7]
	s.nr42, s.nr43 = buf[8], buf[9]
	s.nr50, s.nr51 = buf[10], buf[11]
	s.len1, s.len2, s.len3, s.len4 = buf[12], buf[13], buf[14], buf[15]
	s.ctrl1, s.ctrl2, s.ctrl3, s.ctrl4 = buf[16], buf[17], buf[18], buf[19]
	s.power = buf[20] != 0
	s.fsStep = int(buf[21])
	copy(s.waveRAM[:], buf[22:38])
	// Re-derive the register-driven channel fields.
	s.ch1.sweepPace = int(s.nr10>>4) & 7
	s.ch1.sweepNeg = s.nr10&0x08 != 0
	s.ch1.sweepSlope = int(s.nr10) & 7
	s.ch1.duty = int(s.nr11 >> 6)
	s.ch2.duty = int(s.nr21 >> 6)
	s.ch1.dacOn = s.nr12&0xF8 != 0
	s.ch2.dacOn = s.nr22&0xF8 != 0
	s.ch4.dacOn = s.nr42&0xF8 != 0
	s.ch3.dacOn = s.nr30&0x80 != 0
	s.ch3.volCode = int(s.nr32>>5) & 3
	s.ch1.envUp = s.nr12&0x08 != 0
	s.ch1.envPace = int(s.nr12) & 7
	s.ch1.envInit = int(s.nr12 >> 4)
	s.ch2.envUp = s.nr22&0x08 != 0
	s.ch2.envPace = int(s.nr22) & 7
	s.ch2.envInit = int(s.nr22 >> 4)
	s.ch4.envUp = s.nr42&0x08 != 0
	s.ch4.envPace = int(s.nr42) & 7
	s.ch4.envInit = int(s.nr42 >> 4)

	off := 38
	getSquare := func(ch *square) {
		b := buf[off:]
		ch.enabled = b[0] != 0
		ch.dacOn = b[1] != 0
		ch.freq = binary.LittleEndian.Uint16(b[2:])
		ch.timer = int(int32(binary.LittleEndian.Uint32(b[4:])))
		ch.dutyPos = int(b[8])
		ch.duty = int(b[9])
		ch.length = int(binary.LittleEndian.Uint16(b[10:]))
		ch.lengthOn = b[12] != 0
		ch.vol = int(b[13])
		ch.envTimer = int(binary.LittleEndian.Uint16(b[14:]))
		ch.envPace = int(b[16])
		ch.envUp = b[17] != 0
		ch.shadow = binary.LittleEndian.Uint16(b[18:])
		ch.sweepTimer = int(binary.LittleEndian.Uint16(b[20:]))
		ch.sweepPace = int(b[22])
		ch.sweepSlope = int(b[23])
		ch.sweepNeg = b[24] != 0
		ch.sweepOn = b[25] != 0
		off += 26
	}
	getSquare(&s.ch1)
	getSquare(&s.ch2)
	{
		b := buf[off:]
		s.ch3.enabled = b[0] != 0
		s.ch3.dacOn = b[1] != 0
		s.ch3.freq = binary.LittleEndian.Uint16(b[2:])
		s.ch3.timer = int(int32(binary.LittleEndian.Uint32(b[4:])))
		s.ch3.pos = int(b[8])
		s.ch3.length = int(binary.LittleEndian.Uint16(b[9:]))
		s.ch3.lengthOn = b[11] != 0
		s.ch3.volCode = int(b[12])
		off += 13
	}
	{
		b := buf[off:]
		s.ch4.enabled = b[0] != 0
		s.ch4.dacOn = b[1] != 0
		s.ch4.lfsr = binary.LittleEndian.Uint16(b[2:])
		s.ch4.timer = int(int32(binary.LittleEndian.Uint32(b[4:])))
		s.ch4.length = int(binary.LittleEndian.Uint16(b[8:]))
		s.ch4.lengthOn = b[10] != 0
		s.ch4.vol = int(b[11])
		s.ch4.envTimer = int(binary.LittleEndian.Uint16(b[12:]))
		s.ch4.envPace = int(b[14])
		s.ch4.envUp = b[15] != 0
		off += 16
	}
	s.phase = math.Float64frombits(binary.LittleEndian.Uint64(buf[off:]))
	s.sumL = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+8:]))
	s.sumR = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+16:]))
	s.sumN = int(binary.LittleEndian.Uint32(buf[off+24:]))
	return nil
}
