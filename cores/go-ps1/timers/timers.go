// Package timers implements the three PlayStation
// root counters (timers 0, 1 and 2) from the psx-spx
// timer documentation.
//
// Each timer has a 16-bit counter, a mode register
// and a 16-bit target. The mode register layout:
//
//	bit 0     synchronization enable
//	bits 1-2  synchronization mode
//	bit 3     reset counter at target (else at 0xFFFF)
//	bit 4     IRQ when counter reaches target
//	bit 5     IRQ when counter reaches 0xFFFF
//	bit 6     IRQ once / repeatedly
//	bit 7     IRQ pulse / toggle
//	bits 8-9  clock source
//	bit 10    interrupt request, INVERTED: reads 0
//	          while a request is pending, 1 when idle;
//	          set back to 1 by any mode write
//	bit 11    reached target (cleared by reading mode)
//	bit 12    reached 0xFFFF (cleared by reading mode)
//
// Clock sources: timer 0 takes the system clock or
// the GPU dotclock; timer 1 the system clock or the
// hblank pulse train; timer 2 the system clock or
// the system clock divided by 8.
//
// Synchronization modes (bit 0 set): timer 0 syncs
// to hblank, timer 1 to vblank:
//
//	0  pause the counter during blanking
//	1  reset the counter to 0 at each blank pulse
//	2  reset at each pulse and count only while the
//	   blanking signal is active
//	3  pause until the first pulse, then free run
//
// Timer 2 with synchronization enabled stops forever
// in modes 0 and 3 and free runs in modes 1 and 2.
//
// The timers do not own a clock: the system feeds
// elapsed ticks through Advance, computed from the
// machine cycle position, so counter readback is
// exact for a given number of consumed ticks. IRQ
// delivery is reported as events: in pulse mode an
// IRQ condition asserts the output once; in toggle
// mode the INTF bit flips per condition and the
// output asserts only on the 1-to-0 flip, matching
// the documented behavior.
package timers

// Count is the number of root counters.
const Count = 3

// Inputs carries the tick sources for one Advance
// call, all measured over the same elapsed interval.
// InHBlank and InVBlank describe the blanking state
// during the interval (sampled at its start); sync
// modes that pause use them, and sync resets use the
// pulse counts.
type Inputs struct {
	SysCycles   int  // system clock ticks
	DotTicks    int  // GPU dotclock ticks (timer 0)
	HBlankPulse int  // hblank pulses (timer 1 clock, timer 0 sync)
	VBlankPulse int  // vblank pulses (timer 1 sync)
	InHBlank    bool // hblank active during the interval
	InVBlank    bool // vblank active during the interval
}

// Timer is one root counter.
type Timer struct {
	Counter uint16
	Mode    uint16 // stored control bits 0-9
	Target  uint16

	intf          bool // mode bit 10 as read: true = no request pending
	reachedTarget bool
	reachedFFFF   bool
	oneShotUsed   bool
	sync3Armed    bool // sync mode 3 has seen its first pulse
	div8Rem       int  // timer 2 clock divider remainder
}

// Timers is the set of three root counters.
type Timers struct {
	T [Count]Timer
}

// New returns the timers in reset state.
func New() *Timers {
	t := &Timers{}
	t.Reset()
	return t
}

// Reset clears all counters and modes.
func (t *Timers) Reset() {
	for i := range t.T {
		t.T[i] = Timer{intf: true}
	}
}

// WriteCounter sets a counter's current value.
func (t *Timers) WriteCounter(i int, v uint16) { t.T[i].Counter = v }

// WriteTarget sets a counter's target value.
func (t *Timers) WriteTarget(i int, v uint16) { t.T[i].Target = v }

// WriteMode stores a mode register value. Per the
// documentation, writing the mode resets the counter
// to zero, sets INTF back to "no request", clears
// the reached flags and re-arms one-shot IRQ
// delivery and sync mode 3.
func (t *Timers) WriteMode(i int, v uint16) {
	tm := &t.T[i]
	tm.Mode = v & 0x03FF
	tm.Counter = 0
	tm.intf = true
	tm.reachedTarget = false
	tm.reachedFFFF = false
	tm.oneShotUsed = false
	tm.sync3Armed = false
	tm.div8Rem = 0
}

// ReadMode returns the mode register as the CPU sees
// it (stored bits, live INTF, reached flags) and
// then clears the two reached flags, per the
// documented read-and-clear behavior.
func (t *Timers) ReadMode(i int) uint16 {
	tm := &t.T[i]
	v := tm.Mode
	if tm.intf {
		v |= 1 << 10
	}
	if tm.reachedTarget {
		v |= 1 << 11
	}
	if tm.reachedFFFF {
		v |= 1 << 12
	}
	tm.reachedTarget = false
	tm.reachedFFFF = false
	return v
}

// syncGate decides, for timer i, whether source
// ticks pass this interval and applies sync resets
// from the pulse counts in in.
func (t *Timers) syncGate(i int, in Inputs) bool {
	tm := &t.T[i]
	if tm.Mode&0x0001 == 0 {
		return true // synchronization disabled: free run
	}
	mode := (tm.Mode >> 1) & 3
	if i == 2 {
		// Timer 2: modes 0 and 3 stop forever, modes
		// 1 and 2 free run.
		return mode == 1 || mode == 2
	}
	pulses := in.HBlankPulse
	inBlank := in.InHBlank
	if i == 1 {
		pulses = in.VBlankPulse
		inBlank = in.InVBlank
	}
	switch mode {
	case 0: // pause during blanking
		return !inBlank
	case 1: // reset at each pulse, else free
		if pulses > 0 {
			tm.Counter = 0
		}
		return true
	case 2: // reset at each pulse, count only in blanking
		if pulses > 0 {
			tm.Counter = 0
		}
		return inBlank
	case 3: // pause until first pulse, then free run
		if pulses > 0 {
			tm.sync3Armed = true
		}
		return tm.sync3Armed
	}
	return true
}

