// Package system wires the Dreamcast parts into a running machine:
// the SH-4 CPU from chips/sh4, the physical bus, a GD-ROM image
// from the gd package, the Holly interrupt controller, the
// PowerVR2 from the pvr package (DC-2), and, since DC-3, the
// AICA sound block with its ARM7 CPU, the Maple controller bus,
// and the G2 DMA controller.
//
// Timing model: the CPU charges one cycle per instruction and a
// frame is a fixed budget of FrameCycles (200 MHz divided by 60).
// RunFrame advances the CPU in scanline-sized chunks and steps
// the SPG between chunks, so vblank and hblank interrupts are
// delivered at line accuracy inside the frame. The AICA advances
// in the same chunk rhythm from an exact rational accumulator:
// AICA master cycles = SH-4 cycles * 225792 / 2000000 (the
// 22.5792 MHz AICA master against the 200 MHz SH-4 clock). The
// framebuffer is the PVR display output (FB_R_SOF1 presented per
// FB_R_CTRL), composed at the end of each RunFrame. Controller
// state set with SetButtons is delivered to games over the Maple
// bus on port 0.
package system

import (
	"encoding/binary"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/aica"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/bus"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/chips/sh4"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/g2dma"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/gd"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/holly"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/maple"
	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/pvr"
)

// Display geometry and the flat frame budget.
const (
	Width       = 640
	Height      = 480
	FrameCycles = 3333333 // 200 MHz / 60, slice 1 flat model
)

// Pad button bits for PadState.Mask, in the order documented at
// the top of this file's pad section: face buttons first, then
// Start, then the digital cross, then the digital triggers.
const (
	BtnA     uint16 = 1 << 0
	BtnB     uint16 = 1 << 1
	BtnX     uint16 = 1 << 2
	BtnY     uint16 = 1 << 3
	BtnStart uint16 = 1 << 4
	BtnUp    uint16 = 1 << 5
	BtnDown  uint16 = 1 << 6
	BtnLeft  uint16 = 1 << 7
	BtnRight uint16 = 1 << 8
	BtnLT    uint16 = 1 << 9  // left trigger, digital bit
	BtnRT    uint16 = 1 << 10 // right trigger, digital bit
)

// PadState is one Dreamcast controller report: a button mask, the
// two analog triggers (0 to 255) and the analog stick, where 128
// is centred on each axis.
type PadState struct {
	Mask     uint16
	TriggerL uint8
	TriggerR uint8
	JoyX     uint8
	JoyY     uint8
}

// stateVersion is byte 0 of a serialized machine state. Version
// 3 (DC-3) appends the AICA state (including the ARM7 CPU), the
// Maple state (including the VMU images), the G2 DMA state, and
// a small machine tail (the AICA clock accumulator remainder
// and the Maple vblank trigger edge state) after the DC-2
// sections. Version 4 (DC-4) appends the BIOS HLE block (the
// boot flag, the GD request queue, and the unimplemented-call
// diagnostics) after the machine tail.
const stateVersion = 0x04

// machineTailSize is the size of the trailing machine block:
// the AICA clock accumulator remainder (u64) and the Maple
// vblank trigger edge flag (u64, 0 or 1). Both steer future
// stepping, so both are part of the state.
const machineTailSize = 16

// aicaHolly adapts the Holly controller to the AICA's external
// interrupt sink (the AICA raises ISTEXT bit 1).
type aicaHolly struct{ h *holly.Controller }

func (s aicaHolly) RaiseExternal(bits uint32) { s.h.Raise(holly.BankExternal, bits) }
func (s aicaHolly) ClearExternal(bits uint32) { s.h.ClearExternal(bits) }

// mapleHolly adapts the Holly controller to the Maple event sink
// (Maple DMA completion raises the ISTNRM Maple DMA done event).
type mapleHolly struct{ h *holly.Controller }

func (s mapleHolly) RaiseNormal(bits uint32) { s.h.Raise(holly.BankNormal, bits) }
func (s mapleHolly) ClearNormal(bits uint32) {
	s.h.Write32(0x005F6900, bits)
}

// g2Holly adapts the Holly controller to the G2 DMA event sink.
type g2Holly struct{ h *holly.Controller }

func (s g2Holly) RaiseNormal(bits uint32) { s.h.Raise(holly.BankNormal, bits) }

