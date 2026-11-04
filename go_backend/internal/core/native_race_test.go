//go:build gocores

// This file is one of the bundled Go core adapters. The
// shipped app does not carry the Go cores (David's order,
// 1.102): the Rust cores are the only cores in the app, so
// these adapters are compiled only under the gocores build
// tag, which server-side reference and cross-check runs
// use. The Go core sources themselves live in cores/ and
// are untouched.

package core

import (
	"sync"
	"testing"
	"time"
)

// TestNativeConcurrentAccess drives a bridge the way the app
// does: a game loop thread running frames and reading them
// back, an audio thread draining samples, and a UI thread
// poking buttons and save states, all at once. Build 57 let
// the audio thread reach into the engine while the game loop
// was inside it and both goroutines panicked on device; this
// test (especially under -race) is the regression gate.
func TestNativeConcurrentAccess(t *testing.T) {
	for _, tc := range []struct {
		core, name string
		rom        []byte
	}{
		{"libgo_nes.so", "spin.nes", nesROM()},
		{"libgo_snes.so", "loop.sfc", loopROM()},
		{"libgo_gameboy.so", "spin.gb", gbROM()},
		{"libgo_gba.so", "spin.gba", gbaROM()},
	} {
		t.Run(tc.core, func(t *testing.T) {
			nb, ok := LoadNative("/x/" + tc.core)
			if !ok {
				t.Fatalf("no adapter for %s", tc.core)
			}
			if err := nb.LoadGame(writeGame(t, tc.name, tc.rom)); err != nil {
				t.Fatalf("LoadGame: %v", err)
			}
			defer nb.Close()

			stop := make(chan struct{})
			var wg sync.WaitGroup

			// Game loop: frames plus frame readback.
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					nb.RunFrame()
					if _, _, _, err := nb.FrameToRGBA(); err != nil {
						t.Error(err)
						return
					}
				}
			}()

			// Audio thread: drain at full rate.
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					_ = nb.DrainAudio(4096)
				}
			}()

			// UI thread: input and periodic save states.
			wg.Add(1)
			go func() {
				defer wg.Done()
				tick := time.NewTicker(7 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-stop:
						return
					case <-tick.C:
					}
					nb.SetButton(0, 3, true)
					nb.SetButton(0, 3, false)
					if st, err := nb.SaveState(); err == nil {
						_ = nb.LoadState(st)
					}
				}
			}()

			time.Sleep(1500 * time.Millisecond)
			close(stop)
			wg.Wait()

			if nb.VideoFrameCount() == 0 {
				t.Fatal("no frames ran under concurrency")
			}
		})
	}
}
