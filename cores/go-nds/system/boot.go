package system

import (
	"github.com/davidpatton/itak-gaming/cores/go-nds/chips/armcore"
	"github.com/davidpatton/itak-gaming/cores/go-nds/periph"
)

// Direct boot, following the GBATEK direct-boot
// initialization: no BIOS runs, so the machine itself
// performs the documented setup the BIOS would have done.
//
//   - The ARM9 binary is staged through the ARM9 view with
//     WRAMCNT = 3 (all shared WRAM owned by the ARM9), then
//     the ARM7 binary through the ARM7 view with WRAMCNT = 0
//     (all shared WRAM owned by the ARM7). WRAMCNT is left at
//     0: at boot the shared WRAM belongs to the ARM7 and
//     games reprogram it (documented choice).
//   - The 0x200-byte cartridge header is copied to
//     0x027FFE00, the chip ID to 0x027FF800 and 0x027FF804,
//     the header bytes 0x20-0x3F to 0x027FFC00, and the boot
//     flag halfword 0x0001 to 0x027FFFFA.
//   - POWCNT1 is set to 0x020F (LCDs on, 2D engines on, 3D
//     engine on, displays not swapped).
//   - Each CPU's entry register state: CPSR = system mode
//     with interrupts enabled in the CPSR (the global IME is
//     still 0, so no interrupt can fire until the program
//     enables it), SVC stack 0x03007FE0 / IRQ stack
//     0x03007FA0 on the ARM9 (0x0380FDC0 / 0x0380FD80 on the
//     ARM7), user stack at the end of the ARM9 binary's load
//     region (0x0380FE00 on the ARM7), and r12 = r14 = the
//     CPU's header entry point. CP15 keeps the power-on TCM
//     configuration installed by reset.
//   - The CPUs stay halted until Start.

// Documented direct-boot stack addresses.
const (
	bootSVCStack9 = 0x03007FE0
	bootIRQStack9 = 0x03007FA0
	bootSVCStack7 = 0x0380FDC0
	bootIRQStack7 = 0x0380FD80
	bootUSRStack7 = 0x0380FE00
)

func (m *Machine) directBoot() {
	h := m.header
	m.Bus.SetROM(m.rom)
	m.CPU9.Reset()
	m.CPU7.Reset()
	m.CPU9.Halted = true
	m.CPU7.Halted = true

	if h.ARM9Size > 0 {
		m.Bus.SetWRAMCNT(3)
		m.Bus.LoadBytes(true, h.ARM9Load, h.ARM9Binary(m.rom))
	}
	if h.ARM7Size > 0 {
		m.Bus.SetWRAMCNT(0)
		m.Bus.LoadBytes(false, h.ARM7Load, h.ARM7Binary(m.rom))
	}
	m.Bus.SetWRAMCNT(0)

	// The boot block in main RAM.
	w8 := func(addr uint32, b []byte) { m.Bus.LoadBytes(true, addr, b) }
	hdr := make([]byte, 0x200)
	copy(hdr, m.rom)
	w8(0x027FFE00, hdr)
	id := periph.ChipID
	w8(0x027FF800, []byte{byte(id), byte(id >> 8), byte(id >> 16), byte(id >> 24)})
	w8(0x027FF804, []byte{byte(id), byte(id >> 8), byte(id >> 16), byte(id >> 24)})
	w8(0x027FFC00, m.rom[0x20:0x40])
	w8(0x027FFFFA, []byte{0x01, 0x00})

	// POWCNT1: LCDs and both 2D engines (and the 3D engine
	// power bit) on, no swap.
	m.Bus.IO9[0x304] = 0x0F
	m.Bus.IO9[0x305] = 0x02

	m.bootRegs(m.CPU9, h.ARM9Entry, h.ARM9Load+h.ARM9Size, bootSVCStack9, bootIRQStack9)
	m.bootRegs(m.CPU7, h.ARM7Entry, bootUSRStack7, bootSVCStack7, bootIRQStack7)
}

// bootRegs installs one CPU's entry register state. The banked
// stack pointers go into the armcore bank fields; the active
// (system mode) stack pointer and the link register hold the
// user stack and the entry address, and r12 holds the entry
// address as well, per the boot contract.
func (m *Machine) bootRegs(c *armcore.CPU, entry, usrSP, svcSP, irqSP uint32) {
	c.RSVC[0] = svcSP
	c.RSVC[1] = 0
	c.RIRQ[0] = irqSP
	c.RIRQ[1] = 0
	c.RUSR[0] = usrSP
	c.RUSR[1] = entry
	c.R[12] = entry
	c.R[13] = usrSP
	c.R[14] = entry
	c.CPSR = armcore.ModeSYS
}
