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
  leaderboard survives container replacement.
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

## Checks

- `GET /healthz` returns `{"lobbies":0,"status":"ok"}` on both the
  LAN address and the public HTTPS address.
- The app converts `https://` to `wss://` for the session socket;
  the client WebSocket implementation supports both schemes.
