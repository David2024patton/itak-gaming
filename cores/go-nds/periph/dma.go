package periph

// TriggerKind selects which DMA start condition the system or a
// device fires.
type TriggerKind int

const (
	TrigVBlank      TriggerKind = iota // frame start of vblank (line 192)
	TrigHBlank                         // per visible line
	TrigDisplayLine                    // display start + main-memory display
	TrigCard                           // a card block is ready
	TrigGX                             // frame start, geometry FIFO timing
)

// stepAddr applies a DMA address adjust mode: 0 increment,
// 1 decrement, 2 fixed, 3 increment (the destination reload
// mode also increments during the transfer).
func stepAddr(a uint32, mode uint32, unit int) uint32 {
	switch mode {
	case 0, 3:
		return a + uint32(unit)
	case 1:
		return a - uint32(unit)
	}
	return a
}

// --- ARM9 DMA -------------------------------------------------
//
// Registers at 0x0B0 + 16 per channel: SAD, DAD, CNT. CNT
// fields: bits 0-20 unit count (0 means 0x200000 units),
// 21-22 destination adjust, 23-24 source adjust, 25 repeat,
// 26 unit size (0 = 16-bit, 1 = 32-bit), 27-29 start timing,
// 30 IRQ on completion, 31 enable. Timing 0 is immediate and
// runs synchronously inside the register write; that is a
// documented simplification of the hardware's pipelined start.
// The geometry FIFO timing (7) fires once per frame.

type dma9Chan struct {
	sad, dad, cnt uint32
	src, dst      uint32
	remain        uint32
	armed         bool
}

func dma9Count(cnt uint32) uint32 {
	if n := cnt & 0x1FFFFF; n != 0 {
		return n
	}
	return 0x200000
}

func (s *Set) writeDMA9(off uint32, size int, val uint32) {
	ch := &s.dm9[off/16]
	rel := off % 16
	switch rel / 4 {
	case 0:
		ch.sad = putU32(ch.sad, rel, size, val)
	case 1:
		ch.dad = putU32(ch.dad, rel, size, val)
	case 2:
		ch.cnt = putU32(ch.cnt, rel, size, val)
		s.enableDMA9(int(off/16), ch)
	}
}

func (s *Set) readDMA9(off uint32, size int) uint32 {
	ch := &s.dm9[off/16]
	rel := off % 16
	switch rel / 4 {
	case 0:
		return getU32(ch.sad, rel, size)
	case 1:
		return getU32(ch.dad, rel, size)
	}
	return getU32(ch.cnt, rel, size)
}

// enableDMA9 runs or arms the channel after a CNT write with
// the enable bit set.
func (s *Set) enableDMA9(idx int, ch *dma9Chan) {
	if ch.cnt&(1<<31) == 0 {
		ch.armed = false
		return
	}
	timing := (ch.cnt >> 27) & 7
	switch timing {
	case 0:
		ch.src, ch.dst = ch.sad, ch.dad
		ch.remain = dma9Count(ch.cnt)
		s.runDMA9(idx, ch)
	case 5:
		// Card timing: run at once if a block is waiting,
		// otherwise arm for the card unit to fire.
		ch.src, ch.dst = ch.sad, ch.dad
		ch.remain = dma9Count(ch.cnt)
		if s.crd.active {
			s.runDMA9(idx, ch)
		} else {
			ch.armed = true
		}
	case 6:
		// GBA cart timing: no slot device exists in this
		// module; the transfer runs immediately, reading the
		// open-bus cart window, and completes cleanly.
		ch.src, ch.dst = ch.sad, ch.dad
		ch.remain = dma9Count(ch.cnt)
		s.runDMA9(idx, ch)
	default:
		ch.src, ch.dst = ch.sad, ch.dad
		ch.remain = dma9Count(ch.cnt)
		ch.armed = true
	}
}

func (s *Set) runDMA9(idx int, ch *dma9Chan) {
	unit := 2
	if ch.cnt&(1<<26) != 0 {
		unit = 4
	}
	view := s.view9
	for ch.remain > 0 {
		v := view.Read(ch.src, unit)
		view.Write(ch.dst, unit, v)
		ch.src = stepAddr(ch.src, (ch.cnt>>23)&3, unit)
		ch.dst = stepAddr(ch.dst, (ch.cnt>>21)&3, unit)
		ch.remain--
	}
	if ch.cnt&(1<<30) != 0 {
		s.Raise(true, IRQDMA0+uint(idx))
	}
	if ch.cnt&(1<<25) != 0 {
		ch.remain = dma9Count(ch.cnt)
		if (ch.cnt>>21)&3 == 3 {
			ch.dst = ch.dad
		}
		ch.armed = true
	} else {
		ch.cnt &^= 1 << 31
		ch.armed = false
	}
}

