package mobile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidpatton/itak-gaming/go_backend/internal/core"
	"github.com/davidpatton/itak-gaming/go_backend/internal/switchguard"
)

// The backup-and-hold live drill (gate 3) plus the hold's
// write gate (gate b) and preference rule (gate f), driven
// through the production mobile surface and the production
// NativeBridge, in the pilot configuration: the Rust Game Boy
// adapter behind LoadCore, a real homebrew ROM, and a real
// Go-produced save state seeded into an app-shaped saves tree.
//
// Env, house style:
//   ITAK_RUST_TEST_SO   adapter .so (default: the Game Boy
//                       adapter's release build on this host)
//   ITAK_RUST_TEST_ROM  ROM (default: tobutobugirl.gb from the
//                       homebrew folder on this host)

func drillHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBackupHoldDrill(t *testing.T) {
	home, _ := os.UserHomeDir()
	so := os.Getenv("ITAK_RUST_TEST_SO")
	if so == "" {
		so = filepath.Join(home, "itak-adapter-gameboy", "target", "release", "libitak_gameboy.so")
	}
	rom := os.Getenv("ITAK_RUST_TEST_ROM")
	if rom == "" {
		rom = filepath.Join(home, "itak-dev", "homebrew", "tobutobugirl.gb")
	}
	soData, err := os.ReadFile(so)
	if err != nil {
		t.Skipf("adapter library not available at %s", so)
	}
	if _, err := os.Stat(rom); err != nil {
		t.Skipf("drill ROM not available at %s", rom)
	}
	soDir := t.TempDir()
	soPath := filepath.Join(soDir, "libitak_gameboy.so")
	if err := os.WriteFile(soPath, soData, 0o755); err != nil {
		t.Fatal(err)
	}

	// A real Go-produced state at frame 300, and the Go core's
	// own continuation state at frame 900 to compare against.
	goBridge, ok := core.LoadNative("libgo_gameboy.so")
	if !ok {
		t.Fatalf("LoadNative does not know libgo_gameboy.so")
	}
	if err := goBridge.LoadGame(rom); err != nil {
		t.Fatalf("Go LoadGame: %v", err)
	}
	for i := 0; i < 300; i++ {
		goBridge.RunFrame()
	}
	goState, err := goBridge.SaveState()
	if err != nil || len(goState) == 0 {
		t.Fatalf("Go SaveState: %v (%d bytes)", err, len(goState))
	}
	for i := 0; i < 600; i++ {
		goBridge.RunFrame()
	}
	goState900, err := goBridge.SaveState()
	if err != nil || len(goState900) == 0 {
		t.Fatalf("Go SaveState at 900: %v", err)
	}
	goBridge.Close()

	// Seed an app-shaped files root: the Go state in a player
	// slot, a second player's file, and the stored auto-save
	// preference standing by as a prefs file.
	root := t.TempDir()
	savePath := filepath.Join(root, "saves", "Player 1", "tobutobugirl.sav")
	if err := os.MkdirAll(filepath.Dir(savePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(savePath, goState, 0o644); err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(root, "saves", "David", "other.sav")
	if err := os.MkdirAll(filepath.Dir(otherPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("other progress"), 0o644); err != nil {
		t.Fatal(err)
	}
	prefs := []byte("autosave=1\n")
	prefsPath := filepath.Join(root, "itak_prefs.xml")
	if err := os.WriteFile(prefsPath, prefs, 0o644); err != nil {
		t.Fatal(err)
	}
	seeded := drillHashes(t, filepath.Join(root, "saves"))

	// The switched run: files root first (the SetFilesDir
	// call the staged activity makes), then the Rust core.
	SetFilesDir(root)
	defer switchguard.Reset()
	defer Close()
	if err := LoadCore(soPath); err != nil {
		t.Fatalf("LoadCore (Rust): %v", err)
	}
	if err := LoadGame(rom); err != nil {
		t.Fatalf("LoadGame (Rust): %v; the backup must have failed", err)
	}

	// The backup exists and is byte-identical to the seeded
	// tree (gate a at the drill level).
	if !BackupHoldActive() {
		t.Fatalf("hold not active after the first Rust serve")
	}
	backupDir := filepath.Join(root, switchguard.BackupDirName)
	backup := drillHashes(t, backupDir)
	if len(backup) != len(seeded) {
		t.Fatalf("backup holds %d files, seeded %d", len(backup), len(seeded))
	}
	for rel, h := range seeded {
		inner := strings.TrimPrefix(rel, "")
		if backup[inner] != h {
			t.Fatalf("backup file %s differs from the seeded save", inner)
		}
	}

	// Gate (b): during the hold the auto-save path produces
	// nothing, so the Kotlin write that follows it cannot
	// happen; the live tree is untouched. A manual SaveState
	// in the same moment returns real bytes, proving the nil
	// came from the hold and not a dead core.
	if data := AutoSaveState(); data != nil {
		t.Fatalf("AutoSaveState returned %d bytes during the hold", len(data))
	}
	if manual := SaveState(); len(manual) == 0 {
		t.Fatalf("manual SaveState returned nothing during the hold")
	}
	if after := drillHashes(t, filepath.Join(root, "saves")); len(after) != len(seeded) {
		t.Fatalf("live saves changed during the hold")
	} else {
		for rel, h := range seeded {
			if after[rel] != h {
				t.Fatalf("live save %s changed during the hold", rel)
			}
		}
	}

	// The seeded Go state still loads through the Rust core,
	// and the continuation lands byte-identical to the Go
	// core's own continuation state at frame 900: the player's
	// progress survives the switch.
	if err := LoadState(goState); err != nil {
		t.Fatalf("LoadState (Go state into Rust core): %v", err)
	}
	for i := 0; i < 600; i++ {
		RunFrame()
	}
	rustState900 := SaveState()
	if !bytes.Equal(rustState900, goState900) {
		t.Fatalf("Rust continuation state at 900 differs from Go's (%d vs %d bytes)",
			len(rustState900), len(goState900))
	}

	// The manual save ends the hold; auto-save produces bytes
	// again; the stored preference was never touched (gate f).
	NoteManualSave()
	if BackupHoldActive() {
		t.Fatalf("hold did not end on the manual save")
	}
	if data := AutoSaveState(); len(data) == 0 {
		t.Fatalf("AutoSaveState returned nothing after the hold ended")
	}
	gotPrefs, err := os.ReadFile(prefsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPrefs, prefs) {
		t.Fatalf("stored auto-save preference changed: %q", gotPrefs)
	}

	// Session end: the backup is still exactly the seeded
	// tree. Nothing wrote to it at any point.
	Close()
	finalBackup := drillHashes(t, backupDir)
	for rel, h := range seeded {
		if finalBackup[rel] != h {
			t.Fatalf("backup file %s changed by the end of the drill", rel)
		}
	}
}
