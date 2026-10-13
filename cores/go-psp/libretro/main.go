// Command libretro is the libretro shell for the Go PSP core
// ("iTaK GoPSP"). It follows the family C ABI shape the other
// Go cores use (see cores/go-saturn/libretro): libretro.h is
// not vendored, so the small C surface is declared by hand.
//
// Content loads by path: a .iso opens through the umd layer and
// boots PSP_GAME/SYSDR/BOOT.BIN (falling back to EBOOT.BIN
// beside it) through the kernel; .elf and .prx load directly
// through the same kernel boot. Video is the machine's
// 480x272 XRGB8888 frame at the machine's own frame rate
// (3,700,000 cycles per frame at 222 MHz = 60.0 fps). Audio is
// the kernel's mixed sceAudio stream, 44,100 Hz stereo, drained
// every frame. RETRO_MEMORY_SYSTEM_RAM is the 32 MB main RAM.
// RETRO_MEMORY_SAVE_RAM is nil: PSP saves flow through the
// IoFileMgr ms0: store the host installs, which has no stable
// fixed backing slice to expose (see the module README).
package main

/*
#include <stdlib.h>
#include <string.h>

typedef int psp_bool;
typedef void (*psp_video_refresh_t)(const void *data, unsigned width, unsigned height, size_t pitch);
typedef void (*psp_audio_sample_t)(int16_t left, int16_t right);
typedef size_t (*psp_audio_sample_batch_t)(const int16_t *data, size_t frames);
typedef int16_t (*psp_input_state_t)(unsigned port, unsigned device, unsigned index, unsigned id);
typedef void (*psp_input_poll_t)(void);
typedef psp_bool (*psp_environment_t)(unsigned cmd, void *data);
typedef void (*psp_log_printf_t)(int level, const char *fmt, ...);
typedef struct psp_log_callback { psp_log_printf_t log; } psp_log_callback_t;

typedef struct psp_game_info { const char *path; const void *data; size_t size; const char *meta; } psp_game_info_t;
typedef struct psp_system_info { const char *library_name; const char *library_version; const char *valid_extensions; psp_bool need_fullpath; psp_bool block_extract; } psp_system_info_t;
typedef struct psp_game_geometry { unsigned base_width; unsigned base_height; unsigned max_width; unsigned max_height; float aspect_ratio; } psp_game_geometry_t;
typedef struct psp_system_timing { double fps; double sample_rate; } psp_system_timing_t;
typedef struct psp_system_av_info { psp_game_geometry_t geometry; psp_system_timing_t timing; } psp_system_av_info_t;

static psp_video_refresh_t psp_video_cb;
static psp_audio_sample_t psp_audio_cb;
static psp_audio_sample_batch_t psp_audio_batch_cb;
static psp_input_state_t psp_input_cb;
static psp_input_poll_t psp_input_poll_cb;
static psp_environment_t psp_environ_cb;
static psp_log_callback_t psp_log_cb;
static psp_bool psp_have_log;

static void psp_set_video_cb(psp_video_refresh_t cb) { psp_video_cb = cb; }
static void psp_set_audio_cb(psp_audio_sample_t cb) { psp_audio_cb = cb; }
static void psp_set_audio_batch_cb(psp_audio_sample_batch_t cb) { psp_audio_batch_cb = cb; }
static void psp_set_input_state_cb(psp_input_state_t cb) { psp_input_cb = cb; }
static void psp_set_input_poll_cb(psp_input_poll_t cb) { psp_input_poll_cb = cb; }
static void psp_set_environ_cb(psp_environment_t cb) { psp_environ_cb = cb; }

static psp_bool psp_call_environ(unsigned cmd, void *data) {
	if (!psp_environ_cb) return 0;
	return psp_environ_cb(cmd, data);
}
static void psp_call_video(const void *data, unsigned width, unsigned height, size_t pitch) {
	if (psp_video_cb) psp_video_cb(data, width, height, pitch);
}
static void psp_call_audio(int16_t left, int16_t right) {
	if (psp_audio_cb) psp_audio_cb(left, right);
}
static size_t psp_call_audio_batch(const int16_t *data, size_t frames) {
	if (psp_audio_batch_cb) return psp_audio_batch_cb(data, frames);
	return 0;
}
static int16_t psp_call_input(unsigned port, unsigned device, unsigned index, unsigned id) {
	if (psp_input_cb) return psp_input_cb(port, device, index, id);
	return 0;
}
static void psp_call_input_poll(void) {
	if (psp_input_poll_cb) psp_input_poll_cb();
}
static void psp_grab_log(void) {
	psp_have_log = psp_call_environ(27, &psp_log_cb); // RETRO_ENVIRONMENT_GET_LOG_INTERFACE
}
*/
import "C"

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-psp/system"
)

// The libretro constants this shell uses (from libretro.h,
// redeclared here; the header is not vendored).

