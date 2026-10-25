# What We Are Doing Right Now

State as of Sunday 2026-10-25, about 03:15 EDT.

## Just finished: the audio cushion program (1.80, 1.81)

David reported the Super Nintendo Zelda file-select screen tearing
its sound on his TV. Diagnosis (server repro rig): the core's audio
is clean; the TV runs those heavy screens a few percent over the
frame budget, so sound production falls behind real time and the
buffer starves. Fix: an adaptive audio cushion in the shared audio
path (a streaming resampler that stretches sound slightly while
frames overrun, bit-exact pass-through when they do not).

- 1.80 shipped the cushion tuned on SNES.
- David ordered it perfected on every engine, in parallel. Six
  verification passes ran (one per core), then one integration
  matrix (8 candidate tunings x 27 hard checks) picked the winner:
  LEAD_TARGET 1800, LEAD_BAND 300, STRETCH_MAX 1.08. 1.81 shipped
  2026-10-24, verified end to end (feed, APK hash, dissection:
  only the version stamp and the cushion code differ from 1.80).

Verdicts by engine: SNES fine and re-proven (PAL included), GBA
fine (never engages in real play), NES and Game Boy needed the
retune (at their 44.1 kHz rate the old band false-engaged during
healthy play and quietly resampled clean sound), PS1 needed the
higher stretch ceiling for its 5 percent deficit case, Genesis is
a coverage gap (see Open decisions).

## Fresh evidence (2026-10-24 evening, via the debug chute)

The Missouri stick is on 1.81 (it updated itself from the feed)
and somebody played SNES on it for ten straight 600-frame windows:
heavy screens 16.9 to 17.8 ms, later windows 12.2 to 12.6 ms.
That is the current core on real stick hardware: at or near budget
on the heaviest screens, full speed with headroom after.

## Still owed (proofs, not builds)

1. **The 1.81 listen test on David's TV:** Zelda file select by
   ear, plus logcat showing the cushion engaging and releasing,
   and no cushion engagement during healthy NES or Game Boy play
   (the new purity signature). Waits for a window David calls.
2. **The NES verdict on the Missouri stick:** Terry's first NES
   Zelda session on the 1.75-or-later core. Predicted 13.4 to
   16.2 ms. The chute carries the timing home on its own.
3. **Genesis Sonic timing on the stick** with the current core.
   This number now carries a hard line: at or under 17.69 ms per
   frame sustained, the shipped cushion covers Genesis sound
   completely; above it, Genesis needs a speed decision.

## Open decisions (David's words required)

- **Genesis full-speed direction:** (1) pre-read/decoded execution
  engine, (2) multithreading at exact boundaries, (3) a switchable
  faster, less exact mode. Option 3 was tapped by accident on
  2026-10-23 and retracted in words. Nothing is started.
- **SNES speed headroom:** the same heavy screens still run over
  budget on device; the cushion hides the sound cost, the picture
  still runs a few percent slow there. Paths not started.
- **Rust source placement:** twelve cores and twelve adapters are
  complete and ship as prebuilt libraries; their sources are not
  in GitHub yet. Expected home: cores/rust-<slug> in this repo.
- **Restore UI** for the save backups the app already makes.
- **One-press auto update** (download in background, one OK at the
  system installer). Proposed, not approved.
- **Fresh-TV storage prompt:** the app should ask for file access
  itself instead of showing an empty library (proven pain on
  David's TV; fixed there by hand). Small build, not green-lit.
- **Home screen editing** (auto-hide empty systems vs full edit
  mode) and the **living weather home screen** (2026-10-15
  requests, not built).
- **Go defect ledger:** 15 documented Go-core defects are
  deliberately unreplicated-or-replicated per the ledger; fixing
  the Go side is David's call. (Number 16, the SNES joypad swap,
  was fixed in both cores in 1.79.)
- **Monetization** (banner ads, no pop-ups, was David's lean) and
  **voice chat microphone** hardware. Undecided.

## The fleet

- Update feed: https://arcade.itak.live/app/version.json
- David's TV: 1.81 installed. Missouri stick: 1.81, reporting
  through the chute every 15 minutes.
- Pipeline: pushes to main rebuild automatically (about a
  5 minute cycle) when pubspec's versionCode rises. Pubspec is
  always pushed last.
