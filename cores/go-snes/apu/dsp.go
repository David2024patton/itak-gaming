// S-DSP: the SNES digital signal processor, modelled one
// output sample at a time (one StepSample call is one
// 1/32000 s tick). Register semantics, BRR decoding, the
// Gaussian interpolation kernel, the ADSR/GAIN envelope
// rates, pitch modulation, noise generation and the echo
// FIR all follow the published hardware documentation in
// fullsnes (SNES APU DSP chapters). Where the documentation
// offers more than one reading, the choice made here is
// stated in a comment next to the code it affects.
package apu

// Envelope states for one voice.
const (
	envOff = iota
	envAttack
	envDecay
	envSustain
	envRelease
	envGain // ADSR1 bit 7 clear: VxGAIN drives the level
)

// envRatePeriod is the published 32 entry envelope rate
// table: the number of 32000 Hz sample ticks between two
// envelope steps for a 5 bit rate value. Entry 31 is 0 in
// the table; a period of 0 means a step on every tick, so
// ratePeriod maps it to 1.
var envRatePeriod = [32]int{
	2048, 1536, 1280, 1024, 768, 640, 512, 384,
	320, 256, 192, 160, 128, 96, 80, 64,
	48, 40, 32, 24, 20, 16, 12, 10,
	8, 6, 5, 4, 3, 2, 1, 0,
}

func ratePeriod(rate int) int {
	p := envRatePeriod[rate&31]
	if p == 0 {
		return 1
	}
	return p
}

// wrap15 first clamps to a signed 16 bit value and then
// wraps into the signed 15 bit range used by the BRR
// decoder and the echo buffer, following the overflow
// description in fullsnes: values in +4000h..+7FFFh wrap
// to negative, values below -4000h wrap around the 15 bit
// modulus (the lost sign behaviour noted in the doc).
func wrap15(x int) int {
	if x > 0x7FFF {
		x = 0x7FFF
	}
	if x < -0x8000 {
		x = -0x8000
	}
	x %= 0x8000
	if x < 0 {
		x += 0x8000
	}
	if x >= 0x4000 {
		x -= 0x8000
	}
	return x
}

func clamp16(x int) int16 {
	if x > 0x7FFF {
		return 0x7FFF
	}
	if x < -0x8000 {
		return -0x8000
	}
	return int16(x)
}

// dspVoice is the live per voice state that is not held in
// the register file: BRR decode position, Gaussian history,
// and the envelope machine.
type dspVoice struct {
	on        bool
	blockAddr uint16 // current BRR block
	loopAddr  uint16 // directory loop address, latched at KON
	sampleIdx int    // next sample within the block, 0..15
	hdr       uint8  // header of the block being decoded

	filter1 int // most recent decoded sample (BRR filter "old")
	filter2 int // decoded sample before that ("older")

	gauss [4]int // Gaussian history: 0 newest, 1 old, 2 older, 3 oldest

	pitchCounter int // 12 bit fraction accumulated below one source sample

	env        int
	envState   uint8
	envCounter int
	konDelay   int // samples of forced silence left after KON
	outx       int // last voice output after envelope, before VxVOL
}

// DSP is the S-DSP register file plus voice, noise and
// echo state. It reads sample data from the APU RAM given
// to AttachRAM.
type DSP struct {
	regs [128]byte
	ram  []byte

	voices     [8]dspVoice
	endx       uint8
	pendingKon uint8

	noise      uint16 // 15 bit LFSR pattern, bit 14 is the sign end
	noiseCount int
	echoOffset int    // frame index inside the echo ring
	firHistL   [8]int // echo read history, index 0 oldest
	firHistR   [8]int // echo read history, index 0 oldest
	lastEchoL  int    // most recent FIR outputs, for tests
	lastEchoR  int
}

// NewDSP returns a DSP in its reset state.
func NewDSP() *DSP {
	d := &DSP{}
	d.Reset()
	return d
}

// Reset restores the power on state: registers cleared,
// voices off, noise LFSR at its documented initial level
// (pattern 4000h, the 15 bit value -4000h). FLG powers up
// as E0h per fullsnes (soft reset released, amplifier
// muted, echo buffer writes disabled, noise stopped);
// this model stores that value, which matters because
// echo writes must stay off until a driver sets up ESA
// and EDL, or the ring at ESA 0 would scribble over the
// low RAM the IPL uses for its transfer pointer.
func (d *DSP) Reset() {
	ram := d.ram
	*d = DSP{ram: ram}
	d.noise = 0x4000
	d.regs[0x6C] = 0xE0
}

