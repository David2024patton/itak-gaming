// Package cart loads and describes a Game Boy cartridge image.
//
// Written clean-room from the published cartridge header layout. The
// header occupies the bytes at 0x100-0x14F of every Game Boy ROM: an
// entry point and logo, then the title, mapper type, ROM and RAM
// size codes, and checksums.
//
// The package also implements the memory bank controllers the games
// use to page ROM and RAM through the 16-bit address space: MBC0
// (plain ROM), MBC1, MBC3 (with real-time clock state), and MBC5.
// Cartridge RAM contents live in Cart.RAM, which the system owns for
// battery persistence; mapper bank registers and RTC state are kept
// here and serialized with the mapper.
package cart

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// MapperKind selects the bank switching implementation.
type MapperKind int

const (
	MBC0 MapperKind = iota // ROM only, no mapper
	MBC1
	MBC2 // parsed but not implemented in GB-1 (tiny internal RAM)
	MBC3
	MBC5
	MBCUnsupported
)

func (k MapperKind) String() string {
	switch k {
	case MBC0:
		return "MBC0 (ROM only)"
	case MBC1:
		return "MBC1"
	case MBC2:
		return "MBC2"
	case MBC3:
		return "MBC3"
	case MBC5:
		return "MBC5"
	default:
		return "unsupported"
	}
}

// RTC holds the MBC3 real-time clock state: the live counters and
// the latched copies a game reads after a latch pulse. The system
// advances the live counters; the mapper only stores and latches.
type RTC struct {
	Seconds, Minutes, Hours uint8
	DayLow                  uint8 // low 8 bits of the day counter
	DayHigh                 uint8 // bit 0: day bit 8; bit 6: halt; bit 7: day carry
	Latched                 [5]uint8
}

// SerializeSize is the fixed size of the serialized mapper state
// (bank registers, control state, and the RTC block).
const SerializeSize = 32

const serializeVersion = 1

// Cart is a loaded cartridge image plus the facts parsed from its
// header, the mapper state, and the cartridge RAM.
type Cart struct {
	Data []byte // raw ROM image as stored on the cartridge

	Title      string // title (header 0x134), trailing NULs trimmed
	CGBFlag    uint8  // 0x143: 0x80 CGB-enhanced, 0xC0 CGB-only
	SGBFlag    uint8  // 0x146
	TypeCode   uint8  // 0x147: cartridge type byte
	TypeName   string
	Kind       MapperKind
	ROMSize    int   // ROM size in bytes declared by the header
	ROMBanks   int   // number of 16 KB ROM banks
	RAMSize    int   // cartridge RAM size in bytes declared by the header
	HeaderSum  uint8 // header checksum stored at 0x14D
	HeaderOK   bool  // stored checksum matches the computed one
	LogoOK     bool  // boot logo bytes at 0x104 match the known bitmap
	Version    uint8 // 0x14C
	HasBattery bool
	HasTimer   bool // MBC3 with RTC

	RAM []byte // cartridge RAM contents, owned by the system for saves
	RTC RTC    // MBC3 clock state (only meaningful when HasTimer)

	// Mapper registers.
	ramEnabled  bool
	romBankLow  uint8 // MBC1 low 5 bits / MBC3 7 bits / MBC5 low 8 bits
	romBankHigh uint8 // MBC5 bit 8, or MBC1 upper bits register
	ramBank     uint8
	bankingMode uint8 // MBC1: 0 ROM banking, 1 RAM banking
	rtcSelect   int   // MBC3 RAM window: -1 RAM, else RTC register 0..4
	latchPrev   uint8 // MBC3 latch edge detector
}

// headerChecksum computes the Game Boy header checksum: starting
// from zero, subtract each byte from 0x134 to 0x14C and one more.
func headerChecksum(data []byte) uint8 {
	var x uint8
	for i := 0x134; i <= 0x14C; i++ {
		x = x - data[i] - 1
	}
	return x
}

// romSizeBytes maps the header ROM size code to a byte count.
func romSizeBytes(code uint8) (int, error) {
	switch {
	case code <= 8:
		return 32768 << code, nil
	case code == 0x52:
		return 72 * 16384, nil // 1.125 MB, 72 banks
	case code == 0x53:
		return 80 * 16384, nil // 1.25 MB, 80 banks
	case code == 0x54:
		return 96 * 16384, nil // 1.5 MB, 96 banks
	}
	return 0, fmt.Errorf("cart: unknown ROM size code 0x%02X", code)
}

