// Package cart parses the Nintendo DS ROM header: the fixed
// 0x200-byte layout at the start of every DS cartridge image,
// documented in GBATEK. Slice 1 reads fields only. It does not
// decrypt anything: retail images carry an encrypted secure
// area whose KEY1/KEY2 scheme (documented in the manuals) is
// implemented by a later slice if direct boot of retail images
// requires it. No keys are embedded here.
package cart

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Header is the parsed DS ROM header.
type Header struct {
	Title          string // 12 bytes at 0x000, ASCII, NUL padded
	GameCode       string // 4 bytes at 0x00C
	MakerCode      string // 2 bytes at 0x010
	UnitCode       byte   // 0x012: 0 = DS, 2 = DS/DSi, 3 = DSi
	SeedSelect     byte   // 0x013: encryption seed select
	Capacity       byte   // 0x014: device capacity (128KB << n)
	ARM9Offset     uint32 // 0x020: ROM offset of the ARM9 binary
	ARM9Entry      uint32 // 0x024: ARM9 entry address
	ARM9Load       uint32 // 0x028: ARM9 load address
	ARM9Size       uint32 // 0x02C: ARM9 binary size
	ARM7Offset     uint32 // 0x030
	ARM7Entry      uint32 // 0x034
	ARM7Load       uint32 // 0x038
	ARM7Size       uint32 // 0x03C
	IconOffset     uint32 // 0x068: icon/title structure offset
	SecureCRC      uint16 // 0x06C: secure area CRC field (read only here)
	LogoCRC        uint16 // 0x15C: stored CRC of the logo area
	HeaderCRC      uint16 // 0x15E: stored header CRC
	HeaderCRCValid bool   // computed CRC16 over 0x000-0x15D matches HeaderCRC
}

// MinHeaderSize is the smallest image that can hold a header.
const MinHeaderSize = 0x160

// ErrShortImage reports an image too small to contain a header.
var ErrShortImage = errors.New("cart: image smaller than a DS header")

// crc16 computes the CRC used by the DS header: polynomial
// 0x8005 reflected (0xA001), initial value 0xFFFF.
func crc16(data []byte) uint16 {
	crc := uint32(0xFFFF)
	for _, x := range data {
		crc ^= uint32(x)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return uint16(crc)
}

// Parse reads the header from the start of a ROM image. It fails
// only on structural problems: an image too small, or ARM9/ARM7
// binary extents outside the image. A bad header CRC is reported
// in HeaderCRCValid, not as an error, because homebrew images in
// the wild carry stale CRCs.
func Parse(rom []byte) (*Header, error) {
	if len(rom) < MinHeaderSize {
		return nil, ErrShortImage
	}
	le := binary.LittleEndian
	trim := func(s string) string {
		return strings.TrimRight(s, "\x00 ")
	}
	h := &Header{
		Title:      trim(string(rom[0x000:0x00C])),
		GameCode:   string(rom[0x00C:0x010]),
		MakerCode:  string(rom[0x010:0x012]),
		UnitCode:   rom[0x012],
		SeedSelect: rom[0x013],
		Capacity:   rom[0x014],
		ARM9Offset: le.Uint32(rom[0x020:]),
		ARM9Entry:  le.Uint32(rom[0x024:]),
		ARM9Load:   le.Uint32(rom[0x028:]),
		ARM9Size:   le.Uint32(rom[0x02C:]),
		ARM7Offset: le.Uint32(rom[0x030:]),
		ARM7Entry:  le.Uint32(rom[0x034:]),
		ARM7Load:   le.Uint32(rom[0x038:]),
		ARM7Size:   le.Uint32(rom[0x03C:]),
		IconOffset: le.Uint32(rom[0x068:]),
		SecureCRC:  le.Uint16(rom[0x06C:]),
		LogoCRC:    le.Uint16(rom[0x15C:]),
		HeaderCRC:  le.Uint16(rom[0x15E:]),
	}
	h.HeaderCRCValid = crc16(rom[:0x15E]) == h.HeaderCRC
	if err := h.checkExtent("ARM9", h.ARM9Offset, h.ARM9Size, len(rom)); err != nil {
		return nil, err
	}
	if err := h.checkExtent("ARM7", h.ARM7Offset, h.ARM7Size, len(rom)); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Header) checkExtent(name string, off, size uint32, romLen int) error {
	if size == 0 {
		return nil
	}
	if uint64(off)+uint64(size) > uint64(romLen) {
		return fmt.Errorf("cart: %s binary [%08x, +%08x) exceeds image size %08x",
			name, off, size, romLen)
	}
	return nil
}

// ARM9Binary returns the ARM9 program bytes from the image.
func (h *Header) ARM9Binary(rom []byte) []byte {
	return rom[h.ARM9Offset : h.ARM9Offset+h.ARM9Size]
}

// ARM7Binary returns the ARM7 program bytes from the image.
func (h *Header) ARM7Binary(rom []byte) []byte {
	return rom[h.ARM7Offset : h.ARM7Offset+h.ARM7Size]
}

// CapacityBytes returns the cartridge capacity class in bytes
// from the header capacity byte (128 KB shifted by n).
func (h *Header) CapacityBytes() uint32 {
	return 128 << 10 << h.Capacity
}
