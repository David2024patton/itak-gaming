package system

// PS1-4 system gates: disc boot through SYSTEM.CNF,
// the diagnostics tally, and the host-forced analog
// pad mode. The disc fixture is the homebrew
// package's synthetic ISO9660 image carrying the
// enginetest EXE, so the boot gate re-runs the
// whole guest end to end from a file, the way a
// real disc boot would reach it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-ps1/homebrew"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestBootDiscEnginetest(t *testing.T) {
	img := homebrew.EnginetestISO(homebrew.EnginetestEXE())
	m := New()
	if err := m.OpenDisc(writeTemp(t, "ENGTEST.ISO", img)); err != nil {
		t.Fatalf("OpenDisc: %v", err)
	}
	if err := m.BootDisc(); err != nil {
		t.Fatalf("BootDisc: %v", err)
	}
	passed := false
	for chunk := 0; chunk < 40 && !passed; chunk++ {
		m.RunCycles(1000000)
		if m.Bus.Read32(0x80020000) == 0x600DF00D {
			passed = true
		}
	}
	if !passed {
		t.Fatalf("guest PASS magic not reached from disc boot: stage marker %d, unimplemented BIOS calls %d",
			m.Bus.Read32(0x80020100), m.UnimplementedBIOSCallCount())
	}
	if got := m.Bus.Read32(0x80020100); got != 6 {
		t.Errorf("final stage marker actual %d, want 6", got)
	}
	// The guest read ISO sector 0 (all zeros) for its
	// CD stage, so the recorded checksum is 0, unlike
	// the in-process gate's patterned disc.
	if got := m.Bus.Read32(0x80020104); got != 0 {
		t.Errorf("CD checksum actual 0x%08X, want 0 (zero sector 0)", got)
	}
	if d := m.Diagnostics(); !d.Empty() {
		t.Errorf("diagnostics after clean boot actual %q, want empty", d.String())
	}
	if w, h := m.DisplayWidth(), m.DisplayHeight(); w != 320 || h != 240 {
		t.Errorf("display size actual %dx%d, want 320x240", w, h)
	}
}

func TestBootDiscErrors(t *testing.T) {
	m := New()
	if err := m.BootDisc(); err == nil {
		t.Error("BootDisc with no disc: got nil error, want an error")
	}
	// A non-ISO disc (the raw sector pattern source)
	// has no SYSTEM.CNF to boot from.
	if err := m.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	if err := m.BootDisc(); err == nil {
		t.Error("BootDisc on a non-ISO disc: got nil error, want an error")
	}
	// A bad EXE magic behind a good SYSTEM.CNF.
	bad := homebrew.EnginetestISO([]byte("not an exe"))
	if err := m.OpenDisc(writeTemp(t, "BAD.ISO", bad)); err != nil {
		t.Fatalf("OpenDisc: %v", err)
	}
	if err := m.BootDisc(); err == nil {
		t.Error("BootDisc with a corrupt boot file: got nil error, want an error")
	}
}

func TestBootPath(t *testing.T) {
	for _, c := range []struct {
		cnf  string
		want string
	}{
		{"BOOT = cdrom:\\ENGTEST.EXE;1\r\nTCB = 4\r\n", "ENGTEST.EXE;1"},
		{"boot=cdrom:\\GAMES\\DEMO.EXE;1\n", "GAMES/DEMO.EXE;1"},
		{"BOOT = cdrom:/PLAIN.EXE\n", "PLAIN.EXE"},
		{"  BOOT  =  cdrom:\\SPACED.EXE;1  \n", "SPACED.EXE;1"},
	} {
		got, err := BootPath(c.cnf)
		if err != nil {
			t.Errorf("BootPath(%q): unexpected error %v", c.cnf, err)
		} else if got != c.want {
			t.Errorf("BootPath(%q) actual %q, want %q", c.cnf, got, c.want)
		}
	}
	for _, cnf := range []string{"", "TCB = 4\r\n", "BOOT = cdrom:\r\n"} {
		if _, err := BootPath(cnf); err == nil {
			t.Errorf("BootPath(%q): got nil error, want an error", cnf)
		}
	}
}

