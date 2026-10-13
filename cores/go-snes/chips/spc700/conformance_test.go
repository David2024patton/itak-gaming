// The Tom Harte single-step conformance gate.
//
// The corpus (github.com/TomHarte/ProcessorTests, the
// spc700 v1 set, generated from the JSMoo core) provides
// 1,000 randomized tests per opcode: an initial processor
// state and RAM contents, the final state after exactly
// one instruction, the final contents of every RAM cell
// the instruction could have touched, and a cycle-by-cycle
// bus trace whose entries are [address, value, kind] with
// kind "read", "write", or "wait" (address and value are
// null for waits and for dummy accesses).
//
// The schema differs from the 65816 set's: registers are
// flat fields named pc, a, x, y, sp, and psw (not p), RAM
// is a list of [address, value] pairs in a flat 64 KB
// space, and the cycles list is present for every test.
//
// The runner installs the initial state, executes one
// Step, and compares:
//   - every register, including the full PSW;
//   - the final value of every listed RAM cell;
//   - the total cycle count against the trace length;
//   - the data-carrying bus access sequence: the trace
//     entries that carry a value (address, direction, and
//     data), in order, against the accesses this CPU
//     actually made. Trace entries with a null value are
//     idle or dummy cycles that move no data (the generator
//     records, for example, DIV's internal cycles and the
//     indirect modes' idle read that way), so they carry
//     nothing to compare; the cycle count check above
//     still accounts for them.
//
// Two opcodes are exempt from the cycle comparison: SLEEP
// and STOP halt the CPU, the trace models the halt as an
// idle spin of a length the generator chose, and this core
// charges a halted Step a flat 16 cycles by contract (see
// the package comment). Their registers and RAM are still
// compared.
//
// The corpus lives under testroms/tomharte-spc700 in this
// module (gitignored, local evidence only). When it is
// absent the gate skips.
package spc700

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type thPair [2]int

type thState struct {
	PC  int      `json:"pc"`
	A   int      `json:"a"`
	X   int      `json:"x"`
	Y   int      `json:"y"`
	SP  int      `json:"sp"`
	PSW int      `json:"psw"`
	RAM []thPair `json:"ram"`
}

// thCycle is one trace entry: [address, value, kind], with
// address and value null when the cycle makes no addressed
// access or moves no data. hasVal marks the entries that
// carry data and so take part in the access comparison.
type thCycle struct {
	addr   int
	val    int
	hasVal bool
	write  bool
}

func (cc *thCycle) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 3 {
		return fmt.Errorf("cycle entry with %d parts", len(parts))
	}
	if string(parts[0]) != "null" {
		if err := json.Unmarshal(parts[0], &cc.addr); err != nil {
			return err
		}
	}
	if string(parts[1]) != "null" {
		if err := json.Unmarshal(parts[1], &cc.val); err != nil {
			return err
		}
		cc.hasVal = true
	}
	var kind string
	if err := json.Unmarshal(parts[2], &kind); err != nil {
		return err
	}
	cc.write = kind == "write"
	return nil
}

type thTest struct {
	Name    string    `json:"name"`
	Initial thState   `json:"initial"`
	Final   thState   `json:"final"`
	Cycles  []thCycle `json:"cycles"`
}

// thBus is the corpus environment: a flat 64 KB of RAM
// that remembers every cell ever dirtied so the next
// test's reset only has to clear those, and a recording of
// every access the CPU makes.
type thBus struct {
	mem     []byte
	access  []thAccess
	touched map[uint16]bool
}

type thAccess struct {
	addr  uint16
	val   uint8
	write bool
}

func newTHBus() *thBus {
	return &thBus{mem: make([]byte, 1<<16), touched: make(map[uint16]bool)}
}

func (b *thBus) Read(addr uint16) uint8 {
	v := b.mem[addr]
	b.access = append(b.access, thAccess{addr: addr, val: v})
	return v
}

func (b *thBus) Write(addr uint16, val uint8) {
	b.mem[addr] = val
	b.touched[addr] = true
	b.access = append(b.access, thAccess{addr: addr, val: val, write: true})
}

// reset restores the corpus invariant: RAM is zero except
// for the test's initial entries.
func (b *thBus) reset(initial []thPair) {
	for a := range b.touched {
		b.mem[a] = 0
	}
	b.touched = make(map[uint16]bool)
	b.access = b.access[:0]
	for _, p := range initial {
		a := uint16(p[0])
		b.mem[a] = uint8(p[1])
		b.touched[a] = true
	}
}

