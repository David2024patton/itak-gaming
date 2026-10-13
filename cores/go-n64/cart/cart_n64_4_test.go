package cart

import "testing"

// The documented per-CIC initial checksum values
// (n64brew PIF-NUS table): seed*magic+1 truncated.
func TestCICChecksumInitValues(t *testing.T) {
	want := map[CIC]uint32{
		CIC6101: 0xF8CA4DDC,
		CIC6102: 0xF8CA4DDC,
		CIC6103: 0xA3886759,
		CIC6105: 0xDF26F436,
		CIC6106: 0x1FEA617A,
	}
	for cic, w := range want {
		got, ok := CICChecksumInit(cic)
		if !ok || got != w {
			t.Errorf("CIC %s init = %#08x, %v; want %#08x", cic, got, ok, w)
		}
	}
	if _, ok := CICChecksumInit(CICUnknown); ok {
		t.Error("unknown CIC reported an init value")
	}
}

// A checksum computed over a ROM must verify against
// itself: write the computed words into a copy of the
// header and recompute (the header is outside the
// summed region, so the values must be stable).
func TestIPL3ChecksumSelfConsistent(t *testing.T) {
	rom := make([]byte, 0x101000)
	for i := 0x1000; i < len(rom); i++ {
		rom[i] = byte(i*7 + 3)
	}
	for _, cic := range []CIC{CIC6101, CIC6102, CIC6103, CIC6105, CIC6106} {
		c1, c2, ok := IPL3Checksum(rom, cic)
		if !ok {
			t.Fatalf("cic %s: not ok", cic)
		}
		d1, d2, _ := IPL3Checksum(rom, cic)
		if c1 != d1 || c2 != d2 {
			t.Errorf("cic %s: checksum not deterministic", cic)
		}
		// Flip one summed byte: the words must change.
		rom[0x5000] ^= 0xFF
		e1, e2, _ := IPL3Checksum(rom, cic)
		rom[0x5000] ^= 0xFF
		if e1 == c1 && e2 == c2 {
			t.Errorf("cic %s: checksum insensitive to data", cic)
		}
	}
	// Distinct CIC families give distinct words on the
	// same data (different seeds and magics).
	a1, _, _ := IPL3Checksum(rom, CIC6102)
	b1, _, _ := IPL3Checksum(rom, CIC6103)
	if a1 == b1 {
		t.Error("6102 and 6103 checksums collide on test data")
	}
	if _, _, ok := IPL3Checksum(rom, CICUnknown); ok {
		t.Error("unknown CIC produced a checksum")
	}
}

func TestSaveStoreEEPROM(t *testing.T) {
	s := NewSaveStore(SaveEEPROM4K)
	id, ok := s.EEPROMInfoID()
	if !ok || id != 0x0080 {
		t.Fatalf("4K info id = %#04x, %v", id, ok)
	}
	if s.EEPROM[0] != 0xFF {
		t.Error("fresh EEPROM not erased (0xFF)")
	}
	var blk [8]byte
	for i := range blk {
		blk[i] = byte(i + 1)
	}
	if !s.EEPROMWrite(5, blk) {
		t.Fatal("write failed")
	}
	got, ok := s.EEPROMRead(5)
	if !ok || got != blk {
		t.Fatalf("read back %v, %v", got, ok)
	}
	// 4 Kbit wrap: block 69 is block 5.
	got, _ = s.EEPROMRead(69)
	if got != blk {
		t.Error("4K block wrap failed")
	}
	s.Configure(SaveEEPROM16K)
	id, ok = s.EEPROMInfoID()
	if !ok || id != 0x00C0 {
		t.Fatalf("16K info id = %#04x, %v", id, ok)
	}
	if len(s.EEPROM) != 2048 {
		t.Fatalf("16K size = %d", len(s.EEPROM))
	}
	s.Configure(SaveSRAM)
	if _, ok := s.EEPROMInfoID(); ok {
		t.Error("SRAM store reports an EEPROM")
	}
	if len(s.Data()) != SRAMSize {
		t.Errorf("SRAM data size = %d", len(s.Data()))
	}
}

func TestFlashCommandProtocol(t *testing.T) {
	f := NewFlashRAM()
	if f.LoadByte(0) != 0xFF {
		t.Fatal("fresh flash not erased")
	}
	// Program page 3: latch page, enter program mode,
	// DMA the buffer in, execute.
	f.Command(0xA5000003)
	if f.Status != flashStatusProgram {
		t.Fatalf("status after 0xA5 = %#x", f.Status)
	}
	f.Command(0xB4000000)
	page := make([]byte, FlashPageSize)
	for i := range page {
		page[i] = byte(i ^ 0x5A)
	}
	f.DMAWrite(page, 3*FlashPageSize)
	f.Command(0xD2000000)
	f.Command(0xF0000000) // read mode
	for i := 0; i < FlashPageSize; i++ {
		if got := f.LoadByte(uint32(3*FlashPageSize + i)); got != page[i] {
			t.Fatalf("flash byte %d = %#02x, want %#02x", i, got, page[i])
		}
	}
	// Status mode readback: the read-mode status word.
	f.Command(0xE1000000)
	if got := f.Read(0, 8); got != flashStatusMode {
		t.Fatalf("status read = %#016x, want %#016x", got, uint64(flashStatusMode))
	}
	// Erase page 3: latch, erase mode, execute.
	f.Command(0x4B000003)
	f.Command(0x78000000)
	if f.Status != flashStatusErase {
		t.Fatalf("status after 0x78 = %#x", f.Status)
	}
	f.Command(0xD2000000)
	f.Command(0xF0000000)
	if got := f.LoadByte(3 * FlashPageSize); got != 0xFF {
		t.Fatalf("erased byte = %#02x", got)
	}
	// A non-command word is not a command.
	if f.Command(0x12000000) {
		t.Error("0x12...... accepted as a command")
	}
}

func TestSaveStoreStateRoundTrip(t *testing.T) {
	s := NewSaveStore(SaveFlash)
	s.Flash.Command(0xA5000001)
	s.Flash.Command(0xB4000000)
	s.Flash.DMAWrite([]byte{1, 2, 3, 4}, FlashPageSize)
	s.Flash.Command(0xD2000000)
	buf := make([]byte, SaveStateSize)
	s.Serialize(buf)
	s2 := NewSaveStore(SaveNone)
	s2.Deserialize(buf)
	if s2.Type != SaveFlash {
		t.Fatalf("restored type = %v", s2.Type)
	}
	if s2.Flash.Status != s.Flash.Status {
		t.Errorf("restored status = %#x, want %#x", s2.Flash.Status, s.Flash.Status)
	}
	s2.Flash.Command(0xF0000000)
	if got := s2.Flash.LoadByte(FlashPageSize); got != 1 {
		t.Errorf("restored flash byte = %d", got)
	}
}
