// Package cd reads Saturn disc images in ISO and cue sheet form.
//
// It exposes raw and user sector reads, a small ISO9660 reader for the
// root directory of the primary volume, and a parser for the Saturn IP
// header that lives in the first sector of a game disc.
package cd

import (
	"fmt"
	"os"
)

// Track describes one track of a disc image in the virtual disc layout.
//
// Start is the logical block address of the track INDEX 01 position.
// Pregap sectors occupy the virtual sectors immediately before Start and
// read back as zeros. Length counts only the data sectors of the track,
// from Start up to the next track pregap or the end of the image.
type Track struct {
	Number     int
	Mode       int
	Form2      bool
	SectorSize int
	Pregap     int
	Start      int
	Length     int
	FileOffset int64
}

// trackState pairs the public Track description with the file sector
// index of the track INDEX 01 position inside the backing data.
type trackState struct {
	pub      Track
	rawIndex int
}

// Image is an opened disc image. The whole backing file is held in
// memory, which keeps the layout logic simple and is adequate for the
// synthetic and tooling uses this package targets.
type Image struct {
	data       []byte
	tracks     []trackState
	total      int
	synthRaw   bool
	detectMode bool
}

// OpenISO opens a plain 2048 byte per sector data image holding a
// single Mode 1 track.
func OpenISO(path string) (*Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cd: open iso %s: %w", path, err)
	}
	im, err := newSingleTrack(data, 2048, 1)
	if err != nil {
		return nil, err
	}
	im.synthRaw = true
	return im, nil
}

// OpenBytes builds an in memory single track image from data.
//
// A sector size of 2048 means a Mode 1 track. A sector size of 2352
// means raw sectors whose mode is detected per sector from the sector
// header, with Mode 2 subheader form detection on Mode 2 sectors.
// Sector sizes 2336 and 2324 are accepted as Mode 2 raw forms.
func OpenBytes(data []byte, sectorSize int) (*Image, error) {
	mode := 0
	switch sectorSize {
	case 2048:
		mode = 1
	case 2352, 2336, 2324:
		mode = 2
	default:
		return nil, fmt.Errorf("cd: unsupported sector size %d", sectorSize)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	im, err := newSingleTrack(cp, sectorSize, mode)
	if err != nil {
		return nil, err
	}
	if sectorSize == 2352 {
		im.detectMode = true
	}
	if sectorSize == 2324 {
		im.tracks[0].pub.Form2 = true
	}
	return im, nil
}

// newSingleTrack validates data against sectorSize and builds an Image
// with one track starting at virtual sector zero.
func newSingleTrack(data []byte, sectorSize int, mode int) (*Image, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cd: empty image")
	}
	if len(data)%sectorSize != 0 {
		return nil, fmt.Errorf("cd: image size %d is not a multiple of sector size %d", len(data), sectorSize)
	}
	n := len(data) / sectorSize
	st := trackState{
		pub: Track{
			Number:     1,
			Mode:       mode,
			SectorSize: sectorSize,
			Start:      0,
			Length:     n,
			FileOffset: 0,
		},
		rawIndex: 0,
	}
	return &Image{data: data, tracks: []trackState{st}, total: n}, nil
}

// Tracks returns a copy of the track table.
func (im *Image) Tracks() []Track {
	out := make([]Track, len(im.tracks))
	for i, st := range im.tracks {
		out[i] = st.pub
	}
	return out
}

// NumSectors returns the total number of virtual sectors, including
// pregap sectors that read back as zeros.
func (im *Image) NumSectors() int {
	return im.total
}

// locate finds the track state owning virtual sector lba. It reports
// whether the sector is a pregap sector of that track.
func (im *Image) locate(lba int) (trackState, bool, error) {
	if lba < 0 || lba >= im.total {
		return trackState{}, false, fmt.Errorf("cd: sector %d out of range, image has %d sectors", lba, im.total)
	}
	for _, st := range im.tracks {
		segStart := st.pub.Start - st.pub.Pregap
		segEnd := st.pub.Start + st.pub.Length
		if lba >= segStart && lba < segEnd {
			return st, lba < st.pub.Start, nil
		}
	}
	return trackState{}, false, fmt.Errorf("cd: sector %d is not mapped to any track", lba)
}

// ReadSector returns the user data of one virtual sector: 2048 bytes
// for Mode 1 and Mode 2 Form 1 tracks, 2324 bytes for Mode 2 Form 2
// sectors. Pregap sectors return zero filled user data. Reading an
// audio sector returns an error.
func (im *Image) ReadSector(lba int) ([]byte, error) {
	st, pregap, err := im.locate(lba)
	if err != nil {
		return nil, err
	}
	if st.pub.Mode == 0 {
		return nil, fmt.Errorf("cd: sector %d is an audio sector, not data", lba)
	}
	if pregap {
		return make([]byte, userSize(st.pub, nil)), nil
	}
	raw, err := im.storedSector(st, lba)
	if err != nil {
		return nil, err
	}
	return im.extractUser(st, raw)
}

