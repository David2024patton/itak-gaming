package cd

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// FileEntry is one entry of an ISO9660 directory listing. LBA is the
// extent position as stored in the directory record, relative to the
// start of the data track that carries the file system.
type FileEntry struct {
	Name  string
	LBA   int
	Size  int
	IsDir bool
}

// dataTrack returns the first non audio track state, which is the
// track the file system and the Saturn header live on.
func (im *Image) dataTrack() (trackState, error) {
	for _, st := range im.tracks {
		if st.pub.Mode != 0 {
			return st, nil
		}
	}
	return trackState{}, fmt.Errorf("cd: image has no data track")
}

// ReadLogicalSector reads the user data of the sector at position
// lba relative to the start of the first data track. It is the
// exported form of readLogicalSector, for the system boot code,
// which loads the initial program from the start of the data area.
func (im *Image) ReadLogicalSector(lba int) ([]byte, error) {
	return im.readLogicalSector(lba)
}

// readLogicalSector reads the user data of the sector at position lba
// relative to the start of the first data track.
func (im *Image) readLogicalSector(lba int) ([]byte, error) {
	st, err := im.dataTrack()
	if err != nil {
		return nil, err
	}
	return im.ReadSector(st.pub.Start + lba)
}

// readExtent reads size bytes starting at the given extent position,
// walking user sectors one at a time. Only a single extent is read,
// which is all this package supports.
func (im *Image) readExtent(lba int, size int) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("cd: negative extent size %d", size)
	}
	out := make([]byte, 0, size)
	sector := 0
	for len(out) < size {
		chunk, err := im.readLogicalSector(lba + sector)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			return nil, fmt.Errorf("cd: empty sector while reading extent at %d", lba)
		}
		out = append(out, chunk...)
		sector++
	}
	return out[:size], nil
}

// ListRoot lists the entries of the ISO9660 root directory of the
// primary volume descriptor, which lives at logical sector 16 of the
// first data track.
func (im *Image) ListRoot() ([]FileEntry, error) {
	pvd, err := im.readLogicalSector(16)
	if err != nil {
		return nil, err
	}
	if len(pvd) < 2048 {
		return nil, fmt.Errorf("cd: primary volume descriptor sector is %d bytes, want at least 2048", len(pvd))
	}
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		return nil, fmt.Errorf("cd: no ISO9660 primary volume descriptor at sector 16")
	}
	root, _, err := parseDirRecord(pvd[156:190])
	if err != nil {
		return nil, fmt.Errorf("cd: bad root directory record: %w", err)
	}
	if !root.IsDir {
		return nil, fmt.Errorf("cd: root directory record is not a directory")
	}
	data, err := im.readExtent(root.LBA, root.Size)
	if err != nil {
		return nil, err
	}
	var entries []FileEntry
	offset := 0
	for offset < len(data) {
		recLen := int(data[offset])
		if recLen == 0 {
			// Zero padding runs to the end of the current logical
			// block, which is 2048 bytes in the assembled extent data.
			next := (offset/2048 + 1) * 2048
			if next <= offset || next > len(data) {
				break
			}
			offset = next
			continue
		}
		if recLen < 34 || offset+recLen > len(data) {
			return nil, fmt.Errorf("cd: malformed directory record at offset %d", offset)
		}
		rec := data[offset : offset+recLen]
		if isDotEntry(rec) {
			offset += recLen
			continue
		}
		entry, _, err := parseDirRecord(rec)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
		offset += recLen
	}
	return entries, nil
}

// ReadFile returns the whole contents of a file entry. It errors on
// directory entries. Only the single extent named by the entry is
// read.
func (im *Image) ReadFile(e FileEntry) ([]byte, error) {
	if e.IsDir {
		return nil, fmt.Errorf("cd: %s is a directory", e.Name)
	}
	return im.readExtent(e.LBA, e.Size)
}

// isDotEntry reports whether a directory record is the current or
// parent directory entry, whose file identifiers are the single bytes
// 0x00 and 0x01.
func isDotEntry(rec []byte) bool {
	if len(rec) < 34 || rec[32] != 1 {
		return false
	}
	return rec[33] == 0x00 || rec[33] == 0x01
}

// parseDirRecord decodes one ISO9660 directory record. Both endian
// fields are read from their little endian half. The returned name has
// any version suffix removed, so a stored name like HELLO.TXT;1 is
// presented as HELLO.TXT.
func parseDirRecord(rec []byte) (FileEntry, int, error) {
	if len(rec) < 34 {
		return FileEntry{}, 0, fmt.Errorf("directory record is %d bytes, want at least 34", len(rec))
	}
	nameLen := int(rec[32])
	if 33+nameLen > len(rec) {
		return FileEntry{}, 0, fmt.Errorf("directory record name overruns the record")
	}
	name := string(rec[33 : 33+nameLen])
	if idx := strings.Index(name, ";"); idx >= 0 {
		name = name[:idx]
	}
	isDir := rec[25]&0x02 != 0
	if isDir && strings.HasSuffix(name, ".") && len(name) > 1 {
		name = name[:len(name)-1]
	}
	entry := FileEntry{
		Name:  name,
		LBA:   int(binary.LittleEndian.Uint32(rec[2:6])),
		Size:  int(binary.LittleEndian.Uint32(rec[10:14])),
		IsDir: isDir,
	}
	return entry, int(rec[0]), nil
}
