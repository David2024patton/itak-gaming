// Package disc parses user-supplied GameCube and Wii disc
// images: the boot header, the bi2 and apploader blocks,
// the main DOL executable, and the FST file system table.
// It only reads the user's own image bytes; nothing is
// fetched or decrypted here.
//
// Wii discs are detected and their partition table is
// reported as metadata only. Wii partitions are encrypted
// with keys this project does not have and does not want:
// no decryption is attempted, and the GameCube structures
// are not present on a Wii disc in directly usable form.
package disc

import (
	"encoding/binary"
	"errors"
	"strings"
)

// Magic words at fixed header offsets.
const (
	GCMagic  uint32 = 0xC2339F3D // at 0x1C on GameCube discs
	WiiMagic uint32 = 0x5D1C9EA3 // at 0x18 on Wii discs
)

var (
	// ErrNotGameCube: neither magic word is present.
	ErrNotGameCube = errors.New("disc: not a GameCube or Wii disc image")
	// ErrTruncated: the image ends inside a structure.
	ErrTruncated = errors.New("disc: image is truncated")
	// ErrWiiDisc: a GameCube-only structure was requested
	// from a Wii disc.
	ErrWiiDisc = errors.New("disc: Wii discs expose partition metadata only")
	// ErrNoFile: an FST path did not match any file.
	ErrNoFile = errors.New("disc: no such file in the FST")
)

// BI2 is the parsed bi2.bin block at 0x440.
type BI2 struct {
	DebugMonitorSize    uint32
	SimulatedMemorySize uint32
	ArgumentOffset      uint32
	DebugFlag           uint32
	TrackLocation       uint32
	TrackSize           uint32
	CountryCode         uint32
}

// Apploader is the parsed apploader header at 0x2440.
type Apploader struct {
	Date        string
	Entry       uint32
	Size        uint32
	TrailerSize uint32
}

// Partition is one Wii partition table entry (metadata
// only; offsets are byte offsets into the image).
type Partition struct {
	Group  int
	Offset uint32
	Type   uint32
}

// DOLSection is one loadable DOL section.
type DOLSection struct {
	Offset  uint32 // file offset inside the DOL image data
	Address uint32 // load address
	Size    uint32
	Text    bool
	Data    []byte // the section bytes, a slice of the image
}

// DOL is a parsed GameCube main executable.
type DOL struct {
	Sections   []DOLSection
	BSSAddress uint32
	BSSSize    uint32
	Entry      uint32
}

// FileEntry is one file in the FST.
type FileEntry struct {
	Path   string
	Offset uint32
	Size   uint32
}

// Image is a parsed disc image.
type Image struct {
	SystemID    byte
	GameCode    string // two characters at 0x01
	CompanyCode string // two characters at 0x03
	DiscNumber  byte
	Version     byte
	Name        string

	DOLOffset uint32
	FSTOffset uint32
	FSTSize   uint32

	BI2       BI2
	Apploader Apploader
	HasBI2    bool

	IsWii      bool
	Partitions []Partition

	data  []byte
	dol   *DOL
	files []FileEntry
}

func be32(b []byte, off uint32) uint32 {
	return binary.BigEndian.Uint32(b[off:])
}

func cstr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// Parse reads the disc header and, for GameCube discs, the
// bi2 and apploader blocks. DOL and FST parsing happen on
// first use through DOL and Files.
func Parse(data []byte) (*Image, error) {
	if len(data) < 0x440 {
		return nil, ErrTruncated
	}
	img := &Image{data: data}
	img.SystemID = data[0]
	img.GameCode = string(data[1:3])
	img.CompanyCode = string(data[3:5])
	img.DiscNumber = data[5]
	img.Version = data[6]
	img.Name = cstr(data[0x20:0x3E0])

	gc := be32(data, 0x1C) == GCMagic
	wii := be32(data, 0x18) == WiiMagic
	switch {
	case gc:
		// GameCube layout.
	case wii:
		img.IsWii = true
		img.parsePartitions()
		return img, nil
	default:
		return nil, ErrNotGameCube
	}

	img.DOLOffset = be32(data, 0x420)
	img.FSTOffset = be32(data, 0x424)
	img.FSTSize = be32(data, 0x428)

	img.BI2 = BI2{
		DebugMonitorSize:    be32(data, 0x440),
		SimulatedMemorySize: be32(data, 0x444),
		ArgumentOffset:      be32(data, 0x448),
		DebugFlag:           be32(data, 0x44C),
		TrackLocation:       be32(data, 0x450),
		TrackSize:           be32(data, 0x454),
		CountryCode:         be32(data, 0x458),
	}
	img.HasBI2 = true

	if len(data) >= 0x2460 {
		img.Apploader = Apploader{
			Date:        cstr(data[0x2440:0x2450]),
			Entry:       be32(data, 0x2450),
			Size:        be32(data, 0x2454),
			TrailerSize: be32(data, 0x2458),
		}
	}
	return img, nil
}