func TestDiagnosticsTally(t *testing.T) {
	m := New()
	if err := m.LoadDisc(&seqDisc{sectors: 16}); err != nil {
		t.Fatalf("LoadDisc: %v", err)
	}
	// One unimplemented BIOS call: A(7Fh).
	a := homebrew.NewAsm()
	a.Li(homebrew.A0, 0)
	a.CallBIOS(0xA0, 0x7F)
	a.Spin("spin")
	loadProg(m, 0x80010000, a.Build(0x80010000))
	setPC(m, 0x80010000)
	m.RunCycles(10000)
	// One invalid CD command (17h) and one unknown
	// GP1 command (3Fh).
	m.Bus.Write8(0x1F801803, 0x1F)
	m.Bus.Write8(0x1F801801, 0x17)
	m.GPU.WriteGP1(0x3F000000)
	m.GPU.WriteGP1(0x3F000000)

	d := m.Diagnostics()
	if d.Empty() {
		t.Fatal("diagnostics empty, want the three tallies below")
	}
	if d.BIOSCallCount < 1 || len(d.BIOSCalls) < 1 || d.BIOSCalls[0].Table != 'A' || d.BIOSCalls[0].Func != 0x7F {
		t.Errorf("BIOS tally actual count %d log %v, want A(7F) first", d.BIOSCallCount, d.BIOSCalls)
	}
	if d.UnknownCDCount != 1 || len(d.UnknownCD) != 1 || d.UnknownCD[0] != 0x17 {
		t.Errorf("CD tally actual count %d list %v, want one 17h", d.UnknownCDCount, d.UnknownCD)
	}
	if d.UnknownGP1Count != 2 || len(d.UnknownGP1) != 1 || d.UnknownGP1[0] != 0x3F {
		t.Errorf("GP1 tally actual count %d list %v, want two of one 3Fh", d.UnknownGP1Count, d.UnknownGP1)
	}
	if s := d.String(); !strings.Contains(s, "A(7F)") || !strings.Contains(s, "[17]") || !strings.Contains(s, "[3F]") {
		t.Errorf("diagnostics string actual %q, want it to name A(7F), 17 and 3F", s)
	}
	// The tallies survive a machine reset (they are
	// per-load diagnostics, not machine state).
	m.Reset()
	if d2 := m.Diagnostics(); d2.BIOSCallCount != 0 || d2.UnknownCDCount != 1 || d2.UnknownGP1Count != 2 {
		t.Errorf("after Reset: BIOS %d (want 0, BIOS log resets), CD %d (want 1), GP1 %d (want 2)",
			d2.BIOSCallCount, d2.UnknownCDCount, d2.UnknownGP1Count)
	}
}

func TestAnalogModeHostForced(t *testing.T) {
	m := New()
	m.SetAnalogMode(true)
	m.Reset() // LoadEXE and friends reset; the mode must be reapplied
	if !m.PAD.AnalogMode() {
		t.Fatal("pad analog mode actual false after SetAnalogMode(true) and Reset, want true")
	}
	// The pad answers InitPAD2-style fills with the
	// analog ID 73h. Byte 1 of the port 1 buffer is
	// the pad ID; emulate the BIOS fill path by a
	// frame of the InitPAD2 sequence is covered by
	// the PS1-3 BIOS gates, so here the ID itself is
	// probed through a raw poll in the pad gate
	// style: run the guest from TestBIOSInitPAD2Fill
	// shaped code is overkill; the direct evidence
	// is the mode flag plus the digital default.
	m.SetAnalogMode(false)
	m.Reset()
	if m.PAD.AnalogMode() {
		t.Error("pad analog mode actual true after SetAnalogMode(false), want false")
	}
}
