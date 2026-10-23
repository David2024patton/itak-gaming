<!--
INSTRUCTIONS FOR THIS FILE (user and AI: read before editing. Keep this header.)

WHAT THIS FILE IS: every link David posts about this project, and the
answer to it. When David drops a link in any chat (a repo, an article, a
prompt collection, a product), it lands here with what it is and what we
took from it, so the research is never lost and never repeated.

EXACT STRUCTURE: one entry per link, newest first:
## <YYYY-MM-DD HH:MM TZ>: <link>
- **What it is:** <one or two lines, factual>
- **License:** <license or "n/a">
- **Lulu's answer:** <verdict: TAKE, RETROFIT, REFERENCE, or SKIP, with
  the reason in plain words>
- **Went to:** <where the takeaway lives now: a TODO item id, an IDEA
  entry, the shelf file, or "nowhere, verdict only">

RULES:
- Every posted link gets an entry the same day, even when the verdict
  is SKIP; the skip and its reason are the value.
- Facts only: license and contents verified from the source, not from
  the link's reputation.
- No secrets, passwords, or tokens in this file, ever.
-->
# REFERENCES: iTaK Gaming

## 2026-10-23 09:53 EDT: https://github.com/jsgroth/jgenesis
- **What it is:** Mature multi-system emulator written in Rust (Genesis, Sega CD, SNES, and more; 2,500+ commits) with an ARCHITECTURE.md. David re-sent this link on 2026-10-23 and set the standing rule that these repos are a solution mine for our cores: when one of our cores has a defect or a stuck problem, check how the reference solved it and implement the fix in our own code.
- **License:** GPL-3.0
- **Lulu's answer:** REFERENCE. Primary technique reference for the SNES speed round 2 and for Genesis. Techniques, architecture, and algorithms only; no code text is copied into our MIT repo, because that would relicense the project GPL.
- **Went to:** SNES round 2 study (~/itak-speed/SNES-REFS.md on the build server) and the Genesis speed pass (~/itak-speed/GENESIS.md on the build server).

## 2026-10-23 09:53 EDT: https://github.com/ioncodes/sly
- **What it is:** Multi-system emulator and debugger written in Rust, with SNES, GBA, and PS1 cores under crates/cores/. David re-sent this link on 2026-10-23 under the same solution-mine rule.
- **License:** GPL-3.0
- **Lulu's answer:** REFERENCE. Mined for the PS1 disc boot fix and for SNES round 2. Techniques only; no code text into our MIT repo (GPL).
- **Went to:** The PS1 disc boot fix work item and the SNES round 2 study (~/itak-speed/SNES-REFS.md on the build server).

## 2026-10-23 09:53 EDT: https://github.com/nat-rix/rsnes
- **What it is:** Work-in-progress SNES emulator written in Rust. Its own README lists sprites, Mode 7, and color math as not yet implemented. David re-sent this link on 2026-10-23 under the same solution-mine rule.
- **License:** MIT
- **Lulu's answer:** REFERENCE, light only. Usable as a light structural reference for a simple processor loop; too incomplete to teach the picture work where our frame cost lives.
- **Went to:** SNES round 2 study reading list (~/itak-speed/SNES-REFS.md on the build server); no takeaway adopted yet.
