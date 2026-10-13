package cdblock

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
)

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

// Page access helpers, using the register layout of the package:
// HIRQ at [0x08:0x0A], its mask at [0x0C:0x0E], CR n (1 to 4) at
// [0x18+4(n-1):0x1A+4(n-1)], and the data port at [0x28:0x2A].

func pageCR(page []byte, n int) uint16 {
	return binary.BigEndian.Uint16(page[0x18+4*(n-1) : 0x1A+4*(n-1)])
}

func pageSetCR(page []byte, n int, v uint16) {
	binary.BigEndian.PutUint16(page[0x18+4*(n-1):0x1A+4*(n-1)], v)
}

func pageHIRQ(page []byte) uint16 {
	return binary.BigEndian.Uint16(page[0x08:0x0A])
}

func pageSetHIRQ(page []byte, v uint16) {
	binary.BigEndian.PutUint16(page[0x08:0x0A], v)
}

func pageSetMask(page []byte, v uint16) {
	binary.BigEndian.PutUint16(page[0x0C:0x0E], v)
}

func pagePort(page []byte) uint16 {
	return binary.BigEndian.Uint16(page[0x28:0x2A])
}

func statusByte(page []byte) byte {
	return byte(pageCR(page, 1) >> 8)
}

// issue clears HIRQ (the host acknowledgement), writes a command
// and its parameters into the CRs, and polls once.
func issue(page []byte, b *Block, crs [4]uint16) {
	pageSetHIRQ(page, 0)
	for i, v := range crs {
		pageSetCR(page, i+1, v)
	}
	b.Poll()
}

func issueCmd(page []byte, b *Block, cmd byte) {
	issue(page, b, [4]uint16{uint16(cmd) << 8, 0, 0, 0})
}

// drain reads n words from the data port, polling after each
// read to advance the FIFO, and returns the bytes.
func drain(page []byte, b *Block, words int) []byte {
	out := make([]byte, 0, words*2)
	for i := 0; i < words; i++ {
		w := pagePort(page)
		out = append(out, byte(w>>8), byte(w))
		b.Poll()
	}
	return out
}

// sectorCmd builds the parameter set shared by the sector data
// commands in this package's numbering: CR2 sector offset, CR3
// partition in the high byte, CR4 sector count.
func sectorCmd(cmd byte, off, part, count int) [4]uint16 {
	return [4]uint16{uint16(cmd) << 8, uint16(off), uint16(part) << 8, uint16(count)}
}

func sectorNumber(page []byte, b *Block, part int) int {
	issue(page, b, sectorCmd(CmdGetSectorNumber, 0, part, 0))
	return int(pageCR(page, 4))
}

// playRange issues a Play Disc for a FAD range with the given
// play mode byte.
func playRange(page []byte, b *Block, startFAD, endFAD int, mode byte) {
	s := uint32(0x800000 | startFAD)
	e := uint32(0x800000 | endFAD)
	issue(page, b, [4]uint16{
		uint16(CmdPlayDisc)<<8 | uint16(byte(s>>16)),
		uint16(s & 0xFFFF),
		uint16(mode)<<8 | uint16(byte(e>>16)),
		uint16(e & 0xFFFF),
	})
}

// makeMode1Sector builds one raw 2352 byte Mode 1 sector whose
// user bytes follow a known pattern seeded by seed.
func makeMode1Sector(lba int, seed int) []byte {
	sec := make([]byte, 2352)
	for j := 1; j <= 10; j++ {
		sec[j] = 0xff
	}
	m, s, f := cd.LBAtoMSF(lba)
	sec[12] = toBCD(m)
	sec[13] = toBCD(s)
	sec[14] = toBCD(f)
	sec[15] = 1
	for j := 0; j < 2048; j++ {
		sec[16+j] = byte(seed + j*3)
	}
	return sec
}

// dataImage builds a single track raw Mode 1 image of n sectors.
func dataImage(t *testing.T, n int) *cd.Image {
	t.Helper()
	var raw []byte
	for i := 0; i < n; i++ {
		raw = append(raw, makeMode1Sector(i, i*40+5)...)
	}
	im, err := cd.OpenBytes(raw, 2352)
	check(t, err == nil, "OpenBytes data image: %v", err)
	return im
}

