package system

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-n64/chips/r4300"
)

// Assembler helpers (kept local to the test).
func rr(rs, rt, rd, sa, funct uint32) uint32 {
	return rs<<21 | rt<<16 | rd<<11 | sa<<6 | funct
}
func ii(op, rs, rt uint32, imm uint16) uint32 {
	return op<<26 | rs<<21 | rt<<16 | uint32(imm)
}
func lui(rt uint32, imm uint16) uint32 { return ii(0x0F, 0, rt, imm) }
func ori(rt, rs uint32, imm uint16) uint32 {
	return ii(0x0D, rs, rt, imm)
}
func addiu(rt, rs uint32, imm int16) uint32 { return ii(0x09, rs, rt, uint16(imm)) }
func lw(rt, rs uint32, off int16) uint32    { return ii(0x23, rs, rt, uint16(off)) }
func sw(rt, rs uint32, off int16) uint32    { return ii(0x2B, rs, rt, uint16(off)) }
func bne(rs, rt uint32, off int16) uint32   { return ii(0x05, rs, rt, uint16(off)) }
func mtc0(rt, rd uint32) uint32 {
	return 0x10<<26 | 4<<21 | rt<<16 | rd<<11
}

const (
	t0 = 8
	t1 = 9
	t2 = 10
	t3 = 11
	t4 = 12
	t5 = 13
	t6 = 14
	t7 = 15
	s0 = 16
	s1 = 17
	s2 = 18
)

// testROM builds a 1 MB cartridge image whose bytes from
// offset 0x1000 follow byte(i) = i*3+5.
func testROM() []byte {
	rom := make([]byte, 1<<20)
	binary.BigEndian.PutUint32(rom[0x00:], 0x80371240)
	binary.BigEndian.PutUint32(rom[0x08:], 0x80000400)
	copy(rom[0x20:], "GO-N64 TEST CART    ")
	for i := 0x1000; i < len(rom); i++ {
		rom[i] = byte(i*3 + 5)
	}
	return rom
}

// testProgram installs a TLB mapping (VA 0x00400000 -> PA
// 0x00100000), stores a word through it, runs a 64-byte PI
// DMA from ROM offset 0x1000 to RDRAM 0x200000, sums the 16
// transferred words, and writes the sum and the TLB word to
// RDRAM 0x300/0x304 before spinning.
func testProgram() []uint32 {
	return []uint32{
		lui(t0, 0x0040),         // 0: t0 = 0x00400000
		mtc0(t0, 10),            // 1: EntryHi
		mtc0(0, 5),              // 2: PageMask = 0
		addiu(t1, 0, 0x401F),    // 3
		mtc0(t1, 2),             // 4: EntryLo0
		addiu(t1, 0, 0x405F),    // 5
		mtc0(t1, 3),             // 6: EntryLo1
		mtc0(0, 0),              // 7: Index = 0
		0x42000002,              // 8: tlbwi
		lui(t2, 0x1234),         // 9
		ori(t2, t2, 0x5678),     // 10
		sw(t2, t0, 0),           // 11: store via TLB
		lui(s0, 0xA460),         // 12: PI registers (kseg1)
		lui(t3, 0x0020),         // 13
		sw(t3, s0, 0),           // 14: PI_DRAM_ADDR = 0x200000
		lui(t3, 0x1000),         // 15
		ori(t3, t3, 0x1000),     // 16
		sw(t3, s0, 4),           // 17: PI_CART_ADDR = 0x10001000
		addiu(t3, 0, 63),        // 18
		sw(t3, s0, 8),           // 19: PI_RD_LEN -> DMA
		lui(t4, 0x8020),         // 20: KSEG0 0x80200000
		addiu(t5, 0, 16),        // 21: counter
		rr(0, 0, s1, 0, 0x2D),   // 22: s1 = 0 (daddu)
		ii(0x27, t4, t6, 0),     // 23: loop: lwu t6, 0(t4)
		rr(s1, t6, s1, 0, 0x2D), // 24: daddu s1, s1, t6
		addiu(t4, t4, 4),        // 25
		addiu(t5, t5, -1),       // 26
		bne(t5, 0, -5),          // 27: -> 23
		lw(s2, t0, 0),           // 28: read back TLB word
		lui(t7, 0x8000),         // 29
		sw(s1, t7, 0x300),       // 30: sum low word
		rr(0, s1, t6, 0, 0x3E),  // 31: dsrl32 t6 = s1 >> 32
		sw(t6, t7, 0x304),       // 32: sum high word
		sw(s2, t7, 0x308),       // 33: TLB word
		0x0A000022,              // 34: j self (0x80000088)
		0,                       // 35: nop
	}
}