// AttachRAM points the DSP at the 64 KB APU RAM. Sample
// directory reads, BRR data and the echo ring live there.
func (d *DSP) AttachRAM(ram []byte) { d.ram = ram }

func (d *DSP) readRAM(addr uint16) byte {
	if d.ram == nil {
		return 0
	}
	return d.ram[addr]
}

func (d *DSP) readRAM16(addr uint16) uint16 {
	return uint16(d.readRAM(addr)) | uint16(d.readRAM(addr+1))<<8
}

// WriteReg writes one DSP register (address 0..127).
func (d *DSP) WriteReg(addr uint8, v uint8) {
	addr &= 0x7F
	switch addr {
	case 0x7C: // ENDX: any write clears all bits (fullsnes).
		d.endx = 0
		return
	case 0x4C: // KON: latched for reads, bits consumed at the next sample.
		d.regs[addr] = v
		d.pendingKon |= v
		return
	case 0x6C: // FLG.
		d.regs[addr] = v
		if v&0x80 != 0 {
			d.softReset()
		}
		return
	}
	d.regs[addr] = v
	// Direct GAIN is applied at the moment the GAIN register
	// is written (the documented reading: envelope level =
	// (GAIN AND 7Fh) * 16). It only matters for a voice that
	// is currently running in GAIN mode.
	if addr&0x0F == 0x07 && addr < 0x80 {
		vi := int(addr >> 4)
		voice := &d.voices[vi]
		if d.regs[vi*16+5]&0x80 == 0 && v&0x80 == 0 && voice.envState == envGain {
			voice.env = int(v&0x7F) << 4
		}
	}
}

// softReset is FLG bit 7: all voices keyed off with
// envelopes at zero, and KON, KOFF and ENDX cleared.
func (d *DSP) softReset() {
	for i := range d.voices {
		d.voices[i].on = false
		d.voices[i].env = 0
		d.voices[i].envState = envOff
		d.voices[i].outx = 0
	}
	d.endx = 0
	d.pendingKon = 0
	d.regs[0x4C] = 0
	d.regs[0x5C] = 0
}

// ReadReg reads one DSP register. Write only registers
// return their stored latch value; ENVX, OUTX and ENDX
// reflect live state.
func (d *DSP) ReadReg(addr uint8) uint8 {
	addr &= 0x7F
	if addr == 0x7C {
		return d.endx
	}
	switch addr & 0x0F {
	case 0x08: // ENVX: upper 7 bits of the 11 bit envelope.
		if addr < 0x80 {
			return uint8(d.voices[addr>>4].env >> 4)
		}
	case 0x09: // OUTX: high byte of the 15 bit voice output.
		if addr < 0x80 {
			return uint8(d.voices[addr>>4].outx >> 7)
		}
	}
	return d.regs[addr]
}

// Envelope returns the current 11 bit envelope level of a
// voice (0..0x7FF), for tests and tooling.
func (d *DSP) Envelope(v int) int {
	if v < 0 || v > 7 {
		return 0
	}
	return d.voices[v].env
}

// VoiceOn reports whether a voice is currently keyed on.
func (d *DSP) VoiceOn(v int) bool {
	if v < 0 || v > 7 {
		return false
	}
	return d.voices[v].on
}

// Outx returns the last post envelope, pre volume sample
// of a voice, for tests and tooling.
func (d *DSP) Outx(v int) int {
	if v < 0 || v > 7 {
		return 0
	}
	return d.voices[v].outx
}

// NoiseState returns the raw 15 bit noise LFSR pattern.
func (d *DSP) NoiseState() uint16 { return d.noise }

// keyOn starts a voice from its directory entry: position
// at the start address, BRR and Gaussian history cleared,
// envelope from zero in Attack (or GAIN) state, and the
// documented 5 sample KON delay during which the voice
// produces no output.
func (d *DSP) keyOn(vi int) {
	voice := &d.voices[vi]
	srcn := d.regs[vi*16+4]
	entry := int(d.regs[0x5D])<<8 + int(srcn)*4
	voice.blockAddr = d.readRAM16(uint16(entry))
	voice.loopAddr = d.readRAM16(uint16(entry + 2))
	voice.sampleIdx = 0
	voice.hdr = 0
	voice.filter1, voice.filter2 = 0, 0
	voice.gauss = [4]int{}
	voice.pitchCounter = 0
	voice.env = 0
	voice.envCounter = 0
	voice.konDelay = 5
	voice.outx = 0
	voice.on = true
	if d.regs[vi*16+5]&0x80 != 0 {
		voice.envState = envAttack
	} else {
		voice.envState = envGain
		if gain := d.regs[vi*16+7]; gain&0x80 == 0 {
			voice.env = int(gain&0x7F) << 4
		}
	}
}

