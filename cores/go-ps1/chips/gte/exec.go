package gte

import "fmt"

// This file implements the GTE command math from the
// psx-spx "Geometry Transformation Engine (GTE)"
// chapter. Section names in comments refer to that
// chapter.
//
// Command word fields (psx-psx, "GTE Command
// Encoding"): the real opcode is bits 0..5, the sf
// (shift fraction) bit is bit 19, the lm (IR saturation
// mode) bit is bit 10, and the MVMVA selects are mx in
// bits 17..18, v in bits 15..16 and cv in bits 13..14.
// Bits 20..24 hold a fake opcode number that the
// hardware ignores.
//
// Accumulator model (psx-spx, "GTE Saturation"):
// MAC1..MAC3 are 44-bit signed accumulators and are
// not saturated; a result outside the 44-bit signed
// range sets the per-MAC positive or negative overflow
// bits in FLAG (bits 30..28 and 27..25) and wraps.
// MAC0 is a 32-bit accumulator with its own overflow
// bits (16 and 15). IR1..IR3 are saturated from the
// MACs, to -8000h..+7FFFh with lm=0 or 0..+7FFFh with
// lm=1, setting FLAG bits 24..22.

// FLAG bit numbers (psx-spx, "GTE Saturation").
const (
	flagMAC1Pos = 30
	flagMAC2Pos = 29
	flagMAC3Pos = 28
	flagMAC1Neg = 27
	flagMAC2Neg = 26
	flagMAC3Neg = 25
	flagIR1Sat  = 24
	flagIR2Sat  = 23
	flagIR3Sat  = 22
	flagColR    = 21
	flagColG    = 20
	flagColB    = 19
	flagZSat    = 18
	flagDivOvf  = 17
	flagMAC0Pos = 16
	flagMAC0Neg = 15
	flagSXSat   = 14
	flagSYSat   = 13
	flagIR0Sat  = 12
)

const (
	mac44Max = int64(0x7FFFFFFFFFF)  // 2^43 - 1
	mac44Min = -int64(0x80000000000) // -2^43
)

// unrTable is the reciprocal table of the psx-spx
// "GTE Division Inaccuracy" section, generated with
// the rule given there: unr_table[i] =
// max(0, (40000h/(i+100h)+1)/2-101h) for i in
// 000h..100h, with the division carried out before
// truncation (the entry for i=001h is FDh, which pins
// down that rounding).
var unrTable = func() [0x101]int64 {
	var t [0x101]int64
	for i := 0; i <= 0x100; i++ {
		d := int64(i + 0x100)
		v := (0x40000+d)/(2*d) - 0x101
		if v < 0 {
			v = 0
		}
		t[i] = v
	}
	return t
}()

// engine carries the per-command execution state: the
// FLAG bits accumulated by the running command and the
// wide accumulator values (the register file itself
// only stores their low 32 bits).
type engine struct {
	g    *GTE
	flag uint32
	sf   bool
	lm   bool
	mac  [3]int64 // 44-bit signed, wrapped
	mac0 int64    // 32-bit signed, wrapped
}

func (e *engine) sfShift() uint {
	if e.sf {
		return 12
	}
	return 0
}

func (e *engine) lmMin() int64 {
	if e.lm {
		return 0
	}
	return -0x8000
}

func s16(v uint32) int64 { return int64(int16(uint16(v))) }

func s32(v uint32) int64 { return int64(int32(v)) }

// setMAC stores v in a 44-bit MAC accumulator, setting
// the documented overflow bits and wrapping the value
// to 44 bits (psx-spx, "GTE Saturation").
func (e *engine) setMAC(i int, v int64) {
	if v > mac44Max {
		e.flag |= 1 << uint(flagMAC1Pos-i)
	} else if v < mac44Min {
		e.flag |= 1 << uint(flagMAC1Neg-i)
	}
	v &= 0xFFFFFFFFFFF
	if v > mac44Max {
		v -= 0x100000000000
	}
	e.mac[i] = v
}

