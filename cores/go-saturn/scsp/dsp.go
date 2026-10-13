package scsp

import (
	"encoding/binary"
)

// The SCSP effect DSP (FH-1) subset implemented here.
//
// ST-77 documents the DSP register areas, its internal buffers
// and the ring buffer used for delays, but it does not publish
// the FH-1 microinstruction encoding. This package therefore
// executes a documented subset encoding of its own design, using
// the manual's data set: COEF (64 signed 13 bit coefficients in
// Q12), MADRS (32 sound RAM address entries), TEMP (128 words of
// 24 bit work RAM whose base pointer decreases by one every
// sample, per the manual), MEMS (32 inputs), MIXS (16 mix inputs
// from the slots), EFREG (16 outputs) and EXTS (2 CD inputs).
//
// Per sample, MEMS is loaded as: MEMS 0 to 15 are the MIXS slot
// sends for this sample, MEMS 16 and 17 are EXTS (CD left and
// right), and MEMS 18 to 31 start at 0 and are the destinations
// for memory reads. TEMP, EFREG and the accumulator persist
// across samples. All values are held in sample units (a full
// scale sample is 32767) inside 24 bit containers; memory reads
// and writes move one 16 bit big endian sound RAM word.
//
// One microprogram step is four 16 bit words in MPRO at page
// offset 800H plus 8 times the step index. Execution runs steps
// 0 to 127 and stops early at the first all zero step, which is
// the no-op padding.
//
//	word 0: bits 15 to 12 destination (0 none, 1 TEMP, 2 EFREG,
//	  3 MEMS, 4 sound RAM write through MADRS), bits 11 to 5
//	  destination index, bits 4 to 1 ALU operation, bit 0 MUL
//	  enable.
//	word 1: bits 15 to 14 source kind (0 MEMS, 1 TEMP, 2 MIXS,
//	  3 EFREG), bit 13 source extension (when set: 4 EXTS,
//	  5 last memory read result, 6 zero, 7 immediate from
//	  word 3), bits 12 to 6 source index, bits 5 to 0 COEF
//	  index.
//	word 2: bit 15 memory read enable, bits 12 to 8 MEMS index
//	  the read lands in, bits 7 to 2 MADRS index.
//	word 3: bits 7 to 0 immediate byte (sign extended), the
//	  remaining bits are unused in this subset.
//
// Per step: the source value X is selected. If MUL is enabled the
// operand P is (X times COEF) shifted right 12 (the Q12 product);
// otherwise P is X. The ALU then applies its operation between
// the accumulator and P: 0 none (accumulator unchanged), 1 load,
// 2 add, 3 subtract, 4 and, 5 or, 6 xor, 7 shift left 1, 8
// arithmetic shift right 1 (the shifts ignore the accumulator).
// The destination, if any, receives the accumulator after the
// ALU step, except destination 4 which writes the accumulator as
// one sound RAM word at the MADRS address plus the memory phase.
//
// Memory addressing: the word address is the ring base from RBP
// plus the MADRS entry plus the memory phase, wrapped to the
// ring length. The memory phase advances by one word per sample.
// The ring length is RBL (8K, 16K, 32K or 64K words) unless
// COEF 63 is nonzero, in which case COEF 63 is the ring length in
// words; that extension exists so small delay lines are usable
// and testable, and it is the one deliberate departure from the
// manual data set in this subset. A read in a step happens before
// that step's source selection result is used by later steps, so
// a program can read a delay tap into MEMS and multiply it in the
// following step.
//
// Not in the subset, stored only in the page: the Y bus and table
// addressing of the real FH-1, the second accumulator, and any
// conditional execution. Programs using fields outside the list
// above are not executed by this subset.

// DSP destination kinds (word 0 bits 15 to 12).
const (
	dspDestNone = iota
	dspDestTEMP
	dspDestEFREG
	dspDestMEMS
	dspDestMemWrite
)

