package cd

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Minimal ISO9660 reading over a disc Reader: the
// primary volume descriptor at sector 16, directory
// records, and file extents. Joliet and Rock Ridge
// extensions are out of scope; PlayStation discs are
// plain ISO9660 with 2048-byte logical blocks, which
// is what the user-sector reader returns.

const (
	isoSectorSize = 2048
	pvdSector     = 16
)

// DirEntry is one ISO9660 directory record.
type DirEntry struct {
	Name      string // as stored, e.g. "SYSTEM.CNF;1"; "." and ".." for the links
	ExtentLBA int    // first sector of the file or directory data
	Length    int    // data length in bytes
	IsDir     bool
}

type dirRecord struct {
	entry DirEntry
}

// ISO is an ISO9660 filesystem view of a disc.
type ISO struct {
	r          Reader
	volumeID   string
	blockSize  int
	root       DirEntry
	rootRecord dirRecord
}

// OpenISO parses the primary volume descriptor of an
// opened disc image.
func OpenISO(r Reader) (*ISO, error) {
	pvd, err := r.ReadSector(pvdSector, false)
	if err != nil {
		return nil, err
	}
	if len(pvd) < 192 {
		return nil, fmt.Errorf("cd: short primary volume descriptor (%d bytes)", len(pvd))
	}
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		return nil, fmt.Errorf("cd: sector 16 is not an ISO9660 primary volume descriptor")
	}
	blockSize := int(binary.LittleEndian.Uint16(pvd[128:]))
	if blockSize != isoSectorSize {
		return nil, fmt.Errorf("cd: ISO logical block size %d, want %d", blockSize, isoSectorSize)
	}
	root, n := parseDirRecord(pvd[156:])
	if n == 0 {
		return nil, fmt.Errorf("cd: bad root directory record in volume descriptor")
	}
	iso := &ISO{
		r:         r,
		volumeID:  strings.Trim(string(pvd[40:72]), " \x00"),
		blockSize: blockSize,
		root:      root.entry,
	}
	iso.root.Name = ""
	return iso, nil
}

// VolumeID returns the volume identifier string.
func (iso *ISO) VolumeID() string { return iso.volumeID }

// Root returns the root directory entry.
func (iso *ISO) Root() DirEntry { return iso.root }

// parseDirRecord parses one directory record from the
// front of b, returning the entry and the record's
// total length (0 at a zero padding byte).
func parseDirRecord(b []byte) (dirRecord, int) {
	if len(b) < 1 || b[0] == 0 {
		return dirRecord{}, 0
	}
	recLen := int(b[0])
	if recLen < 34 || len(b) < recLen {
		return dirRecord{}, 0
	}
	nameLen := int(b[32])
	if 33+nameLen > recLen {
		return dirRecord{}, 0
	}
	rawName := b[33 : 33+nameLen]
	name := string(rawName)
	if nameLen == 1 && rawName[0] == 0 {
		name = "."
	} else if nameLen == 1 && rawName[0] == 1 {
		name = ".."
	}
	return dirRecord{entry: DirEntry{
		Name:      name,
		ExtentLBA: int(binary.LittleEndian.Uint32(b[2:])),
		Length:    int(binary.LittleEndian.Uint32(b[10:])),
		IsDir:     b[25]&2 != 0,
	}}, recLen
}

// readExtent returns the data of an entry.
func (iso *ISO) readExtent(e DirEntry) ([]byte, error) {
	if e.Length == 0 {
		return []byte{}, nil
	}
	sectors := (e.Length + isoSectorSize - 1) / isoSectorSize
	out := make([]byte, 0, sectors*isoSectorSize)
	for i := 0; i < sectors; i++ {
		sec, err := iso.r.ReadSector(e.ExtentLBA+i, false)
		if err != nil {
			return nil, err
		}
		out = append(out, sec...)
	}
	return out[:e.Length], nil
}

// listDir parses a directory extent into entries,
// skipping the "." and ".." links.
func (iso *ISO) listDir(e DirEntry) ([]DirEntry, error) {
	data, err := iso.readExtent(e)
	if err != nil {
		return nil, err
	}
	var out []DirEntry
	off := 0
	for off < len(data) {
		rec, n := parseDirRecord(data[off:])
		if n == 0 {
			// Padding to the end of the current block.
			off = ((off / isoSectorSize) + 1) * isoSectorSize
			continue
		}
		off += n
		if rec.entry.Name == "." || rec.entry.Name == ".." {
			continue
		}
		out = append(out, rec.entry)
	}
	return out, nil
}

// lookup resolves a path like "DATA/MOVIE.STR;1" (the
// version suffix may be omitted) to its entry.
func (iso *ISO) lookup(path string) (DirEntry, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return iso.root, nil
	}
	cur := iso.root
	for _, part := range strings.Split(path, "/") {
		if !cur.IsDir {
			return DirEntry{}, fmt.Errorf("cd: %q crosses a non-directory", path)
		}
		entries, err := iso.listDir(cur)
		if err != nil {
			return DirEntry{}, err
		}
		found := false
		for _, e := range entries {
			if sameISOName(e.Name, part) {
				cur = e
				found = true
				break
			}
		}
		if !found {
			return DirEntry{}, fmt.Errorf("cd: path %q not found", path)
		}
	}
	return cur, nil
}

// sameISOName compares a stored name with a query
// component, case-insensitively, and tolerant of a
// missing ";1" version suffix in the query.
func sameISOName(stored, query string) bool {
	if strings.EqualFold(stored, query) {
		return true
	}
	if i := strings.IndexByte(stored, ';'); i >= 0 {
		return strings.EqualFold(stored[:i], query)
	}
	return false
}

// List returns the entries of the directory at path
// ("" or "/" for the root).
func (iso *ISO) List(path string) ([]DirEntry, error) {
	e, err := iso.lookup(path)
	if err != nil {
		return nil, err
	}
	if !e.IsDir {
		return nil, fmt.Errorf("cd: %q is not a directory", path)
	}
	return iso.listDir(e)
}

// ReadFile returns the full contents of the file at
// path.
func (iso *ISO) ReadFile(path string) ([]byte, error) {
	e, err := iso.lookup(path)
	if err != nil {
		return nil, err
	}
	if e.IsDir {
		return nil, fmt.Errorf("cd: %q is a directory", path)
	}
	return iso.readExtent(e)
}

// Walk visits every file and directory under the
// root, calling fn with each path (directories end
// without a trailing slash; the root itself is not
// visited).
func (iso *ISO) Walk(fn func(path string, e DirEntry) error) error {
	return iso.walkDir("", iso.root, fn)
}

func (iso *ISO) walkDir(prefix string, dir DirEntry, fn func(string, DirEntry) error) error {
	entries, err := iso.listDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := e.Name
		if prefix != "" {
			p = prefix + "/" + e.Name
		}
		if err := fn(p, e); err != nil {
			return err
		}
		if e.IsDir {
			if err := iso.walkDir(p, e, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
