package system

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// SCD-2 system gates: main CPU CD windows through the real bus, and
// the save/restore audio determinism gate.
// ---------------------------------------------------------------------------

// TestCDWindowsRequireAttachment proves the CD windows in cartridge
// space (0x200000 Word RAM, 0x420000 PRG-RAM bank, 0x600000 Word RAM)
// stay inert when no CD is attached, so cartridge behavior is
// untouched.
func TestCDWindowsRequireAttachment(t *testing.T) {
	sys := New()
	if err := sys.LoadGame(ymTimerROM(0x05)); err != nil {
		t.Fatalf("LoadGame: %v", err)
	}
	if got := sys.bus.Read8(0x420000); got != 0xFF {
		t.Fatalf("bus read 0x420000 without CD = %#04x, want 0xFF (open bus)", got)
	}
	if got := sys.bus.Read8(0x600000); got != 0xFF {
		t.Fatalf("bus read 0x600000 without CD = %#04x, want 0xFF (open bus)", got)
	}
	// 0x200000 without a CD is cartridge space: the timer ROM is
	// small, so this reads the ROM mirror/open value, never a CD
	// window. The ROM's own read path must not be disturbed; just
	// assert the machine still runs its program.
	for i := 0; i < 3; i++ {
		sys.RunFrame()
	}
	if got := binary.BigEndian.Uint32(sys.SystemRAM()[0:4]); got != ymTimerMagic {
		t.Fatalf("RAM magic = %#08x, want %#08x", got, ymTimerMagic)
	}
}

// TestCDMainWindowsAndBanking drives the SCD-2 main CPU windows
// through the real Genesis bus: Word RAM at 0x200000 and its 2M
// alias at 0x600000, and the banked, write-protected PRG-RAM window
// at 0x420000.
func TestCDMainWindowsAndBanking(t *testing.T) {
	sys := New()
	if err := sys.LoadDisc(makeSCDISO(t, cdTestIP)); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	u := sys.CDUnit()

	// Word RAM starts owned by the sub CPU in 2M mode: the main
	// window reads open bus (0xFF) and writes are dropped.
	if got := sys.bus.Read8(0x201000); got != 0xFF {
		t.Fatalf("main Word RAM read while sub owns = %#04x, want 0xFF", got)
	}
	sys.bus.Write8(0x201000, 0x77)
	if got := u.WordRAM()[0x1000]; got != 0x00 {
		t.Fatalf("main Word RAM write while sub owns landed: %#04x", got)
	}

	// The sub CPU hands Word RAM to the main CPU (DMNA).
	u.Write8(0xFF8002, 0x02)
	sys.bus.Write8(0x201000, 0x77)
	if got := u.WordRAM()[0x1000]; got != 0x77 {
		t.Fatalf("WordRAM[0x1000] = %#04x, want 0x77 written through the main window", got)
	}
	if got := sys.bus.Read8(0x201000); got != 0x77 {
		t.Fatalf("main Word RAM readback = %#04x, want 0x77", got)
	}
	// The 2M alias at 0x600000 shows the same byte.
	if got := sys.bus.Read8(0x601000); got != 0x77 {
		t.Fatalf("2M alias read = %#04x, want 0x77", got)
	}
	sys.bus.Write8(0x601001, 0x88)
	if got := u.WordRAM()[0x1001]; got != 0x88 {
		t.Fatalf("WordRAM[0x1001] = %#04x, want 0x88 written through the 2M alias", got)
	}

	// PRG-RAM bank window at 0x420000: default bank 0.
	sys.bus.Write8(0x420100, 0xB0)
	if got := u.PRGRAM()[0x100]; got != 0xB0 {
		t.Fatalf("PRGRAM[0x100] = %#04x, want 0xB0 (bank 0 window write)", got)
	}
	// Select bank 2 (memory mode bits 7:6 = 2) and write a
	// different byte at the same window offset.
	sys.bus.Write8(0xA12002, 0x80)
	sys.bus.Write8(0x420100, 0xB2)
	if got := u.PRGRAM()[0x40100]; got != 0xB2 {
		t.Fatalf("PRGRAM[0x40100] = %#04x, want 0xB2 (bank 2 window write)", got)
	}
	if got := u.PRGRAM()[0x100]; got != 0xB0 {
		t.Fatalf("PRGRAM[0x100] = %#04x, want 0xB0 undisturbed by the bank 2 write", got)
	}
	// Back to bank 0: the window shows the bank 0 byte again.
	sys.bus.Write8(0xA12002, 0x00)
	if got := sys.bus.Read8(0x420100); got != 0xB0 {
		t.Fatalf("bank 0 window readback = %#04x, want 0xB0", got)
	}

	// Write protection: WP field 2 (bits 4:2 of the memory mode
	// byte as written by the main CPU) protects the first 128 KB
	// of physical PRG-RAM against main window writes.
	sys.bus.Write8(0xA12002, 0x08) // bank 0, WP = 2
	sys.bus.Write8(0x420100, 0xEE)
	if got := u.PRGRAM()[0x100]; got != 0xB0 {
		t.Fatalf("protected write landed: PRGRAM[0x100] = %#04x, want 0xB0", got)
	}
	// Bank 1 offset 0x100 is physical 0x20100, outside the
	// protected region: the write lands.
	sys.bus.Write8(0xA12002, 0x48) // bank 1, WP = 2
	sys.bus.Write8(0x420100, 0xB1)
	if got := u.PRGRAM()[0x20100]; got != 0xB1 {
		t.Fatalf("PRGRAM[0x20100] = %#04x, want 0xB1 (unprotected bank 1 write)", got)
	}
	// Clearing WP lets the bank 0 write through.
	sys.bus.Write8(0xA12002, 0x00)
	sys.bus.Write8(0x420100, 0xEE)
	if got := u.PRGRAM()[0x100]; got != 0xEE {
		t.Fatalf("PRGRAM[0x100] = %#04x, want 0xEE after WP cleared", got)
	}
}

