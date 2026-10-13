// Package apu is the SNES audio processing unit: the
// S-SMP (SPC700 CPU, 64 KB RAM, timers and I/O ports, IPL
// boot ROM) together with the S-DSP in dsp.go. The main
// CPU talks to it through the four communication ports;
// the SPC700 runs its own program in APU RAM and drives
// the DSP through ports 00F2h/00F3h. Behaviour follows
// the published hardware documentation in fullsnes
// (SNES APU chapters).
package apu

import (
	"encoding/binary"
	"errors"

	"github.com/davidpatton/itak-gaming/cores/go-snes/chips/spc700"
)

// iplROM is the documented 64 byte SPC700 boot ROM at
// FFC0h..FFFFh, as listed (disassembly) in fullsnes,
// SNES APU Main CPU Communication Port chapter. It
// zerofills RAM 0001h..00EFh, posts AAh to output port 0
// and BBh to output port 1, then runs the block transfer
// protocol: on a CCh kick (later kicks are the running
// counter plus 2, odd) it reads a destination from input
// ports 2/3 and a command from input port 1, echoes each
// data byte counter on output port 0 while storing the
// bytes from input port 1, and on the end counter either
// loops for another block (command nonzero) or jumps to
// the address on ports 2/3 (command zero). The last two
// bytes are the reset vector, FFC0h.
var iplROM = [64]byte{
	0xCD, 0xEF, 0xBD, 0xE8, 0x00, 0xC6, 0x1D, 0xD0,
	0xFC, 0x8F, 0xAA, 0xF4, 0x8F, 0xBB, 0xF5, 0x78,
	0xCC, 0xF4, 0xD0, 0xFB, 0x2F, 0x19, 0xEB, 0xF4,
	0xD0, 0xFC, 0x7E, 0xF4, 0xD0, 0x0B, 0xE4, 0xF5,
	0xCB, 0xF4, 0xD7, 0x00, 0xFC, 0xD0, 0xF3, 0xAB,
	0x01, 0x10, 0xEF, 0x7E, 0xF4, 0x10, 0xEB, 0xBA,
	0xF6, 0xDA, 0x00, 0xBA, 0xF4, 0xC4, 0xF4, 0xDD,
	0x5D, 0xD0, 0xDB, 0x1F, 0x00, 0x00, 0xC0, 0xFF,
}

// timerState is one of the three SPC timers: an SPC cycle
// divider (tick the stage every 128 cycles for T0/T1,
// every 16 for T2), an 8 bit stage counter that is
// compared against the target, and the 4 bit output
// counter read through ports 00FDh..00FFh.
type timerState struct {
	div   int
	stage int
	out   int
}

var timerTickPeriod = [3]int{128, 128, 16}

// Serialization layout (all integers little endian):
//
//	byte 0      version (1)
//	bytes 1..   64 KB RAM
//	then        SPC700 state (spc700.SerializeSize bytes)
//	then        SMP block (smpSerializeSize bytes)
//	then        DSP block (dspSerializeSize bytes)
const serializeVersion = 1
const smpSerializeSize = 31

// dspVoiceSerializeSize is the serialized size of one
// DSP voice; see serializeDSP for the field layout.
const dspVoiceSerializeSize = 29
const dspSerializeSize = 128 + 2 + 8*dspVoiceSerializeSize + 6 + 16 + 16

// SerializeSize is the exact size of APU.Serialize
// output: version byte + RAM + CPU state + SMP state +
// DSP state.
const SerializeSize = 1 + 65536 + spc700.SerializeSize + smpSerializeSize + dspSerializeSize

// APU is the whole audio unit. It implements the
// spc700.Bus interface: the CPU is attached to the APU
// itself.
type APU struct {
	ram [65536]byte
	cpu *spc700.CPU
	dsp *DSP

	control  uint8 // last CONTROL (00F1h) write
	testReg  uint8 // last TEST (00F0h) write, stored only
	dspAddr  uint8 // DSPADDR (00F2h) latch
	inPorts  [4]uint8
	outPorts [4]uint8

	targets [3]uint8
	timers  [3]timerState

	dspPhase   int // SPC cycles accumulated toward the next DSP sample, 0..31
	pendingCPU int // SPC cycles still owed to the CPU (instruction overshoot)
	audio      []int16
}

// New returns an APU in its reset state: IPL ROM enabled,
// SPC700 at the boot vector, DSP reset.
func New() *APU {
	a := &APU{}
	a.dsp = NewDSP()
	a.dsp.AttachRAM(a.ram[:])
	a.cpu = spc700.New()
	a.cpu.Attach(a)
	a.Reset()
	return a
}

