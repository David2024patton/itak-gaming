// Package system assembles the NES machine: the 6502 CPU, the
// dot-clocked PPU, the cartridge, and the address space around
// them.
//
// Timing: the CPU runs cycle by cycle, and every cycle moves
// the PPU three dots, interleaved with the access itself
// (first dot, interrupt line sample, second dot, the access,
// third dot), which is the alignment the hardware's
// documented races (the $2002 read on the vblank dot, the
// $2000 NMI races) are measured against.
// OAM DMA seizes the bus through the CPU's stall hook while its
// dummy reads keep the PPU running, and the cartridge's MMC3
// IRQ is clocked by the PPU's real A12 edges, not a counter.
//
// The APU (NES-3) ticks once per bus access, which this
// machine's accounting makes exactly one tick per CPU cycle:
// register accesses land at the access point of the cycle,
// the rest of the cycle's APU work (the frame sequencer, the
// channel timers, DMC fetches through the stall hook) runs
// right after, and the interrupt lines sync on the result.
//
// Round 2 (speed): the devices no longer advance eagerly per
// access. Each bus access records the dots and cycle it owes
// (two dots, the access, one APU cycle, one dot) plus a sync
// mark at the point the old code synced the interrupt lines,
// and the devices render lazily: a register access first
// catches its device up to the access point, a write that can
// change what a device sees (PPU registers, cartridge banking)
// catches the affected devices up before it lands, and the
// CPU's end-of-Step poll resolves the interrupt lines from
// the marks (SyncPoll, SyncFinal, SyncLatch below). Every
// observable therefore still resolves at the exact dot and
// cycle it did when the machine stepped eagerly; only the
// rendering work batches.
package system

import (
	"encoding/binary"
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-nes/cart"
	"github.com/davidpatton/itak-gaming/cores/go-nes/chips/cpu6502"
	"github.com/davidpatton/itak-gaming/cores/go-nes/ppu"
	"github.com/davidpatton/itak-gaming/cores/go-nes/sound"
)

// Width and Height mirror the PPU's framebuffer dimensions.
const (
	Width  = ppu.Width
	Height = ppu.Height
)

const serializeVersion = 2

// stateSize is the whole-machine save state size: version
// byte, the CPU state, a length-prefixed cartridge state, the
// PPU state, RAM, and the controller and bus latches. The
// cartridge state is variable-length (PRG RAM size differs per
// game), hence the length prefix.
const stateFixedTail = 2048 + 6 // RAM, open bus, two button latches, two shift registers, strobe

// System is the assembled machine.
type System struct {
	cpu  *cpu6502.CPU
	cart *cart.Cart
	ppu  *ppu.PPU
	apu  *sound.APU

	ram     [2048]uint8
	openBus uint8 // the last byte driven on the CPU data bus

	// Controller ports: button latches and shift registers,
	// one shared strobe.
	buttons [2]uint8
	shift   [2]uint8
	strobe  bool

	// accessSeq numbers the bus accesses (reads and writes,
	// dummy and DMA-stall accesses included) in execution
	// order. Each access records a sync mark under its number
	// in both devices; the interrupt poll resolves its sample
	// from the mark of the access before the Step's last one.
	accessSeq int

	// driveSeq is the newest access whose sync mark has been
	// replayed into the CPU's interrupt pins. The eager
	// machine drove the pins at every access's sync point;
	// the lazy machine replays those drives, in order, at the
	// points where their effects are observed (the poll
	// points), which preserves every edge the CPU's NMI latch
	// would have caught, including a pulse that rose at one
	// sync point and fell at the next.
	driveSeq int
}

// New builds a machine around a cartridge image and runs the
// reset sequence.
func New(image []byte) (*System, error) {
	c, err := cart.New(image)
	if err != nil {
		return nil, err
	}
	s := &System{cart: c}
	s.ppu = ppu.New(c)
	s.apu = sound.New()
	s.apu.FetchDMC = s.fetchDMCSample
	s.cpu = cpu6502.New()
	s.cpu.Attach(s)
	s.cpu.Reset()
	return s, nil
}

