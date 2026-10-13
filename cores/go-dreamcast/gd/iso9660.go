package gd

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// ISOEntry is one entry of an ISO9660 directory listing. Name has any
// version suffix (";1") stripped.
type ISOEntry struct {
	Name  string
	Size  int
	IsDir bool
}

// dirRecord is one parsed ISO9660 directory record.
type dirRecord struct {
	extent uint32
	size   uint32
	flags  byte
	name   string // display name: "." and ".." for the 0x00 and 0x01 ids, otherwise version stripped
	isDir  bool
}

// isoDataTrack returns the data track or an error explaining its absence.
func (im *Image) isoDataTrack() (*Track, error) {
	dt := im.DataTrack()
	if dt == nil {
		return nil, fmt.Errorf("gd: image has no data track for ISO9660 access")
	}
	return dt, nil
}

// isoRoot parses the Primary Volume Descriptor at relative sector 16 of
// the data track and returns its root directory record. Sector numbers
// in this file are always relative to the data track start; absolute
// LBA is dataTrack.LBA + relative sector.
func (im *Image) isoRoot() (dirRecord, error) {
	dt, err := im.isoDataTrack()
	if err != nil {
		return dirRecord{}, err
	}
	sec, err := im.ReadSector(dt.LBA + 16)
	if err != nil {
		return dirRecord{}, fmt.Errorf("gd: read volume descriptor: %w", err)
	}
	if sec[0] != 1 {
		return dirRecord{}, fmt.Errorf("gd: sector 16 is not a primary volume descriptor (type %d)", sec[0])
	}
	if string(sec[1:6]) != "CD001" {
		return dirRecord{}, fmt.Errorf("gd: sector 16 has no CD001 identifier")
	}
	// The root directory record sits at offset 156 of the descriptor
	// and is 34 bytes long in practice.
	rec, _, err := parseDirRecord(sec, 156)
	if err != nil {
		return dirRecord{}, fmt.Errorf("gd: parse root directory record: %w", err)
	}
	return rec, nil
}

// parseDirRecord parses the directory record starting at off in data and
// returns the record and its total on-disc length. Both-endian (733)
// fields are read from their little-endian half, per the API contract.
func parseDirRecord(data []byte, off int) (dirRecord, int, error) {
	var rec dirRecord
	if off < 0 || off >= len(data) {
		return rec, 0, fmt.Errorf("offset %d outside buffer of %d bytes", off, len(data))
	}
	recLen := int(data[off])
	if recLen == 0 {
		return rec, 0, fmt.Errorf("zero length record")
	}
	if recLen < 34 {
		return rec, 0, fmt.Errorf("record length %d is below the 34 byte minimum", recLen)
	}
	if off+recLen > len(data) {
		return rec, 0, fmt.Errorf("record at %d with length %d overruns buffer of %d bytes", off, recLen, len(data))
	}
	rec.extent = binary.LittleEndian.Uint32(data[off+2 : off+6])
	rec.size = binary.LittleEndian.Uint32(data[off+10 : off+14])
	rec.flags = data[off+25]
	rec.isDir = rec.flags&0x02 != 0
	nameLen := int(data[off+32])
	if 33+nameLen > recLen {
		return rec, 0, fmt.Errorf("name length %d overruns record length %d", nameLen, recLen)
	}
	nameBytes := data[off+33 : off+33+nameLen]
	switch {
	case nameLen == 1 && nameBytes[0] == 0x00:
		rec.name = "."
	case nameLen == 1 && nameBytes[0] == 0x01:
		rec.name = ".."
	default:
		rec.name = stripISOVersion(string(nameBytes))
	}
	return rec, recLen, nil
}

// stripISOVersion removes a trailing ";N" version suffix, and a trailing
// dot left behind by names stored as "NAME.;1".
func stripISOVersion(name string) string {
	if idx := strings.Index(name, ";"); idx >= 0 {
		name = name[:idx]
	}
	name = strings.TrimSuffix(name, ".")
	return name
}

