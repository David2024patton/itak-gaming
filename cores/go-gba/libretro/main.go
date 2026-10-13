// Command libretro builds the Go GBA core as a libretro shared
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

static void gba_set_environ(retro_environment_t cb) { environ_cb = cb; }
static void gba_set_video(retro_video_refresh_t cb) { video_cb = cb; }
static void gba_set_audio(retro_audio_sample_t cb) { audio_cb = cb; }
static void gba_set_audio_batch(retro_audio_sample_batch_t cb) { audio_batch_cb = cb; }
static void gba_set_input_poll(retro_input_poll_t cb) { input_poll_cb = cb; }
static void gba_set_input_state(retro_input_state_t cb) { input_state_cb = cb; }

static bool gba_environ(unsigned cmd, void *data) { return environ_cb ? environ_cb(cmd, data) : false; }
static void gba_video(const void *data, unsigned w, unsigned h, size_t pitch) { if (video_cb) video_cb(data, w, h, pitch); }
static size_t gba_audio_batch(const int16_t *data, size_t frames) { return audio_batch_cb ? audio_batch_cb(data, frames) : 0; }
static void gba_audio_sample(int16_t l, int16_t r) { if (audio_cb) audio_cb(l, r); }
static void gba_input_poll(void) { if (input_poll_cb) input_poll_cb(); }
static int16_t gba_input_state(unsigned port, unsigned dev, unsigned idx, unsigned id) { return input_state_cb ? input_state_cb(port, dev, idx, id) : 0; }

static const char *gba_get_name() { return "iTaK GoGBA"; }
static const char *gba_get_version() { return "0.1"; }
static const char *gba_get_exts() { return "gba"; }

struct gba_system_info { const char *library_name; const char *library_version; const char *valid_extensions; bool need_fullpath; bool block_extract; };
struct gba_game_geometry { unsigned base_width; unsigned base_height; unsigned max_width; unsigned max_height; float aspect_ratio; };
struct gba_system_timing { double fps; double sample_rate; };
struct gba_av_info { struct gba_game_geometry geometry; struct gba_system_timing timing; };
struct gba_game_info { const char *path; const void *data; size_t size; const char *meta; };
*/
import "C"

import (
	"bytes"
	"hash/fnv"
	"os"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-gba/system"
)

// libretro constants used here (from the public libretro API).
const (
	envSetPixelFormat  = 10
	pixelFormatXRGB888 = 1
	deviceJoypad       = 1
	joypadB            = 0
	joypadSelect       = 2
	joypadStart        = 3
	joypadUp           = 4
	joypadDown         = 5
	joypadLeft         = 6
	joypadRight        = 7
	joypadA            = 8
	joypadL            = 10
	joypadR            = 11
	regionNTSC         = 0
	gbaFPS             = 16777216.0 / 280896.0 // 59.7275
	gbaAspect          = 1.5                   // 240x160, square pixels
	memSaveRAM         = 0
	defaultAudioRate   = 32768 // SOUNDBIAS power-on rate
)

var sys *system.Machine

// declaredRate is the audio sample rate captured from the
// machine at load time and reported in the AV info. A game can
// change the machine's live rate mid-run by writing SOUNDBIAS;
// the frontend keeps consuming at the declared rate, so the
// shell resamples to it (see drainAudio).
var declaredRate = defaultAudioRate

// Save RAM plumbing. The machine hands out copies of the save
// contents, but the libretro memory interface needs a stable
// buffer the frontend can both read and write. The shell keeps
// one canonical copy: game-side changes are pulled into it
// after every frame, and frontend edits are detected by hash
// and pushed back before the next frame runs.
var (
	saveRAMCache []byte
	saveRAMHash  uint64
)

// resampler state for the SOUNDBIAS rate conversion: leftover
// input samples and the fractional read position within them.
var (
	resBuf   []int16
	resPhase float64
)

func main() {}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_init
func retro_init() { sys = system.New() }

