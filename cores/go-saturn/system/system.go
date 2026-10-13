// Package system is the whole Sega Saturn machine skeleton
// for SAT-1: two SH-2 CPUs (master and slave) on the shared
// bus from the bus package, the CD image foundations from
// the cd package, a blank framebuffer, the standard pad
// state, and deterministic save states.
//
// Slice boundaries, stated plainly: there is no BIOS in
// this core and none is loaded here. Reset parks the
// master CPU at address 0 with the conventional stack
// pointer; real game boot through the documented HLE
// path (read the disc IP header, load the first read
// file, jump to its entry) arrives in SAT-4. The slave
// CPU starts held, matching the power on state software
// observes, and is released explicitly through
// ReleaseSlave. VDP1, VDP2, SCSP, SMPC command handling,
// and the SCU are later slices; their register windows
// and memories already exist on the bus as storage.
package system

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/bus"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/chips/sh2"
)

// Width and Height are the framebuffer dimensions for
// the standard NTSC display mode used by this skeleton.
const (
	Width  = 320
	Height = 224
)

// CyclesPerFrame is the per CPU cycle budget of one
// frame at 28.6364 MHz and 60 frames per second,
// truncated to a whole cycle (477273 of 477273.33).
const CyclesPerFrame = 477273

// Standard Saturn digital pad buttons, in the bit order
// used by SetButtons. The peripheral ID convention for
// the standard digital pad is 0x02 (see PadID).
const (
	ButtonUp    = 1 << 0
	ButtonDown  = 1 << 1
	ButtonLeft  = 1 << 2
	ButtonRight = 1 << 3
	ButtonA     = 1 << 4
	ButtonB     = 1 << 5
	ButtonC     = 1 << 6
	ButtonX     = 1 << 7
	ButtonY     = 1 << 8
	ButtonZ     = 1 << 9
	ButtonL     = 1 << 10
	ButtonR     = 1 << 11
	ButtonStart = 1 << 12
)

// PadID is the Saturn peripheral ID byte for the standard
// digital pad. SMPC peripheral polling lands in SAT-3;
// the ID is recorded here as part of the pad state.
const PadID = 0x02

// Save state layout (all integers big endian):
//
//	byte 0: version (1)
//	byte 1: flags, bit 0 set when the slave is released
//	bytes 2..3: button mask
//	then the master CPU state (sh2.SerializeSize bytes)
//	then the slave CPU state (sh2.SerializeSize bytes)
//	then Work RAM-L (1 MiB), Work RAM-H (1 MiB), and the
//	logical backup RAM (32 KiB)
const stateVersion = 1

// StateSize is the exact serialized size in bytes.
const StateSize = 4 + 2*sh2.SerializeSize + 0x100000 + 0x100000 + 0x8000

// System is one powered on Saturn skeleton.
type System struct {
	Master *sh2.CPU
	Slave  *sh2.CPU
	Bus    *bus.Bus

	disc   *cd.Image
	header *cd.IPHeader

	slaveReleased bool
	buttons       uint16
	frame         uint64
	framebuffer   []uint32
}

// New builds a System and resets it.
func New() *System {
	s := &System{
		Bus:         bus.New(),
		framebuffer: make([]uint32, Width*Height),
	}
	s.Master = sh2.New(s.Bus)
	s.Slave = sh2.New(s.Bus)
	s.Reset()
	return s
}

// Reset returns the machine to its power on state. The
// master is parked at the BIOS window base with the
// conventional initial stack; the slave is held again.
func (s *System) Reset() {
	s.slaveReleased = false
	s.buttons = 0
	s.frame = 0
	s.Bus.SetCurrentCPU(0)
	s.Master.Reset(0x00000000, 0x06001000)
	s.Slave.Reset(0x00000000, 0x06001000)
	for i := range s.framebuffer {
		s.framebuffer[i] = 0
	}
}

// LoadDisc opens a user supplied disc image by file
// extension (.cue or .iso) and parses its Saturn IP
// header. The image bytes stay with the caller supplied
// file; nothing is copied into the repository or the
// save state.
func (s *System) LoadDisc(path string) error {
	var img *cd.Image
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue":
		img, err = cd.OpenCue(path)
	case ".iso":
		img, err = cd.OpenISO(path)
	default:
		return fmt.Errorf("system: unsupported disc image extension %q", filepath.Ext(path))
	}
	if err != nil {
		return err
	}
	return s.LoadDiscImage(img)
}

