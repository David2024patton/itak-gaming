package sound

import "math"

// YM2612 FM engine (milestone 2), written from Yamaha's published
// YM2612 documentation: the register map, the phase and envelope
// generator behavior, the eight algorithm connection diagrams, the
// SSG-EG pattern set and the timer formulas. No third-party emulator
// source was consulted; where the datasheet gives behavior as tables
// this implementation follows the documented shape and says so.
//
// Model summary:
//
//   - The chip runs at its native sample rate, master clock / 144
//     (about 53,267 Hz on the Genesis). Produce generates native-rate
//     stereo into a reusable buffer and linear-interpolates down to
//     the 44.1 kHz output rate.
//   - Operator phase is a 32-bit accumulator whose cycle length is
//     2^21 counts, so the documented increment (F-number << block)
//     at multiple 1 is exact and multiple 0 (the 0.5x case) keeps a
//     fractional bit. The sine table index is the top 10 bits.
//   - Operator output is sine times a log amplitude: attenuation is
//     tracked in the chip's own units, 1024 steps spanning 96 dB,
//     with total level adding 8 steps per count (0.75 dB), key
//     scaling and AM from the LFO added on top.
//   - The envelope generator ticks once every 3 native samples, as
//     on the chip. Its full-range time follows the datasheet rate
//     table shape (time doubles every 4 effective rate steps).
//     Attack is the documented exponential approach to full volume;
//     decay, sustain decay and release advance linearly in dB.
//   - Timers A and B count master clocks with the documented
//     periods, (1024 - NA) * 18 and (256 - NB) * 288 clocks, set
//     their status flags on expiry when the matching enable bit in
//     register 0x27 is set, and auto-reload while loaded.
//   - Channel 3 special mode (register 0x27 bits 6-7 nonzero) gives
//     operators 2, 3 and 4 their own F-number and block from the
//     0xA8-0xAE registers. CSM mode is approximated as normal
//     keying: the hardware would key channel 3 from timer A, which
//     almost no Genesis game uses, and the difference is documented
//     here rather than hidden.
//   - When register 0x2B bit 7 is set, channel 6 outputs the DAC
//     sample from register 0x2A (unsigned, 0x80 is the center)
//     instead of FM, through channel 6's pan controls.

// ymNativeRate is the YM2612 output sample rate on the Genesis:
// master clock divided by 144.
const ymNativeRate = float64(ymClock) / 144.0

// ymRatio is native samples per 44.1 kHz output frame.
const ymRatio = ymNativeRate / SampleRate

// ymMaxFP is the envelope attenuation ceiling in fixed point:
// 1023 attenuation steps (silence) shifted up by 20 fractional bits.
const ymMaxFP = 1023 << 20

// Envelope generator stages.
const (
	egOff = iota
	egAttack
	egDecay
	egSustain
	egRelease
	egSSG
)

// ymOp is one FM operator's runtime state. Only state that affects
// future output lives here, and all of it is serialized.
type ymOp struct {
	phase   uint32 // phase accumulator, one cycle is 2^21 counts
	levelFP int32  // envelope attenuation, 0 loudest, ymMaxFP silent
	stage   uint8  // eg stage (egOff, egAttack, ...)
	keyOn   bool   // current key state from register 0x28
	ssgDown bool   // SSG-EG: current leg runs toward silence
	ssgHold bool   // SSG-EG: pattern finished, holding its level
}

// ymChan is one channel's runtime state beyond its registers.
type ymChan struct {
	fb0 float32 // operator 1 output, previous sample (feedback)
	fb1 float32 // operator 1 output, two samples ago
}

