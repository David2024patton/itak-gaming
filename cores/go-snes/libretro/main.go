// Command libretro builds the Go SNES core as a libretro shared
// library (go build -buildmode=c-shared). It is a thin C ABI shell
// over the system package: the frontend (the iTaK engine) registers
// its callbacks, hands us a ROM, and calls retro_run once per
// frame.
package main

/*
#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

typedef bool (*retro_environment_t)(unsigned cmd, void *data);
typedef void (*retro_video_refresh_t)(const void *data, unsigned width, unsigned height, size_t pitch);
typedef void (*retro_audio_sample_t)(int16_t left, int16_t right);
typedef size_t (*retro_audio_sample_batch_t)(const int16_t *data, size_t frames);
typedef void (*retro_input_poll_t)(void);
typedef int16_t (*retro_input_state_t)(unsigned port, unsigned device, unsigned index, unsigned id);

static retro_environment_t environ_cb;
static retro_video_refresh_t video_cb;
static retro_audio_sample_t audio_cb;
static retro_audio_sample_batch_t audio_batch_cb;
static retro_input_poll_t input_poll_cb;
static retro_input_state_t input_state_cb;

static void snes_set_environ(retro_environment_t cb) { environ_cb = cb; }
static void snes_set_video(retro_video_refresh_t cb) { video_cb = cb; }
static void snes_set_audio(retro_audio_sample_t cb) { audio_cb = cb; }
static void snes_set_audio_batch(retro_audio_sample_batch_t cb) { audio_batch_cb = cb; }
static void snes_set_input_poll(retro_input_poll_t cb) { input_poll_cb = cb; }
static void snes_set_input_state(retro_input_state_t cb) { input_state_cb = cb; }

static bool snes_environ(unsigned cmd, void *data) { return environ_cb ? environ_cb(cmd, data) : false; }
static void snes_video(const void *data, unsigned w, unsigned h, size_t pitch) { if (video_cb) video_cb(data, w, h, pitch); }
static size_t snes_audio_batch(const int16_t *data, size_t frames) { return audio_batch_cb ? audio_batch_cb(data, frames) : 0; }
static void snes_audio_sample(int16_t l, int16_t r) { if (audio_cb) audio_cb(l, r); }
static void snes_input_poll(void) { if (input_poll_cb) input_poll_cb(); }
static int16_t snes_input_state(unsigned port, unsigned dev, unsigned idx, unsigned id) { return input_state_cb ? input_state_cb(port, dev, idx, id) : 0; }

static const char *snes_get_name() { return "iTaK GoSNES"; }
static const char *snes_get_version() { return "0.1"; }
static const char *snes_get_exts() { return "sfc|smc"; }

struct snes_system_info { const char *library_name; const char *library_version; const char *valid_extensions; bool need_fullpath; bool block_extract; };
struct snes_game_geometry { unsigned base_width; unsigned base_height; unsigned max_width; unsigned max_height; float aspect_ratio; };
struct snes_system_timing { double fps; double sample_rate; };
struct snes_av_info { struct snes_game_geometry geometry; struct snes_system_timing timing; };
struct snes_game_info { const char *path; const void *data; size_t size; const char *meta; };
*/
import "C"

import (
	"os"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-snes/ppu"
	"github.com/davidpatton/itak-gaming/cores/go-snes/system"
)

// libretro constants used here (from the public libretro API).
const (
	envSetPixelFormat  = 10
	pixelFormatXRGB888 = 1
	deviceJoypad       = 1
	joypadB            = 0
	joypadY            = 1
	joypadSelect       = 2
	joypadStart        = 3
	joypadUp           = 4
	joypadDown         = 5
	joypadLeft         = 6
	joypadRight        = 7
	joypadA            = 8
	joypadX            = 9
	joypadL            = 10
	joypadR            = 11
	regionNTSC         = 0
	snesFPS            = 60.0988 // NTSC SNES frame rate
	snesAspect         = 1.33333 // 4/3, the app convention for SNES
	snesSampleRate     = 32000   // DSP output rate, stereo
	memSaveRAM         = 0
	memSystemRAM       = 1
)

var sys *system.System

func main() {}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_init
func retro_init() {}

//export retro_deinit
func retro_deinit() { sys = nil }

