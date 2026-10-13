// Command libretro is the libretro shell for the
// from-scratch Go PlayStation core (PS1-4). It
// follows the family pattern set by the SNES and
// Genesis shells: a c-shared package main exporting
// the retro_* C ABI, with the machine behind it in
// the system package.
//
// Choices specific to this core:
//
//   - Content loads by path (need_fullpath): discs
//     (.cue, .bin, .iso, .img) open through the cd
//     image layer and boot through SYSTEM.CNF, the
//     way the console boots them; a bare .exe is an
//     HLE-booted PS-X executable. A disc with no
//     bootable SYSTEM.CNF is rejected, like the
//     third-party core it replaces rejects it.
//   - The guest framebuffer is presented at the
//     machine's current native display size every
//     frame (PlayStation games change resolution
//     mid-game), with a SET_GEOMETRY notice when the
//     size changes. Base geometry 320x240, max
//     640x480, aspect 4:3 (the app convention).
//   - The pad is forced into analog mode at load:
//     this shell always presents an analog pad. The
//     digital buttons still read digitally; the
//     sticks come from the libretro analog axes.
//   - Rumble is wired to the pad motor state through
//     SET_RUMBLE_INTERFACE when the frontend offers
//     it: the weak effect is the small motor (on or
//     off), the strong effect is the large motor
//     byte scaled to 16 bits. Frontends that do not
//     offer the interface (the iTaK Go bridge does
//     not) simply never rumble.
//   - At unload, the machine's not-implemented
//     diagnostics (unimplemented BIOS calls, unknown
//     CD and GP1 commands) are printed through the
//     frontend log interface when any were seen, so
//     a real game failing on the TV names exactly
//     what it called that this core does not have.
package main

