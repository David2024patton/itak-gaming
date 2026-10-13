// Package system wires the VR4300 CPU, the RSP, the
// RDP/VI/AI output devices, the physical memory bus,
// the PIF, and a parsed cartridge into the machine the
// libretro shell drives. Slice N64-2 adds the RSP: it
// executes from IMEM under the interleave model
// described on RunCycles. Slice N64-3 adds the RDP
// rasterizer, the VI presentation, and AI audio output.
// Slice N64-4 adds the PIF with controllers and paks,
// the cartridge save devices, the real SI DMA, and the
// HLE boot (see boot.go): Start boots a cartridge
// through its own IPL3. Bare code loaded directly into
// memory still runs without booting, which the
// low-level test harness uses.
package system

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-n64/ai"
	"github.com/davidpatton/itak-gaming/cores/go-n64/bus"
	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/rsp"
	"github.com/davidpatton/itak-gaming/cores/go-n64/pif"
	"github.com/davidpatton/itak-gaming/cores/go-n64/vi"
)

// Frame geometry and timing (NTSC): the VR4300 runs at
// 93.75 MHz; a 60 Hz frame budget is 1,562,500 cycles.
const (
	Width          = 320
	Height         = 240
	CyclesPerFrame = 93750000 / 60
)

// Controller button bits in the standard N64 controller
// data layout (the order the PIF reports them).
const (
	ButtonA      = 1 << 15
	ButtonB      = 1 << 14
	ButtonZ      = 1 << 13
	ButtonStart  = 1 << 12
	ButtonDUp    = 1 << 11
	ButtonDDown  = 1 << 10
	ButtonDLeft  = 1 << 9
	ButtonDRight = 1 << 8
	ButtonL      = 1 << 5
	ButtonR      = 1 << 4
	ButtonCUp    = 1 << 3
	ButtonCDown  = 1 << 2
	ButtonCLeft  = 1 << 1
	ButtonCRight = 1 << 0
)

// System is the N64 machine.
type System struct {
	CPU  *r4300.CPU
	RSP  *rsp.RSP
	Mem  *bus.Memory
	Cart *cart.Cart

	expansion bool
	pads      [4]padState
	fb        []uint32
	rspFrac   int // leftover RSP budget numerator (of 3 per CPU cycle)

	cicOverride    cart.CIC
	cicSet         bool
	bootCIC        cart.CIC
	bootCRC1       uint32
	bootCRC2       uint32
	bootChecksumOK bool
	booted         bool
}

// padState is one controller port's host-side input.
type padState struct {
	buttons uint16
	stickX  int8
	stickY  int8
}

// New builds a machine around a raw cartridge dump.
func New(rom []byte) (*System, error) {
	return NewWithOptions(rom, false)
}

// NewWithOptions builds a machine; expansion selects the
// 8 MB Expansion Pak memory size instead of the stock 4 MB.
func NewWithOptions(rom []byte, expansion bool) (*System, error) {
	s := &System{expansion: expansion, fb: make([]uint32, Width*Height)}
	for i := range s.fb {
		s.fb[i] = 0xFF000000 // opaque black
	}
	if err := s.LoadROM(rom); err != nil {
		return nil, err
	}
	return s, nil
}

// LoadROM parses a cartridge dump and resets the machine
// around it. A Controller Pak starts inserted in port 1
// (the common case; games that use one work without any
// host setup). The save device starts as none: select
// one with SetSaveType.
func (s *System) LoadROM(rom []byte) error {
	c, err := cart.Parse(rom)
	if err != nil {
		return err
	}
	s.Cart = c
	s.Mem = bus.New(c.ROM, s.expansion)
	s.RSP = s.Mem.RSP
	s.CPU = r4300.New(s.Mem)
	s.Mem.OnMIChange = func(active bool) {
		s.CPU.SetInterruptLine(active)
	}
	s.Mem.PIF.InsertPak(0, pif.PakMemory)
	s.pads = [4]padState{}
	s.booted = false
	s.Reset()
	return nil
}

// Reset returns the machine to its power-on state: the
// CPU resets and the RDP, VI, and AI are rebuilt fresh.
// Reset does not boot the cartridge; Start does. Test
// code that seeds PC and memory directly uses Reset's
// state, as it did before the boot existed.
func (s *System) Reset() {
	s.Mem.ResetDevices()
	s.CPU.Reset()
	s.booted = false
}

