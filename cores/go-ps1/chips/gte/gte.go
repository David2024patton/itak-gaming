// Package gte is the PlayStation geometry engine
// (coprocessor 2) for the go-ps1 core.
//
// PS1-1 scope is deliberately narrow: the register
// file exists (32 data registers and 32 control
// registers with plain storage semantics) and the
// command dispatch table exists, but no GTE math is
// implemented. Execute on a real command returns
// ErrNotImplemented and changes no register; the full
// fixed-point implementation is PS1-3 work. Nothing
// here fakes a computed result.
package gte

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrNotImplemented is returned by Execute for a known
// GTE command whose math is not implemented yet.
var ErrNotImplemented = errors.New("gte: command not implemented in this slice")

// ErrUnknownCommand is returned by Execute for a
// command opcode outside the documented table.
var ErrUnknownCommand = errors.New("gte: unknown command opcode")

// Command describes one documented GTE command.
type Command struct {
	Opcode uint32
	Name   string
}

// Commands is the documented GTE command dispatch
// table (opcode in the low 6 bits of the COP2 word).
// Every entry is unimplemented in PS1-1; the table is
// where PS1-3 hangs the real fixed-point bodies.
var Commands = []Command{
	{0x01, "RTPS"},
	{0x30, "RTPT"},
	{0x06, "NCLIP"},
	{0x0C, "OP"},
	{0x10, "DPCS"},
	{0x11, "INTPL"},
	{0x12, "MVMVA"},
	{0x13, "NCDS"},
	{0x14, "CDP"},
	{0x16, "NCDT"},
	{0x1B, "NCCS"},
	{0x1C, "CC"},
	{0x1E, "NCS"},
	{0x20, "NCT"},
	{0x2A, "SQR"},
	{0x29, "DCPL"},
	{0x2D, "DPCT"},
	{0x15, "AVSZ3"},
	{0x2E, "AVSZ4"},
	{0x3D, "GPF"},
	{0x3E, "GPL"},
	{0x3F, "NCCT"},
}

var commandByOpcode = func() map[uint32]Command {
	m := make(map[uint32]Command, len(Commands))
	for _, cmd := range Commands {
		m[cmd.Opcode] = cmd
	}
	return m
}()

// CommandName returns the documented mnemonic for a
// GTE command opcode, if it has one.
func CommandName(opcode uint32) (string, bool) {
	cmd, ok := commandByOpcode[opcode&0x3F]
	return cmd.Name, ok
}

// StateSize is the serialized size of the register
// file: 32 data plus 32 control registers.
const StateSize = 64 * 4

// GTE is the coprocessor 2 register file.
type GTE struct {
	data    [32]uint32
	control [32]uint32

	lastCommand uint32
	unsupported int // count of commands refused with ErrNotImplemented
}

// New returns a GTE in reset state (all registers zero).
func New() *GTE { return &GTE{} }

// Data reads a GTE data register (0-31).
func (g *GTE) Data(reg uint32) uint32 { return g.data[reg&31] }

// SetData writes a GTE data register (0-31). Storage
// only in this slice; the read-only and saturating
// registers of the real chip get their documented
// behavior with the math in PS1-3.
func (g *GTE) SetData(reg uint32, v uint32) { g.data[reg&31] = v }

// Control reads a GTE control register (0-31).
func (g *GTE) Control(reg uint32) uint32 { return g.control[reg&31] }

// SetControl writes a GTE control register (0-31).
func (g *GTE) SetControl(reg uint32, v uint32) { g.control[reg&31] = v }

// Execute dispatches one COP2 command word. In PS1-1
// every documented command returns ErrNotImplemented
// and leaves the register file untouched.
func (g *GTE) Execute(cmd uint32) error {
	opcode := cmd & 0x3F
	if _, ok := commandByOpcode[opcode]; !ok {
		return fmt.Errorf("%w: 0x%02X", ErrUnknownCommand, opcode)
	}
	g.lastCommand = cmd
	g.unsupported++
	return fmt.Errorf("%w: %s", ErrNotImplemented, commandByOpcode[opcode].Name)
}

// Command is the r3000a.Cop2 entry point. The CPU has
// no error channel for coprocessor commands, so the
// refusal is recorded (see Unsupported) and the
// register file is left exactly as it was.
func (g *GTE) Command(cmd uint32) { _ = g.Execute(cmd) }

// LastCommand returns the most recent command word
// that Execute refused, or 0 if none.
func (g *GTE) LastCommand() uint32 { return g.lastCommand }

// Unsupported returns how many commands Execute has
// refused as unimplemented.
func (g *GTE) Unsupported() int { return g.unsupported }

// Serialize returns the register file as a fixed-size
// block.
func (g *GTE) Serialize() []byte {
	out := make([]byte, StateSize)
	off := 0
	for _, v := range g.data {
		binary.LittleEndian.PutUint32(out[off:], v)
		off += 4
	}
	for _, v := range g.control {
		binary.LittleEndian.PutUint32(out[off:], v)
		off += 4
	}
	return out
}

// Deserialize restores a Serialize block.
func (g *GTE) Deserialize(in []byte) error {
	if len(in) != StateSize {
		return fmt.Errorf("gte: state size %d, want %d", len(in), StateSize)
	}
	off := 0
	for i := range g.data {
		g.data[i] = binary.LittleEndian.Uint32(in[off:])
		off += 4
	}
	for i := range g.control {
		g.control[i] = binary.LittleEndian.Uint32(in[off:])
		off += 4
	}
	return nil
}
