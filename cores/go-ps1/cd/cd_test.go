package cd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMSFConversion(t *testing.T) {
	if got := MSFToSector(1, 2, 3); got != 4653 {
		t.Errorf("MSFToSector(1,2,3) = %d, want 4653", got)
	}
	m, s, f := SectorToMSF(4653)
	if m != 1 || s != 2 || f != 3 {
		t.Errorf("SectorToMSF(4653) = %d:%02d:%02d, want 1:02:03", m, s, f)
	}
	m, s, f = LBAToMSF(0)
	if m != 0 || s != 2 || f != 0 {
		t.Errorf("LBAToMSF(0) = %d:%02d:%02d, want 0:02:00 (150 frame lead-in)", m, s, f)
	}
}

// writeCueFixture builds a two-track bin/cue in dir: a
// 4-sector Mode 2 data track whose user bytes are
// filled with 0x10+sector, and a 3-sector audio track
// filled with 0xA0+sector, with a 2-sector generated
// pregap in front of the audio.
func writeCueFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var bin []byte
	for s := 0; s < 4; s++ {
		sec := make([]byte, 2352)
		sec[0] = byte(s) // raw marker
		for i := 24; i < 24+2048; i++ {
			sec[i] = byte(0x10 + s)
		}
		bin = append(bin, sec...)
	}
	for s := 0; s < 3; s++ {
		sec := make([]byte, 2352)
		for i := range sec {
			sec[i] = byte(0xA0 + s)
		}
		bin = append(bin, sec...)
	}
	if err := os.WriteFile(filepath.Join(dir, "game.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	cue := "FILE \"game.bin\" BINARY\n" +
		"  TRACK 01 MODE2/2352\n" +
		"    INDEX 01 00:00:00\n" +
		"  TRACK 02 AUDIO\n" +
		"    PREGAP 00:00:02\n" +
		"    INDEX 01 00:00:04\n"
	cuePath := filepath.Join(dir, "game.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	return cuePath
}

func TestCueLayoutAndSectors(t *testing.T) {
	im, err := Open(writeCueFixture(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer im.Close()
	tracks := im.Tracks()
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	d, a := tracks[0], tracks[1]
	if d.Mode != TrackData || d.StartLBA != 0 || d.DataLBA != 0 || d.Length != 4 {
		t.Errorf("data track = %+v, want data at 0, length 4", d)
	}
	if a.Mode != TrackAudio || a.StartLBA != 4 || a.DataLBA != 6 || a.Pregap != 2 || a.Length != 5 {
		t.Errorf("audio track = start %d data %d pregap %d length %d, want 4/6/2/5",
			a.StartLBA, a.DataLBA, a.Pregap, a.Length)
	}

	user, err := im.ReadSector(0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(user) != 2048 || !bytes.Equal(user, bytes.Repeat([]byte{0x10}, 2048)) {
		t.Errorf("sector 0 user data: len %d, first bytes %v, want 2048 of 0x10", len(user), user[:4])
	}
	raw, err := im.ReadSector(2, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2352 || raw[0] != 2 || raw[24] != 0x12 {
		t.Errorf("sector 2 raw: len %d marker 0x%02X user start 0x%02X, want 2352/0x02/0x12",
			len(raw), raw[0], raw[24])
	}
	// Generated pregap reads as silence.
	for _, lba := range []int{4, 5} {
		sec, err := im.ReadSector(lba, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(sec, make([]byte, 2352)) {
			t.Errorf("pregap LBA %d not silent", lba)
		}
	}
	audio, err := im.ReadSector(6, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(audio, bytes.Repeat([]byte{0xA0}, 2352)) {
		t.Errorf("audio sector 6: want 2352 bytes of 0xA0")
	}
	last, err := im.ReadSector(8, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(last, bytes.Repeat([]byte{0xA2}, 2352)) {
		t.Errorf("audio sector 8: want 2352 bytes of 0xA2")
	}
	if _, err := im.ReadSector(9, false); err == nil {
		t.Errorf("LBA 9 read succeeded, want out-of-range error")
	}
}

func TestFlatISOOpen(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 2048*3)
	data[2048] = 0x77
	p := filepath.Join(dir, "flat.iso")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	im, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer im.Close()
	tracks := im.Tracks()
	if len(tracks) != 1 || tracks[0].Length != 3 || tracks[0].Mode != TrackData {
		t.Fatalf("tracks = %+v, want one 3-sector data track", tracks)
	}
	sec, err := im.ReadSector(1, false)
	if err != nil {
		t.Fatal(err)
	}
	if sec[0] != 0x77 {
		t.Errorf("sector 1 first byte = 0x%02X, want 0x77", sec[0])
	}
}

func TestCueErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.cue")
	if err := os.WriteFile(p, []byte("FILE \"missing.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Errorf("cue with missing bin opened, want error")
	}
	if _, err := Open(filepath.Join(dir, "nope.xyz")); err == nil {
		t.Errorf("unknown extension opened, want error")
	}
}
