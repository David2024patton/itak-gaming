package umd

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// put723 writes a both-endian 16 bit value: little endian then big endian.
func put723(b []byte, off int, v uint16) {
	binary.LittleEndian.PutUint16(b[off:], v)
	binary.BigEndian.PutUint16(b[off+2:], v)
}

// put733 writes a both-endian 32 bit value: little endian then big endian.
func put733(b []byte, off int, v uint32) {
	binary.LittleEndian.PutUint32(b[off:], v)
	binary.BigEndian.PutUint32(b[off+4:], v)
}

// makeDirRecord builds one ISO9660 directory record for name. A name of a
// single 0x00 byte is ".", a single 0x01 byte is "..".
func makeDirRecord(name []byte, extent, size uint32, isDir bool) []byte {
	recLen := 33 + len(name)
	if recLen%2 != 0 {
		recLen++ // records are padded to an even length
	}
	rec := make([]byte, recLen)
	rec[0] = byte(recLen)
	rec[1] = 0 // extended attribute record length
	put733(rec, 2, extent)
	put733(rec, 10, size)
	// Bytes 18..24 (recording date) are left zero; the reader ignores them.
	if isDir {
		rec[25] = 0x02
	}
	put723(rec, 28, 1) // volume sequence number
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}

// writeRecord copies a directory record into img at a sector offset and
// returns the offset just past it.
func writeRecord(img []byte, sector, offset int, rec []byte) int {
	copy(img[sector*sectorSize+offset:], rec)
	return offset + len(rec)
}

func align4(n int) int {
	return (n + 3) &^ 3
}

// sfoEntrySpec describes one value for the synthetic SFO builder.
type sfoEntrySpec struct {
	key    string
	format uint16
	str    string
	num    int32
}

// buildSFO builds a synthetic PARAM.SFO. Specs must be sorted by key, as the
// real format requires. String values use the null terminated format and
// are padded to 4 byte alignment inside their maximum lengths; integer
// values use the int32 format.
func buildSFO() []byte {
	specs := []sfoEntrySpec{
		{key: "APP_VER", format: sfoFormatString, str: "01.00"},
		{key: "CATEGORY", format: sfoFormatString, str: "UG"},
		{key: "DISC_ID", format: sfoFormatString, str: "TEST00001"},
		{key: "PSP_SYSTEM_VER", format: sfoFormatInt32, num: 0x00050010},
		{key: "TITLE", format: sfoFormatString, str: "Synthetic Test Game"},
	}

	const headerSize = 20
	keyTableOffset := headerSize + len(specs)*16

	// Lay out the key table: null terminated keys in key order.
	var keyTable []byte
	keyOffsets := make([]int, len(specs))
	for i, spec := range specs {
		keyOffsets[i] = len(keyTable)
		keyTable = append(keyTable, spec.key...)
		keyTable = append(keyTable, 0)
	}
	dataTableOffset := align4(keyTableOffset + len(keyTable))

	// Lay out the data table in key order with 4 byte alignment.
	var dataTable []byte
	dataOffsets := make([]int, len(specs))
	lengths := make([]int, len(specs))
	maxLengths := make([]int, len(specs))
	for i, spec := range specs {
		for len(dataTable)%4 != 0 {
			dataTable = append(dataTable, 0)
		}
		dataOffsets[i] = len(dataTable)
		switch spec.format {
		case sfoFormatString:
			raw := append([]byte(spec.str), 0)
			lengths[i] = len(raw)
			maxLengths[i] = align4(len(raw))
			dataTable = append(dataTable, raw...)
			for len(dataTable) < dataOffsets[i]+maxLengths[i] {
				dataTable = append(dataTable, 0)
			}
		case sfoFormatInt32:
			lengths[i] = 4
			maxLengths[i] = 4
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(spec.num))
			dataTable = append(dataTable, buf[:]...)
		}
	}

	out := make([]byte, dataTableOffset+len(dataTable))
	// Header: magic, version 1.1, table offsets, entry count.
	out[1] = 'P'
	out[2] = 'S'
	out[3] = 'F'
	binary.LittleEndian.PutUint32(out[4:], 0x00000101)
	binary.LittleEndian.PutUint32(out[8:], uint32(keyTableOffset))
	binary.LittleEndian.PutUint32(out[12:], uint32(dataTableOffset))
	binary.LittleEndian.PutUint32(out[16:], uint32(len(specs)))
	// Index entries.
	for i, spec := range specs {
		off := headerSize + i*16
		binary.LittleEndian.PutUint16(out[off:], uint16(keyOffsets[i]))
		binary.LittleEndian.PutUint16(out[off+2:], spec.format)
		binary.LittleEndian.PutUint32(out[off+4:], uint32(lengths[i]))
		binary.LittleEndian.PutUint32(out[off+8:], uint32(maxLengths[i]))
		binary.LittleEndian.PutUint32(out[off+12:], uint32(dataOffsets[i]))
	}
	copy(out[keyTableOffset:], keyTable)
	copy(out[dataTableOffset:], dataTable)
	return out
}

