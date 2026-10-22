// Backup-and-hold surface for the app. The Kotlin activity
// calls SetFilesDir before LoadCore (the SetSystemDir pattern),
// asks BackupHoldActive for its exit messaging, routes its
// automatic saves through AutoSaveState, and reports manual
// saves through NoteManualSave. All state and all enforcement
// live in internal/switchguard; this file is only the
// gomobile-shaped doorway, plus the one behavior that belongs
// to this layer: an automatic save during the hold produces no
// bytes, so the Kotlin write that follows it cannot happen.
package mobile

import (
	"log"
	"os"

	"github.com/davidpatton/itak-gaming/go_backend/internal/switchguard"
)

// SetFilesDir points backup-and-hold at the app's files
// directory, the directory that contains saves/. Call before
// LoadCore. It mirrors SetSystemDir: the path is also kept in
// the environment so any layer that asks switchguard later in
// the process sees the same root.
func SetFilesDir(path string) {
	_ = os.Setenv("ITAK_FILES_DIR", path)
	switchguard.Configure(path)
	if path != "" {
		log.Printf("backup-hold: app files root set to %s", path)
	}
}

// BackupHoldActive reports whether auto-save is held for the
// first switched session. The activity uses it only for what
// it tells the player; enforcement does not depend on it,
// because AutoSaveState already returns nothing while held.
func BackupHoldActive() bool {
	return switchguard.HoldActive()
}

// NoteManualSave reports that the player saved by hand. That
// is one of the hold's two safe ends (the other is a clean
// session end, reported by Close): the live slot now holds a
// state the current core wrote on purpose.
func NoteManualSave() {
	switchguard.NoteManualSave()
}

// AutoSaveState returns the serialized state for an automatic
// save (the 60 second timer and the exit save in the
// activity), or nil while the backup-and-hold hold is active.
// The activity treats nil as "nothing to write", exactly like
// a failed SaveState today, so during the hold the auto-save
// path writes nothing into the live saves tree. Manual saves
// keep using SaveState and are never blocked.
func AutoSaveState() []byte {
	if switchguard.HoldActive() {
		log.Printf("backup-hold: auto-save skipped: hold active for the first switched session")
		return nil
	}
	return SaveState()
}
