# Inventory: every node in iTaK Gaming

State key: LIVE means built and in use today. PLANNED means ordered
and not yet built. ABSENT means named because a reader might expect
it; it is not there.

## Family 1: People and devices (external)

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| Player | LIVE | The person holding a controller or remote | n/a |
| David home TV (Philips 4K) | LIVE | Main test and play TV, Android TV, 32 bit | Device record, app runs on it |
| Missouri stick (Chromecast HD) | LIVE | Terry and Mary stick, self updates on launch | Device record, debug chute label Mary |
| Game controller (Bluetooth pad) | LIVE | Assigned per player by device descriptor | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/EmulatorActivity.kt |
| TV remote | LIVE | D pad navigation everywhere; in game it is a backup controller for seat 0 only when the owner pad is away | Same EmulatorActivity.kt seat rules |
| ROM files on device storage | LIVE | Game files live on the device under ROMs folders, never in this repo | flutter_ui/lib/main.dart ROM scan |

## Family 2: App shell (Flutter, flutter_ui/lib)

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| App root and intro gate | LIVE | Starts the app, plays the intro video, then opens home; BACK on home opens the exit popup | flutter_ui/lib/main.dart, flutter_ui/lib/intro_video.dart |
| Home tab | LIVE | Player chip, systems grid, version footer; six tabs in all: Home, Leaderboard, Online, Settings, Newsmax, Patriots | flutter_ui/lib/main.dart |
| Systems catalog and grid | LIVE | 13 catalog entries. Seven playable on the 32 bit build: NES, SNES, Genesis, Sega CD, Game Boy, GBA, PS1. Six locked cards that need a 64 bit build: N64, Dreamcast, PSP, DS, Saturn, GameCube | flutter_ui/lib/main.dart (kSystems, requires64) |
| Game list per system | LIVE | Scans the device ROM folder for that system, box art beside each game | flutter_ui/lib/main.dart |
| Who is playing picker | LIVE | Pick, add, edit, or remove a player; decides whose saves and stats a session writes | flutter_ui/lib/main.dart |
| Player identity and avatars | LIVE | First name required; middle and last initial, city, state optional; avatar style and seed per player | flutter_ui/lib/main.dart (PlayerIdentity, Avatars) |
| Leaderboard tab | LIVE | Reads every player stats file on the device and ranks play time and plays | flutter_ui/lib/main.dart; stats files written by EmulatorActivity.kt |
| Online tab | LIVE | Lobby list, host and join, friends, watch and chat entry points | flutter_ui/lib/main.dart; go_backend/mobile/netplay.go |
| Settings tab | LIVE | Rows that each open a popup: Swap buttons, Box art, Auto save, Friend alerts, Sound test, App updates, Logs, Space background, Intro video, Debug reports, Send report, Install code | flutter_ui/lib/main.dart |
| Newsmax tab | LIVE | Livestream tiles for the Newsmax channel | flutter_ui/lib/main.dart; StreamPlayerActivity.kt |
| Patriots tab | LIVE | Livestream tiles for shows that have their own verified channel | flutter_ui/lib/main.dart; StreamPlayerActivity.kt |
| Space background | LIVE | Optional starfield behind the shell | flutter_ui/lib/space_background.dart |
| Debug chute (Dart side) | LIVE | Builds and sends device debug reports when Debug reports is on | flutter_ui/lib/debug_chute.dart |
| One Dart file per screen | ABSENT | Every screen lives in the one main.dart; only the intro, the space background, and the debug chute have their own Dart files | flutter_ui/lib/ |
| Method channel net.itak/emu | LIVE | The one channel the Dart shell uses to call the Android layer: prefs, players, leaderboard, controllers, sound test, streams, restart | flutter_ui/lib/main.dart; MainActivity.kt |

