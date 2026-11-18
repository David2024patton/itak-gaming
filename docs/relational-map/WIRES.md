# Wires: every connection in iTaK Gaming

A wire names what crosses it. Where a wire only exists in one
direction, that is said plainly.

## People, devices, and the shell

| From | To | What crosses | Source |
| :--- | :--- | :--- | :--- |
| Player | App shell | D pad and button presses on remote or pad | flutter_ui/lib/main.dart focus handling |
| Player | EmulatorActivity | Game input through the seat system | EmulatorActivity.kt slotFor |
| App shell | Device storage | ROM folder scan per system; box art lookup | flutter_ui/lib/main.dart |
| App shell | MainActivity | Method channel calls: prefs, players, leaderboard, controllers, sound, streams, restart | Method channel net.itak/emu |
| MainActivity | Play stats files | Reads every player stats file for the leaderboard; repairs labels on launch | MainActivity.kt getLeaderboard, repairStatsLabels |
| MainActivity | Preferences | Reads and writes itak_prefs | MainActivity.kt getPref, setPref |

## One local game session

| From | To | What crosses | Source |
| :--- | :--- | :--- | :--- |
| App shell | EmulatorActivity | Launch intent naming the game and the core for its system | MainActivity.kt, EmulatorActivity.kt intent extras |
| EmulatorActivity | Core extractor | System name; the staged library file comes back, hash checked | EmulatorActivity.kt extractCore |
| EmulatorActivity | Mobile binding | Load core by file path, then run frame, drain audio, save and load state calls | go_backend/mobile/mobile.go |
| Mobile binding | Native bridge | Every engine call, serialized by one mutex | go_backend/internal/core/native.go |
| Native bridge | Rust core library | Frame steps in; framebuffer, audio samples, and save state bytes out | go_backend/internal/core/native_rust.go |
| Native bridge | Go reference cores | Same calls, only in host tools and cross checks, never in the shipped app | go_backend/internal/core/native_registry.go (gocores tag) |
| EmulatorActivity | Display tick pacing | A one deep permit per TV refresh; the game loop waits for it before each frame (local play only) | EmulatorActivity.kt DisplayTick |
| EmulatorActivity | EmuGLView | requestRender after each produced frame; the renderer pulls the finished frame copy and uploads it as a texture | EmuGLView.kt; bridge frame copy under mutex |
| EmulatorActivity | Audio pipeline | Core audio samples in; resampler when rates differ; cushion holds the lead; track writes to the device output | AudioResampler.kt, AudioCushion.kt, EmuAudioOutput.kt |
| Audio diagnostics | Debug panel | Latency, lead, stretch, underruns, starved time | AudioDiag.kt; EmulatorActivity.kt buildDebugStatsText |
| SNES core | Debug panel | 128 DSP register bytes, shown only when the core returns exactly 128 | native_rust.go DspRegs; EmulatorActivity.kt |
| EmulatorActivity | Save state file | Writes on Save Game, on the 60 second auto save, and on clean exit; reads on Load Game and on Continue | EmulatorActivity.kt |
| EmulatorActivity | Resume prompt | Save existence check at launch; the player choice decides load, fresh boot, or backup restore | EmulatorActivity.kt maybeOfferResume |
| EmulatorActivity | Play stats file | Play count up at session start; seconds added at session end | EmulatorActivity.kt statsBegin, statsEnd |
| EmulatorActivity | Stats service | Per game totals posted at session end; the server keeps the greater totals | netplay/internal/stats/ |
| Switchguard | Saves tree | One time copy aside before the first Rust core serves an install | go_backend/internal/switchguard/switchguard.go |

## Netplay and watching

| From | To | What crosses | Source |
| :--- | :--- | :--- | :--- |
| App shell (Online tab) | Lobby server | Lobby list, host and join, friends, presence | go_backend/mobile/netplay.go; netplay/internal/lobby/, friends/ |
| EmulatorActivity | Net session (Go side) | Room join, seat claim, roster, lockstep steps | go_backend/internal/netsession/ |
| Net session | WebSocket hub | Signaling and input steps relayed peer to peer; both sides run the same core and game locally | netplay/internal/ws/, server/ |
| Host session | Spectators | State snapshots and chat for watchers; watchers send no game input | go_backend/internal/netsession/watch.go |
| EmulatorActivity | Chat panel | Text chat for players and watchers in a match | go_backend/internal/netsession/chat.go |
| Lobby server | Matches service | Match records as games are set up and finished | netplay/internal/matches/ |
| Voice signaling | Lobby relay | Signaling messages only; voice media has no wire yet (PLANNED) | go_backend/internal/netsession/signal.go |

## Streams

| From | To | What crosses | Source |
| :--- | :--- | :--- | :--- |
| App shell (Newsmax and Patriots tabs) | Stream player | The chosen stream tile opens the player | flutter_ui/lib/main.dart |
| Stream player | arcade.itak.live embed page | The player frames the embed from a real https page, which is what the video host requires | StreamPlayerActivity.kt |

## Reports, updates, and builds

| From | To | What crosses | Source |
| :--- | :--- | :--- | :--- |
| App (Dart and Kotlin chute) | Debug chute service | Labelled debug reports when Debug reports is on | flutter_ui/lib/debug_chute.dart; DebugChute.kt; netplay/internal/debugchute/ |
| This repo | Autobuild pipeline | Every push; a pubspec version rise triggers a build | tools/autobuild-server.sh |
| Staged Rust libraries | Autobuild pipeline | The six core libraries are copied into the app fresh on every build | tools/autobuild-server.sh |
| Autobuild pipeline | Update feed | The built APK and version.json are published together | netplay/internal/appdist/ |
| Update feed | App shell | Version check; the app downloads the APK and hands it to the system installer | flutter_ui/lib/main.dart |
| dl.itak.live | Player devices | Manual downloads and setup categories for family installs | External service |
