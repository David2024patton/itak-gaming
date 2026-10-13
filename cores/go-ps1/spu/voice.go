package spu

// keyOn starts a voice: envelope to attack at level
// zero, current address to the start address, decode
// and interpolation state cleared, ENDX cleared.
func (s *SPU) keyOn(n int) {
	v := &s.voices[n]
	v.active = true
	v.phase = phaseAttack
	v.envLevel = 0
	v.envCounter = 0
	v.konDelay = konDelaySamples
	v.addr = uint32(v.ssa) << 3 & (RAMSize - 1)
	v.pitchCounter = 0
	v.decOld, v.decOlder = 0, 0
	v.hist = [4]int32{}
	v.outx = 0
	v.repeatWritten = false
	v.blockLoaded = false
	v.pos = 0
	s.endx &^= 1 << uint(n)
	s.loadBlock(v, n)
}

// keyOff starts the release phase.
func (s *SPU) keyOff(n int) {
	v := &s.voices[n]
	if v.active && v.phase != phaseOff {
		v.phase = phaseRelease
		v.envCounter = 0
		v.konDelay = 0
	}
}

// loadBlock decodes the 16-byte ADPCM block at the
// voice current address into 28 samples. Loop Start
// headers copy the address to the repeat register
// unless software wrote it (the written flag). A
// block read from the IRQ address raises the
// interrupt.
func (s *SPU) loadBlock(v *voice, n int) {
	base := v.addr & (RAMSize - 1)
	hdr := s.RAM[base]
	flags := s.RAM[(base+1)&(RAMSize-1)]
	if flags&4 != 0 && !v.repeatWritten {
		v.lsax = uint16(base >> 3)
	}
	if uint32(s.irqAddr)<<3 == base {
		s.raiseIRQ()
	}
	shift := uint(hdr & 0xF)
	filter := int(hdr>>4) & 7
	var f0, f1 int32
	if filter <= 4 {
		f0, f1 = adpcmFilterF0[filter], adpcmFilterF1[filter]
	}
	old, older := v.decOld, v.decOlder
	for i := 0; i < 28; i++ {
		b := s.RAM[(base+2+uint32(i/2))&(RAMSize-1)]
		var nib int32
		if i&1 == 0 {
			nib = int32(b & 0xF)
		} else {
			nib = int32(b >> 4)
		}
		if nib >= 8 {
			nib -= 16
		}
		sample := (nib<<12)>>shift + (old*f0+older*f1+32)/64
		sample = clamp16(sample)
		v.block[i] = int16(sample)
		older, old = old, sample
	}
	v.decOld, v.decOlder = old, older
	v.blockFlags = flags
	v.blockLoaded = true
	v.pos = 0
}

// nextSample returns the next decoded ADPCM sample,
// moving to the next block (or the repeat address at
// a loop end) when the current block is exhausted.
func (s *SPU) nextSample(v *voice, n int) int32 {
	if !v.blockLoaded {
		s.loadBlock(v, n)
	}
	out := int32(v.block[v.pos])
	v.pos++
	if v.pos >= 28 {
		flags := v.blockFlags
		next := (v.addr + 16) & (RAMSize - 1)
		if flags&1 != 0 {
			s.endx |= 1 << uint(n)
			if flags&2 == 0 {
				// End+Mute: jump, force release,
				// envelope to zero.
				v.envLevel = 0
				v.phase = phaseOff
				v.active = false
			}
			v.addr = uint32(v.lsax) << 3 & (RAMSize - 1)
		} else {
			v.addr = next
		}
		s.loadBlock(v, n)
	}
	return out
}

// voiceSample advances one voice by one output
// sample and returns its post-envelope sample
// (OUTX). prevOutx is the previous voice OUTX of
// this same output cycle, for pitch modulation.
func (s *SPU) voiceSample(n int, prevOutx int32) int32 {
	v := &s.voices[n]
	if !v.active && v.phase == phaseOff {
		v.outx = 0
		return 0
	}
	// Envelope.
	if v.konDelay > 0 {
		v.konDelay--
	} else if v.phase != phaseOff {
		v.runEnvelope()
	}
	// Volume sweeps.
	if v.sweepOnL {
		v.curVolL = runSweep(v.curVolL, &v.sweepCntL, v.volLRaw)
	}
	if v.sweepOnR {
		v.curVolR = runSweep(v.curVolR, &v.sweepCntR, v.volRRaw)
	}
	// Pitch step, with the documented pitch
	// modulation formula and clamp.
	step := int32(v.pitch)
	if n > 0 && s.pmon>>uint(n)&1 != 0 {
		factor := prevOutx + 0x8000 // 0..FFFFh
		step = int32(int16(v.pitch))
		step = (step * factor) >> 15
		step &= 0xFFFF
	}
	if step > 0x3FFF {
		step = 0x4000
	}
	if step < 0 {
		step = 0
	}
	v.pitchCounter += uint32(step)
	for v.pitchCounter >= 0x1000 {
		v.pitchCounter -= 0x1000
		v.hist[0] = v.hist[1]
		v.hist[1] = v.hist[2]
		v.hist[2] = v.hist[3]
		v.hist[3] = s.nextSample(v, n)
	}
	var raw int32
	if s.non>>uint(n)&1 != 0 {
		raw = int32(int16(s.noiseLevel))
	} else {
		i := int((v.pitchCounter >> 4) & 0xFF)
		raw = (gaussTable[0xFF-i]*v.hist[0])>>15 +
			(gaussTable[0x1FF-i]*v.hist[1])>>15 +
			(gaussTable[0x100+i]*v.hist[2])>>15 +
			(gaussTable[i]*v.hist[3])>>15
		raw = clamp16(raw)
	}
	v.outx = (raw * v.envLevel) >> 15
	return v.outx
}

// runNoise advances the shared noise generator by
// one output sample, per the documented algorithm:
// the timer decrements by the step (4..7), the
// parity bit is the XOR of level bits 15, 12, 11 and
// 10 plus 1, and when the timer goes negative the
// level shifts and the timer reloads (twice if
// needed) with 20000h shifted by the noise shift.
func (s *SPU) runNoise() {
	shift := uint(s.spucnt>>10) & 0xF
	step := int32(s.spucnt>>8)&3 + 4
	s.noiseTimer -= step
	parity := ((s.noiseLevel >> 15) & 1) ^ ((s.noiseLevel >> 12) & 1) ^
		((s.noiseLevel >> 11) & 1) ^ ((s.noiseLevel >> 10) & 1) ^ 1
	if s.noiseTimer < 0 {
		s.noiseLevel = (s.noiseLevel << 1) | parity
		s.noiseTimer += int32(0x20000) >> shift
		if s.noiseTimer < 0 {
			s.noiseTimer += int32(0x20000) >> shift
		}
	}
	s.noiseLevel = int32(int16(s.noiseLevel))
}

// raiseIRQ latches the interrupt flag and calls
// OnIRQ on the 0-to-1 transition, when the SPU is
// enabled and the IRQ is enabled in SPUCNT.
func (s *SPU) raiseIRQ() {
	if s.spucnt&0x8000 == 0 || s.spucnt&0x40 == 0 {
		return
	}
	if s.irqFlag {
		return
	}
	s.irqFlag = true
	if s.OnIRQ != nil {
		s.OnIRQ()
	}
}
