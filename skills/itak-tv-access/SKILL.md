# iTaK Gaming: David's Philips TV Access (David Patton)

How to reach, inspect, and safely drive the iTaK Gaming app on
David's home television, the project's home bench. Read this
before any TV session. Device inventory lives in the itak-gaming
repo, field-manual branch, DEVICES.md.

## The device

- Philips 4K TV ("Philips 4K A1", device queenstown, Funai,
  MediaTek m7332). Android 11, armeabi-v7a only, quad Cortex-A53
  at 1.55 GHz, Mali-G52, about 1.7 GB RAM.
- It is a bench, not the stopwatch: roughly 10% different muscle
  from the Missouri stick. Early warnings from here are real;
  final full-speed verdicts come from the stick itself.
- The TV is in David's living room. Liz may be asleep in the
  house. Treat it like somebody's home, because it is.

## Connection recipe

- All adb runs from ubuntu-server:
  `ssh -F /home/hatch/.ssh/ts/config ts-server`
- On the server: `export HOME=/home/server`, adb at /usr/bin/adb.
- Endpoint: `adb connect 192.168.0.122:5555` (usually already
  connected). The address is DHCP and can change. If connect
  fails: check the server LAN (`ip neigh`, `arp -a`), the UniFi
  controller, or the TV's own network settings, then update the
  address in this skill and in DEVICES.md the same session.
- Health check before anything else:
  `adb -s 192.168.0.122:5555 shell dumpsys power` (want
  mWakefulness=Awake) and `getprop sys.boot_completed` (want 1).

## Etiquette law (no exceptions)

1. **Screenshot first.** Before any input, capture the screen and
   look at it. A black frame or a movie/show playing means hands
   off: do nothing but watch passively or leave, unless David
   has called this window himself.
2. **Verify the foreground immediately before EVERY input run**,
   not once per session. A TV can switch itself to a channel or
   screensaver between runs (it happened 2026-10-24: keys meant
   for the app landed in a movie channel's UI).
3. **Silent installs are fine anytime:** `adb install -r <apk>`
   does not steal focus or disturb playback. Proven practice
   through 1.79, 1.80, 1.81.
4. **Never change TV settings** (picture, stay-awake, volume)
   without David's word. Stay-awake is 0 on this TV on purpose.
   Volume was set to 6 of 100 at his word on 2026-10-24 and
   stays there until he says otherwise.
5. Leave the app parked at its home screen with games and saves
   untouched when you finish a proof run.

## Evidence: screenshots only

- `screenrecord` on this TV records pure black (MediaTek MMA
  quirk). Never use it. Use screencaps:
  `adb shell screencap -p /sdcard/<name>.png`, then pull.
- A screencap fails transiently now and then ("failed to stat
  remote object"). Retry once or twice before concluding
  anything; check `adb devices` state if it persists.
- A 10,608-byte PNG is the known pure-black frame.
- Pull caps to ~/workspace/itak-stick-live/ so David can see them.

## Driving the app

- Keyevent codes: CENTER 23, DOWN 20, UP 19, LEFT 21, RIGHT 22,
  BACK 4, START 108, A-button 96.
- **Game input needs long presses:** `adb shell input keyevent
  --longpress 108`. Short synthetic presses (~50-100 ms) are
  unreliable through the app's key-state polling and prove
  nothing when they fail. Menu navigation takes normal presses.
- Verify focus by screenshot at every navigation step. Never
  blind-navigate a list; misfires land on the wrong game
  (learned 2026-10-23, an SMW attempt opened NES Zelda).
- Exit path: BACK opens the pause menu, choose Exit Game,
  confirm, land on the app home. The pause menu and dialogs are
  app-themed dark since 1.78.

## If the library shows "No games"

The files can be present while the app lacks file access (fresh
install). Fix from the server:
`adb shell cmd appops set --uid net.itak.itak_gaming MANAGE_EXTERNAL_STORAGE allow`
plus `adb shell pm grant net.itak.itak_gaming android.permission.READ_EXTERNAL_STORAGE`,
then relaunch and verify the tile counts against DEVICES.md.

## Current state (2026-10-25)

- App 1.81 installed (silent install, dumpsys verified).
- Debug chute key 2797855e36f2, label "David home TV".
- TV storage: about 2.7 GB free of 11.7 GB (2026-10-23).
- Owed on this device: the 1.81 listen test (SNES Zelda file
  select by ear plus cushion logcat lines), in a window David
  calls.
