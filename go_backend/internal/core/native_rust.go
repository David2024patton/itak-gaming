package core

/*
#cgo !windows LDFLAGS: -ldl
#ifndef _WIN32
#include <dlfcn.h>
#endif
#ifdef _WIN32
#include <windows.h>
#endif
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stddef.h>
#include <stdio.h>

// Dynamic library loading, per platform, in the same shape
// bridge.go uses: dlopen on unix/Android, LoadLibrary on
// Windows. Distinct names (itakrs_*) because cgo concatenates
// the preambles of every file in the package: nothing here may
// shadow a bridge.go symbol.
#ifdef _WIN32
static void* itakrs_dl_open(const char* path) { return (void*)LoadLibraryA(path); }
static void* itakrs_dl_sym(void* h, const char* name) { return (void*)GetProcAddress((HMODULE)h, name); }
static void itakrs_dl_close(void* h) { if (h) FreeLibrary((HMODULE)h); }
static const char* itakrs_dl_error(void) { return "LoadLibrary/GetProcAddress failed"; }
#else
static void* itakrs_dl_open(const char* path) { return dlopen(path, RTLD_NOW); }
static void* itakrs_dl_sym(void* h, const char* name) { return dlsym(h, name); }
static void itakrs_dl_close(void* h) { if (h) dlclose(h); }
static const char* itakrs_dl_error(void) { const char* e = dlerror(); return e ? e : "unknown dl error"; }
#endif

// The switch ABI surface, one typedef per function in
// include/itak_bridge.h (iTaK bridge ABI 1.0). These mirror
// the header exactly; CONTRACT.md in the bridge project is
// the words that govern them.
typedef uint32_t (*itakrs_abi_magic_fn)(void);
typedef uint32_t (*itakrs_abi_version_fn)(void);
typedef void* (*itakrs_create_fn)(void);
typedef void (*itakrs_destroy_fn)(void*);
typedef const char* (*itakrs_system_id_fn)(void*);
typedef int32_t (*itakrs_load_rom_fn)(void*, const uint8_t*, size_t, const char*);
typedef int32_t (*itakrs_reset_fn)(void*);
typedef void (*itakrs_run_frame_fn)(void*);
typedef int32_t (*itakrs_frame_size_fn)(void*, uint32_t*, uint32_t*);
typedef const uint8_t* (*itakrs_framebuffer_fn)(void*, uint32_t*, uint32_t*, uint32_t*);
typedef int32_t (*itakrs_fps_fn)(void*, uint32_t*, uint32_t*);
typedef double (*itakrs_aspect_ratio_fn)(void*);
typedef uint32_t (*itakrs_audio_rate_fn)(void*);
typedef uint32_t (*itakrs_audio_pending_fn)(void*);
typedef uint32_t (*itakrs_audio_drain_fn)(void*, int16_t*, uint32_t);
typedef void (*itakrs_set_buttons_fn)(void*, uint32_t, uint32_t);
typedef size_t (*itakrs_state_size_fn)(void*);
typedef int32_t (*itakrs_save_state_fn)(void*, uint8_t*, size_t, size_t*);
typedef int32_t (*itakrs_load_state_fn)(void*, const uint8_t*, size_t);
typedef int32_t (*itakrs_cheat_supported_fn)(void*);
typedef void (*itakrs_cheat_reset_fn)(void*);
typedef int32_t (*itakrs_cheat_set_fn)(void*, uint32_t, const char*);

typedef struct {
    void* dl;
    itakrs_abi_magic_fn abi_magic;
    itakrs_abi_version_fn abi_version;
    itakrs_create_fn create;
    itakrs_destroy_fn destroy;
    itakrs_system_id_fn system_id;
    itakrs_load_rom_fn load_rom;
    itakrs_reset_fn reset;
    itakrs_run_frame_fn run_frame;
    itakrs_frame_size_fn frame_size;
    itakrs_framebuffer_fn framebuffer;
    itakrs_fps_fn fps;
    itakrs_aspect_ratio_fn aspect_ratio;
    itakrs_audio_rate_fn audio_rate;
    itakrs_audio_pending_fn audio_pending;
    itakrs_audio_drain_fn audio_drain;
    itakrs_set_buttons_fn set_buttons;
    itakrs_state_size_fn state_size;
    itakrs_save_state_fn save_state;
    itakrs_load_state_fn load_state;
    itakrs_cheat_supported_fn cheat_supported;
    itakrs_cheat_reset_fn cheat_reset;
    itakrs_cheat_set_fn cheat_set;
} ItakRsCore;

// Resolve one symbol; on failure record its name and bail.
#define ITAKRS_SYM(vt, field, name) do { \
    (vt)->field = (itakrs_##field##_fn)itakrs_dl_sym((vt)->dl, name); \
    if (!(vt)->field) { \
        snprintf(err, errcap, "missing symbol %s", name); \
        itakrs_dl_close((vt)->dl); free(vt); return NULL; \
    } \
} while (0)

// Open the library at path and bind every ABI 1.0 symbol.
// Returns NULL with a message in err when the library cannot
// be opened or any required symbol is absent: a library
// missing an export is not a conforming adapter (CONTRACT.md).
static ItakRsCore* itakrs_bind(const char* path, char* err, size_t errcap) {
    ItakRsCore* vt = (ItakRsCore*)calloc(1, sizeof(ItakRsCore));
    if (!vt) { snprintf(err, errcap, "out of memory"); return NULL; }
    vt->dl = itakrs_dl_open(path);
    if (!vt->dl) {
        snprintf(err, errcap, "%s", itakrs_dl_error());
        free(vt);
        return NULL;
    }
    ITAKRS_SYM(vt, abi_magic, "itak_abi_magic");
    ITAKRS_SYM(vt, abi_version, "itak_abi_version");
    ITAKRS_SYM(vt, create, "itak_create");
    ITAKRS_SYM(vt, destroy, "itak_destroy");
    ITAKRS_SYM(vt, system_id, "itak_system_id");
    ITAKRS_SYM(vt, load_rom, "itak_load_rom");
    ITAKRS_SYM(vt, reset, "itak_reset");
    ITAKRS_SYM(vt, run_frame, "itak_run_frame");
    ITAKRS_SYM(vt, frame_size, "itak_frame_size");
    ITAKRS_SYM(vt, framebuffer, "itak_framebuffer");
    ITAKRS_SYM(vt, fps, "itak_fps");
    ITAKRS_SYM(vt, aspect_ratio, "itak_aspect_ratio");
    ITAKRS_SYM(vt, audio_rate, "itak_audio_rate");
    ITAKRS_SYM(vt, audio_pending, "itak_audio_pending");
    ITAKRS_SYM(vt, audio_drain, "itak_audio_drain");
    ITAKRS_SYM(vt, set_buttons, "itak_set_buttons");
    ITAKRS_SYM(vt, state_size, "itak_state_size");
    ITAKRS_SYM(vt, save_state, "itak_save_state");
    ITAKRS_SYM(vt, load_state, "itak_load_state");
    ITAKRS_SYM(vt, cheat_supported, "itak_cheat_supported");
    ITAKRS_SYM(vt, cheat_reset, "itak_cheat_reset");
    ITAKRS_SYM(vt, cheat_set, "itak_cheat_set");
    return vt;
}

static void itakrs_unbind(ItakRsCore* vt) {
    if (!vt) return;
    if (vt->dl) itakrs_dl_close(vt->dl);
    free(vt);
}

static uint32_t itakrs_magic(ItakRsCore* vt) { return vt->abi_magic(); }
static uint32_t itakrs_version(ItakRsCore* vt) { return vt->abi_version(); }
static void* itakrs_create(ItakRsCore* vt) { return vt->create(); }
static void itakrs_destroy(ItakRsCore* vt, void* h) { vt->destroy(h); }
static const char* itakrs_system_id(ItakRsCore* vt, void* h) { return vt->system_id(h); }
static int32_t itakrs_load_rom(ItakRsCore* vt, void* h, const uint8_t* data, size_t len, const char* path) { return vt->load_rom(h, data, len, path); }
static int32_t itakrs_reset(ItakRsCore* vt, void* h) { return vt->reset(h); }
static void itakrs_run_frame(ItakRsCore* vt, void* h) { vt->run_frame(h); }
static int32_t itakrs_frame_size(ItakRsCore* vt, void* h, uint32_t* w, uint32_t* ht) { return vt->frame_size(h, w, ht); }
static const uint8_t* itakrs_framebuffer(ItakRsCore* vt, void* h, uint32_t* w, uint32_t* ht, uint32_t* p) { return vt->framebuffer(h, w, ht, p); }
static int32_t itakrs_fps(ItakRsCore* vt, void* h, uint32_t* num, uint32_t* den) { return vt->fps(h, num, den); }
static double itakrs_aspect_ratio(ItakRsCore* vt, void* h) { return vt->aspect_ratio(h); }
static uint32_t itakrs_audio_rate(ItakRsCore* vt, void* h) { return vt->audio_rate(h); }
static uint32_t itakrs_audio_pending(ItakRsCore* vt, void* h) { return vt->audio_pending(h); }
static uint32_t itakrs_audio_drain(ItakRsCore* vt, void* h, int16_t* out, uint32_t max_frames) { return vt->audio_drain(h, out, max_frames); }
static void itakrs_set_buttons(ItakRsCore* vt, void* h, uint32_t player, uint32_t mask) { vt->set_buttons(h, player, mask); }
static size_t itakrs_state_size(ItakRsCore* vt, void* h) { return vt->state_size(h); }
static int32_t itakrs_save_state(ItakRsCore* vt, void* h, uint8_t* out, size_t cap, size_t* written) { return vt->save_state(h, out, cap, written); }
static int32_t itakrs_load_state(ItakRsCore* vt, void* h, const uint8_t* data, size_t len) { return vt->load_state(h, data, len); }
static int32_t itakrs_cheat_supported(ItakRsCore* vt, void* h) { return vt->cheat_supported(h); }
static void itakrs_cheat_reset(ItakRsCore* vt, void* h) { vt->cheat_reset(h); }
static int32_t itakrs_cheat_set(ItakRsCore* vt, void* h, uint32_t index, const char* code) { return vt->cheat_set(h, index, code); }
*/
import "C"

