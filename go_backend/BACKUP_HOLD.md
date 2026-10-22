# Backup-and-Hold: design note

The save-state audit (~/itak-rust-bridge/SAVE_STATES.md) found
the switch's data-loss trap: the app has no battery files, each
saves/<player>/<game>.sav holds the core's full state blob, a
failed load is graceful, but auto-save (on by default) overwrites
that same file every 60 seconds and again on exit. After a core
swap the old blob fails to load and the only copy of the player's
progress is silently overwritten within about a minute. This
feature is the adopted countermeasure. It lives in the staged
switch backend (~/itak-switch-backend) plus one staged Kotlin
file (~/itak-switch-app). Nothing here touches ~/itak-repo.

## Trigger

The trigger fires inside core.LoadRust (internal/core/
native_rust.go), at the one moment a Rust core is known to be
about to serve: the adapter library opened, its ABI magic and
major version checked, a handle created, and its system id
accepted. Construction failures never fire it. The pure-Go path
(LoadNative) never calls it, and neither does the desktop .so
bridge. LoadRust's signature has no error return, so a protection
failure is deferred to LoadGame, the same surface construction
failures already use: the app sees the load fail, logs why, and
no Rust game ever runs unprotected.

Once per root, not once per launch: a completed protection
writes a versioned marker (see below). Later launches and later
switch builds find the marker, log that protection already
exists, and do not back up again, do not hold again.

Contexts with no app files root configured (desktop harnesses,
core tests) skip protection inside switchguard with a one-line
log: they have no saves tree to protect. The app always
configures its root before loading a core (staged Kotlin edit
1), so a shipped switched build cannot reach the Rust path
unconfigured.

## Backup

Location rule, derived from the app's storage layout
(EmulatorActivity.kt: filesDir/saves/<player>/<game>.sav, plus
the legacy filesDir/saves/<game>.sav):

- Live tree: <filesDir>/saves, never written by this feature.
- Backup: <filesDir>/saves-backup-before-rust, a verbatim copy
  of the whole saves tree (every regular file, relative paths
  preserved, byte-identical). No code path writes to it after
  creation; there is no restore, prune, or rotation code.
- If that name is already taken (an earlier backup, or anything
  else) it is never overwritten: the copy goes to
  saves-backup-before-rust-2, then -3, and so on.
- Marker: <filesDir>/switchguard/marker.json, written only
  after the copy completes, atomically (tmp file plus rename).
  Format carries a version field (1) plus created time, backup
  dir name, file count, and total bytes.
- A partial copy is removed on failure, so a half backup can
  never be mistaken for a complete one on the next attempt.
- A fresh install with no saves tree still protects: an empty
  backup plus a marker recording zero files.

## Hold

Protection starts the hold in the same call. While the hold is
active, mobile.AutoSaveState returns nil, and the activity's
two automatic saves (the 60 second timer and the exit save)
both route through AutoSaveState in the staged Kotlin, so the
auto-save path writes nothing into the live saves tree. Manual
saves are never blocked: they use Mobile.saveState as before.

The hold ends at the first of:

- the player saves manually once (Kotlin doSave calls
  Mobile.noteManualSave after its write lands), or
- the session ends cleanly (mobile.Close calls
  switchguard.EndHold).

The hold is process state only. It is never written to the
player's stored auto-save preference (the itak_prefs "autosave"
key is read by the activity exactly as before and written by
nobody in this feature), and gate tests prove the stored
preference survives a full cycle byte-identical. The exit
dialog asks Mobile.backupHoldActive so it does not promise an
auto-save while the hold is on.

Why a one-session hold is enough: the backup is the durable
protection. The hold covers the audited danger window, the
first session, where a player whose old save failed to load
would otherwise lose it before ever deciding to save by hand.
From the second session on, the pre-switch saves still exist
in the backup even if the live slot has moved on.

## Failure behavior

- Backup copy fails (unreadable saves tree, unwritable root,
  marker write fails): EnsureProtected returns an error naming
  the failure, LoadRust converts it into a deferred LoadGame
  error, and the log carries "backup-hold: refusing Rust core
  <name>: backup-hold: backup failed, Rust core refused: ...".
  No marker, no hold, no partial backup left behind.