// cdAudioIP is the SCD-2 determinism gate's initial program. It
// fills PCM wave RAM bank 0 with one square period (128 bytes of
// +126, 128 bytes of -126, then 32 loop markers), programs PCM
// channel 0 (ENV and PAN full, FD = 0x0800, start 0), turns the
// chip and the channel on, issues a CDD PLAY for the audio track
// starting at LBA 2 (MSF 00:02:02) through the TC nibble registers
// with a valid checksum, publishes the boot magic 0x600D as comm
// status 0, and idles. Encodings follow the absolute long forms
// used by the other hand-assembled test programs in this repo.
var cdAudioIP = []byte{
	0x41, 0xF9, 0x00, 0xFF, 0x10, 0x00, // lea $FF1000, a0 (wave window)
	0x70, 0xFE, // moveq #-2, d0 (byte 0xFE = +126 sign-magnitude)
	0x32, 0x3C, 0x00, 0x7F, // move.w #127, d1
	0x10, 0xC0, // fillA: move.b d0, (a0)+
	0x51, 0xC9, 0xFF, 0xFC, // dbra d1, fillA
	0x70, 0x7E, // moveq #126, d0 (byte 0x7E = -126)
	0x32, 0x3C, 0x00, 0x7F, // move.w #127, d1
	0x10, 0xC0, // fillB: move.b d0, (a0)+
	0x51, 0xC9, 0xFF, 0xFC, // dbra d1, fillB
	0x70, 0xFF, // moveq #-1, d0 (0xFF loop markers)
	0x32, 0x3C, 0x00, 0x1F, // move.w #31, d1
	0x10, 0xC0, // fillC: move.b d0, (a0)+
	0x51, 0xC9, 0xFF, 0xFC, // dbra d1, fillC
	// PCM channel 0 setup. CTRL first: 0xC0 = chip on, MOD = 1,
	// channel 0 selected.
	0x13, 0xFC, 0x00, 0xC0, 0x00, 0xFF, 0x00, 0x07, // move.b #$C0, $FF0007
	0x13, 0xFC, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0x00, // ENV = 0xFF
	0x13, 0xFC, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0x01, // PAN = 0xFF
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x00, 0x02, // FD.L = 0x00
	0x13, 0xFC, 0x00, 0x08, 0x00, 0xFF, 0x00, 0x03, // FD.H = 0x08 (FD = 0x0800)
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x00, 0x06, // ST = 0 (retrigger)
	0x13, 0xFC, 0x00, 0x01, 0x00, 0xFF, 0x00, 0x08, // ON/OFF: channel 0 on
	// CDD PLAY at MSF 00:02:02: TC nibbles 0,3, 0,0, 0,2, 0,2, 0,
	// checksum 8 (nibbles sum to 7, checksum is the complement).
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x42, // TC0 = 0
	0x13, 0xFC, 0x00, 0x03, 0x00, 0xFF, 0x80, 0x43, // TC1 = 3 (PLAY)
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x44, // TC2 = 0
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x45, // TC3 = 0
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x46, // TC4 = 0
	0x13, 0xFC, 0x00, 0x02, 0x00, 0xFF, 0x80, 0x47, // TC5 = 2
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x48, // TC6 = 0
	0x13, 0xFC, 0x00, 0x02, 0x00, 0xFF, 0x80, 0x49, // TC7 = 2
	0x13, 0xFC, 0x00, 0x00, 0x00, 0xFF, 0x80, 0x4A, // TC8 = 0
	0x13, 0xFC, 0x00, 0x08, 0x00, 0xFF, 0x80, 0x4B, // TC9 = 8 (checksum)
	0x33, 0xFC, 0x60, 0x0D, 0x00, 0xFF, 0x80, 0x20, // comm status 0 = 0x600D
	0x60, 0xFE, // idle: bra idle
}

