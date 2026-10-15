// Package sound is an original implementation of the NES audio
// processing unit (the sound half of the Ricoh 2A03), written
// from the published hardware documentation (the NESdev APU
// chapters and the documented behavior Blargg's apu_test suite
// measures).
//
// Five channels: two pulse waves (each with a sweep unit, a
// volume envelope, and a length counter), a triangle wave (with
// its linear counter), a noise channel (a 15-bit LFSR with two
// tap positions), and the DMC sample channel (delta-modulated
// playback fetched from CPU memory through DMA). A frame
// sequencer clocks the envelopes (quarter frame), the length
// counters and sweeps (half frame), and raises the frame IRQ.
//
// Timing model: the system calls Tick once per CPU cycle. The
// frame sequencer counts CPU cycles directly, with its events
// at the documented cycle offsets (quarter at 3729, 7457,
// 11186, 14915; the 5-step sequence adds its last step at
// 18641). The pulse and noise timers are clocked on APU
// cycles, every second CPU cycle, through a divider whose
// phase also decides the $4017 reset delay (3 cycles on one
// phase, 4 on the other), which is the jitter Blargg's tests
// synchronize against. The triangle and DMC timers count CPU
// cycles. A write to $4017 schedules the sequencer reset at
// that delayed point; a write selecting the 5-step sequence
// also clocks quarter and half frame immediately when the
// reset lands.
//
// Channel mixing follows the documented non-linear DAC model:
// the two pulse channels sum through one resistor network and
// the triangle, noise, and DMC through another, with the
// published resistor values. The mixed level is box-filtered
// (averaged per output sample) down to SampleRate.
package sound

import "errors"

// SampleRate is the host output rate the APU renders to.
const SampleRate = 44100

// cpuHz is the NTSC CPU clock the APU hangs off.
const cpuHz = 1789773

// lengthTable is the documented length counter load table,
// indexed by bits 7-3 of a length register write.
var lengthTable = [32]uint8{
	0x0A, 0xFE, 0x14, 0x02, 0x28, 0x04, 0x50, 0x06,
	0xA0, 0x08, 0x3C, 0x0A, 0x0E, 0x0C, 0x1A, 0x0E,
	0x0C, 0x10, 0x18, 0x12, 0x30, 0x14, 0x60, 0x16,
	0xC0, 0x18, 0x48, 0x1A, 0x10, 0x1C, 0x20, 0x1E,
}

// dutyTable is the pulse duty sequences in output order.
var dutyTable = [4][8]uint8{
	{0, 1, 0, 0, 0, 0, 0, 0},
	{0, 1, 1, 0, 0, 0, 0, 0},
	{0, 1, 1, 1, 1, 0, 0, 0},
	{1, 0, 0, 1, 1, 1, 1, 1},
}

// noisePeriods is the noise timer table in APU cycles (the
// documented CPU-cycle periods halved: the noise timer counts
// APU cycles like the pulse timers do).
var noisePeriods = [16]int{
	2, 4, 8, 16, 32, 48, 64, 80,
	101, 127, 190, 254, 381, 508, 1017, 2034,
}

// dmcRates is the DMC timer table in CPU cycles.
var dmcRates = [16]int{
	428, 380, 340, 320, 286, 254, 226, 214,
	190, 160, 142, 128, 106, 84, 72, 54,
}

// triangleSteps is the triangle channel's 32-step output
// sequence: 15 down to 0, then back up to 15.
var triangleSteps = [32]uint8{
	15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0,
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
}

// envelope is the volume envelope generator shared by the two
// pulses and the noise channel. The divider counts quarter
// frame clocks; the decay level steps every period+1 clocks.
type envelope struct {
	startFlag bool
	divider   int
	decay     int
	period    int  // the V value (also the constant volume)
	constVol  bool // output the period value instead of decay
	loop      bool // the channel's halt bit doubles as loop
}

func (e *envelope) clock() {
	if e.startFlag {
		e.startFlag = false
		e.decay = 15
		e.divider = e.period
		return
	}
	if e.divider > 0 {
		e.divider--
		return
	}
	e.divider = e.period
	if e.decay > 0 {
		e.decay--
	} else if e.loop {
		e.decay = 15
	}
}

func (e *envelope) out() int {
	if e.constVol {
		return e.period
	}
	return e.decay
}

// lengthCounter is the shared length counter: clocked at half
// frame, suspended by its halt flag, forced to zero when the
// channel is disabled through $4015.
type lengthCounter struct {
	value   int
	halt    bool
	enabled bool
}

func (l *lengthCounter) clock() {
	if !l.halt && l.value > 0 {
		l.value--
	}
}

func (l *lengthCounter) load(regVal uint8) {
	if l.enabled {
		l.value = int(lengthTable[regVal>>3])
	}
}

func (l *lengthCounter) setEnabled(on bool) {
	l.enabled = on
	if !on {
		l.value = 0
	}
}

// pulse is one square wave channel. Pulse 1's sweep negates in
// ones' complement, pulse 2's in two's complement.
type pulse struct {
	duty     int
	step     int
	period   int // 11-bit timer period register
	timer    int // APU cycles until the next duty step
	env      envelope
	length   lengthCounter
	swEnable bool
	swPeriod int
	swNegate bool
	swShift  int
	swDiv    int
	swReload bool
	onesComp bool
}

