# Deploying the lobby + leaderboard server

The server (`cmd/lobbyserver`) is a single static Go binary. In
production it runs in a small Docker container behind Traefik with a
public HTTPS/WSS address, so boxes on different home networks can
find each other. The app points at that address
(`kServerBase` in the Flutter UI, `SERVER_BASE` in
EmulatorActivity).

## Pieces

- DNS: an `A` record for the subdomain (for example
  `arcade.example.com`) pointing at the host's public IP.
- Image: Alpine plus the compiled `lobbyserver` binary, started as
  `/lobbyserver -addr :8790` with `ITAK_STATS_FILE=/data/stats.json`.
- Data: the stats JSON lives on a mounted volume (`/data`) so the
  leaderboard survives container replacement. Friends lists
  persist the same way: `ITAK_FRIENDS_FILE` sets the path, and
  when it is unset the server writes `friends.json` next to the
  stats file, so the same `/data` volume holds both. Tournament
  results persist the same way too: `ITAK_MATCHES_FILE` sets
  the path, and when it is unset the server writes
  `matches.json` next to the stats file.
- Routing labels (Dokploy/Traefik, container joins the proxy
  network):

```
traefik.enable=true
traefik.docker.network=dokploy-network
traefik.http.routers.itak-lobby.rule=Host(`arcade.example.com`)
traefik.http.routers.itak-lobby.entrypoints=websecure
traefik.http.routers.itak-lobby.tls=true
traefik.http.routers.itak-lobby.tls.certresolver=letsencrypt
traefik.http.routers.itak-lobby.service=itak-lobby
traefik.http.services.itak-lobby.loadbalancer.server.port=8790
```

- Also publish `8790` on the host so boxes on the same LAN can use
  the direct address during development.

## Why the container, not a bare process

On the production host, a host firewall drops inbound LAN traffic
to bare processes on this port while Docker's published-port NAT
passes it. Running in Docker with `--restart unless-stopped` also
brings the server back after host reboots with no manual step.

## App updates

The same binary serves app self-updates when `ITAK_APP_DIR`
points at a directory holding published builds:

- `GET /app/version.json` — the manifest: versionCode,
  versionName, apk file name, sha256, size, notes, publishedAt.
- `GET /app/latest.apk` — the APK the manifest names (plain file
  names only; path traversal is refused).

Publishing a build is a file operation: copy the APK into the
directory and write a fresh `version.json` with the new
versionCode and the APK's SHA-256. Mount the directory read-only
into the container (for example `/appdist`).

The app checks the manifest on launch and from Settings, offers
the update, downloads it, verifies the SHA-256, and opens the
system installer. The first update on a box asks the user to allow
installs from iTaK Gaming once (Android's per-app setting); after
that, each update is one press on the installer's Update button.

### Auto-build from GitHub

Pushing to the repo's main branch is all it takes to ship: a
cron job on the server (every 5 minutes, `~/itak-autobuild.sh`
wrapper around `tools/autobuild-server.sh`) watches main. When a
push raises the pubspec version above the published one, it
syncs the sources, rebinds the Go AAR if `go_backend` changed,
builds the APK with the server toolchain, and publishes it as
above, taking the update notes from the repo-root
`release-notes.txt`. Pushes without a version bump (docs, fixes
not meant to ship yet) are noted and skipped. Failed builds keep
the old published version and retry at most every 30 minutes.
Log: `~/itak-autobuild.log` on the server.

## Debug chute

The same binary receives debug reports from app installs when
`ITAK_DEBUG_DIR` points at a writable directory (in production a
bind mount of `~/itak-debug` to `/debug` on the host):

- `POST /debug/ping` — tiny check-in (version, model, free
  storage, newest `native timing` lines). Refreshes the stick's
  `latest.json` with a server-side `lastSeen` and answers
  `{"want_full_report": bool}`.
- `POST /debug/report` — the full report (adds the log tail,
  capped at 300 KB). Stored per stick, newest 20 kept, and
  clears that stick's want-full-report flag.

Sticks are keyed by the first 12 hex chars of the SHA-256 of
their install code: `<dir>/<key>/report-<UTC>.json` plus
`<dir>/<key>/latest.json`. `labels.json` in the same directory
maps a key to a label, either a bare string or
`{"label": "...", "wantFullReport": true}`; setting
`wantFullReport` makes the stick's next check-in answer true,
and the stick then uploads a full report on its own. Nothing
here accepts commands or serves the reports back; reading them
is a host-side file operation.

## Checks

- `GET /healthz` returns `{"lobbies":0,"status":"ok"}` on both the
  LAN address and the public HTTPS address.
- The app converts `https://` to `wss://` for the session socket;
  the client WebSocket implementation supports both schemes.