// twoTrackImage builds a cue and bin pair in a temp dir: track 1
// is 4 raw Mode 1 data sectors, track 2 is 90 audio sectors whose
// bytes follow a known pattern.
func twoTrackImage(t *testing.T) *cd.Image {
	t.Helper()
	dir := t.TempDir()
	var bin []byte
	for i := 0; i < 4; i++ {
		bin = append(bin, makeMode1Sector(i, i*40+5)...)
	}
	for s := 0; s < 90; s++ {
		sec := make([]byte, 2352)
		for j := range sec {
			sec[j] = byte(j*3 + s*11)
		}
		bin = append(bin, sec...)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.bin"), bin, 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	cue := "FILE \"two.bin\" BINARY\n" +
		"  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n" +
		"  TRACK 02 AUDIO\n    INDEX 01 00:00:04\n"
	if err := os.WriteFile(filepath.Join(dir, "two.cue"), []byte(cue), 0o644); err != nil {
		t.Fatalf("write cue: %v", err)
	}
	im, err := cd.OpenCue(filepath.Join(dir, "two.cue"))
	check(t, err == nil, "OpenCue two track: %v", err)
	tr := im.Tracks()
	check(t, len(tr) == 2 && tr[1].Mode == 0 && tr[1].Start == 4, "two track layout = %+v", tr)
	return im
}

func TestHandshakeAndStatus(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)

	// No disc: Get Status reports NO DISC and sets CMOK.
	issueCmd(page, b, CmdGetStatus)
	check(t, pageHIRQ(page)&HIRQCMOK != 0, "CMOK not set after Get Status, HIRQ = %#x", pageHIRQ(page))
	check(t, statusByte(page) == StatusNODISC, "no disc status = %#x, want %#x", statusByte(page), StatusNODISC)
	check(t, pageCR(page, 1)&0xFF == 0, "flag and repeat low byte = %#x, want 0", pageCR(page, 1)&0xFF)

	// With a disc: STANDBY, and the FAD field reads the home
	// position 150 split across CR3 low byte and CR4.
	b.SetImage(dataImage(t, 4))
	issueCmd(page, b, CmdGetStatus)
	check(t, statusByte(page) == StatusSTANDBY, "disc status = %#x, want STANDBY", statusByte(page))
	check(t, pageCR(page, 2) == 0x4101, "status CR2 = %#x, want ctrl/adr 0x41 and track 1", pageCR(page, 2))
	check(t, pageCR(page, 3) == 0x0100, "status CR3 = %#x, want index 1 and FAD high 0", pageCR(page, 3))
	check(t, pageCR(page, 4) == 150, "status CR4 = %d, want FAD low 150", pageCR(page, 4))

	// OnIRQ fires when the newly set bit is unmasked, and stays
	// quiet when the mask clears it.
	fires := 0
	b.OnIRQ = func() { fires++ }
	pageSetMask(page, HIRQCMOK)
	issueCmd(page, b, CmdGetHardwareInfo)
	check(t, fires == 1, "OnIRQ fires = %d, want 1 with CMOK unmasked", fires)
	check(t, pageCR(page, 2) == 0x0002, "hardware info CR2 = %#x, want version 2", pageCR(page, 2))
	pageSetMask(page, 0)
	issueCmd(page, b, CmdGetStatus)
	check(t, fires == 1, "OnIRQ fires = %d, want still 1 with CMOK masked", fires)

	// A second identical Get Status still executes, because the
	// response tuple differs from the command tuple.
	check(t, pageHIRQ(page)&HIRQCMOK != 0, "CMOK not set on repeated Get Status")

	// Power off: Poll does nothing, no CMOK response.
	b.SetPower(false)
	pageSetHIRQ(page, 0)
	pageSetCR(page, 1, uint16(CmdGetStatus)<<8)
	b.Poll()
	check(t, pageHIRQ(page)&HIRQCMOK == 0, "CMOK set while power is off")
	b.SetPower(true)
}

