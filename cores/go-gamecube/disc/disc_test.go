package disc

import (
	"encoding/binary"
	"testing"
)

func put32(b []byte, off uint32, v uint32) {
	binary.BigEndian.PutUint32(b[off:], v)
}

// syntheticGC builds a complete, fully synthetic GameCube
// disc image in memory: header, bi2, apploader, a DOL with
// one text and one data section, and an FST with a
// directory and two files. No bytes come from any real
// disc.
func syntheticGC() []byte {
	data := make([]byte, 0x10000)
	data[0] = 'G'
	copy(data[1:], "GT")
	copy(data[3:], "01")
	data[6] = 1 // version
	copy(data[0x20:], "Synthetic Test Disc")
	put32(data, 0x1C, GCMagic)
	put32(data, 0x420, 0x5000) // DOL offset
	put32(data, 0x424, 0x6000) // FST offset
	put32(data, 0x428, 75)     // FST size

	// bi2: simulated memory size 24 MB at +4.
	put32(data, 0x444, 24<<20)
	// Apploader header.
	copy(data[0x2440:], "2001/01/01")
	put32(data, 0x2450, 0x81200000)
	put32(data, 0x2454, 0x100)

	// DOL header at 0x5000.
	dol := uint32(0x5000)
	put32(data, dol+0x00, 0x100)      // text0 offset
	put32(data, dol+0x48, 0x80003000) // text0 address
	put32(data, dol+0x90, 0x10)       // text0 size
	put32(data, dol+0x1C, 0x200)      // data0 offset
	put32(data, dol+0x64, 0x80004000) // data0 address
	put32(data, dol+0xAC, 8)          // data0 size
	put32(data, dol+0xD8, 0x80005000) // BSS address
	put32(data, dol+0xDC, 0x100)      // BSS size
	put32(data, dol+0xE0, 0x80003000) // entry
	for i := 0; i < 0x10; i++ {
		data[0x5100+i] = byte(0xA0 + i)
	}
	copy(data[0x5200:], "DOL-DATA")

	// FST at 0x6000: root, dir "data", two files.
	fst := uint32(0x6000)
	entry := func(i uint32, typ byte, nameOff, off, size uint32) {
		o := fst + i*12
		data[o] = typ
		data[o+1] = byte(nameOff >> 16)
		data[o+2] = byte(nameOff >> 8)
		data[o+3] = byte(nameOff)
		put32(data, o+4, off)
		put32(data, o+8, size)
	}
	entry(0, 1, 0, 0, 4)       // root, 4 entries total
	entry(1, 1, 1, 0, 3)       // dir "data", subtree ends at 3
	entry(2, 0, 6, 0x7000, 5)  // data/hello.txt
	entry(3, 0, 16, 0x7010, 7) // readme.txt
	strs := "\x00data\x00hello.txt\x00readme.txt\x00"
	copy(data[fst+48:], strs)
	copy(data[0x7000:], "HELLO")
	copy(data[0x7010:], "README!")
	return data
}

func TestParseHeader(t *testing.T) {
	img, err := Parse(syntheticGC())
	if err != nil {
		t.Fatal(err)
	}
	if img.IsWii {
		t.Fatal("synthetic GC image reported as Wii")
	}
	if img.SystemID != 'G' || img.GameCode != "GT" || img.CompanyCode != "01" {
		t.Fatalf("ids: %c %q %q", img.SystemID, img.GameCode, img.CompanyCode)
	}
	if img.Name != "Synthetic Test Disc" {
		t.Fatalf("name = %q", img.Name)
	}
	if img.Version != 1 {
		t.Fatalf("version = %d", img.Version)
	}
	if img.BI2.SimulatedMemorySize != 24<<20 {
		t.Fatalf("bi2 mem size = %#x", img.BI2.SimulatedMemorySize)
	}
	if img.Apploader.Date != "2001/01/01" || img.Apploader.Entry != 0x81200000 ||
		img.Apploader.Size != 0x100 {
		t.Fatalf("apploader = %+v", img.Apploader)
	}
}

func TestParseDOL(t *testing.T) {
	img, err := Parse(syntheticGC())
	if err != nil {
		t.Fatal(err)
	}
	d, err := img.DOL()
	if err != nil {
		t.Fatal(err)
	}
	if d.Entry != 0x80003000 {
		t.Fatalf("entry = %#x", d.Entry)
	}
	if d.BSSAddress != 0x80005000 || d.BSSSize != 0x100 {
		t.Fatalf("bss = %#x/%#x", d.BSSAddress, d.BSSSize)
	}
	if len(d.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(d.Sections))
	}
	text := d.Sections[0]
	if !text.Text || text.Address != 0x80003000 || text.Size != 0x10 ||
		text.Data[0] != 0xA0 || text.Data[15] != 0xAF {
		t.Fatalf("text section = %+v", text)
	}
	dat := d.Sections[1]
	if dat.Text || string(dat.Data) != "DOL-DATA" {
		t.Fatalf("data section = %+v", dat)
	}
}

func TestFSTWalkAndExtract(t *testing.T) {
	img, err := Parse(syntheticGC())
	if err != nil {
		t.Fatal(err)
	}
	files, err := img.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	if files[0].Path != "data/hello.txt" || files[1].Path != "readme.txt" {
		t.Fatalf("paths = %q, %q", files[0].Path, files[1].Path)
	}
	b, err := img.Extract("data/hello.txt")
	if err != nil || string(b) != "HELLO" {
		t.Fatalf("hello.txt = %q, %v", b, err)
	}
	b, err = img.Extract("/readme.txt")
	if err != nil || string(b) != "README!" {
		t.Fatalf("readme.txt = %q, %v", b, err)
	}
	if _, err := img.Extract("nope.bin"); err != ErrNoFile {
		t.Fatalf("missing file err = %v", err)
	}
}

func TestWiiMetadataOnly(t *testing.T) {
	data := make([]byte, 0x50000)
	data[0] = 'R'
	copy(data[0x20:], "Wii Synthetic")
	put32(data, 0x18, WiiMagic)
	// Partition table: group 0 has one partition, table at
	// 0x48000, partition data offset 0x50000, type 0.
	put32(data, 0x40000, 1)
	put32(data, 0x40004, 0x48000>>2)
	put32(data, 0x48000, 0x50000>>2)
	put32(data, 0x48004, 0)

	img, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !img.IsWii {
		t.Fatal("Wii image not detected")
	}
	if len(img.Partitions) != 1 {
		t.Fatalf("partitions = %v", img.Partitions)
	}
	p := img.Partitions[0]
	if p.Offset != 0x50000 || p.Type != 0 || p.Group != 0 {
		t.Fatalf("partition = %+v", p)
	}
	if _, err := img.DOL(); err != ErrWiiDisc {
		t.Fatalf("DOL on Wii: err = %v", err)
	}
	if _, err := img.Files(); err != ErrWiiDisc {
		t.Fatalf("Files on Wii: err = %v", err)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse(make([]byte, 100)); err != ErrTruncated {
		t.Fatalf("short image err = %v", err)
	}
	if _, err := Parse(make([]byte, 0x10000)); err != ErrNotGameCube {
		t.Fatalf("zero image err = %v", err)
	}
}
