package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/bus"
	gensystem "github.com/davidpatton/itak-gaming/cores/go-genesis/system"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/vdp"
)

// Native adapter for the Go Genesis core, cartridge and Sega CD
// in one adapter, matching the one .so the shell builds. Every
// constant and mapping mirrors cores/go-genesis/libretro/main.go
// and libretro/disc.go, which stay the reference shell for this
// engine.

type genesisCore struct {
	sys     *gensystem.System
	input   [2]uint16 // translated to bus.Pad* layout by SetInput
	packed  []uint16  // frame repacked to the display width
	tempISO string    // routeDiscData temp file, as in the shell

	// ranFrames mirrors the shell's flag of the same name: the
	// VDP powers on in H32 mode, but most games switch to H40
	// during boot, so before the first frame the honest
	// reported geometry is the 320 wide default.
	ranFrames bool
}

// discSignature is the boot signature at the start of every
// Sega CD disc's data area (libretro/disc.go).
const discSignature = "SEGADISCSYSTEM"

// sniffDisc reports whether data looks like a Sega CD disc
// image, copied from libretro/disc.go: a 2048 byte sector image
// carries the signature at offset 0; a raw 2352 byte sector
// image carries the twelve byte sync pattern first (0x00, ten
// 0xFF bytes, 0x00) then a four byte header, so the signature
// lands at offset 16. A cartridge, which carries "SEGA" at
// offset 0x100 instead, returns false.
func sniffDisc(data []byte) bool {
	if len(data) >= len(discSignature) && string(data[:len(discSignature)]) == discSignature {
		return true
	}
	if len(data) < 16+len(discSignature) {
		return false
	}
	if data[0] != 0x00 || data[11] != 0x00 {
		return false
	}
	for i := 1; i <= 10; i++ {
		if data[i] != 0xFF {
			return false
		}
	}
	return string(data[16:16+len(discSignature)]) == discSignature
}

// Load routes, the same three the shell's routeFor picks
// between (libretro/disc.go).
const (
	genesisRouteCartridge = iota
	genesisRouteDiscPath
	genesisRouteDiscData
)

// routeFor picks the loader for one load, copied from
// libretro/disc.go: a .cue or .iso path always wins (the cue
// sheet is not the disc data, and an ISO is a disc by
// definition); otherwise the data decides, a disc signature
// means a disc, loaded from its path when it has one and from
// the bytes themselves when it does not; anything else is a
// cartridge.
func genesisRouteFor(path string, data []byte) int {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue", ".iso":
		return genesisRouteDiscPath
	}
	if sniffDisc(data) {
		if path != "" {
			return genesisRouteDiscPath
		}
		return genesisRouteDiscData
	}
	return genesisRouteCartridge
}

func (c *genesisCore) LoadGame(data []byte, path string) error {
	if c.sys == nil {
		c.sys = gensystem.New()
	}
	switch genesisRouteFor(path, data) {
	case genesisRouteDiscPath:
		// Disc loads work on files: LoadDisc opens the .cue,
		// .iso or .bin at path (scd.OpenDisc) and, for a CD
		// only boot, powers the machine on around the
		// project's own main stub program (system.LoadDisc via
		// scd.MainStubROM). No Sega BIOS file is read at load
		// time or later; the BIOS service layer is HLE inside
		// the scd package.
		return c.sys.LoadDisc(path)
	case genesisRouteDiscData:
		// LoadDisc works on files, so park the bytes in a temp
		// ISO first, exactly as the shell does. Close deletes
		// it, mirroring retro_unload_game.
		f, err := os.CreateTemp("", "itak-disc-*.iso")
		if err != nil {
			return err
		}
		tmpPath := f.Name()
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return err
		}
		if err := f.Close(); err != nil {
			os.Remove(tmpPath)
			return err
		}
		if err := c.sys.LoadDisc(tmpPath); err != nil {
			os.Remove(tmpPath)
			return err
		}
		c.tempISO = tmpPath
		return nil
	default:
		return c.sys.LoadGame(data)
	}
}

func (c *genesisCore) RunFrame() {
	if c.sys == nil || !c.sys.Loaded() {
		return
	}
	c.ranFrames = true
	c.sys.SetButtons(0, c.input[0])
	c.sys.SetButtons(1, c.input[1])
	c.sys.RunFrame()
}

// FrameSize mirrors retro_get_system_av_info: 320 by 224 until
// a game is loaded and a frame has run, then the VDP's live
// display size (256 or 320 wide, 224 or 240 tall; CD games run
// through the same VDP, so disc mode changes nothing here).
func (c *genesisCore) FrameSize() (int, int) {
	if c.sys != nil && c.sys.Loaded() && c.ranFrames {
		return c.sys.Width(), c.sys.Height()
	}
	return 320, 224
}

