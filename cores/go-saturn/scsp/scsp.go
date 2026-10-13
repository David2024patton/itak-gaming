// Package scsp models the Sega Saturn SCSP (Yamaha YMF292) sound
// processor: 32 PCM and FM slots, the FH-1 style effect DSP, three
// timers, the internal DMA, the MIDI stub, and the on chip MC68EC000
// sound CPU.
//
// Register map and semantics follow the SCSP User's Manual
// (Sega ST-77-R2-052594, the ST-068 family document). Where the
// manual and the slice brief disagreed, the manual won; the
// divergences are:
//
//   - Slot registers: the manual allocates 12 words per slot on a
//     32 byte stride (words 12 to 15 of each stride are unused),
//     not 8 words per slot.
//   - MADRS: the manual allocates 32 words at 100780H, not 64.
//     COEF is 64 words at 100700H. The DSP internal buffers live
//     at 100C00H and up: TEMP 128 entries, MEMS 32, MIXS 16,
//     EFREG 16 and EXTS 2.
//   - Interrupt enables: SCIEB, SCIPD and SCIRE belong to the sound
//     CPU (the 68EC000); MCIEB, MCIPD and MCIRE belong to the main
//     CPU (the SH-2 side). SCIPD bit 5 written by the main CPU is
//     the manual request to the sound CPU; MCIPD bit 5 written by
//     the sound CPU is the manual request to the main CPU, exported
//     here as OnSoundRequest. Pending bits are cleared by writing
//     1 bits to the matching reset register (SCIRE or MCIRE), not
//     by writing the pending register.
//   - There is no CD send level or pan field in the common register
//     area. CD-DA arrives on EXTS0 and EXTS1 and is mixed with the
//     EFSDL and EFPAN fields of slot registers 16 and 17, which is
//     the routing games and the BIOS use.
//   - DIPAN full left is 1FH (right channel at minus infinity) and
//     full right is 0FH, per the manual pan table.
//   - The m68ec000 package in this module exposes PC, D, A and SR
//     as methods, not fields.
//
// Shared storage model: the caller owns the 512 KB sound RAM slice
// and the 4 KB register page slice. New keeps both slices without
// copying them. Because both CPUs write the page directly, this
// chip observes writes by polling the page once per sample tick:
// KYONEX execution, the SCIRE and MCIRE clear registers, the bit 5
// manual request bits of SCIPD and MCIPD, and the DMA DEXE bit are
// all edge processed at that poll. The chip writes back the bits
// hardware would clear (KYONEX, DEXE, the reset registers) and
// mirrors the pending registers, the monitor call address, the
// sound stacks and the DSP internal buffer areas into the page.
//
// Clock model: the SCSP runs at 22.5792 MHz and the machine (SH-2)
// clock at 28.63636 MHz. One sample tick is 512 SCSP cycles, which
// is 44100 Hz. Step takes SH-2 cycles and converts them with an
// exact integer accumulator (cycles times 22579200, divided by
// 512 times 28636360), so there is no float drift. The accumulator
// is part of the serialized state; a restore that shifted the
// sample phase would be a determinism failure. Per sample tick the
// 68EC000 runs a 256 cycle budget of its own 11.2896 MHz clock
// (11289600 divided by 44100 is exactly 256) by calling its Step
// until the budget is spent; the final instruction may overshoot
// the budget, which is deterministic because the CPU state is
// serialized.
//
// Sound hold: SetSoundOn(false) models SMPC SNDOFF. While off, the
// 68EC000 is held in reset (its PC never advances), no sample ticks
// are generated, AudioBuffer stays empty, and Step accumulates
// nothing at all (the cycles are discarded, not banked). Turning
// sound on resets the 68EC000, which reads its initial stack
// pointer and PC from sound RAM addresses 0 and 4. Power on state
// on real hardware is sound held off until the SMPC releases it,
// but New starts in the released (on) state so package level tests
// can drive the chip directly; the system calls SetSoundOn(false)
// at boot.
//
// Approximations, stated plainly:
//
//   - Slot sample interpolation is linear between adjacent
//     samples.
//   - EG moves linearly in its 10 bit attenuation level. The rate
//     table below stands in for the manual rate descriptions; key
//     rate scaling follows the formula other implementations
//     document from hardware (effective rate is twice the rate
//     field plus twice KRS plus the signed octave plus FNS bit 9,
//     and KRS 15 turns scaling off, per the manual).
//   - Attenuation (EG level, TL bit weights, send levels and pan)
//     is computed from the manual decibel figures with the exact
//     20 log10 relation, not from a copied ROM table.
//   - MVOL gain is MVOL divided by 15, so 15 is unity and 0 is
//     silent, matching how the BIOS and drivers use it.
//   - FM modulation uses the previous sample outputs of the two
//     selected slots, averaged, scaled by the manual MDL maximum
//     address shift table. The manual two generation sound stack
//     timing is not modelled.
//   - The DSP executes the documented subset described in dsp.go,
//     not the full FH-1 encoding, which ST-77 does not publish.
package scsp

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/davidpatton/itak-gaming/cores/go-saturn/chips/m68ec000"
)

