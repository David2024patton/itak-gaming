package periph

// Timer register offsets (relative to 0x04000000). Channel
// n has its counter/reload halfword at 0x100+4n and its
// control halfword at 0x102+4n.
const timerBase = 0x100

// Timer control (CNT_H) bits, in the terms used below.
const (
	timerEnable  = 1 << 7
	timerCascade = 1 << 2
	timerIRQ     = 1 << 6
)

// prescalerPeriods maps the CNT_H prescaler select (bits
// 0-1) to the number of system cycles per counter tick.
var prescalerPeriods = [4]int{1, 64, 256, 1024}

// timerChan is one timer channel. The divider holds the
// prescaler remainder in cycles, so live counter reads
// reflect every Advance call exactly with no drift.
type timerChan struct {
	reload  uint16
	counter uint16
	control uint16
	divider uint16 // cycles, always below 1024
}

func (c *timerChan) enabled() bool { return c.control&timerEnable != 0 }

// Timers is the set of four GBA timer channels. Channels
// count at a prescaled rate, or, in cascade mode, once per
// overflow of the previous channel.
type Timers struct {
	ch [4]timerChan

	// OnOverflow, when set, is called once per channel
	// overflow during Advance, with the channel index. The
	// sound unit uses it to clock the Direct Sound FIFOs:
	// timers 0 and 1 are the sample rate generators for
	// FIFO channels A and B. It fires whether or not the
	// channel's IRQ enable is set.
	OnOverflow func(ch int)
}

// NewTimers returns the timers in power-on state: all
// channels stopped, reload and counter zero.
func NewTimers() *Timers { return &Timers{} }

// byteAt returns the register byte at an IO offset in
// 0x100..0x10F: the live counter bytes at the CNT_L
// offsets and the stored control bytes at the CNT_H
// offsets. Other offsets read as zero.
func (t *Timers) byteAt(addr uint32) byte {
	if addr < timerBase || addr > timerBase+0xF {
		return 0
	}
	rel := addr - timerBase
	c := &t.ch[rel/4]
	switch rel % 4 {
	case 0:
		return byte(c.counter)
	case 1:
		return byte(c.counter >> 8)
	case 2:
		return byte(c.control)
	default:
		return byte(c.control >> 8)
	}
}

// writeByte applies one byte of a register write.
//
// A CNT_L byte updates the reload value; while the channel
// is stopped the live counter follows the reload value,
// so a stopped channel can be primed by writing CNT_L. A
// CNT_H low byte can flip the enable bit: on a stopped to
// running transition the counter loads from the reload
// value and the prescaler divider restarts.
func (t *Timers) writeByte(addr uint32, b byte) {
	if addr < timerBase || addr > timerBase+0xF {
		return
	}
	rel := addr - timerBase
	c := &t.ch[rel/4]
	switch rel % 4 {
	case 0:
		c.reload = (c.reload & 0xFF00) | uint16(b)
		if !c.enabled() {
			c.counter = c.reload
		}
	case 1:
		c.reload = (c.reload & 0x00FF) | uint16(b)<<8
		if !c.enabled() {
			c.counter = c.reload
		}
	case 2:
		was := c.enabled()
		c.control = (c.control & 0xFF00) | uint16(b)
		if !was && c.enabled() {
			c.counter = c.reload
			c.divider = 0
		}
	case 3:
		c.control = (c.control & 0x00FF) | uint16(b)<<8
	}
}

// ReadReg reads size bytes at off (0x100..0x10F), composed
// little endian. Reading a CNT_L offset returns the live
// counter, not the reload value.
func (t *Timers) ReadReg(off uint32, size int) uint32 {
	if !validSize(size) {
		return 0
	}
	return compose(off, size, t.byteAt)
}

// WriteReg writes size bytes at off (0x100..0x10F),
// decomposed little endian. See writeByte for the reload
// and enable rules.
func (t *Timers) WriteReg(off uint32, size int, val uint32) {
	if !validSize(size) {
		return
	}
	decompose(off, size, val, t.writeByte)
}

// Counter returns the live counter of channel ch (0..3),
// as a CNT_L read would report it. Out of range channels
// read as zero.
func (t *Timers) Counter(ch int) uint16 {
	if ch < 0 || ch > 3 {
		return 0
	}
	return t.ch[ch].counter
}

// Advance moves every running channel forward by cycles
// system cycles. Non cascade channels accumulate the
// cycles into their prescaler divider and tick once per
// period. A tick past 0xFFFF reloads the counter from the
// reload value, raises the channel IRQ when CNT_H asks
// for it, and feeds one count into the next channel when
// that channel runs in cascade mode. Channels are
// processed in order within the call, so a cascade chain
// settles completely: a cascading channel can itself
// overflow and feed the next one. Stopped channels do not
// count, and a stopped or non cascade channel breaks a
// cascade chain.
func (t *Timers) Advance(cycles int, irq *IRQ) {
	if cycles <= 0 {
		return
	}
	var feed uint32 // overflows handed from channel n-1 to n
	for n := range t.ch {
		c := &t.ch[n]
		var ticks uint32
		switch {
		case !c.enabled():
			// Stopped: no ticks, and the chain stops here.
		case c.control&timerCascade != 0:
			// Cascade mode counts previous overflows; the
			// prescaler select is ignored.
			ticks = feed
		default:
			period := prescalerPeriods[c.control&3]
			total := int(c.divider) + cycles
			ticks = uint32(total / period)
			c.divider = uint16(total % period)
		}
		feed = 0
		for ticks > 0 {
			until := uint32(0x10000) - uint32(c.counter)
			if ticks < until {
				c.counter += uint16(ticks)
				ticks = 0
				break
			}
			ticks -= until
			c.counter = c.reload
			feed++
			if c.control&timerIRQ != 0 && irq != nil {
				irq.Raise(uint16(IRQTimer0 << n))
			}
			if t.OnOverflow != nil {
				t.OnOverflow(n)
			}
		}
	}
}

// TimersStateSize is the serialized size of the timers
// state: 8 bytes per channel times 4 channels.
//
// Layout, per channel at 8n: reload halfword at 0,
// counter halfword at 2, control halfword at 4, prescaler
// divider halfword at 6, all little endian.
const TimersStateSize = 4 * 8

// Serialize writes the timers state to dst, which must
// be at least TimersStateSize bytes, and returns the
// number of bytes written.
func (t *Timers) Serialize(dst []byte) int {
	for n := range t.ch {
		c := &t.ch[n]
		put16(dst, 8*n+0, c.reload)
		put16(dst, 8*n+2, c.counter)
		put16(dst, 8*n+4, c.control)
		put16(dst, 8*n+6, c.divider)
	}
	return TimersStateSize
}

// Deserialize restores a state written by Serialize. It
// reports false, leaving the state untouched, when src is
// too short.
func (t *Timers) Deserialize(src []byte) bool {
	if len(src) < TimersStateSize {
		return false
	}
	for n := range t.ch {
		c := &t.ch[n]
		c.reload = get16(src, 8*n+0)
		c.counter = get16(src, 8*n+2)
		c.control = get16(src, 8*n+4)
		c.divider = get16(src, 8*n+6)
	}
	return true
}
