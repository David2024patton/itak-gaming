package scu

// SCU DMA channels 0, 1, and 2.
//
// Register layout on the page, per the SCU manual: each
// channel owns a 0x20 byte block starting at 0x00, 0x20,
// and 0x40. Within a block:
//
//	+0x00 read address (DxR, 27 bits)
//	+0x04 write address (DxW, 27 bits)
//	+0x08 transfer byte count (DxC; 20 bits on channel 0,
//	      12 bits on channels 1 and 2)
//	+0x0C address add value (DxAD)
//	+0x10 enable (DxEN)
//	+0x14 mode, address update, start factor (DxMD)
//
// The DMA force stop register DSTP is at 0x60 (bit 0).
// The DMA status register DSTA is at 0x7C. The task
// brief transposed those two offsets; the manual is
// unambiguous (DSTP 25FE0060, DSTA 25FE007C), so the
// manual wins, and a stop write to 0x7C is also honored
// as an alias so either reading stops DMA.
//
// DxEN: this core enables a channel when bit 0 or bit 8
// of DxEN is set. The manual puts the enable flag at
// bit 8 and a separate GO flag at bit 0 (the GO flag is
// the trigger for start factor 7); the task brief puts
// the enable at bit 0. Accepting either bit keeps both
// readings working: software sets one of them, Poll
// sees the 0 to 1 edge, and factor 7 channels run.
//
// DxMD fields, per the manual: bit 24 mode (0 direct,
// 1 indirect), bit 16 read address update (0 save,
// 1 update), bit 8 write address update, bits 2..0
// start factor select. The manual has no transfer unit
// field in DxMD: the count in DxC is always a byte
// count. The copy below moves the bulk in longword
// units through Mem and the tail in bytes, which is
// observationally identical to a byte copy on memory.
//
// DxAD fields, per the manual: bit 8 is the read add
// code (0 adds nothing, 1 adds 4 per unit) and bits 2..0
// are the write add code (0 adds nothing, then 2, 4, 8,
// 16, 32, 64, 128 per halfword cycle, which totals 4
// per longword on this abstract bus). This core applies
// the add codes as advance or hold: a code of 0 holds
// the address fixed for the whole transfer (the FIFO
// style access the code exists for, aimed at A-Bus CS2
// devices), and any nonzero code advances the address
// contiguously, one unit per unit of data. The exact
// per cycle add amount is a bus width artifact that is
// not observable through the Mem interface.
//
// Direct mode count: the masked count from DxC, with a
// count of 0 meaning the full width (1 MiB on channel
// 0, 4 KiB on channels 1 and 2), per the manual's
// register widths. At the end of a direct transfer the
// address registers hold the next addresses when the
// matching update bit is set, and keep the values they
// were set with when it is clear (save mode).
//
// Indirect mode: the parameters come from a table in
// memory pointed to by DxW. Each entry is three
// longwords: read address, write address, and a count
// word whose bit 31 is the end flag and whose low 20
// bits are the byte count (0 means 1 MiB). Entries are
// 12 bytes apart. The transfer runs entries in order
// until one with the end flag set has run. When the
// write address update bit is set, DxW is left pointing
// past the last entry; in save mode it is unchanged.
// A safety cap of 4096 entries guards against an
// unterminated table.
//
// Prohibited region: SCU DMA may not touch the SCU
// register page itself. A transfer whose source or
// destination range intersects the page window
// (0x05FE0000..0x05FE00FF, or its cache through alias
// at 0x25FE0000) is skipped entirely, counted by
// IllegalDMA, and raises no end interrupt. This core's
// IST map has no DMA illegal source (see scu.go), so
// the count and the missing end interrupt are the
// status. Every other transfer rides the machine bus
// through the Mem interface; the manual's A-Bus and
// B-Bus same bus rules cannot be expressed through Mem
// and are not modeled.
//
// Priority: when several channels are due in one Poll,
// channel 0 runs first, then 1, then 2, as the brief
// specifies. (The manual calls channel 2 the highest
// priority for bus arbitration; with atomic transfers
// the only observable effect of order is which transfer
// lands last on overlapping ranges, and the brief pins
// channel 0 first.)
//
// Transfers run atomically inside Poll, so DSTA busy,
// standby, and access flags are never observable set:
// Poll leaves DSTA at 0 (idle) when it returns.

