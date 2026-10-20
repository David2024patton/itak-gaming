<!--
INSTRUCTIONS FOR THIS FILE (user and AI: read before editing. Keep this header.)

WHAT THIS FILE IS: the operations book for this project, at docs/RUNBOOK.md.
How to build it, run it, deploy it, verify it, and where everything lives,
so anyone can operate the project without tribal knowledge.

EXACT STRUCTURE:
- "## Build": the real commands, copied from the working build.
- "## Run locally": how to start it and what you should see.
- "## Deploy": where it is hosted, how a deploy happens, how to roll back.
- "## Verify": the checks that prove it works after a change or deploy.
- "## Where things live": hosts, services, dashboards, and WHERE
  credentials are stored (names of vault entries or connectors only).
- "## Gotchas": the traps already learned the hard way, each with its fix.

RULES:
- NEVER a credential value in this file: no passwords, keys, or tokens.
  Pointers to secure storage only.
- Commands are copied from what actually works; if a command changes,
  this file changes in the same commit.
- No secrets, passwords, or tokens in this file, ever.
-->
# RUNBOOK: iTaK Gaming

## Build
```bash
# Go backend and cores
go build ./...
go test ./...

# Flutter app (per platform build; see README platform table)
flutter build apk        # Android builds are split 32-bit and 64-bit on purpose
```

## Run locally
```bash
# Backend host
go run ./cmd
curl http://127.0.0.1:8081/api/systems

# Lobby server
go run ./cmd/lobbyserver
```
The Flutter UI runs on the target device class (TV stick, phone, desktop); navigation must work with TV remote, Bluetooth remote, and keyboard.

## Deploy
- App updates ship through the arcade update feed: publish the APK plus version metadata with its SHA-256; the app offers the update, verifies the hash, downloads, and hands off to the installer.
- Platform builds are separate: Android 32-bit (Chromecast HD class sticks), Android 64-bit, Windows 64-bit, Linux 64-bit, each from the same source revision.
- The netplay lobby deploys from the `netplay/` service per netplay/DEPLOY.md.
- Rollback: point the feed back to the prior version's APK and metadata; devices on the bad build are offered the restored version.

## Verify
- Run the shipped app on the real target device and launch at least one game without returning to the home screen. This is the ship gate (DECISIONS 2026-10-15); build-machine proof alone never clears a build.
- Confirm the published APK hash matches the feed record.
- Confirm saves stay per player (10 slots per player per game) and the lobby health endpoint answers.

## Where things live
- Source: this repo (public, MIT).
- Update feed and download page: David's Downloads at dl.itak.live; Downloader app short code 9661571.
- Lobby: the netplay service (see netplay/DEPLOY.md for its host).
- Credentials: none belong in this repo or in the app. Crash reporting must never embed a GitHub token in the APK (TODO T-1.8).

## Gotchas
- Build 56 crashed on every launch on the real TV after being cleared on the build machine; its clearance was revoked. Never skip the real-device run.
- A weak TV stick does not carry systems it cannot run well; keep the per-platform system sets separate.
- Speed work is profile-first: measure the hot path on the Chromecast HD before optimizing, and never blame the hardware without profile evidence (TODO T-3.4).
- ROMs and core binaries stay out of git; the tree leak check (TODO T-0.6) runs before every public push.