// readExtent reads size bytes starting at relative sector extent of the
// data track, spanning as many whole sectors as needed.
func (im *Image) readExtent(extent, size uint32) ([]byte, error) {
	dt, err := im.isoDataTrack()
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return []byte{}, nil
	}
	sectors := (int64(size) + 2047) / 2048
	out := make([]byte, 0, sectors*2048)
	for i := int64(0); i < sectors; i++ {
		sec, err := im.ReadSector(dt.LBA + int(extent) + int(i))
		if err != nil {
			return nil, err
		}
		out = append(out, sec...)
	}
	return out[:size], nil
}

// readDirRecords reads and parses every record of the directory stored
// at the given extent. Directories may span several sectors; records
// never cross a sector boundary, so a zero length byte means the rest
// of the current sector is padding.
func (im *Image) readDirRecords(extent, size uint32) ([]dirRecord, error) {
	buf, err := im.readExtent(extent, size)
	if err != nil {
		return nil, err
	}
	var records []dirRecord
	for off := 0; off < len(buf); {
		if buf[off] == 0 {
			off = (off/2048 + 1) * 2048
			continue
		}
		rec, recLen, err := parseDirRecord(buf, off)
		if err != nil {
			return nil, fmt.Errorf("gd: directory at extent %d, offset %d: %w", extent, off, err)
		}
		records = append(records, rec)
		off += recLen
	}
	return records, nil
}

// splitISOPath splits a slash separated path into cleaned components.
// Empty components are dropped, so "", "/" and "DATA/" all behave as
// expected.
func splitISOPath(p string) []string {
	p = strings.TrimSpace(p)
	var parts []string
	for _, part := range strings.Split(p, "/") {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// findEntry locates a child of the directory described by dir, matching
// case-insensitively. Dot entries are skipped.
func (im *Image) findEntry(dir dirRecord, name string) (dirRecord, error) {
	records, err := im.readDirRecords(dir.extent, dir.size)
	if err != nil {
		return dirRecord{}, err
	}
	for _, rec := range records {
		if rec.name == "." || rec.name == ".." {
			continue
		}
		if strings.EqualFold(rec.name, name) {
			return rec, nil
		}
	}
	return dirRecord{}, fmt.Errorf("gd: %q not found", name)
}

// resolveDir walks from the root through each component, which must
// name a directory, and returns the final directory record.
func (im *Image) resolveDir(parts []string) (dirRecord, error) {
	cur, err := im.isoRoot()
	if err != nil {
		return dirRecord{}, err
	}
	for _, part := range parts {
		next, err := im.findEntry(cur, part)
		if err != nil {
			return dirRecord{}, err
		}
		if !next.isDir {
			return dirRecord{}, fmt.Errorf("gd: %q is not a directory", part)
		}
		cur = next
	}
	return cur, nil
}

// ISOListDir lists the directory at path. An empty path or "/" lists the
// root directory. Names are returned with version suffixes stripped and
// the "." and ".." entries omitted.
func (im *Image) ISOListDir(path string) ([]ISOEntry, error) {
	dir, err := im.resolveDir(splitISOPath(path))
	if err != nil {
		return nil, err
	}
	records, err := im.readDirRecords(dir.extent, dir.size)
	if err != nil {
		return nil, err
	}
	entries := make([]ISOEntry, 0, len(records))
	for _, rec := range records {
		if rec.name == "." || rec.name == ".." {
			continue
		}
		entries = append(entries, ISOEntry{
			Name:  rec.name,
			Size:  int(rec.size),
			IsDir: rec.isDir,
		})
	}
	return entries, nil
}

// ISOReadFile reads the file at name, for example "1ST_READ.BIN" or
// "DATA/FOO.TXT". Matching is case-insensitive and a ";1" version suffix
// on any component is ignored.
func (im *Image) ISOReadFile(name string) ([]byte, error) {
	parts := splitISOPath(name)
	if len(parts) == 0 {
		return nil, fmt.Errorf("gd: empty file name")
	}
	for i, part := range parts {
		parts[i] = stripISOVersion(part)
	}
	dir, err := im.resolveDir(parts[:len(parts)-1])
	if err != nil {
		return nil, err
	}
	rec, err := im.findEntry(dir, parts[len(parts)-1])
	if err != nil {
		return nil, err
	}
	if rec.isDir {
		return nil, fmt.Errorf("gd: %q is a directory", name)
	}
	return im.readExtent(rec.extent, rec.size)
}
