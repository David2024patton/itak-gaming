# iTaK Gaming Netplay Protocol

How two iTaK Gaming clients play one game together over the internet:
David on his TV box in Kentucky, his brother on another box in
Missouri, same game, same moment, each holding their own controller.

The design is **lockstep input sync**, the technique the original
kaillera/GGPO-era emulators and RetroArch netplay are built on:

- Both clients run the **same core and the same ROM locally**.
- The game state is never streamed. Only **button states** cross the
  wire, one small record per frame.
- Because the cores are deterministic, identical inputs produce
  identical games on both sides. If they ever drift, a save-state
  snapshot puts them back on the same frame.

The lobby server (`cmd/lobbyserver`) is deliberately dumb: it
introduces players, then relays messages between them verbatim. All
intelligence lives in the clients.

---

## 1. Roles and lifecycle

```
Host                                    Lobby server                 Guest
 |  POST /lobbies {name, game, hostPlayer, password?}                 |
 | --------------------------------------> |                           |
 |  201 {id, hostToken, wsUrl}             |                           |
 | <-------------------------------------- |                           |
 |                                         |  GET /lobbies             |
 |                                         | <-----------------------  |
 |                                         |  list (id, name, game,    |
 |                                         |        host, locked, ...) |
 |                                         | ----------------------->  |
 |                                         |  POST /lobbies/{id}/join  |
 |                                         |    {player, password?}    |
 |                                         | <-----------------------  |
 |                                         |  200 {guestToken, wsUrl}  |
 |                                         | ----------------------->  |
 |  WS /ws?lobby={id}&token={hostToken}    |                           |
 | --------------------------------------> |                           |
 |                                         |  WS /ws?lobby={id}      |
 |                                         |    &token={guestToken}    |
 |                                         | <-----------------------  |
 |  {"type":"peer_joined","role":"guest"}  |                           |
 | <-------------------------------------- |                           |
 |            signaling (section 3)        |                           |
 | <======================================> | <=======================> |
 |            lockstep session (section 4) |                           |
 | <======================================> | <=======================> |
```

Lobby lifetime rules (enforced by the server):

- The host disconnecting from the signaling socket **ends the
  session**: the guest receives `{"type":"lobby_closed", ...}`, its
  socket is closed, and the lobby is deleted.
- A guest disconnecting notifies the host (`peer_left`), revokes the
  guest's token, and frees the slot so a replacement guest can join.
- A lobby whose peers have all been gone for **60 seconds** is reaped.
- A lobby nobody ever connected to expires **10 minutes** after
  creation.
- At most **500** lobbies exist at once server-wide.

---

## 2. HTTP API

Base types: JSON request/response bodies, `Content-Type: application/json`.
Errors are `{"error": "..."}` with a matching HTTP status.

### `POST /lobbies`

```json
{ "name": "Kart night", "game": "Super Mario Kart (USA).sfc",
  "hostPlayer": "David", "password": "optional", "maxPlayers": 2 }
```

- `game` is the ROM path or title. It is opaque to the server; the
  clients compare it during session setup (section 4.1) and must refuse
  to start on a mismatch.
- `maxPlayers` defaults to 2 (host + 1 guest), accepted range 2-8.
  Lockstep in this protocol is specified for 2 players; larger values
  are reserved.
- **201** → `{ "id": "XBPVW", "hostToken": "...", "wsUrl": "/ws?lobby=XBPVW&token=...", "lobby": {...} }`
- **400** invalid fields, **429** create rate limit (10/min/IP),
  **503** server full (500 lobbies).

### `GET /lobbies`

**200** → `{ "lobbies": [ PublicLobby, ... ] }`, oldest first.

```json
{ "id": "XBPVW", "name": "Kart night", "game": "Super Mario Kart (USA).sfc",
  "hostPlayer": "David", "players": 1, "maxPlayers": 2,
  "locked": true, "createdAt": "2026-10-11T22:00:00Z" }
```

The list never contains password hashes, salts, or tokens.

### `POST /lobbies/{id}/join`

```json
{ "player": "Brother", "password": "if the lobby is locked" }
```

- **200** → `{ "guestToken": "...", "wsUrl": "...", "lobby": {...} }`
- **401** wrong password, **404** no such lobby, **409** lobby full or
  name already taken in the lobby, **429** too many attempts
  (20/min per IP per lobby, counting successes and failures alike).

### `GET /healthz`

**200** → `{ "status": "ok", "lobbies": 3 }`

---

## 3. Signaling channel (WebSocket)

Endpoint: `GET /ws?lobby={id}&token={token}`. A bad lobby or token is
rejected with **401 before the upgrade**. A client IP may hold at most
**10** concurrent sockets. Individual messages are capped at **256 KB**;
oversize messages close the socket with code 1009.