// ymState is the whole FM engine state.
type ymState struct {
	ops   [6][4]ymOp // [channel][operator in logical order 1-4]
	chans [6]ymChan

	lfoPhase  uint32 // LFO phase accumulator, one cycle is 2^32
	timerAAcc uint32 // master clocks accumulated toward timer A expiry
	timerBAcc uint32 // master clocks accumulated toward timer B expiry
	egDiv     uint8  // envelope divider counter, ticks every 3 samples
	prev27    uint8  // last register 0x27 value, for timer load edges
	// advLedger is the master clocks of timer time the system has
	// already stepped through AdvanceClocks but the render loop has
	// not yet accounted for. ymGenerate consumes it instead of
	// stepping the timers a second time for the same span, so a
	// span of emulated time is never counted twice. Serialized:
	// without it a restore would re-step (or skip) timer time and
	// the flag pattern after the restore would drift.
	advLedger int64
}

// opSlot maps between logical operator index (0..3 for OP1..OP4) and
// register slot index. The YM2612 register file stores the operators
// in the order OP1, OP3, OP2, OP4, so the map is its own inverse:
// slot = opSlot[op] and op = opSlot[slot].
var opSlot = [4]int{0, 2, 1, 3}

// modMask[algo][op] is the bitmask of earlier operators whose outputs
// modulate this operator, from the documented algorithm diagrams.
var modMask = [8][4]uint8{
	{0, 0x1, 0x2, 0x4}, // 0: 1-2-3-4 chain
	{0, 0x0, 0x3, 0x4}, // 1: 1+2 into 3, 3 into 4
	{0, 0x0, 0x1, 0x6}, // 2: 1 into 3, 3+2 into 4
	{0, 0x1, 0x0, 0x6}, // 3: 1 into 2, 2+3 into 4
	{0, 0x1, 0x0, 0x4}, // 4: 1 into 2, 3 into 4
	{0, 0x1, 0x1, 0x1}, // 5: 1 into 2, 3 and 4
	{0, 0x1, 0x0, 0x0}, // 6: 1 into 2
	{0, 0x0, 0x0, 0x0}, // 7: four independent carriers
}

// carrierMask[algo] is the bitmask of operators that reach the
// channel output.
var carrierMask = [8]uint8{0x8, 0x8, 0x8, 0x8, 0xA, 0xE, 0xE, 0xF}

// carrierGain normalizes channel loudness by carrier count.
var carrierGain = [8]float32{1, 1, 1, 1, 0.7, 0.55, 0.55, 0.45}

// fbScale[fb] is the operator 1 feedback depth in sine table index
// units at full-scale output, doubling per step as documented.
var fbScale = [8]float32{0, 32, 64, 128, 256, 512, 1024, 2048}

// lfoFreqTable is the documented LFO frequency table (Hz) for
// register 0x22 bits 0-2.
var lfoFreqTable = [8]float64{3.98, 5.56, 6.02, 6.37, 6.88, 9.63, 48.1, 72.2}

// pmsCents[pms] is the documented pitch modulation depth in cents
// (peak deviation) for register 0xB4 bits 0-2.
var pmsCents = [8]float64{0, 3.4, 6.7, 10, 14, 20, 40, 80}

// amsSteps[ams] is the documented AM depth in attenuation steps
// (0, 1.4, 5.9, 11.8 dB) for register 0xB4 bits 4-5.
var amsSteps = [4]int32{0, 15, 63, 126}

// slSteps[sl] is the sustain level in attenuation steps from the
// datasheet table; note the documented jump at 15.
var slSteps = [16]int32{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 992}

// Tables built once at package load; the sample loop only reads.
var (
	sinTable [1024]float32
	// ampTable[a] is the linear amplitude for a attenuation steps,
	// a in 0..2175 (EG 1023 + TL 1016 + AM 126, plus headroom).
	ampTable   [2176]float32
	egTicks    [64]int32 // EG ticks for a full-range sweep per effective rate
	egIncFP    [64]int32 // fixed-point attenuation step per EG tick
	lfoIncTab  [8]uint32 // LFO phase increment per native sample
	ymInitDone = buildYMTables()
)

