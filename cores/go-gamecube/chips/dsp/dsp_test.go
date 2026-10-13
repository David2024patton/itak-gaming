package dsp

import (
	"bytes"
	"testing"
)

// Register numbers used by the hand assembled programs
// below, per the published GCDSP register map.
const (
	regAR0  = 0
	regWR0  = 8
	regACH0 = 16
	regAXL0 = 24
	regAXL1 = 25
	regAXH0 = 26
	regACL0 = 28
	regACM0 = 30
	regACM1 = 31
)

type fakeMem struct{ b []byte }

func (m *fakeMem) Read8(addr uint32) uint8 {
	if int(addr) < len(m.b) {
		return m.b[addr]
	}
	return 0
}

func (m *fakeMem) Write8(addr uint32, v uint8) {
	if int(addr) < len(m.b) {
		m.b[addr] = v
	}
}

// boot loads prog at IRAM 0 and releases the reset.
func boot(prog []uint16) *DSP {
	d := New(nil)
	d.UploadIRAM(0, prog)
	d.Write16(mmControl, 0)
	return d
}

func TestAccumulatorArithmetic(t *testing.T) {
	d := boot([]uint16{
		0x0080 | regAXL0, 0xFFFE, // LRI $ax0.l, -2
		0x0080 | regAXH0, 0x0003, // LRI $ax0.h, 3
		0x9000,                   // MUL $ax0: prod = -2 * 3
		0x6E00,                   // MOVP $ac0
		0x8100,                   // CLR $ac0
		0x0080 | regACM0, 0x0005, // LRI $ac0.m, 5
		0x0080 | regACM1, 0x0007, // LRI $ac1.m, 7
		0x4C00,                   // ADD $ac0, $ac1
		0x8F00,                   // SET40
		0x0080 | regACL0, 0xFFFF, // LRI $ac0.l, -1
		0x0080 | regACM0, 0xFFFF, // LRI $ac0.m, -1 (H sign extends)
		0x0080 | regACH0, 0x007F, // LRI $ac0.h, 0x7F (last: full 40 bit max)
		0x7600,                   // INC $ac0: 40 bit overflow
		0x8E00,                   // SET16
		0x0080 | regACM0, 0x7FFF, // LRI $ac0.m, 0x7FFF
		0x0080 | regACL0, 0xFFFF, // LRI $ac0.l, -1 (acc = MaxInt32)
		0x7600, // INC $ac0: clamps at MaxInt32
		0x0021, // HALT
	})
	// Run only up to the MOVP first for the product
	// checks, by stepping a fresh copy would complicate
	// the test; instead verify product views before the
	// program overwrites PROD nowhere else, so step the
	// first three instructions on a twin.
	twin := boot([]uint16{
		0x0080 | regAXL0, 0xFFFE,
		0x0080 | regAXH0, 0x0003,
		0x9000,
		0x0021,
	})
	twin.Step(3)
	if twin.prod != -6 {
		t.Fatalf("prod = %d, want -6", twin.prod)
	}
	if v := twin.getReg(20); v != 0xFFFA {
		t.Fatalf("PROD.L = %#04x, want 0xFFFA", v)
	}
	if v := twin.getReg(21); v != 0xFFFF {
		t.Fatalf("PROD.M = %#04x, want 0xFFFF", v)
	}
	if v := twin.getReg(22); v != 0xFFFF {
		t.Fatalf("PROD.H = %#04x, want 0xFFFF (sign extended)", v)
	}
	if v := twin.getReg(23); v != 0xFFFF {
		t.Fatalf("PROD.M2 = %#04x, want 0xFFFF (sign extended)", v)
	}
	twin.Step(1) // HALT: MOVP is not in this twin, step once more is harmless

	d.Step(4) // through MOVP
	if d.acc[0] != -6 {
		t.Fatalf("acc0 after MOVP = %d, want -6", d.acc[0])
	}
	d.Step(4) // CLR, two LRI, ADD
	if v := d.getReg(regACM0); v != 12 {
		t.Fatalf("ac0.m after ADD = %d, want 12", v)
	}
	if d.acc[0] != 12<<16 {
		t.Fatalf("acc0 = %d, want %d", d.acc[0], 12<<16)
	}
	d.Step(5) // SET40, three LRI, INC
	if d.acc[0] != -(1 << 39) {
		t.Fatalf("acc0 after 40 bit INC overflow = %d, want %d", d.acc[0], -(1 << 39))
	}
	if d.sr&srOver == 0 || d.sr&srOverStk == 0 {
		t.Fatalf("SR = %#04x, want V and OS set", d.sr)
	}
	d.Step(4) // SET16, two LRI, INC (clamp)
	if d.acc[0] != (1<<31)-1 {
		t.Fatalf("acc0 after 32 bit clamp = %d, want %d", d.acc[0], (1<<31)-1)
	}
	if d.sr&srOver == 0 {
		t.Fatalf("SR = %#04x, want V set on clamp", d.sr)
	}
}

