package gd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// openGDI parses a .gdi track list and opens every track file it names.
//
// The format is plain text: the first line is the number of tracks, and
// each following line is
//
//	trackNumber startLBA ctrl sectorSize filename offset
//
// Filenames may be quoted with double quotes, which is how names with
// spaces are stored. Track files are resolved relative to the directory
// containing the .gdi file unless the name is already absolute. The
// stored Offset is used verbatim: absolute LBA maps into the track file
// at Offset + (lba - track.LBA) * SectorSize.
func openGDI(path string) (*Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gd: read gdi %s: %w", path, err)
	}
	tracks, err := parseGDI(string(data))
	if err != nil {
		return nil, fmt.Errorf("gd: parse gdi %s: %w", path, err)
	}
	base := filepath.Dir(path)
	im := &Image{
		files: make(map[string]*os.File),
	}
	for _, tr := range tracks {
		resolved := tr.File
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(base, resolved)
		}
		f, ok := im.files[resolved]
		if !ok {
			f, err = os.Open(resolved)
			if err != nil {
				_ = im.Close()
				return nil, fmt.Errorf("gd: open track file %s: %w", resolved, err)
			}
			im.files[resolved] = f
		}
		info, err := f.Stat()
		if err != nil {
			_ = im.Close()
			return nil, fmt.Errorf("gd: stat track file %s: %w", resolved, err)
		}
		if tr.Offset < 0 || tr.Offset > info.Size() {
			_ = im.Close()
			return nil, fmt.Errorf("gd: track %d offset %d is outside file %s of size %d", tr.Number, tr.Offset, resolved, info.Size())
		}
		tr.Length = info.Size() - tr.Offset
		im.Tracks = append(im.Tracks, tr)
		im.resolved = append(im.resolved, resolved)
	}
	return im, nil
}

// parseGDI parses the text of a .gdi file into tracks. Length is left at
// zero here; openGDI fills it from the backing file size.
func parseGDI(text string) ([]Track, error) {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	count, err := strconv.Atoi(strings.Fields(lines[0])[0])
	if err != nil {
		return nil, fmt.Errorf("first line %q is not a track count", lines[0])
	}
	if count < 0 {
		return nil, fmt.Errorf("negative track count %d", count)
	}
	body := lines[1:]
	if len(body) < count {
		return nil, fmt.Errorf("declares %d tracks but lists only %d", count, len(body))
	}
	tracks := make([]Track, 0, count)
	for i := 0; i < count; i++ {
		tr, err := parseGDITrackLine(body[i])
		if err != nil {
			return nil, fmt.Errorf("track line %d: %w", i+2, err)
		}
		tracks = append(tracks, tr)
	}
	return tracks, nil
}

// parseGDITrackLine parses one .gdi track line, handling a quoted
// filename that may contain spaces.
func parseGDITrackLine(line string) (Track, error) {
	var tr Track
	rest := strings.TrimSpace(line)

	nextInt := func(what string) (int64, error) {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return 0, fmt.Errorf("missing %s", what)
		}
		end := strings.IndexAny(rest, " \t")
		var tok string
		if end < 0 {
			tok = rest
			rest = ""
		} else {
			tok = rest[:end]
			rest = rest[end:]
		}
		v, err := strconv.ParseInt(tok, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("bad %s %q", what, tok)
		}
		return v, nil
	}

	num, err := nextInt("track number")
	if err != nil {
		return tr, err
	}
	tr.Number = int(num)

	lba, err := nextInt("start LBA")
	if err != nil {
		return tr, err
	}
	tr.LBA = int(lba)

	ctrl, err := nextInt("ctrl")
	if err != nil {
		return tr, err
	}
	tr.Ctrl = int(ctrl)

	size, err := nextInt("sector size")
	if err != nil {
		return tr, err
	}
	tr.SectorSize = int(size)

	// Filename: quoted (may contain spaces) or a single bare token.
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return tr, fmt.Errorf("missing filename")
	}
	if rest[0] == '"' {
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return tr, fmt.Errorf("unterminated quoted filename")
		}
		tr.File = rest[1 : 1+end]
		rest = rest[1+end+1:]
	} else {
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			tr.File = rest
			rest = ""
		} else {
			tr.File = rest[:end]
			rest = rest[end:]
		}
	}
	if tr.File == "" {
		return tr, fmt.Errorf("empty filename")
	}

	rest = strings.TrimSpace(rest)
	if rest != "" {
		off, err := strconv.ParseInt(strings.Fields(rest)[0], 10, 64)
		if err != nil {
			return tr, fmt.Errorf("bad offset %q", rest)
		}
		tr.Offset = off
	}
	return tr, nil
}
