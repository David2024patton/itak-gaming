package cart

import (
	"errors"
)

// --- Mapper 2: UxROM ---
//
// A latch selects which 16 KB bank sits at $8000-$BFFF; the last
// bank is fixed at $C000-$FFFF. Any write to $8000-$FFFF sets the
// latch. (Real boards have bus conflicts, the write value ANDed
// with the ROM byte under the address; games are not supposed to
// depend on them and this core does not model them, here or in
// the other latch mappers.)

type uxrom struct {
	baseMapper
	bank uint8
}

func (m *uxrom) readPRG(c *Cart, addr uint16) uint8 {
	if addr < 0xC000 {
		return c.prg[c.prgOff(int(m.bank), 16384)+int(addr-0x8000)]
	}
	return c.prg[c.prgOff(-1, 16384)+int(addr-0xC000)]
}
func (m *uxrom) writePRG(c *Cart, addr uint16, val uint8) { m.bank = val }
func (m *uxrom) state() []byte                            { return []byte{m.bank} }
func (m *uxrom) setState(in []byte) error {
	if len(in) != 1 {
		return errors.New("cart: bad UxROM state")
	}
	m.bank = in[0]
	return nil
}

// --- Mapper 3: CNROM ---
//
// One latch selects the 8 KB CHR bank; PRG is plain NROM. Any
// write to $8000-$FFFF sets the latch.

type cnrom struct {
	baseMapper
	chrBank uint8
}

func (m *cnrom) readCHR(c *Cart, addr uint16) uint8 {
	return c.chr[c.chrOff(int(m.chrBank), 8192)+int(addr)]
}
func (m *cnrom) writePRG(c *Cart, addr uint16, val uint8) { m.chrBank = val }
func (m *cnrom) state() []byte                            { return []byte{m.chrBank} }
func (m *cnrom) setState(in []byte) error {
	if len(in) != 1 {
		return errors.New("cart: bad CNROM state")
	}
	m.chrBank = in[0]
	return nil
}

// --- Mapper 7: AxROM ---
//
// One latch selects a whole 32 KB PRG bank and, in bit 4, which
// single nametable page is shown (AxROM boards carry one physical
// nametable and switch its CIRAM address line).

type axrom struct {
	baseMapper
	latch uint8
}

func (m *axrom) readPRG(c *Cart, addr uint16) uint8 {
	return c.prg[c.prgOff(int(m.latch&0x07), 32768)+int(addr-0x8000)]
}
func (m *axrom) writePRG(c *Cart, addr uint16, val uint8) { m.latch = val }
func (m *axrom) mirror() (Mirror, bool) {
	if m.latch&0x10 != 0 {
		return MirrorSingle1, true
	}
	return MirrorSingle0, true
}
func (m *axrom) state() []byte { return []byte{m.latch} }
func (m *axrom) setState(in []byte) error {
	if len(in) != 1 {
		return errors.New("cart: bad AxROM state")
	}
	m.latch = in[0]
	return nil
}

// --- Mapper 11: Color Dreams ---
//
// One latch: bits 0-1 select the 32 KB PRG bank, bits 4-7 the
// 8 KB CHR bank.

type colorDreams struct {
	baseMapper
	latch uint8
}

func (m *colorDreams) readPRG(c *Cart, addr uint16) uint8 {
	return c.prg[c.prgOff(int(m.latch&0x03), 32768)+int(addr-0x8000)]
}
func (m *colorDreams) readCHR(c *Cart, addr uint16) uint8 {
	return c.chr[c.chrOff(int(m.latch>>4), 8192)+int(addr)]
}
func (m *colorDreams) writePRG(c *Cart, addr uint16, val uint8) { m.latch = val }
func (m *colorDreams) state() []byte                            { return []byte{m.latch} }
func (m *colorDreams) setState(in []byte) error {
	if len(in) != 1 {
		return errors.New("cart: bad Color Dreams state")
	}
	m.latch = in[0]
	return nil
}

// --- Mapper 66: GxROM ---
//
// One latch: bits 0-1 select the 32 KB PRG bank, bits 4-5 the
// 8 KB CHR bank.