/*
#cgo CFLAGS: -std=c11

#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
#include <stdbool.h>
#include <string.h>

typedef bool (*ps1_environ_cb_t)(unsigned cmd, void *data);
typedef void (*ps1_video_refresh_cb_t)(const void *data, unsigned width, unsigned height, size_t pitch);
typedef void (*ps1_audio_sample_cb_t)(int16_t left, int16_t right);
typedef size_t (*ps1_audio_sample_batch_cb_t)(const int16_t *data, size_t frames);
typedef void (*ps1_input_poll_cb_t)(void);
typedef int16_t (*ps1_input_state_cb_t)(unsigned port, unsigned device, unsigned index, unsigned id);
typedef void (*ps1_log_printf_t)(int level, const char *fmt, ...);
typedef bool (*ps1_set_rumble_state_t)(unsigned port, int effect, uint16_t strength);

static ps1_environ_cb_t ps1_environ_cb;
static ps1_video_refresh_cb_t ps1_video_cb;
static ps1_audio_sample_cb_t ps1_audio_cb;
static ps1_audio_sample_batch_cb_t ps1_audio_batch_cb;
static ps1_input_poll_cb_t ps1_input_poll_cb;
static ps1_input_state_cb_t ps1_input_state_cb;
static ps1_log_printf_t ps1_log_fn;
static ps1_set_rumble_state_t ps1_rumble_fn;

static void ps1_set_environ_cb(ps1_environ_cb_t cb) { ps1_environ_cb = cb; }
static void ps1_set_video_cb(ps1_video_refresh_cb_t cb) { ps1_video_cb = cb; }
static void ps1_set_audio_cb(ps1_audio_sample_cb_t cb) { ps1_audio_cb = cb; }
static void ps1_set_audio_batch_cb(ps1_audio_sample_batch_cb_t cb) { ps1_audio_batch_cb = cb; }
static void ps1_set_input_poll_cb(ps1_input_poll_cb_t cb) { ps1_input_poll_cb = cb; }
static void ps1_set_input_state_cb(ps1_input_state_cb_t cb) { ps1_input_state_cb = cb; }

static bool ps1_call_environ(unsigned cmd, void *data) {
	if (!ps1_environ_cb) return false;
	return ps1_environ_cb(cmd, data);
}
static void ps1_call_video(const void *data, unsigned w, unsigned h, size_t pitch) {
	if (ps1_video_cb) ps1_video_cb(data, w, h, pitch);
}
static void ps1_call_audio(int16_t l, int16_t r) {
	if (ps1_audio_cb) ps1_audio_cb(l, r);
}
static size_t ps1_call_audio_batch(const int16_t *data, size_t frames) {
	if (!ps1_audio_batch_cb) return 0;
	return ps1_audio_batch_cb(data, frames);
}
static void ps1_call_input_poll(void) {
	if (ps1_input_poll_cb) ps1_input_poll_cb();
}
static int16_t ps1_call_input_state(unsigned port, unsigned device, unsigned index, unsigned id) {
	if (!ps1_input_state_cb) return 0;
	return ps1_input_state_cb(port, device, index, id);
}

struct ps1_log_callback { ps1_log_printf_t log; };
static void ps1_grab_log(void *data) {
	struct ps1_log_callback *lc = (struct ps1_log_callback *)data;
	if (ps1_call_environ(27, data)) { // GET_LOG_INTERFACE
		ps1_log_fn = lc->log;
	}
}
static void ps1_log_msg(int level, const char *msg) {
	if (ps1_log_fn) ps1_log_fn(level, "%s", msg);
}

struct ps1_rumble_interface { ps1_set_rumble_state_t set_rumble_state; };
static void ps1_grab_rumble(void *data) {
	struct ps1_rumble_interface *ri = (struct ps1_rumble_interface *)data;
	if (ps1_call_environ(23, data)) { // SET_RUMBLE_INTERFACE
		ps1_rumble_fn = ri->set_rumble_state;
	}
}
static void ps1_call_rumble(unsigned port, int effect, uint16_t strength) {
	if (ps1_rumble_fn) ps1_rumble_fn(port, effect, strength);
}

struct ps1_game_info {
	const char *path;
	const void *data;
	size_t size;
	const char *meta;
};

struct ps1_system_info {
	const char *library_name;
	const char *library_version;
	const char *valid_extensions;
	bool need_fullpath;
	bool block_extract;
};

struct ps1_game_geometry {
	unsigned base_width;
	unsigned base_height;
	unsigned max_width;
	unsigned max_height;
	float aspect_ratio;
};

struct ps1_system_timing {
	double fps;
	double sample_rate;
};

struct ps1_system_av_info {
	struct ps1_game_geometry geometry;
	struct ps1_system_timing timing;
};

static const char *ps1_core_name(void) { return "iTaK GoPS1"; }
static const char *ps1_core_version(void) { return "0.1"; }
static const char *ps1_core_extensions(void) { return "cue|bin|iso|exe"; }
*/
import "C"

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/system"
)

// libretro constants used by this shell.
const (
	envSetPixelFormat     = 10
	envGetLogInterface    = 27 // handled inside the C helper
	envSetRumbleInterface = 23 // handled inside the C helper
	envSetGeometry        = 37

	pixelFormatXRGB8888 = 1

	deviceJoypad = 1
	deviceAnalog = 5

	rumbleStrong = 0
	rumbleWeak   = 1

	logInfo = 1
	logWarn = 2

	memSaveRAM   = 0 // RETRO_MEMORY_SAVE_RAM
	memSystemRAM = 1 // RETRO_MEMORY_SYSTEM_RAM
)

var (
	sys        *system.Machine
	videoW     int
	videoH     int
	diagLogged bool
	lastSmall  bool
	lastBig    uint8
)

// joypadMap pairs libretro joypad ids with the PS1
// pad bits they drive: the standard libretro pad
// layout mapped onto the PlayStation pad.
var joypadMap = []struct {
	id  uint
	bit uint32
}{
	{0, system.ButtonCross},  // B
	{1, system.ButtonSquare}, // Y
	{2, system.ButtonSelect}, // Select
	{3, system.ButtonStart},  // Start
	{4, system.ButtonUp},     // Up
	{5, system.ButtonDown},   // Down
	{6, system.ButtonLeft},   // Left
	{7, system.ButtonRight},  // Right
	{8, system.ButtonCircle}, // A
	{9, system.ButtonTri},    // X
	{10, system.ButtonL1},    // L
	{11, system.ButtonR1},    // R
	{12, system.ButtonL2},    // L2
	{13, system.ButtonR2},    // R2
	{14, system.ButtonL3},    // L3
	{15, system.ButtonR3},    // R3
}