// Clock and geometry constants.
const (
	scspClockHz   = 22579200
	machineClock  = 28636360
	sampleRate    = 44100
	scspPerSample = 512 // SCSP cycles per sample tick
	cpuPerSample  = 256 // 68EC000 cycles per sample tick

	soundRAMSize = 512 * 1024
	regPageSize  = 4 * 1024

	addrRAMLimit = 0x080000 // CPU byte addresses 0x000000 to 0x07FFFF
	addrRegBase  = 0x100000 // CPU byte addresses 0x100000 to 0x100FFF
)

// Slot register word offsets within one slot stride of 0x20 bytes.
// The manual figure 4.2 lists 12 words; words 12 to 15 are unused.
const (
	slotStride = 0x20

	SlotCtrl   = 0x00 // KYONEX, KYONB, SBCTL, SSCTL, LPCTL, PCM8B, SA[19:16]
	SlotSA     = 0x02 // SA[15:0]
	SlotLSA    = 0x04
	SlotLEA    = 0x06
	SlotEG1    = 0x08 // D2R, D1R, EGHOLD, AR
	SlotEG2    = 0x0A // LPSLNK, KRS, DL, RR
	SlotVol    = 0x0C // STWINH, SDIR, TL
	SlotFM     = 0x0E // MDL, MDXSL, MDYSL
	SlotPitch  = 0x10 // OCT, FNS
	SlotLFO    = 0x12 // LFORE, LFOF, PLFOWS, PLFOS, ALFOWS, ALFOS
	SlotInSel  = 0x14 // ISEL, IMXL
	SlotOutSel = 0x16 // DISDL, DIPAN, EFSDL, EFPAN
)

// Common register offsets within the page, from the manual common
// control register figure and the worked interrupt example (which
// pins SCIEB at word address 10041EH, SCILV0 at 100424H, SCILV1 at
// 100426H, SCILV2 at 100428H and MCIEB at 10042AH). Words 40AH to
// 40EH and word 416H are unused in the manual figure; they stay
// plain storage here.
const (
	RegMaster  = 0x400 // MEM4MB, DAC18B, VER, MVOL
	RegRing    = 0x402 // RBL, RBP
	RegMIDIIn  = 0x404 // MOFULL, MOEMP, MIOVF, MIFULL, MIEMP, MIBUF
	RegMIDIOut = 0x406 // MOBUF
	RegMonitor = 0x408 // MSLC, CA
	RegDMALow  = 0x410 // DMEA[15:1]
	RegDMAHigh = 0x412 // DMEA[19:16], DRGA[11:1]
	RegDMACtrl = 0x414 // DGATE, DDIR, DEXE, DTLG[11:1]
	RegTimerA  = 0x418 // TACTL, TIMA
	RegTimerB  = 0x41A // TBCTL, TIMB
	RegTimerC  = 0x41C // TCCTL, TIMC
	RegSCIEB   = 0x41E
	RegSCIPD   = 0x420
	RegSCIRE   = 0x422
	RegSCILV0  = 0x424
	RegSCILV1  = 0x426
	RegSCILV2  = 0x428
	RegMCIEB   = 0x42A
	RegMCIPD   = 0x42C
	RegMCIRE   = 0x42E
)

