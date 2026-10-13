package system

// PS-X EXE loading (PS1-3). The header layout is the
// documented one from psx-spx (CDROM File Formats,
// EXE file header): an 0x800-byte header starting
// with the ASCII magic "PS-X EXE", with the initial
// PC and GP, the text address and size, and the
// stack address and size; the saved-register fields
// at 0x38 and up are ignored, as the brief requires
// and as most loaders do. The text image follows the
// header.
//
// LoadEXE resets the machine first (the loaded disc,
// if any, stays attached), copies the text to its
// address, and sets the entry registers: PC = pc0,
// GP = gp0, and the stack per the documented rule:
// when s_addr is nonzero, SP and FP are
// s_addr + s_size; when it is zero the stack is the
// kernel default stack top (0x801FFF00, the value at
// the documented user-memory default).

import "fmt"

// exeHeaderSize is the documented PS-X EXE header
// size; the text image starts at this offset.
const exeHeaderSize = 0x800

// exeDefaultStack is the documented default stack
// top used when the header carries no stack address.
const exeDefaultStack = 0x801FFF00

// LoadEXE loads a PS-X EXE image into the machine
// and leaves it ready to run from the entry point.
func (m *Machine) LoadEXE(data []byte) error {
	if len(data) < exeHeaderSize {
		return fmt.Errorf("system: EXE too small: %d bytes, header is %d", len(data), exeHeaderSize)
	}
	if string(data[0:8]) != "PS-X EXE" {
		return fmt.Errorf("system: bad EXE magic %q, want \"PS-X EXE\"", data[0:8])
	}
	le := func(off int) uint32 { return get32(data[off:]) }
	pc0 := le(0x10)
	gp0 := le(0x14)
	tAddr := le(0x18)
	tSize := le(0x1C)
	sAddr := le(0x30)
	sSize := le(0x34)
	if uint64(exeHeaderSize)+uint64(tSize) > uint64(len(data)) {
		return fmt.Errorf("system: EXE text size %d exceeds file size %d", tSize, len(data))
	}
	m.Reset()
	text := data[exeHeaderSize : exeHeaderSize+tSize]
	for i, b := range text {
		m.Bus.Write8(tAddr+uint32(i), b)
	}
	m.CPU.PC = pc0
	m.CPU.NextPC = pc0 + 4
	m.CPU.Regs[28] = gp0 // $gp
	sp := uint32(exeDefaultStack)
	if sAddr != 0 {
		sp = sAddr + sSize
	}
	m.CPU.Regs[29] = sp // $sp
	m.CPU.Regs[30] = sp // $fp
	return nil
}
