// Command libretro builds the Go GameCube core as a
// libretro shared library (go build -buildmode=c-shared).
// It is a thin C ABI shell over the system package: the
// frontend registers its callbacks, hands us a disc
// image, and calls retro_run once per frame. The machine
// boots the disc's DOL through the core's HLE boot at
// load time, exactly as the GCN-3 and GCN-4 gates do.
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

static void gcn_set_environ(retro_environment_t cb) { environ_cb = cb; }
static void gcn_set_video(retro_video_refresh_t cb) { video_cb = cb; }
static void gcn_set_audio(retro_audio_sample_t cb) { audio_cb = cb; }
static void gcn_set_audio_batch(retro_audio_sample_batch_t cb) { audio_batch_cb = cb; }
static void gcn_set_input_poll(retro_input_poll_t cb) { input_poll_cb = cb; }
static void gcn_set_input_state(retro_input_state_t cb) { input_state_cb = cb; }

static bool gcn_environ(unsigned cmd, void *data) { return environ_cb ? environ_cb(cmd, data) : false; }
static void gcn_video(const void *data, unsigned w, unsigned h, size_t pitch) { if (video_cb) video_cb(data, w, h, pitch); }
static size_t gcn_audio_batch(const int16_t *data, size_t frames) { return audio_batch_cb ? audio_batch_cb(data, frames) : 0; }
static void gcn_audio_sample(int16_t l, int16_t r) { if (audio_cb) audio_cb(l, r); }
static void gcn_input_poll(void) { if (input_poll_cb) input_poll_cb(); }
static int16_t gcn_input_state(unsigned port, unsigned dev, unsigned idx, unsigned id) { return input_state_cb ? input_state_cb(port, dev, idx, id) : 0; }

static const char *gcn_get_name() { return "iTaK GoGCN"; }
static const char *gcn_get_version() { return "0.1"; }
static const char *gcn_get_exts() { return "iso|gcm"; }

struct gcn_system_info { const char *library_name; const char *library_version; const char *valid_extensions; bool need_fullpath; bool block_extract; };
struct gcn_game_geometry { unsigned base_width; unsigned base_height; unsigned max_width; unsigned max_height; float aspect_ratio; };
struct gcn_system_timing { double fps; double sample_rate; };
struct gcn_av_info { struct gcn_game_geometry geometry; struct gcn_system_timing timing; };
struct gcn_game_info { const char *path; const void *data; size_t size; const char *meta; };
*/
import "C"

import (
	"os"
	"path/filepath"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/disc"
	"github.com/davidpatton/itak-gaming/cores/go-gamecube/system"
)

// libretro constants used here (from the public libretro API).
const (
	envSetPixelFormat   = 10
	envGetSaveDirectory = 31
	pixelFormatXRGB888  = 1
	deviceJoypad        = 1
	deviceAnalog        = 5
	analogIndexLeft     = 0
	analogIndexRight    = 1
	analogIndexButton   = 2
	analogAxisX         = 0
	analogAxisY         = 1
	joypadB             = 0
	joypadY             = 1
	joypadSelect        = 2
	joypadStart         = 3
	joypadUp            = 4
	joypadDown          = 5
	joypadLeft          = 6
	joypadRight         = 7
	joypadA             = 8
	joypadX             = 9
	joypadL             = 10
	joypadR             = 11
	joypadL2            = 12
	joypadR2            = 13
	regionNTSC          = 0
	gcnFPS              = 60.0 // the core frame: 8,100,000 cycles at 486 MHz
	gcnAspect           = 1.33333
	gcnSampleRate       = 48000 // declared rate; the AI rate is resampled to it
	memSaveRAM          = 0
	memSystemRAM        = 1
	cardAFileName       = "iTaK GoGCN.cardA.raw"
)

// sys is the running machine, gameImage the parsed disc
// kept for resets, cardPath the memory card A image file
// inside the frontend save directory (empty when the
// environment does not provide one).
var (
	sys       *system.Machine
	gameImage *disc.Image
	cardPath  string
	resamp    resampler
)

func main() {}

//export retro_api_version
func retro_api_version() C.uint { return 1 }

//export retro_init
func retro_init() {}

//export retro_deinit
func retro_deinit() {
	flushCard()
	sys = nil
}

//export retro_set_environment
func retro_set_environment(cb C.retro_environment_t) { C.gcn_set_environ(cb) }

//export retro_set_video_refresh
func retro_set_video_refresh(cb C.retro_video_refresh_t) { C.gcn_set_video(cb) }

//export retro_set_audio_sample
func retro_set_audio_sample(cb C.retro_audio_sample_t) { C.gcn_set_audio(cb) }

//export retro_set_audio_sample_batch
func retro_set_audio_sample_batch(cb C.retro_audio_sample_batch_t) { C.gcn_set_audio_batch(cb) }

//export retro_set_input_poll
func retro_set_input_poll(cb C.retro_input_poll_t) { C.gcn_set_input_poll(cb) }

//export retro_set_input_state
func retro_set_input_state(cb C.retro_input_state_t) { C.gcn_set_input_state(cb) }

