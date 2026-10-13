// Package scd provides the Sega CD block layer for the Genesis core.
//
// This file covers disc images: it opens user supplied .cue/.bin and
// .iso images, parses cue sheets, and serves 2048 byte user data
// sectors to the CDC model. Only the Go standard library is used.
package scd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// TrackMode identifies the layout of one track on a disc image.
type TrackMode int

const (
	// TrackMode1_2048 is a Mode 1 data track stored as bare 2048 byte
	// sectors with no sync, header, or error correction bytes.
	TrackMode1_2048 TrackMode = iota
	// TrackMode1_2352 is a Mode 1 data track stored as raw 2352 byte
	// sectors. User data lives at bytes [16:2064] of each sector.
	TrackMode1_2352
	// TrackMode2_2352 is a Mode 2 data track stored as raw 2352 byte
	// sectors. User data lives at bytes [24:2072] of each sector.
	TrackMode2_2352
	// TrackAudio is a Red Book audio track stored as raw 2352 byte
	// sectors. It carries no file system user data.
	TrackAudio
)

// sectorSize returns the on disc byte size of one sector in this mode.
func (m TrackMode) sectorSize() int {
	switch m {
	case TrackMode1_2048:
		return 2048
	case TrackMode1_2352, TrackMode2_2352, TrackAudio:
		return 2352
	default:
		return 0
	}
}

// Track describes one track of a disc image.
type Track struct {
	Number     int       // track number as written in the cue sheet, starting at 1
	Mode       TrackMode // sector layout for this track
	StartLBA   int       // first logical block address of the track
	Sectors    int       // number of sectors in the track
	FileIndex  int       // index into the disc file list that holds this track
	FileOffset int64     // byte offset of the track first sector within its file
	SectorSize int       // bytes per raw sector in the backing file
}

// Disc is an opened disc image. It keeps one file handle open per
// backing file until Close is called.
type Disc struct {
	tracks []Track
	files  []*os.File
	closed bool
}

// MSFToLBA converts a minutes, seconds, frames position to a logical
// block address. There are 75 frames per second and the first data
// sector sits at 00:02:00, which is LBA 0, hence the 150 frame offset.
func MSFToLBA(m, s, f int) int {
	return ((m*60)+s)*75 + f - 150
}

// LBAToMSF converts a logical block address back to a minutes,
// seconds, frames position. It is the inverse of MSFToLBA.
func LBAToMSF(lba int) (m, s, f int) {
	total := lba + 150
	f = total % 75
	total /= 75
	s = total % 60
	m = total / 60
	return m, s, f
}

// OpenDisc opens a disc image at path. The format is chosen from the
// file extension, case insensitively: .cue for a cue sheet with one or
// more binary image files, .iso for a bare MODE1/2048 image, and .bin
// for a bare MODE1/2352 image.
func OpenDisc(path string) (*Disc, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".iso":
		return openSingleFile(path, TrackMode1_2048)
	case ".bin":
		return openSingleFile(path, TrackMode1_2352)
	case ".cue":
		return openCue(path)
	default:
		return nil, fmt.Errorf("scd: unsupported disc image extension %q", filepath.Ext(path))
	}
}

// openSingleFile opens an image that holds exactly one data track and
// no cue sheet. It backs both the .iso and the direct .bin cases.
func openSingleFile(path string, mode TrackMode) (*Disc, error) {
	sectorSize := mode.sectorSize()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("scd: open disc image: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("scd: stat disc image %q: %w", path, err)
	}
	size := info.Size()
	if size == 0 {
		f.Close()
		return nil, fmt.Errorf("scd: disc image %q is empty", path)
	}
	if size%int64(sectorSize) != 0 {
		f.Close()
		return nil, fmt.Errorf("scd: disc image %q size %d is not a multiple of sector size %d", path, size, sectorSize)
	}
	track := Track{
		Number:     1,
		Mode:       mode,
		StartLBA:   0,
		Sectors:    int(size / int64(sectorSize)),
		FileIndex:  0,
		FileOffset: 0,
		SectorSize: sectorSize,
	}
	return &Disc{tracks: []Track{track}, files: []*os.File{f}}, nil
}