func buildYMTables() bool {
	for i := range sinTable {
		sinTable[i] = float32(math.Sin(2 * math.Pi * float64(i) / 1024))
	}
	for i := range ampTable {
		db := float64(i) * (96.0 / 1024.0)
		ampTable[i] = float32(math.Pow(10, -db/20))
	}
	for er := 0; er < 64; er++ {
		// Datasheet rate table shape: the full-range time doubles
		// every 4 rate steps, about 0.34 ms at rate 63.
		t := int32(6.2*math.Pow(2, float64(63-er)/4) + 0.5)
		if t < 1 {
			t = 1
		}
		egTicks[er] = t
		inc := int32(ymMaxFP) / t
		if inc < 1 {
			inc = 1
		}
		egIncFP[er] = inc
	}
	for i, f := range lfoFreqTable {
		lfoIncTab[i] = uint32(f / ymNativeRate * 4294967296.0)
	}
	return true
}

// ymBank returns the register bank and channel offset for a channel:
// channels 0-2 live in bank A, channels 3-5 in bank B.
func (s *Sound) ymBank(ch int) (*[256]uint8, int) {
	if ch < 3 {
		return &s.ymRegsA, ch
	}
	return &s.ymRegsB, ch - 3
}

// ymOpFreq returns the effective F-number and block for an operator,
// honoring channel 3 special mode (per-operator frequency registers
// 0xA8-0xAE; operator 1 keeps the normal channel registers).
func (s *Sound) ymOpFreq(ch, op int) (fnum, block int) {
	if ch == 2 && (s.ymRegsA[0x27]>>6)&3 != 0 {
		switch op {
		case 1:
			return ymFreqPair(s.ymRegsA[0xA8], s.ymRegsA[0xAC])
		case 2:
			return ymFreqPair(s.ymRegsA[0xA9], s.ymRegsA[0xAD])
		case 3:
			return ymFreqPair(s.ymRegsA[0xAA], s.ymRegsA[0xAE])
		}
	}
	bank, off := s.ymBank(ch)
	return ymFreqPair(bank[0xA0+off], bank[0xA4+off])
}

func ymFreqPair(lo, hi uint8) (fnum, block int) {
	return (int(hi&0x07) << 8) | int(lo), int(hi>>3) & 0x07
}

// ymKey applies a key on/off register (0x28) write: bits 4-7 key the
// operators in register slot order, bits 0-2 select the channel
// (0,1,2,4,5,6; 3 and 7 are not channels). Only transitions act:
// key on starts the attack from the current level, key off starts
// the release. The phase accumulator is not reset, as on the chip.
func (s *Sound) ymKey(val uint8) {
	var ch int
	switch sel := val & 0x07; {
	case sel < 3:
		ch = int(sel)
	case sel >= 4 && sel <= 6:
		ch = int(sel) - 1
	default:
		return
	}
	mask := (val >> 4) & 0x0F
	for slot := 0; slot < 4; slot++ {
		o := &s.ym.ops[ch][opSlot[slot]]
		if mask&(1<<uint(slot)) != 0 {
			if !o.keyOn {
				o.keyOn = true
				o.stage = egAttack
				o.ssgHold = false
			}
		} else if o.keyOn {
			o.keyOn = false
			o.stage = egRelease
		}
	}
}

// ymAdvanceTimers advances timers A and B by the given number of
// master clocks and sets the status flags for expiries whose
// enable bit in register 0x27 is set. A timer only counts while
// its load bit is set; the accumulator restarts when the load bit
// rises, which WriteYM handles through prev27. Expiry drains whole
// periods off the accumulator, so the timers auto-reload and any
// chunk size (one native sample of 144 clocks from the render
// loop, a scanline of 68000 cycles from the system) behaves the
// same.
func (s *Sound) ymAdvanceTimers(clocks uint32) {
	r27 := s.ymRegsA[0x27]
	if r27&0x01 != 0 { // timer A load
		na := (int(s.ymRegsA[0x24]) << 2) | (int(s.ymRegsA[0x25]) >> 6)
		period := uint32(18 * (1024 - na))
		s.ym.timerAAcc += clocks
		for s.ym.timerAAcc >= period {
			s.ym.timerAAcc -= period
			if r27&0x04 != 0 { // timer A enable
				s.ymStatus |= 0x01
			}
		}
	}
	if r27&0x02 != 0 { // timer B load
		period := uint32(288 * (256 - int(s.ymRegsA[0x26])))
		s.ym.timerBAcc += clocks
		for s.ym.timerBAcc >= period {
			s.ym.timerBAcc -= period
			if r27&0x08 != 0 { // timer B enable
				s.ymStatus |= 0x02
			}
		}
	}
}

