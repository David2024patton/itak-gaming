package cd

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// dirRecord builds one synthetic ISO9660 directory
// record (little-endian fields only; the reader reads
// the little-endian half, like the real parser path).
func makeDirRecord(extent, length uint32, isDir bool, name []byte) []byte {
	recLen := 33 + len(name)
	if recLen%2 == 1 {
		recLen++
	}
	b := make([]byte, recLen)
	b[0] = byte(recLen)
	binary.LittleEndian.PutUint32(b[2:], extent)
	binary.LittleEndian.PutUint32(b[10:], length)
	if isDir {
		b[25] = 2
	}
	b[32] = byte(len(name))
	copy(b[33:], name)
	return b
}

func joinRecords(recs ...[]byte) []byte {
	var out []byte
	for _, r := range recs {
		out = append(out, r...)
	}
	return out
}

// buildSyntheticISO writes a 24-sector ISO with a
// root directory holding HELLO.TXT;1 and a SUB
// directory holding INNER.BIN;1, and returns its
// path.
func buildSyntheticISO(t *testing.T) string {
	t.Helper()
	const (
		pvdSec   = 16
		rootSec  = 20
		helloSec = 21
		subSec   = 22
		innerSec = 23
	)
	sectors := make([][]byte, 24)
	for i := range sectors {
		sectors[i] = make([]byte, 2048)
	}
	sectors[helloSec] = append([]byte("HELLO"), make([]byte, 2048-5)...)
	sectors[innerSec][0] = 0xDE
	sectors[innerSec][1] = 0xAD
	sectors[innerSec][2] = 0xBE
	sectors[innerSec][3] = 0xEF

	rootDir := joinRecords(
		makeDirRecord(rootSec, 2048, true, []byte{0}),
		makeDirRecord(rootSec, 2048, true, []byte{1}),
		makeDirRecord(helloSec, 5, false, []byte("HELLO.TXT;1")),
		makeDirRecord(subSec, 2048, true, []byte("SUB")),
	)
	copy(sectors[rootSec], rootDir)
	subDir := joinRecords(
		makeDirRecord(subSec, 2048, true, []byte{0}),
		makeDirRecord(rootSec, 2048, true, []byte{1}),
		makeDirRecord(innerSec, 4, false, []byte("INNER.BIN;1")),
	)
	copy(sectors[subSec], subDir)

	pvd := sectors[pvdSec]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	pvd[6] = 1
	copy(pvd[40:72], "PS1TEST")
	binary.LittleEndian.PutUint16(pvd[128:], 2048)
	copy(pvd[156:], makeDirRecord(rootSec, 2048, true, []byte{0}))

	var flat []byte
	for _, s := range sectors {
		flat = append(flat, s...)
	}
	p := filepath.Join(t.TempDir(), "synthetic.iso")
	if err := os.WriteFile(p, flat, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestISO9660WalkAndRead(t *testing.T) {
	im, err := Open(buildSyntheticISO(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer im.Close()
	iso, err := OpenISO(im)
	if err != nil {
		t.Fatalf("OpenISO: %v", err)
	}
	if iso.VolumeID() != "PS1TEST" {
		t.Errorf("VolumeID = %q, want PS1TEST", iso.VolumeID())
	}
	entries, err := iso.List("")
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("root entries = %d, want 2 (%v)", len(entries), entries)
	}
	hello, err := iso.ReadFile("HELLO.TXT") // no ;1 suffix in the query
	if err != nil {
		t.Fatalf("ReadFile HELLO.TXT: %v", err)
	}
	if string(hello) != "HELLO" {
		t.Errorf("HELLO.TXT = %q, want HELLO", hello)
	}
	inner, err := iso.ReadFile("SUB/INNER.BIN;1")
	if err != nil {
		t.Fatalf("ReadFile SUB/INNER.BIN;1: %v", err)
	}
	if !bytes.Equal(inner, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Errorf("INNER.BIN = % X, want DE AD BE EF", inner)
	}
	var walked []string
	err = iso.Walk(func(path string, e DirEntry) error {
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := []string{"HELLO.TXT;1", "SUB", "SUB/INNER.BIN;1"}
	if len(walked) != len(want) {
		t.Fatalf("walked = %v, want %v", walked, want)
	}
	for i := range want {
		if walked[i] != want[i] {
			t.Errorf("walked[%d] = %q, want %q", i, walked[i], want[i])
		}
	}
	if _, err := iso.ReadFile("SUB"); err == nil {
		t.Errorf("ReadFile on a directory succeeded, want error")
	}
	if _, err := iso.ReadFile("NOPE.TXT"); err == nil {
		t.Errorf("ReadFile on a missing path succeeded, want error")
	}
}

func TestISORejectsNonISO(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blank.iso")
	if err := os.WriteFile(p, make([]byte, 2048*20), 0o644); err != nil {
		t.Fatal(err)
	}
	im, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer im.Close()
	if _, err := OpenISO(im); err == nil {
		t.Errorf("OpenISO on a blank image succeeded, want error")
	}
}
