package scu

// SCU DSP.
//
// Memories, held in the SCU struct (not on the page):
// program RAM is 256 words of 32 bits; data RAM is four
// banks of 64 words of 32 bits.
//
// Control ports on the page, per the manual:
//
//	0x80 DSP program control port (the brief's PPAF):
//	     bits 7..0 program RAM address, bit 15 LE (load
//	     the address into the program counter), bit 16
//	     EX (program execute control), bit 17 ES (step
//	     execute, not modeled beyond storage), bit 18 E
//	     (program end flag, set by ENDI), bit 19 V,
//	     bit 20 C, bit 21 Z, bit 22 S, bit 23 T0 (D0 bus
//	     DMA in progress), bit 25 EP, bit 26 PR.
//	0x84 DSP program RAM data port (the brief's PPAC):
//	     write only. Writing a word stores it in program
//	     RAM at the address in the control port, and the
//	     address counts up by one.
//	0x88 DSP data RAM address port (the brief's PDAC):
//	     bits 7..6 select the data RAM bank, bits 5..0
//	     the word address in the bank.
//	0x8C DSP data RAM data port (the brief's transfer
//	     register): read and write. Accessing it moves
//	     one word between the CPU and the selected data
//	     RAM word, and the address counts up by one.
//
// Load mechanism in this passive page model: the bus
// page has no write strobes, so the ports are consumed
// by syncDSPPorts at the start of Poll and RunDSP.
// Software writes a port, then a Poll (in the machine,
// the per scanline Poll) consumes it. The program data
// port is cleared to zero after it is consumed, so
// consecutive writes of the same nonzero word are each
// seen; a written word of zero is indistinguishable
// from no write when the port already reads zero, and
// program RAM already reads zero there in the common
// case, which is the documented limit of this model.
// The data data port presents the selected RAM word
// after the address port changes (a read setup), and a
// changed data port value with no address change is a
// write, which stores the word and counts the address
// up. Direct accessors (DSPWriteProgram and friends)
// are provided for tests and tooling that do not want
// to drive the ports.
//
// Execution: when EX is set, Poll runs the DSP with a
// large step budget, and RunDSP runs it on demand.
// Execution starts at the program RAM address in the
// control port when EX rises. The END command stops
// execution and clears EX. ENDI also sets the E flag in
// the control port and raises IntDSPEnd. There are no
// goroutines; execution is deterministic and atomic to
// the caller.
//
// Instruction encoding, per chapter 4 of the manual.
// The top bits select the class:
//
//	0b00 operation command (parallel ALU, X bus, Y bus,
//	     and D1 bus fields in one word)
//	0b10 load immediate (MVI)
//	0b1100 DMA command
//	0b1101 jump command
//	0b1110 loop bottom command (BTM, LPS)
//	0b1111 end command (END, ENDI)
//
// Operation command fields: ALU bits 29..26, X bus bits
// 25..20 (opcode bits 25..23, source bits 22..20), Y
// bus bits 19..14 (opcode bits 19..17, source bits
// 16..14), D1 bus bits 13..0. A source select of the
// form 1bb picks data RAM bank bb and increments its CT
// pointer after the transfer (the MC form); 0bb picks
// the bank without incrementing (the M form).
//
// ALU opcodes (bits 29..26): 0 NOP, 1 AND, 2 OR, 3 XOR,
// 4 ADD, 5 SUB, 6 AD2 (48 bit add of A and P), 8 SR
// (arithmetic shift right), 9 RR (rotate right), 10 SL
// (shift left), 11 RL (rotate left), 14 RR8 (rotate
// right 8), 15 RL8 (rotate left 8). Codes 7, 12, and 13
// are not assigned in the manual and act as NOP here.
// Logic, add, and shift results are written to the
// accumulator (the ALU output) in the same step, so a
// parallel MOV ALU, A in the Y field is a copy of the
// same result and never double applies. Flags S, Z, C,
// and V follow the manual: S is the result sign, Z is
// result zero, C is the carry out (for SUB, C is the
// carry out of the subtraction, so 1 means no borrow),
// and V is signed overflow. For AD2 the result and
// flags are 48 bit wide.
//
// X bus opcodes (bits 25..23): 0 NOP, 4 MOV [s], X
// (data RAM word into RX), 2 MOV MUL, P (the signed
// product RX times RY, computed from the RX and RY
// values at the start of the instruction, into P:
// PH takes the high 16 bits of the 48 bit product and
// PL the low 32), 3 MOV [s], P (data RAM word into PL,
// with PH set by sign extension of PL).
//
// Y bus opcodes (bits 19..17): 0 NOP, 4 MOV [s], Y
// (data RAM word into RY), 1 CLR A (A cleared to 0),
// 2 MOV ALU, A (the ALU result of this instruction
// into A), 3 MOV [s], A (data RAM word into ACL, with
// ACH set by sign extension).
//
// D1 bus field (bits 13..0): bits 13..12 select the
// move kind: 0 NOP, 1 MOV SImm, [d] (sign extended 8
// bit immediate in bits 7..0 into the destination in
// bits 11..8), 3 MOV [s], [d] (the source in bits 3..0
// into the destination in bits 11..8; kind 2 is not
// assigned). Destination codes: 0..3 data RAM banks
// 0..3 (written at the bank CT pointer, which then
// increments), 4 RX, 5 PL (PH follows by sign
// extension), 6 RA0, 7 WA0, 8 LOP, 9 TOP, 10..13
// CT0..CT3, 14 PC (MVI only), 15 unused. Source codes
// for MOV [s], [d]: 0..3 data RAM banks (no increment),
// 4..7 data RAM banks (with increment), 8 ACL, 9 ACH
// (sign extended from 16 bits).
//
// MVI format: bits 31..30 are 10, the destination code
// is bits 29..26, bit 25 is 0 for the unconditional
// form (immediate is the signed 25 bit value in bits
// 24..0) and 1 for the conditional form (bit 24 is the
// condition polarity, 1 means the flag must be set;
// bits 23..19 are the condition code: 1 Z, 2 S, 3 Z or
// S, 4 C, 8 T0; the immediate is the signed 19 bit
// value in bits 18..0). Writing PC with MVI stores the
// return address (the address after the MVI) in TOP
// first, which is the manual's subroutine call form.
//
// Jump format: bits 31..28 are 1101. The unconditional
// form has bits 27..21 clear. Conditional forms set
// bit 27, put the polarity in bit 26 (1 means the flag
// must be set), and the condition code in bits 25..21
// (same codes as MVI). The target is the 8 bit program
// address in bits 7..0. The manual's prefetch delay
// (the instruction after a taken jump executes once)
// is not modeled: a taken jump takes effect at once.
//
// Loop bottom format: bits 31..28 are 1110. Bit 27
// clear is BTM: when LOP is nonzero, LOP decrements
// and the program counter returns to TOP; at LOP zero
// execution falls through. Bit 27 set is LPS: the next
// instruction repeats until LOP reaches zero, so it
// executes LOP plus one times in total.
//
// DMA command format: bits 31..28 are 1100. Bit 27 is
// the count mode: 0 takes the transfer count from the
// 8 bit immediate in bits 7..0, 1 takes it from a data
// RAM word selected by bits 2..0 (a source select as
// above). Bits 17..15 are the external address add
// code (0, 1, 2, 4, 8, 16, 32, 64 for codes 0..7, in
// word units). Bit 14 is the hold flag: with hold set,
// RA0 or WA0 and the transfer counter keep their start
// values after the transfer. Bit 12 is the direction:
// 0 moves data from the D0 bus (external memory,
// starting at the byte address RA0 times 4) into DSP
// RAM, 1 moves data from DSP RAM to the D0 bus
// (starting at WA0 times 4). Bits 10..8 select the DSP
// RAM: 0..3 are data RAM banks (the transfer starts at
// the bank CT pointer, which advances one per word),
// and 4 selects program RAM (the transfer starts at an
// internal program DMA pointer that starts at 0 and
// advances one per word). RA0 and WA0 hold word
// addresses (the manual stores the byte address shifted
// right 2), and TN0 holds the count while a transfer
// runs; transfers here are atomic, so TN0 reads 0 after
// a non hold transfer and keeps the count after a hold
// transfer. A count of 0 transfers nothing. Without an
// attached Mem a DMA command does nothing.
//
// Registers: RX and RY are the multiplier inputs (the
// X and Y of the brief), P is the 48 bit product
// register (PH and PL views), A is the 48 bit
// accumulator (ACH and ACL views), CT0..CT3 are the
// 6 bit data RAM pointers, RA0 and WA0 are the external
// DMA address registers, and PC, TOP, and LOP are the
// program counter, loop top, and 12 bit loop counter.

