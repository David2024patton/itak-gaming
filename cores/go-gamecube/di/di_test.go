package di

import (
	"bytes"
	"testing"
)

type fakeSource struct{ data []byte }

func (f *fakeSource) Read(off uint32, dst []byte) int {
	if int(off) >= len(f.data) {
		return 0
	}
	return copy(dst, f.data[off:])
}

func (f *fakeSource) Header() (byte, string, string, byte, byte) {
	return 0xAB, "GS", "01", 2, 3
}

type fakeMem struct{ b []byte }

func (f *fakeMem) Read8(addr uint32) uint8 {
	if int(addr) < len(f.b) {
		return f.b[addr]
	}
	return 0
}

func (f *fakeMem) Write8(addr uint32, v uint8) {
	if int(addr) < len(f.b) {
		f.b[addr] = v
	}
}

func put32(d *DI, off uint32, v uint32) {
	d.Write16(off, uint16(v>>16))
	d.Write16(off+2, uint16(v))
}

func get32(d *DI, off uint32) uint32 {
	return uint32(d.Read16(off))<<16 | uint32(d.Read16(off+2))
}

func newRig() (*DI, *fakeSource, *fakeMem) {
	src := &fakeSource{data: make([]byte, 4096)}
	for i := range src.data {
		src.data[i] = byte(i*7 + 3)
	}
	mem := &fakeMem{b: make([]byte, 1<<16)}
	return New(src, mem), src, mem
}

func TestDMAReadGate(t *testing.T) {
	d, src, mem := newRig()
	// Unmask the transfer complete interrupt.
	d.Write16(regDISR+2, disrTCINTMSK)
	put32(d, regDIMAR, 0x1003) // low 5 bits ignored by DMA
	put32(d, regCMDBUF0, 0xA8000000)
	put32(d, regCMDBUF1, 0x10) // disc offset 0x40
	put32(d, regCMDBUF2, 64)
	d.Write16(regDICR+2, dicrTSTART|dicrDMA)

	if !bytes.Equal(mem.b[0x1000:0x1040], src.data[0x40:0x80]) {
		t.Fatal("MEM1 does not hold the 64 disc bytes at 0x40")
	}
	if got := get32(d, regDILEN); got != 0 {
		t.Fatalf("DILENGTH = %d, want 0 when done", got)
	}
	if d.disr&disrTCINT == 0 {
		t.Fatal("TCINT not set after the read")
	}
	if !d.IRQAsserted() {
		t.Fatal("IRQAsserted false with TCINT set and TCINTMSK on")
	}
	if d.Read16(regDICR+2)&dicrTSTART != 0 {
		t.Fatal("TSTART reads back set after execution")
	}
	// Write 1 to clear drops TCINT and the IRQ.
	d.Write16(regDISR+2, disrTCINTMSK|disrTCINT)
	if d.disr&disrTCINT != 0 {
		t.Fatal("TCINT not cleared by write 1 to clear")
	}
	if d.IRQAsserted() {
		t.Fatal("IRQAsserted still true after clearing TCINT")
	}
}

func TestIdentifyGate(t *testing.T) {
	d, _, mem := newRig()
	put32(d, regDIMAR, 0x2000)
	put32(d, regCMDBUF0, 0xAD000000)
	put32(d, regCMDBUF1, 0)
	put32(d, regCMDBUF2, 32)
	d.Write16(regDICR+2, dicrTSTART|dicrDMA)

	block := mem.b[0x2000:0x2020]
	want := [32]byte{}
	copy(want[4:6], "GS")
	copy(want[6:8], "01")
	want[8] = 2
	want[9] = 3
	want[10] = 0xAB
	if !bytes.Equal(block, want[:]) {
		t.Fatalf("identify block = %v, want %v", block, want)
	}
	if d.disr&disrTCINT == 0 {
		t.Fatal("TCINT not set after identify")
	}
	if got := get32(d, regDILEN); got != 0 {
		t.Fatalf("DILENGTH after identify = %d, want 0", got)
	}
}

func TestUnknownCommandAndRWError(t *testing.T) {
	d, _, mem := newRig()
	put32(d, regCMDBUF0, 0xFF000000)
	d.Write16(regDICR+2, dicrTSTART|dicrDMA)
	if d.disr&disrDEINT == 0 {
		t.Fatal("unknown command did not set DEINT")
	}
	if d.disr&disrTCINT != 0 {
		t.Fatal("unknown command set TCINT")
	}

	// A read command with the RW bit set is an error and
	// transfers nothing.
	d2, _, mem2 := newRig()
	put32(d2, regDIMAR, 0x3000)
	put32(d2, regCMDBUF0, 0xA8000000)
	put32(d2, regCMDBUF1, 0x10)
	put32(d2, regCMDBUF2, 64)
	d2.Write16(regDICR+2, dicrTSTART|dicrDMA|dicrRW)
	if d2.disr&disrDEINT == 0 || d2.disr&disrTCINT != 0 {
		t.Fatal("RW read did not fail with DEINT only")
	}
	if !bytes.Equal(mem2.b[0x3000:0x3040], make([]byte, 64)) {
		t.Fatal("RW read transferred data")
	}
	_ = mem
}

