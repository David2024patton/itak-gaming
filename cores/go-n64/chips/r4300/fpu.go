package r4300

import (
	"math"
)

// FPU implementation for the VR4300's CP1. The register file
// follows the standard emulator storage model: FPR[i] holds
// 64 bits; with FR=0 a double lives in the low words of an
// even/odd pair, with FR=1 a double lives in one register.
//
// Single-precision arithmetic is computed in float64 and
// rounded to single with the FCSR rounding mode applied, so
// all four modes are honored for .s operations and for
// every conversion. Double arithmetic uses the host's
// round-to-nearest float64 result; the other modes are
// honored for conversions (see README).

// FCR0Value is the FPU implementation/revision identifier
// reported by CFC1 from register 0 (informational).
const FCR0Value = 0x00000B00

// FCSR field positions.
const (
	fcsrFlagI = 1 << 2
	fcsrFlagU = 1 << 3
	fcsrFlagO = 1 << 4
	fcsrFlagZ = 1 << 5
	fcsrFlagV = 1 << 6
)

func (c *CPU) fr() bool { return c.status()&StatusFR != 0 }

// getSRaw returns the raw 32 bits of single register i.
func (c *CPU) getSRaw(i uint32) uint32 { return uint32(c.FPR[i&31]) }

// setSRaw stores raw single bits into register i.
func (c *CPU) setSRaw(i uint32, v uint32) {
	i &= 31
	if c.fr() {
		c.FPR[i] = uint64(v)
	} else {
		c.FPR[i] = (c.FPR[i] & 0xFFFFFFFF00000000) | uint64(v)
	}
}

// getDRaw returns the raw 64 bits of double register i under
// the current FR mapping.
func (c *CPU) getDRaw(i uint32) uint64 {
	i &= 31
	if c.fr() {
		return c.FPR[i]
	}
	even := i &^ 1
	return uint64(uint32(c.FPR[even])) | uint64(uint32(c.FPR[even+1]))<<32
}

// setDRaw stores raw double bits into register i.
func (c *CPU) setDRaw(i uint32, v uint64) {
	i &= 31
	if c.fr() {
		c.FPR[i] = v
		return
	}
	even := i &^ 1
	c.FPR[even] = (c.FPR[even] & 0xFFFFFFFF00000000) | uint64(uint32(v))
	c.FPR[even+1] = (c.FPR[even+1] & 0xFFFFFFFF00000000) | uint64(uint32(v>>32))
}

func (c *CPU) getS(i uint32) float32 { return math.Float32frombits(c.getSRaw(i)) }
func (c *CPU) setS(i uint32, f float32) {
	c.setSRaw(i, math.Float32bits(f))
}
func (c *CPU) getD(i uint32) float64 { return math.Float64frombits(c.getDRaw(i)) }
func (c *CPU) setD(i uint32, f float64) {
	c.setDRaw(i, math.Float64bits(f))
}

// roundingMode returns the FCSR RM field (0 RN, 1 RZ, 2 RP,
// 3 RM).
func (c *CPU) roundingMode() uint32 { return c.FCSR & 3 }

// f32Up/f32Down step a float32 one ulp toward +inf/-inf.
func f32Up(f float32) float32 {
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 1) {
		return f
	}
	b := math.Float32bits(f)
	if f >= 0 {
		b++
	} else {
		b--
	}
	return math.Float32frombits(b)
}

func f32Down(f float32) float32 {
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), -1) {
		return f
	}
	b := math.Float32bits(f)
	if f >= 0 {
		b--
	} else {
		b++
	}
	return math.Float32frombits(b)
}

// roundS rounds an exact float64 result to float32 under
// the current FCSR rounding mode.
func (c *CPU) roundS(y float64) float32 {
	f := float32(y)
	if math.IsNaN(y) || math.IsInf(y, 0) {
		return f
	}
	switch c.roundingMode() {
	case 1: // RZ: truncate magnitude
		if math.Abs(float64(f)) > math.Abs(y) {
			if y > 0 {
				f = f32Down(f)
			} else {
				f = f32Up(f)
			}
		}
	case 2: // RP: toward +inf
		if float64(f) < y {
			f = f32Up(f)
		}
	case 3: // RM: toward -inf
		if float64(f) > y {
			f = f32Down(f)
		}
	}
	return f
}

// flagFP records FPU exception flags in FCSR and returns
// true if the matching enable is set (caller then raises
// the FPE exception).
func (c *CPU) flagFP(flag uint32) bool {
	c.FCSR |= flag         // flags field
	causeBit := flag << 10 // cause sits 10 bits above flags
	c.FCSR |= causeBit     // cause field
	enableBit := flag << 5 // enables sit 5 bits above flags
	return c.FCSR&enableBit != 0
}

// composeFCSR builds the readable FCR31 value with the
// condition bits folded in (FCC0 at bit 23, FCC1-7 at 25-31,
// FS at bit 24 always 0 in this core).
func (c *CPU) composeFCSR() uint32 {
	v := c.FCSR & 0x007CFFFF // keep RM/flags/enables/cause + bit 18 area clean
	v &^= 0xFE800000
	for i, b := range c.Cond {
		if !b {
			continue
		}
		if i == 0 {
			v |= 1 << 23
		} else {
			v |= 1 << uint(24+i)
		}
	}
	return v
}