//export retro_set_controller_port_device
func retro_set_controller_port_device(port C.unsigned, device C.unsigned) {}

//export retro_get_system_info
func retro_get_system_info(info *C.struct_gcn_system_info) {
	info.library_name = C.gcn_get_name()
	info.library_version = C.gcn_get_version()
	info.valid_extensions = C.gcn_get_exts()
	info.need_fullpath = false
	info.block_extract = false
}

//export retro_get_system_av_info
func retro_get_system_av_info(info *C.struct_gcn_av_info) {
	info.geometry.base_width = system.Width
	info.geometry.base_height = system.Height
	info.geometry.max_width = system.Width
	info.geometry.max_height = system.Height
	info.geometry.aspect_ratio = gcnAspect
	info.timing.fps = gcnFPS
	info.timing.sample_rate = gcnSampleRate
}

//export retro_get_region
func retro_get_region() C.unsigned { return regionNTSC }

// flushCard writes the card A image back to the save
// directory. Card images are persistent media outside
// the save state (the GCN-3 design), so the shell owns
// their file lifetime. Errors are ignored: a missing or
// unwritable save directory just loses persistence.
func flushCard() {
	if sys == nil || cardPath == "" {
		return
	}
	_ = os.WriteFile(cardPath, sys.CardImage(0), 0644)
}

// bootMachine builds a fresh machine on gameImage: the
// card A image is loaded from the save directory first
// (card media survives machine resets), then the disc
// loads and boots its DOL through the HLE boot.
func bootMachine() bool {
	m := system.New()
	if cardPath != "" {
		if data, err := os.ReadFile(cardPath); err == nil {
			copy(m.CardImage(0), data)
		}
	}
	if err := m.LoadDisc(gameImage); err != nil {
		return false
	}
	if err := m.BootDisc(); err != nil {
		return false
	}
	sys = m
	resamp.reset()
	fmt888 := C.uint32_t(pixelFormatXRGB888)
	C.gcn_environ(envSetPixelFormat, unsafe.Pointer(&fmt888))
	return true
}

//export retro_load_game
func retro_load_game(info *C.struct_gcn_game_info) C.bool {
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
	img, err := disc.Parse(data)
	if err != nil {
		return false
	}
	// Wii discs parse as metadata only; the GameCube
	// structures are not there to boot. Refuse cleanly.
	if img.IsWii {
		return false
	}
	gameImage = img

	// Memory card A lives in the frontend save directory
	// under the libretro convention when one is offered.
	cardPath = ""
	var dirPtr *C.char
	if C.gcn_environ(envGetSaveDirectory, unsafe.Pointer(&dirPtr)) && dirPtr != nil {
		if dir := C.GoString(dirPtr); dir != "" {
			cardPath = filepath.Join(dir, cardAFileName)
		}
	}
	return C.bool(bootMachine())
}

//export retro_unload_game
func retro_unload_game() {
	flushCard()
	sys = nil
	gameImage = nil
	cardPath = ""
}

//export retro_reset
func retro_reset() {
	if gameImage == nil {
		return
	}
	flushCard()
	bootMachine()
}

func pressed(port int, id uint) bool {
	return C.gcn_input_state(C.unsigned(port), deviceJoypad, 0, C.unsigned(id)) != 0
}

func axis(port int, index uint, id uint) int16 {
	return int16(C.gcn_input_state(C.unsigned(port), deviceAnalog, C.unsigned(index), C.unsigned(id)))
}

func analogButton(port int, id uint) int16 {
	return int16(C.gcn_input_state(C.unsigned(port), deviceAnalog, analogIndexButton, C.unsigned(id)))
}

// stickByte converts a libretro analog axis (-32768 to
// 32767, positive down for Y) into a GameCube stick byte
// (0 to 255, centered at 128, up is high for Y).
func stickByte(v int16, invert bool) uint8 {
	scaled := int32(v) * 127 / 32767
	out := int32(128)
	if invert {
		out -= scaled
	} else {
		out += scaled
	}
	if out < 0 {
		out = 0
	}
	if out > 255 {
		out = 255
	}
	return uint8(out)
}

// triggerByte converts a libretro analog trigger value
// into a GameCube analog trigger byte, with the digital
// full-press fallback when the frontend reports no
// analog value.
func triggerByte(port int, analogID uint, digitalPressed bool) uint8 {
	if v := analogButton(port, analogID); v > 0 {
		return uint8(int32(v) * 255 / 32767)
	}
	if digitalPressed {
		return 255
	}
	return 0
}