// DSP ALU operations (word 0 bits 4 to 1).
const (
	dspALUNone = iota
	dspALULoad
	dspALUAdd
	dspALUSub
	dspALUAnd
	dspALUOr
	dspALUXor
	dspALUShl
	dspALUShr
)

// DSP source kinds.
const (
	dspSrcMEMS = iota
	dspSrcTEMP
	dspSrcMIXS
	dspSrcEFREG
	dspSrcEXTS
	dspSrcMemRead
	dspSrcZero
	dspSrcImm
)

type dspState struct {
	acc      int32
	temp     [128]int32
	tempBase int // ring base, decreases by one per sample
	mems     [32]int32
	mixs     [16]int32
	efreg    [16]int32
	exts     [2]int32
	memrd    int32
	memPhase uint32
	samples  uint64
}

// coef reads COEF entry i from the page as a signed 13 bit value.
func (s *SCSP) coef(i int) int32 {
	v := int32(s.reg16(RegCOEF+i*2)) & 0x1FFF
	if v&0x1000 != 0 {
		v -= 0x2000
	}
	return v
}

// madrs reads MADRS entry i from the page (a word address).
func (s *SCSP) madrs(i int) uint32 {
	return uint32(s.reg16(RegMADRS + i*2))
}

// ringWords returns the DSP ring buffer length in words.
func (s *SCSP) ringWords() uint32 {
	if c := s.reg16(RegCOEF + 63*2); c != 0 {
		return uint32(c)
	}
	rbl := (s.reg16(RegRing) >> 12) & 3
	return 8192 << uint(rbl)
}

// ringBaseWords returns the DSP ring buffer base in words, from
// RBP (byte address bits 19 to 13).
func (s *SCSP) ringBaseWords() uint32 {
	rbp := uint32(s.reg16(RegRing) & 0x7F)
	return rbp << 12
}

func (d *dspState) memAddr(s *SCSP, madrsIdx int) uint32 {
	wrap := s.ringWords()
	if wrap == 0 {
		return 0
	}
	a := s.madrs(madrsIdx) + d.memPhase
	return s.ringBaseWords() + a%wrap
}

// runDSP executes this sample's microprogram. mixs holds the 16
// slot sends computed by renderSlots.
func (s *SCSP) runDSP(mixs [16]int32) {
	d := &s.dsp
	d.mixs = mixs
	for i := 0; i < 16; i++ {
		d.mems[i] = mixs[i]
	}
	d.mems[16] = d.exts[0]
	d.mems[17] = d.exts[1]
	for i := 18; i < 32; i++ {
		d.mems[i] = 0
	}
	d.memrd = 0
	d.tempBase = (d.tempBase - 1) & 127

	for step := 0; step < 128; step++ {
		off := RegMPRO + step*8
		if off+7 >= len(s.regs) {
			break
		}
		w0 := s.reg16(off)
		w1 := s.reg16(off + 2)
		w2 := s.reg16(off + 4)
		w3 := s.reg16(off + 6)
		if w0|w1|w2|w3 == 0 {
			break
		}
		s.dspStep(w0, w1, w2, w3)
	}

	if wrap := s.ringWords(); wrap != 0 {
		d.memPhase = (d.memPhase + 1) % wrap
	}
	d.samples++
}