// Sound stack and DSP register area offsets within the page, from
// the manual memory map figure.
const (
	RegStackA = 0x600 // SOUS generation A, 32 words
	RegStackB = 0x640 // SOUS generation B, 32 words
	RegCOEF   = 0x700 // 64 words
	RegMADRS  = 0x780 // 32 words
	RegMPRO   = 0x800 // 128 steps of 4 words
	RegTEMP   = 0xC00 // 128 entries of 2 words
	RegMEMS   = 0xE00 // 32 entries of 2 words
	RegMIXS   = 0xE80 // 16 entries of 2 words
	RegEFREG  = 0xEC0 // 16 words
	RegEXTS   = 0xEE0 // 2 words
)

// Interrupt source bits, from the manual interrupt factor table.
const (
	IntEXT0    = 0
	IntEXT1    = 1
	IntEXT2    = 2
	IntMIDIIn  = 3
	IntDMAEnd  = 4
	IntCPU     = 5
	IntTimerA  = 6
	IntTimerB  = 7
	IntTimerC  = 8
	IntMIDIOut = 9
	IntSample  = 10
)

// SCSP is one sound processor over shared RAM and register storage.
type SCSP struct {
	ram  []byte
	regs []byte

	cpu *m68ec000.CPU
	bus *cpuBus
	on  bool

	// OnSoundRequest, when set, is called once per rising main CPU
	// interrupt bit: a bit that is pending in MCIPD and enabled in
	// MCIEB and was not already signalled. The system wires it to
	// the SCU sound request source.
	OnSoundRequest func()

	acc int64 // SH-2 cycle to sample tick rational accumulator

	timers   [3]timerState
	sciPend  uint16
	mciPend  uint16
	mainSent uint16 // main bits already signalled via OnSoundRequest

	dma dmaState

	slots [32]slotState
	dsp   dspState

	cdQueue []int16
	audio   []int16
}

type timerState struct {
	preset  uint8
	counter uint8
	phase   int // prescaler phase, counts samples modulo 2^ctl
}

type dmaState struct {
	active      bool
	remaining   int
	ramWord     uint32
	regWord     uint32
	dirRegToRAM bool
	gate        bool
}

// cpuBus adapts the SCSP memory map to the 68EC000 Bus interface.
// CPU byte address 0x000000 to 0x07FFFF maps to sound RAM and
// 0x100000 to 0x100FFF maps to the register page. Every other
// address reads as 0 and ignores writes.
type cpuBus struct {
	s *SCSP
}

func (b *cpuBus) Read8(addr uint32) uint8 {
	v, _ := b.read(addr)
	return v
}

func (b *cpuBus) read(addr uint32) (byte, bool) {
	switch {
	case addr < addrRAMLimit:
		if int(addr) < len(b.s.ram) {
			return b.s.ram[addr], true
		}
	case addr >= addrRegBase && addr < addrRegBase+regPageSize:
		if int(addr-addrRegBase) < len(b.s.regs) {
			return b.s.regs[addr-addrRegBase], true
		}
	}
	return 0, false
}

func (b *cpuBus) write(addr uint32, v byte) {
	switch {
	case addr < addrRAMLimit:
		if int(addr) < len(b.s.ram) {
			b.s.ram[addr] = v
		}
	case addr >= addrRegBase && addr < addrRegBase+regPageSize:
		if int(addr-addrRegBase) < len(b.s.regs) {
			b.s.regs[addr-addrRegBase] = v
		}
	}
}

