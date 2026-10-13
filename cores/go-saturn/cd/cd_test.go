package cd

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertions counts every named check in this file so the suite can
// prove its own coverage at the end.
var assertions int

func check(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	assertions++
	if !cond {
		t.Fatalf(format, args...)
	}
}

func checkBytes(t *testing.T, got []byte, want []byte, what string) {
	t.Helper()
	assertions++
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch: got %d bytes starting %v, want %d bytes starting %v", what, len(got), first8(got), len(want), first8(want))
	}
}

func first8(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

var helloContents = []byte("Hello, Saturn world, from a synthetic disc.\n")

func dataBinContents() []byte {
	out := make([]byte, 5000)
	for i := range out {
		out[i] = byte(i*7 + 3)
	}
	return out
}

// putText writes s into a fixed width field at off, padding the rest of
// the field with spaces.
func putText(sec []byte, off int, width int, s string) {
	for i := 0; i < width; i++ {
		sec[off+i] = ' '
	}
	copy(sec[off:off+width], s)
}

// putBothEndian32 writes a 32 bit value as little endian followed by
// big endian, the ISO9660 both endian form.
func putBothEndian32(rec []byte, off int, v uint32) {
	binary.LittleEndian.PutUint32(rec[off:off+4], v)
	binary.BigEndian.PutUint32(rec[off+4:off+8], v)
}

// dirRecord builds one ISO9660 directory record.
func dirRecord(extent int, size int, flags byte, name []byte) []byte {
	length := 33 + len(name)
	if length%2 == 1 {
		length++
	}
	rec := make([]byte, length)
	rec[0] = byte(length)
	putBothEndian32(rec, 2, uint32(extent))
	putBothEndian32(rec, 10, uint32(size))
	rec[18] = 0x7c
	rec[19] = 1
	rec[20] = 1
	rec[25] = flags
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	return rec
}

// buildISO constructs a synthetic 2048 byte sector ISO9660 image with
// a Saturn IP header in sector zero, a primary volume descriptor at
// sector 16, a root directory at sector 20, HELLO.TXT at sector 21 and
// DATA.BIN spanning sectors 22 to 24.
func buildISO() []byte {
	const total = 25
	img := make([]byte, total*2048)

	// Sector 0: Saturn IP header.
	sec0 := img[0:2048]
	copy(sec0[0:16], "SEGA SEGASATURN ")
	putText(sec0, 0x10, 16, "SEGA ENTERPRISES")
	putText(sec0, 0x20, 10, "MK-12345")
	putText(sec0, 0x2a, 6, "V1.000")
	putText(sec0, 0x30, 8, "19960101")
	putText(sec0, 0x38, 8, "CD-1/1")
	putText(sec0, 0x40, 10, "JTUE")
	putText(sec0, 0x50, 16, "JOYPAD")
	putText(sec0, 0x60, 112, "SYNTHETIC TEST")
	binary.BigEndian.PutUint32(sec0[0xf0:0xf4], 0x06001000)
	binary.BigEndian.PutUint32(sec0[0xf8:0xfc], 0x06000800)

	// Sector 16: primary volume descriptor.
	pvd := img[16*2048 : 17*2048]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	pvd[6] = 1
	copy(pvd[156:190], dirRecord(20, 2048, 2, []byte{0}))

	// Sector 17: volume descriptor terminator.
	term := img[17*2048 : 18*2048]
	term[0] = 255
	copy(term[1:6], "CD001")
	term[6] = 1

	// Sector 20: root directory records.
	root := img[20*2048 : 21*2048]
	off := 0
	for _, rec := range [][]byte{
		dirRecord(20, 2048, 2, []byte{0}),
		dirRecord(20, 2048, 2, []byte{1}),
		dirRecord(21, len(helloContents), 0, []byte("HELLO.TXT;1")),
		dirRecord(22, len(dataBinContents()), 0, []byte("DATA.BIN;1")),
	} {
		copy(root[off:], rec)
		off += len(rec)
	}

	// Sectors 21 to 24: file contents.
	copy(img[21*2048:], helloContents)
	copy(img[22*2048:], dataBinContents())
	return img
}

// wrapMode1Raw wraps every 2048 byte sector of iso in a raw 2352 byte
// Mode 1 sector with sync, a BCD MSF header and a zero tail.
func wrapMode1Raw(iso []byte) []byte {
	n := len(iso) / 2048
	out := make([]byte, n*2352)
	for i := 0; i < n; i++ {
		sec := out[i*2352 : (i+1)*2352]
		sec[0] = 0x00
		for j := 1; j <= 10; j++ {
			sec[j] = 0xff
		}
		sec[11] = 0x00
		m, s, f := LBAtoMSF(i)
		sec[12] = toBCD(m)
		sec[13] = toBCD(s)
		sec[14] = toBCD(f)
		sec[15] = 1
		copy(sec[16:2064], iso[i*2048:(i+1)*2048])
	}
	return out
}

// writeFile is a small test helper that writes data and fails on error.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestMSFConversions(t *testing.T) {
	check(t, MSFtoLBA(0, 2, 0) == 0, "MSFtoLBA(0,2,0) = %d, want 0", MSFtoLBA(0, 2, 0))
	check(t, MSFtoLBA(1, 0, 0) == 4350, "MSFtoLBA(1,0,0) = %d, want 4350", MSFtoLBA(1, 0, 0))
	check(t, MSFtoLBA(0, 0, 0) == 0-150, "MSFtoLBA(0,0,0) = %d, want %d", MSFtoLBA(0, 0, 0), 0-150)
	m, s, f := LBAtoMSF(0)
	check(t, m == 0 && s == 2 && f == 0, "LBAtoMSF(0) = %d:%d:%d, want 0:2:0", m, s, f)
	m, s, f = LBAtoMSF(4350)
	check(t, m == 1 && s == 0 && f == 0, "LBAtoMSF(4350) = %d:%d:%d, want 1:0:0", m, s, f)
	m, s, f = LBAtoMSF(MSFtoLBA(12, 34, 56))
	check(t, m == 12 && s == 34 && f == 56, "round trip = %d:%d:%d, want 12:34:56", m, s, f)
}

