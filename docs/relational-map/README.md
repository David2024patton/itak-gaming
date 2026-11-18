# Relational map: iTaK Gaming

This folder is the relational diagram for the whole iTaK Gaming system:
every page, screen, module, engine, service, and wire, built from the
code in this repo.

## How to read it

- `INVENTORY.md` lists every node: its family, what it does, whether it
  is LIVE, PLANNED, or ABSENT, and the source file it was read from.
- `WIRES.md` lists every connection between nodes: from, to, what
  crosses the wire, and the source file that makes the connection.
- `DIAGRAM.md` draws the same nodes and wires as Mermaid diagrams, one
  sheet per family, so the system can be seen at a glance.
- `docs/MAP.md` at the repo root points here and carries the short
  summary.

## The rules for this map

- Code beats paper. Where an older document and the code disagree, the
  code wins and the disagreement is named in the inventory.
- Every node carries a state. LIVE means the code exists and ships or
  runs today. PLANNED means David has ordered or approved it and it is
  not built yet. ABSENT means a surface a reader might expect is not
  there, named so nobody goes looking for it.
- This map refreshes with every update. See the Relational diagram
  section in `docs/RULES.md`: a feature is not fully landed until this
  map matches it, in the same push cycle.

## State of this edition

- Built from repo HEAD at app version 1.119 (pubspec 1.119.0+120),
  the castle zoom HDMA fix release.
- The old `docs/MAP.md` grid was verified against the code for this
  edition and had drifted in five places. The drift and its fixes are
  listed at the end of `INVENTORY.md`.
- The Rust core sources that ship in the app are not in this repo.
  They are drawn here as external nodes with their staging path named,
  because the wires to them are real even though their source lives
  outside this repo. That gap is recorded, not hidden.