// stepEnvelope advances one voice envelope by one sample
// tick. The per voice counter steps when it reaches the
// period from envRatePeriod for the current rate.
func (d *DSP) stepEnvelope(vi int) {
	voice := &d.voices[vi]
	base := vi * 16
	adsr1 := d.regs[base+5]
	adsr2 := d.regs[base+6]
	switch voice.envState {
	case envAttack:
		rate := int(adsr1&0x0F)*2 + 1
		voice.envCounter++
		if voice.envCounter >= ratePeriod(rate) {
			voice.envCounter = 0
			voice.env += 0x20
			if voice.env >= 0x7FF {
				voice.env = 0x7FF
				voice.envState = envDecay
			}
		}
	case envDecay:
		rate := int((adsr1>>4)&0x07) * 2
		voice.envCounter++
		if voice.envCounter >= ratePeriod(rate) {
			voice.envCounter = 0
			voice.env = voice.env * 251 / 256
			sustain := ((int(adsr2>>5) + 1) << 8) - 1
			if voice.env <= sustain {
				voice.envState = envSustain
			}
		}
	case envSustain:
		rate := int(adsr2&0x1F) * 2
		if rate > 31 {
			rate = 31
		}
		voice.envCounter++
		if voice.envCounter >= ratePeriod(rate) {
			voice.envCounter = 0
			voice.env = voice.env * 251 / 256
		}
	case envRelease:
		// Release steps on every sample.
		voice.env = voice.env * 251 / 256
		if voice.env == 0 {
			voice.on = false
			voice.envState = envOff
		}
	case envGain:
		gain := d.regs[base+7]
		if gain&0x80 == 0 {
			// Direct gain: fixed level.
			voice.env = int(gain&0x7F) << 4
			return
		}
		voice.envCounter++
		if voice.envCounter < ratePeriod(int(gain&0x1F)) {
			return
		}
		voice.envCounter = 0
		switch (gain >> 5) & 0x03 {
		case 0: // linear decrease
			voice.env -= 8
			if voice.env < 0 {
				voice.env = 0
			}
		case 1: // exponential decrease
			voice.env = voice.env * 251 / 256
		case 2: // linear increase
			voice.env += 0x20
		case 3: // bent increase: full step below 600h, half at and above
			if voice.env < 0x600 {
				voice.env += 0x20
			} else {
				voice.env += 0x10
			}
		}
		if voice.env > 0x7FF {
			voice.env = 0x7FF
		}
		if voice.env < 0 {
			voice.env = 0
		}
	}
}

// decodeNextBRR decodes the next source sample of a voice
// and returns it as a signed 15 bit value. Block headers
// are read when a block starts; an end block sets ENDX at
// the start of the block (per fullsnes), and when the
// block finishes the voice either jumps to its loop
// address (loop flag) or is keyed off with envelope zero.
func (d *DSP) decodeNextBRR(vi int) int {
	voice := &d.voices[vi]
	if voice.sampleIdx == 0 {
		voice.hdr = d.readRAM(voice.blockAddr)
		if voice.hdr&0x01 != 0 {
			d.endx |= 1 << uint(vi)
		}
	}
	hdr := voice.hdr
	b := d.readRAM(voice.blockAddr + 1 + uint16(voice.sampleIdx>>1))
	nib := int(b & 0x0F)
	if voice.sampleIdx&1 == 0 {
		nib = int(b >> 4)
	}
	if nib >= 8 {
		nib -= 16
	}
	// Convert the nibble to a 15 bit sample:
	// sample = (nibble SHL shift) SAR 1. For shift 13..15
	// the doc says decoding works as if shift=12 and the
	// nibble were (nibble SAR 3), which collapses samples
	// to 0 or -2048: the sign only special case.
	shift := int(hdr >> 4)
	var s int
	if shift >= 13 {
		s = ((nib >> 3) << 12) >> 1
	} else {
		s = (nib << uint(shift)) >> 1
	}
	p1, p2 := voice.filter1, voice.filter2
	var out int
	switch (hdr >> 2) & 0x03 {
	case 0:
		out = s
	case 1:
		// s + old*15/16, exact form: old + ((-old) SAR 4)
		out = s + p1 + ((-p1) >> 4)
	case 2:
		// s + old*61/32 - older*15/16, exact doc form
		out = s + 2*p1 + ((-3 * p1) >> 5) - p2 + (p2 >> 4)
	case 3:
		// s + old*115/64 - older*13/16, exact doc form
		out = s + 2*p1 + ((-13 * p1) >> 6) - p2 + ((3 * p2) >> 4)
	}
	out = wrap15(out)
	voice.filter2 = p1
	voice.filter1 = out
	voice.sampleIdx++
	if voice.sampleIdx == 16 {
		voice.sampleIdx = 0
		if hdr&0x01 != 0 {
			if hdr&0x02 != 0 {
				voice.blockAddr = voice.loopAddr
			} else {
				voice.on = false
				voice.envState = envOff
				voice.env = 0
			}
		} else {
			voice.blockAddr += 9
		}
	}
	return out
}