// Machine is one powered-on Dreamcast.
type Machine struct {
	CPU   *sh4.CPU
	Bus   *bus.Bus
	Disc  *gd.Image
	PVR   *pvr.PVR
	Holly *holly.Controller
	AICA  *aica.AICA
	Maple *maple.Maple
	G2DMA *g2dma.Controller

	framebuffer []uint32
	pad         PadState
	discPath    string
	bios        biosState

	// aicaAcc is the numerator remainder of the SH-4 to AICA
	// master cycle conversion (denominator 2000000).
	aicaAcc uint64
	// vblankOut tracks the previous Holly vblank-out bit so the
	// Maple hardware trigger fires once per vblank-out edge.
	vblankOut bool
}

// New builds a machine (bus plus CPU) and resets the CPU. The
// Holly controller presents interrupts to the CPU; the PVR owns
// its register window, its two FIFO ports, and the shared VRAM.
// The AICA owns its register window, its RTC window, and the
// shared AICA RAM slice (which stays bus-owned for DMA and state
// serialization). The Maple controller and the G2 DMA controller
// own their register windows.
func New() *Machine {
	b := bus.New()
	cpu := sh4.New(b)
	h := holly.New(cpu)
	p := pvr.New(b.VRAM, h)
	snd := aica.New(b.AICARAM, aicaHolly{h})
	mp := maple.New(b, mapleHolly{h})
	g2 := g2dma.New(b, snd, g2Holly{h})
	// Attach order is priority order: the TA FIFO range spans
	// 0x00500000 to 0x00600000, which contains the Holly, Maple,
	// G2, and PVR register windows, so every register device
	// must attach before the two FIFO ports.
	b.Attach(0x005F6900, 0x005F6940, h, false)
	b.Attach(0x005F8000, 0x005FA000, p, false)
	b.Attach(0x005F6C00, 0x005F6D00, mp, false)
	b.Attach(0x005F7800, 0x005F7900, g2, false)
	b.Attach(0x00700000, 0x00708000, snd, false)
	b.Attach(0x00710000, 0x0071000C, snd, false)
	b.Attach(0x00500000, 0x00600000, p.TAPort(), true)
	b.Attach(0x00600000, 0x00601000, p.YUVPort(), true)
	m := &Machine{
		Bus:         b,
		CPU:         cpu,
		PVR:         p,
		Holly:       h,
		AICA:        snd,
		Maple:       mp,
		G2DMA:       g2,
		framebuffer: make([]uint32, Width*Height),
		pad:         PadState{JoyX: 128, JoyY: 128},
	}
	for port := 0; port < 4; port++ {
		mp.SetPad(port, maple.PadState{JoyX: 128, JoyY: 128})
	}
	m.CPU.Reset()
	return m
}

// LoadDisc opens a GD-ROM image (.gdi, .iso or .bin) and holds it
// as the machine's disc. The open error, if any, propagates to the
// caller and any previously loaded disc stays in place.
func (m *Machine) LoadDisc(path string) error {
	img, err := gd.Open(path)
	if err != nil {
		return err
	}
	if m.Disc != nil {
		_ = m.Disc.Close()
	}
	m.Disc = img
	m.discPath = path
	return nil
}

// DiscPath returns the path the current disc was loaded from, or
// the empty string when no disc is loaded.
func (m *Machine) DiscPath() string { return m.discPath }

// LoadProgram is a test and tooling helper: it writes code bytes
// into the bus at the physical address pa and points the CPU at
// the matching P1 cached alias, so a program placed in main RAM
// (pa 0x0C000000) starts executing at virtual 0x8C000000.
func (m *Machine) LoadProgram(pa uint32, code []byte) {
	for i, c := range code {
		m.Bus.Write8(pa+uint32(i), c)
	}
	m.CPU.PC = 0x80000000 | pa
}

// SetPC sets the CPU program counter to the virtual address va.
func (m *Machine) SetPC(va uint32) { m.CPU.PC = va }

// RunFrame runs one frame of CPU time. The CPU advances in
// scanline-sized chunks with the SPG stepped between chunks, so
// SPG interrupts pend and are accepted inside the frame at line
// accuracy. The AICA advances in the same chunk rhythm by the
// exact clock ratio. A rising edge of the vblank-out event fires
// the Maple hardware trigger for tables programmed with
// SB_MDTSEL = 1. At the end of the frame the PVR display output
// is composed into the framebuffer.
func (m *Machine) RunFrame() {
	remaining := FrameCycles
	for remaining > 0 {
		chunk := m.PVR.CyclesPerLine()
		if chunk <= 0 || chunk > remaining {
			chunk = remaining
		}
		m.stepCycles(chunk)
		m.PVR.Advance(chunk)
		m.advanceAICA(chunk)
		m.pollMapleTrigger()
		remaining -= chunk
	}
	m.PVR.Present(m.framebuffer, Width, Height)
}

