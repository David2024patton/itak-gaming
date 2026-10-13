// Command libretro is the libretro shell for the
// from-scratch Go Sega Saturn core (SAT-4). It
// follows the family pattern set by the SNES,
// Genesis and PS1 shells: a c-shared package main
// exporting the retro_* C ABI, with the machine
// behind it in the system package.
//
// Choices specific to this core:
//
//   - Content loads by path (need_fullpath): .cue
//     and .iso images open through the cd image
//     layer and boot through system.BootDisc, the
//     documented HLE boot (System ID verified, IP
//     loaded to Work RAM-H, entry at 0x06002100).
//     No boot ROM is involved. A disc whose System
//     ID or IP does not check out is rejected.
//   - The guest framebuffer is presented at the
//     machine's current TVMD display size every
//     frame, with a SET_GEOMETRY notice when the
//     size changes (the PS1 shell's approach). Base
//     geometry 320x224, max 640x480, aspect 4:3.
//   - The frame rate is declared 60.0: the machine
//     runs 477,273 master cycles per frame at
//     28.6364 MHz, the NTSC rate. PAL timing is out
//     of scope for this core.
//   - Audio is 44,100 Hz stereo from the SCSP.
//   - The Saturn digital pad has no Select button,
//     so the libretro Select id is unmapped. Face
//     buttons map letter for letter (A, B, X, Y);
//     the Saturn C and Z buttons, which have no
//     libretro face counterparts, sit on R2 and L2.
//     L and R are the libretro L and R ids.
//   - RETRO_MEMORY_SAVE_RAM is the backup RAM's
//     LOGICAL view: the bus stores backup data in a
//     32 KiB packed slice that the odd byte window
//     maps onto the cells, and Bus.BackupRAM exposes
//     exactly that logical store, so the pointer is
//     handed out directly and guest writes are live
//     in it.
//   - RETRO_MEMORY_SYSTEM_RAM is Work RAM Low
//     followed by Work RAM High (2 MiB). The two
//     regions live in disjoint bus slices, so the
//     shell mirrors them in one cache slice with the
//     GBA shell's hash discipline: frontend edits to
//     the cache are pushed into the machine before
//     the frame runs, and machine state is pulled
//     back into the cache after it.
//   - retro_reset re-boots the loaded disc: a bare
//     machine Reset parks the CPUs at the BIOS
//     window with no program, so the shell resets
//     and runs BootDisc again when a disc is loaded.
package main

