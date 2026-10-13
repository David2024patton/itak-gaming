package scu

// SCU timers 0 and 1.
//
// Registers on the page, per the manual: timer 0
// compare at 0x90 (T0C, 10 bits), timer 1 set data at
// 0x94 (T1S, 9 bits), and timer 1 mode at 0x98 (T1MD:
// bit 8 is the mode bit, bit 0 is the timer enable
// bit). The brief calls the compare register TC0; the
// manual name is T0C, at the same offset 0x90.
//
// The system drives the timers with TickLine, once per
// scanline, and tells the SCU the display dot timing
// with SetLineDots.
//
// Timer 0 model: the compare value is a dot position.
// Each line covers the dot range that starts at
// line times the dots per line and runs for the dots
// per line. Timer 0 fires once, on the line whose range
// contains the compare value; a compare of 0 therefore
// fires at the start of line 0. When SetLineDots has
// never been called with a positive value, the dot
// ranges are unknown and timer 0 fires only for a
// compare of 0 (on whatever line is ticked). This is a
// deliberate simplification of the hardware counter,
// which counts HBlank-IN pulses from VBlank-OUT and
// compares against T0C as a line count; the dot model
// is the one the brief specifies, and it agrees with
// the hardware model when the dots per line are treated
// as one unit.
//
// Timer 1 model: timer 1 counts lines. With the T1MD
// mode bit clear it counts every ticked line; with the
// mode bit set it counts only lines on which timer 0
// fired (the manual's "occurs only at lines designated
// by timer 0"). When the counter reaches the T1S value
// the SCU raises IntTimer1 and the counter resets to
// zero, so timer 1 fires repeatedly with a period of
// T1S qualifying lines. A T1S of 0 fires on every
// qualifying line. The manual's timer 1 is a countdown
// in dot clocks from a value reloaded each HBlank; the
// line counting model here is the brief's, with the
// same observable interrupt cadence at line level.
//
// The T1MD enable bit (bit 0) gates timer 1, as the
// manual specifies: with it clear, timer 1 never counts
// or fires. That is the power on state, so a machine
// that never programs T1MD sees no timer 1 interrupts;
// software sets the enable bit when it programs T1S.
// (The SAT-3 SCU first shipped without the gate; the
// system level masked interrupt gate caught it, because
// an always on timer 1 with T1S 0 fires every line.)
// Firing raises the interrupt source, which also
// latches the matching DMA start factor (see Raise).

// Timer register page offsets.
const (
	offT0C  = 0x90
	offT1S  = 0x94
	offT1MD = 0x98
)

// SetLineDots records how many display dots one
// scanline spans, for the timer 0 dot range model.
// A nonpositive value disables the dot ranges (timer 0
// then fires only for a compare of 0).
func (s *SCU) SetLineDots(dots int) {
	if dots < 0 {
		dots = 0
	}
	s.lineDots = dots
}

// TickLine advances the timers by one scanline. The
// line argument is the scanline index within the frame.
func (s *SCU) TickLine(line int) {
	s.lastLine = line

	compare := int(s.word(offT0C) & 0x3FF)
	fired0 := false
	if s.lineDots > 0 {
		lo := line * s.lineDots
		if compare >= lo && compare < lo+s.lineDots {
			fired0 = true
		}
	} else if compare == 0 {
		fired0 = true
	}
	if fired0 {
		s.Raise(IntTimer0)
	}

	if s.word(offT1MD)&1 == 0 {
		return // timer 1 disabled (T1MD enable bit clear)
	}
	modeAtTimer0 := s.word(offT1MD)&(1<<8) != 0
	if modeAtTimer0 && !fired0 {
		return
	}
	s.t1Counter++
	set := int(s.word(offT1S) & 0x1FF)
	if set == 0 || s.t1Counter >= set {
		s.Raise(IntTimer1)
		s.t1Counter = 0
	}
}
