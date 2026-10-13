package gd

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Synthetic ISO layout used by the tests. Sector numbers are relative
// to the data track start.
const (
	testTotalSectors  = 25
	testRootExtent    = 20
	testRootSize      = 4096 // the root directory spans two sectors on purpose
	testDataExtent    = 22
	testHelloExtent   = 23
	testWorldExtent   = 24
	testRootBExtent   = 21 // second sector of the root directory
	testDataDirSector = testDataExtent
)

var (
	testHelloContent = []byte("Hello, Dreamcast!\nThis is a synthetic test file.\n")
	testWorldContent = func() []byte {
		b := make([]byte, 512)
		for i := range b {
			b[i] = byte(i*7 + 3)
		}
		return b
	}()
)

// buildTestDirRecord builds one ISO9660 directory record. name is the
// raw file identifier: a single 0x00 byte for ".", 0x01 for "..", or
// bytes like "HELLO.TXT;1".
func buildTestDirRecord(extent, size uint32, isDir bool, name []byte) []byte {
	recLen := 33 + len(name)
	if len(name)%2 == 0 {
		recLen++
	}
	rec := make([]byte, recLen)
	rec[0] = byte(recLen)
	binary.LittleEndian.PutUint32(rec[2:6], extent)
	binary.BigEndian.PutUint32(rec[6:10], extent)
	binary.LittleEndian.PutUint32(rec[10:14], size)
	binary.BigEndian.PutUint32(rec[14:18], size)
	// Recording date: 2025-06-15 12:00:00 GMT.
	copy(rec[18:25], []byte{125, 6, 15, 12, 0, 0, 0})
	if isDir {
		rec[25] = 0x02
	}
	binary.LittleEndian.PutUint16(rec[28:30], 1)
	binary.BigEndian.PutUint16(rec[30:32], 1)
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}

// buildTestPVD builds the primary volume descriptor sector.
func buildTestPVD() []byte {
	sec := make([]byte, 2048)
	sec[0] = 1
	copy(sec[1:6], "CD001")
	sec[6] = 1
	for i := 8; i < 72; i++ {
		sec[i] = ' '
	}
	copy(sec[8:], "SEGA")
	copy(sec[40:], "TESTDISC")
	binary.LittleEndian.PutUint32(sec[80:84], testTotalSectors)
	binary.BigEndian.PutUint32(sec[84:88], testTotalSectors)
	binary.LittleEndian.PutUint16(sec[104:106], 1)
	binary.BigEndian.PutUint16(sec[106:108], 1)
	binary.LittleEndian.PutUint16(sec[108:110], 1)
	binary.BigEndian.PutUint16(sec[110:112], 1)
	binary.LittleEndian.PutUint16(sec[112:114], 2048)
	binary.BigEndian.PutUint16(sec[114:116], 2048)
	root := buildTestDirRecord(testRootExtent, testRootSize, true, []byte{0x00})
	copy(sec[156:], root)
	return sec
}

// buildTestISO builds a complete synthetic ISO image in memory.
// Boot area sectors 0 to 15 are zero; callers may overwrite them.
func buildTestISO() []byte {
	img := make([]byte, testTotalSectors*2048)
	set := func(sector int, data []byte) {
		copy(img[sector*2048:(sector+1)*2048], data)
	}
	set(16, buildTestPVD())

	// Root directory, first sector: dot entries and HELLO.TXT, then
	// zero padding to the end of the sector.
	rootA := make([]byte, 0, 2048)
	rootA = append(rootA, buildTestDirRecord(testRootExtent, testRootSize, true, []byte{0x00})...)
	rootA = append(rootA, buildTestDirRecord(testRootExtent, testRootSize, true, []byte{0x01})...)
	rootA = append(rootA, buildTestDirRecord(testHelloExtent, uint32(len(testHelloContent)), false, []byte("HELLO.TXT;1"))...)
	set(testRootExtent, rootA)

	// Root directory, second sector: the DATA subdirectory entry. This
	// is what makes the fixture exercise multi-sector directories and
	// the zero padding skip.
	rootB := make([]byte, 0, 2048)
	rootB = append(rootB, buildTestDirRecord(testDataExtent, 2048, true, []byte("DATA"))...)
	set(testRootBExtent, rootB)

	// DATA directory.
	dataDir := make([]byte, 0, 2048)
	dataDir = append(dataDir, buildTestDirRecord(testDataExtent, 2048, true, []byte{0x00})...)
	dataDir = append(dataDir, buildTestDirRecord(testRootExtent, testRootSize, true, []byte{0x01})...)
	dataDir = append(dataDir, buildTestDirRecord(testWorldExtent, uint32(len(testWorldContent)), false, []byte("WORLD.BIN;1"))...)
	set(testDataDirSector, dataDir)

	set(testHelloExtent, testHelloContent)
	set(testWorldExtent, testWorldContent)
	return img
}

// wrapRaw2352 wraps 2048 byte sectors into raw 2352 byte Mode 1
// sectors: 12 byte sync, 4 byte header, user data, and 288 bytes of
// error correction filler filling the sector out to 2352 bytes.
func wrapRaw2352(iso []byte) []byte {
	sectors := len(iso) / 2048
	out := make([]byte, 0, sectors*2352)
	sync := []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}
	for i := 0; i < sectors; i++ {
		out = append(out, sync...)
		total := i + 150
		out = append(out, byte(total/4500), byte((total/75)%60), byte(total%75), 0x01)
		out = append(out, iso[i*2048:(i+1)*2048]...)
		out = append(out, make([]byte, 288)...)
	}
	return out
}

// wrap2336 wraps 2048 byte sectors into 2336 byte sectors: an 8 byte
// header, user data, and 280 bytes of filler.
func wrap2336(iso []byte) []byte {
	sectors := len(iso) / 2048
	out := make([]byte, 0, sectors*2336)
	for i := 0; i < sectors; i++ {
		hdr := make([]byte, 8)
		hdr[0] = 0x08
		binary.LittleEndian.PutUint32(hdr[4:], uint32(i))
		out = append(out, hdr...)
		out = append(out, iso[i*2048:(i+1)*2048]...)
		out = append(out, make([]byte, 280)...)
	}
	return out
}

// writeTestFile writes data into a fresh temp dir and returns the path.
func writeTestFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// padField returns s padded with spaces to exactly width bytes, for
// crafting fixed width IP.BIN fields.
func padField(s string, width int) string {
	for len(s) < width {
		s += " "
	}
	return s[:width]
}

// buildTestBootArea crafts a 0x8000 byte boot area with known IP.BIN
// field values at the documented offsets.
func buildTestBootArea() []byte {
	boot := make([]byte, 0x8000)
	put := func(off int, s string, width int) {
		copy(boot[off:off+width], padField(s, width))
	}
	put(0x00, "SEGA SEGAKATANA", 16)
	put(0x10, "TEST MAKER", 16)
	put(0x20, "GD-ROM1/1", 16)
	put(0x30, "JUE", 8)
	put(0x38, "E000F10", 16)
	put(0x48, "T-12345", 10)
	put(0x52, "V1.000", 6)
	put(0x58, "20000101", 8)
	put(0x60, "1ST_READ.BIN", 16)
	put(0x70, "TEST COMPANY", 16)
	put(0x80, "MY TEST GAME", 128)
	return boot
}
