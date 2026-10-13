// Package system wires the parts of the Genesis into a running machine:
// the two CPUs, the bus, the VDP and the sound section, clocked and
// interrupted the way the real hardware does it (NTSC timing).
//
// Timing model: the NTSC master clock is 53,693,175 Hz, divided by 7 for
// the 68000 (7.67 MHz) and by 15 for the Z80 (3.58 MHz). One frame is 3420
// master cycles x 262 lines = 896,040 master cycles (59.92 fps). The CPUs
// run in per-scanline budgets; the VDP renders each visible line after the
// CPUs have run for it, raises the HBlank interrupt during the line
// sequence and the VBlank interrupt (level 6) plus the Z80 interrupt at
// the start of the blanking period.
package system

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/bus"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/cart"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/scd"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/sound"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/vdp"

	m68k "github.com/davidpatton/itak-gaming/cores/go-genesis/chips/m68k"
	"github.com/davidpatton/itak-gaming/cores/go-genesis/chips/z80"
)

const (
	masterHz      = 53693175
	masterPerLine = 3420
	linesTotal    = 262
	masterFrame   = masterPerLine * linesTotal // 896,040
	z80PerLine    = 228                        // 896,040 / 15 / 262, exact
)

// subFrameCycles is the number of sub CPU cycles in one frame: the
// Sega CD sub CPU runs at 12.5 MHz.
const subFrameCycles = 12500000 * masterFrame / masterHz

// subTarget is the sub CPU cycle count at the end of the given line.
func subTarget(line int) uint64 {
	return uint64(line+1) * subFrameCycles / linesTotal
}

// System is one Genesis console.
type System struct {
	cart  *cart.Cart
	vdp   *vdp.VDP
	sound *sound.Sound
	bus   *bus.Bus
	cpu   *m68k.CPU
	zcpu  *z80.CPU
	cd    *scd.Unit
	scpu  *m68k.CPU
	disc  *scd.Disc

	audio    []int16 // samples produced since the last AudioBuffer call
	audioAcc float64 // fractional samples owed
	frame    uint64  // frames run since power-on (drives cumulative cycle targets)
	loaded   bool

	// Anchors for timestamping Z80 writes to the YM2612 (see the
	// SetYMClock closure in LoadGame): the YM time at the start of
	// the scanline whose Z80 budget is currently running, and the
	// Z80 cycle counter at that moment. Recomputed at every line
	// before any Z80 write can occur, so they never need to be
	// serialized.
	z80LineYM     int64
	z80LineCycles uint64
}

// New returns an empty machine; call LoadGame before RunFrame.
func New() *System { return &System{} }

// LoadGame builds the machine around a cartridge image and powers it on.
// Loading a cartridge powers the machine on fresh: any Sega CD
// attachment is cleared before the new bus is built, and any disc
// must be loaded again afterwards with LoadDisc.
func (s *System) LoadGame(data []byte) error {
	c, err := cart.Load(data)
	if err != nil {
		return err
	}
	s.cd = nil
	s.scpu = nil
	if s.disc != nil {
		s.disc.Close()
		s.disc = nil
	}
	s.cart = c
	s.vdp = vdp.New()
	s.sound = sound.New()
	s.bus = bus.New(c, s.vdp, s.sound)
	s.cpu = m68k.New(s.bus)
	s.zcpu = z80.New(s.bus)
	// Timestamp source for YM2612 writes, so the sound section
	// can render the pending FM span before a write lands (the
	// bus calls it at the write's emulated moment). The YM2612
	// clock equals the 68000 clock (both master / 7), so a 68000
	// write is stamped with the CPU's own counter, which both
	// CPUs charge after each instruction: a stamp is the time at
	// the start of the executing instruction, instruction
	// granularity (a few cycles, far inside the ~465-clock
	// spacing of a DAC stream). A Z80 write is stamped from the
	// line anchors set in RunFrame: the line's start time plus
	// the Z80 cycles elapsed in the line, converted at the fixed
	// ratio (one Z80 cycle is 15/7 YM clocks; the conversion is
	// computed from the anchor each call, so no fractional
	// remainder ever accumulates). One imprecision is inherent
	// to the frame loop's shape: the 68000 runs a whole line
	// before the Z80 runs its share, so within one scanline a
	// Z80 stamp can fall up to a line (~489 clocks, ~3.4 native
	// samples) behind 68000 time already rendered; the sound
	// section clamps such spans to nothing, and the relative
	// spacing of writes from one CPU, which is what a sample
	// stream is made of, stays exact.
	s.bus.SetYMClock(func(fromZ80 bool) int64 {
		if fromZ80 {
			return s.z80LineYM + int64((s.zcpu.Cycles()-s.z80LineCycles)*15/7)
		}
		return int64(s.cpu.Cycles())
	})
	s.audio = nil
	s.audioAcc = 0
	s.frame = 0
	s.loaded = true
	s.cpu.Reset()  // reads SSP + PC from the cartridge vectors
	s.zcpu.Reset() // held in reset by the bus until the game releases it
	return nil
}

