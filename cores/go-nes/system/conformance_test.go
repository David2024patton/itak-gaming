package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-nes/chips/cpu6502"
)

// testromsDir locates the gitignored public test ROMs. The suite
// skips cleanly when they are absent (a fresh checkout has no
// testroms directory).
func testromsDir(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"../testroms", "../../testroms"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	t.Skip("testroms/ not present (public conformance ROMs are a local-only fetch)")
	return ""
}

// maskAnnotation blanks the trailing "= XX" memory-value
// annotation of a trace line so two lines can be compared on
// everything except an annotated value.
func maskAnnotation(line string) string {
	idx := strings.LastIndex(line, " = ")
	if idx < 0 || idx+5 > len(line) {
		return line
	}
	isHex := func(c byte) bool {
		return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
	}
	if !isHex(line[idx+3]) || !isHex(line[idx+4]) {
		return line
	}
	return line[:idx] + " = ??" + line[idx+5:]
}

// TestNestest runs Kevin Horton's nestest in its standard
// automated mode: execution starts at $C000 with the golden
// post-reset register state, every instruction trace line is
// compared against the shipped golden log, and the official
// result bytes at $0002/$0003 must both be zero at the end.
//
// The rig's one known divergence class is annotated values read
// from the stub IO space ($2000-$401F): the golden log was
// recorded on an emulator whose APU register readback returns
// $FF where this rig returns open bus. Those lines are counted
// separately and reported, never silently passed.
func TestNestest(t *testing.T) {
	dir := testromsDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "nestest", "nestest.nes"))
	if err != nil {
		t.Skipf("nestest.nes not fetched: %v", err)
	}
	rawLog, err := os.ReadFile(filepath.Join(dir, "nestest", "nestest.log"))
	if err != nil {
		t.Skipf("nestest.log not fetched: %v", err)
	}
	golden := strings.Split(strings.ReplaceAll(string(rawLog), "\r\n", "\n"), "\n")
	for len(golden) > 0 && golden[len(golden)-1] == "" {
		golden = golden[:len(golden)-1]
	}
	s, err := New(data)
	if err != nil {
		t.Fatal(err)
	}
	cpu := s.CPU()
	r := cpu.Regs() // post-reset state already matches the golden start
	r.PC = 0xC000
	cpu.SetRegs(r)

	strict, ioAnnotated, hard := 0, 0, 0
	firstDiv := ""
	for i, want := range golden {
		pre := cpu.Regs()
		line, annAddr, hasAnn := cpu6502.FormatTrace(s.Peek, pre.PC, pre, cpu.Cycles())
		switch {
		case line == want:
			strict++
		case hasAnn && annAddr >= 0x2000 && annAddr <= 0x401F && maskAnnotation(line) == maskAnnotation(want):
			ioAnnotated++
		default:
			hard++
			if firstDiv == "" {
				firstDiv = fmt.Sprintf("trace line %d:\n  mine:   %q\n  golden: %q", i+1, line, want)
			}
		}
		s.Step()
	}
	res2, res3 := s.Peek(0x0002), s.Peek(0x0003)
	t.Logf("nestest: %d/%d trace lines strict, %d IO-annotation-only, %d hard divergences; result bytes $%02X $%02X",
		strict, len(golden), ioAnnotated, hard, res2, res3)
	if hard > 0 {
		t.Fatalf("nestest trace diverged: %s", firstDiv)
	}
	if res2 != 0 || res3 != 0 {
		t.Fatalf("nestest result bytes = %02X %02X, want 00 00", res2, res3)
	}
}

// runBlargg loads a Blargg-protocol ROM, steps it in bursts
// while watching $6000, and returns the finished verdict.
func runBlargg(t *testing.T, path string, budget uint64) (done, passed bool, text string, cycles uint64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("ROM not fetched: %v", err)
	}
	s, err := New(data)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	start := s.Cycles()
	for s.Cycles()-start < budget {
		for i := 0; i < 8192; i++ {
			s.Step()
		}
		if ok, p, txt := s.BlarggResult(); ok {
			return true, p, txt, s.Cycles() - start
		}
	}
	return false, false, "", s.Cycles() - start
}

// TestBlarggInstrTestV5 is the NES-1 CPU gate: every rom_single
// of Blargg's instr_test-v5 plus the combined official_only and
// all_instrs ROMs must report a pass through the $6000 protocol.
func TestBlarggInstrTestV5(t *testing.T) {
	dir := testromsDir(t)
	base := filepath.Join(dir, "instr_test-v5")
	var roms []string
	for i := 1; i <= 16; i++ {
		matches, _ := filepath.Glob(filepath.Join(base, "rom_singles", fmt.Sprintf("%02d-*.nes", i)))
		if len(matches) == 0 {
			matches, _ = filepath.Glob(filepath.Join(base, fmt.Sprintf("%02d-*.nes", i)))
		}
		roms = append(roms, matches...)
	}
	roms = append(roms,
		filepath.Join(base, "official_only.nes"),
		filepath.Join(base, "all_instrs.nes"),
	)
	if len(roms) < 18 {
		t.Skipf("instr_test-v5 set incomplete: found %d ROMs", len(roms))
	}
	for _, rom := range roms {
		rom := rom
		// The combined ROMs run the full sweep of every
		// instruction (official_only alone takes about 55M
		// cycles); the singles finish in a few million each.
		budget := uint64(60_000_000)
		if strings.HasSuffix(rom, "official_only.nes") {
			budget = 120_000_000
		}
		if strings.HasSuffix(rom, "all_instrs.nes") {
			budget = 240_000_000
		}
		t.Run(filepath.Base(rom), func(t *testing.T) {
			done, passed, text, cycles := runBlargg(t, rom, budget)
			if !done {
				t.Fatalf("no result within budget (%d cycles)", cycles)
			}
			t.Logf("%s: %d cycles, report: %s", filepath.Base(rom), cycles, strings.TrimSpace(text))
			if !passed {
				t.Fatalf("FAILED: %s", strings.TrimSpace(text))
			}
		})
	}
}

// TestBlarggTimingROMsReport runs the Blargg timing suites and
// logs their verdicts without gating NES-1 on them: these ROMs
// measure instruction timing against machine timing sources the
// NES-1 rig does not have (the APU frame counter and dot-level
// PPU phase), so their outcome is recorded here for NES-2 and
// NES-3 to close out. The instruction gate is
// TestBlarggInstrTestV5 above.
func TestBlarggTimingROMsReport(t *testing.T) {
	dir := testromsDir(t)
	roms := []string{
		filepath.Join(dir, "cpu_timing", "cpu_timing_test.nes"),
		filepath.Join(dir, "instr_timing", "1-instr_timing.nes"),
		filepath.Join(dir, "instr_timing", "2-branch_timing.nes"),
	}
	for _, rom := range roms {
		if _, err := os.Stat(rom); err != nil {
			t.Logf("%s: not fetched", filepath.Base(rom))
			continue
		}
		done, passed, text, cycles := runBlargg(t, rom, 90_000_000)
		t.Logf("%s: done=%v passed=%v cycles=%d report: %s",
			filepath.Base(rom), done, passed, cycles, strings.TrimSpace(text))
	}
}
