package spu

// Advance advances the SPU by CPU cycles (CPU clock
// 33868800 Hz) and generates output samples at
// 44100 Hz. The ratio is exact (768 CPU cycles per
// sample); the remainder is kept in cycleAcc so the
// conversion is fully deterministic. cd is called
// exactly once per generated sample to fetch the CD
// input sample; a nil cd is silence.
func (s *SPU) Advance(cycles int, cd func() (int16, int16)) {
	if cycles <= 0 {
		return
	}
	s.cycleAcc += int64(cycles) * 44100
	n := s.cycleAcc / 33868800
	s.cycleAcc -= n * 33868800
	for i := int64(0); i < n; i++ {
		s.genSample(cd)
	}
}

// AudioBuffer drains the pending interleaved stereo
// output samples (left, right pairs).
func (s *SPU) AudioBuffer() []int16 {
	if len(s.audio) == 0 {
		return nil
	}
	out := s.audio
	s.audio = nil
	return out
}

// genSample generates one 44100 Hz output sample.
func (s *SPU) genSample(cd func() (int16, int16)) {
	var cdL, cdR int16
	if cd != nil {
		cdL, cdR = cd()
	}
	s.runNoise()

	enabled := s.spucnt&0x8000 != 0
	unmuted := s.spucnt&0x4000 != 0

	var dryL, dryR, sendL, sendR int32
	var capV1, capV3 int32
	var prevOutx int32
	for n := 0; n < numVoices; n++ {
		outx := s.voiceSample(n, prevOutx)
		prevOutx = outx
		if n == 1 {
			capV1 = outx
		}
		if n == 3 {
			capV3 = outx
		}
		if !enabled || !unmuted {
			continue
		}
		v := &s.voices[n]
		cL := mul15(outx, v.curVolL)
		cR := mul15(outx, v.curVolR)
		dryL += cL
		dryR += cR
		if s.eon>>uint(n)&1 != 0 {
			sendL += cL
			sendR += cR
		}
	}
	// CD input, at CD volume, when enabled in
	// SPUCNT (bit 0). CD is not affected by the SPU
	// enable or mute bits per the document.
	if s.spucnt&0x1 != 0 {
		cL := mul15(int32(cdL), s.cdVolL)
		cR := mul15(int32(cdR), s.cdVolR)
		dryL += cL
		dryR += cR
		if s.spucnt&0x4 != 0 {
			sendL += cL
			sendR += cR
		}
	}
	// Reverb at half rate, on every second sample.
	if !s.revPhase {
		s.runReverb(sendL, sendR)
	}
	s.revPhase = !s.revPhase
	wetL, wetR := int32(0), int32(0)
	if unmuted {
		wetL, wetR = s.revOutL, s.revOutR
	}
	// Main volume sweeps.
	if s.mainSweepOnL {
		s.mainCurL = runSweep(s.mainCurL, &s.mainSweepCntL, s.mainRawL)
	}
	if s.mainSweepOnR {
		s.mainCurR = runSweep(s.mainCurR, &s.mainSweepCntR, s.mainRawR)
	}
	outL := clamp16(mul15(dryL+wetL, s.mainCurL))
	outR := clamp16(mul15(dryR+wetR, s.mainCurR))

	// Capture buffers: CD left at 00000h, CD right
	// at 00400h, voice 1 at 00800h, voice 3 at
	// 00C00h, position from the output sample
	// counter modulo 512 samples.
	pos := uint32(s.totalSamples&0x1FF) * 2
	put := func(base uint32, v int32) {
		u := uint16(int16(clamp16(v)))
		s.RAM[base+pos] = byte(u)
		s.RAM[base+pos+1] = byte(u >> 8)
	}
	put(0x000, int32(cdL))
	put(0x400, int32(cdR))
	put(0x800, capV1)
	put(0xC00, capV3)

	s.audio = append(s.audio, int16(outL), int16(outR))
	s.totalSamples++
}
