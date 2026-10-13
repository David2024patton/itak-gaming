// Package cart parses Nintendo 64 cartridge ROM images for
// the go-n64 core: endianness detection and normalization
// of .z64/.v64/.n64 dumps into the canonical big-endian
// layout, header field extraction, and CIC chip family
// detection from the ROM's own boot area checksum.
//
// No Nintendo boot code is embedded or reproduced here.
// The CIC family is identified only by computing the CRC32
// of the boot code region already present in the user's
// ROM image and comparing it with the documented checksum
// values for each CIC family.
package cart

import (
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
)

// CIC family identifiers.
type CIC int

const (
	CICUnknown CIC = iota
	CIC6101
	CIC6102
	CIC6103
	CIC6105
	CIC6106
)

func (c CIC) String() string {
	switch c {
	case CIC6101:
		return "6101"
	case CIC6102:
		return "6102"
	case CIC6103:
		return "6103"
	case CIC6105:
		return "6105"
	case CIC6106:
		return "6106"
	}
	return "unknown"
}

// Documented CRC32 (IEEE) checksums of the boot code region
// (ROM bytes 0x40 through 0xFFF) for each CIC family.
var cicBootCRCs = map[uint32]CIC{
	0x6170A4A1: CIC6101,
	0x90BB6CB5: CIC6102,
	0x0B050EE0: CIC6103,
	0x98BC2C86: CIC6105,
	0xACC8580A: CIC6106,
}

// Cart is a parsed cartridge image.
type Cart struct {
	ROM []byte // canonical big-endian image

	ClockRate   uint32 // header offset 0x04
	BootAddress uint32 // header offset 0x08: entry point set by the boot code
	Release     uint32 // header offset 0x0C
	CRC1        uint32 // header offset 0x10
	CRC2        uint32 // header offset 0x14
	Name        string // header offset 0x20, 20 bytes
	MediaFormat byte   // header offset 0x3B
	CartType    byte   // header offset 0x3C
	Country     byte   // header offset 0x3E

	CIC CIC // detected boot chip family
}

// Endianness of a raw dump, from the first word.
const (
	magicZ64 = 0x80371240 // big-endian (.z64)
	magicV64 = 0x37804012 // bytes swapped within 16-bit pairs (.v64)
	magicN64 = 0x40123780 // bytes swapped within 32-bit words (.n64)
)

// Normalize returns the canonical big-endian form of a raw
// ROM dump, detecting .z64, .v64, and .n64 layouts by their
// first word.
func Normalize(data []byte) ([]byte, error) {
	if len(data) < 0x40 {
		return nil, errors.New("cart: image too small for an N64 header")
	}
	if len(data)%4 != 0 {
		return nil, errors.New("cart: image size is not a multiple of 4")
	}
	magic := uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	out := make([]byte, len(data))
	switch magic {
	case magicZ64:
		copy(out, data)
	case magicV64:
		for i := 0; i < len(data); i += 2 {
			out[i], out[i+1] = data[i+1], data[i]
		}
	case magicN64:
		for i := 0; i < len(data); i += 4 {
			out[i], out[i+1], out[i+2], out[i+3] = data[i+3], data[i+2], data[i+1], data[i]
		}
	default:
		return nil, fmt.Errorf("cart: unrecognized ROM magic %08x", magic)
	}
	return out, nil
}

// Parse normalizes a raw dump and extracts its header.
func Parse(data []byte) (*Cart, error) {
	rom, err := Normalize(data)
	if err != nil {
		return nil, err
	}
	be := func(off int) uint32 {
		return uint32(rom[off])<<24 | uint32(rom[off+1])<<16 | uint32(rom[off+2])<<8 | uint32(rom[off+3])
	}
	name := strings.TrimRight(string(rom[0x20:0x34]), " \x00")
	c := &Cart{
		ROM:         rom,
		ClockRate:   be(0x04),
		BootAddress: be(0x08),
		Release:     be(0x0C),
		CRC1:        be(0x10),
		CRC2:        be(0x14),
		Name:        name,
		MediaFormat: rom[0x3B],
		CartType:    rom[0x3C],
		Country:     rom[0x3E],
	}
	c.CIC = detectCIC(rom)
	return c, nil
}

// detectCIC identifies the CIC family by the CRC32 of the
// boot code region (0x40..0x1000) of the ROM itself.
func detectCIC(rom []byte) CIC {
	if len(rom) < 0x1000 {
		return CICUnknown
	}
	sum := crc32.ChecksumIEEE(rom[0x40:0x1000])
	if cic, ok := cicBootCRCs[sum]; ok {
		return cic
	}
	return CICUnknown
}
