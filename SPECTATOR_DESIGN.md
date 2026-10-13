# Spectator and big-lobby design (David's arcade room vision, 2026-10-12)

Goal: a lobby stops being two people. Two player seats, a crowd of
spectators, a host who runs the seats, a Got Next line, and later
host-run tournaments with their own leaderboard. This doc is the
skeleton those builds follow.

## What the current code already gives us

- The signaling hub (netplay/internal/server/hub.go) keeps a SET of
  sockets per lobby with no size cap and relays every client message
  verbatim to everyone else in the room. Fan-out to spectators is
  free; nothing in the transport assumes two peers.
- Peers already carry a lobby.Role (host, guest). Spectator is a
  third role, not a new concept.
- The 2-player limit lives in the lobby membership layer
  (internal/lobby, MaxPlayers) and in the client session
  (internal/netsession assumes exactly one peer). Those are the two
  places the work lands.
- Lockstep means a spectator does not need video. It runs the same
  core and ROM locally and applies both players' input streams,
  GGPO style. Input frames are tiny, so ten spectators cost the
  players almost nothing.
- Chat already relays room-wide. Spectators read and send chat with
  no protocol change.

## Protocol changes (all backward compatible)

Old clients ignore unknown JSON fields, and the relay is verbatim,
so every change below is additive. A session_offer gains "v": 2;
a v1 peer answers session_reject and the host falls back politely.

1. Seat-tagged inputs. The input envelope gains "seat": 1 or 2,
   set by the sender. Two-player games read it as "the peer's seat"
   exactly as today (the peer is always the other seat). A watcher
   needs the tag to know which stream is player 1 and which is
   player 2.
2. Roster notices. The server already mints lifecycle notices
   (peer_joined, peer_left). It gains "roster": the full member
   list [{name, role, seat}] sent to the room on every membership
   change, so every client (players and watchers) sees the same
   room: who holds the seats, who is watching, who is in line.
3. Watch join. POST /lobbies/{id}/watch {player, password?} issues
   a spectator token (cap: 10 spectators per lobby, host can close
   watching by making the lobby private as today). The listing
   payload gains a spectators count.
4. Mid-match sync for watchers. A watcher sends "watch_sync"; the
   host answers with the existing snapshot resync payload (state at
   frame F). The watcher loads the snapshot, then applies seat
   tagged inputs from F onward. Same machinery as desync recovery,
   pointed at a third client.
5. Back to room. A game ending today ends the session. Rotation
   needs "match_end": both players return to the room phase on the
   same sockets (like peer_left in the room phase, voluntary and
   mutual), the lobby persists, and a new match can start with
   different seat holders. Either player can send it; the host's
   client treats its own send as authoritative for rotation.

## Who referees what

- The host client is the referee for v1: seat assignment, swaps,
  random pick-two, and the Got Next line all live in host-side
  envelopes ("seat_assign" {seat, player}, "got_next" join/leave
  broadcasts, queue order replicated from roster plus queue
  messages). No server state, no new persistence. If the host
  leaves, the room ends, which is acceptable for v1 and matches
  today's host-leaves rule.
- Match results for the tournament board are reported by the host
  client (POST /matches {game, p1, p2, winner, lobby}) into a
  stats-style store. Honor system, stated plainly in the UI. A
  server-refereed queue is the tournament-grade upgrade if abuse
  ever shows up; it is not needed for family and streamer rooms.

## Build slices

- S1 server: spectator role + watch join + roster notices + listing
  counts, with tests. Deploy as the next lobby image.
- S2 netsession: seat-tagged inputs, watch mode (never sends input,
  ready, or countdown; applies both streams; watch_sync snapshot
  join), match_end phase return. Race-tested like the rest of the
  package.
- S3 Android: waiting room member list (seats, watchers, queue),
  watch screen (emulator view, inputs locked, chat available),
  join-as-spectator entry from the lobby list and Friends.
- S4 host controls: seat assign and swap UI, random pick-two,
  between-match flow on match_end.
- S5 Got Next: queue join from the watch screen, auto-promotion of
  the line head into the loser's seat on match_end, winner stays.
- S6 tournaments: match reporting, tournament store and board,
  host tournament setup (game, best-of, bracket from the room).

## Risks and open points

- Password lobbies: spectators need the password too. Same secret,
  no separate watcher password in v1.
- A watcher on a slow box falls behind the input stream; the
  snapshot resync is also the catch-up mechanism (request a fresh
  watch_sync when more than a few seconds behind).
- Voice chat with spectators: v1 voice stays between the two seat
  holders. Room-wide voice is a mixer problem and waits.
- The public lobby payload exposes only hostPlayer today, which is
  why a friend guesting in someone else's lobby is not joinable
  from the Friends list. S1's roster work fixes that as a side
  effect (guest name becomes visible server-side).
