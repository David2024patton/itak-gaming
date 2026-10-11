package core

/*
#cgo CFLAGS: -I.
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include <stdbool.h>
#include <string.h>
#include <stdint.h>

// Libretro API function pointers
typedef void (*retro_init_fn)(void);
typedef void (*retro_deinit_fn)(void);
typedef bool (*retro_load_game_fn)(void* game_info);
typedef void (*retro_run_fn)(void);
typedef void (*retro_set_environment_fn)(void* cb);
typedef void (*retro_set_video_refresh_fn)(void* cb);
typedef void (*retro_set_audio_sample_batch_fn)(void* cb);
typedef void (*retro_set_input_poll_fn)(void* cb);
typedef void (*retro_set_input_state_fn)(void* cb);
typedef size_t (*retro_serialize_size_fn)(void);
typedef bool (*retro_serialize_fn)(void* data, size_t size);
typedef bool (*retro_unserialize_fn)(void* data, size_t size);
typedef void (*retro_get_system_av_info_fn)(void* info);

typedef struct {
    void* handle;
    retro_init_fn init;
    retro_deinit_fn deinit;
    retro_load_game_fn load_game;
    retro_run_fn run;
    retro_serialize_size_fn serialize_size;
    retro_serialize_fn serialize;
    retro_unserialize_fn unserialize;
    retro_get_system_av_info_fn get_av_info;
    float aspect_ratio;
    double fps;
    double sample_rate;
} CoreHandle;

// Video frame buffer (shared with Go)
static unsigned char* video_buffer = NULL;
static unsigned video_width = 0;
static unsigned video_height = 0;
static unsigned video_pitch = 0;
static unsigned long video_frame_count = 0;
static unsigned long video_null_count = 0;
static long video_first_nonzero = -1;
static int video_ever_nonzero = 0;

// Called by the core for each video frame
static void video_refresh_cb(const void* data, unsigned width, unsigned height, size_t pitch) {
    video_frame_count++;
    if (!data) { video_null_count++; return; }
    // Scan for first non-zero byte in this frame
    const unsigned char* p = (const unsigned char*)data;
    size_t total = height * pitch;
    for (size_t i = 0; i < total; i++) {
        if (p[i] != 0) { video_first_nonzero = (long)i; break; }
    }
    if (video_first_nonzero >= 0) video_ever_nonzero = 1;
    // Allocate buffer if needed
    size_t size = height * pitch;
    if (!video_buffer || width != video_width || height != video_height) {
        free(video_buffer);
        video_buffer = malloc(size);
        video_width = width;
        video_height = height;
        video_pitch = pitch;
    }
    memcpy(video_buffer, data, size);
}

// Audio capture ring buffer. The core pushes interleaved int16 stereo samples;
// Go drains them for playback.
#define AUDIO_RING_SIZE (48000 * 2 * 4)
static int16_t audio_ring[AUDIO_RING_SIZE];
static volatile size_t audio_write_pos = 0;
static volatile size_t audio_read_pos = 0;
static unsigned long audio_samples_total = 0;

// Audio callback - captures samples into the ring buffer
static size_t audio_batch_cb(const short* data, size_t frames) {
    for (size_t i = 0; i < frames * 2; i++) {
        audio_ring[audio_write_pos] = data[i];
        audio_write_pos = (audio_write_pos + 1) % AUDIO_RING_SIZE;
        if (audio_write_pos == audio_read_pos) {
            audio_read_pos = (audio_read_pos + 1) % AUDIO_RING_SIZE;
        }
    }
    audio_samples_total += frames;
    return frames;
}

// Drain up to max_frames stereo frames into out. Returns frames written.
int core_drain_audio(int16_t* out, int max_frames) {
    int n = 0;
    while (n < max_frames && audio_read_pos != audio_write_pos) {
        out[n*2] = audio_ring[audio_read_pos];
        audio_read_pos = (audio_read_pos + 1) % AUDIO_RING_SIZE;
        out[n*2+1] = audio_ring[audio_read_pos];
        audio_read_pos = (audio_read_pos + 1) % AUDIO_RING_SIZE;
        n++;
    }
    return n;
}

unsigned long core_get_audio_samples() { return audio_samples_total; }

// Input state shared between Go and the core callbacks.
// 2 players, 16 joypad buttons each. Set from Go via core_set_button.
static int input_buttons[2][16];
static unsigned long input_state_calls = 0;

// Input callbacks
static void input_poll_cb(void) {
    // State is pushed from Go; nothing to poll here.
}

static int16_t input_state_cb(unsigned port, unsigned device, unsigned index, unsigned id) {
    input_state_calls++;
    // device 1 = RETRO_DEVICE_JOYPAD
    if (device != 1) return 0;
    if (port > 1 || id > 15) return 0;
    return input_buttons[port][id] ? 1 : 0;
}

void core_set_button(int port, int id, int pressed) {
    if (port < 0 || port > 1 || id < 0 || id > 15) return;
    input_buttons[port][id] = pressed;
}

void core_clear_buttons() {
    memset(input_buttons, 0, sizeof(input_buttons));
}

unsigned long core_get_input_state_calls() { return input_state_calls; }

// Environment callback
static int g_pixel_format = -1;
static bool env_cb(unsigned cmd, void* data) {
    // cmd 10 = RETRO_ENVIRONMENT_SET_PIXEL_FORMAT
    // data is enum retro_pixel_format: 0=0RGB1555, 1=XRGB8888, 2=RGB565
    if (cmd == 10) {
        if (data) g_pixel_format = *(int*)data;
        return true;
    }
    return false;
}
int core_get_pixel_format() { return g_pixel_format; }

CoreHandle* core_load(const char* path) {
    CoreHandle* h = calloc(1, sizeof(CoreHandle));
    h->handle = dlopen(path, RTLD_NOW);
    if (!h->handle) {
        free(h);
        return NULL;
    }
    h->init = (retro_init_fn)dlsym(h->handle, "retro_init");
    h->deinit = (retro_deinit_fn)dlsym(h->handle, "retro_deinit");
    h->load_game = (retro_load_game_fn)dlsym(h->handle, "retro_load_game");
    h->run = (retro_run_fn)dlsym(h->handle, "retro_run");
    h->serialize_size = (retro_serialize_size_fn)dlsym(h->handle, "retro_serialize_size");
    h->serialize = (retro_serialize_fn)dlsym(h->handle, "retro_serialize");
    h->unserialize = (retro_unserialize_fn)dlsym(h->handle, "retro_unserialize");
    h->get_av_info = (retro_get_system_av_info_fn)dlsym(h->handle, "retro_get_system_av_info");
    h->aspect_ratio = 0;
    h->fps = 60.0;
    h->sample_rate = 32000.0;

    if (!h->init || !h->load_game || !h->run) {
        dlclose(h->handle);
        free(h);
        return NULL;
    }

    retro_set_environment_fn set_env = (retro_set_environment_fn)dlsym(h->handle, "retro_set_environment");
    retro_set_video_refresh_fn set_video = (retro_set_video_refresh_fn)dlsym(h->handle, "retro_set_video_refresh");
    retro_set_audio_sample_batch_fn set_audio = (retro_set_audio_sample_batch_fn)dlsym(h->handle, "retro_set_audio_sample_batch");
    retro_set_input_poll_fn set_poll = (retro_set_input_poll_fn)dlsym(h->handle, "retro_set_input_poll");
    retro_set_input_state_fn set_state = (retro_set_input_state_fn)dlsym(h->handle, "retro_set_input_state");

    if (set_env) set_env(env_cb);
    if (set_video) set_video(video_refresh_cb);
    if (set_audio) set_audio(audio_batch_cb);
    if (set_poll) set_poll(input_poll_cb);
    if (set_state) set_state(input_state_cb);

    h->init();
    return h;
}

bool core_load_game(CoreHandle* h, const void* data, size_t size) {
    // retro_game_info struct
    struct {
        const char* path;
        const void* data;
        size_t size;
        const char* meta;
    } info = {NULL, data, size, NULL};
    bool ok = h->load_game(&info);
    if (ok && h->get_av_info) {
        // retro_system_av_info: geometry then timing
        struct {
            unsigned base_width;
            unsigned base_height;
            unsigned max_width;
            unsigned max_height;
            float aspect_ratio;
        } geometry;
        struct {
            double fps;
            double sample_rate;
        } timing;
        struct {
            typeof(geometry) geometry;
            typeof(timing) timing;
        } av;
        memset(&av, 0, sizeof(av));
        h->get_av_info(&av);
        h->aspect_ratio = av.geometry.aspect_ratio;
        h->fps = av.timing.fps;
        h->sample_rate = av.timing.sample_rate;
    }
    return ok;
}

float core_get_aspect(CoreHandle* h) { return h->aspect_ratio; }
double core_get_fps(CoreHandle* h) { return h->fps; }
double core_get_sample_rate(CoreHandle* h) { return h->sample_rate; }

void core_run(CoreHandle* h) {
    h->run();
}

void core_unload(CoreHandle* h) {
    if (h->deinit) h->deinit();
    if (h->handle) dlclose(h->handle);
    free(h);
    if (video_buffer) {
        free(video_buffer);
        video_buffer = NULL;
    }
}

// Get the latest video frame (copies to Go-provided buffer)
int core_get_frame(unsigned char* out, int max_size) {
    if (!video_buffer) return 0;
    size_t size = video_height * video_pitch;
    if ((int)size > max_size) return -1;
    memcpy(out, video_buffer, size);
    return size;
}

int core_get_width() { return video_width; }
int core_get_height() { return video_height; }
int core_get_pitch() { return video_pitch; }
unsigned long core_get_frame_count() { return video_frame_count; }
unsigned long core_get_null_count() { return video_null_count; }
long core_get_first_nonzero() { return video_first_nonzero; }
int core_get_ever_nonzero() { return video_ever_nonzero; }

// Save state
size_t core_save_size(CoreHandle* h) {
    if (h->serialize_size) return h->serialize_size();
    return 0;
}

bool core_save(CoreHandle* h, void* data, size_t size) {
    if (h->serialize) return h->serialize(data, size);
    return false;
}

bool core_load_save(CoreHandle* h, void* data, size_t size) {
    if (h->unserialize) return h->unserialize(data, size);
    return false;
}
*/
import "C"
import (
	"fmt"
	"os"
	"unsafe"
)

