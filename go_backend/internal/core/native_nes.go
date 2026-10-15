package core

import (
	"fmt"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-nes/ppu"
	nessystem "github.com/davidpatton/itak-gaming/cores/go-nes/system"
)

// Native adapter for the Go NES core. Every constant and
// mapping mirrors cores/go-nes/libretro/main.go, which stays
// the reference shell for this engine.

type nesCore struct {
	sys   *nessystem.System
	input [2]uint8
}

func (c *nesCore) LoadGame(data []byte, _ string) error {
	s, err := nessystem.New(data)
	if err != nil {
		return err
	}
	c.sys = s
	return nil
}

func (c *nesCore) RunFrame() {
	if c.sys == nil {
		return
	}
	c.sys.SetButtons(0, c.input[0])
	c.sys.SetButtons(1, c.input[1])
	c.sys.RunFrame()
}

func (c *nesCore) FrameSize() (int, int) { return ppu.Width, ppu.Height }

func (c *nesCore) Frame() []byte {
	if c.sys == nil {
		return nil
	}
	fb := c.sys.Framebuffer()
	if len(fb) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&fb[0])), len(fb)*4)
}

// PixelFormat: the PPU produces XRGB8888 (the format the
// shell requests from the frontend in retro_load_game).
func (c *nesCore) PixelFormat() int { return 1 }

// AspectRatio mirrors the shell's declared 1.33333.
func (c *nesCore) AspectRatio() float64 { return 1.33333 }

// FPS mirrors the shell's NTSC frame rate.
func (c *nesCore) FPS() float64 { return 60.0988 }

// SampleRate mirrors the shell's timing report: the APU
// renders at sound.SampleRate, 44100 Hz.
func (c *nesCore) SampleRate() float64 { return 44100 }

// SetInput translates a libretro-order mask into the NES
// button mask, replicating the shell's pollPad: libretro A
// (bit 8) or X (bit 9) is the console A, libretro B (bit 0)
// or Y (bit 1) is the console B, and Select, Start and the
// d-pad map straight across. The result uses the shift
// register order SetButtons expects: A, B, Select, Start,
// Up, Down, Left, Right in bits 0 to 7. Libretro L, R and
// the remaining buttons have no NES counterpart and are
// dropped, exactly as the shell drops them.
func (c *nesCore) SetInput(player int, libretroMask uint16) {
	if player < 0 || player > 1 {
		return
	}
	var m uint8
	if libretroMask&(1<<8) != 0 || libretroMask&(1<<9) != 0 {
		m |= 1 << 0 // console A
	}
	if libretroMask&(1<<0) != 0 || libretroMask&(1<<1) != 0 {
		m |= 1 << 1 // console B
	}
	if libretroMask&(1<<2) != 0 {
		m |= 1 << 2 // Select
	}
	if libretroMask&(1<<3) != 0 {
		m |= 1 << 3 // Start
	}
	if libretroMask&(1<<4) != 0 {
		m |= 1 << 4 // Up
	}
	if libretroMask&(1<<5) != 0 {
		m |= 1 << 5 // Down
	}
	if libretroMask&(1<<6) != 0 {
		m |= 1 << 6 // Left
	}
	if libretroMask&(1<<7) != 0 {
		m |= 1 << 7 // Right
	}
	c.input[player] = m
}

// AudioDrain: the engine's AudioBuffer hands over every
// pending sample and clears its buffer (sound.APU.AudioBuffer
// in the go-nes module), which is the drain the contract
// asks for.
func (c *nesCore) AudioDrain() []int16 {
	if c.sys == nil {
		return nil
	}
	return c.sys.AudioBuffer()
}

func (c *nesCore) SaveState() ([]byte, error) {
	if c.sys == nil {
		return nil, fmt.Errorf("no game loaded")
	}
	st := c.sys.Serialize()
	if len(st) == 0 {
		return nil, fmt.Errorf("core does not support save states")
	}
	return st, nil
}

func (c *nesCore) LoadState(data []byte) error {
	if c.sys == nil {
		return fmt.Errorf("no game loaded")
	}
	return c.sys.Deserialize(data)
}

// Cheats: the shell's retro_cheat_reset and retro_cheat_set
// are empty no-ops (its comment: the core does not expose
// cheat hooks yet), so the native adapter honestly reports
// none.
func (c *nesCore) CheatSupported() bool { return false }
func (c *nesCore) CheatReset()          {}
func (c *nesCore) CheatSet(int, string) {}
func (c *nesCore) Close()               { c.sys = nil }
