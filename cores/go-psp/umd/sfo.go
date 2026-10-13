package umd

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// SFO format codes from the documented PSF (PARAM.SFO) format.
const (
	sfoFormatRawString = 0x0004 // raw UTF-8 string, not null terminated
	sfoFormatString    = 0x0204 // UTF-8 string, null terminated
	sfoFormatInt32     = 0x0404 // little endian int32
)

// SFOValue is one PARAM.SFO value: either a string or an int32.
type SFOValue struct {
	IsInt bool
	Str   string
	Int   int32
}

// SFO is a parsed PARAM.SFO file.
type SFO struct {
	Values map[string]SFOValue
}

// ParseSFO parses PARAM.SFO data in the documented PSF format. Malformed
// data returns an error; ParseSFO never panics on bad input.
func ParseSFO(data []byte) (*SFO, error) {
	const headerSize = 20
	const indexEntrySize = 16
	if len(data) < headerSize {
		return nil, fmt.Errorf("umd: SFO data too short: %d bytes, need at least %d", len(data), headerSize)
	}
	if data[0] != 0x00 || data[1] != 'P' || data[2] != 'S' || data[3] != 'F' {
		return nil, fmt.Errorf("umd: bad SFO magic %q, want 0x00 PSF", string(data[0:4]))
	}
	version := binary.LittleEndian.Uint32(data[4:8])
	if version&0xFFFFFF00 != 0x00000100 {
		return nil, fmt.Errorf("umd: unsupported SFO version %#08x, want 0x000001xx", version)
	}
	keyTableOffset := binary.LittleEndian.Uint32(data[8:12])
	dataTableOffset := binary.LittleEndian.Uint32(data[12:16])
	entryCount := binary.LittleEndian.Uint32(data[16:20])

	if uint64(keyTableOffset) > uint64(len(data)) {
		return nil, fmt.Errorf("umd: SFO key table offset %d exceeds data length %d", keyTableOffset, len(data))
	}
	if uint64(dataTableOffset) > uint64(len(data)) {
		return nil, fmt.Errorf("umd: SFO data table offset %d exceeds data length %d", dataTableOffset, len(data))
	}
	indexEnd := uint64(headerSize) + uint64(entryCount)*indexEntrySize
	if indexEnd > uint64(len(data)) {
		return nil, fmt.Errorf("umd: SFO index of %d entries exceeds data length %d", entryCount, len(data))
	}

	sfo := &SFO{Values: make(map[string]SFOValue, entryCount)}
	for i := uint32(0); i < entryCount; i++ {
		off := headerSize + int(i)*indexEntrySize
		entry := data[off : off+indexEntrySize]
		keyOffset := binary.LittleEndian.Uint16(entry[0:2])
		paramFormat := binary.LittleEndian.Uint16(entry[2:4])
		paramLength := binary.LittleEndian.Uint32(entry[4:8])
		// entry[8:12] is paramMaxLength, the padded capacity of the value.
		// Reads are bounded by paramLength, so it is not needed here.
		dataOffset := binary.LittleEndian.Uint32(entry[12:16])

		key, err := sfoKey(data, keyTableOffset, keyOffset)
		if err != nil {
			return nil, fmt.Errorf("umd: SFO entry %d: %w", i, err)
		}

		valueStart := uint64(dataTableOffset) + uint64(dataOffset)
		valueEnd := valueStart + uint64(paramLength)
		switch paramFormat {
		case sfoFormatRawString:
			if valueEnd > uint64(len(data)) {
				return nil, fmt.Errorf("umd: SFO entry %d (%s): value of %d bytes at offset %d exceeds data length %d", i, key, paramLength, valueStart, len(data))
			}
			sfo.Values[key] = SFOValue{Str: string(data[valueStart:valueEnd])}
		case sfoFormatString:
			if valueEnd > uint64(len(data)) {
				return nil, fmt.Errorf("umd: SFO entry %d (%s): value of %d bytes at offset %d exceeds data length %d", i, key, paramLength, valueStart, len(data))
			}
			sfo.Values[key] = SFOValue{Str: strings.TrimRight(string(data[valueStart:valueEnd]), "\x00")}
		case sfoFormatInt32:
			if paramLength != 4 {
				return nil, fmt.Errorf("umd: SFO entry %d (%s): int32 value length %d, want 4", i, key, paramLength)
			}
			if valueEnd > uint64(len(data)) {
				return nil, fmt.Errorf("umd: SFO entry %d (%s): value at offset %d exceeds data length %d", i, key, valueStart, len(data))
			}
			sfo.Values[key] = SFOValue{
				IsInt: true,
				Int:   int32(binary.LittleEndian.Uint32(data[valueStart:valueEnd])),
			}
		default:
			// Unknown formats are skipped, not an error, so newer files
			// with extra value kinds still parse.
			continue
		}
	}
	return sfo, nil
}

// sfoKey reads the null terminated key string at
// keyTableOffset + keyOffset inside data.
func sfoKey(data []byte, keyTableOffset uint32, keyOffset uint16) (string, error) {
	start := uint64(keyTableOffset) + uint64(keyOffset)
	if start >= uint64(len(data)) {
		return "", fmt.Errorf("key offset %d exceeds data length %d", start, len(data))
	}
	rest := data[start:]
	end := -1
	for j, c := range rest {
		if c == 0 {
			end = j
			break
		}
	}
	if end < 0 {
		return "", fmt.Errorf("key at offset %d is not null terminated", start)
	}
	return string(rest[:end]), nil
}

// String returns the string value stored under key. The second result is
// false when the key is missing or holds an integer.
func (s *SFO) String(key string) (string, bool) {
	v, ok := s.Values[key]
	if !ok || v.IsInt {
		return "", false
	}
	return v.Str, true
}

// Int returns the int32 value stored under key. The second result is false
// when the key is missing or holds a string.
func (s *SFO) Int(key string) (int32, bool) {
	v, ok := s.Values[key]
	if !ok || !v.IsInt {
		return 0, false
	}
	return v.Int, true
}