// openCue parses the cue sheet at path, opens every image file it
// names, and derives each track byte offset and sector count.
//
// Simplification: tracks are taken in cue sheet order. A track sector
// count comes from the start of the next track in the same file when
// there is one, otherwise it runs to the end of its own file. A cue
// sheet that spreads one logical track across several files is not
// supported, which matches how Sega CD images are normally shipped.
func openCue(path string) (*Disc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scd: read cue sheet: %w", err)
	}
	dir := filepath.Dir(path)

	var fileNames []string
	var tracks []Track
	currentTrack := -1

	for lineNo, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		keyword := strings.ToUpper(fields[0])
		switch keyword {
		case "FILE":
			name, fileType, err := parseCueFileLine(line)
			if err != nil {
				return nil, fmt.Errorf("scd: cue line %d: %w", lineNo+1, err)
			}
			if !strings.EqualFold(fileType, "BINARY") {
				return nil, fmt.Errorf("scd: cue line %d: unsupported file type %q, only BINARY is supported", lineNo+1, fileType)
			}
			if !filepath.IsAbs(name) {
				name = filepath.Join(dir, name)
			}
			fileNames = append(fileNames, name)
		case "TRACK":
			if len(fileNames) == 0 {
				return nil, fmt.Errorf("scd: cue line %d: TRACK appears before any FILE", lineNo+1)
			}
			if len(fields) < 3 {
				return nil, fmt.Errorf("scd: cue line %d: malformed TRACK line %q", lineNo+1, line)
			}
			number, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("scd: cue line %d: bad track number %q", lineNo+1, fields[1])
			}
			mode, err := parseCueTrackMode(fields[2])
			if err != nil {
				return nil, fmt.Errorf("scd: cue line %d: %w", lineNo+1, err)
			}
			tracks = append(tracks, Track{
				Number:     number,
				Mode:       mode,
				StartLBA:   -1,
				FileIndex:  len(fileNames) - 1,
				SectorSize: mode.sectorSize(),
			})
			currentTrack = len(tracks) - 1
		case "INDEX":
			if len(fields) < 3 || fields[1] != "01" {
				// INDEX 00 and any other index points do not set the
				// track start, so they are ignored here.
				continue
			}
			if currentTrack < 0 {
				return nil, fmt.Errorf("scd: cue line %d: INDEX appears before any TRACK", lineNo+1)
			}
			m, s, fr, err := parseMSF(fields[2])
			if err != nil {
				return nil, fmt.Errorf("scd: cue line %d: %w", lineNo+1, err)
			}
			tracks[currentTrack].StartLBA = MSFToLBA(m, s, fr)
		default:
			// REM, PREGAP, FLAGS, CATALOG, and every other cue sheet
			// line carry nothing the block layer needs, so they are
			// ignored by design.
			continue
		}
	}

	if len(fileNames) == 0 {
		return nil, fmt.Errorf("scd: cue sheet %q names no image file", path)
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("scd: cue sheet %q has an empty track list", path)
	}
	for i := range tracks {
		if tracks[i].StartLBA < 0 {
			return nil, fmt.Errorf("scd: cue sheet %q track %d has no INDEX 01", path, tracks[i].Number)
		}
	}

	files := make([]*os.File, 0, len(fileNames))
	fileSizes := make([]int64, 0, len(fileNames))
	closeFiles := func() {
		for _, f := range files {
			f.Close()
		}
	}
	for _, name := range fileNames {
		f, err := os.Open(name)
		if err != nil {
			closeFiles()
			return nil, fmt.Errorf("scd: open cue image file %q: %w", name, err)
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			closeFiles()
			return nil, fmt.Errorf("scd: stat cue image file %q: %w", name, err)
		}
		files = append(files, f)
		fileSizes = append(fileSizes, info.Size())
	}

	// Byte offsets accumulate within each file: a track starts where
	// the previous track in the same file ended, measured in that
	// previous track sector size.
	for i := range tracks {
		prev := previousTrackInFile(tracks, i)
		if prev < 0 {
			tracks[i].FileOffset = 0
			continue
		}
		gap := tracks[i].StartLBA - tracks[prev].StartLBA
		tracks[i].FileOffset = tracks[prev].FileOffset + int64(gap)*int64(tracks[prev].SectorSize)
	}
	for i := range tracks {
		next := nextTrackInFile(tracks, i)
		if next >= 0 {
			tracks[i].Sectors = tracks[next].StartLBA - tracks[i].StartLBA
			continue
		}
		remaining := fileSizes[tracks[i].FileIndex] - tracks[i].FileOffset
		tracks[i].Sectors = int(remaining / int64(tracks[i].SectorSize))
	}
	for i := range tracks {
		if tracks[i].Sectors <= 0 {
			closeFiles()
			return nil, fmt.Errorf("scd: cue sheet %q track %d has no sectors in its image file", path, tracks[i].Number)
		}
	}

	return &Disc{tracks: tracks, files: files}, nil
}