//export retro_set_environment
func retro_set_environment(cb C.retro_environment_t) { C.snes_set_environ(cb) }

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.retro_video_refresh_t) { C.snes_set_video(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.retro_audio_sample_t) { C.snes_set_audio(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.retro_audio_sample_batch_t) { C.snes_set_audio_batch(cb) }

//export retro_set_input_poll
func retro_set_input_poll(cb C.retro_input_poll_t) { C.snes_set_input_poll(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.retro_input_state_t) { C.snes_set_input_state(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port C.unsigned, device C.unsigned) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_snes_system_info) {
	info.library_name = C.snes_get_name()
	info.library_version = C.snes_get_version()
	info.valid_extensions = C.snes_get_exts()
	info.need_fullpath = false
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_snes_av_info) {
	info.geometry.base_width = ppu.Width
	info.geometry.base_height = ppu.Height
	info.geometry.max_width = ppu.Width
	info.geometry.max_height = ppu.Height
	info.geometry.aspect_ratio = snesAspect
	info.timing.fps = snesFPS
	info.timing.sample_rate = snesSampleRate
}

//export retro_get_region
func retro_get_region() C.unsigned { return regionNTSC }

//export retro_load_game
func retro_load_game(info *C.struct_snes_game_info) C.bool {
	var data []byte
	if info.data != nil && info.size > 0 {
		data = C.GoBytes(info.data, C.int(info.size))
	} else if info.path != nil {
		b, err := os.ReadFile(C.GoString(info.path))
		if err != nil {
			return false
		}
		data = b
	} else {
		return false
	}
	s, err := system.New(data)
	if err != nil {
		return false
	}
	sys = s
	// Ask the frontend for XRGB8888 frames (what the PPU produces).
	fmt888 := C.uint32_t(pixelFormatXRGB888)
	C.snes_environ(envSetPixelFormat, unsafe.Pointer(&fmt888))
	return true
}

//export retro_unload_game
func retro_unload_game() { sys = nil }

//export retro_reset
func retro_reset() {
	if sys != nil {
		sys.Reset()
	}
}

func pressed(port int, id uint) bool {
	return C.snes_input_state(C.unsigned(port), deviceJoypad, 0, C.unsigned(id)) != 0
}

// pollPad translates libretro joypad state into the SNES button
// mask. The libretro joypad ids use the standard order B, Y,
// Select, Start, Up, Down, Left, Right, A, X, L, R, which matches
// the system Button bit order 1:1, so this is a direct mapping.
func pollPad(port int) uint16 {
	var m uint16
	if pressed(port, joypadB) {
		m |= system.ButtonB
	}
	if pressed(port, joypadY) {
		m |= system.ButtonY
	}
	if pressed(port, joypadSelect) {
		m |= system.ButtonSelect
	}
	if pressed(port, joypadStart) {
		m |= system.ButtonStart
	}
	if pressed(port, joypadUp) {
		m |= system.ButtonUp
	}
	if pressed(port, joypadDown) {
		m |= system.ButtonDown
	}
	if pressed(port, joypadLeft) {
		m |= system.ButtonLeft
	}
	if pressed(port, joypadRight) {
		m |= system.ButtonRight
	}
	if pressed(port, joypadA) {
		m |= system.ButtonA
	}
	if pressed(port, joypadX) {
		m |= system.ButtonX
	}
	if pressed(port, joypadL) {
		m |= system.ButtonL
	}
	if pressed(port, joypadR) {
		m |= system.ButtonR
	}
	return m
}

//export retro_run
func retro_run() {
	if sys == nil {
		return
	}
	C.snes_input_poll()
	sys.SetButtons(0, pollPad(0))
	sys.SetButtons(1, pollPad(1))
	sys.RunFrame()

	fb := sys.Framebuffer()
	if len(fb) > 0 {
		C.snes_video(unsafe.Pointer(&fb[0]),
			C.unsigned(ppu.Width), C.unsigned(ppu.Height),
			C.size_t(ppu.Width*4))
	}
	ab := sys.AudioBuffer()
	if len(ab) >= 2 {
		frames := len(ab) / 2
		done := int(C.snes_audio_batch((*C.int16_t)(unsafe.Pointer(&ab[0])), C.size_t(frames)))
		for i := done; i < frames; i++ {
			C.snes_audio_sample(C.int16_t(ab[2*i]), C.int16_t(ab[2*i+1]))
		}
	}
}

//export retro_serialize_size
func retro_serialize_size() C.size_t {
	if sys == nil {
		return 0
	}
	return C.size_t(sys.StateSize())
}

//export retro_serialize
func retro_serialize(data unsafe.Pointer) C.bool {
	if sys == nil || data == nil {
		return false
	}
	state := sys.Serialize()
	if len(state) == 0 {
		return false
	}
	buf := unsafe.Slice((*byte)(data), len(state))
	copy(buf, state)
	return true
}

//export retro_unserialize
func retro_unserialize(data unsafe.Pointer) C.bool {
	if sys == nil || data == nil {
		return false
	}
	size := sys.StateSize()
	if size == 0 {
		return false
	}
	buf := unsafe.Slice((*byte)(data), size)
	cp := make([]byte, size)
	copy(cp, buf)
	return sys.Deserialize(cp) == nil
}

// The memory interface exposes cartridge RAM as SAVE_RAM and the
// console work RAM as SYSTEM_RAM, the standard libretro memory
// map ids.

//export retro_get_memory_data
func retro_get_memory_data(id C.unsigned) unsafe.Pointer {
	if sys == nil {
		return nil
	}
	var ram []uint8
	switch id {
	case memSaveRAM:
		ram = sys.SaveRAM()
	case memSystemRAM:
		ram = sys.SystemRAM()
	}
	if len(ram) == 0 {
		return nil
	}
	return unsafe.Pointer(&ram[0])
}

//export retro_get_memory_size
func retro_get_memory_size(id C.unsigned) C.size_t {
	if sys == nil {
		return 0
	}
	switch id {
	case memSaveRAM:
		return C.size_t(len(sys.SaveRAM()))
	case memSystemRAM:
		return C.size_t(len(sys.SystemRAM()))
	}
	return 0
}

// Cheats are applied by the iTaK engine through Game Genie decoding
// for cores that support them; this core does not expose cheat
// hooks yet.

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.unsigned, enabled C.bool, code *C.char) {}