// ymEffRate computes the effective envelope rate for a rate register
// value with key scaling: rate = 2*R + keycode addend, clamped to
// 63. The keycode derives from the operator's own pitch.
func ymEffRate(r int, ks, kc int) int {
	add := 0
	if ks > 0 {
		add = kc >> uint(3-ks)
	}
	er := 2*r + add
	if er > 63 {
		er = 63
	}
	return er
}

// ymEGAfterAttack picks the stage that follows a completed attack:
// the SSG-EG pattern stage when SSG-EG is enabled for the operator,
// otherwise the first decay.
func (s *Sound) ymEGAfterAttack(o *ymOp, ssgReg uint8) {
	if ssgReg&0x08 != 0 {
		o.stage = egSSG
		o.ssgDown = (ssgReg & 0x07) < 4
		o.ssgHold = false
	} else {
		o.stage = egDecay
	}
}

// ymTickEG advances every operator's envelope by one EG tick.
func (s *Sound) ymTickEG() {
	for ch := 0; ch < 6; ch++ {
		bank, off := s.ymBank(ch)
		for op := 0; op < 4; op++ {
			o := &s.ym.ops[ch][op]
			if o.stage == egOff {
				continue
			}
			s.ymTickOp(bank, off, ch, op, o)
		}
	}
}

// ymTickOp advances one operator's envelope by one EG tick.
func (s *Sound) ymTickOp(bank *[256]uint8, off, ch, op int, o *ymOp) {
	slot := opSlot[op]
	ksReg := bank[0x50+slot*4+off]
	ks := int(ksReg >> 6)
	fnum, block := s.ymOpFreq(ch, op)
	kc := (block << 2) | (fnum >> 9)
	rate := func(r int) int { return ymEffRate(r, ks, kc) }

	switch o.stage {
	case egAttack:
		er := rate(int(ksReg & 0x1F))
		div := egTicks[er] / 4
		if div < 1 {
			div = 1
		}
		// Exponential approach: each tick removes a fraction of
		// the remaining attenuation, plus one unit so the tail
		// always lands.
		step := o.levelFP/div + 1
		if step >= o.levelFP {
			o.levelFP = 0
			s.ymEGAfterAttack(o, bank[0x90+slot*4+off])
		} else {
			o.levelFP -= step
		}
	case egDecay:
		er := rate(int(bank[0x60+slot*4+off] & 0x1F))
		o.levelFP += egIncFP[er]
		slFP := slSteps[bank[0x80+slot*4+off]>>4] << 20
		if o.levelFP >= slFP {
			o.stage = egSustain
		}
		if o.levelFP > ymMaxFP {
			o.levelFP = ymMaxFP
		}
	case egSustain:
		er := rate(int(bank[0x70+slot*4+off] & 0x1F))
		o.levelFP += egIncFP[er]
		if o.levelFP >= ymMaxFP {
			o.levelFP = ymMaxFP
			o.stage = egOff
		}
	case egRelease:
		// Documented release rate is 2*RR + 1 before key scaling.
		rr := int(bank[0x80+slot*4+off] & 0x0F)
		add := 0
		if ks > 0 {
			add = kc >> uint(3-ks)
		}
		er := 2*rr + 1 + add
		if er > 63 {
			er = 63
		}
		o.levelFP += egIncFP[er]
		if o.levelFP >= ymMaxFP {
			o.levelFP = ymMaxFP
			o.stage = egOff
		}
	case egSSG:
		if o.ssgHold {
			return
		}
		pat := bank[0x90+slot*4+off] & 0x07
		er := rate(int(bank[0x60+slot*4+off] & 0x1F)) // SSG-EG cycles at D1R
		if o.ssgDown {
			o.levelFP += egIncFP[er]
			if o.levelFP >= ymMaxFP {
				o.levelFP = ymMaxFP
				s.ymSSGBound(o, pat, true)
			}
		} else {
			div := egTicks[er] / 4
			if div < 1 {
				div = 1
			}
			step := o.levelFP/div + 1
			if step >= o.levelFP {
				o.levelFP = 0
				s.ymSSGBound(o, pat, false)
			} else {
				o.levelFP -= step
			}
		}
	}
}