var testReadme = []byte("Hello from a synthetic PSP disc.\n")

// buildTestISO writes a synthetic 24 sector ISO image holding a README file
// and /PSP_GAME/PARAM.SFO, and returns its path plus the SFO bytes used.
func buildTestISO(t *testing.T) (string, []byte) {
	t.Helper()
	sfo := buildSFO()

	const (
		rootSector     = 20
		gameSector     = 21
		sfoSector      = 22
		readmeSector   = 23
		totalSectors   = 24
		volumeIDSector = 16
	)
	img := make([]byte, totalSectors*sectorSize)

	// Primary Volume Descriptor at sector 16.
	pvd := volumeIDSector * sectorSize
	img[pvd] = 1
	copy(img[pvd+1:], "CD001")
	img[pvd+6] = 1
	for i := 0; i < 32; i++ {
		img[pvd+40+i] = ' '
	}
	copy(img[pvd+40:], "PSP_TEST_DISC")
	rootRec := makeDirRecord([]byte{0x00}, rootSector, sectorSize, true)
	copy(img[pvd+156:], rootRec)

	// Root directory at sector 20.
	off := 0
	off = writeRecord(img, rootSector, off, makeDirRecord([]byte{0x00}, rootSector, sectorSize, true))
	off = writeRecord(img, rootSector, off, makeDirRecord([]byte{0x01}, rootSector, sectorSize, true))
	off = writeRecord(img, rootSector, off, makeDirRecord([]byte("README.TXT;1"), readmeSector, uint32(len(testReadme)), false))
	writeRecord(img, rootSector, off, makeDirRecord([]byte("PSP_GAME"), gameSector, sectorSize, true))

	// PSP_GAME directory at sector 21.
	off = 0
	off = writeRecord(img, gameSector, off, makeDirRecord([]byte{0x00}, gameSector, sectorSize, true))
	off = writeRecord(img, gameSector, off, makeDirRecord([]byte{0x01}, rootSector, sectorSize, true))
	writeRecord(img, gameSector, off, makeDirRecord([]byte("PARAM.SFO;1"), sfoSector, uint32(len(sfo)), false))

	// File data.
	copy(img[sfoSector*sectorSize:], sfo)
	copy(img[readmeSector*sectorSize:], testReadme)

	path := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatalf("write test ISO: %v", err)
	}
	return path, sfo
}

