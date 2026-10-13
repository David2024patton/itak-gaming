// Package cart loads and describes a Super NES cartridge
// image, and implements the console memory map the CPU
// runs against: ROM windows for the LoROM, HiROM, ExLoROM,
// and ExHiROM layouts, battery-backed SRAM windows, the
// full 128 KB of Work RAM and its low mirror, and the
// system area whose IO registers belong to later slices.
//
// Written clean-room from the published cartridge header
// layout and the memory maps in the SNES development
// documentation (the layout Nintendo documents for licensees
// and that fullsnes records): the 64-byte internal header
// at file offset 0x7FC0 for LoROM images or 0xFFC0 for
// HiROM (0x40FFC0 for ExHiROM), its map-mode, chipset,
// size, checksum, and vector fields, and the standard
// scoring heuristic emulators use to decide which candidate
// header location a real image uses.
//
// Games that need a coprocessor v1 does not implement
// (SA-1, SuperFX, DSP-1 and friends, S-DD1, SPC7110, and
// the rest of the enhancement list) are detected from the
// chipset byte and the map mode, named in the header, and
// refused by Load with an error that names the chip: the
// core never silently misruns a game whose hardware it
// does not have.
package cart

import (
	"errors"
	"fmt"
	"strings"
)

// Layout is a cartridge memory-map family.
type Layout int

const (
	LayoutLoROM Layout = iota
	LayoutHiROM
	LayoutExLoROM // LoROM wiring for images over 4 MB
	LayoutExHiROM // HiROM wiring with the upper halves swapped
)

func (l Layout) String() string {
	switch l {
	case LayoutLoROM:
		return "LoROM"
	case LayoutHiROM:
		return "HiROM"
	case LayoutExLoROM:
		return "ExLoROM"
	case LayoutExHiROM:
		return "ExHiROM"
	}
	return "unknown"
}

// Coprocessor identifies an enhancement chip the cartridge
// carries, as the header's chipset byte and map mode
// report it. CoprocessorNone is a plain ROM board.
type Coprocessor int

const (
	CoprocessorNone    Coprocessor = iota
	CoprocessorDSP                 // DSP-1/2/3/4 family (chipset 0x0x with map modes that carry it, and 0x0x generally)
	CoprocessorSuperFX             // GSU / SuperFX
	CoprocessorOBC1
	CoprocessorSA1
	CoprocessorSDD1
	CoprocessorSRTC
	CoprocessorSPC7110
	CoprocessorST010
	CoprocessorST011
	CoprocessorST018
	CoprocessorCx4
	CoprocessorOther // an enhancement byte this core does not recognize
)

func (c Coprocessor) String() string {
	switch c {
	case CoprocessorNone:
		return "none"
	case CoprocessorDSP:
		return "DSP"
	case CoprocessorSuperFX:
		return "SuperFX (GSU)"
	case CoprocessorOBC1:
		return "OBC1"
	case CoprocessorSA1:
		return "SA-1"
	case CoprocessorSDD1:
		return "S-DD1"
	case CoprocessorSRTC:
		return "S-RTC"
	case CoprocessorSPC7110:
		return "SPC7110"
	case CoprocessorST010:
		return "ST010"
	case CoprocessorST011:
		return "ST011"
	case CoprocessorST018:
		return "ST018"
	case CoprocessorCx4:
		return "Cx4"
	case CoprocessorOther:
		return "unknown enhancement chip"
	}
	return "unknown"
}

// Vectors holds the cartridge's interrupt vectors, as read
// from the header (offsets 0x20 to 0x3F).
type Vectors struct {
	NativeCOP   uint16
	NativeBRK   uint16
	NativeAbort uint16
	NativeNMI   uint16
	NativeIRQ   uint16
	EmuCOP      uint16
	EmuAbort    uint16
	EmuNMI      uint16
	EmuReset    uint16
	EmuIRQBRK   uint16
}

// Header is the parsed internal cartridge header.
type Header struct {
	Title       string // 21 bytes, JIS X 0201 decoded (see decodeTitle)
	MapMode     uint8  // raw mapper byte (0xFFD5): speed in the high nibble, layout in the low
	Chipset     uint8  // raw ROM type byte (0xFFD6)
	ROMSizeByte uint8
	RAMSizeByte uint8
	Country     uint8
	Licensee    uint8
	Version     uint8

	Layout      Layout
	FastROM     bool // map mode high nibble marks a FastROM board
	Coprocessor Coprocessor

	ROMSize int // bytes the header declares
	RAMSize int // battery-backed SRAM bytes the header declares
	Battery bool

	Checksum   uint16
	Complement uint16
	ChecksumOK bool // checksum and complement pair up (sum 0xFFFF)

	Vectors Vectors

	// headerOff is the file offset of the header this parse
	// came from.
	headerOff int
}