import (
	"fmt"
	"log"
	"path/filepath"
	"unsafe"

	"github.com/davidpatton/itak-gaming/go_backend/internal/switchguard"
)

// This file is the Rust switch path: a NativeCore adapter that
// drives a Rust emulator core built as a shared library against
// the iTaK bridge ABI (CONTRACT.md and include/itak_bridge.h in
// the itak-rust-bridge project, ABI 1.0). It sits beside the Go
// native adapters in native.go and serves the same NativeBridge,
// so the app cannot tell whether the engine underneath is the
// bundled Go core linked into this binary or a Rust core loaded
// from disk.
//
// Why this is safe where Go c-shared cores were not: a Rust
// cdylib carries no second Go runtime. The build 56 crash class
// (two Go runtimes in one process) does not apply; the library
// is plain native code behind a C ABI, loaded with the same
// dlopen the desktop harness bridge (bridge.go) already uses.
//
// Semantic reconciliations between the ABI and NativeCore,
// decided here:
//   - Video is XRGB8888 only at the ABI, so PixelFormat is
//     always 1. A core that renders another format internally
//     (the Go Genesis adapter produces RGB565) converts inside
//     its adapter, per the contract.
//   - FPS crosses the ABI as an exact rational (num/den), so a
//     PAL SNES game reports 21281370/425568, and FPS() returns
//     its quotient. The value is per loaded game: it is read
//     once at create (the system nominal rate) and again after
//     every successful LoadGame.
//   - AudioDrain keeps the NativeCore meaning (every pending
//     sample, buffer cleared) over the ABI's capped drain: it
//     asks itak_audio_pending and drains until the adapter
//     reports empty. The capped drain never drops samples, so
//     nothing is lost between the two calls, and the bridge
//     mutex guarantees no run_frame interleaves them.
//   - SetInput passes the libretro-order mask through 1:1.
//     The ABI defines the mask in libretro order and each Rust
//     adapter translates to its engine internally, the same
//     layering the Go adapters use.
//   - The contract's reset has no NativeCore counterpart; it
//     is exposed as Reset on this adapter for future callers
//     and the bridge never calls it.
//   - The Game Boy adapter's battery exports (a minor-bump
//     style addition, not part of ABI 1.0) are not bound:
//     NativeCore has no battery surface.
//
// Threading: identical to every other adapter. NativeBridge
// serializes all calls under its mutex; this adapter keeps no
// locks of its own, and the ABI guarantees the adapter may
// assume the same.