func TestHardwareLoop(t *testing.T) {
	d := boot([]uint16{
		0x0080 | regACM0, 0x0000, // 0: LRI $ac0.m, 0
		0x0080 | regACM1, 0x0003, // 2: LRI $ac1.m, 3
		0x1105, 0x0006, // 4: BLOOPI 5, end 6
		0x4C00, // 6: ADD $ac0, $ac1
		0x0021, // 7: HALT
	})
	d.Step(100)
	if v := d.getReg(regACM0); v != 15 {
		t.Fatalf("ac0.m = %d, want 15 (5 iterations of +3)", v)
	}
	for s := 0; s < 4; s++ {
		if d.depth[s] != 0 {
			t.Fatalf("stack %d depth = %d, want 0 after loop", s, d.depth[s])
		}
	}
}

func TestWrapAddressing(t *testing.T) {
	d := boot([]uint16{
		0x0080 | regAR0, 0x010E, // LRI $ar0, 0x010E
		0x0080 | regWR0, 0x000F, // LRI $wr0, 15 (block of 16)
		0x0080 | regACM0, 0x1111,
		0x1B00 | regACM0, // SRRI @ar0, $ac0.m
		0x0080 | regACM0, 0x2222,
		0x1B00 | regACM0,
		0x0080 | regACM0, 0x3333,
		0x1B00 | regACM0,
		0x0080 | regACM0, 0x4444,
		0x1B00 | regACM0,
		0x0021,
	})
	d.Step(100)
	want := map[uint16]uint16{0x010E: 0x1111, 0x010F: 0x2222, 0x0100: 0x3333, 0x0101: 0x4444}
	for addr, v := range want {
		if d.dram[addr] != v {
			t.Fatalf("DRAM[%#04x] = %#04x, want %#04x", addr, d.dram[addr], v)
		}
	}
	if d.dram[0x0102] != 0 {
		t.Fatalf("DRAM[0x0102] = %#04x, want 0 (wrapped, not linear)", d.dram[0x0102])
	}
	if d.ar[0] != 0x0102 {
		t.Fatalf("ar0 = %#04x, want 0x0102", d.ar[0])
	}
}

