package periph

// Keypad register offsets (relative to 0x04000000):
// KEYINPUT at 0x130, KEYCNT at 0x132.
const (
	regKeyInput = 0x130
	regKeyCnt   = 0x132
)

// keyMask covers the ten button bits, in GBATEK KEYINPUT
// bit order: A, B, Select, Start, Right, Left, Up, Down,
// R, L at bits 0..9.
const keyMask = 0x3FF

// KEYCNT bits, in the terms used below.
const (
	keyCntIRQEnable = 1 << 14
	keyCntAnd       = 1 << 15
)

// Keypad is the controller input state: the pressed
// button mask in pressed set form and the KEYCNT
// interrupt control register. KEYINPUT presents the mask
// active low, as the hardware does.
type Keypad struct {
	buttons     uint32 // pressed set form, low 10 bits
	keycnt      uint16 // stored as written
	lastCond    bool   // condition at the last evaluation
	lastEnabled bool   // KEYCNT IRQ enable at the last evaluation
}

// NewKeypad returns a keypad in power-on state: nothing
// pressed, KEYCNT zero (IRQ off, OR condition).
func NewKeypad() *Keypad { return &Keypad{} }

// inputValue is the KEYINPUT halfword: bits 0-9 are the
// key states active low (0 means pressed), and the
// unused bits 10-15 read as zero.
func (k *Keypad) inputValue() uint16 {
	return uint16(^k.buttons) & keyMask
}

// condition evaluates the KEYCNT interrupt condition
// against the current buttons: with bit 15 clear, any
// selected key pressed is enough (OR); with bit 15 set,
// every selected key must be pressed (AND). Selecting no
// keys never satisfies either form.
func (k *Keypad) condition() bool {
	sel := uint32(k.keycnt) & keyMask
	if sel == 0 {
		return false
	}
	hit := k.buttons & sel
	if k.keycnt&keyCntAnd != 0 {
		return hit == sel
	}
	return hit != 0
}

// SetButtons stores a new pressed mask (GBATEK KEYINPUT
// bit order, set bit means pressed) and evaluates the
// KEYCNT condition: when the interrupt condition as a
// whole (condition true and KEYCNT IRQ enable set) moves
// from false to true, the keypad IRQ is raised. Folding
// the enable bit into the evaluated edge is what lets a
// KEYCNT write that arms the interrupt while keys are
// already held fire on the next SetButtons call, which
// the system makes once per frame. Evaluation therefore
// lags such a write by at most one frame; that latency
// is a deliberate simplification of this core.
func (k *Keypad) SetButtons(mask uint32, irq *IRQ) {
	k.buttons = mask & keyMask
	cond := k.condition()
	enabled := k.keycnt&keyCntIRQEnable != 0
	if cond && enabled && !(k.lastCond && k.lastEnabled) && irq != nil {
		irq.Raise(IRQKeypad)
	}
	k.lastCond = cond
	k.lastEnabled = enabled
}

// Buttons returns the stored pressed mask, in pressed
// set form with only the low 10 bits meaningful.
func (k *Keypad) Buttons() uint32 { return k.buttons }

// byteAt returns the register byte at an IO offset:
// KEYINPUT bytes at 0x130..0x131, KEYCNT bytes at
// 0x132..0x133. Other offsets read as zero.
func (k *Keypad) byteAt(addr uint32) byte {
	switch addr {
	case regKeyInput:
		return byte(k.inputValue())
	case regKeyInput + 1:
		return byte(k.inputValue() >> 8)
	case regKeyCnt:
		return byte(k.keycnt)
	case regKeyCnt + 1:
		return byte(k.keycnt >> 8)
	}
	return 0
}

// writeByte applies one byte of a register write. Only
// KEYCNT is writable; KEYINPUT writes are ignored.
func (k *Keypad) writeByte(addr uint32, b byte) {
	switch addr {
	case regKeyCnt:
		k.keycnt = (k.keycnt & 0xFF00) | uint16(b)
	case regKeyCnt + 1:
		k.keycnt = (k.keycnt & 0x00FF) | uint16(b)<<8
	}
}

// ReadReg reads size bytes at off (0x130 KEYINPUT,
// 0x132 KEYCNT), composed little endian.
func (k *Keypad) ReadReg(off uint32, size int) uint32 {
	if !validSize(size) {
		return 0
	}
	return compose(off, size, k.byteAt)
}

// WriteReg writes size bytes at off, decomposed little
// endian. KEYCNT stores; writes to KEYINPUT are ignored.
// The write itself does not evaluate the IRQ condition
// (it has no IRQ handle); SetButtons does, and the
// system calls it every frame (see SetButtons).
func (k *Keypad) WriteReg(off uint32, size int, val uint32) {
	if !validSize(size) {
		return
	}
	decompose(off, size, val, k.writeByte)
}

// KeypadStateSize is the serialized size of the keypad
// state.
//
// Layout: buttons mask word at 0 (little endian, low 10
// bits meaningful), KEYCNT halfword at 4, and one flags
// byte at 6 (bit 0 the last evaluated condition, bit 1
// the last evaluated IRQ enable), with one reserved zero
// byte at 7.
const KeypadStateSize = 8

// Serialize writes the keypad state to dst, which must
// be at least KeypadStateSize bytes, and returns the
// number of bytes written.
func (k *Keypad) Serialize(dst []byte) int {
	put32(dst, 0, k.buttons)
	put16(dst, 4, k.keycnt)
	var flags byte
	if k.lastCond {
		flags |= 1
	}
	if k.lastEnabled {
		flags |= 2
	}
	dst[6] = flags
	dst[7] = 0
	return KeypadStateSize
}

// Deserialize restores a state written by Serialize. It
// reports false, leaving the state untouched, when src is
// too short.
func (k *Keypad) Deserialize(src []byte) bool {
	if len(src) < KeypadStateSize {
		return false
	}
	k.buttons = get32(src, 0) & keyMask
	k.keycnt = get16(src, 4)
	k.lastCond = src[6]&1 != 0
	k.lastEnabled = src[6]&2 != 0
	return true
}
