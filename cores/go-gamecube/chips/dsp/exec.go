// Instruction execution for the GameCube DSP. The
// encodings follow the published GCDSP opcode table
// (the same factual table Dolphin keeps in DSPTables):
// primary opcodes are one or two words, and opcodes
// from 0x3000 up carry a parallel extended opcode in
// their low byte (low 7 bits for the 0x3xxx group),
// which this core executes before the main operation.
//
// Flag model: SR carries C, V, Z, S, sticky overflow
// OS, top bits TB and logic zero LZ as documented in
// the package brief. When SR.AM is clear, ADD, SUB and
// CMP family results clamp to signed 32 bit; when set,
// they use the full 40 bit accumulator range.
package dsp

const (
	mask40 = (int64(1) << 40) - 1
	mask48 = (int64(1) << 48) - 1
)

// Step advances the DSP by up to n cycles, one cycle
// per instruction in this flat timing model. While
// DSPRST is set or the core is halted, Step does
// nothing.
func (d *DSP) Step(n int) {
	for i := 0; i < n; i++ {
		if d.ctrl&ctlDSPRST != 0 || d.halted {
			return
		}
		d.stepOne()
	}
}

// fetch reads the instruction word at pc. IRAM covers
// 0x0000-0x0FFF; the IROM window 0x8000-0x8FFF and
// everything else read as zero (NOP), because no ROM
// dump is shipped.
func (d *DSP) fetch(pc uint16) uint16 {
	if pc < iramWords {
		return d.iram[pc]
	}
	return 0
}

// readData reads one word of DSP data space: DRAM at
// 0x0000-0x0FFF, DROM 0x8000-0x87FF as zeros, and the
// sparse hardware page at 0xFFxx.
func (d *DSP) readData(addr uint16) uint16 {
	switch {
	case addr < dramWords:
		return d.dram[addr]
	case addr == 0xFFFC: // DMBH: CPU to DSP mail, high
		v := d.cpuInH & 0x7FFF
		if d.cpuPending {
			v |= 0x8000
		}
		return v
	case addr == 0xFFFD: // DMBL: reading clears pending
		v := d.cpuInL
		d.cpuPending = false
		return v
	}
	return 0
}

// writeData writes one word of DSP data space.
func (d *DSP) writeData(addr uint16, v uint16) {
	switch {
	case addr < dramWords:
		d.dram[addr] = v
	case addr == 0xFFF9: // DIRQ
		if v&1 != 0 {
			d.ctrl |= ctlDSPINT
		}
	case addr == 0xFFFC: // DMBH: DSP to CPU mail, high
		d.dspOutH = v & 0x7FFF
	case addr == 0xFFFD: // DMBL: completes the mail
		d.cpuOutH = d.dspOutH
		d.cpuOutL = v
		d.cpuReady = true
	}
}

// Stack indices, per the published register map.
const (
	stCall     = 0
	stData     = 1
	stLoopAddr = 2
	stLoopCnt  = 3
)

func (d *DSP) push(s int, v uint16) {
	d.stack[s][d.sp[s]] = v
	d.sp[s] = (d.sp[s] + 1) & 31
	if d.depth[s] < 32 {
		d.depth[s]++
	}
}

func (d *DSP) pop(s int) uint16 {
	if d.depth[s] == 0 {
		return 0
	}
	d.sp[s] = (d.sp[s] - 1) & 31
	d.depth[s]--
	return d.stack[s][d.sp[s]]
}

func (d *DSP) top(s int) uint16 {
	if d.depth[s] == 0 {
		return 0
	}
	return d.stack[s][(d.sp[s]-1)&31]
}

func (d *DSP) setTop(s int, v uint16) {
	if d.depth[s] == 0 {
		d.push(s, v)
		return
	}
	d.stack[s][(d.sp[s]-1)&31] = v
}

// wrapAdd applies WR wrap addressing to an address
// register update: with WR = 0xFFFF wrapping is off,
// otherwise the address wraps inside the aligned block
// of WR+1 words that contains it.
func (d *DSP) wrapAdd(idx int, delta int32) {
	ar := int32(d.ar[idx])
	wr := d.wr[idx]
	if wr == 0xFFFF {
		d.ar[idx] = uint16(ar + delta)
		return
	}
	length := int32(wr) + 1
	base := ar - ar%length
	off := (ar%length + delta) % length
	if off < 0 {
		off += length
	}
	d.ar[idx] = uint16(base + off)
}

// postMod applies an AR post modification: mode 0 none,
// 1 increment, 2 decrement, 3 add IX of the same index.
func (d *DSP) postMod(idx int, mode int) {
	switch mode {
	case 1:
		d.wrapAdd(idx, 1)
	case 2:
		d.wrapAdd(idx, -1)
	case 3:
		d.wrapAdd(idx, int32(d.ix[idx]))
	}
}

// postModeEnc translates the encoded post modification
// field used by the LRR, SRR and ILRR families (0 none,
// 1 decrement, 2 increment, 3 add IX) into postMod
// mode numbers.
func postModeEnc(enc int) int {
	return [...]int{0, 2, 1, 3}[enc&3]
}