// DSP control port offsets on the page.
const (
	offPPAF = 0x80
	offPPD  = 0x84
	offPDA  = 0x88
	offPDD  = 0x8C
)

// PPAF bit numbers.
const (
	ppafLE = 15
	ppafEX = 16
	ppafE  = 18
	ppafV  = 19
	ppafC  = 20
	ppafZ  = 21
	ppafS  = 22
	ppafT0 = 23
)

// pollDSPBudget is the step budget Poll gives the DSP.
// Programs that do not end within the budget stay
// running and continue on the next Poll.
const pollDSPBudget = 100000

// dspState is the off page DSP state.
type dspState struct {
	prog [256]uint32
	data [4][64]uint32

	rx, ry uint32
	p      uint64 // 48 bit product: PH in bits 47..32, PL in 31..0
	acc    uint64 // 48 bit accumulator: ACH in bits 47..32, ACL in 31..0
	ct     [4]uint8
	ra0    uint32
	wa0    uint32
	tn0    uint32
	pc     uint8
	top    uint8
	lop    uint16 // 12 bit

	flagS, flagZ, flagC, flagV, flagT0 bool
	endFlag                            bool

	running bool
	lastEX  bool

	// LPS repeat state: the address of the instruction
	// being repeated, valid while repeatActive is set.
	repeatAddr   uint8
	repeatActive bool
	lpsPending   bool

	// Program RAM DMA pointer for DMA commands that
	// select program RAM.
	dmaProgAddr uint32

	// Data port pointer selected by the address port:
	// bits 7..6 bank, bits 5..0 word address.
	dataPtr uint8
}

