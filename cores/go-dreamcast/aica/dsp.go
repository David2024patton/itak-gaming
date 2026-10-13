package aica

// DSP state beyond the CPU visible register areas.
type dspState struct {
	acc    int32
	memval int32
	frc    int32
	yreg   int32
	adrs   int32
	dec    int32 // TEMP ring decrement counter, 7 bits
	mdec   int32 // memory ring decrement counter
}

func (a *AICA) ringWords() int32 {
	rbl := (a.getRaw16(0x2804) >> 13) & 3
	return int32(8192) << uint(rbl)
}

// unpackWord converts the DSP packed 16 bit float format to a 24 bit
// value, following the published conversion routine for this DSP
// family: the word holds a sign, a 4 bit exponent and an 11 bit
// mantissa.
func unpackWord(w uint16) int32 {
	sign := uint32(w >> 15)
	exp := uint32((w >> 11) & 0xF)
	mant := uint32(w & 0x7FF)
	u := mant << 11
	if exp > 11 {
		exp = 11
	} else {
		u |= (sign ^ 1) << 22
	}
	u |= sign << 23
	return signExtend(u, 24) >> int(exp)
}

// packWord converts a 24 bit value to the packed 16 bit float format:
// count the leading redundant sign bits (up to 12) as the exponent,
// then keep the top 11 bits as the mantissa.
func packWord(v int32) uint16 {
	sign := uint32(v>>23) & 1
	mag := uint32(v) & 0x7FFFFF
	if sign != 0 {
		mag = (^mag) & 0x7FFFFF
	}
	exp := 0
	for exp < 12 {
		if mag&(1<<22) != 0 {
			break
		}
		mag <<= 1
		exp += 1
	}
	vv := v
	if exp < 12 {
		vv = (vv << int(exp)) & 0x3FFFFF
	} else {
		vv <<= 11
	}
	vv >>= 11
	return uint16(uint32(vv&0x7FF) | sign<<15 | uint32(exp)<<11)
}

func (a *AICA) dspReadWord(byteAddr uint32) uint16 {
	lo := uint16(a.ramByte(byteAddr))
	hi := uint16(a.ramByte(byteAddr + 1))
	return lo | hi<<8
}

func (a *AICA) dspWriteWord(byteAddr uint32, w uint16) {
	if len(a.ram) == 0 {
		return
	}
	a.ram[a.ramIndex(byteAddr)] = byte(w)
	a.ram[a.ramIndex(byteAddr+1)] = byte(w >> 8)
}