// bootTestMachine loads the test ROM, plants the program at
// RDRAM 0, and points the CPU at it in kernel mode.
func bootTestMachine(t *testing.T) *System {
	t.Helper()
	sys, err := New(testROM())
	if err != nil {
		t.Fatal(err)
	}
	prog := testProgram()
	for i, in := range prog {
		sys.Mem.Write(uint32(i*4), 4, uint64(in))
	}
	sys.CPU.CP0[r4300.Cp0Status] = r4300.StatusCU0 | r4300.StatusCU1
	sys.CPU.PC = 0xFFFFFFFF80000000
	sys.CPU.NPC = sys.CPU.PC + 4
	return sys
}

func expectedSum() uint64 {
	var sum uint64
	for w := 0; w < 16; w++ {
		var word uint64
		for b := 0; b < 4; b++ {
			i := 0x1000 + w*4 + b
			word = word<<8 | uint64(byte(i*3+5))
		}
		sum += word
	}
	return sum
}

func TestProgramTLBAndPIDMA(t *testing.T) {
	sys := bootTestMachine(t)
	for i := 0; i < 200; i++ {
		sys.CPU.Step()
	}
	// TLB store reached physical 0x100000.
	if got := sys.Mem.Read(0x100000, 4); got != 0x12345678 {
		t.Errorf("tlb store: %08x", got)
	}
	// PI DMA landed the ROM bytes at RDRAM 0x200000.
	for i := 0; i < 64; i++ {
		if sys.Mem.RDRAM[0x200000+i] != byte((0x1000+i)*3+5) {
			t.Fatalf("dma byte %d wrong", i)
		}
	}
	if got := sys.Mem.Read(0x300, 4) | sys.Mem.Read(0x304, 4)<<32; got != expectedSum() {
		t.Errorf("sum: got %x want %x", got, expectedSum())
	}
	if got := sys.Mem.Read(0x308, 4); got != 0x12345678 {
		t.Errorf("tlb word cell: got %08x", got)
	}
}

func TestMidRunSaveRestoreDeterministic(t *testing.T) {
	a := bootTestMachine(t)
	for i := 0; i < 30; i++ { // stop mid-loop, after the DMA
		a.CPU.Step()
	}
	state := a.Serialize()
	for i := 0; i < 170; i++ {
		a.CPU.Step()
	}

	b := bootTestMachine(t)
	for i := 0; i < 10; i++ { // different history, then adopt A's state
		b.CPU.Step()
	}
	if err := b.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 170; i++ {
		b.CPU.Step()
	}
	if !bytes.Equal(a.Mem.RDRAM, b.Mem.RDRAM) {
		t.Error("RDRAM diverged after restore")
	}
	if a.CPU.Reg != b.CPU.Reg {
		t.Error("registers diverged after restore")
	}
	if a.CPU.PC != b.CPU.PC || a.CPU.Cycles != b.CPU.Cycles {
		t.Error("pc/cycles diverged after restore")
	}
	if got := b.Mem.Read(0x300, 4) | b.Mem.Read(0x304, 4)<<32; got != expectedSum() {
		t.Errorf("restored run sum: %x want %x", got, expectedSum())
	}
}

func TestSerializeValidation(t *testing.T) {
	sys := bootTestMachine(t)
	state := sys.Serialize()
	if len(state) != sys.SerializeSize() {
		t.Fatalf("size %d want %d", len(state), sys.SerializeSize())
	}
	bad := append([]byte(nil), state...)
	bad[0] = 99
	if err := sys.Deserialize(bad); err == nil {
		t.Error("bad version accepted")
	}
	if err := sys.Deserialize(state[:len(state)-1]); err == nil {
		t.Error("truncated state accepted")
	}
	exp, err := NewWithOptions(testROM(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := exp.Deserialize(state); err == nil {
		t.Error("expansion mismatch accepted")
	}
}

func TestFramebufferBlankAndButtons(t *testing.T) {
	sys := bootTestMachine(t)
	fb := sys.Framebuffer()
	if len(fb) != Width*Height {
		t.Fatalf("framebuffer %d", len(fb))
	}
	for i, px := range fb {
		if px != 0xFF000000 {
			t.Fatalf("pixel %d = %08x, want opaque black", i, px)
		}
	}
	sys.SetButtons(ButtonStart | ButtonA)
	if sys.Buttons() != ButtonStart|ButtonA {
		t.Error("buttons not recorded")
	}
	if sys.Title() != "GO-N64 TEST CART" {
		t.Errorf("title %q", sys.Title())
	}
}

func TestRunFrameBudget(t *testing.T) {
	sys := bootTestMachine(t)
	sys.RunFrame()
	if sys.CPU.Cycles < CyclesPerFrame {
		t.Errorf("frame consumed %d cycles, want at least %d", sys.CPU.Cycles, CyclesPerFrame)
	}
}