// Bridge wraps a loaded libretro core
type Bridge struct {
	handle *C.CoreHandle
}

// LoadCore loads a libretro core from path
func LoadCore(path string) (*Bridge, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	h := C.core_load(cpath)
	if h == nil {
		return nil, fmt.Errorf("failed to load core: %s", path)
	}
	return &Bridge{handle: h}, nil
}

// LoadGame loads a ROM file into the core
func (b *Bridge) LoadGame(romPath string) error {
	data, err := os.ReadFile(romPath)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty ROM file")
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)

	ok := C.core_load_game(b.handle, cdata, C.size_t(len(data)))
	if !bool(ok) {
		return fmt.Errorf("core failed to load game")
	}
	return nil
}

// RunFrame runs one emulation frame
func (b *Bridge) RunFrame() {
	C.core_run(b.handle)
}

// VideoFrameCount returns how many times the video callback fired
func (b *Bridge) VideoFrameCount() uint64 {
	return uint64(C.core_get_frame_count())
}

// VideoNullCount returns frames where data was NULL (dupe frames)
func (b *Bridge) VideoNullCount() uint64 {
	return uint64(C.core_get_null_count())
}

// VideoFirstNonzero returns offset of first non-zero byte in latest frame, -1 if all black
func (b *Bridge) VideoFirstNonzero() int64 {
	return int64(C.core_get_first_nonzero())
}