func findEntry(entries []Entry, name string) (Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

func TestISOFromSyntheticImage(t *testing.T) {
	path, sfoBytes := buildTestISO(t)

	iso, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer iso.Close()

	if got := iso.VolumeID(); got != "PSP_TEST_DISC" {
		t.Errorf("VolumeID = %q, want %q", got, "PSP_TEST_DISC")
	}

	root, err := iso.List("/")
	if err != nil {
		t.Fatalf("List(/): %v", err)
	}
	if len(root) != 2 {
		t.Fatalf("List(/) returned %d entries, want 2: %+v", len(root), root)
	}
	game, ok := findEntry(root, "PSP_GAME")
	if !ok {
		t.Fatalf("List(/) missing PSP_GAME: %+v", root)
	}
	if !game.Directory {
		t.Errorf("PSP_GAME Directory = false, want true")
	}
	readme, ok := findEntry(root, "README.TXT")
	if !ok {
		t.Fatalf("List(/) missing README.TXT: %+v", root)
	}
	if readme.Directory {
		t.Errorf("README.TXT Directory = true, want false")
	}
	if readme.Size != uint32(len(testReadme)) {
		t.Errorf("README.TXT Size = %d, want %d", readme.Size, len(testReadme))
	}

	gameEntries, err := iso.List("/PSP_GAME")
	if err != nil {
		t.Fatalf("List(/PSP_GAME): %v", err)
	}
	if len(gameEntries) != 1 {
		t.Fatalf("List(/PSP_GAME) returned %d entries, want 1: %+v", len(gameEntries), gameEntries)
	}
	param := gameEntries[0]
	if param.Name != "PARAM.SFO" {
		t.Errorf("PSP_GAME entry Name = %q, want %q", param.Name, "PARAM.SFO")
	}
	if param.Directory {
		t.Errorf("PARAM.SFO Directory = true, want false")
	}
	if param.Size != uint32(len(sfoBytes)) {
		t.Errorf("PARAM.SFO Size = %d, want %d", param.Size, len(sfoBytes))
	}

	gotSFO, err := iso.ReadFile("/PSP_GAME/PARAM.SFO")
	if err != nil {
		t.Fatalf("ReadFile(/PSP_GAME/PARAM.SFO): %v", err)
	}
	if !bytes.Equal(gotSFO, sfoBytes) {
		t.Errorf("ReadFile(/PSP_GAME/PARAM.SFO) returned %d bytes that differ from the %d synthetic bytes", len(gotSFO), len(sfoBytes))
	}

	// Path lookup is case-insensitive.
	gotReadme, err := iso.ReadFile("/readme.txt")
	if err != nil {
		t.Fatalf("ReadFile(/readme.txt): %v", err)
	}
	if !bytes.Equal(gotReadme, testReadme) {
		t.Errorf("ReadFile(/readme.txt) = %q, want %q", gotReadme, testReadme)
	}

	sfo, err := iso.ParamSFO()
	if err != nil {
		t.Fatalf("ParamSFO: %v", err)
	}
	checkSFO(t, sfo)

	if _, err := iso.ReadFile("/PSP_GAME/MISSING.BIN"); err == nil {
		t.Errorf("ReadFile on a missing path: got nil error, want an error")
	}
	if _, err := iso.ReadFile("/NOPE/PARAM.SFO"); err == nil {
		t.Errorf("ReadFile in a missing directory: got nil error, want an error")
	}
	if _, err := iso.ReadFile("/PSP_GAME"); err == nil {
		t.Errorf("ReadFile on a directory: got nil error, want an error")
	}
	if _, err := iso.List("/README.TXT"); err == nil {
		t.Errorf("List on a file: got nil error, want an error")
	}
}

func checkSFO(t *testing.T, sfo *SFO) {
	t.Helper()
	strChecks := map[string]string{
		"TITLE":    "Synthetic Test Game",
		"CATEGORY": "UG",
		"APP_VER":  "01.00",
		"DISC_ID":  "TEST00001",
	}
	for key, want := range strChecks {
		got, ok := sfo.String(key)
		if !ok {
			t.Errorf("String(%q): not found", key)
			continue
		}
		if got != want {
			t.Errorf("String(%q) = %q, want %q", key, got, want)
		}
	}
	ver, ok := sfo.Int("PSP_SYSTEM_VER")
	if !ok {
		t.Fatalf("Int(PSP_SYSTEM_VER): not found")
	}
	if ver != 0x00050010 {
		t.Errorf("Int(PSP_SYSTEM_VER) = %#08x, want 0x00050010", ver)
	}
	if _, ok := sfo.String("PSP_SYSTEM_VER"); ok {
		t.Errorf("String(PSP_SYSTEM_VER): got ok for an integer value, want false")
	}
	if _, ok := sfo.Int("TITLE"); ok {
		t.Errorf("Int(TITLE): got ok for a string value, want false")
	}
	if _, ok := sfo.String("NO_SUCH_KEY"); ok {
		t.Errorf("String(NO_SUCH_KEY): got ok for a missing key, want false")
	}
}

func TestParseSFOStandalone(t *testing.T) {
	sfo, err := ParseSFO(buildSFO())
	if err != nil {
		t.Fatalf("ParseSFO: %v", err)
	}
	if len(sfo.Values) != 5 {
		t.Errorf("ParseSFO parsed %d values, want 5", len(sfo.Values))
	}
	checkSFO(t, sfo)
}

func TestOpenRejectsNonISO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-an-iso.bin")
	junk := bytes.Repeat([]byte("this is not an iso "), 2048)
	if err := os.WriteFile(path, junk, 0o644); err != nil {
		t.Fatalf("write junk file: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Errorf("Open on a non-ISO file: got nil error, want an error")
	}

	empty := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	if _, err := Open(empty); err == nil {
		t.Errorf("Open on an empty file: got nil error, want an error")
	}
}

func TestParseSFORejectsBadMagic(t *testing.T) {
	data := buildSFO()
	data[1] = 'X'
	if _, err := ParseSFO(data); err == nil {
		t.Errorf("ParseSFO with bad magic: got nil error, want an error")
	}
}

func TestParseSFOTruncated(t *testing.T) {
	full := buildSFO()
	for _, n := range []int{0, 3, 10, 19, 20, 40, 99, len(full) / 2, len(full) - 1} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ParseSFO truncated to %d bytes panicked: %v", n, r)
				}
			}()
			if _, err := ParseSFO(full[:n]); err == nil {
				t.Errorf("ParseSFO truncated to %d bytes: got nil error, want an error", n)
			}
		}()
	}
}
