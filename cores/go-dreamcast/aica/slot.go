package aica

import "math"

// EG states, in monitor order.
const (
	egAttack = iota
	egDecay1
	egDecay2
	egRelease
)

// slot holds the runtime state of one of the 64 sound slots. Register
// values themselves live in the shared register file and are read
// live every sample, so mid flight register changes take effect at
// once.
type slot struct {
	active  bool
	loopHit bool

	phase uint64 // 10 fractional bits, integer part is samples from SA

	egState  int
	egVolFP  int64 // 10 bit volume scaled by 65536
	fegState int
	fegFP    int64 // 13 bit level scaled by 65536
	filterY  float64

	adpcmPred int32
	adpcmStep int32
	adpcmInit bool

	seqCur  int16
	seqNext int16
	seqPos  int // sample position seqNext belongs to

	lfsr     uint32
	lfoPhase uint32
	lfoNoise uint8
}

func (a *AICA) slotReg(idx int, off uint32) uint16 {
	return a.getRaw16(uint32(idx)*0x80 + off)
}

func (s *slot) sequential(format uint16) bool {
	return format >= 2
}

func (a *AICA) octSigned(idx int) int {
	raw := (a.slotReg(idx, 0x18) >> 11) & 0xF
	return int(raw^8) - 8
}

func (a *AICA) keyOffset(idx int) int {
	krs := int((a.slotReg(idx, 0x14) >> 10) & 0xF)
	if krs == 0xF {
		return 0
	}
	fns := a.slotReg(idx, 0x18) & 0x3FF
	return 2*(krs+a.octSigned(idx)) + int((fns>>9)&1)
}

func (a *AICA) effRate(idx int, rateReg uint16) int {
	e := 2*int(rateReg) + a.keyOffset(idx)
	if e < 0 {
		e = 0
	}
	if e > 63 {
		e = 63
	}
	return e
}

// egIncrement returns the per sample volume step in fixed point.
func (a *AICA) egIncrement(idx int, rateReg uint16, attack bool) int64 {
	var table *[64]float64
	if attack {
		table = &attackTimesMs
	} else {
		table = &decayTimesMs
	}
	e := a.effRate(idx, rateReg)
	t := table[e]
	if t < 0 {
		return 0
	}
	if t == 0 {
		return 1024 * 65536
	}
	return int64(1023.0 * 65536 / (t * 44.1))
}

// fegIncrement returns the per sample filter level step in fixed
// point, scaled like egVolFP but over the 13 bit level range.
func (a *AICA) fegIncrement(idx int, rateReg uint16, attack bool) int64 {
	var table *[64]float64
	if attack {
		table = &attackTimesMs
	} else {
		table = &decayTimesMs
	}
	e := a.effRate(idx, rateReg)
	t := table[e]
	if t < 0 {
		return 0
	}
	if t == 0 {
		return (0x1FF8 - 0x0008) * 65536
	}
	return int64((0x1FF8 - 0x0008) / 1.0 * 65536 / (t * 44.1))
}

func moveToward(cur int64, target int64, inc int64) int64 {
	if cur < target {
		cur += inc
		if cur > target {
			cur = target
		}
	} else if cur > target {
		cur -= inc
		if cur < target {
			cur = target
		}
	}
	return cur
}

func (a *AICA) flv(idx int, n int) int64 {
	return int64(a.slotReg(idx, 0x2C+uint32(n)*4)&0x1FFF) << 16
}

// keyOn restarts a slot.
func (a *AICA) keyOn(idx int) {
	s := &a.slots[idx]
	r0 := a.slotReg(idx, 0x00)
	format := (r0 >> 7) & 3
	s.active = true
	s.loopHit = false
	s.phase = 0
	s.egState = egAttack
	s.egVolFP = 0
	s.fegState = egAttack
	s.fegFP = a.flv(idx, 0)
	s.filterY = 0
	if format != 3 || !s.adpcmInit {
		s.adpcmPred = 0
		s.adpcmStep = 127
		s.adpcmInit = true
	}
	s.lfsr = 0xACE1
	s.lfoPhase = 0
	a.seqInit(idx)
}