func (s *SCSP) dspStep(w0, w1, w2, w3 uint16) {
	d := &s.dsp

	// Memory read first, so later steps (and this step's MEMRD
	// source) can use the value.
	if w2&0x8000 != 0 {
		madrsIdx := int(w2>>2) & 0x3F
		if madrsIdx > 31 {
			madrsIdx = 31
		}
		addr := d.memAddr(s, madrsIdx)
		var v int32
		if int(addr)*2+1 < len(s.ram) {
			v = int32(int16(binary.BigEndian.Uint16(s.ram[addr*2 : addr*2+2])))
		}
		d.mems[int(w2>>8)&0x1F] = v
		d.memrd = v
	}

	srcKind := int(w1>>14) & 3
	if w1&0x2000 != 0 {
		srcKind += 4
	}
	srcIdx := int(w1>>6) & 0x7F
	var x int32
	switch srcKind {
	case dspSrcMEMS:
		x = d.mems[srcIdx&31]
	case dspSrcTEMP:
		x = d.temp[(d.tempBase+srcIdx)&127]
	case dspSrcMIXS:
		x = d.mixs[srcIdx&15]
	case dspSrcEFREG:
		x = d.efreg[srcIdx&15]
	case dspSrcEXTS:
		x = d.exts[srcIdx&1]
	case dspSrcMemRead:
		x = d.memrd
	case dspSrcZero:
		x = 0
	case dspSrcImm:
		x = int32(int8(w3))
	}

	p := x
	if w0&1 != 0 {
		coefIdx := int(w1) & 0x3F
		p = int32((int64(x) * int64(s.coef(coefIdx))) >> 12)
	}

	switch int(w0>>1) & 0xF {
	case dspALULoad:
		d.acc = p
	case dspALUAdd:
		d.acc += p
	case dspALUSub:
		d.acc -= p
	case dspALUAnd:
		d.acc &= p
	case dspALUOr:
		d.acc |= p
	case dspALUXor:
		d.acc ^= p
	case dspALUShl:
		d.acc = p << 1
	case dspALUShr:
		d.acc = p >> 1
	}
	d.acc = clamp24(d.acc)

	destIdx := int(w0>>5) & 0x7F
	switch int(w0>>12) & 0xF {
	case dspDestTEMP:
		d.temp[(d.tempBase+destIdx)&127] = d.acc
	case dspDestEFREG:
		d.efreg[destIdx&15] = d.acc
	case dspDestMEMS:
		d.mems[destIdx&31] = d.acc
	case dspDestMemWrite:
		madrsIdx := destIdx & 31
		addr := d.memAddr(s, madrsIdx)
		if int(addr)*2+1 < len(s.ram) {
			v := uint16(int16(clamp16(int64(d.acc))))
			binary.BigEndian.PutUint16(s.ram[addr*2:addr*2+2], v)
		}
	}
}

// clamp24 keeps a DSP value inside the signed 24 bit container.
func clamp24(v int32) int32 {
	if v > 0x7FFFFF {
		return 0x7FFFFF
	}
	if v < -0x800000 {
		return -0x800000
	}
	return v
}

// writePage mirrors the DSP internal buffers into the page areas
// the manual defines: TEMP as two words per entry (low byte in
// the first word, bits 23 to 8 in the second), MEMS the same,
// MIXS the same, EFREG one word per entry, and EXTS one word per
// entry.
func (d *dspState) writePage(s *SCSP) {
	put24 := func(off int, v int32) {
		if off+3 < len(s.regs) {
			s.setReg16(off, uint16(v&0xFF))
			s.setReg16(off+2, uint16((v>>8)&0xFFFF))
		}
	}
	for i := 0; i < 128; i++ {
		put24(RegTEMP+i*4, d.temp[(d.tempBase+i)&127])
	}
	for i := 0; i < 32; i++ {
		put24(RegMEMS+i*4, d.mems[i])
	}
	for i := 0; i < 16; i++ {
		put24(RegMIXS+i*4, d.mixs[i])
	}
	for i := 0; i < 16; i++ {
		if RegEFREG+i*2+1 < len(s.regs) {
			s.setReg16(RegEFREG+i*2, uint16(int16(clamp16(int64(d.efreg[i])))))
		}
	}
	for i := 0; i < 2; i++ {
		if RegEXTS+i*2+1 < len(s.regs) {
			s.setReg16(RegEXTS+i*2, uint16(int16(d.exts[i])))
		}
	}
}
