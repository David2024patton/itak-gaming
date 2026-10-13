// Package cart loads and describes an NES cartridge image in the
// iNES and NES 2.0 formats, and implements the memory mappers
// (bank controllers) that page PRG and CHR memory through the
// CPU and PPU address spaces.
//
// Written clean-room from the published header layouts and the
// mapper behavior documented on the NESdev wiki. Cartridge RAM
// contents live in Cart's PRG RAM slice, which the system owns
// for battery persistence; mapper bank registers are kept in the
// mapper and serialized with it.
//
// NES-1 implements mappers 0 (NROM), 1 (MMC1), 2 (UxROM),
// 3 (CNROM), 4 (MMC3), 7 (AxROM), 11 (Color Dreams), and
// 66 (GxROM). The MMC3 IRQ counter is fully implemented here;
// the system clocks it from PPU address line A12 starting in
// NES-2.
package cart

import (
	"errors"
	"fmt"
)

// Mirror describes a nametable mirroring arrangement. The header
// sets the default; several mappers override it dynamically.
type Mirror int

const (
	MirrorHorizontal Mirror = iota
	MirrorVertical
	MirrorSingle0
	MirrorSingle1
	MirrorFourScreen
)

func (m Mirror) String() string {
	switch m {
	case MirrorHorizontal:
		return "horizontal"
	case MirrorVertical:
		return "vertical"
	case MirrorSingle0:
		return "single screen (low)"
	case MirrorSingle1:
		return "single screen (high)"
	case MirrorFourScreen:
		return "four screen"
	}
	return "unknown"
}

// Header is the parsed cartridge header.
type Header struct {
	Mapper    int
	Submapper int
	NES2      bool // the image carries a NES 2.0 header

	PRGSize    int // PRG ROM bytes
	CHRSize    int // CHR ROM bytes (0 means CHR RAM)
	PRGRAMDecl int // CPU $6000-$7FFF RAM bytes the header declares
	CHRRAMDecl int // CHR RAM bytes the header declares

	Mirror     Mirror // as the header declares it
	Battery    bool
	Trainer    bool
	VS         bool // VS. System cartridge
	PlayChoice bool // PlayChoice-10 cartridge
}

// Cart is a loaded cartridge image.
type Cart struct {
	Header Header

	prg    []uint8 // PRG ROM
	chr    []uint8 // CHR ROM, or CHR RAM when chrIsRAM
	chrRAM bool
	prgRAM []uint8

	mapper mapper

	// cycle is the CPU cycle count of the most recent access,
	// fed by the system through SetCycle. MMC1 consults it for
	// the consecutive-write ignore quirk.
	cycle int64

	// a12Clocks counts ClockA12 calls (diagnostic).
	a12Clocks int64
}

// mapper is the bank controller interface. Read and write at the
// PRG side cover $8000-$FFFF (the system routes $6000-$7FFF RAM
// through ramEnabled and Cart's own slice); the CHR side covers
// $0000-$1FFF.
type mapper interface {
	readPRG(c *Cart, addr uint16) uint8
	writePRG(c *Cart, addr uint16, val uint8)
	readCHR(c *Cart, addr uint16) uint8
	writeCHR(c *Cart, addr uint16, val uint8)
	mirror() (Mirror, bool) // ok false: use the header's
	ramEnabled() bool
	irqPending() bool
	clockA12()
	state() []byte
	setState([]byte) error
}

// baseMapper provides the NROM behavior plus the defaults every
// simple mapper shares: linear CHR, RAM enabled, no IRQ.
type baseMapper struct{}

func (baseMapper) readPRG(c *Cart, addr uint16) uint8 {
	return c.prg[int(addr-0x8000)%len(c.prg)]
}
func (baseMapper) writePRG(c *Cart, addr uint16, val uint8) {}
func (baseMapper) readCHR(c *Cart, addr uint16) uint8 {
	return c.chr[int(addr)%len(c.chr)]
}
func (baseMapper) writeCHR(c *Cart, addr uint16, val uint8) {
	if c.chrRAM {
		c.chr[int(addr)%len(c.chr)] = val
	}
}
func (baseMapper) mirror() (Mirror, bool) { return 0, false }
func (baseMapper) ramEnabled() bool       { return true }
func (baseMapper) irqPending() bool       { return false }
func (baseMapper) clockA12()              {}
func (baseMapper) state() []byte          { return nil }
func (baseMapper) setState([]byte) error  { return nil }

// bankOff returns the byte offset of a bank of the given unit
// size inside a ROM image, wrapping the bank number modulo the
// number of banks the image holds, as the hardware's unconnected
// high address lines do.
func bankOff(bank, unit, size int) int {
	n := size / unit
	b := bank % n
	if b < 0 {
		b += n
	}
	return b * unit
}