// candidate header locations, in the order they are tried.
var headerCandidates = []struct {
	off    int
	layout Layout
}{
	{0x007FC0, LayoutLoROM},
	{0x00FFC0, LayoutHiROM},
	{0x40FFC0, LayoutExHiROM},
}

// parseHeaderAt decodes the 64-byte header at off. The
// image must extend at least to off+64.
func parseHeaderAt(img []byte, off int, layout Layout) (Header, error) {
	var h Header
	if off < 0 || off+0x40 > len(img) {
		return h, errors.New("cart: header past end of image")
	}
	b := img[off : off+0x40]
	h.headerOff = off
	h.Layout = layout
	h.Title = decodeTitle(b[0x00:0x15])
	h.MapMode = b[0x15]
	h.Chipset = b[0x16]
	h.ROMSizeByte = b[0x17]
	h.RAMSizeByte = b[0x18]
	h.Country = b[0x19]
	h.Licensee = b[0x1A]
	h.Version = b[0x1B]
	h.Complement = uint16(b[0x1C]) | uint16(b[0x1D])<<8
	h.Checksum = uint16(b[0x1E]) | uint16(b[0x1F])<<8
	h.ChecksumOK = h.Checksum^h.Complement == 0xFFFF
	h.FastROM = h.MapMode&0x10 != 0
	if h.ROMSizeByte >= 7 && h.ROMSizeByte <= 16 {
		h.ROMSize = (1 << h.ROMSizeByte) * 1024
	}
	if h.RAMSizeByte > 0 && h.RAMSizeByte <= 12 {
		h.RAMSize = (1 << h.RAMSizeByte) * 1024
	}
	h.Coprocessor, h.Battery = decodeChipset(h.Chipset, h.MapMode)
	// Some boards carry RAM the chipset byte does not call
	// battery-backed in its low nibble but still size in
	// the RAM byte; battery persistence follows the chipset
	// byte, decoded above.
	v := func(o int) uint16 { return uint16(b[o]) | uint16(b[o+1])<<8 }
	h.Vectors = Vectors{
		NativeCOP:   v(0x24),
		NativeBRK:   v(0x26),
		NativeAbort: v(0x28),
		NativeNMI:   v(0x2A),
		NativeIRQ:   v(0x2E),
		EmuCOP:      v(0x34),
		EmuAbort:    v(0x38),
		EmuNMI:      v(0x3A),
		EmuReset:    v(0x3C),
		EmuIRQBRK:   v(0x3E),
	}
	return h, nil
}

// decodeChipset splits the ROM type byte into the
// coprocessor family (high nibble, with the low-nibble
// exceptions Nintendo's table defines) and the battery
// flag (the RAM-and-battery component forms end in 2, 5,
// or 9 in the low nibble per the published table; the
// common component codes are handled explicitly).
func decodeChipset(chipset, mapMode uint8) (Coprocessor, bool) {
	lo := chipset & 0x0F
	hi := chipset & 0xF0
	battery := lo == 0x02 || lo == 0x05 || lo == 0x09
	switch {
	case chipset <= 0x02:
		return CoprocessorNone, battery
	case hi == 0x00:
		// 0x03..0x0F with a plain map mode: the DSP family.
		return CoprocessorDSP, battery
	case hi == 0x10:
		return CoprocessorSuperFX, battery
	case hi == 0x20:
		return CoprocessorOBC1, battery
	case hi == 0x30:
		return CoprocessorSA1, battery
	case hi == 0x40:
		return CoprocessorSDD1, battery
	case hi == 0x50:
		return CoprocessorSRTC, battery
	case hi == 0xE0:
		return CoprocessorCx4, battery
	case hi == 0xF0:
		// Licensee-specific enhancement chips, told apart
		// by the full byte and the map mode.
		switch chipset {
		case 0xF3:
			return CoprocessorCx4, battery
		case 0xF5:
			if mapMode&0x0F == 0x05 {
				return CoprocessorSPC7110, battery
			}
			return CoprocessorSRTC, battery
		case 0xF6:
			// ST010 and ST011 share a byte; the ROM size
			// separates them on real boards (ST011 images
			// are larger). v1 names both unsupported; the
			// distinction is cosmetic here.
			return CoprocessorST010, battery
		case 0xF9:
			return CoprocessorCx4, battery
		}
		return CoprocessorOther, battery
	}
	return CoprocessorOther, battery
}

// decodeTitle renders the 21 title bytes. The documented
// encoding is JIS X 0201: bytes 0x20..0x7E are ASCII, and
// 0xA1..0xDF are halfwidth katakana, which map one-to-one
// onto U+FF61..U+FF9F. Other bytes (padding zeros and
// spaces, control codes) become spaces, and the result is
// trimmed.
func decodeTitle(raw []byte) string {
	var sb strings.Builder
	for _, c := range raw {
		switch {
		case c >= 0x20 && c <= 0x7E:
			sb.WriteByte(c)
		case c >= 0xA1 && c <= 0xDF:
			sb.WriteRune(rune(c) + 0xFEC0)
		default:
			sb.WriteByte(' ')
		}
	}
	return strings.TrimRight(sb.String(), " ")
}