// Register file access by published register number:
// 0-3 AR, 4-7 IX, 8-11 WR, 12-15 ST (stack tops),
// 16-17 ACC.H, 18 CR, 19 SR, 20-23 PROD.L/M/H/M2,
// 24-27 AX.L/H parts, 28-31 ACC.L/M parts, 32-33 full
// ACC (low word visible here), 34-35 full AX.
func (d *DSP) getReg(r int) uint16 {
	switch {
	case r < 4:
		return d.ar[r]
	case r < 8:
		return uint16(d.ix[r-4])
	case r < 12:
		return d.wr[r-8]
	case r < 16:
		return d.top(r - 12)
	case r == 16, r == 17:
		return uint16(int16(int8(d.acc[r-16] >> 32)))
	case r == 18:
		return d.cr
	case r == 19:
		return d.sr
	case r == 20:
		return uint16(d.prod)
	case r == 21:
		return uint16(d.prod >> 16)
	case r == 22:
		return uint16(d.prod >> 32)
	case r == 23:
		return uint16(d.prod >> 48)
	case r < 28:
		a := d.ax[(r-24)%2]
		if (r-24)/2 == 0 {
			return uint16(a)
		}
		return uint16(a >> 16)
	case r < 32:
		a := d.acc[(r-28)%2]
		if (r-28)/2 == 0 {
			return uint16(a)
		}
		return uint16(a >> 16)
	case r < 34:
		return uint16(d.acc[r-32])
	case r < 36:
		return uint16(d.ax[r-34])
	}
	return 0
}

func norm40(v int64) int64 {
	v &= mask40
	if v&(1<<39) != 0 {
		v -= 1 << 40
	}
	return v
}

func norm48(v int64) int64 {
	v &= mask48
	if v&(1<<47) != 0 {
		v -= 1 << 48
	}
	return v
}

// setReg writes register r. Writing ACC.M sign extends
// the new middle word into ACC.H, matching the
// documented register write behaviour; writing ACC.H
// replaces the top byte only.
func (d *DSP) setReg(r int, v uint16) {
	switch {
	case r < 4:
		d.ar[r] = v
	case r < 8:
		d.ix[r-4] = int16(v)
	case r < 12:
		d.wr[r-8] = v
	case r < 16:
		d.setTop(r-12, v)
	case r == 16, r == 17:
		i := r - 16
		d.acc[i] = norm40((d.acc[i] & 0xFFFFFFFF) | int64(int8(v))<<32)
	case r == 18:
		d.cr = v
	case r == 19:
		d.sr = v
	case r == 20, r == 21, r == 22, r == 23:
		raw := uint64(d.prod) & uint64(mask48)
		shift := uint(r-20) * 16
		raw = raw&^(0xFFFF<<shift) | uint64(v)<<shift
		d.prod = norm48(int64(raw))
	case r < 28:
		i := (r - 24) % 2
		if (r-24)/2 == 0 {
			d.ax[i] = int32(uint32(d.ax[i])&0xFFFF0000 | uint32(v))
		} else {
			d.ax[i] = int32(uint32(d.ax[i])&0x0000FFFF | uint32(v)<<16)
		}
	case r < 32:
		i := (r - 28) % 2
		if (r-28)/2 == 0 {
			d.acc[i] = norm40((d.acc[i] &^ 0xFFFF) | int64(v))
		} else {
			d.acc[i] = norm40(int64(int16(v))<<16 | (d.acc[i] & 0xFFFF))
		}
	case r < 34:
		d.acc[r-32] = int64(int16(v))
	case r < 36:
		d.ax[r-34] = int32(int16(v))
	}
}

func (d *DSP) accM(i int) int64 { return int64(int16(d.acc[i] >> 16)) }

func (d *DSP) axPart(i int, high bool) int64 {
	if high {
		return int64(int16(d.ax[i] >> 16))
	}
	return int64(int16(d.ax[i]))
}

func (d *DSP) setProd(v int64) {
	if d.prodTimes2 {
		v *= 2
	}
	d.prod = norm48(v)
}

// setValueFlags updates Z, S and TB from a 40 bit
// accumulator value.
func (d *DSP) setValueFlags(v int64) {
	if v == 0 {
		d.sr |= srZero
	} else {
		d.sr &^= srZero
	}
	if v < 0 {
		d.sr |= srSign
	} else {
		d.sr &^= srSign
	}
	top := uint64(v) & uint64(mask40) >> 38
	if top == 0 || top == 3 {
		d.sr |= srTopBits
	} else {
		d.sr &^= srTopBits
	}
}

func (d *DSP) setAccFlags(i int) { d.setValueFlags(d.acc[i]) }

// aluAddSub is the ADD/SUB/CMP datapath. It sets C, V,
// Z, S, TB and sticky OS, clamps the result to signed
// 32 bit when SR.AM is clear, and returns the result
// (CMP callers discard it).
func (d *DSP) aluAddSub(a, b int64, sub bool) int64 {
	ua := uint64(a) & uint64(mask40)
	ub := uint64(b) & uint64(mask40)
	var raw int64
	if sub {
		raw = a - b
		if ua < ub {
			d.sr |= srCarry
		} else {
			d.sr &^= srCarry
		}
	} else {
		raw = a + b
		if ua+ub > uint64(mask40) {
			d.sr |= srCarry
		} else {
			d.sr &^= srCarry
		}
	}
	var res int64
	overflow := false
	if d.sr&srAccMode == 0 {
		if raw > (1<<31)-1 {
			res, overflow = (1<<31)-1, true
		} else if raw < -(1 << 31) {
			res, overflow = -(1 << 31), true
		} else {
			res = raw
		}
	} else {
		if raw > mask40>>1 || raw < -(mask40>>1)-1 {
			overflow = true
		}
		res = norm40(raw)
	}
	if overflow {
		d.sr |= srOver | srOverStk
	} else {
		d.sr &^= srOver
	}
	d.setValueFlags(res)
	return res
}

