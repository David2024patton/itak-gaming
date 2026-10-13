// Package umd reads PSP UMD disc images in ISO9660 format and parses the
// PARAM.SFO metadata file that every PSP game ships in /PSP_GAME.
package umd

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
)

// sectorSize is the fixed ISO9660 logical sector size used by UMD images.
const sectorSize = 2048

// pvdSector is the sector number of the Primary Volume Descriptor.
const pvdSector = 16

// Entry is a single file or directory inside an ISO image.
type Entry struct {
	Name      string // file or directory name, version suffix stripped, e.g. "PARAM.SFO"
	Size      uint32
	Directory bool
}

// dirRecord is the parsed form of one ISO9660 directory record.
type dirRecord struct {
	extent uint32
	size   uint32
	flags  byte
	name   string // raw name as stored, or "." / ".." for the special entries
}

func (r dirRecord) isDir() bool {
	return r.flags&0x02 != 0
}

// ISO is an opened ISO9660 image. The underlying file is kept open and all
// reads go through ReadAt, so the whole image is never loaded into memory.
type ISO struct {
	file     *os.File
	volumeID string
	root     dirRecord
}

// Open opens the image at path and validates its Primary Volume Descriptor.
func Open(path string) (*ISO, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("umd: open %q: %w", path, err)
	}
	iso := &ISO{file: f}
	if err := iso.readPVD(); err != nil {
		f.Close()
		return nil, err
	}
	return iso, nil
}

// Close closes the underlying image file.
func (iso *ISO) Close() error {
	if iso == nil || iso.file == nil {
		return nil
	}
	err := iso.file.Close()
	iso.file = nil
	return err
}

// VolumeID returns the trimmed volume identifier from the PVD.
func (iso *ISO) VolumeID() string {
	return iso.volumeID
}

// readPVD reads and validates sector 16 and extracts the volume identifier
// and the root directory record.
func (iso *ISO) readPVD() error {
	pvd := make([]byte, sectorSize)
	if _, err := iso.file.ReadAt(pvd, pvdSector*sectorSize); err != nil {
		return fmt.Errorf("umd: read primary volume descriptor: %w", err)
	}
	if pvd[0] != 1 {
		return fmt.Errorf("umd: not an ISO9660 image: volume descriptor type %d, want 1", pvd[0])
	}
	if string(pvd[1:6]) != "CD001" {
		return fmt.Errorf("umd: not an ISO9660 image: bad descriptor magic %q", string(pvd[1:6]))
	}
	// The volume identifier is a 32 byte space padded field at offset 40.
	iso.volumeID = strings.Trim(string(pvd[40:72]), " \t\r\n\x00")

	// The root directory record is a 34 byte record at offset 156.
	root, _, err := parseDirRecord(pvd[156:])
	if err != nil {
		return fmt.Errorf("umd: parse root directory record: %w", err)
	}
	iso.root = root
	return nil
}

// parseDirRecord parses one directory record from the start of b. It returns
// the record and the record length in bytes. The caller guarantees b holds at
// least the start of a record; a zero first byte is handled by callers as a
// sector padding marker, not passed here.
func parseDirRecord(b []byte) (dirRecord, int, error) {
	var rec dirRecord
	if len(b) < 34 {
		return rec, 0, fmt.Errorf("directory record truncated: %d bytes available", len(b))
	}
	recLen := int(b[0])
	if recLen < 33 {
		return rec, 0, fmt.Errorf("directory record length %d is too small", recLen)
	}
	if recLen > len(b) {
		return rec, 0, fmt.Errorf("directory record length %d exceeds %d available bytes", recLen, len(b))
	}
	// Extent and data length are 733 fields: a little endian value followed
	// by a big endian copy. The little endian half is authoritative here.
	rec.extent = binary.LittleEndian.Uint32(b[2:6])
	rec.size = binary.LittleEndian.Uint32(b[10:14])
	rec.flags = b[25]
	nameLen := int(b[32])
	if 33+nameLen > recLen {
		return rec, 0, fmt.Errorf("directory record name length %d exceeds record length %d", nameLen, recLen)
	}
	nameBytes := b[33 : 33+nameLen]
	switch {
	case nameLen == 1 && nameBytes[0] == 0x00:
		rec.name = "."
	case nameLen == 1 && nameBytes[0] == 0x01:
		rec.name = ".."
	default:
		rec.name = string(nameBytes)
	}
	return rec, recLen, nil
}

