package core

import (
	"fmt"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-gameboy/ppu"
	"github.com/davidpatton/itak-gaming/cores/go-gameboy/sound"
	gbsystem "github.com/davidpatton/itak-gaming/cores/go-gameboy/system"
)

// Native adapter for the Go Game Boy core. Every constant and
// mapping mirrors cores/go-gameboy/libretro/main.go, which
// stays the reference shell for this engine. The one binary
// serves .gb and .gbc images; the system package picks DMG or
// CGB mode from the cartridge header.

type gameboyCore struct {
	sys   *gbsystem.System
	input [2]uint16
}

func (c *gameboyCore) LoadGame(data []byte, _ string) error {
	s, err := gbsystem.New(data)
	if err != nil {
		return err
	}
	c.sys = s
	return nil
}

func (c *gameboyCore) RunFrame() {
	if c.sys == nil || !c.sys.Loaded() {
		return
	}
	c.sys.SetButtons(0, c.input[0])
	c.sys.SetButtons(1, c.input[1])
	c.sys.RunFrame()
}

func (c *gameboyCore) FrameSize() (int, int) { return ppu.Width, ppu.Height }

func (c *gameboyCore) Frame() []byte {
	if c.sys == nil {
		return nil
	}
	fb := c.sys.Framebuffer()
	if len(fb) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&fb[0])), len(fb)*4)
}

// PixelFormat: the PPU produces XRGB8888.
func (c *gameboyCore) PixelFormat() int { return 1 }

// AspectRatio mirrors the shell's declared 1.1111
// (160/144 = 10/9, pixel aspect 1:1).
func (c *gameboyCore) AspectRatio() float64 { return 1.1111 }

// FPS mirrors the shell's frame rate: 4194304 dots per second
// over 70224 dots per frame.
func (c *gameboyCore) FPS() float64 { return 59.7275 }

// SampleRate mirrors the shell, which declares the sound
// package's host output rate.
func (c *gameboyCore) SampleRate() float64 { return sound.SampleRate }

// Libretro joypad ids as bits of the mask SetInput receives
// (bit b = RETRO_DEVICE_ID_JOYPAD b), matching the joypad*
// constants in the shell.
const (
	lrJoypadB      = 1 << 0
	lrJoypadY      = 1 << 1
	lrJoypadSelect = 1 << 2
	lrJoypadStart  = 1 << 3
	lrJoypadUp     = 1 << 4
	lrJoypadDown   = 1 << 5
	lrJoypadLeft   = 1 << 6
	lrJoypadRight  = 1 << 7
	lrJoypadA      = 1 << 8
	lrJoypadX      = 1 << 9
)

// SetInput translates a libretro-order mask into the engine's
// button bits, replicating the shell's pollPad: the Game Boy
// has four buttons, and the shell maps libretro A and X to the
// console A, B and Y to the console B (the usual handheld map).
func (c *gameboyCore) SetInput(player int, libretroMask uint16) {
	if player < 0 || player > 1 {
		return
	}
	var m uint16
	if libretroMask&lrJoypadRight != 0 {
		m |= gbsystem.ButtonRight
	}
	if libretroMask&lrJoypadLeft != 0 {
		m |= gbsystem.ButtonLeft
	}
	if libretroMask&lrJoypadUp != 0 {
		m |= gbsystem.ButtonUp
	}
	if libretroMask&lrJoypadDown != 0 {
		m |= gbsystem.ButtonDown
	}
	if libretroMask&(lrJoypadA|lrJoypadX) != 0 {
		m |= gbsystem.ButtonA
	}
	if libretroMask&(lrJoypadB|lrJoypadY) != 0 {
		m |= gbsystem.ButtonB
	}
	if libretroMask&lrJoypadSelect != 0 {
		m |= gbsystem.ButtonSelect
	}
	if libretroMask&lrJoypadStart != 0 {
		m |= gbsystem.ButtonStart
	}
	c.input[player] = m
}

// AudioDrain mirrors the shell, which hands the frontend
// everything sys.AudioBuffer returns; that call drains the
// samples produced since the previous one.
func (c *gameboyCore) AudioDrain() []int16 {
	if c.sys == nil {
		return nil
	}
	return c.sys.AudioBuffer()
}

// SaveState mirrors the shell's retro_serialize: the engine
// writes into a caller buffer sized by SerializeSize.
func (c *gameboyCore) SaveState() ([]byte, error) {
	if c.sys == nil {
		return nil, fmt.Errorf("no game loaded")
	}
	size := c.sys.SerializeSize()
	if size == 0 {
		return nil, fmt.Errorf("core does not support save states")
	}
	buf := make([]byte, size)
	if err := c.sys.Serialize(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *gameboyCore) LoadState(data []byte) error {
	if c.sys == nil {
		return fmt.Errorf("no game loaded")
	}
	return c.sys.Deserialize(data)
}

// Cheats: the shell's cheat hooks are no-ops (it exposes no
// cheat support yet), so the native adapter honestly reports
// none.
func (c *gameboyCore) CheatSupported() bool { return false }
func (c *gameboyCore) CheatReset()          {}
func (c *gameboyCore) CheatSet(int, string) {}
func (c *gameboyCore) Close()               { c.sys = nil }
