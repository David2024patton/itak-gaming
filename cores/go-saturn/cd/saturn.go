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
//
// The binary fields follow the published System ID layout (Sega's
// disc format documentation): the IP size is the big endian word at
// 0xE0, the master SH-2 stack pointer (Stack-M) at 0xE8, the slave
// SH-2 stack pointer (Stack-S) at 0xEC, and the first read transfer
// address and size at 0xF0 and 0xF4. Slices before SAT-4 read the
// stacks from 0xF0 and 0xF8; those offsets are the first read
// fields, and the parse was corrected to the documented layout in
// SAT-4.
type IPHeader struct {
	Signature        string
	MakerID          string
	ProductNumber    string
	Version          string
	ReleaseDate      string
	DeviceInfo       string
	AreaSymbols      string
	Peripherals      string
	Title            string
	IPSize           uint32
	MasterStack      uint32
	SlaveStack       uint32
	FirstReadAddress uint32
	FirstReadSize    uint32
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
		Signature:        string(sec[0:16]),
		MakerID:          trimField(sec[0x10:0x20]),
		ProductNumber:    trimField(sec[0x20:0x2a]),
		Version:          trimField(sec[0x2a:0x30]),
		ReleaseDate:      trimField(sec[0x30:0x38]),
		DeviceInfo:       trimField(sec[0x38:0x40]),
		AreaSymbols:      trimField(sec[0x40:0x4a]),
		Peripherals:      trimField(sec[0x50:0x60]),
		Title:            trimField(sec[0x60:0xd0]),
		IPSize:           binary.BigEndian.Uint32(sec[0xe0:0xe4]),
		MasterStack:      binary.BigEndian.Uint32(sec[0xe8:0xec]),
		SlaveStack:       binary.BigEndian.Uint32(sec[0xec:0xf0]),
		FirstReadAddress: binary.BigEndian.Uint32(sec[0xf0:0xf4]),
		FirstReadSize:    binary.BigEndian.Uint32(sec[0xf4:0xf8]),
	}
	return h, nil
}

// trimField converts a fixed width on disc text field to a string with
// trailing spaces and NUL bytes removed.
func trimField(b []byte) string {
	return strings.TrimRight(string(b), " \t\r\n\x00")
}
