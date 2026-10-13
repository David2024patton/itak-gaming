# Wiring the Go Genesis core into the app (recon done 2026-10-12 night)

Goal: Genesis games in the app run on our in-house Go core instead of
Genesis Plus GX, with zero regression.

## Where cores are wired

- Flutter table: flutter_ui/lib/main.dart, kSystems, the genesis entry
  (id 'genesis', exts .md/.gen/.bin) names core 'libgenesis_plus_gx.so'.
- Go backend registry: go_backend/internal/core/types.go line 36
  (SystemGenesis) and line 41 (SystemSegaCD), both naming
  libgenesis_plus_gx.so today.
- Core binaries: flutter_ui/android/app/src/main/assets/cores/*.so.
  They are NOT in git. The server pipeline rsync EXCLUDES that dir, so
  staging is a file operation on the server build tree (~/itak-gaming)
  plus the local workspace copy.

## The Sega CD catch

Genesis Plus GX also serves Sega CD (SystemSegaCD, .cue/.iso) in the
backend registry. Our core has no Sega CD yet (roadmap item). So:

- Switch SystemGenesis (both registries) to libgo_genesis.so.
- Sega CD STAYS on libgenesis_plus_gx.so; the binary stays in assets
  for that one system until our Sega CD support lands, then it is
  deleted. Removing Sega CD outright would regress a working system,
  which David's no-regression rule forbids.

## Build the Android core

Target is android-arm (32-bit), per the pipeline's flutter build.
On the server (NDK 26.1 at ~/toolchain/android-sdk/ndk/26.1.10909125,
Go at ~/toolchain/go/bin):

  cd cores/go-genesis
  CGO_ENABLED=1 GOOS=android GOARCH=arm GOARM=7 \
  CC=$NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi24-clang \
  go build -buildmode=c-shared -trimpath \
    -o libgo_genesis.so ./libretro

(The libretro package builds c-shared on linux today; the same entry
must produce the Android .so. Discard the generated .h or keep it out
of assets.) Verify with `file` (ELF 32-bit ARM) and nm -D that the
retro_* exports are present.

## Ship steps

1. Stage libgo_genesis.so into assets/cores on the server build tree
   and in the local workspace.
2. main.dart genesis entry: core 'libgo_genesis.so'.
3. types.go SystemGenesis row: display name 'Go Genesis', file
   'libgo_genesis.so'. (A go_backend change makes the pipeline rebind
   the AAR, which it already handles.)
4. Bump pubspec to the next build, release notes, push, let the
   pipeline publish.
5. TV proof: take the update on the Death Star, launch Sonic
   (.md in /sdcard/ROMs/Genesis), ADB screenshots of title and
   gameplay, and David's ears are the final sound verdict (FM music,
   SEGA jingle, drums).
6. Netplay note: Genesis netplay state hashes change with the core;
   both ends must run the new build, same as the same-core rule today.

## Sega CD update (SCD-4, 2026-10-13)

The catch above is resolved. The Go Genesis core now covers Sega CD
in-house (SCD-1..4: sub 68000, CDD, CD-DA, RF5C164 PCM, graphics ASIC,
sub BIOS call services, BURAM). Swap preparation is done locally:

- go_backend/internal/core/types.go: the SystemSegaCD row now names
  'Go Genesis' / libgo_genesis.so with extensions .cue/.iso/.bin.
- flutter_ui/lib/main.dart: a Sega CD SystemInfo entry (id 'segacd',
  core libgo_genesis.so, dir 'SegaCD') sits after the Genesis entry.
- The Kotlin activities and tools/autobuild-server.sh never named a
  core for Sega CD (the core name flows from Dart through the
  registry), so they needed no edit.

Remaining ship step (parent, at Sega CD ship time): delete
libgenesis_plus_gx.so from the assets/cores directory on the server
build tree and the local workspace copy, bump pubspec, publish. No
pubspec change is part of SCD-4 (GBA-4 holds build 53).