// fetchDMCSample is the DMC memory reader's DMA: it reads the
// byte through the side-effect-free peek path and charges the
// CPU the documented 4-cycle halt through the stall hook (the
// stall dummy reads then advance the whole machine, the APU
// included, as they do for OAM DMA).
func (s *System) fetchDMCSample(addr uint16) uint8 {
	s.cpu.Stall(4)
	return s.Peek(addr)
}

// APU exposes the audio unit for tests and debuggers. The
// unit is caught up first, so what the caller reads is the
// state at the current cycle.
func (s *System) APU() *sound.APU { s.apu.CatchUp(); return s.apu }

// Reset restarts the machine at the reset vector with the
// cartridge (and its battery RAM) kept: RAM clears, the PPU
// and APU return to their post-reset state, the CPU runs its
// reset sequence.
func (s *System) Reset() {
	s.accessSeq = 0
	s.driveSeq = 0
	s.ppu = ppu.New(s.cart)
	for i := range s.ram {
		s.ram[i] = 0
	}
	s.openBus = 0
	s.buttons = [2]uint8{}
	s.shift = [2]uint8{}
	s.strobe = false
	s.apu.Reset()
	s.cpu.Reset()
}

// CPU exposes the processor for the conformance harness.
func (s *System) CPU() *cpu6502.CPU { return s.cpu }

// Cart exposes the cartridge.
func (s *System) Cart() *cart.Cart { return s.cart }

// PPU exposes the picture unit for debuggers and tests. The
// chip is caught up first, so what the caller reads is the
// state at the current dot.
func (s *System) PPU() *ppu.PPU { s.ppu.CatchUp(); return s.ppu }

// Cycles returns total CPU cycles since power-on.
func (s *System) Cycles() uint64 { return s.cpu.Cycles() }

// Frames returns how many frames the PPU has completed.
func (s *System) Frames() uint64 { return s.ppu.Frames() }

// Framebuffer returns the PPU's current frame, XRGB8888,
// 256 by 240.
func (s *System) Framebuffer() []uint32 { return s.ppu.Framebuffer() }

// AudioBuffer drains the APU's samples produced since the
// last call, interleaved stereo int16 at sound.SampleRate.
func (s *System) AudioBuffer() []int16 {
	s.apu.CatchUp()
	return s.apu.AudioBuffer()
}

// SystemRAM returns the console's 2 KB internal RAM, for
// the libretro memory interface.
func (s *System) SystemRAM() []uint8 { return s.ram[:] }

// Step executes one CPU step (an instruction, an interrupt
// sequence, or a stall cycle).
func (s *System) Step() int { return s.cpu.Step() }

// RunCycles executes until at least n more CPU cycles have
// elapsed.
func (s *System) RunCycles(n int) {
	target := s.cpu.Cycles() + uint64(n)
	for s.cpu.Cycles() < target {
		s.Step()
	}
}

// RunFrame executes until the PPU completes the current frame
// (the next vblank begins), then returns. This is the
// frontend's frame loop.
func (s *System) RunFrame() {
	target := s.ppu.Frames() + 1
	for s.ppu.Frames() < target {
		s.Step()
	}
}

// The interrupt poll hooks. The CPU calls SyncPoll at the
// end of each Step, just before latching its poll decision,
// SyncFinal just after, and SyncLatch at the mid-sequence
// points where it reads the NMI latch directly (the BRK and
// interrupt vector hijack checks).
//
// The decision the CPU latches is the sample taken before the
// Step's last access, and that sample reads the line levels
// as the access BEFORE it left them: its sync point (two dots
// into its cycle, its APU tick done). Each access records
// that point as a mark in both devices, so SyncPoll resolves
// the sample from the second-to-last access's marks, exactly
// where the eager machine's per-access sync left the levels.
// SyncFinal then drives the levels at the last access's own
// sync point, which is where the next Step's samples (and
// any direct latch reads between Steps) pick them up.
// Pulses that live and die inside one access are seen or
// missed exactly as the eager sync saw or missed them,
// because both resolve at the same points.