func runTomHarteFile(t *testing.T, path string) (passed, failed int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var tests []thTest
	if err := json.Unmarshal(data, &tests); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(tests) > 10000 {
		tests = tests[:10000]
	}
	bus := newTHBus()
	cpu := New()
	cpu.Attach(bus)
	var firstFailure string
	for _, tc := range tests {
		bus.reset(tc.Initial.RAM)
		cpu.SetRegs(Registers{
			PC: uint16(tc.Initial.PC), A: uint8(tc.Initial.A),
			X: uint8(tc.Initial.X), Y: uint8(tc.Initial.Y),
			SP: uint8(tc.Initial.SP), P: uint8(tc.Initial.PSW),
		})
		op := bus.mem[uint16(tc.Initial.PC)]
		got := cpu.Step()
		if msg := checkTomHarte(cpu, bus, tc, op, got); msg != "" {
			failed++
			if firstFailure == "" {
				firstFailure = fmt.Sprintf("test %q: %s", tc.Name, msg)
			}
			continue
		}
		passed++
	}
	if firstFailure != "" {
		t.Errorf("%s: %d/%d failed; first: %s", filepath.Base(path), failed, passed+failed, firstFailure)
	}
	return passed, failed
}

func checkTomHarte(cpu *CPU, bus *thBus, tc thTest, op uint8, gotCycles int) string {
	r := cpu.Regs()
	f := tc.Final
	chk := func(name string, got, want int) string {
		if got != want {
			return fmt.Sprintf("%s = %d (%#x), want %d (%#x)", name, got, got, want, want)
		}
		return ""
	}
	if msg := chk("pc", int(r.PC), f.PC); msg != "" {
		return msg
	}
	if msg := chk("a", int(r.A), f.A); msg != "" {
		return msg
	}
	if msg := chk("x", int(r.X), f.X); msg != "" {
		return msg
	}
	if msg := chk("y", int(r.Y), f.Y); msg != "" {
		return msg
	}
	if msg := chk("sp", int(r.SP), f.SP); msg != "" {
		return msg
	}
	if msg := chk("psw", int(r.P), f.PSW); msg != "" {
		return msg
	}
	for _, p := range f.RAM {
		if got := bus.mem[uint16(p[0])]; got != uint8(p[1]) {
			return fmt.Sprintf("ram[%#x] = %#x, want %#x", p[0], got, p[1])
		}
	}
	// SLEEP and STOP are exempt from the cycle comparison:
	// the trace spins in the halt, this core charges a
	// flat 16 (see the file comment).
	if op != 0xEF && op != 0xFF && gotCycles != len(tc.Cycles) {
		return fmt.Sprintf("cycles = %d, want %d", gotCycles, len(tc.Cycles))
	}
	// The data-carrying access subsequence: trace entries
	// with a value, in order, must equal the accesses this
	// CPU made (address, direction, and data).
	valued := make([]thCycle, 0, len(tc.Cycles))
	for _, cc := range tc.Cycles {
		if cc.hasVal {
			valued = append(valued, cc)
		}
	}
	if len(valued) != len(bus.access) {
		return fmt.Sprintf("data accesses = %d, want %d", len(bus.access), len(valued))
	}
	for i, cc := range valued {
		acc := bus.access[i]
		if int(acc.addr) != cc.addr {
			return fmt.Sprintf("access %d addr = %#x, want %#x", i, acc.addr, cc.addr)
		}
		if acc.write != cc.write {
			return fmt.Sprintf("access %d (%#x) write = %v, want %v", i, acc.addr, acc.write, cc.write)
		}
		if int(acc.val) != cc.val {
			return fmt.Sprintf("access %d (%#x) data = %#x, want %#x", i, acc.addr, acc.val, cc.val)
		}
	}
	return ""
}

func TestTomHarteSPC700(t *testing.T) {
	dir := filepath.Join("..", "..", "testroms", "tomharte-spc700")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("Tom Harte corpus not present at %s: %v", dir, err)
	}
	var files []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("Tom Harte corpus directory holds no test files")
	}
	// TH_FILES (a comma-separated list of base names) limits
	// the run for iteration; the gate itself runs all files
	// present.
	only := map[string]bool{}
	if env := os.Getenv("TH_FILES"); env != "" {
		for _, name := range splitComma(env) {
			only[name] = true
		}
	}
	totalPass, totalFail := 0, 0
	for _, path := range files {
		if len(only) > 0 && !only[filepath.Base(path)] {
			continue
		}
		p, f := runTomHarteFile(t, path)
		totalPass += p
		totalFail += f
	}
	t.Logf("Tom Harte SPC700: %d passed, %d failed, %d total", totalPass, totalFail, totalPass+totalFail)
	if len(only) == 0 && len(files) < 256 {
		t.Logf("note: corpus subset present: %d of 256 files", len(files))
	}
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
