// Package gte is the PlayStation geometry engine
// (coprocessor 2) for the go-ps1 core.
//
// The register file (32 data and 32 control registers)
// and the full fixed-point command set are implemented
// from the psx-spx "Geometry Transformation Engine
// (GTE)" chapter: register access side effects (SXY
// FIFO push, IRGB/ORGB conversion, LZCS/LZCR), the
// FLAG saturation and overflow bits, and all commands
// in the documented opcode table, including the
// Unsigned Newton-Raphson division used by RTPS/RTPT.
package gte

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrNotImplemented is retained for API compatibility.
// Execute no longer returns it: every documented
// opcode is implemented.
var ErrNotImplemented = errors.New("gte: command not implemented in this slice")

// ErrUnknownCommand is returned by Execute for a
// command opcode outside the documented table.
var ErrUnknownCommand = errors.New("gte: unknown command opcode")

// Command describes one documented GTE command.
type Command struct {
	Opcode uint32
	Name   string
	Cycles int
}

// Commands is the documented GTE command dispatch
// table (real opcode in the low 6 bits of the COP2
// word) with the per-command cycle counts from the
// psx-spx "GTE Opcode Summary" table.
//
// Note: an earlier draft of this table carried the
// wrong real opcodes for SQR, DPCT and AVSZ3 (it had
// AVSZ3 at 15h, which is AVSZ3's fake opcode, SQR at
// 2Ah and DPCT at 2Dh). The psx-spx real-opcode table
// says SQR=28h, DPCT=2Ah, AVSZ3=2Dh; those are the
// values below.
var Commands = []Command{
	{0x01, "RTPS", 15},
	{0x06, "NCLIP", 8},
	{0x0C, "OP", 6},
	{0x10, "DPCS", 8},
	{0x11, "INTPL", 8},
	{0x12, "MVMVA", 8},
	{0x13, "NCDS", 19},
	{0x14, "CDP", 13},
	{0x2D, "AVSZ3", 5},
	{0x16, "NCDT", 44},
	{0x1B, "NCCS", 17},
	{0x1C, "CC", 11},
	{0x1E, "NCS", 14},
	{0x20, "NCT", 30},
	{0x28, "SQR", 5},
	{0x29, "DCPL", 8},
	{0x2A, "DPCT", 17},
	{0x2E, "AVSZ4", 6},
	{0x30, "RTPT", 23},
	{0x3D, "GPF", 5},
	{0x3E, "GPL", 5},
	{0x3F, "NCCT", 39},
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

// CommandCycles returns the documented cycle count
// for a GTE command opcode from the psx-spx opcode
// summary table, or 0 if the opcode is undocumented.
func CommandCycles(opcode uint32) int {
	if cmd, ok := commandByOpcode[opcode&0x3F]; ok {
		return cmd.Cycles
	}
	return 0
}

// StateSize is the serialized size of the register
// file: 32 data plus 32 control registers.
const StateSize = 64 * 4

// flagErrorMask selects the FLAG bits that are ORed
// into FLAG bit 31 on read: bits 30..23 and 18..13
// (psx-spx, "GTE Saturation": FLAG register layout).
const flagErrorMask = 0x7F800000 | 0x0007E000

// GTE is the coprocessor 2 register file.
type GTE struct {
	data    [32]uint32
	control [32]uint32

	lastCommand uint32
	unsupported int // count of commands refused with ErrUnknownCommand
}

// New returns a GTE in reset state (all registers zero).
func New() *GTE { return &GTE{} }

// Data reads a GTE data register (0-31).
//
// Documented read side effects (psx-spx, "GTE
// Registers"): register 15 (SXYP) reads back SXY2;
// registers 28 (IRGB) and 29 (ORGB) both read as the
// 5:5:5 collapse of IR1, IR2, IR3 (ORGB is a read-only
// mirror of IRGB). All other registers are plain
// storage.
func (g *GTE) Data(reg uint32) uint32 {
	switch reg & 31 {
	case 15: // SXYP: mirror of SXY2
		return g.data[14]
	case 28, 29: // IRGB, ORGB: converted from IR1..IR3
		return g.collapsedRGB()
	}
	return g.data[reg&31]
}

// SetData writes a GTE data register (0-31).
//
// Documented write side effects (psx-spx, "GTE
// Registers"): writing register 15 (SXYP) pushes the
// SXY FIFO (SXY0=SXY1, SXY1=SXY2, SXY2=value); writing
// register 28 (IRGB) expands the 5:5:5 fields into
// IR1, IR2, IR3 (each field multiplied by 80h);
// writing register 29 (ORGB) is ignored (read-only);
// writing register 30 (LZCS) updates register 31
// (LZCR) with the leading sign-bit count. All other
// registers are plain storage.
func (g *GTE) SetData(reg uint32, v uint32) {
	switch reg & 31 {
	case 15: // SXYP: move-on-write FIFO push
		g.data[12] = g.data[13]
		g.data[13] = g.data[14]
		g.data[14] = v
	case 28: // IRGB: expand 5:5:5 into IR1, IR2, IR3
		g.data[9] = (v & 0x1F) << 7
		g.data[10] = ((v >> 5) & 0x1F) << 7
		g.data[11] = ((v >> 10) & 0x1F) << 7
	case 29: // ORGB: read-only, writes ignored
	case 30: // LZCS: recompute LZCR
		g.data[30] = v
		g.data[31] = uint32(leadingSignBits(v))
	default:
		g.data[reg&31] = v
	}
}

// collapsedRGB converts IR1, IR2, IR3 to the 5:5:5
// value read from IRGB and ORGB: each 16-bit component
// divided by 80h and saturated to 00h..1Fh (psx-spx,
// "cop2r29 - ORGB"; no FLAG bits are set by this).
func (g *GTE) collapsedRGB() uint32 {
	var out uint32
	for i := 0; i < 3; i++ {
		c := int32(int16(uint16(g.data[9+i]))) / 0x80
		if c < 0 {
			c = 0
		}
		if c > 0x1F {
			c = 0x1F
		}
		out |= uint32(c) << (5 * i)
	}
	return out
}

// leadingSignBits counts the leading bits of v that
// are equal to its sign bit (psx-spx, LZCS/LZCR: the
// leading zero count for positive values, the leading
// one count for negative values, in the range 1..32).
func leadingSignBits(v uint32) int {
	sign := v >> 31
	n := 0
	for i := 31; i >= 0; i-- {
		if (v>>uint(i))&1 != sign {
			break
		}
		n++
	}
	return n
}

// Control reads a GTE control register (0-31).
//
// Register 31 (FLAG) reads with bit 31 set to the OR
// of the documented error bits (bits 30..23 and
// 18..13) and bits 11..0 always zero (psx-spx, "GTE
// Saturation"). All other registers are plain storage.
func (g *GTE) Control(reg uint32) uint32 {
	if reg&31 == 31 {
		v := g.control[31] & 0x7FFFF000
		if g.control[31]&flagErrorMask != 0 {
			v |= 0x80000000
		}
		return v
	}
	return g.control[reg&31]
}

// SetControl writes a GTE control register (0-31).
// Writing FLAG (register 31) stores the written bits;
// the next command overwrites them (psx-spx: all FLAG
// bits are automatically reset at the begin of a new
// GTE command).
func (g *GTE) SetControl(reg uint32, v uint32) { g.control[reg&31] = v }

// LastCommand returns the most recent documented
// command word passed to Execute, or 0 if none.
func (g *GTE) LastCommand() uint32 { return g.lastCommand }

// Unsupported returns how many commands Execute has
// refused as unknown opcodes.
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
