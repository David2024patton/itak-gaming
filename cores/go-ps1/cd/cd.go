// Package cd reads PlayStation disc images supplied by
// the user at runtime: .cue/.bin pairs and single-file
// .iso images. No disc image is ever part of this
// repository; this package only parses and reads files
// it is handed.
//
// Sector model: a cue sheet describes tracks made of
// fixed-size sectors. Data tracks expose 2048 bytes of
// user data per sector at a mode-dependent offset;
// audio tracks are raw 2352-byte sectors. ReadSector
// returns either the raw sector or the user data, with
// MSF/LBA conversion helpers alongside.
//
// Cue positions (INDEX times) are frame counts inside
// their file: MSFToSector converts them with no 150
// frame disc offset. Disc LBAs are zero-based from the
// first track. LBAToMSF converts a disc LBA back to an
// absolute MSF time, which includes the standard 150
// frame (2 second) lead-in offset.
package cd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// TrackMode classifies a track's sector layout.
type TrackMode int

const (
	TrackData  TrackMode = iota // 2048 bytes of user data per sector
	TrackAudio                  // raw 2352-byte audio sectors
)

// Track is one cue track laid out on the disc.
type Track struct {
	Number     int
	Mode       TrackMode
	SectorSize int // bytes per sector in the file
	UserOffset int // offset of user data inside a raw sector
	UserSize   int // user bytes per sector
	StartLBA   int // first disc sector of the track, including any pregap
	DataLBA    int // disc sector where INDEX 01 data begins
	Length     int // total disc sectors, including any pregap
	Pregap     int // pregap sectors (DataLBA - StartLBA)

	file          int  // index into Image.sources
	dataSector    int  // file sector of DataLBA
	storedPregap  bool // pregap lives in the file (INDEX 00 present)
	generatedPrep bool // pregap is silence (PREGAP command)
}

// Reader is what the system package consumes: a disc
// that reports its tracks and serves sectors.
type Reader interface {
	Tracks() []Track
	// ReadSector returns the raw sector when raw is
	// true, else the sector's user data.
	ReadSector(lba int, raw bool) ([]byte, error)
	Close() error
}

// Image is an opened disc image (one or more files).
type Image struct {
	tracks  []Track
	sources []io.ReaderAt
	closers []io.Closer
}

var _ Reader = (*Image)(nil)

// Tracks returns a copy of the track layout.
func (im *Image) Tracks() []Track {
	out := make([]Track, len(im.tracks))
	copy(out, im.tracks)
	return out
}