// Loaded reports whether a game is in the machine.
func (s *System) Loaded() bool { return s.loaded }

// LoadDisc opens a disc image and attaches a Sega CD unit. With no
// cartridge loaded, the machine powers on around the built-in main
// stub program so a CD-only boot works; with a cartridge loaded, the
// CD attaches alongside and the cartridge stays in control (it must
// release the sub CPU itself through the gate array, as on hardware).
// Loading a cartridge afterwards clears the attachment, so a disc
// must be loaded again after LoadGame.
func (s *System) LoadDisc(path string) error {
	disc, err := scd.OpenDisc(path)
	if err != nil {
		return err
	}
	if !s.loaded {
		if err := s.LoadGame(scd.MainStubROM()); err != nil {
			disc.Close()
			return err
		}
	}
	if s.disc != nil {
		s.disc.Close()
	}
	s.disc = disc
	s.cd = scd.NewUnit(disc)
	s.scpu = m68k.New(s.cd)
	s.bus.AttachCD(s.cd)
	return nil
}

// CDAttached reports whether a Sega CD unit is attached.
func (s *System) CDAttached() bool { return s.cd != nil }

// CDUnit returns the attached Sega CD unit, or nil when detached.
func (s *System) CDUnit() *scd.Unit { return s.cd }

// Reset is the console reset button: the 68000 restarts from its vectors.
// RAM, VRAM and cartridge save RAM survive, as they do on hardware.
func (s *System) Reset() {
	if !s.loaded {
		return
	}
	s.cpu.Reset()
	if s.cd != nil {
		s.cd.ForceSubReset()
	}
}

// Title returns the cartridge's overseas title, for logs and UIs.
func (s *System) Title() string {
	if s.cart == nil {
		return ""
	}
	return s.cart.Title
}

// SetButtons records pad state for a player, in bus.Pad* bit layout.
func (s *System) SetButtons(player int, mask uint16) {
	if s.loaded {
		s.bus.SetPad(player, mask)
	}
}

// Framebuffer returns the VDP's RGB565 frame (stride vdp.MaxWidth).
func (s *System) Framebuffer() []uint16 {
	if !s.loaded {
		return nil
	}
	return s.vdp.Framebuffer()
}

// Width is the current display width in pixels (256 or 320).
func (s *System) Width() int {
	if !s.loaded {
		return 320
	}
	return s.vdp.DisplayWidth()
}

// Height is the current display height in pixels (224 or 240).
func (s *System) Height() int {
	if !s.loaded {
		return 224
	}
	return s.vdp.DisplayHeight()
}

// SystemRAM exposes the 64KB work RAM (libretro memory interface).
func (s *System) SystemRAM() []uint8 {
	if !s.loaded {
		return nil
	}
	return s.bus.RAM[:]
}

// SaveRAM exposes the cartridge save RAM, if the cartridge has any.
func (s *System) SaveRAM() []uint8 {
	if !s.loaded {
		return nil
	}
	return s.bus.SRAM
}

// SoundWrites reports how many PSG and YM2612 register writes the game
// has made (diagnostics; see sound.WriteCounts).
func (s *System) SoundWrites() (psg, ym int) {
	if !s.loaded {
		return 0, 0
	}
	return s.sound.WriteCounts()
}

