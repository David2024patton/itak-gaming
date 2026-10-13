package system

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// pspautotests harness: runs the open source pspautotests PRXs
// from the local corpus (testroms/pspautotests, gitignored, never
// committed) through the HLE kernel and compares each test's
// result file against its .expected file, byte exact after line
// ending normalization. The corpus framework writes its output
// to host0:/__testoutput.txt (the psplink host channel), which
// the kernel routes into the file store; that file's content is
// the test result. cpu/cpu_alu is the gating set: every one of
// its tests must pass. The other cpu groups run report only:
// their outcomes are logged (and visible with go test -v)
// without failing the suite, which is what closes or keeps
// flagged the PSP-2 VFPU cross-check items. The whole file skips
// when the corpus is absent.

func corpusDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "testroms", "pspautotests")
	if _, err := os.Stat(filepath.Join(dir, "cpu")); err != nil {
		t.Skip("pspautotests corpus not present locally (testroms/pspautotests); skipping")
	}
	return dir
}

// runCorpusTest boots one PRX and returns the content of its
// host0 result file. The guest exits through the crt0 exit path;
// the loop also stops at a generous cycle cap for tests that
// never terminate, returning whatever output was produced.
func runCorpusTest(prxPath string) (string, error) {
	data, err := os.ReadFile(prxPath)
	if err != nil {
		return "", err
	}
	m := New()
	files := map[string][]byte{}
	m.SetMemoryStickFiles(files)
	if err := m.LoadExecutable(data); err != nil {
		return "", fmt.Errorf("LoadExecutable(%s): %w", filepath.Base(prxPath), err)
	}
	const slice = 4 << 20 // cycles per scheduler slice
	out := func() string { return string(files["host0:/__testoutput.txt"]) }
	for i := 0; i < 75; i++ {
		m.RunCycles(slice)
		if m.Exited() {
			return out(), nil
		}
	}
	return out(), fmt.Errorf("%s: guest did not exit within the cycle cap", filepath.Base(prxPath))
}

func normalizeCRLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func firstDiff(got, want string) string {
	g := strings.Split(got, "\n")
	w := strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		gs, ws := "<eof>", "<eof>"
		if i < len(g) {
			gs = g[i]
		}
		if i < len(w) {
			ws = w[i]
		}
		if gs != ws {
			return fmt.Sprintf("line %d: got %q, want %q", i+1, truncLine(gs), truncLine(ws))
		}
	}
	return "outputs differ only in length"
}

func truncLine(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// corpusTestNames lists the test names (PRX basenames) of a group.
func corpusTestNames(t *testing.T, root, group string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "cpu", group))
	if err != nil {
		t.Fatalf("read corpus group %s: %v", group, err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".prx") {
			names = append(names, strings.TrimSuffix(e.Name(), ".prx"))
		}
	}
	sort.Strings(names)
	return names
}

func corpusOne(root, group, name string) (string, bool) {
	dir := filepath.Join(root, "cpu", group)
	wantRaw, err := os.ReadFile(filepath.Join(dir, name+".expected"))
	if err != nil {
		return "missing .expected: " + err.Error(), false
	}
	got, runErr := runCorpusTest(filepath.Join(dir, name+".prx"))
	gotN, wantN := normalizeCRLF(got), normalizeCRLF(string(wantRaw))
	if runErr != nil {
		return runErr.Error() + "; " + firstDiff(gotN, wantN), false
	}
	if gotN != wantN {
		return firstDiff(gotN, wantN), false
	}
	return "", true
}

// TestPSPAutotestsCPUALU is the gating corpus test: the whole
// cpu_alu group must reproduce its .expected output exactly.
func TestPSPAutotestsCPUALU(t *testing.T) {
	root := corpusDir(t)
	for _, name := range corpusTestNames(t, root, "cpu_alu") {
		name := name
		t.Run(name, func(t *testing.T) {
			if diff, ok := corpusOne(root, "cpu_alu", name); !ok {
				t.Fatalf("console mismatch: %s", diff)
			}
		})
	}
}

// TestPSPAutotestsReport runs the remaining cpu groups report
// only: every outcome is logged, nothing here fails the suite.
func TestPSPAutotestsReport(t *testing.T) {
	root := corpusDir(t)
	for _, group := range []string{"fpu", "icache", "lsu", "vfpu"} {
		var passed, failed []string
		diffs := map[string]string{}
		for _, name := range corpusTestNames(t, root, group) {
			if diff, ok := corpusOne(root, group, name); ok {
				passed = append(passed, name)
			} else {
				failed = append(failed, name)
				diffs[name] = diff
			}
		}
		t.Logf("%s: %d passed, %d failed", group, len(passed), len(failed))
		if len(failed) > 0 {
			t.Logf("%s failing: %s", group, strings.Join(failed, ", "))
		}
		for _, name := range failed {
			t.Logf("%s/%s: %s", group, name, diffs[name])
		}
	}
}