// Reset restores the power on state. CONTROL powers up
// as B0h on hardware (IPL enabled plus both port reset
// strobes); the strobes are momentary, so the stored
// value kept here is 80h: IPL enabled, timers disabled,
// ports cleared.
func (a *APU) Reset() {
	for i := range a.ram {
		a.ram[i] = 0
	}
	a.dsp.Reset()
	a.control = 0x80
	a.testReg = 0x0A
	a.dspAddr = 0
	a.inPorts = [4]uint8{}
	a.outPorts = [4]uint8{}
	a.targets = [3]uint8{}
	a.timers = [3]timerState{}
	a.dspPhase = 0
	a.pendingCPU = 0
	a.audio = nil
	a.cpu.Reset()
}

// RAM returns the 64 KB APU RAM. Tests and the DSP share
// this backing store.
func (a *APU) RAM() []byte { return a.ram[:] }

// CPU returns the SPC700, for tests.
func (a *APU) CPU() *spc700.CPU { return a.cpu }

// DSP returns the S-DSP, for tests.
func (a *APU) DSP() *DSP { return a.dsp }

// ReadPort returns what the main CPU reads at 2140h+i:
// the output latch the SPC side wrote to 00F4h+i.
func (a *APU) ReadPort(i int) uint8 { return a.outPorts[i&3] }

// WritePort delivers a main CPU write to 2140h+i: the
// input latch the SPC side reads at 00F4h+i.
func (a *APU) WritePort(i int, v uint8) { a.inPorts[i&3] = v }

// Step advances the whole APU by n SPC cycles at
// 1.024 MHz. The CPU runs first (instruction overshoot
// is carried in pendingCPU so the long term rate is
// exact), the timers advance by exactly n cycles, and
// the DSP produces one sample per 32 SPC cycles.
func (a *APU) Step(cycles int) {
	if cycles <= 0 {
		return
	}
	a.pendingCPU += cycles
	for a.pendingCPU > 0 {
		c := a.cpu.Step()
		if c <= 0 {
			c = 1
		}
		a.pendingCPU -= c
	}
	for i := range a.timers {
		if a.control>>uint(i)&1 != 0 {
			a.advanceTimer(i, cycles)
		}
	}
	a.dspPhase += cycles
	for a.dspPhase >= 32 {
		a.dspPhase -= 32
		l, r := a.dsp.StepSample()
		a.audio = append(a.audio, l, r)
	}
}

func (a *APU) advanceTimer(i, cycles int) {
	t := &a.timers[i]
	target := int(a.targets[i])
	if target == 0 {
		target = 256
	}
	t.div += cycles
	ticks := t.div / timerTickPeriod[i]
	t.div %= timerTickPeriod[i]
	if ticks == 0 {
		return
	}
	pos := t.stage + ticks
	t.out = (t.out + pos/target) & 0x0F
	t.stage = pos % target
}

// DrainAudio returns the pending stereo samples
// (interleaved L, R at 32000 Hz) and clears the buffer.
func (a *APU) DrainAudio() []int16 {
	out := a.audio
	a.audio = nil
	return out
}

// Read implements spc700.Bus.
func (a *APU) Read(addr uint16) uint8 {
	if addr >= 0xFFC0 && a.control&0x80 != 0 {
		return iplROM[addr-0xFFC0]
	}
	if addr >= 0x00F0 && addr <= 0x00FF {
		return a.readIO(addr)
	}
	return a.ram[addr]
}

// Write implements spc700.Bus. Writes to FFC0h..FFFFh
// always go to RAM, even while the IPL ROM is mapped for
// reads.
func (a *APU) Write(addr uint16, val uint8) {
	if addr >= 0x00F0 && addr <= 0x00FF {
		a.writeIO(addr, val)
		return
	}
	a.ram[addr] = val
}

func (a *APU) readIO(addr uint16) uint8 {
	switch addr {
	case 0xF0, 0xF1: // TEST and CONTROL are write only; reads return 0.
		return 0
	case 0xF2:
		return a.dspAddr
	case 0xF3:
		return a.dsp.ReadReg(a.dspAddr & 0x7F)
	case 0xF4, 0xF5, 0xF6, 0xF7:
		return a.inPorts[addr-0xF4]
	case 0xF8, 0xF9: // aux ports behave as plain RAM cells
		return a.ram[addr]
	case 0xFA, 0xFB, 0xFC: // timer targets are write only
		return 0
	default: // 00FDh..00FFh timer outputs: read returns and clears.
		i := addr - 0xFD
		v := uint8(a.timers[i].out)
		a.timers[i].out = 0
		return v
	}
}