// driveTo replays the NMI pin drives for every access up
// to seq, in order: the pin takes each sync mark's level, so
// the CPU's edge latch sees exactly the waveform the eager
// per-access sync produced. A mark evicted before its replay
// is skipped; the ring holds several Steps of marks and
// replays run at least once a Step, so eviction cannot
// happen in practice.
func (s *System) driveTo(seq int) {
	// Render once to the newest mark being replayed: the
	// render fills every mark it crosses in order, so the
	// replay below is pure reads and the pin still takes
	// each level (and every edge between them) in order.
	if seq > s.driveSeq {
		if dot, ok := s.ppu.MarkDot(seq); ok {
			s.ppu.CatchUpTo(dot)
		}
	}
	for s.driveSeq < seq {
		next := s.driveSeq + 1
		if lvl, ok := s.ppu.MarkNMI(next); ok {
			s.cpu.SetNMILine(lvl)
		}
		s.driveSeq = next
	}
}

// driveIRQ sets the IRQ line from the level at seq's sync
// point. The cartridge half resolves exactly (the PPU has
// rendered to the point, which is what clocks the mapper).
// The APU half is projected from the unit's rendered state
// in the common case: the frame flag's evolution between
// renders is closed-form sequencer arithmetic, and the
// accesses that can clear it render the unit first. The
// rare stretches the projection declines (a playing DMC
// sample, a sequencer reset landing in the window) render
// the unit to the point and read its mark, as before.
func (s *System) driveIRQ(seq int) {
	irq := s.cart.IRQPending()
	cycle := s.apu.Target() - int64(s.accessSeq-seq)
	if lvl, ok := s.apu.ProjectIRQ(cycle); ok {
		if lvl {
			irq = true
		}
	} else if lvl, ok := s.apu.MarkIRQ(seq); ok && lvl {
		irq = true
	}
	s.cpu.SetIRQLine(irq)
}

// SyncPoll drives the interrupt lines through the decision
// point of the Step that just executed (the sync point of
// the access before its last one).
func (s *System) SyncPoll() {
	seq := s.accessSeq - 1
	if seq < 1 {
		// No mark to resolve from (a first Step right after
		// a state load): the lines stand as the loaded CPU
		// state carries them.
		return
	}
	s.driveTo(seq)
	s.driveIRQ(seq)
}

// SyncFinal drives the NMI line through the most recent
// access's sync point, which is where the machine state
// (and the saved CPU state) reports it between Steps. The
// IRQ line is not driven here: it is level-triggered, its
// consumers sample it at poll points (SyncPoll drives it
// there), and leaving the APU unrendered at Step ends is
// what lets its cycles batch into the long spans the
// frame-end drain runs.
func (s *System) SyncFinal() {
	seq := s.accessSeq
	if seq < 1 {
		return
	}
	s.driveTo(seq)
}

// SyncLatch drives the NMI line through the last completed
// access's sync point for a direct mid-sequence latch read.
func (s *System) SyncLatch() { s.SyncFinal() }

// Read is the CPU bus read. The PPU runs two of the cycle's
// dots, the access captures against that state, the
// interrupt lines sync on the result, and the third dot
// runs. That split is the phase the hardware's $2002 and
// $2000 race behavior pins down. The dots and the APU cycle
// are recorded as owed work (with a sync mark at the sync
// point); the devices render them lazily, and anything that
// observes state catches up to its point first.
func (s *System) Read(addr uint16) uint8 {
	s.accessSeq++
	s.ppu.Advance(2)
	v := s.readAt(addr)
	s.openBus = v
	s.apu.Advance(1)
	s.ppu.Mark(s.accessSeq)
	s.apu.Mark(s.accessSeq)
	s.ppu.Advance(1)
	return v
}