// cond evaluates condition code cc (0-15) against SR.
func (d *DSP) cond(cc uint16) bool {
	s := d.sr
	switch cc & 0xF {
	case 0: // GE: not sign xor overflow
		return (s&srSign == 0) == (s&srOver == 0)
	case 1: // L
		return (s&srSign == 0) != (s&srOver == 0)
	case 2: // G
		return s&srZero == 0 && (s&srSign == 0) == (s&srOver == 0)
	case 3: // LE
		return s&srZero != 0 || (s&srSign == 0) != (s&srOver == 0)
	case 4:
		return s&srZero == 0
	case 5:
		return s&srZero != 0
	case 6:
		return s&srCarry == 0
	case 7:
		return s&srCarry != 0
	case 8:
		return s&srTopBits == 0
	case 9:
		return s&srTopBits != 0
	case 10:
		return s&srLogicZ == 0
	case 11:
		return s&srLogicZ != 0
	case 12:
		return s&srOverStk != 0
	case 13:
		return s&srOverStk == 0
	case 14:
		return s&srOver != 0
	}
	return true
}

// instrSize returns the word size of an instruction
// with opcode op, for IF skip handling.
func instrSize(op uint16) int {
	switch {
	case op&0xFFE0 == 0x0080 || op&0xFFE0 == 0x00C0 || op&0xFFE0 == 0x00E0:
		return 2 // LRI, LR, SR
	case op&0xFFF0 == 0x0290 || op&0xFFF0 == 0x02B0:
		return 2 // JMPcc, CALLcc
	case op&0xFFE0 == 0x0060:
		return 2 // BLOOP
	case op&0xFF00 == 0x1100 || op&0xFF00 == 0x1600:
		return 2 // BLOOPI, SI
	case op&0xFEFF == 0x0200 || op&0xFEFF == 0x0220 || op&0xFEFF == 0x0240 ||
		op&0xFEFF == 0x0260 || op&0xFEFF == 0x0280 || op&0xFEFF == 0x02A0 ||
		op&0xFEFF == 0x02C0:
		return 2 // immediate ALU group
	}
	return 1
}

// stepOne executes one instruction at PC, including the
// mailbox interrupt entry check. Vector choice: with no
// IPL microcode present, a pending CPU mailbox with
// SR.IE and SR.ME set vectors to 0x0000, pushing PC on
// the call stack and SR on the data stack and clearing
// SR.IE, as documented in the package brief.
func (d *DSP) stepOne() {
	if d.cpuPending && d.sr&srIntEn != 0 && d.sr&srMailEn != 0 {
		d.push(stCall, d.pc)
		d.push(stData, d.sr)
		d.sr &^= srIntEn
		d.pc = 0x0000
	}
	pc0 := d.pc
	op := d.fetch(pc0)
	next := d.fetch(pc0 + 1)
	size := instrSize(op)
	d.pc = pc0 + uint16(size)

	// Extended opcodes ride in the low byte of
	// extendable primaries (0x3000 and up) and run
	// before the main operation.
	if op >= 0x3000 {
		ext := op & 0xFF
		if op>>12 == 0x3 {
			ext = op & 0x7F
		}
		d.execExt(uint8(ext))
	}
	d.execMain(op, next, pc0)

	// Hardware loop end check: an instruction that
	// falls through from the loop end address repeats
	// the block while the counter lasts.
	if d.depth[stLoopAddr] > 0 && pc0 == d.top(stLoopAddr) &&
		d.pc == pc0+uint16(size) {
		cnt := d.top(stLoopCnt) - 1
		if cnt > 0 {
			d.setTop(stLoopCnt, cnt)
			d.pc = d.top(stCall)
		} else {
			d.pop(stCall)
			d.pop(stLoopAddr)
			d.pop(stLoopCnt)
		}
	}
}

// startLoop pushes the loop state for LOOP and BLOOP
// family instructions. A zero count starts no loop and
// the body simply executes once.
func (d *DSP) startLoop(count uint16, repeat, end uint16) {
	if count == 0 {
		return
	}
	d.push(stCall, repeat)
	d.push(stLoopAddr, end)
	d.push(stLoopCnt, count)
}