func (b *cpuBus) Read16(addr uint32) uint16 {
	hi, _ := b.read(addr)
	lo, _ := b.read(addr + 1)
	return uint16(hi)<<8 | uint16(lo)
}

func (b *cpuBus) Read32(addr uint32) uint32 {
	return uint32(b.Read16(addr))<<16 | uint32(b.Read16(addr+2))
}

func (b *cpuBus) Write8(addr uint32, v uint8) { b.write(addr, v) }

func (b *cpuBus) Write16(addr uint32, v uint16) {
	b.write(addr, byte(v>>8))
	b.write(addr+1, byte(v))
}

func (b *cpuBus) Write32(addr uint32, v uint32) {
	b.Write16(addr, uint16(v>>16))
	b.Write16(addr+2, uint16(v))
}

// New builds an SCSP over the caller owned sound RAM (512 KB) and
// register page (4 KB). Neither slice is copied. The chip starts
// in the sound on (released) state described in the package
// comment, with the 68EC000 reset once against the current RAM
// contents.
func New(ram []byte, regs []byte) *SCSP {
	s := &SCSP{ram: ram, regs: regs, on: true}
	s.bus = &cpuBus{s: s}
	s.cpu = m68ec000.New(s.bus)
	for i := range s.slots {
		s.slots[i].lfsr = 0xACE1 + uint32(i)*0x1B31
		s.slots[i].egLevel = egMaxQ
		s.slots[i].egPhase = egEnded
	}
	s.dsp.tempBase = 0
	s.cpu.Reset()
	return s
}

// CPU exposes the sound CPU for inspection by the system and by
// tests. The SCSP owns its stepping; callers must not Step it
// themselves while the SCSP is running.
func (s *SCSP) CPU() *m68ec000.CPU { return s.cpu }

// SoundOn reports whether sound is released.
func (s *SCSP) SoundOn() bool { return s.on }

// SetSoundOn releases or holds the sound block. Turning sound on
// after it was off resets the 68EC000 from the vectors in sound
// RAM. Turning it off holds the CPU in reset; while off, Step
// accumulates nothing and produces no audio.
func (s *SCSP) SetSoundOn(on bool) {
	if on == s.on {
		return
	}
	s.on = on
	if on {
		s.cpu.Reset()
	}
}

// Step advances the chip by the given number of SH-2 cycles. While
// sound is off the cycles are discarded entirely.
func (s *SCSP) Step(cycles int) {
	if cycles <= 0 || !s.on {
		return
	}
	s.acc += int64(cycles) * scspClockHz
	const denom = int64(scspPerSample) * machineClock
	for s.acc >= denom {
		s.acc -= denom
		s.sampleTick()
	}
}

// AudioBuffer drains the interleaved stereo output produced since
// the last call, at 44100 Hz.
func (s *SCSP) AudioBuffer() []int16 {
	out := s.audio
	s.audio = nil
	return out
}

// InputCDSamples queues interleaved stereo CD-DA samples. Each
// sample tick consumes one pair; an empty queue feeds silence.
func (s *SCSP) InputCDSamples(samples []int16) {
	s.cdQueue = append(s.cdQueue, samples...)
}

// reg16 reads one big endian word of the register page.
func (s *SCSP) reg16(off int) uint16 {
	if off+1 >= len(s.regs) {
		return 0
	}
	return binary.BigEndian.Uint16(s.regs[off : off+2])
}

// setReg16 writes one big endian word of the register page.
func (s *SCSP) setReg16(off int, v uint16) {
	if off+1 >= len(s.regs) {
		return
	}
	binary.BigEndian.PutUint16(s.regs[off:off+2], v)
}

func (s *SCSP) slotReg(slot, wordOff int) uint16 {
	return s.reg16(slot*slotStride + wordOff)
}

