package core

import (
	"fmt"
	"unsafe"

	gbasystem "github.com/davidpatton/itak-gaming/cores/go-gba/system"
)

// Native adapter for the Go GBA core. Every constant and
// mapping mirrors cores/go-gba/libretro/main.go, which stays
// the reference shell for this engine.

// gbaDefaultAudioRate is the shell's defaultAudioRate: the
// SOUNDBIAS power-on sample rate, reported until a game is
// loaded (and the fallback when a rate reads as 0).
const gbaDefaultAudioRate = 32768

type gbaCore struct {
	sys   *gbasystem.Machine
	input uint32 // translated GBA button mask, single pad

	// declaredRate is the audio sample rate captured from
	// the machine at load time, exactly as the shell does:
	// the frontend consumes at this rate for the whole
	// session, even if the game later rewrites SOUNDBIAS.
	declaredRate int

	// Resampler state for the SOUNDBIAS rate conversion,
	// carried across frames as in the shell: leftover input
	// samples and the fractional read position within them.
	resBuf   []int16
	resPhase float64
}

func (c *gbaCore) LoadGame(data []byte, _ string) error {
	// The engine installs its own BIOS stub at power-on
	// (system power() runs bios.Install), so no external
	// BIOS file is needed or read here; the shell likewise
	// loads from the ROM bytes alone.
	if c.sys == nil {
		c.sys = gbasystem.New()
	}
	if err := c.sys.LoadCart(data); err != nil {
		return err
	}
	c.declaredRate = c.sys.AudioSampleRate()
	c.resBuf = c.resBuf[:0]
	c.resPhase = 0
	return nil
}

func (c *gbaCore) RunFrame() {
	if c.sys == nil || c.sys.Cart() == nil {
		return
	}
	// The shell polls input, sets the pad, then runs the
	// frame. Its save RAM cache sync has no native
	// counterpart: nothing outside the machine writes the
	// save contents here (the shell's cache exists only
	// for the libretro memory interface), and the save
	// device plus bus SRAM ride inside save states.
	c.sys.SetButtons(c.input)
	c.sys.RunFrame()
}

func (c *gbaCore) FrameSize() (int, int) {
	return gbasystem.Width, gbasystem.Height
}

func (c *gbaCore) Frame() []byte {
	if c.sys == nil {
		return nil
	}
	fb := c.sys.Framebuffer()
	if len(fb) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&fb[0])), len(fb)*4)
}

// PixelFormat: the GPU produces XRGB8888; the shell asks the
// frontend for that format at load.
func (c *gbaCore) PixelFormat() int { return 1 }

// AspectRatio mirrors the shell's gbaAspect (240x160, square
// pixels).
func (c *gbaCore) AspectRatio() float64 { return 1.5 }

// FPS mirrors the shell's gbaFPS: the master clock over the
// cycles in one frame.
func (c *gbaCore) FPS() float64 { return 16777216.0 / 280896.0 }

// SampleRate mirrors the shell's AV info: the rate declared
// at load time, with the power-on rate as fallback.
func (c *gbaCore) SampleRate() float64 {
	rate := c.declaredRate
	if rate == 0 {
		rate = gbaDefaultAudioRate
	}
	return float64(rate)
}

// SetInput translates a libretro-order mask into the GBA
// button mask, replicating the shell's pollPad: the GBA bits
// are GBATEK KEYINPUT order via the system Button constants.
// Libretro ids the shell never polls (Y, X, L2, R2, L3, R3)
// have no GBA counterpart and are dropped. The GBA has one
// pad; the shell polls port 0 only, so other ports are
// ignored.
func (c *gbaCore) SetInput(player int, libretroMask uint16) {
	if player != 0 {
		return
	}
	var m uint32
	if libretroMask&(1<<8) != 0 { // joypadA
		m |= gbasystem.ButtonA
	}
	if libretroMask&(1<<0) != 0 { // joypadB
		m |= gbasystem.ButtonB
	}
	if libretroMask&(1<<2) != 0 { // joypadSelect
		m |= gbasystem.ButtonSelect
	}
	if libretroMask&(1<<3) != 0 { // joypadStart
		m |= gbasystem.ButtonStart
	}
	if libretroMask&(1<<7) != 0 { // joypadRight
		m |= gbasystem.ButtonRight
	}
	if libretroMask&(1<<6) != 0 { // joypadLeft
		m |= gbasystem.ButtonLeft
	}
	if libretroMask&(1<<4) != 0 { // joypadUp
		m |= gbasystem.ButtonUp
	}
	if libretroMask&(1<<5) != 0 { // joypadDown
		m |= gbasystem.ButtonDown
	}
	if libretroMask&(1<<11) != 0 { // joypadR
		m |= gbasystem.ButtonR
	}
	if libretroMask&(1<<10) != 0 { // joypadL
		m |= gbasystem.ButtonL
	}
	c.input = m
}

// AudioDrain mirrors the shell's drainAudio: it returns the
// machine's pending samples at the declared rate. When the
// machine's live rate differs (a game rewrote SOUNDBIAS
// after load), the input is converted with the shell's
// linear interpolator over a carried buffer, so the
// conversion is continuous across calls.
func (c *gbaCore) AudioDrain() []int16 {
	if c.sys == nil {
		return nil
	}
	in := c.sys.AudioBuffer()
	live := c.sys.AudioSampleRate()
	if live == c.declaredRate || live == 0 || c.declaredRate == 0 {
		c.resBuf = c.resBuf[:0]
		c.resPhase = 0
		return in
	}
	c.resBuf = append(c.resBuf, in...)
	total := len(c.resBuf) / 2
	step := float64(live) / float64(c.declaredRate)
	var out []int16
	for c.resPhase+1 < float64(total) {
		i := int(c.resPhase)
		frac := c.resPhase - float64(i)
		l := gbaLerp(c.resBuf[2*i], c.resBuf[2*i+2], frac)
		r := gbaLerp(c.resBuf[2*i+1], c.resBuf[2*i+3], frac)
		out = append(out, l, r)
		c.resPhase += step
	}
	consumed := int(c.resPhase)
	if consumed > 0 {
		c.resBuf = append(c.resBuf[:0], c.resBuf[2*consumed:]...)
		c.resPhase -= float64(consumed)
	}
	return out
}

// gbaLerp is the shell's lerp for the rate conversion.
func gbaLerp(a, b int16, frac float64) int16 {
	return int16(float64(a) + (float64(b)-float64(a))*frac)
}

func (c *gbaCore) SaveState() ([]byte, error) {
	if c.sys == nil || c.sys.Cart() == nil {
		return nil, fmt.Errorf("no game loaded")
	}
	st := c.sys.Serialize()
	if len(st) == 0 {
		return nil, fmt.Errorf("core does not support save states")
	}
	return st, nil
}

func (c *gbaCore) LoadState(data []byte) error {
	if c.sys == nil || c.sys.Cart() == nil {
		return fmt.Errorf("no game loaded")
	}
	// Deserialize itself demands exactly StateSize bytes
	// and the current state version, as in the shell path.
	return c.sys.Deserialize(data)
}

// Cheats: the shell's cheat hooks are empty no-ops, so the
// native adapter honestly reports none.
func (c *gbaCore) CheatSupported() bool { return false }
func (c *gbaCore) CheatReset()          {}
func (c *gbaCore) CheatSet(int, string) {}

// Close mirrors the shell's unload: a fresh empty machine
// state, with the declared rate back at the power-on rate.
func (c *gbaCore) Close() {
	c.sys = nil
	c.declaredRate = gbaDefaultAudioRate
	c.resBuf = nil
	c.resPhase = 0
}