func (d *DSP) execMain(op, next uint16, pc0 uint16) {
	switch {
	// Small opcodes, 0x0000-0x02FF.
	case op&0xFFFC == 0x0000: // NOP
	case op&0xFFFC == 0x0004: // DAR
		d.wrapAdd(int(op&3), -1)
	case op&0xFFFC == 0x0008: // IAR
		d.wrapAdd(int(op&3), 1)
	case op&0xFFFC == 0x000C: // SUBARN
		i := int(op & 3)
		d.wrapAdd(i, -int32(d.ix[i]))
	case op&0xFFF0 == 0x0010: // ADDARN
		d.wrapAdd(int(op&3), int32(d.ix[(op>>2)&3]))
	case op == 0x0021: // HALT
		d.halted = true
	case op&0xFFE0 == 0x0040: // LOOP $R
		d.startLoop(d.getReg(int(op&0x1F)), pc0+1, pc0+1)
	case op&0xFFE0 == 0x0060: // BLOOP $R, addr
		d.startLoop(d.getReg(int(op&0x1F)), pc0+2, next)
	case op&0xFFE0 == 0x0080: // LRI $D, #imm
		d.setReg(int(op&0x1F), next)
	case op&0xFFE0 == 0x00C0: // LR $D, @addr
		d.setReg(int(op&0x1F), d.readData(next))
	case op&0xFFE0 == 0x00E0: // SR @addr, $S
		d.writeData(next, d.getReg(int(op&0x1F)))

	// Immediate ALU group, two words, acc bit at 8.
	case op&0xFEFF == 0x0200: // ADDI
		i := int((op >> 8) & 1)
		d.acc[i] = d.aluAddSub(d.acc[i], int64(int16(next))<<16, false)
	case op&0xFEFF == 0x0220: // XORI (on .m)
		i := int((op >> 8) & 1)
		d.setAccM(i, uint16(d.accM(i))^next)
	case op&0xFEFF == 0x0240: // ANDI
		i := int((op >> 8) & 1)
		d.setAccM(i, uint16(d.accM(i))&next)
	case op&0xFEFF == 0x0260: // ORI
		i := int((op >> 8) & 1)
		d.setAccM(i, uint16(d.accM(i))|next)
	case op&0xFEFF == 0x0280: // CMPI
		i := int((op >> 8) & 1)
		d.aluAddSub(d.acc[i], int64(int16(next))<<16, true)
	case op&0xFEFF == 0x02A0: // ANDF: LZ from .m AND imm
		i := int((op >> 8) & 1)
		d.setLogicZ(uint16(d.accM(i))&next == 0)
	case op&0xFEFF == 0x02C0: // ANDCF
		i := int((op >> 8) & 1)
		d.setLogicZ(uint16(d.accM(i))&next == next)
	case op&0xFEFC == 0x0210 || op&0xFEFC == 0x0214 ||
		op&0xFEFC == 0x0218 || op&0xFEFC == 0x021C: // ILRR family
		i := int((op >> 8) & 1)
		ai := int(op & 3)
		d.setReg(30+i, d.fetch(d.ar[ai]))
		d.postMod(ai, postModeEnc(int((op>>2)&3)))
	case op == 0x02CA: // LSRN
		d.shiftByReg(false)
	case op == 0x02CB: // ASRN
		d.shiftByReg(true)

	// Conditional control flow, cc in the low nibble.
	case op&0xFFF0 == 0x0270: // IFcc: skip next if false
		if !d.cond(op) {
			nop := d.fetch(d.pc)
			d.pc += uint16(instrSize(nop))
		}
	case op&0xFFF0 == 0x0290: // JMPcc
		if d.cond(op) {
			d.pc = next
		}
	case op&0xFFF0 == 0x02B0: // CALLcc
		if d.cond(op) {
			d.push(stCall, pc0+2)
			d.pc = next
		}
	case op&0xFFF0 == 0x02D0: // RETcc
		if d.cond(op) {
			d.pc = d.pop(stCall)
		}
	case op&0xFFF0 == 0x02F0: // RTIcc
		if d.cond(op) {
			d.sr = d.pop(stData)
			d.pc = d.pop(stCall)
		}

	case op&0xFE00 == 0x0400: // ADDIS
		i := int((op >> 8) & 1)
		d.acc[i] = d.aluAddSub(d.acc[i], int64(int8(op))<<16, false)
	case op&0xFE00 == 0x0600: // CMPIS
		i := int((op >> 8) & 1)
		d.aluAddSub(d.acc[i], int64(int8(op))<<16, true)
	case op&0xF800 == 0x0800: // LRIS
		d.setReg(24+int((op>>8)&7), uint16(int16(int8(op))))
	case op&0xFF00 == 0x1000: // LOOPI
		d.startLoop(op&0xFF, pc0+1, pc0+1)
	case op&0xFF00 == 0x1100: // BLOOPI
		d.startLoop(op&0xFF, pc0+2, next)
	case op&0xFF00 == 0x1200: // SBCLR
		d.sr &^= 1 << ((op & 7) + 6)
	case op&0xFF00 == 0x1300: // SBSET
		d.sr |= 1 << ((op & 7) + 6)
	case op&0xFEC0 == 0x1400 || op&0xFEC0 == 0x1480: // LSL, ASL
		i := int((op >> 8) & 1)
		d.acc[i] = norm40(d.acc[i] << (op & 0x3F))
		if op&0x3F >= 40 {
			d.acc[i] = 0
		}
		d.setAccFlags(i)
	case op&0xFEC0 == 0x1440: // LSR
		i := int((op >> 8) & 1)
		d.acc[i] = int64(uint64(d.acc[i]) & uint64(mask40) >> (op & 0x3F))
		d.setAccFlags(i)
	case op&0xFEC0 == 0x14C0: // ASR
		i := int((op >> 8) & 1)
		d.acc[i] = norm40(d.acc[i] >> (op & 0x3F))
		d.setAccFlags(i)
	case op&0xFF00 == 0x1600: // SI @0xFFxx, #imm
		d.writeData(0xFF00|(op&0xFF), next)
	case op&0xFFF0 == 0x1700: // JMPRcc $R
		if d.cond(op) {
			d.pc = d.getReg(int((op >> 5) & 7))
		}
	case op&0xFFF0 == 0x1710: // CALLRcc $R
		if d.cond(op) {
			d.push(stCall, pc0+1)
			d.pc = d.getReg(int((op >> 5) & 7))
		}

	// LRR and SRR families.
	case op&0xFF80 == 0x1800 || op&0xFF80 == 0x1880 ||
		op&0xFF80 == 0x1900 || op&0xFF80 == 0x1980: // LRR/D/I/N
		ai := int((op >> 5) & 3)
		d.setReg(int(op&0x1F), d.readData(d.ar[ai]))
		d.postMod(ai, postModeEnc(int((op>>7)&3)))
	case op&0xFF80 == 0x1A00 || op&0xFF80 == 0x1A80 ||
		op&0xFF80 == 0x1B00 || op&0xFF80 == 0x1B80: // SRR/D/I/N
		ai := int((op >> 5) & 3)
		d.writeData(d.ar[ai], d.getReg(int(op&0x1F)))
		d.postMod(ai, postModeEnc(int((op>>7)&3)))

	case op&0xFC00 == 0x1C00: // MRR $D, $S
		d.setReg(int((op>>5)&0x1F), d.getReg(int(op&0x1F)))
	case op&0xF800 == 0x2000: // LRS $(D+24), @page:imm
		addr := uint16(d.cr&0xFF)<<8 | (op & 0xFF)
		d.setReg(24+int((op>>8)&7), d.readData(addr))
	case op&0xFE00 == 0x2800: // SRSH @page:imm, $acS.h
		addr := uint16(d.cr&0xFF)<<8 | (op & 0xFF)
		d.writeData(addr, d.getReg(16+int((op>>8)&1)))
	case op&0xFC00 == 0x2C00: // SRS @page:imm, $(S+28)
		addr := uint16(d.cr&0xFF)<<8 | (op & 0xFF)
		d.writeData(addr, d.getReg(28+int((op>>8)&3)))

	default:
		d.execALU(op)
	}
}

