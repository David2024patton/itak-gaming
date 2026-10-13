package arm7tdmi

// Step executes exactly one instruction and returns the number of
// bus cycles it consumed in the ARM7TDMI cycle model.
func (c *CPU) Step() int {
	if c.Halted {
		return 1
	}
	instr := c.Executing
	c.flushed = false
	state := c.Thumb()
	var n int
	if state {
		n = c.execThumb(uint16(instr))
	} else {
		n = c.execARM(instr)
	}
	if !c.flushed {
		// The pipeline advances in the state the instruction
		// was fetched in, even if the instruction rewrote the
		// CPSR T bit (an MSR may do that in the reference
		// implementation without disturbing this step).
		c.advance(state)
	}
	return n
}

// advance moves the pipeline one instruction unit forward without
// a control-flow change: the decode-stage unit enters execute and
// a new unit is fetched at the fetch-stage address.
func (c *CPU) advance(thumb bool) {
	if thumb {
		nf := c.Bus.Fetch((c.R[15]&^1)+2, 2)
		c.Executing = c.Pipe[0]
		c.Pipe[0] = c.Pipe[1]
		c.Pipe[1] = nf
		c.R[15] += 2
		c.ExecutingValid = true
		return
	}
	nf := c.Bus.Fetch((c.R[15]&^3)+4, 4)
	c.Executing = c.Pipe[0]
	c.Pipe[0] = c.Pipe[1]
	c.Pipe[1] = nf
	c.R[15] += 4
	c.ExecutingValid = true
}

// branch refills the pipeline at addr in the state the CPU is in
// after any CPSR change the instruction already made. Alignment
// is applied inside refill.
func (c *CPU) branch(addr uint32) {
	c.refill(addr)
	c.flushed = true
}

// condPassed evaluates an ARM condition field against the CPSR
// flags. Condition 0xF (NV) never passes on ARMv4.
func (c *CPU) condPassed(cond uint32) bool {
	n := c.CPSR&FlagN != 0
	z := c.CPSR&FlagZ != 0
	cf := c.CPSR&FlagC != 0
	v := c.CPSR&FlagV != 0
	switch cond {
	case 0x0:
		return z
	case 0x1:
		return !z
	case 0x2:
		return cf
	case 0x3:
		return !cf
	case 0x4:
		return n
	case 0x5:
		return !n
	case 0x6:
		return v
	case 0x7:
		return !v
	case 0x8:
		return cf && !z
	case 0x9:
		return !cf || z
	case 0xA:
		return n == v
	case 0xB:
		return n != v
	case 0xC:
		return !z && n == v
	case 0xD:
		return z || n != v
	case 0xE:
		return true
	}
	return false
}

func (c *CPU) setNZ(v uint32) {
	c.CPSR &^= FlagN | FlagZ
	if v&0x80000000 != 0 {
		c.CPSR |= FlagN
	}
	if v == 0 {
		c.CPSR |= FlagZ
	}
}

func (c *CPU) setNZC(v uint32, carry bool) {
	c.setNZ(v)
	c.setCarry(carry)
}

func (c *CPU) setCarry(carry bool) {
	c.CPSR &^= FlagC
	if carry {
		c.CPSR |= FlagC
	}
}

// memReg reads a register in a memory-operand position, where
// R15 reads as A+12 (the pipeline value plus one unit). Used for
// swap addressing and operands, and for stored PC values.
func (c *CPU) memReg(i uint32) uint32 {
	if i == 15 {
		return c.R[15] + 4
	}
	return c.R[i]
}

// tickMultiply reports whether the ARM7TDMI multiplier runs the
// full four beats for the given multiplier operand, with no
// early termination. Termination happens when the remaining
// high bytes of the multiplier are all zero, or (for signed
// multiplies) all ones.
func tickMultiply(signed bool, multiplier uint32) bool {
	mask := uint32(0xFFFFFF00)
	for {
		multiplier &= mask
		if multiplier == 0 {
			break
		}
		if signed && multiplier == mask {
			break
		}
		mask <<= 8
	}
	return mask == 0
}