func TestOpenBytesAndSaturnHeader(t *testing.T) {
	iso := buildISO()
	im, err := OpenBytes(iso, 2048)
	check(t, err == nil, "OpenBytes error: %v", err)
	check(t, im.NumSectors() == 25, "NumSectors = %d, want 25", im.NumSectors())
	tr := im.Tracks()
	check(t, len(tr) == 1, "tracks = %d, want 1", len(tr))
	check(t, tr[0].Mode == 1 && tr[0].SectorSize == 2048, "track mode %d size %d, want 1 and 2048", tr[0].Mode, tr[0].SectorSize)
	check(t, tr[0].Start == 0 && tr[0].Length == 25, "track start %d length %d, want 0 and 25", tr[0].Start, tr[0].Length)

	sec0, err := im.ReadSector(0)
	check(t, err == nil, "ReadSector(0) error: %v", err)
	checkBytes(t, sec0[0:16], []byte("SEGA SEGASATURN "), "sector 0 signature")
	pvd, err := im.ReadSector(16)
	check(t, err == nil, "ReadSector(16) error: %v", err)
	check(t, pvd[0] == 1 && string(pvd[1:6]) == "CD001", "PVD marker wrong: type %d id %q", pvd[0], string(pvd[1:6]))

	h, err := im.SaturnHeader()
	check(t, err == nil, "SaturnHeader error: %v", err)
	check(t, h.Signature == "SEGA SEGASATURN ", "Signature = %q", h.Signature)
	check(t, h.MakerID == "SEGA ENTERPRISES", "MakerID = %q", h.MakerID)
	check(t, h.ProductNumber == "MK-12345", "ProductNumber = %q", h.ProductNumber)
	check(t, h.Version == "V1.000", "Version = %q", h.Version)
	check(t, h.ReleaseDate == "19960101", "ReleaseDate = %q", h.ReleaseDate)
	check(t, h.DeviceInfo == "CD-1/1", "DeviceInfo = %q", h.DeviceInfo)
	check(t, h.AreaSymbols == "JTUE", "AreaSymbols = %q", h.AreaSymbols)
	check(t, h.Peripherals == "JOYPAD", "Peripherals = %q", h.Peripherals)
	check(t, h.Title == "SYNTHETIC TEST", "Title = %q", h.Title)
	check(t, h.MasterStack == 0x06001000, "MasterStack = %#x, want 0x06001000", h.MasterStack)
	check(t, h.SlaveStack == 0x06000800, "SlaveStack = %#x, want 0x06000800", h.SlaveStack)
}