// itakABIMagic is the value itak_abi_magic must return
// ("ITAK"). A library answering anything else is not an
// adapter and is refused before any other symbol is trusted.
const itakABIMagic = 0x4954414B

// itakABIMajor is the ABI major version this shim implements.
const itakABIMajor = 1

// rustCore is a NativeCore over one loaded Rust adapter
// library and one handle created from it.
type rustCore struct {
	vt *C.ItakRsCore  // bound library; nil when binding failed
	h  unsafe.Pointer // itak_handle; nil when create failed
	// err defers a construction failure (dlopen, missing
	// symbol, bad magic or version, failed create, wrong
	// system id) to LoadGame, the first NativeCore call that
	// can return an error. LoadRust mirrors LoadNative's
	// signature, which has no error return.
	err  error
	name string // library file base name, for messages
	sys  string // itak_system_id read after create

	loaded     bool
	width      int
	height     int
	fps        float64
	aspect     float64
	sampleRate float64
}

// rustCores maps a Rust adapter library file name (what the
// app passes to LoadCore) to the system ids that library may
// report through itak_system_id before a game is loaded. The
// multi-system entries mirror the Go registry: the Game Boy
// library serves "gameboy" and "gbc", the Genesis library
// "genesis" and "segacd". The file naming (libitak_<system>.so)
// parallels the Go cores' libgo_<system>.so.
var rustCores = map[string][]string{
	"libitak_gameboy.so": {"gameboy", "gbc"},
	"libitak_nes.so":     {"nes"},
	"libitak_snes.so":    {"snes"},
	"libitak_genesis.so": {"genesis", "segacd"},
	"libitak_gba.so":     {"gba"},
	"libitak_ps1.so":     {"ps1"},
}

