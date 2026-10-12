# iTaK Gaming — Missouri Setup Kit

Everything needed to set up the arcade box at Mom's house in Missouri,
plus the short list of things to do to THIS box (the Death Star
Chromecast) before it travels.

## What's in the kit

- `iTaKGaming-<version>.apk` — the app installer (also updates itself
  afterwards, see Updates).
- This file.
- The ROM folders to copy onto the box (David supplies these from
  `C:\ROMs` on his PC; they are not in the public repo on purpose).

## Before the box leaves Kentucky (do at David's house)

1. **Disable Tailscale key expiry for the Chromecast** in the
   Tailscale admin console (Machines → Death Star → Disable key
   expiry). If the key expires in Missouri, remote support access
   dies with it.
2. Make sure the box is on the latest build: open iTaK Gaming,
   Settings → App updates. The footer on the Home screen shows the
   version (v1.0.40 or newer).
3. Confirm player profiles are set up (David at minimum) and the
   controller pairs and is assigned to a player (Players → pick a
   player → Assign controller → press a button on the pad).
4. Copy the ROM folders over (below) and open each system once to
   check the games show up.

## Setting up the box in Missouri

1. Plug the Chromecast into the TV (HDMI + USB power). Connect it to
   the house Wi-Fi.
2. If the app is not already on the box: put the kit APK on a USB
   stick or download it on the box, open it, and allow the install.
   The first time the app updates itself, Android asks once to allow
   installs from iTaK Gaming — tap Allow (one time per box).
3. ROMs live on the box at `/sdcard/ROMs/` in one folder per system:
   `NES`, `SNES`, `Genesis`, `GameBoy` (.gb/.gbc), `GBA` (.gba),
   `PS1` (.cue/.iso), `Dreamcast`. Drop the files in with any file
   manager (or `adb push`), then reopen the app; counts on the Home
   screen show what it found.
4. PlayStation and Sega CD games also want their BIOS files (the
   console's own firmware, supplied by the player) in
   `/sdcard/iTaK/system/`. Without one, PlayStation still runs most
   things on its built-in fallback BIOS.
5. Pair the Bluetooth controller in Android TV settings, then assign
   it to a player inside the app (see step 3 above).

## Playing online (Kentucky ↔ Missouri)

- Both boxes need internet. Open the Online tab: one side taps
  Create lobby, picks the game, optionally sets a password; the
  other side taps the lobby in the list. Both players must own the
  same game file.
- You land in a waiting room that shows both players. Each player
  taps READY; when both are ready there is a 5 second countdown and
  the game starts, in sync on both TVs.
- Pausing is safe: opening the menu tells the other side, the game
  waits, and their screen shows who paused. Closing the menu picks
  right back up.
- Cheats and save/load are off during online games (both sides must
  match exactly); your play time still counts on the leaderboard.

## Updates

The app updates itself. New builds are published to
`https://arcade.itak.live` and every box offers the update on its
Home screen ("Update now"), or check manually in Settings → App
updates. Nobody needs this kit again for updates.

## If something misbehaves

- Settings → Logs → Save a copy writes the debug log to
  `/sdcard/iTaK/`. Send that file to David; it records what the app
  did, including the emulator.
- Do NOT factory reset the box (it wipes the app, the ROMs, the
  saves and the wireless debugging pairing).
- The box's name on the network: Death Star, 192.168.0.247 on
  David's LAN (Missouri will give it a different address; Tailscale
  is the stable way in).

## The gift

This box is the family arcade: David in Kentucky, his brother and
Mom in Missouri, same game, same screen, over the internet. It
started as a surprise for Mary and Terry and grew into iTaK Gaming,
open source at https://github.com/David2024patton/itak-gaming
(the repo ships no games and no console firmware; those live on the
boxes, where they belong).
