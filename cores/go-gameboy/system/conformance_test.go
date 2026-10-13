package system

import (
	"image"
	_ "image/png"
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
// The GB-2 gating sets (mooneyeTimer, mooneyePPU, mooneyeDMA and
// mooneyeIRQ below) extend the gate to the subsystems GB-2 models.
// Remaining exclusions: halt_ime0_ei.gb needs a real VBlank
// interrupt source timed against EI (its CPU semantics, EI before
// HALT gives IME=1 halt behavior, are covered by a unit test in
// the sm83 package).
var mooneyeCore = map[string]bool{
	"bits/reg_f.gb":       true,
	"bits/mem_oam.gb":     true,
	"ei_sequence.gb":      true,
	"halt_ime1_timing.gb": true,
	"rapid_di_ei.gb":      true,
}

// GB-2 gate: the full mooneye timer suite. tim00-11 passed in GB-1
// on the simple counter; the div-trigger, reload, and write cases
// pin the falling-edge model the GB-2 timer implements.
var mooneyeTimer = map[string]bool{
	"timer/div_write.gb":            true,
	"timer/rapid_toggle.gb":         true,
	"timer/tim00.gb":                true,
	"timer/tim00_div_trigger.gb":    true,
	"timer/tim01.gb":                true,
	"timer/tim01_div_trigger.gb":    true,
	"timer/tim10.gb":                true,
	"timer/tim10_div_trigger.gb":    true,
	"timer/tim11.gb":                true,
	"timer/tim11_div_trigger.gb":    true,
	"timer/tima_reload.gb":          true,
	"timer/tima_write_reloading.gb": true,
	"timer/tma_write_reloading.gb":  true,
}

// GB-2 gate: the mooneye PPU suite's DMG-appropriate interrupt
// timing and STAT tests. The -GS variants target the GameShark
// boot environment, and the lcdon tests probe boot-time behavior
// of specific models, so they stay out of the gate (still run and
// logged by the sweep). intr_2_mode0_timing_sprites is excluded
// for a hardware-model reason: it measures how the mode 3 sprite
// fetch penalty varies with each sprite's X position, which is
// set by the pixel FIFO's internal fetch phase as sprites are
// encountered mid-transfer. The PPU here renders line-wise with
// the documented penalty level (the first fetched sprite pays
// its X phase cost, the rest 6 dots each) and does not simulate
// the FIFO's phase interaction between sprites at different X
// positions.
var mooneyePPU = map[string]bool{
	"ppu/intr_2_0_timing.gb":      true,
	"ppu/intr_2_mode0_timing.gb":  true,
	"ppu/intr_2_mode3_timing.gb":  true,
	"ppu/intr_2_oam_ok_timing.gb": true,
	"ppu/stat_irq_blocking.gb":    true,
	"ppu/stat_lyc_onoff.gb":       true,
}

// GB-2 gate: the mooneye OAM DMA suite (sources-GS excluded: it
// targets the GameShark boot environment).
var mooneyeDMA = map[string]bool{
	"oam_dma/basic.gb":           true,
	"oam_dma/reg_read.gb":        true,
	"oam_dma/oam_dma_restart.gb": true,
	"oam_dma/oam_dma_start.gb":   true,
	"oam_dma/oam_dma_timing.gb":  true,
}

// GB-2 gate: interrupt dispatch internals.
var mooneyeIRQ = map[string]bool{
	"interrupts/ie_push.gb": true,
}

func mooneyeGated(rel string) bool {
	rel = filepath.ToSlash(rel)
	return mooneyeCore[rel] || mooneyeTimer[rel] || mooneyePPU[rel] ||
		mooneyeDMA[rel] || mooneyeIRQ[rel]
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
		core := mooneyeGated(rel)
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

// TestBlarggDMGSound gates on Blargg's dmg_sound singles: the
// APU behavior suite (registers, length counters, trigger
// behavior, sweep, wave RAM quirks, power cycling). These ROM
// builds report only through the memory protocol documented in
// the suite readme: $A000 holds $80 while running and the final
// result code (0 = passed) once done, $A001-$A003 hold the
// DE B0 61 signature, and the output text accumulates at $A004.
// They never print to the serial port, so the harness polls the
// result byte instead.
func runDMGSound(t *testing.T, path string, budget int) (int, string) {
	t.Helper()
	s, err := New(loadROM(t, path))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	code := -1
	for spent := 0; spent < budget; spent += s.Step() {
		if s.Read(0xA001) == 0xDE && s.Read(0xA002) == 0xB0 && s.Read(0xA003) == 0x61 {
			if v := s.Read(0xA000); v != 0x80 {
				code = int(v)
				break
			}
		}
	}
	text := ""
	for a := uint16(0xA004); a < 0xA800; a++ {
		c := s.Read(a)
		if c == 0 {
			break
		}
		text += string(c)
	}
	return code, text
}

func TestBlarggDMGSound(t *testing.T) {
	dir := filepath.Join(testromsDir(t), "dmg_sound")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("dmg_sound ROMs absent: %v", err)
	}
	var roms []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gb") {
			roms = append(roms, e.Name())
		}
	}
	sort.Strings(roms)
	if len(roms) == 0 {
		t.Skip("no dmg_sound ROMs found")
	}
	for _, name := range roms {
		name := name
		t.Run(name, func(t *testing.T) {
			code, text := runDMGSound(t, filepath.Join(dir, name), 400_000_000)
			switch {
			case code == 0:
				t.Logf("PASS %s", name)
			case code < 0:
				t.Errorf("FAIL %s: no result within budget; text so far:\n%s", name, text)
			default:
				t.Errorf("FAIL %s: result code %d; text:\n%s", name, code, text)
			}
		})
	}
}

