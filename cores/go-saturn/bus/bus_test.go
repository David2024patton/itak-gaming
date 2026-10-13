package bus

import "testing"

func check32(t *testing.T, name string, got uint32, want uint32) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got 0x%08X, want 0x%08X", name, got, want)
	}
}

func check16(t *testing.T, name string, got uint16, want uint16) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got 0x%04X, want 0x%04X", name, got, want)
	}
}

func check8(t *testing.T, name string, got uint8, want uint8) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got 0x%02X, want 0x%02X", name, got, want)
	}
}

func checkInt32(t *testing.T, name string, got int32, want int32) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %d, want %d", name, got, want)
	}
}

func checkBool(t *testing.T, name string, got bool, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
}

func TestWorkRAMLowRoundTrips(t *testing.T) {
	b := New()
	if len(b.WorkRAMLow()) != 1<<20 {
		t.Fatalf("work low size: got %d", len(b.WorkRAMLow()))
	}
	b.Write8(0x00200000, 0xA5)
	check8(t, "low byte", b.Read8(0x00200000), 0xA5)
	b.Write16(0x00200010, 0xBEEF)
	check16(t, "low word", b.Read16(0x00200010), 0xBEEF)
	check8(t, "low word high byte", b.Read8(0x00200010), 0xBE)
	check8(t, "low word low byte", b.Read8(0x00200011), 0xEF)
	b.Write32(0x00200100, 0x12345678)
	check32(t, "low long", b.Read32(0x00200100), 0x12345678)
	check16(t, "low long top word", b.Read16(0x00200100), 0x1234)
	check16(t, "low long bottom word", b.Read16(0x00200102), 0x5678)
	b.WorkRAMLow()[0x200] = 0x5A
	check8(t, "low backing visible", b.Read8(0x00200200), 0x5A)
}

func TestWorkRAMHighMirrorAndAliases(t *testing.T) {
	b := New()
	if len(b.WorkRAMHigh()) != 1<<20 {
		t.Fatalf("work high size: got %d", len(b.WorkRAMHigh()))
	}
	b.Write8(0x06000000, 0x11)
	check8(t, "high byte", b.Read8(0x06000000), 0x11)
	b.Write16(0x06000002, 0x2233)
	check16(t, "high word", b.Read16(0x06000002), 0x2233)
	b.Write32(0x06000000, 0xDEADBEEF)
	check32(t, "high long", b.Read32(0x06000000), 0xDEADBEEF)
	check32(t, "high mirror one meg", b.Read32(0x06100000), 0xDEADBEEF)
	check32(t, "high cache through alias", b.Read32(0x26000000), 0xDEADBEEF)
	b.Write32(0x26000004, 0x01020304)
	check32(t, "high written via alias", b.Read32(0x06000004), 0x01020304)
}

func TestBackupRAMOddBytes(t *testing.T) {
	b := New()
	if len(b.BackupRAM()) != 32<<10 {
		t.Fatalf("backup size: got %d", len(b.BackupRAM()))
	}
	b.Write8(0x00180000, 0xAB)
	check8(t, "backup even write lands", b.BackupRAM()[0], 0xAB)
	check8(t, "backup odd read", b.Read8(0x00180001), 0xAB)
	check8(t, "backup even read is FF", b.Read8(0x00180000), 0xFF)
	b.Write8(0x00180003, 0xCD)
	check8(t, "backup odd write lands", b.BackupRAM()[1], 0xCD)
	b.Write16(0x00180004, 0x1234)
	check8(t, "backup word low byte wins cell", b.BackupRAM()[2], 0x34)
	check16(t, "backup word read lanes", b.Read16(0x00180004), 0xFF34)
	b.Write32(0x00180008, 0xA1B2C3D4)
	check8(t, "backup long cell two", b.BackupRAM()[4], 0xB2)
	check8(t, "backup long cell three", b.BackupRAM()[5], 0xD4)
	check32(t, "backup long read lanes", b.Read32(0x00180008), 0xFFB2FFD4)
}