// ramSizeBytes maps the header RAM size code to a byte count.
func ramSizeBytes(code uint8) (int, error) {
	switch code {
	case 0:
		return 0, nil
	case 1:
		return 2048, nil
	case 2:
		return 8192, nil
	case 3:
		return 32768, nil
	case 4:
		return 131072, nil
	case 5:
		return 65536, nil
	}
	return 0, fmt.Errorf("cart: unknown RAM size code 0x%02X", code)
}

// classify maps the cartridge type byte to a mapper kind and the
// battery and timer facts.
func classify(code uint8) (kind MapperKind, name string, battery, timer bool, err error) {
	switch code {
	case 0x00:
		return MBC0, "ROM ONLY", false, false, nil
	case 0x01:
		return MBC1, "MBC1", false, false, nil
	case 0x02:
		return MBC1, "MBC1+RAM", false, false, nil
	case 0x03:
		return MBC1, "MBC1+RAM+BATTERY", true, false, nil
	case 0x05:
		return MBC2, "MBC2", false, false, nil
	case 0x06:
		return MBC2, "MBC2+BATTERY", true, false, nil
	case 0x08:
		return MBC0, "ROM+RAM", false, false, nil
	case 0x09:
		return MBC0, "ROM+RAM+BATTERY", true, false, nil
	case 0x0F:
		return MBC3, "MBC3+TIMER+BATTERY", true, true, nil
	case 0x10:
		return MBC3, "MBC3+TIMER+RAM+BATTERY", true, true, nil
	case 0x11:
		return MBC3, "MBC3", false, false, nil
	case 0x12:
		return MBC3, "MBC3+RAM", false, false, nil
	case 0x13:
		return MBC3, "MBC3+RAM+BATTERY", true, false, nil
	case 0x19:
		return MBC5, "MBC5", false, false, nil
	case 0x1A:
		return MBC5, "MBC5+RAM", false, false, nil
	case 0x1B:
		return MBC5, "MBC5+RAM+BATTERY", true, false, nil
	case 0x1C:
		return MBC5, "MBC5+RUMBLE", false, false, nil
	case 0x1D:
		return MBC5, "MBC5+RUMBLE+RAM", false, false, nil
	case 0x1E:
		return MBC5, "MBC5+RUMBLE+RAM+BATTERY", true, false, nil
	}
	return MBCUnsupported, fmt.Sprintf("type 0x%02X", code), false, false,
		fmt.Errorf("cart: unsupported cartridge type 0x%02X", code)
}

// bootLogo is the 48-byte bitmap the boot ROM compares against
// 0x104-0x133, reproduced from the published header specification.
var bootLogo = [48]byte{
	0xCE, 0xED, 0x66, 0x66, 0xCC, 0x0D, 0x00, 0x0B, 0x03, 0x73, 0x00, 0x83,
	0x00, 0x0C, 0x00, 0x0D, 0x00, 0x08, 0x11, 0x1F, 0x88, 0x89, 0x00, 0x0E,
	0xDC, 0xCC, 0x6E, 0xE6, 0xDD, 0xDD, 0xD9, 0x99, 0xBB, 0xBB, 0x67, 0x63,
	0x6E, 0x0E, 0xEC, 0xCC, 0xDD, 0xDC, 0x99, 0x9F, 0xBB, 0xB9, 0x33, 0x3E,
}

// Load parses a ROM image. data is the whole file contents. Errors
// are header problems only: a truncated image, a bad size code, or
// an unsupported mapper type. A wrong header checksum or logo is
// reported through HeaderOK and LogoOK, not as an error, so test
// and homebrew images still load.
func Load(data []byte) (*Cart, error) {
	if len(data) < 0x150 {
		return nil, errors.New("cart: image too small to hold a Game Boy header")
	}
	c := &Cart{Data: data, rtcSelect: -1}
	title := data[0x134:0x144]
	if i := strings.IndexByte(string(title), 0); i >= 0 {
		title = title[:i]
	}
	c.Title = strings.TrimRight(string(title), " ")
	c.CGBFlag = data[0x143]
	c.SGBFlag = data[0x146]
	c.TypeCode = data[0x147]
	c.Version = data[0x14C]

	kind, name, battery, timer, err := classify(c.TypeCode)
	if err != nil {
		return nil, err
	}
	c.Kind, c.TypeName = kind, name
	c.HasBattery, c.HasTimer = battery, timer
	// The MBC1 and MBC3 ROM bank registers can never hold zero (a
	// zero write selects bank one), so the register powers on as 1.
	// MBC5 has no such fixup and legitimately starts on bank 0.
	if kind == MBC1 || kind == MBC3 {
		c.romBankLow = 1
	}

	romSize, err := romSizeBytes(data[0x148])
	if err != nil {
		return nil, err
	}
	c.ROMSize = romSize
	c.ROMBanks = len(data) / 16384
	if c.ROMBanks < 2 {
		c.ROMBanks = 2 // smallest real cartridge: 32 KB fixed
	}
	ramSize, err := ramSizeBytes(data[0x149])
	if err != nil {
		return nil, err
	}
	c.RAMSize = ramSize
	// MBC2 carries 512 half-bytes of internal RAM regardless of the
	// size code; it is parsed here but not mapped in GB-1.
	if kind == MBC2 {
		c.RAMSize = 512
	}
	c.RAM = make([]byte, c.RAMSize)

	c.HeaderSum = data[0x14D]
	c.HeaderOK = c.HeaderSum == headerChecksum(data)
	c.LogoOK = true
	for i, b := range bootLogo {
		if data[0x104+i] != b {
			c.LogoOK = false
			break
		}
	}
	return c, nil
}

