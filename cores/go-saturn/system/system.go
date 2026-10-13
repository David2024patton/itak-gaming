// Package system is the whole Sega Saturn machine: two
// SH-2 CPUs (master and slave) on the shared bus, the
// VDP1 sprite and polygon engine, the VDP2 background
// engine composing the picture, the SCU interrupt
// controller wiring, the CD image foundations, the
// standard pad state, and deterministic save states.
//
// SAT-3 state of the machine: RunFrame advances the
// display scanline by scanline. At the start of each
// frame the VDP1 performs its frame change processing
// (bank swap, erase, auto draw per FBCR and PTMR). Each
// line the SCU gains its HBlank-IN status, VBlank-IN at
// the first blanking line, and VBlank-OUT at line 0, and
// one pending unmasked SCU interrupt is offered to both
// CPUs at the per source level of the SCU manual. The
// VDP2 renders each visible line into the framebuffer
// after that line's CPU slice, so mid frame register
// writes take effect on later lines just like hardware
// raster effects. Per line the machine also steps the
// SCSP sound chip by the line's cycle budget and polls
// the SMPC (executing any command written to COMREG),
// the CD block, and the SCU (DMA channels and DSP).
//
// The SMPC owns the machine level resets: SSHON and
// SSHOFF release and hold the slave, SNDON and SNDOFF
// release and hold the SCSP 68EC000, CDON and CDOFF
// power the CD block, and NMIREQ raises the master NMI.
// At power on the sound chip and the CD block are held
// off, matching the SMPC reset status the boot ROM
// reports, until their ON commands run. The CD block
// interrupt reaches the SCU as its A-Bus external input
// (scu.IntABusCD). SCSP main CPU requests raise the SCU
// sound request source. CD audio streams from the CD
// block into the SCSP CD input; AudioBuffer drains the
// mixed 44100 Hz stereo output.
//
// Slice boundaries, stated plainly: there is no BIOS in
// this core and none is loaded here. Reset parks the
// master CPU at address 0 with the conventional stack
// pointer; real game boot through the documented HLE
// path arrives in SAT-4. The slave CPU starts held and
// is released through ReleaseSlave or the SMPC SSHON
// command.
package system

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/bus"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/cd"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/cdblock"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/chips/sh2"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/scsp"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/scu"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/smpc"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/vdp1"
	"github.com/davidpatton/itak-gaming/cores/go-saturn/vdp2"
)

// Width and Height are the framebuffer dimensions of
// the power on display mode (TVMD cleared: 320 by 224).
// The live dimensions follow TVMD and are reported by
// DisplaySize; the framebuffer resizes when they change.
const (
	Width  = 320
	Height = 224
)

// CyclesPerFrame is the per CPU cycle budget of one
// frame at 28.6364 MHz and 60 frames per second,
// truncated to a whole cycle (477273 of 477273.33). The
// budget is split across the total scanlines of the
// current display mode: every line gets
// CyclesPerFrame/TotalLines cycles, and the first
// CyclesPerFrame%TotalLines lines get one extra cycle,
// so a frame always totals exactly CyclesPerFrame.
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
//	byte 0: version (3)
//	byte 1: flags, bit 0 set when the slave is released,
//	  bit 1 set when the next frame is the odd field
//	bytes 2..3: button mask
//	bytes 4..11: frame counter
//	bytes 12..15: current scanline
//	bytes 16..17: VDP1 misc state (draw bank, trigger
//	  latch, draw end flag)
//	bytes 18..19: zero
//	then the master CPU state (sh2.SerializeSize bytes)
//	then the slave CPU state (sh2.SerializeSize bytes)
//	then Work RAM-L (1 MiB), Work RAM-H (1 MiB), and the
//	logical backup RAM (32 KiB)
//	then VDP2 VRAM (512 KiB), VDP2 CRAM (4 KiB), and the
//	VDP2 register page (512 bytes)
//	then VDP1 VRAM (512 KiB), the VDP1 frame buffer
//	(both banks, 512 KiB), and the VDP1 register page
//	(128 bytes)
//	then the SCU register page (256 bytes)
//
// Version 3 appends, after that layout: the sound RAM
// (512 KiB) and the SCSP register page (4 KiB), the
// SMPC page (128 bytes) and the CD block page (64
// bytes), the SCU extra state (scu.ExtraSize bytes), the
// SMPC state (smpc.SerializeSize bytes), and the two
// variable sections, each a 4 byte big endian length
// followed by its bytes: the SCSP state, then the CD
// block state (buffered sector descriptors only; sector
// payloads re-read from the attached disc by FAD).
const stateVersion = 3