Every client message is a JSON envelope with a `type` field. The
server **does not parse client messages**: whatever one peer sends is
delivered to the other peer byte for byte. (Messages are relayed, not
re-serialized, so unknown fields survive.) The only messages the
server itself mints are lifecycle notices:

| type           | extra fields | meaning                                  |
|----------------|--------------|------------------------------------------|
| `peer_joined`  | `role`       | the other side connected                 |
| `peer_left`    | `role`       | a guest disconnected; slot is free       |
| `lobby_closed` | `role`       | the host left; session over, lobby gone  |

Keepalive: the server pings every 25 s and drops sockets that show no
sign of life (message or pong) for 90 s.

### 3.1 Establishing the game channel

The signaling socket carries session setup and is the **fallback**
transport for lockstep traffic. The preferred game transport is a
direct peer-to-peer data channel negotiated over signaling, WebRTC
style:

1. The **guest** creates an offer and sends `{"type":"offer","sdp":"..."}`.
2. The **host** answers with `{"type":"answer","sdp":"..."}`.
3. Both sides trickle ICE candidates as they appear:
   `{"type":"ice","candidate":"...","sdpMid":"0","sdpMLineIndex":0}`.
4. When the data channel opens, lockstep traffic moves to it. If it
   drops or never opens (symmetric NAT, blocked UDP), the clients fall
   back to sending the same lockstep messages over the signaling
   socket with `"type":"relay"` semantics: the envelopes are identical,
   only the transport changes. The server treats `relay`-typed messages
   exactly like any other: forwarded verbatim. **The direct-vs-relay
   decision is entirely the clients'.**

The SDP/ICE payload formats follow WebRTC conventions, so a client may
use any WebRTC stack for the direct channel; a raw UDP channel with an
equivalent offer/answer exchange (endpoint addresses + a per-session
key in the same fields) is equally valid.

---

## 4. Lockstep session

### 4.1 Session setup

Once both peers are on the signaling socket, before any frame runs:

1. Host sends `session_offer`:
   ```json
   { "type": "session_offer", "game": "Super Mario Kart (USA).sfc",
     "romHash": "sha256-hex-of-the-rom-file", "core": "snes9x",
     "coreVersion": "...", "protocol": 1 }
   ```
2. Guest verifies it has the identical ROM (hash must match) and a
   compatible core, then replies:
   ```json
   { "type": "session_accept", "inputDelay": 4, "hashInterval": 300 }
   ```
   On mismatch it replies `{ "type": "session_reject", "reason": "..." }`
   and both sides show the reason and stop.