// ReadRawSector returns the full raw sector as stored, SectorSize
// bytes. Pregap sectors return zero filled raw sectors. For images
// opened with OpenISO a raw 2352 byte Mode 1 sector is synthesized
// around the stored 2048 data bytes, with a sync pattern, an MSF header
// for lba plus 150, and a zero filled EDC and ECC tail.
func (im *Image) ReadRawSector(lba int) ([]byte, error) {
	st, pregap, err := im.locate(lba)
	if err != nil {
		return nil, err
	}
	if pregap {
		return make([]byte, st.pub.SectorSize), nil
	}
	if im.synthRaw {
		user, err := im.storedSector(st, lba)
		if err != nil {
			return nil, err
		}
		return synthesizeRawMode1(lba, user), nil
	}
	return im.storedSector(st, lba)
}

// storedSector returns a copy of the raw stored bytes for a data
// sector of the given track at virtual sector lba.
func (im *Image) storedSector(st trackState, lba int) ([]byte, error) {
	size := int64(st.pub.SectorSize)
	off := st.pub.FileOffset + int64(lba-st.pub.Start)*size
	if off < 0 || off+size > int64(len(im.data)) {
		return nil, fmt.Errorf("cd: sector %d maps outside the backing data", lba)
	}
	out := make([]byte, size)
	copy(out, im.data[off:off+size])
	return out, nil
}

// userSize gives the user data size for a track, consulting a raw
// sector when one is supplied because Mode 2 form varies per sector.
func userSize(t Track, raw []byte) int {
	if t.Mode == 2 {
		if t.SectorSize == 2324 {
			return 2324
		}
		if raw != nil && len(raw) >= 24 && form2Sector(raw, t.SectorSize) {
			return 2324
		}
		if t.Form2 {
			return 2324
		}
	}
	return 2048
}

// extractUser pulls the user data bytes out of one raw stored sector.
func (im *Image) extractUser(st trackState, raw []byte) ([]byte, error) {
	t := st.pub
	switch t.SectorSize {
	case 2048:
		return append([]byte(nil), raw...), nil
	case 2324:
		return append([]byte(nil), raw...), nil
	case 2336:
		if form2Sector(raw, 2336) {
			return append([]byte(nil), raw[8:2332]...), nil
		}
		return append([]byte(nil), raw[8:2056]...), nil
	case 2352:
		mode := t.Mode
		if im.detectMode {
			hdr := int(raw[15])
			if hdr == 1 || hdr == 2 {
				mode = hdr
			}
		}
		if mode == 1 {
			return append([]byte(nil), raw[16:2064]...), nil
		}
		if mode == 2 {
			if form2Sector(raw, 2352) {
				return append([]byte(nil), raw[24:2348]...), nil
			}
			return append([]byte(nil), raw[24:2072]...), nil
		}
		return nil, fmt.Errorf("cd: unsupported sector mode %d", mode)
	}
	return nil, fmt.Errorf("cd: unsupported sector size %d", t.SectorSize)
}

// form2Sector reports whether a Mode 2 raw sector is Form 2. The form
// bit is bit 5 of the subheader submode byte. In a 2352 byte sector the
// subheader starts at byte 16, so the submode byte is byte 18 with its
// duplicate at byte 22. In a 2336 byte sector the subheader starts at
// byte 0, so the submode byte is byte 2 with its duplicate at byte 6.
// As a tolerance for writers that place the value one byte later, the
// byte pair after the submode pair is also accepted when both copies
// carry the form bit.
func form2Sector(raw []byte, sectorSize int) bool {
	base := 16
	if sectorSize == 2336 {
		base = 0
	}
	if len(raw) < base+8 {
		return false
	}
	if raw[base+2]&0x20 != 0 || raw[base+6]&0x20 != 0 {
		return true
	}
	if raw[base+3]&0x20 != 0 && raw[base+7]&0x20 != 0 {
		return true
	}
	return false
}

// synthesizeRawMode1 wraps 2048 user bytes in a raw 2352 byte Mode 1
// sector for virtual sector lba.
func synthesizeRawMode1(lba int, user []byte) []byte {
	out := make([]byte, 2352)
	out[0] = 0x00
	for i := 1; i <= 10; i++ {
		out[i] = 0xff
	}
	out[11] = 0x00
	m, s, f := LBAtoMSF(lba)
	out[12] = toBCD(m)
	out[13] = toBCD(s)
	out[14] = toBCD(f)
	out[15] = 1
	copy(out[16:2064], user)
	return out
}

// toBCD converts a value in the range 0 to 99 to binary coded decimal.
func toBCD(v int) byte {
	return byte((v/10)<<4 | (v % 10))
}

// fromBCD converts a binary coded decimal byte back to an integer.
func fromBCD(b byte) int {
	return int(b>>4)*10 + int(b&0x0f)
}

// MSFtoLBA converts a minutes, seconds, frames position to a logical
// block address. The 150 sector offset accounts for the disc lead in
// area, so the position 0 minutes, 2 seconds, 0 frames is LBA zero.
func MSFtoLBA(m, s, f int) int {
	return (m*60+s)*75 + f - 150
}

// LBAtoMSF converts a logical block address back to minutes, seconds
// and frames. It is the inverse of MSFtoLBA.
func LBAtoMSF(lba int) (m, s, f int) {
	total := lba + 150
	m = total / 4500
	rem := total % 4500
	s = rem / 75
	f = rem % 75
	return m, s, f
}
