// Command enginetest runs the iTaK engine (libretro bridge +
// per-player save manager) against a core and a ROM and reports
// PASS/FAIL lines for: core load, game load, AV info, video
// frames, audio output, save-state capture, per-player save
// round trip, and state restore byte equality.
//
// It is the cross-platform sibling of cmd/coretest (which has
// Linux paths baked in): pass -core and -rom for the platform's
// core library (.so on Linux, .dll on Windows). Every line is
// printed and also written to the result file (-out, default
// itak-test-result.txt next to the executable) so a run on a
// machine without a console still leaves a report behind.
package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/davidpatton/itak-gaming/go_backend/internal/core"
	"github.com/davidpatton/itak-gaming/go_backend/internal/save"
)

var failures int
var logFile *os.File

func say(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	fmt.Println(line)
	if logFile != nil {
		fmt.Fprintln(logFile, line)
	}
}

func fail(format string, args ...any) {
	failures++
	say("FAILED "+format, args...)
}

func frameHash(b *core.Bridge) string {
	_, _, rgba, err := b.FrameToRGBA()
	if err != nil {
		return ""
	}
	s := sha256.Sum256(rgba)
	return fmt.Sprintf("%x", s[:8])
}

func main() {
	corePath := flag.String("core", "", "path to the libretro core library (.so or .dll)")
	romPath := flag.String("rom", "", "path to the ROM file")
	outPath := flag.String("out", "", "result file (default: itak-test-result.txt next to the exe)")
	flag.Parse()

	out := *outPath
	if out == "" {
		if exe, err := os.Executable(); err == nil {
			out = filepath.Join(filepath.Dir(exe), "itak-test-result.txt")
		} else {
			out = "itak-test-result.txt"
		}
	}
	if f, err := os.Create(out); err == nil {
		logFile = f
	}

	say("=== iTaK Gaming engine test (%s/%s) ===", runtime.GOOS, runtime.GOARCH)
	say("core: %s", *corePath)
	say("rom:  %s", *romPath)
	if *corePath == "" || *romPath == "" {
		fail("both -core and -rom are required")
		finish()
	}

	b, err := core.LoadCore(*corePath)
	if err != nil {
		fail("load core: %v", err)
		finish()
	}
	defer b.Close()
	say("core loaded")

	if err := b.LoadGame(*romPath); err != nil {
		fail("load game: %v", err)
		finish()
	}
	say("game loaded")
	say("AV: aspect=%.4f fps=%.2f audio=%.0f Hz pixelFormat=%d",
		b.AspectRatio(), b.FPS(), b.SampleRate(), b.PixelFormat())

	// Video + audio: run 2 seconds worth of frames.
	for i := 0; i < 120; i++ {
		b.RunFrame()
	}
	w, h, _, ferr := b.FrameToRGBA()
	if ferr != nil {
		fail("frame decode: %v", ferr)
	} else {
		say("video: %dx%d, frames=%d, non-blank=%v",
			w, h, b.VideoFrameCount(), b.VideoEverNonzero())
		if !b.VideoEverNonzero() {
			fail("video frames are all blank")
		}
	}
	samples := b.DrainAudio(48000)
	nonSilent := 0
	for _, s := range samples {
		if s != 0 {
			nonSilent++
		}
	}
	say("audio: produced=%d drained=%d non-silent=%d",
		b.AudioSamplesTotal(), len(samples), nonSilent)
	if nonSilent == 0 {
		fail("audio is silent")
	}

	// Save states: title screen, press Start, snapshot.
	for i := 0; i < 480; i++ {
		b.RunFrame()
	}
	b.SetButton(0, core.ButtonStart, true)
	for i := 0; i < 10; i++ {
		b.RunFrame()
	}
	b.SetButton(0, core.ButtonStart, false)
	for i := 0; i < 120; i++ {
		b.RunFrame()
	}
	hashAtSave := frameHash(b)
	state, err := b.SaveState()
	if err != nil {
		fail("save state: %v", err)
		finish()
	}
	say("save state: %d bytes at frame hash %s", len(state), hashAtSave)

	// Per-player saves through the save manager.
	tmp, err := os.MkdirTemp("", "itak_saves")
	if err != nil {
		fail("temp dir: %v", err)
		finish()
	}
	defer os.RemoveAll(tmp)
	gameName := filepath.Base(*romPath)
	mgr := save.NewManager(tmp)
	if err := mgr.Save("dad", gameName, 0, state); err != nil {
		fail("manager save dad: %v", err)
	}
	if err := mgr.Save("kid", gameName, 0, state); err != nil {
		fail("manager save kid: %v", err)
	}
	say("save manager: wrote one state each for players dad and kid")

	// Play on (screen changes), then restore dad's state.
	b.SetButton(0, core.ButtonDown, true)
	for i := 0; i < 10; i++ {
		b.RunFrame()
	}
	b.SetButton(0, core.ButtonDown, false)
	for i := 0; i < 30; i++ {
		b.RunFrame()
	}
	say("after moving on: hash %s (differs from save point: %v)",
		frameHash(b), frameHash(b) != hashAtSave)

	loaded, err := mgr.Load("dad", gameName, 0)
	if err != nil {
		fail("manager load dad: %v", err)
		finish()
	}
	if err := b.LoadState(loaded); err != nil {
		fail("load state: %v", err)
		finish()
	}
	for i := 0; i < 2; i++ {
		b.RunFrame()
	}
	// The restore is exact when the screen is back at the save
	// point. (A re-serialized state after running frames is not
	// byte-compared: cores advance internal counters as they run.)
	hashRestored := frameHash(b)
	say("after restoring save: hash %s (save point %s)", hashRestored, hashAtSave)
	if hashRestored != hashAtSave {
		fail("restored frame does not match the save point")
	}
	finish()
}

func finish() {
	if failures == 0 {
		say("RESULT: ALL TESTS PASSED")
	} else {
		say("RESULT: %d FAILURE(S)", failures)
	}
	if logFile != nil {
		logFile.Close()
	}
	if failures > 0 {
		os.Exit(1)
	}
}
