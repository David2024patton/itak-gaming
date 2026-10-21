package core

import (
	"fmt"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-snes/ppu"
	snessystem "github.com/davidpatton/itak-gaming/cores/go-snes/system"
)

// Native adapter for the Go SNES core. Every constant and
// mapping mirrors cores/go-snes/libretro/main.go, which stays
// the reference shell for this engine.

type snesCore struct {
	sys   *snessystem.System
	input [2]uint16
}

func (c *snesCore) LoadGame(data []byte, _ string) error {
	s, err := snessystem.New(data)
	if err != nil {
		return err
	}
	c.sys = s
	return nil
}

func (c *snesCore) RunFrame() {
	if c.sys == nil {
		return
	}
	c.sys.SetButtons(0, c.input[0])
	c.sys.SetButtons(1, c.input[1])
	c.sys.RunFrame()
}

func (c *snesCore) FrameSize() (int, int) { return ppu.Width, ppu.Height }

func (c *snesCore) Frame() []byte {
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
func (c *snesCore) PixelFormat() int { return 1 }

// AspectRatio mirrors the shell's declared 1.33333.
func (c *snesCore) AspectRatio() float64 { return 1.33333 }

// FPS reports the loaded game's real frame rate: the
// core derives it from the cartridge region (about
// 60.0988 for an NTSC game, about 50.007 for a PAL game),
// so a PAL game is presented at PAL speed. With no game
// loaded it reports the NTSC rate, as it always has.
func (c *snesCore) FPS() float64 {
	if c.sys == nil {
		return 60.0988
	}
	return c.sys.FPS()
}

// SampleRate mirrors the shell's DSP output rate.
func (c *snesCore) SampleRate() float64 { return 32000 }

// SetInput: the engine's button bit order matches the
// libretro joypad order 1:1 (see the shell's pollPad).
func (c *snesCore) SetInput(player int, libretroMask uint16) {
	if player < 0 || player > 1 {
		return
	}
	c.input[player] = libretroMask
}

func (c *snesCore) AudioDrain() []int16 {
	if c.sys == nil {
		return nil
	}
	return c.sys.AudioBuffer()
}

func (c *snesCore) SaveState() ([]byte, error) {
	if c.sys == nil {
		return nil, fmt.Errorf("no game loaded")
	}
	st := c.sys.Serialize()
	if len(st) == 0 {
		return nil, fmt.Errorf("core does not support save states")
	}
	return st, nil
}

func (c *snesCore) LoadState(data []byte) error {
	if c.sys == nil {
		return fmt.Errorf("no game loaded")
	}
	return c.sys.Deserialize(data)
}

// Cheats: the shell exposes no cheat hooks yet, so the native
// adapter honestly reports none.
func (c *snesCore) CheatSupported() bool { return false }
func (c *snesCore) CheatReset()          {}
func (c *snesCore) CheatSet(int, string) {}
func (c *snesCore) Close()               { c.sys = nil }