func TestISOListRootAndReadFile(t *testing.T) {
	im, err := OpenBytes(buildISO(), 2048)
	check(t, err == nil, "OpenBytes error: %v", err)
	entries, err := im.ListRoot()
	check(t, err == nil, "ListRoot error: %v", err)
	check(t, len(entries) == 2, "root entries = %d, want 2", len(entries))
	check(t, entries[0].Name == "HELLO.TXT", "entry 0 name = %q", entries[0].Name)
	check(t, entries[1].Name == "DATA.BIN", "entry 1 name = %q", entries[1].Name)
	check(t, entries[0].LBA == 21 && entries[0].Size == len(helloContents), "HELLO.TXT lba %d size %d", entries[0].LBA, entries[0].Size)
	check(t, entries[1].LBA == 22 && entries[1].Size == 5000, "DATA.BIN lba %d size %d", entries[1].LBA, entries[1].Size)
	check(t, !entries[0].IsDir && !entries[1].IsDir, "file entries marked as directories")
	hello, err := im.ReadFile(entries[0])
	check(t, err == nil, "ReadFile HELLO.TXT error: %v", err)
	checkBytes(t, hello, helloContents, "HELLO.TXT contents")
	data, err := im.ReadFile(entries[1])
	check(t, err == nil, "ReadFile DATA.BIN error: %v", err)
	check(t, len(data) == 5000, "DATA.BIN read length = %d, want 5000", len(data))
	checkBytes(t, data, dataBinContents(), "DATA.BIN contents spanning 3 sectors")
	_, err = im.ReadFile(FileEntry{Name: "ROOT", LBA: 20, Size: 2048, IsDir: true})
	check(t, err != nil, "ReadFile on a directory should fail")
}

func TestOpenISOAndSynthesizedRaw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disc.iso")
	iso := buildISO()
	writeFile(t, path, iso)
	im, err := OpenISO(path)
	check(t, err == nil, "OpenISO error: %v", err)
	check(t, im.NumSectors() == 25, "NumSectors = %d, want 25", im.NumSectors())
	sec, err := im.ReadSector(21)
	check(t, err == nil, "ReadSector(21) error: %v", err)
	checkBytes(t, sec[:len(helloContents)], helloContents, "ISO hello sector")
	raw, err := im.ReadRawSector(0)
	check(t, err == nil, "ReadRawSector(0) error: %v", err)
	check(t, len(raw) == 2352, "raw length = %d, want 2352", len(raw))
	check(t, raw[0] == 0x00 && raw[1] == 0xff && raw[10] == 0xff && raw[11] == 0x00, "sync pattern wrong: %v", raw[0:12])
	check(t, raw[12] == 0x00 && raw[13] == 0x02 && raw[14] == 0x00, "header MSF = %v, want BCD 00 02 00", raw[12:15])
	check(t, raw[15] == 1, "header mode byte = %d, want 1", raw[15])
	checkBytes(t, raw[16:2064], iso[0:2048], "synthesized raw user data")
	check(t, raw[2064] == 0 && raw[2351] == 0, "EDC and ECC tail should be zero")
	raw21, err := im.ReadRawSector(21)
	check(t, err == nil, "ReadRawSector(21) error: %v", err)
	check(t, fromBCD(raw21[13]) == 2 && fromBCD(raw21[14]) == 21, "sector 21 header = %v, want minute 0 second 2 frame 21 in BCD", raw21[12:15])
}