// AudioBuffer drains the samples produced by the last RunFrame.
func (s *System) AudioBuffer() []int16 {
	out := s.audio
	s.audio = nil
	return out
}

// m68kTarget is the 68000 cycle count at the end of the given line.
func m68kTarget(line int) uint64 {
	return uint64(line+1) * masterFrame / 7 / linesTotal
}

// run68k advances the 68000 toward target cycles. A CPU parked in STOP
// makes no progress; it wakes when an interrupt lands at a line boundary.
func (s *System) run68k(target uint64) {
	for i := 0; i < 32; i++ {
		cur := s.cpu.Cycles()
		if cur >= target {
			return
		}
		s.cpu.StepCycles(int(target - cur))
		if s.cpu.Cycles() == cur {
			return
		}
	}
}

// runSubLine advances the Sega CD sub CPU by one scanline of cycles,
// honoring the gate array reset and halt lines the main CPU controls.
func (s *System) runSubLine(line int) {
	if s.cd == nil {
		return
	}
	if s.cd.TakeSubReset() {
		s.scpu.Reset()
	}
	if s.cd.SubHalted() {
		return
	}
	base := s.frame * subFrameCycles
	target := base + subTarget(line)
	for i := 0; i < 32; i++ {
		cur := s.scpu.Cycles()
		if cur >= target {
			return
		}
		s.scpu.StepCycles(int(target - cur))
		if s.scpu.Cycles() == cur {
			return
		}
	}
}

// runZ80Line advances the Z80 by one scanline of cycles, honoring the
// bus-request and reset lines the 68000 controls. lineStartYM is the
// scanline's start time in YM2612 clocks; it and the Z80 counter at
// entry anchor the timestamps of any YM2612 writes the Z80 makes
// during the line (see the SetYMClock closure in LoadGame).
func (s *System) runZ80Line(lineStartYM int64) {
	if s.bus.TakeZ80Reset() {
		s.zcpu.Reset()
	}
	s.z80LineYM = lineStartYM
	s.z80LineCycles = s.zcpu.Cycles()
	if s.bus.Z80Halted() {
		return
	}
	target := s.zcpu.Cycles() + z80PerLine
	for i := 0; i < 32; i++ {
		cur := s.zcpu.Cycles()
		if cur >= target {
			return
		}
		s.zcpu.StepCycles(int(target - cur))
		if s.zcpu.Cycles() == cur {
			return
		}
	}
}