// romByte reads the ROM image at a bank-relative address, wrapping
// out-of-range banks the way masked address lines would.
func (c *Cart) romByte(bank int, offset uint16) uint8 {
	bank %= c.ROMBanks
	addr := bank*16384 + int(offset)
	if addr < 0 || addr >= len(c.Data) {
		return 0xFF
	}
	return c.Data[addr]
}

// ReadROM serves the 0x0000-0x7FFF ROM window for the active mapper.
func (c *Cart) ReadROM(addr uint16) uint8 {
	switch c.Kind {
	case MBC1:
		if addr < 0x4000 {
			bank := 0
			if c.bankingMode == 1 {
				bank = int(c.romBankHigh) << 5
			}
			return c.romByte(bank, addr)
		}
		bank := int(c.romBankLow) | int(c.romBankHigh)<<5
		return c.romByte(bank, addr-0x4000)
	case MBC3:
		if addr < 0x4000 {
			return c.romByte(0, addr)
		}
		return c.romByte(int(c.romBankLow), addr-0x4000)
	case MBC5:
		if addr < 0x4000 {
			return c.romByte(0, addr)
		}
		bank := int(c.romBankLow) | int(c.romBankHigh)<<8
		return c.romByte(bank, addr-0x4000)
	default: // MBC0 and anything unimplemented read as flat ROM
		return c.romByte(0, addr)
	}
}

// ReadRAM serves the 0xA000-0xBFFF cartridge RAM window. A disabled
// or absent RAM reads as 0xFF, as open bus does on the hardware.
func (c *Cart) ReadRAM(addr uint16) uint8 {
	if !c.ramEnabled || len(c.RAM) == 0 {
		if c.Kind == MBC3 && c.rtcSelect >= 0 && c.ramEnabled {
			return c.RTC.Latched[c.rtcSelect]
		}
		return 0xFF
	}
	offset := int(addr - 0xA000)
	switch c.Kind {
	case MBC1:
		bank := 0
		if c.bankingMode == 1 {
			bank = int(c.ramBank)
		}
		return c.ramByte(bank*8192 + offset)
	case MBC3:
		if c.rtcSelect >= 0 {
			return c.RTC.Latched[c.rtcSelect]
		}
		return c.ramByte(int(c.ramBank)*8192 + offset)
	case MBC5:
		return c.ramByte(int(c.ramBank&0x0F)*8192 + offset)
	default:
		return c.ramByte(offset)
	}
}

func (c *Cart) ramByte(addr int) uint8 {
	if addr < 0 || addr >= len(c.RAM) {
		return 0xFF
	}
	return c.RAM[addr]
}

// Write handles writes to the cartridge address space: mapper
// control registers below 0x8000 and the RAM window above 0xA000.
func (c *Cart) Write(addr uint16, val uint8) {
	if addr >= 0xA000 && addr <= 0xBFFF {
		c.writeRAM(addr, val)
		return
	}
	switch c.Kind {
	case MBC1:
		switch {
		case addr < 0x2000:
			c.ramEnabled = val&0x0F == 0x0A
		case addr < 0x4000:
			c.romBankLow = val & 0x1F
			if c.romBankLow == 0 {
				c.romBankLow = 1 // bank zero is not selectable here
			}
		case addr < 0x6000:
			c.romBankHigh = val & 0x03
			c.ramBank = val & 0x03
		default:
			c.bankingMode = val & 1
		}
	case MBC3:
		switch {
		case addr < 0x2000:
			c.ramEnabled = val&0x0F == 0x0A
		case addr < 0x4000:
			c.romBankLow = val & 0x7F
			if c.romBankLow == 0 {
				c.romBankLow = 1
			}
		case addr < 0x6000:
			c.writeMBC3Select(val)
		default:
			if c.latchPrev == 0 && val == 1 {
				c.latchRTC()
			}
			c.latchPrev = val
		}
	case MBC5:
		switch {
		case addr < 0x2000:
			c.ramEnabled = val&0x0F == 0x0A
		case addr < 0x3000:
			c.romBankLow = val
		case addr < 0x4000:
			c.romBankHigh = val & 1
		case addr < 0x6000:
			c.ramBank = val & 0x0F
		}
	}
}

