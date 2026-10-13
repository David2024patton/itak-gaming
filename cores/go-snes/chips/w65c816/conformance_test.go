// The Tom Harte single-step conformance gate.
//
// The corpus (github.com/TomHarte/ProcessorTests, the
// 65816 v1 set) provides 10,000 randomized tests per
// opcode per mode: an initial processor state and RAM
// contents, the final state after exactly one instruction,
// the final contents of every RAM cell the instruction
// could have touched, and a cycle-by-cycle bus trace.
//
// The runner installs the initial state, executes one
// Step, and compares:
//   - every register, including PBR, DBR, D, and the
//     emulation flag;
//   - the final value of every listed RAM cell;
//   - the total bus cycle count against the trace length;
//   - the subsequence of valid bus accesses (cycles whose
//     trace entry asserts VDA, VPA, or VPB) against the
//     accesses this CPU actually made: address, direction,
//     and data (the value written, or the value a read
//     returned).
//
// The corpus lives under testroms/tomharte in this module
// (gitignored, local evidence only). When it is absent the
// gate skips. Fetch it with:
//
//	git clone --depth 1 https://github.com/TomHarte/ProcessorTests
//	# then copy or link ProcessorTests/65816/v1/*.json into
//	# testroms/tomharte/
package w65c816

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
	S   int      `json:"s"`
	P   int      `json:"p"`
	A   int      `json:"a"`
	X   int      `json:"x"`
	Y   int      `json:"y"`
	DBR int      `json:"dbr"`
	D   int      `json:"d"`
	PBR int      `json:"pbr"`
	E   int      `json:"e"`
	RAM []thPair `json:"ram"`
}

type thCycle struct {
	addr  uint32
	val   int // -1 when the trace records null
	write bool
	valid bool
}

func (cc *thCycle) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 3 {
		return fmt.Errorf("cycle entry with %d parts", len(parts))
	}
	var addr int
	if err := json.Unmarshal(parts[0], &addr); err != nil {
		return err
	}
	cc.addr = uint32(addr)
	cc.val = -1
	if string(parts[1]) != "null" {
		var v int
		if err := json.Unmarshal(parts[1], &v); err != nil {
			return err
		}
		cc.val = v
	}
	var pins string
	if err := json.Unmarshal(parts[2], &pins); err != nil {
		return err
	}
	if len(pins) >= 4 {
		cc.write = pins[3] == 'w'
		cc.valid = pins[0] == 'd' || pins[1] == 'p' || pins[2] == 'v'
	}
	return nil
}

type thTest struct {
	Name    string    `json:"name"`
	Initial thState   `json:"initial"`
	Final   thState   `json:"final"`
	Cycles  []thCycle `json:"cycles"`
}

// thBus is the corpus environment: a flat 16 MB of RAM,
// uniform slow accesses, and a recording of every access
// the CPU makes.
type thBus struct {
	mem     []byte
	access  []thAccess
	touched map[uint32]bool
}

type thAccess struct {
	addr  uint32
	val   uint8
	write bool
}

func newTHBus() *thBus {
	return &thBus{mem: make([]byte, 1<<24), touched: make(map[uint32]bool)}
}

func (b *thBus) Read(bank uint8, addr uint16) uint8 {
	a := uint32(bank)<<16 | uint32(addr)
	v := b.mem[a]
	b.access = append(b.access, thAccess{addr: a, val: v})
	return v
}

func (b *thBus) Write(bank uint8, addr uint16, val uint8) {
	a := uint32(bank)<<16 | uint32(addr)
	b.mem[a] = val
	b.touched[a] = true
	b.access = append(b.access, thAccess{addr: a, val: val, write: true})
}

func (b *thBus) Speed(bank uint8, addr uint16) Speed { return SpeedSlow }

// WriteDummy performs an RMW restore write: the memory
// changes, but the corpus does not count the cycle as a
// valid access, so nothing is recorded.
func (b *thBus) WriteDummy(bank uint8, addr uint16, val uint8) {
	a := uint32(bank)<<16 | uint32(addr)
	b.mem[a] = val
	b.touched[a] = true
}