func (c *Cart) prgOff(bank, unit int) int { return bankOff(bank, unit, len(c.prg)) }
func (c *Cart) chrOff(bank, unit int) int { return bankOff(bank, unit, len(c.chr)) }

// New parses a cartridge image and builds its mapper.
func New(data []byte) (*Cart, error) {
	h, off, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	need := off + h.PRGSize + h.CHRSize
	if len(data) < need {
		return nil, fmt.Errorf("cart: image truncated: have %d bytes, header promises %d", len(data), need)
	}
	c := &Cart{Header: h}
	c.prg = append(c.prg, data[off:off+h.PRGSize]...)
	if h.CHRSize > 0 {
		c.chr = append(c.chr, data[off+h.PRGSize:need]...)
	} else {
		ram := h.CHRRAMDecl
		if ram <= 0 {
			ram = 8192
		}
		c.chr = make([]uint8, ram)
		c.chrRAM = true
	}
	if h.PRGSize == 0 {
		return nil, errors.New("cart: image has no PRG ROM")
	}
	if h.PRGAMSize() > 0 {
		c.prgRAM = make([]uint8, h.PRGAMSize())
	}
	m, err := newMapper(h.Mapper)
	if err != nil {
		return nil, err
	}
	c.mapper = m
	return c, nil
}

// PRGAMSize is the amount of CPU work RAM at $6000-$7FFF. A
// header that declares RAM wins. Otherwise an iNES header means
// the standard 8 KB: iNES has no way to declare the RAM absent,
// and the format's cartridges (the public test ROMs included)
// assume it is there. NES 2.0 declares exact sizes including
// zero, and there zero is honored.
func (h Header) PRGAMSize() int {
	if h.PRGRAMDecl > 0 {
		return h.PRGRAMDecl
	}
	if h.NES2 {
		return 0
	}
	return 8192
}

func newMapper(num int) (mapper, error) {
	switch num {
	case 0:
		return baseMapper{}, nil
	case 1:
		return newMMC1(), nil
	case 2:
		return &uxrom{}, nil
	case 3:
		return &cnrom{}, nil
	case 4:
		return newMMC3(), nil
	case 7:
		return &axrom{}, nil
	case 11:
		return &colorDreams{}, nil
	case 66:
		return &gxrom{}, nil
	}
	return nil, fmt.Errorf("cart: mapper %d not implemented in this core yet", num)
}

// parseHeader decodes the 16-byte header and returns the offset
// of the PRG ROM (past the header and any trainer).
func parseHeader(data []byte) (Header, int, error) {
	var h Header
	if len(data) < 16 {
		return h, 0, errors.New("cart: image shorter than a header")
	}
	if data[0] != 'N' || data[1] != 'E' || data[2] != 'S' || data[3] != 0x1A {
		return h, 0, errors.New("cart: missing iNES magic")
	}
	flags6, flags7 := data[6], data[7]
	h.NES2 = flags7&0x0C == 0x08
	h.Mapper = int(flags6>>4) | int(flags7&0xF0)
	h.Battery = flags6&0x02 != 0
	h.Trainer = flags6&0x04 != 0
	if flags6&0x08 != 0 {
		h.Mirror = MirrorFourScreen
	} else if flags6&0x01 != 0 {
		h.Mirror = MirrorVertical
	} else {
		h.Mirror = MirrorHorizontal
	}

	if h.NES2 {
		h.Mapper |= int(data[8]&0x0F) << 8
		h.Submapper = int(data[8] >> 4)
		h.PRGSize = nes2Size(data[4], data[9]&0x0F, 16384)
		h.CHRSize = nes2Size(data[5], data[9]>>4, 8192)
		h.PRGRAMDecl = nes2ShiftSize(data[10]&0x0F, data[10]>>4)
		h.CHRRAMDecl = nes2ShiftSize(data[11]&0x0F, data[11]>>4)
		switch data[7] & 0x03 {
		case 1:
			h.VS = true
		case 2:
			h.PlayChoice = true
		}
	} else {
		h.PRGSize = int(data[4]) * 16384
		h.CHRSize = int(data[5]) * 8192
		if data[8] > 0 {
			h.PRGRAMDecl = int(data[8]) * 8192
		}
		h.VS = flags7&0x01 != 0
		h.PlayChoice = flags7&0x02 != 0
	}
	off := 16
	if h.Trainer {
		off += 512
	}
	return h, off, nil
}

// nes2Size decodes a NES 2.0 ROM size field: either a plain
// 12-bit count of units, or, when the high nibble is 0xF, the
// exponent-multiplier form 2^E * (2*M + 1) bytes.
func nes2Size(lo byte, hi byte, unit int) int {
	if hi == 0x0F {
		exp := int(lo >> 2)
		mult := int(lo&0x03)*2 + 1
		return (1 << exp) * mult
	}
	return (int(hi)<<8 | int(lo)) * unit
}

