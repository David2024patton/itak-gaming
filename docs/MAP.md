# MAP: iTaK Gaming

The full relational diagram for this project lives in
`docs/relational-map/`:

- `docs/relational-map/README.md` explains how to read the map and
  states its rules.
- `docs/relational-map/INVENTORY.md` lists every node with its state
  (LIVE, PLANNED, or ABSENT) and the source file it was read from.
- `docs/relational-map/WIRES.md` lists every connection between nodes.
- `docs/relational-map/DIAGRAM.md` draws the same map as diagrams, one
  sheet per family.

Short summary: a Flutter shell on Android TV talks through one method
channel to a Kotlin layer. The Kotlin emulator activity loads a Rust
core library through a Go binding and a mutex guarded bridge, paces
frames to the TV refresh, and writes one rolling save state and one
stats file per player per game. Six systems ship as Rust cores (Game
Boy, NES, SNES, Genesis, GBA, PS1); the Go cores in this repo are
reference twins. A Go lobby service at arcade.itak.live carries
lobbies, lockstep netplay relay, friends, stats, debug reports, and
the app update feed. An autobuild pipeline watches this repo and
publishes on a version rise.

This file used to hold a coarse grid of its own. That grid was
verified against the code at version 1.119 for the first relational
map edition and had drifted in five places; the corrections are
listed at the end of `docs/relational-map/INVENTORY.md`, and the
relational map folder is the one source from here on.

Update rule: this map refreshes with every feature build or change,
in the same push cycle. A feature is not fully landed until the map
matches it. The rule is written in full in `docs/RULES.md`.