// checkMACOverflow sets the overflow bits for v without
// storing it (used by the bugged MVMVA far-color path,
// whose FLAG bits behave as if the full operation had
// been executed).
func (e *engine) checkMACOverflow(i int, v int64) {
	if v > mac44Max {
		e.flag |= 1 << uint(flagMAC1Pos-i)
	} else if v < mac44Min {
		e.flag |= 1 << uint(flagMAC1Neg-i)
	}
}

// setMAC0 stores v in the 32-bit MAC0 accumulator,
// setting FLAG bits 16 (positive) and 15 (negative)
// on overflow (psx-spx, "GTE Saturation").
func (e *engine) setMAC0(v int64) {
	if v > 0x7FFFFFFF {
		e.flag |= 1 << flagMAC0Pos
	} else if v < -0x80000000 {
		e.flag |= 1 << flagMAC0Neg
	}
	e.mac0 = int64(int32(v))
}

// satIRValue clamps v to the IR range selected by lm,
// setting the given FLAG bit if clamping occurred.
func (e *engine) satIRValue(v int64, lm bool, bit uint) int64 {
	min := int64(-0x8000)
	if lm {
		min = 0
	}
	if v > 0x7FFF {
		e.flag |= 1 << bit
		return 0x7FFF
	}
	if v < min {
		e.flag |= 1 << bit
		return min
	}
	return v
}

// storeIR writes an already saturated value to an IR
// register (sign-extended 16-bit storage).
func (e *engine) storeIR(i int, v int64) {
	e.g.data[9+i] = uint32(int32(int16(v)))
}

// satIR saturates a MAC value into IR register i using
// the command's lm bit.
func (e *engine) satIR(i int) {
	e.storeIR(i, e.satIRValue(e.mac[i], e.lm, uint(flagIR1Sat-i)))
}

// irFromMAC copies all three MACs into IR1..IR3 with
// saturation.
func (e *engine) irFromMAC() {
	for i := 0; i < 3; i++ {
		e.satIR(i)
	}
}

// ir reads IR register i (0..2) as a signed value.
func (e *engine) ir(i int) int64 { return s16(e.g.data[9+i]) }

// ir0 reads IR0 as a signed value.
func (e *engine) ir0() int64 { return s16(e.g.data[8]) }

func (e *engine) irVec() [3]int64 { return [3]int64{e.ir(0), e.ir(1), e.ir(2)} }

// vec reads input vector n (0..2, the V0..V2
// registers) as signed components.
func (e *engine) vec(n int) [3]int64 {
	d := e.g.data
	return [3]int64{
		s16(d[2*n]),
		s16(d[2*n] >> 16),
		s16(d[2*n+1]),
	}
}

// matrix reads one of the packed 3x3 control-register
// matrices. base is the first control register: 0 for
// the rotation matrix, 8 for the light matrix, 16 for
// the light color matrix (psx-spx, "Matrix Registers":
// the first four registers pack two elements each and
// the fifth holds the last element in its low half).
func (e *engine) matrix(base int) [3][3]int64 {
	c := e.g.control
	return [3][3]int64{
		{s16(c[base]), s16(c[base] >> 16), s16(c[base+1])},
		{s16(c[base+1] >> 16), s16(c[base+2]), s16(c[base+2] >> 16)},
		{s16(c[base+3]), s16(c[base+3] >> 16), s16(c[base+4])},
	}
}

func (e *engine) trVec() [3]int64 {
	c := e.g.control
	return [3]int64{s32(c[5]), s32(c[6]), s32(c[7])}
}

func (e *engine) bkVec() [3]int64 {
	c := e.g.control
	return [3]int64{s32(c[13]), s32(c[14]), s32(c[15])}
}

func (e *engine) fcVec() [3]int64 {
	c := e.g.control
	return [3]int64{s32(c[21]), s32(c[22]), s32(c[23])}
}