// stateHeader is the size of the fixed state header.
const stateHeader = 20

// stateFixedSize is the size of the fixed part of a
// version 3 state: everything except the two variable
// sections (the SCSP and CD block states), whose sizes
// depend on their queue and buffer contents.
const stateFixedSize = stateHeader + 2*sh2.SerializeSize +
	0x100000 + 0x100000 + 0x8000 +
	0x80000 + 0x1000 + 0x200 +
	0x80000 + 0x80000 + 0x80 +
	0x100 +
	0x80000 + 0x1000 + 0x80 + 0x40 +
	scu.ExtraSize + smpc.SerializeSize + 8

// System is one powered on Saturn.
type System struct {
	Master *sh2.CPU
	Slave  *sh2.CPU
	Bus    *bus.Bus
	VDP1   *vdp1.VDP1
	VDP2   *vdp2.VDP2
	SCU    *scu.SCU
	SMPC   *smpc.SMPC
	CD     *cdblock.Block
	Sound  *scsp.SCSP

	disc   *cd.Image
	header *cd.IPHeader

	slaveReleased bool
	buttons       uint16
	frame         uint64
	line          int
	oddField      bool
	fbW, fbH      int
	framebuffer   []uint32
}

// New builds a System and resets it.
func New() *System {
	s := &System{Bus: bus.New()}
	s.Master = sh2.New(s.Bus)
	s.Slave = sh2.New(s.Bus)
	s.VDP1 = vdp1.New(s.Bus.VDP1VRAM(), s.Bus.VDP1Framebuffer(), s.Bus.VDP1Registers())
	s.VDP2 = vdp2.New(s.Bus.VDP2VRAM(), s.Bus.VDP2CRAM(), s.Bus.VDP2Registers(), s.VDP1)
	s.SCU = scu.New(s.Bus.SCURegisters())
	s.SCU.AttachMem(s.Bus)
	// Timer 0 compares against the NTSC line width in
	// dot clocks (1708 per line at 320 wide); the SCU
	// timers only need a fixed positive line width.
	s.SCU.SetLineDots(1708)
	s.VDP1.SetRotateFunc(s.VDP2.FBRotationCoord)
	s.Reset()
	return s
}

// Reset returns the machine to its power on state. The
// master is parked at the BIOS window base with the
// conventional initial stack; the slave is held again.
// Device memories keep their contents, matching SAT-1:
// a fresh System is all zero because the bus starts
// zeroed.
func (s *System) Reset() {
	s.slaveReleased = false
	s.buttons = 0
	s.frame = 0
	s.line = 0
	s.oddField = false
	s.Bus.SetCurrentCPU(0)
	s.Master.Reset(0x00000000, 0x06001000)
	s.Slave.Reset(0x00000000, 0x06001000)
	s.wireDevices()
	s.updateDisplaySize()
	for i := range s.framebuffer {
		s.framebuffer[i] = 0
	}
}

// wireDevices (re)builds the SAT-3 devices over the bus
// pages and memories and connects their callbacks. It
// runs at power on (through Reset): the SCSP and the CD
// block start held off, matching the SMPC power on
// reset status, until the SMPC SNDON and CDON commands
// release them. The SMPC command hooks land here: SSHON
// releases the slave (ReleaseSlave semantics; the slave
// keeps its current PC), SSHOFF holds and resets it,
// NMIREQ raises the master NMI, and the sound and CD
// hooks drive the devices built above.
func (s *System) wireDevices() {
	s.Sound = scsp.New(s.Bus.SoundRAM(), s.Bus.SCSPRegisters())
	s.Sound.OnSoundRequest = func() { s.SCU.Raise(scu.IntSoundReq) }
	s.Sound.SetSoundOn(false)
	s.CD = cdblock.New(s.Bus.CDRegisters())
	s.CD.OnIRQ = func() { s.SCU.Raise(scu.IntABusCD) }
	s.CD.SetAudioSink(func(samples []int16) {
		s.Sound.InputCDSamples(samples)
	})
	s.CD.SetPower(false)
	if s.disc != nil {
		s.CD.SetImage(s.disc)
	}
	s.SMPC = smpc.New(s.Bus.SMPCRegisters(), smpc.Hooks{
		OnSSHON: func() { s.slaveReleased = true },
		OnSSHOFF: func() {
			s.slaveReleased = false
			s.Slave.Reset(0x00000000, 0x06001000)
		},
		OnNMIReq: func() { s.Master.RaiseNMI() },
		OnSNDON:  func() { s.Sound.SetSoundOn(true) },
		OnSNDOFF: func() { s.Sound.SetSoundOn(false) },
		OnCDOn:   func() { s.CD.SetPower(true) },
		OnCDOff:  func() { s.CD.SetPower(false) },
	})
}