// fireDMA9 runs every armed channel with the given timing code.
func (s *Set) fireDMA9(timing uint32) {
	for i := range s.dm9 {
		ch := &s.dm9[i]
		if ch.armed && (ch.cnt>>27)&7 == timing {
			ch.armed = false
			s.runDMA9(i, ch)
		}
	}
}

// --- ARM7 DMA -------------------------------------------------
//
// GBA register layout at 0x0B0 + 12 per channel: SAD, DAD,
// CNT_L (unit count, 16-bit, 0 means 0x10000), CNT_H: bits 5-6
// destination adjust, 7-8 source adjust, 9 repeat, 10 unit
// size, 11-12 timing (0 immediate, 1 vblank, 2 hblank,
// 3 special), 14 IRQ, 15 enable. The special timing covers the
// DS sound FIFO and card/wifi triggers on the ARM7; none has a
// real source in this module, so a special-timing transfer
// completes cleanly on enable without moving data (documented).

type dma7Chan struct {
	sad, dad   uint32
	cntL, cntH uint16
	src, dst   uint32
	remain     uint32
	armed      bool
}

func dma7Count(cntL uint16) uint32 {
	if n := uint32(cntL); n != 0 {
		return n
	}
	return 0x10000
}

func (s *Set) writeDMA7(off uint32, size int, val uint32) {
	ch := &s.dm7[off/12]
	rel := off % 12
	switch {
	case rel < 4:
		ch.sad = putU32(ch.sad, rel, size, val)
	case rel < 8:
		ch.dad = putU32(ch.dad, rel, size, val)
	case rel < 10:
		ch.cntL = uint16(putU32(uint32(ch.cntL), rel, size, val))
	default:
		ch.cntH = uint16(putU32(uint32(ch.cntH)<<16, rel, size, val) >> 16)
		s.enableDMA7(int(off/12), ch)
	}
}

func (s *Set) readDMA7(off uint32, size int) uint32 {
	ch := &s.dm7[off/12]
	rel := off % 12
	switch {
	case rel < 4:
		return getU32(ch.sad, rel, size)
	case rel < 8:
		return getU32(ch.dad, rel, size)
	case rel < 10:
		return getU32(uint32(ch.cntL), rel, size)
	}
	return getU32(uint32(ch.cntH)<<16, rel, size)
}

func (s *Set) enableDMA7(idx int, ch *dma7Chan) {
	if ch.cntH&0x8000 == 0 {
		ch.armed = false
		return
	}
	timing := (ch.cntH >> 11) & 3
	switch timing {
	case 0:
		ch.src, ch.dst = ch.sad, ch.dad
		ch.remain = dma7Count(ch.cntL)
		s.runDMA7(idx, ch)
	case 3:
		// Special timing: no source. Complete cleanly.
		ch.remain = 0
		if ch.cntH&0x4000 != 0 {
			s.Raise(false, IRQDMA0+uint(idx))
		}
		if ch.cntH&0x0200 == 0 {
			ch.cntH &^= 0x8000
		}
	default:
		ch.src, ch.dst = ch.sad, ch.dad
		ch.remain = dma7Count(ch.cntL)
		ch.armed = true
	}
}

func (s *Set) runDMA7(idx int, ch *dma7Chan) {
	unit := 2
	if ch.cntH&0x0400 != 0 {
		unit = 4
	}
	view := s.view7
	for ch.remain > 0 {
		v := view.Read(ch.src, unit)
		view.Write(ch.dst, unit, v)
		ch.src = stepAddr(ch.src, uint32(ch.cntH>>7)&3, unit)
		ch.dst = stepAddr(ch.dst, uint32(ch.cntH>>5)&3, unit)
		ch.remain--
	}
	if ch.cntH&0x4000 != 0 {
		s.Raise(false, IRQDMA0+uint(idx))
	}
	if ch.cntH&0x0200 != 0 {
		ch.remain = dma7Count(ch.cntL)
		if (ch.cntH>>5)&3 == 3 {
			ch.dst = ch.dad
		}
		ch.armed = true
	} else {
		ch.cntH &^= 0x8000
		ch.armed = false
	}
}

func (s *Set) fireDMA7(timing uint32) {
	for i := range s.dm7 {
		ch := &s.dm7[i]
		if ch.armed && uint32(ch.cntH>>11)&3 == timing {
			ch.armed = false
			s.runDMA7(i, ch)
		}
	}
}

// TriggerDMA fires a DMA start condition across both CPUs. The
// system calls it at the video timing points; the card unit
// calls the card trigger itself when a block is ready.
func (s *Set) TriggerDMA(k TriggerKind) {
	switch k {
	case TrigVBlank:
		s.fireDMA9(1)
		s.fireDMA7(1)
	case TrigHBlank:
		s.fireDMA9(2)
		s.fireDMA7(2)
	case TrigDisplayLine:
		s.fireDMA9(3)
		s.fireDMA9(4)
	case TrigCard:
		s.fireDMA9(5)
	case TrigGX:
		s.fireDMA9(7)
	}
}
