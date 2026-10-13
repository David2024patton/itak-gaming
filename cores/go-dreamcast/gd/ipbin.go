package gd

import (
	"fmt"
	"strings"
)

// IPBin is the parsed bootstrap block found in the first 16 sectors of a
// Dreamcast data track. Every field is a fixed width, space padded ASCII
// string in the raw block; ParseIPBin trims trailing spaces and NULs.
type IPBin struct {
	HardwareID   string // offset 0x00, 16 bytes, e.g. "SEGA SEGAKATANA "
	MakerID      string // 0x10, 16
	DeviceInfo   string // 0x20, 16
	AreaSymbols  string // 0x30, 8
	Peripherals  string // 0x38, 16
	ProductNo    string // 0x48, 10
	Version      string // 0x52, 6
	ReleaseDate  string // 0x58, 8 (YYYYMMDD)
	BootFilename string // 0x60, 16
	CompanyName  string // 0x70, 16
	Title        string // 0x80, 128
}

// ipField extracts one fixed width field, trimming trailing spaces and
// NUL padding.
func ipField(b []byte, off, length int) string {
	return strings.TrimRight(string(b[off:off+length]), " \x00")
}

// ParseIPBin parses a boot area (or at least its first 0x100 bytes) into
// an IPBin. The block must be at least 0x100 bytes long and its hardware
// ID must begin with "SEGA", otherwise an error is returned.
//
// Note on the company name offset: some references place other data in
// the 0x70 slot, but this package follows the layout in the public API
// contract, where 0x70 holds the 16 byte company name and the title
// follows at 0x80.
func ParseIPBin(b []byte) (*IPBin, error) {
	if len(b) < 0x100 {
		return nil, fmt.Errorf("gd: IP.BIN block too short: %d bytes, need at least 0x100", len(b))
	}
	if string(b[0x00:0x04]) != "SEGA" {
		return nil, fmt.Errorf("gd: IP.BIN hardware ID %q does not begin with SEGA", ipField(b, 0x00, 16))
	}
	return &IPBin{
		HardwareID:   ipField(b, 0x00, 16),
		MakerID:      ipField(b, 0x10, 16),
		DeviceInfo:   ipField(b, 0x20, 16),
		AreaSymbols:  ipField(b, 0x30, 8),
		Peripherals:  ipField(b, 0x38, 16),
		ProductNo:    ipField(b, 0x48, 10),
		Version:      ipField(b, 0x52, 6),
		ReleaseDate:  ipField(b, 0x58, 8),
		BootFilename: ipField(b, 0x60, 16),
		CompanyName:  ipField(b, 0x70, 16),
		Title:        ipField(b, 0x80, 128),
	}, nil
}

// IPBin reads the image boot area and parses it with ParseIPBin.
func (im *Image) IPBin() (*IPBin, error) {
	boot, err := im.BootArea()
	if err != nil {
		return nil, err
	}
	return ParseIPBin(boot)
}