// DSPProgram returns the program RAM word at addr.
func (s *SCU) DSPProgram(addr int) uint32 {
	if addr < 0 || addr >= 256 {
		return 0
	}
	return s.dsp.prog[addr]
}

// DSPWriteProgram stores a word in program RAM directly,
// bypassing the load ports.
func (s *SCU) DSPWriteProgram(addr int, v uint32) {
	if addr < 0 || addr >= 256 {
		return
	}
	s.dsp.prog[addr] = v
}

// DSPData returns the data RAM word in a bank.
func (s *SCU) DSPData(bank, addr int) uint32 {
	if bank < 0 || bank >= 4 || addr < 0 || addr >= 64 {
		return 0
	}
	return s.dsp.data[bank][addr]
}

// DSPWriteData stores a word in data RAM directly,
// bypassing the load ports.
func (s *SCU) DSPWriteData(bank, addr int, v uint32) {
	if bank < 0 || bank >= 4 || addr < 0 || addr >= 64 {
		return
	}
	s.dsp.data[bank][addr] = v
}

// DSPAccumulator returns the 48 bit accumulator.
func (s *SCU) DSPAccumulator() uint64 { return s.dsp.acc & 0xFFFFFFFFFFFF }

// DSPProduct returns the 48 bit product register.
func (s *SCU) DSPProduct() uint64 { return s.dsp.p & 0xFFFFFFFFFFFF }

// DSPRunning reports whether the DSP is executing.
func (s *SCU) DSPRunning() bool { return s.dsp.running }