func TestMailboxRoundTrip(t *testing.T) {
	d := New(nil)
	d.UploadIRAM(0, []uint16{
		0x00C0 | regACM0, 0xFFFC, // 0: LR $ac0.m, @DMBH
		0x00E0 | regACM0, 0x0012, // 2: SR @0x12, $ac0.m
		0x00C0 | regACM0, 0xFFFD, // 4: LR $ac0.m, @DMBL
		0x00E0 | regACM0, 0x0013, // 6: SR @0x13, $ac0.m
		0x00C0 | regACM0, 0x0010, // 8: LR $ac0.m, @0x10
		0x1401,                   // 10: LSL $ac0, 1
		0x00E0 | regACM0, 0x0011, // 11: SR @0x11, $ac0.m
		0x16FC, 0x1234, // 13: SI @DMBH, 0x1234
		0x16FD, 0x5678, // 15: SI @DMBL, 0x5678
		0x0021, // 17: HALT
	})
	d.dram[0x10] = 37
	d.Write16(mmControl, 0) // release reset
	// CPU to DSP mail through MMIO.
	d.Write16(mmMBDSPHigh, 0x0ABC)
	d.Write16(mmMBDSPLow, 0x1234)
	d.Step(100)
	if d.dram[0x12] != 0x8ABC {
		t.Fatalf("DRAM[0x12] = %#04x, want 0x8ABC (DMBH with pending bit)", d.dram[0x12])
	}
	if d.dram[0x13] != 0x1234 {
		t.Fatalf("DRAM[0x13] = %#04x, want 0x1234 (DMBL)", d.dram[0x13])
	}
	if d.cpuPending {
		t.Fatal("mail pending still set after DMBL read")
	}
	if d.dram[0x11] != 74 {
		t.Fatalf("DRAM[0x11] = %d, want 74 (37 doubled)", d.dram[0x11])
	}
	// DSP to CPU mail read back through MMIO.
	if v := d.Read16(mmMBCPUHigh); v != 0x8000|0x1234 {
		t.Fatalf("MB_CPU_H = %#04x, want 0x9234", v)
	}
	if v := d.Read16(mmMBCPULow); v != 0x5678 {
		t.Fatalf("MB_CPU_L = %#04x, want 0x5678", v)
	}
	if v := d.Read16(mmMBCPUHigh); v != 0x1234 {
		t.Fatalf("MB_CPU_H after clear = %#04x, want 0x1234 (ready cleared)", v)
	}
}

func TestDIRQAndControl(t *testing.T) {
	d := boot([]uint16{
		0x16F9, 0x0001, // SI @DIRQ, 1
		0x0021, // HALT
	})
	d.Step(10)
	if d.Read16(mmControl)&ctlDSPINT == 0 {
		t.Fatal("DSPINT not set after DIRQ write")
	}
	if d.IRQAsserted() {
		t.Fatal("IRQAsserted with DSPINTMSK clear")
	}
	d.Write16(mmControl, ctlDSPINTMSK)
	if !d.IRQAsserted() {
		t.Fatal("IRQAsserted false with DSPINT set and unmasked")
	}
	d.Write16(mmControl, ctlDSPINT|ctlDSPINTMSK) // write 1 to clear
	if d.Read16(mmControl)&ctlDSPINT != 0 {
		t.Fatal("DSPINT not cleared by write 1 to clear")
	}
	if d.IRQAsserted() {
		t.Fatal("IRQAsserted still true after clear")
	}
}