// previousTrackInFile returns the index of the closest track before i
// that lives in the same file as track i, or -1 when there is none.
func previousTrackInFile(tracks []Track, i int) int {
	for j := i - 1; j >= 0; j-- {
		if tracks[j].FileIndex == tracks[i].FileIndex {
			return j
		}
	}
	return -1
}

// nextTrackInFile returns the index of the closest track after i that
// lives in the same file as track i, or -1 when there is none.
func nextTrackInFile(tracks []Track, i int) int {
	for j := i + 1; j < len(tracks); j++ {
		if tracks[j].FileIndex == tracks[i].FileIndex {
			return j
		}
	}
	return -1
}

// parseCueFileLine splits a FILE line into its image name and its
// file type. The name may be quoted, in which case it can contain
// spaces, or it may be a single bare token.
func parseCueFileLine(line string) (name, fileType string, err error) {
	rest := strings.TrimSpace(line[len(strings.Fields(line)[0]):])
	if rest == "" {
		return "", "", fmt.Errorf("malformed FILE line %q", line)
	}
	if rest[0] == '"' {
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return "", "", fmt.Errorf("unterminated quoted file name in FILE line %q", line)
		}
		name = rest[1 : 1+end]
		rest = strings.TrimSpace(rest[1+end+1:])
	} else {
		parts := strings.Fields(rest)
		name = parts[0]
		rest = strings.TrimSpace(strings.TrimPrefix(rest, name))
	}
	typeFields := strings.Fields(rest)
	if len(typeFields) == 0 {
		return "", "", fmt.Errorf("FILE line %q has no file type", line)
	}
	return name, typeFields[0], nil
}

// parseCueTrackMode maps a cue sheet track mode token to a TrackMode.
func parseCueTrackMode(token string) (TrackMode, error) {
	switch strings.ToUpper(token) {
	case "MODE1/2048":
		return TrackMode1_2048, nil
	case "MODE1/2352":
		return TrackMode1_2352, nil
	case "MODE2/2352":
		return TrackMode2_2352, nil
	case "AUDIO":
		return TrackAudio, nil
	default:
		return 0, fmt.Errorf("unsupported track mode %q", token)
	}
}

// parseMSF parses an mm:ss:ff cue sheet timestamp.
func parseMSF(text string) (m, s, f int, err error) {
	parts := strings.Split(text, ":")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("bad MSF timestamp %q", text)
	}
	values := make([]int, 3)
	for i, part := range parts {
		value, convErr := strconv.Atoi(part)
		if convErr != nil {
			return 0, 0, 0, fmt.Errorf("bad MSF timestamp %q", text)
		}
		values[i] = value
	}
	return values[0], values[1], values[2], nil
}