func TestCueRawMode1(t *testing.T) {
	dir := t.TempDir()
	iso := buildISO()
	bin := wrapMode1Raw(iso)
	writeFile(t, filepath.Join(dir, "disc.bin"), bin)
	cue := "FILE \"disc.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"
	writeFile(t, filepath.Join(dir, "disc.cue"), []byte(cue))
	im, err := OpenCue(filepath.Join(dir, "disc.cue"))
	check(t, err == nil, "OpenCue error: %v", err)
	tr := im.Tracks()
	check(t, len(tr) == 1 && tr[0].SectorSize == 2352 && tr[0].Mode == 1, "track = %+v", tr)
	check(t, im.NumSectors() == 25, "NumSectors = %d, want 25", im.NumSectors())
	sec, err := im.ReadSector(16)
	check(t, err == nil, "ReadSector(16) error: %v", err)
	checkBytes(t, sec, iso[16*2048:17*2048], "raw cue PVD user sector")
	raw, err := im.ReadRawSector(3)
	check(t, err == nil, "ReadRawSector(3) error: %v", err)
	checkBytes(t, raw, bin[3*2352:4*2352], "raw cue stored sector 3")
	entries, err := im.ListRoot()
	check(t, err == nil, "ListRoot error: %v", err)
	check(t, len(entries) == 2, "root entries = %d, want 2", len(entries))
	content, err := im.ReadFile(entries[1])
	check(t, err == nil, "ReadFile error: %v", err)
	checkBytes(t, content, dataBinContents(), "raw cue DATA.BIN contents")
	h, err := im.SaturnHeader()
	check(t, err == nil, "SaturnHeader error: %v", err)
	check(t, h.Title == "SYNTHETIC TEST", "Title = %q", h.Title)
}

func TestCueIndex00Pregap(t *testing.T) {
	dir := t.TempDir()
	const sectors = 65
	img := make([]byte, sectors*2048)
	for i := 0; i < sectors; i++ {
		sec := img[i*2048 : (i+1)*2048]
		for j := range sec {
			sec[j] = 0xee
		}
	}
	for i := 0; i < 10; i++ {
		sec := img[i*2048 : (i+1)*2048]
		for j := range sec {
			sec[j] = byte(i + 1)
		}
	}
	for i := 60; i < sectors; i++ {
		sec := img[i*2048 : (i+1)*2048]
		for j := range sec {
			sec[j] = byte(0xa0 + i - 60)
		}
	}
	writeFile(t, filepath.Join(dir, "two.bin"), img)
	cue := strings.Join([]string{
		"FILE \"two.bin\" BINARY",
		"  TRACK 01 MODE1/2048",
		"    INDEX 01 00:00:00",
		"  TRACK 02 MODE1/2048",
		"    INDEX 00 00:00:10",
		"    INDEX 01 00:00:60",
		"",
	}, "\n")
	writeFile(t, filepath.Join(dir, "two.cue"), []byte(cue))
	im, err := OpenCue(filepath.Join(dir, "two.cue"))
	check(t, err == nil, "OpenCue error: %v", err)
	tr := im.Tracks()
	check(t, len(tr) == 2, "tracks = %d, want 2", len(tr))
	check(t, tr[0].Start == 0 && tr[0].Length == 10, "track 1 start %d length %d, want 0 and 10", tr[0].Start, tr[0].Length)
	check(t, tr[1].Pregap == 50, "track 2 pregap = %d, want 50", tr[1].Pregap)
	check(t, tr[1].Start == 60, "track 2 start = %d, want 60", tr[1].Start)
	check(t, tr[1].Length == 5, "track 2 length = %d, want 5", tr[1].Length)
	check(t, tr[1].FileOffset == 60*2048, "track 2 file offset = %d, want %d", tr[1].FileOffset, 60*2048)
	check(t, im.NumSectors() == 65, "NumSectors = %d, want 65", im.NumSectors())
	last, err := im.ReadSector(9)
	check(t, err == nil, "ReadSector(9) error: %v", err)
	check(t, last[0] == 10 && last[2047] == 10, "track 1 last sector fill = %d", last[0])
	pre, err := im.ReadSector(10)
	check(t, err == nil, "ReadSector(10) error: %v", err)
	check(t, len(pre) == 2048 && pre[0] == 0 && pre[2047] == 0, "pregap sector should be zeros, first byte %d", pre[0])
	preRaw, err := im.ReadRawSector(59)
	check(t, err == nil, "ReadRawSector(59) error: %v", err)
	check(t, len(preRaw) == 2048 && preRaw[0] == 0 && preRaw[2047] == 0, "pregap raw sector should be zeros")
	t2, err := im.ReadSector(60)
	check(t, err == nil, "ReadSector(60) error: %v", err)
	check(t, t2[0] == 0xa0 && t2[2047] == 0xa0, "track 2 first sector fill = %#x, want 0xa0", t2[0])
}