// stepCycles executes n flat-model cycles of SH-4 code. Each
// cycle is one instruction step (the DC-1 model charges every
// instruction one cycle), and each step first offers the
// program counter to the BIOS HLE: a PC sitting on a handler
// stub is a syscall, serviced in Go (see bios.go). A serviced
// syscall still costs its one cycle.
func (m *Machine) stepCycles(n int) {
	for i := 0; i < n; i++ {
		if m.stepBIOS() {
			continue
		}
		m.CPU.Step()
	}
}

// SystemRAM returns the machine's 16 MB main RAM, the
// RETRO_MEMORY_SYSTEM_RAM block for the shell.
func (m *Machine) SystemRAM() []byte { return m.Bus.RAM }

// VMUImage returns the live 128 KB storage image of port 0's
// VMU, the RETRO_MEMORY_SAVE_RAM block for the shell.
func (m *Machine) VMUImage() []byte { return m.Maple.VMUImage(0) }

// advanceAICA converts a span of SH-4 cycles to AICA master
// cycles with the exact ratio 225792/2000000 and steps the sound
// block, carrying the remainder so long runs drift by zero.
func (m *Machine) advanceAICA(sh4Cycles int) {
	m.aicaAcc += uint64(sh4Cycles) * 225792
	master := int(m.aicaAcc / 2000000)
	m.aicaAcc %= 2000000
	if master > 0 {
		m.AICA.AdvanceMaster(master)
	}
}

// pollMapleTrigger fires the Maple vblank-triggered transfer
// once per vblank-out event edge.
func (m *Machine) pollMapleTrigger() {
	out := m.Holly.Status(holly.BankNormal)&(1<<holly.EvtVBlankOut) != 0
	if out && !m.vblankOut {
		m.Maple.OnVblankOut()
	}
	m.vblankOut = out
}

// AudioBuffer drains the stereo samples the AICA produced since
// the last call, at AudioSampleRate.
func (m *Machine) AudioBuffer() [][2]int16 { return m.AICA.AudioBuffer() }

// AudioSampleRate is the AICA output sample rate.
func (m *Machine) AudioSampleRate() int { return aica.AudioSampleRate() }

// Framebuffer returns the 640 by 480 XRGB8888 frame, composed
// from the PVR display framebuffer at the end of RunFrame. Before
// the first RunFrame it is all zero. The returned slice is the
// machine's own buffer.
func (m *Machine) Framebuffer() []uint32 { return m.framebuffer }

// SetButtons records the current controller state and feeds it
// to the Maple controller on port 0.
func (m *Machine) SetButtons(p PadState) {
	m.pad = p
	m.Maple.SetPad(0, maple.PadState{
		Buttons:  p.Mask,
		TriggerL: p.TriggerL,
		TriggerR: p.TriggerR,
		JoyX:     p.JoyX,
		JoyY:     p.JoyY,
	})
}

// Buttons returns the last controller state set with SetButtons.
func (m *Machine) Buttons() PadState { return m.pad }

// fixedStateSize is the byte size of every section of a machine
// state except the PVR section, whose length depends on the TA
// scene it holds. The AICA, Maple, and G2 DMA blocks are fixed
// size and sit after the PVR section.
func (m *Machine) fixedStateSize() int {
	return 1 + sh4.SerializeSize +
		len(m.Bus.RAM) + len(m.Bus.VRAM) + len(m.Bus.Flash) + len(m.Bus.AICARAM) +
		holly.SerializeSize +
		aica.SerializeSize + maple.SerializeSize + g2dma.SerializeSize +
		machineTailSize + biosStateSize
}

// StateSize is the exact byte size of a serialized machine state
// for the machine's current state.
func (m *Machine) StateSize() int {
	return m.fixedStateSize() + m.PVR.SerializeSize()
}

