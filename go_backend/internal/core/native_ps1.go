package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	ps1system "github.com/davidpatton/itak-gaming/cores/go-ps1/system"
)

// Native adapter for the Go PlayStation core. Every
// constant and mapping mirrors
// cores/go-ps1/libretro/main.go, which stays the
// reference shell for this engine.

type ps1Core struct {
	sys   *ps1system.Machine
	input uint32 // translated pad mask, port 1 only
}

// ps1JoypadMap translates libretro joypad ids to the
// engine's pad bits, in libretro id order. It is the
// shell's joypadMap table (libretro/main.go) laid out
// by id: the standard libretro pad layout mapped
// onto the PlayStation pad.
var ps1JoypadMap = [16]uint32{
	ps1system.ButtonCross,  // 0: B
	ps1system.ButtonSquare, // 1: Y
	ps1system.ButtonSelect, // 2: Select
	ps1system.ButtonStart,  // 3: Start
	ps1system.ButtonUp,     // 4: Up
	ps1system.ButtonDown,   // 5: Down
	ps1system.ButtonLeft,   // 6: Left
	ps1system.ButtonRight,  // 7: Right
	ps1system.ButtonCircle, // 8: A
	ps1system.ButtonTri,    // 9: X
	ps1system.ButtonL1,     // 10: L
	ps1system.ButtonR1,     // 11: R
	ps1system.ButtonL2,     // 12: L2
	ps1system.ButtonR2,     // 13: R2
	ps1system.ButtonL3,     // 14: L3
	ps1system.ButtonR3,     // 15: R3
}

// LoadGame dispatches exactly like the shell's
// loadPath: the content kind is decided by the path
// extension. Discs (.cue, .bin, .iso, .img) open by
// path through the cd image layer and boot through
// SYSTEM.CNF; a .exe boots directly from its bytes;
// anything else is accepted only when the data
// carries the PS-X EXE magic. The new machine is
// installed only after a successful load, as in the
// shell.
func (c *ps1Core) LoadGame(data []byte, path string) error {
	m := ps1system.New()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe":
		if len(data) == 0 {
			b, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("cannot read %s: %w", path, err)
			}
			data = b
		}
		if err := m.LoadEXE(data); err != nil {
			return fmt.Errorf("bad PS-X EXE: %w", err)
		}
	case ".cue", ".bin", ".iso", ".img":
		if err := m.OpenDisc(path); err != nil {
			return fmt.Errorf("cannot open disc image: %w", err)
		}
		if err := m.BootDisc(); err != nil {
			return fmt.Errorf("disc does not boot: %w", err)
		}
	default:
		// A data-only load with no usable path: a
		// PS-X EXE is recognizable by its magic.
		if len(data) < 8 || string(data[:8]) != "PS-X EXE" {
			return fmt.Errorf("unsupported content: %s", path)
		}
		if err := m.LoadEXE(data); err != nil {
			return fmt.Errorf("bad PS-X EXE: %w", err)
		}
	}
	// The shell always presents an analog pad (see
	// the shell's package comment), forced at load.
	m.SetAnalogMode(true)
	// The native contract carries no analog axes.
	// The shell converts an untouched frontend axis
	// (raw 0) through its axis() helper to 128,
	// centered, every frame; pin the sticks there.
	m.SetAnalog(128, 128, 128, 128)
	c.sys = m
	return nil
}

func (c *ps1Core) RunFrame() {
	if c.sys == nil {
		return
	}
	c.sys.SetButtons(c.input)
	c.sys.RunFrame()
}

// FrameSize reports the GPU's current display size.
// PlayStation games change resolution mid-game: the
// shell answers with a SET_GEOMETRY notice, the
// native bridge simply reads the live size on every
// call. The 320x240 fallback before a load mirrors
// the shell's retro_get_system_av_info default
// (declared base geometry 320x240, max 640x480).
func (c *ps1Core) FrameSize() (int, int) {
	if c.sys == nil {
		return 320, 240
	}
	return c.sys.DisplayWidth(), c.sys.DisplayHeight()
}

func (c *ps1Core) Frame() []byte {
	if c.sys == nil {
		return nil
	}
	w, h := c.sys.DisplayWidth(), c.sys.DisplayHeight()
	fb := c.sys.Framebuffer()
	if w <= 0 || h <= 0 || len(fb) < w*h {
		return nil
	}
	// The engine framebuffer is exactly the current
	// display size, tightly packed (the shell
	// presents it with pitch w*4).
	fb = fb[:w*h]
	return unsafe.Slice((*byte)(unsafe.Pointer(&fb[0])), len(fb)*4)
}

// PixelFormat: the shell requests XRGB8888 through
// SET_PIXEL_FORMAT and the engine framebuffer is
// XRGB8888.
func (c *ps1Core) PixelFormat() int { return 1 }

// AspectRatio mirrors the shell's declared 4:3.
func (c *ps1Core) AspectRatio() float64 { return 4.0 / 3.0 }

// FPS mirrors the shell's declared 59.94.
func (c *ps1Core) FPS() float64 { return 59.94 }

// SampleRate mirrors the shell's av info, which
// reports the engine's AudioSampleRate (44100).
func (c *ps1Core) SampleRate() float64 { return ps1system.AudioSampleRate }

// SetInput translates the libretro-order mask into
// the engine's pad bits through ps1JoypadMap. The
// machine has one pad port and the shell only ever
// polls libretro port 0, so player 1 input has no
// shell counterpart and is dropped.
func (c *ps1Core) SetInput(player int, libretroMask uint16) {
	if player != 0 {
		return
	}
	var mask uint32
	for id := 0; id < 16; id++ {
		if libretroMask&(1<<uint(id)) != 0 {
			mask |= ps1JoypadMap[id]
		}
	}
	c.input = mask
}

// AudioDrain: the engine's AudioBuffer drains every
// pending sample, the same call the shell presents
// per frame.
func (c *ps1Core) AudioDrain() []int16 {
	if c.sys == nil {
		return nil
	}
	return c.sys.AudioBuffer()
}

func (c *ps1Core) SaveState() ([]byte, error) {
	if c.sys == nil {
		return nil, fmt.Errorf("no game loaded")
	}
	st := c.sys.Serialize()
	if len(st) == 0 {
		return nil, fmt.Errorf("core does not support save states")
	}
	return st, nil
}

func (c *ps1Core) LoadState(data []byte) error {
	if c.sys == nil {
		return fmt.Errorf("no game loaded")
	}
	return c.sys.Deserialize(data)
}

// Cheats: the shell's retro_cheat_reset and
// retro_cheat_set are empty stubs, so the native
// adapter honestly reports none.
func (c *ps1Core) CheatSupported() bool { return false }
func (c *ps1Core) CheatReset()          {}
func (c *ps1Core) CheatSet(int, string) {}
func (c *ps1Core) Close()               { c.sys = nil }
