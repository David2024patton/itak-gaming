// Package gd provides GD-ROM and CD image foundations for user supplied
// disc images. It parses .gdi track lists, plain .iso images and raw .bin
// images, reads user data sectors out of any supported sector layout, and
// exposes the Dreamcast boot area and ISO9660 filesystem on top of that
// sector layer. Only the standard library is used, and no disc content is
// bundled with the package: every image is supplied by the user at runtime.
package gd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Track describes one track of a disc image.
type Track struct {
	Number     int
	LBA        int    // starting LBA on the disc
	Ctrl       int    // 0 = audio, 4 = data
	SectorSize int    // raw bytes per sector in the file (2048, 2336, 2352)
	File       string // backing file name (for .gdi); empty for single-file images
	Offset     int64  // byte offset of this track's first sector inside File
	Length     int64  // byte length of this track inside File
}

// Image is an opened disc image. The zero value is not usable; call Open.
type Image struct {
	Tracks []Track

	files    map[string]*os.File // keyed by resolved file path
	resolved []string            // resolved backing path per track, parallel to Tracks
}

// Open opens a disc image, dispatching on the file extension.
//
// Supported forms:
//   - .gdi: a GD-ROM track list. Track files are resolved relative to the
//     directory containing the .gdi file.
//   - .iso: a single data track with 2048 byte sectors.
//   - .bin: a single data track with raw 2352 byte sectors. A neighbouring
//     .cue sheet is ignored by design; a lone .bin is treated as raw data.
//   - .cdi: not supported; Open returns a clear error.
func Open(path string) (*Image, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".gdi":
		return openGDI(path)
	case ".iso":
		return openSingle(path, 2048)
	case ".bin":
		return openSingle(path, 2352)
	case ".cdi":
		return nil, fmt.Errorf("gd: .cdi images are not supported: %s", path)
	default:
		return nil, fmt.Errorf("gd: unsupported image extension %q: %s", ext, path)
	}
}

// openSingle opens a single-file image holding one data track.
func openSingle(path string, sectorSize int) (*Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("gd: open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("gd: stat %s: %w", path, err)
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("gd: %s is a directory, not an image file", path)
	}
	im := &Image{
		Tracks: []Track{{
			Number:     1,
			LBA:        0,
			Ctrl:       4,
			SectorSize: sectorSize,
			File:       "",
			Offset:     0,
			Length:     info.Size(),
		}},
		files:    map[string]*os.File{path: f},
		resolved: []string{path},
	}
	return im, nil
}

// Close releases every backing file held by the image. It is safe to call
// Close more than once.
func (im *Image) Close() error {
	if im == nil {
		return nil
	}
	var firstErr error
	for name, f := range im.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("gd: close %s: %w", name, err)
		}
	}
	im.files = nil
	return firstErr
}

// DataTrack returns the last track with Ctrl == 4, or nil when the image
// has no data track.
func (im *Image) DataTrack() *Track {
	if im == nil {
		return nil
	}
	for i := len(im.Tracks) - 1; i >= 0; i-- {
		if im.Tracks[i].Ctrl == 4 {
			return &im.Tracks[i]
		}
	}
	return nil
}

// trackForLBA returns the index of the track that owns the given absolute
// LBA. A track owns an LBA when the LBA falls inside the sectors spanned
// by the track's byte length. When several tracks could claim an LBA, the
// one with the highest starting LBA wins.
func (im *Image) trackForLBA(lba int) (int, error) {
	if im == nil {
		return -1, fmt.Errorf("gd: nil image")
	}
	best := -1
	for i := range im.Tracks {
		tr := &im.Tracks[i]
		if tr.SectorSize <= 0 {
			continue
		}
		if lba < tr.LBA {
			continue
		}
		sectors := tr.Length / int64(tr.SectorSize)
		if int64(lba-tr.LBA) >= sectors {
			continue
		}
		if best < 0 || tr.LBA > im.Tracks[best].LBA {
			best = i
		}
	}
	if best < 0 {
		return -1, fmt.Errorf("gd: LBA %d is outside every track", lba)
	}
	return best, nil
}

// ReadSector returns exactly 2048 user data bytes for the sector at the
// given absolute LBA, locating the owning track first.
//
// User data is extracted according to the track sector size: 2048 copies
// the sector as-is, 2352 (raw Mode 1) takes bytes 16 to 2064, and 2336
// skips the 8 byte subheader. Reading a sector from an audio track or
// outside the image returns an error.
func (im *Image) ReadSector(lba int) ([]byte, error) {
	idx, err := im.trackForLBA(lba)
	if err != nil {
		return nil, err
	}
	tr := &im.Tracks[idx]
	if tr.Ctrl != 4 {
		return nil, fmt.Errorf("gd: LBA %d is in audio track %d, no data to read", lba, tr.Number)
	}
	if im.files == nil {
		return nil, fmt.Errorf("gd: image is closed")
	}
	f := im.files[im.resolved[idx]]
	if f == nil {
		return nil, fmt.Errorf("gd: backing file for track %d is not open", tr.Number)
	}
	raw := make([]byte, tr.SectorSize)
	pos := tr.Offset + int64(lba-tr.LBA)*int64(tr.SectorSize)
	if _, err := f.ReadAt(raw, pos); err != nil && err != io.EOF {
		return nil, fmt.Errorf("gd: read LBA %d at offset %d: %w", lba, pos, err)
	} else if err == io.EOF {
		return nil, fmt.Errorf("gd: read LBA %d at offset %d: unexpected end of file", lba, pos)
	}
	out := make([]byte, 2048)
	switch tr.SectorSize {
	case 2048:
		copy(out, raw[0:2048])
	case 2352:
		copy(out, raw[16:2064])
	case 2336:
		copy(out, raw[8:2056])
	default:
		return nil, fmt.Errorf("gd: unsupported sector size %d on track %d", tr.SectorSize, tr.Number)
	}
	return out, nil
}

// BootArea returns the first 16 sectors (0x8000 bytes) of the data track,
// concatenated. This is the region holding the IP.BIN bootstrap block.
func (im *Image) BootArea() ([]byte, error) {
	dt := im.DataTrack()
	if dt == nil {
		return nil, fmt.Errorf("gd: image has no data track")
	}
	out := make([]byte, 0, 0x8000)
	for i := 0; i < 16; i++ {
		sec, err := im.ReadSector(dt.LBA + i)
		if err != nil {
			return nil, fmt.Errorf("gd: boot area sector %d: %w", i, err)
		}
		out = append(out, sec...)
	}
	return out, nil
}

// msfToLBA converts a minutes, seconds, frames position to a logical
// block address. Sectors run at 75 frames per second and the data area
// starts 150 sectors in, so 00:02:00 is LBA 0 and 01:00:00 is LBA 4350.
func msfToLBA(m, s, f int) int {
	return (m*60+s)*75 + f - 150
}