func TestCuePregapCommand(t *testing.T) {
	dir := t.TempDir()
	img := make([]byte, 7*2048)
	for i := 0; i < 7; i++ {
		sec := img[i*2048 : (i+1)*2048]
		for j := range sec {
			sec[j] = byte(0x30 + i)
		}
	}
	writeFile(t, filepath.Join(dir, "pg.bin"), img)
	cue := strings.Join([]string{
		"FILE \"pg.bin\" BINARY",
		"  TRACK 01 MODE1/2048",
		"    INDEX 01 00:00:00",
		"  TRACK 02 MODE1/2048",
		"    PREGAP 00:00:25",
		"    INDEX 01 00:00:04",
		"",
	}, "\n")
	writeFile(t, filepath.Join(dir, "pg.cue"), []byte(cue))
	im, err := OpenCue(filepath.Join(dir, "pg.cue"))
	check(t, err == nil, "OpenCue error: %v", err)
	tr := im.Tracks()
	check(t, tr[1].Pregap == 25, "pregap = %d, want 25", tr[1].Pregap)
	check(t, tr[1].Start == 29, "track 2 start = %d, want 29", tr[1].Start)
	check(t, tr[0].Length == 4, "track 1 length = %d, want 4", tr[0].Length)
	check(t, tr[1].Length == 3 && tr[1].FileOffset == 4*2048, "track 2 length %d offset %d", tr[1].Length, tr[1].FileOffset)
	check(t, im.NumSectors() == 32, "NumSectors = %d, want 32 (7 file sectors plus 25 pregap)", im.NumSectors())
	pre, err := im.ReadSector(28)
	check(t, err == nil, "ReadSector(28) error: %v", err)
	check(t, pre[0] == 0 && pre[2047] == 0, "command pregap sector should be zeros")
	t2, err := im.ReadSector(29)
	check(t, err == nil, "ReadSector(29) error: %v", err)
	check(t, t2[0] == 0x34, "track 2 first sector fill = %#x, want 0x34", t2[0])
}

func TestMode2FormsViaOpenBytes(t *testing.T) {
	makeSector := func(form2 bool, user []byte) []byte {
		sec := make([]byte, 2352)
		for j := 1; j <= 10; j++ {
			sec[j] = 0xff
		}
		sec[15] = 2
		submode := byte(0x08)
		if form2 {
			submode = 0x28
		}
		sec[16] = 0
		sec[17] = 0
		sec[18] = submode
		sec[19] = 0
		copy(sec[20:24], sec[16:20])
		copy(sec[24:], user)
		return sec
	}
	user1 := make([]byte, 2048)
	for i := range user1 {
		user1[i] = byte(i)
	}
	user2 := make([]byte, 2324)
	for i := range user2 {
		user2[i] = byte(255 - i%256)
	}
	img := append(makeSector(false, user1), makeSector(true, user2)...)
	im, err := OpenBytes(img, 2352)
	check(t, err == nil, "OpenBytes error: %v", err)
	check(t, im.Tracks()[0].Mode == 2, "track mode = %d, want 2", im.Tracks()[0].Mode)
	got1, err := im.ReadSector(0)
	check(t, err == nil, "ReadSector(0) error: %v", err)
	check(t, len(got1) == 2048, "form 1 user length = %d, want 2048", len(got1))
	checkBytes(t, got1, user1, "Mode 2 Form 1 user data")
	got2, err := im.ReadSector(1)
	check(t, err == nil, "ReadSector(1) error: %v", err)
	check(t, len(got2) == 2324, "form 2 user length = %d, want 2324", len(got2))
	checkBytes(t, got2, user2, "Mode 2 Form 2 user data")
}

