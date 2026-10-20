# IDEA: iTaK Gaming

David's idea capture for iTaK Gaming. When David talks or dictates a thought about this project, it lands here in his words, dated, newest first, with a one-line note of what it would take. An idea leaves this file only by moving into TODO.md (marked MOVED with the TODO phase) or by David killing it (marked KILLED with the date). Nothing is silently dropped.

## 2026-10-15
- Add crash reporting: the app posts crash and log data to the service, the service files it as a GitHub issue, and no GitHub token lives inside the app.
  - Would take: a small crash intake endpoint, deduplication, and a forced-crash proof.
  - Status: MOVED to TODO Phase 1.
- Add a living home screen: weather from Open-Meteo, a weather-reactive scene, a readout, and an idle screensaver that reuses the scene with a clock.
  - Would take: city-level location choice, a weak-chip frame budget, and device proof.
  - Status: MOVED to TODO Phase 1.
- Home screen editing still needs David's pick between auto-hide for locked and empty systems and a full edit mode.
  - Would take: one decision, then a settings module with its own tests.
  - Status: NEW, parked in TODO Phase 0.

## 2026-10-12
- "make the Sega core out of golang and see what happens."
  - Would take: a clean-room Go Genesis core from published hardware documents, then the same treatment for the other systems.
  - Status: MOVED to TODO Phase 2.
- Use a public lobby system, not Tailscale, so David in Kentucky can play his brother in Missouri.
  - Would take: a rendezvous server, lockstep input sync, passwords, spectators, and a real interstate acceptance game.
  - Status: MOVED to TODO Phase 4.
- Build the arcade room idea: spectators can watch a lobby, with room controls and tournament play around the match.
  - Would take: spectator protocol, seat controls, scoreboard service, and TV-first room screens.
  - Status: MOVED to TODO Phase 4. Spectator watching is live; the remaining room work stays open there.
- Monetize with banner ads and no pop-ups.
  - Would take: an exact license manifest for every core in the build before any ad ships.
  - Status: MOVED to TODO Phase 6.
- Never serve or stream games to other people from the server.
  - Would take: keep the server to lobbies, signals, inputs, stats, friends, and updates only.
  - Status: MOVED to TODO Phase 0 as a standing boundary.

## 2026-10-11
- Make iTaK Gaming open source under MIT and ship no ROMs and no core binaries in the repo.
  - Would take: the LICENSE file, ignore rules, and a leak check before public pushes.
  - Status: MOVED to TODO Phase 0.

## 2026-10-10
- Make the arcade a family-wide product called iTaK Gaming, not only a gift build for Mary and Terry.
  - Would take: a TV-first family interface, separate platform builds, per-player saves, and self-updates.
  - Status: MOVED to TODO Phases 1 and 3.