func TestGetTOCAndEndTransfer(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	b.SetImage(twoTrackImage(t))

	issueCmd(page, b, CmdGetTOC)
	check(t, pageHIRQ(page)&HIRQDRDY != 0, "DRDY not set after Get TOC")
	check(t, pageCR(page, 2) == 6, "TOC size = %d words, want 6 (3 entries)", pageCR(page, 2))
	got := drain(page, b, 6)
	want := []byte{
		0x41, 0, 0, 150, // track 1, data, FAD 150
		0x01, 0, 0, 154, // track 2, audio, FAD 154
		0x41, 0, 0, 244, // lead-out at FAD 244 (94 sectors in all)
	}
	checkBytes(t, got, want, "TOC entries")
	check(t, pageHIRQ(page)&HIRQDRDY == 0, "DRDY still set after TOC drained")
	check(t, pageHIRQ(page)&HIRKEHST != 0, "EHST not set after TOC drained")

	// End Data Transfer mid FIFO: EHST sets and DRDY clears.
	issueCmd(page, b, CmdGetTOC)
	drain(page, b, 2)
	issueCmd(page, b, CmdEndDataTransfer)
	check(t, pageHIRQ(page)&HIRKEHST != 0, "EHST not set after End Data Transfer")
	check(t, pageHIRQ(page)&HIRQDRDY == 0, "DRDY still set after End Data Transfer")
	words := int(pageCR(page, 1)&0xFF)<<16 | int(pageCR(page, 2))
	check(t, words == 2, "transferred word count = %d, want 2", words)
}

func TestPlayDataBufferAndSectorData(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	im := dataImage(t, 6)
	b.SetImage(im)

	playRange(page, b, 150, 153, 0)
	check(t, statusByte(page) == StatusPLAY, "status after Play = %#x, want PLAY", statusByte(page))
	b.Advance(28636400)
	check(t, pageHIRQ(page)&HIRQPEND != 0, "PEND not set at play end")
	check(t, sectorNumber(page, b, 0) == 4, "partition 0 count = %d, want 4", sectorNumber(page, b, 0))

	// Get Sector Data returns the first sector's user bytes.
	issue(page, b, sectorCmd(CmdGetSectorData, 0, 0, 1))
	check(t, pageHIRQ(page)&HIRQDRDY != 0, "DRDY not set after Get Sector Data")
	user0, err := im.ReadSector(0)
	check(t, err == nil, "ReadSector(0): %v", err)
	got := drain(page, b, 1024)
	checkBytes(t, got, user0, "Get Sector Data sector 0")
	check(t, pageHIRQ(page)&HIRKEHST != 0, "EHST not set after sector data drained")

	// Sector info and actual size over the buffered range.
	issue(page, b, sectorCmd(CmdGetSectorInfo, 1, 0, 0))
	check(t, pageCR(page, 2) == 151, "sector info FAD = %d, want 151", pageCR(page, 2))
	issue(page, b, sectorCmd(CmdCalcActualSize, 0, 0, 2))
	issueCmd(page, b, CmdGetActualSize)
	words := int(pageCR(page, 1)&0xFF)<<16 | int(pageCR(page, 2))
	check(t, words == 2048, "actual size = %d words, want 2048 (2 sectors of 2048 bytes)", words)

	// FAD search finds the block holding FAD 152.
	issue(page, b, [4]uint16{uint16(CmdExecuteFADSearch) << 8, 0, 0, 152})
	issueCmd(page, b, CmdGetFADSearch)
	check(t, pageCR(page, 2) == 2, "FAD search position = %d, want 2", pageCR(page, 2))
	check(t, pageCR(page, 4) == 152, "FAD search FAD = %d, want 152", pageCR(page, 4))

	// Get Then Delete returns the same first sector and then
	// removes it.
	issue(page, b, sectorCmd(CmdGetThenDelete, 0, 0, 1))
	got = drain(page, b, 1024)
	checkBytes(t, got, user0, "Get Then Delete sector 0")
	check(t, sectorNumber(page, b, 0) == 3, "count after Get Then Delete = %d, want 3", sectorNumber(page, b, 0))

	// Delete removes one more.
	issue(page, b, sectorCmd(CmdDeleteSectorData, 0, 0, 1))
	check(t, pageHIRQ(page)&HIRKEHST != 0, "EHST not set after Delete Sector Data")
	check(t, sectorNumber(page, b, 0) == 2, "count after Delete = %d, want 2", sectorNumber(page, b, 0))
}

func TestSectorLengthFullSector(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	im := dataImage(t, 3)
	b.SetImage(im)
	playRange(page, b, 150, 151, 0)
	b.Advance(28636400)

	// Set the read length to the full sector form (type 3), with
	// the put direction marked no change.
	issue(page, b, [4]uint16{uint16(CmdSetSectorLength)<<8 | 3, 0xFF << 8, 0, 0})
	issue(page, b, sectorCmd(CmdGetSectorData, 0, 0, 1))
	raw, err := im.ReadRawSector(0)
	check(t, err == nil, "ReadRawSector(0): %v", err)
	got := drain(page, b, 1176)
	checkBytes(t, got, raw, "full sector Get Sector Data")
}