type gxrom struct {
	baseMapper
	latch uint8
}

func (m *gxrom) readPRG(c *Cart, addr uint16) uint8 {
	return c.prg[c.prgOff(int(m.latch&0x03), 32768)+int(addr-0x8000)]
}
func (m *gxrom) readCHR(c *Cart, addr uint16) uint8 {
	return c.chr[c.chrOff(int(m.latch>>4&0x03), 8192)+int(addr)]
}
func (m *gxrom) writePRG(c *Cart, addr uint16, val uint8) { m.latch = val }
func (m *gxrom) state() []byte                            { return []byte{m.latch} }
func (m *gxrom) setState(in []byte) error {
	if len(in) != 1 {
		return errors.New("cart: bad GxROM state")
	}
	m.latch = in[0]
	return nil
}

// --- Mapper 1: MMC1 ---
//
// The MMC1 is programmed through a serial shift register: each
// write to $8000-$FFFF shifts bit 0 of the value in, and the
// fifth consecutive write commits the accumulated five bits to
// the register the write address selects. A write with bit 7 set
// resets the shift register and sets the control register's two
// PRG-mode bits. Two hardware quirks are modeled:
//
//   - Writes on consecutive CPU cycles are ignored entirely: the
//     chip cannot keep up. The system feeds the cart the cycle of
//     each access through SetCycle; the mapper remembers the
//     cycle of its last accepted write and drops a write that
//     lands exactly one cycle later. (This is the state-level
//     model; a write sequence driven without cycle information,
//     such as unit tests that never call SetCycle, sees constant
//     spacing and nothing is dropped.)
//   - The PRG bank register's bit 4 disables cartridge RAM on
//     the MMC1B and later revisions when set.

type mmc1 struct {
	baseMapper
	shift   uint8
	count   int
	control uint8 // mirroring, PRG mode, CHR mode
	chr0    uint8
	chr1    uint8
	prgReg  uint8

	lastWriteCycle int64
	haveLastWrite  bool
}

func newMMC1() *mmc1 {
	return &mmc1{control: 0x0C} // power-on: PRG mode 3, like the reset state
}

func (m *mmc1) writePRG(c *Cart, addr uint16, val uint8) {
	if m.haveLastWrite && c.cycle == m.lastWriteCycle+1 {
		return // consecutive-cycle write: ignored
	}
	m.haveLastWrite = true
	m.lastWriteCycle = c.cycle
	if val&0x80 != 0 {
		m.shift = 0
		m.count = 0
		m.control |= 0x0C
		return
	}
	m.shift |= (val & 1) << uint(m.count)
	m.count++
	if m.count < 5 {
		return
	}
	v := m.shift & 0x1F
	m.shift = 0
	m.count = 0
	switch {
	case addr < 0xA000:
		m.control = v
	case addr < 0xC000:
		m.chr0 = v
	case addr < 0xE000:
		m.chr1 = v
	default:
		m.prgReg = v
	}
}

func (m *mmc1) readPRG(c *Cart, addr uint16) uint8 {
	banks16 := len(c.prg) / 16384
	last := banks16 - 1
	switch (m.control >> 2) & 3 {
	case 0, 1: // 32 KB switch at $8000
		bank := int(m.prgReg&0x0F) >> 1
		return c.prg[c.prgOff(bank, 32768)+int(addr-0x8000)]
	case 2: // $8000 fixed to bank 0, $C000 switches
		if addr < 0xC000 {
			return c.prg[int(addr-0x8000)]
		}
		return c.prg[c.prgOff(int(m.prgReg&0x0F), 16384)+int(addr-0xC000)]
	default: // $8000 switches, $C000 fixed to the last bank
		if addr < 0xC000 {
			return c.prg[c.prgOff(int(m.prgReg&0x0F), 16384)+int(addr-0x8000)]
		}
		return c.prg[c.prgOff(last, 16384)+int(addr-0xC000)]
	}
}

