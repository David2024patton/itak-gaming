package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file is the native core path: the bundled Go cores linked
// straight into the backend as Go packages, behind the same calls
// the cgo .so bridge (bridge.go) serves.
//
// Why it exists: a Go c-shared core (.so) carries its own Go
// runtime. Loaded into the app process, which already runs the
// gomobile backend runtime, the second runtime dies during its
// own startup (fatal error: stoplockedm: not runnable, captured
// on the Death Star 2026-10-15 for every 32-bit core). Multiple
// Go runtimes in one process are not a supported configuration.
// Linking the engines into the backend keeps exactly one
// runtime in the process. The .so bridge remains for desktop
// harness use (cmd/enginetest, cmd/coretest).

// NativeCore is the uniform contract each bundled core adapter
// implements. Adapters wrap a core engine's system package the
// same way that core's libretro shell does; the shell stays the
// reference for every mapping (input bit order, timing
// constants, memory exposure).
type NativeCore interface {
	// LoadGame starts the machine on a game. Cartridge cores
	// use data; disc cores use path (data then holds the small
	// descriptor file, e.g. the .cue sheet).
	LoadGame(data []byte, path string) error
	RunFrame()
	// FrameSize is the native frame geometry in pixels.
	FrameSize() (w, h int)
	// Frame returns the latest frame in PixelFormat, tightly
	// packed (pitch = width * bytes per pixel). The slice is
	// owned by the engine and rewritten by the next RunFrame;
	// callers that keep it must copy.
	Frame() []byte
	// PixelFormat is the libretro pixel format: 0 = 0RGB1555,
	// 1 = XRGB8888, 2 = RGB565.
	PixelFormat() int
	AspectRatio() float64
	FPS() float64
	SampleRate() float64
	// SetInput hands the core one player's buttons as a
	// libretro-order mask (bit b = RETRO_DEVICE_ID_JOYPAD b).
	// The adapter translates to the engine's own bit order.
	SetInput(player int, libretroMask uint16)
	// AudioDrain returns every pending stereo int16 sample
	// (interleaved L, R) and clears the engine's buffer.
	AudioDrain() []int16
	SaveState() ([]byte, error)
	LoadState(data []byte) error
	CheatSupported() bool
	CheatReset()
	CheatSet(index int, code string)
	Close()
}

// nativeCores maps a core file name (what the app passes to
// LoadCore) to its adapter constructor.
var nativeCores = map[string]func() NativeCore{
	"libgo_gameboy.so": func() NativeCore { return &gameboyCore{} },
	"libgo_nes.so":     func() NativeCore { return &nesCore{} },
	"libgo_snes.so":    func() NativeCore { return &snesCore{} },
	"libgo_genesis.so": func() NativeCore { return &genesisCore{} },
	"libgo_gba.so":     func() NativeCore { return &gbaCore{} },
	"libgo_ps1.so":     func() NativeCore { return &ps1Core{} },
}

// LoadNative returns a bridge over the bundled native core for
// the given core path, or false when the path names no bundled
// core (the caller then falls back to the .so bridge).
func LoadNative(corePath string) (*NativeBridge, bool) {
	ctor, ok := nativeCores[filepath.Base(corePath)]
	if !ok {
		return nil, false
	}
	return &NativeBridge{core: ctor()}, true
}

// NativeBridge serves the same method set as Bridge (bridge.go)
// over a NativeCore, so the mobile package drives either
// without knowing which is underneath.
type NativeBridge struct {
	core    NativeCore
	buttons [2]uint16 // libretro-order masks
	pending []int16   // drained audio waiting for the caller

	frames      uint64
	audioTotal  uint64
	inputCalls  uint64
	everNonzero bool
}

// LoadGame loads a ROM file into the core.
func (b *NativeBridge) LoadGame(romPath string) error {
	if b.core == nil {
		return fmt.Errorf("no core loaded")
	}
	data, err := os.ReadFile(romPath)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty ROM file")
	}
	return b.core.LoadGame(data, romPath)
}

// RunFrame runs one emulation frame.
func (b *NativeBridge) RunFrame() {
	if b.core == nil {
		return
	}
	b.core.SetInput(0, b.buttons[0])
	b.core.SetInput(1, b.buttons[1])
	b.core.RunFrame()
	b.frames++
	if !b.everNonzero && firstNonzero(b.core.Frame()) >= 0 {
		b.everNonzero = true
	}
}

// GetFrame returns the latest video frame as bytes. The data is
// a copy: the engine rewrites its own buffer on the next frame.
func (b *NativeBridge) GetFrame() (width, height, pitch int, data []byte) {
	if b.core == nil {
		return 0, 0, 0, nil
	}
	w, h := b.core.FrameSize()
	raw := b.core.Frame()
	if w == 0 || h == 0 || len(raw) == 0 {
		return 0, 0, 0, nil
	}
	pitch = len(raw) / h
	out := make([]byte, len(raw))
	copy(out, raw)
	return w, h, pitch, out
}

// PixelFormat returns the libretro pixel format the core
// produces (0=0RGB1555, 1=XRGB8888, 2=RGB565).
func (b *NativeBridge) PixelFormat() int {
	if b.core == nil {
		return 1
	}
	return b.core.PixelFormat()
}

// AspectRatio returns the display aspect ratio reported by the
// core, falling back to 4:3.
func (b *NativeBridge) AspectRatio() float64 {
	if b.core == nil {
		return 4.0 / 3.0
	}
	if ar := b.core.AspectRatio(); ar > 0 {
		return ar
	}
	return 4.0 / 3.0
}