// VideoEverNonzero returns 1 if any frame ever had visible content
func (b *Bridge) VideoEverNonzero() bool {
	return C.core_get_ever_nonzero() == 1
}

// PixelFormat returns the libretro pixel format the core requested
// 0=0RGB1555, 1=XRGB8888, 2=RGB565
func (b *Bridge) PixelFormat() int {
	return int(C.core_get_pixel_format())
}

// AspectRatio returns the display aspect ratio reported by the core
// (e.g. 4/3 for SNES). Falls back to 4:3 when the core reports 0.
// The renderer must scale the raw frame to this ratio (pillarboxed),
// NOT show raw square pixels, or games look squished.
func (b *Bridge) AspectRatio() float64 {
	ar := float64(C.core_get_aspect(b.handle))
	if ar <= 0 {
		return 4.0 / 3.0
	}
	return ar
}

// FPS returns the core's target frame rate
func (b *Bridge) FPS() float64 {
	return float64(C.core_get_fps(b.handle))
}

// SampleRate returns the core's audio sample rate
func (b *Bridge) SampleRate() float64 {
	return float64(C.core_get_sample_rate(b.handle))
}

// FrameToRGBA converts the latest frame to RGBA bytes at native resolution.
// The caller scales it to AspectRatio() for display.
func (b *Bridge) FrameToRGBA() (width, height int, rgba []byte, err error) {
	w, h, pitch, data := b.GetFrame()
	if len(data) == 0 {
		return 0, 0, nil, fmt.Errorf("no frame available")
	}
	rgba = make([]byte, w*h*4)
	switch b.PixelFormat() {
	case 2: // RGB565
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				off := y*pitch + x*2
				v := uint16(data[off]) | uint16(data[off+1])<<8
				r := (v >> 11) & 0x1F
				g := (v >> 5) & 0x3F
				bl := v & 0x1F
				dst := (y*w + x) * 4
				rgba[dst+0] = byte(r<<3 | r>>2)
				rgba[dst+1] = byte(g<<2 | g>>4)
				rgba[dst+2] = byte(bl<<3 | bl>>2)
				rgba[dst+3] = 0xFF
			}
		}
	case 1: // XRGB8888 (memory: B,G,R,X)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				off := y*pitch + x*4
				dst := (y*w + x) * 4
				rgba[dst+0] = data[off+2]
				rgba[dst+1] = data[off+1]
				rgba[dst+2] = data[off+0]
				rgba[dst+3] = 0xFF
			}
		}
	default:
		return 0, 0, nil, fmt.Errorf("unsupported pixel format %d", b.PixelFormat())
	}
	return w, h, rgba, nil
}