// setFCSR stores an FCR31 write, splitting out the
// condition bits.
func (c *CPU) setFCSR(v uint32) {
	c.Cond[0] = v&(1<<23) != 0
	for i := 1; i < 8; i++ {
		c.Cond[i] = v&(1<<uint(24+i)) != 0
	}
	c.FCSR = v &^ 0xFE800000
}

// cvtToInt converts a float64 to an integer of the given
// width with an explicit rounding mode, clamping on
// overflow per the MIPS result for invalid conversions.
func cvtToInt(f float64, mode uint32, wide bool) (int64, bool) {
	var r float64
	switch mode {
	case 0:
		r = math.RoundToEven(f)
	case 1:
		r = math.Trunc(f)
	case 2:
		r = math.Ceil(f)
	case 3:
		r = math.Floor(f)
	}
	bad := math.IsNaN(f)
	if wide {
		if r >= 9223372036854775808.0 || bad {
			return 1<<63 - 1, true
		}
		if r < -9223372036854775808.0 {
			return -1 << 63, true
		}
		return int64(r), bad
	}
	if r > 2147483647.0 || bad {
		return 2147483647, true
	}
	if r < -2147483648.0 {
		return -2147483648, true
	}
	return int64(int32(r)), bad
}

// execCOP1 handles the COP1 opcode group.
func (c *CPU) execCOP1(instr uint32, pc uint64, inDelay bool) int {
	if !c.cp1Usable() {
		c.raiseException(exc{code: ExcCpU, ce: 1}, pc, inDelay)
		return 1
	}
	rs := (instr >> 21) & 31 // format / function selector
	rt := (instr >> 16) & 31
	fs := (instr >> 11) & 31
	fd := (instr >> 6) & 31
	funct := instr & 63
	raise := func(code int) int {
		c.raiseException(exc{code: code}, pc, inDelay)
		return 1
	}
	const cycFP = 4
	const cycDiv = 23

	switch rs {
	case 0: // MFC1
		c.Reg[rt] = signExtend32(c.getSRaw(fs))
		return 1
	case 1: // DMFC1
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.Reg[rt] = c.getDRaw(fs)
		return 1
	case 2: // CFC1
		if fs == 0 {
			c.Reg[rt] = signExtend32(FCR0Value)
		} else if fs == 31 {
			c.Reg[rt] = signExtend32(c.composeFCSR())
		} else {
			c.Reg[rt] = 0
		}
		return 1
	case 4: // MTC1
		c.setSRaw(fs, uint32(c.Reg[rt]))
		return 1
	case 5: // DMTC1
		if !c.allow64Ops() {
			return raise(ExcReserved)
		}
		c.setDRaw(fs, c.Reg[rt])
		return 1
	case 6: // CTC1
		if fs == 31 {
			c.setFCSR(uint32(c.Reg[rt]))
		}
		return 1
	case 8: // BC1
		cc := (instr >> 18) & 7
		nd := instr&(1<<17) != 0
		tf := instr&(1<<16) != 0
		cond := c.Cond[cc] == tf
		off := uint64(int64(int16(instr&0xFFFF))) << 2
		if cond {
			c.NPC = pc + 4 + off
			c.delaySlot = true
		} else if nd {
			c.nullify = true
		} else {
			c.delaySlot = true
		}
		return 1
	}

	// Arithmetic: rs is the format.
	switch rs {
	case 16: // single
		a := c.getS(fs)
		b := c.getS(rt)
		switch funct {
		case 0: // ADD.S
			c.setS(fd, c.roundS(float64(a)+float64(b)))
		case 1: // SUB.S
			c.setS(fd, c.roundS(float64(a)-float64(b)))
		case 2: // MUL.S
			c.setS(fd, c.roundS(float64(a)*float64(b)))
			return cycFP + 1
		case 3: // DIV.S
			if b == 0 && a != 0 && !math.IsNaN(float64(a)) {
				c.flagFP(fcsrFlagZ)
			}
			c.setS(fd, c.roundS(float64(a)/float64(b)))
			return cycDiv
		case 4: // SQRT.S
			if a < 0 {
				c.flagFP(fcsrFlagV)
			}
			c.setS(fd, c.roundS(math.Sqrt(float64(a))))
			return cycDiv
		case 5: // ABS.S
			c.setS(fd, c.roundS(math.Abs(float64(a))))
		case 6: // MOV.S
			c.setS(fd, a)
		case 7: // NEG.S
			c.setS(fd, -a)
		case 8: // ROUND.L.S
			v, bad := cvtToInt(float64(a), 0, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 9: // TRUNC.L.S
			v, bad := cvtToInt(float64(a), 1, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 10: // CEIL.L.S
			v, bad := cvtToInt(float64(a), 2, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 11: // FLOOR.L.S
			v, bad := cvtToInt(float64(a), 3, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 12: // ROUND.W.S
			v, bad := cvtToInt(float64(a), 0, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 13: // TRUNC.W.S
			v, bad := cvtToInt(float64(a), 1, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 14: // CEIL.W.S
			v, bad := cvtToInt(float64(a), 2, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 15: // FLOOR.W.S
			v, bad := cvtToInt(float64(a), 3, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 32: // CVT.S.S (identity)
			c.setS(fd, a)
		case 33: // CVT.D.S
			c.setD(fd, float64(a))
		case 36: // CVT.W.S
			v, bad := cvtToInt(float64(a), c.roundingMode(), false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 37: // CVT.L.S
			v, bad := cvtToInt(float64(a), c.roundingMode(), true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		default:
			if funct >= 48 { // C.cond.S
				c.compareFP(funct&15, float64(a), float64(b), instr)
				return 1
			}
			return raise(ExcReserved)
		}
		return cycFP
	case 17: // double
		a := c.getD(fs)
		b := c.getD(rt)
		switch funct {
		case 0: // ADD.D
			c.setD(fd, a+b)
		case 1: // SUB.D
			c.setD(fd, a-b)
		case 2: // MUL.D
			c.setD(fd, a*b)
			return cycFP + 1
		case 3: // DIV.D
			if b == 0 && a != 0 && !math.IsNaN(a) {
				c.flagFP(fcsrFlagZ)
			}
			c.setD(fd, a/b)
			return cycDiv
		case 4: // SQRT.D
			if a < 0 {
				c.flagFP(fcsrFlagV)
			}
			c.setD(fd, math.Sqrt(a))
			return cycDiv
		case 5: // ABS.D
			c.setD(fd, math.Abs(a))
		case 6: // MOV.D
			c.setD(fd, a)
		case 7: // NEG.D
			c.setD(fd, -a)
		case 8: // ROUND.L.D
			v, bad := cvtToInt(a, 0, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 9: // TRUNC.L.D
			v, bad := cvtToInt(a, 1, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 10: // CEIL.L.D
			v, bad := cvtToInt(a, 2, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 11: // FLOOR.L.D
			v, bad := cvtToInt(a, 3, true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		case 12: // ROUND.W.D
			v, bad := cvtToInt(a, 0, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 13: // TRUNC.W.D
			v, bad := cvtToInt(a, 1, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 14: // CEIL.W.D
			v, bad := cvtToInt(a, 2, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 15: // FLOOR.W.D
			v, bad := cvtToInt(a, 3, false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 32: // CVT.S.D
			c.setS(fd, c.roundS(a))
		case 33: // CVT.D.D (identity)
			c.setD(fd, a)
		case 36: // CVT.W.D
			v, bad := cvtToInt(a, c.roundingMode(), false)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setSRaw(fd, uint32(v))
		case 37: // CVT.L.D
			v, bad := cvtToInt(a, c.roundingMode(), true)
			if bad {
				c.flagFP(fcsrFlagV)
			}
			c.setDRaw(fd, uint64(v))
		default:
			if funct >= 48 { // C.cond.D
				c.compareFP(funct&15, a, b, instr)
				return 1
			}
			return raise(ExcReserved)
		}
		return cycFP
	case 20: // word (integer bit patterns)
		v := int32(c.getSRaw(fs))
		switch funct {
		case 32: // CVT.S.W
			c.setS(fd, c.roundS(float64(v)))
		case 33: // CVT.D.W
			c.setD(fd, float64(v))
		default:
			return raise(ExcReserved)
		}
		return cycFP
	case 21: // long
		v := int64(c.getDRaw(fs))
		switch funct {
		case 32: // CVT.S.L
			c.setS(fd, c.roundS(float64(v)))
		case 33: // CVT.D.L
			c.setD(fd, float64(v))
		default:
			return raise(ExcReserved)
		}
		return cycFP
	}
	return raise(ExcReserved)
}

// compareFP evaluates a C.cond condition into Cond[cc].
func (c *CPU) compareFP(cond uint32, a, b float64, instr uint32) {
	cc := (instr >> 8) & 7
	unordered := math.IsNaN(a) || math.IsNaN(b)
	less := a < b
	equal := a == b
	var r bool
	switch cond {
	case 0: // F
		r = false
	case 1: // UN
		r = unordered
	case 2: // EQ
		r = equal
	case 3: // UEQ
		r = unordered || equal
	case 4: // OLT
		r = less
	case 5: // ULT
		r = unordered || less
	case 6: // OLE
		r = less || equal
	case 7: // ULE
		r = unordered || less || equal
	case 8: // SF
		r = false
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	case 9: // NGLE
		r = unordered
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	case 10: // SEQ
		r = equal
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	case 11: // NGL: not greater or less = unordered or equal
		r = unordered || equal
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	case 12: // LT
		r = less
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	case 13: // NGE: not greater or equal = unordered or less
		r = unordered || less
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	case 14: // LE
		r = less || equal
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	case 15: // NGT
		r = unordered || less || equal
		if unordered {
			c.flagFP(fcsrFlagV)
		}
	}
	c.Cond[cc] = r
}