func (m *mmc1) readCHR(c *Cart, addr uint16) uint8 {
	if m.control&0x10 == 0 { // one 8 KB bank, low bit ignored
		return c.chr[c.chrOff(int(m.chr0>>1), 8192)+int(addr)]
	}
	if addr < 0x1000 {
		return c.chr[c.chrOff(int(m.chr0), 4096)+int(addr)]
	}
	return c.chr[c.chrOff(int(m.chr1), 4096)+int(addr-0x1000)]
}

func (m *mmc1) writeCHR(c *Cart, addr uint16, val uint8) {
	if !c.chrRAM {
		return
	}
	if m.control&0x10 == 0 {
		c.chr[c.chrOff(int(m.chr0>>1), 8192)+int(addr)] = val
		return
	}
	if addr < 0x1000 {
		c.chr[c.chrOff(int(m.chr0), 4096)+int(addr)] = val
	} else {
		c.chr[c.chrOff(int(m.chr1), 4096)+int(addr-0x1000)] = val
	}
}

func (m *mmc1) mirror() (Mirror, bool) {
	switch m.control & 3 {
	case 0:
		return MirrorSingle0, true
	case 1:
		return MirrorSingle1, true
	case 2:
		return MirrorVertical, true
	default:
		return MirrorHorizontal, true
	}
}

func (m *mmc1) ramEnabled() bool { return m.prgReg&0x10 == 0 }

func (m *mmc1) state() []byte {
	return []byte{m.shift, uint8(m.count), m.control, m.chr0, m.chr1, m.prgReg}
}
func (m *mmc1) setState(in []byte) error {
	if len(in) != 6 {
		return errors.New("cart: bad MMC1 state")
	}
	m.shift, m.count = in[0], int(in[1])
	m.control, m.chr0, m.chr1, m.prgReg = in[2], in[3], in[4], in[5]
	return nil
}

// --- Mapper 4: MMC3 ---
//
// The MMC3 pages PRG in 8 KB banks through six window registers
// (two switchable PRG windows whose positions swap with the PRG
// mode bit) and CHR in 1 KB and 2 KB windows whose layout flips
// with the CHR inversion bit. Its scanline counter is clocked by
// PPU A12 rising edges: on each edge, if a reload is pending or
// the counter is zero, the counter reloads from the latch,
// otherwise it decrements; a counter that reaches zero with
// interrupts enabled asserts the IRQ output until software
// acknowledges it by writing the disable register.

type mmc3 struct {
	baseMapper
	bankSelect uint8
	regs       [8]uint8
	mirrorReg  uint8
	ramEnable  bool
	ramProtect bool

	irqLatch   uint8
	irqCounter uint8
	irqReload  bool
	irqEnabled bool
	irqAssert  bool
}

func newMMC3() *mmc3 {
	m := &mmc3{ramEnable: true}
	m.regs[6] = 0
	m.regs[7] = 1
	return m
}

func (m *mmc3) prgWindow(c *Cart, addr uint16) (bank int) {
	total := len(c.prg) / 8192
	secondLast := total - 2
	mode := (m.bankSelect >> 6) & 1
	switch {
	case addr < 0xA000:
		if mode == 0 {
			return int(m.regs[6])
		}
		return secondLast
	case addr < 0xC000:
		return int(m.regs[7])
	case addr < 0xE000:
		if mode == 0 {
			return secondLast
		}
		return int(m.regs[6])
	default:
		return total - 1
	}
}

func (m *mmc3) readPRG(c *Cart, addr uint16) uint8 {
	bank := m.prgWindow(c, addr)
	return c.prg[c.prgOff(bank, 8192)+int(addr&0x1FFF)]
}

func (m *mmc3) readCHR(c *Cart, addr uint16) uint8 {
	return c.chr[m.chrOffset(c, addr)]
}

func (m *mmc3) writeCHR(c *Cart, addr uint16, val uint8) {
	if c.chrRAM {
		c.chr[m.chrOffset(c, addr)] = val
	}
}