## Family 3: Android native layer (Kotlin)

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| MainActivity | LIVE | Owns the method channel, player files, stats repair on launch, controller capture, sound delay measure, app restart | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/MainActivity.kt |
| EmulatorActivity | LIVE | Runs one game session: core load, game loop, input seats, pause menu, saves, stats, chat panel, netplay and watching modes | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/EmulatorActivity.kt |
| Display tick pacing | LIVE | Choreographer frame callback gates the local game loop to the TV refresh; shipped in 1.118 | EmulatorActivity.kt (DisplayTick) |
| Pause menu | LIVE | Exit Game, Resume, Save Game, Load Game, Debug, Cheats | EmulatorActivity.kt |
| Debug submenu and panel | LIVE | Debug stats overlay with color coded lines and target hints, Fast forward, Debug control; DSP register block for SNES | EmulatorActivity.kt |
| Debug control | LIVE | Lets tool sent key events (device id -1) play seat 0 while its switch is on; off by default | EmulatorActivity.kt |
| Controller seats | LIVE | Seat 0 is the current player; pads are matched by permanent descriptor, never by numeric device id; fallback pad takes seat 0 if the owner pad is away | EmulatorActivity.kt |
| Resume prompt | LIVE | On launch, offers Continue, Start fresh, or Restore backup when a save state exists for that player and game | EmulatorActivity.kt |
| EmuGLView and renderer | LIVE | OpenGL view; uploads each finished frame as a texture, aspect correct, sharp bilinear picture mode | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/EmuGLView.kt |
| Audio output | LIVE | Builds the Android audio track at the device rate | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/EmuAudioOutput.kt |
| Audio resampler | LIVE | In app sinc resampler, used when the core rate and the device rate differ | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/AudioResampler.kt |
| Audio cushion | LIVE | Tracks buffer lead, trims and stretches a little to hold the target lead, counts underruns and starved time | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/AudioCushion.kt |
| Audio diagnostics | LIVE | Session latency and lead sampler; feeds the debug panel and the session end log line | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/AudioDiag.kt |
| Sound test activity | LIVE | Settings sound test and sound delay measure surface | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/SoundTestActivity.kt |
| Stream player activity | LIVE | Plays livestreams in a web view framed from the arcade site, which is what makes the embeds play | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/StreamPlayerActivity.kt |
| Debug chute (Kotlin side) | LIVE | Sends labelled debug reports from the emulator side | flutter_ui/android/app/src/main/kotlin/net/itak/itak_gaming/DebugChute.kt |
| Core extractor | LIVE | Copies the bundled core library for the chosen system out of the app assets, hash checked on every launch, re extracted on mismatch | EmulatorActivity.kt (extractCore) |
| Remote button remapping | ABSENT | No remap layer exists: the remote talk button is hard mapped to the game A button, and the color buttons, OPTIONS, and gear buttons have no app handling. David asked for this on 2026-11-07; not built yet | EmulatorActivity.kt mapButton (no remap code found) |

## Family 4: Go backend host (go_backend, runs inside the app)

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| Mobile binding | LIVE | The gomobile surface the Android layer calls: load core, run frame, drain audio, save and load state, netplay | go_backend/mobile/mobile.go |
| Native bridge | LIVE | One mutex serializes every engine touch; frame copies leave under the same lock, so a posted frame is never a half written frame | go_backend/internal/core/native.go |
| Rust core loader | LIVE | Loads a Rust core library by file path behind the native interface | go_backend/internal/core/native_rust.go |
| Core registry (gocores tag) | LIVE | Since 1.102 the app build carries no Go cores; the registry behind the gocores tag is what strips them | go_backend/internal/core/native_registry.go, native_registry_off.go |
| Per system native shims | LIVE | One shim file per shipped Rust system | go_backend/internal/core/native_snes.go, native_nes.go, native_gameboy.go, native_gba.go, native_genesis.go, native_ps1.go |
| Libretro bridge | LIVE | Loads libretro core libraries with video, audio, and input callbacks; the desktop harness path and the fallback inside the mobile load | go_backend/internal/core/bridge.go |
| Local API server | LIVE | Small local API for the desktop host and test tools. Only the systems route is real; the games, launch, save, load, and players routes are ABSENT, marked TODO in the source | go_backend/internal/server/api.go |
| Save manager (Go side) | LIVE | Per player, per game, per slot save handling used by the Go server and harness path only. The app's own save files are written by the Kotlin layer, not by this manager | go_backend/internal/save/manager.go |
| Switchguard | LIVE | On the first Rust core serve of an install, copies the saves tree aside and writes a marker, so a core switch never silently eats old saves | go_backend/internal/switchguard/switchguard.go; go_backend/mobile/backup_hold.go |
| Net session (Go side) | LIVE | Rooms, seats, roster, lockstep steps, signaling, chat, got next, match end, tournament, watching | go_backend/internal/netsession/ |
| Host test tools | LIVE | coretest, enginetest, netplayguest command line tools | go_backend/cmd/ |