// updateDisplaySize follows TVMD: when the VDP2 reports
// new dimensions, the framebuffer is rebuilt and the
// VDP1 learns its drawing bounds.
func (s *System) updateDisplaySize() {
	w, h := s.VDP2.DisplaySize()
	if w == s.fbW && h == s.fbH && s.framebuffer != nil {
		return
	}
	s.fbW, s.fbH = w, h
	s.framebuffer = make([]uint32, w*h)
	s.VDP1.SetDisplaySize(w, h)
}

// DisplaySize reports the current framebuffer dimensions
// as set by TVMD.
func (s *System) DisplaySize() (w, h int) { return s.fbW, s.fbH }

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
	s.CD.SetImage(img)
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

// Line returns the scanline currently being executed,
// or 0 between frames.
func (s *System) Line() int { return s.line }

// RunFrame advances the machine by one frame. Frame
// change processing runs first (it belongs to the
// vertical blank of the previous frame), then each
// scanline: timing flags and SCU status for the line,
// the SCU timers, one interrupt delivery attempt, the
// CPU cycle slice for the line, the SAT-3 device step
// (SCSP, SMPC, CD block, SCU DMA and DSP), the VDP1
// trigger watch, and the VDP2 render of the line when
// it is a visible line.
func (s *System) RunFrame() {
	s.VDP1.FrameBoundary()
	s.Bus.SetVDP1DrawBank(s.VDP1.DrawBank())
	s.updateDisplaySize()
	s.SMPC.SetPad(0, s.buttons)
	s.SMPC.SetPad(1, 0)

	total := s.VDP2.TotalLines()
	dispH := s.fbH
	base := CyclesPerFrame / total
	rem := CyclesPerFrame % total
	for line := 0; line < total; line++ {
		s.line = line
		s.VDP2.SetTiming(line, s.oddField)
		if line == dispH {
			s.SCU.Raise(scu.IntVBlankIn)
		}
		if line == 0 {
			s.SCU.Raise(scu.IntVBlankOut)
		}
		s.SCU.Raise(scu.IntHBlankIn)
		s.SCU.TickLine(line)
		s.deliverInterrupts()
		s.VDP2.SetHBlank(true)
		budget := base
		if line < rem {
			budget++
		}
		s.runCPUs(budget)
		s.stepDevices(budget)
		s.VDP2.SetHBlank(false)
		s.VDP1.PollTrigger()
		if s.VDP1.TakeDrawEnd() {
			s.SCU.Raise(scu.IntSpriteEnd)
		}
		if line < dispH {
			s.VDP2.RenderLine(s.framebuffer, line)
		}
	}
	s.frame++
	s.oddField = !s.oddField
	s.line = 0
}

// runCPUs steps both CPUs for one scanline slice. The
// master always runs; the slave runs the same budget
// when released. The bus current CPU selector is set
// before every step so the per CPU on-chip register
// windows answer for the CPU that is executing.
func (s *System) runCPUs(budget int) {
	s.runOne(0, s.Master, budget)
	if s.slaveReleased {
		s.runOne(1, s.Slave, budget)
	}
}

func (s *System) runOne(id int, cpu *sh2.CPU, budget int) {
	start := cpu.Cycles
	for cpu.Cycles-start < uint64(budget) {
		s.Bus.SetCurrentCPU(id)
		cpu.Step()
	}
}

