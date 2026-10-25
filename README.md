# iTaK Gaming Field Manual

This branch is the pickup point. Everything the project has tried,
what worked, what did not, what is being built right now, and what
the end goal is, written so anyone (David, lulu, or a fresh agent
with no memory of the work) can start cold from here and be current
in one read.

## Read in this order

1. **ENDGOAL.md** — what this project is and what finished looks like.
2. **NOW.md** — what is in flight right now, what is owed, and which
   decisions are waiting on David.
3. **TRIED.md** — everything tried, by area: what worked, what
   partially worked, what failed, and the dead ends not to retry.
4. **DEVICES.md** — every machine and device in the loop, with specs
   and current state.
5. **REFS.md** — the reference repos we mine, the license law, and
   where the evidence reports live.

## Where things stand (2026-10-25)

- Latest shipped build: **1.81** (versionCode 82), live on the
  update feed at https://arcade.itak.live/app/version.json
- Both benches are on 1.81: David's Philips TV and the Missouri
  stick (Mary and Terry's Chromecast HD, "Death Star").
- Six systems ship in the 1.x line (Game Boy, NES, SNES, Genesis,
  GBA, PS1), all running from-scratch Rust cores. Six more cores
  are built (DS, N64, Saturn, GameCube, PSP, Dreamcast) and wait
  on a placement decision.
- The running log lives on the **work-notes** branch of this repo
  (notes/DONE.md, notes/TODO.md). This branch is the manual; that
  branch is the diary. The deep evidence lives on the server in
  ~/itak-speed/ (see REFS.md).

## The laws that never bend

- **Fidelity contract:** the cores match real hardware provably
  (pixel-exact frames, byte-exact save states, million-instruction
  hardware test suites). Speed work never trades accuracy away
  unless David says so in words.
- **Never reboot ubuntu-server.** It serves live business websites.
  Any fix that claims to need a reboot gets the manual no-reboot
  path or stops.
- **No ROMs in the repo, ever.** Test ROMs are local copies only,
  never committed, never distributed.
- **Prove it on the real device.** Server numbers are predictions.
  A build is proven when it has run on the actual stick or TV with
  evidence in hand.
- **TV etiquette:** screenshot first, verify the foreground before
  every input run, and if a picture is playing, hands off unless
  David calls the window.