- Marker exists but does not parse, or carries a foreign
  version: treated as a failure, never as "already protected".
  Guessing here could skip a needed backup.
- Pure-Go builds: no trigger, no backup, no hold, never blocked.

## Diagnostics

Every step goes through the standard log package with the
"backup-hold:" prefix (the backend's diagnostic channel; the
bridges log the same way, and it reaches logcat and the app's
log screen): root configured, trigger, backup source and
destination, files and bytes copied, backup complete, hold
start, every skipped auto-save, hold end with its reason,
already-protected notices, and refusals with their cause.

## Files assembly must take

Backend (from ~/itak-switch-backend):

- internal/switchguard/switchguard.go (new package)
- internal/switchguard/switchguard_test.go (new)
- internal/core/native_rust.go (trigger wiring in LoadRust,
  one import)
- internal/core/backup_hold_test.go (new)
- mobile/backup_hold.go (new: SetFilesDir, BackupHoldActive,
  NoteManualSave, AutoSaveState)
- mobile/backup_hold_test.go (new: the live drill)
- mobile/mobile.go (Close ends the hold; one import)
- BACKUP_HOLD.md (this note)

App (from ~/itak-switch-app, a copy of flutter_ui; the only
changed file is android/app/src/main/kotlin/net/itak/
itak_gaming/EmulatorActivity.kt, five edits):

1. onCreate: Mobile.setFilesDir(filesDir.absolutePath)
   immediately before Mobile.loadCore, beside setSystemDir.
2. The 60 second auto-save calls Mobile.autoSaveState()
   instead of Mobile.saveState().
3. autoSaveNow (the exit save) calls Mobile.autoSaveState().
4. doSave calls Mobile.noteManualSave() after its write.
5. confirmExit checks Mobile.backupHoldActive() for its
   message text.

No Dart change, no pubspec change, no new permissions. The
gomobile names surface in Kotlin exactly as written above
(setFilesDir, autoSaveState, noteManualSave, backupHoldActive),
matching the existing setSystemDir / saveState pattern.

## Gate record (2026-10-22, this host)

- go test ./... : all packages pass, same as the pristine
  baseline run taken before the changes.
- Targeted gates: switchguard 6 tests (byte-identical backup;
  second run leaves the backup untouched and starts no hold;
  backup failure refuses with no marker and no partial backup;
  stored preference unchanged across a full cycle and the hold
  ends on manual save; existing backup without a marker gets a
  sibling; empty tree still protects). Core 3 tests (pure-Go
  path creates nothing and is never blocked; failed Rust
  construction creates nothing; real adapter plus unmakeable
  backup refuses at LoadGame naming the failure).
- Live drill (mobile TestBackupHoldDrill, pilot configuration):
  a real Go-produced tobutobugirl state at frame 300 seeded as
  saves/Player 1/tobutobugirl.sav; the Rust Game Boy adapter
  served through production LoadCore and NativeBridge; backup
  byte-identical (2 files, 150,791 bytes); AutoSaveState nil
  during the hold with the live tree untouched while manual
  SaveState returned bytes; the Go state loaded into the Rust
  core and its frame-900 continuation state was byte-identical
  to the Go core's own frame-900 state; hold ended on the
  manual save; preference bytes unchanged.
- Race: go test -race on internal/switchguard, internal/core,
  mobile, internal/save: clean.
- go vet ./... clean; gofmt clean on all touched files.
- The env-gated production bridge test (TestRustBridgeAdapter,
  ITAK_RUST_TEST_SO pointed at the current adapter build)
  passes with the trigger wired in.

## What assembly still owes

- Ship the staged Kotlin file with the switched build. A
  switched build without edit 1 never configures the root and
  the backend then skips protection as a non-app context; the
  pairing is the safety property.
- Device proof, when a stick is reachable: seed an old-format
  save, install the switched build, confirm the backup folder
  on disk and the hold lines in the app log, and confirm the
  second launch creates nothing new.
- A restore path for the backup is deliberately not built: the
  backup is a safety copy a human can inspect or copy back by
  hand. Building restore UI is a separate decision.
