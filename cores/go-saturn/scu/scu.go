// Package scu models the Sega Saturn SCU: the interrupt
// mask and status registers (IMS and IST) on the SCU
// register page, the three DMA channels, the SCU DSP,
// and the two timers.
//
// IMS and IST are 32 bit registers at page offsets 0xA0
// and 0xA4. The interrupt sources occupy the low bits.
// A mask bit of 1 in IMS blocks delivery of that source.
// A status bit of 1 in IST means the source has fired and
// is waiting; software clears a source by writing 0 to
// its IST bit through the bus page. This package is a
// thin view over the bus page for the registers: it keeps
// no shadow copy of register words, so CPU writes to the
// page take effect immediately. State that does not live
// on the page (the DSP program and data RAMs, the DSP
// registers, DMA latches, and timer counters) lives in
// the SCU struct and is covered by SerializeExtra.
//
// Delivery rule: sources are offered in the documented
// SCU priority order (VBlank-IN first, sprite draw end
// last, the A-Bus external input after those) at the per
// source levels of the manual's Table 2.1 (see LevelOf)
// with the standard SCU vector numbers, one source per
// Deliver call. The system calls Deliver once per
// scanline and offers the result to both SH-2s. Because
// IST bits stay set until software clears them, no
// source is lost when a CPU already has a request
// latched; it is offered again on a later line.
//
// The system calls Poll once per scanline to run the DMA
// channels and the DSP; see dma.go, dsp.go, and timers.go.
package scu

import "encoding/binary"

// Interrupt source bit numbers within IMS and IST.
//
// These bit numbers are the ones frozen by the SAT-2
// slice. The SCU manual (ST-058 family, third version
// ST-97) lists one more low source the SAT-2 map does
// not have: bit 12 is DMA illegal there and sprite draw
// end is bit 13. This core keeps sprite draw end at bit
// 12 with vector 0x4D, as SAT-2 shipped it, and has no
// DMA illegal source. There is no CD interrupt source in
// IST in any version of the manual: CD block interrupts
// reach the CPU as A-Bus external interrupts (IST bits
// 16 and up) gated by the CD block HIRQ register, so no
// IntCD constant exists here.
const (
	IntVBlankIn  = 0
	IntVBlankOut = 1
	IntHBlankIn  = 2
	IntTimer0    = 3
	IntTimer1    = 4
	IntDSPEnd    = 5
	IntSoundReq  = 6
	IntSMPC      = 7
	IntPad       = 8
	IntDMA2End   = 9
	IntDMA1End   = 10
	IntDMA0End   = 11
	IntSpriteEnd = 12
)

// IntABusCD is the IST bit this core assigns to its one
// A-Bus external interrupt input, wired to the CD block.
// The manual puts A-Bus external inputs at IST bits 16
// and up, gated by the interrupting device's own
// interrupt register (the CD block HIRQ). On hardware
// the device supplies the interrupt vector during the
// acknowledge cycle; this core fixes the CD input at
// vector 0x50 and level 8 (see Vector and LevelOf).
const IntABusCD = 16

// sourceCount is the number of defined interrupt sources.
const sourceCount = 13

// vectors holds the SH-2 vector number for each source,
// in bit order. These are the standard SCU assignments:
// VBlank-IN is vector 0x40 and the sources follow in
// priority order, with the sprite draw end at 0x4D.
var vectors = [sourceCount]int{
	0x40, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46,
	0x47, 0x48, 0x49, 0x4A, 0x4B, 0x4D,
}

// Level is retained from SAT-2, where every wired SCU
// source was delivered to the master at this one level.
// SAT-3 delivery uses the per source levels of the
// manual's Table 2.1 through LevelOf; Level stays for
// compatibility with the SAT-2 wiring.
const Level = 6

// manualLevels holds the per source interrupt levels
// from Table 2.1 of the SCU manual, matched to this
// package's source names: VBlank-IN 15, VBlank-OUT 14,
// HBlank-IN 13, Timer 0 12, Timer 1 11, DSP end 10,
// sound request 9, system manager 8, pad 8, level 2 DMA
// end 6, level 1 DMA end 6, level 0 DMA end 5, and
// sprite draw end 2. LevelOf returns these levels and
// the system delivers each source at its own level to
// both SH-2s. ManualLevelOf returns the same table.
var manualLevels = [sourceCount]int{
	15, 14, 13, 12, 11, 10, 9, 8, 8, 6, 6, 5, 2,
}

// LevelOf returns the SH-2 interrupt level a source is
// delivered at: the per source level from Table 2.1 of
// the SCU manual for the low sources, the fixed level 8
// for the A-Bus CD input, or -1 for an unknown source.
func LevelOf(bit int) int {
	if bit == IntABusCD {
		return 8
	}
	if bit < 0 || bit >= sourceCount {
		return -1
	}
	return manualLevels[bit]
}