// sweepTarget computes the sweep adder's current output: the
// period shifted right, added to the period, or subtracted
// when negating (with the channel's complement flavor).
func (p *pulse) sweepTarget() int {
	shifted := p.period >> uint(p.swShift)
	if p.swNegate {
		if p.onesComp {
			return p.period - shifted - 1
		}
		return p.period - shifted
	}
	return p.period + shifted
}

// muted reports the sweep unit's continuous muting rule: a
// period under 8, or a sweep target above the 11-bit range,
// silences the channel regardless of the sweep enable.
func (p *pulse) muted() bool {
	if p.period < 8 {
		return true
	}
	return p.swShift > 0 && p.sweepTarget() > 0x7FF
}

func (p *pulse) clockSweep() {
	if p.swDiv == 0 && p.swEnable && p.swShift > 0 && !p.muted() {
		p.period = p.sweepTarget()
	}
	if p.swDiv == 0 || p.swReload {
		p.swDiv = p.swPeriod
		p.swReload = false
	} else {
		p.swDiv--
	}
}

func (p *pulse) tickTimer() {
	if p.timer > 0 {
		p.timer--
		return
	}
	p.timer = p.period
	p.step = (p.step + 1) & 7
}

func (p *pulse) out() int {
	if p.length.value == 0 || p.muted() {
		return 0
	}
	if dutyTable[p.duty][p.step] == 0 {
		return 0
	}
	return p.env.out()
}

// triangle is the triangle wave channel.
type triangle struct {
	period  int // 11-bit timer period register
	timer   int // CPU cycles until the next step
	step    int
	length  lengthCounter
	linear  int
	linLoad int
	linReLd bool // linear counter reload flag
	linCtrl bool // $4008 bit 7: linear control and length halt
}

func (t *triangle) clockLinear() {
	if t.linReLd {
		t.linear = t.linLoad
	} else if t.linear > 0 {
		t.linear--
	}
	if !t.linCtrl {
		t.linReLd = false
	}
}

func (t *triangle) tickTimer() {
	if t.timer > 0 {
		t.timer--
		return
	}
	t.timer = t.period
	// The sequencer advances only while both counters let it.
	// At periods 0 and 1 this still steps it at CPU rate, the
	// documented ultrasonic behavior: the output becomes the
	// sequence value at ultrasonic frequency rather than a
	// forced silence.
	if t.length.value > 0 && t.linear > 0 {
		t.step = (t.step + 1) & 31
	}
}

func (t *triangle) out() int {
	return int(triangleSteps[t.step])
}

// noise is the noise channel: a 15-bit LFSR whose bit 0 gates
// the envelope output.
type noise struct {
	period int // index into noisePeriods
	timer  int // APU cycles until the next shift
	mode   bool
	lfsr   uint16
	env    envelope
	length lengthCounter
}

func (n *noise) tickTimer() {
	if n.timer > 0 {
		n.timer--
		return
	}
	// The table values are the periods themselves, so the
	// countdown reloads one less and expires on schedule.
	n.timer = noisePeriods[n.period] - 1
	var tap uint16
	if n.mode {
		tap = (n.lfsr >> 6) & 1
	} else {
		tap = (n.lfsr >> 1) & 1
	}
	fb := (n.lfsr & 1) ^ tap
	n.lfsr = (n.lfsr >> 1) | (fb << 14)
}

func (n *noise) out() int {
	if n.length.value == 0 || n.lfsr&1 != 0 {
		return 0
	}
	return n.env.out()
}

// dmc is the delta modulation sample channel. The memory
// reader fetches bytes through the system's DMA hook when the
// one-byte buffer empties and playback still has bytes to go.
type dmc struct {
	irqEnabled bool
	irqFlag    bool
	loop       bool
	rateIdx    int
	addrReg    uint8
	lenReg     uint8

	outLevel    int // the 7-bit output counter
	timer       int // CPU cycles until the next output clock
	shiftReg    uint8
	bitsLeft    int
	buffer      uint8
	bufferFull  bool
	silent      bool
	currentAddr uint16
	bytesLeft   int
}

func (d *dmc) sampleAddr() uint16 { return 0xC000 + uint16(d.addrReg)*64 }
func (d *dmc) sampleLen() int     { return int(d.lenReg)*16 + 1 }

// APU is the whole audio unit.
type APU struct {
	pulse1, pulse2 pulse
	tri            triangle
	noi            noise
	dm             dmc

	phase bool // APU divider phase: true on APU-clock cycles

	// Frame sequencer.
	fcCounter int
	fcMode    int // 0 = 4-step, 1 = 5-step
	fcInhibit bool
	fcIRQ     bool
	fcPending int // ticks until a $4017 reset lands; -1 none
	fcPendMd  int

	// Lazy advance: the machine owes the APU cycles it has
	// not run yet. Advance records owed cycles; CatchUp and
	// CatchUpTo run them, oldest first, so every observer
	// (register accesses, the interrupt poll, save states)
	// syncs the unit to its exact cycle before looking.
	// executed counts the cycles run since the last epoch
	// reset (power-on or a state load); marks are named in
	// the same count.
	pending  int
	executed int64
	marks    [32]cycleMark
	markFill int // sequence number of the oldest unfilled mark, -1 if none
	markLast int // sequence number of the newest mark recorded

	// FetchDMC, when set, is called for each DMC sample byte:
	// it returns the byte at addr and charges the machine the
	// DMA stall. The system installs it.
	FetchDMC func(addr uint16) uint8

	// Sample production: the mixed level is accumulated per
	// CPU cycle and box-filtered into SampleRate output.
	mixSum   float64
	mixCount int
	mixPhase int
	out      []int16

	// Mixer memo: the mixed level is a pure function of the
	// four channel levels (pulse sum, triangle, noise, DMC),
	// and Tick asks for it on every CPU cycle, about 30
	// thousand times per frame, while the levels only move
	// when a channel timer or register write moves them. The
	// last tuple and its level are kept here; because the key
	// is the full live tuple, the memo can never go stale and
	// needs no invalidation on writes, reset, or state loads.
	mixP, mixT, mixN, mixD int
	mixLevel               float64
	mixValid               bool
}

