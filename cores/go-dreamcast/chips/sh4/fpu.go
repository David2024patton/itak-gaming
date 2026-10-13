package sh4

import "math"

// FPU execution for the 1111 opcode family.
//
// FPSCR.PR selects the precision of the arithmetic instructions:
// PR = 0 operates on the FR singles, PR = 1 on the DR register pairs
// (even numbered pairs of the active bank). FPSCR.SZ selects the
// FMOV transfer size: SZ = 0 moves 32-bit singles, SZ = 1 moves
// 64-bit pairs, whose destination or source is a DR pair of the
// active bank or an XD pair of the inactive bank, selected by the
// low bit of the register field.
//
// Executing any FPU instruction while SR.FD is set raises the FPU
// disabled exception (0x800). Arithmetic follows Go float32 and
// float64 semantics. FIPR and FTRV accumulate in float32, one product
// at a time, in index order.

func f32(bits uint32) float32  { return math.Float32frombits(bits) }
func f32bits(f float32) uint32 { return math.Float32bits(f) }
func f64(bits uint64) float64  { return math.Float64frombits(bits) }

// drBits reads the 64-bit pattern of the DR pair starting at even
// register pair of the given bank (low word first).
func drBits(bank *[16]uint32, pair int) uint64 {
	return uint64(bank[pair&0xE]) | uint64(bank[pair&0xE+1])<<32
}

func setDRBits(bank *[16]uint32, pair int, v uint64) {
	bank[pair&0xE] = uint32(v)
	bank[pair&0xE+1] = uint32(v >> 32)
}

func (c *CPU) getDR(pair int) float64 { return f64(drBits(&c.FR, pair)) }
func (c *CPU) setDR(pair int, v float64) {
	setDRBits(&c.FR, pair, math.Float64bits(v))
}

func (c *CPU) execFPU(op uint16, pc uint32, inDelay bool) {
	if c.SR&srFD != 0 {
		c.enterException(excFPUDisabled, pc)
		return
	}
	// FRCHG and FSCHG are exact opcodes whose bit patterns would
	// otherwise alias FTRV, so they are matched first.
	switch op {
	case 0xFBFD: // FRCHG
		c.setFPSCR(c.FPSCR ^ fpscrFR)
		return
	case 0xF3FD: // FSCHG
		c.setFPSCR(c.FPSCR ^ fpscrSZ)
		return
	}

	n := int(op >> 8 & 0xF)
	m := int(op >> 4 & 0xF)
	low := op & 0xF
	double := c.FPSCR&fpscrPR != 0
	sz := c.FPSCR&fpscrSZ != 0

	switch low {
	case 0x0, 0x1, 0x2, 0x3: // FADD, FSUB, FMUL, FDIV
		if double {
			a, b := c.getDR(n), c.getDR(m)
			var r float64
			switch low {
			case 0x0:
				r = a + b
			case 0x1:
				r = a - b
			case 0x2:
				r = a * b
			case 0x3:
				r = a / b
			}
			c.setDR(n, r)
		} else {
			a, b := f32(c.FR[n]), f32(c.FR[m])
			var r float32
			switch low {
			case 0x0:
				r = a + b
			case 0x1:
				r = a - b
			case 0x2:
				r = a * b
			case 0x3:
				r = a / b
			}
			c.FR[n] = f32bits(r)
		}
	case 0x4, 0x5: // FCMP/EQ, FCMP/GT
		var r bool
		if double {
			a, b := c.getDR(n), c.getDR(m)
			if low == 0x4 {
				r = a == b
			} else {
				r = a > b
			}
		} else {
			a, b := f32(c.FR[n]), f32(c.FR[m])
			if low == 0x4 {
				r = a == b
			} else {
				r = a > b
			}
		}
		c.setT(boolU32(r))
	case 0x6, 0x8, 0x9: // FMOV load forms: @(R0,Rm), @Rm, @Rm+
		var addr uint32
		switch low {
		case 0x6:
			addr = c.R[0] + c.R[m]
		case 0x8, 0x9:
			addr = c.R[m]
		}
		if !c.fmovLoad(pc, addr, n, sz) {
			return
		}
		if low == 0x9 {
			if sz {
				c.R[m] += 8
			} else {
				c.R[m] += 4
			}
		}
	case 0x7, 0xA, 0xB: // FMOV store forms: @(R0,Rn), @Rn, @-Rn
		var addr uint32
		size := uint32(4)
		if sz {
			size = 8
		}
		switch low {
		case 0x7:
			addr = c.R[0] + c.R[n]
		case 0xA:
			addr = c.R[n]
		case 0xB:
			addr = c.R[n] - size
			c.R[n] = addr
		}
		c.fmovStore(pc, addr, m, sz)
	case 0xC: // FMOV register to register
		if !sz {
			c.FR[n] = c.FR[m]
		} else {
			var v uint64
			if m&1 != 0 {
				v = drBits(&c.XFR, m)
			} else {
				v = drBits(&c.FR, m)
			}
			if n&1 != 0 {
				setDRBits(&c.XFR, n, v)
			} else {
				setDRBits(&c.FR, n, v)
			}
		}
	case 0xE: // FMAC FR0,FRm,FRn (single precision only)
		if double {
			c.illegal(inDelay, pc)
			return
		}
		c.FR[n] = f32bits(f32(c.FR[0])*f32(c.FR[m]) + f32(c.FR[n]))
	case 0xD:
		c.execFPUSpecial(op, pc, inDelay, n, double)
	default:
		c.illegal(inDelay, pc)
	}
}

