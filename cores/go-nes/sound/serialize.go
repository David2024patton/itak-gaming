package sound

import (
	"encoding/binary"
	"math"
)

// SerializeSize is the fixed size of the serialized APU
// state: a version byte and a hand-laid-out image of every
// register, counter, and latch that can influence future
// output. Like the other cores' state blocks, it is not a
// file format: it moves with the code.
const SerializeSize = 200

const serializeVersion = 1

// Serialize returns the APU state as a fixed-size block.
func (a *APU) Serialize() []byte {
	buf := make([]byte, SerializeSize)
	buf[0] = serializeVersion
	o := 1
	put8 := func(v uint8) { buf[o] = v; o++ }
	putB := func(b bool) {
		if b {
			put8(1)
		} else {
			put8(0)
		}
	}
	put16 := func(v uint16) { binary.LittleEndian.PutUint16(buf[o:], v); o += 2 }
	putI := func(v int) { put16(uint16(v)) }
	putEnv := func(e *envelope) {
		putB(e.startFlag)
		putI(e.divider)
		putI(e.decay)
		putI(e.period)
		putB(e.constVol)
		putB(e.loop)
	}
	putLen := func(l *lengthCounter) {
		putI(l.value)
		putB(l.halt)
		putB(l.enabled)
	}
	putPulse := func(p *pulse) {
		putI(p.duty)
		putI(p.step)
		putI(p.period)
		putI(p.timer)
		putEnv(&p.env)
		putLen(&p.length)
		putB(p.swEnable)
		putI(p.swPeriod)
		putB(p.swNegate)
		putI(p.swShift)
		putI(p.swDiv)
		putB(p.swReload)
	}
	putPulse(&a.pulse1)
	putPulse(&a.pulse2)

	putI(a.tri.period)
	putI(a.tri.timer)
	putI(a.tri.step)
	putLen(&a.tri.length)
	putI(a.tri.linear)
	putI(a.tri.linLoad)
	putB(a.tri.linReLd)
	putB(a.tri.linCtrl)

	putI(a.noi.period)
	putI(a.noi.timer)
	putB(a.noi.mode)
	put16(a.noi.lfsr)
	putEnv(&a.noi.env)
	putLen(&a.noi.length)

	d := &a.dm
	putB(d.irqEnabled)
	putB(d.irqFlag)
	putB(d.loop)
	putI(d.rateIdx)
	put8(d.addrReg)
	put8(d.lenReg)
	putI(d.outLevel)
	putI(d.timer)
	put8(d.shiftReg)
	putI(d.bitsLeft)
	put8(d.buffer)
	putB(d.bufferFull)
	putB(d.silent)
	put16(d.currentAddr)
	putI(d.bytesLeft)

	putB(a.phase)
	putI(a.fcCounter)
	putI(a.fcMode)
	putB(a.fcInhibit)
	putB(a.fcIRQ)
	// fcPending is signed with -1 for none: stored biased.
	putI(a.fcPending + 1)
	putI(a.fcPendMd)
	// The sample accumulator rides along so a restored APU
	// emits its next sample on the same cycle as the
	// original would have.
	binary.LittleEndian.PutUint64(buf[o:], math.Float64bits(a.mixSum))
	o += 8
	putI(a.mixCount)
	// mixPhase counts up to the CPU clock (about 1.79M), too
	// wide for the 16-bit fields above: stored as 32 bits.
	binary.LittleEndian.PutUint32(buf[o:], uint32(a.mixPhase))
	o += 4
	return buf
}

// Deserialize restores a block written by Serialize.
func (a *APU) Deserialize(buf []byte) error {
	if len(buf) != SerializeSize || buf[0] != serializeVersion {
		return ErrStateSize
	}
	defer a.resetLazy()
	fetch := a.FetchDMC
	o := 1
	get8 := func() uint8 { v := buf[o]; o++; return v }
	getB := func() bool { return get8() != 0 }
	getI := func() int { v := int(binary.LittleEndian.Uint16(buf[o:])); o += 2; return v }
	getEnv := func(e *envelope) {
		e.startFlag = getB()
		e.divider = getI()
		e.decay = getI()
		e.period = getI()
		e.constVol = getB()
		e.loop = getB()
	}
	getLen := func(l *lengthCounter) {
		l.value = getI()
		l.halt = getB()
		l.enabled = getB()
	}
	getPulse := func(p *pulse) {
		p.duty = getI()
		p.step = getI()
		p.period = getI()
		p.timer = getI()
		getEnv(&p.env)
		getLen(&p.length)
		p.swEnable = getB()
		p.swPeriod = getI()
		p.swNegate = getB()
		p.swShift = getI()
		p.swDiv = getI()
		p.swReload = getB()
	}
	getPulse(&a.pulse1)
	getPulse(&a.pulse2)
	a.pulse1.onesComp = true
	a.pulse2.onesComp = false

	a.tri.period = getI()
	a.tri.timer = getI()
	a.tri.step = getI()
	getLen(&a.tri.length)
	a.tri.linear = getI()
	a.tri.linLoad = getI()
	a.tri.linReLd = getB()
	a.tri.linCtrl = getB()

	a.noi.period = getI()
	a.noi.timer = getI()
	a.noi.mode = getB()
	a.noi.lfsr = binary.LittleEndian.Uint16(buf[o:])
	o += 2
	getEnv(&a.noi.env)
	getLen(&a.noi.length)

	d := &a.dm
	d.irqEnabled = getB()
	d.irqFlag = getB()
	d.loop = getB()
	d.rateIdx = getI()
	d.addrReg = get8()
	d.lenReg = get8()
	d.outLevel = getI()
	d.timer = getI()
	d.shiftReg = get8()
	d.bitsLeft = getI()
	d.buffer = get8()
	d.bufferFull = getB()
	d.silent = getB()
	d.currentAddr = binary.LittleEndian.Uint16(buf[o:])
	o += 2
	d.bytesLeft = getI()

	a.phase = getB()
	a.fcCounter = getI()
	a.fcMode = getI()
	a.fcInhibit = getB()
	a.fcIRQ = getB()
	a.fcPending = getI() - 1
	a.fcPendMd = getI()
	a.mixSum = math.Float64frombits(binary.LittleEndian.Uint64(buf[o:]))
	o += 8
	a.mixCount = getI()
	a.mixPhase = int(binary.LittleEndian.Uint32(buf[o:]))
	o += 4
	a.out = nil
	a.FetchDMC = fetch
	return nil
}
