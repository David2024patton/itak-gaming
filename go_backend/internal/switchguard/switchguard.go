// Package switchguard implements backup-and-hold, the
// app-side safety feature for the Go to Rust core switch.
//
// The trap it exists to close (~/itak-rust-bridge/SAVE_STATES.md):
// the app has no separate battery files. Each save file under
// filesDir/saves holds the core's full state blob with the
// battery RAM inside it. A failed state load is graceful, but
// auto-save is on by default and overwrites that same file every
// 60 seconds and again on exit. After a naive core swap the old
// blob fails to load, the player starts fresh, and within about
// a minute the only copy of their progress is silently gone.
//
// The rule this package enforces: before a Rust core serves its
// first game on an install, the entire saves tree is copied to a
// backup that no code path ever writes again, and auto-save is
// held off for that first switched session until the player
// saves manually once or the session ends. If the backup cannot
// be made, the Rust path refuses to serve rather than proceed
// unprotected. A pure-Go build never calls this package's
// trigger and is never blocked by it.
//
// Storage layout, derived from the app (EmulatorActivity.kt):
// the app files dir holds saves/<player>/<game>.sav plus the
// legacy saves/<game>.sav. This package treats the whole saves
// tree as opaque files and copies it verbatim to a sibling:
//
//	<root>/saves/                        live tree, never touched here
//	<root>/saves-backup-before-rust/     first backup, write-once
//	<root>/saves-backup-before-rust-N/   sibling if the first name
//	                                     was already taken without
//	                                     a marker (never overwritten)
//	<root>/switchguard/marker.json      versioned marker, written
//	                                     only after a backup completes
//
// Diagnostics: every step is logged through the standard log
// package with the "backup-hold:" prefix, the same channel the
// bridges use (logcat on device, the app log screen reads it).
package switchguard

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BackupDirName is the first backup directory name, a sibling
// of the saves tree inside the app files root.
const BackupDirName = "saves-backup-before-rust"

// markerDirName and markerFileName locate the marker that
// records a completed protection. The marker lives outside the
// backup so the backup directory is a pure copy of the saves
// tree and nothing else.
const (
	markerDirName  = "switchguard"
	markerFileName = "marker.json"
)

// markerVersion versions the marker format, so a future switch
// build can still read what this one wrote.
const markerVersion = 1

// Marker is the on-disk record of a completed first protection.
type Marker struct {
	Version     int    `json:"version"`
	CreatedUnix int64  `json:"created_unix"`
	BackupDir   string `json:"backup_dir"` // base name, sibling of saves
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
}

// Guard holds the backup-and-hold state for one app files root.
// The zero value is not usable; call New. A Guard is safe for
// concurrent use.
type Guard struct {
	mu     sync.Mutex
	root   string
	logf   func(format string, args ...any)
	done   bool   // protection completed or found, this process
	backup string // absolute backup path, once known
	hold   bool   // auto-save held for this session
}

// New returns a Guard rooted at the app files directory (the
// directory that contains saves/).
func New(root string) *Guard {
	return &Guard{root: root}
}

// SetLogger overrides the diagnostic sink. Tests use it to
// capture the log; production leaves the standard logger.
func (g *Guard) SetLogger(logf func(format string, args ...any)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logf = logf
}

func (g *Guard) diag(format string, args ...any) {
	if g.logf != nil {
		g.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Root returns the configured app files root, or "".
func (g *Guard) Root() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.root
}

func (g *Guard) markerPath() string {
	return filepath.Join(g.root, markerDirName, markerFileName)
}

// readMarker loads the marker, returning ok=false when there is
// none. A marker that exists but does not parse is an error:
// guessing here could skip a needed backup.
func (g *Guard) readMarker() (Marker, bool, error) {
	data, err := os.ReadFile(g.markerPath())
	if os.IsNotExist(err) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, false, err
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, false, fmt.Errorf("parse marker: %w", err)
	}
	if m.Version != markerVersion {
		return Marker{}, false, fmt.Errorf("marker version %d, this build writes %d", m.Version, markerVersion)
	}
	return m, true, nil
}

