# iTaK Gaming netplay: lobby server

The rendezvous service for iTaK Gaming online multiplayer. Players
create and join **lobbies** here; the actual game runs as lockstep
input sync between the two clients (same core, same ROM, only button
presses cross the wire). This server introduces the players and
relays their signaling messages verbatim. It never sees a ROM, a save
state, or a frame of gameplay.

Full design: [PROTOCOL.md](PROTOCOL.md).

The same binary also runs a **global leaderboard**: apps report their players' cumulative play stats (`POST /stats/report`) and the server aggregates them across every device into player, game, and recent-activity boards (`GET /leaderboard/{players,games,recent}`), persisted to `./stats.json` (flag `-statsfile` / env `ITAK_STATS_FILE`). See PROTOCOL.md section 6.

## Run it

```sh
export PATH=$PATH:~/goroot/go/bin   # wherever the Go toolchain lives
cd netplay
go run ./cmd/lobbyserver            # listens on :8790
# or
go build -o lobbyserver ./cmd/lobbyserver
./lobbyserver -addr :8790
```

Flags:

| flag     | default | meaning                        |
|----------|---------|--------------------------------|
| `-addr`  | `:8790` | listen address (host:port)     |
| `-sweep` | `15s`   | how often the lobby reaper runs |

Standard library only, no dependencies. Put it behind TLS (HTTPS/WSS)
if it faces the internet: lobby passwords and member tokens travel in
request bodies and URLs.

## Endpoints

| method | path                  | what it does |
|--------|-----------------------|--------------|
| POST   | `/lobbies`            | create a lobby; returns `id` + `hostToken` + `wsUrl` (201) |
| GET    | `/lobbies`            | public list of open lobbies: id, name, game, host, players, locked, createdAt |
| POST   | `/lobbies/{id}/join`  | join with `{player, password?}`; returns `guestToken` + `wsUrl` |
| GET    | `/ws?lobby=&token=`   | WebSocket signaling relay between the lobby's peers |
| GET    | `/healthz`            | `{"status":"ok","lobbies":N}` |

Passwords are stored only as a salted SHA-256 hash, and tokens only as
SHA-256 digests. The public list never exposes either.

WebSocket messages are opaque JSON envelopes with a `type` field
(`offer`, `answer`, `ice`, `input`, `state_hash`, `snapshot_*`, ...)
passed through untouched; the server adds only `peer_joined`,
`peer_left`, and `lobby_closed` lifecycle notices. Message cap
256 KB. Host disconnect closes the lobby; a guest disconnect frees
their slot. Lobbies empty of peers for 60 s are reaped, and lobbies
nobody ever connected to expire after 10 minutes. Caps: 500 lobbies,
10 create/min/IP, 20 join attempts/min/IP/lobby, 10 sockets/IP.

## Layout

```
cmd/lobbyserver/   entrypoint: flags, HTTP server, reaper loop
internal/lobby/    lobby store (interface + in-memory impl), password/token hashing
internal/server/   HTTP handlers, WebSocket hub, rate limits
internal/ws/       minimal RFC 6455 WebSocket implementation (stdlib only)
```

## Test

```sh
go test ./...
go vet ./...
```
