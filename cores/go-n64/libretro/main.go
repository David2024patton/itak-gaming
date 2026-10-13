// Command libretro builds the Go N64 core as a libretro
// shared library (go build -buildmode=c-shared). It is
// a thin C ABI shell over the system package: the
// frontend (the iTaK engine) registers its callbacks,
// hands us a ROM, and calls retro_run once per frame.
//
// Core options (set through RETRO_ENVIRONMENT_SET_VARIABLES
// and read back at load):
//
//	gon64_save_type:      None | EEPROM 4K | EEPROM 16K | SRAM | Flash
//	gon64_expansion_pak:  On | Off
//
// The save type needs an option because N64 cartridges
// do not declare their save device in the ROM header
// and this core keeps no game database.
package main

/*
#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdlib.h>

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

static void gon64_set_environ(retro_environment_t cb) { environ_cb = cb; }
static void gon64_set_video(retro_video_refresh_t cb) { video_cb = cb; }
static void gon64_set_audio(retro_audio_sample_t cb) { audio_cb = cb; }
static void gon64_set_audio_batch(retro_audio_sample_batch_t cb) { audio_batch_cb = cb; }
static void gon64_set_input_poll(retro_input_poll_t cb) { input_poll_cb = cb; }
static void gon64_set_input_state(retro_input_state_t cb) { input_state_cb = cb; }

static bool gon64_environ(unsigned cmd, void *data) { return environ_cb ? environ_cb(cmd, data) : false; }
static void gon64_video(const void *data, unsigned w, unsigned h, size_t pitch) { if (video_cb) video_cb(data, w, h, pitch); }
static size_t gon64_audio_batch(const int16_t *data, size_t frames) { return audio_batch_cb ? audio_batch_cb(data, frames) : 0; }
static void gon64_audio_sample(int16_t l, int16_t r) { if (audio_cb) audio_cb(l, r); }
static void gon64_input_poll(void) { if (input_poll_cb) input_poll_cb(); }
static int16_t gon64_input_state(unsigned port, unsigned dev, unsigned idx, unsigned id) { return input_state_cb ? input_state_cb(port, dev, idx, id) : 0; }

static const char *gon64_get_name() { return "iTaK GoN64"; }
static const char *gon64_get_version() { return "1.0"; }
static const char *gon64_get_exts() { return "z64|n64|v64"; }

struct gon64_system_info { const char *library_name; const char *library_version; const char *valid_extensions; bool need_fullpath; bool block_extract; };
struct gon64_game_geometry { unsigned base_width; unsigned base_height; unsigned max_width; unsigned max_height; float aspect_ratio; };
struct gon64_system_timing { double fps; double sample_rate; };
struct gon64_av_info { struct gon64_game_geometry geometry; struct gon64_system_timing timing; };
struct gon64_game_info { const char *path; const void *data; size_t size; const char *meta; };
struct gon64_variable { const char *key; const char *value; };

static struct gon64_variable gon64_variables[] = {
	{ "gon64_save_type", "Save type; None|EEPROM 4K|EEPROM 16K|SRAM|Flash" },
	{ "gon64_expansion_pak", "Expansion Pak; On|Off" },
	{ NULL, NULL },
};

static void gon64_offer_variables(void) { gon64_environ(16, (void*)gon64_variables); }

static const char *gon64_variable_value(const char *key) {
	struct gon64_variable v;
	v.key = key;
	v.value = NULL;
	if (!gon64_environ(15, (void*)&v)) return NULL;
	return v.value;
}
*/
import "C"

import (
	"os"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/system"
)