func TestSMPCoddBytes(t *testing.T) {
	b := New()
	b.Write8(0x00100000, 0x55)
	check16(t, "smpc even write ignored", b.Read16(0x00100000), 0xFF00)
	b.Write8(0x00100001, 0x66)
	check8(t, "smpc odd write stored", b.Read8(0x00100001), 0x66)
	check8(t, "smpc even read is FF", b.Read8(0x00100000), 0xFF)
	check8(t, "smpc mirrored register", b.Read8(0x00100081), 0x66)
	b.Write16(0x00100002, 0xABCD)
	check16(t, "smpc word odd byte carries", b.Read16(0x00100002), 0xFFCD)
	b.Write32(0x00100004, 0x11223344)
	check32(t, "smpc long odd bytes carry", b.Read32(0x00100004), 0xFF22FF44)
}

func TestCaptureFlags(t *testing.T) {
	b := New()
	checkBool(t, "slave starts clear", b.CaptureEvent(1), false)
	checkBool(t, "master starts clear", b.CaptureEvent(0), false)
	b.Write16(0x01000000, 0x0000)
	checkBool(t, "minit sets slave", b.CaptureEvent(1), true)
	checkBool(t, "minit flag clears after read", b.CaptureEvent(1), false)
	b.Write16(0x01000000, 0xFFFF)
	checkBool(t, "minit does not set master", b.CaptureEvent(0), false)
	checkBool(t, "minit slave still pending", b.CaptureEvent(1), true)
	b.Write8(0x01000000, 0x12)
	checkBool(t, "minit byte write no flag", b.CaptureEvent(1), false)
	b.Write32(0x01000000, 0x12345678)
	checkBool(t, "minit long write no flag", b.CaptureEvent(1), false)
	b.Write16(0x01800000, 0x0001)
	checkBool(t, "sinit sets master", b.CaptureEvent(0), true)
	checkBool(t, "sinit flag clears after read", b.CaptureEvent(0), false)
	b.Write8(0x01800000, 0x12)
	checkBool(t, "sinit byte write no flag", b.CaptureEvent(0), false)
	check16(t, "minit read word", b.Read16(0x01000000), 0xFFFF)
	check32(t, "sinit read long", b.Read32(0x01800000), 0xFFFFFFFF)
	check8(t, "minit read byte", b.Read8(0x01000000), 0xFF)
}

func TestCDBlockRegisters(t *testing.T) {
	b := New()
	base := uint32(0x05800000)
	b.Write16(base+CDCR1, 0xBEEF)
	check16(t, "cd cr1 word", b.Read16(base+CDCR1), 0xBEEF)
	check16(t, "cd cr1 mirrored 64", b.Read16(base+0x40+CDCR1), 0xBEEF)
	check16(t, "cd cr1 mirrored far", b.Read16(base+0x400+CDCR1), 0xBEEF)
	check8(t, "cd cr1 high byte", b.Read8(base+CDCR1), 0xBE)
	check8(t, "cd cr1 low byte", b.Read8(base+CDCR1+1), 0xEF)
	b.Write32(base+CDCR2, 0x01020304)
	check32(t, "cd cr2 long as two words", b.Read32(base+CDCR2), 0x01020304)
	check16(t, "cd cr2 top word", b.Read16(base+CDCR2), 0x0102)
	b.Write16(base+CDHIRQ, 0x00F0)
	check16(t, "cd hirq word", b.Read16(base+HIRQ), 0x00F0)
	b.Write8(base+CDCR4+1, 0x77)
	check16(t, "cd cr4 byte lane", b.Read16(base+CR4), 0x0077)
}

func TestSCURegisterMirroring(t *testing.T) {
	b := New()
	b.Write32(0x05FE0000, 0xCAFEBABE)
	check32(t, "scu long", b.Read32(0x05FE0000), 0xCAFEBABE)
	check32(t, "scu mirrored long", b.Read32(0x05FE0100), 0xCAFEBABE)
	b.Write8(0x05FE0010, 0x99)
	check8(t, "scu byte mirrored", b.Read8(0x05FE0110), 0x99)
}

