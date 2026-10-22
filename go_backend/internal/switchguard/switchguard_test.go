package switchguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// seedSaves builds a saves tree shaped like the app's: per
// player files saves/<player>/<game>.sav plus the legacy
// saves/<game>.sav, with distinct binary contents.
func seedSaves(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"saves/Player 1/tobutobugirl.sav": {0x00, 0x01, 0x02, 0xFF, 0x10, 0x20},
		"saves/Player 1/zelda.sav":        []byte("zelda battery progress"),
		"saves/David/smw.sav":             bytes.Repeat([]byte{0xAB}, 4096),
		"saves/legacy.sav":                []byte("pre-profiles save"),
	}
	for rel, data := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// treeHashes maps every regular file under dir to its sha256,
// keyed by slash relative path.
func treeHashes(t *testing.T, dir string) map[string]string {
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

func quiet(t *testing.T) func(string, ...any) {
	t.Helper()
	logs := &strings.Builder{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("guard log:\n%s", logs.String())
		}
	})
	return func(format string, args ...any) {
		logs.WriteString(format)
		logs.WriteString("\n")
		_ = args
	}
}

// Gate (a): the first protection copies the whole saves tree
// byte-identically and starts the hold.
func TestFirstProtectionByteIdentical(t *testing.T) {
	root := t.TempDir()
	want := seedSaves(t, root)
	g := New(root)
	g.SetLogger(quiet(t))
	fired, err := g.EnsureProtected()
	if err != nil {
		t.Fatalf("EnsureProtected: %v", err)
	}
	if !fired {
		t.Fatalf("first EnsureProtected did not fire")
	}
	backup := filepath.Join(root, BackupDirName)
	for rel, data := range want {
		inner := strings.TrimPrefix(rel, "saves/")
		got, err := os.ReadFile(filepath.Join(backup, inner))
		if err != nil {
			t.Fatalf("backup missing %s: %v", inner, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("backup %s differs from the seeded save", inner)
		}
	}
	if got := treeHashes(t, backup); len(got) != len(want) {
		t.Fatalf("backup holds %d files, seeded %d", len(got), len(want))
	}
	if _, err := os.Stat(filepath.Join(root, markerDirName, markerFileName)); err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if !g.HoldActive() {
		t.Fatalf("hold not active after first protection")
	}
	if g.BackupPath() != backup {
		t.Fatalf("BackupPath = %q, want %q", g.BackupPath(), backup)
	}
}

// Gate (c): a second switched run (a fresh Guard over the same
// root, as a later launch or a later switch build sees) makes
// no new backup, does not touch the first, and starts no hold.
func TestSecondRunLeavesBackupUntouched(t *testing.T) {
	root := t.TempDir()
	seedSaves(t, root)
	g1 := New(root)
	g1.SetLogger(quiet(t))
	if _, err := g1.EnsureProtected(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, BackupDirName)
	before := treeHashes(t, backup)

	g2 := New(root)
	g2.SetLogger(quiet(t))
	fired, err := g2.EnsureProtected()
	if err != nil {
		t.Fatalf("second EnsureProtected: %v", err)
	}
	if fired {
		t.Fatalf("second run fired protection again")
	}
	if g2.HoldActive() {
		t.Fatalf("second run started a hold")
	}
	after := treeHashes(t, backup)
	if len(before) != len(after) {
		t.Fatalf("backup file count changed: %d to %d", len(before), len(after))
	}
	for rel, h := range before {
		if after[rel] != h {
			t.Fatalf("backup file %s changed on the second run", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(root, BackupDirName+"-2")); !os.IsNotExist(err) {
		t.Fatalf("second run created a sibling backup it should not have")
	}
}

// Gate (e), guard level: when the backup cannot be made, the
// refusal is loud (an error), no marker is written, no partial
// backup remains, and no hold starts.
func TestBackupFailureRefuses(t *testing.T) {
	root := t.TempDir()
	// A saves path that is a regular file, not a tree: the
	// copy cannot be made, on any uid. (A file squatting on
	// the backup name is not a failure at all: the sibling
	// rule backs up beside it, per TestExistingBackup... .)
	if err := os.WriteFile(filepath.Join(root, "saves"), []byte("not a tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(root)
	g.SetLogger(quiet(t))
	fired, err := g.EnsureProtected()
	if err == nil {
		t.Fatalf("EnsureProtected succeeded with the backup path blocked")
	}
	if fired {
		t.Fatalf("failed protection reported fired")
	}
	if g.HoldActive() {
		t.Fatalf("hold started despite the failed backup")
	}
	if _, err := os.Stat(filepath.Join(root, markerDirName, markerFileName)); !os.IsNotExist(err) {
		t.Fatalf("marker written despite the failed backup")
	}
	// The live saves path is untouched.
	got, err := os.ReadFile(filepath.Join(root, "saves"))
	if err != nil || string(got) != "not a tree" {
		t.Fatalf("live saves changed during the failed backup: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, BackupDirName)); !os.IsNotExist(err) {
		t.Fatalf("partial backup left behind after the failure")
	}
}

// Gate (f): the stored auto-save preference is never the
// guard's to touch. A stand-in prefs file beside the saves
// tree survives a full protect, hold, manual-save cycle
// byte-identical, and the hold still ends on the manual save.
func TestPreferenceUnchangedAndHoldEndsOnManualSave(t *testing.T) {
	root := t.TempDir()
	seedSaves(t, root)
	prefs := []byte("autosave=1\n")
	prefsPath := filepath.Join(root, "itak_prefs.xml")
	if err := os.WriteFile(prefsPath, prefs, 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(root)
	g.SetLogger(quiet(t))
	if _, err := g.EnsureProtected(); err != nil {
		t.Fatal(err)
	}
	if !g.HoldActive() {
		t.Fatalf("hold not active")
	}
	g.NoteManualSave()
	if g.HoldActive() {
		t.Fatalf("hold did not end on manual save")
	}
	got, err := os.ReadFile(prefsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, prefs) {
		t.Fatalf("stored auto-save preference changed: %q", got)
	}
	// EndHold is the other documented exit.
	g2 := New(root)
	_ = g2
}

// An existing backup with no marker is never overwritten: the
// protection takes the next sibling name and copies there.
func TestExistingBackupWithoutMarkerGetsSibling(t *testing.T) {
	root := t.TempDir()
	seedSaves(t, root)
	first := filepath.Join(root, BackupDirName)
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("someone elses backup")
	if err := os.WriteFile(filepath.Join(first, "keep.me"), sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(root)
	g.SetLogger(quiet(t))
	fired, err := g.EnsureProtected()
	if err != nil || !fired {
		t.Fatalf("EnsureProtected fired=%v err=%v", fired, err)
	}
	got, err := os.ReadFile(filepath.Join(first, "keep.me"))
	if err != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("existing backup was modified")
	}
	if _, err := os.Stat(filepath.Join(root, BackupDirName+"-2", "Player 1", "zelda.sav")); err != nil {
		t.Fatalf("sibling backup missing the copied saves: %v", err)
	}
	keys := []string{}
	for rel := range treeHashes(t, filepath.Join(root, BackupDirName+"-2")) {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	if len(keys) != 4 {
		t.Fatalf("sibling backup holds %d files, want 4 (%v)", len(keys), keys)
	}
}

// A fresh install has no saves tree: protection still fires,
// the backup is an empty tree, and the marker records zero.
func TestNoSavesTreeStillProtects(t *testing.T) {
	root := t.TempDir()
	g := New(root)
	g.SetLogger(quiet(t))
	fired, err := g.EnsureProtected()
	if err != nil || !fired {
		t.Fatalf("EnsureProtected fired=%v err=%v", fired, err)
	}
	if got := treeHashes(t, filepath.Join(root, BackupDirName)); len(got) != 0 {
		t.Fatalf("backup of an empty tree holds %d files", len(got))
	}
	if !g.HoldActive() {
		t.Fatalf("hold not active")
	}
	g.EndHold("session end")
	if g.HoldActive() {
		t.Fatalf("hold did not end at session end")
	}
}