// makeCDAudioCue builds a three track cue/bin fixture: track 1 is
// two MODE1/2352 data sectors (boot header + the IP), track 2 is
// 150 audio sectors of a 440 Hz sine, track 3 is 150 audio sectors
// of an 880 Hz sine. It returns the cue path.
func makeCDAudioCue(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := make([]byte, 0, 302*2352)

	// Track 1, sector 0: raw MODE1/2352 sector whose user area
	// (bytes 16..2064) is the boot header.
	sec0 := make([]byte, 2352)
	user := sec0[16:2064]
	copy(user, "SEGADISCSYSTEM")
	binary.BigEndian.PutUint32(user[0x30:], 1)
	binary.BigEndian.PutUint32(user[0x34:], uint32(len(cdAudioIP)))
	bin = append(bin, sec0...)
	// Track 1, sector 1: the IP in the user area.
	sec1 := make([]byte, 2352)
	copy(sec1[16:2064], cdAudioIP)
	bin = append(bin, sec1...)

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

	binPath := filepath.Join(dir, "audio.bin")
	if err := os.WriteFile(binPath, bin, 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	cue := "FILE \"audio.bin\" BINARY\n" +
		"  TRACK 01 MODE1/2352\n" +
		"    INDEX 01 00:02:00\n" +
		"  TRACK 02 AUDIO\n" +
		"    INDEX 01 00:02:02\n" +
		"  TRACK 03 AUDIO\n" +
		"    INDEX 01 00:04:02\n"
	cuePath := filepath.Join(dir, "audio.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatalf("write cue: %v", err)
	}
	return cuePath
}

// TestCDAudioDeterminism is the SCD-2 determinism gate: the IP
// starts a PCM channel and a CD-DA track, the machine is saved
// mid-playback, and a restored machine must continue with
// byte-identical state and sample-identical audio.
func TestCDAudioDeterminism(t *testing.T) {
	cue := makeCDAudioCue(t)

	a := New()
	if err := a.LoadDisc(cue); err != nil {
		t.Fatalf("LoadDisc A: %v", err)
	}
	// Boot and let the IP set up PCM + CD-DA, then play for a
	// while before saving.
	for i := 0; i < 40; i++ {
		a.RunFrame()
	}
	_ = a.AudioBuffer() // drain pre-save audio
	u := a.CDUnit()
	if !u.CDDPlaying() {
		t.Fatalf("CDD is not playing after IP PLAY command (state %d)", u.CDDState())
	}
	posAtSave := u.CDDPositionLBA()
	if posAtSave <= 2 {
		t.Fatalf("CDD position LBA = %d, want past the track 2 start (2)", posAtSave)
	}
	sA := make([]byte, a.SerializeSize())
	if err := a.Serialize(sA); err != nil {
		t.Fatalf("Serialize A: %v", err)
	}
	for i := 0; i < 30; i++ {
		a.RunFrame()
	}
	audioA := a.AudioBuffer()
	sA2 := make([]byte, a.SerializeSize())
	if err := a.Serialize(sA2); err != nil {
		t.Fatalf("Serialize A2: %v", err)
	}

	// The mixed audio must actually contain signal (CD-DA sine
	// plus the PCM square), not silence.
	nonzero := 0
	for _, s := range audioA {
		if s != 0 {
			nonzero++
		}
	}
	if nonzero < len(audioA)/4 {
		t.Fatalf("only %d of %d continuation samples are nonzero, want real CD audio in the mix", nonzero, len(audioA))
	}

	b := New()
	if err := b.LoadDisc(cue); err != nil {
		t.Fatalf("LoadDisc B: %v", err)
	}
	if err := b.Deserialize(sA); err != nil {
		t.Fatalf("Deserialize B: %v", err)
	}
	for i := 0; i < 30; i++ {
		b.RunFrame()
	}
	audioB := b.AudioBuffer()
	sB2 := make([]byte, b.SerializeSize())
	if err := b.Serialize(sB2); err != nil {
		t.Fatalf("Serialize B2: %v", err)
	}
	if !bytes.Equal(sA2, sB2) {
		t.Fatal("state after restore plus 30 frames differs from the unrestored run")
	}
	if !bytes.Equal(int16sToBytes(audioA), int16sToBytes(audioB)) {
		t.Fatal("continuation audio after restore differs from the unrestored run")
	}
	t.Logf("continuation audio: %d samples compared byte for byte, CDD position at save = LBA %d", len(audioA), posAtSave)
}

func int16sToBytes(in []int16) []byte {
	out := make([]byte, 2*len(in))
	for i, v := range in {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}
