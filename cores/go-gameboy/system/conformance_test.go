package system

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Conformance harness for the public Game Boy test ROMs. The ROMs
// live under the module's testroms/ directory (never committed); the
// tests skip cleanly when the directory is absent so a fresh
// checkout still builds, but in the development tree they are
// expected to be present and green.
//
// Blargg cpu_instrs is the GB-1 acceptance gate: any failure fails
// the test. The mooneye-gb acceptance suite is reported per ROM; a
// curated core list (pure CPU and interrupt behavior that GB-1
// claims to model) also gates, while timing- and PPU-bound tests
// are logged with their honest results for GB-2 to pick up.

func testromsDir(t *testing.T) string {
	t.Helper()
	for _, dir := range []string{
		filepath.Join("..", "testroms"),
		filepath.Join("..", "..", "testroms"),
	} {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	t.Skip("testroms/ not present; conformance suites skipped")
	return ""
}

func loadROM(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// runBlargg steps a Blargg ROM until it reports through the serial
// port, and returns the captured text. Blargg verdicts end with the
// word Passed or Failed on their own line.
func runBlargg(t *testing.T, path string, budget int) string {
	t.Helper()
	s, err := New(loadROM(t, path))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for spent := 0; spent < budget; {
		spent += s.Step()
		out := s.Serial()
		if strings.Contains(out, "Passed") || strings.Contains(out, "Failed") {
			break
		}
	}
	return s.Serial()
}

func blarggVerdict(out string) (pass bool, detail string) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "Passed" {
			return true, ""
		}
		if strings.HasPrefix(ln, "Failed") {
			// The failure report lists the offending opcodes in the
			// lines around the verdict; hand back the whole text.
			_ = i
			return false, strings.TrimSpace(out)
		}
	}
	return false, "no verdict printed; serial so far: " + strings.TrimSpace(out)
}

func TestBlarggCPUInstrsIndividual(t *testing.T) {
	dir := filepath.Join(testromsDir(t), "blargg", "individual")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("blargg individual ROMs absent: %v", err)
	}
	var roms []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gb") {
			roms = append(roms, e.Name())
		}
	}
	sort.Strings(roms)
	if len(roms) == 0 {
		t.Skip("no blargg individual ROMs found")
	}
	for _, name := range roms {
		name := name
		t.Run(name, func(t *testing.T) {
			out := runBlargg(t, filepath.Join(dir, name), 200_000_000)
			pass, detail := blarggVerdict(out)
			if pass {
				t.Logf("PASS %s", name)
			} else {
				t.Errorf("FAIL %s\n%s", name, detail)
			}
		})
	}
}

func TestBlarggCPUInstrsCombined(t *testing.T) {
	path := filepath.Join(testromsDir(t), "blargg", "cpu_instrs.gb")
	if _, err := os.Stat(path); err != nil {
		t.Skip("combined cpu_instrs.gb absent")
	}
	out := runBlargg(t, path, 900_000_000)
	pass, detail := blarggVerdict(out)
	if pass {
		t.Logf("PASS cpu_instrs.gb (combined)\n%s", strings.TrimSpace(out))
	} else {
		t.Errorf("FAIL cpu_instrs.gb (combined)\n%s", detail)
	}
}

// mooneyeCore lists acceptance tests that exercise only CPU,
// interrupt, and memory behavior GB-1 claims to model. Failures in
// this list fail the test; everything else is reported in the log.
// Two notable exclusions: interrupts/ie_push.gb probes M-cycle
// internal dispatch cancellation while the return address is being
// pushed (GB-1 dispatches within a single step), and
// halt_ime0_ei.gb needs a real VBlank interrupt source; its CPU
// semantics (EI before HALT gives IME=1 halt behavior) are covered
// by a unit test in the sm83 package instead.
var mooneyeCore = map[string]bool{
	"bits/reg_f.gb":       true,
	"bits/mem_oam.gb":     true,
	"ei_sequence.gb":      true,
	"halt_ime1_timing.gb": true,
	"rapid_di_ei.gb":      true,
}

// runMooneye steps a mooneye ROM until it reaches its quit
// breakpoint: opcode LD B, B (0x40) executed with the result
// signature already in the registers. Passing is the Fibonacci set
// B=3 C=5 D=8 E=13 H=21 L=34; failing writes 0x42 to all six.
func runMooneye(t *testing.T, path string, budget int) (done, pass bool) {
	t.Helper()
	s, err := New(loadROM(t, path))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	s.LYStub = 0xFF // mooneye's documented headless bypass
	for spent := 0; spent < budget; {
		r := s.CPU.Regs()
		fib := r.B == 3 && r.C == 5 && r.D == 8 && r.E == 13 && r.H == 21 && r.L == 34
		bad := r.B == 0x42 && r.C == 0x42 && r.D == 0x42 &&
			r.E == 0x42 && r.H == 0x42 && r.L == 0x42
		if (fib || bad) && s.Read(r.PC) == 0x40 {
			return true, fib
		}
		spent += s.Step()
	}
	return false, false
}

func TestMooneyeAcceptance(t *testing.T) {
	root := filepath.Join(testromsDir(t), "mooneye", "acceptance")
	if _, err := os.Stat(root); err != nil {
		t.Skip("mooneye acceptance ROMs absent")
	}
	var roms []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".gb") {
			rel, _ := filepath.Rel(root, path)
			roms = append(roms, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(roms)
	passed, failed, unfinished := 0, 0, 0
	for _, rel := range roms {
		rel := rel
		// ROMs whose names target the CGB family cannot pass on a
		// DMG rig by design; skip them entirely.
		if strings.Contains(rel, "cgb") {
			t.Logf("SKIP %s (CGB target)", rel)
			continue
		}
		done, pass := runMooneye(t, filepath.Join(root, rel), 25_000_000)
		core := mooneyeCore[filepath.ToSlash(rel)]
		switch {
		case done && pass:
			passed++
			t.Logf("PASS %s", rel)
		case done:
			failed++
			t.Logf("FAIL %s (test reported failure)", rel)
			if core {
				t.Errorf("core mooneye test failed: %s", rel)
			}
		default:
			unfinished++
			t.Logf("FAIL %s (no verdict within budget; needs PPU, boot ROM, or cycle-exact timing GB-1 does not model)", rel)
			if core {
				t.Errorf("core mooneye test produced no verdict: %s", rel)
			}
		}
	}
	t.Logf("mooneye acceptance summary: %d passed, %d reported failure, %d no verdict, %d total",
		passed, failed, unfinished, len(roms))
}
