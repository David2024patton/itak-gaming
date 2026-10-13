// Command libretro is the libretro shell for the
// from-scratch Go Dreamcast core (DC-4). It follows the
// family pattern set by the SNES, Genesis, and PS1 shells:
// a c-shared package main exporting the retro_* C ABI,
// with the machine behind it in the system package.
//
// Choices specific to this core:
//
//   - Content loads by path (need_fullpath): a .gdi
//     needs its track files resolved relative to the
//     .gdi's own directory, which only a real path can
//     do. The gd layer opens .gdi, .iso, and .bin images
//     (there is no .cue parser in this core, so cue is
//     not advertised), and the machine boots the disc
//     through the HLE BIOS (system BootDisc). A disc
//     with a missing or malformed IP.BIN is rejected.
//   - Geometry is fixed at 640x480, the PVR Present
//     output, XRGB8888, aspect 4:3 (the app convention).
//   - Timing: the machine frame is a flat budget of
//     3,333,333 SH-4 cycles at the documented 200 MHz
//     nominal clock, so the declared fps is the quotient
//     200,000,000 / 3,333,333 = 60.000006..., declared
//     as computed below rather than rounded to the
//     NTSC 59.94 figure the brief first suggested: the
//     core's own constants give 60.0, not 59.94.
//   - Audio is the AICA's native 44,100 Hz stereo.
//   - The pad maps the libretro standard layout onto
//     the Dreamcast pad: A, B, X, Y, Start, and the
//     dpad from the joypad buttons; the analog triggers
//     from the libretro analog button axes (L2 and R2),
//     falling back to a full press from the digital L
//     and R buttons; the main stick from the left analog
//     axes, scaled to the pad's 0..255 axes with 128
//     centered. The iTaK Go bridge serves only digital
//     joypad state, so through the app the stick reads
//     centered and the triggers read through L and R.
//   - RETRO_MEMORY_SYSTEM_RAM is the 16 MB main RAM;
//     RETRO_MEMORY_SAVE_RAM is the live 128 KB VMU
//     image of port 0's storage device.
//   - At unload, the BIOS HLE diagnostics (every
//     unimplemented BIOS call shape the game hit) are
//     printed through the frontend log interface when
//     any were seen, the PS1 core's pattern.
package main

/*
#cgo CFLAGS: -std=c11

#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
#include <stdbool.h>
#include <string.h>

typedef bool (*godc_environ_cb_t)(unsigned cmd, void *data);
typedef void (*godc_video_refresh_cb_t)(const void *data, unsigned width, unsigned height, size_t pitch);
typedef void (*godc_audio_sample_cb_t)(int16_t left, int16_t right);
typedef size_t (*godc_audio_sample_batch_cb_t)(const int16_t *data, size_t frames);
typedef void (*godc_input_poll_cb_t)(void);
typedef int16_t (*godc_input_state_cb_t)(unsigned port, unsigned device, unsigned index, unsigned id);
typedef void (*godc_log_printf_t)(int level, const char *fmt, ...);

static godc_environ_cb_t godc_environ_cb;
static godc_video_refresh_cb_t godc_video_cb;
static godc_audio_sample_cb_t godc_audio_cb;
static godc_audio_sample_batch_cb_t godc_audio_batch_cb;
static godc_input_poll_cb_t godc_input_poll_cb;
static godc_input_state_cb_t godc_input_state_cb;
static godc_log_printf_t godc_log_fn;

static void godc_set_environ_cb(godc_environ_cb_t cb) { godc_environ_cb = cb; }
static void godc_set_video_cb(godc_video_refresh_cb_t cb) { godc_video_cb = cb; }
static void godc_set_audio_cb(godc_audio_sample_cb_t cb) { godc_audio_cb = cb; }
static void godc_set_audio_batch_cb(godc_audio_sample_batch_cb_t cb) { godc_audio_batch_cb = cb; }
static void godc_set_input_poll_cb(godc_input_poll_cb_t cb) { godc_input_poll_cb = cb; }
static void godc_set_input_state_cb(godc_input_state_cb_t cb) { godc_input_state_cb = cb; }

static bool godc_call_environ(unsigned cmd, void *data) {
	if (!godc_environ_cb) return false;
	return godc_environ_cb(cmd, data);
}
static void godc_call_video(const void *data, unsigned w, unsigned h, size_t pitch) {
	if (godc_video_cb) godc_video_cb(data, w, h, pitch);
}
static void godc_call_audio(int16_t l, int16_t r) {
	if (godc_audio_cb) godc_audio_cb(l, r);
}
static size_t godc_call_audio_batch(const int16_t *data, size_t frames) {
	if (!godc_audio_batch_cb) return 0;
	return godc_audio_batch_cb(data, frames);
}
static void godc_call_input_poll(void) {
	if (godc_input_poll_cb) godc_input_poll_cb();
}
static int16_t godc_call_input_state(unsigned port, unsigned device, unsigned index, unsigned id) {
	if (!godc_input_state_cb) return 0;
	return godc_input_state_cb(port, device, index, id);
}

struct godc_log_callback { godc_log_printf_t log; };
static void godc_grab_log(void *data) {
	struct godc_log_callback *lc = (struct godc_log_callback *)data;
	if (godc_call_environ(27, data)) { // GET_LOG_INTERFACE
		godc_log_fn = lc->log;
	}
}
static void godc_log_msg(int level, const char *msg) {
	if (godc_log_fn) godc_log_fn(level, "%s", msg);
}

struct godc_game_info {
	const char *path;
	const void *data;
	size_t size;
	const char *meta;
};

struct godc_system_info {
	const char *library_name;
	const char *library_version;
	const char *valid_extensions;
	bool need_fullpath;
	bool block_extract;
};

struct godc_game_geometry {
	unsigned base_width;
	unsigned base_height;
	unsigned max_width;
	unsigned max_height;
	float aspect_ratio;
};

struct godc_system_timing {
	double fps;
	double sample_rate;
};

struct godc_system_av_info {
	struct godc_game_geometry geometry;
	struct godc_system_timing timing;
};

static const char *godc_core_name(void) { return "iTaK GoDC"; }
static const char *godc_core_version(void) { return "0.1"; }
static const char *godc_core_extensions(void) { return "gdi|iso|bin"; }
*/
import "C"