// fmovLoad loads one FMOV operand from addr into the destination
// selected by the register field n and the SZ bit.
func (c *CPU) fmovLoad(pc uint32, addr uint32, n int, sz bool) bool {
	if !sz {
		v, ok := c.read32(addr)
		if !ok {
			c.enterMemFault(pc)
			return false
		}
		c.FR[n] = v
		return true
	}
	v, ok := c.read64(addr)
	if !ok {
		c.enterMemFault(pc)
		return false
	}
	if n&1 != 0 {
		setDRBits(&c.XFR, n, v)
	} else {
		setDRBits(&c.FR, n, v)
	}
	return true
}

// fmovStore stores one FMOV operand from the source selected by the
// register field m and the SZ bit to addr.
func (c *CPU) fmovStore(pc uint32, addr uint32, m int, sz bool) {
	if !sz {
		if !c.write32(addr, c.FR[m]) {
			c.enterMemFault(pc)
		}
		return
	}
	var v uint64
	if m&1 != 0 {
		v = drBits(&c.XFR, m)
	} else {
		v = drBits(&c.FR, m)
	}
	if !c.write64(addr, v) {
		c.enterMemFault(pc)
	}
}

// execFPUSpecial handles the 1101 low-nibble family, selected by
// bits 7..4: moves to and from FPUL, conversions, unary operations,
// the conversion pair FCNVSD and FCNVDS, and the graphics operations
// FIPR and FTRV.
func (c *CPU) execFPUSpecial(op uint16, pc uint32, inDelay bool, n int, double bool) {
	sub := int(op >> 4 & 0xF)
	switch sub {
	case 0x0: // FSTS FPUL,FRn
		c.FR[n] = c.FPUL
	case 0x1: // FLDS FRm,FPUL (register field in bits 11..8)
		c.FPUL = c.FR[n]
	case 0x2: // FLOAT FPUL,FRn or FPUL,DRn
		v := float64(int32(c.FPUL))
		if double {
			c.setDR(n, v)
		} else {
			c.FR[n] = f32bits(float32(v))
		}
	case 0x3: // FTRC FRm,FPUL or DRm,FPUL
		var v float64
		if double {
			v = c.getDR(n)
		} else {
			v = float64(f32(c.FR[n]))
		}
		c.FPUL = ftrc(v)
	case 0x4: // FNEG
		if double {
			c.FR[(n&0xE)+1] ^= 0x80000000
		} else {
			c.FR[n] ^= 0x80000000
		}
	case 0x5: // FABS
		if double {
			c.FR[(n&0xE)+1] &= 0x7FFFFFFF
		} else {
			c.FR[n] &= 0x7FFFFFFF
		}
	case 0x6: // FSQRT
		if double {
			c.setDR(n, math.Sqrt(c.getDR(n)))
		} else {
			c.FR[n] = f32bits(float32(math.Sqrt(float64(f32(c.FR[n])))))
		}
	case 0x7: // FSRRA FRn: reciprocal square root approximation,
		// computed exactly here as float32(1/sqrt(x)) via float64
		if double {
			c.illegal(inDelay, pc)
			return
		}
		x := float64(f32(c.FR[n]))
		c.FR[n] = f32bits(float32(1 / math.Sqrt(x)))
	case 0x8: // FLDI0 FRn
		c.FR[n] = 0
	case 0x9: // FLDI1 FRn
		c.FR[n] = 0x3F800000
	case 0xA: // FCNVSD FPUL,DRn
		c.setDR(n, float64(f32(c.FPUL)))
	case 0xB: // FCNVDS DRm,FPUL
		c.FPUL = f32bits(float32(c.getDR(n)))
	case 0xE: // FIPR FVm,FVn
		nn := int(op >> 10 & 3)
		mm := int(op >> 8 & 3)
		var a, b [4]float32
		for i := 0; i < 4; i++ {
			a[i] = f32(c.FR[mm*4+i])
			b[i] = f32(c.FR[nn*4+i])
		}
		var dot float32
		for i := 0; i < 4; i++ {
			dot += a[i] * b[i]
		}
		c.FR[nn*4+3] = f32bits(dot)
	case 0xF: // FTRV XMTRX,FVn
		if int(op>>8&3) != 1 {
			c.illegal(inDelay, pc)
			return
		}
		nn := int(op >> 10 & 3)
		var in [4]float32
		for i := 0; i < 4; i++ {
			in[i] = f32(c.FR[nn*4+i])
		}
		// The transform matrix is the inactive bank read as
		// XMTRX in row-major order: element (row, col) is
		// XFR[row*4+col], and out[row] = sum over col of
		// matrix[row][col] * in[col], accumulated in float32.
		for row := 0; row < 4; row++ {
			var acc float32
			for col := 0; col < 4; col++ {
				acc += f32(c.XFR[row*4+col]) * in[col]
			}
			c.FR[nn*4+row] = f32bits(acc)
		}
	default:
		c.illegal(inDelay, pc)
	}
}

// ftrc converts a floating-point value to a signed 32-bit integer
// with truncation toward zero, saturating to the int32 range. NaN
// converts to the negative limit, matching the hardware behavior
// for invalid inputs.
func ftrc(v float64) uint32 {
	if math.IsNaN(v) || v < -2147483648.0 {
		return 0x80000000
	}
	if v >= 2147483648.0 {
		return 0x7FFFFFFF
	}
	return uint32(int32(v))
}