// LoadRust returns a bridge over the Rust adapter core for the
// given core path, or false when the path names no registered
// Rust core. It mirrors LoadNative exactly, so the mobile
// package chooses Go or Rust per core by the file name the
// build ships: libgo_<system>.so is the bundled Go core,
// libitak_<system>.so is the Rust adapter.
//
// Construction problems (a missing or nonconforming library
// file) cannot be reported through this signature; they are
// logged here and returned by the bridge's LoadGame.
func LoadRust(corePath string) (*NativeBridge, bool) {
	base := filepath.Base(corePath)
	systems, ok := rustCores[base]
	if !ok {
		return nil, false
	}
	rc := newRustCore(corePath, systems)
	if rc.err == nil {
		// Backup-and-hold trigger: a Rust core is about to
		// serve its first game through this path. switchguard
		// copies the saves tree to a write-once backup and
		// starts the auto-save hold before that can happen. If
		// the backup cannot be made, the Rust core is refused:
		// the failure is deferred to LoadGame, this signature's
		// only error surface, exactly like a construction
		// failure. Contexts with no app files root configured
		// (desktop harnesses, tests) skip protection inside
		// switchguard; they have no saves tree to protect.
		if _, err := switchguard.EnsureProtected(); err != nil {
			log.Printf("backup-hold: refusing Rust core %s: %v", base, err)
			rc.Close()
			rc.err = err
		}
	}
	return &NativeBridge{core: rc, name: base}, true
}