// New returns a powered-on APU: all channels disabled, the
// frame sequencer at the start of its 4-step sequence.
func New() *APU {
	a := &APU{fcPending: -1, markFill: -1}
	a.pulse1.onesComp = true
	a.noi.lfsr = 1
	a.dm.silent = true
	a.dm.bitsLeft = 8
	for i := range a.marks {
		a.marks[i].seq = -1
	}
	return a
}

// Reset returns the APU to its post-reset state: channels
// disabled (a $4015 write of zero), the frame sequencer
// restarted, output state cleared.
func (a *APU) Reset() {
	fetch := a.FetchDMC
	*a = *New()
	a.FetchDMC = fetch
}

// IRQAsserted reports the APU's IRQ output: the frame IRQ or
// the DMC IRQ, both level flags.
func (a *APU) IRQAsserted() bool { return a.fcIRQ || a.dm.irqFlag }

// Write applies a register write ($4000-$4017).
func (a *APU) Write(addr uint16, val uint8) {
	switch {
	case addr <= 0x4003:
		a.writePulse(&a.pulse1, int(addr-0x4000), val)
	case addr <= 0x4007:
		a.writePulse(&a.pulse2, int(addr-0x4004), val)
	case addr == 0x4008:
		a.tri.linCtrl = val&0x80 != 0
		a.tri.length.halt = val&0x80 != 0
		a.tri.linLoad = int(val & 0x7F)
	case addr == 0x400A:
		a.tri.period = (a.tri.period & 0x700) | int(val)
	case addr == 0x400B:
		a.tri.period = (a.tri.period & 0xFF) | int(val&7)<<8
		a.tri.length.load(val)
		a.tri.linReLd = true
	case addr == 0x400C:
		a.noi.env.loop = val&0x20 != 0
		a.noi.length.halt = val&0x20 != 0
		a.noi.env.constVol = val&0x10 != 0
		a.noi.env.period = int(val & 0x0F)
	case addr == 0x400E:
		a.noi.mode = val&0x80 != 0
		a.noi.period = int(val & 0x0F)
	case addr == 0x400F:
		a.noi.length.load(val)
		a.noi.env.startFlag = true
	case addr == 0x4010:
		a.dm.irqEnabled = val&0x80 != 0
		if !a.dm.irqEnabled {
			a.dm.irqFlag = false
		}
		a.dm.loop = val&0x40 != 0
		a.dm.rateIdx = int(val & 0x0F)
	case addr == 0x4011:
		a.dm.outLevel = int(val & 0x7F)
	case addr == 0x4012:
		a.dm.addrReg = val
	case addr == 0x4013:
		a.dm.lenReg = val
	case addr == 0x4015:
		a.writeStatus(val)
	case addr == 0x4017:
		a.writeFrameCounter(val)
	}
}

func (a *APU) writePulse(p *pulse, reg int, val uint8) {
	switch reg {
	case 0:
		p.duty = int(val >> 6)
		p.env.loop = val&0x20 != 0
		p.length.halt = val&0x20 != 0
		p.env.constVol = val&0x10 != 0
		p.env.period = int(val & 0x0F)
	case 1:
		p.swEnable = val&0x80 != 0
		p.swPeriod = int(val>>4) & 7
		p.swNegate = val&0x08 != 0
		p.swShift = int(val & 7)
		p.swReload = true
	case 2:
		p.period = (p.period & 0x700) | int(val)
	case 3:
		p.period = (p.period & 0xFF) | int(val&7)<<8
		p.length.load(val)
		p.step = 0
		p.env.startFlag = true
	}
}

// writeStatus handles a $4015 write: channel enables, plus
// the DMC start/stop and IRQ clear rules.
func (a *APU) writeStatus(val uint8) {
	a.pulse1.length.setEnabled(val&0x01 != 0)
	a.pulse2.length.setEnabled(val&0x02 != 0)
	a.tri.length.setEnabled(val&0x04 != 0)
	a.noi.length.setEnabled(val&0x08 != 0)
	// Any $4015 write clears the DMC IRQ flag.
	a.dm.irqFlag = false
	if val&0x10 == 0 {
		a.dm.bytesLeft = 0
	} else if a.dm.bytesLeft == 0 {
		a.dm.currentAddr = a.dm.sampleAddr()
		a.dm.bytesLeft = a.dm.sampleLen()
	}
}