// ps1Log prints one line through the frontend log
// interface when one was offered.
func ps1Log(level int, msg string) {
	cmsg := C.CString(msg)
	C.ps1_log_msg(C.int(level), cmsg)
	C.free(unsafe.Pointer(cmsg))
}

// logDiagnostics emits the machine's
// not-implemented tally, once per loaded game, when
// anything unimplemented was seen. The iTaK bridge
// prints core log lines to stderr; RetroArch shows
// them in its log.
func logDiagnostics() {
	if sys == nil || diagLogged {
		return
	}
	diagLogged = true
	d := sys.Diagnostics()
	if d.Empty() {
		return
	}
	ps1Log(logWarn, "[GoPS1] not-implemented report: "+d.String())
}

// loadPath loads content from a filesystem path:
// discs boot through SYSTEM.CNF, .exe files boot
// directly. It is the Go-level core of
// retro_load_game, kept plainly typed so the shell
// test can drive it in-process.
func loadPath(path string, data []byte) bool {
	m := system.New()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe":
		if len(data) == 0 {
			b, err := os.ReadFile(path)
			if err != nil {
				ps1Log(logWarn, "[GoPS1] cannot read "+path)
				return false
			}
			data = b
		}
		if err := m.LoadEXE(data); err != nil {
			ps1Log(logWarn, "[GoPS1] bad PS-X EXE: "+err.Error())
			return false
		}
	case ".cue", ".bin", ".iso", ".img":
		if err := m.OpenDisc(path); err != nil {
			ps1Log(logWarn, "[GoPS1] cannot open disc image: "+err.Error())
			return false
		}
		if err := m.BootDisc(); err != nil {
			ps1Log(logWarn, "[GoPS1] disc does not boot: "+err.Error())
			return false
		}
	default:
		// A data-only load with no usable path: a
		// PS-X EXE is recognizable by its magic.
		if len(data) >= 8 && string(data[:8]) == "PS-X EXE" {
			if err := m.LoadEXE(data); err != nil {
				ps1Log(logWarn, "[GoPS1] bad PS-X EXE: "+err.Error())
				return false
			}
			break
		}
		ps1Log(logWarn, "[GoPS1] unsupported content: "+path)
		return false
	}
	pf := C.int(pixelFormatXRGB8888)
	C.ps1_call_environ(envSetPixelFormat, unsafe.Pointer(&pf))
	// The shell always presents an analog pad (see
	// the package comment).
	m.SetAnalogMode(true)
	sys = m
	diagLogged = false
	lastSmall, lastBig = false, 0
	videoW, videoH = m.DisplayWidth(), m.DisplayHeight()
	ps1Log(logInfo, "[GoPS1] loaded "+path)
	return true
}

// axis reads one libretro analog axis as an
// unsigned stick byte, 128 centered.
func axis(index, id uint) uint8 {
	v := int(C.ps1_call_input_state(0, deviceAnalog, C.uint(index), C.uint(id)))
	return uint8((v >> 8) + 128)
}

