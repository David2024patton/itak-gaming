package scsp

import (
	"encoding/binary"
	"math"
)

// Envelope phases.
const (
	egIdle = iota
	egAttack
	egDecay1
	egDecay2
	egRelease
	egEnded
)

// egMaxQ is the maximum attenuation level (10 bit 3FFH) in Q8.
const egMaxQ = 0x3FF << 8

// slotState is one slot's runtime state. The register fields are
// read from the shared page on every tick, so only true runtime
// state lives here.
type slotState struct {
	active  bool
	phase   int64 // waveform position from SA, samples in Q16
	dir     int64 // 1 forward, -1 reverse (loop modes 2 and 3)
	egPhase int
	egLevel int32 // attenuation in Q8: 0 full volume, egMaxQ silent
	egHold  int32 // EGHOLD remaining attack hold ticks

	lfoPhase uint32 // Q32 phase
	lfsr     uint32

	prevOut int32 // previous sample output, the FM stack model
	out     int32 // current sample output before the mixers
}

// slotRegs is the decoded view of one slot's 12 register words.
type slotRegs struct {
	kyonb  bool
	sbctl  int
	ssctl  int
	lpctl  int
	pcm8b  bool
	sa     int
	lsa    int
	lea    int
	d2r    int
	d1r    int
	eghold bool
	ar     int
	lpslnk bool
	krs    int
	dl     int
	rr     int
	stwinh bool
	sdir   bool
	tl     int
	mdl    int
	mdxsl  int
	mdysl  int
	oct    int // signed octave, -8 to 7
	fns    int
	lfore  bool
	lfof   int
	plfows int
	plfos  int
	alfows int
	alfos  int
	isel   int
	imxl   int
	disdl  int
	dipan  int
	efsdl  int
	efpan  int
}

func (s *SCSP) decodeSlot(i int) slotRegs {
	var r slotRegs
	w0 := s.slotReg(i, SlotCtrl)
	r.kyonb = w0&0x4000 != 0
	r.sbctl = int(w0>>12) & 3
	r.ssctl = int(w0>>10) & 3
	r.lpctl = int(w0>>8) & 3
	r.pcm8b = w0&0x0080 != 0
	r.sa = int(w0&0x000F)<<16 | int(s.slotReg(i, SlotSA))
	r.lsa = int(s.slotReg(i, SlotLSA))
	r.lea = int(s.slotReg(i, SlotLEA))

	w := s.slotReg(i, SlotEG1)
	r.d2r = int(w>>11) & 0x1F
	r.d1r = int(w>>6) & 0x1F
	r.eghold = w&0x0020 != 0
	r.ar = int(w) & 0x1F

	w = s.slotReg(i, SlotEG2)
	r.lpslnk = w&0x8000 != 0
	r.krs = int(w>>11) & 0x0F
	r.dl = int(w>>6) & 0x1F
	r.rr = int(w) & 0x1F

	w = s.slotReg(i, SlotVol)
	r.stwinh = w&0x8000 != 0
	r.sdir = w&0x4000 != 0
	r.tl = int(w) & 0xFF

	w = s.slotReg(i, SlotFM)
	r.mdl = int(w>>12) & 0x0F
	r.mdxsl = int(w>>6) & 0x3F
	r.mdysl = int(w) & 0x3F

	w = s.slotReg(i, SlotPitch)
	oct := int(w>>11) & 0x0F
	if oct >= 8 {
		oct -= 16
	}
	r.oct = oct
	r.fns = int(w) & 0x3FF

	w = s.slotReg(i, SlotLFO)
	r.lfore = w&0x8000 != 0
	r.lfof = int(w>>10) & 0x1F
	r.plfows = int(w>>8) & 3
	r.plfos = int(w>>5) & 7
	r.alfows = int(w>>3) & 3
	r.alfos = int(w) & 7

	w = s.slotReg(i, SlotInSel)
	r.isel = int(w>>4) & 0x0F
	r.imxl = int(w) & 7

	w = s.slotReg(i, SlotOutSel)
	r.disdl = int(w>>13) & 7
	r.dipan = int(w>>8) & 0x1F
	r.efsdl = int(w>>5) & 7
	r.efpan = int(w) & 0x1F
	return r
}