// sampleTick runs one 44100 Hz tick: poll the page, advance the
// timers, render the slots, run the DSP, mix CD and effect output,
// service DMA and interrupts, then run the sound CPU.
func (s *SCSP) sampleTick() {
	s.pollPage()
	s.tickTimers()
	s.raiseSource(IntSample)

	var mixL, mixR int64
	mixs := s.renderSlots(&mixL, &mixR)

	// CD input for this tick.
	var cdL, cdR int16
	if len(s.cdQueue) >= 2 {
		cdL, cdR = s.cdQueue[0], s.cdQueue[1]
		s.cdQueue = s.cdQueue[2:]
	}
	s.dsp.exts[0] = int32(cdL)
	s.dsp.exts[1] = int32(cdR)

	s.runDSP(mixs)

	// Effect returns: EFREG[i] through EFSDL and EFPAN of slot i.
	for i := 0; i < 16; i++ {
		w := s.slotReg(i, SlotOutSel)
		g := sendGain(int(w>>5) & 7)
		if g == 0 || s.dsp.efreg[i] == 0 {
			continue
		}
		gl, gr := panGains(int(w & 0x1F))
		v := int64(s.dsp.efreg[i])
		mixL += int64(float64(v) * g * gl)
		mixR += int64(float64(v) * g * gr)
	}
	// CD returns: EXTS0 and EXTS1 through EFSDL and EFPAN of
	// slots 16 and 17.
	for ch, v := range []int32{int32(cdL), int32(cdR)} {
		w := s.slotReg(16+ch, SlotOutSel)
		g := sendGain(int(w>>5) & 7)
		if g == 0 || v == 0 {
			continue
		}
		gl, gr := panGains(int(w & 0x1F))
		mixL += int64(float64(v) * g * gl)
		mixR += int64(float64(v) * g * gr)
	}

	mvol := float64(s.reg16(RegMaster)&0x000F) / 15.0
	l := clamp16(int64(float64(mixL) * mvol))
	r := clamp16(int64(float64(mixR) * mvol))
	s.audio = append(s.audio, l, r)

	s.serviceDMA()
	s.deliverInterrupts()
	s.runCPU()
	s.writeMirrors()
}

// pollPage edge processes the page registers the chip owns.
func (s *SCSP) pollPage() {
	// KYONEX: any slot word 0 with bit 15 set executes key on and
	// off for every slot from its KYONB bit, then clears.
	kyonex := false
	for i := 0; i < 32; i++ {
		if s.slotReg(i, SlotCtrl)&0x8000 != 0 {
			kyonex = true
		}
	}
	if kyonex {
		for i := 0; i < 32; i++ {
			off := i*slotStride + SlotCtrl
			w := s.reg16(off)
			s.setReg16(off, w&^0x8000)
			if w&0x4000 != 0 {
				s.keyOn(i)
			} else {
				s.keyOff(i)
			}
		}
	}

	// Interrupt reset registers: a nonzero word clears the
	// matching pending bits and is then zeroed, as hardware does.
	if v := s.reg16(RegSCIRE); v != 0 {
		s.sciPend &^= v & 0x07FF
		s.setReg16(RegSCIRE, 0)
	}
	if v := s.reg16(RegMCIRE); v != 0 {
		s.mciPend &^= v & 0x07FF
		s.mainSent &^= v & 0x07FF
		s.setReg16(RegMCIRE, 0)
	}

	// Manual request bits: bit 5 of SCIPD written by the main CPU
	// requests the sound CPU; bit 5 of MCIPD written by the sound
	// CPU requests the main CPU. The page words are mirrors of the
	// internal pending state, so a set bit 5 that is not already
	// pending is a fresh request.
	if s.reg16(RegSCIPD)&(1<<IntCPU) != 0 && s.sciPend&(1<<IntCPU) == 0 {
		s.sciPend |= 1 << IntCPU
	}
	if s.reg16(RegMCIPD)&(1<<IntCPU) != 0 && s.mciPend&(1<<IntCPU) == 0 {
		s.mciPend |= 1 << IntCPU
	}

	// Timer presets: a changed TIMx byte restarts that timer from
	// the new preset.
	for i, off := range []int{RegTimerA, RegTimerB, RegTimerC} {
		p := uint8(s.reg16(off))
		if p != s.timers[i].preset {
			s.timers[i].preset = p
			s.timers[i].counter = p
			s.timers[i].phase = 0
		}
	}

	// DMA trigger: DEXE set starts a transfer at this tick. The
	// bit is cleared in the page, as hardware clears it at the end
	// of the transfer, which is instant in this model.
	ctrl := s.reg16(RegDMACtrl)
	if ctrl&0x2000 != 0 && !s.dma.active {
		low := s.reg16(RegDMALow)
		high := s.reg16(RegDMAHigh)
		s.dma.active = true
		s.dma.ramWord = (uint32(high>>12)&0xF)<<16 | uint32(low>>1)
		s.dma.regWord = uint32(high>>1) & 0x7FF
		s.dma.remaining = int(ctrl>>1) & 0x7FF
		s.dma.dirRegToRAM = ctrl&0x4000 != 0
		s.dma.gate = ctrl&0x8000 != 0
		s.setReg16(RegDMACtrl, ctrl&^0x2000)
	}
}