// GetFrame returns the latest video frame as bytes
func (b *Bridge) GetFrame() (width, height, pitch int, data []byte) {
	w := int(C.core_get_width())
	h := int(C.core_get_height())
	p := int(C.core_get_pitch())
	if w == 0 || h == 0 {
		return 0, 0, 0, nil
	}
	size := h * p
	buf := make([]byte, size)
	n := C.core_get_frame((*C.uchar)(unsafe.Pointer(&buf[0])), C.int(size))
	if n <= 0 {
		return 0, 0, 0, nil
	}
	return w, h, p, buf[:n]
}

// SaveState captures the current emulation state
func (b *Bridge) SaveState() ([]byte, error) {
	size := C.core_save_size(b.handle)
	if size == 0 {
		return nil, fmt.Errorf("core does not support save states")
	}
	buf := make([]byte, size)
	ok := C.core_save(b.handle, unsafe.Pointer(&buf[0]), size)
	if !bool(ok) {
		return nil, fmt.Errorf("save failed")
	}
	return buf, nil
}

// LoadState restores an emulation state
func (b *Bridge) LoadState(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty save data")
	}
	ok := C.core_load_save(b.handle, unsafe.Pointer(&data[0]), C.size_t(len(data)))
	if !bool(ok) {
		return fmt.Errorf("load state failed")
	}
	return nil
}

// Joypad button IDs (libretro RETRO_DEVICE_ID_JOYPAD_*)
const (
	ButtonB      = 0
	ButtonY      = 1
	ButtonSelect = 2
	ButtonStart  = 3
	ButtonUp     = 4
	ButtonDown   = 5
	ButtonLeft   = 6
	ButtonRight  = 7
	ButtonA      = 8
	ButtonX      = 9
	ButtonL      = 10
	ButtonR      = 11
	ButtonL2     = 12
	ButtonR2     = 13
	ButtonL3     = 14
	ButtonR3     = 15
)

// SetButton sets a joypad button state for a player (0 or 1)
func (b *Bridge) SetButton(player int, button int, pressed bool) {
	p := 0
	if pressed {
		p = 1
	}
	C.core_set_button(C.int(player), C.int(button), C.int(p))
}

// ClearButtons releases all buttons
func (b *Bridge) ClearButtons() {
	C.core_clear_buttons()
}

// InputStateCalls returns how many times the core queried input (diagnostics)
func (b *Bridge) InputStateCalls() uint64 {
	return uint64(C.core_get_input_state_calls())
}

// DrainAudio returns up to maxFrames stereo frames of int16 samples
func (b *Bridge) DrainAudio(maxFrames int) []int16 {
	out := make([]int16, maxFrames*2)
	n := C.core_drain_audio((*C.int16_t)(unsafe.Pointer(&out[0])), C.int(maxFrames))
	if n <= 0 {
		return nil
	}
	return out[:n*2]
}

// AudioSamplesTotal returns total audio frames the core produced (diagnostics)
func (b *Bridge) AudioSamplesTotal() uint64 {
	return uint64(C.core_get_audio_samples())
}

// Close unloads the core
func (b *Bridge) Close() {
	if b.handle != nil {
		C.core_unload(b.handle)
		b.handle = nil
	}
}
