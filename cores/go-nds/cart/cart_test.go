package cart

import (
	"encoding/binary"
	"testing"
)

// syntheticROM builds a minimal well-formed DS image: a 0x200
// byte header region, an ARM9 binary at 0x1000 and an ARM7
// binary at 0x2000, with a valid header CRC stamped.
func syntheticROM() []byte {
	rom := make([]byte, 0x4000)
	copy(rom[0x000:], "DS1 UNITTEST")
	copy(rom[0x00C:], "ADSE")
	copy(rom[0x010:], "01")
	rom[0x012] = 0 // unit code: DS
	rom[0x013] = 1 // encryption seed select
	rom[0x014] = 2 // capacity: 512 KB class
	le := binary.LittleEndian
	le.PutUint32(rom[0x020:], 0x1000)     // ARM9 offset
	le.PutUint32(rom[0x024:], 0x02000000) // ARM9 entry
	le.PutUint32(rom[0x028:], 0x02000000) // ARM9 load
	le.PutUint32(rom[0x02C:], 16)         // ARM9 size
	le.PutUint32(rom[0x030:], 0x2000)     // ARM7 offset
	le.PutUint32(rom[0x034:], 0x03800000) // ARM7 entry
	le.PutUint32(rom[0x038:], 0x03800000) // ARM7 load
	le.PutUint32(rom[0x03C:], 20)         // ARM7 size
	le.PutUint32(rom[0x068:], 0x3000)     // icon offset
	le.PutUint16(rom[0x06C:], 0xBEEF)     // secure area CRC field
	le.PutUint16(rom[0x15E:], crc16(rom[:0x15E]))
	return rom
}

func TestParseSynthetic(t *testing.T) {
	h, err := Parse(syntheticROM())
	if err != nil {
		t.Fatal(err)
	}
	if h.Title != "DS1 UNITTEST" || h.GameCode != "ADSE" || h.MakerCode != "01" {
		t.Errorf("identity: %q %q %q", h.Title, h.GameCode, h.MakerCode)
	}
	if h.UnitCode != 0 || h.SeedSelect != 1 || h.Capacity != 2 {
		t.Errorf("unit/seed/capacity: %d %d %d", h.UnitCode, h.SeedSelect, h.Capacity)
	}
	if h.ARM9Offset != 0x1000 || h.ARM9Entry != 0x02000000 || h.ARM9Load != 0x02000000 || h.ARM9Size != 16 {
		t.Errorf("ARM9 fields: %+v", h)
	}
	if h.ARM7Offset != 0x2000 || h.ARM7Entry != 0x03800000 || h.ARM7Load != 0x03800000 || h.ARM7Size != 20 {
		t.Errorf("ARM7 fields: %+v", h)
	}
	if h.IconOffset != 0x3000 || h.SecureCRC != 0xBEEF {
		t.Errorf("icon/secure: %08x %04x", h.IconOffset, h.SecureCRC)
	}
	if !h.HeaderCRCValid {
		t.Error("stamped header CRC should validate")
	}
	if h.CapacityBytes() != 512<<10 {
		t.Errorf("CapacityBytes: %d", h.CapacityBytes())
	}
}

func TestParseRejects(t *testing.T) {
	if _, err := Parse(make([]byte, 0x100)); err == nil {
		t.Error("short image accepted")
	}
	rom := syntheticROM()
	binary.LittleEndian.PutUint32(rom[0x02C:], 0xFFFFF) // ARM9 size out of range
	if _, err := Parse(rom); err == nil {
		t.Error("out-of-range ARM9 extent accepted")
	}
}

func TestParseBadCRCIsNotFatal(t *testing.T) {
	rom := syntheticROM()
	rom[0x40] ^= 0xFF // corrupt a reserved byte after stamping
	h, err := Parse(rom)
	if err != nil {
		t.Fatal(err)
	}
	if h.HeaderCRCValid {
		t.Error("corrupted header should report CRC invalid")
	}
}
