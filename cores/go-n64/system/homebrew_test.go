package system

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
)

// TestN64SystemtestReportOnly is a report-only harness for
// the open-source n64-systemtest homebrew ROM. It is NOT a
// gate: the ROM normally boots through the PIF/CIC process,
// which lands in slice N64-4, and the project's official
// GitHub offers no prebuilt ROM (no releases; the Rust
// source needs a MIPS toolchain to build). If a legally
// obtained build is ever placed at
// testroms/n64-systemtest.z64, this harness boots it in raw
// CPU mode (IPL3-style: the ROM image from offset 0x1000 is
// copied to RDRAM 0x400 and the CPU starts at the header
// boot address) and logs how far execution gets.
func TestN64SystemtestReportOnly(t *testing.T) {
	path := filepath.Join("..", "testroms", "n64-systemtest.z64")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("n64-systemtest ROM not present (%v); report-only harness idle", err)
	}
	sys, err := New(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Logf("cart: name=%q cic=%s boot=%08x", sys.Cart.Name, sys.Cart.CIC, sys.Cart.BootAddress)
	// IPL3-style raw load: copy ROM[0x1000:] to RDRAM 0x400.
	copy(sys.Mem.RDRAM[0x400:], sys.Cart.ROM[0x1000:])
	sys.CPU.CP0[r4300.Cp0Status] = r4300.StatusCU0 | r4300.StatusCU1
	sys.CPU.Reg[29] = 0xFFFFFFFF803FFFF0 // stack near top of RDRAM (kseg0)
	entry := sys.Cart.BootAddress
	if entry == 0 {
		entry = 0x80000400
	}
	sys.CPU.PC = uint64(int64(int32(entry)))
	sys.CPU.NPC = sys.CPU.PC + 4
	const budget = 3000000
	steps := 0
	lastPC := sys.CPU.PC
	stall := 0
	for steps < budget {
		sys.CPU.Step()
		steps++
		if sys.CPU.PC == lastPC {
			stall++
		} else {
			stall = 0
			lastPC = sys.CPU.PC
		}
		if stall > 200000 {
			break
		}
	}
	t.Logf("ran %d steps, final PC %x, cycles %d, stall-run %d",
		steps, sys.CPU.PC, sys.CPU.Cycles, stall)
	t.Logf("cause=%08x epc=%x status=%08x",
		sys.CPU.CP0[r4300.Cp0Cause], sys.CPU.CP0[r4300.Cp0EPC], sys.CPU.CP0[r4300.Cp0Status])
}