// setAccM replaces ACC.M and sign extends it into
// ACC.H, as the .m logical operations do.
func (d *DSP) setAccM(i int, m uint16) {
	d.acc[i] = norm40(int64(int16(m))<<16 | (d.acc[i] & 0xFFFF))
	d.setLogicZ(m == 0)
	d.setAccFlags(i)
}

func (d *DSP) setLogicZ(z bool) {
	if z {
		d.sr |= srLogicZ
	} else {
		d.sr &^= srLogicZ
	}
}

// shiftByReg implements LSRN and ASRN: ACC0 shifts by
// the signed 7 bit amount in ACC1.M.
func (d *DSP) shiftByReg(arith bool) {
	v := int((d.acc[1] >> 16) & 0x7F)
	if v&0x40 != 0 {
		v -= 0x80
	}
	d.shiftAccN(0, v, arith)
}

// shiftAccN shifts ACC i by a signed amount: positive
// is a right shift, negative a left shift.
func (d *DSP) shiftAccN(i, amt int, arith bool) {
	switch {
	case amt > 0:
		if arith {
			d.acc[i] = norm40(d.acc[i] >> amt)
		} else {
			if amt >= 40 {
				d.acc[i] = 0
			} else {
				d.acc[i] = int64(uint64(d.acc[i]) & uint64(mask40) >> amt)
			}
		}
	case amt < 0:
		if -amt >= 40 {
			d.acc[i] = 0
		} else {
			d.acc[i] = norm40(d.acc[i] << (-amt))
		}
	}
	d.setAccFlags(i)
}

// shiftSrc returns a signed 7 bit shift amount from a
// 16 bit source value.
func shiftSrc(v int64) int {
	n := int(v & 0x7F)
	if n&0x40 != 0 {
		n -= 0x80
	}
	return n
}

