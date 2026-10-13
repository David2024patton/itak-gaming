package spu

// The envelope step model follows the psx-spx
// "Envelope Operation depending on
// Shift/Step/Mode/Direction" pseudocode: a step size
// derived from the step field (with the sign flip for
// decreasing or phase negative modes), shifted by
// the shift field, applied when a counter that
// accumulates 8000h shifted by the shift field
// reaches 8000h. Exponential increase slows down
// above level 6000h; exponential decrease scales the
// step by the current level. A rate whose shift and
// step fields are all ones never steps.

type envParams struct {
	shift    int
	stepVal  int
	exp      bool
	decrease bool
	phaseNeg bool
	allOnes  bool // shift/step fields are all ones: never steps
}

// envApply advances one 44.1 kHz envelope tick and
// returns the new level.
func envApply(level int32, counter *int32, p envParams) int32 {
	step := int32(7 - p.stepVal)
	if p.decrease != p.phaseNeg {
		step = ^step // +7..+4 becomes -8..-5
	}
	var inc int32
	if p.shift <= 11 {
		step <<= uint(11 - p.shift)
		inc = 0x8000
	} else {
		inc = int32(0x8000) >> uint(p.shift-11)
	}
	if p.exp && !p.decrease && level > 0x6000 {
		if p.shift < 10 {
			step >>= 2
		} else if p.shift >= 11 {
			inc >>= 2
		} else {
			step >>= 1
			inc >>= 1
		}
	} else if p.exp && p.decrease {
		step = step * level / 0x8000
	}
	if inc == 0 && !p.allOnes {
		inc = 1
	}
	*counter += inc
	if *counter < 0x8000 {
		return level
	}
	*counter -= 0x8000
	level += step
	if !p.decrease {
		if level > 0x7FFF {
			level = 0x7FFF
		}
		if level < -0x8000 {
			level = -0x8000
		}
	} else if p.phaseNeg {
		if level > 0 {
			level = 0
		}
		if level < -0x8000 {
			level = -0x8000
		}
	} else {
		if level < 0 {
			level = 0
		}
	}
	return level
}

// sweepParams extracts the sweep fields from a
// volume register value in sweep mode.
func sweepParams(raw uint16) envParams {
	return envParams{
		shift:    int(raw>>2) & 0x1F,
		stepVal:  int(raw) & 3,
		exp:      raw&0x4000 != 0,
		decrease: raw&0x2000 != 0,
		phaseNeg: raw&0x1000 != 0,
		allOnes:  raw&0x7C == 0x7C && raw&3 == 3,
	}
}

func (v *voice) sustainLevel() int32 {
	lvl := int32((v.adsrLo&0xF)+1)<<11 - 1
	if lvl > 0x7FFF {
		lvl = 0x7FFF
	}
	return lvl
}

// runEnvelope advances the ADSR envelope by one
// output sample.
func (v *voice) runEnvelope() {
	switch v.phase {
	case phaseAttack:
		p := envParams{
			shift:   int(v.adsrLo>>10) & 0x1F,
			stepVal: int(v.adsrLo>>8) & 3,
			exp:     v.adsrLo&0x8000 != 0,
			allOnes: v.adsrLo&0x7C00 == 0x7C00 && (v.adsrLo>>8)&3 == 3,
		}
		v.envLevel = envApply(v.envLevel, &v.envCounter, p)
		if v.envLevel >= 0x7FFF {
			v.envLevel = 0x7FFF
			v.phase = phaseDecay
			v.envCounter = 0
		}
	case phaseDecay:
		p := envParams{
			shift:    int(v.adsrLo>>4) & 0xF,
			stepVal:  0, // fixed step "-8" after the sign flip
			exp:      true,
			decrease: true,
		}
		v.envLevel = envApply(v.envLevel, &v.envCounter, p)
		if v.envLevel <= v.sustainLevel() {
			// Decay decreases until the sustain
			// level: a step that undershoots stops
			// at the level instead of below it.
			v.envLevel = v.sustainLevel()
			v.phase = phaseSustain
			v.envCounter = 0
		}
	case phaseSustain:
		p := envParams{
			shift:    int(v.adsrHi>>8) & 0x1F,
			stepVal:  int(v.adsrHi>>6) & 3,
			exp:      v.adsrHi&0x8000 != 0,
			decrease: v.adsrHi&0x4000 != 0,
			allOnes:  v.adsrHi&0x1F00 == 0x1F00 && (v.adsrHi>>6)&3 == 3,
		}
		v.envLevel = envApply(v.envLevel, &v.envCounter, p)
	case phaseRelease:
		p := envParams{
			shift:    int(v.adsrHi) & 0x1F,
			stepVal:  0,
			exp:      v.adsrHi&0x20 != 0,
			decrease: true,
			allOnes:  v.adsrHi&0x1F == 0x1F,
		}
		v.envLevel = envApply(v.envLevel, &v.envCounter, p)
		if v.envLevel <= 0 {
			v.envLevel = 0
			v.phase = phaseOff
			v.active = false
		}
	}
}

// runSweep advances one volume sweep channel by one
// output sample and returns the new level.
func runSweep(level int32, counter *int32, raw uint16) int32 {
	return envApply(level, counter, sweepParams(raw))
}
