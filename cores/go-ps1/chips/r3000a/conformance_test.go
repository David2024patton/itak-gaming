package r3000a

import (
	"os"
	"testing"
)

// TestTomHarteMIPSCorpus is the family-pattern
// presence-gated conformance slot. For PS1-1 the check
// was made against the TomHarte/ProcessorTests tree:
// it contains 6502, 65816, 680x0, 8088, spc700 and
// 65C02 variants, and no MIPS or PSX single-step set.
// If a corpus directory ever appears under testroms,
// this test says so instead of passing silently; the
// CPU is meanwhile gated on the documentation-derived
// unit tests in this package.
func TestTomHarteMIPSCorpus(t *testing.T) {
	candidates := []string{
		"../../testroms/tomharte-mips",
		"../../testroms/tomharte-psx",
		"../../testroms/mips",
		"../../testroms/psx",
	}
	for _, d := range candidates {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			t.Skipf("corpus directory %s exists, but PS1-1 ships no harness for its schema; see the core README", d)
		}
	}
	t.Skip("no MIPS single-step corpus present: TomHarte/ProcessorTests has no mips or psx set (checked for PS1-1); CPU gated on unit tests")
}
