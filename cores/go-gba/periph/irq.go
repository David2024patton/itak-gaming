package periph

// Interrupt source bits, in IE/IF register order.
const (
	IRQVBlank  = 1 << 0
	IRQHBlank  = 1 << 1
	IRQVCount  = 1 << 2
	IRQTimer0  = 1 << 3
	IRQTimer1  = 1 << 4
	IRQTimer2  = 1 << 5
	IRQTimer3  = 1 << 6
	IRQSerial  = 1 << 7
	IRQDMA0    = 1 << 8
	IRQDMA1    = 1 << 9
	IRQDMA2    = 1 << 10
	IRQDMA3    = 1 << 11
	IRQKeypad  = 1 << 12
	IRQGamePak = 1 << 13
)

// irqMask covers the 14 meaningful IE/IF bits.
const irqMask = 0x3FFF

// Register offsets handled by IRQ (relative to 0x04000000).
const (
	regIE  = 0x200
	regIF  = 0x202
	regIME = 0x208
)

// IRQ is the interrupt controller state: the IE enable
// mask, the IF flag register and the IME master switch.
// A source raises its IF bit through Raise; the CPU side
// is interrupted only when the bit is also enabled in IE
// and IME bit 0 is set (see Asserted).
type IRQ struct {
	ie     uint16 // stored masked to irqMask
	iflags uint16
	ime    uint8 // 0 or 1
}

// NewIRQ returns an interrupt controller in power-on
// state: nothing enabled, no flags, master switch off.
func NewIRQ() *IRQ { return &IRQ{} }

// byteAt returns the register byte at an IO offset. The
// offsets owned here are the IE halfword at 0x200, the IF
// halfword at 0x202 and the IME word at 0x208 (whose upper
// three bytes read as zero); anything else reads as zero.
func (q *IRQ) byteAt(addr uint32) byte {
	switch addr {
	case regIE:
		return byte(q.ie)
	case regIE + 1:
		return byte(q.ie >> 8)
	case regIF:
		return byte(q.iflags)
	case regIF + 1:
		return byte(q.iflags >> 8)
	case regIME:
		return q.ime
	}
	return 0
}

// writeByte applies one byte of a register write. IE
// bytes are plain stores. IF bytes follow the hardware
// acknowledge rule: a bit written as 1 clears the flag, a
// bit written as 0 leaves it, so software confirms an
// interrupt by writing back the bits it handled. The IME
// low byte keeps only bit 0.
func (q *IRQ) writeByte(addr uint32, b byte) {
	switch addr {
	case regIE:
		q.ie = (q.ie & 0xFF00) | uint16(b)
		q.ie &= irqMask
	case regIE + 1:
		q.ie = (q.ie & 0x00FF) | uint16(b)<<8
		q.ie &= irqMask
	case regIF:
		q.iflags &^= uint16(b)
	case regIF + 1:
		q.iflags &^= uint16(b) << 8
	case regIME:
		q.ime = b & 1
	}
}

// ReadReg reads size bytes at off, composed little endian
// across the IE/IF pair or the IME word. Offsets outside
// the owned set read as zero.
func (q *IRQ) ReadReg(off uint32, size int) uint32 {
	if !validSize(size) {
		return 0
	}
	return compose(off, size, q.byteAt)
}

// WriteReg writes size bytes at off, decomposed little
// endian; the IF lanes acknowledge instead of storing
// (see writeByte). Writes to offsets outside the owned
// set are ignored.
func (q *IRQ) WriteReg(off uint32, size int, val uint32) {
	if !validSize(size) {
		return
	}
	decompose(off, size, val, q.writeByte)
}

// Raise sets the given IF flag bits. Bits outside the 14
// meaningful ones are dropped.
func (q *IRQ) Raise(mask uint16) {
	q.iflags |= mask & irqMask
}

// Pending returns the flags that are both raised and
// enabled (IE AND IF). The IME switch is not part of this
// value; Asserted folds it in.
func (q *IRQ) Pending() uint16 {
	return q.ie & q.iflags
}

// Asserted reports whether an interrupt should reach the
// CPU: the master switch is on and at least one enabled
// flag is raised.
func (q *IRQ) Asserted() bool {
	return q.ime != 0 && q.Pending() != 0
}

// IRQStateSize is the serialized size of the IRQ state.
//
// Layout: IE as a little endian halfword at 0, IF as a
// little endian halfword at 2, IME (0 or 1) at 4, and one
// reserved zero byte at 5.
const IRQStateSize = 6

// Serialize writes the IRQ state to dst, which must be
// at least IRQStateSize bytes, and returns the number of
// bytes written.
func (q *IRQ) Serialize(dst []byte) int {
	put16(dst, 0, q.ie)
	put16(dst, 2, q.iflags)
	dst[4] = q.ime
	dst[5] = 0
	return IRQStateSize
}

// Deserialize restores a state written by Serialize. It
// reports false, leaving the state untouched, when src is
// too short.
func (q *IRQ) Deserialize(src []byte) bool {
	if len(src) < IRQStateSize {
		return false
	}
	q.ie = get16(src, 0) & irqMask
	q.iflags = get16(src, 2) & irqMask
	q.ime = src[4] & 1
	return true
}