// matMul computes MAC = (trans*1000h + mat*vec) SAR
// (sf*12), or MAC = (mat*vec) SAR (sf*12) when trans
// is nil. This is the shared multiply step of MVMVA,
// RTPS/RTPT and the color commands (psx-spx, "Details
// on (LLM*V0) SAR (sf*12) and (BK*1000h + LCM*IR) SAR
// (sf*12)").
func (e *engine) matMul(mat [3][3]int64, vec [3]int64, trans *[3]int64) {
	sh := e.sfShift()
	for i := 0; i < 3; i++ {
		sum := mat[i][0]*vec[0] + mat[i][1]*vec[1] + mat[i][2]*vec[2]
		if trans != nil {
			sum += trans[i] << 12
		}
		e.setMAC(i, sum>>sh)
	}
}

func (e *engine) rgbc() (r, g, b, code int64) {
	v := e.g.data[6]
	return int64(v & 0xFF), int64((v >> 8) & 0xFF),
		int64((v >> 16) & 0xFF), int64((v >> 24) & 0xFF)
}

// pushColor writes the MACs to the top of the color
// FIFO: each component divided by 16 and saturated to
// 00h..FFh (FLAG bits 21..19 on saturation), with the
// CODE byte taken from RGBC, and the older FIFO
// entries moved one stage down (psx-spx, "Notes" in
// the color command section).
func (e *engine) pushColor() {
	bits := [3]uint{flagColR, flagColG, flagColB}
	var by [3]uint32
	for i := 0; i < 3; i++ {
		v := e.mac[i] >> 4
		if v < 0 {
			v = 0
			e.flag |= 1 << bits[i]
		} else if v > 0xFF {
			v = 0xFF
			e.flag |= 1 << bits[i]
		}
		by[i] = uint32(v)
	}
	_, _, _, code := e.rgbc()
	d := &e.g.data
	d[20] = d[21]
	d[21] = d[22]
	d[22] = uint32(code)<<24 | by[2]<<16 | by[1]<<8 | by[0]
}

// finishColor is the shared tail of the color
// commands: push the color FIFO, then copy the MACs
// into IR1..IR3 with saturation.
func (e *engine) finishColor() {
	e.pushColor()
	e.irFromMAC()
}

// multiplyRGBC is the "[MAC1,MAC2,MAC3] =
// [R*IR1,G*IR2,B*IR3] SHL 4" step of the NCDx, NCCx,
// CC, CDP and DCPL commands.
func (e *engine) multiplyRGBC() {
	r, g, b, _ := e.rgbc()
	bytes := [3]int64{r, g, b}
	for i := 0; i < 3; i++ {
		e.setMAC(i, (bytes[i]*e.ir(i))<<4)
	}
}

// depthCue is the shared "MAC+(FC-MAC)*IR0" step
// (psx-spx, "Details on MAC+(FC-MAC)*IR0"): the
// difference (FC SHL 12)-MAC is shifted by (sf*12),
// saturated into IR1..IR3 as if lm=0, multiplied by
// IR0 and added back to the MACs.
func (e *engine) depthCue() {
	fc := e.fcVec()
	ir0 := e.ir0()
	sh := e.sfShift()
	for i := 0; i < 3; i++ {
		diff := e.satIRValue((fc[i]<<12-e.mac[i])>>sh, false, uint(flagIR1Sat-i))
		e.storeIR(i, diff)
		e.setMAC(i, diff*ir0+e.mac[i])
	}
}

// finalColorShift is the closing "[MAC1,MAC2,MAC3] =
// [MAC1,MAC2,MAC3] SAR (sf*12)" step of the NCDx,
// NCCx, CC and CDP family and of DPCS, DPCT, DCPL and
// INTPL.
func (e *engine) finalColorShift() {
	sh := e.sfShift()
	for i := 0; i < 3; i++ {
		e.setMAC(i, e.mac[i]>>sh)
	}
}

// lightingStep is the "(LLM*Vn) SAR (sf*12)" step of
// the normal color commands.
func (e *engine) lightingStep(v [3]int64) {
	e.matMul(e.matrix(8), v, nil)
	e.irFromMAC()
}

