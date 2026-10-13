package system

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/scd"
)

// ---------------------------------------------------------------------------
// SCD-4 system gate: the full-stack enginetest. A custom Genesis
// ROM (built below) releases the sub CPU; the sub CPU boot stub
// loads the test IP from a synthetic cue/bin disc; the IP drives
// the sub BIOS services (_cdbios at $5F22, _buram at $5F16) through
// their published stages: DRVINIT and TOC reads, MSCPLAY plus
// CDBSTAT, a raw-register RF5C164 tone, the ROMREADN sector read
// flow, backup RAM format/write/search/read/delete, and the
// SCD-3 ASIC identity trace, then hands Word RAM to the main CPU,
// which programs the VDP, copies the traced image buffer into
// VRAM tiles plus a 16x16 cell tile map, and checksums the VRAM
// readback into comm command 1. The Go side asserts the stage
// bitmap, the checksums, the rendered frame, both audio tones,
// save/restore determinism at 45 frames, and legacy CD state
// acceptance.
// ---------------------------------------------------------------------------

// Branch condition codes for the assembler below.
const (
	cd4BRA = 0
	cd4BHI = 2
	cd4BCC = 4
	cd4BCS = 5
	cd4BNE = 6
	cd4BEQ = 7
	cd4BGE = 12
	cd4BLT = 13
	cd4BGT = 14
	cd4BLE = 15
)

// MOVE size codes (bits 13-12 of the opcode word).
const (
	cd4SizeB = 1
	cd4SizeW = 3
	cd4SizeL = 2
)

// cd4Fixup is one pending patch: a word branch displacement or a
// 32 bit absolute address.
type cd4Fixup struct {
	pos   int
	label string
	abs32 bool
}

// cd4asm is a small two-pass 68000 assembler local to this test
// file, in the spirit of the scd package assembler: labels,
// branches, and the MOVE and JSR forms the two test programs
// need. Addresses are absolute: the assembler is created with the
// address its first byte will run at.
type cd4asm struct {
	base   int
	buf    []byte
	labels map[string]int
	fixups []cd4Fixup
}

func newCD4Asm(base int) *cd4asm {
	return &cd4asm{base: base, labels: make(map[string]int)}
}

func (a *cd4asm) mark(name string) { a.labels[name] = a.base + len(a.buf) }

func (a *cd4asm) emit16(v uint16) {
	a.buf = append(a.buf, byte(v>>8), byte(v))
}

