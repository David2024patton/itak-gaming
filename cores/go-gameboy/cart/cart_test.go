package cart

import "testing"

// makeROM builds a synthetic ROM image of the given size with a
// valid header for the given type and size codes. bankFill, when
// non-nil, fills each 16 KB bank with its bank index plus bankFill.
func makeROM(t *testing.T, size int, typeCode, romCode, ramCode uint8, fillBanks bool) []byte {
	t.Helper()
	data := make([]byte, size)
	if fillBanks {
		for b := 0; b < size/16384; b++ {
			for i := 0; i < 16384; i++ {
				data[b*16384+i] = uint8(b)
			}
		}
	}
	copy(data[0x104:0x134], bootLogo[:])
	title := "TESTCART"
	copy(data[0x134:], title)
	data[0x147] = typeCode
	data[0x148] = romCode
	data[0x149] = ramCode
	data[0x14D] = headerChecksum(data)
	return data
}

func TestLoadHeaderFacts(t *testing.T) {
	data := makeROM(t, 32768, 0x1B, 0, 3, false)
	c, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "TESTCART" {
		t.Fatalf("Title = %q, want TESTCART", c.Title)
	}
	if c.Kind != MBC5 || !c.HasBattery {
		t.Fatalf("Kind = %v battery=%v, want MBC5 with battery", c.Kind, c.HasBattery)
	}
	if c.ROMBanks != 2 || c.RAMSize != 32768 {
		t.Fatalf("ROMBanks=%d RAMSize=%d, want 2 and 32768", c.ROMBanks, c.RAMSize)
	}
	if !c.HeaderOK || !c.LogoOK {
		t.Fatalf("HeaderOK=%v LogoOK=%v, want both true", c.HeaderOK, c.LogoOK)
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	if _, err := Load(make([]byte, 100)); err == nil {
		t.Fatal("tiny image accepted")
	}
	bad := makeROM(t, 32768, 0x00, 0, 0, false)
	bad[0x147] = 0x77
	if _, err := Load(bad); err == nil {
		t.Fatal("unsupported mapper accepted")
	}
}

func TestMBC1BankSwitching(t *testing.T) {
	data := makeROM(t, 8*16384, 0x03, 2, 2, true)
	c, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ReadROM(0x4000); got != 1 {
		t.Fatalf("power-on switchable bank reads bank %d, want 1", got)
	}
	c.Write(0x2000, 2)
	if got := c.ReadROM(0x4000); got != 2 {
		t.Fatalf("after bank write switchable bank = %d, want 2", got)
	}
	// Bank zero written to the selector acts as bank one.
	c.Write(0x2000, 0)
	if got := c.ReadROM(0x4000); got != 1 {
		t.Fatalf("bank zero write selected bank %d, want 1", got)
	}
	// Upper bits join in for big ROMs: mode 0, upper=1, low=2 gives
	// bank 0x22 masked over 8 banks = bank 2... use a 64-bank image
	// instead for a clean check.
	data = makeROM(t, 64*16384, 0x03, 5, 3, true)
	c, _ = Load(data)
	c.Write(0x2000, 2)
	c.Write(0x4000, 1)
	if got := c.ReadROM(0x4000); got != 0x22 {
		t.Fatalf("upper bits: switchable bank = %d, want 34 (0x22)", got)
	}
	if got := c.ReadROM(0x0000); got != 0 {
		t.Fatalf("mode 0 fixed bank = %d, want 0", got)
	}
	c.Write(0x6000, 1) // RAM banking mode: fixed region takes upper bits
	if got := c.ReadROM(0x0000); got != 0x20 {
		t.Fatalf("mode 1 fixed bank = %d, want 32 (0x20)", got)
	}
}

func TestMBC1RAMGatingAndBanking(t *testing.T) {
	data := makeROM(t, 32768, 0x03, 0, 3, false) // 32 KB RAM, 4 banks
	c, _ := Load(data)
	c.Write(0xA000, 0x55)
	if got := c.ReadRAM(0xA000); got != 0xFF {
		t.Fatalf("RAM read while disabled = 0x%02X, want 0xFF", got)
	}
	c.Write(0x0000, 0x0A)
	c.Write(0xA000, 0x55)
	if got := c.ReadRAM(0xA000); got != 0x55 {
		t.Fatalf("RAM read = 0x%02X, want 0x55", got)
	}
	// RAM banking needs mode 1 before the bank register counts.
	c.Write(0x6000, 1)
	c.Write(0x4000, 2)
	c.Write(0xA000, 0x77)
	c.Write(0x4000, 0)
	if got := c.ReadRAM(0xA000); got != 0x55 {
		t.Fatalf("bank 0 RAM = 0x%02X, want 0x55", got)
	}
	c.Write(0x4000, 2)
	if got := c.ReadRAM(0xA000); got != 0x77 {
		t.Fatalf("bank 2 RAM = 0x%02X, want 0x77", got)
	}
}

func TestMBC3RTCSelectAndLatch(t *testing.T) {
	data := makeROM(t, 32768, 0x10, 0, 3, false) // MBC3+TIMER+RAM+BATTERY
	c, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasTimer {
		t.Fatal("HasTimer false for type 0x10")
	}
	c.Write(0x0000, 0x0A) // enable RAM and RTC access
	c.RTC.Seconds = 0x42
	c.RTC.DayHigh = 0xC0
	c.Write(0x4000, 0x08) // select RTC seconds
	// Before a latch pulse the latched copy is stale.
	if got := c.ReadRAM(0xA000); got != 0x00 {
		t.Fatalf("latched seconds before latch = 0x%02X, want 0x00", got)
	}
	c.Write(0x6000, 0x00)
	c.Write(0x6000, 0x01) // latch pulse
	if got := c.ReadRAM(0xA000); got != 0x42 {
		t.Fatalf("latched seconds = 0x%02X, want 0x42", got)
	}
	// Writing through the window updates the live register.
	c.Write(0xA000, 0x17)
	if c.RTC.Seconds != 0x17 {
		t.Fatalf("RTC.Seconds = 0x%02X, want 0x17", c.RTC.Seconds)
	}
}

func TestMBC5NineBitBankAndBankZero(t *testing.T) {
	data := makeROM(t, 256*16384, 0x1B, 8, 3, true)
	c, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	// MBC5 allows bank zero in the switchable window (no fixup).
	if got := c.ReadROM(0x4000); got != 0 {
		t.Fatalf("power-on switchable bank = %d, want 0 on MBC5", got)
	}
	c.Write(0x2000, 0x23)
	c.Write(0x3000, 0x01) // bank bit 8: bank 0x123, masked to 256 banks
	if got := c.ReadROM(0x4000); got != 0x23 {
		t.Fatalf("switchable bank content = %d, want 0x23 (bank 0x123 mod 256)", got)
	}
}

func TestCartSerializeRoundTrip(t *testing.T) {
	data := makeROM(t, 32768, 0x10, 0, 3, false)
	c, _ := Load(data)
	c.Write(0x0000, 0x0A)
	c.Write(0x2000, 5)
	c.RTC.Minutes = 0x33
	buf := make([]byte, SerializeSize)
	if err := c.Serialize(buf); err != nil {
		t.Fatal(err)
	}
	c2, _ := Load(data)
	if err := c2.Deserialize(buf); err != nil {
		t.Fatal(err)
	}
	if c2.romBankLow != 5 || !c2.ramEnabled || c2.RTC.Minutes != 0x33 {
		t.Fatalf("round trip mismatch: bank=%d enabled=%v minutes=0x%02X",
			c2.romBankLow, c2.ramEnabled, c2.RTC.Minutes)
	}
}