// colorStep is the "(BK*1000h + LCM*IR) SAR (sf*12)"
// step of the normal color and color color commands.
func (e *engine) colorStep() {
	bk := e.bkVec()
	e.matMul(e.matrix(16), e.irVec(), &bk)
	e.irFromMAC()
}

// divide runs the Unsigned Newton-Raphson division of
// the psx-spx "GTE Division Inaccuracy" section: it
// approximates (H*20000h/SZ3+1)/2, saturated to
// 1FFFFh. When H >= SZ3*2 (the vertex is at or inside
// the near clip plane) it returns 1FFFFh and sets the
// divide overflow FLAG bit 17; the plain min() clamp
// at the end does not set the flag.
func (e *engine) divide(sz uint32) int64 {
	h := uint32(e.g.control[26] & 0xFFFF)
	if h >= sz*2 {
		e.flag |= 1 << flagDivOvf
		return 0x1FFFF
	}
	// sz is nonzero here: sz == 0 always takes the
	// overflow branch above.
	z := uint(0)
	for i := 15; i >= 0; i-- {
		if sz&(1<<uint(i)) != 0 {
			break
		}
		z++
	}
	n := int64(h) << z
	d := int64(sz) << z // 8000h..FFFFh
	u := unrTable[(d-0x7FC0)>>7] + 0x101
	d = (0x2000080 - d*u) >> 8
	d = (0x80 + d*u) >> 8
	n = (n*d + 0x8000) >> 16
	if n > 0x1FFFF {
		n = 0x1FFFF
	}
	return n
}

// clampScreen saturates a screen coordinate to
// -0400h..+03FFh, setting the given FLAG bit.
func (e *engine) clampScreen(v int64, bit uint) int64 {
	if v > 0x3FF {
		e.flag |= 1 << bit
		return 0x3FF
	}
	if v < -0x400 {
		e.flag |= 1 << bit
		return -0x400
	}
	return v
}

// rtpsVertex is one RTPS pass over a single vertex
// (psx-spx, "GTE Coordinate Calculation Commands"):
// rotate and translate, push the SZ FIFO, divide,
// push the SXY FIFO, and compute the depth cue value
// in IR0.
func (e *engine) rtpsVertex(v [3]int64) {
	tr := e.trVec()
	e.matMul(e.matrix(0), v, &tr)
	e.satIR(0)
	e.satIR(1)
	// IR3 saturation quirk (psx-spx, "GTE Saturation"
	// and the RTPS note): the FLAG.22 check always
	// acts as if lm=0, and with sf=0 it checks
	// MAC3 SAR 12, while the stored IR3 respects the
	// actual lm bit.
	chk := e.mac[2]
	if !e.sf {
		chk >>= 12
	}
	if chk > 0x7FFF || chk < -0x8000 {
		e.flag |= 1 << flagIR3Sat
	}
	e.storeIR(2, clampPlain(e.mac[2], e.lmMin(), 0x7FFF))
	// SZ3 = MAC3 SAR ((1-sf)*12), saturated to
	// 0000h..FFFFh (FLAG bit 18).
	szv := e.mac[2]
	if !e.sf {
		szv >>= 12
	}
	if szv < 0 {
		szv = 0
		e.flag |= 1 << flagZSat
	} else if szv > 0xFFFF {
		szv = 0xFFFF
		e.flag |= 1 << flagZSat
	}
	d := &e.g.data
	d[16] = d[17]
	d[17] = d[18]
	d[18] = d[19]
	d[19] = uint32(szv)
	n := e.divide(uint32(szv))
	ofx := s32(e.g.control[24])
	ofy := s32(e.g.control[25])
	e.setMAC0(n*e.ir(0) + ofx)
	sx := e.clampScreen(e.mac0>>16, flagSXSat)
	e.setMAC0(n*e.ir(1) + ofy)
	sy := e.clampScreen(e.mac0>>16, flagSYSat)
	d[12] = d[13]
	d[13] = d[14]
	d[14] = uint32(uint16(int16(sy)))<<16 | uint32(uint16(int16(sx)))
	// Depth cueing: IR0 = (n*DQA + DQB) / 1000h,
	// saturated to 0..+1000h (FLAG bit 12).
	e.setMAC0(n*s16(e.g.control[27]) + s32(e.g.control[28]))
	ir0 := e.mac0 >> 12
	if ir0 < 0 {
		ir0 = 0
		e.flag |= 1 << flagIR0Sat
	} else if ir0 > 0x1000 {
		ir0 = 0x1000
		e.flag |= 1 << flagIR0Sat
	}
	d[8] = uint32(int32(int16(ir0)))
}