// keyOff puts a slot into release.
func (a *AICA) keyOff(idx int) {
	s := &a.slots[idx]
	if !s.active {
		return
	}
	s.egState = egRelease
	s.fegState = egRelease
	s.fegFP = a.flv(idx, 3)
}

// applyKeyOnEx services a KYONEX write: every slot keys on or off
// according to its own KYONB bit.
func (a *AICA) applyKeyOnEx() {
	for i := 0; i < 64; i += 1 {
		if a.slotReg(i, 0x00)&(1<<14) != 0 {
			a.keyOn(i)
		} else if a.slots[i].active {
			a.keyOff(i)
		}
	}
}

// Sample fetching

func (a *AICA) slotSA(idx int) uint32 {
	r0 := a.slotReg(idx, 0x00)
	sa := uint32(r0&0x7F)<<16 | uint32(a.slotReg(idx, 0x04))
	return sa
}

func (a *AICA) ramByte(addr uint32) byte {
	if len(a.ram) == 0 {
		return 0
	}
	return a.ram[a.ramIndex(addr)]
}

func xorshift32(x uint32) uint32 {
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	return x
}

// decodeADPCM advances the predictor by one nibble and returns the
// new sample. The integer rules here are the documented model: delta
// is step times (2m+1) over 8, negated by the sign bit; the step then
// scales by a per magnitude factor over 256 and clamps to the range
// 127 to 24576.
func (s *slot) decodeADPCM(nib uint32) int16 {
	m := int32(nib & 7)
	delta := (s.adpcmStep * (2*m + 1)) / 8
	if nib&8 != 0 {
		delta = -delta
	}
	p := s.adpcmPred + delta
	if p > 32767 {
		p = 32767
	}
	if p < -32768 {
		p = -32768
	}
	s.adpcmPred = p
	st := (s.adpcmStep * adpcmFactors[m]) / 256
	if st < 127 {
		st = 127
	}
	if st > 24576 {
		st = 24576
	}
	s.adpcmStep = st
	return int16(p)
}

func (a *AICA) adpcmNibble(idx int, pos int) uint32 {
	sa := a.slotSA(idx)
	b := a.ramByte(sa + uint32(pos)/2)
	if pos%2 == 0 {
		return uint32(b & 0xF)
	}
	return uint32(b >> 4)
}

func (a *AICA) noiseStep(s *slot) int16 {
	s.lfsr = xorshift32(s.lfsr)
	// Approximation: the exact noise algorithm is not in the
	// documentation. The top half of the LFSR is the sample.
	return int16(uint16(s.lfsr >> 16))
}

// seqSampleAt decodes the sequential sample at a fresh position. It
// is used at sequence init and at loop restarts, where the decode
// position is known exactly.
func (a *AICA) seqSampleAt(idx int, pos int) int16 {
	s := &a.slots[idx]
	r0 := a.slotReg(idx, 0x00)
	format := (r0 >> 7) & 3
	if (r0>>10)&1 != 0 {
		return a.noiseStep(s)
	}
	if format == 2 || format == 3 {
		return s.decodeADPCM(a.adpcmNibble(idx, pos))
	}
	return 0
}

// seqInit sets up the current and next sequential samples at key on.
func (a *AICA) seqInit(idx int) {
	s := &a.slots[idx]
	r0 := a.slotReg(idx, 0x00)
	if (r0>>10)&1 == 0 && !s.sequential((r0>>7)&3) {
		return
	}
	s.seqCur = a.seqSampleAt(idx, 0)
	s.seqNext = a.seqSampleAt(idx, 1)
	s.seqPos = 1
}

// seqAdvance moves the sequential pair forward by delta positions.
func (a *AICA) seqAdvance(idx int, delta int) {
	s := &a.slots[idx]
	for i := 0; i < delta; i += 1 {
		s.seqCur = s.seqNext
		s.seqPos += 1
		s.seqNext = a.seqSampleAt(idx, s.seqPos)
	}
}