// tickTimers advances the three prescaled up counters. A counter
// that reaches FFH raises its interrupt source and reloads from
// its preset, so the interrupt period is 255 minus the preset
// prescaled ticks (a preset of FEH interrupts every tick).
func (s *SCSP) tickTimers() {
	for i, off := range []int{RegTimerA, RegTimerB, RegTimerC} {
		ctl := int(s.reg16(off)>>8) & 7
		t := &s.timers[i]
		t.phase++
		if t.phase < (1 << uint(ctl)) {
			continue
		}
		t.phase = 0
		t.counter++
		if t.counter == 0xFF {
			s.raiseSource(IntTimerA + i)
			t.counter = t.preset
		}
	}
}

// raiseSource sets a source bit in both pending registers; the two
// enable registers decide delivery per side.
func (s *SCSP) raiseSource(bit int) {
	s.sciPend |= 1 << uint(bit)
	s.mciPend |= 1 << uint(bit)
}

// interruptLevel returns the autovector level programmed for a
// source. SCILV0, SCILV1 and SCILV2 hold bits 0, 1 and 2 of the
// level code. Sources 0 to 6 use their own column; sources 7 to
// 10 share column 7, per the manual level register figure.
func (s *SCSP) interruptLevel(src int) int {
	col := src
	if col > 7 {
		col = 7
	}
	l0 := (s.reg16(RegSCILV0) >> uint(col)) & 1
	l1 := (s.reg16(RegSCILV1) >> uint(col)) & 1
	l2 := (s.reg16(RegSCILV2) >> uint(col)) & 1
	return int(l0 | l1<<1 | l2<<2)
}

// deliverInterrupts fires the main CPU callback for rising enabled
// main bits and latches the best enabled sound CPU interrupt.
func (s *SCSP) deliverInterrupts() {
	mciEnabled := s.mciPend & s.reg16(RegMCIEB) & 0x07FF
	rising := mciEnabled &^ s.mainSent
	if rising != 0 {
		s.mainSent |= rising
		if s.OnSoundRequest != nil {
			for b := 0; b <= 10; b++ {
				if rising&(1<<uint(b)) != 0 {
					s.OnSoundRequest()
				}
			}
		}
	}
	// Bits that are no longer pending and enabled stop being
	// signalled, so a later raise fires again.
	s.mainSent &= mciEnabled

	sciEnabled := s.sciPend & s.reg16(RegSCIEB) & 0x07FF
	best := 0
	for b := 0; b <= 10; b++ {
		if sciEnabled&(1<<uint(b)) != 0 {
			if lv := s.interruptLevel(b); lv > best {
				best = lv
			}
		}
	}
	if best > 0 {
		s.cpu.Interrupt(best)
	}
}