3. Host sends a starting snapshot (section 4.4) so both sides begin
   from byte-identical state (normally the core's power-on state after
   loading the ROM, captured via the bridge's save-state function).
4. Both sides reset their frame counter to 0 and start.

**Input delay** (`inputDelay`, in frames) is the core lockstep knob:
inputs are always applied `inputDelay` frames after they are pressed,
which gives each side time to receive the other's input for that frame
before simulating it. Negotiated by the guest in `session_accept`
based on measured signaling round-trip time: 2 frames for LAN,
**4 frames default** (~67 ms at 60 fps), up to 8 for slow links. Both
sides use the accepted value; it does not change mid-session.

### 4.2 Per-frame input exchange

Each frame, each side sends exactly one message for its local player:

```json
{ "type": "input", "frame": 1234, "buttons": 9 }
```

- `frame`: the frame this input **applies to**, i.e. current frame +
  inputDelay at the moment it was sampled.
- `buttons`: a 16-bit bitmask in the bridge's libretro button order
  (bit 0 = B, 1 = Y, 2 = Select, 3 = Start, 4 = Up, 5 = Down, 6 = Left,
  7 = Right, 8 = A, 9 = X, 10 = L, 11 = R, 12 = L2, 13 = R2, 14 = L3,
  15 = R3). Two bytes of payload per frame: at 60 fps this is a few
  hundred bytes per second including envelope overhead.

Simulation rule (the whole protocol in one paragraph): a side may
simulate frame *f* only when it holds **both** players' inputs for
*f*. It applies its own already-sent input and the remote input to the
bridge (`SetButton(player, ...)`), runs one frame, and repeats. If the
remote input for the next frame has not arrived, the side **stalls**
(does not run the core; the picture freezes) until it does. Stalls are
the visible cost of latency spikes; with the negotiated delay they
should be rare and short. A side that has not heard from its peer at
all for 5 seconds shows "connection lost"; at 15 seconds it ends the
session.

Implementations should pipeline: send the input for frame *f* as soon
as it is sampled, not when frame *f - inputDelay* finishes simulating.
Over the direct channel, transport is UDP-style (unordered loss is
acceptable **only** because inputs are also cumulatively re-sent: each
`input` message includes the sender's previous two frames' buttons as
`prev: [b1, b2]`, so one dropped packet self-heals; a receiver missing
a frame it cannot reconstruct sends `{"type":"input_request","frame":f}`
and the sender replies with that frame's `input` message again). Over
the signaling socket the transport is TCP, ordered and reliable, and
`prev`/`input_request` are unnecessary but harmless.

### 4.3 Desync detection: state hash check

Every **300 frames** (5 seconds at 60 fps, `hashInterval` from the
session setup), immediately after simulating a frame whose number is a
multiple of the interval, each side hashes its serialized state:

- Serialize with the bridge's save-state function.
- Hash the bytes with SHA-256 (any fast hash works since both sides
  agree in `session_offer`; SHA-256 is the specified default) and send:
  ```json
  { "type": "state_hash", "frame": 1500, "hash": "hex..." }
  ```
- Each side compares the remote hash for that frame with its own.
  Equal: play on. Different: **desync** (section 4.4).

### 4.4 Resync and snapshots

On a hash mismatch, and at session start, the **host is the source of
truth**:

1. The side that detects the mismatch sends `{"type":"resync_request","frame":f}`.
2. The host pauses simulation, serializes its current state, and sends
   it as a snapshot. Because a save state can exceed the 256 KB
   message cap (an SNES state is ~100-800 KB), snapshots are chunked:
   ```json
   { "type": "snapshot_begin", "frame": 1500, "bytes": 823407, "chunks": 13 }
   { "type": "snapshot_chunk", "seq": 0, "data": "base64..." }
   ... seq 1..12, each chunk at most 64 KB of raw state (base64 ~85 KB)
   { "type": "snapshot_end", "sha256": "hex-of-the-full-state" }
   ```
3. The guest reassembles, verifies the SHA-256, loads the state via
   the bridge, sets its frame counter to the snapshot's frame, clears
   any queued inputs past that frame, and replies
   `{"type":"snapshot_applied","frame":1500}`.
4. The host resumes; both sides exchange fresh inputs starting at
   `frame + inputDelay`.

The same snapshot flow powers **late join**: a guest who joins a lobby
whose host is already mid-game receives the current state instead of
the power-on state and drops straight in. (The host marks its lobby
"in game" by sending the server nothing at all; the lobby list is only
a directory. Late join is negotiated peer to peer like everything
else: the guest asks with `{"type":"join_mid_game"}` after
`session_accept`, and the host answers with a snapshot.)

Save/load during netplay: local save states are disabled for both
players while a session runs (they would instantly desync the pair).
Cheats are likewise disabled unless both sides have the identical
cheat list enabled, in which case they are part of the deterministic
input as far as the cores are concerned; the safe default is off, and
`session_offer` carries `"cheats": []` so any difference rejects the
session.

### 4.5 Ending a session

Either side may send `{"type":"session_end","reason":"quit"}` and then
disconnect. The host disconnecting also triggers the server's
`lobby_closed` path (section 1). Stats (play time, leaderboard) are
recorded locally by each client exactly as for a local game, credited
to the local player.

---

## 5. Security notes

- **Passwords**: never stored or transmitted in recovery form. The
  server keeps `sha256(salt || password)` with a random 16-byte salt
  per lobby and compares in constant time. The plaintext password only
  exists in the create/join request bodies, so the server should be
  served over TLS (WSS/HTTPS) in production: tokens and passwords are
  bearer secrets on the wire.
- **Tokens**: 256-bit random per member, issued at create/join. Only
  their SHA-256 digests are stored server-side. A token authorizes
  exactly one membership in exactly one lobby, is revoked when a guest
  disconnects, and dies with the lobby. Anyone holding a token *is*
  that player as far as the server is concerned: clients must treat
  tokens like passwords and never log them.
- **No secrets in the lobby list**: `GET /lobbies` exposes names, game,
  host player, player counts, lock status, and creation time. Nothing
  else.
- **Rate limits**: lobby creation 10/min/IP; join attempts 20/min per
  IP per lobby (the password-guessing brake); 10 concurrent signaling
  sockets per IP; 500 lobbies server-wide; 256 KB per signaling
  message; 64 KB per HTTP body. Unknown JSON fields are rejected.
- **Trust model**: the server relays but never verifies game traffic.
  A cheating client can lie about its inputs or hashes; lockstep makes
  casual cheating self-defeating (a forged input desyncs the pair and
  forces a host snapshot), and the host is always the state authority.
  This is a family lobby system, not a competitive anti-cheat design.
- **Exposure**: the lobby server is a rendezvous point, safe to run on
  a public VPS or at home behind a tunnel/reverse proxy. Game traffic
  on the direct channel is peer-to-peer and never touches it.