// seqRestart restarts sequential decoding at a loop position.
func (a *AICA) seqRestart(idx int, pos int, reset bool) {
	s := &a.slots[idx]
	r0 := a.slotReg(idx, 0x00)
	format := (r0 >> 7) & 3
	if reset && format != 3 {
		s.adpcmPred = 0
		s.adpcmStep = 127
	}
	if (r0>>10)&1 != 0 {
		s.lfsr = 0xACE1
	}
	s.seqCur = a.seqSampleAt(idx, pos)
	s.seqNext = a.seqSampleAt(idx, pos+1)
	s.seqPos = pos + 1
}

// fetchPCM reads a memory sample at a sample position.
func (a *AICA) fetchPCM(idx int, pos int) int16 {
	sa := a.slotSA(idx)
	format := (a.slotReg(idx, 0x00) >> 7) & 3
	switch format {
	case 0:
		addr := sa + uint32(pos)*2
		lo := uint16(a.ramByte(addr))
		hi := uint16(a.ramByte(addr + 1))
		return int16(lo | hi<<8)
	case 1:
		return int16(int8(a.ramByte(sa+uint32(pos)))) << 8
	}
	return 0
}

// currentPair returns the interpolated source sample pair at the
// current phase.
func (a *AICA) currentPair(idx int) (int32, int32) {
	s := &a.slots[idx]
	r0 := a.slotReg(idx, 0x00)
	format := (r0 >> 7) & 3
	lea := int(a.slotReg(idx, 0x0C))
	lsa := int(a.slotReg(idx, 0x08))
	pos := int(s.phase >> 10)
	if (r0>>10)&1 != 0 || s.sequential(format) {
		cur := int32(s.seqCur)
		next := int32(s.seqNext)
		return cur, next
	}
	cur := int32(a.fetchPCM(idx, pos))
	var next int32
	switch {
	case pos+1 <= lea:
		next = int32(a.fetchPCM(idx, pos+1))
	case a.slotReg(idx, 0x00)&(1<<9) != 0:
		next = int32(a.fetchPCM(idx, lsa))
	default:
		next = int32(a.fetchPCM(idx, lea))
	}
	return cur, next
}

// LFO

func (a *AICA) lfoWave(idx int, ws uint16) uint32 {
	s := &a.slots[idx]
	switch ws & 3 {
	case 0:
		return s.lfoPhase >> 24
	case 1:
		if s.lfoPhase&(1<<31) != 0 {
			return 255
		}
		return 0
	case 2:
		if s.lfoPhase < (1 << 31) {
			return s.lfoPhase >> 23
		}
		return 255 - ((s.lfoPhase - (1 << 31)) >> 23)
	case 3:
		return uint32(s.lfoNoise)
	}
	return 0
}

func (a *AICA) lfoAdvance(idx int) {
	s := &a.slots[idx]
	r := a.slotReg(idx, 0x1C)
	if r&(1<<15) != 0 {
		s.lfoPhase = 0
		return
	}
	inc := uint32(uint64(lfoMilliHz[(r>>10)&0x1F]) * (1 << 32) / 44100000)
	old := s.lfoPhase
	s.lfoPhase = old + inc
	if s.lfoPhase < old {
		s.lfsr = xorshift32(s.lfsr)
		s.lfoNoise = uint8(s.lfsr >> 24)
	}
}

// Envelope updates

func (a *AICA) updateAEG(idx int) {
	s := &a.slots[idx]
	r := a.slotReg(idx, 0x10)
	r2 := a.slotReg(idx, 0x14)
	dl := int64((r2 >> 5) & 0x1F)
	switch s.egState {
	case egAttack:
		s.egVolFP += a.egIncrement(idx, (r>>0)&0x1F, true)
		if s.egVolFP >= 1023*65536 {
			s.egVolFP = 1023 * 65536
			s.egState = egDecay1
		}
	case egDecay1:
		s.egVolFP -= a.egIncrement(idx, (r>>6)&0x1F, false)
		if s.egVolFP < 0 {
			s.egVolFP = 0
		}
		if (s.egVolFP>>16)>>5 <= (31 - dl) {
			s.egState = egDecay2
		}
	case egDecay2:
		s.egVolFP -= a.egIncrement(idx, (r>>11)&0x1F, false)
		if s.egVolFP < 0 {
			s.egVolFP = 0
		}
	case egRelease:
		s.egVolFP -= a.egIncrement(idx, r2&0x1F, false)
		if s.egVolFP <= 0 {
			s.egVolFP = 0
			s.active = false
		}
	}
}