// FPS returns the core's target frame rate.
func (b *NativeBridge) FPS() float64 {
	if b.core == nil {
		return 60
	}
	return b.core.FPS()
}

// SampleRate returns the core's audio sample rate.
func (b *NativeBridge) SampleRate() float64 {
	if b.core == nil {
		return 44100
	}
	return b.core.SampleRate()
}

// FrameToRGBA converts the latest frame to RGBA bytes at
// native resolution; the caller scales to AspectRatio().
func (b *NativeBridge) FrameToRGBA() (width, height int, rgba []byte, err error) {
	w, h, pitch, data := b.GetFrame()
	if len(data) == 0 {
		return 0, 0, nil, fmt.Errorf("no frame available")
	}
	rgba, err = convertToRGBA(w, h, pitch, data, b.PixelFormat())
	if err != nil {
		return 0, 0, nil, err
	}
	return w, h, rgba, nil
}

// convertToRGBA expands a raw frame in a libretro pixel format
// to RGBA8888. Shared by the native bridge; the cgo bridge
// carries the same conversion for .so cores.
func convertToRGBA(w, h, pitch int, data []byte, format int) ([]byte, error) {
	rgba := make([]byte, w*h*4)
	switch format {
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
	case 1: // XRGB8888 (memory: B, G, R, X)
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
		return nil, fmt.Errorf("unsupported pixel format %d", format)
	}
	return rgba, nil
}

// SaveState captures the current emulation state.
func (b *NativeBridge) SaveState() ([]byte, error) {
	if b.core == nil {
		return nil, fmt.Errorf("no core loaded")
	}
	return b.core.SaveState()
}

// LoadState restores an emulation state.
func (b *NativeBridge) LoadState(data []byte) error {
	if b.core == nil {
		return fmt.Errorf("no core loaded")
	}
	if len(data) == 0 {
		return fmt.Errorf("empty save data")
	}
	return b.core.LoadState(data)
}

// CheatsSupported reports whether the core accepts cheat codes.
func (b *NativeBridge) CheatsSupported() bool {
	return b.core != nil && b.core.CheatSupported()
}

// ApplyCheats replaces the core's cheat list and returns how
// many entries were set; multi-part codes joined with '+' are
// registered as separate entries, as in the .so bridge.
func (b *NativeBridge) ApplyCheats(codes []string) int {
	if b.core == nil {
		return 0
	}
	b.core.CheatReset()
	if !b.core.CheatSupported() {
		return 0
	}
	idx := 0
	for _, code := range codes {
		for _, part := range strings.Split(code, "+") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			b.core.CheatSet(idx, part)
			idx++
		}
	}
	return idx
}

// ClearCheats removes every cheat from the core.
func (b *NativeBridge) ClearCheats() {
	if b.core != nil {
		b.core.CheatReset()
	}
}

// SetButton sets a joypad button state for a player (0 or 1).
func (b *NativeBridge) SetButton(player int, button int, pressed bool) {
	if player < 0 || player > 1 || button < 0 || button > 15 {
		return
	}
	b.inputCalls++
	if pressed {
		b.buttons[player] |= 1 << uint(button)
	} else {
		b.buttons[player] &^= 1 << uint(button)
	}
}

// ClearButtons releases all buttons.
func (b *NativeBridge) ClearButtons() {
	b.buttons = [2]uint16{}
}

// DrainAudio returns up to maxFrames stereo frames of int16
// samples, buffering any excess for the next call.
func (b *NativeBridge) DrainAudio(maxFrames int) []int16 {
	if b.core != nil {
		b.pending = append(b.pending, b.core.AudioDrain()...)
	}
	n := len(b.pending) / 2
	if n > maxFrames {
		n = maxFrames
	}
	if n <= 0 {
		return nil
	}
	out := make([]int16, n*2)
	copy(out, b.pending[:n*2])
	b.pending = append(b.pending[:0], b.pending[n*2:]...)
	b.audioTotal += uint64(n)
	return out
}

// VideoFrameCount returns how many frames have run.
func (b *NativeBridge) VideoFrameCount() uint64 { return b.frames }

// VideoNullCount returns 0: native cores always produce a
// frame, there are no dupe frames.
func (b *NativeBridge) VideoNullCount() uint64 { return 0 }

// VideoFirstNonzero returns the offset of the first non-zero
// byte in the latest frame, or -1 when it is all black.
func (b *NativeBridge) VideoFirstNonzero() int64 {
	_, _, _, data := b.GetFrame()
	return int64(firstNonzero(data))
}

// VideoEverNonzero reports whether any frame had visible
// content.
func (b *NativeBridge) VideoEverNonzero() bool { return b.everNonzero }

// InputStateCalls returns how many button updates arrived
// (diagnostics).
func (b *NativeBridge) InputStateCalls() uint64 { return b.inputCalls }

// AudioSamplesTotal returns total audio frames handed out
// (diagnostics).
func (b *NativeBridge) AudioSamplesTotal() uint64 { return b.audioTotal }

// Close unloads the core.
func (b *NativeBridge) Close() {
	if b.core != nil {
		b.core.Close()
		b.core = nil
	}
}

func firstNonzero(data []byte) int {
	for i, v := range data {
		if v != 0 {
			return i
		}
	}
	return -1
}
