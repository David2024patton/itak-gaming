package gd

import (
	"bytes"
	"testing"
)

// Test (3): a .gdi with an audio track and a data track whose file has
// a quoted name containing spaces and a non-zero offset.
func TestGDIImage(t *testing.T) {
	dir := t.TempDir()
	iso := buildTestISO()
	raw := wrapRaw2352(iso)

	// Audio dummy: ten raw silent sectors.
	audio := make([]byte, 10*2352)
	writeTestFile(t, dir, "track01.raw", audio)

	// Data file: a 123 byte prefix the track offset must skip, then
	// the raw sectors. The file name contains spaces and is quoted
	// in the .gdi, exercising the quoted name parser.
	const dataOffset = 123
	dataFile := make([]byte, 0, dataOffset+len(raw))
	dataFile = append(dataFile, bytes.Repeat([]byte{0xAB}, dataOffset)...)
	dataFile = append(dataFile, raw...)
	writeTestFile(t, dir, "track 03 data.bin", dataFile)

	gdiText := "2\n" +
		"1 0 0 2352 \"track01.raw\" 0\n" +
		"3 450 4 2352 \"track 03 data.bin\" 123\n"
	gdiPath := writeTestFile(t, dir, "game.gdi", []byte(gdiText))

	im, err := Open(gdiPath)
	if err != nil {
		t.Fatalf("Open gdi: %v", err)
	}
	defer im.Close()

	if len(im.Tracks) != 2 {
		t.Fatalf("image has %d tracks, want 2", len(im.Tracks))
	}
	if im.Tracks[0].Number != 1 || im.Tracks[0].Ctrl != 0 || im.Tracks[0].LBA != 0 {
		t.Errorf("track 1 = %+v, want number 1 audio at LBA 0", im.Tracks[0])
	}
	dt := im.DataTrack()
	if dt == nil {
		t.Fatal("DataTrack is nil for .gdi")
	}
	if dt.Number != 3 || dt.LBA != 450 || dt.Ctrl != 4 || dt.SectorSize != 2352 {
		t.Errorf("data track = %+v, want number 3 LBA 450 ctrl 4 sector size 2352", dt)
	}
	if dt.File != "track 03 data.bin" {
		t.Errorf("data track file = %q, want %q", dt.File, "track 03 data.bin")
	}
	if dt.Offset != dataOffset {
		t.Errorf("data track offset = %d, want %d", dt.Offset, dataOffset)
	}
	if dt.Length != int64(len(raw)) {
		t.Errorf("data track length = %d, want %d", dt.Length, len(raw))
	}

	// Absolute LBA mapping: the PVD lives at data LBA 450 + 16.
	sec, err := im.ReadSector(450 + 16)
	if err != nil {
		t.Fatalf("ReadSector 466: %v", err)
	}
	if !bytes.Equal(sec, iso[16*2048:17*2048]) {
		t.Errorf("sector at absolute LBA 466 differs from ISO sector 16")
	}

	// Audio sectors must be refused.
	if _, err := im.ReadSector(0); err == nil {
		t.Errorf("ReadSector on audio track: expected error, got nil")
	}
	// The gap between the audio track end and the data track start,
	// and space past the end of the disc, must both be refused.
	if _, err := im.ReadSector(100); err == nil {
		t.Errorf("ReadSector in track gap: expected error, got nil")
	}
	if _, err := im.ReadSector(450 + testTotalSectors + 5); err == nil {
		t.Errorf("ReadSector past data track end: expected error, got nil")
	}

	checkISOContents(t, im)
}

// A .gdi naming a missing track file must fail at Open, not later.
func TestGDIMissingTrackFile(t *testing.T) {
	dir := t.TempDir()
	gdiPath := writeTestFile(t, dir, "bad.gdi", []byte("1\n1 0 4 2048 \"nope.iso\" 0\n"))
	if _, err := Open(gdiPath); err == nil {
		t.Errorf("Open gdi with missing track file: expected error, got nil")
	}
}

// DataTrack must pick the LAST data track.
func TestDataTrackPicksLast(t *testing.T) {
	dir := t.TempDir()
	iso := buildTestISO()
	writeTestFile(t, dir, "a.iso", iso)
	writeTestFile(t, dir, "b.iso", iso)
	gdiText := "2\n" +
		"1 0 4 2048 \"a.iso\" 0\n" +
		"2 100 4 2048 \"b.iso\" 0\n"
	gdiPath := writeTestFile(t, dir, "two.gdi", []byte(gdiText))
	im, err := Open(gdiPath)
	if err != nil {
		t.Fatalf("Open two data tracks: %v", err)
	}
	defer im.Close()
	if dt := im.DataTrack(); dt == nil || dt.Number != 2 {
		t.Errorf("DataTrack = %+v, want track number 2", dt)
	}

	// An image with only audio tracks has no data track.
	audioOnly := writeTestFile(t, dir, "audio.gdi", []byte("1\n1 0 0 2352 \"a.iso\" 0\n"))
	// Reuse a.iso bytes as the dummy audio backing file; ctrl is 0.
	im2, err := Open(audioOnly)
	if err != nil {
		t.Fatalf("Open audio only gdi: %v", err)
	}
	defer im2.Close()
	if dt := im2.DataTrack(); dt != nil {
		t.Errorf("DataTrack on audio only image = %+v, want nil", dt)
	}
	if _, err := im2.BootArea(); err == nil {
		t.Errorf("BootArea on audio only image: expected error, got nil")
	}
}