func (a *AICA) updateFEG(idx int) {
	s := &a.slots[idx]
	r40 := a.slotReg(idx, 0x40)
	r44 := a.slotReg(idx, 0x44)
	switch s.fegState {
	case egAttack:
		target := a.flv(idx, 1)
		s.fegFP = moveToward(s.fegFP, target, a.fegIncrement(idx, (r40>>8)&0x1F, true))
		if s.fegFP == target {
			s.fegState = egDecay1
		}
	case egDecay1:
		target := a.flv(idx, 2)
		s.fegFP = moveToward(s.fegFP, target, a.fegIncrement(idx, r40&0x1F, false))
		if s.fegFP == target {
			s.fegState = egDecay2
		}
	case egDecay2:
		// Holds FLV2 until key off.
	case egRelease:
		target := a.flv(idx, 4)
		s.fegFP = moveToward(s.fegFP, target, a.fegIncrement(idx, r44&0x1F, false))
	}
}

// slotPitchStep computes the phase step for this sample, including
// the pitch LFO.
func (a *AICA) slotPitchStep(idx int) uint32 {
	r := a.slotReg(idx, 0x18)
	fns := int(r & 0x3FF)
	base := 1024 + fns
	oct := a.octSigned(idx)
	step := base
	if oct >= 0 {
		step = base << uint(oct)
	} else {
		step = base >> uint(-oct)
	}
	lfoReg := a.slotReg(idx, 0x1C)
	plfos := (lfoReg >> 5) & 7
	if plfos != 0 {
		w := int(a.lfoWave(idx, (lfoReg>>8)&3))
		v := w - 128
		var cents float64
		if v >= 0 {
			cents = float64(v) * float64(plfoCentsPos[plfos]) / 127
		} else {
			cents = float64(v) * float64(plfoCentsNeg[plfos]) / 128
		}
		step = int(float64(step)*math.Pow(2, cents/1200) + 0.5)
	}
	if step < 0 {
		step = 0
	}
	return uint32(step)
}

// slotSample renders one sample of a slot. It returns the direct left
// and right contributions and the DSP send contribution for MIXS.
//
// The DSP send scaling is fixed here and is part of the unit gate:
// val is the slot value after all gains in 16 bit sample units, the
// send gain is Q15 with unity at 32768, and the contribution is
// ((val * gainQ15) >> 15) << 4, clamped to the 20 bit MIXS range, so
// MIXS stores true 20 bit values.
func (a *AICA) slotSample(idx int, mono bool) (float64, float64, int32) {
	s := &a.slots[idx]
	if !s.active {
		return 0, 0, 0
	}
	a.lfoAdvance(idx)
	a.updateAEG(idx)
	a.updateFEG(idx)
	if !s.active {
		return 0, 0, 0
	}

	cur, next := a.currentPair(idx)
	frac := int32(s.phase & 0x3FF)
	interp := cur + (next-cur)*frac/1024

	// Filter envelope: one pole lowpass with coefficient from the
	// current level, plus a resonance term from Q. The Q path is a
	// documented simplification: out = y + (x - y) * (g - 1) with
	// g = 10^((0.75*Q - 3)/20), so Q = 4 is a pure lowpass and the
	// maximum level makes the slot transparent.
	q := float64((a.slotReg(idx, 0x28) & 0x1F))
	k := float64(s.fegFP>>16) / 8184.0
	x := float64(interp)
	s.filterY += k * (x - s.filterY)
	g := math.Pow(10, (0.75*q-3)/20)
	filtered := s.filterY + (x-s.filterY)*(g-1)

	v10 := s.egVolFP >> 16
	if v10 < 0 {
		v10 = 0
	}
	if v10 > 1023 {
		v10 = 1023
	}
	gain := float64(aegGainQ16[v10]) / 65536
	tl := (a.slotReg(idx, 0x28) >> 8) & 0xFF
	gain *= dbToGain(tlAttenuationDB(tl))

	lfoReg := a.slotReg(idx, 0x1C)
	alfos := lfoReg & 7
	if alfos != 0 {
		w := a.lfoWave(idx, (lfoReg>>3)&3)
		db := float64(w) / 255 * alfoDepthDB[alfos]
		gain *= dbToGain(db)
	}

	valF := filtered * gain
	val := int32(valF)
	if valF >= 0 {
		val = int32(valF + 0.5)
	} else {
		val = int32(valF - 0.5)
	}

	var left, right float64
	r24 := a.slotReg(idx, 0x24)
	send := sendGain((r24 >> 8) & 0xF)
	if send != 0 {
		pan := r24 & 0x1F
		if mono {
			left = float64(val) * send
			right = float64(val) * send
		} else {
			left = float64(val) * send * panGain(pan, 0)
			right = float64(val) * send * panGain(pan, 1)
		}
	}

	var dspSend int32
	r20 := a.slotReg(idx, 0x20)
	imxl := (r20 >> 4) & 0xF
	if imxl != 0 {
		gq := sendGainQ15(imxl)
		dspSend = clampInt32(int32(((int64(val)*int64(gq))>>15)<<4), -524288, 524287)
	}

	a.advancePhase(idx)
	return left, right, dspSend
}