// DMA start factor numbers, per the start factor table
// in the manual (DxMD bits 2..0 and the StartFactor
// argument). Factor 7 is the immediate factor: the
// channel runs when its enable is set.
const (
	FactorVBlankIn  = 0
	FactorVBlankOut = 1
	FactorHBlankIn  = 2
	FactorTimer0    = 3
	FactorTimer1    = 4
	FactorSoundReq  = 5
	FactorSpriteEnd = 6
	FactorImmediate = 7
)

// Register page offsets for the DMA block as a whole.
const (
	offDSTP = 0x60
	offDSTA = 0x7C
)

// Per channel register offsets within a channel block.
const (
	chR  = 0x00
	chW  = 0x04
	chC  = 0x08
	chAD = 0x0C
	chEN = 0x10
	chMD = 0x14
)

// maxIndirectEntries caps one indirect mode run.
const maxIndirectEntries = 4096

// Mem is the machine bus view the SCU DMA and the DSP
// DMA transfer through. The bus package satisfies it.
type Mem interface {
	Read8(uint32) uint8
	Read16(uint32) uint16
	Read32(uint32) uint32
	Write8(uint32, uint8)
	Write16(uint32, uint16)
	Write32(uint32, uint32)
}

// AttachMem attaches the bus the DMA transfers ride.
// Without a Mem, due transfers are skipped silently.
func (s *SCU) AttachMem(m Mem) { s.mem = m }

// StartFactor latches a DMA start factor event for the
// next Poll. Factors are the Factor constants above.
// Out of range factors are ignored. A latched factor is
// a pulse: Poll consumes all latches whether or not a
// channel used them.
func (s *SCU) StartFactor(f int) {
	if f < 0 || f > 7 {
		return
	}
	s.latchedFactors |= 1 << uint(f)
}

// IllegalDMA returns how many DMA transfers have been
// skipped for touching the SCU register page window.
func (s *SCU) IllegalDMA() uint32 { return s.illegalDMA }

// Poll runs one scanline of SCU work: it syncs the DSP
// load ports from the page, honors DMA stop writes,
// runs every DMA channel that is due, and runs the DSP
// while its execute flag is set. The system calls Poll
// once per scanline.
func (s *SCU) Poll() {
	s.syncDSPPorts()
	s.pollDMA()
	s.pollDSP()
}

// chanBase returns the page offset of a channel block.
func chanBase(ch int) int { return ch * 0x20 }

// pollDMA handles stop writes, enable edges, latched
// factors, and due transfers for all three channels.
func (s *SCU) pollDMA() {
	// Force stop: DSTP at 0x60 bit 0, with the brief's
	// transposed offset 0x7C honored as an alias. A stop
	// clears every channel enable on the page and drops
	// latched factors. The DSTP word is consumed so a
	// later enable is not stopped again.
	stop := s.word(offDSTP)&1 != 0
	if s.word(offDSTA)&1 != 0 {
		stop = true
	}
	if stop {
		for ch := 0; ch < 3; ch++ {
			s.setWord(chanBase(ch)+chEN, 0)
			s.lastEnabled[ch] = false
		}
		s.latchedFactors = 0
	}
	s.setWord(offDSTP, 0)

	latched := s.latchedFactors
	s.latchedFactors = 0

	for ch := 0; ch < 3; ch++ {
		base := chanBase(ch)
		en := s.word(base + chEN)
		enabled := en&0x101 != 0
		edge := enabled && !s.lastEnabled[ch]
		s.lastEnabled[ch] = enabled
		if !enabled {
			continue
		}
		md := s.word(base + chMD)
		factor := int(md & 7)
		due := false
		if factor == FactorImmediate {
			due = edge || latched&(1<<uint(FactorImmediate)) != 0
		} else {
			due = latched&(1<<uint(factor)) != 0
		}
		if due {
			s.runDMAChannel(ch)
		}
	}
	// Transfers are atomic, so the status reads idle.
	s.setWord(offDSTA, 0)
}

