package scd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestFile writes data to dir/name and returns the full path.
func writeTestFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// fillSector returns one raw sector of the given size where the user
// data window [userStart:userStart+2048] is filled with value and
// every other byte is junk, so a read that lands on the wrong window
// cannot accidentally pass.
func fillSector(size, userStart int, value byte) []byte {
	sector := make([]byte, size)
	for i := range sector {
		sector[i] = 0xEE
	}
	// Twelve sync style bytes and a short header pattern sit in front
	// of the user data on raw sector layouts.
	for i := 0; i < userStart && i < size; i++ {
		sector[i] = byte(i)
	}
	for i := userStart; i < userStart+2048 && i < size; i++ {
		sector[i] = value
	}
	return sector
}

// buildRawBin assembles a bin image from per sector fill values using
// fillSector for each sector.
func buildRawBin(sectorSize, userStart int, values []byte) []byte {
	var bin []byte
	for _, value := range values {
		bin = append(bin, fillSector(sectorSize, userStart, value)...)
	}
	return bin
}

// wantUserData builds the expected 2048 byte block filled with value.
func wantUserData(value byte) []byte {
	out := make([]byte, 2048)
	for i := range out {
		out[i] = value
	}
	return out
}

// openAndClose opens a disc and registers its Close with Cleanup.
func openAndClose(t *testing.T, path string) *Disc {
	t.Helper()
	disc, err := OpenDisc(path)
	if err != nil {
		t.Fatalf("OpenDisc(%s): %v", path, err)
	}
	t.Cleanup(func() {
		if err := disc.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return disc
}

func TestMSFLBARoundTrips(t *testing.T) {
	if got := MSFToLBA(0, 2, 0); got != 0 {
		t.Errorf("MSFToLBA(0, 2, 0) = %d, want 0", got)
	}
	m, s, f := LBAToMSF(0)
	if m != 0 || s != 2 || f != 0 {
		t.Errorf("LBAToMSF(0) = %02d:%02d:%02d, want 00:02:00", m, s, f)
	}

	// A mid disc position: 12 minutes, 34 seconds, 56 frames.
	const wantLBA = ((12*60)+34)*75 + 56 - 150
	if got := MSFToLBA(12, 34, 56); got != wantLBA {
		t.Errorf("MSFToLBA(12, 34, 56) = %d, want %d", got, wantLBA)
	}
	m, s, f = LBAToMSF(wantLBA)
	if m != 12 || s != 34 || f != 56 {
		t.Errorf("LBAToMSF(%d) = %02d:%02d:%02d, want 12:34:56", wantLBA, m, s, f)
	}

	for _, lba := range []int{0, 1, 74, 75, 449, 450, 12345, 269999} {
		m, s, f := LBAToMSF(lba)
		if got := MSFToLBA(m, s, f); got != lba {
			t.Errorf("round trip LBA %d gave %d via %02d:%02d:%02d", lba, got, m, s, f)
		}
	}
}

func TestOpenISO(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 8*2048)
	for sector := 0; sector < 8; sector++ {
		for i := 0; i < 2048; i++ {
			data[sector*2048+i] = byte(sector)
		}
	}
	copy(data, "SEGA CD TEST MARKER")
	path := writeTestFile(t, dir, "game.iso", data)

	disc := openAndClose(t, path)
	if got := disc.NumSectors(); got != 8 {
		t.Errorf("NumSectors = %d, want 8", got)
	}
	tracks := disc.Tracks()
	if len(tracks) != 1 {
		t.Fatalf("len(Tracks) = %d, want 1", len(tracks))
	}
	track := tracks[0]
	if track.Mode != TrackMode1_2048 || track.SectorSize != 2048 || track.Sectors != 8 || track.StartLBA != 0 {
		t.Errorf("track = %+v, want MODE1/2048 with 8 sectors of 2048 bytes at LBA 0", track)
	}
	if got := disc.DataTracks(); got != 1 {
		t.Errorf("DataTracks = %d, want 1", got)
	}

	sector0, err := disc.ReadData(0)
	if err != nil {
		t.Fatalf("ReadData(0): %v", err)
	}
	if !bytes.HasPrefix(sector0, []byte("SEGA CD TEST MARKER")) {
		t.Errorf("ReadData(0) lost the sector 0 marker, starts with %q", sector0[:19])
	}

	sector3, err := disc.ReadData(3)
	if err != nil {
		t.Fatalf("ReadData(3): %v", err)
	}
	if len(sector3) != 2048 {
		t.Fatalf("len(ReadData(3)) = %d, want 2048", len(sector3))
	}
	if !bytes.Equal(sector3, wantUserData(3)) {
		t.Errorf("ReadData(3) is not 2048 bytes of value 3")
	}

	if _, err := disc.ReadData(8); err == nil {
		t.Error("ReadData(8) succeeded, want an out of range error")
	}
	if _, err := disc.ReadData(-1); err == nil {
		t.Error("ReadData(-1) succeeded, want an out of range error")
	}
}

func TestOpenCueMode1_2352(t *testing.T) {
	dir := t.TempDir()
	values := []byte{0xA0, 0xA1, 0xA2, 0xA3}
	bin := buildRawBin(2352, 16, values)
	writeTestFile(t, dir, "game.bin", bin)
	cue := "FILE \"game.bin\" BINARY\n" +
		"  TRACK 1 MODE1/2352\n" +
		"    INDEX 01 00:02:00\n"
	cuePath := writeTestFile(t, dir, "game.cue", []byte(cue))

	disc := openAndClose(t, cuePath)
	if got := disc.NumSectors(); got != 4 {
		t.Errorf("NumSectors = %d, want 4", got)
	}
	tracks := disc.Tracks()
	if len(tracks) != 1 || tracks[0].Mode != TrackMode1_2352 || tracks[0].SectorSize != 2352 {
		t.Fatalf("tracks = %+v, want one MODE1/2352 track", tracks)
	}
	for i, value := range values {
		got, err := disc.ReadData(i)
		if err != nil {
			t.Fatalf("ReadData(%d): %v", i, err)
		}
		if !bytes.Equal(got, wantUserData(value)) {
			t.Errorf("ReadData(%d) does not match the user data window [16:2064]", i)
		}
	}
}

func TestOpenCueMode2_2352(t *testing.T) {
	dir := t.TempDir()
	values := []byte{0xB0, 0xB1}
	bin := buildRawBin(2352, 24, values)
	writeTestFile(t, dir, "game.bin", bin)
	// Bare, unquoted file name form.
	cue := "FILE game.bin BINARY\n" +
		"  TRACK 1 MODE2/2352\n" +
		"    INDEX 01 00:02:00\n"
	cuePath := writeTestFile(t, dir, "game.cue", []byte(cue))

	disc := openAndClose(t, cuePath)
	tracks := disc.Tracks()
	if len(tracks) != 1 || tracks[0].Mode != TrackMode2_2352 {
		t.Fatalf("tracks = %+v, want one MODE2/2352 track", tracks)
	}
	for i, value := range values {
		got, err := disc.ReadData(i)
		if err != nil {
			t.Fatalf("ReadData(%d): %v", i, err)
		}
		if !bytes.Equal(got, wantUserData(value)) {
			t.Errorf("ReadData(%d) does not match the user data window [24:2072]", i)
		}
	}
}

func TestOpenCueTwoDataTracks(t *testing.T) {
	dir := t.TempDir()
	// Six sectors in one file: track 1 owns sectors 0 to 3 and
	// track 2 owns sectors 4 to 5. LBA 4 is MSF 00:02:04.
	values := []byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15}
	bin := buildRawBin(2352, 16, values)
	writeTestFile(t, dir, "game.bin", bin)
	cue := "FILE \"game.bin\" BINARY\n" +
		"  TRACK 1 MODE1/2352\n" +
		"    INDEX 01 00:02:00\n" +
		"  TRACK 2 MODE1/2352\n" +
		"    INDEX 01 00:02:04\n"
	cuePath := writeTestFile(t, dir, "game.cue", []byte(cue))

	disc := openAndClose(t, cuePath)
	tracks := disc.Tracks()
	if len(tracks) != 2 {
		t.Fatalf("len(Tracks) = %d, want 2", len(tracks))
	}
	if tracks[0].Sectors != 4 {
		t.Errorf("track 1 Sectors = %d, want 4", tracks[0].Sectors)
	}
	if tracks[1].StartLBA != 4 {
		t.Errorf("track 2 StartLBA = %d, want 4", tracks[1].StartLBA)
	}
	if tracks[1].FileOffset != int64(4*2352) {
		t.Errorf("track 2 FileOffset = %d, want %d", tracks[1].FileOffset, 4*2352)
	}
	if got := disc.NumSectors(); got != 6 {
		t.Errorf("NumSectors = %d, want 6", got)
	}

	firstOfTrack2, err := disc.ReadData(4)
	if err != nil {
		t.Fatalf("ReadData(4): %v", err)
	}
	if !bytes.Equal(firstOfTrack2, wantUserData(0x14)) {
		t.Error("ReadData(4) does not return track 2 first sector data")
	}
	lastOfTrack1, err := disc.ReadData(3)
	if err != nil {
		t.Fatalf("ReadData(3): %v", err)
	}
	if !bytes.Equal(lastOfTrack1, wantUserData(0x13)) {
		t.Error("ReadData(3) does not return track 1 last sector data")
	}
}