// parsePartitions reads the Wii partition table at
// 0x40000: four groups, each a count and a table offset
// stored shifted right by two.
func (img *Image) parsePartitions() {
	data := img.data
	if len(data) < 0x40020 {
		return
	}
	for g := 0; g < 4; g++ {
		count := be32(data, uint32(0x40000+g*8))
		table := be32(data, uint32(0x40004+g*8)) << 2
		for i := uint32(0); i < count; i++ {
			off := table + i*8
			if uint32(len(data)) < off+8 {
				return
			}
			img.Partitions = append(img.Partitions, Partition{
				Group:  g,
				Offset: be32(data, off) << 2,
				Type:   be32(data, off+4),
			})
		}
	}
}

// DOL parses the main executable at the header's DOL
// offset. The DOL header is 0x100 bytes: seven text and
// eleven data sections, each an offset, a load address,
// and a size, then the BSS address and size and the entry
// point.
func (img *Image) DOL() (*DOL, error) {
	if img.IsWii {
		return nil, ErrWiiDisc
	}
	if img.dol != nil {
		return img.dol, nil
	}
	data := img.data
	base := img.DOLOffset
	if uint32(len(data)) < base+0x100 {
		return nil, ErrTruncated
	}
	h := data[base:]
	d := &DOL{
		BSSAddress: be32(h, 0xD8),
		BSSSize:    be32(h, 0xDC),
		Entry:      be32(h, 0xE0),
	}
	add := func(offPos, addrPos, sizePos uint32, text bool) error {
		off := be32(h, offPos)
		size := be32(h, sizePos)
		if size == 0 {
			return nil
		}
		if uint32(len(data)) < base+off+size {
			return ErrTruncated
		}
		d.Sections = append(d.Sections, DOLSection{
			Offset:  off,
			Address: be32(h, addrPos),
			Size:    size,
			Text:    text,
			Data:    data[base+off : base+off+size],
		})
		return nil
	}
	for i := uint32(0); i < 7; i++ {
		if err := add(i*4, 0x48+i*4, 0x90+i*4, true); err != nil {
			return nil, err
		}
	}
	for i := uint32(0); i < 11; i++ {
		if err := add(0x1C+i*4, 0x64+i*4, 0xAC+i*4, false); err != nil {
			return nil, err
		}
	}
	img.dol = d
	return d, nil
}

type fstEntry struct {
	isDir bool
	name  string
	off   uint32
	size  uint32
}

// Files walks the FST and returns every file with its full
// path. The FST is a table of 12-byte entries followed by
// a string table; the root entry's size field is the total
// entry count, and a directory's size field is the index
// just past the end of its subtree.
func (img *Image) Files() ([]FileEntry, error) {
	if img.IsWii {
		return nil, ErrWiiDisc
	}
	if img.files != nil {
		return img.files, nil
	}
	data := img.data
	base := img.FSTOffset
	if uint32(len(data)) < base+12 {
		return nil, ErrTruncated
	}
	count := int(be32(data, base+8))
	if count <= 0 || uint32(len(data)) < base+uint32(count*12) {
		return nil, ErrTruncated
	}
	strBase := base + uint32(count*12)
	ents := make([]fstEntry, count)
	for i := 0; i < count; i++ {
		off := base + uint32(i*12)
		nameOff := uint32(data[off+1])<<16 | uint32(data[off+2])<<8 | uint32(data[off+3])
		name := ""
		if strBase+nameOff < uint32(len(data)) {
			name = cstr(data[strBase+nameOff:])
		}
		ents[i] = fstEntry{
			isDir: data[off] == 1,
			name:  name,
			off:   be32(data, off+4),
			size:  be32(data, off+8),
		}
	}
	var out []FileEntry
	var walk func(idx int, prefix string) (int, error)
	walk = func(idx int, prefix string) (int, error) {
		e := ents[idx]
		if !e.isDir {
			out = append(out, FileEntry{
				Path:   prefix + e.name,
				Offset: e.off,
				Size:   e.size,
			})
			return idx + 1, nil
		}
		end := int(e.size)
		if end > count {
			end = count
		}
		p := prefix
		if idx != 0 {
			p = prefix + e.name + "/"
		}
		i := idx + 1
		for i < end {
			var err error
			i, err = walk(i, p)
			if err != nil {
				return 0, err
			}
		}
		return end, nil
	}
	if _, err := walk(0, ""); err != nil {
		return nil, err
	}
	img.files = out
	return out, nil
}

// ReadAt copies disc bytes starting at byte offset off
// into dst and returns how many bytes were copied (fewer
// at the end of the image, zero beyond it). It serves the
// DI drive model in the system package, which reads raw
// sectors anywhere on the disc.
func (img *Image) ReadAt(off uint32, dst []byte) int {
	if off >= uint32(len(img.data)) {
		return 0
	}
	return copy(dst, img.data[off:])
}

// Extract returns the bytes of the FST file at path (with
// or without a leading slash).
func (img *Image) Extract(path string) ([]byte, error) {
	files, err := img.Files()
	if err != nil {
		return nil, err
	}
	path = strings.TrimPrefix(path, "/")
	for _, f := range files {
		if f.Path == path {
			if uint32(len(img.data)) < f.Offset+f.Size {
				return nil, ErrTruncated
			}
			out := make([]byte, f.Size)
			copy(out, img.data[f.Offset:f.Offset+f.Size])
			return out, nil
		}
	}
	return nil, ErrNoFile
}
