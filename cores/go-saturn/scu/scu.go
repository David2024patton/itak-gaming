// Package scu models the part of the Sega Saturn SCU that
// SAT-2 needs: the interrupt mask and status registers
// (IMS and IST) on the SCU register page, with real status
// bits for the four interrupt sources this slice wires up:
// VBlank-IN, VBlank-OUT, HBlank-IN, and the VDP1 sprite
// draw end. Everything else on the page, including the
// SCU DSP, the DMA controllers, and the timer compare
// registers, stays plain storage owned by the bus until
// SAT-3.
//
// IMS and IST are 32 bit registers at page offsets 0xA0
// and 0xA4. The interrupt sources occupy the low 16 bits.
// A mask bit of 1 in IMS blocks delivery of that source.
// A status bit of 1 in IST means the source has fired and
// is waiting; software clears a source by writing 0 to its
// IST bit through the bus page. This package is a thin
// view over the bus page: it keeps no shadow copy, so CPU
// writes to the page take effect immediately.
//
// Delivery rule for this slice: sources are offered to the
// master SH-2 in the documented SCU priority order
// (VBlank-IN first, sprite draw end last) at interrupt
// level 6 with the standard SCU vector numbers, one
// source per Deliver call. The system calls Deliver once
// per scanline. Because IST bits stay set until software
// clears them, no source is lost when the CPU already has
// a request latched; it is offered again on a later line.
package scu

import "encoding/binary"

// Interrupt source bit numbers within IMS and IST.
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

// Level is the SH-2 interrupt level every wired SCU
// source is delivered at in this slice. Level 6 with
// vector 0x40 is the documented default configuration
// for VBlank-IN on this machine.
const Level = 6

// Register page offsets of IMS and IST.
const (
	offIMS = 0xA0
	offIST = 0xA4
)

// SCU is a view over the 256 byte SCU register page owned
// by the bus.
type SCU struct {
	regs []byte
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

// Raise sets the IST status bit for a source. The bit is
// set whether or not the source is masked; masking only
// blocks delivery. Out of range sources are ignored.
func (s *SCU) Raise(bit int) {
	if bit < 0 || bit >= sourceCount {
		return
	}
	s.setWord(offIST, s.word(offIST)|(1<<uint(bit)))
}

// Status returns the raw IST word (low 16 bits used).
func (s *SCU) Status() uint32 { return s.word(offIST) }

// Mask returns the raw IMS word (low 16 bits used).
func (s *SCU) Mask() uint32 { return s.word(offIMS) }

// Pending returns the set of sources that have fired and
// are not masked: IST AND NOT IMS, low 16 bits.
func (s *SCU) Pending() uint32 {
	return s.word(offIST) &^ s.word(offIMS) & 0xFFFF
}

// Vector returns the SH-2 vector number for a source, or
// -1 for an out of range source.
func Vector(bit int) int {
	if bit < 0 || bit >= sourceCount {
		return -1
	}
	return vectors[bit]
}

// Deliver picks the highest priority pending unmasked
// source (lowest bit number) and returns its bit number
// and vector. It returns bit -1 when nothing is pending.
// Deliver does not clear the IST bit; clearing is the
// software job, done by writing the page through the bus.
// The caller passes the result to the master CPU
// Interrupt method at Level.
func (s *SCU) Deliver() (bit int, vector int) {
	p := s.Pending()
	for b := 0; b < sourceCount; b++ {
		if p&(1<<uint(b)) != 0 {
			return b, vectors[b]
		}
	}
	return -1, -1
}