// Close closes every backing file handle. It is idempotent: calling
// it more than once returns nil after the first call.
func (d *Disc) Close() error {
	if d == nil || d.closed {
		return nil
	}
	d.closed = true
	var firstErr error
	for _, f := range d.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// NumSectors returns the total number of sectors across all tracks.
func (d *Disc) NumSectors() int {
	total := 0
	for _, track := range d.tracks {
		total += track.Sectors
	}
	return total
}

// Tracks returns a copy of the track list, so callers cannot mutate
// the state the disc reads through.
func (d *Disc) Tracks() []Track {
	out := make([]Track, len(d.tracks))
	copy(out, d.tracks)
	return out
}

// DataTracks returns the number of non audio tracks on the disc.
func (d *Disc) DataTracks() int {
	count := 0
	for _, track := range d.tracks {
		if track.Mode != TrackAudio {
			count++
		}
	}
	return count
}

// ReadData returns exactly 2048 bytes of user data for the sector at
// lba. Audio sectors and out of range addresses return an error. The
// returned slice is freshly allocated on every call.
func (d *Disc) ReadData(lba int) ([]byte, error) {
	if d == nil || d.closed {
		return nil, fmt.Errorf("scd: ReadData on a closed disc")
	}
	track := d.trackForLBA(lba)
	if track == nil {
		return nil, fmt.Errorf("scd: LBA %d is out of range, disc has %d sectors", lba, d.NumSectors())
	}
	if track.Mode == TrackAudio {
		return nil, fmt.Errorf("scd: LBA %d is in audio track %d, audio tracks have no user data", lba, track.Number)
	}
	offset := track.FileOffset + int64(lba-track.StartLBA)*int64(track.SectorSize)
	raw := make([]byte, track.SectorSize)
	if _, err := d.files[track.FileIndex].ReadAt(raw, offset); err != nil {
		return nil, fmt.Errorf("scd: read sector at LBA %d: %w", lba, err)
	}
	out := make([]byte, 2048)
	switch track.Mode {
	case TrackMode1_2048:
		copy(out, raw)
	case TrackMode1_2352:
		copy(out, raw[16:2064])
	case TrackMode2_2352:
		copy(out, raw[24:2072])
	default:
		return nil, fmt.Errorf("scd: track %d has unsupported mode %d", track.Number, track.Mode)
	}
	return out, nil
}

// ReadAudio returns a freshly allocated 2352 byte raw sector for
// the sector at lba. It serves any track whose sector size is 2352,
// audio or data. Tracks stored as 2048 byte sectors and out of
// range addresses return an error.
func (d *Disc) ReadAudio(lba int) ([]byte, error) {
	if d == nil || d.closed {
		return nil, fmt.Errorf("scd: ReadAudio on a closed disc")
	}
	track := d.trackForLBA(lba)
	if track == nil {
		return nil, fmt.Errorf("scd: LBA %d is out of range, disc has %d sectors", lba, d.NumSectors())
	}
	if track.SectorSize != 2352 {
		return nil, fmt.Errorf("scd: LBA %d is in track %d with sector size %d, want 2352", lba, track.Number, track.SectorSize)
	}
	offset := track.FileOffset + int64(lba-track.StartLBA)*int64(track.SectorSize)
	out := make([]byte, 2352)
	if _, err := d.files[track.FileIndex].ReadAt(out, offset); err != nil {
		return nil, fmt.Errorf("scd: read audio sector at LBA %d: %w", lba, err)
	}
	return out, nil
}

// trackForLBA finds the track that owns lba, or nil when no track does.
func (d *Disc) trackForLBA(lba int) *Track {
	for i := range d.tracks {
		track := &d.tracks[i]
		if lba >= track.StartLBA && lba < track.StartLBA+track.Sectors {
			return track
		}
	}
	return nil
}