// ReadStatus composes a $4015 read: channel activity, the
// frame IRQ flag, and the DMC IRQ flag. Reading clears the
// frame IRQ flag (only); if the sequencer sets it again in
// the same cycle's tick (the triple set), the set wins.
func (a *APU) ReadStatus() uint8 {
	var v uint8
	if a.pulse1.length.value > 0 {
		v |= 0x01
	}
	if a.pulse2.length.value > 0 {
		v |= 0x02
	}
	if a.tri.length.value > 0 {
		v |= 0x04
	}
	if a.noi.length.value > 0 {
		v |= 0x08
	}
	if a.dm.bytesLeft > 0 {
		v |= 0x10
	}
	if a.fcIRQ {
		v |= 0x40
	}
	if a.dm.irqFlag {
		v |= 0x80
	}
	a.fcIRQ = false
	return v
}

// writeFrameCounter handles a $4017 write: the inhibit bit
// clears the frame IRQ at once, and the sequencer reset is
// scheduled 3 or 4 cycles out depending on the divider phase
// at the write. If the new sequence is the 5-step one,
// quarter and half frame clock when the reset lands.
func (a *APU) writeFrameCounter(val uint8) {
	a.fcInhibit = val&0x40 != 0
	if a.fcInhibit {
		a.fcIRQ = false
	}
	a.fcPendMd = 0
	if val&0x80 != 0 {
		a.fcPendMd = 1
	}
	if a.phase {
		a.fcPending = 3
	} else {
		a.fcPending = 4
	}
}

func (a *APU) clockQuarter() {
	a.pulse1.env.clock()
	a.pulse2.env.clock()
	a.noi.env.clock()
	a.tri.clockLinear()
}

func (a *APU) clockHalf() {
	a.pulse1.length.clock()
	a.pulse2.length.clock()
	a.tri.length.clock()
	a.noi.length.clock()
	a.pulse1.clockSweep()
	a.pulse2.clockSweep()
}

// fcTick advances the frame sequencer by one CPU cycle.
// stepped says whether this cycle is one of the sequencer's
// own cycles: the sequencer counts APU cycles, stepping once
// every two CPU cycles on the divider phase Tick selects.
//
// The counter runs 14915 APU cycles in 4-step mode (29830
// CPU cycles) and 18641 in 5-step mode. Quarter frame clocks
// fire when the counter reaches 3729, 7457, 11186, and the
// end of the sequence; half frame clocks at 7457 and the
// end. After the end value the counter wraps on the next
// step.
//
// The frame IRQ flag is a CPU-side signal with its own
// phase: it is set on the last three CPU cycles of the
// 4-step sequence, while the counter sits at 14914 on its
// non-step cycle and through 14915, so a read that clears
// it on one of the first two cycles sees it set again on
// the next (the triple set the timing tests measure). The
// first of the three lands 29830 CPU cycles after an
// aligned $4017 write, which together with the 3-or-4
// cycle reset delay is the jitter those tests pin down.
//
// While a $4017 reset countdown runs, the old sequence
// keeps stepping. When the countdown lands, the counter
// restarts at 0 in the new mode, and in 5-step mode the
// quarter and half clocks fire immediately, on that cycle.
func (a *APU) fcTick(stepped bool) {
	applied := false
	if a.fcPending >= 0 {
		a.fcPending--
		if a.fcPending == 0 {
			a.fcPending = -1
			a.fcCounter = 0
			a.fcMode = a.fcPendMd
			applied = true
			if a.fcMode == 1 {
				a.clockQuarter()
				a.clockHalf()
			}
		}
	}
	if stepped && !applied {
		a.fcCounter++
		// The step past the end value is the first step of
		// the next sequence (the counter goes ... 14914,
		// 14915, 1, 2 ...), so a sequence is exactly its
		// documented length in steps.
		if (a.fcMode == 0 && a.fcCounter > 14915) ||
			(a.fcMode == 1 && a.fcCounter > 18641) {
			a.fcCounter = 1
		}
		switch a.fcCounter {
		case 3729:
			a.clockQuarter()
		case 7457:
			a.clockQuarter()
			a.clockHalf()
		case 11186:
			a.clockQuarter()
		case 14915:
			if a.fcMode == 0 {
				a.clockQuarter()
				a.clockHalf()
			}
		case 18641:
			if a.fcMode == 1 {
				a.clockQuarter()
				a.clockHalf()
			}
		}
	}
	if a.fcMode == 0 && !a.fcInhibit {
		if a.fcCounter == 14915 || (a.fcCounter == 14914 && !stepped) {
			a.fcIRQ = true
		}
	}
}