// execALU handles the extendable primary groups
// 0x3000-0xFFFF: logic, add and subtract families,
// moves, multiply and multiply-accumulate, shifts and
// the configuration opcodes.
func (d *DSP) execALU(op uint16) {
	acc := int((op >> 8) & 1) // common accumulator select
	switch {
	// Group 3: register logic and register shifts.
	// The main opcode occupies 9 bits here.
	case op&0xFE80 == 0x3080: // XORC: .m ^= other acc .m
		d.setAccM(acc, uint16(d.accM(acc))^uint16(d.accM(acc^1)))
	case op&0xFE80 == 0x3280: // NOT
		d.setAccM(acc, ^uint16(d.accM(acc)))
	case op&0xFE80 == 0x3C00: // ANDC
		d.setAccM(acc, uint16(d.accM(acc))&uint16(d.accM(acc^1)))
	case op&0xFE80 == 0x3E00: // ORC
		d.setAccM(acc, uint16(d.accM(acc))|uint16(d.accM(acc^1)))
	case op&0xFE80 == 0x3C80: // LSRNR
		d.shiftAccN(acc, shiftSrc(d.accM(acc^1)), false)
	case op&0xFE80 == 0x3E80: // ASRNR
		d.shiftAccN(acc, shiftSrc(d.accM(acc^1)), true)
	case op&0xFC80 == 0x3000: // XORR: .m ^= $axS.h
		s := int((op >> 9) & 1)
		d.setAccM(acc, uint16(d.accM(acc))^uint16(int16(d.ax[s]>>16)))
	case op&0xFC80 == 0x3400: // ANDR
		s := int((op >> 9) & 1)
		d.setAccM(acc, uint16(d.accM(acc))&uint16(int16(d.ax[s]>>16)))
	case op&0xFC80 == 0x3480: // LSRNRX
		s := int((op >> 9) & 1)
		d.shiftAccN(acc, shiftSrc(int64(int16(d.ax[s]>>16))), false)
	case op&0xFC80 == 0x3800: // ORR
		s := int((op >> 9) & 1)
		d.setAccM(acc, uint16(d.accM(acc))|uint16(int16(d.ax[s]>>16)))
	case op&0xFC80 == 0x3880: // ASRNRX
		s := int((op >> 9) & 1)
		d.shiftAccN(acc, shiftSrc(int64(int16(d.ax[s]>>16))), true)

	// Group 4: additions. Register sources from the
	// 24-27 bank enter at the .m position, the fixed
	// point convention of this datapath.
	case op&0xF800 == 0x4000: // ADDR
		s := int((op >> 9) & 3)
		d.acc[acc] = d.aluAddSub(d.acc[acc], int64(int16(d.getReg(24+s)))<<16, false)
	case op&0xFC00 == 0x4800: // ADDAX
		s := int((op >> 9) & 1)
		d.acc[acc] = d.aluAddSub(d.acc[acc], int64(d.ax[s]), false)
	case op&0xFE00 == 0x4C00: // ADD
		d.acc[acc] = d.aluAddSub(d.acc[acc], d.acc[acc^1], false)
	case op&0xFE00 == 0x4E00: // ADDP
		d.acc[acc] = d.aluAddSub(d.acc[acc], norm40(d.prod), false)

	// Group 5: subtractions.
	case op&0xF800 == 0x5000: // SUBR
		s := int((op >> 9) & 3)
		d.acc[acc] = d.aluAddSub(d.acc[acc], int64(int16(d.getReg(24+s)))<<16, true)
	case op&0xFC00 == 0x5800: // SUBAX
		s := int((op >> 9) & 1)
		d.acc[acc] = d.aluAddSub(d.acc[acc], int64(d.ax[s]), true)
	case op&0xFE00 == 0x5C00: // SUB
		d.acc[acc] = d.aluAddSub(d.acc[acc], d.acc[acc^1], true)
	case op&0xFE00 == 0x5E00: // SUBP
		d.acc[acc] = d.aluAddSub(d.acc[acc], norm40(d.prod), true)

	// Group 6: moves into the accumulators.
	case op&0xF800 == 0x6000: // MOVR
		s := int((op >> 9) & 3)
		d.acc[acc] = int64(int16(d.getReg(24+s))) << 16
		d.setAccFlags(acc)
	case op&0xFC00 == 0x6800: // MOVAX
		s := int((op >> 9) & 1)
		d.acc[acc] = int64(d.ax[s])
		d.setAccFlags(acc)
	case op&0xFE00 == 0x6C00: // MOV
		d.acc[acc] = d.acc[acc^1]
		d.setAccFlags(acc)
	case op&0xFE00 == 0x6E00: // MOVP
		d.acc[acc] = norm40(d.prod)
		d.setAccFlags(acc)

	// Group 7.
	case op&0xFC00 == 0x7000: // ADDAXL
		s := int((op >> 9) & 1)
		d.acc[acc] = d.aluAddSub(d.acc[acc], int64(uint16(d.ax[s]))<<16, false)
	case op&0xFE00 == 0x7400: // INCM
		d.acc[acc] = d.aluAddSub(d.acc[acc], 1<<16, false)
	case op&0xFE00 == 0x7600: // INC
		d.acc[acc] = d.aluAddSub(d.acc[acc], 1, false)
	case op&0xFE00 == 0x7800: // DECM
		d.acc[acc] = d.aluAddSub(d.acc[acc], 1<<16, true)
	case op&0xFE00 == 0x7A00: // DEC
		d.acc[acc] = d.aluAddSub(d.acc[acc], 1, true)
	case op&0xFE00 == 0x7C00: // NEG
		d.acc[acc] = d.aluAddSub(0, d.acc[acc], true)
	case op&0xFE00 == 0x7E00: // MOVNP
		d.acc[acc] = norm40(-d.prod)
		d.setAccFlags(acc)

	// Group 8: clears, compares, tests, config.
	case op&0xF700 == 0x8000: // NX: extended op only
	case op&0xF700 == 0x8100: // CLR
		i := int((op >> 11) & 1)
		d.acc[i] = 0
		d.setAccFlags(i)
	case op&0xFF00 == 0x8200: // CMP $ac0, $ac1
		d.aluAddSub(d.acc[0], d.acc[1], true)
	case op&0xFF00 == 0x8300: // MULAXH
		h := int64(int16(d.ax[0] >> 16))
		d.setProd(h * h)
	case op&0xFF00 == 0x8400: // CLRP
		d.prod = 0
	case op&0xFF00 == 0x8500: // TSTPROD
		d.setValueFlags(norm40(d.prod))
		if d.prod == 0 {
			d.sr |= srZero
		} else {
			d.sr &^= srZero
		}
	case op&0xFE00 == 0x8600: // TSTAXH
		s := int((op >> 8) & 1)
		d.setValueFlags(int64(int16(d.ax[s]>>16)) << 16)
	case op&0xFF00 == 0x8A00: // M2
		d.prodTimes2 = true
	case op&0xFF00 == 0x8B00: // M0
		d.prodTimes2 = false
	case op&0xFF00 == 0x8C00: // CLR15: signed multiply mode
		d.mulUnsigned = false
	case op&0xFF00 == 0x8D00: // SET15: unsigned low word in MUL
		d.mulUnsigned = true
	case op&0xFF00 == 0x8E00: // SET16: 32 bit accumulator mode
		d.sr &^= srAccMode
	case op&0xFF00 == 0x8F00: // SET40: 40 bit accumulator mode
		d.sr |= srAccMode

	// Group 9: MUL and its move/accumulate variants.
	case op&0xF700 == 0x9000: // MUL $axS.l * $axS.h
		s := int((op >> 11) & 1)
		d.setProd(d.mulPair(s))
	case op&0xF700 == 0x9100: // ASR16
		i := int((op >> 11) & 1)
		d.acc[i] = norm40(d.acc[i] >> 16)
		d.setAccFlags(i)
	case op&0xF600 == 0x9200: // MULMVZ
		s, r := int((op>>11)&1), int((op>>8)&1)
		d.acc[r] = norm40(d.prod &^ 0xFFFF)
		d.setProd(d.mulPair(s))
		d.setAccFlags(r)
	case op&0xF600 == 0x9400: // MULAC
		s, r := int((op>>11)&1), int((op>>8)&1)
		d.acc[r] = d.aluAddSub(d.acc[r], norm40(d.prod), false)
		d.setProd(d.mulPair(s))
	case op&0xF600 == 0x9600: // MULMV
		s, r := int((op>>11)&1), int((op>>8)&1)
		d.acc[r] = norm40(d.prod)
		d.setProd(d.mulPair(s))
		d.setAccFlags(r)

	// Groups A and B: cross multiplies.
	case op&0xF700 == 0xA100: // ABS
		i := int((op >> 11) & 1)
		if d.acc[i] < 0 {
			d.acc[i] = norm40(-d.acc[i])
		}
		d.setAccFlags(i)
	case op&0xE700 == 0xA000: // MULX
		d.setProd(d.mulCross(op))
	case op&0xE600 == 0xA200: // MULXMVZ
		r := int((op >> 8) & 1)
		d.acc[r] = norm40(d.prod &^ 0xFFFF)
		d.setProd(d.mulCross(op))
		d.setAccFlags(r)
	case op&0xE600 == 0xA400: // MULXAC
		r := int((op >> 8) & 1)
		d.acc[r] = d.aluAddSub(d.acc[r], norm40(d.prod), false)
		d.setProd(d.mulCross(op))
	case op&0xE600 == 0xA600: // MULXMV
		r := int((op >> 8) & 1)
		d.acc[r] = norm40(d.prod)
		d.setProd(d.mulCross(op))
		d.setAccFlags(r)
	case op&0xF700 == 0xB100: // TST
		i := int((op >> 11) & 1)
		d.setAccFlags(i)

	// Groups C and D: multiplies against ACC.M.
	case op&0xE700 == 0xC100: // CMPAXH
		s, r := int((op>>12)&1), int((op>>11)&1)
		d.aluAddSub(d.acc[s], int64(int16(d.ax[r]>>16))<<16, true)
	case op&0xE700 == 0xC000: // MULC
		d.setProd(d.mulC(op))
	case op&0xE600 == 0xC200: // MULCMVZ
		r := int((op >> 8) & 1)
		d.acc[r] = norm40(d.prod &^ 0xFFFF)
		d.setProd(d.mulC(op))
		d.setAccFlags(r)
	case op&0xE600 == 0xC400: // MULCAC
		r := int((op >> 8) & 1)
		d.acc[r] = d.aluAddSub(d.acc[r], norm40(d.prod), false)
		d.setProd(d.mulC(op))
	case op&0xE600 == 0xC600: // MULCMV
		r := int((op >> 8) & 1)
		d.acc[r] = norm40(d.prod)
		d.setProd(d.mulC(op))
		d.setAccFlags(r)

	// Group E: multiply-accumulate into PROD.
	case op&0xFC00 == 0xE000: // MADDX
		d.prod = norm48(d.prod + d.mulXParts(op))
	case op&0xFC00 == 0xE400: // MSUBX
		d.prod = norm48(d.prod - d.mulXParts(op))
	case op&0xFC00 == 0xE800: // MADDC
		d.prod = norm48(d.prod + d.mulCParts(op))
	case op&0xFC00 == 0xEC00: // MSUBC
		d.prod = norm48(d.prod - d.mulCParts(op))

	// Group F.
	case op&0xFE00 == 0xF000: // LSL16
		d.acc[acc] = norm40(d.acc[acc] << 16)
		d.setAccFlags(acc)
	case op&0xFE00 == 0xF200: // MADD
		s := int((op >> 8) & 1)
		d.prod = norm48(d.prod + d.mulPair(s))
	case op&0xFE00 == 0xF400: // LSR16
		d.acc[acc] = int64(uint64(d.acc[acc]) & uint64(mask40) >> 16)
		d.setAccFlags(acc)
	case op&0xFE00 == 0xF600: // MSUB
		s := int((op >> 8) & 1)
		d.prod = norm48(d.prod - d.mulPair(s))
	case op&0xFC00 == 0xF800: // ADDPAXZ
		dd, s := int((op>>9)&1), int((op>>8)&1)
		v := (d.prod + int64(int16(d.ax[s]>>16))<<16) &^ 0xFFFF
		d.acc[dd] = norm40(v)
		d.setAccFlags(dd)
	case op&0xFE00 == 0xFC00: // CLRL
		i := int((op >> 11) & 1)
		d.acc[i] = norm40(d.acc[i] &^ 0xFFFF)
		d.setAccFlags(i)
	case op&0xFE00 == 0xFE00: // MOVPZ
		d.acc[acc] = norm40(d.prod &^ 0xFFFF)
		d.setAccFlags(acc)
	}
}