import (
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/system"
)

// libretro constants used by this shell.
const (
	envSetPixelFormat = 10

	pixelFormatXRGB8888 = 1

	deviceJoypad = 1
	deviceAnalog = 5

	// Analog axis indexes: left stick 0, right stick 1,
	// analog buttons (the trigger axes) 2.
	analogLeft   = 0
	analogButton = 2
	analogX      = 0
	analogY      = 1

	// Joypad ids for the analog trigger axes.
	joypadL2 = 12
	joypadR2 = 13

	logInfo = 1
	logWarn = 2

	memSaveRAM   = 0 // RETRO_MEMORY_SAVE_RAM
	memSystemRAM = 1 // RETRO_MEMORY_SYSTEM_RAM

	// fps is the machine's frame rate from its own
	// documented constants: 200 MHz nominal SH-4 clock
	// over the 3,333,333 cycle flat frame budget.
	fps = 200_000_000.0 / system.FrameCycles

	sampleRate = 44100 // the AICA's native output rate
)

var (
	sys        *system.Machine
	diagLogged bool
)

// joypadMap pairs libretro joypad ids with the DC pad
// bits they drive. Select (2), L3 (14), and R3 (15) have
// no Dreamcast counterpart and are not mapped.
var joypadMap = []struct {
	id  uint
	bit uint16
}{
	{0, system.BtnB},     // B
	{1, system.BtnY},     // Y
	{3, system.BtnStart}, // Start
	{4, system.BtnUp},    // Up
	{5, system.BtnDown},  // Down
	{6, system.BtnLeft},  // Left
	{7, system.BtnRight}, // Right
	{8, system.BtnA},     // A
	{9, system.BtnX},     // X
	{10, system.BtnLT},   // L (digital trigger fallback)
	{11, system.BtnRT},   // R (digital trigger fallback)
}

// dcLog prints one line through the frontend log
// interface when one was offered.
func dcLog(level int, msg string) {
	cmsg := C.CString(msg)
	C.godc_log_msg(C.int(level), cmsg)
	C.free(unsafe.Pointer(cmsg))
}

// logDiagnostics emits the BIOS HLE's unimplemented
// call tally, once per loaded game, when anything
// unimplemented was seen. The iTaK bridge prints core
// log lines to stderr; RetroArch shows them in its log.
func logDiagnostics() {
	if sys == nil || diagLogged {
		return
	}
	diagLogged = true
	d := sys.BIOSDiagnostics()
	if len(d.Calls) == 0 && d.Overflow == 0 {
		return
	}
	dcLog(logWarn, "[GoDC] "+d.String())
}

// loadPath loads a disc image from a filesystem path
// and boots it through the HLE BIOS. It is the
// Go-level core of retro_load_game, kept plainly typed
// so the shell test can drive it in-process.
func loadPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gdi", ".iso", ".bin":
	default:
		dcLog(logWarn, "[GoDC] unsupported content: "+path)
		return false
	}
	m := system.New()
	if err := m.LoadDisc(path); err != nil {
		dcLog(logWarn, "[GoDC] cannot open disc image: "+err.Error())
		return false
	}
	if err := m.BootDisc(); err != nil {
		dcLog(logWarn, "[GoDC] disc does not boot: "+err.Error())
		return false
	}
	pf := C.int(pixelFormatXRGB8888)
	C.godc_call_environ(envSetPixelFormat, unsafe.Pointer(&pf))
	sys = m
	diagLogged = false
	dcLog(logInfo, "[GoDC] loaded "+path)
	return true
}

// stickAxis reads one libretro analog stick axis as an
// unsigned pad axis byte, 128 centered.
func stickAxis(index, id uint) uint8 {
	v := int(C.godc_call_input_state(0, deviceAnalog, C.uint(index), C.uint(id)))
	return uint8((v >> 8) + 128)
}

