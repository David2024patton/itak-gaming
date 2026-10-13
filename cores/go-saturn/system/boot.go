package system

import "fmt"

// HLE boot constants, from the published Saturn boot process.
//
// The boot ROM loads the initial program (IP) from the start
// of the disc data area into Work RAM-H at IPLoadAddress. The
// first 256 bytes of the loaded area are the System ID itself,
// and the boot ROM enters the IP at IPEntryAddress, the first
// byte after the System ID (the security code area of a
// licensed IP). On the way it copies the System ID to
// SysIDCopyAddress and the 32 parameter bytes at IP offset
// 0xE0 (the IP size, stack and first read fields) to
// BootParamsAddress, both in Work RAM-H, where system software
// reads them back. The master SH-2 stack comes from the
// System ID Stack-M field and the slave stack from Stack-S;
// the slave stays held in reset until the IP releases it with
// the SMPC SSHON command, as real software does.
const (
	IPLoadAddress     = 0x06002000
	IPEntryAddress    = 0x06002100
	SysIDCopyAddress  = 0x06000C00
	BootParamsAddress = 0x060002A0

	// ipLoadRoom is the space available for the IP in Work
	// RAM-H from the load address to the end of the 1 MiB
	// window.
	ipLoadRoom = 0x100000 - 0x2000
)

// BootDisc performs the documented HLE boot of the attached
// disc: the System ID signature is verified (again, from the
// image), the IP named by the header IP size field is loaded
// from the start of the data area to Work RAM-H at
// 0x06002000, the boot ROM's System ID and parameter copies
// are made, and the machine is left in the documented post
// boot state: the master SH-2 executing the IP at 0x06002100
// with the Stack-M stack pointer, the slave SH-2 reset at the
// same entry with the Stack-S stack pointer and held (the IP
// releases it with SMPC SSHON), the SMPC at its power on
// status values, the sound chip and the CD block held off
// until their SMPC ON commands, and the CD block holding the
// attached image at its standby state.
//
// No boot ROM bytes are read or embedded: the BIOS window
// keeps reading zero, and this function replaces the boot
// ROM with the documented transfers above. BootDisc is the
// real boot path used by the libretro shell. The power on
// seeding in Reset is kept for the device gates that seed
// their own programs.
//
// A stack field of zero in the header falls back to the
// conventional 0x06001000 power on stack for that CPU: some
// published headers leave the fields zero and rely on the
// boot area default. An IP size below 256 (the System ID
// alone) or above the Work RAM-H room is an error, as is a
// size the image does not contain.
func (s *System) BootDisc() error {
	if s.disc == nil {
		return ErrNoDisc
	}
	hdr, err := s.disc.SaturnHeader()
	if err != nil {
		return err
	}
	size := int(hdr.IPSize)
	if size < 0x100 {
		return fmt.Errorf("system: IP size %#x is smaller than the 256 byte System ID", hdr.IPSize)
	}
	if size > ipLoadRoom {
		return fmt.Errorf("system: IP size %#x does not fit in Work RAM-H from %#x", hdr.IPSize, uint32(IPLoadAddress))
	}
	// Read the whole IP before touching the machine, so a
	// short or unreadable image fails the boot cleanly.
	ip := make([]byte, 0, size)
	for lba := 0; len(ip) < size; lba++ {
		sec, err := s.disc.ReadLogicalSector(lba)
		if err != nil {
			return fmt.Errorf("system: reading IP sector %d: %w", lba, err)
		}
		if len(sec) == 0 {
			return fmt.Errorf("system: empty sector while reading IP sector %d", lba)
		}
		ip = append(ip, sec...)
	}
	ip = ip[:size]

	// The button mask is host input state, not machine
	// state: it survives the power on reset so a frontend
	// that sets buttons before booting sees them
	// reflected in the booted program's pad reads.
	buttons := s.buttons
	s.Reset()
	s.buttons = buttons
	s.header = hdr
	ram := s.Bus.WorkRAMHigh()
	copy(ram[IPLoadAddress-0x06000000:], ip)
	copy(ram[SysIDCopyAddress-0x06000000:], ip[:0x100])
	copy(ram[BootParamsAddress-0x06000000:], ip[0xE0:0x100])

	masterStack := hdr.MasterStack
	if masterStack == 0 {
		masterStack = 0x06001000
	}
	slaveStack := hdr.SlaveStack
	if slaveStack == 0 {
		slaveStack = 0x06001000
	}
	s.Master.Reset(IPEntryAddress, masterStack)
	s.Slave.Reset(IPEntryAddress, slaveStack)
	s.slaveReleased = false
	return nil
}
