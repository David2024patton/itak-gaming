<!--
INSTRUCTIONS FOR THIS FILE (user and AI: read before editing. Keep this header.)

WHAT THIS FILE IS: the settled-decision log for this project. IDEA.md is
the raw capture of what David said; this file is only the calls that are
FINAL, so no one re-asks a settled question or quietly reverses a call.

EXACT STRUCTURE: one entry per decision, newest first:
## <YYYY-MM-DD>: <decision in one line>
- **Decided:** <what was picked>
- **Why:** <the reason, in plain words>
- **Supersedes:** <the earlier call it replaces, or "none">
- **By:** David | <who, with David's approval noted>

RULES:
- A decision enters here the day it is made, in the same pass as any
  PLAN or TODO change it causes.
- Reversing a decision means a NEW entry that names what it supersedes;
  never edit an old entry into a different decision.
- No secrets, passwords, or tokens in this file, ever.
-->
# DECISIONS: iTaK Gaming

## 2026-10-15: A build is not family-ready until it runs on the real device
- **Decided:** No build is cleared for family use until the shipped app has been run on the real target device and at least one game launched without returning to the home screen (now TODO T-3.3).
- **Why:** Build 56 was cleared and shipped, then crashed on every launch on the real TV and its clearance was revoked; builds 57 to 61 fixed the crashes. Emulator and build-machine proof is not device proof.
- **Supersedes:** clearing builds on build-machine verification alone
- **By:** David

## 2026-10-15: Mail the stick on build 1.61; 1.62 comes from the update feed
- **Decided:** The family stick shipped to Mary and Terry in Missouri with build 1.61 (build 62) aboard after device testing showed correct picture; build 1.62 with the speed passes and the white menu bar is live on the update feed and offers itself when the stick goes online.
- **Why:** 1.61 was proven on the device; waiting longer delayed the gift for speed work the update feed delivers anyway.
- **Supersedes:** holding the stick until all speed work finished
- **By:** David

## 2026-10-12: No server-side game delivery, ever
- **Decided:** iTaK Gaming never serves or streams games to other people. Netplay relays inputs and signals only; both sides run the same core and ROM locally.
- **Why:** Lawsuit risk in delivering other people's games from our server.
- **Supersedes:** any server-side delivery design
- **By:** David

## 2026-10-11: Open source under MIT, no ROMs or core binaries in the repo
- **Decided:** The project is public under MIT at github.com/David2024patton/itak-gaming. The repo ships no ROMs and no core binaries; built apps obtain or bundle cores outside the source tree.
- **Why:** David's call to open the project while keeping game content and third-party binaries out of it.
- **Supersedes:** none
- **By:** David
