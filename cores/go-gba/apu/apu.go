// Package apu is the GBA audio processing unit, built
// from documented register and channel behaviour and
// from the proven PSG logic of our Game Boy core: two
// square channels (channel 1 with a frequency sweep), a
// wave channel reading two banks of 4 bit digits, and a
// noise channel driven by an LFSR, plus two Direct Sound
// FIFOs fed by timer overflows.
//
// Timing runs in GBA CPU cycles (16777216 Hz). A 512 Hz
// frame sequencer clocks length counters on steps 0, 2,
// 4 and 6, the sweep on steps 2 and 6, and the envelopes
// on step 7. The mixer accumulates each cycle of output
// into a box filter and emits one stereo sample at the
// rate selected by the SOUNDBIAS resolution bits.
//
// All register offsets in this package are relative to
// the IO base 0x04000000.
package apu

import (
	"encoding/binary"
	"math"
)

// cpuHz is the GBA master clock.
const cpuHz = 16777216

// fsPeriod is the frame sequencer step period in cycles:
// 16777216 / 512 = 32768.
const fsPeriod = 32768

// Register offsets handled by the APU.
const (
	regSound1L = 0x60
	regSound1H = 0x62
	regSound1X = 0x64
	regSound2H = 0x68
	regSound2X = 0x6C
	regSound3L = 0x70
	regSound3H = 0x72
	regSound3X = 0x74
	regSound4L = 0x78
	regSound4X = 0x7C
	regCntL    = 0x80
	regCntH    = 0x82
	regCntX    = 0x84
	regBias    = 0x88
	regWaveRAM = 0x90
	regFIFOAB  = 0xA0
	regFIFOBB  = 0xA4
)

// dutyPatterns are the four square waveforms, one bit per
// step, MSB first in step order. Ported from the Game
// Boy core.
var dutyPatterns = [4]uint8{0x01, 0x03, 0x0F, 0x3F}

// noiseDivisors maps the dividing ratio code to the
// noise timer base period in CPU cycles, before the
// shift clock is applied.
var noiseDivisors = [8]int{32, 64, 128, 192, 256, 320, 384, 448}

// sampleRates maps a SOUNDBIAS resolution code to the
// output sample rate in Hz.
var sampleRates = [4]int{32768, 65536, 131072, 262144}

type square struct {
	enabled  bool
	freq     int // 11 bit frequency value
	timer    int // cycles until the next duty step
	dutyPos  int
	length   int
	lengthOn bool
	vol      int // live envelope volume
	envTimer int

	// Sweep (channel 1 only).
	shadow     int
	sweepTimer int
	sweepOn    bool
}

type wave struct {
	enabled  bool
	freq     int
	timer    int
	pos      int // digit position in the playing sequence
	length   int
	lengthOn bool
}

type noise struct {
	enabled  bool
	lfsr     uint16
	outBit   int
	timer    int
	length   int
	lengthOn bool
	vol      int
	envTimer int
}

type fifo struct {
	buf  [32]byte
	head int
	len  int
	cur  int8
}

func (f *fifo) clear() {
	for i := range f.buf {
		f.buf[i] = 0
	}
	f.head = 0
	f.len = 0
	f.cur = 0
}

func (f *fifo) pushByte(b byte) {
	if f.len >= 32 {
		return
	}
	f.buf[(f.head+f.len)&31] = b
	f.len++
}

func (f *fifo) pushWord(w uint32) {
	for i := 0; i < 4; i++ {
		f.pushByte(byte(w >> (8 * i)))
	}
}

func (f *fifo) pop() {
	if f.len <= 0 {
		return
	}
	f.cur = int8(f.buf[f.head&31])
	f.head = (f.head + 1) & 31
	f.len--
}

// APU is one GBA audio processing unit.
type APU struct {
	// Refill, when non-nil, is called from
	// OnTimerOverflow after a FIFO pop that leaves the
	// FIFO at or below half full, so the caller can top
	// the FIFO up (normally from DMA). The argument is
	// the FIFO index: 0 for A, 1 for B.
	Refill func(fifo int)

	regs [42]byte // register shadow, offsets 0x60 to 0x89

	ch1 square
	ch2 square
	ch3 wave
	ch4 noise

	waveRAM [32]byte // bank 0 bytes first, then bank 1

	fifoA fifo
	fifoB fifo

	master  bool
	fsStep  int // next frame sequencer step to execute, 0 to 7
	fsPhase int // cycles counted toward the next step

	sampleCount int
	sumL        float64
	sumR        float64
	pending     []int16
}

// New returns an APU in power-on state: master sound
// off, all channels off, frame sequencer at step 0, and
// SOUNDBIAS at its default value 0x200.
func New() *APU {
	a := &APU{}
	a.regs[regBias-regSound1L] = 0x00
	a.regs[regBias+1-regSound1L] = 0x02
	return a
}

// validSize reports whether size is a routable IO access
// width (byte, halfword or word).
func validSize(size int) bool {
	return size == 1 || size == 2 || size == 4
}

