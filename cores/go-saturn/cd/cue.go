package cd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cueTrack is the parsed form of one TRACK block of a cue sheet.
type cueTrack struct {
	number     int
	mode       int
	declared   int
	hasIndex00 bool
	raw00      int
	hasIndex01 bool
	raw01      int
	pregapCmd  int
	hasPregap  bool
}

// OpenCue parses a cue sheet and opens the single binary file it
// references. Cue sheets naming more than one FILE are rejected.
//
// Supported lines are FILE with the BINARY type, TRACK in AUDIO,
// MODE1/2048, MODE1/2352, MODE2/2352, MODE2/2336 and MODE2/2324 forms,
// PREGAP, and INDEX 00 and INDEX 01 positions. Times are read as
// minutes, seconds and frames, where the frame count is the raw sector
// position inside the file.
//
// Virtual layout is built sequentially from sector zero. A track pregap
// (from an INDEX 00 distance or a PREGAP line) occupies virtual sectors
// before the track data, and those sectors read back as zeros. A track
// length runs from its INDEX 01 position up to the next track INDEX 00
// position, or its INDEX 01 position when the next track has no
// INDEX 00, so one simplification applies: pregap sectors stored in the
// file between INDEX 00 and INDEX 01 are presented as virtual zeros
// instead of their stored bytes. All tracks in the one file must share
// the raw sector size declared by the first sized track, because a
// single file cannot be walked with mixed strides.
func OpenCue(cuePath string) (*Image, error) {
	raw, err := os.ReadFile(cuePath)
	if err != nil {
		return nil, fmt.Errorf("cd: open cue %s: %w", cuePath, err)
	}
	fileName, tracks, err := parseCue(string(raw))
	if err != nil {
		return nil, err
	}
	binPath := fileName
	if !filepath.IsAbs(binPath) {
		binPath = filepath.Join(filepath.Dir(cuePath), fileName)
	}
	data, err := os.ReadFile(binPath)
	if err != nil {
		return nil, fmt.Errorf("cd: open cue binary %s: %w", binPath, err)
	}
	return buildCueImage(data, tracks)
}

// parseCue parses cue sheet text into the binary file name and the
// list of track blocks.
func parseCue(text string) (string, []cueTrack, error) {
	var fileName string
	fileCount := 0
	var tracks []cueTrack
	var cur *cueTrack
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch strings.ToUpper(fields[0]) {
		case "FILE":
			fileCount++
			if fileCount > 1 {
				return "", nil, fmt.Errorf("cd: cue sheets with multiple FILE entries are not supported")
			}
			name, typ, err := parseFileLine(line)
			if err != nil {
				return "", nil, err
			}
			if !strings.EqualFold(typ, "BINARY") {
				return "", nil, fmt.Errorf("cd: cue FILE type %s is not supported, want BINARY", typ)
			}
			fileName = name
		case "TRACK":
			if len(fields) < 3 {
				return "", nil, fmt.Errorf("cd: malformed TRACK line %q", line)
			}
			num, err := strconv.Atoi(fields[1])
			if err != nil {
				return "", nil, fmt.Errorf("cd: bad track number in line %q", line)
			}
			mode, size, err := parseTrackType(fields[2])
			if err != nil {
				return "", nil, err
			}
			tracks = append(tracks, cueTrack{number: num, mode: mode, declared: size})
			cur = &tracks[len(tracks)-1]
		case "INDEX":
			if cur == nil || len(fields) < 3 {
				return "", nil, fmt.Errorf("cd: malformed INDEX line %q", line)
			}
			idx, err := strconv.Atoi(fields[1])
			if err != nil {
				return "", nil, fmt.Errorf("cd: bad index number in line %q", line)
			}
			pos, err := parseCueTime(fields[2])
			if err != nil {
				return "", nil, err
			}
			if idx == 0 {
				cur.hasIndex00 = true
				cur.raw00 = pos
			} else if idx == 1 {
				cur.hasIndex01 = true
				cur.raw01 = pos
			}
		case "PREGAP":
			if cur == nil || len(fields) < 2 {
				return "", nil, fmt.Errorf("cd: malformed PREGAP line %q", line)
			}
			pos, err := parseCueTime(fields[1])
			if err != nil {
				return "", nil, err
			}
			cur.hasPregap = true
			cur.pregapCmd = pos
		default:
			// REM, CATALOG, PERFORMER, TITLE, SONGWRITER, FLAGS and
			// POSTGAP lines carry nothing the layout needs.
			continue
		}
	}
	if fileName == "" {
		return "", nil, fmt.Errorf("cd: cue sheet has no FILE entry")
	}
	if len(tracks) == 0 {
		return "", nil, fmt.Errorf("cd: cue sheet has no TRACK entry")
	}
	return fileName, tracks, nil
}

