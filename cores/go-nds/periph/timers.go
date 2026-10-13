package periph

// Timer is one GBA-style DS timer channel. The register pair is
// CNT_L (reload value; reads return the live counter) and CNT_H
// (prescaler select, cascade, IRQ enable, start). Both CPUs use
// this identical implementation at their own bus pages.
//
// Two documented choices: writing CNT_L loads the counter from
// the reload value immediately (not only at the next overflow),
// and starting the timer (CNT_H bit 7 rising) resets both the
// counter and the prescaler remainder.
type Timer struct {
	reload  uint16
	ctrl    uint16
	counter uint16
	sub     uint32 // prescaler remainder, in CPU cycles
}

var prescalerShift = [4]uint{0, 6, 8, 10} // F/1, F/64, F/256, F/1024

// advance moves the timer by the given CPU cycles plus cascade
// ticks taken from the previous timer's overflows. It returns
// this timer's overflow count.
func (t *Timer) advance(cycles, cascade int) int {
	if t.ctrl&0x80 == 0 {
		return 0
	}
	var ticks int
	if t.ctrl&0x04 != 0 {
		ticks = cascade
	} else {
		sh := prescalerShift[t.ctrl&3]
		t.sub += uint32(cycles)
		ticks = int(t.sub >> sh)
		t.sub &= (1 << sh) - 1
	}
	if ticks == 0 {
		return 0
	}
	period := 0x10000 - int(t.reload)
	pos := int(t.counter) - int(t.reload)
	if pos < 0 {
		// The reload value was raised above the counter after
		// the counter was latched; count from the bottom.
		pos = 0
	}
	total := pos + ticks
	t.counter = uint16(int(t.reload) + total%period)
	return total / period
}

// AdvanceTimers runs one side's four timers for the given
// number of executed CPU cycles, chaining cascade ticks and
// raising overflow interrupts.
func (s *Set) AdvanceTimers(nine bool, cycles int) {
	side := sideIdx(nine)
	ov := 0
	for i := 0; i < 4; i++ {
		ov = s.tim[side][i].advance(cycles, ov)
		if ov > 0 && s.tim[side][i].ctrl&0x40 != 0 {
			s.Raise(nine, IRQTimer0+uint(i))
		}
	}
}

// readReg serves a read of the timer's 4-byte register slot at
// the given byte lane.
func (t *Timer) readReg(lane uint32, size int) uint32 {
	v := uint32(t.counter) | uint32(t.ctrl)<<16
	return getU32(v, lane, size)
}

// writeReg serves a write of the timer's register slot. CNT_L
// writes update the reload and the live counter; CNT_H writes
// update the control, and a start rising edge latches the
// counter from the reload value.
func (t *Timer) writeReg(lane uint32, size int, val uint32) {
	old := t.ctrl
	// Compose the post-write 32-bit register image, applying
	// the write to the reload and control halves separately.
	lo := putU32(uint32(t.reload), lane, size, val)
	hi := putU32(uint32(t.ctrl)<<16, lane, size, val) >> 16
	touchedLo := size == 4 || lane < 2
	touchedHi := size == 4 || lane >= 2
	if touchedLo {
		t.reload = uint16(lo)
		t.counter = t.reload
	}
	if touchedHi {
		t.ctrl = uint16(hi)
		if old&0x80 == 0 && t.ctrl&0x80 != 0 {
			t.counter = t.reload
			t.sub = 0
		}
	}
}