func TestCueAudioTrack(t *testing.T) {
	dir := t.TempDir()
	img := make([]byte, 4*2352)
	for i := 0; i < 2; i++ {
		sec := img[i*2352 : (i+1)*2352]
		sec[15] = 1
		for j := 16; j < 2064; j++ {
			sec[j] = 0x44
		}
	}
	for i := 2; i < 4; i++ {
		sec := img[i*2352 : (i+1)*2352]
		for j := range sec {
			sec[j] = 0x5a
		}
	}
	writeFile(t, filepath.Join(dir, "aud.bin"), img)
	cue := strings.Join([]string{
		"FILE \"aud.bin\" BINARY",
		"  TRACK 01 MODE1/2352",
		"    INDEX 01 00:00:00",
		"  TRACK 02 AUDIO",
		"    INDEX 01 00:00:02",
		"",
	}, "\n")
	writeFile(t, filepath.Join(dir, "aud.cue"), []byte(cue))
	im, err := OpenCue(filepath.Join(dir, "aud.cue"))
	check(t, err == nil, "OpenCue error: %v", err)
	tr := im.Tracks()
	check(t, len(tr) == 2 && tr[1].Mode == 0, "tracks = %+v", tr)
	check(t, im.NumSectors() == 4, "NumSectors = %d, want 4", im.NumSectors())
	_, err = im.ReadSector(2)
	check(t, err != nil, "ReadSector on an audio sector should fail")
	raw, err := im.ReadRawSector(3)
	check(t, err == nil, "ReadRawSector(3) error: %v", err)
	check(t, len(raw) == 2352 && raw[0] == 0x5a && raw[2351] == 0x5a, "audio raw sector wrong: len %d first %#x", len(raw), raw[0])
	data, err := im.ReadSector(1)
	check(t, err == nil, "ReadSector(1) error: %v", err)
	check(t, len(data) == 2048 && data[0] == 0x44, "data track user sector wrong")
}

func TestErrorCases(t *testing.T) {
	iso := buildISO()
	bad := append([]byte(nil), iso...)
	bad[0] = 'X'
	im, err := OpenBytes(bad, 2048)
	check(t, err == nil, "OpenBytes error: %v", err)
	_, err = im.SaturnHeader()
	check(t, err != nil, "SaturnHeader should reject a bad signature")

	good, err := OpenBytes(iso, 2048)
	check(t, err == nil, "OpenBytes error: %v", err)
	_, err = good.ReadSector(0 - 1)
	check(t, err != nil, "ReadSector below range should fail")
	_, err = good.ReadSector(good.NumSectors())
	check(t, err != nil, "ReadSector at NumSectors should fail")
	_, err = good.ReadRawSector(9999)
	check(t, err != nil, "ReadRawSector far out of range should fail")
	_, err = OpenBytes(iso, 1234)
	check(t, err != nil, "OpenBytes with a bad sector size should fail")
	_, err = OpenBytes(iso[:1000], 2048)
	check(t, err != nil, "OpenBytes with a partial sector should fail")

	dir := t.TempDir()
	multi := strings.Join([]string{
		"FILE \"a.bin\" BINARY",
		"  TRACK 01 MODE1/2048",
		"    INDEX 01 00:00:00",
		"FILE \"b.bin\" BINARY",
		"  TRACK 02 MODE1/2048",
		"    INDEX 01 00:00:01",
		"",
	}, "\n")
	writeFile(t, filepath.Join(dir, "multi.cue"), []byte(multi))
	_, err = OpenCue(filepath.Join(dir, "multi.cue"))
	check(t, err != nil, "a multi FILE cue should be rejected")

	mixed := "FILE \"a.bin\" BINARY\n  TRACK 01 MODE1/2048\n    INDEX 01 00:00:00\n  TRACK 02 MODE2/2352\n    INDEX 01 00:00:01\n"
	writeFile(t, filepath.Join(dir, "mixed.cue"), []byte(mixed))
	writeFile(t, filepath.Join(dir, "a.bin"), make([]byte, 4096))
	_, err = OpenCue(filepath.Join(dir, "mixed.cue"))
	check(t, err != nil, "mixed raw sector sizes in one file should be rejected")

	_, err = OpenISO(filepath.Join(dir, "missing.iso"))
	check(t, err != nil, "OpenISO on a missing file should fail")
}

func TestZZAssertionCount(t *testing.T) {
	t.Logf("assertions executed before this test: %d", assertions)
	check(t, assertions >= 20, "assertion count = %d, want at least 20", assertions)
}