// parseFileLine extracts the file name and type from a FILE line. The
// name may be quoted, in which case it can contain spaces.
func parseFileLine(line string) (string, string, error) {
	rest := strings.TrimSpace(line[len("FILE"):])
	if strings.HasPrefix(rest, "\"") {
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			return "", "", fmt.Errorf("cd: unterminated quoted name in FILE line %q", line)
		}
		name := rest[1 : 1+end]
		typ := strings.Fields(rest[1+end+1:])
		if len(typ) < 1 {
			return "", "", fmt.Errorf("cd: FILE line %q has no type", line)
		}
		return name, typ[0], nil
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return "", "", fmt.Errorf("cd: malformed FILE line %q", line)
	}
	return fields[0], fields[1], nil
}

// parseTrackType maps a cue track type token to a mode number and a
// declared raw sector size. A size of zero means the type carries no
// size, which is the case for AUDIO.
func parseTrackType(tok string) (int, int, error) {
	switch strings.ToUpper(tok) {
	case "AUDIO":
		return 0, 0, nil
	case "MODE1/2048":
		return 1, 2048, nil
	case "MODE1/2352":
		return 1, 2352, nil
	case "MODE2/2352":
		return 2, 2352, nil
	case "MODE2/2336":
		return 2, 2336, nil
	case "MODE2/2324":
		return 2, 2324, nil
	}
	return 0, 0, fmt.Errorf("cd: unsupported track type %s", tok)
}

// parseCueTime converts an mm:ss:ff token to a raw frame count.
func parseCueTime(tok string) (int, error) {
	parts := strings.Split(tok, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("cd: bad cue time %q", tok)
	}
	vals := make([]int, 3)
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("cd: bad cue time %q", tok)
		}
		vals[i] = v
	}
	return (vals[0]*60+vals[1])*75 + vals[2], nil
}

// cueTimeString formats a raw frame count as an mm:ss:ff token. It is
// used by the tests and kept with the parser for symmetry.
func cueTimeString(frames int) string {
	m := frames / 4500
	rem := frames % 4500
	s := rem / 75
	f := rem % 75
	return fmt.Sprintf("%02d:%02d:%02d", m, s, f)
}

// buildCueImage lays parsed tracks out in the virtual disc space and
// binds them to the backing file bytes.
func buildCueImage(data []byte, tracks []cueTrack) (*Image, error) {
	sectorSize := 0
	for _, ct := range tracks {
		if ct.declared != 0 {
			if sectorSize == 0 {
				sectorSize = ct.declared
			} else if ct.declared != sectorSize {
				return nil, fmt.Errorf("cd: tracks disagree on raw sector size within one file, %d and %d", sectorSize, ct.declared)
			}
		}
	}
	if sectorSize == 0 {
		sectorSize = 2352
	}
	if len(data)%sectorSize != 0 {
		return nil, fmt.Errorf("cd: binary size %d is not a multiple of sector size %d", len(data), sectorSize)
	}
	fileSectors := len(data) / sectorSize
	states := make([]trackState, 0, len(tracks))
	cursor := 0
	for i, ct := range tracks {
		if !ct.hasIndex01 {
			return nil, fmt.Errorf("cd: track %d has no INDEX 01", ct.number)
		}
		pregap := 0
		if ct.hasIndex00 {
			if ct.raw00 > ct.raw01 {
				return nil, fmt.Errorf("cd: track %d INDEX 00 is after INDEX 01", ct.number)
			}
			pregap = ct.raw01 - ct.raw00
		} else if ct.hasPregap {
			pregap = ct.pregapCmd
		}
		length := 0
		if i+1 < len(tracks) {
			next := tracks[i+1]
			if !next.hasIndex01 {
				return nil, fmt.Errorf("cd: track %d has no INDEX 01", next.number)
			}
			boundary := next.raw01
			if next.hasIndex00 {
				boundary = next.raw00
			}
			length = boundary - ct.raw01
		} else {
			length = fileSectors - ct.raw01
		}
		if length < 0 {
			return nil, fmt.Errorf("cd: track %d has a negative length", ct.number)
		}
		start := cursor + pregap
		pub := Track{
			Number:     ct.number,
			Mode:       ct.mode,
			SectorSize: sectorSize,
			Pregap:     pregap,
			Start:      start,
			Length:     length,
			FileOffset: int64(ct.raw01) * int64(sectorSize),
		}
		if ct.declared == 2324 {
			pub.Form2 = true
		}
		states = append(states, trackState{pub: pub, rawIndex: ct.raw01})
		cursor = start + length
	}
	return &Image{data: data, tracks: states, total: cursor}, nil
}
