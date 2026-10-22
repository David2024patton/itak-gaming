package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for the Rust switch adapter (native_rust.go).
//
// TestLoadRustRegistry runs anywhere: it pins the registry
// shape and the deferred-error behavior for a library file
// that is not there.
//
// TestRustBridgeAdapter drives a real adapter library through
// the production NativeBridge and needs one, so it is gated on
// environment variables, in the house style of the scratch
// gates:
//
//	ITAK_RUST_TEST_SO          adapter .so to load (required)
//	ITAK_RUST_TEST_ROM         ROM file to load (required)
//	ITAK_RUST_TEST_REF         dir holding frame_<n>.bin reference
//	                           captures from the core's own harness
//	                           (required)
//	ITAK_RUST_TEST_CHECKPOINTS comma list of frame counts to
//	                           compare and to save/continue
//	                           between (default "300,900")
//	ITAK_RUST_TEST_FPS         expected FPS() after load (optional)
//	ITAK_RUST_TEST_RATE        expected SampleRate() (optional)
//	ITAK_RUST_TEST_W/H         expected frame geometry (optional)
//
// The reference captures must come from the core itself (for
// the Game Boy core: harness framedump <rom> <frames> <out>),
// never from the Go core: the point is that the picture the
// app would show through this bridge is byte-identical to the
// picture the Rust core produces on its own.

func TestLoadRustRegistry(t *testing.T) {
	nb, ok := LoadRust("/cores/libitak_gameboy.so")
	if !ok {
		t.Fatalf("LoadRust does not know libitak_gameboy.so")
	}
	defer nb.Close()
	// The library file does not exist at that path. The
	// LoadNative-shaped signature cannot return the failure,
	// so it must surface at LoadGame, the first call that can.
	rom := writeGame(t, "spin.gb", gbROM())
	if err := nb.LoadGame(rom); err == nil {
		t.Fatalf("LoadGame with a missing adapter library succeeded")
	} else {
		t.Logf("deferred load error, as designed: %v", err)
	}
	// The drained and probed surface stays inert, not panicky.
	nb.RunFrame()
	if got := nb.DrainAudio(16); len(got) != 0 {
		t.Fatalf("DrainAudio on a failed core returned %d samples", len(got))
	}
	if _, err := nb.SaveState(); err == nil {
		t.Fatalf("SaveState on a failed core succeeded")
	}
	if _, ok := LoadRust("/cores/libitak_nope.so"); ok {
		t.Fatalf("LoadRust claimed an unregistered library name")
	}
	if _, ok := LoadRust("/cores/libgo_gameboy.so"); ok {
		t.Fatalf("LoadRust claimed a Go native core name")
	}
	if _, ok := LoadRust("/cores/libgo_snes.so"); ok {
		t.Fatalf("LoadRust claimed a Go native core name")
	}
}