/*
#cgo CFLAGS: -std=c11

#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
#include <stdbool.h>
#include <string.h>

typedef bool (*saturn_environ_cb_t)(unsigned cmd, void *data);
typedef void (*saturn_video_refresh_cb_t)(const void *data, unsigned width, unsigned height, size_t pitch);
typedef void (*saturn_audio_sample_cb_t)(int16_t left, int16_t right);
typedef size_t (*saturn_audio_sample_batch_cb_t)(const int16_t *data, size_t frames);
typedef void (*saturn_input_poll_cb_t)(void);
typedef int16_t (*saturn_input_state_cb_t)(unsigned port, unsigned device, unsigned index, unsigned id);
typedef void (*saturn_log_printf_t)(int level, const char *fmt, ...);

static saturn_environ_cb_t saturn_environ_cb;
static saturn_video_refresh_cb_t saturn_video_cb;
static saturn_audio_sample_cb_t saturn_audio_cb;
static saturn_audio_sample_batch_cb_t saturn_audio_batch_cb;
static saturn_input_poll_cb_t saturn_input_poll_cb;
static saturn_input_state_cb_t saturn_input_state_cb;
static saturn_log_printf_t saturn_log_fn;

static void saturn_set_environ_cb(saturn_environ_cb_t cb) { saturn_environ_cb = cb; }
static void saturn_set_video_cb(saturn_video_refresh_cb_t cb) { saturn_video_cb = cb; }
static void saturn_set_audio_cb(saturn_audio_sample_cb_t cb) { saturn_audio_cb = cb; }
static void saturn_set_audio_batch_cb(saturn_audio_sample_batch_cb_t cb) { saturn_audio_batch_cb = cb; }
static void saturn_set_input_poll_cb(saturn_input_poll_cb_t cb) { saturn_input_poll_cb = cb; }
static void saturn_set_input_state_cb(saturn_input_state_cb_t cb) { saturn_input_state_cb = cb; }

static bool saturn_call_environ(unsigned cmd, void *data) {
	if (!saturn_environ_cb) return false;
	return saturn_environ_cb(cmd, data);
}
static void saturn_call_video(const void *data, unsigned w, unsigned h, size_t pitch) {
	if (saturn_video_cb) saturn_video_cb(data, w, h, pitch);
}
static void saturn_call_audio(int16_t l, int16_t r) {
	if (saturn_audio_cb) saturn_audio_cb(l, r);
}
static size_t saturn_call_audio_batch(const int16_t *data, size_t frames) {
	if (!saturn_audio_batch_cb) return 0;
	return saturn_audio_batch_cb(data, frames);
}
static void saturn_call_input_poll(void) {
	if (saturn_input_poll_cb) saturn_input_poll_cb();
}
static int16_t saturn_call_input_state(unsigned port, unsigned device, unsigned index, unsigned id) {
	if (!saturn_input_state_cb) return 0;
	return saturn_input_state_cb(port, device, index, id);
}

struct saturn_log_callback { saturn_log_printf_t log; };
static void saturn_grab_log(void *data) {
	struct saturn_log_callback *lc = (struct saturn_log_callback *)data;
	if (saturn_call_environ(27, data)) { // GET_LOG_INTERFACE
		saturn_log_fn = lc->log;
	}
}
static void saturn_log_msg(int level, const char *msg) {
	if (saturn_log_fn) saturn_log_fn(level, "%s", msg);
}

struct saturn_game_info {
	const char *path;
	const void *data;
	size_t size;
	const char *meta;
};

struct saturn_system_info {
	const char *library_name;
	const char *library_version;
	const char *valid_extensions;
	bool need_fullpath;
	bool block_extract;
};

struct saturn_game_geometry {
	unsigned base_width;
	unsigned base_height;
	unsigned max_width;
	unsigned max_height;
	float aspect_ratio;
};

struct saturn_system_timing {
	double fps;
	double sample_rate;
};

struct saturn_system_av_info {
	struct saturn_game_geometry geometry;
	struct saturn_system_timing timing;
};

static const char *saturn_core_name(void) { return "iTaK GoSaturn"; }
static const char *saturn_core_version(void) { return "0.1"; }
static const char *saturn_core_extensions(void) { return "cue|iso"; }
*/
import "C"

import (
	"hash/fnv"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/system"
)

// libretro constants used by this shell.
const (
	envSetPixelFormat  = 10
	envGetLogInterface = 27 // handled inside the C helper
	envSetGeometry     = 37

	pixelFormatXRGB8888 = 1

	deviceJoypad = 1

	logInfo = 1
	logWarn = 2

	memSaveRAM   = 0 // RETRO_MEMORY_SAVE_RAM
	memSystemRAM = 1 // RETRO_MEMORY_SYSTEM_RAM
)

var (
	sys     *system.System
	hasDisc bool
	videoW  int
	videoH  int

	// sysRAMCache mirrors Work RAM Low then High for
	// RETRO_MEMORY_SYSTEM_RAM; sysRAMHash is the hash
	// of the cache as of the last sync.
	sysRAMCache []byte
	sysRAMHash  uint64
)

// joypadMap pairs libretro joypad ids with the
// Saturn pad bits they drive (see the package
// comment for the map's rationale). Id 2 (Select)
// is absent: the Saturn digital pad has no Select.
var joypadMap = []struct {
	id  uint
	bit uint16
}{
	{0, system.ButtonB},     // B
	{1, system.ButtonY},     // Y
	{3, system.ButtonStart}, // Start
	{4, system.ButtonUp},    // Up
	{5, system.ButtonDown},  // Down
	{6, system.ButtonLeft},  // Left
	{7, system.ButtonRight}, // Right
	{8, system.ButtonA},     // A
	{9, system.ButtonX},     // X
	{10, system.ButtonL},    // L
	{11, system.ButtonR},    // R
	{12, system.ButtonZ},    // L2: Saturn Z
	{13, system.ButtonC},    // R2: Saturn C
}