func (a *APU) writeIO(addr uint16, val uint8) {
	switch addr {
	case 0xF0:
		// TEST: the documented safe subset. Reads return
		// 0 and writes are only stored. No TEST bit is
		// acted on: bit 0 and bit 3 would disable the
		// timers, bit 1 would write protect RAM, bit 2
		// crashes the CPU on hardware, and bits 4..7 add
		// waitstates. All of those are ignored here, which
		// matches the doc guidance that software should
		// never change this register from its 0Ah default.
		a.testReg = val
	case 0xF1:
		a.writeControl(val)
	case 0xF2:
		a.dspAddr = val
	case 0xF3:
		// DSP register writes are ignored while the
		// address latch has bit 7 set (those addresses
		// are the read only mirror range).
		if a.dspAddr&0x80 == 0 {
			a.dsp.WriteReg(a.dspAddr&0x7F, val)
		}
	case 0xF4, 0xF5, 0xF6, 0xF7:
		a.outPorts[addr-0xF4] = val
	case 0xF8, 0xF9:
		a.ram[addr] = val
	case 0xFA, 0xFB, 0xFC:
		a.targets[addr-0xFA] = val
	default: // 00FDh..00FFh are read only.
	}
}

func (a *APU) writeControl(val uint8) {
	old := a.control
	for i := 0; i < 3; i++ {
		ob := old >> uint(i) & 1
		nb := val >> uint(i) & 1
		if ob != nb {
			// Any enable change reloads the timer stage
			// divider; disabling also clears the output
			// counter (per the CONTROL register doc).
			a.timers[i].div = 0
			a.timers[i].stage = 0
			if nb == 0 {
				a.timers[i].out = 0
			}
		}
	}
	if val&0x10 != 0 {
		a.inPorts[0] = 0
		a.inPorts[1] = 0
	}
	if val&0x20 != 0 {
		a.inPorts[2] = 0
		a.inPorts[3] = 0
	}
	a.control = val
}

// Serialize returns the full APU state in the layout
// documented at the top of this file. Pending drained
// audio is not part of the state; call DrainAudio first
// if it matters.
func (a *APU) Serialize() []byte {
	out := make([]byte, SerializeSize)
	out[0] = serializeVersion
	copy(out[1:], a.ram[:])
	off := 1 + 65536
	copy(out[off:], a.cpu.Serialize())
	off += spc700.SerializeSize
	a.serializeSMP(out[off : off+smpSerializeSize])
	off += smpSerializeSize
	a.dsp.serialize(out[off : off+dspSerializeSize])
	return out
}

func (a *APU) serializeSMP(b []byte) {
	b[0] = a.control
	b[1] = a.testReg
	b[2] = a.dspAddr
	copy(b[3:7], a.inPorts[:])
	copy(b[7:11], a.outPorts[:])
	copy(b[11:14], a.targets[:])
	for i, t := range a.timers {
		o := 14 + i*4
		binary.LittleEndian.PutUint16(b[o:], uint16(t.div))
		b[o+2] = uint8(t.stage)
		b[o+3] = uint8(t.out)
	}
	b[26] = uint8(a.dspPhase)
	binary.LittleEndian.PutUint32(b[27:], uint32(int32(a.pendingCPU)))
}

// Deserialize restores a state produced by Serialize.
// Continuing to Step afterwards produces bit identical
// audio to the original.
func (a *APU) Deserialize(b []byte) error {
	if len(b) != SerializeSize {
		return errors.New("apu: bad serialized length")
	}
	if b[0] != serializeVersion {
		return errors.New("apu: bad serialized version")
	}
	copy(a.ram[:], b[1:1+65536])
	off := 1 + 65536
	cpuState := b[off : off+spc700.SerializeSize]
	off += spc700.SerializeSize
	a.deserializeSMP(b[off : off+smpSerializeSize])
	off += smpSerializeSize
	// Restore the SMP block before the CPU so the CPU
	// state lands in a machine whose IPL mapping and
	// ports already match the saved ones.
	if err := a.cpu.Deserialize(cpuState); err != nil {
		return err
	}
	a.dsp.deserialize(b[off : off+dspSerializeSize])
	a.audio = nil
	return nil
}

