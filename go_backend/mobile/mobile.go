// Package mobile is the gomobile-bound API surface for iTaK Gaming.
// The Android (Flutter) app calls these functions to run emulation.
// Only gomobile-supported types are used.
package mobile

import (
	"encoding/binary"
	"strings"

	"github.com/davidpatton/itak-gaming/go_backend/internal/core"
)

var emu *core.Bridge

// Joypad button IDs mirror core package constants.
const (
	ButtonB      = core.ButtonB
	ButtonY      = core.ButtonY
	ButtonSelect = core.ButtonSelect
	ButtonStart  = core.ButtonStart
	ButtonUp     = core.ButtonUp
	ButtonDown   = core.ButtonDown
	ButtonLeft   = core.ButtonLeft
	ButtonRight  = core.ButtonRight
	ButtonA      = core.ButtonA
	ButtonX      = core.ButtonX
	ButtonL      = core.ButtonL
	ButtonR      = core.ButtonR
)

// LoadCore loads a libretro core .so from an absolute path.
func LoadCore(path string) error {
	b, err := core.LoadCore(path)
	if err != nil {
		return err
	}
	emu = b
	return nil
}

// LoadGame loads a ROM file into the loaded core.
func LoadGame(path string) error {
	if emu == nil {
		return errNoCore
	}
	return emu.LoadGame(path)
}

var errNoCore = &simpleError{"no core loaded"}

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

// RunFrame advances emulation by one frame.
func RunFrame() {
	if emu != nil {
		emu.RunFrame()
	}
}

// FrameWidth returns the native frame width in pixels.
func FrameWidth() int {
	if emu == nil {
		return 0
	}
	w, _, _, _ := emu.GetFrame()
	return w
}

// FrameHeight returns the native frame height in pixels.
func FrameHeight() int {
	if emu == nil {
		return 0
	}
	_, h, _, _ := emu.GetFrame()
	return h
}

// FrameRGBA returns the latest frame converted to RGBA bytes
// (width*height*4). Draw it scaled to AspectRatio().
func FrameRGBA() []byte {
	if emu == nil {
		return nil
	}
	_, _, rgba, err := emu.FrameToRGBA()
	if err != nil {
		return nil
	}
	return rgba
}

// AspectRatio returns the display aspect ratio reported by the core.
func AspectRatio() float32 {
	if emu == nil {
		return 4.0 / 3.0
	}
	return float32(emu.AspectRatio())
}

// FPS returns the core target frame rate.
func FPS() float32 {
	if emu == nil {
		return 60
	}
	return float32(emu.FPS())
}

// SampleRate returns the core audio sample rate in Hz.
func SampleRate() int {
	if emu == nil {
		return 32040
	}
	return int(emu.SampleRate())
}

// SetButton sets a joypad button for a player (0 or 1).
func SetButton(player, button int, pressed bool) {
	if emu != nil {
		emu.SetButton(player, button, pressed)
	}
}

// ClearButtons releases all buttons.
func ClearButtons() {
	if emu != nil {
		emu.ClearButtons()
	}
}

// DrainAudio returns pending stereo int16 audio as little-endian bytes.
func DrainAudio() []byte {
	if emu == nil {
		return nil
	}
	samples := emu.DrainAudio(4096)
	if len(samples) == 0 {
		return nil
	}
	out := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}

// SaveState returns the serialized emulation state.
func SaveState() []byte {
	if emu == nil {
		return nil
	}
	data, err := emu.SaveState()
	if err != nil {
		return nil
	}
	return data
}

// LoadState restores a serialized emulation state.
func LoadState(data []byte) error {
	if emu == nil {
		return errNoCore
	}
	return emu.LoadState(data)
}

// CheatsSupported reports whether the loaded core takes cheat codes.
func CheatsSupported() bool {
	if emu == nil {
		return false
	}
	return emu.CheatsSupported()
}

// ApplyCheats replaces the core's cheat list. Codes arrive newline
// separated; multi-part codes keep their '+' joins and are expanded
// by the bridge. Returns how many entries were set on the core.
func ApplyCheats(joined string) int {
	if emu == nil {
		return 0
	}
	var codes []string
	for _, line := range strings.Split(joined, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			codes = append(codes, line)
		}
	}
	return emu.ApplyCheats(codes)
}

// ClearCheats removes all cheats from the core.
func ClearCheats() {
	if emu != nil {
		emu.ClearCheats()
	}
}

// Close shuts down the core.
func Close() {
	if emu != nil {
		emu.Close()
		emu = nil
	}
}