## Family 5: Cores

Shipped cores are Rust libraries. Their sources are not in this repo;
they are staged on the build server and copied into the app by the
pipeline. The Go cores in this repo are the reference twins: same
behavior, kept so the logic can be read, tested, and cross checked.

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| Rust SNES core (shipped) | LIVE | libitak_snes.so, 26 exported functions, DSP register debug export, HDMA rules fixed in 1.119 | Staged library; adapter contract in go_backend/internal/core/native_snes.go |
| Rust NES core (shipped) | LIVE | libitak_nes.so, battery save functions present in the library | Staged library; native_nes.go |
| Rust Game Boy core (shipped) | LIVE | libitak_gameboy.so, battery save functions present | Staged library; native_gameboy.go |
| Rust Genesis core (shipped) | LIVE | libitak_genesis.so, includes Sega CD work | Staged library; native_genesis.go |
| Rust GBA core (shipped) | LIVE | libitak_gba.so | Staged library; native_gba.go |
| Rust PS1 core (shipped) | LIVE | libitak_ps1.so | Staged library; native_ps1.go |
| Go SNES core (reference) | LIVE | Full twin: cpu, ppu, apu and DSP, dma and HDMA, system | cores/go-snes/ |
| Go NES core (reference) | LIVE | Twin core | cores/go-nes/ |
| Go Game Boy core (reference) | LIVE | Twin core, SM83 cpu, ppu, sound | cores/go-gameboy/ |
| Go GBA core (reference) | LIVE | Twin core, ARM7TDMI, gpu, apu, save types | cores/go-gba/ |
| Go Genesis core (reference) | LIVE | Twin core, M68K and Z80, VDP, YM2612 and PSG, Sega CD | cores/go-genesis/ |
| Go PS1 core (reference) | LIVE | Twin core | cores/go-ps1/ |
| Go N64 core (reference) | LIVE | R4300, RSP, RDP; no shipped Rust library in the app today | cores/go-n64/ |
| Go DS core (reference) | LIVE | ARM7 and ARM9, GX 3D; reference only today | cores/go-nds/ |
| Go PSP core (reference) | LIVE | Reference only today | cores/go-psp/ |
| Go Saturn core (reference) | LIVE | Reference only today | cores/go-saturn/ |
| Go Dreamcast core (reference) | LIVE | SH4 and ARM7, PowerVR, AICA; reference only today | cores/go-dreamcast/ |
| Go GameCube core (reference) | LIVE | Gekko, DSP, GPU; reference only today | cores/go-gamecube/ |
| The six 64 bit systems in the app | PLANNED | N64, Dreamcast, PSP, DS, Saturn, and GameCube sit in the shell catalog as locked cards (requires64). They have no backend adapter file and no wire into the shipped 32 bit app; their Go cores above are reference only, and their Rust ladder sources live outside this repo with their repo home not yet decided | flutter_ui/lib/main.dart (kSystems); absence checked in go_backend/internal/core/ |
| Battery save wiring (SNES, NES, Game Boy) | PLANNED | The libraries export battery save, load, and size; the bridge and app do not call them yet, so cartridge battery saves do not persist outside a save state | Proposal recorded 2026-11-02, not ordered |

## Family 6: On device data

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| Save state file | LIVE | One rolling save state per player per game: saves folder, player folder, game name .sav | EmulatorActivity.kt saveFile |
| Save backup file | LIVE | The .sav.bak kept once when a player chooses Start fresh, offered back by Restore backup | EmulatorActivity.kt keepSaveBackup |
| Switchguard hold copy | LIVE | Whole saves tree copied aside on the first Rust serve of an install | go_backend/internal/switchguard/switchguard.go |
| Auto save | LIVE | Every 60 seconds while playing when Auto save is on, plus a save on clean exit | EmulatorActivity.kt |
| Play stats file | LIVE | One file per player: plays, seconds, last played, per game; written at session start and end | EmulatorActivity.kt statsBegin, statsEnd |
| Stats repair and merge | LIVE | On launch, a stats file with a wrong name inside is relabelled to its player; on rename, histories merge per game | MainActivity.kt repairStatsLabels, mergeStatsOnRename (1.111) |
| Preferences (itak_prefs) | LIVE | Settings switches, controller map, debug switches | MainActivity.kt, EmulatorActivity.kt |
| Controller map | LIVE | Player name to pad descriptor, written by the player picker | EmulatorActivity.kt controller_map |
| Cartridge battery RAM outside a save state | ABSENT | Fresh boot starts with blank cartridge RAM; battery data only exists inside a save state today | See Battery save wiring, PLANNED |

