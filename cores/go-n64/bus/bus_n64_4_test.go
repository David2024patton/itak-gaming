package bus

import (
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-n64/cart"
	"github.com/davidpatton/itak-gaming/cores/go-n64/pif"
)

// An SI write DMA delivers a Joybus frame to the PIF
// (which parses it), and an SI read DMA executes it and
// lands the replies in RDRAM. The SI interrupt fires on
// each completion and clears on a status write.
func TestSIDMAJoybusRoundTrip(t *testing.T) {
	m := New(nil, false)
	m.PIF.SetController(0, 0x8000, 3, -4) // A button
	// Frame at RDRAM 0x2000: ch0 state read, then FE.
	m.RDRAM[0x2000] = 0x01 // TX
	m.RDRAM[0x2001] = 0x04 // RX
	m.RDRAM[0x2002] = 0x01 // cmd: read state
	m.RDRAM[0x2007] = 0xFE
	m.RDRAM[0x2000+0x3F] = 0x01 // command byte: parse frame

	var line []bool
	m.OnMIChange = func(active bool) { line = append(line, active) }

	m.Write(0x04800000, 4, 0x2000)     // SI_DRAM_ADDR
	m.Write(0x04800010, 4, 0x1FC007C0) // WR64B: RDRAM to PIF
	if got := m.Read(0x0480001C, 4); got&siStatusIntr == 0 {
		t.Fatal("SI interrupt bit not set after write DMA")
	}
	intr, _ := m.MIState()
	if intr&MIIntrSI == 0 {
		t.Fatal("MI SI bit not set after write DMA")
	}
	// Until the read DMA, the receive space is untouched.
	if m.RDRAM[0x2003] != 0 {
		t.Fatal("frame executed on the write DMA")
	}
	m.Write(0x0480001C, 4, 0) // ack SI
	intr, _ = m.MIState()
	if intr&MIIntrSI != 0 {
		t.Fatal("MI SI bit not cleared by status write")
	}
	m.Write(0x04800004, 4, 0x1FC007C0) // RD64B: execute + copy out
	if m.RDRAM[0x2003] != 0x80 || m.RDRAM[0x2004] != 0x00 {
		t.Errorf("buttons landed as %02x %02x", m.RDRAM[0x2003], m.RDRAM[0x2004])
	}
	if int8(m.RDRAM[0x2005]) != 3 || int8(m.RDRAM[0x2006]) != -4 {
		t.Errorf("stick landed as %d,%d", int8(m.RDRAM[0x2005]), int8(m.RDRAM[0x2006]))
	}
	if len(line) == 0 {
		t.Error("MI line callback never fired")
	}
}

// The SRAM window is a direct store on the CPU bus and
// through PI DMA in both directions.
func TestSaveWindowSRAM(t *testing.T) {
	m := New(nil, false)
	m.Saves.Configure(cart.SaveSRAM)
	m.Write(0x08000000, 4, 0xDEADBEEF)
	if got := m.Read(0x08000000, 4); got != 0xDEADBEEF {
		t.Fatalf("sram word = %08x", got)
	}
	m.Write(0x08007FFF, 1, 0x42)
	if got := m.Read(0x08007FFF, 1); got != 0x42 {
		t.Fatalf("sram byte = %02x", got)
	}
	// PI DMA in: RDRAM 0x3000 to SRAM 0x100.
	for i := 0; i < 16; i++ {
		m.RDRAM[0x3000+i] = byte(i + 1)
	}
	m.Write(0x04600000, 4, 0x3000)     // PI_DRAM_ADDR
	m.Write(0x04600004, 4, 0x08000100) // PI_CART_ADDR (domain 2)
	m.Write(0x0460000C, 4, 15)         // PI_WR_LEN
	if m.Saves.SRAM[0x100] != 1 || m.Saves.SRAM[0x10F] != 16 {
		t.Fatal("PI DMA into SRAM failed")
	}
	// PI DMA out: SRAM 0x100 to RDRAM 0x4000.
	m.Write(0x04600000, 4, 0x4000)
	m.Write(0x04600008, 4, 15) // PI_RD_LEN
	if m.RDRAM[0x4000] != 1 || m.RDRAM[0x400F] != 16 {
		t.Fatal("PI DMA out of SRAM failed")
	}
}

// Flash commands arrive as 32-bit CPU writes to the
// window; the page buffer fills by PI DMA in program
// mode; execute programs the page.
func TestSaveWindowFlash(t *testing.T) {
	m := New(nil, false)
	m.Saves.Configure(cart.SaveFlash)
	m.Write(0x08000000, 4, 0xA5000002) // latch program page 2
	m.Write(0x08000000, 4, 0xB4000000) // program mode
	// DMA 128 bytes from RDRAM 0x2000 into the window
	// at the page address.
	for i := 0; i < 128; i++ {
		m.RDRAM[0x2000+i] = byte(0x80 + i)
	}
	m.Write(0x04600000, 4, 0x2000)
	m.Write(0x04600004, 4, 0x08000000+2*128)
	m.Write(0x0460000C, 4, 127)        // PI_WR_LEN
	m.Write(0x08000000, 4, 0xD2000000) // execute
	m.Write(0x08000000, 4, 0xF0000000) // read mode
	if got := m.Read(0x08000000+2*128, 4); got != 0x80818283 {
		t.Fatalf("flash word = %08x", got)
	}
	// Status mode via the CPU window.
	m.Write(0x08000000, 4, 0xE1000000)
	if got := m.Read(0x08000000, 8); got != 0x1111800100C2001D {
		t.Fatalf("flash status = %016x", got)
	}
}

// PIF RAM written directly by the CPU parses its frame
// the same way (the documented model parses on any
// command-byte write).
func TestPIFDirectWriteParses(t *testing.T) {
	m := New(nil, false)
	m.Write(0x1FC007C0, 4, 0x01030000) // TX=1 RX=3 cmd=0x00 (info)
	m.Write(0x1FC007C0+0x3F, 1, 0x01)  // command byte
	m.PIF.Execute()
	ram := m.PIFRAM()
	if ram[3] != 0x05 || ram[4] != 0x00 || ram[5] != 0x02 {
		t.Errorf("info reply = %02x %02x %02x", ram[3], ram[4], ram[5])
	}
	if ram[0x3F]&0x01 != 0 {
		t.Error("frame bit not cleared after parse")
	}
	_ = pif.RAMSize
}
