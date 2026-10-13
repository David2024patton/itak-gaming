package system

import (
	"fmt"

	"github.com/davidpatton/itak-gaming/cores/go-dreamcast/gd"
)

// BIOS HLE boot (DC-4), following the documented Dreamcast boot
// sequence. The real BIOS reads the disc's IP.BIN (the first 16
// sectors of the data area, 32 KB) into RAM at 0x8C008000, sets
// up the BIOS state in low RAM, and jumps to the IP's entry at
// 0x8C008300; the IP then loads the boot file named in its own
// header (offset 0x60, conventionally 1ST_READ.BIN) through the
// GD-ROM syscall interface and jumps to it at 0x8C010000.
//
// BootDisc performs exactly the machine side of that contract.
// No Sega BIOS bytes exist or run anywhere in the process: the
// syscall vectors point at this core's own handler stubs (see
// bios.go), and the work the BIOS would have done between power
// on and the IP entry is done here in Go.
//
// Documented handover state established here:
//
//   - IP.BIN is copied to 0x8C008000 (physical 0x0C008000).
//   - The BIOS variables page 0x8C000000 to 0x8C001000 is
//     cleared, then the syscall vector table is written at its
//     documented slots: sysinfo 0x8C0000B0, font 0x8C0000B4,
//     flashrom 0x8C0000B8, GD-ROM 0x8C0000BC and 0x8C0000C0,
//     system boot 0x8C0000E0, and the GD-ROM direct entry
//     pointer at 0x8C0010F0.
//   - The SYSINFO id block lives at 0x8C000068 (24 bytes). It
//     stays zeroed: the real BIOS copies it from the flash
//     sysinfo partition, which this core does not model.
//   - The CPU enters at PC 0x8C008300 with SP 0x8C00F000 (the
//     boot stack region the public boot documentation places
//     just below the 0x8C00F000 vector and scratch block) and
//     PR 0. SR keeps the CPU's power-on value; a real IP sets
//     its own SR, VBR, and stack as its first act, and the
//     fixture IP does exactly that.
//
// The boot file itself is descrambled on retail discs; this
// HLE serves the file bytes as stored, so unscrambled
// (homebrew layout) images are the supported form.

const (
	// IPLoadAddr is the documented IP.BIN load address.
	IPLoadAddr = 0x8C008000
	// IPEntryAddr is the documented IP entry point.
	IPEntryAddr = 0x8C008300
	// BootFileAddr is the documented load address of the boot
	// file the IP loads (1ST_READ.BIN on retail discs).
	BootFileAddr = 0x8C010000
	// bootStackTop is the stack pointer at IP entry.
	bootStackTop = 0x8C00F000
	// biosVarsSize is the cleared BIOS variables area size.
	biosVarsSize = 0x1000
)

// BootDisc boots the loaded disc through the HLE BIOS: it reads
// and validates the disc's IP.BIN with the gd parser, installs
// it and the BIOS handover state in RAM, and enters the IP.
// It fails cleanly, before touching the CPU, when the disc has
// no IP.BIN or the IP.BIN is malformed.
func (m *Machine) BootDisc() error {
	if m.Disc == nil {
		return fmt.Errorf("system: BootDisc with no disc loaded")
	}
	ipRaw, err := m.Disc.BootArea()
	if err != nil {
		return fmt.Errorf("system: read IP.BIN: %w", err)
	}
	if _, err := gd.ParseIPBin(ipRaw); err != nil {
		return fmt.Errorf("system: parse IP.BIN: %w", err)
	}

	m.CPU.Reset()
	m.bios = biosState{}

	// The BIOS variables page starts cleared, as does the rest
	// of the IP's footprint (IP.BIN overwrites 0x8000 on).
	for i := 0; i < biosVarsSize; i++ {
		m.Bus.RAM[i] = 0
	}
	copy(m.Bus.RAM[0x8000:], ipRaw)

	// The documented syscall vector table. The slots are
	// named by their virtual addresses (0x8C0000B0 and
	// family); the bus routes physical addresses, so these
	// are the physical forms (RAM offsets 0xB0 and 0x10F0).
	wr := func(pa, v uint32) { m.Bus.Write32(pa, v) }
	wr(0x0C0000B0, biosStubAddr(stubSysinfo))
	wr(0x0C0000B4, biosStubAddr(stubFont))
	wr(0x0C0000B8, biosStubAddr(stubFlash))
	wr(0x0C0000BC, biosStubAddr(stubGD))
	wr(0x0C0000C0, biosStubAddr(stubGD))
	wr(0x0C0000E0, biosStubAddr(stubSystem))
	// The GD-ROM direct entry pointer in the stub block.
	wr(0x0C0010F0, biosStubAddr(stubGDEntry))

	// Post-BIOS CPU state: enter the IP.
	m.CPU.PC = IPEntryAddr
	m.CPU.R[15] = bootStackTop
	m.CPU.PR = 0
	m.bios.booted = true
	return nil
}

// Reset powers the machine back to its post-boot state: a
// fresh machine with the same disc (if any) booted again
// through BootDisc. The host pad state is preserved across the
// reset, the way a held controller survives a console reset.
func (m *Machine) Reset() {
	disc := m.Disc
	discPath := m.discPath
	pad := m.pad
	*m = *New()
	m.pad = pad
	m.SetButtons(pad)
	if disc != nil {
		m.Disc = disc
		m.discPath = discPath
		// BootDisc can only fail on a disc that already
		// booted once; ignore the result.
		_ = m.BootDisc()
	}
}