// EnsureProtected performs the first-run protection if it has
// not happened for this root yet. It reports whether this call
// did the protection (and so started the hold). Later calls in
// the same process, and every call once a marker exists, are
// no-ops that report false. An error means protection could not
// be completed; the caller must refuse to serve the Rust core.
func (g *Guard) EnsureProtected() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.root == "" {
		return false, fmt.Errorf("backup-hold: no app files root configured")
	}
	if g.done {
		return false, nil
	}
	if m, ok, err := g.readMarker(); err != nil {
		return false, fmt.Errorf("backup-hold: read marker: %w", err)
	} else if ok {
		g.done = true
		g.backup = filepath.Join(g.root, m.BackupDir)
		g.diag("backup-hold: already protected on %s (%d files, %d bytes in %s); hold not started, this is not the first switched session",
			time.Unix(m.CreatedUnix, 0).Format(time.RFC3339), m.Files, m.Bytes, g.backup)
		return false, nil
	}

	// Pick a backup name that is not taken. An existing backup
	// is never overwritten, even one this build did not make.
	name := BackupDirName
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(g.root, name)); os.IsNotExist(err) {
			break
		} else if err != nil {
			return false, fmt.Errorf("backup-hold: stat backup dir: %w", err)
		}
		name = fmt.Sprintf("%s-%d", BackupDirName, i)
	}
	backupPath := filepath.Join(g.root, name)
	g.diag("backup-hold: trigger: first Rust core serve on this install; backing up saves tree %s to %s",
		filepath.Join(g.root, "saves"), backupPath)

	files, bytes, err := copyTree(filepath.Join(g.root, "saves"), backupPath, func(format string, args ...any) {
		g.diag(format, args...)
	})
	if err != nil {
		// A partial backup must never be mistaken for a
		// complete one: remove what this attempt made.
		_ = os.RemoveAll(backupPath)
		return false, fmt.Errorf("backup-hold: backup failed, Rust core refused: %w", err)
	}
	m := Marker{
		Version:     markerVersion,
		CreatedUnix: time.Now().Unix(),
		BackupDir:   name,
		Files:       files,
		Bytes:       bytes,
	}
	if err := g.writeMarker(m); err != nil {
		_ = os.RemoveAll(backupPath)
		return false, fmt.Errorf("backup-hold: backup failed, Rust core refused: write marker: %w", err)
	}
	g.done = true
	g.backup = backupPath
	g.hold = true
	g.diag("backup-hold: backup complete: %d files, %d bytes copied to %s (never written again)", files, bytes, backupPath)
	g.diag("backup-hold: hold start: auto-save held for this first switched session until a manual save or a clean session end; the stored auto-save preference is not changed")
	return true, nil
}

// writeMarker persists the marker atomically (tmp file plus
// rename) so a crash mid-write leaves no marker, and the next
// run protects again instead of trusting a torn record.
func (g *Guard) writeMarker(m Marker) error {
	dir := filepath.Join(g.root, markerDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, markerFileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.markerPath())
}

// BackupPath returns the absolute backup directory once
// protection has completed or been found, else "".
func (g *Guard) BackupPath() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.backup
}

// HoldActive reports whether auto-save is currently held.
func (g *Guard) HoldActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hold
}

// NoteManualSave ends the hold because the player saved by
// hand: the live slot now holds a state the current core wrote
// on purpose, so automatic saves may resume. No-op when no
// hold is active.
func (g *Guard) NoteManualSave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.hold {
		return
	}
	g.hold = false
	g.diag("backup-hold: hold end: player saved manually; auto-save resumed under the stored preference")
}

// EndHold ends the hold for a stated reason (a clean session
// end). The backup stays on disk regardless; the next session
// is not a first switched session and starts no new hold.
func (g *Guard) EndHold(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.hold {
		return
	}
	g.hold = false
	g.diag("backup-hold: hold end: %s", reason)
}

// copyTree copies every regular file under src into dst,
// preserving relative paths, and returns the file count and
// total bytes. A missing src is an empty tree, not an error:
// a fresh install has no saves to lose, and the (empty) backup
// plus marker still record that protection ran.
func copyTree(src, dst string, diag func(format string, args ...any)) (int, int64, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, 0, err
	}
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("saves path %s is not a directory", src)
	}
	files := 0
	var total int64
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			diag("backup-hold: backup: skipping non-regular file %s", path)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if fi, err := d.Info(); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
		files++
		total += int64(len(data))
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return files, total, nil
}

// The process-wide default Guard. The mobile package configures
// it through SetFilesDir; the core package's LoadRust fires the
// trigger through EnsureProtected. Contexts that never
// configure a root (desktop harnesses, core tests) skip
// protection: there is no app saves tree to protect there.

var (
	defaultMu    sync.Mutex
	defaultGuard = New("")
	defaultNoted bool
)

// Configure points the default Guard at the app files root.
// It may be called again with the same root harmlessly; a
// different root replaces the guard (a new app context).
func Configure(root string) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultGuard.Root() == root {
		return
	}
	defaultGuard = New(root)
	defaultNoted = false
}

// Configured reports whether a root is set, falling back to the
// ITAK_FILES_DIR environment variable (the SetSystemDir
// pattern) for contexts that configure by environment.
func configured() *Guard {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultGuard.Root() == "" {
		if env := os.Getenv("ITAK_FILES_DIR"); env != "" {
			defaultGuard = New(env)
		}
	}
	return defaultGuard
}

// EnsureProtected fires the trigger on the default Guard. With
// no root configured it reports (false, nil): protection is an
// app-context duty, and non-app callers have no saves tree.
func EnsureProtected() (bool, error) {
	g := configured()
	if g.Root() == "" {
		defaultMu.Lock()
		if !defaultNoted {
			defaultNoted = true
			log.Printf("backup-hold: no app files root configured; protection skipped (non-app context)")
		}
		defaultMu.Unlock()
		return false, nil
	}
	return g.EnsureProtected()
}

// HoldActive reports the default Guard's hold state.
func HoldActive() bool { return configured().HoldActive() }

// NoteManualSave ends the default Guard's hold.
func NoteManualSave() { configured().NoteManualSave() }

// EndHold ends the default Guard's hold for a stated reason.
func EndHold(reason string) { configured().EndHold(reason) }

// BackupPath returns the default Guard's backup path.
func BackupPath() string { return configured().BackupPath() }

// Reset clears the default Guard. It exists for tests and for
// an app context that is torn down completely; production code
// never calls it.
func Reset() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultGuard = New("")
	defaultNoted = false
}