// readAt dispatches a read.
func (s *System) readAt(addr uint16) uint8 {
	switch {
	case addr < 0x2000:
		return s.ram[addr&0x07FF]
	case addr < 0x4000:
		s.ppu.CatchUp()
		return s.ppu.ReadReg(int(addr & 7))
	case addr == 0x4015:
		s.apu.CatchUp()
		return s.apu.ReadStatus()
	case addr == 0x4016:
		return s.readController(0)
	case addr == 0x4017:
		return s.readController(1)
	case addr < 0x6000:
		// The write-only APU registers and $4014, and the
		// $4020-$5FFF expansion area, read as open bus.
		return s.openBus
	default:
		s.cart.SetCycle(int64(s.cpu.Cycles()))
		return s.cart.ReadPRG(addr)
	}
}

// Write is the CPU bus write, with the same two-dots-in
// split as Read.
func (s *System) Write(addr uint16, val uint8) {
	s.accessSeq++
	s.ppu.Advance(2)
	s.openBus = val
	s.writeAt(addr, val)
	s.apu.Advance(1)
	s.ppu.Mark(s.accessSeq)
	s.apu.Mark(s.accessSeq)
	s.ppu.Advance(1)
}

// writeAt dispatches a write. A write that can change what a
// device sees catches that device up to the access point
// before it lands, so the dots and cycles before the write
// render under the state they were owed, and the ones after
// render under the new state.
func (s *System) writeAt(addr uint16, val uint8) {
	switch {
	case addr < 0x2000:
		s.ram[addr&0x07FF] = val
	case addr < 0x4000:
		s.ppu.CatchUp()
		s.ppu.WriteReg(int(addr&7), val)
	case addr == 0x4014:
		s.ppu.CatchUp()
		s.oamDMA(val)
	case addr == 0x4015 || addr == 0x4017 || addr < 0x4014:
		s.apu.CatchUp()
		s.apu.Write(addr, val)
	case addr == 0x4016:
		s.writeStrobe(val)
	case addr < 0x6000:
		// Expansion writes ($4020-$5FFF) are ignored.
	default:
		// A cartridge write can switch the banks both the
		// PPU's fetches and the DMC's fetches read through,
		// so both devices catch up before it lands.
		s.ppu.CatchUp()
		s.apu.CatchUp()
		s.cart.SetCycle(int64(s.cpu.Cycles()))
		s.cart.WritePRG(addr, val)
	}
}

// Peek reads an address without side effects and without
// advancing time: $2002 reports its flags without clearing
// them, the controller shift registers do not advance, and
// cartridge reads are side-effect free in this core's mappers,
// so they pass through.
func (s *System) Peek(addr uint16) uint8 {
	switch {
	case addr < 0x2000:
		return s.ram[addr&0x07FF]
	case addr < 0x4000:
		s.ppu.CatchUp()
		return s.ppu.PeekReg(int(addr & 7))
	case addr < 0x6000:
		return s.openBus
	default:
		return s.cart.ReadPRG(addr)
	}
}

// --- Controllers ---

func (s *System) readController(port int) uint8 {
	if s.strobe {
		return s.buttons[port] & 1
	}
	v := s.shift[port] & 1
	s.shift[port] = (s.shift[port] >> 1) | 0x80
	return v
}

// SetButtons sets a controller's button state, in read order:
// bit 0 A, 1 B, 2 Select, 3 Start, 4 Up, 5 Down, 6 Left, 7 Right.
func (s *System) SetButtons(port int, mask uint8) {
	if port < 0 || port > 1 {
		return
	}
	s.buttons[port] = mask
	if s.strobe {
		s.shift[port] = mask
	}
}

func (s *System) writeStrobe(v uint8) {
	s.strobe = v&1 != 0
	if s.strobe {
		s.shift[0] = s.buttons[0]
		s.shift[1] = s.buttons[1]
	}
}

// --- OAM DMA ---

// oamDMA runs the $4014 DMA: 256 bytes copied from the given
// CPU page into PPU OAM through the PPU's DMA port (starting
// at the current OAM address, wrapping), while the CPU stalls
// for the documented 513 cycles, plus one when the write
// landed on an odd CPU cycle. The stall burns as dummy reads
// through the bus, so the PPU keeps running through the copy.
func (s *System) oamDMA(page uint8) {
	base := uint16(page) << 8
	for i := 0; i < 256; i++ {
		s.ppu.DMAWrite(s.Peek(base + uint16(i)))
	}
	stall := 513
	if s.cpu.Cycles()&1 != 0 {
		stall = 514
	}
	s.cpu.Stall(stall)
}