func (s *SCSP) keyOn(i int) {
	st := &s.slots[i]
	st.active = true
	st.phase = 0
	st.dir = 1
	st.egLevel = egMaxQ
	st.egPhase = egAttack
	st.egHold = 0
	if r := s.decodeSlot(i); r.eghold {
		step := egStepQ8(s.effRate(&r, r.ar))
		if step > 0 {
			st.egHold = int32((egMaxQ + step - 1) / step)
			st.egLevel = 0
		}
	}
	st.lfoPhase = 0
}

func (s *SCSP) keyOff(i int) {
	st := &s.slots[i]
	if st.active && st.egPhase != egEnded {
		st.egPhase = egRelease
	}
}

// effRate applies key rate scaling to a 5 bit EG rate field. KRS
// 15 turns scaling off (the manual rule); otherwise the effective
// rate is twice the rate plus twice KRS plus the signed octave
// plus FNS bit 9, the formula documented from hardware by other
// SCSP implementations.
func (s *SCSP) effRate(r *slotRegs, rate int) int {
	if r.krs >= 15 {
		return 2 * rate
	}
	e := 2*rate + 2*r.krs + r.oct + (r.fns >> 9 & 1)
	if e < 0 {
		return 0
	}
	if e > 63 {
		return 63
	}
	return e
}

// egStepQ8 converts an effective rate (0 to 63) to a per sample
// EG level step in Q8. Rate 0 does not move. The step doubles
// every four effective steps; effective 60 and up move the full
// range in one sample. With KRS off (effective rate twice the
// field), AR 20 therefore takes exactly 128 samples from maximum
// attenuation to full volume, and AR 31 is instant.
func egStepQ8(eff int) int32 {
	if eff <= 0 {
		return 0
	}
	if eff >= 60 {
		return egMaxQ
	}
	return int32(1) << uint((eff+7)/4)
}

// pitchStepQ16 is the per sample phase step in Q16 samples, from
// the manual pitch relations: the step is (1024 plus FNS) over
// 1024, times 2 to the signed octave. OCT 0 and FNS 0 give exactly
// one sample per tick.
func pitchStepQ16(r *slotRegs, plfoFactor float64) int64 {
	base := int64(1024+r.fns) << 16 >> 10
	if r.oct >= 0 {
		base <<= uint(r.oct)
	} else {
		base >>= uint(-r.oct)
	}
	if plfoFactor != 1 {
		base = int64(float64(base) * plfoFactor)
	}
	return base
}

// mdlShift is the manual MDL maximum address shift table, in
// samples: MDL 0 to 4 give no shift, then 32 doubling to 32768.
var mdlShift = [16]int64{0, 0, 0, 0, 0, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768}

// sampleAt reads one waveform sample at a sample index relative
// to SA, with no interpolation.
func (s *SCSP) sampleAt(r *slotRegs, idx int64) int32 {
	switch r.ssctl {
	case 1, 2, 3:
		return 0 // noise is per output sample; 2 and 3 are silence
	}
	if r.pcm8b {
		off := r.sa + int(idx)
		if off < 0 || off >= len(s.ram) {
			return 0
		}
		return int32(int8(s.ram[off])) << 8
	}
	off := r.sa + int(idx)*2
	if off < 0 || off+1 >= len(s.ram) {
		return 0
	}
	return int32(int16(binary.BigEndian.Uint16(s.ram[off : off+2])))
}

// fetchSample returns the linearly interpolated sample at the
// slot's current phase, including the FM phase offset.
func (s *SCSP) fetchSample(i int, r *slotRegs) int32 {
	st := &s.slots[i]
	if r.ssctl == 1 {
		v := int32(int16(st.lfsr))
		if r.sbctl&1 != 0 {
			v = (v & -32768) | (^v & 0x7FFF)
		}
		if r.sbctl&2 != 0 {
			v ^= -32768
		}
		return v
	}
	pos := st.phase
	if shift := mdlShift[r.mdl]; shift != 0 {
		srcX := (i - (r.mdxsl & 0x1F) + 32) & 31
		srcY := (i - (r.mdysl & 0x1F) + 32) & 31
		avg := (s.slots[srcX].prevOut + s.slots[srcY].prevOut) / 2
		// avg is in sample units; avg/32768 * shift samples, Q16.
		pos += int64(avg) * shift * 2
	}
	idx := pos >> 16
	frac := int64(uint64(pos) & 0xFFFF)
	cur := s.sampleAt(r, idx)
	nxt := s.sampleAt(r, s.nextIndex(r, idx))
	v := cur + int32(int64(nxt-cur)*frac/65536)
	if r.sbctl&1 != 0 {
		v = (v & -32768) | (^v & 0x7FFF)
	}
	if r.sbctl&2 != 0 {
		v ^= -32768
	}
	return v
}