// syncDSPPorts consumes the DSP load ports on the page
// and the control port strobes. See the package comment
// at the top of this file for the load model.
func (s *SCU) syncDSPPorts() {
	d := &s.dsp
	ppaf := s.word(offPPAF)

	// Program data port: a nonzero value that differs
	// from the last consumed value (or any value when
	// the port holds an unconsumed write) is a load.
	if v := s.word(offPPD); v != 0 && (!s.progPortValid || v != s.lastProgPort) {
		addr := uint8(ppaf & 0xFF)
		d.prog[addr] = v
		// The control port address counts up by one.
		ppaf = (ppaf &^ 0xFF) | uint32(addr+1)
		s.setWord(offPPAF, ppaf)
		s.lastProgPort = v
		s.progPortValid = true
		s.setWord(offPPD, 0)
	}

	// Data address port: a change selects a new data
	// RAM word and presents it on the data port.
	addrWord := s.word(offPDA)
	dataWord := s.word(offPDD)
	if addrWord != s.lastDataAddr {
		d.dataPtr = uint8(addrWord & 0xFF)
		s.lastDataAddr = addrWord
		if dataWord == s.lastDataData {
			v := d.data[d.dataPtr>>6][d.dataPtr&0x3F]
			s.setWord(offPDD, v)
			s.lastDataData = v
			dataWord = v
		}
	}
	// Data data port: a changed value is a write to the
	// selected word; the address counts up by one.
	if dataWord != s.lastDataData {
		d.data[d.dataPtr>>6][d.dataPtr&0x3F] = dataWord
		s.lastDataData = dataWord
		d.dataPtr = (d.dataPtr & 0xC0) | ((d.dataPtr + 1) & 0x3F)
		s.setWord(offPDA, uint32(d.dataPtr))
		s.lastDataAddr = uint32(d.dataPtr)
	}

	// Control port strobes.
	ppaf = s.word(offPPAF)
	if ppaf&(1<<ppafLE) != 0 && !d.running {
		d.pc = uint8(ppaf & 0xFF)
		s.setWord(offPPAF, ppaf&^(1<<ppafLE))
		ppaf &^= 1 << ppafLE
	}
	ex := ppaf&(1<<ppafEX) != 0
	if ex && !d.lastEX {
		d.pc = uint8(ppaf & 0xFF)
		d.endFlag = false
		d.running = true
	}
	if !ex {
		d.running = false
	}
	d.lastEX = ex
	s.writeDSPFlags()
}

// writeDSPFlags mirrors the DSP flags into the PPAF
// status bits on the page.
func (s *SCU) writeDSPFlags() {
	d := &s.dsp
	ppaf := s.word(offPPAF)
	ppaf &^= 0x3F << 18
	if d.endFlag {
		ppaf |= 1 << ppafE
	}
	if d.flagV {
		ppaf |= 1 << ppafV
	}
	if d.flagC {
		ppaf |= 1 << ppafC
	}
	if d.flagZ {
		ppaf |= 1 << ppafZ
	}
	if d.flagS {
		ppaf |= 1 << ppafS
	}
	if d.flagT0 {
		ppaf |= 1 << ppafT0
	}
	s.setWord(offPPAF, ppaf)
}

// pollDSP runs the DSP from Poll while EX is set.
func (s *SCU) pollDSP() {
	if s.dsp.running {
		s.runDSPSteps(pollDSPBudget)
	}
}

// RunDSP runs the DSP for at most maxSteps instructions
// and returns how many instructions executed. It starts
// from the current program counter; when the control
// port EX flag has just been set, the start address is
// the control port address, as in Poll. Execution stops
// early at an END or ENDI command. A nonpositive
// maxSteps runs nothing.
func (s *SCU) RunDSP(maxSteps int) int {
	s.syncDSPPorts()
	if maxSteps <= 0 {
		return 0
	}
	if !s.dsp.running {
		// A direct RunDSP call is an execution request
		// from the current program counter even when EX
		// was not raised through the page. If the budget
		// runs out before an END command, the DSP stays
		// running so Poll or a later RunDSP continues
		// where this call stopped.
		s.dsp.running = true
	}
	return s.runDSPSteps(maxSteps)
}

// runDSPSteps executes up to maxSteps instructions.
func (s *SCU) runDSPSteps(maxSteps int) int {
	n := 0
	for n < maxSteps && s.dsp.running {
		s.stepDSP()
		n++
	}
	s.writeDSPFlags()
	return n
}

// condTrue evaluates a condition code with a polarity
// against the DSP flags. Condition codes: 1 Z, 2 S,
// 3 Z or S, 4 C, 8 T0. Unknown codes are false.
func (s *SCU) condTrue(code uint32, polarity bool) bool {
	d := &s.dsp
	var v bool
	switch code {
	case 1:
		v = d.flagZ
	case 2:
		v = d.flagS
	case 3:
		v = d.flagZ || d.flagS
	case 4:
		v = d.flagC
	case 8:
		v = d.flagT0
	default:
		return false
	}
	if polarity {
		return v
	}
	return !v
}

// signExtend returns v sign extended from the given
// bit width.
func signExtend(v uint32, bits uint) int32 {
	shift := 32 - bits
	return int32(v<<shift) >> shift
}

