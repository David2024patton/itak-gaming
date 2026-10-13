package cd

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// IPHeader is the parsed Saturn IP header, stored in the user data of
// sector zero of a game disc. Text fields are trimmed of trailing
// spaces and NUL bytes, except Signature, which is kept as the exact
// 16 bytes stored on disc.
type IPHeader struct {
	Signature     string
	MakerID       string
	ProductNumber string
	Version       string
	ReleaseDate   string
	DeviceInfo    string
	AreaSymbols   string
	Peripherals   string
	Title         string
	MasterStack   uint32
	SlaveStack    uint32
}

// saturnSignature is the exact 16 byte signature at offset zero of the
// IP header, including its trailing space.
const saturnSignature = "SEGA SEGASATURN "

// SaturnHeader reads and parses the IP header from sector zero of the
// first data track. It returns an error unless the signature matches
// the expected 16 byte string exactly.
func (im *Image) SaturnHeader() (*IPHeader, error) {
	sec, err := im.readLogicalSector(0)
	if err != nil {
		return nil, err
	}
	if len(sec) < 256 {
		return nil, fmt.Errorf("cd: sector zero is %d bytes, too small for a Saturn IP header", len(sec))
	}
	if string(sec[0:16]) != saturnSignature {
		return nil, fmt.Errorf("cd: bad Saturn signature %q, want %q", string(sec[0:16]), saturnSignature)
	}
	h := &IPHeader{
		Signature:     string(sec[0:16]),
		MakerID:       trimField(sec[0x10:0x20]),
		ProductNumber: trimField(sec[0x20:0x2a]),
		Version:       trimField(sec[0x2a:0x30]),
		ReleaseDate:   trimField(sec[0x30:0x38]),
		DeviceInfo:    trimField(sec[0x38:0x40]),
		AreaSymbols:   trimField(sec[0x40:0x4a]),
		Peripherals:   trimField(sec[0x50:0x60]),
		Title:         trimField(sec[0x60:0xd0]),
		MasterStack:   binary.BigEndian.Uint32(sec[0xf0:0xf4]),
		SlaveStack:    binary.BigEndian.Uint32(sec[0xf8:0xfc]),
	}
	return h, nil
}

// trimField converts a fixed width on disc text field to a string with
// trailing spaces and NUL bytes removed.
func trimField(b []byte) string {
	return strings.TrimRight(string(b), " \t\r\n\x00")
}