// mulPair multiplies AX s low by AX s high. In the
// default CLR15 mode both words are signed; SET15
// makes the low word unsigned.
func (d *DSP) mulPair(s int) int64 {
	hi := int64(int16(d.ax[s] >> 16))
	var lo int64
	if d.mulUnsigned {
		lo = int64(uint16(d.ax[s]))
	} else {
		lo = int64(int16(d.ax[s]))
	}
	return lo * hi
}

// mulCross multiplies an AX0 part by an AX1 part, the
// part selects coming from bits 12 and 11.
func (d *DSP) mulCross(op uint16) int64 {
	a := d.axPart(0, op&0x1000 != 0)
	b := d.axPart(1, op&0x0800 != 0)
	return a * b
}

// mulC multiplies ACC.M (bit 12 selects the
// accumulator) by AX high (bit 11 selects AX).
func (d *DSP) mulC(op uint16) int64 {
	s := int((op >> 12) & 1)
	t := int((op >> 11) & 1)
	return d.accM(s) * int64(int16(d.ax[t]>>16))
}

// mulXParts is the MADDX/MSUBX product term: AX0 part
// from bit 9, AX1 part from bit 8.
func (d *DSP) mulXParts(op uint16) int64 {
	a := d.axPart(0, op&0x0200 != 0)
	b := d.axPart(1, op&0x0100 != 0)
	return a * b
}