func clampPlain(v, min, max int64) int64 {
	if v > max {
		return max
	}
	if v < min {
		return min
	}
	return v
}

// Execute dispatches one COP2 command word. It returns
// nil for every documented opcode and
// ErrUnknownCommand otherwise. FLAG is reset at the
// start of the command and the accumulated bits are
// stored to control register 31 at the end (psx-spx,
// "GTE Saturation").
func (g *GTE) Execute(cmd uint32) error {
	opcode := cmd & 0x3F
	if _, ok := commandByOpcode[opcode]; !ok {
		g.unsupported++
		return fmt.Errorf("%w: 0x%02X", ErrUnknownCommand, opcode)
	}
	g.lastCommand = cmd
	e := &engine{
		g:    g,
		sf:   cmd>>19&1 == 1,
		lm:   cmd>>10&1 == 1,
		mac0: s32(g.data[24]),
	}
	for i := 0; i < 3; i++ {
		e.mac[i] = s32(g.data[25+i])
	}
	e.run(opcode, cmd)
	g.data[24] = uint32(int32(e.mac0))
	for i := 0; i < 3; i++ {
		g.data[25+i] = uint32(int32(e.mac[i]))
	}
	g.control[31] = e.flag & 0x7FFFF000
	return nil
}

// Command is the r3000a.Cop2 entry point. It executes
// the command and returns the documented cycle count
// from the psx-spx opcode summary table (1 for an
// undocumented opcode, which the CPU has no error
// channel to report).
func (g *GTE) Command(cmd uint32) int {
	cycles := CommandCycles(cmd & 0x3F)
	_ = g.Execute(cmd)
	if cycles == 0 {
		return 1
	}
	return cycles
}

// run executes one documented opcode with the engine
// state prepared by Execute.
func (e *engine) run(opcode uint32, cmd uint32) {
	switch opcode {
	case 0x01: // RTPS
		e.rtpsVertex(e.vec(0))
	case 0x30: // RTPT
		for i := 0; i < 3; i++ {
			e.rtpsVertex(e.vec(i))
		}
	case 0x06: // NCLIP
		e.nclip()
	case 0x2D: // AVSZ3
		e.avsz(3)
	case 0x2E: // AVSZ4
		e.avsz(4)
	case 0x12: // MVMVA
		e.mvmva(cmd)
	case 0x28: // SQR
		e.sqr()
	case 0x0C: // OP
		e.op()
	case 0x1E: // NCS
		e.lightingStep(e.vec(0))
		e.colorStep()
		e.finishColor()
	case 0x20: // NCT
		for i := 0; i < 3; i++ {
			e.lightingStep(e.vec(i))
			e.colorStep()
			e.finishColor()
		}
	case 0x1B: // NCCS
		e.nccsBody(e.vec(0))
	case 0x3F: // NCCT
		for i := 0; i < 3; i++ {
			e.nccsBody(e.vec(i))
		}
	case 0x13: // NCDS
		e.ncdsBody(e.vec(0))
	case 0x16: // NCDT
		for i := 0; i < 3; i++ {
			e.ncdsBody(e.vec(i))
		}
	case 0x1C: // CC
		e.colorStep()
		e.multiplyRGBC()
		e.finalColorShift()
		e.finishColor()
	case 0x14: // CDP
		e.colorStep()
		e.multiplyRGBC()
		e.depthCue()
		e.finalColorShift()
		e.finishColor()
	case 0x29: // DCPL
		e.multiplyRGBC()
		e.depthCue()
		e.finalColorShift()
		e.finishColor()
	case 0x10: // DPCS
		r, g, b, _ := e.rgbc()
		e.setMAC(0, r<<16)
		e.setMAC(1, g<<16)
		e.setMAC(2, b<<16)
		e.depthCue()
		e.finalColorShift()
		e.finishColor()
	case 0x2A: // DPCT
		e.dpct()
	case 0x11: // INTPL
		ir := e.irVec()
		for i := 0; i < 3; i++ {
			e.setMAC(i, ir[i]<<12)
		}
		e.depthCue()
		e.finalColorShift()
		e.finishColor()
	case 0x3D: // GPF
		for i := 0; i < 3; i++ {
			e.setMAC(i, 0)
		}
		e.gpFormula()
		e.finishColor()
	case 0x3E: // GPL
		sh := e.sfShift()
		for i := 0; i < 3; i++ {
			e.setMAC(i, e.mac[i]<<sh)
		}
		e.gpFormula()
		e.finishColor()
	}
}

