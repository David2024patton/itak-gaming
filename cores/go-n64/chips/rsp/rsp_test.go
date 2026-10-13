package rsp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Small assembler helpers for hand-derived test programs.
func rr(rs, rt, rd, sa, funct uint32) uint32 {
	return rs<<21 | rt<<16 | rd<<11 | sa<<6 | funct
}
func ii(op, rs, rt uint32, imm uint16) uint32 {
	return op<<26 | rs<<21 | rt<<16 | uint32(imm)
}
func addiu(rt, rs uint32, imm int16) uint32 { return ii(0x09, rs, rt, uint16(imm)) }
func lui(rt uint32, imm uint16) uint32      { return ii(0x0F, 0, rt, imm) }
func ori(rt, rs uint32, imm uint16) uint32  { return ii(0x0D, rs, rt, imm) }
func sw(rt, rs uint32, off int16) uint32    { return ii(0x2B, rs, rt, uint16(off)) }
func lw(rt, rs uint32, off int16) uint32    { return ii(0x23, rs, rt, uint16(off)) }

const breakInstr = 0x0000000D

func loadProgram(r *RSP, words []uint32) {
	for i, w := range words {
		binary.BigEndian.PutUint32(r.IMEM[i*4:], w)
	}
}

// start clears the halt flag and runs until the RSP stops
// (BREAK or budget exhausted).
func start(r *RSP) {
	r.WriteCP0(CP0Status, 0x001) // clear halt
	r.Run(100000)
}

func runProgram(t *testing.T, words []uint32) *RSP {
	t.Helper()
	r := New()
	loadProgram(r, words)
	start(r)
	if !r.Halted() {
		t.Fatal("program did not halt (no BREAK reached?)")
	}
	return r
}

func dmemWord(r *RSP, off int) uint32 {
	return binary.BigEndian.Uint32(r.DMEM[off:])
}

func TestScalarArithmeticAndMemory(t *testing.T) {
	// DMEM fixture for the LWL/LWR checks.
	r := New()
	binary.BigEndian.PutUint32(r.DMEM[0x200:], 0xAABBCCDD)
	r.DMEM[0x204] = 0xEE
	loadProgram(r, []uint32{
		addiu(8, 0, 7),         // t0 = 7
		addiu(9, 0, 5),         // t1 = 5
		rr(8, 9, 0, 0, 0x18),   // mult t0, t1
		rr(0, 0, 10, 0, 0x12),  // mflo t2 = 35
		sw(10, 0, 0x100),       // DMEM[0x100] = 35
		lui(11, 0x1122),        // t3 = 0x11220000
		ori(11, 11, 0x3344),    // t3 = 0x11223344
		ii(0x22, 0, 11, 0x201), // lwl t3, 0x201($0): top 3 bytes from DMEM
		sw(11, 0, 0x104),       // store merged value
		ii(0x26, 0, 11, 0x204), // lwr t3, 0x204($0): low byte from DMEM
		sw(11, 0, 0x108),       // store again
		addiu(12, 0, -17),      // t4 = -17
		addiu(13, 0, 5),        // t5 = 5
		rr(12, 13, 0, 0, 0x1A), // div t4, t5
		rr(0, 0, 14, 0, 0x12),  // mflo t6 = -3
		rr(0, 0, 15, 0, 0x10),  // mfhi t7 = -2
		sw(14, 0, 0x10C),
		sw(15, 0, 0x110),
		breakInstr,
	})
	start(r)
	if got := dmemWord(r, 0x100); got != 35 {
		t.Errorf("mult/mflo/sw: %d", got)
	}
	// LWL at 0x201 merges BB CC DD over the top bytes,
	// keeping the low byte 0x44.
	if got := dmemWord(r, 0x104); got != 0xBBCCDD44 {
		t.Errorf("lwl merge: %08x", got)
	}
	// LWR at 0x204 then replaces the low byte with 0xEE.
	if got := dmemWord(r, 0x108); got != 0xBBCCDDEE {
		t.Errorf("lwr merge: %08x", got)
	}
	if got := dmemWord(r, 0x10C); got != 0xFFFFFFFD {
		t.Errorf("div quotient: %08x", got)
	}
	if got := dmemWord(r, 0x110); got != 0xFFFFFFFE {
		t.Errorf("div remainder: %08x", got)
	}
}

