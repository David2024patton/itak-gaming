// Package cart parses a Game Boy Advance cartridge
// header, written from the published GBATEK cartridge
// header layout:
//
//	0x00  entry point (an ARM branch, 4 bytes)
//	0x04  the 156 byte logo bitmap (never embedded or
//	      checked by this core; it is Nintendo data)
//	0xA0  game title, 12 bytes
//	0xAC  game code, 4 bytes
//	0xB0  maker code, 2 bytes
//	0xB2  fixed byte, must be 0x96
//	0xBC  software version
//	0xBD  header checksum: -(0x19 + sum of 0xA0..0xBC)
//
// The save type is not a header field on GBA cartridges;
// games name their save chip in an ASCII signature string
// embedded in the ROM ("SRAM_V", "FLASH_V", "FLASH512_V",
// "FLASH1M_V", "EEPROM_V", each followed by version
// digits), and Parse finds it by scanning the image the
// way the published documentation describes.
package cart

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// SaveKind classifies the save chip a cartridge's
// signature names.
type SaveKind int

const (
	SaveNone SaveKind = iota
	SaveSRAM
	SaveFlash
	SaveFlash512
	SaveFlash1M
	SaveEEPROM
)

func (k SaveKind) String() string {
	switch k {
	case SaveSRAM:
		return "SRAM"
	case SaveFlash:
		return "FLASH"
	case SaveFlash512:
		return "FLASH512"
	case SaveFlash1M:
		return "FLASH1M"
	case SaveEEPROM:
		return "EEPROM"
	}
	return "none"
}

// saveSignatures are scanned longest first, so the
// longer FLASH512_V and FLASH1M_V signatures win over
// the FLASH_V prefix they share a stem with.
var saveSignatures = []struct {
	sig  string
	kind SaveKind
}{
	{"FLASH512_V", SaveFlash512},
	{"FLASH1M_V", SaveFlash1M},
	{"EEPROM_V", SaveEEPROM},
	{"FLASH_V", SaveFlash},
	{"SRAM_V", SaveSRAM},
}

// Cart is a parsed cartridge image.
type Cart struct {
	ROM []byte // the image exactly as loaded

	EntryPoint uint32 // the word at 0x00 (an ARM branch on real games)
	Title      string // 0xA0, 12 bytes, trailing zeros and spaces trimmed
	GameCode   string // 0xAC, 4 bytes
	MakerCode  string // 0xB0, 2 bytes
	Version    uint8  // 0xBC

	HeaderChecksum      uint8 // the byte stored at 0xBD
	HeaderChecksumValid bool  // whether 0xBD matches the computed checksum

	// ROMSize is the addressable ROM size: the smallest
	// power of two that holds the image, capped at the
	// 32 MB a GamePak window serves. Undersized images
	// mirror across it (see the bus package).
	ROMSize int

	SaveKind SaveKind
	// SaveType is the full signature as found in the
	// image, signature plus its version digits (for
	// example "FLASH1M_V103"), or empty when the image
	// carries no save signature.
	SaveType string
}

// headerChecksum computes the checksum byte for header
// bytes 0xA0..0xBC: chk = -(0x19 + sum of the bytes).
func headerChecksum(rom []byte) uint8 {
	sum := 0x19
	for _, c := range rom[0xA0 : 0xBC+1] {
		sum += int(c)
	}
	return uint8(-sum)
}

// detectSave scans the image for the save signatures,
// longest signature first (see saveSignatures), and
// returns the kind and the full signature string with
// its version digits.
func detectSave(rom []byte) (SaveKind, string) {
	for _, s := range saveSignatures {
		i := bytes.Index(rom, []byte(s.sig))
		if i < 0 {
			continue
		}
		end := i + len(s.sig)
		for end < len(rom) && rom[end] >= '0' && rom[end] <= '9' {
			end++
		}
		return s.kind, string(rom[i:end])
	}
	return SaveNone, ""
}

// Parse validates and describes a cartridge image. It
// refuses images too small to hold a header and images
// whose fixed byte at 0xB2 is not 0x96. A wrong header
// checksum is reported through HeaderChecksumValid
// rather than refused: real world images with damaged
// headers exist, and the caller decides.
func Parse(rom []byte) (*Cart, error) {
	if len(rom) < 0xC0 {
		return nil, fmt.Errorf("cart: image too small for a GBA header (%d bytes, need at least %d)", len(rom), 0xC0)
	}
	if rom[0xB2] != 0x96 {
		return nil, fmt.Errorf("cart: fixed byte at 0xB2 is %#02x, want 0x96", rom[0xB2])
	}
	c := &Cart{ROM: rom}
	c.EntryPoint = binary.LittleEndian.Uint32(rom[0x00:0x04])
	c.Title = strings.TrimRight(string(rom[0xA0:0xAC]), "\x00 ")
	c.GameCode = string(rom[0xAC:0xB0])
	c.MakerCode = string(rom[0xB0:0xB2])
	c.Version = rom[0xBC]
	c.HeaderChecksum = rom[0xBD]
	c.HeaderChecksumValid = headerChecksum(rom) == rom[0xBD]
	c.ROMSize = 1
	for c.ROMSize < len(rom) && c.ROMSize < 32*1024*1024 {
		c.ROMSize <<= 1
	}
	c.SaveKind, c.SaveType = detectSave(rom)
	return c, nil
}

// Load is Parse under the family name the sibling cores
// use for their cartridge entry point.
func Load(rom []byte) (*Cart, error) { return Parse(rom) }
