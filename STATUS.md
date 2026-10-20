<!--
INSTRUCTIONS FOR THIS FILE (user and AI: read before editing. Keep this header.)

WHAT THIS FILE IS: the living right-now snapshot of this project. Anyone
opening the repo cold reads this first and is current in two minutes.
Updated at the end of every work session on the project; a stale STATUS
is worse than none, so when you finish work here, update it in the same
pass.

EXACT STRUCTURE:
- "## As of <YYYY-MM-DD>": the date this snapshot was written.
- "## Proven state": what is done, with its proof named.
- "## In flight": work currently underway and where it stands.
- "## Blocked": each blocker and exactly who or what it waits on.
- "## Next moves": the next three moves, in order, referencing TODO ids.

RULES:
- Claims here carry the same proof standard as TODO checkboxes.
- Keep it short: a snapshot, not a history (history is CHANGELOG.md).
- No secrets, passwords, or tokens in this file, ever.
-->
# STATUS: iTaK Gaming

## As of 2026-10-20

## Proven state
- Open source under MIT, shipping no ROMs and no core binaries in the repo (proof: `LICENSE` on main; README rule and `.gitignore`, TODO T-0.1 and T-0.2).
- Version 1.62 (build 63) published to the update feed; the public APK hash matched the published build (proof: feed verification 2026-10-17, TODO T-3.1).
- Family stick with build 1.61 aboard is in the mail to Mary and Terry in Missouri; build 1.62 waits on the update feed for it on arrival (proof: project record 2026-10-15 to 2026-10-17, TODO T-3.2).
- Flutter TV shell, Go backend host, clean-room Go cores, public lobby with lockstep inputs, spectators, and self-update flow are built and tested in the tree (proof: TODO Phase 1, 2, and 4 checked items with named proofs).
- Repo standard set complete: TODO, IDEA, PLAN, ENDGOAL, docs/RULES.md, plus this jump-back-in set (proof: files on main).

## In flight
- Core proof packs, benchmarks on the Chromecast HD, and backend route reconciliation (TODO T-2.4 to T-2.8).
- Repo standard jump-back-in backfill (this file set) landing 2026-10-20.

## Blocked
- The Kentucky to Missouri acceptance game between David and his brother (waiting on: both players at their boxes, TODO T-4.5).
- In-person Missouri setup at Mary and Terry's house (waiting on: travel and hands at the destination, TODO T-5.3).
- Home screen editing model choice (waiting on: David, TODO T-0.7).

## Next moves
1. Add the per-build core manifest with licenses and revenue eligibility (TODO T-0.5).
2. Run the tree leak check script before the next public push (TODO T-0.6).
3. Split flutter_ui/lib/main.dart into feature modules, one move at a time (TODO T-1.5).