// stripVersion removes a trailing version suffix such as ";1" from a file
// name. A suffix counts only when every byte after the semicolon is a digit.
func stripVersion(name string) string {
	i := strings.LastIndexByte(name, ';')
	if i < 0 || i == len(name)-1 {
		return name
	}
	for j := i + 1; j < len(name); j++ {
		if name[j] < '0' || name[j] > '9' {
			return name
		}
	}
	return name[:i]
}

// readDir returns every record stored in the directory described by rec,
// including the "." and ".." entries.
func (iso *ISO) readDir(rec dirRecord) ([]dirRecord, error) {
	if !rec.isDir() {
		return nil, fmt.Errorf("umd: extent %d is not a directory", rec.extent)
	}
	data := make([]byte, rec.size)
	if rec.size > 0 {
		if _, err := iso.file.ReadAt(data, int64(rec.extent)*sectorSize); err != nil {
			return nil, fmt.Errorf("umd: read directory at extent %d: %w", rec.extent, err)
		}
	}
	var out []dirRecord
	offset := 0
	for offset < len(data) {
		// A record never spans a sector. A zero length byte means the rest
		// of the current sector is padding, so jump to the next boundary.
		if data[offset] == 0 {
			offset = (offset/sectorSize + 1) * sectorSize
			continue
		}
		sectorEnd := (offset/sectorSize + 1) * sectorSize
		if sectorEnd > len(data) {
			sectorEnd = len(data)
		}
		entry, recLen, err := parseDirRecord(data[offset:sectorEnd])
		if err != nil {
			return nil, fmt.Errorf("umd: parse directory at extent %d offset %d: %w", rec.extent, offset, err)
		}
		out = append(out, entry)
		offset += recLen
	}
	return out, nil
}

// splitPath splits an absolute image path into its components. Empty
// components and "." components are dropped.
func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." {
			continue
		}
		parts = append(parts, p)
	}
	return parts
}

// lookup finds the child of dir whose name matches component, comparing
// case-insensitively with version suffixes stripped.
func (iso *ISO) lookup(dir dirRecord, component string) (dirRecord, error) {
	entries, err := iso.readDir(dir)
	if err != nil {
		return dirRecord{}, err
	}
	want := stripVersion(component)
	for _, e := range entries {
		if e.name == "." || e.name == ".." {
			continue
		}
		if strings.EqualFold(stripVersion(e.name), want) {
			return e, nil
		}
	}
	return dirRecord{}, fmt.Errorf("umd: %q not found", component)
}

// resolve walks path components from the root and returns the final record.
func (iso *ISO) resolve(path string) (dirRecord, error) {
	cur := iso.root
	for _, part := range splitPath(path) {
		if !cur.isDir() {
			return dirRecord{}, fmt.Errorf("umd: %q: %q is not a directory", path, part)
		}
		next, err := iso.lookup(cur, part)
		if err != nil {
			return dirRecord{}, fmt.Errorf("umd: resolve %q: %w", path, err)
		}
		cur = next
	}
	return cur, nil
}

// List returns the entries of the directory at dirPath, for example "/" or
// "/PSP_GAME". The listing is not recursive and excludes "." and "..".
func (iso *ISO) List(dirPath string) ([]Entry, error) {
	rec, err := iso.resolve(dirPath)
	if err != nil {
		return nil, err
	}
	if !rec.isDir() {
		return nil, fmt.Errorf("umd: list %q: not a directory", dirPath)
	}
	records, err := iso.readDir(rec)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, r := range records {
		if r.name == "." || r.name == ".." {
			continue
		}
		out = append(out, Entry{
			Name:      stripVersion(r.name),
			Size:      r.size,
			Directory: r.isDir(),
		})
	}
	return out, nil
}

// ReadFile returns the full contents of the file at path, for example
// "/PSP_GAME/PARAM.SFO". Multi-extent files are not supported: the file is
// read from its single starting extent for its recorded data length, which
// covers the small single-extent metadata files this package targets.
func (iso *ISO) ReadFile(path string) ([]byte, error) {
	rec, err := iso.resolve(path)
	if err != nil {
		return nil, err
	}
	if rec.isDir() {
		return nil, fmt.Errorf("umd: read %q: is a directory", path)
	}
	data := make([]byte, rec.size)
	if rec.size > 0 {
		if _, err := iso.file.ReadAt(data, int64(rec.extent)*sectorSize); err != nil {
			return nil, fmt.Errorf("umd: read file %q at extent %d: %w", path, rec.extent, err)
		}
	}
	return data, nil
}

// ParamSFO reads and parses /PSP_GAME/PARAM.SFO from the image.
func (iso *ISO) ParamSFO() (*SFO, error) {
	data, err := iso.ReadFile("/PSP_GAME/PARAM.SFO")
	if err != nil {
		return nil, err
	}
	return ParseSFO(data)
}