// RunCycles executes the CPU for at least the given
// number of flat cycles, interleaving the RSP. The RSP
// runs at 62.5 MHz against the CPU's 93.75 MHz, a 2:3
// ratio: after every CPU step the RSP is credited 2
// cycles per 3 CPU cycles (the fractional remainder is
// carried exactly) and runs that many instructions,
// stopping early when halted. A CPU write that sets the
// halt flag therefore stops the RSP at the next system
// step boundary.
func (s *System) RunCycles(cycles int) {
	n := 0
	for n < cycles {
		c := s.CPU.Step()
		n += c
		s.rspFrac += 2 * c
		steps := s.rspFrac / 3
		s.rspFrac -= steps * 3
		if steps > 0 {
			s.RSP.Run(steps)
		}
	}
	// The VI and AI advance on the cycles actually
	// consumed: device timing granularity is the
	// RunCycles call.
	s.Mem.VI.Advance(n)
	s.Mem.AI.Advance(n)
}

// RunFrame executes one 60 Hz frame budget of CPU cycles.
func (s *System) RunFrame() {
	s.RunCycles(CyclesPerFrame)
}

// Framebuffer returns the current video output: the VI
// presentation of the programmed color image when the
// VI is on, otherwise the blank 320x240 buffer.
func (s *System) Framebuffer() []uint32 {
	if fb, _, _ := s.Mem.VI.Present(s.Mem.RDRAM); fb != nil {
		return fb
	}
	return s.fb
}

// FramebufferSize reports the VI output geometry when
// the VI is presenting (width, height), or the blank
// buffer's 320x240 when it is not.
func (s *System) FramebufferSize() (int, int) {
	if _, w, h := s.Mem.VI.Present(s.Mem.RDRAM); w > 0 {
		return w, h
	}
	return Width, Height
}

// AudioBuffer drains the AI's produced samples:
// interleaved signed 16-bit stereo at AudioRate.
func (s *System) AudioBuffer() []int16 { return s.Mem.AI.Drain() }

// AudioRate reports the AI's current DAC sample rate in
// Hz, from the programmed DACRATE register.
func (s *System) AudioRate() int { return s.Mem.AI.SampleRate() }

// SetButtons records player 1's controller state. The
// state reaches games through the PIF on their next
// Joybus poll.
func (s *System) SetButtons(mask uint16) { s.SetButtonsPort(0, mask) }

// SetButtonsPort records one port's button state
// (port 0 is player 1).
func (s *System) SetButtonsPort(port int, mask uint16) {
	if port < 0 || port > 3 {
		return
	}
	s.pads[port].buttons = mask
	s.pushPad(port)
}

// SetStick records one port's analog stick position
// (each axis in the -80..80 hardware range; out of
// range values are clamped).
func (s *System) SetStick(port int, x, y int8) {
	if port < 0 || port > 3 {
		return
	}
	s.pads[port].stickX = clampStick(x)
	s.pads[port].stickY = clampStick(y)
	s.pushPad(port)
}

func clampStick(v int8) int8 {
	if v > 80 {
		return 80
	}
	if v < -80 {
		return -80
	}
	return v
}

func (s *System) pushPad(port int) {
	p := s.pads[port]
	s.Mem.PIF.SetController(port, p.buttons, p.stickX, p.stickY)
}

// Buttons returns player 1's recorded controller state.
func (s *System) Buttons() uint16 { return s.pads[0].buttons }

// InsertPak puts an accessory (memory pak or rumble
// pak) into a controller port; pif.PakNone removes it.
func (s *System) InsertPak(port int, kind pif.PakKind) {
	s.Mem.PIF.InsertPak(port, kind)
}

// SetSaveType selects the cartridge save device
// (EEPROM 4K/16K, SRAM, or Flash). Cartridges do not
// declare a save device in their headers and this core
// keeps no database, so the host picks: the libretro
// shell exposes it as the gon64_save_type core option
// and tests set it directly. Switching discards the
// previous device's contents.
func (s *System) SetSaveType(t cart.SaveType) { s.Mem.Saves.Configure(t) }

// SaveType returns the selected save device.
func (s *System) SaveType() cart.SaveType { return s.Mem.Saves.Type }

// SaveRAM returns the active save device's contents
// (the libretro SAVE_RAM image), or nil when no device
// is fitted.
func (s *System) SaveRAM() []byte { return s.Mem.Saves.Data() }

// SetSaveRAM loads a host save image into the active
// save device.
func (s *System) SetSaveRAM(b []byte) { s.Mem.Saves.SetData(b) }