// sourceTicks selects the tick count for timer i
// from the interval inputs, applying timer 2's
// divide-by-8 with its remainder.
func (t *Timers) sourceTicks(i int, in Inputs) int {
	tm := &t.T[i]
	src := (tm.Mode >> 8) & 3
	switch i {
	case 0:
		if src == 1 || src == 3 {
			return in.DotTicks
		}
		return in.SysCycles
	case 1:
		if src == 1 || src == 3 {
			return in.HBlankPulse
		}
		return in.SysCycles
	default: // timer 2
		if src == 2 || src == 3 {
			total := tm.div8Rem + in.SysCycles
			tm.div8Rem = total % 8
			return total / 8
		}
		return in.SysCycles
	}
}

// irq fires one IRQ condition on timer tm and
// reports whether the timer's interrupt output
// asserts (pulse mode: always; toggle mode: only
// when INTF flips 1 to 0). One-shot mode suppresses
// every condition after the first until the mode
// register is written again.
func (tm *Timer) irq() bool {
	if tm.Mode&0x0040 == 0 && tm.oneShotUsed {
		return false
	}
	tm.oneShotUsed = true
	if tm.Mode&0x0080 != 0 { // toggle
		tm.intf = !tm.intf
		return !tm.intf
	}
	// Pulse: the request shows in INTF until the
	// condition passes; the output asserts once.
	tm.intf = false
	return true
}

// tick advances timer i by one source tick, applying
// target/overflow rules, and reports an asserted
// interrupt output.
func (t *Timers) tick(i int) bool {
	tm := &t.T[i]
	nc := uint32(tm.Counter) + 1
	wrapped := nc > 0xFFFF
	if wrapped {
		nc = 0
	}
	assert := false
	targetHit := tm.Target != 0 && !wrapped && uint16(nc) == tm.Target
	if tm.Target == 0 && wrapped && tm.Mode&0x0008 != 0 {
		// Target 0 with reset-at-target behaves as a
		// period of 0x10000 ticks.
		targetHit = true
	}
	if targetHit {
		tm.reachedTarget = true
		if tm.Mode&0x0010 != 0 && tm.irq() {
			assert = true
		}
		if tm.Mode&0x0008 != 0 {
			tm.Counter = 0
			return assert
		}
	}
	if !wrapped && nc == 0xFFFF {
		tm.reachedFFFF = true
		if tm.Mode&0x0020 != 0 && tm.irq() {
			assert = true
		}
	}
	tm.Counter = uint16(nc)
	return assert
}

// Advance feeds one interval of ticks to all timers
// and returns a bitmask of the timers whose
// interrupt output asserted during the interval
// (bit i = timer i), for the interrupt controller.
func (t *Timers) Advance(in Inputs) uint32 {
	var asserted uint32
	for i := 0; i < Count; i++ {
		if !t.syncGate(i, in) {
			continue
		}
		n := t.sourceTicks(i, in)
		for k := 0; k < n; k++ {
			if t.tick(i) {
				asserted |= 1 << uint(i)
			}
		}
		if t.T[i].Mode&0x0080 == 0 {
			// Pulse mode: the INTF dip is momentary
			// (a few clocks on hardware); by the end of
			// the interval it reads idle again. The
			// assertion event above is what the
			// interrupt controller latches.
			t.T[i].intf = true
		}
	}
	return asserted
}

// TimerStateSize is the serialized size of one timer.
const TimerStateSize = 16

// Serialize writes the full timer set state.
func (t *Timers) Serialize() []byte {
	out := make([]byte, Count*TimerStateSize)
	for i := range t.T {
		tm := &t.T[i]
		o := out[i*TimerStateSize:]
		put16(o[0:], tm.Counter)
		put16(o[2:], tm.Mode)
		put16(o[4:], tm.Target)
		put16(o[6:], uint16(tm.div8Rem))
		flags := byte(0)
		if tm.intf {
			flags |= 1
		}
		if tm.reachedTarget {
			flags |= 2
		}
		if tm.reachedFFFF {
			flags |= 4
		}
		if tm.oneShotUsed {
			flags |= 8
		}
		if tm.sync3Armed {
			flags |= 16
		}
		o[8] = flags
	}
	return out
}

// Deserialize restores a state produced by Serialize.
func (t *Timers) Deserialize(in []byte) {
	for i := range t.T {
		tm := &t.T[i]
		o := in[i*TimerStateSize:]
		tm.Counter = get16(o[0:])
		tm.Mode = get16(o[2:])
		tm.Target = get16(o[4:])
		tm.div8Rem = int(get16(o[6:]))
		f := o[8]
		tm.intf = f&1 != 0
		tm.reachedTarget = f&2 != 0
		tm.reachedFFFF = f&4 != 0
		tm.oneShotUsed = f&8 != 0
		tm.sync3Armed = f&16 != 0
	}
}

// StateSize is the serialized size of the timer set.
func StateSize() int { return Count * TimerStateSize }

func put16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
func get16(b []byte) uint16    { return uint16(b[0]) | uint16(b[1])<<8 }
