// Package cart loads and describes a Sega Genesis / Mega Drive ROM image.
//
// Written clean-room from the published cartridge header layout. The header
// occupies the bytes at 0x100-0x1FF of every Genesis ROM.
package cart

import (
	"encoding/binary"
	"errors"
	"strings"
)

// Cart is a loaded cartridge image plus the facts parsed from its header.
type Cart struct {
	Data       []byte // raw ROM image, big-endian as stored on the cartridge
	Title      string // overseas title (header 0x150)
	TitleJP    string // domestic title (header 0x120)
	Serial     string // serial number (header 0x180)
	Checksum   uint16 // checksum stored in the header (0x18E)
	ChecksumOK bool   // stored checksum matches the computed one
	SRAM       bool   // cartridge carries battery/save RAM
	SRAMStart  uint32 // save RAM window start (usually 0x200000)
	SRAMEnd    uint32 // save RAM window end (inclusive)
	Region     byte   // first region code byte (header 0x1F0): 'J', 'U', 'E'
}

// Load parses a ROM image. data is the whole file contents.
func Load(data []byte) (*Cart, error) {
	if len(data) < 0x200 {
		return nil, errors.New("cart: image too small to hold a Genesis header")
	}
	c := &Cart{Data: data}
	sys := strings.TrimSpace(string(data[0x100:0x110]))
	if !strings.HasPrefix(sys, "SEGA") {
		return nil, errors.New("cart: missing SEGA header at 0x100")
	}
	c.TitleJP = strings.TrimSpace(string(data[0x120:0x150]))
	c.Title = strings.TrimSpace(string(data[0x150:0x180]))
	c.Serial = strings.TrimSpace(string(data[0x180:0x18E]))
	c.Checksum = binary.BigEndian.Uint16(data[0x18E:0x190])
	c.ChecksumOK = c.Checksum == ComputeChecksum(data)
	if data[0x1B0] == 'R' && data[0x1B1] == 'A' {
		c.SRAM = true
		c.SRAMStart = binary.BigEndian.Uint32(data[0x1B4:0x1B8])
		c.SRAMEnd = binary.BigEndian.Uint32(data[0x1B8:0x1BC])
		if c.SRAMStart == 0 {
			c.SRAMStart = 0x200000
		}
		if c.SRAMEnd < c.SRAMStart {
			c.SRAMEnd = c.SRAMStart + 0xFFFF
		}
	}
	c.Region = data[0x1F0]
	return c, nil
}

// ComputeChecksum returns the Genesis header checksum for an image: the
// 16-bit wrapping sum of every word from offset 0x200 to the end.
func ComputeChecksum(data []byte) uint16 {
	var sum uint16
	for i := 0x200; i+1 < len(data); i += 2 {
		sum += binary.BigEndian.Uint16(data[i : i+2])
	}
	return sum
}

// ReadROM returns the ROM byte at a 68k address inside the cartridge window
// (0x000000-0x3FFFFF). Images smaller than the window mirror, as they do on
// real hardware when address lines go unconnected.
func (c *Cart) ReadROM(addr uint32) uint8 {
	if len(c.Data) == 0 {
		return 0xFF
	}
	return c.Data[addr%uint32(len(c.Data))]
}

// ReadROM16 returns the big-endian ROM word at addr, applying the
// same mirror wrap as ReadROM to each byte: byte one is the byte
// ReadROM(addr) returns, byte two is ReadROM(addr+1), with a single
// modulo for the pair.
func (c *Cart) ReadROM16(addr uint32) uint16 {
	n := uint32(len(c.Data))
	if n == 0 {
		return 0xFFFF
	}
	i := addr % n
	hi := uint16(c.Data[i])
	i++
	if i == n {
		i = 0
	}
	return hi<<8 | uint16(c.Data[i])
}