// mulCarry computes the C flag a multiply leaves behind in the
// reference implementation. On the silicon the flag is the
// carry out of the booth multiplier's carry-save adder tree;
// the routines below model that adder. (Booth carry model
// after the published work of zaydlang and calc84maniac on the
// ARM7TDMI multiplier, as also used by NanoBoyAdvance, the
// corpus generator.) An early-terminated multiply takes the
// low-word carry; a full-length one takes the final injected
// booth carry bit for the short forms and the high-word carry
// for the long forms.
func mulCarry(full bool, isLong bool, signExtend bool, multiplicand, multiplier, accumLo, accumHi uint32) bool {
	if full {
		if isLong {
			return mulCarryHi(signExtend, multiplicand, multiplier, accumHi)
		}
		return (multiplier>>30)&3 == 2
	}
	return mulCarryLo(multiplicand, multiplier, accumLo)
}

// mulCarryLo models the carry out of the multiplier's
// carry-save adder for the low result word, processing the
// multiplier in radix-4 booth steps exactly as the hardware
// accumulates it.
func mulCarryLo(multiplicand, multiplier, accum uint32) bool {
	// Setting the low bit of the multiplicand makes negation
	// invert the upper bits; the bit cannot reach the carry.
	multiplicand |= 1

	booth := uint32(int32(multiplier<<31) >> 31)
	carry := multiplicand * booth
	sum := carry + accum

	shift := 29
	for {
		// Four booth iterations cover 8 multiplier bits.
		for i := 0; i < 4; i++ {
			nextBooth := uint32(int32(multiplier<<uint(shift)) >> uint(shift))
			factor := nextBooth - booth
			booth = nextBooth
			addend := multiplicand * factor
			// Carry-save accumulation.
			newAccum := accum ^ carry ^ addend
			sum += addend
			carry = sum - newAccum
			accum = newAccum
			shift -= 2
		}
		if booth == multiplier {
			break
		}
	}
	return carry>>31 != 0
}

// mulCarryHi models the carry out of the high result word of a
// long multiply. Only the last three booth iterations can
// influence it, so both operands are reduced to the relevant
// high bits and the carry is propagated through magic marker
// bits, following the published model.
func mulCarryHi(signExtend bool, multiplicand, multiplier, accumHi uint32) bool {
	if signExtend {
		multiplicand = uint32(int32(multiplicand) >> 6)
		multiplier = uint32(int32(multiplier) >> 26)
	} else {
		multiplicand >>= 6
		multiplier >>= 26
	}
	multiplicand |= 1

	carry := ^accumHi & 0x20000000
	accum := accumHi - 0x08000000

	booth0 := uint32(int32(multiplier<<27) >> 27)
	booth1 := uint32(int32(multiplier<<29) >> 29)
	booth2 := uint32(int32(multiplier<<31) >> 31)
	factor0 := multiplier - booth0
	factor1 := booth0 - booth1
	factor2 := booth1 - booth2

	addend := multiplicand * factor2
	accum -= addend & 0x10000000
	addend = multiplicand * factor1
	accum -= addend & 0x40000000

	sum := accum + (addend & 0x20000000)
	accum -= carry

	addend = multiplicand * factor0
	sum += addend & 0x40000000

	return (sum^accum)>>31 != 0
}

func (c *CPU) carry() bool { return c.CPSR&FlagC != 0 }

// addFlags computes a+b+carryIn and sets N, Z, C, V.
func (c *CPU) addFlags(a, b uint32, carryIn bool) uint32 {
	var ci uint64
	if carryIn {
		ci = 1
	}
	sum := uint64(a) + uint64(b) + ci
	res := uint32(sum)
	c.setNZ(res)
	c.CPSR &^= FlagC | FlagV
	if sum > 0xFFFFFFFF {
		c.CPSR |= FlagC
	}
	if (a^res)&(b^res)&0x80000000 != 0 {
		c.CPSR |= FlagV
	}
	return res
}