// voiceSample advances the BRR position of a voice by its
// pitch and returns the Gaussian interpolated sample
// before the envelope is applied.
func (d *DSP) voiceSample(vi int) int {
	voice := &d.voices[vi]
	base := vi * 16
	pitch := int(d.regs[base+3]&0x3F)<<8 | int(d.regs[base+2])
	// Pitch modulation (fullsnes BRR Pitch chapter):
	// factor = (OUTX of the previous voice SAR 4) + 400h,
	// step = (pitch * factor) SAR 10. The previous voice
	// has already been processed this sample, so its OUTX
	// is fresh.
	if vi > 0 && d.regs[0x2D]>>uint(vi)&1 != 0 {
		factor := (d.voices[vi-1].outx >> 4) + 0x400
		pitch = (pitch * factor) >> 10
	}
	if pitch < 0 {
		pitch = 0
	}
	voice.pitchCounter += pitch
	steps := voice.pitchCounter >> 12
	voice.pitchCounter &= 0xFFF
	for i := 0; i < steps; i++ {
		s := d.decodeNextBRR(vi)
		voice.gauss[3] = voice.gauss[2]
		voice.gauss[2] = voice.gauss[1]
		voice.gauss[1] = voice.gauss[0]
		voice.gauss[0] = s
		if !voice.on {
			break
		}
	}
	// Gaussian kernel over the 4 sample history with phase
	// from the pitch counter fraction (bits 4..11 of the
	// 12 bit fraction). Tap order per fullsnes:
	// gauss[0FFh-i] oldest, gauss[1FFh-i] older,
	// gauss[100h+i] old, gauss[i] newest, total shift 11
	// (the doc applies SAR 10 per tap then SAR 1; this
	// model sums first and shifts once, which differs by
	// at most the per tap rounding).
	phase := (voice.pitchCounter >> 4) & 0xFF
	sum := int(gaussTable[255-phase])*voice.gauss[3] +
		int(gaussTable[511-phase])*voice.gauss[2] +
		int(gaussTable[256+phase])*voice.gauss[1] +
		int(gaussTable[phase])*voice.gauss[0]
	return sum >> 11
}

// stepNoise clocks the shared 15 bit noise LFSR once.
func (d *DSP) stepNoise() {
	fb := (d.noise & 1) ^ ((d.noise >> 1) & 1)
	d.noise = (d.noise>>1)&0x3FFF | fb<<14
}

func noiseValue(pattern uint16) int {
	v := int(pattern)
	if v >= 0x4000 {
		v -= 0x8000
	}
	return v
}