func TestNoDisc(t *testing.T) {
	mem := &fakeMem{b: make([]byte, 1<<16)}
	d := New(nil, mem)
	put32(d, regDIMAR, 0x1000)
	put32(d, regCMDBUF0, 0xA8000000)
	put32(d, regCMDBUF1, 0x10)
	put32(d, regCMDBUF2, 64)
	d.Write16(regDICR+2, dicrTSTART|dicrDMA)
	if d.disr&disrDEINT == 0 || d.disr&disrTCINT != 0 {
		t.Fatal("read with no disc did not fail with DEINT only")
	}
	if !bytes.Equal(mem.b[0x1000:0x1040], make([]byte, 64)) {
		t.Fatal("read with no disc transferred data")
	}
}

func TestStopMotorAndAudioCommands(t *testing.T) {
	d, _, _ := newRig()
	put32(d, regCMDBUF0, 0xE3000000)
	d.Write16(regDICR+2, dicrTSTART)
	if !d.Stopped() {
		t.Fatal("stop motor did not set the stopped flag")
	}
	if d.disr&disrDEINT == 0 {
		t.Fatal("stop motor did not set DEINT")
	}
	put32(d, regCMDBUF0, 0xE1000042)
	d.Write16(regDICR+2, dicrTSTART)
	if got := d.AudioConfig(); got != 0xE1000042 {
		t.Fatalf("AudioConfig = %#x, want 0xE1000042", got)
	}
}

func TestCoverGate(t *testing.T) {
	d, _, _ := newRig()
	d.Write16(regDICVR+2, dicvrCVRINTMSK)
	d.SetCover(true)
	if d.dicvr&dicvrCVR == 0 {
		t.Fatal("CVR not set by SetCover(true)")
	}
	if d.dicvr&dicvrCVRINT == 0 {
		t.Fatal("CVRINT not set by the cover change")
	}
	if !d.IRQAsserted() {
		t.Fatal("IRQAsserted false with CVRINT set and unmasked")
	}
	// Clearing CVRINT and repeating the same state does
	// not raise it again; closing and reopening does.
	d.Write16(regDICVR+2, dicvrCVRINTMSK|dicvrCVRINT)
	d.SetCover(true)
	if d.dicvr&dicvrCVRINT != 0 {
		t.Fatal("CVRINT raised again with no cover change")
	}
	d.SetCover(false)
	if d.dicvr&dicvrCVR != 0 || d.dicvr&dicvrCVRINT == 0 {
		t.Fatal("closing the cover did not clear CVR and raise CVRINT")
	}
}

func TestResetAndSerializeRoundTrip(t *testing.T) {
	d, _, _ := newRig()
	if got := get32(d, regDICFG); got != 1 {
		t.Fatalf("DICFG reset value = %d, want 1", got)
	}
	d.Write16(regDISR+2, disrTCINTMSK|disrDEINTMSK)
	put32(d, regCMDBUF0, 0xE1000042)
	d.Write16(regDICR+2, dicrTSTART) // audio command, sets DEINT
	put32(d, regCMDBUF0, 0xE3000000)
	d.Write16(regDICR+2, dicrTSTART) // stop motor
	put32(d, regDICFG, 0x1234)
	put32(d, regDIMAR, 0x8000)
	d.SetCover(true)

	state := d.Serialize()
	if len(state) != SerializeSize {
		t.Fatalf("Serialize length = %d, want %d", len(state), SerializeSize)
	}
	twin, _, _ := newRig()
	if err := twin.Deserialize(state); err != nil {
		t.Fatal(err)
	}
	for _, off := range []uint32{regDISR, regDICVR, regCMDBUF0, regCMDBUF1, regCMDBUF2, regDICFG, regDIMAR, regDILEN, regDICR} {
		if get32(d, off) != get32(twin, off) {
			t.Fatalf("register %#x differs after restore: %#x vs %#x", off, get32(d, off), get32(twin, off))
		}
	}
	if !twin.Stopped() || twin.AudioConfig() != 0xE3000000 {
		// The audio word was replaced by the stop command
		// buffer; AudioConfig keeps the audio word itself.
		if twin.AudioConfig() != 0xE1000042 {
			t.Fatalf("AudioConfig after restore = %#x, want 0xE1000042", twin.AudioConfig())
		}
	}
	if !twin.Stopped() {
		t.Fatal("stopped flag not preserved")
	}
	if twin.IRQAsserted() != d.IRQAsserted() {
		t.Fatal("IRQAsserted differs after restore")
	}
	if err := twin.Deserialize(state[:4]); err != ErrBadState {
		t.Fatalf("Deserialize short state = %v, want ErrBadState", err)
	}
}
