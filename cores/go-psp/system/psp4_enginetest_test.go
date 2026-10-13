package system

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-psp/enginetest"
)

// PSP-4 enginetest gate: the synthetic UMD image from the
// enginetest fixture package, booted through the same path the
// libretro shell uses (BootUMD: ISO, PSP_GAME/SYSDR/BOOT.BIN,
// kernel). The guest prints, draws, plays a continuous tone,
// polls the pad, and idles on vblank waits; the host asserts
// milestones in order, exact pixels, the tone, the recorded
// buttons, and a byte-identical continuation across a mid-run
// save.

func writeEngineISO(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "psp4_engine.iso")
	if err := os.WriteFile(path, enginetest.ISOImage(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func milestoneProgress(m *Machine) uint32 {
	return m.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffProgress)
}

func TestPSP4EngineTestISO(t *testing.T) {
	path := writeEngineISO(t)

	m := New()
	if err := m.BootUMD(path); err != nil {
		t.Fatalf("BootUMD: %v", err)
	}
	if got := m.GameTitle(); got != enginetest.GameTitle {
		t.Fatalf("GameTitle = %q, want %q (PARAM.SFO through the ISO)", got, enginetest.GameTitle)
	}

	// Host input arrives after boot, like the harness's
	// schedule: Start and Down.
	const buttons = uint32(0x0008 | 0x0040) // Start | Down
	var stream []int16
	frames := 0
	run := func(n int) {
		for i := 0; i < n; i++ {
			m.RunFrame()
			frames++
			stream = append(stream, m.AudioBuffer()...)
		}
	}
	run(10)
	if got := milestoneProgress(m); got < enginetest.MilestoneAudio {
		t.Fatalf("after 10 frames progress = %d, want at least %d (console, GE, audio all started)", got, enginetest.MilestoneAudio)
	}
	m.SetButtons(buttons)
	run(90)

	if m.Exited() {
		t.Fatal("enginetest guest exited; it must idle forever")
	}
	// Milestones complete, in order: the progress cell only
	// ever holds the count of finished stages.
	if got := milestoneProgress(m); got != enginetest.MilestoneFinal {
		t.Fatalf("progress = %d, want %d (all milestones)", got, enginetest.MilestoneFinal)
	}
	if got := m.ConsoleOutput(); got != enginetest.ConsoleText {
		t.Fatalf("console = %q, want %q", got, enginetest.ConsoleText)
	}
	if got := m.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffButtons); got != buttons {
		t.Fatalf("guest recorded buttons = %#x, want %#x (Start|Down as set by the host)", got, buttons)
	}
	if got := m.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffAudioCalls); got < 8 {
		t.Fatalf("guest audio calls = %d, want at least 8", got)
	}
	if got := m.Bus.Read32(enginetest.MilestoneAddr + enginetest.OffVblanks); got < 30 {
		t.Fatalf("guest vblank waits = %d, want at least 30 in 100 frames", got)
	}
	if got := m.Kernel().Vcount(); got != uint32(frames) {
		t.Fatalf("kernel vcount = %d, want %d", got, frames)
	}

	// Exact pixels: clear color everywhere, the sprite green
	// inside its rect.
	fb := m.Framebuffer()
	probe := func(x, y int, want uint32, what string) {
		if got := fb[y*Width+x]; got != want {
			t.Fatalf("%s pixel (%d,%d) = %#08x, want %#08x", what, x, y, got, want)
		}
	}
	probe(0, 0, enginetest.ClearPixel, "clear")
	probe(479, 271, enginetest.ClearPixel, "clear")
	probe(100, 100, enginetest.ClearPixel, "clear")
	probe(10, 10, enginetest.SpritePixel, "sprite")
	probe(15, 11, enginetest.SpritePixel, "sprite")
	probe(16, 12, enginetest.ClearPixel, "clear just past the sprite")

	// The tone: continuous from an early start, table exact.
	tab := enginetest.ToneTable()
	start := -1
	for i := 0; i < len(stream)/2; i++ {
		if stream[2*i] != 0 {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("audio stream is entirely silent")
	}
	if start > 735*5 {
		t.Fatalf("tone starts at stream frame %d, want within the first 5 frames of runtime", start)
	}
	// After the start the guest's phase runs continuously
	// (double buffered fills), so every frame is the table.
	for i := start; i < len(stream)/2; i++ {
		want := tab[(i-start)%enginetest.TonePeriod]
		if stream[2*i] != want || stream[2*i+1] != want {
			t.Fatalf("stream frame %d: got (%d, %d), want tone value %d", i, stream[2*i], stream[2*i+1], want)
		}
	}
	t.Logf("enginetest: %d frames run, tone from stream frame %d, %d samples exact", frames, start, len(stream))
}

// TestPSP4EngineTestSaveRestore: a save taken mid-run restores
// a byte-identical continuation: machine state, framebuffer,
// and the audio block produced after the save point.
func TestPSP4EngineTestSaveRestore(t *testing.T) {
	path := writeEngineISO(t)
	const buttons = uint32(0x0008 | 0x0040)

	boot := func() *Machine {
		m := New()
		if err := m.BootUMD(path); err != nil {
			t.Fatalf("BootUMD: %v", err)
		}
		return m
	}

	// Reference run: 40 warm-up frames, then 30 more.
	ref := boot()
	ref.SetButtons(buttons)
	for f := 0; f < 40; f++ {
		ref.RunFrame()
		ref.AudioBuffer()
	}
	var refAudio []int16
	for f := 0; f < 30; f++ {
		ref.RunFrame()
		refAudio = append(refAudio, ref.AudioBuffer()...)
	}
	refState := ref.Serialize()
	refFB := append([]uint32{}, ref.Framebuffer()...)

	// Restored run: same 40 frames, save, continue 30, then
	// restore and continue the same 30 again.
	m := boot()
	m.SetButtons(buttons)
	for f := 0; f < 40; f++ {
		m.RunFrame()
		m.AudioBuffer()
	}
	saved := m.Serialize()
	for f := 0; f < 30; f++ {
		m.RunFrame()
		m.AudioBuffer()
	}
	if err := m.Deserialize(saved); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	var gotAudio []int16
	for f := 0; f < 30; f++ {
		m.RunFrame()
		gotAudio = append(gotAudio, m.AudioBuffer()...)
	}
	if got := m.Serialize(); !equalBytes(got, refState) {
		t.Fatal("state after restore + 30 frames differs from the reference run")
	}
	fb := m.Framebuffer()
	for i := range fb {
		if fb[i] != refFB[i] {
			t.Fatalf("framebuffer pixel %d = %#08x, want %#08x", i, fb[i], refFB[i])
		}
	}
	if len(gotAudio) != len(refAudio) {
		t.Fatalf("audio block %d samples, want %d", len(gotAudio), len(refAudio))
	}
	for i := range gotAudio {
		if gotAudio[i] != refAudio[i] {
			t.Fatalf("audio sample %d = %d, want %d", i, gotAudio[i], refAudio[i])
		}
	}
}