// deliverInterrupts offers the highest priority pending
// unmasked SCU source to both CPUs at the per source
// level of the SCU manual (scu.LevelOf). IST bits stay
// set until software clears them, so the slave is
// offered the same source the master sees; each CPU
// takes it only when its own SR mask allows. The slave
// is offered sources only while released.
func (s *System) deliverInterrupts() {
	bit, vec := s.SCU.Deliver()
	if vec < 0 {
		return
	}
	level := scu.LevelOf(bit)
	s.Master.Interrupt(level, vec)
	if s.slaveReleased {
		s.Slave.Interrupt(level, vec)
	}
}

// stepDevices advances the SAT-3 devices by one
// scanline slice, after the CPUs have run: the SCSP by
// the line's cycle budget, then the polled devices. The
// SMPC executes at most one command per line (a command
// written by a CPU during this line runs at its end);
// the CD block polls its command registers and streams
// at sector rate; the SCU Poll runs any DMA channel
// whose start factor latched and services the DSP.
func (s *System) stepDevices(cycles int) {
	s.Sound.Step(cycles)
	s.SMPC.Poll()
	s.SMPC.Advance(cycles)
	s.CD.Poll()
	s.CD.Advance(cycles)
	s.SCU.Poll()
}

// Framebuffer returns the shared frame buffer in
// XRGB8888 at the current TVMD dimensions. Pixels are
// written by the VDP2 during RunFrame; before the first
// frame the buffer is all zero.
func (s *System) Framebuffer() []uint32 { return s.framebuffer }

// AudioBuffer drains the SCSP mixed output: interleaved
// stereo int16 samples at 44100 Hz produced since the
// previous call. Callers that save state for a bit
// exact continuation drain first: the pending output
// buffer is not part of the save state (the SCSP
// documents the same contract).
func (s *System) AudioBuffer() []int16 { return s.Sound.AudioBuffer() }

// StateSize returns the exact serialized size of the
// current machine state. Unlike SAT-2 the size is not
// constant: the SCSP section grows with its CD queue
// and the CD block section with its buffered sectors.
func (s *System) StateSize() int {
	return stateFixedSize + len(s.Sound.Serialize()) + len(s.CD.Serialize())
}

// Serialize writes the full deterministic machine state
// described at the top of this file.
func (s *System) Serialize() []byte {
	scspState := s.Sound.Serialize()
	cdState := s.CD.Serialize()
	out := make([]byte, stateFixedSize+len(scspState)+len(cdState))
	out[0] = stateVersion
	if s.slaveReleased {
		out[1] |= 1
	}
	if s.oddField {
		out[1] |= 2
	}
	binary.BigEndian.PutUint16(out[2:4], s.buttons)
	binary.BigEndian.PutUint64(out[4:12], s.frame)
	binary.BigEndian.PutUint32(out[12:16], uint32(s.line))
	binary.BigEndian.PutUint16(out[16:18], s.VDP1.Misc())
	off := stateHeader
	copy(out[off:off+sh2.SerializeSize], s.Master.Serialize())
	off += sh2.SerializeSize
	copy(out[off:off+sh2.SerializeSize], s.Slave.Serialize())
	off += sh2.SerializeSize
	copy(out[off:off+0x100000], s.Bus.WorkRAMLow())
	off += 0x100000
	copy(out[off:off+0x100000], s.Bus.WorkRAMHigh())
	off += 0x100000
	copy(out[off:off+0x8000], s.Bus.BackupRAM())
	off += 0x8000
	copy(out[off:off+0x80000], s.Bus.VDP2VRAM())
	off += 0x80000
	copy(out[off:off+0x1000], s.Bus.VDP2CRAM())
	off += 0x1000
	copy(out[off:off+0x200], s.Bus.VDP2Registers())
	off += 0x200
	copy(out[off:off+0x80000], s.Bus.VDP1VRAM())
	off += 0x80000
	copy(out[off:off+0x80000], s.Bus.VDP1Framebuffer())
	off += 0x80000
	copy(out[off:off+0x80], s.Bus.VDP1Registers())
	off += 0x80
	copy(out[off:off+0x100], s.Bus.SCURegisters())
	off += 0x100
	copy(out[off:off+0x80000], s.Bus.SoundRAM())
	off += 0x80000
	copy(out[off:off+0x1000], s.Bus.SCSPRegisters())
	off += 0x1000
	copy(out[off:off+0x80], s.Bus.SMPCRegisters())
	off += 0x80
	copy(out[off:off+0x40], s.Bus.CDRegisters())
	off += 0x40
	copy(out[off:off+scu.ExtraSize], s.SCU.SerializeExtra())
	off += scu.ExtraSize
	copy(out[off:off+smpc.SerializeSize], s.SMPC.Serialize())
	off += smpc.SerializeSize
	binary.BigEndian.PutUint32(out[off:off+4], uint32(len(scspState)))
	off += 4
	copy(out[off:off+len(scspState)], scspState)
	off += len(scspState)
	binary.BigEndian.PutUint32(out[off:off+4], uint32(len(cdState)))
	off += 4
	copy(out[off:off+len(cdState)], cdState)
	return out
}

