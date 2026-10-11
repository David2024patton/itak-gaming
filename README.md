# iTaK Gaming

A family arcade emulator for the living room. Pick a system, pick a game, play.
No accounts, no ads, no RetroArch dependency.

Built by a disabled veteran for his family, and open source so your family can use it too.

## What works today

- Libretro cores loaded and run directly from a Go backend (CGO bridge)
- Correct video output: core-negotiated pixel formats (RGB565 / XRGB8888), converted to RGBA
- Correct aspect ratio: the renderer honors the aspect ratio reported by the core (4:3 for SNES),
  pillarboxed on modern displays, never stretched or squished
- Controller input: 2 players, full joypad (D-pad, face buttons, shoulders, start/select)
- Audio capture from the core (interleaved stereo, ring buffered)
- Save states with **per-player profiles**: multiple people can play the same game
  with separate saves that never overwrite each other
- Built-in test harness (`go_backend/cmd/coretest`) boots a core + ROM headlessly

## In progress

- Flutter TV frontend wired to the Go backend (system select is built)
- Game grid, player profile picker
- Android TV packaging (32-bit and 64-bit builds)
- Windows and Linux desktop builds

## Architecture

| Part | Tech | Job |
| --- | --- | --- |
| `flutter_ui/` | Flutter / Dart | TV-first frontend: menus, game grid, profiles |
| `go_backend/` | Go + CGO | Emulator host: libretro cores, video, audio, input, saves |
| libretro cores | C (external) | The emulators themselves (Snes9x, FCEUmm, Genesis Plus GX, ...) |

Platform builds are separate on purpose (no runtime bloat):

| Build | Targets | Systems |
| --- | --- | --- |
| Android 32-bit | Chromecast HD | NES, SNES, Genesis, GB, GBC, GBA, PS1, Sega CD |
| Android 64-bit | Google TV Streamer, Shield | Above + N64, Dreamcast, PSP, DS, Saturn, Wii/GameCube |
| Windows 64-bit | PC | All |
| Linux 64-bit | PC, repurposed boxes | All |

## ROMs and cores

This repo ships **no ROMs and no core binaries**. Both are excluded by `.gitignore`.

- ROMs: use your own legally obtained game backups.
- Cores: download prebuilt libretro cores for your platform from the
  [libretro buildbot](https://buildbot.libretro.com/nightly/) and place them
  where your build expects them (see `go_backend/internal/core`).

## Try the backend

```bash
cd go_backend
go build ./...
go run ./cmd   # serves the systems API on 127.0.0.1:8081
curl http://127.0.0.1:8081/api/systems
```

Headless core test (needs a core `.so` and a ROM you own):

```bash
# edit paths in cmd/coretest, then:
go run ./cmd/coretest
```

## Save states

Saves live under `saves/<player>/<game>/slot<N>.sav` (10 slots per player per game).
Switching players switches save sets; nobody clobbers anyone else's progress.

## License

MIT for the code in this repo (see `LICENSE`). Libretro cores are separate
projects with their own licenses (mostly GPL); respect them.