// writeMBC3Select handles the MBC3 RAM bank and RTC register select
// range: values 0x00-0x03 pick a RAM bank, 0x08-0x0C pick an RTC
// register for the RAM window.
func (c *Cart) writeMBC3Select(val uint8) {
	switch {
	case val <= 0x03:
		c.ramBank = val
		c.rtcSelect = -1
	case val >= 0x08 && val <= 0x0C:
		c.rtcSelect = int(val - 0x08)
	}
}

// latchRTC copies the live RTC counters into the latched set.
func (c *Cart) latchRTC() {
	c.RTC.Latched = [5]uint8{
		c.RTC.Seconds, c.RTC.Minutes, c.RTC.Hours, c.RTC.DayLow, c.RTC.DayHigh,
	}
}

func (c *Cart) writeRAM(addr uint16, val uint8) {
	if !c.ramEnabled {
		return
	}
	offset := int(addr - 0xA000)
	switch c.Kind {
	case MBC3:
		if c.rtcSelect >= 0 {
			c.writeRTCReg(c.rtcSelect, val)
			return
		}
		c.storeRAM(int(c.ramBank)*8192+offset, val)
	case MBC1:
		bank := 0
		if c.bankingMode == 1 {
			bank = int(c.ramBank)
		}
		c.storeRAM(bank*8192+offset, val)
	case MBC5:
		c.storeRAM(int(c.ramBank&0x0F)*8192+offset, val)
	default:
		c.storeRAM(offset, val)
	}
}

func (c *Cart) storeRAM(addr int, val uint8) {
	if addr >= 0 && addr < len(c.RAM) {
		c.RAM[addr] = val
	}
}

// writeRTCReg writes one live RTC register by its latched index:
// 0 seconds, 1 minutes, 2 hours, 3 day low, 4 day high.
func (c *Cart) writeRTCReg(i int, val uint8) {
	switch i {
	case 0:
		c.RTC.Seconds = val
	case 1:
		c.RTC.Minutes = val
	case 2:
		c.RTC.Hours = val
	case 3:
		c.RTC.DayLow = val
	case 4:
		c.RTC.DayHigh = val
	}
}

// Serialize writes the mapper register and RTC state into buf, which
// must be at least SerializeSize bytes long. RAM contents are not
// included; the system saves those with the battery file.
func (c *Cart) Serialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("cart: serialize buffer too small")
	}
	for i := 0; i < SerializeSize; i++ {
		buf[i] = 0
	}
	buf[0] = serializeVersion
	if c.ramEnabled {
		buf[1] = 1
	}
	buf[2] = c.romBankLow
	buf[3] = c.romBankHigh
	buf[4] = c.ramBank
	buf[5] = c.bankingMode
	binary.LittleEndian.PutUint16(buf[6:], int16ToU16(c.rtcSelect))
	buf[8] = c.latchPrev
	r := &c.RTC
	buf[9] = r.Seconds
	buf[10] = r.Minutes
	buf[11] = r.Hours
	buf[12] = r.DayLow
	buf[13] = r.DayHigh
	copy(buf[14:19], r.Latched[:])
	return nil
}

// Deserialize restores a state written by Serialize.
func (c *Cart) Deserialize(buf []byte) error {
	if len(buf) < SerializeSize {
		return errors.New("cart: deserialize buffer too small")
	}
	if buf[0] != serializeVersion {
		return errors.New("cart: unsupported serialize version")
	}
	c.ramEnabled = buf[1] != 0
	c.romBankLow = buf[2]
	c.romBankHigh = buf[3]
	c.ramBank = buf[4]
	c.bankingMode = buf[5]
	c.rtcSelect = int(int16(binary.LittleEndian.Uint16(buf[6:])))
	c.latchPrev = buf[8]
	r := &c.RTC
	r.Seconds, r.Minutes, r.Hours = buf[9], buf[10], buf[11]
	r.DayLow, r.DayHigh = buf[12], buf[13]
	copy(r.Latched[:], buf[14:19])
	return nil
}

func int16ToU16(v int) uint16 { return uint16(int16(v)) }