func TestVDP1AndSoundStorage(t *testing.T) {
	b := New()
	b.Write32(0x05C00000, 0x11223344)
	check32(t, "vdp1 vram long", b.Read32(0x05C00000), 0x11223344)
	b.Write32(0x05C7FFFC, 0x55667788)
	check32(t, "vdp1 vram top long", b.Read32(0x05C7FFFC), 0x55667788)
	b.Write16(0x05C80000, 0xABCD)
	check16(t, "vdp1 fb word", b.Read16(0x05C80000), 0xABCD)
	check16(t, "vdp1 fb mirrored", b.Read16(0x05CC0000), 0xABCD)
	b.Write8(0x05D00000, 0x42)
	check8(t, "vdp1 regs byte", b.Read8(0x05D00000), 0x42)
	check8(t, "vdp1 regs mirrored", b.Read8(0x05D00080), 0x42)
	b.Write32(0x05A00000, 0x99AABBCC)
	check32(t, "sound ram long", b.Read32(0x05A00000), 0x99AABBCC)
	b.Write8(0x05B00000, 0x31)
	check8(t, "scsp regs mirrored", b.Read8(0x05B01000), 0x31)
}

func TestVDP2Storage(t *testing.T) {
	b := New()
	b.Write16(0x05E00000, 0x1357)
	check16(t, "vdp2 vram word", b.Read16(0x05E00000), 0x1357)
	check16(t, "vdp2 vram mirrored", b.Read16(0x05E80000), 0x1357)
	b.Write16(0x05F00000, 0x2468)
	check16(t, "vdp2 cram word", b.Read16(0x05F00000), 0x2468)
	check16(t, "vdp2 cram mirrored", b.Read16(0x05F01000), 0x2468)
	b.Write8(0x05F80000, 0x77)
	check8(t, "vdp2 regs byte", b.Read8(0x05F80000), 0x77)
	check8(t, "vdp2 regs mirrored", b.Read8(0x05F80200), 0x77)
}

func TestOpenAndStubWindows(t *testing.T) {
	b := New()
	check32(t, "bios reads zero", b.Read32(0x00000000), 0)
	b.Write32(0x00000000, 0xFFFFFFFF)
	check32(t, "bios writes ignored", b.Read32(0x00000000), 0)
	check32(t, "bios mirrored zero", b.Read32(0x00080000), 0)
	check32(t, "cart cs0 open zero", b.Read32(0x02000000), 0)
	check32(t, "cart cs1 open zero", b.Read32(0x04000000), 0)
	check32(t, "a bus dummy zero", b.Read32(0x05000000), 0)
	check32(t, "unmapped reads zero", b.Read32(0x00800000), 0)
}

func TestOnChipGenericWindowPerCPU(t *testing.T) {
	b := New()
	b.SetCurrentCPU(0)
	b.Write32(0xFFFFFE00, 0x11112222)
	check32(t, "onchip master word", b.Read32(0xFFFFFE00), 0x11112222)
	b.SetCurrentCPU(1)
	check32(t, "onchip slave separate", b.Read32(0xFFFFFE00), 0)
	b.Write32(0xFFFFFE00, 0x33334444)
	check32(t, "onchip slave word", b.Read32(0xFFFFFE00), 0x33334444)
	b.SetCurrentCPU(0)
	check32(t, "onchip master intact", b.Read32(0xFFFFFE00), 0x11112222)
}

func TestDivideBasic(t *testing.T) {
	b := New()
	b.Write32(RegDVSR, 7)
	b.Write32(RegDVDNT, 100)
	checkInt32(t, "100 over 7 quotient", int32(b.Read32(RegDVDNT)), 14)
	checkInt32(t, "100 over 7 remainder", int32(b.Read32(RegDVDNTH)), 2)
	checkInt32(t, "100 over 7 quotient low mirror reg", int32(b.Read32(RegDVDNTL)), 14)
	b.Write32(RegDVDNT, 0xFFFFFF9C)
	checkInt32(t, "neg 100 over 7 quotient", int32(b.Read32(RegDVDNT)), -14)
	checkInt32(t, "neg 100 over 7 remainder", int32(b.Read32(RegDVDNTH)), -2)
	check32(t, "mirror reads remainder", b.Read32(0xFFFFFF18), 0xFFFFFFFE)
	check32(t, "mirror reads quotient", b.Read32(0xFFFFFF1C), 0xFFFFFFF2)
}