// compose builds a little endian read of size bytes
// starting at off from per byte values.
func compose(off uint32, size int, byteAt func(addr uint32) byte) uint32 {
	var v uint32
	for i := 0; i < size; i++ {
		v |= uint32(byteAt(off+uint32(i))) << (8 * i)
	}
	return v
}

// decompose splits a little endian write of size bytes
// starting at off into per byte writes, lowest byte
// first.
func decompose(off uint32, size int, val uint32, writeByte func(addr uint32, b byte)) {
	for i := 0; i < size; i++ {
		writeByte(off+uint32(i), byte(val>>(8*i)))
	}
}

func put16(dst []byte, i int, v uint16) {
	binary.LittleEndian.PutUint16(dst[i:], v)
}

func put32(dst []byte, i int, v uint32) {
	binary.LittleEndian.PutUint32(dst[i:], v)
}

func put64(dst []byte, i int, v uint64) {
	binary.LittleEndian.PutUint64(dst[i:], v)
}

func get16(src []byte, i int) uint16 {
	return binary.LittleEndian.Uint16(src[i:])
}

func get32(src []byte, i int) uint32 {
	return binary.LittleEndian.Uint32(src[i:])
}

func get64(src []byte, i int) uint64 {
	return binary.LittleEndian.Uint64(src[i:])
}

// reg16 returns the stored halfword at a register
// offset in 0x60 to 0x89.
func (a *APU) reg16(off uint32) uint16 {
	i := off - regSound1L
	return uint16(a.regs[i]) | uint16(a.regs[i+1])<<8
}

func (a *APU) regByte(off uint32) byte {
	return a.regs[off-regSound1L]
}

// biasReg returns the stored SOUNDBIAS halfword.
func (a *APU) biasReg() uint16 {
	return a.reg16(regBias)
}

// biasValue is the DC bias added in mixing.
func (a *APU) biasValue() float64 {
	return float64(a.biasReg() & 0x3FE)
}

// resolution returns the SOUNDBIAS resolution code.
func (a *APU) resolution() int {
	return int(a.biasReg()>>14) & 3
}

// SampleRate returns the current output sample rate in
// Hz, from the SOUNDBIAS resolution bits.
func (a *APU) SampleRate() int {
	return sampleRates[a.resolution()]
}

// samplePeriod is the number of CPU cycles per output
// sample at the current rate.
func (a *APU) samplePeriod() int {
	return cpuHz / a.SampleRate()
}

// nextClocksLength reports whether the next frame
// sequencer step to execute clocks the length counters.
// fsStep holds the next step to execute (see the frame
// sequencer clock, which executes a step and only then
// advances fsStep), and the length steps are the even
// steps 0, 2, 4 and 6, so the rule is fsStep % 2 == 0.
// This matches the Game Boy core in this repository
// exactly; its sound suite passes with this parity.
func (a *APU) nextClocksLength() bool {
	return a.fsStep%2 == 0
}

// lenEnableEdge is the length enable edge quirk, ported
// from the Game Boy core. When the length enable bit
// rises while the channel is enabled and the next frame
// sequencer step clocks lengths, the counter is clocked
// immediately; if that empties it and the length enable
// is set, the channel turns off.
func (a *APU) lenEnableEdge(length *int, lengthOn *bool, enabled *bool, newOn bool) {
	if newOn && !*lengthOn && *enabled && a.nextClocksLength() {
		if *length > 0 {
			*length--
			if *length == 0 && newOn {
				*enabled = false
			}
		}
	}
	*lengthOn = newOn
}

// dacOn reports whether a square or noise channel DAC
// is on: its envelope byte has any of bits 3 to 7 set.
// The wave channel DAC is bit 7 of SOUND3CNT_L.
func (a *APU) dacOn(envOff uint32) bool {
	return a.regByte(envOff)&0xF8 != 0
}

func (a *APU) waveDACOn() bool {
	return a.regByte(regSound3L)&0x80 != 0
}

// sweepPace, sweepSlope and sweepNeg read channel 1
// sweep parameters from SOUND1CNT_L.
func (a *APU) sweepPace() int {
	return int(a.regByte(regSound1L)>>4) & 7
}

func (a *APU) sweepSlope() int {
	return int(a.regByte(regSound1L)) & 7
}

func (a *APU) sweepNeg() bool {
	return a.regByte(regSound1L)&0x08 != 0
}

// calcSweep computes the frequency the sweep would move
// channel 1 to, from the shadow register. Ported from
// the Game Boy core.
func (a *APU) calcSweep() int {
	delta := a.ch1.shadow >> uint(a.sweepSlope())
	if a.sweepNeg() {
		return a.ch1.shadow - delta
	}
	return a.ch1.shadow + delta
}

// syncFreqSquare reloads a square channel frequency
// from the stored CNT_X halfword.
func (a *APU) syncFreqSquare(ch *square, off uint32) {
	ch.freq = int(a.reg16(off) & 0x7FF)
}