// stepDSP executes one instruction at the program
// counter.
func (s *SCU) stepDSP() {
	d := &s.dsp
	instr := d.prog[d.pc]
	curPC := d.pc
	d.pc++

	// LPS: this instruction is the repeated one. The
	// repeat bookkeeping runs after it executes.
	repeating := d.lpsPending || (d.repeatActive && curPC == d.repeatAddr)
	if d.lpsPending {
		d.repeatAddr = curPC
		d.repeatActive = true
		d.lpsPending = false
	}

	jumped := false
	switch {
	case instr>>30 == 0:
		s.execOperation(instr)
	case instr>>30 == 2:
		jumped = s.execMVI(instr)
	case instr>>28 == 0xC:
		s.execDSPDMA(instr)
	case instr>>28 == 0xD:
		if instr&(1<<27) == 0 || s.condTrue((instr>>21)&0x1F, instr&(1<<26) != 0) {
			d.pc = uint8(instr & 0xFF)
			jumped = true
		}
	case instr>>28 == 0xE:
		if instr&(1<<27) != 0 {
			// LPS: repeat the next instruction.
			d.lpsPending = true
		} else {
			// BTM.
			if d.lop > 0 {
				d.lop--
				d.pc = d.top
				jumped = true
			}
		}
	case instr>>28 == 0xF:
		d.running = false
		ppaf := s.word(offPPAF)
		s.setWord(offPPAF, ppaf&^(1<<ppafEX))
		d.lastEX = false
		if instr&(1<<27) != 0 {
			// ENDI.
			d.endFlag = true
			s.Raise(IntDSPEnd)
		}
	default:
		// Class 0b11 with an unassigned top nibble and
		// class 0b01 act as NOP.
	}

	if repeating && d.running && !jumped {
		if d.lop > 0 {
			d.lop--
			d.pc = d.repeatAddr
		} else {
			d.repeatActive = false
		}
	}
	if jumped {
		d.repeatActive = false
		d.lpsPending = false
	}
}

// readRAMSource reads a data RAM word for a 3 bit
// source select (1bb increments the CT pointer, 0bb
// does not), using the CT values from before the
// instruction.
func (s *SCU) readRAMSource(sel uint32, ctSnapshot [4]uint8) uint32 {
	d := &s.dsp
	bank := int(sel & 3)
	v := d.data[bank][ctSnapshot[bank]&0x3F]
	if sel&4 != 0 {
		d.ct[bank] = (ctSnapshot[bank] + 1) & 0x3F
	}
	return v
}

// setP sets the product register from a 32 bit value in
// PL, with PH following by sign extension.
func (s *SCU) setP32(v uint32) {
	d := &s.dsp
	ph := uint64(0)
	if v&0x80000000 != 0 {
		ph = 0xFFFF
	}
	d.p = ph<<32 | uint64(v)
}

// setAcc32 sets the accumulator from a 32 bit value in
// ACL, with ACH following by sign extension.
func (s *SCU) setAcc32(v uint32) {
	d := &s.dsp
	ah := uint64(0)
	if v&0x80000000 != 0 {
		ah = 0xFFFF
	}
	d.acc = ah<<32 | uint64(v)
}