func TestRustBridgeAdapter(t *testing.T) {
	so := os.Getenv("ITAK_RUST_TEST_SO")
	rom := os.Getenv("ITAK_RUST_TEST_ROM")
	refDir := os.Getenv("ITAK_RUST_TEST_REF")
	if so == "" || rom == "" || refDir == "" {
		t.Skip("ITAK_RUST_TEST_SO, ITAK_RUST_TEST_ROM and ITAK_RUST_TEST_REF must be set")
	}
	checkpoints := []int{300, 900}
	if v := os.Getenv("ITAK_RUST_TEST_CHECKPOINTS"); v != "" {
		checkpoints = checkpoints[:0]
		for _, part := range strings.Split(v, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n <= 0 {
				t.Fatalf("bad checkpoint %q", part)
			}
			checkpoints = append(checkpoints, n)
		}
	}
	if len(checkpoints) < 2 {
		t.Fatalf("need at least two checkpoints (save at the first, continue to the second)")
	}
	refs := make(map[int][]byte, len(checkpoints))
	for _, cp := range checkpoints {
		b, err := os.ReadFile(filepath.Join(refDir, fmt.Sprintf("frame_%d.bin", cp)))
		if err != nil {
			t.Fatalf("read reference frame %d: %v", cp, err)
		}
		refs[cp] = b
	}

	nb, err := newRustBridge(so)
	if err != nil {
		t.Fatalf("newRustBridge: %v", err)
	}
	defer nb.Close()
	if err := nb.LoadGame(rom); err != nil {
		t.Fatalf("LoadGame: %v", err)
	}

	if pf := nb.PixelFormat(); pf != 1 {
		t.Fatalf("PixelFormat = %d, want 1 (the ABI is XRGB8888 only)", pf)
	}
	if v := os.Getenv("ITAK_RUST_TEST_FPS"); v != "" {
		want, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("bad ITAK_RUST_TEST_FPS: %v", err)
		}
		if got := nb.FPS(); math.Abs(got-want) > 0.001 {
			t.Fatalf("FPS = %v, want %v", got, want)
		}
	}
	if v := os.Getenv("ITAK_RUST_TEST_RATE"); v != "" {
		want, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("bad ITAK_RUST_TEST_RATE: %v", err)
		}
		if got := nb.SampleRate(); got != want {
			t.Fatalf("SampleRate = %v, want %v", got, want)
		}
	}

	hashOf := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	run := func(n int) {
		for i := 0; i < n; i++ {
			nb.RunFrame()
		}
	}
	frame := func() []byte {
		_, _, _, data := nb.GetFrame()
		return data
	}
	cp0, cp1 := checkpoints[0], checkpoints[1]

	// First checkpoint: the frame through the bridge must be
	// byte-identical to the core's own harness capture.
	run(cp0)
	got := frame()
	if len(got) == 0 {
		t.Fatalf("no frame after %d frames", cp0)
	}
	if !bytes.Equal(got, refs[cp0]) {
		t.Fatalf("frame %d sha256 = %s, reference %s (bridge bytes differ from the core's own capture)",
			cp0, hashOf(got), hashOf(refs[cp0]))
	}
	w, h, _, _ := nb.GetFrame()
	if w*h*4 != len(refs[cp0]) {
		t.Fatalf("frame geometry %dx%d does not match reference length %d", w, h, len(refs[cp0]))
	}
	if v := os.Getenv("ITAK_RUST_TEST_W"); v != "" {
		want, _ := strconv.Atoi(v)
		if w != want {
			t.Fatalf("frame width = %d, want %d", w, want)
		}
	}
	if v := os.Getenv("ITAK_RUST_TEST_H"); v != "" {
		want, _ := strconv.Atoi(v)
		if h != want {
			t.Fatalf("frame height = %d, want %d", h, want)
		}
	}

	// Audio: everything the frames produced must come back
	// out of the bridge drain, in whole stereo frames, and
	// the bridge accounting must agree with what it handed
	// out.
	total := 0
	for {
		s := nb.DrainAudio(4096)
		if len(s) == 0 {
			break
		}
		total += len(s)
	}
	if total == 0 {
		t.Fatalf("no audio drained after %d frames", cp0)
	}
	if total%2 != 0 {
		t.Fatalf("drained %d samples, not a whole number of stereo frames", total)
	}
	if counted := int(nb.AudioSamplesTotal()) * 2; counted != total {
		t.Fatalf("bridge audio accounting = %d samples, drained %d", counted, total)
	}

	// Save at the first checkpoint, continue uninterrupted to
	// the second, then scramble, restore, and continue again.
	// Both continuations must land on the reference frame.
	state, err := nb.SaveState()
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if len(state) == 0 {
		t.Fatalf("SaveState returned no bytes")
	}
	run(cp1 - cp0)
	if got := frame(); !bytes.Equal(got, refs[cp1]) {
		t.Fatalf("frame %d sha256 = %s, reference %s", cp1, hashOf(got), hashOf(refs[cp1]))
	}
	run(37) // scramble away from the saved moment
	if err := nb.LoadState(state); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	run(cp1 - cp0)
	if got := frame(); !bytes.Equal(got, refs[cp1]) {
		t.Fatalf("continuation after LoadState: frame sha256 = %s, reference %s",
			hashOf(got), hashOf(refs[cp1]))
	}

	// Input and cheats through the bridge surface: buttons
	// reach the core (no crash, frames keep coming), and the
	// cheat calls behave.
	nb.SetButton(0, ButtonStart, true)
	nb.SetButton(0, ButtonA, true)
	run(10)
	nb.ClearButtons()
	run(5)
	_ = nb.ApplyCheats([]string{"00000000"})
	nb.ClearCheats()

	// Concurrency smoke, the app access pattern from the
	// build 57 crash: game loop, audio drain, and UI input
	// from three goroutines at once. The bridge mutex is the
	// only thing between them and the engine.
	before := nb.VideoFrameCount()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			nb.RunFrame()
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = nb.DrainAudio(2048)
			_, _, _, _ = nb.GetFrame()
		}
	}()
	go func() {
		defer wg.Done()
		on := false
		for {
			select {
			case <-stop:
				return
			default:
			}
			on = !on
			nb.SetButton(0, ButtonRight, on)
			if _, err := nb.SaveState(); err != nil {
				t.Errorf("concurrent SaveState: %v", err)
				return
			}
		}
	}()
	time.Sleep(250 * time.Millisecond)
	close(stop)
	wg.Wait()
	if nb.VideoFrameCount() <= before {
		t.Fatalf("frame count did not advance under concurrent use")
	}
}