// StepSample produces one stereo output sample at
// 32000 Hz. Order per sample: KOFF and FLG reset are
// applied, pending KON bits are consumed, the noise
// clock runs, voices are processed in order 0..7 (so
// pitch modulation sees the previous voice fresh OUTX),
// then the echo ring is read, filtered, fed back and
// mixed.
func (d *DSP) StepSample() (l, r int16) {
	flg := d.regs[0x6C]
	koff := d.regs[0x5C]
	for vi := 0; vi < 8; vi++ {
		voice := &d.voices[vi]
		if flg&0x80 != 0 {
			voice.on = false
			voice.env = 0
			voice.envState = envOff
			voice.outx = 0
		} else if koff>>uint(vi)&1 != 0 && voice.on {
			voice.envState = envRelease
		}
	}
	if d.pendingKon != 0 {
		for vi := 0; vi < 8; vi++ {
			if d.pendingKon>>uint(vi)&1 != 0 {
				d.keyOn(vi)
			}
		}
		d.pendingKon = 0
	}

	// Noise clock: FLG bits 0..4 select a rate from the
	// same envelope rate table.
	d.noiseCount++
	if d.noiseCount >= ratePeriod(int(flg&0x1F)) {
		d.noiseCount = 0
		d.stepNoise()
	}

	non := d.regs[0x3D]
	eon := d.regs[0x4D]
	var sumL, sumR, echoInL, echoInR int
	for vi := 0; vi < 8; vi++ {
		voice := &d.voices[vi]
		if !voice.on {
			voice.outx = 0
			continue
		}
		d.stepEnvelope(vi)
		raw := 0
		if non>>uint(vi)&1 != 0 {
			// Noise voices still decode BRR blocks on the
			// hardware (an end block still ends the voice),
			// so advance the BRR position as usual and then
			// substitute the noise value.
			_ = d.voiceSample(vi)
			raw = noiseValue(d.noise)
		} else {
			raw = d.voiceSample(vi)
		}
		out := raw * voice.env >> 11
		if voice.konDelay > 0 {
			// KON delay: position and envelope run, but the
			// voice output is gated to zero for 5 samples.
			voice.konDelay--
			out = 0
		}
		voice.outx = out
		base := vi * 16
		termL := out * int(int8(d.regs[base+0])) >> 7
		termR := out * int(int8(d.regs[base+1])) >> 7
		sumL += termL
		sumR += termR
		if eon>>uint(vi)&1 != 0 {
			// Echo input is the voice output summed dry:
			// post envelope, with VxVOL applied, before the
			// master volume (per the echo chapter reading).
			echoInL += termL
			echoInR += termR
		}
	}

	firL, firR := d.stepEcho(echoInL, echoInR, flg)

	// Main mix: voice sums plus the echo FIR output scaled
	// by EVOL, the whole then scaled by MVOL.
	outL := (sumL + firL*int(int8(d.regs[0x1C]))>>7) * int(int8(d.regs[0x0C])) >> 7
	outR := (sumR + firR*int(int8(d.regs[0x3C]))>>7) * int(int8(d.regs[0x2C])) >> 7
	if flg&0x40 != 0 { // FLG mute: internal processing continues, output zero.
		outL, outR = 0, 0
	}
	return clamp16(outL), clamp16(outR)
}

// stepEcho reads one frame from the echo ring at ESA,
// runs the 8 tap FIR over the read history, writes the
// feedback frame back unless FLG bit 5 disables writes,
// and advances the ring offset. Ring length in frames is
// EDL * 512 (EDL * 16 ms, EDL * 2048 bytes total); EDL 0
// is treated as a 1 frame ring, matching the hardware
// note that a zero length buffer reads and writes the
// same address.
func (d *DSP) stepEcho(inL, inR int, flg uint8) (int, int) {
	if d.ram == nil {
		return 0, 0
	}
	edl := int(d.regs[0x7D] & 0x0F)
	frames := edl * 512
	if frames == 0 {
		frames = 1
	}
	if d.echoOffset >= frames {
		d.echoOffset = 0
	}
	addr := uint16(int(d.regs[0x6D])<<8 + d.echoOffset*4)

	wordL := uint16(d.ram[addr]) | uint16(d.ram[addr+1])<<8
	wordR := uint16(d.ram[addr+2]) | uint16(d.ram[addr+3])<<8
	readL := int(int16(wordL)) >> 1
	readR := int(int16(wordR)) >> 1

	copy(d.firHistL[:], d.firHistL[1:])
	copy(d.firHistR[:], d.firHistR[1:])
	d.firHistL[7] = readL
	d.firHistR[7] = readR

	// FIR: sum of coef[i] * history[i] with history index
	// 0 the oldest frame and coef registers 0Fh, 1Fh, ..
	// 7Fh in order, total shift 6.
	var accL, accR int
	for i := 0; i < 8; i++ {
		coef := int(int8(d.regs[0x0F+i*0x10]))
		accL += coef * d.firHistL[i]
		accR += coef * d.firHistR[i]
	}
	firL := accL >> 6
	firR := accR >> 6
	d.lastEchoL, d.lastEchoR = firL, firR

	efb := int(int8(d.regs[0x0D]))
	if flg&0x20 == 0 {
		writeL := uint16(wrap15(inL+firL*efb>>7) << 1)
		writeR := uint16(wrap15(inR+firR*efb>>7) << 1)
		d.ram[addr] = byte(writeL)
		d.ram[addr+1] = byte(writeL >> 8)
		d.ram[addr+2] = byte(writeR)
		d.ram[addr+3] = byte(writeR >> 8)
	}
	d.echoOffset++
	if d.echoOffset >= frames {
		d.echoOffset = 0
	}
	return firL, firR
}