// triggerSquare starts a square channel: the envelope
// restarts, the timer and duty position reload, the
// length counter reloads if empty (with the quirk
// decrement), and channel 1 restarts its sweep with the
// immediate overflow check. Ported from the Game Boy
// core with GBA timer scaling.
func (a *APU) triggerSquare(ch *square, swept bool, envOff uint32) {
	env := a.regByte(envOff)
	if env&0xF8 == 0 {
		ch.enabled = false
		return
	}
	ch.enabled = true
	if ch.length == 0 {
		ch.length = 64
		if ch.lengthOn && a.nextClocksLength() {
			ch.length--
		}
	}
	ch.timer = (2048 - ch.freq) * 16
	ch.dutyPos = 0
	ch.vol = int(env >> 4)
	ch.envTimer = int(env & 7)
	if swept {
		ch.shadow = ch.freq
		ch.sweepTimer = a.sweepPace()
		if ch.sweepTimer == 0 {
			ch.sweepTimer = 8
		}
		ch.sweepOn = a.sweepPace() != 0 || a.sweepSlope() != 0
		if a.sweepSlope() != 0 {
			if a.calcSweep() > 2047 {
				ch.enabled = false
			}
		}
	}
}

func (a *APU) triggerWave() {
	if !a.waveDACOn() {
		a.ch3.enabled = false
		return
	}
	a.ch3.enabled = true
	if a.ch3.length == 0 {
		a.ch3.length = 256
		if a.ch3.lengthOn && a.nextClocksLength() {
			a.ch3.length--
		}
	}
	a.ch3.timer = (2048 - a.ch3.freq) * 8
	a.ch3.pos = 0
}

// noisePeriod converts SOUND4CNT_H low byte fields to
// the LFSR timer period in cycles.
func (a *APU) noisePeriod() int {
	b := a.regByte(regSound4X)
	ratio := int(b) & 7
	shift := int(b>>4) & 0xF
	return noiseDivisors[ratio] << uint(shift)
}

func (a *APU) triggerNoise() {
	env := a.regByte(regSound4L + 1)
	if env&0xF8 == 0 {
		a.ch4.enabled = false
		return
	}
	a.ch4.enabled = true
	if a.ch4.length == 0 {
		a.ch4.length = 64
		if a.ch4.lengthOn && a.nextClocksLength() {
			a.ch4.length--
		}
	}
	if a.regByte(regSound4X)&0x08 != 0 {
		a.ch4.lfsr = 0x40
	} else {
		a.ch4.lfsr = 0x4000
	}
	a.ch4.outBit = 0
	a.ch4.timer = a.noisePeriod()
	a.ch4.vol = int(env >> 4)
	a.ch4.envTimer = int(env & 7)
}

// byteAt returns the register byte at an IO offset,
// applying the documented read masks. Offsets outside
// the owned set read as zero; FIFO ports read as zero.
func (a *APU) byteAt(off uint32) byte {
	switch {
	case off >= regWaveRAM && off < regWaveRAM+16:
		bank := int((a.regByte(regSound3L)>>6)&1) ^ 1
		return a.waveRAM[bank*16+int(off-regWaveRAM)]
	case off >= regFIFOAB && off < regFIFOAB+8:
		return 0
	case off == regCntX:
		var v byte
		if a.ch1.enabled {
			v |= 1
		}
		if a.ch2.enabled {
			v |= 2
		}
		if a.ch3.enabled {
			v |= 4
		}
		if a.ch4.enabled {
			v |= 8
		}
		if a.master {
			v |= 0x80
		}
		return v
	case off == regCntX+1:
		return 0
	case off < regSound1L || off > regBias+1:
		return 0
	}
	stored := a.regs[off-regSound1L]
	switch off {
	case regSound1L:
		return stored | 0x80
	case regSound1H, regSound2H, regSound4L:
		// Duty and length fields: only the top two
		// bits read back, the rest read as ones.
		return (stored & 0xC0) | 0x3F
	case regSound1X, regSound2X, regSound3X:
		// Frequency low bytes are write only.
		return 0xFF
	case regSound1X + 1, regSound2X + 1, regSound3X + 1, regSound4X + 1:
		// Only the length enable bit reads back.
		return (stored & 0x40) | 0x3F
	case regSound3L:
		return stored | 0x1F
	case regSound3H:
		// Length load byte is write only.
		return 0xFF
	case regCntH + 1:
		// The FIFO reset strobes (bits 11 and 15 of
		// the halfword) read as zero.
		return stored & 0x77
	}
	return stored
}