// nccsBody is one NCCS pass: lighting, color matrix,
// then the RGBC multiply and final shift.
func (e *engine) nccsBody(v [3]int64) {
	e.lightingStep(v)
	e.colorStep()
	e.multiplyRGBC()
	e.finalColorShift()
	e.finishColor()
}

// ncdsBody is one NCDS pass: NCCS plus the depth cue
// step before the final shift.
func (e *engine) ncdsBody(v [3]int64) {
	e.lightingStep(v)
	e.colorStep()
	e.multiplyRGBC()
	e.depthCue()
	e.finalColorShift()
	e.finishColor()
}

// dpct runs DPCS three times, reading the color bytes
// from RGB0 (the bottom of the color FIFO) on each
// pass instead of from RGBC; the CODE byte still comes
// from RGBC (psx-spx, DPCT note). Each pass pushes a
// new FIFO entry, so the three passes process the
// three original FIFO entries in order.
func (e *engine) dpct() {
	for n := 0; n < 3; n++ {
		v := e.g.data[20]
		e.setMAC(0, int64(v&0xFF)<<16)
		e.setMAC(1, int64((v>>8)&0xFF)<<16)
		e.setMAC(2, int64((v>>16)&0xFF)<<16)
		e.depthCue()
		e.finalColorShift()
		e.finishColor()
	}
}

// gpFormula is the shared "(([IR1,IR2,IR3] * IR0) +
// [MAC1,MAC2,MAC3]) SAR (sf*12)" step of GPF and GPL.
func (e *engine) gpFormula() {
	ir := e.irVec()
	ir0 := e.ir0()
	sh := e.sfShift()
	for i := 0; i < 3; i++ {
		e.setMAC(i, (ir[i]*ir0+e.mac[i])>>sh)
	}
}

// nclip computes MAC0 = SX0*SY1 + SX1*SY2 + SX2*SY0 -
// SX0*SY2 - SX1*SY0 - SX2*SY1 from the SXY FIFO; the
// sign of the result gives the polygon winding
// (psx-spx, "NCLIP").
func (e *engine) nclip() {
	d := e.g.data
	sx := [3]int64{s16(d[12]), s16(d[13]), s16(d[14])}
	sy := [3]int64{s16(d[12] >> 16), s16(d[13] >> 16), s16(d[14] >> 16)}
	e.setMAC0(sx[0]*sy[1] + sx[1]*sy[2] + sx[2]*sy[0] -
		sx[0]*sy[2] - sx[1]*sy[0] - sx[2]*sy[1])
}