func TestBranchLinkAndDelaySlot(t *testing.T) {
	// jal to a subroutine that adds 1 and returns; the
	// delay slot of the jal must execute exactly once, and
	// jr's delay slot too.
	r := runProgram(t, []uint32{
		addiu(8, 0, 40),       // 0x00
		ii(0x03, 0, 0, 5),     // 0x04: jal 0x14
		addiu(8, 8, 1),        // 0x08: delay slot, t0 = 41
		sw(8, 0, 0x100),       // 0x0C: after return, store
		breakInstr,            // 0x10
		addiu(8, 8, 1),        // 0x14: subroutine: t0 = 42
		rr(31, 0, 0, 0, 0x08), // 0x18: jr $ra
		addiu(9, 0, 7),        // 0x1C: jr delay slot
	})
	if got := dmemWord(r, 0x100); got != 42 {
		t.Errorf("jal/jr flow: %d", got)
	}
	if r.Reg[9] != 7 {
		t.Errorf("jr delay slot not executed: %d", r.Reg[9])
	}
	if r.Reg[31] != 0x0C {
		t.Errorf("link register: %x", r.Reg[31])
	}
}

func TestStatusWriteSemantics(t *testing.T) {
	r := New()
	if got := r.Status(); got != StatusHalt {
		t.Fatalf("power-on status: %08x", got)
	}
	// Set interrupt-on-break (bit 8) and signal 3 set
	// (bit 10 + 2*3 = bit 16).
	r.WriteRegister(0x10, 0x100|(1<<16))
	st := r.Status()
	if st&StatusIntrOnBreak == 0 {
		t.Error("interrupt-on-break not set")
	}
	if st&(1<<10) == 0 { // signal 3 reads at bit 7+3
		t.Error("signal 3 not set")
	}
	// Clear signal 3 (bit 9 + 2*3 = bit 15).
	r.WriteRegister(0x10, 1<<15)
	if r.Status()&(1<<10) != 0 {
		t.Error("signal 3 not cleared")
	}
	// Set halt stops execution; PC writes are ignored
	// while running.
	r.WriteRegister(0x10, 0x001) // clear halt
	if r.Halted() {
		t.Fatal("halt not cleared")
	}
	r.WritePC(0x40)
	if r.PC != 0 {
		t.Errorf("PC write while running took effect: %x", r.PC)
	}
	r.WriteRegister(0x10, 0x002) // set halt
	r.WritePC(0x40)
	if r.PC != 0x40 || r.NPC != 0x44 {
		t.Errorf("PC write while halted: PC %x NPC %x", r.PC, r.NPC)
	}
}

func TestSemaphore(t *testing.T) {
	r := New()
	if got := r.ReadRegister(0x1C); got != 0 {
		t.Fatalf("first semaphore read: %d", got)
	}
	if got := r.ReadRegister(0x1C); got != 1 {
		t.Fatalf("second semaphore read (test-and-set): %d", got)
	}
	r.WriteRegister(0x1C, 0) // documented release: write clears
	if got := r.ReadRegister(0x1C); got != 0 {
		t.Fatalf("semaphore after release: %d", got)
	}
	// Same through the RSP's own CP0 view.
	if got := r.ReadCP0(CP0Semaphore); got != 1 {
		t.Fatalf("CP0 semaphore read after set: %d", got)
	}
}

func TestDMARowsAndSkip(t *testing.T) {
	r := New()
	dram := make([]byte, 4096)
	for i := range dram {
		dram[i] = byte(i)
	}
	r.AttachDRAM(dram)
	// DRAM 0x40 -> DMEM 0x100, two 16-byte rows, skip 16.
	r.WriteCP0(CP0MemAddr, 0x100)
	r.WriteCP0(CP0DramAddr, 0x40)
	r.WriteCP0(CP0RdLen, 8|(1<<12)|(16<<20))
	for k := 0; k < 16; k++ {
		if r.DMEM[0x100+k] != byte(0x40+k) {
			t.Fatalf("row 0 byte %d: %02x", k, r.DMEM[0x100+k])
		}
		if r.DMEM[0x110+k] != byte(0x60+k) {
			t.Fatalf("row 1 byte %d: %02x", k, r.DMEM[0x110+k])
		}
	}
	if got := r.ReadCP0(CP0MemAddr); got != 0x120 {
		t.Errorf("SP_MEM_ADDR after DMA: %x", got)
	}
	if got := r.ReadCP0(CP0DramAddr); got != 0x80 {
		t.Errorf("SP_DRAM_ADDR after DMA: %x", got)
	}
	if got := r.ReadCP0(CP0DMABusy); got != 0 {
		t.Errorf("DMA busy after atomic transfer: %d", got)
	}
	// And back out: DMEM 0x100 -> DRAM 0x200, one row.
	r.WriteCP0(CP0MemAddr, 0x100)
	r.WriteCP0(CP0DramAddr, 0x200)
	r.WriteCP0(CP0WrLen, 8)
	for k := 0; k < 16; k++ {
		if dram[0x200+k] != byte(0x40+k) {
			t.Fatalf("write-back byte %d: %02x", k, dram[0x200+k])
		}
	}
	// IMEM bank select (bit 12) reaches IMEM, not DMEM.
	r.WriteCP0(CP0MemAddr, 0x1000)
	r.WriteCP0(CP0DramAddr, 0x40)
	r.WriteCP0(CP0RdLen, 8)
	if r.IMEM[0] != 0x40 || r.IMEM[15] != 0x4F {
		t.Errorf("IMEM DMA: %02x %02x", r.IMEM[0], r.IMEM[15])
	}
	if r.DMEM[0] == 0x40 && r.DMEM[1] == 0x41 {
		t.Error("IMEM DMA leaked into DMEM")
	}
}