// subFlags computes a-b-(1-carryIn) and sets N, Z, C, V, with C
// meaning NOT borrow, as ARM defines it.
func (c *CPU) subFlags(a, b uint32, carryIn bool) uint32 {
	var ci uint64 = 1
	if carryIn {
		ci = 0
	}
	diff := uint64(a) - uint64(b) - ci
	res := uint32(diff)
	c.setNZ(res)
	c.CPSR &^= FlagC | FlagV
	if uint64(a) >= uint64(b)+ci {
		c.CPSR |= FlagC
	}
	if (a^b)&(a^res)&0x80000000 != 0 {
		c.CPSR |= FlagV
	}
	return res
}

func ror32(v uint32, n uint) uint32 {
	n &= 31
	if n == 0 {
		return v
	}
	return v>>n | v<<(32-n)
}

// shifter runs the ARM barrel shifter for data-processing operand
// 2 in register form. rmVal is the value of Rm (already PC-quirk
// adjusted by the caller), typ is the shift type (0 LSL, 1 LSR,
// 2 ASR, 3 ROR), amount is the shift amount, and byReg reports a
// register-specified shift (amount is Rs&0xFF, encoded cases
// differ). It returns the shifted value and the shifter carry
// out.
func (c *CPU) shifter(rmVal uint32, typ uint32, amount uint32, byReg bool) (uint32, bool) {
	oldC := c.carry()
	if byReg {
		if amount == 0 {
			return rmVal, oldC
		}
		switch typ {
		case 0: // LSL
			if amount < 32 {
				return rmVal << amount, (rmVal>>(32-amount))&1 != 0
			}
			if amount == 32 {
				return 0, rmVal&1 != 0
			}
			return 0, false
		case 1: // LSR
			if amount < 32 {
				return rmVal >> amount, (rmVal>>(amount-1))&1 != 0
			}
			if amount == 32 {
				return 0, rmVal>>31 != 0
			}
			return 0, false
		case 2: // ASR
			if amount >= 32 {
				if rmVal>>31 != 0 {
					return 0xFFFFFFFF, true
				}
				return 0, false
			}
			return uint32(int32(rmVal) >> amount), (rmVal>>(amount-1))&1 != 0
		case 3: // ROR
			m := amount & 31
			if m == 0 {
				return rmVal, rmVal>>31 != 0
			}
			return ror32(rmVal, uint(m)), (rmVal>>(m-1))&1 != 0
		}
	}
	// Immediate shift amounts: 0 is special per type.
	switch typ {
	case 0: // LSL #0
		if amount == 0 {
			return rmVal, oldC
		}
		return rmVal << amount, (rmVal>>(32-amount))&1 != 0
	case 1: // LSR #0 means LSR #32
		if amount == 0 {
			return 0, rmVal>>31 != 0
		}
		return rmVal >> amount, (rmVal>>(amount-1))&1 != 0
	case 2: // ASR #0 means ASR #32
		if amount == 0 {
			if rmVal>>31 != 0 {
				return 0xFFFFFFFF, true
			}
			return 0, false
		}
		return uint32(int32(rmVal) >> amount), (rmVal>>(amount-1))&1 != 0
	case 3: // ROR #0 means RRX
		if amount == 0 {
			res := rmVal >> 1
			if oldC {
				res |= 0x80000000
			}
			return res, rmVal&1 != 0
		}
		return ror32(rmVal, uint(amount)), (rmVal>>(amount-1))&1 != 0
	}
	return rmVal, oldC
}

// spsrIndex maps a mode to its SPSR slot, or -1 when the mode has
// no SPSR (USR and SYS).
func spsrIndex(mode uint32) int {
	switch mode {
	case ModeFIQ:
		return SPSRFIQ
	case ModeSVC:
		return SPSRSVC
	case ModeABT:
		return SPSRABT
	case ModeIRQ:
		return SPSRIRQ
	case ModeUND:
		return SPSRUND
	}
	return -1
}

