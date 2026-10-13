package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
)

// The HLE boot (slice N64-4). This core contains no PIF
// boot ROM and no CIC code: the boot below is an
// original reimplementation of the documented process
// (n64brew PIF-NUS "Console startup" and Initial
// Program Load pages), reduced to the effects the rest
// of the chain depends on:
//
//   - The CIC family (detected from the ROM's own boot
//     area by the cart package, or set with SetCIC, or
//     the 6102 default) selects the documented seed.
//   - The IPL3 checksum is computed over the ROM's own
//     bytes (0x1000..0x101000, the published algorithm)
//     and compared with the header words at 0x10/0x14.
//     The result is reported through BootChecksumOK and
//     never enforced: like common emulators, boot
//     proceeds either way.
//   - The PIF's side of the exchange (the CIC seed
//     handshake and the IPL2 checksum verification) is
//     modeled as passed: PIF RAM carries the documented
//     post-boot state (boot info word at 0x24 with the
//     region and both seeds, command byte 0x80 up),
//     without reproducing the PIF ROM's challenge math.
//   - The ROM's first 0x1000 bytes (header + IPL3) are
//     placed in RSP DMEM exactly as IPL2 leaves them,
//     and the CPU starts at the documented IPL3 entry
//     0xA4000040 with the documented entry state: CP0
//     Status 0x34000000 and Config 0x0006E463 (IPL1's
//     initialization), stack at 0xA4001FF0, the IPL3
//     seed in $s6, and the TV type (NTSC = 1; this
//     core's console region is NTSC) in $s4.
//
// A ROM whose IPL3 is the genuine article therefore
// runs it on this core's CPU and RSP, the standard
// HLE-boot arrangement used by multiple emulators;
// the IPL3 then performs the documented 1 MB copy to
// the header entry address itself.

// Start resets the machine and runs the HLE boot: the
// CPU is left executing the cartridge's IPL3 from RSP
// DMEM. LoadROM alone does not boot (it leaves the
// machine at the reset vector for low-level tests).
func (s *System) Start() {
	s.Reset()
	s.boot()
}

// Booted reports whether Start has run the boot.
func (s *System) Booted() bool { return s.booted }

// BootCIC returns the CIC family the boot used (after
// detection, SetCIC, and the 6102 default resolve).
func (s *System) BootCIC() cart.CIC { return s.bootCIC }

// BootChecksum returns the IPL3 checksum words computed
// at boot over the ROM's own bytes.
func (s *System) BootChecksum() (uint32, uint32) { return s.bootCRC1, s.bootCRC2 }

// BootChecksumOK reports whether the computed IPL3
// checksum matched the header words. Boot proceeds
// regardless; this is a report, not a gate.
func (s *System) BootChecksumOK() bool { return s.bootChecksumOK }

// SetCIC overrides the CIC family for the next boot.
// Real cartridges are identified by the cart package's
// CRC32 detection over the ROM's own boot code, which
// a synthetic ROM cannot reproduce (its boot area is
// original code, not the copyrighted original), so
// tests and homebrew can name the family explicitly.
// Without detection or an override the boot defaults
// to CIC 6102, the family the large majority of
// cartridges use.
func (s *System) SetCIC(c cart.CIC) {
	s.cicOverride = c
	s.cicSet = true
}

func (s *System) boot() {
	cic := s.Cart.CIC
	if s.cicSet {
		cic = s.cicOverride
	}
	if cic == cart.CICUnknown {
		cic = cart.CIC6102
	}
	s.bootCIC = cic
	seed, _ := cart.CICSeed(cic)

	if c1, c2, ok := cart.IPL3Checksum(s.Cart.ROM, cic); ok {
		s.bootCRC1, s.bootCRC2 = c1, c2
		s.bootChecksumOK = c1 == s.Cart.CRC1 && c2 == s.Cart.CRC2
	}

	// PIF RAM post-boot state: the boot info word at
	// 0x24 carries the region (0x01 = NTSC) and the two
	// CIC seeds, and the command byte raises 0x80 to
	// signal the data is available. The exact byte
	// packing of the info word beyond its documented
	// contents (region + both seeds in this word) is
	// this core's HLE arrangement.
	p := s.Mem.PIF
	for i := range p.RAM {
		p.RAM[i] = 0
	}
	p.RAM[0x24] = 0x01 // region: NTSC
	p.RAM[0x25] = seed // IPL2 seed
	p.RAM[0x26] = seed // IPL3 seed (the same byte in every known variant)
	p.RAM[0x3F] = 0x80

	// IPL3 placement: DMEM gets the ROM's first
	// 0x1000 bytes (the header at 0x00, the boot code
	// at 0x40), as IPL2 leaves it.
	n := 0x1000
	if len(s.Cart.ROM) < n {
		n = len(s.Cart.ROM)
	}
	copy(s.RSP.DMEM[:], s.Cart.ROM[:n])

	// CPU entry state (documented on IPL3 entry).
	cpu := s.CPU
	cpu.CP0[r4300.Cp0Status] = 0x34000000
	cpu.CP0[r4300.Cp0Config] = 0x0006E463
	cpu.Reg[29] = 0xFFFFFFFFA4001FF0 // sp, top of DMEM
	cpu.Reg[22] = uint64(seed)       // $s6: IPL3 seed
	cpu.Reg[20] = 1                  // $s4: TV type, NTSC
	cpu.PC = 0xFFFFFFFFA4000040      // IPL3 entry in DMEM
	cpu.NPC = cpu.PC + 4
	s.booted = true
}