// newRustBridge builds a NativeBridge over the Rust adapter at
// soPath without the registry name check, failing fast instead
// of deferring. It exists for tests and tools that load an
// adapter by explicit path (the conformance stub, an adapter
// under development) rather than by shipped file name.
func newRustBridge(soPath string) (*NativeBridge, error) {
	rc := newRustCore(soPath, nil)
	if rc.err != nil {
		return nil, rc.err
	}
	return &NativeBridge{core: rc, name: filepath.Base(soPath)}, nil
}

// newRustCore opens the adapter library, binds the ABI, checks
// the magic and major version before trusting anything else
// (the contract's call order), creates a handle, and reads the
// values that are fixed per library. Any failure is recorded
// in err and logged; no step panics.
func newRustCore(soPath string, systems []string) *rustCore {
	c := &rustCore{
		name:       filepath.Base(soPath),
		fps:        60,
		sampleRate: 44100,
	}
	fail := func(err error) *rustCore {
		c.err = err
		log.Printf("rust core %s unavailable: %v", c.name, err)
		if c.h != nil {
			C.itakrs_destroy(c.vt, c.h)
			c.h = nil
		}
		if c.vt != nil {
			C.itakrs_unbind(c.vt)
			c.vt = nil
		}
		return c
	}

	cpath := C.CString(soPath)
	defer C.free(unsafe.Pointer(cpath))
	errbuf := make([]byte, 256)
	vt := C.itakrs_bind(cpath, (*C.char)(unsafe.Pointer(&errbuf[0])), C.size_t(len(errbuf)))
	if vt == nil {
		return fail(fmt.Errorf("load %s: %s", soPath, cErrString(errbuf)))
	}
	c.vt = vt

	if magic := uint32(C.itakrs_magic(vt)); magic != itakABIMagic {
		return fail(fmt.Errorf("%s: ABI magic %#08x, want %#08x: not an iTaK adapter",
			c.name, magic, itakABIMagic))
	}
	if version := uint32(C.itakrs_version(vt)); version>>16 != itakABIMajor {
		return fail(fmt.Errorf("%s: ABI version %#08x: major %d, this bridge implements major %d",
			c.name, version, version>>16, itakABIMajor))
	}

	h := C.itakrs_create(vt)
	if h == nil {
		return fail(fmt.Errorf("%s: itak_create returned NULL", c.name))
	}
	c.h = h

	if id := C.itakrs_system_id(vt, h); id != nil {
		c.sys = C.GoString(id)
	}
	if len(systems) > 0 && !stringIn(c.sys, systems) {
		return fail(fmt.Errorf("%s: system id %q, registry expects one of %v",
			c.name, c.sys, systems))
	}

	if rate := C.itakrs_audio_rate(vt, h); rate > 0 {
		c.sampleRate = float64(rate)
	}
	c.refreshTiming()
	return c
}

// refreshTiming re-reads the per-game timing values (fps as an
// exact rational, aspect ratio) from the adapter. Called at
// create (system nominal values) and after every successful
// load (the loaded game's values: a PAL game differs from an
// NTSC one).
func (c *rustCore) refreshTiming() {
	if c.vt == nil || c.h == nil {
		return
	}
	var num, den C.uint32_t
	if C.itakrs_fps(c.vt, c.h, &num, &den) == 0 && den != 0 {
		c.fps = float64(num) / float64(den)
	}
	if ar := float64(C.itakrs_aspect_ratio(c.vt, c.h)); ar > 0 {
		c.aspect = ar
	}
}

