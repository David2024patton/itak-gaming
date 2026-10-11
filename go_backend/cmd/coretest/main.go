package main

import (
	"crypto/sha256"
	"fmt"
	"github.com/davidpatton/itak-gaming/go_backend/internal/core"
	"github.com/davidpatton/itak-gaming/go_backend/internal/save"
)

func frameHash(b *core.Bridge) string {
	_, _, rgba, err := b.FrameToRGBA()
	if err != nil { return "" }
	s := sha256.Sum256(rgba)
	return fmt.Sprintf("%x", s[:8])
}

func main() {
	b, err := core.LoadCore("/home/hatch/workspace/itak-gaming/testdata/snes9x_libretro.so")
	if err != nil { fmt.Printf("FAILED load core: %v\n", err); return; }
	defer b.Close()
	if err := b.LoadGame("/home/hatch/workspace/itak-gaming/testdata/Super Mario World (USA).sfc"); err != nil {
		fmt.Printf("FAILED load game: %v\n", err); return
	}

	// --- AUDIO TEST ---
	for i := 0; i < 120; i++ { b.RunFrame() }
	samples := b.DrainAudio(48000)
	nonSilent := 0
	for _, s := range samples { if s != 0 { nonSilent++ } }
	fmt.Printf("AUDIO: total frames produced=%d, drained samples=%d, non-silent=%d\n",
		b.AudioSamplesTotal(), len(samples), nonSilent)

	// --- SAVE STATE TEST ---
	// Get to title, press start for file select, snapshot state
	for i := 0; i < 480; i++ { b.RunFrame() }
	b.SetButton(0, core.ButtonStart, true)
	for i := 0; i < 10; i++ { b.RunFrame() }
	b.SetButton(0, core.ButtonStart, false)
	for i := 0; i < 120; i++ { b.RunFrame() }
	hashAtSave := frameHash(b)

	state, err := b.SaveState()
	if err != nil { fmt.Printf("FAILED save state: %v\n", err); return }
	fmt.Printf("SAVE STATE: captured %d bytes at file-select (hash %s)\n", len(state), hashAtSave)

	// Save it under two different players via the save manager
	mgr := save.NewManager("/tmp/itak_saves")
	mgr.Save("dad", "Super Mario World (USA)", 0, state)
	mgr.Save("kid", "Super Mario World (USA)", 0, state)
	fmt.Println("SAVE MANAGER: wrote state for players 'dad' and 'kid'")

	// Play on: move cursor down (changes screen), then load the state back
	b.SetButton(0, core.ButtonDown, true)
	for i := 0; i < 10; i++ { b.RunFrame() }
	b.SetButton(0, core.ButtonDown, false)
	for i := 0; i < 30; i++ { b.RunFrame() }
	hashMoved := frameHash(b)
	fmt.Printf("After moving cursor: hash %s (differs: %v)\n", hashMoved, hashMoved != hashAtSave)

	// Load dad's state back
	loaded, err := mgr.Load("dad", "Super Mario World (USA)", 0)
	if err != nil { fmt.Printf("FAILED load from manager: %v\n", err); return }
	if err := b.LoadState(loaded); err != nil { fmt.Printf("FAILED load state: %v\n", err); return }
	for i := 0; i < 2; i++ { b.RunFrame() }
	hashRestored := frameHash(b)
	fmt.Printf("After restoring save: hash %s\n", hashRestored)
	// Note: 2 frames of fade may differ slightly; check save/load round trip byte equality
	state2, _ := b.SaveState()
	same := len(state2) == len(state)
	if same {
		for i := range state { if state[i] != state2[i] { same = false; break } }
	}
	fmt.Printf("SAVE ROUND TRIP: state identical after reload: %v\n", same)
}