func TestFilterReverseMode(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	b.SetImage(dataImage(t, 4))

	// Reverse the match on filter 0: everything falls to its
	// false connection, which is discard, so nothing buffers.
	issue(page, b, [4]uint16{uint16(CmdSetFilterMode)<<8 | FilterModeReverse, 0, 0, 0})
	issueCmd(page, b, CmdGetFilterMode)
	check(t, pageCR(page, 1)&0xFF == FilterModeReverse, "filter mode readback = %#x", pageCR(page, 1)&0xFF)
	playRange(page, b, 150, 152, 0)
	b.Advance(28636400)
	check(t, sectorNumber(page, b, 0) == 0, "count with reversed filter = %d, want 0", sectorNumber(page, b, 0))

	// Back to pass all: the same range buffers.
	issue(page, b, [4]uint16{uint16(CmdSetFilterMode)<<8 | 0, 0, 0, 0})
	playRange(page, b, 150, 152, 0)
	b.Advance(28636400)
	check(t, sectorNumber(page, b, 0) == 3, "count with pass all filter = %d, want 3", sectorNumber(page, b, 0))
}

func TestFilterSubheaderFileNumber(t *testing.T) {
	// Three raw Mode 2 sectors with file numbers 0, 1 and 1.
	var raw []byte
	for i, fn := range []byte{0, 1, 1} {
		sec := make([]byte, 2352)
		for j := 1; j <= 10; j++ {
			sec[j] = 0xff
		}
		m, s, f := cd.LBAtoMSF(i)
		sec[12] = toBCD(m)
		sec[13] = toBCD(s)
		sec[14] = toBCD(f)
		sec[15] = 2
		sec[16] = fn
		sec[17] = 0
		sec[18] = 0x08 // submode: end of record style data flag pattern kept simple
		sec[19] = 0
		copy(sec[20:24], sec[16:20])
		for j := 0; j < 2048; j++ {
			sec[24+j] = byte(i*50 + j)
		}
		raw = append(raw, sec...)
	}
	im, err := cd.OpenBytes(raw, 2352)
	check(t, err == nil, "OpenBytes Mode 2: %v", err)

	page := make([]byte, 64)
	b := New(page)
	b.SetImage(im)

	// Filter 0: file number condition valid, file number 1.
	issue(page, b, [4]uint16{uint16(CmdSetFilterSubhead) << 8, 0, 1, 0})
	issue(page, b, [4]uint16{uint16(CmdSetFilterMode)<<8 | FilterModeFN, 0, 0, 0})
	issueCmd(page, b, CmdGetFilterSubhead)
	check(t, pageCR(page, 3)&0xFF == 1, "filter file number readback = %d, want 1", pageCR(page, 3)&0xFF)

	playRange(page, b, 150, 152, 0)
	b.Advance(28636400)
	check(t, sectorNumber(page, b, 0) == 2, "FN filtered count = %d, want 2", sectorNumber(page, b, 0))

	// The first buffered block is the second sector (file 1).
	issue(page, b, sectorCmd(CmdGetSectorInfo, 0, 0, 0))
	check(t, pageCR(page, 2) == 151, "first FN match FAD = %d, want 151", pageCR(page, 2))
	check(t, pageCR(page, 3)>>8 == 1, "first FN match file number = %d, want 1", pageCR(page, 3)>>8)
}

func TestAudioPlayAndSink(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	im := twoTrackImage(t)
	b.SetImage(im)

	var calls [][]int16
	b.SetAudioSink(func(samples []int16) {
		calls = append(calls, append([]int16(nil), samples...))
	})

	// Track 2 runs from FAD 154 to 156.
	playRange(page, b, 154, 156, 0)
	issueCmd(page, b, CmdGetStatus)
	check(t, statusByte(page) == StatusPLAY, "status during play = %#x, want PLAY", statusByte(page))

	b.Advance(381819) // one sector period
	check(t, len(calls) == 1, "sink calls after one period = %d, want 1", len(calls))
	check(t, len(calls[0]) == 1176, "samples per sector = %d, want 1176 (588 stereo pairs)", len(calls[0]))

	b.Advance(28636400)
	check(t, len(calls) == 3, "sink calls total = %d, want 3", len(calls))
	for s := 0; s < 3; s++ {
		raw, err := im.ReadRawSector(4 + s)
		check(t, err == nil, "ReadRawSector audio %d: %v", s, err)
		for j := 0; j < 1176; j++ {
			want := int16(binary.LittleEndian.Uint16(raw[j*2:]))
			if calls[s][j] != want {
				t.Fatalf("sector %d sample %d = %d, want %d from fixture bytes", s, j, calls[s][j], want)
			}
			assertions++
		}
	}
	check(t, pageHIRQ(page)&HIRQPEND != 0, "PEND not set at audio play end")
	issueCmd(page, b, CmdGetStatus)
	check(t, statusByte(page) == StatusPAUSE, "status after play end = %#x, want PAUSE", statusByte(page))

	// Audio sectors are never buffered.
	check(t, sectorNumber(page, b, 0) == 0, "buffered audio sectors = %d, want 0", sectorNumber(page, b, 0))
}