## Family 7: Netplay and lobby services (netplay/)

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| Lobby server | LIVE | The public lobby service at arcade.itak.live, containerised | netplay/cmd/lobbyserver/main.go |
| Lobby and rooms | LIVE | Public lobbies, host and guest tokens, room store. The store is memory only; a persistent lobby store is ABSENT, with the store interface left as the seam for it | netplay/internal/lobby/ |
| Matches | LIVE | Match records and http doors | netplay/internal/matches/ |
| Friends and presence | LIVE | Friend lists, presence, friend alerts | netplay/internal/friends/ |
| Stats service | LIVE | Receives per game play totals from devices, keeps the greater totals, serves the leaderboard | netplay/internal/stats/ |
| WebSocket hub | LIVE | Relays signaling and lockstep inputs between peers; game state never crosses the server | netplay/internal/ws/, netplay/internal/server/ |
| Spectators | LIVE | Watchers follow a match through host snapshots and chat | netplay/internal/server/spectator tests; go_backend/internal/netsession/watch.go |
| App distribution (update feed) | LIVE | Serves version.json, the latest APK, and the embed page the stream player frames; the app checks, verifies, downloads, and hands off to the installer | netplay/internal/appdist/appdist.go |
| Debug chute service | LIVE | Receives labelled device debug reports, keeps the newest per device | netplay/internal/debugchute/debugchute.go |
| Voice chat | PLANNED | Signaling is built on the Go side; voice media is not live. Blocked on a microphone the TV setup does not have | go_backend/internal/netsession/signal.go; VOICE_CHAT_RESEARCH.md |
| In game text chat | LIVE | Chat panel inside the emulator for netplay and watching | go_backend/internal/netsession/chat.go; EmulatorActivity.kt showChatPanel |

## Family 8: Build, publish, and install

| Node | State | What it is | Source |
| :--- | :--- | :--- | :--- |
| Autobuild pipeline | LIVE | Watches this repo; on a version rise it builds the app, copies the six staged Rust libraries in fresh, and publishes | tools/autobuild-server.sh |
| Version gate | LIVE | A build only publishes when the pubspec version code beats the published version.json | tools/autobuild-server.sh; flutter_ui/pubspec.yaml |
| Published APK and version.json | LIVE | Served at arcade.itak.live under /app/ | netplay/internal/appdist/ |
| In app updater | LIVE | Offers the new version inside the app; install is handed to the system installer | flutter_ui/lib/main.dart |
| dl.itak.live downloads page | LIVE | Manual install page with device setup categories; downloader code belongs to the page, not the app | External service |
| Android 64 bit build | PLANNED | The 64 bit branch is paused; the published app is 32 bit (armeabi-v7a) only. A 64 bit launch is planned as version 2.0 | Project record |
| Windows and Linux builds | ABSENT | The Flutter tree carries windows and linux runners, but no Windows or Linux build is published or supported today | flutter_ui/windows, flutter_ui/linux (scaffolding only) |

## Drift found while building this edition (code beat the old map)

The old docs/MAP.md grid was checked line by line. Five claims did
not match the code at 1.119 and are corrected above:

1. Old map: the Go cores are the engines that ship. Code: since 1.102
   the app ships six Rust core libraries and carries no Go cores; the
   Go cores are reference twins.
2. Old map: saves are 10 slots per player per game. Code: one rolling
   save state per player per game, plus a write once backup file.
3. Old map: platform builds include Android 64 bit, Windows, and
   Linux. Code and record: only the 32 bit Android build publishes;
   64 bit is paused and planned as 2.0; desktop runners are scaffolding.
4. Old map: voice chat is research only. Code: Go signaling is built;
   media is the missing piece, blocked on microphone hardware.
5. Old map: the update feed lives on dl.itak.live. Code: the feed is
   served by the lobby service appdist module at arcade.itak.live;
   dl.itak.live is the manual downloads page.
