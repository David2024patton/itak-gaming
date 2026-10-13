// Command libretro builds the Go Genesis core as a libretro shared
// library (go build -buildmode=c-shared). It is a thin C ABI shell over
// the system package: the frontend (the iTaK engine) registers its
// callbacks, hands us a ROM, and calls retro_run once per frame.
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

static void gg_set_environ(retro_environment_t cb) { environ_cb = cb; }
static void gg_set_video(retro_video_refresh_t cb) { video_cb = cb; }
static void gg_set_audio(retro_audio_sample_t cb) { audio_cb = cb; }
static void gg_set_audio_batch(retro_audio_sample_batch_t cb) { audio_batch_cb = cb; }
static void gg_set_input_poll(retro_input_poll_t cb) { input_poll_cb = cb; }
static void gg_set_input_state(retro_input_state_t cb) { input_state_cb = cb; }

static bool gg_environ(unsigned cmd, void *data) { return environ_cb ? environ_cb(cmd, data) : false; }
static void gg_video(const void *data, unsigned w, unsigned h, size_t pitch) { if (video_cb) video_cb(data, w, h, pitch); }
static size_t gg_audio_batch(const int16_t *data, size_t frames) { return audio_batch_cb ? audio_batch_cb(data, frames) : 0; }
static void gg_audio_sample(int16_t l, int16_t r) { if (audio_cb) audio_cb(l, r); }
static void gg_input_poll(void) { if (input_poll_cb) input_poll_cb(); }
static int16_t gg_input_state(unsigned port, unsigned dev, unsigned idx, unsigned id) { return input_state_cb ? input_state_cb(port, dev, idx, id) : 0; }

static const char *gg_get_name() { return "iTaK GoGenesis"; }
static const char *gg_get_version() { return "0.1"; }
static const char *gg_get_exts() { return "bin|gen|md|smd|cue|iso"; }

struct gg_system_info { const char *library_name; const char *library_version; const char *valid_extensions; bool need_fullpath; bool block_extract; };
struct gg_game_geometry { unsigned base_width; unsigned base_height; unsigned max_width; unsigned max_height; float aspect_ratio; };
struct gg_system_timing { double fps; double sample_rate; };
struct gg_av_info { struct gg_game_geometry geometry; struct gg_system_timing timing; };
struct gg_game_info { const char *path; const void *data; size_t size; const char *meta; };
*/
import "C"

import (
	"os"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/bus"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/system"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/vdp"
)

// libretro constants used here (from the public libretro API).
const (
	envSetPixelFormat = 10
	pixelFormatRGB565 = 2
	deviceJoypad      = 1
	joypadB           = 0
	joypadY           = 1
	joypadSelect      = 2
	joypadStart       = 3
	joypadUp          = 4
	joypadDown        = 5
	joypadLeft        = 6
	joypadRight       = 7
	joypadA           = 8
	regionNTSC        = 0
	genesisFPS        = 59.9227
	genesisAspectH40  = 1.3061 // pixel-aspect corrected, matches Genesis Plus GX
	genesisAspectH32  = 1.2152
	sampleRate        = 44100
)

var sys *system.System

// ranFrames distinguishes "loaded but not started yet" from a running
// game for geometry reporting: the VDP powers on in H32 mode, but most
// Genesis games switch to H40 during boot, so before the first frame the
// honest default geometry is 320 wide.
var ranFrames bool

// discTempPath is the temp file written when a frontend hands us disc
// bytes with no path (the routeDiscData route). retro_unload_game
// deletes it.
var discTempPath string

func main() {}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_init
func retro_init() { sys = system.New() }

//export retro_deinit
func retro_deinit() { sys = nil }