// TestDMGAcid2 renders the dmg-acid2 test image and compares it
// pixel for pixel against the shipped reference render. The
// reference PNG uses the four DMG grays (0xFF, 0xAA, 0x55, 0x00),
// which map to the PPU's four framebuffer shades.
func TestDMGAcid2(t *testing.T) {
	dir := filepath.Join(testromsDir(t), "dmg-acid2")
	romPath := filepath.Join(dir, "dmg-acid2.gb")
	refPath := filepath.Join(dir, "reference-dmg.png")
	if _, err := os.Stat(romPath); err != nil {
		t.Skip("dmg-acid2 ROM absent")
	}
	f, err := os.Open(refPath)
	if err != nil {
		t.Skipf("dmg-acid2 reference absent: %v", err)
	}
	defer f.Close()
	ref, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode reference: %v", err)
	}
	if b := ref.Bounds(); b.Dx() != 160 || b.Dy() != 144 {
		t.Fatalf("reference size = %v, want 160x144", b)
	}
	shadeFor := func(gray uint32) (uint16, bool) {
		switch gray >> 8 {
		case 0xFF:
			return 0xFFFF, true
		case 0xAA:
			return 0xAD55, true
		case 0x55:
			return 0x52AA, true
		case 0x00:
			return 0x0000, true
		}
		return 0, false
	}
	s, err := New(loadROM(t, romPath))
	if err != nil {
		t.Fatal(err)
	}
	// The ROM draws the test face and then idles; give it time
	// to settle into the final image.
	for i := 0; i < 60; i++ {
		s.RunFrame()
	}
	fb := s.Framebuffer()
	mismatch := 0
	for y := 0; y < 144; y++ {
		for x := 0; x < 160; x++ {
			r, _, _, _ := ref.At(x, y).RGBA()
			want, ok := shadeFor(r)
			if !ok {
				t.Fatalf("reference pixel (%d,%d) has unexpected gray 0x%02X", x, y, r>>8)
			}
			if got := fb[y*160+x]; got != want {
				if mismatch < 10 {
					t.Logf("pixel (%d,%d): got shade 0x%04X, want 0x%04X", x, y, got, want)
				}
				mismatch++
			}
		}
	}
	if mismatch > 0 {
		t.Fatalf("dmg-acid2: %d of %d pixels differ from the reference", mismatch, 160*144)
	}
}
