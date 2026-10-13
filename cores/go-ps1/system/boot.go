package system

// Disc boot (PS1-4). A real PlayStation boots a
// disc by reading SYSTEM.CNF from the ISO9660
// filesystem, taking the executable path from its
// BOOT line, and loading that PS-X EXE. BootDisc
// reproduces exactly that sequence over the loaded
// disc image: no boot ROM is involved, the same way
// LoadEXE boots bare executables. Only the BOOT
// line is honored; the TCB, EVENT and STACK lines
// some SYSTEM.CNF files carry configure the real
// kernel's thread and event tables, which this
// core's BIOS HLE allocates on demand instead.

import (
	"fmt"
	"strings"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/cd"
)

// BootDisc boots the currently loaded disc: it
// parses the disc's ISO9660 filesystem, reads
// SYSTEM.CNF, and loads the PS-X EXE its BOOT line
// names, leaving the machine at the EXE entry point
// with the disc still attached (the guest reads its
// data files from it). It returns an error when the
// disc has no readable SYSTEM.CNF, no BOOT line, or
// no readable boot executable.
func (m *Machine) BootDisc() error {
	if m.Disc == nil {
		return fmt.Errorf("system: BootDisc with no disc loaded")
	}
	iso, err := cd.OpenISO(m.Disc)
	if err != nil {
		return fmt.Errorf("system: disc boot: %w", err)
	}
	cnf, err := iso.ReadFile("SYSTEM.CNF")
	if err != nil {
		return fmt.Errorf("system: disc boot: %w", err)
	}
	path, err := BootPath(string(cnf))
	if err != nil {
		return err
	}
	exe, err := iso.ReadFile(path)
	if err != nil {
		return fmt.Errorf("system: disc boot: %w", err)
	}
	if err := m.LoadEXE(exe); err != nil {
		return fmt.Errorf("system: disc boot: %w", err)
	}
	return nil
}

// BootPath extracts the boot executable path from
// SYSTEM.CNF contents. The BOOT line looks like:
//
//	BOOT = cdrom:\SLES_123.45;1
//
// The "cdrom:" device prefix and a leading slash
// are stripped; the returned path is in the ISO
// reader's form (slash-separated, the ";1" version
// suffix optional).
func BootPath(cnf string) (string, error) {
	for _, line := range strings.Split(cnf, "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "BOOT") {
			continue
		}
		val = strings.TrimSpace(val)
		if i := strings.Index(val, ":"); i >= 0 && strings.EqualFold(val[:i], "cdrom") {
			val = val[i+1:]
		}
		val = strings.TrimLeft(val, "\\/")
		val = strings.ReplaceAll(val, "\\", "/")
		if val == "" {
			return "", fmt.Errorf("system: SYSTEM.CNF BOOT line names no file")
		}
		return val, nil
	}
	return "", fmt.Errorf("system: SYSTEM.CNF has no BOOT line")
}