// nes2ShiftSize decodes a NES 2.0 RAM size field pair (volatile
// and non-volatile shift counts). The cart uses the larger of the
// two as its RAM size; each nonzero shift means 64 << shift bytes.
func nes2ShiftSize(volatile, nonvolatile byte) int {
	size := 0
	for _, s := range []byte{volatile, nonvolatile} {
		if s > 0 {
			if n := 64 << s; n > size {
				size = n
			}
		}
	}
	return size
}

// SetCycle records the CPU cycle of the current access. The
// system calls it from its bus accessors so cycle-sensitive
// mapper behavior (the MMC1 consecutive-write quirk) sees
// realistic spacing.
func (c *Cart) SetCycle(cycle int64) { c.cycle = cycle }

// ReadPRG reads the CPU-side cartridge space, $6000-$FFFF.
// Addresses below $6000 are expansion space and read as zero
// here; the system owns that range.
func (c *Cart) ReadPRG(addr uint16) uint8 {
	if addr >= 0x6000 && addr < 0x8000 {
		if len(c.prgRAM) > 0 && c.mapper.ramEnabled() {
			return c.prgRAM[int(addr-0x6000)%len(c.prgRAM)]
		}
		return 0
	}
	if addr < 0x8000 {
		return 0
	}
	return c.mapper.readPRG(c, addr)
}

// WritePRG writes the CPU-side cartridge space, $6000-$FFFF.
func (c *Cart) WritePRG(addr uint16, val uint8) {
	if addr >= 0x6000 && addr < 0x8000 {
		if len(c.prgRAM) > 0 && c.mapper.ramEnabled() {
			c.prgRAM[int(addr-0x6000)%len(c.prgRAM)] = val
		}
		return
	}
	if addr < 0x8000 {
		return
	}
	c.mapper.writePRG(c, addr, val)
}

// ReadCHR reads the PPU-side pattern space, $0000-$1FFF.
func (c *Cart) ReadCHR(addr uint16) uint8 { return c.mapper.readCHR(c, addr&0x1FFF) }

// WriteCHR writes the PPU-side pattern space. Writes land only
// when the cartridge carries CHR RAM.
func (c *Cart) WriteCHR(addr uint16, val uint8) { c.mapper.writeCHR(c, addr&0x1FFF, val) }

// MirrorMode reports the cartridge's current nametable
// mirroring, honoring the mapper's dynamic override.
func (c *Cart) MirrorMode() Mirror {
	if m, ok := c.mapper.mirror(); ok {
		return m
	}
	return c.Header.Mirror
}

// IRQPending reports whether the mapper is asserting its IRQ
// output (the MMC3 counter, in this slice).
func (c *Cart) IRQPending() bool { return c.mapper.irqPending() }

// ClockA12 clocks the MMC3 IRQ counter the way a rising edge of
// PPU address line A12 does. NES-1 tests drive it directly; NES-2
// calls it from the PPU.
func (c *Cart) ClockA12() {
	c.a12Clocks++
	c.mapper.clockA12()
}

// A12Clocks returns how many filtered A12 rising edges the
// cartridge has been clocked with since power-on. Diagnostic;
// the MMC3 conformance ROMs count these per frame.
func (c *Cart) A12Clocks() int64 { return c.a12Clocks }

// PRGRAM exposes the cartridge RAM contents for battery
// persistence (load) and save-file extraction.
func (c *Cart) PRGRAM() []uint8 { return c.prgRAM }

// LoadPRGRAM installs saved cartridge RAM contents.
func (c *Cart) LoadPRGRAM(data []byte) {
	copy(c.prgRAM, data)
}

// Serialize writes the cartridge state: a format byte, the
// mapper number, the PRG RAM contents, and the mapper's own
// register state.
func (c *Cart) Serialize() []byte {
	out := []byte{1, uint8(c.Header.Mapper), uint8(c.Header.Mapper >> 8)}
	out = append(out, c.prgRAM...)
	out = append(out, c.mapper.state()...)
	return out
}

// Deserialize restores a state written by Serialize.
func (c *Cart) Deserialize(in []byte) error {
	if len(in) < 3 || in[0] != 1 {
		return errors.New("cart: bad state")
	}
	if int(in[1])|int(in[2])<<8 != c.Header.Mapper {
		return errors.New("cart: state is for a different mapper")
	}
	rest := in[3:]
	if len(rest) < len(c.prgRAM) {
		return errors.New("cart: state truncated")
	}
	copy(c.prgRAM, rest[:len(c.prgRAM)])
	return c.mapper.setState(rest[len(c.prgRAM):])
}