func (a *cd4asm) emit32(v uint32) {
	a.buf = append(a.buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func (a *cd4asm) emitBytes(b []byte) { a.buf = append(a.buf, b...) }

func (a *cd4asm) finish() []byte {
	for _, f := range a.fixups {
		target, ok := a.labels[f.label]
		if !ok {
			panic("cd4asm: fixup to unknown label " + f.label)
		}
		if f.abs32 {
			binary.BigEndian.PutUint32(a.buf[f.pos:f.pos+4], uint32(target))
			continue
		}
		disp := target - (a.base + f.pos)
		binary.BigEndian.PutUint16(a.buf[f.pos:f.pos+2], uint16(int16(disp)))
	}
	return a.buf
}

// move emits a generic MOVE: opcode size bits, destination reg
// and mode, source mode and reg, then the source extension words
// followed by the destination extension words.
func (a *cd4asm) move(size, srcMode, srcReg int, srcExt []uint16, dstReg, dstMode int, dstExt []uint16) {
	a.emit16(uint16(size)<<12 | uint16(dstReg)<<9 | uint16(dstMode)<<6 | uint16(srcMode)<<3 | uint16(srcReg))
	for _, e := range srcExt {
		a.emit16(e)
	}
	for _, e := range dstExt {
		a.emit16(e)
	}
}

func ext32(v uint32) []uint16 { return []uint16{uint16(v >> 16), uint16(v)} }

func (a *cd4asm) moveq(v int, dn int) {
	a.emit16(0x7000 | uint16(dn)<<9 | uint16(v&0xFF))
}

func (a *cd4asm) leaAbs(addr uint32, an int) {
	a.emit16(0x41F9 | uint16(an)<<9)
	a.emit32(addr)
}

func (a *cd4asm) leaLabel(name string, an int) {
	a.emit16(0x41F9 | uint16(an)<<9)
	a.fixups = append(a.fixups, cd4Fixup{pos: len(a.buf), label: name, abs32: true})
	a.emit32(0)
}

func (a *cd4asm) moveImmToDn(size int, v uint32, dn int) {
	op := map[int]uint16{cd4SizeB: 0x103C, cd4SizeW: 0x303C, cd4SizeL: 0x203C}[size]
	a.emit16(op | uint16(dn)<<9)
	if size == cd4SizeL {
		a.emit32(v)
	} else {
		a.emit16(uint16(v))
	}
}

func (a *cd4asm) moveImmToAbs(size int, v uint32, addr uint32) {
	op := map[int]uint16{cd4SizeB: 0x13FC, cd4SizeW: 0x33FC, cd4SizeL: 0x23FC}[size]
	a.emit16(op)
	if size == cd4SizeL {
		a.emit32(v)
	} else {
		a.emit16(uint16(v))
	}
	a.emit32(addr)
}

// moveImmToPostInc emits MOVE #imm, (An)+ (used for VDP map and
// ASIC trace table entries).
func (a *cd4asm) moveImmToPostInc(size int, v uint32, an int) {
	var srcExt []uint16
	if size == cd4SizeL {
		srcExt = ext32(v)
	} else {
		srcExt = []uint16{uint16(v)}
	}
	a.move(size, 7, 4, srcExt, an, 3, nil)
}

func (a *cd4asm) moveDnToAbs(size int, dn int, addr uint32) {
	a.move(size, 0, dn, nil, 1, 7, ext32(addr))
}

func (a *cd4asm) moveAbsToDn(size int, addr uint32, dn int) {
	a.move(size, 7, 1, ext32(addr), dn, 0, nil)
}

func (a *cd4asm) moveDnToAn(size int, dn int, an int) {
	a.move(size, 0, dn, nil, an, 2, nil)
}

func (a *cd4asm) moveDnToPostInc(size int, dn int, an int) {
	a.move(size, 0, dn, nil, an, 3, nil)
}

func (a *cd4asm) moveDnToPreDec(size int, dn int, an int) {
	a.move(size, 0, dn, nil, an, 4, nil)
}

func (a *cd4asm) movePostIncToDn(size int, an int, dn int) {
	a.move(size, 3, an, nil, dn, 0, nil)
}

func (a *cd4asm) movePostIncToPostInc(size int, anSrc, anDst int) {
	a.move(size, 3, anSrc, nil, anDst, 3, nil)
}

// moveDispToDn emits MOVE (disp, An), Dn.
func (a *cd4asm) moveDispToDn(size int, disp int, an int, dn int) {
	a.move(size, 5, an, []uint16{uint16(int16(disp))}, dn, 0, nil)
}

func (a *cd4asm) cmpi(size int, v uint32, dn int) {
	op := map[int]uint16{cd4SizeB: 0x0C00, cd4SizeW: 0x0C40, cd4SizeL: 0x0C80}[size]
	a.emit16(op | uint16(dn))
	if size == cd4SizeL {
		a.emit32(v)
	} else {
		a.emit16(uint16(v))
	}
}

// cmpbDn emits CMP.B srcDn, dstDn.
func (a *cd4asm) cmpbDn(srcDn, dstDn int) {
	a.emit16(0xB000 | uint16(dstDn)<<9 | uint16(srcDn))
}

func (a *cd4asm) tstDn(size int, dn int) {
	op := map[int]uint16{cd4SizeB: 0x4A00, cd4SizeW: 0x4A40, cd4SizeL: 0x4A80}[size]
	a.emit16(op | uint16(dn))
}

func (a *cd4asm) btstImm(bit int, dn int) {
	a.emit16(0x0800 | uint16(dn))
	a.emit16(uint16(bit))
}

func (a *cd4asm) bsetImm(bit int, dn int) {
	a.emit16(0x08C0 | uint16(dn))
	a.emit16(uint16(bit))
}

func (a *cd4asm) addq(size int, n int, dn int) {
	op := map[int]uint16{cd4SizeB: 0x5000, cd4SizeW: 0x5040, cd4SizeL: 0x5080}[size]
	a.emit16(op | uint16(n&7)<<9 | uint16(dn))
}

func (a *cd4asm) subq(size int, n int, dn int) {
	op := map[int]uint16{cd4SizeB: 0x5100, cd4SizeW: 0x5140, cd4SizeL: 0x5180}[size]
	a.emit16(op | uint16(n&7)<<9 | uint16(dn))
}

func (a *cd4asm) addiW(v uint16, dn int) {
	a.emit16(0x0640 | uint16(dn))
	a.emit16(v)
}

func (a *cd4asm) addDn(size int, srcDn, dstDn int) {
	op := map[int]uint16{cd4SizeW: 0xD040, cd4SizeL: 0xD080}[size]
	a.emit16(op | uint16(dstDn)<<9 | uint16(srcDn))
}

func (a *cd4asm) clrDn(size int, dn int) {
	op := map[int]uint16{cd4SizeW: 0x4240, cd4SizeL: 0x4280}[size]
	a.emit16(op | uint16(dn))
}

func (a *cd4asm) jsrAbs(addr uint32) {
	a.emit16(0x4EB9)
	a.emit32(addr)
}

func (a *cd4asm) branch(cond int, label string) {
	a.emit16(0x6000 | uint16(cond)<<8)
	a.fixups = append(a.fixups, cd4Fixup{pos: len(a.buf), label: label})
	a.emit16(0)
}

func (a *cd4asm) bra(label string) { a.branch(cd4BRA, label) }

func (a *cd4asm) dbra(dn int, label string) {
	a.emit16(0x51C8 | uint16(dn))
	a.fixups = append(a.fixups, cd4Fixup{pos: len(a.buf), label: label})
	a.emit16(0)
}

// pollCDBCHK emits a bounded CDBCHK poll loop: it calls _cdbios
// with D0.w = $80 until the trampoline reports carry clear, or
// the iteration budget runs out and control falls to failLabel.
// The budget counts jsr round trips; a trampoline that blocks
// until the per-line BIOSService completes the request resolves
// in one trip, and a purely polled one resolves as soon as the
// queued work drains.
func (a *cd4asm) pollCDBCHK(loopLabel string, failLabel string) {
	a.moveImmToDn(cd4SizeL, 20000, 6)
	a.mark(loopLabel)
	a.moveImmToDn(cd4SizeW, 0x80, 0) // CDBCHK
	a.jsrAbs(0x5F22)
	a.branch(cd4BCC, loopLabel+"ok")
	a.subq(cd4SizeL, 1, 6)
	a.branch(cd4BNE, loopLabel)
	a.bra(failLabel)
	a.mark(loopLabel + "ok")
}

func bcd(v int) uint32 {
	return uint32(v/10)<<4 | uint32(v%10)
}

// tocLong returns the expected CDBTOCREAD result for a track: BCD
// minute, second and frame bytes (frame bit 7 set for a data
// track) over the track number byte.
func tocLong(lba int, track int, data bool) uint32 {
	m, s, f := scd.LBAToMSF(lba)
	fr := bcd(f)
	if data {
		fr |= 0x80
	}
	return bcd(m)<<24 | bcd(s)<<16 | fr<<8 | uint32(track)
}

// headerLong returns the expected CDCREAD header long for a data
// sector: BCD minute, second, frame and the mode byte 1.
func headerLong(lba int) uint32 {
	m, s, f := scd.LBAToMSF(lba)
	return bcd(m)<<24 | bcd(s)<<16 | bcd(f)<<8 | 0x01
}

// payloadByte is the fixture payload pattern at byte i of the
// LBA 8 sector user area: (i*7+3) mod 251, with the first eight
// bytes replaced by the ASCII marker ITAKBIOS.
func payloadByte(i int) byte {
	if i < 8 {
		return "ITAKBIOS"[i]
	}
	return byte((i*7 + 3) % 251)
}

// payloadSum is the word checksum of the 2048 payload user bytes.
func payloadSum() uint16 {
	var sum uint16
	for i := 0; i < 2048; i++ {
		sum += uint16(payloadByte(i))
	}
	return sum
}

// scd4Info carries the fixture constants the IP is assembled
// against and the gate asserts.
type scd4Info struct {
	track1Sectors int
	track2Start   int
	track3Start   int
	tocTrack1     uint32
	tocTrack3     uint32
	payloadLBA    int
	payloadSum    uint16
	headerLBA8    uint32
}

// dataSector builds one raw MODE1/2352 sector for lba: the sync
// pattern, a BCD MSF plus mode 1 header, and the 2048 user bytes.
func dataSector(lba int, user []byte) []byte {
	sec := make([]byte, 2352)
	sec[0] = 0x00
	for i := 1; i <= 10; i++ {
		sec[i] = 0xFF
	}
	sec[11] = 0x00
	m, s, f := scd.LBAToMSF(lba)
	sec[12] = byte(bcd(m))
	sec[13] = byte(bcd(s))
	sec[14] = byte(bcd(f))
	sec[15] = 0x01
	copy(sec[16:2064], user)
	return sec
}

// makeSCDBIOSCue builds the SCD-4 fixture disc: track 1 is 32
// MODE1/2352 data sectors (boot sector at LBA 0, the IP in the
// sectors from LBA 1, the known payload sector at LBA 8), track 2
// is 150 audio sectors of a 440 Hz sine starting right after
// track 1, and track 3 is 150 audio sectors of an 880 Hz sine.
// INDEX lines are computed from the actual sector counts. When
// the environment variable SCD4_FIXTURE_DIR is set, the cue and
// bin are written there (so the shell enginetest can share the
// fixture); otherwise a fresh temporary directory is used.
func makeSCDBIOSCue(t *testing.T) (string, scd4Info) {
	t.Helper()
	info := scd4Info{
		track1Sectors: 32,
		payloadLBA:    8,
		payloadSum:    payloadSum(),
	}
	info.track2Start = info.track1Sectors
	info.track3Start = info.track2Start + 150
	info.tocTrack1 = tocLong(0, 1, true)
	info.tocTrack3 = tocLong(info.track3Start, 3, false)
	info.headerLBA8 = headerLong(info.payloadLBA)

	ip := buildSCD4IP(info)
	if len(ip) > 7*2048 {
		t.Fatalf("SCD-4 IP is %d bytes, want at most %d so it stays clear of the payload sector at LBA %d", len(ip), 7*2048, info.payloadLBA)
	}

	// The disc main program: the same main side program the
	// companion cartridge runs, assembled at its Word RAM entry
	// 0x200208. It must fit in the boot sector user bytes from
	// offset 0x208 to the end of the 2048 byte user area.
	mainProg := buildSCD4MainProgram(0x200208)
	if len(mainProg) > 0x800-0x208 {
		t.Fatalf("SCD-4 disc main program is %d bytes, want at most %d so it fits in boot sector user bytes 0x208..0x800", len(mainProg), 0x800-0x208)
	}
	t.Logf("SCD-4 disc main program: %d bytes (limit %d)", len(mainProg), 0x800-0x208)

	bin := make([]byte, 0, 332*2352)
	// Track 1 sector 0: the boot header, plus the disc main
	// program at user offset 0x200 behind the ITAKMAIN marker.
	user0 := make([]byte, 2048)
	copy(user0, "SEGADISCSYSTEM")
	binary.BigEndian.PutUint32(user0[0x30:], 1)
	binary.BigEndian.PutUint32(user0[0x34:], uint32(len(ip)))
	copy(user0[0x200:], "ITAKMAIN")
	copy(user0[0x208:], mainProg)
	bin = append(bin, dataSector(0, user0)...)
	// Track 1 sectors 1 to 31: zeros, with the payload pattern at
	// LBA 8. The IP is overlaid into LBAs 1.. afterwards.
	for lba := 1; lba < info.track1Sectors; lba++ {
		user := make([]byte, 2048)
		if lba == info.payloadLBA {
			for i := range user {
				user[i] = payloadByte(i)
			}
		}
		bin = append(bin, dataSector(lba, user)...)
	}
	// Overlay the IP: the boot stub loads consecutive LBAs from
	// LBA 1 into $026000, so IP chunk k (0 based) sits in disc
	// LBA 1+k. The size cap asserted above (at most 7 sectors,
	// payload at load sector 7 counting from LBA 1) keeps every
	// chunk on LBAs 1..7, clear of the payload sector.
	for k := 0; k*2048 < len(ip); k++ {
		lba := 1 + k
		if lba >= info.payloadLBA {
			t.Fatalf("SCD-4 IP chunk %d would land on payload LBA %d", k, info.payloadLBA)
		}
		off := lba * 2352
		chunk := ip[k*2048:]
		if len(chunk) > 2048 {
			chunk = chunk[:2048]
		}
		copy(bin[off+16:off+16+len(chunk)], chunk)
	}

	// Audio tracks: stereo sine, same value on both channels.
	sine := func(freq float64, sectors int) {
		for s := 0; s < sectors; s++ {
			sec := make([]byte, 2352)
			for f := 0; f < 588; f++ {
				idx := s*588 + f
				v := int16(10000 * math.Sin(2*math.Pi*freq*float64(idx)/44100))
				binary.LittleEndian.PutUint16(sec[f*4:], uint16(v))
				binary.LittleEndian.PutUint16(sec[f*4+2:], uint16(v))
			}
			bin = append(bin, sec...)
		}
	}
	sine(440, 150)
	sine(880, 150)

	dir := t.TempDir()
	if env := os.Getenv("SCD4_FIXTURE_DIR"); env != "" {
		dir = env
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("SCD4_FIXTURE_DIR mkdir: %v", err)
		}
	}
	binPath := filepath.Join(dir, "bios.bin")
	if err := os.WriteFile(binPath, bin, 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	msf := func(lba int) string {
		m, s, f := scd.LBAToMSF(lba)
		return fmt.Sprintf("%02d:%02d:%02d", m, s, f)
	}
	cue := "FILE \"bios.bin\" BINARY\n" +
		"  TRACK 01 MODE1/2352\n" +
		"    INDEX 01 " + msf(0) + "\n" +
		"  TRACK 02 AUDIO\n" +
		"    INDEX 01 " + msf(info.track2Start) + "\n" +
		"  TRACK 03 AUDIO\n" +
		"    INDEX 01 " + msf(info.track3Start) + "\n"
	cuePath := filepath.Join(dir, "bios.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatalf("write cue: %v", err)
	}
	return cuePath, info
}

// ---------------------------------------------------------------------------
// The sub CPU initial program, loaded at $026000 by the boot stub.
// It runs stages a to h from the task brief, maintaining a stage
// bitmap in D7 (bit per stage) that it mirrors into the PRG-RAM
// results cell at $025000 after every stage and finally into comm
// status 1. A failing sub-check stores a distinct detail code in
// the PRG-RAM cell at $025002 (stage letter in the high nibble
// pattern 0xA0 to 0xF0 and 0x60, sub-check in the low bits) and
// the stage bit stays clear. Argument tables and data patterns
// are staged in PRG-RAM by the Go side before the run (see
// stageSCD4Tables), at these sub CPU addresses:
//
//	$025010 DRVINIT table {1, $FF}
//	$025012 MSCPLAY argument word (track 2)
//	$025014 CDBTOCREAD argument word (track 3)
//	$025016 CDBTOCREAD argument word (track 1)
//	$025020 ROMREADN arguments {sector.l = 8, count.l = 1}
//	$02A000 BRM parameter block: filename at +0 (11 bytes),
//	        flag byte at +11, block size word at +12, with a
//	        data address long at +16 and a byte count long at
//	        +20 as extra fields
//	$02B000 BRM write source data (64 bytes)
//	$02C000 BRM read destination (64 bytes)
//
// BIOS register conventions used (the published table style):
// _cdbios functions take their parameter block at A0 when they
// have one (DRVINIT, MSCPLAY, ROMREADN), CDBTOCREAD also takes
// the track number in D1.w, CDCSETMODE takes its mode in D1.w,
// CDCTRN takes the data buffer in A0 and the header buffer in
// A1. _buram calls take the file parameter block at A0, the data
// buffer in A1, the flag in D1.w, the block count in D2.w and
// the byte count in D3.l.
// ---------------------------------------------------------------------------

func buildSCD4IP(info scd4Info) []byte {
	a := newCD4Asm(0x26000)

	storeBitmap := func() { a.moveDnToAbs(cd4SizeW, 7, 0x025000) }
	detail := func(code uint16) { a.moveImmToAbs(cd4SizeW, uint32(code), 0x025002) }

	// Stage a: DRVINIT {start 1, end $FF}, poll CDBCHK.
	a.mark("stageA")
	a.leaAbs(0x025010, 0)
	a.moveImmToDn(cd4SizeW, 0x10, 0)
	a.jsrAbs(0x5F22)
	a.pollCDBCHK("pollA", "failA")
	a.bsetImm(0, 7)
	a.bra("stageAend")
	a.mark("failA")
	detail(0xA1)
	a.mark("stageAend")
	storeBitmap()

	// Stage b: CDBTOCREAD for tracks 3 and 1.
	a.mark("stageB")
	a.leaAbs(0x025014, 0)
	a.moveImmToDn(cd4SizeW, 3, 1)
	a.moveImmToDn(cd4SizeW, 0x83, 0)
	a.jsrAbs(0x5F22)
	a.cmpi(cd4SizeL, info.tocTrack3, 0)
	a.branch(cd4BNE, "failB1")
	a.leaAbs(0x025016, 0)
	a.moveImmToDn(cd4SizeW, 1, 1)
	a.moveImmToDn(cd4SizeW, 0x83, 0)
	a.jsrAbs(0x5F22)
	a.cmpi(cd4SizeL, info.tocTrack1, 0)
	a.branch(cd4BNE, "failB2")
	a.bsetImm(1, 7)
	a.bra("stageBend")
	a.mark("failB1")
	detail(0xB1)
	a.bra("stageBend")
	a.mark("failB2")
	detail(0xB2)
	a.mark("stageBend")
	storeBitmap()

	// Stage c: MSCPLAY track 2, poll, CDBSTAT table checks.
	a.mark("stageC")
	a.leaAbs(0x025012, 0)
	a.moveImmToDn(cd4SizeW, 2, 1)
	a.moveImmToDn(cd4SizeW, 0x11, 0)
	a.jsrAbs(0x5F22)
	a.pollCDBCHK("pollC", "failC1")
	a.moveImmToDn(cd4SizeW, 0x81, 0) // CDBSTAT: A0 = status table
	a.jsrAbs(0x5F22)
	a.moveDispToDn(cd4SizeB, 7, 0, 0) // BP3: current track
	a.cmpi(cd4SizeB, 2, 0)
	a.branch(cd4BNE, "failC2")
	a.moveDispToDn(cd4SizeB, 1, 0, 0) // MSC status byte: 1 = playing
	a.cmpi(cd4SizeB, 1, 0)
	a.branch(cd4BNE, "failC3")
	a.bsetImm(2, 7)
	a.bra("stageCend")
	a.mark("failC1")
	detail(0xC1)
	a.bra("stageCend")
	a.mark("failC2")
	detail(0xC2)
	a.bra("stageCend")
	a.mark("failC3")
	detail(0xC3)
	a.mark("stageCend")
	storeBitmap()

	// Stage d: PCM tone through raw registers only. A 128 byte
	// sine period (sign-magnitude, amplitude 120) goes to wave
	// RAM bank 0 with a loop marker at byte 128 and loop start 0;
	// FD = $1000 advances 2 samples per chip tick, so the tone is
	// (12500000/384) * 2 / 128 = about 508.6 Hz.
	a.mark("stageD")
	a.leaLabel("sinetab", 1)
	a.leaAbs(0xFF1000, 0)
	a.moveImmToDn(cd4SizeW, 127, 4)
	a.mark("loopD")
	a.movePostIncToPostInc(cd4SizeB, 1, 0)
	a.dbra(4, "loopD")
	a.moveImmToAbs(cd4SizeB, 0xFF, 0xFF1080) // loop marker
	a.moveImmToAbs(cd4SizeB, 0xC0, 0xFF0007) // CTRL: on, MOD 1, channel 0
	a.moveImmToAbs(cd4SizeB, 0xFF, 0xFF0000) // ENV
	a.moveImmToAbs(cd4SizeB, 0xFF, 0xFF0001) // PAN
	a.moveImmToAbs(cd4SizeB, 0x00, 0xFF0002) // FD low
	a.moveImmToAbs(cd4SizeB, 0x10, 0xFF0003) // FD high: FD = $1000
	a.moveImmToAbs(cd4SizeB, 0x00, 0xFF0004) // loop start low
	a.moveImmToAbs(cd4SizeB, 0x00, 0xFF0005) // loop start high
	a.moveImmToAbs(cd4SizeB, 0x00, 0xFF0006) // start = 0 (retrigger)
	a.moveImmToAbs(cd4SizeB, 0x01, 0xFF0008) // channel 0 on
	a.moveAbsToDn(cd4SizeB, 0xFF0007, 0)
	a.cmpi(cd4SizeB, 0xC0, 0)
	a.branch(cd4BNE, "failD1")
	a.moveAbsToDn(cd4SizeB, 0xFF0003, 0)
	a.cmpi(cd4SizeB, 0x10, 0)
	a.branch(cd4BNE, "failD2")
	a.moveAbsToDn(cd4SizeB, 0xFF0008, 0)
	a.cmpi(cd4SizeB, 0x01, 0)
	a.branch(cd4BNE, "failD3")
	a.bsetImm(3, 7)
	a.bra("stageDend")
	a.mark("failD1")
	detail(0xD1)
	a.bra("stageDend")
	a.mark("failD2")
	detail(0xD2)
	a.bra("stageDend")
	a.mark("failD3")
	detail(0xD3)
	a.mark("stageDend")
	storeBitmap()

	// Stage e: the ROMREADN sector read flow for LBA 8.
	a.mark("stageE")
	a.leaAbs(0x025020, 0)
	a.moveImmToDn(cd4SizeL, 8, 1)
	a.moveImmToDn(cd4SizeL, 1, 2)
	a.moveImmToDn(cd4SizeW, 0x20, 0) // ROMREADN
	a.jsrAbs(0x5F22)
	a.pollCDBCHK("pollE", "failE1")
	a.moveImmToDn(cd4SizeW, 0x96, 0) // CDCSETMODE, D1.w = 0
	a.moveImmToDn(cd4SizeW, 0, 1)
	a.jsrAbs(0x5F22)
	a.moveImmToDn(cd4SizeL, 20000, 6)
	a.mark("statE")
	a.moveImmToDn(cd4SizeW, 0x8A, 0) // CDCSTAT
	a.jsrAbs(0x5F22)
	a.branch(cd4BCC, "statEok")
	a.subq(cd4SizeL, 1, 6)
	a.branch(cd4BNE, "statE")
	a.bra("failE2")
	a.mark("statEok")
	a.moveImmToDn(cd4SizeW, 0x8B, 0) // CDCREAD: header long in D0
	a.jsrAbs(0x5F22)
	a.cmpi(cd4SizeL, info.headerLBA8, 0)
	a.branch(cd4BNE, "failE3")
	a.leaAbs(0x027000, 0) // CDCTRN data buffer
	a.leaAbs(0x028000, 1) // CDCTRN header buffer
	a.moveImmToDn(cd4SizeW, 0x8C, 0)
	a.jsrAbs(0x5F22)
	a.moveImmToDn(cd4SizeW, 0x8D, 0) // CDCACK
	a.jsrAbs(0x5F22)
	a.leaAbs(0x027000, 0)
	a.clrDn(cd4SizeL, 2)
	a.moveq(0, 3)
	a.moveImmToDn(cd4SizeW, 2047, 1)
	a.mark("sumE")
	a.movePostIncToDn(cd4SizeB, 0, 3)
	a.addDn(cd4SizeW, 3, 2)
	a.dbra(1, "sumE")
	a.cmpi(cd4SizeW, uint32(info.payloadSum), 2)
	a.branch(cd4BNE, "failE4")
	a.moveAbsToDn(cd4SizeL, 0x027000, 0)
	a.cmpi(cd4SizeL, 0x4954414B, 0) // "ITAK"
	a.branch(cd4BNE, "failE5")
	a.moveAbsToDn(cd4SizeL, 0x027004, 0)
	a.cmpi(cd4SizeL, 0x42494F53, 0) // "BIOS"
	a.branch(cd4BNE, "failE5")
	a.bsetImm(4, 7)
	a.bra("stageEend")
	a.mark("failE1")
	detail(0xE1)
	a.bra("stageEend")
	a.mark("failE2")
	detail(0xE2)
	a.bra("stageEend")
	a.mark("failE3")
	detail(0xE3)
	a.bra("stageEend")
	a.mark("failE4")
	detail(0xE4)
	a.bra("stageEend")
	a.mark("failE5")
	detail(0xE5)
	a.mark("stageEend")
	storeBitmap()

	// Stage f: backup RAM through _buram.
	a.mark("stageF")
	a.moveImmToDn(cd4SizeW, 6, 0) // BRMFORMAT
	a.jsrAbs(0x5F16)
	a.branch(cd4BCS, "failF1")
	a.moveImmToDn(cd4SizeW, 1, 0) // BRMSTAT
	a.jsrAbs(0x5F16)
	a.cmpi(cd4SizeW, 125, 0)
	a.branch(cd4BNE, "failF2")
	a.cmpi(cd4SizeW, 0, 1)
	a.branch(cd4BNE, "failF3")
	a.leaAbs(0x02A000, 0) // BRMWRITE
	a.leaAbs(0x02B000, 1)
	a.moveImmToDn(cd4SizeW, 0, 1) // flag 0
	a.moveImmToDn(cd4SizeW, 1, 2) // 1 block
	a.moveImmToDn(cd4SizeL, 64, 3)
	a.moveImmToDn(cd4SizeW, 4, 0)
	a.jsrAbs(0x5F16)
	a.branch(cd4BCS, "failF4")
	a.moveImmToDn(cd4SizeW, 1, 0) // BRMSTAT: 123 free after 1 file
	a.jsrAbs(0x5F16)
	a.cmpi(cd4SizeW, 123, 0)
	a.branch(cd4BNE, "failF5")
	a.leaAbs(0x02A000, 0) // BRMSERCH
	a.moveImmToDn(cd4SizeW, 2, 0)
	a.jsrAbs(0x5F16)
	a.branch(cd4BCS, "failF6")
	a.cmpi(cd4SizeW, 1, 0)
	a.branch(cd4BNE, "failF7")
	a.leaAbs(0x02A000, 0) // BRMREAD
	a.leaAbs(0x02C000, 1)
	a.moveImmToDn(cd4SizeW, 1, 2)
	a.moveImmToDn(cd4SizeW, 3, 0)
	a.jsrAbs(0x5F16)
	a.branch(cd4BCS, "failF8")
	// The compare reads the reference copy at $02D000, not the
	// write source at $02B000: BRMSTAT writes its 12 byte display
	// string through (A1), and A1 still pointed at the source
	// buffer during the second BRMSTAT above, so the source
	// buffer's first bytes are clobbered by design of the call.
	a.leaAbs(0x02D000, 0)
	a.leaAbs(0x02C000, 1)
	a.moveImmToDn(cd4SizeW, 63, 4)
	a.mark("cmpF")
	a.movePostIncToDn(cd4SizeB, 0, 5)
	a.movePostIncToDn(cd4SizeB, 1, 3)
	a.cmpbDn(3, 5)
	a.branch(cd4BNE, "failF9")
	a.dbra(4, "cmpF")
	a.leaAbs(0x02A000, 0) // BRMDEL
	a.moveImmToDn(cd4SizeW, 5, 0)
	a.jsrAbs(0x5F16)
	a.branch(cd4BCS, "failFA")
	a.leaAbs(0x02A000, 0) // BRMSERCH again: expect carry set
	a.moveImmToDn(cd4SizeW, 2, 0)
	a.jsrAbs(0x5F16)
	a.branch(cd4BCC, "failFB")
	a.bsetImm(5, 7)
	a.bra("stageFend")
	a.mark("failF1")
	detail(0xF1)
	a.bra("stageFend")
	a.mark("failF2")
	detail(0xF2)
	a.bra("stageFend")
	a.mark("failF3")
	detail(0xF3)
	a.bra("stageFend")
	a.mark("failF4")
	detail(0xF4)
	a.bra("stageFend")
	a.mark("failF5")
	detail(0xF5)
	a.bra("stageFend")
	a.mark("failF6")
	detail(0xF6)
	a.bra("stageFend")
	a.mark("failF7")
	detail(0xF7)
	a.bra("stageFend")
	a.mark("failF8")
	detail(0xF8)
	a.bra("stageFend")
	a.mark("failF9")
	detail(0xF9)
	a.bra("stageFend")
	a.mark("failFA")
	detail(0xFA)
	a.bra("stageFend")
	a.mark("failFB")
	detail(0xFB)
	a.mark("stageFend")
	storeBitmap()

	// Stage g: the SCD-3 ASIC identity trace, then DMNA.
	a.mark("stageG")
	a.leaAbs(0x200100, 0)
	a.moveImmToDn(cd4SizeW, 0x7F, 0)
	a.mark("fillG")
	a.moveDnToPreDec(cd4SizeB, 0, 0)
	a.dbra(0, "fillG")
	a.moveImmToAbs(cd4SizeW, 1, 0x201000)
	a.leaAbs(0x202000, 0)
	a.moveq(0, 0)
	a.moveq(0, 1)
	a.moveImmToDn(cd4SizeW, 15, 2)
	a.mark("tloopG")
	a.moveDnToPostInc(cd4SizeW, 0, 0)
	a.moveDnToPostInc(cd4SizeW, 1, 0)
	a.moveImmToPostInc(cd4SizeW, 0x0800, 0)
	a.moveDnToPostInc(cd4SizeW, 0, 0)
	a.addiW(8, 1)
	a.dbra(2, "tloopG")
	a.moveImmToAbs(cd4SizeW, 0, 0xFF8058)
	a.moveImmToAbs(cd4SizeW, 0x0400, 0xFF805A)
	a.moveImmToAbs(cd4SizeW, 1, 0xFF805C)
	a.moveImmToAbs(cd4SizeW, 0x1000, 0xFF805E)
	a.moveImmToAbs(cd4SizeW, 0, 0xFF8060)
	a.moveImmToAbs(cd4SizeW, 16, 0xFF8062)
	a.moveImmToAbs(cd4SizeW, 16, 0xFF8064)
	a.moveImmToAbs(cd4SizeW, 0x0800, 0xFF8066)
	a.mark("pollG")
	a.moveAbsToDn(cd4SizeB, 0xFF8058, 0)
	a.tstDn(cd4SizeB, 0)
	a.branch(cd4BNE, "pollG")
	a.leaAbs(0x204000, 0)
	a.moveq(0, 2)
	a.moveq(0, 3)
	a.moveImmToDn(cd4SizeW, 0x7F, 1)
	a.mark("csumG")
	a.movePostIncToDn(cd4SizeB, 0, 3)
	a.addDn(cd4SizeW, 3, 2)
	a.dbra(1, "csumG")
	a.cmpi(cd4SizeW, 0x1FC0, 2)
	a.branch(cd4BNE, "failG")
	a.bsetImm(6, 7)
	a.bra("stageGend")
	a.mark("failG")
	detail(0x60)
	a.mark("stageGend")
	storeBitmap()
	a.moveImmToAbs(cd4SizeB, 0x02, 0xFF8002) // DMNA: Word RAM to main

	// Stage h: publish results and idle.
	a.mark("stageH")
	a.moveDnToAbs(cd4SizeW, 7, 0xFF8022) // comm status 1 = bitmap
	a.moveImmToAbs(cd4SizeW, 0x600D, 0xFF8020)
	a.moveImmToAbs(cd4SizeB, 2, 0xFF800F) // sub flag 2
	a.mark("idle")
	a.bra("idle")

	// The stage d sine table: one 128 byte sign-magnitude sine
	// period, amplitude 120.
	a.mark("sinetab")
	tab := make([]byte, 128)
	for i := range tab {
		v := int(math.Round(120 * math.Sin(2*math.Pi*float64(i)/128)))
		if v >= 0 {
			tab[i] = 0x80 | byte(v)
		} else {
			tab[i] = byte(-v)
		}
	}
	a.emitBytes(tab)
	return a.finish()
}

// stageSCD4Tables writes the IP argument tables and BRM data into
// PRG-RAM before the first frame runs (the sub CPU is still held
// in reset, so nothing races it).
func stageSCD4Tables(sys *System) {
	prg := sys.CDUnit().PRGRAM()
	prg[0x5010] = 1                             // DRVINIT start track
	prg[0x5011] = 0xFF                          // DRVINIT end track
	binary.BigEndian.PutUint16(prg[0x5012:], 2) // MSCPLAY track
	binary.BigEndian.PutUint16(prg[0x5014:], 3) // CDBTOCREAD track 3
	binary.BigEndian.PutUint16(prg[0x5016:], 1) // CDBTOCREAD track 1
	binary.BigEndian.PutUint32(prg[0x5020:], 8) // ROMREADN sector
	binary.BigEndian.PutUint32(prg[0x5024:], 1) // ROMREADN count
	blk := prg[0xA000:]
	copy(blk[0:11], "ITAKTEST___")
	blk[11] = 0                                    // flag byte
	binary.BigEndian.PutUint16(blk[12:], 1)        // block size word
	binary.BigEndian.PutUint32(blk[16:], 0x02B000) // data address (extra field)
	binary.BigEndian.PutUint32(blk[20:], 64)       // byte count (extra field)
	for i := 0; i < 64; i++ {
		prg[0xB000+i] = byte(i*5 + 0x41) // BRM write source
		prg[0xD000+i] = byte(i*5 + 0x41) // pristine reference copy
	}
}

// ---------------------------------------------------------------------------
// The main CPU program, used both as a custom Genesis ROM and
// as the disc main program embedded in boot sector 0. It releases the sub
// CPU, waits for comm status 0 = $600D and for its RET flag, then
// programs the VDP (display on, plane A at $2000, H32, plane B at
// $E000, sprite table at $3000, scroll table at $4000), writes a
// 16 entry palette, copies the Word RAM image buffer at $204000
// into VRAM tiles 1 to 4 with a CPU loop through the data port,
// fills the plane A map with the blank tile 0 and overwrites a
// 16x16 cell region with tiles 1 to 4 cycling by column plus row,
// reads the tiles back through the VDP and stores the word
// checksum in comm command 1, sets the main flag, and idles.
// ---------------------------------------------------------------------------

// buildSCD4MainProgram assembles the main CPU program at the
// given base address: 0x200 for the companion cartridge variant
// and 0x200208 for the disc variant embedded in boot sector 0
// behind the ITAKMAIN marker. The program is position dependent
// only through its palette table label, which the assembler
// resolves from the base, so the same bytes logic runs from ROM
// or from Word RAM.
func buildSCD4MainProgram(base int) []byte {
	a := newCD4Asm(base)
	a.mark("start")
	a.moveImmToAbs(cd4SizeB, 1, 0xA12000) // RES = 1: release sub CPU
	a.mark("wait600d")
	a.moveAbsToDn(cd4SizeW, 0xA12020, 0)
	a.cmpi(cd4SizeW, 0x600D, 0)
	a.branch(cd4BNE, "wait600d")
	a.mark("waitret")
	a.moveAbsToDn(cd4SizeB, 0xA12002, 0)
	a.btstImm(2, 0) // RET flag: Word RAM owned by main
	a.branch(cd4BEQ, "waitret")

	regs := [][2]uint32{
		{1, 0x40},  // display on
		{2, 0x08},  // plane A name table at $2000
		{3, 0x00},  // window table at $0000 (window disabled by edges)
		{4, 0x07},  // plane B name table at $E000
		{5, 0x18},  // sprite table at $3000
		{7, 0x00},  // backdrop: palette 0 color 0
		{12, 0x00}, // H32 (256 px)
		{13, 0x10}, // horizontal scroll table at $4000
		{15, 0x02}, // auto-increment 2
		{16, 0x00}, // planes 32x32 cells
	}
	for _, r := range regs {
		a.moveImmToAbs(cd4SizeW, 0x8000|r[0]<<8|r[1], 0xC00004)
	}

	// Palette: CRAM write at address 0, 16 words from the table.
	a.moveImmToAbs(cd4SizeW, 0xC000, 0xC00004)
	a.moveImmToAbs(cd4SizeW, 0x0000, 0xC00004)
	a.leaLabel("cramtab", 0)
	a.moveImmToDn(cd4SizeW, 15, 1)
	a.mark("cramloop")
	a.movePostIncToDn(cd4SizeW, 0, 0)
	a.moveDnToAbs(cd4SizeW, 0, 0xC00000)
	a.dbra(1, "cramloop")

	// Tiles: VRAM write at $0020 (tile 1), 64 words copied from
	// the Word RAM image buffer through the main window.
	a.moveImmToAbs(cd4SizeW, 0x4020, 0xC00004)
	a.moveImmToAbs(cd4SizeW, 0x0000, 0xC00004)
	a.leaAbs(0x204000, 0)
	a.moveImmToDn(cd4SizeW, 63, 1)
	a.mark("copyloop")
	a.movePostIncToDn(cd4SizeW, 0, 0)
	a.moveDnToAbs(cd4SizeW, 0, 0xC00000)
	a.dbra(1, "copyloop")

	// Map: fill all 32x32 plane A entries with tile 0 (blank,
	// since tile 0 pattern bytes are never written), then write
	// the 16x16 pattern region, one VRAM address set per row.
	// The pattern region runs as nested loops rather than an
	// unrolled write per cell, so the same program also fits in
	// the boot sector user bytes for the disc variant: cell
	// (row, column) gets tile 1+((column+row)&3). D3 counts rows,
	// D4 holds the VRAM control low word for the current row
	// (0x6000 plus 0x40 per row, since plane A starts at 0x2000
	// and each row is 32 cells at 2 bytes), D5 counts columns,
	// and D6 holds the current tile. Writing 16 cells advances
	// D6 through a whole cycle of 4 and back to the row start
	// tile, so one extra advance before the next row gives that
	// row its start tile.
	a.moveImmToAbs(cd4SizeW, 0x6000, 0xC00004)
	a.moveImmToAbs(cd4SizeW, 0x0000, 0xC00004)
	a.moveImmToDn(cd4SizeW, 1023, 1)
	a.mark("fillloop")
	a.moveImmToAbs(cd4SizeW, 0, 0xC00000)
	a.dbra(1, "fillloop")
	a.moveImmToDn(cd4SizeW, 0x6000, 4)
	a.moveq(0, 3)
	a.moveImmToDn(cd4SizeW, 1, 6)
	a.mark("rowloop")
	a.moveDnToAbs(cd4SizeW, 4, 0xC00004)
	a.moveImmToAbs(cd4SizeW, 0, 0xC00004)
	a.moveq(0, 5)
	a.mark("colloop")
	a.moveDnToAbs(cd4SizeW, 6, 0xC00000)
	a.addq(cd4SizeW, 1, 6)
	a.cmpi(cd4SizeW, 5, 6)
	a.branch(cd4BNE, "colnowrap")
	a.moveImmToDn(cd4SizeW, 1, 6)
	a.mark("colnowrap")
	a.addq(cd4SizeL, 1, 5)
	a.cmpi(cd4SizeL, 16, 5)
	a.branch(cd4BLT, "colloop")
	a.addq(cd4SizeW, 1, 6)
	a.cmpi(cd4SizeW, 5, 6)
	a.branch(cd4BNE, "rownowrap")
	a.moveImmToDn(cd4SizeW, 1, 6)
	a.mark("rownowrap")
	a.addiW(0x40, 4)
	a.addq(cd4SizeL, 1, 3)
	a.cmpi(cd4SizeL, 16, 3)
	a.branch(cd4BLT, "rowloop")

	// Readback: VRAM read at $0020, sum 64 words, publish.
	a.moveImmToAbs(cd4SizeW, 0x0020, 0xC00004)
	a.moveImmToAbs(cd4SizeW, 0x0000, 0xC00004)
	a.moveq(0, 2)
	a.moveImmToDn(cd4SizeW, 63, 1)
	a.mark("rdloop")
	a.moveAbsToDn(cd4SizeW, 0xC00000, 0)
	a.addDn(cd4SizeW, 0, 2)
	a.dbra(1, "rdloop")
	a.moveDnToAbs(cd4SizeW, 2, 0xA12012) // comm command 1
	a.moveq(1, 0)                        // leave D0 odd (see header comment)
	a.moveImmToAbs(cd4SizeB, 1, 0xA1200E)
	a.mark("idle")
	a.bra("idle")

	// Palette words: index i maps to red = i&7, green = (i>>1)&7,
	// blue = (i>>2)&7 in the VDP 9 bit BGR word layout, which is
	// injective over 0..15, so all sixteen colors are distinct.
	a.mark("cramtab")
	for i := 0; i < 16; i++ {
		r := i & 7
		g := (i >> 1) & 7
		b := (i >> 2) & 7
		a.emit16(uint16(r<<1 | g<<5 | b<<9))
	}
	return a.finish()
}

// buildSCD4ROM builds the companion cartridge variant of the
// main program: a Genesis ROM whose code at 0x200 is the main
// program assembled at base 0x200. The cartridge ignores the
// boot sector main program copy in Word RAM.
func buildSCD4ROM() []byte {
	rom := make([]byte, 0x10000)
	binary.BigEndian.PutUint32(rom[0:], 0x00FFFE00)
	binary.BigEndian.PutUint32(rom[4:], 0x00000200)
	copy(rom[0x100:], "SEGA GENESIS    ")
	copy(rom[0x200:], buildSCD4MainProgram(0x200))
	return rom
}

// goertzelPower returns the single-bin DFT power of samples at
// freq Hz over a rate Hz stream.
func goertzelPower(samples []int16, freq, rate float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	k := 2 * math.Pi * freq / rate
	coeff := 2 * math.Cos(k)
	var s1, s2 float64
	for _, v := range samples {
		s0 := float64(v) + coeff*s1 - s2
		s2 = s1
		s1 = s0
	}
	return s1*s1 + s2*s2 - coeff*s1*s2
}

func fbBytes(fb []uint16) []byte {
	out := make([]byte, 2*len(fb))
	for i, v := range fb {
		binary.LittleEndian.PutUint16(out[2*i:], v)
	}
	return out
}

func TestCDBIOSFullStack(t *testing.T) {
	cue, info := makeSCDBIOSCue(t)

	// Fixture sanity: the disc TOC matches the computed layout.
	disc, err := scd.OpenDisc(cue)
	if err != nil {
		t.Fatalf("fixture: OpenDisc: %v", err)
	}
	tracks := disc.Tracks()
	disc.Close()
	if len(tracks) != 3 {
		t.Fatalf("fixture: %d tracks, want 3", len(tracks))
	}
	if tracks[0].StartLBA != 0 || tracks[0].Sectors != info.track1Sectors {
		t.Fatalf("fixture: track 1 at LBA %d with %d sectors, want LBA 0 with %d", tracks[0].StartLBA, tracks[0].Sectors, info.track1Sectors)
	}
	if tracks[1].StartLBA != info.track2Start || tracks[1].Sectors != 150 {
		t.Fatalf("fixture: track 2 at LBA %d with %d sectors, want LBA %d with 150", tracks[1].StartLBA, tracks[1].Sectors, info.track2Start)
	}
	if tracks[2].StartLBA != info.track3Start || tracks[2].Sectors != 150 {
		t.Fatalf("fixture: track 3 at LBA %d with %d sectors, want LBA %d with 150", tracks[2].StartLBA, tracks[2].Sectors, info.track3Start)
	}

	rom := buildSCD4ROM()
	sys := New()
	if err := sys.LoadGame(rom); err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	if err := sys.LoadDisc(cue); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	if got, want := sys.SerializeSize(), 1070469; got != want {
		t.Fatalf("CD mode SerializeSize = %d, want %d (SCD-4 size with BRAM and BIOS sections)", got, want)
	}
	stageSCD4Tables(sys)
	u := sys.CDUnit()

	// Run until the IP publishes sub flag 2, collecting audio
	// per frame for the tone analysis.
	framesAudio := make([][]int16, 0, 256)
	done := false
	framesRun := 0
	for i := 0; i < 1200; i++ {
		sys.RunFrame()
		framesRun++
		framesAudio = append(framesAudio, sys.AudioBuffer())
		if u.SubFlag() == 2 {
			done = true
			break
		}
	}
	bitmapCell := binary.BigEndian.Uint16(u.PRGRAM()[0x5000:0x5002])
	detail := binary.BigEndian.Uint16(u.PRGRAM()[0x5002:0x5004])
	if !done {
		t.Fatalf("stage h: sub flag = %#02x after %d frames, want 2 (IP never finished); stage bitmap cell = %#04x, detail code = %#04x, comm status 0 = %#06x",
			u.SubFlag(), framesRun, bitmapCell, detail, u.CommStatus(0))
	}
	t.Logf("IP finished in %d frames", framesRun)

	// Stage bitmap: bits a to g all set, in comm status 1 and in
	// the PRG-RAM results cell.
	if got := u.CommStatus(1); got != 0x7F {
		t.Fatalf("stage bitmap: comm status 1 = %#04x, want 0x7f (stages a to g all passed); PRG detail code = %#04x", got, detail)
	}
	if bitmapCell != 0x7F {
		t.Fatalf("stage bitmap: PRG results cell = %#04x, want 0x7f; PRG detail code = %#04x", bitmapCell, detail)
	}

	// Main side: the VRAM readback checksum in comm command 1
	// must equal the word sum of the image buffer the ASIC
	// traced into Word RAM.
	wr := u.WordRAM()[0x4000:0x4080]
	var wantVRAMSum uint16
	for i := 0; i < 64; i++ {
		wantVRAMSum += uint16(wr[2*i])<<8 | uint16(wr[2*i+1])
	}
	if got := u.CommCommand(1); got != wantVRAMSum {
		t.Fatalf("main VDP: comm command 1 = %#06x, want %#06x (VRAM tile readback checksum)", got, wantVRAMSum)
	}
	if got := u.MainFlag(); got != 1 {
		t.Fatalf("main VDP: main flag = %#02x, want 1 (main program finished)", got)
	}

	// Let two full frames render with the final VDP state: the
	// main CPU finished its VDP work partway through the frame
	// the IP completed in, so the pattern rows of that frame
	// were still rendered as backdrop.
	for i := 0; i < 2; i++ {
		sys.RunFrame()
		_ = sys.AudioBuffer()
	}

	// Framebuffer: the 16x16 cell pattern region shows the image
	// buffer tiles. Every image byte contributes its two nibbles
	// as pixels; 233 of the 256 nibbles in bytes 0..127 are
	// nonzero and each tile is shown 64 times across the region,
	// so thousands of pixels must differ from the backdrop.
	fb := sys.Framebuffer()
	nonblank := 0
	for y := 0; y < sys.Height(); y++ {
		for x := 0; x < sys.Width(); x++ {
			if fb[y*320+x] != 0 {
				nonblank++
			}
		}
	}
	t.Logf("framebuffer non-background pixels: %d", nonblank)
	if nonblank < 2000 {
		t.Fatalf("framebuffer: %d non-background pixels, want at least 2000 from the 16x16 tile pattern", nonblank)
	}

	// Audio tones: analyze the last up to 45 frames of audio
	// from the initial run (both MSCPLAY CD-DA at 440 Hz and the
	// PCM tone at about 508.6 Hz are running by then).
	start := 0
	if len(framesAudio) > 45 {
		start = len(framesAudio) - 45
	}
	var left []int16
	for _, fr := range framesAudio[start:] {
		for i := 0; i+1 < len(fr); i += 2 {
			left = append(left, fr[i])
		}
	}
	const pcmHz = 12500000.0 / 384 * 2 / 128 // FD $1000, 128 sample period
	p440 := goertzelPower(left, 440, 44100)
	pPCM := goertzelPower(left, pcmHz, 44100)
	pCtl := goertzelPower(left, 1500, 44100)
	t.Logf("tone powers over %d samples: 440 Hz = %.4g, PCM %.1f Hz = %.4g, control 1500 Hz = %.4g", len(left), p440, pcmHz, pPCM, pCtl)
	if !(p440 > 4*pCtl) {
		t.Fatalf("audio stage c: 440 Hz CD-DA power %.4g does not exceed 4x the 1500 Hz control power %.4g", p440, pCtl)
	}
	if !(pPCM > 4*pCtl) {
		t.Fatalf("audio stage d: PCM %.1f Hz power %.4g does not exceed 4x the 1500 Hz control power %.4g", pcmHz, pPCM, pCtl)
	}

	// Determinism leg at a mid-playback frame. The playback
	// still running at this point is the stage d PCM tone: the
	// stage e read flow ends its bounded read with a CDD stop
	// (CDCACK), so the CDD itself is stopped by design here
	// (position LBA 9, one sector past the payload). The PCM
	// channel is never stopped, so the save is taken while it
	// is sounding and the continuation audio must carry it.
	t.Logf("determinism save point: CDD state %d at LBA %d (stopped after the stage e read), PCM tone running", u.CDDState(), u.CDDPositionLBA())
	sMid := make([]byte, sys.SerializeSize())
	if err := sys.Serialize(sMid); err != nil {
		t.Fatalf("determinism: Serialize: %v", err)
	}
	var audioA []int16
	for i := 0; i < 45; i++ {
		sys.RunFrame()
		audioA = append(audioA, sys.AudioBuffer()...)
	}
	// The unrestored continuation audio must carry the PCM
	// tone, proving the save point was mid-playback.
	var leftA []int16
	for i := 0; i+1 < len(audioA); i += 2 {
		leftA = append(leftA, audioA[i])
	}
	pPCMA := goertzelPower(leftA, pcmHz, 44100)
	pCtlA := goertzelPower(leftA, 1500, 44100)
	if !(pPCMA > 4*pCtlA) {
		t.Fatalf("determinism: PCM tone power %.4g in the continuation audio does not exceed 4x the control %.4g; the save point was not mid-playback", pPCMA, pCtlA)
	}
	sAEnd := make([]byte, sys.SerializeSize())
	if err := sys.Serialize(sAEnd); err != nil {
		t.Fatalf("determinism: Serialize A end: %v", err)
	}
	fbA := fbBytes(sys.Framebuffer())

	b := New()
	if err := b.LoadGame(rom); err != nil {
		t.Fatalf("determinism: LoadGame B: %v", err)
	}
	if err := b.LoadDisc(cue); err != nil {
		t.Fatalf("determinism: LoadDisc B: %v", err)
	}
	if err := b.Deserialize(sMid); err != nil {
		t.Fatalf("determinism: Deserialize B: %v", err)
	}
	var audioB []int16
	for i := 0; i < 45; i++ {
		b.RunFrame()
		audioB = append(audioB, b.AudioBuffer()...)
	}
	sBEnd := make([]byte, b.SerializeSize())
	if err := b.Serialize(sBEnd); err != nil {
		t.Fatalf("determinism: Serialize B end: %v", err)
	}
	if !bytes.Equal(sAEnd, sBEnd) {
		t.Fatal("determinism: state after restore plus 45 frames differs from the unrestored run")
	}
	if !bytes.Equal(int16sToBytes(audioA), int16sToBytes(audioB)) {
		t.Fatal("determinism: audio after restore plus 45 frames differs from the unrestored run")
	}
	if !bytes.Equal(fbA, fbBytes(b.Framebuffer())) {
		t.Fatal("determinism: framebuffer after restore plus 45 frames differs from the unrestored run")
	}
	t.Logf("determinism: 45 frames replayed byte-identically, %d audio samples compared", len(audioA))

	// Legacy leg: build a pre-SCD-4 CD state from the current
	// one. Serialize's layout puts the CD unit block before an
	// 8 byte sub CPU cycle trailer, so the unit growth is not at
	// the very end of the state: the legacy state is the current
	// state with the last scd.BRAMSize + scd.BIOSStateSize bytes
	// of the unit block removed, keeping the trailer.
	growth := scd.BRAMSize + scd.BIOSStateSize
	if growth != 8960 {
		t.Fatalf("legacy: BRAMSize + BIOSStateSize = %d, want 8960", growth)
	}
	legacy := make([]byte, 0, len(sMid)-growth)
	legacy = append(legacy, sMid[:len(sMid)-8-growth]...)
	legacy = append(legacy, sMid[len(sMid)-8:]...)
	if len(legacy) != 1061509 {
		t.Fatalf("legacy: constructed legacy state is %d bytes, want 1061509", len(legacy))
	}
	c := New()
	if err := c.LoadGame(rom); err != nil {
		t.Fatalf("legacy: LoadGame C: %v", err)
	}
	if err := c.LoadDisc(cue); err != nil {
		t.Fatalf("legacy: LoadDisc C: %v", err)
	}
	if err := c.Deserialize(legacy); err != nil {
		t.Fatalf("legacy: Deserialize of the legacy-sized CD state failed: %v", err)
	}
	for i := 0; i < 5; i++ {
		c.RunFrame()
	}
	if got := c.CDUnit().SubFlag(); got != 2 {
		t.Fatalf("legacy: sub flag = %#02x after 5 frames on the restored legacy state, want 2 (machine did not keep running)", got)
	}
	// A buffer one byte short of the legacy length must keep
	// failing: only the exact legacy length is expanded.
	legacyBad := legacy[:len(legacy)-1]
	d := New()
	if err := d.LoadGame(rom); err != nil {
		t.Fatalf("legacy negative: LoadGame D: %v", err)
	}
	if err := d.LoadDisc(cue); err != nil {
		t.Fatalf("legacy negative: LoadDisc D: %v", err)
	}
	if err := d.Deserialize(legacyBad); err == nil {
		t.Fatal("legacy negative: Deserialize accepted a truncated legacy-sized state, want an error")
	}
	t.Logf("legacy: %d byte legacy state accepted and ran; truncated variant rejected", len(legacy))
}

// TestCDBIOSDiscOnlyBoot is the disc only variant of the SCD-4
// gate: no cartridge is loaded, so the system synthesizes
// MainStubROM itself. The boot stub copies boot sector 0 into
// Word RAM, the main stub finds the ITAKMAIN marker at 0x200200
// and jumps to the embedded main program at 0x200208, and that
// program programs the VDP exactly as the companion cartridge
// variant does.
func TestCDBIOSDiscOnlyBoot(t *testing.T) {
	cue, _ := makeSCDBIOSCue(t)

	sys := New()
	if err := sys.LoadDisc(cue); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	stageSCD4Tables(sys)
	u := sys.CDUnit()

	done := false
	framesRun := 0
	for i := 0; i < 1200; i++ {
		sys.RunFrame()
		framesRun++
		_ = sys.AudioBuffer()
		if u.SubFlag() == 2 {
			done = true
			break
		}
	}
	detail := binary.BigEndian.Uint16(u.PRGRAM()[0x5002:0x5004])
	if !done {
		t.Fatalf("disc only: sub flag = %#02x after %d frames, want 2 (IP never finished); PRG detail code = %#04x, comm status 0 = %#06x",
			u.SubFlag(), framesRun, detail, u.CommStatus(0))
	}
	t.Logf("disc only: IP finished in %d frames", framesRun)

	if got := u.CommStatus(1); got != 0x7F {
		t.Fatalf("disc only: comm status 1 = %#04x, want 0x7f (stages a to g all passed); PRG detail code = %#04x", got, detail)
	}
	if got := u.CommCommand(1); got != 0xD000 {
		t.Fatalf("disc only: comm command 1 = %#06x, want 0xd000 (VRAM tile readback checksum)", got)
	}
	if got := u.MainFlag(); got != 1 {
		t.Fatalf("disc only: main flag = %#02x, want 1 (main program finished)", got)
	}

	// Let two full frames render with the final VDP state, as in
	// the companion cartridge gate.
	for i := 0; i < 2; i++ {
		sys.RunFrame()
		_ = sys.AudioBuffer()
	}
	fb := sys.Framebuffer()
	nonblank := 0
	for y := 0; y < sys.Height(); y++ {
		for x := 0; x < sys.Width(); x++ {
			if fb[y*320+x] != 0 {
				nonblank++
			}
		}
	}
	t.Logf("disc only: framebuffer non-background pixels: %d", nonblank)
	if nonblank < 2000 {
		t.Fatalf("disc only: framebuffer: %d non-background pixels, want at least 2000 from the 16x16 tile pattern", nonblank)
	}

	// Stability: 30 more frames must leave the framebuffer
	// byte identical.
	before := fbBytes(sys.Framebuffer())
	for i := 0; i < 30; i++ {
		sys.RunFrame()
		_ = sys.AudioBuffer()
	}
	if !bytes.Equal(before, fbBytes(sys.Framebuffer())) {
		t.Fatal("disc only: framebuffer changed over 30 extra frames, want byte identical stability")
	}
	if got := u.SubFlag(); got != 2 {
		t.Fatalf("disc only: sub flag = %#02x after 30 extra frames, want 2 (machine did not stay stable)", got)
	}
	t.Logf("disc only: framebuffer stable over 30 extra frames, %d non-background pixels", nonblank)
}