// chrOffset resolves a CHR address to a byte offset under the
// current banking: two 2 KB windows and four 1 KB windows, with
// the CHR inversion bit swapping which set sits low.
func (m *mmc3) chrOffset(c *Cart, addr uint16) int {
	invert := m.bankSelect&0x80 != 0
	if !invert {
		switch {
		case addr < 0x0800:
			return c.chrOff(int(m.regs[0]&0xFE), 1024) + int(addr)
		case addr < 0x1000:
			return c.chrOff(int(m.regs[1]&0xFE), 1024) + int(addr-0x0800)
		case addr < 0x1400:
			return c.chrOff(int(m.regs[2]), 1024) + int(addr&0x03FF)
		case addr < 0x1800:
			return c.chrOff(int(m.regs[3]), 1024) + int(addr&0x03FF)
		case addr < 0x1C00:
			return c.chrOff(int(m.regs[4]), 1024) + int(addr&0x03FF)
		default:
			return c.chrOff(int(m.regs[5]), 1024) + int(addr&0x03FF)
		}
	}
	switch {
	case addr < 0x0400:
		return c.chrOff(int(m.regs[2]), 1024) + int(addr&0x03FF)
	case addr < 0x0800:
		return c.chrOff(int(m.regs[3]), 1024) + int(addr&0x03FF)
	case addr < 0x0C00:
		return c.chrOff(int(m.regs[4]), 1024) + int(addr&0x03FF)
	case addr < 0x1000:
		return c.chrOff(int(m.regs[5]), 1024) + int(addr&0x03FF)
	case addr < 0x1800:
		return c.chrOff(int(m.regs[0]&0xFE), 1024) + int(addr-0x1000)
	default:
		return c.chrOff(int(m.regs[1]&0xFE), 1024) + int(addr-0x1800)
	}
}

func (m *mmc3) writePRG(c *Cart, addr uint16, val uint8) {
	even := addr&1 == 0
	switch {
	case addr < 0xA000:
		if even {
			m.bankSelect = val & 0xC7
		} else {
			m.regs[m.bankSelect&7] = val
		}
	case addr < 0xC000:
		if even {
			m.mirrorReg = val & 1
		} else {
			m.ramEnable = val&0x80 != 0
			m.ramProtect = val&0x40 != 0
		}
	case addr < 0xE000:
		if even {
			m.irqLatch = val
		} else {
			m.irqReload = true
		}
	default:
		if even {
			m.irqEnabled = false
			m.irqAssert = false
		} else {
			m.irqEnabled = true
		}
	}
}

func (m *mmc3) mirror() (Mirror, bool) {
	if m.mirrorReg&1 != 0 {
		return MirrorHorizontal, true
	}
	return MirrorVertical, true
}

func (m *mmc3) ramEnabled() bool { return m.ramEnable }

func (m *mmc3) clockA12() {
	if m.irqReload || m.irqCounter == 0 {
		m.irqCounter = m.irqLatch
		m.irqReload = false
	} else {
		m.irqCounter--
	}
	if m.irqCounter == 0 && m.irqEnabled {
		m.irqAssert = true
	}
}

func (m *mmc3) irqPending() bool { return m.irqAssert }

func (m *mmc3) state() []byte {
	var flags byte
	if m.ramEnable {
		flags |= 1
	}
	if m.ramProtect {
		flags |= 2
	}
	if m.irqReload {
		flags |= 4
	}
	if m.irqEnabled {
		flags |= 8
	}
	if m.irqAssert {
		flags |= 16
	}
	out := []byte{m.bankSelect}
	out = append(out, m.regs[:]...)
	out = append(out, m.mirrorReg, m.irqLatch, m.irqCounter, flags)
	return out
}

func (m *mmc3) setState(in []byte) error {
	if len(in) != 13 {
		return errors.New("cart: bad MMC3 state")
	}
	m.bankSelect = in[0]
	copy(m.regs[:], in[1:9])
	m.mirrorReg = in[9]
	m.irqLatch = in[10]
	m.irqCounter = in[11]
	flags := in[12]
	m.ramEnable = flags&1 != 0
	m.ramProtect = flags&2 != 0
	m.irqReload = flags&4 != 0
	m.irqEnabled = flags&8 != 0
	m.irqAssert = flags&16 != 0
	return nil
}