//export retro_set_environment
func retro_set_environment(cb C.retro_environment_t) { C.gg_set_environ(cb) }

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.retro_video_refresh_t) { C.gg_set_video(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.retro_audio_sample_t) { C.gg_set_audio(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.retro_audio_sample_batch_t) { C.gg_set_audio_batch(cb) }

//export retro_set_input_poll
func retro_set_input_poll(cb C.retro_input_poll_t) { C.gg_set_input_poll(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.retro_input_state_t) { C.gg_set_input_state(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port C.unsigned, device C.unsigned) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_gg_system_info) {
	info.library_name = C.gg_get_name()
	info.library_version = C.gg_get_version()
	info.valid_extensions = C.gg_get_exts()
	info.need_fullpath = false
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_gg_av_info) {
	w, h := 320, 224
	aspect := genesisAspectH40
	if sys != nil && sys.Loaded() && ranFrames {
		w, h = sys.Width(), sys.Height()
		if w == 256 {
			aspect = genesisAspectH32
		}
	}
	info.geometry.base_width = C.unsigned(w)
	info.geometry.base_height = C.unsigned(h)
	info.geometry.max_width = vdp.MaxWidth
	info.geometry.max_height = vdp.MaxHeight
	info.geometry.aspect_ratio = C.float(aspect)
	info.timing.fps = genesisFPS
	info.timing.sample_rate = sampleRate
}

//export retro_get_region
func retro_get_region() C.unsigned { return regionNTSC }

//export retro_load_game
func retro_load_game(info *C.struct_gg_game_info) C.bool {
	var path string
	if info.path != nil {
		path = C.GoString(info.path)
	}
	var data []byte
	if info.data != nil && info.size > 0 {
		data = C.GoBytes(info.data, C.int(info.size))
	} else if info.path != nil {
		b, err := os.ReadFile(path)
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
	switch routeFor(path, data) {
	case routeDiscPath:
		if err := sys.LoadDisc(path); err != nil {
			return false
		}
	case routeDiscData:
		// LoadDisc works on files, so park the bytes in a temp ISO
		// first. retro_unload_game deletes it.
		f, err := os.CreateTemp("", "itak-disc-*.iso")
		if err != nil {
			return false
		}
		tmpPath := f.Name()
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return false
		}
		if err := f.Close(); err != nil {
			os.Remove(tmpPath)
			return false
		}
		if err := sys.LoadDisc(tmpPath); err != nil {
			os.Remove(tmpPath)
			return false
		}
		discTempPath = tmpPath
	default:
		if err := sys.LoadGame(data); err != nil {
			return false
		}
	}
	// Ask the frontend for RGB565 frames (what the VDP produces).
	fmt565 := C.uint32_t(pixelFormatRGB565)
	C.gg_environ(envSetPixelFormat, unsafe.Pointer(&fmt565))
	return true
}

//export retro_unload_game
func retro_unload_game() {
	if discTempPath != "" {
		os.Remove(discTempPath)
		discTempPath = ""
	}
	sys = system.New()
	ranFrames = false
}

//export retro_reset
func retro_reset() {
	if sys != nil {
		sys.Reset()
	}
}

func pressed(port int, id uint) bool {
	return C.gg_input_state(C.unsigned(port), deviceJoypad, 0, C.unsigned(id)) != 0
}

// pollPad translates libretro joypad state into the bus pad layout.
// 3-button Genesis pad mapping (same as Genesis Plus GX defaults):
// libretro Y = Genesis A, B = B, A = C, Select = Mode, Start = Start.
func pollPad(port int) uint16 {
	var m uint16
	if pressed(port, joypadUp) {
		m |= bus.PadUp
	}
	if pressed(port, joypadDown) {
		m |= bus.PadDown
	}
	if pressed(port, joypadLeft) {
		m |= bus.PadLeft
	}
	if pressed(port, joypadRight) {
		m |= bus.PadRight
	}
	if pressed(port, joypadY) {
		m |= bus.PadA
	}
	if pressed(port, joypadB) {
		m |= bus.PadB
	}
	if pressed(port, joypadA) {
		m |= bus.PadC
	}
	if pressed(port, joypadStart) {
		m |= bus.PadStart
	}
	if pressed(port, joypadSelect) {
		m |= bus.PadMode
	}
	return m
}

//export retro_run
func retro_run() {
	if sys == nil || !sys.Loaded() {
		return
	}
	ranFrames = true
	C.gg_input_poll()
	sys.SetButtons(0, pollPad(0))
	sys.SetButtons(1, pollPad(1))
	sys.RunFrame()

	fb := sys.Framebuffer()
	if len(fb) > 0 {
		C.gg_video(unsafe.Pointer(&fb[0]),
			C.unsigned(sys.Width()), C.unsigned(sys.Height()),
			C.size_t(vdp.MaxWidth*2))
	}
	ab := sys.AudioBuffer()
	if len(ab) >= 2 {
		frames := len(ab) / 2
		done := int(C.gg_audio_batch((*C.int16_t)(unsafe.Pointer(&ab[0])), C.size_t(frames)))
		for i := done; i < frames; i++ {
			C.gg_audio_sample(C.int16_t(ab[2*i]), C.int16_t(ab[2*i+1]))
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
	size := sys.SerializeSize()
	if size == 0 {
		return false
	}
	buf := unsafe.Slice((*byte)(data), size)
	return sys.Serialize(buf) == nil
}

//export retro_unserialize
func retro_unserialize(data unsafe.Pointer) C.bool {
	if sys == nil || data == nil {
		return false
	}
	size := sys.SerializeSize()
	if size == 0 {
		return false
	}
	buf := unsafe.Slice((*byte)(data), size)
	return sys.Deserialize(buf) == nil
}

// Memory access goes through serialize in the iTaK engine; the direct
// memory interface is not offered yet.

//export retro_get_memory_data
func retro_get_memory_data(id C.unsigned) unsafe.Pointer { return nil }

//export retro_get_memory_size
func retro_get_memory_size(id C.unsigned) C.size_t { return 0 }

// Cheats are applied by the iTaK engine through Game Genie decoding for
// cores that support them; this core does not expose cheat hooks yet.

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.unsigned, enabled C.bool, code *C.char) {}