func TestARDMA(t *testing.T) {
	mem := &fakeMem{b: make([]byte, 0x1000)}
	d := New(mem)
	for i := 0; i < 64; i++ {
		mem.b[0x40+i] = byte(i + 1)
	}
	// MEM1 0x40 to ARAM 0x100, 64 bytes.
	d.Write16(mmARAddrH, 0)
	d.Write16(mmARAddrL, 0x0100)
	d.Write16(mmARMemH, 0)
	d.Write16(mmARMemL, 0x0040)
	d.Write16(mmARCntH, 0)
	d.Write16(mmARCntL, 0x40)
	for i := 0; i < 64; i++ {
		if d.ARAM()[0x100+i] != byte(i+1) {
			t.Fatalf("ARAM[0x100+%d] = %d, want %d", i, d.ARAM()[0x100+i], i+1)
		}
	}
	if d.Read16(mmControl)&ctlARINT == 0 {
		t.Fatal("ARINT not set after DMA")
	}
	if d.Read16(mmARCntH) != 0 || d.Read16(mmARCntL) != 0 {
		t.Fatal("count registers do not read back zero after DMA")
	}
	// Reverse direction: ARAM to MEM1.
	d.Write16(mmControl, ctlARINT) // clear ARINT (also releases reset)
	for i := range mem.b {
		mem.b[i] = 0
	}
	for i := 0; i < 64; i++ {
		d.ARAM()[0x100+i] = byte(0xF0 + i)
	}
	d.Write16(mmARCntH, 0x8000) // direction: ARAM to MEM1
	d.Write16(mmARCntL, 0x40)
	for i := 0; i < 64; i++ {
		if mem.b[0x40+i] != byte(0xF0+i) {
			t.Fatalf("MEM[0x40+%d] = %#02x, want %#02x", i, mem.b[0x40+i], byte(0xF0+i))
		}
	}
	if d.Read16(mmControl)&ctlARINT == 0 {
		t.Fatal("ARINT not set after reverse DMA")
	}
	d.Write16(mmControl, ctlARINTMSK)
	if !d.IRQAsserted() {
		t.Fatal("IRQAsserted false with ARINT set and unmasked")
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	prog := []uint16{
		0x0080 | regACM0, 0x1111, // LRI $ac0.m, 0x1111
		0x0080 | regAR0, 0x0020, // LRI $ar0, 0x20
		0x1B00 | regACM0,         // SRRI @ar0, $ac0.m
		0x0080 | regACM0, 0x2222, // LRI $ac0.m, 0x2222
		0x1B00 | regACM0, // SRRI @ar0, $ac0.m
		0x0021,           // HALT
	}
	d1 := boot(prog)
	d1.Step(3)
	state := d1.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("len(Serialize()) = %d, want SerializeSize %d", len(state), SerializeSize)
	}
	d2 := New(nil)
	if err := d2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	d1.Step(50)
	d2.Step(50)
	if d1.dram[0x20] != 0x1111 || d1.dram[0x21] != 0x2222 {
		t.Fatalf("d1 DRAM = %#04x, %#04x", d1.dram[0x20], d1.dram[0x21])
	}
	if !bytes.Equal(d1.Serialize(), d2.Serialize()) {
		t.Fatal("restored DSP diverged from the original after continuing")
	}
	if err := d2.Deserialize(state[:len(state)-1]); err == nil {
		t.Fatal("Deserialize accepted a short state")
	}
	bad := append([]byte(nil), state...)
	bad[0] = 0xFF
	if err := d2.Deserialize(bad); err == nil {
		t.Fatal("Deserialize accepted a bad version")
	}
}

func TestSmallGates(t *testing.T) {
	// SBSET and SBCLR operate on SR bits 6 to 13.
	d := boot([]uint16{
		0x1303, // SBSET 3: SR bit 9 (IE)
		0x1306, // SBSET 6: SR bit 12 (AM)
		0x1203, // SBCLR 3: SR bit 9 clear
		0x0021,
	})
	d.Step(10)
	if d.sr&srIntEn != 0 {
		t.Fatalf("SR = %#04x, IE should be cleared by SBCLR", d.sr)
	}
	if d.sr&srAccMode == 0 {
		t.Fatalf("SR = %#04x, AM should be set by SBSET 6", d.sr)
	}

	// CALL and RET.
	d2 := boot([]uint16{
		0x02BF, 0x0005, // 0: CALL 5
		0x00E0 | regACM0, 0x0020, // 2: SR @0x20, $ac0.m
		0x0021,                   // 4: HALT
		0x0080 | regACM0, 0x0042, // 5: LRI $ac0.m, 0x42
		0x02DF, // 7: RET
	})
	d2.Step(20)
	if d2.dram[0x20] != 0x42 {
		t.Fatalf("DRAM[0x20] = %#04x, want 0x42 (CALL/RET path)", d2.dram[0x20])
	}

	// Parallel extended op: CLR $ac1 with 'MV $ax1.l,
	// $ac0.m riding in the low byte.
	d3 := boot([]uint16{
		0x0080 | regACM0, 0x1234, // LRI $ac0.m, 0x1234
		0x0080 | regACM1, 0x0009, // LRI $ac1.m, 9
		0x8916, // CLR $ac1 'MV $ax1.l, $ac0.m
		0x0021,
	})
	d3.Step(10)
	if d3.acc[1] != 0 {
		t.Fatalf("acc1 = %d, want 0 after CLR", d3.acc[1])
	}
	if v := d3.getReg(regAXL1); v != 0x1234 {
		t.Fatalf("ax1.l = %#04x, want 0x1234 from extended MV", v)
	}
	if d3.acc[0] != 0x1234<<16 {
		t.Fatalf("acc0 = %d, want unchanged %d", d3.acc[0], 0x1234<<16)
	}
}
