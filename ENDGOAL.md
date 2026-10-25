# The End Goal

David's words, 2026-10-13: "Make sure everything matches exactly.
We want this to be the best emulator in the world!!!"

## What iTaK Gaming is

An open-source (MIT) game console app for cheap Android TV sticks
and televisions, built from scratch: our own emulator cores, our
own frontend, our own netplay. No RetroArch, no borrowed cores in
the shipped product. It runs the games people already own, from
files on the device, exactly as the original hardware played them.

## What finished looks like

- **Every supported system plays at full speed on stick-class
  hardware** (a $30 Chromecast HD), with picture and sound
  indistinguishable from the real console.
- **Accuracy is provable, not claimed:** pixel-exact frames,
  byte-exact save states that stay interchangeable with the Go
  reference cores, and hardware test corpora passing in the
  millions of instructions per core.
- **Twelve systems:** Game Boy, NES, SNES, Genesis (plus Sega CD),
  GBA, PS1 ship today. Nintendo DS, N64, Saturn, GameCube, PSP,
  and Dreamcast cores are built and join the lineup as the
  product grows into them.
- **Netplay that cannot desync:** two machines, same inputs, same
  state hash, proven machine to machine. Friends and family play
  across states (Kentucky to Missouri is the founding case).
- **It installs and updates itself** from our own feed, keeps
  saves safe across every update (automatic backup before any
  core switch), and reports its own health home through the debug
  chute so problems are visible without touching the device.
- **The 1.x line is 32-bit** (armeabi-v7a), because that is what
  the installed base of sticks and TVs runs. **2.0 is the 64-bit
  edition**, the major-version jump when the 64-bit pipeline ships.

## The standing constraints

- Fidelity first: a faster, less exact mode exists only if David
  orders it in words. (He has not; a stray widget tap on
  2026-10-23 was retracted in words seconds later and does not
  count.)
- No copyrighted games are ever distributed by the app or the
  repo. Players bring their own files; the app reads local
  storage only. Server-side game delivery was considered and
  scratched permanently on 2026-10-12 over lawsuit risk.
- Everything modular: every feature is its own module, one
  feature per commit, pushed when verified.
- Build 20 years ahead: interface seams at every boundary that
  will change (cores behind one bridge ABI, limits as config,
  data formats versioned).
