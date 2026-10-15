// The SNES memory map: how the 16 MB address space decodes
// to ROM, SRAM, Work RAM, and the system area, per layout.
//
// The console map (from the SNES development documentation
// and fullsnes):
//
//   - Banks 0x00-0x3F and their mirrors 0x80-0xBF form the
//     system area: 0x0000-0x1FFF mirrors the first 8 KB of
//     Work RAM, 0x2000-0x5FFF is the IO region (PPU, APU,
//     CPU peripherals, controllers), and 0x6000-0x7FFF is a
//     small SRAM window on HiROM boards. ROM shows at
//     0x8000-0xFFFF in these banks.
//   - Banks 0x40-0x7D and 0xC0-0xFF are ROM space: full
//     64 KB banks on HiROM, 32 KB pages on LoROM.
//   - Banks 0x7E-0x7F are the 128 KB of Work RAM itself.
//   - LoROM SRAM lives at banks 0x70-0x73 (mirrored at
//     0xF0-0xF3); HiROM SRAM at banks 0x20-0x3F (mirrored at
//     0xA0-0xBF), addresses 0x6000-0x7FFF.
//
// SNES-1 ships this map with the IO region inert: reads
// there return the map's open-bus byte and writes are
// dropped, documented here and in the README. The PPU, APU,
// DMA, and peripheral slices replace those hooks with real
// registers without the map's shape changing.
//
// Access speeds (the CPU package's Speed values): Work RAM
// and ordinary ROM are slow (8 master clocks); ROM in
// banks 0x80-0xFF is fast (6) on FastROM boards; the IO
// region is extra slow (12), matching the console.
package cart

import "github.com/davidpatton/itak-gaming/cores/go-snes/chips/w65c816"

// WRAMSize is the console's Work RAM size in bytes.
const WRAMSize = 128 * 1024

// Map is the assembled console bus for one cartridge. It
// owns the Work RAM and the cartridge SRAM; the cartridge
// ROM it reads from the loaded Cart.
type Map struct {
	cart *Cart
	wram []byte
	sram []byte

	// OpenBus is the byte reads of unmapped and (for now)
	// IO addresses return. On hardware the data bus holds
	// the last value driven on it; SNES-1 keeps a plain
	// byte the system may set, defaulting to zero.
	OpenBus uint8
}

// NewMap builds the bus for a loaded cartridge. SRAM is
// sized from the header (zero when the header declares
// none, in which case the SRAM windows read open bus).
func NewMap(c *Cart) *Map {
	m := &Map{cart: c, wram: make([]byte, WRAMSize)}
	if c.Header.RAMSize > 0 {
		m.sram = make([]byte, c.Header.RAMSize)
	}
	return m
}

// SRAM exposes the battery-backed RAM contents for the
// system's persistence layer.
func (m *Map) SRAM() []byte { return m.sram }

// WRAM exposes the Work RAM contents.
func (m *Map) WRAM() []byte { return m.wram }

// romAt translates a 24-bit address to a ROM offset under
// the cartridge's layout, reporting whether the address is
// ROM space at all.
func (m *Map) romAt(bank uint8, addr uint16) (int, bool) {
	switch m.cart.Header.Layout {
	case LayoutLoROM:
		return m.loROMOffset(bank, addr)
	case LayoutExLoROM:
		return m.loROMOffset(bank^0x40, addr)
	case LayoutHiROM:
		return m.hiROMOffset(bank, addr)
	case LayoutExHiROM:
		return m.hiROMOffset(bank^0x40, addr)
	}
	return 0, false
}

// loROMOffset computes the LoROM ROM offset for an
// address, or reports the address is not ROM. LoROM is a
// pure 32 KB page map: the offset is the bank's low seven
// bits shifted by 15 plus the address's low fifteen bits.
// The system banks only decode the high half (0x8000 up);
// in the ROM banks (0x40-0x7D and 0xC0-0xFF) the same page
// shows at both halves, because the wiring ignores
// address bit 15 there. That uniform formula is the whole
// map.
func (m *Map) loROMOffset(bank uint8, addr uint16) (int, bool) {
	inSystemBanks := bank < 0x40 || (bank >= 0x80 && bank < 0xC0)
	if addr < 0x8000 && inSystemBanks {
		return 0, false
	}
	off := int(bank&0x7F)*0x8000 + int(addr&0x7FFF)
	return wrapROM(off, len(m.cart.rom)), true
}

// hiROMOffset computes the HiROM ROM offset for an
// address, or reports the address is not ROM. HiROM maps
// full 64 KB banks at 0x40-0x7D and 0xC0-0xFF, and mirrors
// their high halves at 0x8000-0xFFFF of the system banks.
func (m *Map) hiROMOffset(bank uint8, addr uint16) (int, bool) {
	switch {
	case bank >= 0x40 && bank < 0x80 || bank >= 0xC0:
		off := int(bank&0x7F)*0x10000 + int(addr)
		return wrapROM(off, len(m.cart.rom)), true
	case addr >= 0x8000:
		// System banks: mirror of the same-numbered full
		// bank's high half.
		off := int(bank&0x3F)*0x10000 + int(addr)
		return wrapROM(off, len(m.cart.rom)), true
	}
	return 0, false
}