// triggerAxis reads one libretro analog trigger axis
// as a 0..255 pull; a zero analog read with the digital
// button down is a full press.
func triggerAxis(id uint, digital bool) uint8 {
	v := int(C.godc_call_input_state(0, deviceAnalog, analogButton, C.uint(id)))
	if v < 0 {
		v = 0
	}
	t := uint8(v >> 7)
	if t == 0 && digital {
		t = 255
	}
	return t
}

// readPad polls the frontend and returns the DC pad
// state for player 0.
func readPad() system.PadState {
	var mask uint16
	down := map[uint]bool{}
	for _, b := range joypadMap {
		if C.godc_call_input_state(0, deviceJoypad, 0, C.uint(b.id)) != 0 {
			mask |= b.bit
			down[b.id] = true
		}
	}
	// The digital trigger buttons also count as held
	// for the trigger fallback even though they are in
	// the map already; the analog read decides.
	return system.PadState{
		Mask:     mask,
		TriggerL: triggerAxis(joypadL2, down[10]),
		TriggerR: triggerAxis(joypadR2, down[11]),
		JoyX:     stickAxis(analogLeft, analogX),
		JoyY:     stickAxis(analogLeft, analogY),
	}
}

// runFrame advances the loaded machine one frame,
// presenting video and audio. It is the Go-level core
// of retro_run.
func runFrame() {
	if sys == nil {
		return
	}
	C.godc_call_input_poll()
	sys.SetButtons(readPad())
	sys.RunFrame()
	if fb := sys.Framebuffer(); len(fb) >= system.Width*system.Height {
		C.godc_call_video(unsafe.Pointer(&fb[0]), C.uint(system.Width),
			C.uint(system.Height), C.size_t(system.Width*4))
	}
	pairs := sys.AudioBuffer()
	if len(pairs) == 0 {
		return
	}
	flat := make([]int16, 2*len(pairs))
	for i, p := range pairs {
		flat[2*i], flat[2*i+1] = p[0], p[1]
	}
	done := int(C.godc_call_audio_batch((*C.int16_t)(unsafe.Pointer(&flat[0])), C.size_t(len(pairs))))
	for i := done; i < len(pairs); i++ {
		C.godc_call_audio(C.int16_t(pairs[i][0]), C.int16_t(pairs[i][1]))
	}
}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_init
func retro_init() {}

//export retro_deinit
func retro_deinit() {
	// The iTaK bridge calls retro_deinit without a
	// preceding retro_unload_game; emit the
	// diagnostics here too (guarded to once).
	logDiagnostics()
	sys = nil
}

//export retro_set_environment
func retro_set_environment(cb C.godc_environ_cb_t) {
	C.godc_set_environ_cb(cb)
	var lc C.struct_godc_log_callback
	C.godc_grab_log(unsafe.Pointer(&lc))
}

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.godc_video_refresh_cb_t) { C.godc_set_video_cb(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.godc_audio_sample_cb_t) { C.godc_set_audio_cb(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.godc_audio_sample_batch_cb_t) {
	C.godc_set_audio_batch_cb(cb)
}

//export retro_set_input_poll
func retro_set_input_poll(cb C.godc_input_poll_cb_t) { C.godc_set_input_poll_cb(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.godc_input_state_cb_t) { C.godc_set_input_state_cb(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port, device C.uint) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_godc_system_info) {
	info.library_name = C.godc_core_name()
	info.library_version = C.godc_core_version()
	info.valid_extensions = C.godc_core_extensions()
	info.need_fullpath = true
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_godc_system_av_info) {
	info.geometry.base_width = C.uint(system.Width)
	info.geometry.base_height = C.uint(system.Height)
	info.geometry.max_width = C.uint(system.Width)
	info.geometry.max_height = C.uint(system.Height)
	info.geometry.aspect_ratio = 4.0 / 3.0
	info.timing.fps = fps
	info.timing.sample_rate = sampleRate
}

//export retro_get_region
func retro_get_region() C.uint { return 0 } // NTSC

//export retro_load_game
func retro_load_game(info *C.struct_godc_game_info) bool {
	if info == nil || info.path == nil {
		return false
	}
	return loadPath(C.GoString(info.path))
}

//export retro_unload_game
func retro_unload_game() {
	logDiagnostics()
	sys = nil
}

//export retro_reset
func retro_reset() {
	if sys == nil {
		return
	}
	sys.Reset()
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
	return sys.Deserialize(C.GoBytes(data, C.int(size)))
}

//export retro_get_memory_data
func retro_get_memory_data(id C.uint) unsafe.Pointer {
	if sys == nil {
		return nil
	}
	switch id {
	case memSaveRAM:
		// Port 0's 128 KB VMU image.
		return unsafe.Pointer(&sys.VMUImage()[0])
	case memSystemRAM:
		return unsafe.Pointer(&sys.SystemRAM()[0])
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
		return C.size_t(len(sys.VMUImage()))
	case memSystemRAM:
		return C.size_t(len(sys.SystemRAM()))
	}
	return 0
}

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.uint, enabled bool, code *C.char) {}

func main() {}
