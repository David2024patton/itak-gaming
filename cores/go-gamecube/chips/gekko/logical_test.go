package gekko

import "testing"

// TestLogicalXFormFieldLayout pins the architected field
// layout of the register logical instructions (GCN-4):
// the source sits in the f1 field (rS), the destination
// in the f2 field (rA), the second source in f3 (rB),
// the same layout andi. and ori use in D form and the
// shifts use in X form. The GCN-4 enginetest fixture
// caught an earlier build writing the result into the
// f1 register; these vectors use distinct source and
// destination registers so the fields cannot be swapped
// without failing.
func TestLogicalXFormFieldLayout(t *testing.T) {
	const (
		xoAnd  = 28
		xoAndc = 60
		xoOr   = 444
		xoOrc  = 412
		xoXor  = 316
		xoNand = 476
		xoNor  = 124
		xoEqv  = 284
	)
	cases := []struct {
		name string
		xo   uint32
		a    uint32 // rS value, f1 field
		b    uint32 // rB value, f3 field
		want uint32
	}{
		{"and", xoAnd, 0xF0F0FF00, 0x0FF00FF0, 0x00F00F00},
		{"andc", xoAndc, 0xF0F0FF00, 0x0FF00FF0, 0xF000F000},
		{"or", xoOr, 0xF0F0FF00, 0x0FF00FF0, 0xFFF0FFF0},
		{"orc", xoOrc, 0xF0F0FF00, 0x0FF00FF0, 0xF0FFFF0F},
		{"xor", xoXor, 0xF0F0FF00, 0x0FF00FF0, 0xFF00F0F0},
		{"nand", xoNand, 0xF0F0FF00, 0x0FF00FF0, 0xFF0FF0FF},
		{"nor", xoNor, 0xF0F0FF00, 0x0FF00FF0, 0x000F000F},
		{"eqv", xoEqv, 0xF0F0FF00, 0x0FF00FF0, 0x00FF0F0F},
	}
	for _, tc := range cases {
		bus := newRAM()
		c := New(bus)
		// or r7, r5, r6 shape: f1 = rS = 5, f2 = rA = 7,
		// f3 = rB = 6. Sources in r5 and r6, the result
		// must land in r7 and leave r5 untouched.
		prog(c, bus, 0x1000, ins(31, 5, 7, 6, tc.xo, false))
		c.GPR[5] = tc.a
		c.GPR[6] = tc.b
		c.GPR[7] = 0xDEADBEEF
		steps(c, 1)
		if c.GPR[7] != tc.want {
			t.Fatalf("%s: r7 = %#x, want %#x", tc.name, c.GPR[7], tc.want)
		}
		if c.GPR[5] != tc.a {
			t.Fatalf("%s: source r5 clobbered: %#x", tc.name, c.GPR[5])
		}
		if c.GPR[6] != tc.b {
			t.Fatalf("%s: source r6 clobbered: %#x", tc.name, c.GPR[6])
		}
	}

	// mr r29, r3 (or r29, r3, r3) moves without touching
	// the source: the exact shape the GCN-4 fixture opens
	// with to save the boot argument.
	bus := newRAM()
	c := New(bus)
	prog(c, bus, 0x1000, ins(31, 3, 29, 3, xoOr, false))
	c.GPR[3] = 0x80005100
	steps(c, 1)
	if c.GPR[29] != 0x80005100 || c.GPR[3] != 0x80005100 {
		t.Fatalf("mr r29, r3: r29 = %#x, r3 = %#x", c.GPR[29], c.GPR[3])
	}
}