// writeByte applies one byte of a register write.
func (a *APU) writeByte(off uint32, b byte) {
	switch {
	case off >= regWaveRAM && off < regWaveRAM+16:
		bank := int((a.regByte(regSound3L)>>6)&1) ^ 1
		a.waveRAM[bank*16+int(off-regWaveRAM)] = b
		return
	case off >= regFIFOAB && off < regFIFOAB+4:
		a.fifoA.pushByte(b)
		return
	case off >= regFIFOBB && off < regFIFOBB+4:
		a.fifoB.pushByte(b)
		return
	case off < regSound1L || off > regBias+1:
		return
	}

	if off == regCntX {
		a.writeMaster(b)
		return
	}
	if !a.master && off >= regSound1L && off <= regCntL+1 {
		// While the master enable is off, writes to
		// offsets 0x60 to 0x81 are ignored.
		return
	}

	oldRes := a.resolution()
	a.regs[off-regSound1L] = b

	switch off {
	case regSound1H:
		a.ch1.length = 64 - int(b&0x3F)
	case regSound1H + 1:
		if b&0xF8 == 0 {
			a.ch1.enabled = false
		}
	case regSound1X:
		a.syncFreqSquare(&a.ch1, regSound1X)
	case regSound1X + 1:
		a.syncFreqSquare(&a.ch1, regSound1X)
		a.lenEnableEdge(&a.ch1.length, &a.ch1.lengthOn, &a.ch1.enabled, b&0x40 != 0)
		if b&0x80 != 0 {
			a.triggerSquare(&a.ch1, true, regSound1H+1)
		}
	case regSound2H:
		a.ch2.length = 64 - int(b&0x3F)
	case regSound2H + 1:
		if b&0xF8 == 0 {
			a.ch2.enabled = false
		}
	case regSound2X:
		a.syncFreqSquare(&a.ch2, regSound2X)
	case regSound2X + 1:
		a.syncFreqSquare(&a.ch2, regSound2X)
		a.lenEnableEdge(&a.ch2.length, &a.ch2.lengthOn, &a.ch2.enabled, b&0x40 != 0)
		if b&0x80 != 0 {
			a.triggerSquare(&a.ch2, false, regSound2H+1)
		}
	case regSound3L:
		if b&0x80 == 0 {
			a.ch3.enabled = false
		}
	case regSound3H:
		a.ch3.length = 256 - int(b)
	case regSound3X:
		a.ch3.freq = int(a.reg16(regSound3X) & 0x7FF)
	case regSound3X + 1:
		a.ch3.freq = int(a.reg16(regSound3X) & 0x7FF)
		a.lenEnableEdge(&a.ch3.length, &a.ch3.lengthOn, &a.ch3.enabled, b&0x40 != 0)
		if b&0x80 != 0 {
			a.triggerWave()
		}
	case regSound4L:
		a.ch4.length = 64 - int(b&0x3F)
	case regSound4L + 1:
		if b&0xF8 == 0 {
			a.ch4.enabled = false
		}
	case regSound4X + 1:
		a.lenEnableEdge(&a.ch4.length, &a.ch4.lengthOn, &a.ch4.enabled, b&0x40 != 0)
		if b&0x80 != 0 {
			a.triggerNoise()
		}
	case regCntH + 1:
		if b&0x08 != 0 {
			a.fifoA.clear()
		}
		if b&0x80 != 0 {
			a.fifoB.clear()
		}
	case regBias, regBias + 1:
		if a.resolution() != oldRes {
			a.sumL = 0
			a.sumR = 0
			a.sampleCount = 0
		}
	}
}

// writeMaster handles the SOUNDCNT_X low byte: bit 7 is
// the master enable, the only writable bit. On a 1 to 0
// transition the register shadow for offsets 0x60 to
// 0x81 is zeroed and all channels are disabled;
// SOUNDCNT_H, SOUNDBIAS, wave RAM and FIFO contents are
// left untouched. Either transition also resets the
// output sample accumulator, so no partially averaged
// sample straddles the switch: output stops (and
// restarts) exactly at the write.
func (a *APU) writeMaster(b byte) {
	on := b&0x80 != 0
	a.regs[regCntX-regSound1L] = b & 0x80
	if on != a.master {
		a.sumL = 0
		a.sumR = 0
		a.sampleCount = 0
	}
	if a.master && !on {
		for i := 0; i <= int(regCntL+1-regSound1L); i++ {
			a.regs[i] = 0
		}
		a.ch1.enabled = false
		a.ch2.enabled = false
		a.ch3.enabled = false
		a.ch4.enabled = false
		a.ch1.lengthOn = false
		a.ch2.lengthOn = false
		a.ch3.lengthOn = false
		a.ch4.lengthOn = false
	}
	a.master = on
}

// ReadReg reads size bytes at off, composed little
// endian. Offsets outside the owned set read as zero.
func (a *APU) ReadReg(off uint32, size int) uint32 {
	if !validSize(size) {
		return 0
	}
	return compose(off, size, a.byteAt)
}

// WriteReg writes size bytes at off, decomposed little
// endian into byte writes.
func (a *APU) WriteReg(off uint32, size int, val uint32) {
	if !validSize(size) {
		return
	}
	decompose(off, size, val, a.writeByte)
}

// clockLengths clocks the four length counters.
func (a *APU) clockLengths() {
	clock := func(length *int, lengthOn bool, enabled *bool) {
		if lengthOn && *length > 0 {
			*length--
			if *length == 0 {
				*enabled = false
			}
		}
	}
	clock(&a.ch1.length, a.ch1.lengthOn, &a.ch1.enabled)
	clock(&a.ch2.length, a.ch2.lengthOn, &a.ch2.enabled)
	clock(&a.ch3.length, a.ch3.lengthOn, &a.ch3.enabled)
	clock(&a.ch4.length, a.ch4.lengthOn, &a.ch4.enabled)
}