// scoreHeader grades a candidate header the way the
// standard heuristic does: a checksum that pairs with its
// complement is the strongest signal, a reset vector whose
// target byte is a plausible first opcode and whose map
// mode matches the candidate's layout is close behind, and
// sane size bytes and a printable title break ties. A
// candidate must score above zero to be believed.
func scoreHeader(h Header, img []byte) int {
	score := 0
	if h.ChecksumOK {
		score += 4
	}
	// Map mode low nibble agreement with the candidate.
	wantMode := map[Layout]uint8{
		LayoutLoROM:   0x00,
		LayoutHiROM:   0x01,
		LayoutExHiROM: 0x05,
		LayoutExLoROM: 0x00,
	}
	if h.MapMode&0x0F == wantMode[h.Layout] {
		score += 2
	}
	// Reset vector: must land in the ROM window the layout
	// serves, and the byte there should look like a first
	// instruction (SEI, CLC, JMP, JML, STZ and friends are
	// the common power-on opcodes).
	reset := h.Vectors.EmuReset
	if target, ok := resetTarget(h.Layout, reset); ok && target < len(img) {
		switch img[target] {
		case 0x78, 0x18, 0x38, 0x4C, 0x5C, 0x9C, 0x8C, 0xA9, 0xA2, 0xA0, 0xD8, 0x58, 0xFB, 0xC2, 0xE2:
			score += 3
		default:
			score += 1
		}
	}
	// ROM size byte should be plausible for the image.
	if h.ROMSize > 0 && h.ROMSize <= len(img)*2 && h.ROMSize*2 >= len(img) {
		score++
	}
	// Mostly printable title.
	printable := 0
	for _, r := range h.Title {
		if r >= 0x20 && r != 0xFFFD {
			printable++
		}
	}
	if len(h.Title) > 0 && printable*2 >= len(h.Title) {
		score++
	}
	return score
}

// resetTarget maps a reset vector to the file offset of
// its first byte under a layout, reporting whether the
// vector is plausible for that layout at all.
func resetTarget(layout Layout, vec uint16) (int, bool) {
	switch layout {
	case LayoutLoROM, LayoutExLoROM:
		if vec < 0x8000 {
			return 0, false
		}
		return int(vec - 0x8000), true // bank 0 in the file's first page
	case LayoutHiROM, LayoutExHiROM:
		if vec < 0x8000 {
			return 0, false
		}
		return int(vec), true // bank 0xC0 in HiROM terms starts the image
	}
	return 0, false
}

// Cart is a loaded cartridge: the header facts and the ROM
// bytes. The bus serves the image through Map.
type Cart struct {
	Header Header
	rom    []byte
}

// ROM returns the cartridge ROM bytes (with any copier
// header stripped).
func (c *Cart) ROM() []byte { return c.rom }

// Load parses an image and picks its header location by
// score. It refuses images with no plausible header and
// images whose game needs a coprocessor this core does
// not implement, naming the chip in the error.
func Load(image []byte) (*Cart, error) {
	img, err := stripCopierHeader(image)
	if err != nil {
		return nil, err
	}
	if len(img) < 0x8000 {
		return nil, errors.New("cart: image too small for an SNES ROM")
	}
	var best *Header
	bestScore := 0
	for _, cand := range headerCandidates {
		h, err := parseHeaderAt(img, cand.off, cand.layout)
		if err != nil {
			continue
		}
		s := scoreHeader(h, img)
		if s > bestScore {
			hh := h
			best, bestScore = &hh, s
		}
	}
	if best == nil || bestScore < 4 {
		// A header must at least pair its checksum, or
		// combine the weaker signals convincingly: a zeroed
		// image's candidate scores 2 from the map-mode
		// nibble alone and must not pass.
		return nil, errors.New("cart: no plausible SNES header found")
	}
	// The map mode can mark an oversized LoROM image as
	// ExLoROM: LoROM wiring over more than 4 MB swaps the
	// halves like ExHiROM does.
	if best.Layout == LayoutLoROM && len(img) > 4*1024*1024 {
		best.Layout = LayoutExLoROM
	}
	if best.Coprocessor != CoprocessorNone {
		return nil, fmt.Errorf("cart: %q needs the %s coprocessor, which this core does not implement yet",
			best.Title, best.Coprocessor)
	}
	return &Cart{Header: *best, rom: img}, nil
}

// stripCopierHeader removes the 512-byte copier header
// some dump formats prepend, detected by the image size
// modulo 32 KB.
func stripCopierHeader(image []byte) ([]byte, error) {
	if len(image)%1024 == 512 {
		if len(image) <= 512 {
			return nil, errors.New("cart: image is only a copier header")
		}
		return image[512:], nil
	}
	return image, nil
}