// ManualLevelOf returns the per source interrupt level
// from Table 2.1 of the SCU manual for a source, or -1
// when the manual assigns the source no fixed level.
// The A-Bus external inputs are such a case (their
// levels are set by device side registers on hardware),
// so ManualLevelOf returns -1 for IntABusCD while
// LevelOf returns this core's fixed level 8.
func ManualLevelOf(bit int) int {
	if bit < 0 || bit >= sourceCount {
		return -1
	}
	return manualLevels[bit]
}

// Register page offsets of IMS and IST.
const (
	offIMS = 0xA0
	offIST = 0xA4
)

// SCU is a view over the 256 byte SCU register page owned
// by the bus, plus the off page state of the DMA
// channels, the DSP, and the timers.
type SCU struct {
	regs []byte

	// DMA state (dma.go).
	mem            Mem
	lastEnabled    [3]bool
	latchedFactors uint8
	illegalDMA     uint32

	// DSP state (dsp.go).
	dsp dspState

	// Port sync state (dsp.go): the last values consumed
	// from the DSP load ports on the page.
	lastProgPort  uint32
	progPortValid bool
	lastDataAddr  uint32
	lastDataData  uint32

	// Timer state (timers.go).
	lineDots  int
	t1Counter int
	lastLine  int
}

// New wraps a register page. The slice must be at least
// 0xA8 bytes; the bus page is 256 bytes.
func New(regs []byte) *SCU {
	return &SCU{regs: regs}
}

func (s *SCU) word(off int) uint32 {
	return binary.BigEndian.Uint32(s.regs[off : off+4])
}

func (s *SCU) setWord(off int, v uint32) {
	binary.BigEndian.PutUint32(s.regs[off:off+4], v)
}

// factorForBit maps an interrupt source bit to the DMA
// start factor number that source signals, per the DMA
// start factor table in the manual. The second result
// is false for sources that are not DMA start factors.
func factorForBit(bit int) (int, bool) {
	switch bit {
	case IntVBlankIn:
		return FactorVBlankIn, true
	case IntVBlankOut:
		return FactorVBlankOut, true
	case IntHBlankIn:
		return FactorHBlankIn, true
	case IntTimer0:
		return FactorTimer0, true
	case IntTimer1:
		return FactorTimer1, true
	case IntSoundReq:
		return FactorSoundReq, true
	case IntSpriteEnd:
		return FactorSpriteEnd, true
	}
	return 0, false
}

// validSource reports whether a bit number names a
// source this core models: one of the low sources or
// the A-Bus CD input.
func validSource(bit int) bool {
	return (bit >= 0 && bit < sourceCount) || bit == IntABusCD
}

// Raise sets the IST status bit for a source. The bit is
// set whether or not the source is masked; masking only
// blocks delivery. Out of range sources are ignored.
// Raising a source that is a DMA start factor also
// latches that factor for the next Poll, matching the
// hardware rule that the factor signal is a pulse even
// though the IST bit stays set until software clears it.
func (s *SCU) Raise(bit int) {
	if !validSource(bit) {
		return
	}
	s.setWord(offIST, s.word(offIST)|(1<<uint(bit)))
	if f, ok := factorForBit(bit); ok {
		s.latchedFactors |= 1 << uint(f)
	}
}

// sourceMask selects the IST and IMS bits that name a
// modeled source: the low 13 sources and the A-Bus CD
// input.
const sourceMask = 0x1FFF | (1 << IntABusCD)

// Status returns the raw IST word (modeled source bits
// used).
func (s *SCU) Status() uint32 { return s.word(offIST) }

// Mask returns the raw IMS word (modeled source bits
// used).
func (s *SCU) Mask() uint32 { return s.word(offIMS) }

// Pending returns the set of sources that have fired and
// are not masked: IST AND NOT IMS, modeled source bits.
func (s *SCU) Pending() uint32 {
	return s.word(offIST) &^ s.word(offIMS) & sourceMask
}

// Vector returns the SH-2 vector number for a source, or
// -1 for an out of range source. The A-Bus CD input
// returns this core's fixed vector 0x50: on hardware the
// interrupting device supplies the vector during the
// acknowledge cycle, and this core models the one
// external input with a fixed assignment.
func Vector(bit int) int {
	if bit == IntABusCD {
		return 0x50
	}
	if bit < 0 || bit >= sourceCount {
		return -1
	}
	return vectors[bit]
}

// Deliver picks the highest priority pending unmasked
// source (lowest bit number, the A-Bus CD input last)
// and returns its bit number and vector. It returns bit
// -1 when nothing is pending. Deliver does not clear
// the IST bit; clearing is the software job, done by
// writing the page through the bus. The caller passes
// the result to the CPU Interrupt methods at
// LevelOf(bit); the system offers it to both SH-2s.
func (s *SCU) Deliver() (bit int, vector int) {
	p := s.Pending()
	for b := 0; b < sourceCount; b++ {
		if p&(1<<uint(b)) != 0 {
			return b, vectors[b]
		}
	}
	if p&(1<<uint(IntABusCD)) != 0 {
		return IntABusCD, 0x50
	}
	return -1, -1
}