// clockSweep clocks channel 1 sweep. The first
// calculated frequency is written, the second
// calculation is an overflow check only. Ported from
// the Game Boy core.
func (a *APU) clockSweep() {
	ch := &a.ch1
	pace := a.sweepPace()
	if !ch.sweepOn || pace == 0 {
		return
	}
	if ch.sweepTimer > 0 {
		ch.sweepTimer--
	}
	if ch.sweepTimer != 0 {
		return
	}
	ch.sweepTimer = pace
	if !ch.enabled {
		return
	}
	next := a.calcSweep()
	if next > 2047 {
		ch.enabled = false
		return
	}
	ch.freq = next
	ch.shadow = next
	if a.calcSweep() > 2047 {
		ch.enabled = false
	}
}

// clockEnvelopes clocks the square and noise envelopes.
func (a *APU) clockEnvelopes() {
	env := func(vol *int, timer *int, envOff uint32) {
		b := a.regByte(envOff)
		pace := int(b & 7)
		if pace == 0 || b&0xF8 == 0 {
			return
		}
		if *timer > 0 {
			*timer--
		}
		if *timer == 0 {
			*timer = pace
			if b&0x08 != 0 && *vol < 15 {
				*vol++
			}
			if b&0x08 == 0 && *vol > 0 {
				*vol--
			}
		}
	}
	env(&a.ch1.vol, &a.ch1.envTimer, regSound1H+1)
	env(&a.ch2.vol, &a.ch2.envTimer, regSound2H+1)
	env(&a.ch4.vol, &a.ch4.envTimer, regSound4L+1)
}

// execFSStep runs the frame sequencer step held in
// fsStep, then advances fsStep to the next step.
func (a *APU) execFSStep() {
	step := a.fsStep
	switch step {
	case 0, 2, 4, 6:
		a.clockLengths()
	}
	switch step {
	case 2, 6:
		a.clockSweep()
	case 7:
		a.clockEnvelopes()
	}
	a.fsStep = (a.fsStep + 1) & 7
}

// waveDigits is the number of digits in the playing
// sequence: 32 in one bank mode, 64 in two bank mode.
func (a *APU) waveDigits() int {
	if a.regByte(regSound3L)&0x20 != 0 {
		return 64
	}
	return 32
}

// clockNoiseLFSR advances the noise LFSR one step in
// the Galois form documented for the GBA.
func (a *APU) clockNoiseLFSR() {
	x := a.ch4.lfsr
	carry := x & 1
	x >>= 1
	if a.regByte(regSound4X)&0x08 != 0 {
		x &= 0x7F
		if carry != 0 {
			x ^= 0x60
			a.ch4.outBit = 1
		} else {
			a.ch4.outBit = 0
		}
		a.ch4.lfsr = x & 0x7F
	} else {
		if carry != 0 {
			x ^= 0x6000
			a.ch4.outBit = 1
		} else {
			a.ch4.outBit = 0
		}
		a.ch4.lfsr = x & 0x7FFF
	}
}

// tickChannels advances the channel timers by one
// cycle.
func (a *APU) tickChannels() {
	if a.ch1.enabled {
		a.ch1.timer--
		if a.ch1.timer <= 0 {
			a.ch1.timer += (2048 - a.ch1.freq) * 16
			a.ch1.dutyPos = (a.ch1.dutyPos + 1) & 7
		}
	}
	if a.ch2.enabled {
		a.ch2.timer--
		if a.ch2.timer <= 0 {
			a.ch2.timer += (2048 - a.ch2.freq) * 16
			a.ch2.dutyPos = (a.ch2.dutyPos + 1) & 7
		}
	}
	if a.ch3.enabled {
		a.ch3.timer--
		if a.ch3.timer <= 0 {
			a.ch3.timer += (2048 - a.ch3.freq) * 8
			a.ch3.pos++
			if a.ch3.pos >= a.waveDigits() {
				a.ch3.pos = 0
			}
		}
	}
	if a.ch4.enabled {
		a.ch4.timer--
		if a.ch4.timer <= 0 {
			a.ch4.timer += a.noisePeriod()
			a.clockNoiseLFSR()
		}
	}
}

// waveCode returns the current wave channel DAC code
// after volume scaling.
func (a *APU) waveCode() int {
	pos := a.ch3.pos
	bankBit := int((a.regByte(regSound3L) >> 6) & 1)
	bank := bankBit
	idx := pos
	if a.regByte(regSound3L)&0x20 != 0 {
		if pos >= 32 {
			bank = bankBit ^ 1
			idx = pos - 32
		}
	} else if idx >= 32 {
		idx &= 31
	}
	b := a.waveRAM[bank*16+idx/2]
	var nib int
	if idx%2 == 0 {
		nib = int(b >> 4)
	} else {
		nib = int(b & 0x0F)
	}
	h := a.regByte(regSound3H + 1)
	if h&0x80 != 0 {
		return (nib * 3) >> 2
	}
	switch (h >> 5) & 3 {
	case 1:
		return nib
	case 2:
		return nib >> 1
	case 3:
		return nib >> 2
	}
	return 0
}