// execOperation executes an operation command word.
func (s *SCU) execOperation(instr uint32) {
	d := &s.dsp
	ctSnapshot := d.ct
	acl := uint32(d.acc)
	ach := uint32(d.acc >> 32)
	pl := uint32(d.p)

	// ALU field.
	alu := (instr >> 26) & 0xF
	aluResult := d.acc
	aluChanged := false
	switch alu {
	case 0: // NOP
	case 1: // AND
		r := acl & pl
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagC = false
		d.flagV = false
	case 2: // OR
		r := acl | pl
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagC = false
		d.flagV = false
	case 3: // XOR
		r := acl ^ pl
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagC = false
		d.flagV = false
	case 4: // ADD
		sum := uint64(acl) + uint64(pl)
		r := uint32(sum)
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagC = sum>>32 != 0
		d.flagV = (acl^r)&(pl^r)&0x80000000 != 0
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
	case 5: // SUB
		diff := uint64(acl) - uint64(pl)
		r := uint32(diff)
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagC = acl >= pl
		d.flagV = (acl^pl)&(acl^r)&0x80000000 != 0
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
	case 6: // AD2: 48 bit A plus P.
		aBefore := d.acc & 0xFFFFFFFFFFFF
		pBefore := d.p & 0xFFFFFFFFFFFF
		sum := aBefore + pBefore
		r := sum & 0xFFFFFFFFFFFF
		d.acc = r
		aluResult = r
		aluChanged = true
		d.flagC = sum>>48 != 0
		d.flagS = r&(1<<47) != 0
		d.flagZ = r == 0
		// Signed overflow: the operands share a sign
		// that the result does not.
		d.flagV = (aBefore>>47)&1 == (pBefore>>47)&1 &&
			(r>>47)&1 != (aBefore>>47)&1
	case 8: // SR: arithmetic shift right.
		r := (acl >> 1) | (acl & 0x80000000)
		d.flagC = acl&1 != 0
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagV = false
	case 9: // RR: rotate right.
		r := (acl >> 1) | ((acl & 1) << 31)
		d.flagC = acl&1 != 0
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagV = false
	case 10: // SL: shift left.
		r := acl << 1
		d.flagC = acl&0x80000000 != 0
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagV = false
	case 11: // RL: rotate left.
		r := (acl << 1) | (acl >> 31)
		d.flagC = acl&0x80000000 != 0
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagV = false
	case 14: // RR8: rotate right 8.
		r := (acl >> 8) | (acl << 24)
		d.flagC = acl&(1<<7) != 0
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagV = false
	case 15: // RL8: rotate left 8.
		r := (acl << 8) | (acl >> 24)
		d.flagC = acl&(1<<24) != 0
		s.setAcc32(r)
		aluResult = d.acc
		aluChanged = true
		d.flagS = r&0x80000000 != 0
		d.flagZ = r == 0
		d.flagV = false
	default:
		// Unassigned ALU codes act as NOP.
	}
	_ = aluChanged

	// X bus field.
	xOp := (instr >> 23) & 7
	xSrc := (instr >> 20) & 7
	switch xOp {
	case 4: // MOV [s], X
		d.rx = s.readRAMSource(xSrc, ctSnapshot)
	case 2: // MOV MUL, P: signed product of the RX and
		// RY values from the start of the instruction.
		prod := int64(int32(d.rx)) * int64(int32(d.ry))
		d.p = uint64(prod) & 0xFFFFFFFFFFFF
	case 3: // MOV [s], P
		s.setP32(s.readRAMSource(xSrc, ctSnapshot))
	}

	// Y bus field.
	yOp := (instr >> 17) & 7
	ySrc := (instr >> 14) & 7
	switch yOp {
	case 4: // MOV [s], Y
		d.ry = s.readRAMSource(ySrc, ctSnapshot)
	case 1: // CLR A
		d.acc = 0
	case 2: // MOV ALU, A
		d.acc = aluResult & 0xFFFFFFFFFFFF
	case 3: // MOV [s], A
		s.setAcc32(s.readRAMSource(ySrc, ctSnapshot))
	}

	// D1 bus field.
	d1Kind := (instr >> 12) & 3
	switch d1Kind {
	case 1: // MOV SImm, [d]
		dest := (instr >> 8) & 0xF
		imm := uint32(signExtend(instr&0xFF, 8))
		s.writeDSPDest(dest, imm, false)
	case 3: // MOV [s], [d]
		dest := (instr >> 8) & 0xF
		src := instr & 0xF
		var v uint32
		switch {
		case src < 8:
			bank := int(src & 3)
			v = d.data[bank][ctSnapshot[bank]&0x3F]
			if src&4 != 0 {
				d.ct[bank] = (ctSnapshot[bank] + 1) & 0x3F
			}
		case src == 8:
			v = uint32(d.acc)
		case src == 9:
			v = uint32(int32(int16(ach)))
		default:
			v = 0
		}
		s.writeDSPDest(dest, v, false)
	}
	_ = ach
}

// writeDSPDest writes v to a D1 or MVI destination.
// When fromMVI is set, destination 14 is PC with the
// subroutine return address stored in TOP.
func (s *SCU) writeDSPDest(dest uint32, v uint32, fromMVI bool) {
	d := &s.dsp
	switch {
	case dest < 4:
		bank := int(dest)
		d.data[bank][d.ct[bank]&0x3F] = v
		d.ct[bank] = (d.ct[bank] + 1) & 0x3F
	case dest == 4:
		d.rx = v
	case dest == 5:
		s.setP32(v)
	case dest == 6:
		d.ra0 = v
	case dest == 7:
		d.wa0 = v
	case dest == 8:
		d.lop = uint16(v) & 0xFFF
	case dest == 9:
		d.top = uint8(v)
	case dest >= 10 && dest <= 13:
		d.ct[dest-10] = uint8(v) & 0x3F
	case dest == 14 && fromMVI:
		d.top = d.pc // pc already points past the MVI
		d.pc = uint8(v)
	}
}