// pollPad translates libretro input into the GameCube
// pad state. Mapping: A, B, X, Y and Start map to their
// GameCube namesakes; libretro R2 maps to the GameCube Z
// button (Z is the lone right shoulder button on the GC
// pad); L and R map to the GC L and R buttons and also
// drive the analog triggers, whose values come from the
// libretro analog L2 and R2 triggers when the frontend
// reports them and fall back to a digital full press
// otherwise; the main stick rides the left analog and
// the C-stick the right analog.
func pollPad(port int) system.PadState {
	var p system.PadState
	if pressed(port, joypadA) {
		p.Buttons |= system.PadA
	}
	if pressed(port, joypadB) {
		p.Buttons |= system.PadB
	}
	if pressed(port, joypadX) {
		p.Buttons |= system.PadX
	}
	if pressed(port, joypadY) {
		p.Buttons |= system.PadY
	}
	if pressed(port, joypadStart) {
		p.Buttons |= system.PadStart
	}
	if pressed(port, joypadUp) {
		p.Buttons |= system.PadUp
	}
	if pressed(port, joypadDown) {
		p.Buttons |= system.PadDown
	}
	if pressed(port, joypadLeft) {
		p.Buttons |= system.PadLeft
	}
	if pressed(port, joypadRight) {
		p.Buttons |= system.PadRight
	}
	lDig := pressed(port, joypadL)
	rDig := pressed(port, joypadR)
	if lDig {
		p.Buttons |= system.PadL
	}
	if rDig {
		p.Buttons |= system.PadR
	}
	if pressed(port, joypadR2) || analogButton(port, joypadR2) > 16384 {
		p.Buttons |= system.PadZ
	}
	p.TriggerL = triggerByte(port, joypadL2, lDig)
	p.TriggerR = triggerByte(port, joypadR2, rDig)
	p.StickX = stickByte(axis(port, analogIndexLeft, analogAxisX), false)
	p.StickY = stickByte(axis(port, analogIndexLeft, analogAxisY), true)
	p.CStickX = stickByte(axis(port, analogIndexRight, analogAxisX), false)
	p.CStickY = stickByte(axis(port, analogIndexRight, analogAxisY), true)
	return p
}

// resampler linearly resamples the AI output to the
// declared 48000 Hz. The AI rate is game-programmed
// (48000 or 32000 per the AICR rate select); at 48000
// the stream passes through untouched. The pending
// buffer carries the fractional read position across
// frames so no drift accumulates.
type resampler struct {
	buf [][2]int16
	pos float64
}

func (r *resampler) reset() {
	r.buf = r.buf[:0]
	r.pos = 0
}

func (r *resampler) process(in []int16, srcRate int) []int16 {
	if srcRate <= 0 || srcRate == gcnSampleRate {
		return in
	}
	for i := 0; i+1 < len(in); i += 2 {
		r.buf = append(r.buf, [2]int16{in[i], in[i+1]})
	}
	step := float64(srcRate) / gcnSampleRate
	var out []int16
	for r.pos+1 < float64(len(r.buf)) {
		i := int(r.pos)
		frac := r.pos - float64(i)
		a, b := r.buf[i], r.buf[i+1]
		out = append(out,
			int16(float64(a[0])+(float64(b[0])-float64(a[0]))*frac),
			int16(float64(a[1])+(float64(b[1])-float64(a[1]))*frac))
		r.pos += step
	}
	if drop := int(r.pos); drop > 0 {
		r.buf = append(r.buf[:0], r.buf[drop:]...)
		r.pos -= float64(drop)
	}
	return out
}

//export retro_run
func retro_run() {
	if sys == nil {
		return
	}
	C.gcn_input_poll()
	sys.SetButtons(pollPad(0))
	sys.RunFrame()

	fb := sys.Framebuffer()
	if len(fb) > 0 {
		C.gcn_video(unsafe.Pointer(&fb[0]),
			C.unsigned(system.Width), C.unsigned(system.Height),
			C.size_t(system.Width*4))
	}
	ab := resamp.process(sys.AudioBuffer(), sys.AudioRate())
	if len(ab) >= 2 {
		frames := len(ab) / 2
		done := int(C.gcn_audio_batch((*C.int16_t)(unsafe.Pointer(&ab[0])), C.size_t(frames)))
		for i := done; i < frames; i++ {
			C.gcn_audio_sample(C.int16_t(ab[2*i]), C.int16_t(ab[2*i+1]))
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
	buf := unsafe.Slice((*byte)(data), system.StateSize)
	cp := make([]byte, system.StateSize)
	copy(cp, buf)
	return sys.Deserialize(cp) == nil
}

// The memory interface exposes MEM1 as SYSTEM_RAM. There
// is no SAVE_RAM device: GameCube saves live on memory
// cards, which this shell persists as files in the save
// directory instead.

//export retro_get_memory_data
func retro_get_memory_data(id C.unsigned) unsafe.Pointer {
	if sys == nil || id != memSystemRAM {
		return nil
	}
	ram := sys.SystemRAM()
	if len(ram) == 0 {
		return nil
	}
	return unsafe.Pointer(&ram[0])
}

//export retro_get_memory_size
func retro_get_memory_size(id C.unsigned) C.size_t {
	if sys == nil || id != memSystemRAM {
		return 0
	}
	return C.size_t(len(sys.SystemRAM()))
}

// Cheats are applied by the iTaK engine through its own
// decoding for cores that support them; this core does
// not expose cheat hooks yet.

//export retro_cheat_reset
func retro_cheat_reset() {}

//export retro_cheat_set
func retro_cheat_set(index C.unsigned, enabled C.bool, code *C.char) {}