// dmcTick advances the DMC one CPU cycle: the memory reader
// refills the buffer as soon as it empties while bytes
// remain, and the output timer runs at the rate table.
func (a *APU) dmcTick() {
	d := &a.dm
	if !d.bufferFull && d.bytesLeft > 0 {
		var b uint8
		if a.FetchDMC != nil {
			b = a.FetchDMC(d.currentAddr)
		}
		d.buffer = b
		d.bufferFull = true
		d.currentAddr++
		if d.currentAddr == 0 {
			d.currentAddr = 0x8000
		}
		d.bytesLeft--
		if d.bytesLeft == 0 {
			if d.loop {
				d.currentAddr = d.sampleAddr()
				d.bytesLeft = d.sampleLen()
			} else if d.irqEnabled {
				d.irqFlag = true
			}
		}
	}
	if d.timer > 0 {
		d.timer--
		return
	}
	// The rates are exact cycle counts between output clocks.
	d.timer = dmcRates[d.rateIdx] - 1
	if !d.silent {
		if d.shiftReg&1 != 0 {
			if d.outLevel <= 125 {
				d.outLevel += 2
			}
		} else if d.outLevel >= 2 {
			d.outLevel -= 2
		}
		d.shiftReg >>= 1
	}
	d.bitsLeft--
	if d.bitsLeft == 0 {
		d.bitsLeft = 8
		if d.bufferFull {
			d.shiftReg = d.buffer
			d.bufferFull = false
			d.silent = false
		} else {
			d.silent = true
		}
	}
}

// Mixer term tables: the per-channel terms of the DAC
// formulas below, precomputed for every level a channel can
// output, with exactly the divisions the direct formulas do.
var pulseMixTable [31]float64
var triMixTable [16]float64
var noiMixTable [16]float64
var dmcMixTable [128]float64

func init() {
	for p := 1; p < len(pulseMixTable); p++ {
		pulseMixTable[p] = 95.88 / (8128/float64(p) + 100)
	}
	for i := 0; i < 16; i++ {
		triMixTable[i] = float64(i) / 8227
		noiMixTable[i] = float64(i) / 12241
	}
	for i := 0; i < len(dmcMixTable); i++ {
		dmcMixTable[i] = float64(i) / 22638
	}
}

// mix computes the current mixed output level (roughly 0..1)
// from the channel values through the documented non-linear
// DAC formulas. The level is memoized on the channel levels
// (see the APU struct): recomputation happens only when one
// of them moved, and it is bit-identical to the direct
// formula, since the tables hold the same quotients and the
// surviving division runs on the same operands in the same
// order.
func (a *APU) mix() float64 {
	p := a.pulse1.out() + a.pulse2.out()
	t := a.tri.out()
	n := a.noi.out()
	d := a.dm.outLevel
	if a.mixValid && p == a.mixP && t == a.mixT && n == a.mixN && d == a.mixD {
		return a.mixLevel
	}
	a.mixP, a.mixT, a.mixN, a.mixD = p, t, n, d
	lvl := pulseMixTable[p]
	if s := triMixTable[t] + noiMixTable[n] + dmcMixTable[d]; s > 0 {
		lvl += 159.79 / (1/s + 100)
	}
	a.mixLevel = lvl
	a.mixValid = true
	return lvl
}

// Tick advances the APU by one CPU cycle: the frame
// sequencer, the channel timers (APU-rate units on the
// divider phase, CPU-rate units every cycle), the DMC, and
// the sample accumulator.
func (a *APU) Tick() { a.tickCycle() }

// tickCycle is one CPU cycle of APU work, shared by the eager
// Tick and the lazy CatchUp.
func (a *APU) tickCycle() {
	a.phase = !a.phase
	a.fcTick(a.phase)
	if a.phase {
		a.pulse1.tickTimer()
		a.pulse2.tickTimer()
		a.noi.tickTimer()
	}
	a.tri.tickTimer()
	a.dmcTick()

	a.mixSum += a.mix()
	a.mixCount++
	a.mixPhase += SampleRate
	if a.mixPhase >= cpuHz {
		a.mixPhase -= cpuHz
		avg := a.mixSum / float64(a.mixCount)
		s := int16(avg * 30000)
		a.out = append(a.out, s, s)
		a.mixSum = 0
		a.mixCount = 0
	}
}

// AudioBuffer drains the samples produced since the last
// call, interleaved stereo int16.
func (a *APU) AudioBuffer() []int16 {
	out := a.out
	a.out = nil
	return out
}

// DebugChannelSample returns a channel's current pre-mix
// level: 0 pulse 1, 1 pulse 2, 2 triangle, 3 noise, 4 DMC.
// Test and debugging hook, mirroring the GB core's.
func (a *APU) DebugChannelSample(ch int) float64 {
	switch ch {
	case 0:
		return float64(a.pulse1.out())
	case 1:
		return float64(a.pulse2.out())
	case 2:
		return float64(a.tri.out())
	case 3:
		return float64(a.noi.out())
	case 4:
		return float64(a.dm.outLevel)
	}
	return 0
}

// DebugLength returns a channel's length counter (channels
// as in DebugChannelSample; the DMC reports bytes left).
func (a *APU) DebugLength(ch int) int {
	switch ch {
	case 0:
		return a.pulse1.length.value
	case 1:
		return a.pulse2.length.value
	case 2:
		return a.tri.length.value
	case 3:
		return a.noi.length.value
	case 4:
		return a.dm.bytesLeft
	}
	return 0
}

// DebugEnabled reports whether a channel's length counter is
// enabled through $4015.
func (a *APU) DebugEnabled(ch int) bool {
	switch ch {
	case 0:
		return a.pulse1.length.enabled
	case 1:
		return a.pulse2.length.enabled
	case 2:
		return a.tri.length.enabled
	case 3:
		return a.noi.length.enabled
	}
	return false
}