// channelTerm returns the mixer term for a PSG channel
// (0 for channel 1, 1 for channel 2, 2 for wave, 3 for
// noise) and whether the channel contributes at all. A
// contributing channel with DAC code zero still gives
// the negative full scale term; a disabled or DAC off
// channel contributes nothing.
func (a *APU) channelTerm(ch int) (float64, bool) {
	var code int
	switch ch {
	case 0:
		if !a.ch1.enabled || !a.dacOn(regSound1H+1) {
			return 0, false
		}
		duty := int(a.regByte(regSound1H) >> 6)
		if dutyPatterns[duty]>>uint(7-a.ch1.dutyPos)&1 != 0 {
			code = a.ch1.vol
		}
	case 1:
		if !a.ch2.enabled || !a.dacOn(regSound2H+1) {
			return 0, false
		}
		duty := int(a.regByte(regSound2H) >> 6)
		if dutyPatterns[duty]>>uint(7-a.ch2.dutyPos)&1 != 0 {
			code = a.ch2.vol
		}
	case 2:
		if !a.ch3.enabled || !a.waveDACOn() {
			return 0, false
		}
		code = a.waveCode()
	case 3:
		if !a.ch4.enabled || !a.dacOn(regSound4L+1) {
			return 0, false
		}
		if a.ch4.outBit != 0 {
			code = a.ch4.vol
		}
	}
	return float64(code)*256.0/15.0 - 128.0, true
}