// Title returns the cartridge name from its header.
func (s *System) Title() string {
	if s.Cart == nil {
		return ""
	}
	return s.Cart.Name
}

const stateVersion = 4

// SerializeSize reports the byte size of a serialized
// system for this machine's RDRAM configuration.
func (s *System) SerializeSize() int {
	return 2 + r4300.SerializeSize + len(s.Mem.RDRAM) + rsp.SerializeSize + 8 + 8 +
		s.Mem.DP.SerializeSize() + vi.SerializeSize + ai.SerializeSize +
		8 + pif.RAMSize + pif.StateSize + cart.SaveStateSize
}

// Serialize writes the full machine state: version byte,
// option flags, the CPU block, RDRAM, the RSP block, the
// MI interrupt and mask registers, the RSP interleave
// remainder, the RDP, VI, and AI blocks, and (version
// 4) the SI registers, PIF RAM, the PIF runtime block
// (pak storage and accessory flags), and the cartridge
// save device block.
func (s *System) Serialize() []byte {
	out := make([]byte, s.SerializeSize())
	out[0] = stateVersion
	if s.expansion {
		out[1] = 1
	}
	n := s.CPU.Serialize(out[2:])
	off := 2 + n
	copy(out[off:], s.Mem.RDRAM)
	off += len(s.Mem.RDRAM)
	off += s.RSP.Serialize(out[off:])
	intr, mask := s.Mem.MIState()
	binary.BigEndian.PutUint32(out[off:], intr)
	binary.BigEndian.PutUint32(out[off+4:], mask)
	off += 8
	binary.BigEndian.PutUint64(out[off:], uint64(int64(s.rspFrac)))
	off += 8
	off += s.Mem.DP.Serialize(out[off:])
	off += s.Mem.VI.Serialize(out[off:])
	off += s.Mem.AI.Serialize(out[off:])
	dramAddr, status := s.Mem.SIState()
	binary.BigEndian.PutUint32(out[off:], dramAddr)
	binary.BigEndian.PutUint32(out[off+4:], status)
	off += 8
	copy(out[off:], s.Mem.PIF.RAM[:])
	off += pif.RAMSize
	s.Mem.PIF.Serialize(out[off:])
	off += pif.StateSize
	s.Mem.Saves.Serialize(out[off:])
	return out
}

// Deserialize restores a state written by Serialize.
func (s *System) Deserialize(in []byte) error {
	if len(in) != s.SerializeSize() {
		return fmt.Errorf("system: state size %d, want %d", len(in), s.SerializeSize())
	}
	if in[0] != stateVersion {
		return fmt.Errorf("system: state version %d", in[0])
	}
	if (in[1] == 1) != s.expansion {
		return errors.New("system: Expansion Pak configuration mismatch")
	}
	if err := s.CPU.Deserialize(in[2:]); err != nil {
		return err
	}
	off := 2 + r4300.SerializeSize
	copy(s.Mem.RDRAM, in[off:])
	off += len(s.Mem.RDRAM)
	if err := s.RSP.Deserialize(in[off:]); err != nil {
		return err
	}
	off += rsp.SerializeSize
	intr := binary.BigEndian.Uint32(in[off:])
	mask := binary.BigEndian.Uint32(in[off+4:])
	s.Mem.RestoreMI(intr, mask)
	off += 8
	s.rspFrac = int(int64(binary.BigEndian.Uint64(in[off:])))
	off += 8
	if err := s.Mem.DP.Deserialize(in[off:]); err != nil {
		return err
	}
	off += s.Mem.DP.SerializeSize()
	if err := s.Mem.VI.Deserialize(in[off:]); err != nil {
		return err
	}
	off += vi.SerializeSize
	if err := s.Mem.AI.Deserialize(in[off:]); err != nil {
		return err
	}
	off += ai.SerializeSize
	dramAddr := binary.BigEndian.Uint32(in[off:])
	siStatus := binary.BigEndian.Uint32(in[off+4:])
	s.Mem.RestoreSI(dramAddr, siStatus)
	off += 8
	copy(s.Mem.PIF.RAM[:], in[off:])
	off += pif.RAMSize
	s.Mem.PIF.Deserialize(in[off:])
	off += pif.StateSize
	s.Mem.Saves.Deserialize(in[off:])
	s.CPU.SetInterruptLine(s.Mem.MIInterrupt())
	return nil
}