// refreshSize re-reads the native frame geometry. Adapters
// answer frame_size only once a game is loaded, so a failed
// query leaves the cached geometry alone.
func (c *rustCore) refreshSize() {
	if c.vt == nil || c.h == nil {
		return
	}
	var w, h C.uint32_t
	if C.itakrs_frame_size(c.vt, c.h, &w, &h) == 0 && w > 0 && h > 0 {
		c.width, c.height = int(w), int(h)
	}
}

func (c *rustCore) statusErr(op string, st C.int32_t) error {
	return fmt.Errorf("rust core %s (%s): %s: %s (%d)",
		c.name, c.sys, op, rustStatusName(int32(st)), int32(st))
}

// rustStatusName spells an ABI status code for error messages.
func rustStatusName(st int32) string {
	switch st {
	case -1:
		return "ITAK_ERR_GENERIC"
	case -2:
		return "ITAK_ERR_NO_GAME"
	case -3:
		return "ITAK_ERR_BAD_ARG"
	case -4:
		return "ITAK_ERR_TOO_SMALL"
	case -5:
		return "ITAK_ERR_UNSUPPORTED"
	case -6:
		return "ITAK_ERR_BAD_STATE"
	}
	return "ITAK_ERR_UNKNOWN"
}

// LoadGame mirrors the Go adapters: cartridge cores consume
// data; disc cores load from path with data holding the
// descriptor bytes, exactly as the ABI specifies.
func (c *rustCore) LoadGame(data []byte, path string) error {
	if c.err != nil {
		return c.err
	}
	var dptr *C.uint8_t
	if len(data) > 0 {
		dptr = (*C.uint8_t)(unsafe.Pointer(&data[0]))
	}
	var cpath *C.char
	if path != "" {
		cpath = C.CString(path)
		defer C.free(unsafe.Pointer(cpath))
	}
	if st := C.itakrs_load_rom(c.vt, c.h, dptr, C.size_t(len(data)), cpath); st != 0 {
		return c.statusErr("itak_load_rom", st)
	}
	c.loaded = true
	c.refreshSize()
	c.refreshTiming()
	if id := C.itakrs_system_id(c.vt, c.h); id != nil {
		c.sys = C.GoString(id)
	}
	return nil
}

func (c *rustCore) RunFrame() {
	if c.h == nil {
		return
	}
	C.itakrs_run_frame(c.vt, c.h)
}

func (c *rustCore) FrameSize() (int, int) {
	// Geometry is live, not load-time: adapters report
	// the size of the frame they currently present, and
	// some systems change it per game (Genesis H32 games
	// present 256 wide against the 320 nominal answered
	// before the first frame). A cached load-time size
	// pairs the wrong width with the live framebuffer
	// and FrameToRGBA reads out of bounds, so re-query
	// on every call; a failed query keeps the last size.
	c.refreshSize()
	return c.width, c.height
}

// Frame returns the adapter's framebuffer as a slice over the
// core's memory, with the same ownership rule as the Go
// adapters: valid until the next mutating call, and
// NativeBridge.GetFrame copies it under the bridge mutex. The
// ABI fixes the layout (XRGB8888, pitch = width * 4); anything
// else is treated as no frame rather than trusted.
func (c *rustCore) Frame() []byte {
	if c.h == nil {
		return nil
	}
	var w, h, pitch C.uint32_t
	ptr := C.itakrs_framebuffer(c.vt, c.h, &w, &h, &pitch)
	if ptr == nil || w == 0 || h == 0 || w > 8192 || h > 8192 {
		return nil
	}
	if uint64(pitch) != uint64(w)*4 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(pitch)*int(h))
}

// PixelFormat: the ABI presents XRGB8888 only.
func (c *rustCore) PixelFormat() int { return 1 }

func (c *rustCore) AspectRatio() float64 { return c.aspect }