func clampSample(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// mixCycle accumulates one cycle of mixed output and
// emits a finished stereo sample when the accumulated
// cycle count reaches the current sample period. The
// output for a cycle is taken from the channel state at
// the start of the cycle; timers advance afterwards in
// Advance, so a channel period aligns exactly with a
// whole number of output samples.
func (a *APU) mixCycle() {
	bias := a.biasValue()
	var totalL, totalR float64
	if a.master {
		cntL := a.reg16(regCntL)
		volR := float64(cntL&7+1) / 8
		volL := float64((cntL>>4)&7+1) / 8
		ratioCode := a.regByte(regCntH) & 3
		ratio := 1.0
		switch ratioCode {
		case 0:
			ratio = 0.25
		case 1:
			ratio = 0.5
		}
		for ch := 0; ch < 4; ch++ {
			term, on := a.channelTerm(ch)
			if !on {
				continue
			}
			if cntL>>uint(12+ch)&1 != 0 {
				totalL += term
			}
			if cntL>>uint(8+ch)&1 != 0 {
				totalR += term
			}
		}
		totalL *= volL * ratio
		totalR *= volR * ratio

		cntH := a.reg16(regCntH)
		volA := 0.5
		if cntH&0x0004 != 0 {
			volA = 1.0
		}
		volB := 0.5
		if cntH&0x0008 != 0 {
			volB = 1.0
		}
		if cntH&0x0200 != 0 {
			totalL += float64(a.fifoA.cur) * volA * 4.0
		}
		if cntH&0x0100 != 0 {
			totalR += float64(a.fifoA.cur) * volA * 4.0
		}
		if cntH&0x2000 != 0 {
			totalL += float64(a.fifoB.cur) * volB * 4.0
		}
		if cntH&0x1000 != 0 {
			totalR += float64(a.fifoB.cur) * volB * 4.0
		}
	}
	a.sumL += bias + totalL
	a.sumR += bias + totalR
	a.sampleCount++
	if a.sampleCount >= a.samplePeriod() {
		n := float64(a.sampleCount)
		avgL := a.sumL / n
		avgR := a.sumR / n
		if avgL < 0 {
			avgL = 0
		}
		if avgL > 1023 {
			avgL = 1023
		}
		if avgR < 0 {
			avgR = 0
		}
		if avgR > 1023 {
			avgR = 1023
		}
		a.pending = append(a.pending,
			clampSample((avgL-bias)*64), clampSample((avgR-bias)*64))
		a.sumL = 0
		a.sumR = 0
		a.sampleCount = 0
	}
}

// Advance moves the APU forward by the given number of
// CPU cycles. While the master enable is off, channel
// and sequencer state does not advance and only silence
// samples are emitted, at the current sample rate.
func (a *APU) Advance(cycles int) {
	for i := 0; i < cycles; i++ {
		a.mixCycle()
		if !a.master {
			continue
		}
		a.tickChannels()
		a.fsPhase++
		if a.fsPhase >= fsPeriod {
			a.fsPhase = 0
			a.execFSStep()
		}
	}
}

// AudioBuffer drains the pending stereo samples,
// interleaved left then right per frame.
func (a *APU) AudioBuffer() []int16 {
	out := a.pending
	a.pending = nil
	if out == nil {
		return []int16{}
	}
	return out
}

// PushFIFO appends the four bytes of word, little
// endian lowest byte first, to FIFO 0 (A) or 1 (B).
// Bytes that do not fit in the 32 byte ring are
// dropped.
func (a *APU) PushFIFO(fifo int, word uint32) {
	switch fifo {
	case 0:
		a.fifoA.pushWord(word)
	case 1:
		a.fifoB.pushWord(word)
	}
}

// FIFOLevel returns the number of bytes held in FIFO 0
// (A) or 1 (B).
func (a *APU) FIFOLevel(fifo int) int {
	switch fifo {
	case 0:
		return a.fifoA.len
	case 1:
		return a.fifoB.len
	}
	return 0
}

// WantsData reports whether FIFO 0 (A) or 1 (B) should
// be refilled: the master enable is on and the FIFO is
// at or below half full.
func (a *APU) WantsData(fifo int) bool {
	if !a.master {
		return false
	}
	return a.FIFOLevel(fifo) <= 16
}

// OnTimerOverflow is called when a hardware timer
// overflows. Each Direct Sound channel whose timer
// select matches pops one sample from its FIFO; if the
// FIFO is then at or below half full, Refill is called
// for it when set. While the master enable is off,
// nothing happens.
func (a *APU) OnTimerOverflow(timer int) {
	if !a.master {
		return
	}
	cntH := a.reg16(regCntH)
	if int(cntH>>10)&1 == timer&1 {
		a.fifoA.pop()
		if a.fifoA.len <= 16 && a.Refill != nil {
			a.Refill(0)
		}
	}
	if int(cntH>>14)&1 == timer&1 {
		a.fifoB.pop()
		if a.fifoB.len <= 16 && a.Refill != nil {
			a.Refill(1)
		}
	}
}

// DebugChannelOn reports whether channel ch (1 to 4) is
// currently enabled.
func (a *APU) DebugChannelOn(ch int) bool {
	switch ch {
	case 1:
		return a.ch1.enabled
	case 2:
		return a.ch2.enabled
	case 3:
		return a.ch3.enabled
	case 4:
		return a.ch4.enabled
	}
	return false
}

// DebugFreq returns the frequency value of square
// channel ch (1 or 2).
func (a *APU) DebugFreq(ch int) int {
	switch ch {
	case 1:
		return a.ch1.freq
	case 2:
		return a.ch2.freq
	}
	return 0
}

// DebugVol returns the live envelope volume of channel
// ch (1, 2 or 4).
func (a *APU) DebugVol(ch int) int {
	switch ch {
	case 1:
		return a.ch1.vol
	case 2:
		return a.ch2.vol
	case 4:
		return a.ch4.vol
	}
	return 0
}

// DebugLFSR returns the noise channel LFSR value.
func (a *APU) DebugLFSR() uint16 {
	return a.ch4.lfsr
}

// DebugNoiseOut returns the noise channel output bit.
func (a *APU) DebugNoiseOut() int {
	return a.ch4.outBit
}

// DebugWavePos returns the wave channel digit position.
func (a *APU) DebugWavePos() int {
	return a.ch3.pos
}

// StateSize is the serialized size of the APU state.
//
// Layout, all integers little endian:
//
//	0    1 byte    version, always 1
//	1    2 bytes   SOUNDBIAS halfword
//	3    1 byte    master enable flag
//	4    1 byte    frame sequencer step
//	5    3 bytes   zero padding
//	8    4 bytes   frame sequencer phase, cycles
//	12   4 bytes   sample accumulator cycle count
//	16   8 bytes   left sample sum, float64 bits
//	24   8 bytes   right sample sum, float64 bits
//	32   42 bytes  register shadow, offsets 0x60 to 0x89
//	74   32 bytes  wave RAM, bank 0 then bank 1
//	106  24 bytes  square channel 1 block
//	130  24 bytes  square channel 2 block
//	154  16 bytes  wave channel block
//	170  24 bytes  noise channel block
//	194  40 bytes  FIFO A block
//	234  40 bytes  FIFO B block
//	274  6 bytes   zero padding
//
// Square block (24 bytes): timer int32 at 0, frequency
// uint16 at 4, envelope timer uint16 at 6, sweep shadow
// uint16 at 8, sweep timer uint16 at 10, volume at 12,
// length at 13, duty position at 14, enabled flag at 15,
// sweep on flag at 16, zero at 17 to 23.
//
// Wave block (16 bytes): timer int32 at 0, frequency
// uint16 at 4, length uint16 at 6, position at 8,
// enabled flag at 9, zero at 10 to 15.
//
// Noise block (24 bytes): timer int32 at 0, envelope
// timer uint16 at 4, volume at 6, length at 7, LFSR
// uint16 at 8, output bit at 10, enabled flag at 11,
// zero at 12 to 23.
//
// FIFO block (40 bytes): ring bytes in storage order at
// 0 to 31, head at 32, length at 33, held sample byte at
// 34, zero at 35 to 39.
//
// Pending output samples are not part of the state and
// are dropped by Deserialize.
const StateSize = 280

const stateVersion = 1

// Serialize writes the APU state (see StateSize) into
// dst and returns the number of bytes used. It returns
// 0 if dst is short.
func (a *APU) Serialize(dst []byte) int {
	if len(dst) < StateSize {
		return 0
	}
	for i := 0; i < StateSize; i++ {
		dst[i] = 0
	}
	dst[0] = stateVersion
	put16(dst, 1, a.biasReg())
	if a.master {
		dst[3] = 1
	}
	dst[4] = byte(a.fsStep)
	put32(dst, 8, uint32(a.fsPhase))
	put32(dst, 12, uint32(a.sampleCount))
	put64(dst, 16, math.Float64bits(a.sumL))
	put64(dst, 24, math.Float64bits(a.sumR))
	copy(dst[32:74], a.regs[:])
	copy(dst[74:106], a.waveRAM[:])

	putSquare := func(off int, ch *square) {
		put32(dst, off+0, uint32(int32(ch.timer)))
		put16(dst, off+4, uint16(ch.freq))
		put16(dst, off+6, uint16(ch.envTimer))
		put16(dst, off+8, uint16(ch.shadow))
		put16(dst, off+10, uint16(ch.sweepTimer))
		dst[off+12] = byte(ch.vol)
		dst[off+13] = byte(ch.length)
		dst[off+14] = byte(ch.dutyPos)
		if ch.enabled {
			dst[off+15] = 1
		}
		if ch.sweepOn {
			dst[off+16] = 1
		}
	}
	putSquare(106, &a.ch1)
	putSquare(130, &a.ch2)

	put32(dst, 154, uint32(int32(a.ch3.timer)))
	put16(dst, 158, uint16(a.ch3.freq))
	put16(dst, 160, uint16(a.ch3.length))
	dst[162] = byte(a.ch3.pos)
	if a.ch3.enabled {
		dst[163] = 1
	}

	put32(dst, 170, uint32(int32(a.ch4.timer)))
	put16(dst, 174, uint16(a.ch4.envTimer))
	dst[176] = byte(a.ch4.vol)
	dst[177] = byte(a.ch4.length)
	put16(dst, 178, a.ch4.lfsr)
	dst[180] = byte(a.ch4.outBit)
	if a.ch4.enabled {
		dst[181] = 1
	}

	putFIFO := func(off int, f *fifo) {
		copy(dst[off:off+32], f.buf[:])
		dst[off+32] = byte(f.head)
		dst[off+33] = byte(f.len)
		dst[off+34] = byte(f.cur)
	}
	putFIFO(194, &a.fifoA)
	putFIFO(234, &a.fifoB)
	return StateSize
}

// Deserialize restores a state written by Serialize. It
// reports false, leaving the state untouched, when src
// is too short or carries a different version. Pending
// output samples are dropped. Register derived state
// (length enables, and everything read from registers
// at use time) is re-derived from the restored shadow.
func (a *APU) Deserialize(src []byte) bool {
	if len(src) < StateSize {
		return false
	}
	if src[0] != stateVersion {
		return false
	}
	refill := a.Refill
	t := New()
	t.Refill = refill

	copy(t.regs[:], src[32:74])
	// The SOUNDBIAS halfword has its own field at 1;
	// fold it back into the shadow bytes for 0x88.
	t.regs[regBias-regSound1L] = src[1]
	t.regs[regBias+1-regSound1L] = src[2]
	t.master = src[3] != 0
	t.fsStep = int(src[4]) & 7
	t.fsPhase = int(get32(src, 8))
	t.sampleCount = int(get32(src, 12))
	t.sumL = math.Float64frombits(get64(src, 16))
	t.sumR = math.Float64frombits(get64(src, 24))
	copy(t.waveRAM[:], src[74:106])

	getSquare := func(off int, ch *square) {
		ch.timer = int(int32(get32(src, off+0)))
		ch.freq = int(get16(src, off+4))
		ch.envTimer = int(get16(src, off+6))
		ch.shadow = int(get16(src, off+8))
		ch.sweepTimer = int(get16(src, off+10))
		ch.vol = int(src[off+12])
		ch.length = int(src[off+13])
		ch.dutyPos = int(src[off+14]) & 7
		ch.enabled = src[off+15] != 0
		ch.sweepOn = src[off+16] != 0
	}
	getSquare(106, &t.ch1)
	getSquare(130, &t.ch2)

	t.ch3.timer = int(int32(get32(src, 154)))
	t.ch3.freq = int(get16(src, 158))
	t.ch3.length = int(get16(src, 160))
	t.ch3.pos = int(src[162]) & 63
	t.ch3.enabled = src[163] != 0

	t.ch4.timer = int(int32(get32(src, 170)))
	t.ch4.envTimer = int(get16(src, 174))
	t.ch4.vol = int(src[176])
	t.ch4.length = int(src[177])
	t.ch4.lfsr = get16(src, 178)
	t.ch4.outBit = int(src[180])
	t.ch4.enabled = src[181] != 0

	getFIFO := func(off int, f *fifo) {
		copy(f.buf[:], src[off:off+32])
		f.head = int(src[off+32]) & 31
		f.len = int(src[off+33])
		if f.len > 32 {
			f.len = 32
		}
		f.cur = int8(src[off+34])
	}
	getFIFO(194, &t.fifoA)
	getFIFO(234, &t.fifoB)

	// Re-derive the length enable flags from the
	// restored CNT_X registers.
	t.ch1.lengthOn = t.regByte(regSound1X+1)&0x40 != 0
	t.ch2.lengthOn = t.regByte(regSound2X+1)&0x40 != 0
	t.ch3.lengthOn = t.regByte(regSound3X+1)&0x40 != 0
	t.ch4.lengthOn = t.regByte(regSound4X+1)&0x40 != 0

	*a = *t
	return true
}