// curSPSR returns the SPSR of the current mode. In modes without
// an SPSR it returns the CPSR, the value MRS SPSR observes on the
// hardware paths the corpus exercises.
func (c *CPU) curSPSR() uint32 {
	if idx := spsrIndex(c.Mode()); idx >= 0 {
		return c.SPSR[idx]
	}
	return c.CPSR
}

// storeBank parks the R8-R14 values of the given (outgoing) mode
// into its bank fields, restoring the neutral user R8-R12 view
// after a FIQ exit.
func (c *CPU) storeBank(mode uint32) {
	switch mode {
	case ModeFIQ:
		copy(c.RFIQ[0:5], c.R[8:13])
		c.RFIQ[5] = c.R[13]
		c.RFIQ[6] = c.R[14]
		copy(c.R[8:13], c.usr8_12[0:5])
	case ModeSVC:
		c.RSVC[0], c.RSVC[1] = c.R[13], c.R[14]
	case ModeABT:
		c.RABT[0], c.RABT[1] = c.R[13], c.R[14]
	case ModeIRQ:
		c.RIRQ[0], c.RIRQ[1] = c.R[13], c.R[14]
	case ModeUND:
		c.RUND[0], c.RUND[1] = c.R[13], c.R[14]
	default: // USR, SYS
		c.RUSR[0], c.RUSR[1] = c.R[13], c.R[14]
	}
}

// loadBank brings the R8-R14 values of the given (incoming) mode
// into the live register view.
func (c *CPU) loadBank(mode uint32) {
	switch mode {
	case ModeFIQ:
		copy(c.usr8_12[0:5], c.R[8:13])
		copy(c.R[8:13], c.RFIQ[0:5])
		c.R[13] = c.RFIQ[5]
		c.R[14] = c.RFIQ[6]
	case ModeSVC:
		c.R[13], c.R[14] = c.RSVC[0], c.RSVC[1]
	case ModeABT:
		c.R[13], c.R[14] = c.RABT[0], c.RABT[1]
	case ModeIRQ:
		c.R[13], c.R[14] = c.RIRQ[0], c.RIRQ[1]
	case ModeUND:
		c.R[13], c.R[14] = c.RUND[0], c.RUND[1]
	default:
		c.R[13], c.R[14] = c.RUSR[0], c.RUSR[1]
	}
}

// setCPSR writes the CPSR, swapping register banks when the mode
// bits change.
func (c *CPU) setCPSR(v uint32) {
	oldMode := c.Mode()
	newMode := v & 0x1F
	if oldMode != newMode {
		c.storeBank(oldMode)
		c.loadBank(newMode)
	}
	c.CPSR = v
}

// msr writes val into the fields of the CPSR or the current SPSR
// selected by fieldMask (bit0 c, bit1 x, bit2 s, bit3 f, from the
// instruction's mask field). CPSR control-field (c) writes
// require a privileged mode.
func (c *CPU) msr(toSPSR bool, fieldMask uint32, val uint32) {
	// Field write mask. In user mode only the flag field of the
	// CPSR is writable; the s, x and c fields require privilege.
	// A CPSR control-field write preserves bit 4 of the mode
	// field (only bits 3-0 of the mode are writable) in the
	// reference implementation, while an SPSR control-field
	// write takes bit 4 from the operand; even the T bit is
	// stored from the operand.
	priv := c.Mode() != ModeUSR
	var m uint32
	if fieldMask&8 != 0 {
		m |= 0xFF000000
	}
	if fieldMask&4 != 0 && (toSPSR || priv) {
		m |= 0x00FF0000
	}
	if fieldMask&2 != 0 && (toSPSR || priv) {
		m |= 0x0000FF00
	}
	if fieldMask&1 != 0 && toSPSR {
		m |= 0x000000FF
	}
	if fieldMask&1 != 0 && !toSPSR && priv {
		m |= 0x000000EF
	}
	if toSPSR {
		idx := spsrIndex(c.Mode())
		if idx < 0 {
			c.spsrDummy = val
			return
		}
		cur := c.SPSR[idx]
		c.SPSR[idx] = cur&^m | val&m
		return
	}
	nv := c.CPSR&^m | val&m
	c.setCPSR(nv)
}

