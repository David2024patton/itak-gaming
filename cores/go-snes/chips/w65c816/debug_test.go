package w65c816

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestDebugOne runs a single named corpus test named by
// the TH_ONE env var ("<file>:<test name>") and prints the
// full divergence: states, cycles, and both access lists.
// It is a debugging aid, not a gate.
func TestDebugOne(t *testing.T) {
	spec := os.Getenv("TH_ONE")
	if spec == "" {
		t.Skip("TH_ONE not set")
	}
	var file, name string
	for i := 0; i < len(spec); i++ {
		if spec[i] == ':' {
			file, name = spec[:i], spec[i+1:]
			break
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "testroms", "tomharte", file))
	if err != nil {
		t.Fatal(err)
	}
	var tests []thTest
	if err := json.Unmarshal(data, &tests); err != nil {
		t.Fatal(err)
	}
	for _, tc := range tests {
		if tc.Name != name {
			continue
		}
		bus := newTHBus()
		cpu := New()
		cpu.Attach(bus)
		bus.reset(tc.Initial.RAM)
		cpu.SetRegs(Registers{
			A: uint16(tc.Initial.A), X: uint16(tc.Initial.X), Y: uint16(tc.Initial.Y),
			S: uint16(tc.Initial.S), D: uint16(tc.Initial.D), PC: uint16(tc.Initial.PC),
			DBR: uint8(tc.Initial.DBR), PBR: uint8(tc.Initial.PBR),
			P: uint8(tc.Initial.P), E: tc.Initial.E != 0,
		})
		before := cpu.Cycles()
		cpu.Step()
		got := int(cpu.Cycles() - before)
		t.Logf("initial: %+v", tc.Initial)
		t.Logf("want final: %+v", tc.Final)
		t.Logf("got regs: %+v cycles %d want %d", cpu.Regs(), got, len(tc.Cycles))
		t.Logf("msg: %s", checkTomHarte(cpu, bus, tc, got))
		for i, cc := range tc.Cycles {
			mine := ""
			if i < len(bus.access) {
				a := bus.access[i]
				dir := "r"
				if a.write {
					dir = "w"
				}
				mine = fmt.Sprintf("mine[%d] %s %#x %#x", i, dir, a.addr, a.val)
			}
			t.Logf("cyc %d: want addr %#x val %d write %v valid %v | %s", i, cc.addr, cc.val, cc.write, cc.valid, mine)
		}
		return
	}
	t.Fatalf("test %q not found in %s", name, file)
}