// wrapROM folds an offset into the ROM image. Images are
// power-of-two sized in practice, where the fold is a
// mask; the modulo form covers any other size and never
// reads out of range. Offsets are never negative, so the
// two forms agree.
func wrapROM(off, size int) int {
	if size == 0 {
		return 0
	}
	if size&(size-1) == 0 {
		return off & (size - 1)
	}
	return off % size
}

// sramAt translates a 24-bit address to an SRAM offset
// under the cartridge's layout, reporting whether the
// address is an SRAM window.
func (m *Map) sramAt(bank uint8, addr uint16) (int, bool) {
	if len(m.sram) == 0 {
		return 0, false
	}
	switch m.cart.Header.Layout {
	case LayoutLoROM, LayoutExLoROM:
		if (bank >= 0x70 && bank <= 0x73) || (bank >= 0xF0 && bank <= 0xF3) {
			off := int(bank&0x03)*0x10000 + int(addr)
			return wrapROM(off, len(m.sram)), true
		}
	case LayoutHiROM, LayoutExHiROM:
		if addr >= 0x6000 && addr < 0x8000 &&
			((bank >= 0x20 && bank < 0x40) || (bank >= 0xA0 && bank < 0xC0)) {
			off := int(bank&0x1F)*0x2000 + int(addr-0x6000)
			return wrapROM(off, len(m.sram)), true
		}
	}
	return 0, false
}

// isSystemLow reports whether the address sits in the
// system area low half (Work RAM mirror and IO) of a
// system bank.
func isSystemBank(bank uint8) bool {
	return bank < 0x40 || (bank >= 0x80 && bank < 0xC0)
}

// Read implements the CPU bus read side.
func (m *Map) Read(bank uint8, addr uint16) uint8 {
	// Work RAM itself.
	if bank == 0x7E || bank == 0x7F {
		return m.wram[int(bank&0x01)*0x10000+int(addr)]
	}
	if isSystemBank(bank) && addr < 0x2000 {
		return m.wram[addr]
	}
	if off, ok := m.sramAt(bank, addr); ok {
		return m.sram[off]
	}
	if off, ok := m.romAt(bank, addr); ok {
		return m.cart.rom[off]
	}
	// IO region and anything unmapped: open bus for now.
	return m.OpenBus
}

// Write implements the CPU bus write side.
func (m *Map) Write(bank uint8, addr uint16, val uint8) {
	if bank == 0x7E || bank == 0x7F {
		m.wram[int(bank&0x01)*0x10000+int(addr)] = val
		return
	}
	if isSystemBank(bank) && addr < 0x2000 {
		m.wram[addr] = val
		return
	}
	if off, ok := m.sramAt(bank, addr); ok {
		m.sram[off] = val
		return
	}
	// ROM and IO writes go nowhere in SNES-1.
}

// Speed implements the CPU bus speed provider.
func (m *Map) Speed(bank uint8, addr uint16) w65c816.Speed {
	if isSystemBank(bank) && addr >= 0x2000 && addr < 0x6000 {
		return w65c816.SpeedXSlow // the IO region
	}
	if m.cart.Header.FastROM && bank >= 0x80 {
		if m.isROM(bank, addr) {
			return w65c816.SpeedFast
		}
	}
	return w65c816.SpeedSlow
}

// isROM reports whether the address is ROM space: the
// ok half of romAt without the offset math, for the speed
// path, which runs on every byte access.
func (m *Map) isROM(bank uint8, addr uint16) bool {
	switch m.cart.Header.Layout {
	case LayoutLoROM:
		return loROMIsROM(bank, addr)
	case LayoutExLoROM:
		return loROMIsROM(bank^0x40, addr)
	case LayoutHiROM:
		return hiROMIsROM(bank, addr)
	case LayoutExHiROM:
		return hiROMIsROM(bank^0x40, addr)
	}
	return false
}

// loROMIsROM mirrors loROMOffsets ok result: every
// address is ROM except the low half of the system banks.
func loROMIsROM(bank uint8, addr uint16) bool {
	inSystemBanks := bank < 0x40 || (bank >= 0x80 && bank < 0xC0)
	return !(addr < 0x8000 && inSystemBanks)
}

// hiROMIsROM mirrors hiROMOffsets ok result: the ROM
// banks in full, plus the high half of any other bank.
func hiROMIsROM(bank uint8, addr uint16) bool {
	return (bank >= 0x40 && bank < 0x80) || bank >= 0xC0 || addr >= 0x8000
}

// compile-time check: Map is a CPU bus.
var _ w65c816.Bus = (*Map)(nil)
