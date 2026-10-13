// Command libretro builds the Go DS core as a libretro
// shared library (go build -buildmode=c-shared). It is a
// thin C ABI shell over the system package: the frontend
// (the iTaK engine) registers its callbacks, hands us a
// ROM, and calls retro_run once per frame.
//
// Presentation: the DS has two 256x192 screens. The shell
// presents BOTH in one 256x384 frame, stacked vertically:
// rows 0-191 are the system's top screen and rows 192-383
// the bottom screen, in the order Machine.Framebuffer
// returns them (which already honors the POWCNT1 display
// swap). The touchscreen is the libretro pointer device:
// a press in the bottom half maps into bottom-screen pixel
// coordinates and feeds Machine.SetTouch; a press in the
// top half (the non-touch screen) lifts the pen. The
// microphone is not wired to any frontend source in this
// shell: Machine.SetMicrophone stays available to hosts
// that drive the core in-process.
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

static void gods_set_environ(retro_environment_t cb) { environ_cb = cb; }
static void gods_set_video(retro_video_refresh_t cb) { video_cb = cb; }
static void gods_set_audio(retro_audio_sample_t cb) { audio_cb = cb; }
static void gods_set_audio_batch(retro_audio_sample_batch_t cb) { audio_batch_cb = cb; }
static void gods_set_input_poll(retro_input_poll_t cb) { input_poll_cb = cb; }
static void gods_set_input_state(retro_input_state_t cb) { input_state_cb = cb; }

static bool gods_environ(unsigned cmd, void *data) { return environ_cb ? environ_cb(cmd, data) : false; }
static void gods_video(const void *data, unsigned w, unsigned h, size_t pitch) { if (video_cb) video_cb(data, w, h, pitch); }
static size_t gods_audio_batch(const int16_t *data, size_t frames) { return audio_batch_cb ? audio_batch_cb(data, frames) : 0; }
static void gods_audio_sample(int16_t l, int16_t r) { if (audio_cb) audio_cb(l, r); }
static void gods_input_poll(void) { if (input_poll_cb) input_poll_cb(); }
static int16_t gods_input_state(unsigned port, unsigned dev, unsigned idx, unsigned id) { return input_state_cb ? input_state_cb(port, dev, idx, id) : 0; }

static const char *gods_get_name() { return "iTaK GoDS"; }
static const char *gods_get_version() { return "1.0"; }
static const char *gods_get_exts() { return "nds"; }

struct gods_system_info { const char *library_name; const char *library_version; const char *valid_extensions; bool need_fullpath; bool block_extract; };
struct gods_game_geometry { unsigned base_width; unsigned base_height; unsigned max_width; unsigned max_height; float aspect_ratio; };
struct gods_system_timing { double fps; double sample_rate; };
struct gods_av_info { struct gods_game_geometry geometry; struct gods_system_timing timing; };
struct gods_game_info { const char *path; const void *data; size_t size; const char *meta; };
*/
import "C"

import (
	"os"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-nds/system"
)

// libretro constants used here (from the public libretro API).
const (
	envSetPixelFormat  = 10
	pixelFormatXRGB888 = 1
	deviceJoypad       = 1
	devicePointer      = 6
	pointerX           = 0
	pointerY           = 1
	pointerPressed     = 2
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
	// The frame rate follows from the machine's own
	// constants: the ARM9 master clock is 33,513,982 Hz
	// (twice GBATEK's nominal 16,756,991 Hz bus figure,
	// the same constant the DS-3 audio model uses) and
	// one frame is 560,190 ARM9 cycles (263 lines of
	// 2,130), giving 59.8261 frames per second.
	dsFPS        = 33513982.0 / 560190.0
	dsAspect     = 256.0 / 384.0 // the stacked two-screen frame
	dsSampleRate = 32768         // the sound unit's output rate
	frameWidth   = 256
	frameHeight  = 384 // two 256x192 screens stacked
	memSaveRAM   = 0
	memSystemRAM = 1
)

// DS button bits in Machine.SetButtons order (KEYINPUT
// order, then X and Y).
const (
	btnA      = 1 << 0
	btnB      = 1 << 1
	btnSelect = 1 << 2
	btnStart  = 1 << 3
	btnRight  = 1 << 4
	btnLeft   = 1 << 5
	btnUp     = 1 << 6
	btnDown   = 1 << 7
	btnR      = 1 << 8
	btnL      = 1 << 9
	btnX      = 1 << 10
	btnY      = 1 << 11
)

var (
	sys *system.Machine
	rom []byte
	// frame is the stacked presentation buffer, rebuilt
	// every retro_run from the two screen buffers.
	frame = make([]uint32, frameWidth*frameHeight)
)

func main() {}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_init
func retro_init() {}

//export retro_deinit
func retro_deinit() { sys = nil }

