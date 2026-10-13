package arm7tdmi

import "testing"

// ramBus is a flat word-addressed RAM stub for the halt
// tests: 64 KB at address 0, everything else reads zero.
type ramBus struct {
	mem [0x10000]byte
}

func (b *ramBus) get(addr uint32, size int) uint32 {
	var v uint32
	for i := 0; i < size; i++ {
		a := addr + uint32(i)
		if a < uint32(len(b.mem)) {
			v |= uint32(b.mem[a]) << (8 * i)
		}
	}
	return v
}

func (b *ramBus) Read(addr uint32, size int) uint32  { return b.get(addr, size) }
func (b *ramBus) Fetch(addr uint32, size int) uint32 { return b.get(addr, size) }
func (b *ramBus) Write(addr uint32, size int, val uint32) {
	for i := 0; i < size; i++ {
		a := addr + uint32(i)
		if a < uint32(len(b.mem)) {
			b.mem[a] = byte(val >> (8 * i))
		}
	}
}

func put32(b *ramBus, addr, w uint32) {
	b.mem[addr] = byte(w)
	b.mem[addr+1] = byte(w >> 8)
	b.mem[addr+2] = byte(w >> 16)
	b.mem[addr+3] = byte(w >> 24)
}

// TestHaltedCPUTakesIRQ pins the wake rule the BIOS waits
// depend on: an accepted interrupt ends a halt, the IRQ
// exception is entered, handler code runs, and its
// SUBS PC, LR, #4 return resumes the interrupted
// instruction. Before this was pinned, Step took the
// exception but left Halted set, so the core froze
// inside the exception entry and no handler ever ran
// (the Anguna deadlock).
func TestHaltedCPUTakesIRQ(t *testing.T) {
	bus := &ramBus{}
	// IRQ vector at 0x18: branch to the handler at 0x100.
	put32(bus, 0x18, 0xEA000038) // B 0x100
	// Handler: count the call in R1, return.
	put32(bus, 0x100, 0xE2811001) // ADD R1, R1, #1
	put32(bus, 0x104, 0xE25EF004) // SUBS PC, LR, #4
	// Main at 0x200: one real instruction, then a branch
	// to self that the interrupt lands in front of.
	put32(bus, 0x200, 0xE3A00055) // MOV R0, #0x55
	put32(bus, 0x204, 0xEAFFFFFE) // B .

	c := New(bus)
	c.setCPSR(ModeUSR)
	c.Jump(0x200)
	c.Step() // MOV executes; the branch sits in execute.
	if got := c.R[0]; got != 0x55 {
		t.Fatalf("R0 = %#08x, want 0x55 (MOV did not run)", got)
	}
	// A halted core with no interrupt request stays put.
	c.Halted = true
	if n := c.Step(); n != 1 || !c.Halted {
		t.Fatalf("halted step without IRQ: cycles %d, halted %v, want 1 and still halted", n, c.Halted)
	}
	// The interrupt arrives: the core must wake into the
	// IRQ exception, not freeze inside the entry.
	c.IRQLine = true
	c.Step()
	if c.Halted {
		t.Fatal("accepted IRQ left the core halted; the handler can never run")
	}
	if mode := c.CPSR & 0x1F; mode != ModeIRQ {
		t.Fatalf("mode after IRQ entry = %#02x, want IRQ (%#02x)", mode, ModeIRQ)
	}
	if got := c.R[15] - 8; got != 0x18 {
		t.Fatalf("executing address after IRQ entry = %#08x, want the vector 0x18", got)
	}
	c.Step() // vector branch
	c.Step() // handler ADD
	if got := c.R[1]; got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
	c.Step() // SUBS return
	if mode := c.CPSR & 0x1F; mode != ModeUSR {
		t.Fatalf("mode after handler return = %#02x, want USR (%#02x)", mode, ModeUSR)
	}
	if got := c.R[15] - 8; got != 0x204 {
		t.Fatalf("resumed at %#08x, want the interrupted instruction 0x204", got)
	}
	c.IRQLine = false
	c.Step() // the interrupted branch finally executes
	if got := c.R[15] - 8; got != 0x204 {
		t.Fatalf("after resume the branch at 0x204 should loop to itself, executing %#08x", got)
	}
}