// ymSSGBound applies an SSG-EG pattern boundary: the level just
// reached the bottom (full attenuation, atBottom true) or the top
// (zero attenuation). Patterns follow the documented SSG shapes:
// 0 repeat decay, 1 decay then hold at bottom, 2 alternate, 3 decay
// then hold at top, 4 repeat attack, 5 attack then hold at top,
// 6 alternate, 7 attack then hold at bottom.
func (s *Sound) ymSSGBound(o *ymOp, pat uint8, atBottom bool) {
	switch pat {
	case 0: // repeat decay: jump back to the top and keep decaying
		if atBottom {
			o.levelFP = 0
		}
	case 1: // decay once, hold at the bottom
		if atBottom {
			o.ssgHold = true
		}
	case 2, 6: // alternate direction at each bound
		o.ssgDown = !atBottom
	case 3: // decay once, jump to the top and hold
		if atBottom {
			o.levelFP = 0
			o.ssgHold = true
		}
	case 4: // repeat attack: jump to the bottom and keep attacking
		if !atBottom {
			o.levelFP = ymMaxFP
		}
	case 5: // attack once, hold at the top
		if !atBottom {
			o.ssgHold = true
		}
	case 7: // attack once, jump to the bottom and hold
		if !atBottom {
			o.levelFP = ymMaxFP
			o.ssgHold = true
		}
	}
}

// ymOpStep computes the phase increment for one operator: the
// documented (F-number << block) base, detune offset, multiple
// scaling (multiple 0 is the 0.5x case), and LFO pitch modulation.
func ymOpStep(fnum, block, dt, mul int, pmFactor float64) uint32 {
	base := int32(fnum) << uint(block)
	if mag := dt & 3; mag != 0 && base != 0 {
		// Detune: the datasheet table's offset grows with pitch
		// (keycode) and with the detune magnitude. This follows
		// that shape, up to about 5 percent at the top of the
		// range; it is an approximation of the printed table.
		kc := (block << 2) | (fnum >> 9)
		off := (base * int32(mag) * int32(kc+1)) >> 11
		if dt&4 != 0 {
			base -= off
		} else {
			base += off
		}
		if base < 0 {
			base = 0
		}
	}
	mul2 := int32(2 * mul)
	if mul == 0 {
		mul2 = 1 // documented 0.5x case
	}
	step := base * mul2 >> 1
	if pmFactor != 1 {
		step = int32(float64(step) * pmFactor)
	}
	if step < 0 {
		step = 0
	}
	return uint32(step)
}