const (
	envSetPixelFormat  = 10
	envGetLogInterface = 27
	envSetGeometry     = 37

	pixelFormatXRGB8888 = 1

	deviceJoypad = 1
	deviceAnalog = 2

	joypadB      = 0
	joypadY      = 1
	joypadSelect = 2
	joypadStart  = 3
	joypadUp     = 4
	joypadDown   = 5
	joypadLeft   = 6
	joypadRight  = 7
	joypadA      = 8
	joypadX      = 9
	joypadL      = 10
	joypadR      = 11

	analogLeft    = 0
	analogX       = 0
	analogY       = 1
	memorySysRAM  = 2
	memorySaveRAM = 1
)

// The machine's own timing: 222 MHz over the 3,700,000 cycle
// frame budget is exactly 60 fps, and it is declared as such.
const (
	coreFPS       = 60.0
	coreAudioRate = 44100.0
)

// systemInfoValues is the declared core identity, shared by
// retro_get_system_info and the shell tests.
func systemInfoValues() (name, version, extensions string) {
	return "iTaK GoPSP", "1.0", "iso|elf|prx"
}

// avInfoValues is the declared geometry and timing, shared by
// retro_get_system_av_info and the shell tests: 480x272, the
// machine's own 60.0 fps (3,700,000 cycles per frame at
// 222 MHz), and the 44,100 Hz audio stream.
func avInfoValues() (width, height int, fps, sampleRate float64) {
	return system.Width, system.Height, coreFPS, coreAudioRate
}

var (
	mach *system.Machine

	// The loaded content, kept so retro_reset can re-boot it.
	loadedPath string
	loadedData []byte // executable images only (.elf/.prx)
	loadedISO  bool

	lastWidth  int
	lastHeight int

	cstrName    *C.char
	cstrVersion *C.char
	cstrExts    *C.char
)

func init() {
	name, version, exts := systemInfoValues()
	cstrName = C.CString(name)
	cstrVersion = C.CString(version)
	cstrExts = C.CString(exts)
}

func main() {}

// framePitch is the framebuffer pitch in bytes.
func framePitch(w int) uintptr { return uintptr(w * 4) }

// joypadButtons maps the libretro joypad onto PSP buttons by
// position: Cross is B (bottom), Circle is A (right), Square
// is Y (left), Triangle is X (top).
func joypadButtons() uint32 {
	var b uint32
	pad := func(id uint) bool {
		return C.psp_call_input(0, deviceJoypad, 0, C.uint(id)) != 0
	}
	if pad(joypadB) {
		b |= system.ButtonCross
	}
	if pad(joypadA) {
		b |= system.ButtonCircle
	}
	if pad(joypadY) {
		b |= system.ButtonSquare
	}
	if pad(joypadX) {
		b |= system.ButtonTriangle
	}
	if pad(joypadStart) {
		b |= system.ButtonStart
	}
	if pad(joypadSelect) {
		b |= system.ButtonSelect
	}
	if pad(joypadL) {
		b |= system.ButtonL
	}
	if pad(joypadR) {
		b |= system.ButtonR
	}
	if pad(joypadUp) {
		b |= system.ButtonUp
	}
	if pad(joypadDown) {
		b |= system.ButtonDown
	}
	if pad(joypadLeft) {
		b |= system.ButtonLeft
	}
	if pad(joypadRight) {
		b |= system.ButtonRight
	}
	return b
}

// analogByte converts a libretro axis (-32768..32767) to the
// PSP stick byte (0..255, 0x80 centered).
func analogByte(v int16) byte {
	n := (int32(v) + 32768) >> 8
	if n < 0 {
		n = 0
	}
	if n > 255 {
		n = 255
	}
	return byte(n)
}

// loadPath boots content from a filesystem path.
func loadPath(path string) bool {
	m := system.New()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".iso":
		if err := m.BootUMD(path); err != nil {
			return false
		}
		mach = m
		loadedPath, loadedData, loadedISO = path, nil, true
		return true
	case ".elf", ".prx":
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		if err := m.LoadExecutable(data); err != nil {
			return false
		}
		mach = m
		loadedPath, loadedData, loadedISO = path, data, false
		return true
	}
	return false
}

// reboot re-boots the loaded content (retro_reset).
func reboot() bool {
	if loadedPath == "" {
		return false
	}
	m := system.New()
	if loadedISO {
		if err := m.BootUMD(loadedPath); err != nil {
			return false
		}
	} else {
		if err := m.LoadExecutable(loadedData); err != nil {
			return false
		}
	}
	mach = m
	return true
}