func TestOpenCueAudioTrack(t *testing.T) {
	dir := t.TempDir()
	// Four data sectors followed by four audio sectors in one file.
	values := []byte{0x20, 0x21, 0x22, 0x23, 0x30, 0x31, 0x32, 0x33}
	bin := buildRawBin(2352, 16, values)
	writeTestFile(t, dir, "game.bin", bin)
	cue := "FILE \"game.bin\" BINARY\n" +
		"  TRACK 1 MODE1/2352\n" +
		"    INDEX 01 00:02:00\n" +
		"  TRACK 2 AUDIO\n" +
		"    INDEX 01 00:02:04\n"
	cuePath := writeTestFile(t, dir, "game.cue", []byte(cue))

	disc := openAndClose(t, cuePath)
	if got := disc.DataTracks(); got != 1 {
		t.Errorf("DataTracks = %d, want 1", got)
	}
	if got := disc.NumSectors(); got != 8 {
		t.Errorf("NumSectors = %d, want 8", got)
	}
	if _, err := disc.ReadData(3); err != nil {
		t.Errorf("ReadData(3) on the data track: %v", err)
	}
	_, err := disc.ReadData(4)
	if err == nil {
		t.Fatal("ReadData(4) on the audio track succeeded, want an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "audio") {
		t.Errorf("ReadData(4) error %q does not mention audio", err)
	}
}

func TestOpenDirectBin(t *testing.T) {
	dir := t.TempDir()
	bin := buildRawBin(2352, 16, []byte{0x40, 0x41})
	path := writeTestFile(t, dir, "game.bin", bin)

	disc := openAndClose(t, path)
	tracks := disc.Tracks()
	if len(tracks) != 1 || tracks[0].Mode != TrackMode1_2352 || disc.NumSectors() != 2 {
		t.Fatalf("tracks = %+v, NumSectors = %d, want one MODE1/2352 track of 2 sectors", tracks, disc.NumSectors())
	}
	got, err := disc.ReadData(1)
	if err != nil {
		t.Fatalf("ReadData(1): %v", err)
	}
	if !bytes.Equal(got, wantUserData(0x41)) {
		t.Error("ReadData(1) does not match the user data window [16:2064]")
	}
}

func TestOpenDiscErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("nonexistent path", func(t *testing.T) {
		if _, err := OpenDisc(filepath.Join(dir, "missing.cue")); err == nil {
			t.Error("OpenDisc on a missing cue succeeded, want an error")
		}
		if _, err := OpenDisc(filepath.Join(dir, "missing.iso")); err == nil {
			t.Error("OpenDisc on a missing iso succeeded, want an error")
		}
	})

	t.Run("cue referencing a missing bin", func(t *testing.T) {
		cue := "FILE \"missing.bin\" BINARY\n" +
			"  TRACK 1 MODE1/2352\n" +
			"    INDEX 01 00:02:00\n"
		cuePath := writeTestFile(t, dir, "broken.cue", []byte(cue))
		if _, err := OpenDisc(cuePath); err == nil {
			t.Error("OpenDisc on a cue with a missing bin succeeded, want an error")
		}
	})

	t.Run("iso with a non multiple size", func(t *testing.T) {
		path := writeTestFile(t, dir, "bad.iso", make([]byte, 2048+100))
		if _, err := OpenDisc(path); err == nil {
			t.Error("OpenDisc on a non multiple iso succeeded, want an error")
		}
	})

	t.Run("iso with a zero size", func(t *testing.T) {
		path := writeTestFile(t, dir, "empty.iso", nil)
		if _, err := OpenDisc(path); err == nil {
			t.Error("OpenDisc on an empty iso succeeded, want an error")
		}
	})

	t.Run("cue with an unsupported file type", func(t *testing.T) {
		cue := "FILE \"game.wav\" WAVE\n" +
			"  TRACK 1 AUDIO\n" +
			"    INDEX 01 00:02:00\n"
		cuePath := writeTestFile(t, dir, "wave.cue", []byte(cue))
		if _, err := OpenDisc(cuePath); err == nil {
			t.Error("OpenDisc on a WAVE cue succeeded, want an error")
		}
	})

	t.Run("unsupported extension", func(t *testing.T) {
		path := writeTestFile(t, dir, "game.chd", make([]byte, 2048))
		if _, err := OpenDisc(path); err == nil {
			t.Error("OpenDisc on an unsupported extension succeeded, want an error")
		}
	})
}

func TestDiscCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "game.iso", make([]byte, 2048))
	disc, err := OpenDisc(path)
	if err != nil {
		t.Fatalf("OpenDisc: %v", err)
	}
	if err := disc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := disc.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
	if _, err := disc.ReadData(0); err == nil {
		t.Error("ReadData after Close succeeded, want an error")
	}
}