func TestCP0DrivenDMA(t *testing.T) {
	// The RSP starts a DMA itself with MTC0, exactly as
	// microcode does, then reads back DMA_BUSY as 0.
	dram := make([]byte, 1024)
	for i := range dram {
		dram[i] = byte(0x80 + i)
	}
	mtc0 := func(rt, rd uint32) uint32 { return 0x10<<26 | 4<<21 | rt<<16 | rd<<11 }
	mfc0 := func(rt, rd uint32) uint32 { return 0x10<<26 | 0<<21 | rt<<16 | rd<<11 }
	r := New()
	r.AttachDRAM(dram)
	loadProgram(r, []uint32{
		addiu(8, 0, 0x80),
		mtc0(8, CP0MemAddr),
		addiu(8, 0, 0x20),
		mtc0(8, CP0DramAddr),
		addiu(8, 0, 8), // 16 bytes, one row
		mtc0(8, CP0RdLen),
		mfc0(9, CP0DMABusy),
		sw(9, 0, 0x100),
		breakInstr,
	})
	start(r)
	for k := 0; k < 16; k++ {
		if r.DMEM[0x80+k] != byte(0xA0+k) {
			t.Fatalf("CP0 DMA byte %d: %02x", k, r.DMEM[0x80+k])
		}
	}
	if got := dmemWord(r, 0x100); got != 0 {
		t.Errorf("DMA_BUSY readback: %d", got)
	}
}

func TestBreakInterrupt(t *testing.T) {
	r := New()
	fired := false
	r.OnInterrupt = func(on bool) { fired = on }
	r.WriteCP0(CP0Status, 0x100) // interrupt on break
	loadProgram(r, []uint32{breakInstr})
	start(r)
	if !fired {
		t.Error("BREAK with interrupt-on-break did not assert the interrupt")
	}
	if r.Status()&StatusBroke == 0 {
		t.Error("broke bit not set after BREAK")
	}
	// Clearing the interrupt through the status write
	// drops the output.
	r.WriteCP0(CP0Status, 0x008)
	if fired {
		t.Error("interrupt not cleared by status write")
	}
	// Without interrupt-on-break, BREAK stays quiet.
	r2 := New()
	fired2 := false
	r2.OnInterrupt = func(on bool) { fired2 = on }
	loadProgram(r2, []uint32{breakInstr})
	start(r2)
	if fired2 {
		t.Error("BREAK asserted interrupt without interrupt-on-break")
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	r := New()
	dram := make([]byte, 256)
	r.AttachDRAM(dram)
	loadProgram(r, []uint32{
		addiu(8, 0, 1234),
		sw(8, 0, 0x40),
		breakInstr,
	})
	r.V[3][5] = 0xBEEF
	r.ACC[2] = -123456789
	r.VCC, r.VCO, r.VCE = 0xCAFE, 0x0FF0, 0xA5
	start(r)
	buf := make([]byte, SerializeSize)
	if n := r.Serialize(buf); n != SerializeSize {
		t.Fatalf("serialize wrote %d, SerializeSize %d", n, SerializeSize)
	}
	r2 := New()
	if err := r2.Deserialize(buf); err != nil {
		t.Fatal(err)
	}
	buf2 := make([]byte, SerializeSize)
	r2.Serialize(buf2)
	if !bytes.Equal(buf, buf2) {
		t.Error("serialize/deserialize/serialize not byte identical")
	}
	if r2.DMEM[0x43] != 0xD2 { // 1234 = 0x04D2, low byte at 0x43
		t.Errorf("DMEM not restored: %02x", r2.DMEM[0x43])
	}
	if r2.V[3][5] != 0xBEEF || r2.ACC[2] != -123456789 || r2.VCC != 0xCAFE || r2.VCE != 0xA5 {
		t.Error("vector state not restored")
	}
	if !r2.Halted() {
		t.Error("halt state not restored")
	}
}