// RunFrame runs one whole video frame: 262 scanlines of CPU time, VDP
// rendering and interrupts, then produces the frame's audio.
func (s *System) RunFrame() {
	if !s.loaded {
		return
	}
	h := s.vdp.DisplayHeight()
	// The CPU cycle counter is cumulative since power-on, so line targets
	// must be too: base this frame's targets on the frames already run.
	base := s.frame * masterFrame / 7
	s.vdp.BeginFrame()
	// clocked is the cumulative 68000-cycle target the YM2612
	// timers have been advanced to. The YM2612 clock equals the
	// 68000 clock (both master / 7), so line-target deltas are YM
	// master clocks one to one. Advancing at each line boundary
	// lets a CPU polling the YM status port see timer flags flip
	// mid-frame; Produce accounts for the stepped time through the
	// sound section's ledger and never steps it twice. The deltas
	// depend only on the line number, not the frame base, so a
	// restored machine replays the exact same advance sequence.
	clocked := base
	for line := 0; line < linesTotal; line++ {
		if line == 0 {
			s.vdp.ExitVBlank()
			s.zcpu.INT(false, 0)
		}
		s.run68k(base + m68kTarget(line))
		// The line starts, in 68000 (and YM2612) cycles, at the
		// previous line's cumulative target.
		s.runZ80Line(int64(base + uint64(line)*masterFrame/7/linesTotal))
		s.runSubLine(line)
		if s.cd != nil {
			// Advance the CD block (CDD 75 Hz tick, CD-DA and data
			// read progression) by this line's sub CPU cycle budget.
			// The delta depends only on the line number, so a
			// restored machine replays the exact same advance
			// sequence. Completed CDD commands raise level 4 on
			// the sub CPU and completed CDC transfers level 5,
			// each gated by its gate array interrupt mask bit.
			delta := subTarget(line)
			if line > 0 {
				delta -= subTarget(line - 1)
			}
			s.cd.AdvanceCycles(int(delta))
			if s.cd.TakeCDDInterrupt() {
				s.scpu.RequestInterrupt(4, nil)
			}
			if s.cd.TakeCDCInterrupt() {
				s.scpu.RequestInterrupt(5, nil)
			}
			if fire, vec := s.cd.LineTick(line); fire {
				s.cpu.RequestInterrupt(2, vec)
			}
		}
		target := base + m68kTarget(line)
		s.sound.AdvanceClocks(uint32(target - clocked))
		clocked = target
		if line < h {
			if s.vdp.LineIRQ(line) {
				s.cpu.RequestInterrupt(4, nil)
			}
			s.vdp.RenderLine(line)
		}
		if line == h {
			if s.vdp.EnterVBlank() {
				s.cpu.RequestInterrupt(6, nil)
			}
			s.zcpu.INT(true, 0xFF)
		}
	}
	s.frame++
	// 44,100 Hz at 59.92 fps is ~735.9 samples per frame; carry the fraction.
	s.audioAcc += float64(sound.SampleRate) * masterFrame / masterHz
	n := int(s.audioAcc)
	s.audioAcc -= float64(n)
	gen := s.sound.Produce(n)
	if s.cd != nil {
		// Mix the Sega CD audio (CD-DA through the fader plus the
		// RF5C164 PCM mix, both already at the machine rate) into
		// the Genesis output with saturation.
		cdAudio := s.cd.RenderAudio(n)
		for i := range gen {
			if i >= len(cdAudio) {
				break
			}
			v := int32(gen[i]) + int32(cdAudio[i])
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			gen[i] = int16(v)
		}
	}
	s.audio = append(s.audio, gen...)
}

// --- save states ---------------------------------------------------------

const m68kStateSize = 80 // D[8]+A[8] u32, PC u32, SR u16, USP u32, SSP u32, IR u16

// trailerSize holds the machine-level bookkeeping appended after the
// component states: frame counter (u64), 68000 cycle counter (u64) and
// the audio fraction accumulator (f64 bits). The cycle counter MUST be
// saved: the CPU's SetState zeroes it, and this system's frame
// timing is derived from it, so losing it fast-forwards the machine on
// the first frame after a restore.
const trailerSize = 24

// writeM68kState writes one 68000 register snapshot (m68kStateSize
// bytes) into buf.
func writeM68kState(buf []byte, cpu *m68k.CPU) {
	off := 0
	r := cpu.Registers()
	// The CPU's SetState ignores the A[7] slot and derives the
	// active stack pointer from SSP (supervisor) or USP (user) instead,
	// while its Registers() can report a stale SSP/USP next to the live
	// A[7]. Fold the live A[7] into whichever stack pointer is active so
	// a restore lands on the exact stack the game was using. (Genesis
	// games always run in supervisor mode.)
	if r.SR&0x2000 != 0 {
		r.SSP = r.A[7]
	} else {
		r.USP = r.A[7]
	}
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint32(buf[off:], r.D[i])
		off += 4
	}
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint32(buf[off:], r.A[i])
		off += 4
	}
	binary.LittleEndian.PutUint32(buf[off:], r.PC)
	off += 4
	binary.LittleEndian.PutUint16(buf[off:], r.SR)
	off += 2
	binary.LittleEndian.PutUint32(buf[off:], r.USP)
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], r.SSP)
	off += 4
	binary.LittleEndian.PutUint16(buf[off:], r.IR)
}

// readM68kState parses one 68000 register snapshot from buf and
// installs it into cpu.
func readM68kState(buf []byte, cpu *m68k.CPU) {
	off := 0
	var r m68k.Registers
	for i := 0; i < 8; i++ {
		r.D[i] = binary.LittleEndian.Uint32(buf[off:])
		off += 4
	}
	for i := 0; i < 8; i++ {
		r.A[i] = binary.LittleEndian.Uint32(buf[off:])
		off += 4
	}
	r.PC = binary.LittleEndian.Uint32(buf[off:])
	off += 4
	r.SR = binary.LittleEndian.Uint16(buf[off:])
	off += 2
	r.USP = binary.LittleEndian.Uint32(buf[off:])
	off += 4
	r.SSP = binary.LittleEndian.Uint32(buf[off:])
	off += 4
	r.IR = binary.LittleEndian.Uint16(buf[off:])
	cpu.SetState(r)
}

