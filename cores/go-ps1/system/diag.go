package system

// First-real-game diagnostics (PS1-4). The BIOS HLE
// covers a documented subset; a real game that
// calls outside it, or issues CD controller / GP1
// commands outside the implemented sets, fails in
// ways that are invisible from the TV. Diagnostics
// aggregates everything the machine saw but did not
// implement since it was created, so the libretro
// shell can print it to the frontend log at unload
// and a failure becomes a list of exactly what to
// implement next.

import (
	"fmt"
	"strings"
)

// Diagnostics is the machine's not-implemented
// tally: unimplemented BIOS calls (with per-call
// detail), unknown CD controller commands, and
// unknown GP1 commands.
type Diagnostics struct {
	BIOSCalls       []BIOSCall // the BIOS unimplemented-call log (capped at 256 entries, repeats included)
	BIOSCallCount   int        // total unimplemented BIOS calls, including repeats
	UnknownCD       []uint8    // distinct unknown CD command bytes, first-seen order
	UnknownCDCount  int        // total unknown CD commands
	UnknownGP1      []uint8    // distinct unknown GP1 command bytes, first-seen order
	UnknownGP1Count int        // total unknown GP1 commands
}

// Diagnostics returns the machine's current
// not-implemented tally.
func (m *Machine) Diagnostics() Diagnostics {
	return Diagnostics{
		BIOSCalls:       m.UnimplementedBIOSCalls(),
		BIOSCallCount:   m.UnimplementedBIOSCallCount(),
		UnknownCD:       m.CD.UnknownCommands(),
		UnknownCDCount:  m.CD.UnknownCommandCount(),
		UnknownGP1:      m.GPU.UnknownGP1Commands(),
		UnknownGP1Count: m.GPU.UnknownGP1Count(),
	}
}

// Empty reports whether nothing unimplemented was
// seen at all.
func (d Diagnostics) Empty() bool {
	return d.BIOSCallCount == 0 && d.UnknownCDCount == 0 && d.UnknownGP1Count == 0
}

// String renders the tally as compact log lines:
// counts first, then the distinct offenders in hex.
func (d Diagnostics) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "unimplemented BIOS calls: %d", d.BIOSCallCount)
	if len(d.BIOSCalls) > 0 {
		parts := make([]string, 0, len(d.BIOSCalls))
		for _, c := range d.BIOSCalls {
			parts = append(parts, fmt.Sprintf("%c(%02X)", c.Table, c.Func))
		}
		fmt.Fprintf(&b, " [%s]", strings.Join(parts, " "))
	}
	fmt.Fprintf(&b, "; unknown CD commands: %d", d.UnknownCDCount)
	if len(d.UnknownCD) > 0 {
		fmt.Fprintf(&b, " [%s]", hexList(d.UnknownCD))
	}
	fmt.Fprintf(&b, "; unknown GP1 commands: %d", d.UnknownGP1Count)
	if len(d.UnknownGP1) > 0 {
		fmt.Fprintf(&b, " [%s]", hexList(d.UnknownGP1))
	}
	return b.String()
}

func hexList(bs []uint8) string {
	parts := make([]string, len(bs))
	for i, v := range bs {
		parts[i] = fmt.Sprintf("%02X", v)
	}
	return strings.Join(parts, " ")
}
