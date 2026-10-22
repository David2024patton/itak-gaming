package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidpatton/itak-gaming/go_backend/internal/switchguard"
)

// Backup-and-hold trigger tests. The guard's own copy logic is
// gated in internal/switchguard; these tests gate the wiring:
// the trigger fires only on the Rust path, only when a Rust
// core actually constructs, and a failed backup turns into a
// refused serve at LoadGame.

func seedCoreSaves(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(root, "saves", "Player 1", "game.sav")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("progress"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func backupExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, switchguard.BackupDirName))
	return err == nil
}

// Gate (d): the pure-Go path creates no backup, starts no hold,
// and is never blocked, even with a root configured and saves
// present.
func TestPureGoPathCreatesNoBackup(t *testing.T) {
	root := t.TempDir()
	seedCoreSaves(t, root)
	switchguard.Configure(root)
	defer switchguard.Reset()

	nb, ok := LoadNative("/cores/libgo_gameboy.so")
	if !ok {
		t.Fatalf("LoadNative does not know libgo_gameboy.so")
	}
	defer nb.Close()
	rom := writeGame(t, "spin.gb", gbROM())
	if err := nb.LoadGame(rom); err != nil {
		t.Fatalf("pure-Go LoadGame blocked: %v", err)
	}
	nb.RunFrame()
	if backupExists(root) {
		t.Fatalf("pure-Go path created a backup")
	}
	if _, err := os.Stat(filepath.Join(root, "switchguard")); !os.IsNotExist(err) {
		t.Fatalf("pure-Go path wrote guard state")
	}
	if switchguard.HoldActive() {
		t.Fatalf("pure-Go path started a hold")
	}
}

// A Rust library that fails to construct never serves a game,
// so the trigger must not fire for it either.
func TestRustConstructionFailureCreatesNoBackup(t *testing.T) {
	root := t.TempDir()
	seedCoreSaves(t, root)
	switchguard.Configure(root)
	defer switchguard.Reset()

	nb, ok := LoadRust("/cores/libitak_gameboy.so")
	if !ok {
		t.Fatalf("LoadRust does not know libitak_gameboy.so")
	}
	defer nb.Close()
	rom := writeGame(t, "spin.gb", gbROM())
	if err := nb.LoadGame(rom); err == nil {
		t.Fatalf("LoadGame with a missing adapter library succeeded")
	}
	if backupExists(root) {
		t.Fatalf("failed Rust construction created a backup")
	}
	if switchguard.HoldActive() {
		t.Fatalf("failed Rust construction started a hold")
	}
}

// Gate (e), wiring level: a real adapter library constructs,
// the backup cannot be made, and the serve is refused loudly
// at LoadGame with the backup failure named.
func TestRustBackupFailureRefusesServe(t *testing.T) {
	so := os.Getenv("ITAK_BACKUP_TEST_SO")
	if so == "" {
		home, _ := os.UserHomeDir()
		so = filepath.Join(home, "itak-adapter-gameboy", "target", "release", "libitak_gameboy.so")
	}
	data, err := os.ReadFile(so)
	if err != nil {
		t.Skipf("adapter library not available at %s", so)
	}
	dir := t.TempDir()
	soPath := filepath.Join(dir, "libitak_gameboy.so")
	if err := os.WriteFile(soPath, data, 0o755); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	// A saves path that is a regular file, not a tree: the
	// backup copy cannot be made, on any uid.
	if err := os.WriteFile(filepath.Join(root, "saves"), []byte("not a tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	switchguard.Configure(root)
	defer switchguard.Reset()

	nb, ok := LoadRust(soPath)
	if !ok {
		t.Fatalf("LoadRust does not know the adapter name")
	}
	defer nb.Close()
	rom := writeGame(t, "spin.gb", gbROM())
	err = nb.LoadGame(rom)
	if err == nil {
		t.Fatalf("Rust serve proceeded with no backup in place")
	}
	if !strings.Contains(err.Error(), "backup-hold") {
		t.Fatalf("refusal does not name the backup failure: %v", err)
	}
	t.Logf("refused as designed: %v", err)
	if switchguard.HoldActive() {
		t.Fatalf("hold started despite the refused serve")
	}
	if _, err := os.Stat(filepath.Join(root, "switchguard", "marker.json")); !os.IsNotExist(err) {
		t.Fatalf("marker written despite the refused serve")
	}
}