// runDSP executes the 128 step program for one sample. All 128 steps
// always run; an all zero instruction is harmless internal churn
// (X and B both read TEMP via TRA 0 and nothing is written back
// without TWT), so there is no need to trim trailing zero steps.
//
// At the start the EFREG accumulators clear. MIXS is read during the
// run and cleared at the end, ready for the next sample.
func (a *AICA) runDSP() {
	d := &a.dsp
	for i := 0; i < 16; i += 1 {
		a.efreg[i] = 0
	}
	rbpByte := uint32(a.getRaw16(0x2804)&0xFFF) << 11
	ring := a.ringWords()

	for step := 0; step < 128; step += 1 {
		w := a.mpro[step]
		i0, i1, i2, i3 := w[0], w[1], w[2], w[3]

		tra := (i0 >> 9) & 0x7F
		twt := i0&(1<<8) != 0
		twa := (i0 >> 1) & 0x7F

		xsel := i1&(1<<15) != 0
		ysel := (i1 >> 13) & 3
		ira := (i1 >> 7) & 0x3F
		iwt := i1&(1<<6) != 0
		iwa := (i1 >> 1) & 0x1F

		table := i2&(1<<15) != 0
		mwt := i2&(1<<14) != 0
		mrd := i2&(1<<13) != 0
		ewt := i2&(1<<12) != 0
		ewa := (i2 >> 8) & 0xF
		adrl := i2&(1<<7) != 0
		frcl := i2&(1<<6) != 0
		shift := (i2 >> 4) & 3
		yrl := i2&(1<<3) != 0
		negb := i2&(1<<2) != 0
		zero := i2&(1<<1) != 0
		bsel := i2&1 != 0

		nofl := i3&(1<<15) != 0
		masa := (i3 >> 9) & 0x3F
		adreb := i3&(1<<8) != 0
		nxadr := i3&(1<<7) != 0

		// 1. INPUTS.
		var inputs int32
		switch {
		case ira < 0x20:
			inputs = a.mems[ira]
		case ira < 0x30:
			inputs = wrap24(a.mixs[ira-0x20] << 4)
		case ira < 0x32:
			inputs = 0 // EXTS are silent
		}
		inputs = wrap24(inputs)

		// 2. INPUTS writeback from the memory pipeline.
		if iwt {
			a.mems[iwa] = d.memval
			if ira == iwa {
				inputs = d.memval
			}
		}

		// 3. SHIFTED, from the previous step accumulator.
		var shifted int32
		switch shift {
		case 0:
			shifted = sat24(d.acc)
		case 1:
			shifted = sat24(d.acc * 2)
		case 2:
			shifted = wrap24(d.acc * 2)
		case 3:
			shifted = wrap24(d.acc)
		}

		tempRead := a.temp[(uint32(tra)+uint32(d.dec))&0x7F]

		// 4. B.
		var b int32
		switch {
		case zero:
			b = 0
		case bsel:
			b = d.acc
		default:
			b = tempRead
		}
		if negb {
			b = -b
		}

		// 5. X and Y.
		var x int32
		if xsel {
			x = inputs
		} else {
			x = tempRead
		}
		var y int32
		switch ysel {
		case 0:
			y = d.frc
		case 1:
			y = int32(int16(a.coef[step])) >> 3
		case 2:
			y = (d.yreg >> 11) & 0x1FFF
		case 3:
			y = (d.yreg >> 4) & 0xFFF
		}

		// 6. Multiply and accumulate.
		d.acc = int32((int64(x)*int64(y))>>12 + int64(b))

		// 7. Latch outputs from SHIFTED and INPUTS.
		if twt {
			a.temp[(uint32(twa)+uint32(d.dec))&0x7F] = shifted
		}
		if frcl {
			if shift == 3 {
				d.frc = shifted & 0xFFF
			} else {
				d.frc = (shifted >> 11) & 0x1FFF
			}
		}
		if yrl {
			d.yreg = inputs
		}
		if adrl {
			if shift == 3 {
				d.adrs = (shifted >> 12) & 0xFFF
			} else {
				d.adrs = (inputs >> 16) & 0xFFF
			}
		}

		// 8. Memory, odd steps only.
		if step%2 == 1 && (mrd || mwt) {
			addr := uint32(a.madrs[masa])
			if !table {
				addr += uint32(d.mdec)
			}
			if adreb {
				addr += uint32(d.adrs)
			}
			if nxadr {
				addr += 1
			}
			var byteAddr uint32
			if table {
				addr &= 0xFFFF
				byteAddr = addr * 2
			} else {
				addr &= uint32(ring - 1)
				byteAddr = rbpByte + addr*2
			}
			if mrd {
				word := a.dspReadWord(byteAddr)
				if nofl {
					d.memval = int32(int16(word)) << 8
				} else {
					d.memval = unpackWord(word)
				}
			}
			if mwt {
				var word uint16
				if nofl {
					word = uint16(shifted >> 8)
				} else {
					word = packWord(shifted)
				}
				a.dspWriteWord(byteAddr, word)
			}
		}

		// 9. Effect output accumulate.
		if ewt {
			a.efreg[ewa] += shifted >> 8
		}
	}

	d.dec = (d.dec - 1) & 0x7F
	d.mdec = (d.mdec - 1) % ring
	if d.mdec < 0 {
		d.mdec += ring
	}
	for i := 0; i < 16; i += 1 {
		a.mixs[i] = 0
	}
}