// ymChannel renders one native-rate stereo pair's worth of one FM
// channel into the l/r accumulators, applying pan. DAC mode on
// channel 6 replaces the FM output.
func (s *Sound) ymChannel(ch int, lfoSigned, lfoTri float64, lfoOn bool, acc *[2]float32) {
	bank, off := s.ymBank(ch)
	pan := bank[0xB4+off]

	dacOn := ch == 5 && s.ymRegsA[0x2B]&0x80 != 0

	algo := bank[0xB0+off] & 0x07
	fb := (bank[0xB0+off] >> 3) & 0x07
	ams := (bank[0xB4+off] >> 4) & 0x03
	pms := bank[0xB4+off] & 0x07

	pmFactor := 1.0
	if lfoOn && pms != 0 {
		pmFactor = math.Pow(2, lfoSigned*pmsCents[pms]/1200)
	}
	amAtt := int32(0)
	if lfoOn {
		amAtt = int32(float64(amsSteps[ams]) * lfoTri)
	}

	var outs [4]float32
	for op := 0; op < 4; op++ {
		o := &s.ym.ops[ch][op]
		slot := opSlot[op]

		// Modulation input from earlier operators plus, for
		// operator 1, its feedback history.
		var mod float32
		m := modMask[algo][op]
		for src := 0; src < op; src++ {
			if m&(1<<uint(src)) != 0 {
				mod += outs[src]
			}
		}
		idxOff := int32(mod * 512)
		if op == 0 {
			avg := (s.ym.chans[ch].fb0 + s.ym.chans[ch].fb1) * 0.5
			idxOff += int32(avg * fbScale[fb])
		}
		idx := (int32(o.phase>>11) + idxOff) & 1023

		fnum, block := s.ymOpFreq(ch, op)
		dtMul := bank[0x30+slot*4+off]
		step := ymOpStep(fnum, block, int(dtMul>>4)&0x07, int(dtMul&0x0F), pmFactor)
		o.phase += step

		atten := int(o.levelFP>>20) + int(bank[0x40+slot*4+off]&0x7F)*8
		if bank[0x60+slot*4+off]&0x80 != 0 { // AM enable for this operator
			atten += int(amAtt)
		}
		if atten > len(ampTable)-1 {
			atten = len(ampTable) - 1
		} else if atten < 0 {
			atten = 0
		}
		outs[op] = sinTable[idx] * ampTable[atten]
	}
	s.ym.chans[ch].fb1 = s.ym.chans[ch].fb0
	s.ym.chans[ch].fb0 = outs[0]

	var v float32
	if dacOn {
		// DAC sample: unsigned 8-bit data, 0x80 is the center.
		v = float32(int(s.ymRegsA[0x2A])-128) / 128 * 0.75
	} else {
		cm := carrierMask[algo]
		for op := 0; op < 4; op++ {
			if cm&(1<<uint(op)) != 0 {
				v += outs[op]
			}
		}
		v *= carrierGain[algo]
	}

	if pan&0x80 != 0 {
		acc[0] += v
	}
	if pan&0x40 != 0 {
		acc[1] += v
	}
}

// ymGenerate renders n native-rate stereo samples into s.fmBuf.
func (s *Sound) ymGenerate(n int) {
	need := 2 * n
	if cap(s.fmBuf) < need {
		s.fmBuf = make([]float32, need)
	}
	s.fmBuf = s.fmBuf[:need]

	lfoReg := s.ymRegsA[0x22]
	lfoOn := lfoReg&0x08 != 0
	lfoInc := lfoIncTab[lfoReg&0x07]

	for i := 0; i < n; i++ {
		// Timer time for this native sample (144 master clocks):
		// whatever the system already stepped through
		// AdvanceClocks is consumed from the ledger instead of
		// being stepped again; only the uncovered remainder is
		// stepped here, so total timer time tracks emulated time
		// exactly once no matter who drove it.
		switch {
		case s.ym.advLedger >= 144:
			s.ym.advLedger -= 144
		case s.ym.advLedger > 0:
			s.ymAdvanceTimers(uint32(144 - s.ym.advLedger))
			s.ym.advLedger = 0
		default:
			s.ymAdvanceTimers(144)
		}

		var lfoSigned, lfoTri float64
		if lfoOn {
			s.ym.lfoPhase += lfoInc
			ph := float64(s.ym.lfoPhase) / 4294967296.0
			if ph < 0.5 {
				lfoTri = ph * 2
			} else {
				lfoTri = 2 - ph*2
			}
			lfoSigned = lfoTri*2 - 1
		}

		s.ym.egDiv++
		if s.ym.egDiv >= 3 {
			s.ym.egDiv = 0
			s.ymTickEG()
		}

		var acc [2]float32
		for ch := 0; ch < 6; ch++ {
			s.ymChannel(ch, lfoSigned, lfoTri, lfoOn, &acc)
		}
		s.fmBuf[2*i] = acc[0]
		s.fmBuf[2*i+1] = acc[1]
	}
}