// mulCParts is the MADDC/MSUBC product term: ACC.M
// from bit 9, AX high from bit 8.
func (d *DSP) mulCParts(op uint16) int64 {
	s := int((op >> 9) & 1)
	t := int((op >> 8) & 1)
	return d.accM(s) * int64(int16(d.ax[t]>>16))
}

// execExt executes one parallel extended opcode. The
// encodings are the published extension table: address
// register post modification, register moves, and the
// paired load/store forms that run alongside a main
// operation.
func (d *DSP) execExt(ext uint8) {
	switch {
	case ext&0xFC == 0x04: // DR
		d.wrapAdd(int(ext&3), -1)
	case ext&0xFC == 0x08: // IR
		d.wrapAdd(int(ext&3), 1)
	case ext&0xFC == 0x0C: // NR
		i := int(ext & 3)
		d.wrapAdd(i, int32(d.ix[i]))
	case ext&0xF0 == 0x10: // MV $(D+24), $(S+28)
		d.setReg(24+int((ext>>2)&3), d.getReg(28+int(ext&3)))
	case ext&0xE4 == 0x20: // S: MEM[$arD++] = $(S+28)
		ai := int(ext & 3)
		d.writeData(d.ar[ai], d.getReg(28+int((ext>>3)&3)))
		d.postMod(ai, 1)
	case ext&0xE4 == 0x24: // SN: store, then $arD += $ixD
		ai := int(ext & 3)
		d.writeData(d.ar[ai], d.getReg(28+int((ext>>3)&3)))
		d.postMod(ai, 3)
	case ext&0xC4 == 0x40: // L: $(D+24) = MEM[$arS++]
		ai := int(ext & 3)
		d.setReg(24+int((ext>>3)&7), d.readData(d.ar[ai]))
		d.postMod(ai, 1)
	case ext&0xC4 == 0x44: // LN: load, then $arS += $ixS
		ai := int(ext & 3)
		d.setReg(24+int((ext>>3)&7), d.readData(d.ar[ai]))
		d.postMod(ai, 3)
	case ext&0xC3 == 0xC3: // LDAX family (bits 1-0 fixed at 3)
		d.execExtLDAX(ext)
	case ext&0xCC == 0xC0: // LD family
		d.execExtLD(ext)
	case ext&0xCE == 0x80 || ext&0xCE == 0x82 || ext&0xCE == 0x84 ||
		ext&0xCE == 0x86 || ext&0xCE == 0x88 || ext&0xCE == 0x8A ||
		ext&0xCE == 0x8C || ext&0xCE == 0x8E: // LS and SL family
		d.execExtLS(ext)
	}
}

// execExtLS handles LS, SL and their N and M variants:
// one side loads a register from MEM[$ar0], the other
// stores ACC.M to MEM[$ar3], with post modification by
// increment or by IX on either side per the variant.
func (d *DSP) execExtLS(ext uint8) {
	reg := 24 + int((ext>>4)&3)
	ac := int(ext & 1)
	variant := (ext >> 1) & 7 // bit 0: SL form, bit 1: N on AR0, bit 2: M on AR3
	mod0, mod3 := 1, 1
	if variant&2 != 0 {
		mod0 = 3
	}
	if variant&4 != 0 {
		mod3 = 3
	}
	if variant&1 == 0 { // LS: load via AR0, store via AR3
		v := d.readData(d.ar[0])
		d.writeData(d.ar[3], uint16(d.accM(ac)))
		d.setReg(reg, v)
	} else { // SL: store via AR0, load via AR3
		m := uint16(d.accM(ac))
		v := d.readData(d.ar[3])
		d.writeData(d.ar[0], m)
		d.setReg(reg, v)
	}
	d.postMod(0, mod0)
	d.postMod(3, mod3)
}

// execExtLD handles LD and variants: two AX parts load
// in parallel, one through $arS (bits 1-0) and one
// through $ar3. The part selects are bits 5 and 4
// (0 low, 1 high).
func (d *DSP) execExtLD(ext uint8) {
	ai := int(ext & 3)
	variant := (ext >> 2) & 3
	modS, mod3 := 1, 1
	if variant&1 != 0 {
		modS = 3
	}
	if variant&2 != 0 {
		mod3 = 3
	}
	v0 := d.readData(d.ar[ai])
	v1 := d.readData(d.ar[3])
	d.setAXPart(0, ext&0x20 != 0, v0)
	d.setAXPart(1, ext&0x10 != 0, v1)
	if ai != 3 {
		d.postMod(ai, modS)
	}
	d.postMod(3, mod3)
}

// execExtLDAX handles LDAX and variants: both halves
// of one AX register load in parallel, the high half
// through $arS and the low half through $ar3.
func (d *DSP) execExtLDAX(ext uint8) {
	r := int((ext >> 4) & 1)
	ai := int((ext >> 5) & 1)
	variant := (ext >> 2) & 3
	modS, mod3 := 1, 1
	if variant&1 != 0 {
		modS = 3
	}
	if variant&2 != 0 {
		mod3 = 3
	}
	hi := d.readData(d.ar[ai])
	lo := d.readData(d.ar[3])
	d.setAXPart(r, true, hi)
	d.setAXPart(r, false, lo)
	if ai != 3 {
		d.postMod(ai, modS)
	}
	d.postMod(3, mod3)
}

func (d *DSP) setAXPart(i int, high bool, v uint16) {
	if high {
		d.setReg(26+i, v)
		return
	}
	d.setReg(24+i, v)
}