//export retro_deinit
func retro_deinit() { sys = nil }

//export retro_set_environment
func retro_set_environment(cb C.retro_environment_t) { C.gba_set_environ(cb) }

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.retro_video_refresh_t) { C.gba_set_video(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.retro_audio_sample_t) { C.gba_set_audio(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.retro_audio_sample_batch_t) { C.gba_set_audio_batch(cb) }

//export retro_set_input_poll
func retro_set_input_poll(cb C.retro_input_poll_t) { C.gba_set_input_poll(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.retro_input_state_t) { C.gba_set_input_state(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port C.unsigned, device C.unsigned) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_gba_system_info) {
	info.library_name = C.gba_get_name()
	info.library_version = C.gba_get_version()
	info.valid_extensions = C.gba_get_exts()
	info.need_fullpath = false
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_gba_av_info) {
	info.geometry.base_width = system.Width
	info.geometry.base_height = system.Height
	info.geometry.max_width = system.Width
	info.geometry.max_height = system.Height
	info.geometry.aspect_ratio = gbaAspect
	info.timing.fps = gbaFPS
	rate := declaredRate
	if rate == 0 {
		rate = defaultAudioRate
	}
	info.timing.sample_rate = C.double(rate)
}

//export retro_get_region
func retro_get_region() C.unsigned { return regionNTSC }

//export retro_load_game
func retro_load_game(info *C.struct_gba_game_info) C.bool {
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
	if sys == nil {
		sys = system.New()
	}
	if err := sys.LoadCart(data); err != nil {
		return false
	}
	declaredRate = sys.AudioSampleRate()
	resBuf = resBuf[:0]
	resPhase = 0
	saveRAMCache = sys.SaveRAM()
	saveRAMHash = hashBytes(saveRAMCache)
	// Ask the frontend for XRGB8888 frames (what the GPU produces).
	fmt888 := C.uint32_t(pixelFormatXRGB888)
	C.gba_environ(envSetPixelFormat, unsafe.Pointer(&fmt888))
	return true
}

//export retro_unload_game
func retro_unload_game() {
	sys = system.New()
	saveRAMCache = nil
	declaredRate = defaultAudioRate
}

//export retro_reset
func retro_reset() {
	if sys != nil {
		// The machine keeps its save device contents across
		// a reset (battery backed); refresh the cache so the
		// two stay in step.
		pushSaveRAMEdits()
		sys.Reset()
		pullSaveRAM()
	}
}

func pressed(port int, id uint) bool {
	return C.gba_input_state(C.unsigned(port), deviceJoypad, 0, C.unsigned(id)) != 0
}

// pollPad translates libretro joypad state into the GBA button
// mask (GBATEK KEYINPUT order, via the system Button bits).
func pollPad(port int) uint32 {
	var m uint32
	if pressed(port, joypadA) {
		m |= system.ButtonA
	}
	if pressed(port, joypadB) {
		m |= system.ButtonB
	}
	if pressed(port, joypadSelect) {
		m |= system.ButtonSelect
	}
	if pressed(port, joypadStart) {
		m |= system.ButtonStart
	}
	if pressed(port, joypadRight) {
		m |= system.ButtonRight
	}
	if pressed(port, joypadLeft) {
		m |= system.ButtonLeft
	}
	if pressed(port, joypadUp) {
		m |= system.ButtonUp
	}
	if pressed(port, joypadDown) {
		m |= system.ButtonDown
	}
	if pressed(port, joypadR) {
		m |= system.ButtonR
	}
	if pressed(port, joypadL) {
		m |= system.ButtonL
	}
	return m
}

func hashBytes(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

// pushSaveRAMEdits detects frontend edits to the exposed save
// RAM buffer and writes them into the machine.
func pushSaveRAMEdits() {
	if sys == nil || saveRAMCache == nil {
		return
	}
	if hashBytes(saveRAMCache) != saveRAMHash {
		sys.SetSaveRAM(saveRAMCache)
		pullSaveRAM()
	}
}

// pullSaveRAM refreshes the exposed buffer from the machine
// after game-side changes (and after a push, so size changes
// from EEPROM size learning show up too).
func pullSaveRAM() {
	if sys == nil {
		return
	}
	cur := sys.SaveRAM()
	if !bytes.Equal(cur, saveRAMCache) {
		saveRAMCache = cur
	}
	saveRAMHash = hashBytes(saveRAMCache)
}

// drainAudio returns this frame's audio at the declared rate.
// When the machine's live rate differs (a game rewrote
// SOUNDBIAS after load), the input is converted with a small
// linear interpolator: a fractional read position advances by
// liveRate/declaredRate per output frame over a carried
// buffer, so the conversion is continuous across frames.
// Linear interpolation is a deliberate simplicity choice for a
// rate change that in practice happens once at a game's audio
// init, before any audible output.
func drainAudio() []int16 {
	in := sys.AudioBuffer()
	live := sys.AudioSampleRate()
	if live == declaredRate || live == 0 || declaredRate == 0 {
		resBuf = resBuf[:0]
		resPhase = 0
		return in
	}
	resBuf = append(resBuf, in...)
	total := len(resBuf) / 2
	step := float64(live) / float64(declaredRate)
	var out []int16
	for resPhase+1 < float64(total) {
		i := int(resPhase)
		frac := resPhase - float64(i)
		l := lerp(resBuf[2*i], resBuf[2*i+2], frac)
		r := lerp(resBuf[2*i+1], resBuf[2*i+3], frac)
		out = append(out, l, r)
		resPhase += step
	}
	consumed := int(resPhase)
	if consumed > 0 {
		resBuf = append(resBuf[:0], resBuf[2*consumed:]...)
		resPhase -= float64(consumed)
	}
	return out
}

func lerp(a, b int16, frac float64) int16 {
	return int16(float64(a) + (float64(b)-float64(a))*frac)
}

//export retro_run
func retro_run() {
	if sys == nil || sys.Cart() == nil {
		return
	}
	pushSaveRAMEdits()
	C.gba_input_poll()
	sys.SetButtons(pollPad(0))
	sys.RunFrame()
	pullSaveRAM()

	fb := sys.Framebuffer()
	if len(fb) > 0 {
		C.gba_video(unsafe.Pointer(&fb[0]),
			C.unsigned(system.Width), C.unsigned(system.Height),
			C.size_t(system.Width*4))
	}
	ab := drainAudio()
	if len(ab) >= 2 {
		frames := len(ab) / 2
		done := int(C.gba_audio_batch((*C.int16_t)(unsafe.Pointer(&ab[0])), C.size_t(frames)))
		for i := done; i < frames; i++ {
			C.gba_audio_sample(C.int16_t(ab[2*i]), C.int16_t(ab[2*i+1]))
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
	if err := sys.Deserialize(cp); err != nil {
		return false
	}
	pullSaveRAM()
	return true
}

// The memory interface exposes the cartridge save contents as
// RETRO_MEMORY_SAVE_RAM through the shell-owned cache described
// above. Work RAM is not offered: the GBA's two work RAMs are
// not one contiguous region.

//export retro_get_memory_data
func retro_get_memory_data(id C.unsigned) unsafe.Pointer {
	if sys == nil || id != memSaveRAM || len(saveRAMCache) == 0 {
		return nil
	}
	return unsafe.Pointer(&saveRAMCache[0])
}

//export retro_get_memory_size
func retro_get_memory_size(id C.unsigned) C.size_t {
	if sys == nil || id != memSaveRAM {
		return 0
	}
	return C.size_t(len(saveRAMCache))
}

// Cheats are applied by the iTaK engine through Game Genie decoding
// for cores that support them; this core does not expose cheat
// hooks yet.

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.unsigned, enabled C.bool, code *C.char) {}