func TestSeekAndSubcodeQ(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	b.SetImage(twoTrackImage(t))

	seek := func(fad int) {
		pos := uint32(0x800000 | fad)
		issue(page, b, [4]uint16{
			uint16(CmdSeekDisc)<<8 | uint16(byte(pos>>16)),
			uint16(pos & 0xFFFF), 0, 0,
		})
	}
	subcode := func() []byte {
		issueCmd(page, b, CmdGetSubcodeQ)
		check(t, pageCR(page, 2) == 5, "subcode size = %d words, want 5", pageCR(page, 2))
		check(t, pageHIRQ(page)&HIRQDRDY != 0, "DRDY not set after Get Subcode Q")
		return drain(page, b, 5)
	}

	seek(154)
	q := subcode()
	check(t, len(q) == 10, "subcode Q length = %d, want 10 (CRC bytes not included)", len(q))
	check(t, q[0] == 0x01, "Q control/ADR = %#x, want 0x01 audio", q[0])
	check(t, q[1] == 0x02, "Q track = %#x, want BCD 2", q[1])
	check(t, q[2] == 0x01, "Q index = %#x, want BCD 1", q[2])
	check(t, q[3] == 0 && q[4] == 0 && q[5] == 0, "Q relative MSF = %v, want 0:0:0", q[3:6])
	check(t, q[6] == 0, "Q zero byte = %d", q[6])
	am, as, af := cd.LBAtoMSF(4)
	check(t, q[7] == toBCD(am) && q[8] == toBCD(as) && q[9] == toBCD(af),
		"Q absolute MSF bytes = %v, want BCD of %d:%d:%d", q[7:10], am, as, af)

	// One second into track 2: relative 0:01:00, absolute frame
	// advances by 75.
	seek(154 + 75)
	q = subcode()
	check(t, q[3] == 0 && q[4] == 0x01 && q[5] == 0, "Q relative MSF = %v, want BCD 0:1:0", q[3:6])
	am, as, af = cd.LBAtoMSF(4 + 75)
	check(t, q[7] == toBCD(am) && q[8] == toBCD(as) && q[9] == toBCD(af),
		"Q absolute MSF bytes = %v, want BCD of %d:%d:%d", q[7:10], am, as, af)
}

func TestSerializeRoundTripLazyRead(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	im := dataImage(t, 5)
	b.SetImage(im)
	playRange(page, b, 150, 152, 0)
	b.Advance(28636400)
	check(t, sectorNumber(page, b, 0) == 3, "pre save count = %d, want 3", sectorNumber(page, b, 0))

	state := b.Serialize()
	check(t, len(state) == stateFiltersEnd+2+3*blockRecord, "state size = %d", len(state))

	page2 := make([]byte, 64)
	b2 := New(page2)
	err := b2.Deserialize(state)
	check(t, err == nil, "Deserialize: %v", err)
	b2.SetImage(im)
	check(t, sectorNumber(page2, b2, 0) == 3, "restored count = %d, want 3", sectorNumber(page2, b2, 0))

	// The restored blocks carry no payloads, so this read re
	// reads sector 1 from the image by FAD.
	issue(page2, b2, sectorCmd(CmdGetSectorData, 1, 0, 1))
	user1, err := im.ReadSector(1)
	check(t, err == nil, "ReadSector(1): %v", err)
	got := drain(page2, b2, 1024)
	checkBytes(t, got, user1, "lazy re-read of restored sector 1")

	bad := append([]byte(nil), state...)
	bad[0] = 99
	check(t, b2.Deserialize(bad) != nil, "Deserialize should reject a bad version")
	check(t, b2.Deserialize(state[:10]) != nil, "Deserialize should reject a short state")
}