// LoadDiscImage attaches an already opened image and
// validates the Saturn IP header signature.
func (s *System) LoadDiscImage(img *cd.Image) error {
	hdr, err := img.SaturnHeader()
	if err != nil {
		return err
	}
	s.disc = img
	s.header = hdr
	return nil
}

// Disc returns the attached image, or nil.
func (s *System) Disc() *cd.Image { return s.disc }

// Header returns the parsed Saturn IP header of the
// attached disc, or nil when no disc is attached.
func (s *System) Header() *cd.IPHeader { return s.header }

// ReleaseSlave releases the slave CPU from its held
// state so RunFrame starts stepping it.
func (s *System) ReleaseSlave() { s.slaveReleased = true }

// SlaveReleased reports whether the slave CPU runs.
func (s *System) SlaveReleased() bool { return s.slaveReleased }

// SetButtons stores the standard pad button mask.
func (s *System) SetButtons(mask uint16) { s.buttons = mask }

// Buttons returns the stored pad button mask.
func (s *System) Buttons() uint16 { return s.buttons }

// Frame returns the number of completed frames.
func (s *System) Frame() uint64 { return s.frame }

// RunFrame advances the machine by one frame budget.
// The master always runs; the slave runs the same budget
// when released. The bus current CPU selector is set
// before every step so the per CPU on-chip register
// windows answer for the CPU that is executing.
func (s *System) RunFrame() {
	s.runCPU(0, s.Master)
	if s.slaveReleased {
		s.runCPU(1, s.Slave)
	}
	s.frame += 1
}

func (s *System) runCPU(id int, cpu *sh2.CPU) {
	start := cpu.Cycles
	for cpu.Cycles-start < CyclesPerFrame {
		s.Bus.SetCurrentCPU(id)
		cpu.Step()
	}
}

// Framebuffer returns the shared frame buffer. In this
// slice it stays blank (all pixels 0x00000000); the VDP
// pipeline that fills it arrives in SAT-2.
func (s *System) Framebuffer() []uint32 { return s.framebuffer }

// Serialize writes the full deterministic machine
// state described at the top of this file.
func (s *System) Serialize() []byte {
	out := make([]byte, StateSize)
	out[0] = stateVersion
	if s.slaveReleased {
		out[1] = 1
	}
	binary.BigEndian.PutUint16(out[2:4], s.buttons)
	off := 4
	copy(out[off:off+sh2.SerializeSize], s.Master.Serialize())
	off += sh2.SerializeSize
	copy(out[off:off+sh2.SerializeSize], s.Slave.Serialize())
	off += sh2.SerializeSize
	copy(out[off:off+0x100000], s.Bus.WorkRAMLow())
	off += 0x100000
	copy(out[off:off+0x100000], s.Bus.WorkRAMHigh())
	off += 0x100000
	copy(out[off:off+0x8000], s.Bus.BackupRAM())
	return out
}

// Deserialize restores a state produced by Serialize.
func (s *System) Deserialize(data []byte) error {
	if len(data) != StateSize {
		return fmt.Errorf("system: state size %d, want %d", len(data), StateSize)
	}
	if data[0] != stateVersion {
		return fmt.Errorf("system: state version %d, want %d", data[0], stateVersion)
	}
	off := 4
	if err := s.Master.Deserialize(data[off : off+sh2.SerializeSize]); err != nil {
		return err
	}
	off += sh2.SerializeSize
	if err := s.Slave.Deserialize(data[off : off+sh2.SerializeSize]); err != nil {
		return err
	}
	off += sh2.SerializeSize
	copy(s.Bus.WorkRAMLow(), data[off:off+0x100000])
	off += 0x100000
	copy(s.Bus.WorkRAMHigh(), data[off:off+0x100000])
	off += 0x100000
	copy(s.Bus.BackupRAM(), data[off:off+0x8000])
	s.slaveReleased = data[1]&1 != 0
	s.buttons = binary.BigEndian.Uint16(data[2:4])
	return nil
}

// ErrNoDisc is returned by helpers that need a disc when
// none is attached. It is exported for SAT-4 boot code.
var ErrNoDisc = errors.New("system: no disc attached")
