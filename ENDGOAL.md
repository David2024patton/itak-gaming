<!--
INSTRUCTIONS FOR THIS FILE (user and AI: read before editing. Keep this header.)

WHAT THIS FILE IS: the end state this project exists to reach, in plain
words. TODO.md and PLAN.md serve this file; work continues until the end
goal is reached. When David restates or extends the end goal, update this
file the same day and let PLAN/TODO follow.

EXACT STRUCTURE:
- "## The end state": the finished project described as the experience of
  the person using it, start to finish.
- "## How we will know it is reached": observable conditions, each one
  checkable, tied to the TODO item that proves it where one exists.
- "## Not in the end state": explicit exclusions David has set, so the
  goal cannot drift.

RULES:
- Sourced from David's stated vision, the repo README and features, and
  what he has talked about in the record. Never invented. Where the
  record is thin, say the end state at the level the record supports.
- No secrets, passwords, or tokens in this file, ever.
-->
# ENDGOAL: iTaK Gaming

The end state this project exists to reach, in plain words. TODO.md and PLAN.md are how we get here; this file is how we know we are done. Work continues until this end goal is reached. When David restates or extends the end goal, this file is updated the same day and PLAN/TODO follow.

## The end state

iTaK Gaming is the family arcade on every screen in the house and beyond it: a TV stick, a stronger TV box, a Windows or Linux machine, all running the same open-source arcade. Anyone in the family picks up a remote or controller, picks their profile, picks a system and a game, and plays, with their own save states waiting where they left them. David in Kentucky opens a lobby and plays his brother in Missouri like they are on the same couch, with family watching as spectators. Updates arrive on their own and verify themselves. The box in Mary and Terry's house in Missouri runs the same arcade, set up from the kit, recoverable from a one-page card by a family member with no developer help.

## How we will know it is reached

- The shipped app runs on the real Chromecast HD and every target device, and a game launches and plays without returning to the home screen, proven per build on the device itself.
- The Kentucky to Missouri acceptance game is played over the public lobby with a dated session note: build, game, connection path, desync count, result.
- Every bundled core carries a standard proof pack, and retail-game status is recorded separately from homebrew proof for the 64-bit cores.
- Spectators, friends, play stats, and leaderboards work in the live lobby service over TLS.
- A family member completes setup and a failed-update recovery from the written kit and card without developer help.
- The release gate passes clean on the release commit: tests, lint, secret scan, leak check, save compatibility, and netplay determinism.
- The repo stays clean in public: MIT code, and a tree leak check with every release finds no ROMs, BIOS files, core binaries, private logs, or credentials.

## Not in the end state

- No server-side game delivery or streaming of games to other people, ever. The server carries lobbies, signals, inputs, stats, friends, and updates only.
- No ROMs or core binaries shipped in the repo or the app packages; players supply their own legally obtained games.
- No pop-up ads in any build. Banner ads only in a build whose per-build core license manifest proves every bundled core allows that revenue model.