func TestPutRejectsAndUnknown(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	b.SetImage(dataImage(t, 3))

	issue(page, b, sectorCmd(CmdPutSectorData, 0, 0, 1))
	check(t, statusByte(page) == 0xFF, "Put status byte = %#x, want reject 0xFF", statusByte(page))
	check(t, pageHIRQ(page)&HIRQCMOK != 0, "CMOK not set after Put reject")
	check(t, sectorNumber(page, b, 0) == 0, "Put stored %d blocks, want 0", sectorNumber(page, b, 0))

	issueCmd(page, b, 0x99)
	check(t, statusByte(page) == 0xFF, "unknown command status = %#x, want reject 0xFF", statusByte(page))

	// Play, Seek and TOC reject with no disc.
	page2 := make([]byte, 64)
	b2 := New(page2)
	playRange(page2, b2, 150, 152, 0)
	check(t, statusByte(page2) == 0xFF, "Play with no disc status = %#x, want reject", statusByte(page2))
	issueCmd(page2, b2, CmdGetTOC)
	check(t, statusByte(page2) == 0xFF, "TOC with no disc status = %#x, want reject", statusByte(page2))
	// Get Hardware Info still answers with no disc.
	issueCmd(page2, b2, CmdGetHardwareInfo)
	check(t, statusByte(page2) == StatusNODISC, "hardware info no disc status = %#x, want NO DISC", statusByte(page2))
	check(t, pageCR(page2, 2) == 0x0002, "hardware info version = %#x, want 2", pageCR(page2, 2))
}

func TestDeviceConnectionAndScan(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	b.SetImage(dataImage(t, 6))

	// Disconnect the CD device: play streams but buffers
	// nothing.
	issue(page, b, [4]uint16{uint16(CmdSetCDConnection) << 8, 0, 0xFF << 8, 0})
	issueCmd(page, b, CmdGetCDConnection)
	check(t, pageCR(page, 3)>>8 == 0xFF, "device connection = %#x, want 0xFF", pageCR(page, 3)>>8)
	playRange(page, b, 150, 152, 0)
	b.Advance(28636400)
	check(t, sectorNumber(page, b, 0) == 0, "count with device disconnected = %d, want 0", sectorNumber(page, b, 0))

	// Scan moves the position at 16x and buffers nothing.
	issue(page, b, [4]uint16{uint16(CmdSeekDisc)<<8 | 0x80, 150, 0, 0})
	issueCmd(page, b, CmdScanDisc)
	check(t, statusByte(page) == StatusSCAN, "status in scan = %#x, want SCAN", statusByte(page))
	b.Advance(381819) // one sector period at 16x moves 16 sectors, clamped by the disc end
	issueCmd(page, b, CmdGetStatus)
	check(t, pageCR(page, 4) == 155, "FAD after scan = %d, want last FAD 155", pageCR(page, 4))
	check(t, sectorNumber(page, b, 0) == 0, "scan buffered %d blocks, want 0", sectorNumber(page, b, 0))
}

func TestGetBufferSize(t *testing.T) {
	page := make([]byte, 64)
	b := New(page)
	b.SetImage(dataImage(t, 6))

	// Fresh buffer: total 200 blocks, all 200 free, maximum
	// selectors 24 in the high byte of CR3.
	issueCmd(page, b, CmdGetBufferSize)
	check(t, pageHIRQ(page)&HIRQESEL != 0, "ESEL not set after Get Buffer Size")
	check(t, pageCR(page, 4) == 200, "total blocks = %d, want 200", pageCR(page, 4))
	check(t, pageCR(page, 2) == 200, "free blocks fresh = %d, want 200", pageCR(page, 2))
	check(t, pageCR(page, 3)>>8 == NumFilters, "maximum selectors = %d, want %d", pageCR(page, 3)>>8, NumFilters)

	// Buffer four sectors: free drops to 200 minus the used
	// count and the total stays 200.
	playRange(page, b, 150, 153, 0)
	b.Advance(28636400)
	check(t, sectorNumber(page, b, 0) == 4, "buffered count = %d, want 4", sectorNumber(page, b, 0))
	issueCmd(page, b, CmdGetBufferSize)
	check(t, pageCR(page, 4) == 200, "total blocks after buffering = %d, want 200", pageCR(page, 4))
	check(t, pageCR(page, 2) == 196, "free blocks after buffering 4 = %d, want 196", pageCR(page, 2))
}

func TestZZAssertionCount(t *testing.T) {
	t.Logf("assertions executed before this test: %d", assertions)
	check(t, assertions >= 40, "assertion count = %d, want at least 40", assertions)
}
