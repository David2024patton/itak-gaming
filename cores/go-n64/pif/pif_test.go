package pif

import "testing"

// Checksums pinned against hand-computed values.

// CRC-8 (poly 0x85, init 0) computed by hand:
//   - 32 zero bytes: 0x00
//   - the single byte 0x01: 0x85 (after 8 shifts the
//     lone bit reaches the top and folds once)
//   - the single byte 0xFF: 0x8D
//   - "123456789" ASCII: 0xFE? (verified against the
//     CRC-8/SMBUS variant table: poly 0x85 reflected
//     differs; the MSB-first value for this string
//     under poly 0x85 init 0 is computed in the test
//     comment from the bit algorithm: it is 0x3D.)
func TestDataCRCPinned(t *testing.T) {
	if got := DataCRC(make([]byte, 32)); got != 0x00 {
		t.Errorf("CRC(zeros) = %#02x, want 0x00", got)
	}
	if got := DataCRC([]byte{0x01}); got != 0x85 {
		t.Errorf("CRC(0x01) = %#02x, want 0x85", got)
	}
	if got := DataCRC([]byte{0xFF}); got != 0x8D {
		t.Errorf("CRC(0xFF) = %#02x, want 0x8D", got)
	}
}

// Address checksum: the documented example is address
// 0x0020 encoding checksum 10101b = 0x15 (only bit 5
// set, whose table value is 0x15). Address 0 has
// checksum 0. Address 0x8000 (only bit 15) is 0x01.
func TestAddressChecksumPinned(t *testing.T) {
	if got := AddressChecksum(0x0020); got != 0x15 {
		t.Errorf("checksum(0x0020) = %#02x, want 0x15", got)
	}
	if got := AddressChecksum(0x0000); got != 0x00 {
		t.Errorf("checksum(0) = %#02x, want 0", got)
	}
	if got := AddressChecksum(0x8000); got != 0x01 {
		t.Errorf("checksum(0x8000) = %#02x, want 0x01", got)
	}
	// Bits 6 and 5: 0x1F ^ 0x15 = 0x0A.
	if got := AddressChecksum(0x0060); got != 0x0A {
		t.Errorf("checksum(0x0060) = %#02x, want 0x0A", got)
	}
}

// fakeCart is a channel-4 EEPROM backed by a map.
type fakeCart struct {
	id    uint16
	there bool
	store [2048]byte
}

func (f *fakeCart) EEPROMInfoID() (uint16, bool) { return f.id, f.there }
func (f *fakeCart) EEPROMRead(block byte) ([8]byte, bool) {
	var out [8]byte
	if !f.there {
		return out, false
	}
	copy(out[:], f.store[int(block)*8:])
	return out, true
}
func (f *fakeCart) EEPROMWrite(block byte, data [8]byte) bool {
	if !f.there {
		return false
	}
	copy(f.store[int(block)*8:], data[:])
	return true
}

// loadFrame writes a frame into PIF RAM, sets the
// command bit, and parses (as an SI DMA write would).
func loadFrame(p *PIF, frame []byte) {
	copy(p.RAM[:], frame)
	p.RAM[0x3F] = 0x01
	p.AfterWrite()
}

func TestControllerInfoAndState(t *testing.T) {
	p := New()
	p.SetController(0, 0x8010, -5, 42) // A + Start, stick
	// Frame: ch0 state read (tx1 rx4 cmd 0x01), ch1-3
	// skipped, ch4 info (tx1 rx3 cmd 0x00), FE.
	frame := []byte{
		0x01, 0x04, 0x01, 0, 0, 0, 0,
		0x00, 0x00, 0x00,
		0x01, 0x03, 0x00, 0, 0, 0,
		0xFE,
	}
	loadFrame(p, frame)
	p.Execute()
	if p.RAM[3] != 0x80 || p.RAM[4] != 0x10 {
		t.Errorf("buttons = %02x %02x, want 80 10", p.RAM[3], p.RAM[4])
	}
	if int8(p.RAM[5]) != -5 || int8(p.RAM[6]) != 42 {
		t.Errorf("stick = %d,%d", int8(p.RAM[5]), int8(p.RAM[6]))
	}
	// Ch4 info with no cart device: zero signature.
	if p.RAM[13] != 0 || p.RAM[14] != 0 || p.RAM[15] != 0 {
		t.Errorf("ch4 info = %02x %02x %02x", p.RAM[13], p.RAM[14], p.RAM[15])
	}
	// No error flags anywhere.
	if p.RAM[1]&0xC0 != 0 || p.RAM[11]&0xC0 != 0 {
		t.Errorf("rx flags set: %02x %02x", p.RAM[1], p.RAM[11])
	}
}

