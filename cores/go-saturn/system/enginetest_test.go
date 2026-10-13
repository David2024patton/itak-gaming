package system

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/enginetest"
)

// The SAT-4 full stack gate. A synthetic disc whose IP is
// hand assembled SH-2 boots ONLY through BootDisc and
// drives every device through its documented guest
// interface: SMPC SSHON slave release, CD block file
// reads through the data port, VDP2 and VDP1 programming,
// a 68EC000 sound driver uploaded through sound RAM and
// released with SNDON, an SMPC INTBACK pad read, and a
// backup RAM round trip through the odd byte path. A mid
// run save taken just after the sound starts restores to
// a byte identical continuation.

// mileWord reads one word of the milestone block.
func mileWord(s *System, off int) uint32 {
	return s.Bus.Read32(enginetest.MilestoneAddress + uint32(off))
}

func mileCount(s *System) int {
	return int(mileWord(s, enginetest.MileCount))
}

func TestEnginetestDiscFullStack(t *testing.T) {
	iso := enginetest.BuildDisc()

	// The cd layer agrees with the fixture: the payload
	// is in the root and reads back byte exact.
	img, err := cd.OpenBytes(iso, 2048)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	entries, err := img.ListRoot()
	if err != nil {
		t.Fatalf("ListRoot: %v", err)
	}
	var payloadEntry *cd.FileEntry
	for i := range entries {
		if entries[i].Name == "PAYLOAD.BIN" {
			payloadEntry = &entries[i]
		}
	}
	if payloadEntry == nil {
		t.Fatalf("PAYLOAD.BIN not in the root listing: %v", entries)
	}
	payload, err := img.ReadFile(*payloadEntry)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(payload, enginetest.Payload()) {
		t.Fatal("payload through the cd layer differs from the fixture pattern")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "enginetest.iso")
	if err := os.WriteFile(path, iso, 0o644); err != nil {
		t.Fatal(err)
	}
	// Keep a copy for the desktop .so harness run.
	buildsDir := filepath.Join("..", "builds")
	if err := os.MkdirAll(buildsDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(buildsDir, "enginetest.iso"), iso, 0o644)
	}

	s := New()
	s.SetButtons(enginetest.PadMask)
	if err := s.LoadDisc(path); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	if err := s.BootDisc(); err != nil {
		t.Fatalf("BootDisc: %v", err)
	}
	// Documented post boot state.
	if got := s.Master.PC; got != IPEntryAddress {
		t.Fatalf("master PC = %#x, want %#x", got, uint32(IPEntryAddress))
	}
	if got := s.Master.Reg[15]; got != 0x06001000 {
		t.Fatalf("master SP = %#x, want the header Stack-M 0x06001000", got)
	}
	if got := s.Slave.Reg[15]; got != 0x06000800 {
		t.Fatalf("slave SP = %#x, want the header Stack-S 0x06000800", got)
	}
	if s.SlaveReleased() {
		t.Fatal("slave released at boot; the IP must release it with SSHON")
	}

	// Run until every milestone lands, saving mid run
	// just after the sound starts (milestone 4).
	var snap []byte
	snapFrame := 0
	frames := 0
	for frames < 300 {
		s.RunFrame()
		frames++
		if snap == nil && mileCount(s) >= 4 {
			s.AudioBuffer() // drain: pending output is not part of a state
			snap = s.Serialize()
			snapFrame = frames
			if len(snap) != s.StateSize() {
				t.Fatalf("state size = %d, want StateSize %d", len(snap), s.StateSize())
			}
		}
		if mileCount(s) >= 7 {
			break
		}
	}
	if got := mileWord(s, enginetest.MileError); got != 0 {
		t.Fatalf("IP failure stage = %d after %d frames", got, frames)
	}
	if got := mileCount(s); got != 7 {
		t.Fatalf("milestones = %d after %d frames, want 7 (order log % x)",
			got, frames, s.Bus.WorkRAMHigh()[0x4008:0x4008+7])
	}
	if got := mileWord(s, enginetest.MileMagic); got != enginetest.MileMagicValue {
		t.Fatalf("milestone magic = %#x, want %#x", got, uint32(enginetest.MileMagicValue))
	}
	order := s.Bus.WorkRAMHigh()[0x4000+enginetest.MileOrder : 0x4000+enginetest.MileOrder+7]
	for i, b := range order {
		if b != byte(i+1) {
			t.Fatalf("milestone order = % x, want 1..7 in order", order)
		}
	}
	if got := mileWord(s, enginetest.MileMaster); got != 42 {
		t.Fatalf("master handshake cell = %d, want 42", got)
	}
	if got := mileWord(s, enginetest.MileSlave); got != 43 {
		t.Fatalf("slave handshake cell = %d, want 43", got)
	}
	alive1 := mileWord(s, enginetest.MileAlive)
	s.RunFrame()
	if alive2 := mileWord(s, enginetest.MileAlive); alive2 <= alive1 {
		t.Fatalf("slave alive counter did not advance: %d then %d", alive1, alive2)
	}

	// CD payload: the guest checksum and size, and the
	// bytes themselves in Work RAM-L.
	if got := mileWord(s, enginetest.MileChecksum); got != enginetest.PayloadChecksum() {
		t.Fatalf("guest payload checksum = %#x, want %#x", got, enginetest.PayloadChecksum())
	}
	if got := mileWord(s, enginetest.MileSize); got != enginetest.PayloadSize {
		t.Fatalf("guest payload size = %d, want %d", got, enginetest.PayloadSize)
	}
	if got := s.Bus.WorkRAMLow()[:enginetest.PayloadSize]; !bytes.Equal(got, enginetest.Payload()) {
		t.Fatal("payload bytes in Work RAM-L differ from the fixture")
	}

	// Pad report bytes for the mask set before the run.
	pad := s.Bus.WorkRAMHigh()[0x4000+enginetest.MilePad : 0x4000+enginetest.MilePad+4]
	if !bytes.Equal(pad, enginetest.ExpectedPad[:]) {
		t.Fatalf("INTBACK report = % x, want % x", pad, enginetest.ExpectedPad)
	}

	// Backup RAM: the result byte in the block, and the
	// logical store itself holds the pattern.
	if got := s.Bus.WorkRAMHigh()[0x4000+enginetest.MileBackup]; got != 0xA5 {
		t.Fatalf("backup result byte = %#x, want 0xA5", got)
	}
	for i := 0; i < 32; i++ {
		if got := s.Bus.BackupRAM()[i]; got != byte(0x41+i) {
			t.Fatalf("backup logical byte %d = %#x, want %#x", i, got, byte(0x41+i))
		}
	}

	// Framebuffer probes, the SAT-2 gate's display. The
	// VDP1 draw and framebuffer swap pipeline takes a
	// few frames to present after the guest programs it,
	// so settle first (the assertions stay exact).
	for i := 0; i < 5; i++ {
		s.RunFrame()
	}
	if w, h := s.DisplaySize(); w != 320 || h != 224 {
		t.Fatalf("display size = %dx%d, want 320x224", w, h)
	}
	fb := s.Framebuffer()
	px := func(x, y int) uint32 { return fb[y*320+x] }
	if got := px(0, 0); got != 0xFF00FF00 {
		t.Fatalf("pixel (0,0) = %#x, want green 0xFF00FF00", got)
	}
	if got := px(1, 0); got != 0xFFFF0000 {
		t.Fatalf("pixel (1,0) = %#x, want red 0xFFFF0000", got)
	}
	if got := px(105, 55); got != 0xFFFFFFFF {
		t.Fatalf("pixel (105,55) = %#x, want white 0xFFFFFFFF", got)
	}
	if got := px(99, 55); got != 0xFF000000 {
		t.Fatalf("pixel (99,55) = %#x, want black 0xFF000000", got)
	}

	// Audio: the sine table at unity pitch must measure
	// at 44100/256 Hz within two percent.
	var samples []int16
	for i := 0; i < 15; i++ {
		s.RunFrame()
		samples = append(samples, s.AudioBuffer()...)
	}
	var crossings []int
	for n := 1; n*2 < len(samples); n++ {
		if samples[(n-1)*2] < 0 && samples[n*2] >= 0 {
			crossings = append(crossings, n)
		}
	}
	if len(crossings) < 4 {
		t.Fatalf("only %d positive crossings in %d stereo samples; the tone is not playing",
			len(crossings), len(samples)/2)
	}
	period := float64(crossings[len(crossings)-1]-crossings[0]) / float64(len(crossings)-1)
	freq := 44100.0 / period
	want := 44100.0 / 256.0
	if math.Abs(freq-want)/want > 0.02 {
		t.Fatalf("tone frequency = %.2f Hz, want %.2f within 2 percent", freq, want)
	}
	t.Logf("enginetest: 7 milestones in %d frames, tone %.2f Hz (period %.1f samples), state at sound start %d bytes",
		frames, freq, period, len(snap))

	// Mid run save: byte identical continuation of
	// state, framebuffer and audio. The restored machine
	// first replays the frames the original ran between
	// the snapshot and the measured window (the rest of
	// the milestone run, the alive check frame, the five
	// settle frames and the fifteen tone frames), then
	// both run the same 30 measured frames.
	if snap == nil {
		t.Fatal("the mid run save was never taken")
	}
	preFrames := (frames - snapFrame) + 1 + 5 + 15
	var audioA []int16
	for i := 0; i < 30; i++ {
		s.RunFrame()
		audioA = append(audioA, s.AudioBuffer()...)
	}
	wantState := s.Serialize()
	wantFB := append([]uint32(nil), s.Framebuffer()...)

	s2 := New()
	img2, err := cd.OpenBytes(iso, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.LoadDiscImage(img2); err != nil {
		t.Fatal(err)
	}
	if err := s2.Deserialize(snap); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	for i := 0; i < preFrames; i++ {
		s2.RunFrame()
		s2.AudioBuffer()
	}
	var audioB []int16
	for i := 0; i < 30; i++ {
		s2.RunFrame()
		audioB = append(audioB, s2.AudioBuffer()...)
	}
	equalI16 := func(a, b []int16) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	if !equalI16(audioA, audioB) {
		t.Fatal("audio continuation differs after restore")
	}
	if got := s2.Serialize(); !bytes.Equal(got, wantState) {
		t.Fatal("state continuation differs after restore")
	}
	gotFB := s2.Framebuffer()
	if len(gotFB) != len(wantFB) {
		t.Fatalf("framebuffer size = %d, want %d", len(gotFB), len(wantFB))
	}
	for i := range wantFB {
		if gotFB[i] != wantFB[i] {
			t.Fatalf("framebuffer pixel %d = %#x, want %#x", i, gotFB[i], wantFB[i])
		}
	}
}