// advancePhase moves the phase by the pitch step and handles loop
// wrap and end of sample.
func (a *AICA) advancePhase(idx int) {
	s := &a.slots[idx]
	r0 := a.slotReg(idx, 0x00)
	format := (r0 >> 7) & 3
	lea := int(a.slotReg(idx, 0x0C))
	lsa := int(a.slotReg(idx, 0x08))
	oldPos := int(s.phase >> 10)
	step := a.slotPitchStep(idx)
	newPhase := s.phase + uint64(step)
	newPos := int(newPhase >> 10)
	if newPos > lea {
		if r0&(1<<9) != 0 {
			s.loopHit = true
			if a.slotReg(idx, 0x14)&(1<<14) != 0 && s.egState == egAttack {
				s.egState = egDecay1
			}
			wrapped := newPos
			for wrapped > lea {
				wrapped = lsa + (wrapped - lea - 1)
				if wrapped <= lea {
					break
				}
				if lsa > lea {
					break
				}
			}
			s.phase = uint64(wrapped)<<10 | (newPhase & 0x3FF)
			if (r0>>10)&1 != 0 || s.sequential(format) {
				a.seqRestart(idx, wrapped, true)
			}
			return
		}
		s.phase = uint64(lea) << 10
		s.egVolFP = 0
		s.egState = egRelease
		s.active = false
		return
	}
	s.phase = newPhase
	if newPos != oldPos && ((r0>>10)&1 != 0 || s.sequential(format)) {
		a.seqAdvance(idx, newPos-oldPos)
	}
}

// Monitor readbacks

func (a *AICA) mslc() int {
	return int((a.getRaw16(0x280C) >> 8) & 0x3F)
}

func (a *AICA) monitorValue() uint16 {
	idx := a.mslc()
	s := &a.slots[idx]
	afsel := a.getRaw16(0x280C)&(1<<14) != 0
	var out uint16
	if s.loopHit {
		out |= 1 << 15
	}
	s.loopHit = false
	state := s.egState
	egField := uint16(0)
	if afsel {
		state = s.fegState
	} else {
		v10 := s.egVolFP >> 16
		if v10 < 0 {
			v10 = 0
		}
		if v10 > 1023 {
			v10 = 1023
		}
		egField = uint16(1023-v10) << 3
	}
	out |= uint16(state&3) << 13
	out |= egField & 0x1FFF
	return out
}

func (a *AICA) caValue() uint16 {
	idx := a.mslc()
	return uint16(a.slots[idx].phase >> 10)
}