func TestControllerInfoPakLifecycle(t *testing.T) {
	p := New()
	info := func() byte {
		frame := []byte{0x01, 0x03, 0xFF, 0, 0, 0, 0xFE}
		loadFrame(p, frame)
		p.Execute()
		return p.RAM[5]
	}
	if s := info(); s != 0x02 {
		t.Fatalf("no pak status = %#02x, want 0x02", s)
	}
	p.InsertPak(0, PakMemory)
	if s := info(); s != 0x03 {
		t.Fatalf("fresh pak status = %#02x, want 0x03", s)
	}
	if s := info(); s != 0x01 {
		t.Fatalf("settled pak status = %#02x, want 0x01", s)
	}
}

func TestPakRoundTripAndAbsentBehavior(t *testing.T) {
	p := New()
	addr := uint16(0x0020)
	addr |= uint16(AddressChecksum(addr))
	pakWrite := func(data []byte) byte {
		frame := []byte{0x23, 0x01, 0x03, byte(addr >> 8), byte(addr)}
		frame = append(frame, data...)
		frame = append(frame, 0, 0xFE) // rx space + end
		loadFrame(p, frame)
		p.Execute()
		return p.RAM[37]
	}
	pakRead := func() []byte {
		frame := []byte{0x03, 0x21, 0x02, byte(addr >> 8), byte(addr)}
		frame = append(frame, make([]byte, 33)...)
		frame = append(frame, 0xFE)
		loadFrame(p, frame)
		p.Execute()
		return p.RAM[5:38]
	}
	data := make([]byte, 32)
	for i := range data {
		data[i] = byte(i*3 + 1)
	}
	// Absent pak: write answers the complemented CRC,
	// read returns zeros with CRC 0xFF.
	if crc := pakWrite(data); crc != ^DataCRC(data) {
		t.Errorf("absent write crc = %#02x, want %#02x", crc, ^DataCRC(data))
	}
	got := pakRead()
	for i := 0; i < 32; i++ {
		if got[i] != 0 {
			t.Fatalf("absent read byte %d = %#02x", i, got[i])
		}
	}
	if got[32] != 0xFF {
		t.Errorf("absent read crc = %#02x, want 0xFF", got[32])
	}
	// Insert a pak: while fresh it still behaves
	// absent; an Info settles it.
	p.InsertPak(0, PakMemory)
	if crc := pakWrite(data); crc != ^DataCRC(data) {
		t.Errorf("fresh write crc = %#02x, want complemented", crc)
	}
	frame := []byte{0x01, 0x03, 0x00, 0, 0, 0, 0xFE}
	loadFrame(p, frame)
	p.Execute()
	if crc := pakWrite(data); crc != DataCRC(data) {
		t.Errorf("write crc = %#02x, want %#02x", crc, DataCRC(data))
	}
	got = pakRead()
	for i := 0; i < 32; i++ {
		if got[i] != data[i] {
			t.Fatalf("read byte %d = %#02x, want %#02x", i, got[i], data[i])
		}
	}
	if got[32] != DataCRC(data) {
		t.Errorf("read crc = %#02x, want %#02x", got[32], DataCRC(data))
	}
}

func TestPakBadAddressChecksum(t *testing.T) {
	p := New()
	p.InsertPak(0, PakMemory)
	frame := []byte{0x01, 0x03, 0x00, 0, 0, 0, 0xFE}
	loadFrame(p, frame)
	p.Execute() // settle the pak
	// Address 0x0020 with a deliberately wrong checksum
	// (0x00 instead of 0x15): no reply (timeout flag),
	// and the next Info reports the checksum error bit.
	bad := []byte{0x03, 0x21, 0x02, 0x00, 0x20}
	bad = append(bad, make([]byte, 33)...)
	bad = append(bad, 0xFE)
	loadFrame(p, bad)
	p.Execute()
	if p.RAM[1]&rxTimeout == 0 {
		t.Errorf("bad checksum did not time out: rx byte %#02x", p.RAM[1])
	}
	loadFrame(p, frame)
	p.Execute()
	if p.RAM[5] != 0x01|0x04 {
		t.Errorf("info after bad checksum = %#02x, want 0x05", p.RAM[5])
	}
}