// Close closes the underlying files.
func (im *Image) Close() error {
	var first error
	for _, cl := range im.closers {
		if err := cl.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// trackAt finds the track containing a disc LBA.
func (im *Image) trackAt(lba int) (Track, bool) {
	for _, t := range im.tracks {
		if lba >= t.StartLBA && lba < t.StartLBA+t.Length {
			return t, true
		}
	}
	return Track{}, false
}

// ReadSector returns the raw sector bytes (raw=true)
// or the user data bytes for a disc LBA. Generated
// pregap sectors read as zeros.
func (im *Image) ReadSector(lba int, raw bool) ([]byte, error) {
	t, ok := im.trackAt(lba)
	if !ok {
		return nil, fmt.Errorf("cd: LBA %d outside image (%d tracks)", lba, len(im.tracks))
	}
	size := t.UserSize
	off := t.UserOffset
	if raw {
		size = t.SectorSize
		off = 0
	}
	out := make([]byte, size)
	if t.generatedPrep && lba < t.DataLBA {
		return out, nil // pregap silence
	}
	fileSector := t.dataSector + (lba - t.DataLBA)
	src := im.sources[t.file]
	buf := make([]byte, t.SectorSize)
	if _, err := src.ReadAt(buf, int64(fileSector)*int64(t.SectorSize)); err != nil && err != io.EOF {
		return nil, fmt.Errorf("cd: reading LBA %d: %w", lba, err)
	}
	copy(out, buf[off:off+size])
	return out, nil
}

// MSFToSector converts a cue position (minutes,
// seconds, frames at 75 per second) to a sector count
// inside a file.
func MSFToSector(m, s, f int) int {
	return ((m*60)+s)*75 + f
}

// SectorToMSF converts a sector count to minutes,
// seconds, frames.
func SectorToMSF(sector int) (m, s, f int) {
	m = sector / (60 * 75)
	rem := sector % (60 * 75)
	s = rem / 75
	f = rem % 75
	return m, s, f
}

// LBAToMSF converts a zero-based disc LBA to an
// absolute MSF time, adding the standard 150 frame
// lead-in.
func LBAToMSF(lba int) (m, s, f int) {
	return SectorToMSF(lba + 150)
}

// sectorLayout resolves a cue mode string to sizes.
func sectorLayout(mode string) (sectorSize, userOff, userSize int, tm TrackMode, err error) {
	switch strings.ToUpper(mode) {
	case "AUDIO":
		return 2352, 0, 2352, TrackAudio, nil
	case "MODE1/2048":
		return 2048, 0, 2048, TrackData, nil
	case "MODE1/2352":
		return 2352, 16, 2048, TrackData, nil
	case "MODE2/2048":
		return 2048, 0, 2048, TrackData, nil
	case "MODE2/2336":
		return 2336, 8, 2048, TrackData, nil
	case "MODE2/2352":
		return 2352, 24, 2048, TrackData, nil
	}
	return 0, 0, 0, 0, fmt.Errorf("cd: unsupported track mode %q", mode)
}

// Open opens a disc image by path: .cue (with its
// referenced files), .bin (single raw Mode 2 track),
// or .iso / .img (single 2048-byte-sector data track).
func Open(path string) (*Image, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue":
		return openCue(path)
	case ".iso", ".img":
		return openFlat(path, 2048, 0, 2048, TrackData)
	case ".bin":
		// A lone .bin is treated as one raw Mode 2
		// track when it divides by 2352, else as flat
		// 2048-byte sectors.
		fi, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if fi.Size()%2352 == 0 {
			return openFlat(path, 2352, 24, 2048, TrackData)
		}
		return openFlat(path, 2048, 0, 2048, TrackData)
	}
	return nil, fmt.Errorf("cd: unsupported image extension for %q", path)
}

func openFlat(path string, sectorSize, userOff, userSize int, tm TrackMode) (*Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	n := int(fi.Size()) / sectorSize
	if n == 0 {
		f.Close()
		return nil, fmt.Errorf("cd: %q holds no full %d-byte sectors", path, sectorSize)
	}
	tr := Track{
		Number: 1, Mode: tm, SectorSize: sectorSize,
		UserOffset: userOff, UserSize: userSize,
		StartLBA: 0, DataLBA: 0, Length: n,
		file: 0, dataSector: 0,
	}
	return &Image{
		tracks:  []Track{tr},
		sources: []io.ReaderAt{f},
		closers: []io.Closer{f},
	}, nil
}

// cueTrack is the parser's working record for one
// track before disc layout is computed.
type cueTrack struct {
	track      Track
	idx0, idx1 int
	hasIdx0    bool
	hasIdx1    bool
	pregapCmd  int // PREGAP frames, -1 when absent
	fileSize   int64
}

func openCue(path string) (*Image, error) {
	cf, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer cf.Close()
	dir := filepath.Dir(path)

	im := &Image{}
	var parsed []cueTrack
	var cur *cueTrack
	var fileSizes []int64
	curFile := -1

	sc := bufio.NewScanner(cf)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := cueFields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FILE":
			if len(fields) < 2 {
				return nil, fmt.Errorf("cd: %s:%d: FILE without a name", path, lineNo)
			}
			name := fields[1]
			fp := name
			if !filepath.IsAbs(fp) {
				fp = filepath.Join(dir, name)
			}
			f, err := os.Open(fp)
			if err != nil {
				return nil, err
			}
			fi, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, err
			}
			im.sources = append(im.sources, f)
			im.closers = append(im.closers, f)
			curFile = len(im.sources) - 1
			cur = nil
			// Remember the file size on the next track
			// records via parsed entries; store it on a
			// side table keyed by file index.
			fileSizes = append(fileSizes, fi.Size())
		case "TRACK":
			if curFile < 0 {
				return nil, fmt.Errorf("cd: %s:%d: TRACK before FILE", path, lineNo)
			}
			if len(fields) < 3 {
				return nil, fmt.Errorf("cd: %s:%d: malformed TRACK", path, lineNo)
			}
			num, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("cd: %s:%d: bad track number: %w", path, lineNo, err)
			}
			ss, uo, us, tm, err := sectorLayout(fields[2])
			if err != nil {
				return nil, fmt.Errorf("cd: %s:%d: %w", path, lineNo, err)
			}
			parsed = append(parsed, cueTrack{
				track: Track{
					Number: num, Mode: tm, SectorSize: ss,
					UserOffset: uo, UserSize: us, file: curFile,
				},
				pregapCmd: -1,
				fileSize:  fileSizes[curFile],
			})
			cur = &parsed[len(parsed)-1]
		case "INDEX":
			if cur == nil || len(fields) < 3 {
				return nil, fmt.Errorf("cd: %s:%d: INDEX outside TRACK", path, lineNo)
			}
			which, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("cd: %s:%d: bad INDEX number: %w", path, lineNo, err)
			}
			sec, err := parseMSF(fields[2])
			if err != nil {
				return nil, fmt.Errorf("cd: %s:%d: %w", path, lineNo, err)
			}
			switch which {
			case 0:
				cur.idx0, cur.hasIdx0 = sec, true
			case 1:
				cur.idx1, cur.hasIdx1 = sec, true
			}
		case "PREGAP":
			if cur == nil || len(fields) < 2 {
				return nil, fmt.Errorf("cd: %s:%d: PREGAP outside TRACK", path, lineNo)
			}
			sec, err := parseMSF(fields[1])
			if err != nil {
				return nil, fmt.Errorf("cd: %s:%d: %w", path, lineNo, err)
			}
			cur.pregapCmd = sec
		default:
			// REM, FLAGS, ISRC, PERFORMER, TITLE and the
			// rest carry nothing the reader needs.
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("cd: %q declares no tracks", path)
	}

	// Lay the tracks out on the disc. File boundaries
	// for each track end where the next track in the
	// same file starts (its INDEX 00 if it has one,
	// else INDEX 01), or at the end of the file.
	discLBA := 0
	for i := range parsed {
		ct := &parsed[i]
		if !ct.hasIdx1 {
			return nil, fmt.Errorf("cd: track %d has no INDEX 01", ct.track.Number)
		}
		t := ct.track
		boundary := int(ct.fileSize) / t.SectorSize // file end, in sectors
		for j := i + 1; j < len(parsed); j++ {
			if parsed[j].track.file == t.file {
				if parsed[j].hasIdx0 {
					boundary = parsed[j].idx0
				} else {
					boundary = parsed[j].idx1
				}
				break
			}
		}
		switch {
		case ct.hasIdx0:
			// Pregap is stored in the file.
			t.storedPregap = true
			t.StartLBA = discLBA
			t.Pregap = ct.idx1 - ct.idx0
			t.DataLBA = discLBA + t.Pregap
			t.dataSector = ct.idx1
			t.Length = t.Pregap + (boundary - ct.idx1)
		case ct.pregapCmd > 0:
			// Pregap is generated silence.
			t.generatedPrep = true
			t.StartLBA = discLBA
			t.Pregap = ct.pregapCmd
			t.DataLBA = discLBA + t.Pregap
			t.dataSector = ct.idx1
			t.Length = t.Pregap + (boundary - ct.idx1)
		default:
			t.StartLBA = discLBA
			t.DataLBA = discLBA
			t.dataSector = ct.idx1
			t.Length = boundary - ct.idx1
		}
		if t.Length <= 0 {
			return nil, fmt.Errorf("cd: track %d computes to %d sectors", t.Number, t.Length)
		}
		discLBA += t.Length
		im.tracks = append(im.tracks, t)
	}
	return im, nil
}

// cueFields splits a cue line, keeping a quoted file
// name (which may contain spaces) as one field.
func cueFields(line string) []string {
	var out []string
	i := 0
	for i < len(line) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		if line[i] == '"' {
			j := strings.IndexByte(line[i+1:], '"')
			if j < 0 {
				out = append(out, line[i+1:])
				break
			}
			out = append(out, line[i+1:i+1+j])
			i += j + 2
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' && line[j] != '\t' {
			j++
		}
		out = append(out, line[i:j])
		i = j
	}
	return out
}

// parseMSF parses an mm:ss:ff cue time into a sector
// count.
func parseMSF(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("cd: bad MSF time %q", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return 0, fmt.Errorf("cd: bad MSF time %q", s)
		}
		n[i] = v
	}
	return MSFToSector(n[0], n[1], n[2]), nil
}