// Deserialize restores a state produced by Serialize.
// The total length is validated against the two
// length prefixed variable sections, so a truncated or
// overlong state errors instead of restoring garbage.
func (s *System) Deserialize(data []byte) error {
	if len(data) < stateFixedSize {
		return fmt.Errorf("system: state size %d, want at least %d", len(data), stateFixedSize)
	}
	if data[0] != stateVersion {
		return fmt.Errorf("system: state version %d, want %d", data[0], stateVersion)
	}
	s.slaveReleased = data[1]&1 != 0
	s.oddField = data[1]&2 != 0
	s.buttons = binary.BigEndian.Uint16(data[2:4])
	s.frame = binary.BigEndian.Uint64(data[4:12])
	s.line = int(binary.BigEndian.Uint32(data[12:16]))
	off := stateHeader
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
	off += 0x8000
	copy(s.Bus.VDP2VRAM(), data[off:off+0x80000])
	off += 0x80000
	copy(s.Bus.VDP2CRAM(), data[off:off+0x1000])
	off += 0x1000
	copy(s.Bus.VDP2Registers(), data[off:off+0x200])
	off += 0x200
	copy(s.Bus.VDP1VRAM(), data[off:off+0x80000])
	off += 0x80000
	copy(s.Bus.VDP1Framebuffer(), data[off:off+0x80000])
	off += 0x80000
	copy(s.Bus.VDP1Registers(), data[off:off+0x80])
	off += 0x80
	copy(s.Bus.SCURegisters(), data[off:off+0x100])
	off += 0x100
	copy(s.Bus.SoundRAM(), data[off:off+0x80000])
	off += 0x80000
	copy(s.Bus.SCSPRegisters(), data[off:off+0x1000])
	off += 0x1000
	copy(s.Bus.SMPCRegisters(), data[off:off+0x80])
	off += 0x80
	copy(s.Bus.CDRegisters(), data[off:off+0x40])
	off += 0x40
	if err := s.SCU.DeserializeExtra(data[off : off+scu.ExtraSize]); err != nil {
		return err
	}
	off += scu.ExtraSize
	// The SMPC page is restored above: the SMPC poll
	// tracker resyncs to its COMREG value, so a state
	// saved mid sequence does not re-execute a command.
	if err := s.SMPC.Deserialize(data[off : off+smpc.SerializeSize]); err != nil {
		return err
	}
	off += smpc.SerializeSize
	n := int(binary.BigEndian.Uint32(data[off : off+4]))
	off += 4
	if n < 0 || off+n > len(data) {
		return fmt.Errorf("system: SCSP state length %d overruns the state", n)
	}
	if err := s.Sound.Deserialize(data[off : off+n]); err != nil {
		return err
	}
	off += n
	m := int(binary.BigEndian.Uint32(data[off : off+4]))
	off += 4
	if m < 0 || off+m != len(data) {
		return fmt.Errorf("system: CD block state length %d does not close the state", m)
	}
	if err := s.CD.Deserialize(data[off : off+m]); err != nil {
		return err
	}
	s.VDP1.SetMisc(binary.BigEndian.Uint16(data[16:18]))
	s.Bus.SetVDP1DrawBank(s.VDP1.DrawBank())
	s.updateDisplaySize()
	return nil
}

// ErrNoDisc is returned by helpers that need a disc when
// none is attached. It is exported for SAT-4 boot code.
var ErrNoDisc = errors.New("system: no disc attached")