func TestEEPROMChannel(t *testing.T) {
	p := New()
	fc := &fakeCart{id: 0x0080, there: true}
	p.AttachCart(fc)
	// ch4 write block 7 (skip ch0..3 first).
	frame := []byte{0x00, 0x00, 0x00, 0x00,
		0x0A, 0x01, 0x05, 0x07, 1, 2, 3, 4, 5, 6, 7, 8, 0xEE,
		0xFE}
	loadFrame(p, frame)
	p.Execute()
	if p.RAM[16] != 0x00 {
		t.Errorf("eeprom write reply = %#02x", p.RAM[16])
	}
	// Read it back.
	frame = []byte{0x00, 0x00, 0x00, 0x00,
		0x02, 0x08, 0x04, 0x07, 0, 0, 0, 0, 0, 0, 0, 0,
		0xFE}
	loadFrame(p, frame)
	p.Execute()
	for i := 0; i < 8; i++ {
		if p.RAM[8+i] != byte(i+1) {
			t.Fatalf("eeprom byte %d = %#02x", i, p.RAM[8+i])
		}
	}
	// Info on ch4 returns the EEPROM signature.
	frame = []byte{0x00, 0x00, 0x00, 0x00, 0x01, 0x03, 0xFF, 0, 0, 0, 0xFE}
	loadFrame(p, frame)
	p.Execute()
	if p.RAM[7] != 0x00 || p.RAM[8] != 0x80 || p.RAM[9] != 0x00 {
		t.Errorf("eeprom info = %02x %02x %02x", p.RAM[7], p.RAM[8], p.RAM[9])
	}
	// RTC info with no RTC: three zero bytes, no error.
	frame = []byte{0x00, 0x00, 0x00, 0x00, 0x01, 0x03, 0x06, 0, 0, 0, 0xFE}
	loadFrame(p, frame)
	p.Execute()
	if p.RAM[5]&0xC0 != 0 {
		t.Errorf("rtc info flagged an error: %#02x", p.RAM[5])
	}
	if p.RAM[7] != 0 || p.RAM[8] != 0 || p.RAM[9] != 0 {
		t.Errorf("rtc info bytes nonzero")
	}
}

func TestNoDeviceChannelAndSkipBit(t *testing.T) {
	p := New()
	// All five channels ask an EEPROM read; only ch4
	// exists in this PIF's world when a cart is
	// attached without EEPROM... instead: use an
	// unknown command 0x42 on ch0 (device stays
	// silent) and check the timeout flag, then a
	// skipped handshake (TX bit 7) leaves RAM alone.
	frame := []byte{0x01, 0x02, 0x42, 0xAA, 0xBB, 0xFE}
	loadFrame(p, frame)
	p.Execute()
	if p.RAM[1]&rxTimeout == 0 {
		t.Errorf("unknown command: rx = %#02x, want timeout", p.RAM[1])
	}
	if p.RAM[3] != 0xAA || p.RAM[4] != 0xBB {
		t.Error("timeout wrote into the receive space")
	}
	frame = []byte{0x81, 0x04, 0x01, 0xCC, 0xCC, 0xCC, 0xCC, 0xFE}
	loadFrame(p, frame)
	p.Execute()
	if p.RAM[3] != 0xCC {
		t.Error("skipped handshake executed")
	}
}

func TestCommandBits(t *testing.T) {
	p := New()
	p.RAM[0x3F] = 0x20 // acquire checksum
	copy(p.RAM[0x32:], []byte{1, 2, 3, 4, 5, 6})
	p.AfterWrite()
	if p.RAM[0x32] != 0 {
		t.Error("checksum area not cleared by acquire")
	}
	if p.RAM[0x3F]&0x80 == 0 {
		t.Error("done bit not set after acquire")
	}
	p.RAM[0x3F] = 0x08 // terminate boot
	p.AfterWrite()
	if !p.BootTerminated() {
		t.Error("terminate bit did not register")
	}
	if p.RAM[0x3F]&0x08 != 0 {
		t.Error("terminate bit not cleared")
	}
}

func TestPIFStateRoundTrip(t *testing.T) {
	p := New()
	p.InsertPak(1, PakMemory)
	copy(p.PakData(1)[100:], []byte{9, 8, 7})
	buf := make([]byte, StateSize)
	p.Serialize(buf)
	p2 := New()
	p2.Deserialize(buf)
	if p2.PakKind(1) != PakMemory {
		t.Fatalf("pak kind = %v", p2.PakKind(1))
	}
	if !p2.channels[1].pakFresh {
		t.Error("fresh flag lost")
	}
	if p2.PakData(1)[100] != 9 || p2.PakData(1)[102] != 7 {
		t.Error("pak data lost")
	}
}