// execMVI executes a load immediate command and
// reports whether it wrote PC (a jump).
func (s *SCU) execMVI(instr uint32) bool {
	dest := (instr >> 26) & 0xF
	var imm uint32
	if instr&(1<<25) == 0 {
		imm = uint32(signExtend(instr&0x1FFFFFF, 25))
	} else {
		if !s.condTrue((instr>>19)&0x1F, instr&(1<<24) != 0) {
			return false
		}
		imm = uint32(signExtend(instr&0x7FFFF, 19))
	}
	s.writeDSPDest(dest, imm, true)
	return dest == 14
}

// execDSPDMA executes a DSP DMA command atomically
// through the attached Mem.
func (s *SCU) execDSPDMA(instr uint32) {
	d := &s.dsp
	if s.mem == nil {
		return
	}
	hold := instr&(1<<14) != 0
	dirOut := instr&(1<<12) != 0
	addCode := (instr >> 15) & 7
	addVals := []uint32{0, 1, 2, 4, 8, 16, 32, 64}
	add := addVals[addCode]
	ramSel := (instr >> 8) & 7

	var count uint32
	if instr&(1<<27) == 0 {
		count = instr & 0xFF
	} else {
		sel := instr & 7
		bank := int(sel & 3)
		count = d.data[bank][d.ct[bank]&0x3F]
		if sel&4 != 0 {
			d.ct[bank] = (d.ct[bank] + 1) & 0x3F
		}
	}
	if count == 0 {
		return
	}
	d.tn0 = count
	d.flagT0 = true

	if !dirOut {
		// External memory into DSP RAM, from RA0.
		srcAddr := d.ra0
		for i := uint32(0); i < count; i++ {
			v := s.mem.Read32((srcAddr + i*addOrOne(add)) << 2)
			s.writeDSPRAMWord(ramSel, v)
		}
		if !hold {
			d.ra0 = srcAddr + count*addOrOne(add)
			d.tn0 = 0
		}
	} else {
		// DSP RAM out to external memory, at WA0.
		dstAddr := d.wa0
		for i := uint32(0); i < count; i++ {
			v := s.readDSPRAMWord(ramSel)
			s.mem.Write32((dstAddr+i*addOrOne(add))<<2, v)
		}
		if !hold {
			d.wa0 = dstAddr + count*addOrOne(add)
			d.tn0 = 0
		}
	}
	d.flagT0 = false
}

// addOrOne maps an external address add value of 0 to
// a step of 1 word for sequential DSP DMA transfers in
// this core: the manual's add 0 form is for A-Bus
// devices that sequence internally, and Mem addresses
// must still advance to move a block. (The SCU DMA
// channels in dma.go model add 0 as a true hold.)
func addOrOne(add uint32) uint32 {
	if add == 0 {
		return 1
	}
	return add
}

// writeDSPRAMWord writes one word to the DSP RAM
// selected by a DMA command RAM select, advancing the
// matching pointer.
func (s *SCU) writeDSPRAMWord(sel uint32, v uint32) {
	d := &s.dsp
	if sel == 4 {
		d.prog[d.dmaProgAddr&0xFF] = v
		d.dmaProgAddr++
		return
	}
	bank := int(sel & 3)
	d.data[bank][d.ct[bank]&0x3F] = v
	d.ct[bank] = (d.ct[bank] + 1) & 0x3F
}

// readDSPRAMWord reads one word from the DSP RAM
// selected by a DMA command RAM select, advancing the
// matching pointer.
func (s *SCU) readDSPRAMWord(sel uint32) uint32 {
	d := &s.dsp
	if sel == 4 {
		v := d.prog[d.dmaProgAddr&0xFF]
		d.dmaProgAddr++
		return v
	}
	bank := int(sel & 3)
	v := d.data[bank][d.ct[bank]&0x3F]
	d.ct[bank] = (d.ct[bank] + 1) & 0x3F
	return v
}