// reset restores the corpus invariant: RAM is zero except
// for the test's initial entries.
func (b *thBus) reset(initial []thPair) {
	for a := range b.touched {
		b.mem[a] = 0
	}
	b.touched = make(map[uint32]bool)
	b.access = b.access[:0]
	for _, p := range initial {
		a := uint32(p[0])
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
	bus := newTHBus()
	cpu := New()
	cpu.Attach(bus)
	var firstFailure string
	for _, tc := range tests {
		bus.reset(tc.Initial.RAM)
		cpu.SetRegs(Registers{
			A: uint16(tc.Initial.A), X: uint16(tc.Initial.X), Y: uint16(tc.Initial.Y),
			S: uint16(tc.Initial.S), D: uint16(tc.Initial.D), PC: uint16(tc.Initial.PC),
			DBR: uint8(tc.Initial.DBR), PBR: uint8(tc.Initial.PBR),
			P: uint8(tc.Initial.P), E: tc.Initial.E != 0,
		})
		before := cpu.Cycles()
		// Block moves are the corpus's special case: its
		// generator records at most 100 cycles of a move
		// and stops mid-iteration, so the final state is
		// a truncation, not a completion. The CPU's cycle
		// budget cuts the execution at exactly the
		// recorded cycle so the states line up.
		op := bus.mem[uint32(tc.Initial.PBR)<<16|uint32(tc.Initial.PC)]
		if op == 0x44 || op == 0x54 {
			cpu.SetCycleBudget(before + uint64(len(tc.Cycles)))
			func() {
				defer func() { _ = recover() }()
				cpu.Step()
			}()
			cpu.SetCycleBudget(0)
		} else {
			cpu.Step()
		}
		got := int(cpu.Cycles() - before)
		if msg := checkTomHarte(cpu, bus, tc, got); msg != "" {
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

func checkTomHarte(cpu *CPU, bus *thBus, tc thTest, gotCycles int) string {
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
	if msg := chk("pbr", int(r.PBR), f.PBR); msg != "" {
		return msg
	}
	if msg := chk("s", int(r.S), f.S); msg != "" {
		return msg
	}
	if msg := chk("p", int(r.P), f.P); msg != "" {
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
	if msg := chk("dbr", int(r.DBR), f.DBR); msg != "" {
		return msg
	}
	if msg := chk("d", int(r.D), f.D); msg != "" {
		return msg
	}
	gotE := 0
	if r.E {
		gotE = 1
	}
	if msg := chk("e", gotE, f.E); msg != "" {
		return msg
	}
	for _, p := range f.RAM {
		if got := bus.mem[uint32(p[0])]; got != uint8(p[1]) {
			return fmt.Sprintf("ram[%#x] = %#x, want %#x", p[0], got, p[1])
		}
	}
	if gotCycles != len(tc.Cycles) {
		return fmt.Sprintf("cycles = %d, want %d", gotCycles, len(tc.Cycles))
	}
	// The valid-access subsequence: addresses, direction,
	// and data must match the trace exactly.
	valid := make([]thCycle, 0, len(tc.Cycles))
	for _, cc := range tc.Cycles {
		if cc.valid {
			valid = append(valid, cc)
		}
	}
	if len(valid) != len(bus.access) {
		return fmt.Sprintf("valid accesses = %d, want %d", len(bus.access), len(valid))
	}
	for i, cc := range valid {
		acc := bus.access[i]
		if acc.addr != cc.addr {
			return fmt.Sprintf("access %d addr = %#x, want %#x", i, acc.addr, cc.addr)
		}
		if acc.write != cc.write {
			return fmt.Sprintf("access %d (%#x) write = %v, want %v", i, acc.addr, acc.write, cc.write)
		}
		if cc.val >= 0 && int(acc.val) != cc.val {
			return fmt.Sprintf("access %d (%#x) data = %#x, want %#x", i, acc.addr, acc.val, cc.val)
		}
	}
	return ""
}

func TestTomHarte65816(t *testing.T) {
	dir := filepath.Join("..", "..", "testroms", "tomharte")
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
	t.Logf("Tom Harte 65816: %d passed, %d failed, %d total", totalPass, totalFail, totalPass+totalFail)
	if len(only) == 0 && len(files) < 512 {
		t.Logf("note: corpus subset present: %d of 512 files", len(files))
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