// avsz computes MAC0 = ZSFn * (sum of the newest n SZ
// FIFO entries) and OTZ = MAC0 / 1000h, saturated to
// 0000h..FFFFh with FLAG bit 18 (psx-spx, "AVSZ3" and
// "AVSZ4").
func (e *engine) avsz(n int) {
	d := &e.g.data
	var sum int64
	base := 19 - n + 1 // first of the newest n SZ entries
	for i := 0; i < n; i++ {
		sum += int64(d[base+i])
	}
	zsf := s16(e.g.control[29])
	if n == 4 {
		zsf = s16(e.g.control[30])
	}
	e.setMAC0(zsf * sum)
	otz := e.mac0 >> 12
	if otz < 0 {
		otz = 0
		e.flag |= 1 << flagZSat
	} else if otz > 0xFFFF {
		otz = 0xFFFF
		e.flag |= 1 << flagZSat
	}
	d[7] = uint32(otz)
}

// mvmva implements MVMVA(sf,mx,v,cv,lm) (psx-spx,
// "MVMVA"): MAC = (Tx*1000h + Mx*Vx) SAR (sf*12) with
// the matrix, vector and translation vector selected
// by the command word.
//
// Documented quirks: cv=2 selects the far color
// vector, but the hardware drops the first product
// term and the translation, computing only
// (Mx12*Vx2 + Mx13*Vx3) SAR (sf*12), while some FLAG
// bits are adjusted as if the full operation had been
// executed. mx=3 selects a garbage matrix built from
// -R*10h, +R*10h, IR0, RT13 and RT22 (R is the red
// component of RGBC).
func (e *engine) mvmva(cmd uint32) {
	mx := (cmd >> 17) & 3
	vsel := (cmd >> 15) & 3
	cv := (cmd >> 13) & 3
	var mat [3][3]int64
	switch mx {
	case 0:
		mat = e.matrix(0)
	case 1:
		mat = e.matrix(8)
	case 2:
		mat = e.matrix(16)
	case 3:
		rt := e.matrix(0)
		r := int64(e.g.data[6] & 0xFF)
		mat = [3][3]int64{
			{-r * 0x10, r * 0x10, e.ir0()},
			{rt[0][2], rt[0][2], rt[0][2]},
			{rt[1][1], rt[1][1], rt[1][1]},
		}
	}
	var vec [3]int64
	if vsel == 3 {
		vec = e.irVec()
	} else {
		vec = e.vec(int(vsel))
	}
	sh := e.sfShift()
	switch cv {
	case 0:
		tr := e.trVec()
		e.matMul(mat, vec, &tr)
	case 1:
		bk := e.bkVec()
		e.matMul(mat, vec, &bk)
	case 2:
		fc := e.fcVec()
		for i := 0; i < 3; i++ {
			full := fc[i]<<12 + mat[i][0]*vec[0] + mat[i][1]*vec[1] + mat[i][2]*vec[2]
			e.checkMACOverflow(i, full>>sh)
			e.setMAC(i, (mat[i][1]*vec[1]+mat[i][2]*vec[2])>>sh)
		}
	case 3:
		e.matMul(mat, vec, nil)
	}
	e.irFromMAC()
}

// sqr implements SQR(sf): MAC = IR*IR SHR (sf*12),
// with IR1..IR3 saturated to a maximum of 7FFFh (the
// result is always positive, so the lm bit has no
// effect) (psx-spx, "SQR").
func (e *engine) sqr() {
	ir := e.irVec()
	sh := e.sfShift()
	for i := 0; i < 3; i++ {
		e.setMAC(i, (ir[i]*ir[i])>>sh)
		e.satIR(i)
	}
}

// op implements OP(sf,lm), the cross product of the IR
// vector with the vector formed by the RT11, RT22 and
// RT33 diagonal elements (psx-spx, "OP").
func (e *engine) op() {
	rt := e.matrix(0)
	d1, d2, d3 := rt[0][0], rt[1][1], rt[2][2]
	ir := e.irVec()
	sh := e.sfShift()
	e.setMAC(0, (ir[2]*d2-ir[1]*d3)>>sh)
	e.setMAC(1, (ir[0]*d3-ir[2]*d1)>>sh)
	e.setMAC(2, (ir[1]*d1-ir[0]*d2)>>sh)
	e.irFromMAC()
}