// SerializeSize is the total save-state size in bytes.
//
// Versioning choice: the cartridge-only layout is unchanged and old
// cartridge states still load in cartridge mode; CD states carry an
// appended CD section and loading one requires the CD attached (a
// cartridge-sized buffer in CD mode fails the existing size check).
func (s *System) SerializeSize() int {
	if !s.loaded {
		return 0
	}
	n := m68kStateSize + z80.SerializeSize +
		s.bus.SerializeSize() + s.vdp.SerializeSize() + s.sound.SerializeSize() +
		trailerSize
	if s.cd != nil {
		n += m68kStateSize + s.cd.SerializeSize() + 8
	}
	return n
}

// Serialize writes the full machine state into buf.
//
// Note: a pending-but-unserviced 68000 interrupt is not part of the CPU
// library's register snapshot. Interrupts re-fire every frame (VBlank) or
// are re-requested by the game, so a restore at worst shifts one interrupt
// by a frame.
func (s *System) Serialize(buf []byte) error {
	if !s.loaded {
		return errors.New("system: no game loaded")
	}
	if len(buf) < s.SerializeSize() {
		return errors.New("system: serialize buffer too small")
	}
	off := 0
	writeM68kState(buf[off:], s.cpu)
	off += m68kStateSize

	if err := s.zcpu.Serialize(buf[off:]); err != nil {
		return err
	}
	off += z80.SerializeSize
	if err := s.bus.Serialize(buf[off:]); err != nil {
		return err
	}
	off += s.bus.SerializeSize()
	if err := s.vdp.Serialize(buf[off:]); err != nil {
		return err
	}
	off += s.vdp.SerializeSize()
	if err := s.sound.Serialize(buf[off:]); err != nil {
		return err
	}
	off += s.sound.SerializeSize()
	binary.LittleEndian.PutUint64(buf[off:], s.frame)
	binary.LittleEndian.PutUint64(buf[off+8:], s.cpu.Cycles())
	binary.LittleEndian.PutUint64(buf[off+16:], math.Float64bits(s.audioAcc))
	off += trailerSize
	if s.cd != nil {
		writeM68kState(buf[off:], s.scpu)
		off += m68kStateSize
		if err := s.cd.Serialize(buf[off:]); err != nil {
			return err
		}
		off += s.cd.SerializeSize()
		binary.LittleEndian.PutUint64(buf[off:], s.scpu.Cycles())
	}
	return nil
}

// Deserialize restores a state written by Serialize.
func (s *System) Deserialize(buf []byte) error {
	if !s.loaded {
		return errors.New("system: no game loaded")
	}
	if len(buf) < s.SerializeSize() {
		return errors.New("system: state too small")
	}
	off := 0
	readM68kState(buf[off:], s.cpu)
	off += m68kStateSize

	if err := s.zcpu.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += z80.SerializeSize
	if err := s.bus.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += s.bus.SerializeSize()
	if err := s.vdp.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += s.vdp.SerializeSize()
	if err := s.sound.Deserialize(buf[off:]); err != nil {
		return err
	}
	off += s.sound.SerializeSize()
	s.frame = binary.LittleEndian.Uint64(buf[off:])
	// SetState zeroed the CPU's cycle counter; put it back so the frame
	// timing base and the counter agree again.
	s.cpu.AddCycles(binary.LittleEndian.Uint64(buf[off+8:]))
	s.audioAcc = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+16:]))
	off += trailerSize
	if s.cd != nil {
		readM68kState(buf[off:], s.scpu)
		off += m68kStateSize
		if err := s.cd.Deserialize(buf[off:]); err != nil {
			return err
		}
		off += s.cd.SerializeSize()
		s.scpu.AddCycles(binary.LittleEndian.Uint64(buf[off:]))
	}
	return nil
}
