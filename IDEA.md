# IDEA: iTaK Gaming

David's idea capture for iTaK Gaming. Every idea lands here as a full record, newest first, kept in categories. An idea leaves active status only by moving into TODO.md (status MOVED, with the TODO item id) or by David killing it (status KILLED, with the date). Nothing is silently dropped. Entries carried over from the v1 format keep their recorded date; times are added only where David's message carried one. David's words are quoted only where his exact words are on record; everything else is marked as a summary.

## Feature | 2026-10-15
- **David:** Add crash reporting: the app posts crash and log data to the service, the service files it as a GitHub issue, and no GitHub token lives inside the app. (summary)
- **Lulu:** (summary) Agreed shape: a small crash intake endpoint with deduplication, and a forced-crash proof before it counts as done.
- **Stats:** would take: a crash intake endpoint, deduplication, and a forced-crash proof; effort: M; value: crashes on family TVs report themselves instead of being described over the phone; depends on: the netplay service.
- **Status:** MOVED to TODO T-1.8.

## Feature | 2026-10-15
- **David:** Add a living home screen: weather from Open-Meteo, a weather-reactive scene, a readout, and an idle screensaver that reuses the scene with a clock. (summary)
- **Lulu:** (summary) Spec recorded the same day. The constraint is the weak-chip frame budget on the Chromecast HD; the scene and screensaver share one rendering path.
- **Stats:** would take: city-level location choice, a weak-chip frame budget, and device proof; effort: M; value: the home screen feels alive instead of static; depends on: the TV shell modules.
- **Status:** MOVED to TODO T-1.7.

## Design | 2026-10-15
- **David:** Home screen editing still needs a pick between auto-hide for locked and empty systems and a full edit mode. (summary)
- **Lulu:** (summary) Still David's open call. It sits in Phase 0 as a decision item until he picks; both shapes are cheap once chosen.
- **Stats:** would take: one decision, then a settings module with its own tests; effort: S after the decision; value: the home screen shows only what each household uses; depends on: David's pick.
- **Status:** NEW, carried as decision item TODO T-0.7.

## Tech | 2026-10-12
- **David:** "make the Sega core out of golang and see what happens."
- **Lulu:** (summary) Taken as the core strategy for the whole project: clean-room Go emulator cores written from published hardware documents, one module per system, each proven on its own.
- **Stats:** would take: a clean-room Go Genesis core from published hardware documents, then the same treatment for the other systems; effort: L; value: the arcade owns its cores outright, with no license blockers; depends on: published hardware documentation per system.
- **Status:** MOVED to TODO T-2.2.

## Product | 2026-10-12
- **David:** Use a public lobby system, not Tailscale, so David in Kentucky can play his brother in Missouri. (summary)
- **Lulu:** (summary) The lobby was built on that call and the online stack was proven machine to machine on 2026-10-13. The real family game is still the acceptance test.
- **Stats:** would take: a rendezvous server, lockstep input sync, passwords, spectators, and a real interstate acceptance game; effort: L; value: couch play across states with no VPN and no game data on the server; depends on: the netplay service.
- **Status:** MOVED to TODO T-4.1; acceptance game is T-4.5.

## Feature | 2026-10-12
- **David:** Build the arcade room idea: spectators can watch a lobby, with room controls and tournament play around the match. (summary)
- **Lulu:** (summary) Spectator watching shipped in build 47 with live match view and chat. Seat controls and the tournament scoreboard are the remaining room work in Phase 4.
- **Stats:** would take: spectator protocol, seat controls, scoreboard service, and TV-first room screens; effort: L; value: the whole family can be in the room, not just two players; depends on: the lobby.
- **Status:** MOVED to TODO T-4.3 and T-4.4 (watching live); remainder T-4.6.

## Business | 2026-10-12
- **David:** Monetize with banner ads and no pop-ups. (summary)
- **Lulu:** (summary) Banner ads only, and only per build: the Snes9x and Genesis Plus GX non-commercial license blocker had to be cleared build by build, and the release gate checks the exact core manifest before any ad ships.
- **Stats:** would take: an exact license manifest for every core in the build before any ad ships; effort: M; value: revenue without breaking core licenses or the family experience; depends on: the per-build manifest, TODO T-0.5.
- **Status:** MOVED to TODO T-6.6.

## Product | 2026-10-12
- **David:** Never serve or stream games to other people from the server. (summary)
- **Lulu:** (summary) Recorded 2026-10-12 as a hard scratch: server-side game delivery is out over lawsuit risk. The server carries lobbies, signals, inputs, stats, friends, and updates only, and the boundary stands in PLAN S10.
- **Stats:** would take: keeping the server to rendezvous and relay duties only; effort: S (a boundary, not a build); value: the project never becomes a game distribution service; depends on: none.
- **Status:** MOVED to TODO T-0.3 as a standing boundary.

## Business | 2026-10-11
- **David:** Make iTaK Gaming open source under MIT and ship no ROMs and no core binaries in the repo. (summary)
- **Lulu:** (summary) David's call on 2026-10-11. The repo went public under MIT the same day, with ignore rules and a leak check as the standing guard.
- **Stats:** would take: the LICENSE file, ignore rules, and a leak check before public pushes; effort: S; value: the family project is shareable and legally clean; depends on: none.
- **Status:** MOVED to TODO T-0.1 and T-0.2.

## Product | 2026-10-10
- **David:** Make the arcade a family-wide product called iTaK Gaming, not only a gift build for Mary and Terry. (summary)
- **Lulu:** (summary) The gift build for Mary and Terry grew into the family product: TV-first interface, separate builds per platform, per-player saves, and self-updates.
- **Stats:** would take: a TV-first family interface, separate platform builds, per-player saves, and self-updates; effort: L; value: one arcade for the whole family on every screen; depends on: the platform build matrix.
- **Status:** MOVED to TODO T-1.1 and Phase 3 (T-3.1 to T-3.7).