// --- Save states ---

// Serialize writes the whole-machine state: version byte, CPU
// state, length-prefixed cartridge state, PPU state, APU
// state, RAM, and the controller and bus latches.
func (s *System) Serialize() []byte {
	s.ppu.CatchUp()
	s.apu.CatchUp()
	cartState := s.cart.Serialize()
	out := make([]byte, 0, 1+cpu6502.SerializeSize+2+len(cartState)+ppu.SerializeSize+sound.SerializeSize+stateFixedTail)
	out = append(out, serializeVersion)
	out = append(out, s.cpu.Serialize()...)
	var lb [2]byte
	binary.LittleEndian.PutUint16(lb[:], uint16(len(cartState)))
	out = append(out, lb[:]...)
	out = append(out, cartState...)
	out = append(out, s.ppu.Serialize()...)
	out = append(out, s.apu.Serialize()...)
	out = append(out, s.ram[:]...)
	out = append(out, s.openBus, s.buttons[0], s.buttons[1], s.shift[0])
	out = append(out, s.shift[1])
	if s.strobe {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return out
}

// StateSize returns the size Serialize will produce for the
// loaded cartridge.
func (s *System) StateSize() int {
	return 1 + cpu6502.SerializeSize + 2 + len(s.cart.Serialize()) + ppu.SerializeSize + sound.SerializeSize + stateFixedTail
}

// Deserialize restores a state produced by Serialize for the
// same cartridge.
func (s *System) Deserialize(in []byte) error {
	if len(in) < 1+cpu6502.SerializeSize+2 {
		return errors.New("system: state too small")
	}
	defer func() {
		s.accessSeq = 0
		s.driveSeq = 0
	}()
	if in[0] != serializeVersion {
		return errors.New("system: unsupported state version")
	}
	off := 1
	if err := s.cpu.Deserialize(in[off : off+cpu6502.SerializeSize]); err != nil {
		return err
	}
	off += cpu6502.SerializeSize
	cl := int(binary.LittleEndian.Uint16(in[off:]))
	off += 2
	if len(in) != off+cl+ppu.SerializeSize+sound.SerializeSize+stateFixedTail {
		return errors.New("system: state size mismatch")
	}
	if err := s.cart.Deserialize(in[off : off+cl]); err != nil {
		return err
	}
	off += cl
	if err := s.ppu.Deserialize(in[off : off+ppu.SerializeSize]); err != nil {
		return err
	}
	off += ppu.SerializeSize
	if err := s.apu.Deserialize(in[off : off+sound.SerializeSize]); err != nil {
		return err
	}
	off += sound.SerializeSize
	copy(s.ram[:], in[off:off+2048])
	off += 2048
	s.openBus = in[off]
	s.buttons[0], s.buttons[1] = in[off+1], in[off+2]
	s.shift[0], s.shift[1] = in[off+3], in[off+4]
	s.strobe = in[off+5] != 0
	return nil
}

// BlarggResult inspects the standard Blargg test protocol in
// cartridge RAM: while a test runs, $6000 holds $80; when it
// finishes, $6000 holds the result code (0 for pass) with the
// signature $DE $B0 $61 at $6001-$6003 and a NUL-terminated
// report text at $6004. ok reports whether a finished result is
// present; passed and text are meaningful only then.
func (s *System) BlarggResult() (ok bool, passed bool, text string) {
	if s.Peek(0x6001) != 0xDE || s.Peek(0x6002) != 0xB0 || s.Peek(0x6003) != 0x61 {
		return false, false, ""
	}
	status := s.Peek(0x6000)
	if status == 0x80 {
		return false, false, ""
	}
	var buf []byte
	for addr := uint16(0x6004); addr < 0x8000; addr++ {
		b := s.Peek(addr)
		if b == 0 {
			break
		}
		buf = append(buf, b)
	}
	return true, status == 0, string(buf)
}