// runFrame advances the loaded machine one frame,
// presenting video and audio. It is the Go-level
// core of retro_run.
func runFrame() {
	if sys == nil {
		return
	}
	C.ps1_call_input_poll()
	var mask uint32
	for _, b := range joypadMap {
		if C.ps1_call_input_state(0, deviceJoypad, 0, C.uint(b.id)) != 0 {
			mask |= b.bit
		}
	}
	sys.SetButtons(mask)
	// SetAnalog takes (rx, ry, lx, ly): right stick
	// is analog index 1, left stick index 0.
	sys.SetAnalog(axis(1, 0), axis(1, 1), axis(0, 0), axis(0, 1))
	sys.RunFrame()

	// Rumble follows the pad motor state when the
	// frontend offered the interface.
	small, big := sys.PAD.Motors()
	if small != lastSmall || big != lastBig {
		lastSmall, lastBig = small, big
		weak := uint16(0)
		if small {
			weak = 0xFFFF
		}
		C.ps1_call_rumble(0, rumbleWeak, C.uint16_t(weak))
		C.ps1_call_rumble(0, rumbleStrong, C.uint16_t(uint16(big)*0x101))
	}

	w, h := sys.DisplayWidth(), sys.DisplayHeight()
	if w != videoW || h != videoH {
		videoW, videoH = w, h
		geom := C.struct_ps1_game_geometry{
			base_width:   C.uint(w),
			base_height:  C.uint(h),
			max_width:    640,
			max_height:   480,
			aspect_ratio: 4.0 / 3.0,
		}
		C.ps1_call_environ(envSetGeometry, unsafe.Pointer(&geom))
	}
	if fb := sys.Framebuffer(); len(fb) >= w*h && w > 0 && h > 0 {
		C.ps1_call_video(unsafe.Pointer(&fb[0]), C.uint(w), C.uint(h), C.size_t(w*4))
	}
	samples := sys.AudioBuffer()
	if len(samples) == 0 {
		return
	}
	frames := len(samples) / 2
	done := int(C.ps1_call_audio_batch((*C.int16_t)(unsafe.Pointer(&samples[0])), C.size_t(frames)))
	for i := done * 2; i+1 < len(samples); i += 2 {
		C.ps1_call_audio(C.int16_t(samples[i]), C.int16_t(samples[i+1]))
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
func retro_set_environment(cb C.ps1_environ_cb_t) {
	C.ps1_set_environ_cb(cb)
	var lc C.struct_ps1_log_callback
	C.ps1_grab_log(unsafe.Pointer(&lc))
	var ri C.struct_ps1_rumble_interface
	C.ps1_grab_rumble(unsafe.Pointer(&ri))
}

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.ps1_video_refresh_cb_t) { C.ps1_set_video_cb(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.ps1_audio_sample_cb_t) { C.ps1_set_audio_cb(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.ps1_audio_sample_batch_cb_t) {
	C.ps1_set_audio_batch_cb(cb)
}

//export retro_set_input_poll
func retro_set_input_poll(cb C.ps1_input_poll_cb_t) { C.ps1_set_input_poll_cb(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.ps1_input_state_cb_t) { C.ps1_set_input_state_cb(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port, device C.uint) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_ps1_system_info) {
	info.library_name = C.ps1_core_name()
	info.library_version = C.ps1_core_version()
	info.valid_extensions = C.ps1_core_extensions()
	info.need_fullpath = true
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_ps1_system_av_info) {
	w, h := 320, 240
	if sys != nil {
		w, h = sys.DisplayWidth(), sys.DisplayHeight()
	}
	info.geometry.base_width = C.uint(w)
	info.geometry.base_height = C.uint(h)
	info.geometry.max_width = 640
	info.geometry.max_height = 480
	info.geometry.aspect_ratio = 4.0 / 3.0
	info.timing.fps = 59.94
	info.timing.sample_rate = system.AudioSampleRate
}

//export retro_get_region
func retro_get_region() C.uint { return 0 } // NTSC

//export retro_load_game
func retro_load_game(info *C.struct_ps1_game_info) bool {
	if info == nil {
		return false
	}
	path := ""
	if info.path != nil {
		path = C.GoString(info.path)
	}
	var data []byte
	if info.data != nil && info.size > 0 {
		data = C.GoBytes(info.data, C.int(info.size))
	}
	return loadPath(path, data)
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
	// Reset restores the pad to digital; the shell's
	// analog choice is reapplied (the machine also
	// reapplies its stored host choice).
	sys.SetAnalogMode(true)
	videoW, videoH = sys.DisplayWidth(), sys.DisplayHeight()
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
	return sys.Deserialize(C.GoBytes(data, C.int(size))) == nil
}

//export retro_get_memory_data
func retro_get_memory_data(id C.uint) unsafe.Pointer {
	if sys == nil {
		return nil
	}
	switch id {
	case memSaveRAM:
		// The 128 KB memory card image.
		return unsafe.Pointer(&sys.CardImage()[0])
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
		return C.size_t(len(sys.CardImage()))
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