// DebugFrameCounter reports the frame sequencer's counter,
// mode, pending reset countdown, and divider phase. Test and
// debugging hook.
func (a *APU) DebugFrameCounter() (counter, mode, pending int, phase bool) {
	return a.fcCounter, a.fcMode, a.fcPending, a.phase
}

// ErrStateSize reports a save state block of the wrong size.
var ErrStateSize = errors.New("sound: bad APU state size")

// cycleMark is one recorded bus sync point: the cycle it
// names and (once the advance has run that cycle) the IRQ
// level as it stood there.
type cycleMark struct {
	seq    int
	cycle  int64
	irq    bool
	filled bool
}

// Advance records n more owed cycles without running them.
func (a *APU) Advance(n int) {
	if n > 0 {
		a.pending += n
	}
}

// CatchUp runs every owed cycle.
func (a *APU) CatchUp() { a.runTo(a.executed + int64(a.pending)) }

// CatchUpTo runs cycles up to the given absolute count (a
// no-op when the advance is already past it).
func (a *APU) CatchUpTo(c int64) {
	if c > a.executed {
		a.runTo(c)
	}
}

// Target returns the absolute cycle count the advance will
// reach when every owed cycle has run.
func (a *APU) Target() int64 { return a.executed + int64(a.pending) }

// runTo executes cycles until executed reaches c, filling
// in the marks their cycles carry. It advances in spans:
// quietCycles computes how many cycles can pass before
// anything the CPU or the mixer can observe changes (a
// timer expiry, a frame sequencer threshold or reset
// landing, a DMC fetch), those cycles fold in closed form,
// and the event cycle itself runs the legacy per-cycle
// path, so sweeps, envelope and length clocks, DMC fetch
// stalls, and the frame IRQ's exact set cycle all keep
// their original code and timing.
func (a *APU) runTo(c int64) {
	for a.executed < c {
		room := c - a.executed
		if q := a.quietCycles(room); q > 0 {
			a.advanceQuiet(q)
		} else {
			a.tickCycle()
			a.executed++
			a.pending--
		}
		a.fillMarks()
	}
}

// fillMarks stamps every recorded mark whose cycle has run
// with the IRQ level as it stood there. Within a quiet span
// the level cannot change (both of its sources are event
// driven), so a mark inside one takes the current level,
// which is the level the whole span carries.
func (a *APU) fillMarks() {
	for a.markFill >= 0 {
		m := &a.marks[a.markFill&31]
		if m.seq != a.markFill {
			return
		}
		if !m.filled {
			if m.cycle > a.executed {
				return
			}
			m.irq = a.fcIRQ || a.dm.irqFlag
			m.filled = true
		}
		a.markFill++
		if a.markFill > a.markLast {
			a.markFill = -1
		}
	}
}

// untilStepped returns the number of CPU cycles from now
// until the j-th future APU-rate (stepped) cycle, j >= 1.
// Stepped cycles are the ones the divider phase flips into:
// with the phase currently set, the next cycle clears it and
// the one after steps; with it clear, the next cycle steps.
func (a *APU) untilStepped(j int) int {
	if a.phase {
		return 2 * j
	}
	return 2*j - 1
}

// quietCycles returns how many of the next room cycles are
// quiet: cycles in which no timer expires, the frame
// sequencer crosses no threshold, lands no reset, and
// enters no IRQ set window, and the DMC needs no fetch. The
// cycle that would do any of those is the event cycle and
// is not counted.
func (a *APU) quietCycles(room int64) int {
	q := int(room)
	bound := func(cycles int) {
		if cycles-1 < q {
			q = cycles - 1
		}
	}
	// Channel timer expiries. The pulse and noise timers
	// count APU-rate cycles: the expiry lands on the
	// (timer+1)-th stepped cycle. An expiry is an event
	// only when the channel can sound: a channel gated off
	// (no length, muted, envelope at zero) evolves in
	// closed form in advanceQuiet, duty steps and LFSR
	// shifts included, because none of it reaches the
	// mixer. The triangle timer counts CPU cycles, and its
	// step only matters while its gate is open (a closed
	// gate folds the timer without stepping).
	if !pulseFoldable(&a.pulse1) {
		bound(a.untilStepped(a.pulse1.timer + 1))
	}
	if !pulseFoldable(&a.pulse2) {
		bound(a.untilStepped(a.pulse2.timer + 1))
	}
	if !noiseFoldable(&a.noi) {
		bound(a.untilStepped(a.noi.timer + 1))
	}
	if a.tri.length.value > 0 && a.tri.linear > 0 {
		bound(a.tri.timer + 1)
	}
	// DMC: a fetch is due the next cycle the reader is
	// hungry. The output timer expiry is an event while
	// the reader can still change the output level; the
	// silent idle state (nothing left to play, buffer
	// empty, output parked) only cycles its timer and bit
	// counter, which advanceQuiet folds.
	if !a.dm.bufferFull && a.dm.bytesLeft > 0 {
		return 0
	}
	if !(a.dm.bytesLeft == 0 && !a.dm.bufferFull && a.dm.silent) {
		bound(a.dm.timer + 1)
	}
	// Frame sequencer: a pending $4017 reset lands
	// fcPending cycles out.
	if a.fcPending >= 0 {
		bound(a.fcPending)
	}
	// Frame sequencer thresholds: the stepped cycle that
	// brings fcCounter to a clock point, the sequence end
	// (which clocks), or one past it (which wraps).
	end := 14915
	if a.fcMode == 1 {
		end = 18641
	}
	next := end + 1
	for _, t := range []int{3729, 7457, 11186, 14914, end} {
		if t > a.fcCounter && t < next {
			next = t
		}
	}
	if a.fcMode == 1 && next == 14914 {
		// 14914 is only an IRQ-window point in 4-step mode;
		// in 5-step mode nothing happens there.
		next = end
	}
	bound(a.untilStepped(next - a.fcCounter))
	// The frame IRQ set window, 4-step mode, not inhibited:
	// the sequencer's set check also fires on the
	// non-stepped cycles that sit at counter 14914 (one
	// cycle after the stepped arrival there, a threshold
	// above) and at counter 14915. In an untriggered span
	// those cycles only re-set a set flag, but after a
	// $4015 read clears the flag mid-window they are the
	// cycles that set it again, so they are events.
	if a.fcMode == 0 && !a.fcInhibit && a.phase &&
		(a.fcCounter == 14914 || a.fcCounter == 14915) {
		return 0
	}
	if q < 0 {
		q = 0
	}
	return q
}