// nextIndex is the sample index after idx under the loop mode,
// used only for the interpolation neighbour.
func (s *SCSP) nextIndex(r *slotRegs, idx int64) int64 {
	if r.lpctl == 1 && idx >= int64(r.lea) {
		return int64(r.lsa)
	}
	return idx + 1
}

// advancePhase moves the phase by the pitch step and applies the
// loop mode at the LEA and LSA boundaries.
func (s *SCSP) advancePhase(i int, r *slotRegs, step int64) {
	st := &s.slots[i]
	st.phase += step * st.dir
	idx := st.phase >> 16
	lea := int64(r.lea)
	lsa := int64(r.lsa)
	switch r.lpctl {
	case 0: // loop off: stop at LEA
		if idx > lea {
			st.active = false
			st.egPhase = egEnded
			st.egLevel = egMaxQ
		}
	case 1: // forward loop: wrap to LSA at LEA
		if idx > lea {
			length := lea - lsa + 1
			if length > 0 {
				over := st.phase - (lea+1)<<16
				st.phase = (lsa << 16) + over%(length<<16)
			}
		}
	case 2: // reverse loop: play to LEA, then loop LEA down to LSA
		if st.dir > 0 && idx > lea {
			st.dir = -1
			st.phase = (lea << 16) - (st.phase - lea<<16)
		} else if st.dir < 0 && st.phase < lsa<<16 {
			st.phase = (lea << 16) - (lsa<<16 - st.phase)
		}
	case 3: // alternate loop: ping pong between LSA and LEA
		if st.dir > 0 && idx > lea {
			st.dir = -1
			st.phase = (lea << 16) - (st.phase - lea<<16)
		} else if st.dir < 0 && st.phase < lsa<<16 {
			st.dir = 1
			st.phase = (lsa << 16) + (lsa<<16 - st.phase)
		}
	}
}

// egGain converts the 10 bit attenuation level to a linear gain:
// the full range is 96 dB.
func egGain(levelQ8 int32) float64 {
	return dbGain(-96 * float64(levelQ8) / float64(egMaxQ))
}

// tlGain converts the TL bit weights from the manual table (bit 7
// is minus 48 dB down to bit 0 at minus 0.4 dB) to a linear gain.
func tlGain(tl int) float64 {
	weights := [8]float64{0.4, 0.8, 1.5, 3, 6, 12, 24, 48}
	db := 0.0
	for b := 0; b < 8; b++ {
		if tl&(1<<uint(b)) != 0 {
			db -= weights[b]
		}
	}
	return dbGain(db)
}

// tickEG advances one slot's envelope by one sample.
func (s *SCSP) tickEG(i int, r *slotRegs) {
	st := &s.slots[i]
	switch st.egPhase {
	case egAttack:
		if st.egHold > 0 {
			st.egHold--
			if st.egHold == 0 {
				st.egPhase = egDecay1
			}
			return
		}
		st.egLevel -= egStepQ8(s.effRate(r, r.ar))
		if st.egLevel <= 0 {
			st.egLevel = 0
			if r.lpslnk && (st.phase>>16) < int64(r.lsa) {
				return // hold at full until the loop start point
			}
			st.egPhase = egDecay1
		}
	case egDecay1:
		target := int32(r.dl) << 13 // DL in level units, Q8
		st.egLevel += egStepQ8(s.effRate(r, r.d1r))
		if st.egLevel >= target {
			st.egLevel = target
			st.egPhase = egDecay2
		}
		if st.egLevel >= egMaxQ {
			st.egLevel = egMaxQ
			st.egPhase = egEnded
			st.active = false
		}
	case egDecay2:
		if r.d2r == 0 {
			return // manual rule: D2R 0 maintains the level
		}
		st.egLevel += egStepQ8(s.effRate(r, r.d2r))
		if st.egLevel >= egMaxQ {
			st.egLevel = egMaxQ
			st.egPhase = egEnded
			st.active = false
		}
	case egRelease:
		st.egLevel += egStepQ8(s.effRate(r, r.rr))
		if st.egLevel >= egMaxQ {
			st.egLevel = egMaxQ
			st.egPhase = egEnded
			st.active = false
		}
	}
}