// FPS returns the loaded game's frame rate, the quotient of
// the exact rational the adapter reports.
func (c *rustCore) FPS() float64 { return c.fps }

func (c *rustCore) SampleRate() float64 { return c.sampleRate }

// SetInput hands the adapter the libretro-order mask as-is;
// the adapter translates to its engine, per the ABI.
func (c *rustCore) SetInput(player int, libretroMask uint16) {
	if c.h == nil || player < 0 || player > 1 {
		return
	}
	C.itakrs_set_buttons(c.vt, c.h, C.uint32_t(player), C.uint32_t(libretroMask))
}

// AudioDrain returns every pending sample: it sizes each call
// from itak_audio_pending and drains until the adapter reports
// empty, over the ABI's capped, lossless drain.
func (c *rustCore) AudioDrain() []int16 {
	if c.h == nil {
		return nil
	}
	var out []int16
	for {
		pending := C.itakrs_audio_pending(c.vt, c.h)
		if pending == 0 {
			return out
		}
		buf := make([]int16, int(pending)*2)
		n := C.itakrs_audio_drain(c.vt, c.h,
			(*C.int16_t)(unsafe.Pointer(&buf[0])), pending)
		if n == 0 {
			return out
		}
		out = append(out, buf[:int(n)*2]...)
	}
}

func (c *rustCore) SaveState() ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	if !c.loaded {
		return nil, fmt.Errorf("no game loaded")
	}
	size := C.itakrs_state_size(c.vt, c.h)
	if size == 0 {
		return nil, fmt.Errorf("core does not support save states")
	}
	buf := make([]byte, int(size))
	var written C.size_t
	st := C.itakrs_save_state(c.vt, c.h,
		(*C.uint8_t)(unsafe.Pointer(&buf[0])), size, &written)
	if st != 0 {
		return nil, c.statusErr("itak_save_state", st)
	}
	return buf[:int(written)], nil
}

func (c *rustCore) LoadState(data []byte) error {
	if c.err != nil {
		return c.err
	}
	if !c.loaded {
		return fmt.Errorf("no game loaded")
	}
	st := C.itakrs_load_state(c.vt, c.h,
		(*C.uint8_t)(unsafe.Pointer(&data[0])), C.size_t(len(data)))
	if st != 0 {
		return c.statusErr("itak_load_state", st)
	}
	return nil
}

// Reset is the ABI's one addition beyond the NativeCore
// surface: a power-on reset with the same game loaded. The
// bridge never calls it today; it is here so a future caller
// does not need a second shim.
func (c *rustCore) Reset() error {
	if c.err != nil {
		return c.err
	}
	if st := C.itakrs_reset(c.vt, c.h); st != 0 {
		return c.statusErr("itak_reset", st)
	}
	return nil
}

func (c *rustCore) CheatSupported() bool {
	if c.h == nil {
		return false
	}
	return C.itakrs_cheat_supported(c.vt, c.h) != 0
}

func (c *rustCore) CheatReset() {
	if c.h == nil {
		return
	}
	C.itakrs_cheat_reset(c.vt, c.h)
}

func (c *rustCore) CheatSet(index int, code string) {
	if c.h == nil {
		return
	}
	ccode := C.CString(code)
	defer C.free(unsafe.Pointer(ccode))
	C.itakrs_cheat_set(c.vt, c.h, C.uint32_t(index), ccode)
}

func (c *rustCore) Close() {
	if c.h != nil {
		C.itakrs_destroy(c.vt, c.h)
		c.h = nil
	}
	if c.vt != nil {
		C.itakrs_unbind(c.vt)
		c.vt = nil
	}
	c.loaded = false
}

// cErrString reads a NUL-terminated message out of a byte
// buffer filled from C.
func cErrString(buf []byte) string {
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

func stringIn(s string, set []string) bool {
	for _, v := range set {
		if s == v {
			return true
		}
	}
	return false
}