// touchesSCUPage reports whether the byte range starting
// at addr intersects the SCU register page window.
func touchesSCUPage(addr uint32, count int) bool {
	if count <= 0 {
		return false
	}
	lo := uint64(addr)
	hi := lo + uint64(count) - 1
	for _, base := range []uint64{0x05FE0000, 0x25FE0000} {
		if lo <= base+0xFF && hi >= base {
			return true
		}
	}
	return false
}

// copyBytes moves count bytes from src to dst through
// Mem. When an advance flag is clear the matching
// address stays fixed for every byte (add code 0).
func (s *SCU) copyBytes(src, dst uint32, count int, srcAdv, dstAdv bool) (endSrc, endDst uint32) {
	if srcAdv && dstAdv {
		n := count
		for n >= 4 && src&3 == 0 && dst&3 == 0 {
			s.mem.Write32(dst, s.mem.Read32(src))
			src += 4
			dst += 4
			n -= 4
		}
		for ; n > 0; n-- {
			s.mem.Write8(dst, s.mem.Read8(src))
			src++
			dst++
		}
		return src, dst
	}
	for i := 0; i < count; i++ {
		s.mem.Write8(dst, s.mem.Read8(src))
		if srcAdv {
			src++
		}
		if dstAdv {
			dst++
		}
	}
	return src, dst
}

// runDMAChannel executes one channel transfer and
// raises its end interrupt source.
func (s *SCU) runDMAChannel(ch int) {
	if s.mem == nil {
		return
	}
	base := chanBase(ch)
	src := s.word(base + chR)
	dst := s.word(base + chW)
	md := s.word(base + chMD)
	ad := s.word(base + chAD)
	srcAdv := ad&(1<<8) != 0
	dstAdv := ad&7 != 0
	rup := md&(1<<16) != 0
	wup := md&(1<<8) != 0
	endBit := []int{IntDMA0End, IntDMA1End, IntDMA2End}[ch]

	if md&(1<<24) != 0 {
		// Indirect mode: parameters from the table at DxW.
		table := dst
		for i := 0; i < maxIndirectEntries; i++ {
			r := s.mem.Read32(table)
			w := s.mem.Read32(table + 4)
			cw := s.mem.Read32(table + 8)
			count := int(cw & 0xFFFFF)
			if count == 0 {
				count = 1 << 20
			}
			if touchesSCUPage(r, count) || touchesSCUPage(w, count) {
				s.illegalDMA++
			} else {
				s.copyBytes(r, w, count, srcAdv, dstAdv)
			}
			table += 12
			if cw&(1<<31) != 0 {
				break
			}
		}
		if wup {
			s.setWord(base+chW, table&0x07FFFFFF)
		}
		s.Raise(endBit)
		return
	}

	// Direct mode.
	count := int(s.word(base + chC))
	if ch == 0 {
		count &= 0xFFFFF
		if count == 0 {
			count = 1 << 20
		}
	} else {
		count &= 0xFFF
		if count == 0 {
			count = 1 << 12
		}
	}
	if touchesSCUPage(src, count) || touchesSCUPage(dst, count) {
		s.illegalDMA++
		return
	}
	endSrc, endDst := s.copyBytes(src, dst, count, srcAdv, dstAdv)
	if rup {
		s.setWord(base+chR, endSrc&0x07FFFFFF)
	}
	if wup {
		s.setWord(base+chW, endDst&0x07FFFFFF)
	}
	s.Raise(endBit)
}
