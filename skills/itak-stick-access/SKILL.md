# iTaK Gaming: Missouri Stick Access, "Death Star" (David Patton)

How to reach, inspect, and safely work with the iTaK Gaming app
on Mary and Terry's Chromecast in Missouri, the project's ground
truth device. Read this before any stick session. Device
inventory lives in the itak-gaming repo, field-manual branch,
DEVICES.md.

## The device

- Chromecast with Google TV (HD), 2022, device "boreal".
  Amlogic S805X2, quad Cortex-A35 at 1.8 GHz, Mali-G31 MP2,
  about 1.4 GB RAM, Android 14, armeabi-v7a.
- It is Mary and Terry's living-room TV in Missouri. Mary is the
  living Google TV profile; app and data are shared across
  profiles. Terry plays NES Zelda (USA) on it.
- This stick is the stopwatch: final speed verdicts come from
  here and nowhere else.

## The golden rules

1. **NEVER reboot this stick.** Rebooting kills wireless
   debugging and the pairing trust with it. (David's order,
   2026-10-11. lulu has never rebooted it.)
2. **Evidence first, touch last.** The debug chute (below)
   answers most questions with zero contact. Use it before adb.
3. **Screenshot first, always.** Black screen or a show playing
   means hands off unless David calls the window. Family TV.
4. **Silent installs are accepted practice** while the TV sleeps
   (`adb install -r`), proven from 1.69 through 1.75. Anything
   that launches, navigates, or changes settings waits for a
   called window.
5. Storage is tight (452 MB free on 2026-10-25). Check free space
   before any install; a failed half-install here is a bad day.

## The debug chute (the no-touch path)

- Every install pings home every 15 minutes and can be asked for
  a full report. Data lands on ubuntu-server in ~/itak-debug/.
- This stick: key **ca4ee57efe88**, label **Mary**, install code
  begins 10d65df0.
- Read `~/itak-debug/ca4ee57efe88/latest.json` for lastSeen,
  app version, free storage, and recent native timing lines
  (per-core frame times, the passive speed evidence).
- Full reports: newest 20 in the same folder. To request one,
  set wantFullReport for the key in ~/itak-debug/labels.json;
  the stick uploads on its next check-in and the flag clears.
- A landed report refreshes on its own schedule; a stale
  lastSeen (hours) means the stick is asleep or offline, not
  broken.

## ADB access (when the chute is not enough)

- All adb runs from ubuntu-server:
  `ssh -F /home/hatch/.ssh/ts/config ts-server`, then on the
  server `export HOME=/home/server`, adb at /usr/bin/adb.
- The stick is on the tailnet at 100.113.6.86. Its wireless
  debugging endpoint changes ports when it sleeps (sessions in
  Oct 2026 used port 45655). `adb connect 100.113.6.86:<port>`.
- If the current port is unknown and connect fails, do NOT go
  hunting aggressively. Options, in order: wait for chute data,
  or David relays to the household to read the port from
  Settings, System, Developer options, Wireless debugging.
- The display sleeps when idle: send WAKEUP (keyevent 224)
  first, then move promptly.
- Settings on the stick, set deliberately 2026-10-23:
  stay_on_while_plugged_in = 7, development settings on,
  adb wifi on. If Mary or Terry ever report the TV behaving
  oddly, undo stay_on first (set 0) and tell David.

## Saves

- Saves live in the app's private storage and cannot be read
  over adb on a release build. Their protection is in-app:
  backup-and-hold (since 1.66) copies the whole saves tree aside
  before the first session on new cores and holds auto-save
  until a manual save. Proven on this stick 2026-10-23
  (11 files, 6.8 MB).
- Never delete or overwrite anything under the app's data, and
  never clear app storage, on this stick. Ever.

## Current state (2026-10-25)

- App **1.81** (updated itself from the feed; chute-confirmed).
  Free storage 452 MB.
- Library: NES 5, SNES 8, Genesis 7, Game Boy 2, GBA 1 (Anguna),
  PS1 2 entries (the 240p Test Suite files), Sega CD 0.
- Latest play evidence: 2026-10-24 about 19:42 EDT, SNES, ten
  600-frame windows, heavy screens 16.9-17.8 ms settling to
  12.2-12.6 ms (via chute timing lines).
- Owed from this device: Terry's first NES Zelda session on the
  1.75-or-later core (the NES full-speed verdict; predicted
  13.4-16.2 ms), and a Genesis Sonic timing with the current
  core (17.69 ms is the line that settles Genesis sound).