// libretro constants used here (from the public libretro API).
const (
	envSetPixelFormat  = 10
	pixelFormatXRGB888 = 1
	deviceJoypad       = 1
	deviceAnalog       = 2
	analogLeft         = 0
	analogRight        = 1
	analogX            = 0
	analogY            = 1
	joypadB            = 0
	joypadY            = 1
	joypadStart        = 3
	joypadUp           = 4
	joypadDown         = 5
	joypadLeft         = 6
	joypadRight        = 7
	joypadA            = 8
	joypadX            = 9
	joypadL            = 10
	joypadR            = 11
	joypadL2           = 12
	regionNTSC         = 0
	n64FPS             = 60.0    // NTSC field rate, simplified to 60 for AV info
	n64Aspect          = 1.33333 // 4:3
	n64SampleRate      = 44100   // declared rate; the AI stream is resampled to it
	memSaveRAM         = 0
	memSystemRAM       = 1
	cThreshold         = 16000 // right-stick deflection that presses a C button
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
func retro_set_environment(cb C.retro_environment_t) {
	C.gon64_set_environ(cb)
	C.gon64_offer_variables()
}

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.retro_video_refresh_t) { C.gon64_set_video(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.retro_audio_sample_t) { C.gon64_set_audio(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.retro_audio_sample_batch_t) { C.gon64_set_audio_batch(cb) }

//export retro_set_input_poll
func retro_set_input_poll(cb C.retro_input_poll_t) { C.gon64_set_input_poll(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.retro_input_state_t) { C.gon64_set_input_state(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port C.unsigned, device C.unsigned) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_gon64_system_info) {
	info.library_name = C.gon64_get_name()
	info.library_version = C.gon64_get_version()
	info.valid_extensions = C.gon64_get_exts()
	info.need_fullpath = false
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_gon64_av_info) {
	w, h := system.Width, system.Height
	if sys != nil {
		w, h = sys.FramebufferSize()
	}
	info.geometry.base_width = C.unsigned(w)
	info.geometry.base_height = C.unsigned(h)
	info.geometry.max_width = 640
	info.geometry.max_height = 480
	info.geometry.aspect_ratio = n64Aspect
	info.timing.fps = n64FPS
	info.timing.sample_rate = n64SampleRate
}

//export retro_get_region
func retro_get_region() C.unsigned { return regionNTSC }

func variable(key string) string {
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	v := C.gon64_variable_value(ck)
	if v == nil {
		return ""
	}
	return C.GoString(v)
}

//export retro_load_game
func retro_load_game(info *C.struct_gon64_game_info) C.bool {
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
	expansion := variable("gon64_expansion_pak") != "Off"
	s, err := system.NewWithOptions(data, expansion)
	if err != nil {
		return false
	}
	switch variable("gon64_save_type") {
	case "EEPROM 4K":
		s.SetSaveType(cart.SaveEEPROM4K)
	case "EEPROM 16K":
		s.SetSaveType(cart.SaveEEPROM16K)
	case "SRAM":
		s.SetSaveType(cart.SaveSRAM)
	case "Flash":
		s.SetSaveType(cart.SaveFlash)
	}
	sys = s
	rsReset()
	sys.Start()
	fmt888 := C.uint32_t(pixelFormatXRGB888)
	C.gon64_environ(envSetPixelFormat, unsafe.Pointer(&fmt888))
	return true
}

//export retro_unload_game
func retro_unload_game() { sys = nil }

//export retro_reset
func retro_reset() {
	if sys != nil {
		sys.Start() // a reset reboots through the HLE boot
	}
}

func pressed(port int, id uint) bool {
	return C.gon64_input_state(C.unsigned(port), deviceJoypad, 0, C.unsigned(id)) != 0
}

func axis(port int, index, id uint) int16 {
	return int16(C.gon64_input_state(C.unsigned(port), deviceAnalog, C.unsigned(index), C.unsigned(id)))
}

// stickAxis scales a libretro analog axis (-32768..32767)
// to the N64 stick range (-80..80).
func stickAxis(v int16) int8 {
	n := int(v) * 80 / 32768
	if n > 80 {
		n = 80
	}
	if n < -80 {
		n = -80
	}
	return int8(n)
}

// pollPad translates one port's libretro state into the
// N64 controller state. The mapping (documented in the
// README): A and B are the face buttons, Z is on L2,
// the C buttons sit on the right analog stick past a
// threshold plus X for C-up and Y for C-left.
func pollPad(port int) {
	var m uint16
	if pressed(port, joypadA) {
		m |= system.ButtonA
	}
	if pressed(port, joypadB) {
		m |= system.ButtonB
	}
	if pressed(port, joypadStart) {
		m |= system.ButtonStart
	}
	if pressed(port, joypadUp) {
		m |= system.ButtonDUp
	}
	if pressed(port, joypadDown) {
		m |= system.ButtonDDown
	}
	if pressed(port, joypadLeft) {
		m |= system.ButtonDLeft
	}
	if pressed(port, joypadRight) {
		m |= system.ButtonDRight
	}
	if pressed(port, joypadL) {
		m |= system.ButtonL
	}
	if pressed(port, joypadR) {
		m |= system.ButtonR
	}
	if pressed(port, joypadL2) {
		m |= system.ButtonZ
	}
	if pressed(port, joypadX) {
		m |= system.ButtonCUp
	}
	if pressed(port, joypadY) {
		m |= system.ButtonCLeft
	}
	rx := axis(port, analogRight, analogX)
	ry := axis(port, analogRight, analogY)
	if rx < -cThreshold {
		m |= system.ButtonCLeft
	}
	if rx > cThreshold {
		m |= system.ButtonCRight
	}
	if ry < -cThreshold {
		m |= system.ButtonCUp
	}
	if ry > cThreshold {
		m |= system.ButtonCDown
	}
	sys.SetButtonsPort(port, m)
	sys.SetStick(port,
		stickAxis(axis(port, analogLeft, analogX)),
		stickAxis(axis(port, analogLeft, analogY)))
}

// The audio resampler: the shell declares 44,100 Hz and
// converts the AI's stream (at its programmed DAC rate)
// with linear interpolation, carrying the fractional
// phase and the last source frame across calls.
var (
	rsPrev  [2]int16
	rsPhase float64
)

func rsReset() {
	rsPrev = [2]int16{}
	rsPhase = 0
}

func resample(src []int16, rate int) []int16 {
	if len(src) < 2 || rate <= 0 || rate == n64SampleRate {
		return src
	}
	srcFrames := len(src) / 2
	ext := make([]int16, 0, len(src)+2)
	ext = append(ext, rsPrev[0], rsPrev[1])
	ext = append(ext, src...)
	total := srcFrames + 1 // ext frames: prev + src
	step := float64(rate) / n64SampleRate
	out := make([]int16, 0, srcFrames*2)
	pos := rsPhase
	for pos < float64(total-1) {
		i := int(pos)
		frac := pos - float64(i)
		for ch := 0; ch < 2; ch++ {
			a := float64(ext[2*i+ch])
			b := float64(ext[2*i+2+ch])
			out = append(out, int16(a+(b-a)*frac))
		}
		pos += step
	}
	rsPhase = pos - float64(total-1)
	rsPrev = [2]int16{src[len(src)-2], src[len(src)-1]}
	return out
}

//export retro_run
func retro_run() {
	if sys == nil {
		return
	}
	C.gon64_input_poll()
	for port := 0; port < 4; port++ {
		pollPad(port)
	}
	sys.RunFrame()

	fb := sys.Framebuffer()
	w, h := sys.FramebufferSize()
	if len(fb) > 0 {
		C.gon64_video(unsafe.Pointer(&fb[0]),
			C.unsigned(w), C.unsigned(h), C.size_t(w*4))
	}
	ab := resample(sys.AudioBuffer(), sys.AudioRate())
	if len(ab) >= 2 {
		frames := len(ab) / 2
		done := int(C.gon64_audio_batch((*C.int16_t)(unsafe.Pointer(&ab[0])), C.size_t(frames)))
		for i := done; i < frames; i++ {
			C.gon64_audio_sample(C.int16_t(ab[2*i]), C.int16_t(ab[2*i+1]))
		}
	}
}

//export retro_serialize_size
func retro_serialize_size() C.size_t {
	if sys == nil {
		return 0
	}
	return C.size_t(sys.SerializeSize())
}

//export retro_serialize
func retro_serialize(data unsafe.Pointer) C.bool {
	if sys == nil || data == nil {
		return false
	}
	state := sys.Serialize()
	buf := unsafe.Slice((*byte)(data), len(state))
	copy(buf, state)
	return true
}

//export retro_unserialize
func retro_unserialize(data unsafe.Pointer) C.bool {
	if sys == nil || data == nil {
		return false
	}
	size := sys.SerializeSize()
	buf := unsafe.Slice((*byte)(data), size)
	cp := make([]byte, size)
	copy(cp, buf)
	return sys.Deserialize(cp) == nil
}

// The memory interface exposes the active save device
// as SAVE_RAM and RDRAM as SYSTEM_RAM.

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
		ram = sys.Mem.RDRAM
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
		return C.size_t(len(sys.Mem.RDRAM))
	}
	return 0
}

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.unsigned, enabled C.bool, code *C.char) {}