// Serialize captures the whole machine state. Layout:
//
//	byte 0        version (0x04)
//	bytes 1..     CPU state (sh4.SerializeSize bytes)
//	then          main RAM   (len(Bus.RAM) bytes)
//	then          VRAM       (len(Bus.VRAM) bytes)
//	then          flash      (len(Bus.Flash) bytes)
//	then          AICA RAM   (len(Bus.AICARAM) bytes)
//	then          Holly state (holly.SerializeSize bytes)
//	then          PVR state (variable length, see pvr.Serialize)
//	then          AICA state (aica.SerializeSize bytes, with ARM)
//	then          Maple state (maple.SerializeSize bytes)
//	then          G2 DMA state (g2dma.SerializeSize bytes)
//	then          machine tail (machineTailSize bytes)
//	then          BIOS HLE block (biosStateSize bytes, DC-4)
//
// The register stub storage of the remaining stub ranges is not
// part of the state: those registers have no behaviour yet, so
// nothing a game could observe through them is lost.
func (m *Machine) Serialize() []byte {
	out := make([]byte, m.StateSize())
	out[0] = stateVersion
	off := 1
	copy(out[off:], m.CPU.Serialize())
	off += sh4.SerializeSize
	copy(out[off:], m.Bus.RAM)
	off += len(m.Bus.RAM)
	copy(out[off:], m.Bus.VRAM)
	off += len(m.Bus.VRAM)
	copy(out[off:], m.Bus.Flash)
	off += len(m.Bus.Flash)
	copy(out[off:], m.Bus.AICARAM)
	off += len(m.Bus.AICARAM)
	copy(out[off:], m.Holly.Serialize())
	off += holly.SerializeSize
	pvrLen := m.PVR.SerializeSize()
	copy(out[off:], m.PVR.Serialize())
	off += pvrLen
	m.AICA.Serialize(out[off:])
	off += aica.SerializeSize
	m.Maple.Serialize(out[off:])
	off += maple.SerializeSize
	m.G2DMA.Serialize(out[off:])
	off += g2dma.SerializeSize
	binary.LittleEndian.PutUint64(out[off:], m.aicaAcc)
	if m.vblankOut {
		binary.LittleEndian.PutUint64(out[off+8:], 1)
	}
	off += machineTailSize
	copy(out[off:], m.serializeBIOS())
	return out
}

// Deserialize restores a state produced by Serialize. It returns
// false, leaving the live machine alone as far as the checks can
// guarantee, when the length does not match the sections the
// state itself describes, the version byte differs, or a section
// rejects its block.
func (m *Machine) Deserialize(b []byte) bool {
	fixed := m.fixedStateSize()
	if len(b) < fixed || b[0] != stateVersion {
		return false
	}
	head := 1 + sh4.SerializeSize +
		len(m.Bus.RAM) + len(m.Bus.VRAM) + len(m.Bus.Flash) + len(m.Bus.AICARAM) +
		holly.SerializeSize
	pvrLen := len(b) - fixed
	if pvr.SerializedSize(b[head:head+pvrLen]) != pvrLen {
		return false
	}
	off := 1
	if !m.CPU.Deserialize(b[off : off+sh4.SerializeSize]) {
		return false
	}
	off += sh4.SerializeSize
	copy(m.Bus.RAM, b[off:])
	off += len(m.Bus.RAM)
	copy(m.Bus.VRAM, b[off:])
	off += len(m.Bus.VRAM)
	copy(m.Bus.Flash, b[off:])
	off += len(m.Bus.Flash)
	copy(m.Bus.AICARAM, b[off:])
	off += len(m.Bus.AICARAM)
	if !m.Holly.Deserialize(b[off : off+holly.SerializeSize]) {
		return false
	}
	off += holly.SerializeSize
	if !m.PVR.Deserialize(b[off : off+pvrLen]) {
		return false
	}
	off += pvrLen
	if !m.AICA.Deserialize(b[off : off+aica.SerializeSize]) {
		return false
	}
	off += aica.SerializeSize
	if !m.Maple.Deserialize(b[off : off+maple.SerializeSize]) {
		return false
	}
	off += maple.SerializeSize
	if !m.G2DMA.Deserialize(b[off : off+g2dma.SerializeSize]) {
		return false
	}
	off += g2dma.SerializeSize
	m.aicaAcc = binary.LittleEndian.Uint64(b[off:])
	m.vblankOut = binary.LittleEndian.Uint64(b[off+8:]) != 0
	off += machineTailSize
	m.deserializeBIOS(b[off : off+biosStateSize])
	return true
}