// advanceQuiet folds k quiet cycles into the state in closed
// form. quietCycles guarantees no timer expires, no frame
// threshold or reset lands, no IRQ window opens, and no DMC
// fetch comes due inside the span, so every evolving value
// is a countdown, an accumulation, or a phase flip.
func (a *APU) advanceQuiet(k int) {
	stepped := k / 2
	if !a.phase && k%2 == 1 {
		stepped++
	}
	if k%2 == 1 {
		a.phase = !a.phase
	}
	// Frame sequencer: the counter advances by the stepped
	// count (no threshold crossed), a pending reset counts
	// down without landing.
	a.fcCounter += stepped
	if a.fcPending >= 0 {
		a.fcPending -= k
	}
	// Channel timers: a channel whose expiry is an event
	// was bounded above, so its countdown cannot reach
	// zero here; the gated-off channels fold through
	// their expiries in closed form.
	if pulseFoldable(&a.pulse1) {
		a.pulse1.timer, a.pulse1.step = foldPulse(&a.pulse1, stepped)
	} else {
		a.pulse1.timer -= stepped
	}
	if pulseFoldable(&a.pulse2) {
		a.pulse2.timer, a.pulse2.step = foldPulse(&a.pulse2, stepped)
	} else {
		a.pulse2.timer -= stepped
	}
	if noiseFoldable(&a.noi) {
		a.noi.timer = foldNoise(&a.noi, stepped)
	} else {
		a.noi.timer -= stepped
	}
	if a.tri.length.value > 0 && a.tri.linear > 0 {
		// Gate open: expiries are events, so the span
		// cannot reach one.
		a.tri.timer -= k
	} else {
		// Gate closed: the timer still counts and
		// reloads; only the step is parked.
		a.tri.timer, _ = countdown(a.tri.timer, a.tri.period, k)
	}
	if a.dm.bytesLeft == 0 && !a.dm.bufferFull && a.dm.silent {
		a.dm.timer = foldDMCIdle(&a.dm, k)
	} else {
		a.dm.timer -= k
	}
	// The mixer accumulates the current (memoized) level
	// for the whole span, split at the sample emission
	// points so the produced samples are the ones the
	// per-cycle accumulation would have made.
	m := a.mix()
	rem := k
	for rem > 0 {
		need := (cpuHz - a.mixPhase + SampleRate - 1) / SampleRate
		if need < 1 {
			need = 1
		}
		if need > rem {
			need = rem
		}
		a.mixSum += m * float64(need)
		a.mixCount += need
		a.mixPhase += need * SampleRate
		rem -= need
		if a.mixPhase >= cpuHz {
			a.mixPhase -= cpuHz
			avg := a.mixSum / float64(a.mixCount)
			s := int16(avg * 30000)
			a.out = append(a.out, s, s)
			a.mixSum = 0
			a.mixCount = 0
		}
	}
	a.executed += int64(k)
	a.pending -= k
}

// pulseFoldable reports whether a pulse channel's timer
// expiries cannot reach the mixer: no length counter, the
// mute conditions (period under 8, sweep target overflow),
// or an envelope sitting at zero. Everything about the gate
// is static across a quiet span (length and envelope move
// only on frame clocks, which are events; the mute inputs
// change only on register writes, which break spans).
func pulseFoldable(p *pulse) bool {
	return p.length.value == 0 || p.muted() || p.env.out() == 0
}

// noiseFoldable is the noise channel's version: no length
// counter or an envelope at zero. (The LFSR bit itself is
// not a gate; it moves with every expiry.)
func noiseFoldable(n *noise) bool {
	return n.length.value == 0 || n.env.out() == 0
}

// countdown folds a countdown-with-reload timer over ticks
// ticks: it returns the timer value after the span and the
// number of expiries (reloads) inside it. The cycle length
// is period+1 ticks: the timer decrements to zero over
// timer ticks and expires on the next one.
func countdown(timer, period, ticks int) (int, int) {
	if ticks <= timer {
		return timer - ticks, 0
	}
	rem := ticks - timer - 1
	e := 1 + rem/(period+1)
	return period - rem%(period+1), e
}