//export retro_set_environment
func retro_set_environment(cb C.retro_environment_t) { C.gods_set_environ(cb) }

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.retro_video_refresh_t) { C.gods_set_video(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.retro_audio_sample_t) { C.gods_set_audio(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.retro_audio_sample_batch_t) { C.gods_set_audio_batch(cb) }

//export retro_set_input_poll
func retro_set_input_poll(cb C.retro_input_poll_t) { C.gods_set_input_poll(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.retro_input_state_t) { C.gods_set_input_state(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port C.unsigned, device C.unsigned) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_gods_system_info) {
	info.library_name = C.gods_get_name()
	info.library_version = C.gods_get_version()
	info.valid_extensions = C.gods_get_exts()
	info.need_fullpath = false
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_gods_av_info) {
	info.geometry.base_width = frameWidth
	info.geometry.base_height = frameHeight
	info.geometry.max_width = frameWidth
	info.geometry.max_height = frameHeight
	info.geometry.aspect_ratio = dsAspect
	info.timing.fps = dsFPS
	info.timing.sample_rate = dsSampleRate
}

//export retro_get_region
func retro_get_region() C.unsigned { return regionNTSC }

//export retro_load_game
func retro_load_game(info *C.struct_gods_game_info) C.bool {
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
	m := system.New()
	if err := m.LoadROM(data); err != nil {
		return false
	}
	sys = m
	rom = data
	sys.Start()
	// Ask the frontend for XRGB8888 frames (what the
	// video engines produce).
	fmt888 := C.uint32_t(pixelFormatXRGB888)
	C.gods_environ(envSetPixelFormat, unsafe.Pointer(&fmt888))
	return true
}

//export retro_unload_game
func retro_unload_game() { sys = nil }

//export retro_reset
func retro_reset() {
	if sys == nil || rom == nil {
		return
	}
	// A reset re-runs the direct boot from the loaded
	// image: LoadROM stages the binaries and installs
	// the entry state again.
	m := system.New()
	if err := m.LoadROM(rom); err != nil {
		return
	}
	sys = m
	sys.Start()
}

func pressed(port int, id uint) bool {
	return C.gods_input_state(C.unsigned(port), deviceJoypad, 0, C.unsigned(id)) != 0
}

// pollPad translates libretro joypad state into the DS
// button mask (Machine.SetButtons bit order).
func pollPad(port int) uint32 {
	var m uint32
	if pressed(port, joypadA) {
		m |= btnA
	}
	if pressed(port, joypadB) {
		m |= btnB
	}
	if pressed(port, joypadSelect) {
		m |= btnSelect
	}
	if pressed(port, joypadStart) {
		m |= btnStart
	}
	if pressed(port, joypadRight) {
		m |= btnRight
	}
	if pressed(port, joypadLeft) {
		m |= btnLeft
	}
	if pressed(port, joypadUp) {
		m |= btnUp
	}
	if pressed(port, joypadDown) {
		m |= btnDown
	}
	if pressed(port, joypadR) {
		m |= btnR
	}
	if pressed(port, joypadL) {
		m |= btnL
	}
	if pressed(port, joypadX) {
		m |= btnX
	}
	if pressed(port, joypadY) {
		m |= btnY
	}
	return m
}

// pollTouch translates the libretro pointer into the DS
// touchscreen state. Pointer coordinates span the whole
// presented frame (-0x8000..0x7FFF over 256x384); only
// the bottom half is the touch screen. A press in the
// top half, or no press at all, lifts the pen.
func pollTouch(port int) {
	st := func(id uint) int {
		return int(C.gods_input_state(C.unsigned(port), devicePointer, 0, C.unsigned(id)))
	}
	if st(pointerPressed) == 0 {
		sys.SetTouch(0, 0, false)
		return
	}
	px := (st(pointerX) + 0x8000) * frameWidth / 0x10000
	py := (st(pointerY) + 0x8000) * frameHeight / 0x10000
	if px < 0 {
		px = 0
	}
	if px > frameWidth-1 {
		px = frameWidth - 1
	}
	if py < system.ScreenHeight {
		sys.SetTouch(0, 0, false)
		return
	}
	py -= system.ScreenHeight
	if py > system.ScreenHeight-1 {
		py = system.ScreenHeight - 1
	}
	sys.SetTouch(uint16(px), uint16(py), true)
}

//export retro_run
func retro_run() {
	if sys == nil {
		return
	}
	C.gods_input_poll()
	sys.SetButtons(pollPad(0))
	pollTouch(0)
	sys.RunFrame()

	top, bottom := sys.Framebuffer()
	copy(frame[:frameWidth*system.ScreenHeight], top)
	copy(frame[frameWidth*system.ScreenHeight:], bottom)
	C.gods_video(unsafe.Pointer(&frame[0]),
		frameWidth, frameHeight, C.size_t(frameWidth*4))

	ab := sys.AudioSamples()
	if len(ab) >= 2 {
		frames := len(ab) / 2
		done := int(C.gods_audio_batch((*C.int16_t)(unsafe.Pointer(&ab[0])), C.size_t(frames)))
		for i := done; i < frames; i++ {
			C.gods_audio_sample(C.int16_t(ab[2*i]), C.int16_t(ab[2*i+1]))
		}
	}
}

//export retro_serialize_size
func retro_serialize_size() C.size_t {
	if sys == nil {
		return 0
	}
	return C.size_t(system.StateSize)
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
	buf := unsafe.Slice((*byte)(data), system.StateSize)
	cp := make([]byte, system.StateSize)
	copy(cp, buf)
	return C.bool(sys.Deserialize(cp))
}

// The memory interface exposes the cartridge save memory
// (the AUX SPI EEPROM backing) as SAVE_RAM and main RAM
// as SYSTEM_RAM, the standard libretro memory map ids.

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
		ram = sys.Bus.MainRAM
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
		return C.size_t(len(sys.Bus.MainRAM))
	}
	return 0
}

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.unsigned, enabled C.bool, code *C.char) {}