// exception enters an exception mode: SPSR of the new mode saves
// the old CPSR, the mode switches with I set (and F set for FIQ),
// LR of the new mode receives lr, and the pipeline refills at the
// vector. Entry costs 2S+1N = 3 cycles.
func (c *CPU) exception(vector uint32, mode uint32, lr uint32) int {
	if idx := spsrIndex(mode); idx >= 0 {
		c.SPSR[idx] = c.CPSR
	}
	nc := c.CPSR&^0x1F | mode
	nc &^= BitT
	nc |= BitI
	if mode == ModeFIQ {
		nc |= BitF
	}
	c.setCPSR(nc)
	c.R[14] = lr
	c.branch(vector)
	return 3
}

// undefined raises the undefined-instruction exception for the
// instruction at addr (the executing address).
func (c *CPU) undefined(addr uint32) int {
	if c.Thumb() {
		return c.exception(0x04, ModeUND, addr+2)
	}
	return c.exception(0x04, ModeUND, addr+4)
}

// userReg reads register n of the user bank (for the S-bit block
// transfer forms), independent of the current mode.
func (c *CPU) userReg(n uint32) uint32 {
	switch {
	case n < 8:
		return c.R[n]
	case n < 13:
		if c.Mode() == ModeFIQ {
			return c.usr8_12[n-8]
		}
		return c.R[n]
	case n < 15:
		if c.Mode() == ModeUSR || c.Mode() == ModeSYS {
			return c.R[n]
		}
		return c.RUSR[n-13]
	}
	return c.R[15]
}

// userSet writes register n of the user bank.
func (c *CPU) userSet(n uint32, v uint32) {
	switch {
	case n < 8:
		c.R[n] = v
	case n < 13:
		if c.Mode() == ModeFIQ {
			c.usr8_12[n-8] = v
		} else {
			c.R[n] = v
		}
	case n < 15:
		if c.Mode() == ModeUSR || c.Mode() == ModeSYS {
			c.R[n] = v
		} else {
			c.RUSR[n-13] = v
		}
	}
}

// load performs a data load of the given size in bytes. The bus
// returns the raw aligned unit containing addr; the CPU applies
// the ARM alignment rotation: word loads rotate right by the
// byte offset, and a halfword load from an odd address rotates
// the zero-extended halfword right by 8 (the reference behavior,
// which also feeds the signed forms).
func (c *CPU) load(addr uint32, size int, signed bool) uint32 {
	switch size {
	case 1:
		v := c.Bus.Read(addr, 1)
		if signed {
			return uint32(int32(int8(v)))
		}
		return v
	case 2:
		v := c.Bus.Read(addr, 2)
		if signed && addr&1 != 0 {
			// A misaligned signed halfword load returns the
			// sign-extended byte at addr (the high byte of
			// the aligned halfword read).
			return uint32(int32(int8(v >> 8)))
		}
		if addr&1 != 0 {
			v = ror32(v, 8)
		}
		if signed {
			return uint32(int32(int16(v)))
		}
		return v
	default:
		return ror32(c.Bus.Read(addr, 4), uint((addr&3)*8))
	}
}

// store performs a data store of the given size in bytes,
// masking the value to the transfer width.
func (c *CPU) store(addr uint32, size int, val uint32) {
	switch size {
	case 1:
		val &= 0xFF
	case 2:
		val &= 0xFFFF
	}
	c.Bus.Write(addr, size, val)
}