//export retro_set_environment
func retro_set_environment(cb C.psp_environment_t) {
	C.psp_set_environ_cb(cb)
	C.psp_grab_log()
	var pixelFormat C.int = pixelFormatXRGB8888
	C.psp_call_environ(envSetPixelFormat, unsafe.Pointer(&pixelFormat))
}

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.psp_video_refresh_t) { C.psp_set_video_cb(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.psp_audio_sample_t) { C.psp_set_audio_cb(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.psp_audio_sample_batch_t) { C.psp_set_audio_batch_cb(cb) }

//export retro_set_input_poll
func retro_set_input_poll(cb C.psp_input_poll_t) { C.psp_set_input_poll_cb(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.psp_input_state_t) { C.psp_set_input_state_cb(cb) }

//export retro_init
func retro_init() {}

//export retro_deinit
func retro_deinit() {}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_get_system_info
func retro_get_system_info(info *C.struct_psp_system_info) {
	info.library_name = cstrName
	info.library_version = cstrVersion
	info.valid_extensions = cstrExts
	info.need_fullpath = 1
	info.block_extract = 0
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_psp_system_av_info) {
	info.geometry.base_width = system.Width
	info.geometry.base_height = system.Height
	info.geometry.max_width = system.Width
	info.geometry.max_height = system.Height
	info.geometry.aspect_ratio = C.float(float64(system.Width) / float64(system.Height))
	info.timing.fps = coreFPS
	info.timing.sample_rate = coreAudioRate
}

//export retro_set_controller_port_device
func retro_set_controller_port_device(port, device C.uint) {}

//export retro_reset
func retro_reset() { reboot() }

// runFrame advances one frame and presents it; factored out of
// retro_run so tests can drive the shell in-process.
func runFrame() {
	if mach == nil {
		return
	}
	C.psp_call_input_poll()
	mach.SetButtons(joypadButtons())
	mach.SetAnalog(
		analogByte(int16(C.psp_call_input(0, deviceAnalog, analogLeft, analogX))),
		analogByte(int16(C.psp_call_input(0, deviceAnalog, analogLeft, analogY))),
	)
	mach.RunFrame()

	fb := mach.Framebuffer()
	w, h := system.Width, system.Height
	if w != lastWidth || h != lastHeight {
		lastWidth, lastHeight = w, h
		geom := C.struct_psp_game_geometry{
			base_width:   C.uint(w),
			base_height:  C.uint(h),
			max_width:    C.uint(w),
			max_height:   C.uint(h),
			aspect_ratio: C.float(float64(w) / float64(h)),
		}
		C.psp_call_environ(envSetGeometry, unsafe.Pointer(&geom))
	}
	C.psp_call_video(unsafe.Pointer(&fb[0]), C.uint(w), C.uint(h), C.size_t(framePitch(w)))

	buf := mach.AudioBuffer()
	if len(buf) == 0 {
		return
	}
	n := int(C.psp_call_audio_batch((*C.int16_t)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)/2)))
	if n < 0 {
		n = 0
	}
	for i := n * 2; i+1 < len(buf); i += 2 {
		C.psp_call_audio(C.int16_t(buf[i]), C.int16_t(buf[i+1]))
	}
}

//export retro_run
func retro_run() { runFrame() }

//export retro_serialize_size
func retro_serialize_size() C.size_t {
	if mach == nil {
		return 0
	}
	return C.size_t(mach.StateSize())
}

//export retro_serialize
func retro_serialize(data unsafe.Pointer, size C.size_t) C.int {
	if mach == nil {
		return 0
	}
	state := mach.Serialize()
	if int(size) < len(state) {
		return 0
	}
	dst := unsafe.Slice((*byte)(data), int(size))
	copy(dst, state)
	return 1
}

//export retro_unserialize
func retro_unserialize(data unsafe.Pointer, size C.size_t) C.int {
	if mach == nil {
		return 0
	}
	buf := unsafe.Slice((*byte)(data), int(size))
	if err := mach.Deserialize(buf); err != nil {
		return 0
	}
	return 1
}

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.uint, enabled C.int, code *C.char) {}

//export retro_load_game
func retro_load_game(info *C.struct_psp_game_info) bool {
	if info == nil || info.path == nil {
		return false
	}
	return loadPath(C.GoString(info.path))
}

//export retro_load_game_special
func retro_load_game_special(gameType C.uint, info *C.struct_psp_game_info, numInfo C.size_t) bool {
	return false
}

//export retro_unload_game
func retro_unload_game() {
	mach = nil
	loadedPath, loadedData, loadedISO = "", nil, false
}

//export retro_get_region
func retro_get_region() C.uint { return 0 } // RETRO_REGION_NTSC

//export retro_get_memory_data
func retro_get_memory_data(id C.uint) unsafe.Pointer {
	if mach == nil {
		return nil
	}
	switch id {
	case memorySysRAM:
		return unsafe.Pointer(&mach.Bus.RAM[0])
	case memorySaveRAM:
		// PSP saves flow through the host installed ms0:
		// store; there is no fixed backing slice to expose.
		return nil
	}
	return nil
}

//export retro_get_memory_size
func retro_get_memory_size(id C.uint) C.size_t {
	if mach == nil {
		return 0
	}
	switch id {
	case memorySysRAM:
		return C.size_t(len(mach.Bus.RAM))
	case memorySaveRAM:
		return 0
	}
	return 0
}