func (a *APU) deserializeSMP(b []byte) {
	a.control = b[0]
	a.testReg = b[1]
	a.dspAddr = b[2]
	copy(a.inPorts[:], b[3:7])
	copy(a.outPorts[:], b[7:11])
	copy(a.targets[:], b[11:14])
	for i := range a.timers {
		o := 14 + i*4
		a.timers[i].div = int(binary.LittleEndian.Uint16(b[o:]))
		a.timers[i].stage = int(b[o+2])
		a.timers[i].out = int(b[o+3])
	}
	a.dspPhase = int(b[26])
	a.pendingCPU = int(int32(binary.LittleEndian.Uint32(b[27:])))
}

// serialize writes the DSP block: register file, ENDX
// and pending KON, the eight voices, then noise, echo
// offset and FIR history.
func (d *DSP) serialize(b []byte) {
	copy(b[0:128], d.regs[:])
	b[128] = d.endx
	b[129] = d.pendingKon
	off := 130
	for i := range d.voices {
		v := &d.voices[i]
		o := off + i*dspVoiceSerializeSize
		binary.LittleEndian.PutUint16(b[o:], v.blockAddr)
		binary.LittleEndian.PutUint16(b[o+2:], v.loopAddr)
		b[o+4] = uint8(v.sampleIdx)
		b[o+5] = v.hdr
		binary.LittleEndian.PutUint16(b[o+6:], uint16(int16(v.filter1)))
		binary.LittleEndian.PutUint16(b[o+8:], uint16(int16(v.filter2)))
		for g := 0; g < 4; g++ {
			binary.LittleEndian.PutUint16(b[o+10+g*2:], uint16(int16(v.gauss[g])))
		}
		binary.LittleEndian.PutUint16(b[o+18:], uint16(v.pitchCounter))
		binary.LittleEndian.PutUint16(b[o+20:], uint16(v.env))
		b[o+22] = v.envState
		binary.LittleEndian.PutUint16(b[o+23:], uint16(v.envCounter))
		b[o+25] = uint8(v.konDelay)
		binary.LittleEndian.PutUint16(b[o+26:], uint16(int16(v.outx)))
		if v.on {
			b[o+28] = 1
		} else {
			b[o+28] = 0
		}
	}
	off += 8 * dspVoiceSerializeSize
	binary.LittleEndian.PutUint16(b[off:], d.noise)
	binary.LittleEndian.PutUint16(b[off+2:], uint16(d.noiseCount))
	binary.LittleEndian.PutUint16(b[off+4:], uint16(d.echoOffset))
	for i := 0; i < 8; i++ {
		binary.LittleEndian.PutUint16(b[off+6+i*2:], uint16(int16(d.firHistL[i])))
		binary.LittleEndian.PutUint16(b[off+22+i*2:], uint16(int16(d.firHistR[i])))
	}
}

func (d *DSP) deserialize(b []byte) {
	ram := d.ram
	*d = DSP{ram: ram}
	copy(d.regs[:], b[0:128])
	d.endx = b[128]
	d.pendingKon = b[129]
	off := 130
	for i := range d.voices {
		v := &d.voices[i]
		o := off + i*dspVoiceSerializeSize
		v.blockAddr = binary.LittleEndian.Uint16(b[o:])
		v.loopAddr = binary.LittleEndian.Uint16(b[o+2:])
		v.sampleIdx = int(b[o+4])
		v.hdr = b[o+5]
		v.filter1 = int(int16(binary.LittleEndian.Uint16(b[o+6:])))
		v.filter2 = int(int16(binary.LittleEndian.Uint16(b[o+8:])))
		for g := 0; g < 4; g++ {
			v.gauss[g] = int(int16(binary.LittleEndian.Uint16(b[o+10+g*2:])))
		}
		v.pitchCounter = int(binary.LittleEndian.Uint16(b[o+18:]))
		v.env = int(binary.LittleEndian.Uint16(b[o+20:]))
		v.envState = b[o+22]
		v.envCounter = int(binary.LittleEndian.Uint16(b[o+23:]))
		v.konDelay = int(b[o+25])
		v.outx = int(int16(binary.LittleEndian.Uint16(b[o+26:])))
		v.on = b[o+28] != 0
	}
	off += 8 * dspVoiceSerializeSize
	d.noise = binary.LittleEndian.Uint16(b[off:])
	d.noiseCount = int(binary.LittleEndian.Uint16(b[off+2:]))
	d.echoOffset = int(binary.LittleEndian.Uint16(b[off+4:]))
	for i := 0; i < 8; i++ {
		d.firHistL[i] = int(int16(binary.LittleEndian.Uint16(b[off+6+i*2:])))
		d.firHistR[i] = int(int16(binary.LittleEndian.Uint16(b[off+22+i*2:])))
	}
}