// serviceDMA runs a latched transfer instantly at tick
// granularity and raises the DMA end source.
func (s *SCSP) serviceDMA() {
	if !s.dma.active {
		return
	}
	s.dma.active = false
	for n := 0; n < s.dma.remaining; n++ {
		ramOff := int(s.dma.ramWord+uint32(n)) * 2
		regOff := int(s.dma.regWord+uint32(n)) * 2
		switch {
		case s.dma.gate:
			// DGATE zero clear: the destination is filled with 0
			// and the source is not read.
			if s.dma.dirRegToRAM {
				if ramOff+1 < len(s.ram) {
					s.ram[ramOff], s.ram[ramOff+1] = 0, 0
				}
			} else {
				if regOff+1 < len(s.regs) {
					s.regs[regOff], s.regs[regOff+1] = 0, 0
				}
			}
		case s.dma.dirRegToRAM:
			if ramOff+1 < len(s.ram) && regOff+1 < len(s.regs) {
				s.ram[ramOff] = s.regs[regOff]
				s.ram[ramOff+1] = s.regs[regOff+1]
			}
		default:
			if ramOff+1 < len(s.ram) && regOff+1 < len(s.regs) {
				s.regs[regOff] = s.ram[ramOff]
				s.regs[regOff+1] = s.ram[ramOff+1]
			}
		}
	}
	s.raiseSource(IntDMAEnd)
}

// runCPU spends this tick's 256 cycle 68EC000 budget.
func (s *SCSP) runCPU() {
	budget := cpuPerSample
	for budget > 0 {
		budget -= s.cpu.Step()
	}
}

// writeMirrors updates the page words the chip owns as outputs:
// the pending registers, the MIDI status (input empty, output
// empty), the monitor call address for the MSLC slot, and the DSP
// internal buffer areas.
func (s *SCSP) writeMirrors() {
	s.setReg16(RegSCIPD, s.sciPend)
	s.setReg16(RegMCIPD, s.mciPend)

	// MIDI stub: input FIFO empty (MIEMP), output FIFO empty
	// (MOEMP), no data in MIBUF.
	s.setReg16(RegMIDIIn, 0x4800)

	mon := s.reg16(RegMonitor)
	mslc := int(mon>>11) & 0x1F
	ca := 0
	if mslc < 32 {
		ca = int(s.slots[mslc].phase>>16) >> 12 & 0x7FF
	}
	s.setReg16(RegMonitor, mon&0xF800|uint16(ca))

	s.dsp.writePage(s)
}

func clamp16(v int64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// decibel gain helpers. The manual tables are in decibels; the
// gain is 10 to the power of dB over 20, computed, not tabulated.
func dbGain(db float64) float64 {
	if db <= -96 {
		return 0
	}
	return math.Pow(10, db/20)
}

// sendGain maps a 3 bit send level (DISDL, EFSDL, IMXL) through
// the manual table: 0 is no send, 1 to 7 are minus 36 dB up to
// 0 dB in 6 dB steps.
func sendGain(v int) float64 {
	if v == 0 {
		return 0
	}
	return dbGain(float64(v-7) * 6)
}

// panGains maps a 5 bit pan value through the manual pan table:
// 00H and 10H are centre, 01H to 0FH move right by attenuating
// the left channel 3 dB per step (0FH is full right), and 11H to
// 1FH move left the same way (1FH is full left).
func panGains(pan int) (float64, float64) {
	switch {
	case pan == 0x00 || pan == 0x10:
		return 1, 1
	case pan == 0x0F:
		return 0, 1
	case pan == 0x1F:
		return 1, 0
	case pan < 0x10:
		return dbGain(-3 * float64(pan)), 1
	default:
		return 1, dbGain(-3 * float64(pan-0x10))
	}
}

var errState = errors.New("scsp: bad serialized state")