// foldPulse folds a pulse timer over stepped APU-rate ticks,
// advancing the duty step once per expiry.
func foldPulse(p *pulse, stepped int) (timer, step int) {
	t, e := countdown(p.timer, p.period, stepped)
	return t, (p.step + e) & 7
}

// foldNoise folds the noise timer over stepped APU-rate
// ticks, shifting the LFSR once per expiry exactly as the
// per-cycle path does.
func foldNoise(n *noise, stepped int) int {
	t, e := countdown(n.timer, noisePeriods[n.period]-1, stepped)
	for ; e > 0; e-- {
		var tap uint16
		if n.mode {
			tap = (n.lfsr >> 6) & 1
		} else {
			tap = (n.lfsr >> 1) & 1
		}
		fb := (n.lfsr & 1) ^ tap
		n.lfsr = (n.lfsr >> 1) | (fb << 14)
	}
	return t
}

// foldDMCIdle folds the DMC output timer and bit counter
// over k CPU cycles in the silent idle state: expiries
// reload the timer from the rate table and count the bit
// counter down, wrapping it to 8 (the transfer attempt
// finds the buffer empty and the reader stays silent, so
// nothing audible evolves).
func foldDMCIdle(d *dmc, k int) int {
	t, e := countdown(d.timer, dmcRates[d.rateIdx]-1, k)
	bl := (d.bitsLeft - 1 - e) % 8
	if bl < 0 {
		bl += 8
	}
	d.bitsLeft = bl + 1
	return t
}

// ProjectIRQ returns the IRQ level (frame flag OR DMC flag)
// as it will stand at the given absolute cycle, computed
// from the current rendered state without running cycles,
// plus whether the projection is valid. The frame flag's
// evolution between renders is pure sequencer arithmetic:
// the counter advances on APU-rate cycles with one wrap,
// and the flag can only be set inside the 4-step set window
// (or stay as it is); the flag clears only on register
// accesses, which render first, so no clear can hide inside
// a projection interval. The projection bails (ok false)
// when a $4017 reset lands inside the interval (it rewrites
// the counter path) or while the DMC reader is active (its
// fetches stall the CPU and end samples on their own
// schedule; those stretches render instead, and they are
// short and rare).
func (a *APU) ProjectIRQ(cycle int64) (level bool, ok bool) {
	if cycle < a.executed {
		return false, false
	}
	if a.dm.bytesLeft > 0 || a.dm.bufferFull {
		return false, false
	}
	d := int(cycle - a.executed)
	if a.fcPending >= 0 && a.fcPending <= d {
		return false, false
	}
	level = a.fcIRQ || a.dm.irqFlag
	if level || a.fcMode != 0 || a.fcInhibit {
		return level, true
	}
	// Distance, in cycles from the last rendered cycle, to
	// the first cycle of the set window: the stepped cycle
	// that brings the counter to 14915, or a non-stepped
	// cycle sitting at 14914 or 14915 when the counter is
	// already there.
	c0 := a.fcCounter
	var dist int
	switch {
	case c0 < 14914:
		dist = a.untilStepped(14914 - c0)
		// The arrival at 14914 is stepped; if it is not
		// itself the window (it is not: the set needs
		// 14915 stepped, or a non-stepped cycle at
		// 14914/14915 right after), the window opens on
		// the cycle right after the arrival.
		dist++
	case c0 == 14914:
		dist = 1
	default: // c0 == 14915
		if a.phase {
			dist = 1
		} else {
			// The next cycle is the stepped wrap (the
			// counter restarts at 1); the window comes
			// back one sequence later, on the cycle
			// after the next arrival at 14914.
			dist = a.untilStepped(14914) + 1
		}
	}
	if d >= dist {
		level = true
	}
	return level, true
}

// Mark records a bus sync point at the current target cycle,
// under the machine's per-access sequence number. Marks must
// be recorded in sequence order.
func (a *APU) Mark(seq int) {
	m := &a.marks[seq&31]
	m.seq = seq
	m.cycle = a.Target()
	m.irq = false
	m.filled = false
	if m.cycle <= a.executed {
		// Same record-time snapshot as the PPU's marks:
		// the access caught the unit up inside its own
		// dispatch, so the level as it stands is the
		// level at the cycle (a $4015 read's frame IRQ
		// clear included, as the eager sync read it).
		m.irq = a.fcIRQ || a.dm.irqFlag
		m.filled = true
	}
	if a.markFill < 0 {
		a.markFill = seq
	}
	a.markLast = seq
}

// MarkIRQ returns the IRQ level as it stood at the sync
// point recorded under seq, running cycles up to that point
// first if the advance has not reached it. ok is false when
// the mark is no longer on record.
func (a *APU) MarkIRQ(seq int) (level bool, ok bool) {
	m := &a.marks[seq&31]
	if m.seq != seq {
		return false, false
	}
	if !m.filled {
		a.CatchUpTo(m.cycle)
	}
	if !m.filled {
		return false, false
	}
	return m.irq, true
}

// resetLazy clears the advance bookkeeping after a state
// load: the restored cycle state is the new epoch, fully
// caught up by definition.
func (a *APU) resetLazy() {
	a.pending = 0
	a.executed = 0
	a.markFill = -1
	a.markLast = 0
	for i := range a.marks {
		a.marks[i].seq = -1
		a.marks[i].filled = false
	}
}