// saturnLog prints one line through the frontend
// log interface when one was offered.
func saturnLog(level int, msg string) {
	cmsg := C.CString(msg)
	C.saturn_log_msg(C.int(level), cmsg)
	C.free(unsafe.Pointer(cmsg))
}

func hashBytes(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// pullSystemRAM refreshes the SYSTEM_RAM cache from
// the machine and records its hash.
func pullSystemRAM() {
	copy(sysRAMCache[:0x100000], sys.Bus.WorkRAMLow())
	copy(sysRAMCache[0x100000:], sys.Bus.WorkRAMHigh())
	sysRAMHash = hashBytes(sysRAMCache)
}

// syncSystemRAM pushes frontend edits into the
// machine when the cache changed since the last
// sync, then pulls the machine state back after
// the frame. Called around RunFrame.
func pushSystemRAM() {
	if hashBytes(sysRAMCache) != sysRAMHash {
		copy(sys.Bus.WorkRAMLow(), sysRAMCache[:0x100000])
		copy(sys.Bus.WorkRAMHigh(), sysRAMCache[0x100000:])
	}
}

// loadPath loads a disc image from a filesystem
// path and boots it through BootDisc. It is the
// Go-level core of retro_load_game, kept plainly
// typed so the shell test can drive it in-process.
func loadPath(path string) bool {
	m := system.New()
	if err := m.LoadDisc(path); err != nil {
		saturnLog(logWarn, "[GoSaturn] cannot open disc image: "+err.Error())
		return false
	}
	if err := m.BootDisc(); err != nil {
		saturnLog(logWarn, "[GoSaturn] disc does not boot: "+err.Error())
		return false
	}
	pf := C.int(pixelFormatXRGB8888)
	C.saturn_call_environ(envSetPixelFormat, unsafe.Pointer(&pf))
	sys = m
	hasDisc = true
	videoW, videoH = m.DisplaySize()
	sysRAMCache = make([]byte, 2*1024*1024)
	pullSystemRAM()
	saturnLog(logInfo, "[GoSaturn] loaded "+path)
	return true
}

// runFrame advances the loaded machine one frame,
// presenting video and audio. It is the Go-level
// core of retro_run.
func runFrame() {
	if sys == nil {
		return
	}
	C.saturn_call_input_poll()
	var mask uint16
	for _, b := range joypadMap {
		if C.saturn_call_input_state(0, deviceJoypad, 0, C.uint(b.id)) != 0 {
			mask |= b.bit
		}
	}
	sys.SetButtons(mask)
	pushSystemRAM()
	sys.RunFrame()
	pullSystemRAM()

	w, h := sys.DisplaySize()
	if w != videoW || h != videoH {
		videoW, videoH = w, h
		geom := C.struct_saturn_game_geometry{
			base_width:   C.uint(w),
			base_height:  C.uint(h),
			max_width:    640,
			max_height:   480,
			aspect_ratio: 4.0 / 3.0,
		}
		C.saturn_call_environ(envSetGeometry, unsafe.Pointer(&geom))
	}
	if fb := sys.Framebuffer(); len(fb) >= w*h && w > 0 && h > 0 {
		C.saturn_call_video(unsafe.Pointer(&fb[0]), C.uint(w), C.uint(h), C.size_t(w*4))
	}
	samples := sys.AudioBuffer()
	if len(samples) == 0 {
		return
	}
	frames := len(samples) / 2
	done := int(C.saturn_call_audio_batch((*C.int16_t)(unsafe.Pointer(&samples[0])), C.size_t(frames)))
	for i := done * 2; i+1 < len(samples); i += 2 {
		C.saturn_call_audio(C.int16_t(samples[i]), C.int16_t(samples[i+1]))
	}
}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_init
func retro_init() {}

//export retro_deinit
func retro_deinit() { sys = nil }

//export retro_set_environment
func retro_set_environment(cb C.saturn_environ_cb_t) {
	C.saturn_set_environ_cb(cb)
	var lc C.struct_saturn_log_callback
	C.saturn_grab_log(unsafe.Pointer(&lc))
}

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.saturn_video_refresh_cb_t) { C.saturn_set_video_cb(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.saturn_audio_sample_cb_t) { C.saturn_set_audio_cb(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.saturn_audio_sample_batch_cb_t) {
	C.saturn_set_audio_batch_cb(cb)
}

//export retro_set_input_poll
func retro_set_input_poll(cb C.saturn_input_poll_cb_t) { C.saturn_set_input_poll_cb(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.saturn_input_state_cb_t) { C.saturn_set_input_state_cb(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port, device C.uint) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_saturn_system_info) {
	info.library_name = C.saturn_core_name()
	info.library_version = C.saturn_core_version()
	info.valid_extensions = C.saturn_core_extensions()
	info.need_fullpath = true
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_saturn_system_av_info) {
	w, h := 320, 224
	if sys != nil {
		w, h = sys.DisplaySize()
	}
	info.geometry.base_width = C.uint(w)
	info.geometry.base_height = C.uint(h)
	info.geometry.max_width = 640
	info.geometry.max_height = 480
	info.geometry.aspect_ratio = 4.0 / 3.0
	// 477,273 master cycles per frame at 28.6364 MHz
	// is the NTSC frame; the timing is declared as
	// the round 60.0 the machine definition gives.
	info.timing.fps = 60.0
	info.timing.sample_rate = 44100
}

//export retro_get_region
func retro_get_region() C.uint { return 0 } // NTSC

//export retro_load_game
func retro_load_game(info *C.struct_saturn_game_info) bool {
	if info == nil || info.path == nil {
		return false
	}
	return loadPath(C.GoString(info.path))
}

//export retro_unload_game
func retro_unload_game() {
	sys = nil
	hasDisc = false
}

//export retro_reset
func retro_reset() {
	if sys == nil {
		return
	}
	sys.Reset()
	if hasDisc {
		// A bare reset parks the CPUs with no program;
		// re-boot the loaded disc (see the package
		// comment).
		if err := sys.BootDisc(); err != nil {
			saturnLog(logWarn, "[GoSaturn] re-boot after reset failed: "+err.Error())
		}
	}
	videoW, videoH = sys.DisplaySize()
	pullSystemRAM()
}

//export retro_run
func retro_run() { runFrame() }

//export retro_serialize_size
func retro_serialize_size() C.size_t {
	if sys == nil {
		return 0
	}
	return C.size_t(sys.StateSize())
}

//export retro_serialize
func retro_serialize(data unsafe.Pointer, size C.size_t) bool {
	if sys == nil {
		return false
	}
	state := sys.Serialize()
	if int(size) < len(state) {
		return false
	}
	C.memcpy(data, unsafe.Pointer(&state[0]), C.size_t(len(state)))
	return true
}

//export retro_unserialize
func retro_unserialize(data unsafe.Pointer, size C.size_t) bool {
	if sys == nil {
		return false
	}
	ok := sys.Deserialize(C.GoBytes(data, C.int(size))) == nil
	if ok {
		videoW, videoH = sys.DisplaySize()
		pullSystemRAM()
	}
	return ok
}

//export retro_get_memory_data
func retro_get_memory_data(id C.uint) unsafe.Pointer {
	if sys == nil {
		return nil
	}
	switch id {
	case memSaveRAM:
		// The backup RAM logical store (32 KiB),
		// handed out directly: guest writes land in
		// this very slice through the bus.
		return unsafe.Pointer(&sys.Bus.BackupRAM()[0])
	case memSystemRAM:
		return unsafe.Pointer(&sysRAMCache[0])
	}
	return nil
}

//export retro_get_memory_size
func retro_get_memory_size(id C.uint) C.size_t {
	if sys == nil {
		return 0
	}
	switch id {
	case memSaveRAM:
		return C.size_t(len(sys.Bus.BackupRAM()))
	case memSystemRAM:
		return C.size_t(len(sysRAMCache))
	}
	return 0
}

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.uint, enabled bool, code *C.char) {}

func main() {}
