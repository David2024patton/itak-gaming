# Diagram sheets: iTaK Gaming

Mermaid diagrams of the same nodes and wires listed in INVENTORY.md
and WIRES.md. One sheet per family. Planned items are drawn with
dotted lines.

## Sheet 1: Overview

```mermaid
flowchart TD
  PLAYER[Player] --> SHELL[Flutter app shell]
  SHELL --> MAINACT[MainActivity]
  SHELL --> EMU[EmulatorActivity]
  EMU --> BIND[Go mobile binding]
  BIND --> BRIDGE[Native bridge]
  BRIDGE --> RUST[Rust cores shipped: GB, NES, SNES, Genesis, GBA, PS1]
  BRIDGE -.-> GOREF[Go reference cores in this repo]
  EMU --> SAVES[Save state per player per game]
  EMU --> STATSDEV[Play stats files on device]
  SHELL --> LOBBY[Lobby server at arcade.itak.live]
  LOBBY --> FEED[Update feed: version.json and APK]
  FEED --> SHELL
  EMU --> STATSSVC[Stats service]
  REPO[This repo] --> PIPE[Autobuild pipeline]
  STAGED[Staged Rust libraries] --> PIPE
  PIPE --> FEED
```

## Sheet 2: App shell

```mermaid
flowchart TD
  INTRO[Intro gate and video] --> HOME[Home tab: player chip and systems grid]
  HOME --> GAMES[Game list per system]
  GAMES --> EMU[EmulatorActivity]
  HOME --> PICK[Who is playing picker]
  PICK --> IDENT[Player identity and avatars]
  SHELL[App shell tabs] --> HOME
  SHELL --> LEADER[Leaderboard tab]
  SHELL --> ONLINE[Online tab]
  SHELL --> SETTINGS[Settings tab: popup rows]
  SHELL --> NEWS[Newsmax tab]
  SHELL --> PATRIOTS[Patriots tab]
  NEWS --> STREAM[Stream player]
  PATRIOTS --> STREAM
  SETTINGS --> SOUNDTEST[Sound test activity]
  LEADER --> STATSDEV[Play stats files]
```

## Sheet 3: One local game session

```mermaid
flowchart TD
  EMU[EmulatorActivity] --> EXTRACT[Core extractor, hash checked]
  EXTRACT --> BIND[Go mobile binding]
  BIND --> BRIDGE[Native bridge, one mutex]
  BRIDGE --> CORE[Rust core library]
  CORE --> BRIDGE
  TICK[Display tick pacing, Choreographer] --> EMU
  EMU --> GL[EmuGLView renderer]
  EMU --> AUDIO[Resampler, cushion, audio track]
  EMU --> SEATS[Controller seats by descriptor]
  REMOTE[TV remote] --> SEATS
  PAD[Bluetooth pad] --> SEATS
  SEATS --> EMU
  EMU --> PAUSE[Pause menu: save, load, debug, cheats]
  EMU --> RESUME[Resume prompt at launch]
  RESUME --> SAVES[Save state file]
  PAUSE --> SAVES
  EMU --> PANEL[Debug panel and DSP block]
  AUDIO --> PANEL
```

## Sheet 4: Netplay and services

```mermaid
flowchart TD
  SHELL[App shell Online tab] --> LOBBY[Lobby and rooms]
  EMU[EmulatorActivity] --> NETSESS[Go net session: rooms, seats, lockstep]
  NETSESS --> HUB[WebSocket hub relay]
  HUB --> PEER[Peer session: same core, same game, local]
  NETSESS --> WATCH[Spectators: snapshots and chat]
  LOBBY --> MATCHES[Matches]
  LOBBY --> FRIENDS[Friends and presence]
  LOBBY --> STATSSVC[Stats service]
  LOBBY --> CHUTE[Debug chute service]
  LOBBY --> FEED[App distribution: update feed]
  NETSESS -.-> VOICE[Voice media, PLANNED]
```

## Sheet 5: Build and publish

```mermaid
flowchart LR
  DEV[Push to this repo] --> PIPE[Autobuild pipeline]
  PIPE --> GATE{Pubspec version beats published?}
  GATE -->|yes| BUILD[Build app, copy six Rust libraries in]
  GATE -->|no| SKIP[No build]
  BUILD --> PUB[Publish APK and version.json]
  PUB --> FEED[Update feed at arcade.itak.live]
  FEED --> APP[Installed apps offer the update]
  DL[dl.itak.live downloads page] --> MANUAL[Manual installs]
```