func TestDivide64(t *testing.T) {
	b := New()
	b.Write32(RegDVSR, 3)
	b.Write32(RegDVDNTH, 1)
	b.Write32(RegDVDNTL, 0)
	checkInt32(t, "64 bit quotient", int32(b.Read32(RegDVDNT)), 1431655765)
	checkInt32(t, "64 bit remainder", int32(b.Read32(RegDVDNTH)), 1)
	q, r := b.Divide64(0, 4294967296)
	checkInt32(t, "divide64 helper quotient", q, 1431655765)
	checkInt32(t, "divide64 helper remainder", r, 1)
}

func TestDivideByZero(t *testing.T) {
	b := New()
	b.Write32(RegDVSR, 0)
	b.Write32(RegDVDNT, 100)
	check32(t, "div zero saturates positive", b.Read32(RegDVDNT), 0x7FFFFFFF)
	check32(t, "div zero sets ovf", b.Read32(RegDVCR)&1, 1)
	checkInt32(t, "div zero remainder shift", int32(b.Read32(RegDVDNTH)), 0)

	c := New()
	c.Write32(RegDVSR, 0)
	c.Write32(RegDVDNT, 0xFFFFFF9C)
	check32(t, "neg div zero saturates negative", c.Read32(RegDVDNT), 0x80000000)
	check32(t, "neg div zero sets ovf", c.Read32(RegDVCR)&1, 1)
	checkInt32(t, "neg div zero remainder shift", int32(c.Read32(RegDVDNTH)), -1)
}

func TestDivideIntMinByNegOne(t *testing.T) {
	b := New()
	b.Write32(RegDVSR, 0xFFFFFFFF)
	b.Write32(RegDVDNT, 0x80000000)
	check32(t, "int min quotient", b.Read32(RegDVDNT), 0x80000000)
	check32(t, "int min remainder", b.Read32(RegDVDNTH), 0)
	check32(t, "int min no ovf", b.Read32(RegDVCR)&1, 0)
}

func TestDividePerCPUIsolation(t *testing.T) {
	b := New()
	b.SetCurrentCPU(0)
	b.Write32(RegDVSR, 7)
	b.Write32(RegDVDNT, 100)
	b.SetCurrentCPU(1)
	check32(t, "slave divisor starts zero", b.Read32(RegDVSR), 0)
	check32(t, "slave quotient starts zero", b.Read32(RegDVDNT), 0)
	b.Write32(RegDVSR, 2)
	b.Write32(RegDVDNT, 50)
	checkInt32(t, "slave quotient", int32(b.Read32(RegDVDNT)), 25)
	b.SetCurrentCPU(0)
	check32(t, "master divisor intact", b.Read32(RegDVSR), 7)
	checkInt32(t, "master quotient intact", int32(b.Read32(RegDVDNT)), 14)
	checkInt32(t, "master remainder intact", int32(b.Read32(RegDVDNTH)), 2)
	q, r := b.Divide32(1, 51)
	checkInt32(t, "helper slave quotient", q, 25)
	checkInt32(t, "helper slave remainder", r, 1)
}

func TestDivideByteAndWordComposition(t *testing.T) {
	b := New()
	b.Write8(RegDVSR+3, 7)
	check32(t, "divisor via low byte", b.Read32(RegDVSR), 7)
	b.Write32(RegDVDNT, 100)
	check8(t, "quotient via low byte", b.Read8(RegDVDNT+3), 14)
	check16(t, "quotient via low word", b.Read16(RegDVDNT+2), 14)
	check8(t, "remainder via low byte", b.Read8(RegDVDNTH+3), 2)
	b.Write16(RegDVSR+2, 9)
	check32(t, "divisor via low word", b.Read32(RegDVSR), 9)
	q, r := b.Divide32(0, 90)
	checkInt32(t, "composed divisor quotient", q, 10)
	checkInt32(t, "composed divisor remainder", r, 0)
	b.Write32(RegVCRDIV, 0x12345678)
	check32(t, "vcrdiv stored", b.Read32(RegVCRDIV), 0x12345678)
	b.Write32(RegDVDEN, 0)
	check32(t, "dvcr alias clears", b.Read32(RegDVCR), 0)
}