// LFO frequency table from the manual, in hundredths of a hertz.
var lfoFreqX100 = [32]int{
	17, 19, 23, 27, 34, 39, 45, 55, 68, 78, 92, 110, 139, 160,
	187, 227, 287, 331, 392, 479, 615, 718, 860, 1080, 1440,
	1720, 2150, 2870, 4310, 5740, 8610, 17230,
}

// AM depth table (maximum displacement in dB) and PM depth table
// (maximum displacement in cents) from the manual.
var (
	alfoDb    = [8]float64{0, 0.4, 0.8, 1.5, 3, 6, 12, 24}
	plfoCents = [8]float64{0, 7, 13.5, 27, 55, 112, 230, 494}
)

// lfoWave returns a waveform value: signed in minus 128 to 127
// for pitch use, or 0 to 255 for amplitude use when signed is
// false. Waveforms are 0 sawtooth, 1 square, 2 triangle, 3 noise.
func (st *slotState) lfoWave(sel int, signed bool) int {
	p := st.lfoPhase
	var v int
	switch sel {
	case 0: // sawtooth
		v = int(p >> 24)
	case 1: // square
		if p&(1<<31) != 0 {
			v = 255
		}
	case 2: // triangle
		if p&(1<<31) != 0 {
			v = 255 - int(p>>23&0xFF)
		} else {
			v = int(p >> 23 & 0xFF)
		}
	default: // noise
		v = int(st.lfsr>>8) & 0xFF
	}
	if signed {
		return v - 128
	}
	return v
}

// renderSlots renders every active slot for this tick, accumulates
// the direct mix into mixL and mixR, and returns the 16 DSP input
// mix values (MIXS) built from ISEL and IMXL.
func (s *SCSP) renderSlots(mixL, mixR *int64) [16]int32 {
	var mixs [16]int32
	for i := 0; i < 32; i++ {
		st := &s.slots[i]
		r := s.decodeSlot(i)

		if r.lfore {
			st.lfoPhase = 0
		} else {
			step := uint64(lfoFreqX100[r.lfof]) << 32 / (sampleRate * 100)
			st.lfoPhase += uint32(step)
		}
		st.lfsr ^= st.lfsr << 13
		st.lfsr ^= st.lfsr >> 17
		st.lfsr ^= st.lfsr << 5

		if !st.active {
			st.prevOut = 0
			st.out = 0
			continue
		}

		plfo := 1.0
		if r.plfos != 0 {
			cents := float64(st.lfoWave(r.plfows, true)) / 128 * plfoCents[r.plfos]
			plfo = math.Pow(2, cents/1200)
		}
		raw := s.fetchSample(i, &r)
		s.tickEG(i, &r)

		var out int32
		if r.sdir {
			out = raw
		} else {
			g := egGain(st.egLevel) * tlGain(r.tl)
			if r.alfos != 0 {
				w := float64(st.lfoWave(r.alfows, false)) / 255
				g *= dbGain(-w * alfoDb[r.alfos])
			}
			out = int32(float64(raw) * g)
		}
		st.out = out

		if dg := sendGain(r.disdl); dg != 0 && out != 0 {
			gl, gr := panGains(r.dipan)
			*mixL += int64(float64(out) * dg * gl)
			*mixR += int64(float64(out) * dg * gr)
		}
		if ig := sendGain(r.imxl); ig != 0 && out != 0 {
			mixs[r.isel] += int32(float64(out) * ig)
		}
		if !r.stwinh && len(s.regs) >= RegStackB+64 {
			v := uint16(int16(out))
			binary.BigEndian.PutUint16(s.regs[RegStackA+i*2:RegStackA+i*2+2], v)
			binary.BigEndian.PutUint16(s.regs[RegStackB+i*2:RegStackB+i*2+2], v)
		}

		s.advancePhase(i, &r, pitchStepQ16(&r, plfo))
	}
	for i := range s.slots {
		s.slots[i].prevOut = s.slots[i].out
	}
	return mixs
}