// Frame repacks the VDP frame. The engine framebuffer is
// always vdp.MaxWidth pixels wide (system.Framebuffer, "the
// system crops each line to DisplayWidth when presenting");
// the shell presents it with a MaxWidth pitch, while the
// NativeCore contract wants a tightly packed frame, so each
// line is cropped to the display width here.
func (c *genesisCore) Frame() []byte {
	if c.sys == nil || !c.sys.Loaded() {
		return nil
	}
	fb := c.sys.Framebuffer()
	if len(fb) == 0 {
		return nil
	}
	w, h := c.FrameSize()
	if len(c.packed) != w*h {
		c.packed = make([]uint16, w*h)
	}
	for y := 0; y < h; y++ {
		row := fb[y*vdp.MaxWidth:]
		copy(c.packed[y*w:(y+1)*w], row[:w])
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&c.packed[0])), len(c.packed)*2)
}

// PixelFormat: the VDP produces RGB565 (the shell asks the
// frontend for RGB565 in retro_load_game).
func (c *genesisCore) PixelFormat() int { return 2 }

// AspectRatio mirrors the shell's pixel aspect corrected
// constants ("matches Genesis Plus GX"): 1.3061 for H40, and
// 1.2152 once a loaded game has run a frame in H32 (width
// 256). The default applies before that, as in
// retro_get_system_av_info.
func (c *genesisCore) AspectRatio() float64 {
	if c.sys != nil && c.sys.Loaded() && c.ranFrames && c.sys.Width() == 256 {
		return 1.2152
	}
	return 1.3061
}

// FPS mirrors the shell's genesisFPS constant.
func (c *genesisCore) FPS() float64 { return 59.9227 }

// SampleRate mirrors the shell's sampleRate constant.
func (c *genesisCore) SampleRate() float64 { return 44100 }

// SetInput translates the libretro-order mask into the bus
// pad layout, replicating the shell's pollPad: the 3-button
// Genesis pad mapping (same as Genesis Plus GX defaults),
// libretro Y = Genesis A, B = B, A = C, Select = Mode, Start =
// Start. The shell's libretro ids are B 0, Y 1, Select 2,
// Start 3, Up 4, Down 5, Left 6, Right 7, A 8; the bus bits
// are the bus.Pad* constants.
func (c *genesisCore) SetInput(player int, libretroMask uint16) {
	if player < 0 || player > 1 {
		return
	}
	var m uint16
	if libretroMask&(1<<4) != 0 { // Up
		m |= bus.PadUp
	}
	if libretroMask&(1<<5) != 0 { // Down
		m |= bus.PadDown
	}
	if libretroMask&(1<<6) != 0 { // Left
		m |= bus.PadLeft
	}
	if libretroMask&(1<<7) != 0 { // Right
		m |= bus.PadRight
	}
	if libretroMask&(1<<1) != 0 { // Y
		m |= bus.PadA
	}
	if libretroMask&(1<<0) != 0 { // B
		m |= bus.PadB
	}
	if libretroMask&(1<<8) != 0 { // A
		m |= bus.PadC
	}
	if libretroMask&(1<<3) != 0 { // Start
		m |= bus.PadStart
	}
	if libretroMask&(1<<2) != 0 { // Select
		m |= bus.PadMode
	}
	c.input[player] = m
}

// AudioDrain: system.AudioBuffer drains every sample the
// machine has produced and not yet handed out (cartridge
// sound plus, in CD mode, CD-DA and PCM mixed by the same
// system), which is exactly the drain all contract.
func (c *genesisCore) AudioDrain() []int16 {
	if c.sys == nil {
		return nil
	}
	return c.sys.AudioBuffer()
}

// SaveState serves both modes through the one System
// serializer: SerializeSize grows when a Sega CD unit is
// attached (the CD section is appended), and Serialize fills
// a buffer of that size, exactly the shell's retro_serialize
// path.
func (c *genesisCore) SaveState() ([]byte, error) {
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

// LoadState mirrors retro_unserialize: the state is taken at
// the current machine's SerializeSize (extra bytes are cut,
// as the shell slices the frontend buffer), and Deserialize
// itself handles the rest, including its legacy CD state
// expansion and its refusal of a CD state in cartridge mode
// or the reverse.
func (c *genesisCore) LoadState(data []byte) error {
	if c.sys == nil {
		return fmt.Errorf("no game loaded")
	}
	size := c.sys.SerializeSize()
	if size == 0 {
		return fmt.Errorf("no game loaded")
	}
	if len(data) > size {
		data = data[:size]
	}
	return c.sys.Deserialize(data)
}

// Cheats: the shell's retro_cheat_reset and retro_cheat_set
// are empty hooks ("this core does not expose cheat hooks
// yet"), so the native adapter honestly reports none.
func (c *genesisCore) CheatSupported() bool { return false }
func (c *genesisCore) CheatReset()          {}
func (c *genesisCore) CheatSet(int, string) {}

// Close mirrors retro_unload_game: delete the temp ISO if a
// disc was loaded from bytes, and reset to a fresh, unloaded
// system.
func (c *genesisCore) Close() {
	if c.tempISO != "" {
		os.Remove(c.tempISO)
		c.tempISO = ""
	}
	c.sys = gensystem.New()
	c.ranFrames = false
}
