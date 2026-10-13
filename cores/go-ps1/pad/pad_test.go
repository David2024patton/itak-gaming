package pad

import (
	"bytes"
	"testing"
)

// selectPort enables transfers on port 1, with optional extra JOY_CTRL
// bits (for example interrupt enables).
func selectPort(c *Controller, extra uint16) {
	if ok := c.WriteReg(0x1F80104A, 2, uint32(0x0003|extra)); !ok {
		panic("JOY_CTRL write not claimed")
	}
}

func deselect(c *Controller) {
	c.WriteReg(0x1F80104A, 2, 0x0000)
}

// xfer clocks one byte through JOY_DATA and returns the response.
func xfer(t *testing.T, c *Controller, b byte) byte {
	t.Helper()
	if ok := c.WriteReg(0x1F801040, 1, uint32(b)); !ok {
		t.Fatalf("JOY_DATA write of %02X not claimed", b)
	}
	v, ok := c.ReadReg(0x1F801040, 1)
	if !ok {
		t.Fatalf("JOY_DATA read not claimed")
	}
	return byte(v)
}

// run clocks a full transfer and returns all response bytes,
// including the dummy response to the address byte.
func run(t *testing.T, c *Controller, send ...byte) []byte {
	t.Helper()
	selectPort(c, 0)
	out := make([]byte, len(send))
	for i, b := range send {
		out[i] = xfer(t, c, b)
	}
	deselect(c)
	return out
}

func statReg(t *testing.T, c *Controller) uint32 {
	t.Helper()
	v, ok := c.ReadReg(0x1F801044, 4)
	if !ok {
		t.Fatal("JOY_STAT read not claimed")
	}
	return v
}

func enterConfig(t *testing.T, c *Controller) {
	t.Helper()
	got := run(t, c, 0x01, 0x43, 0x00, 0x01, 0x00)
	if got[1] != 0x41 && got[1] != 0x73 {
		t.Fatalf("enter config response ID = %02X, want normal ID", got[1])
	}
}

func exitConfig(t *testing.T, c *Controller) {
	t.Helper()
	got := run(t, c, 0x01, 0x43, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	want := []byte{0xFF, 0xF3, 0x5A, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("exit config = % X, want % X", got, want)
	}
}

func TestPollDigital(t *testing.T) {
	c := New()
	// Start (bit3) + Cross (bit14) + Up (bit4).
	c.SetButtons(1<<3 | 1<<14 | 1<<4)
	got := run(t, c, 0x01, 0x42, 0x00, 0x00, 0x00)
	// Buttons are active low: low byte covers bits 0..7 (Start and Up
	// pressed), high byte bits 8..15 (Cross pressed).
	want := []byte{0xFF, 0x41, 0x5A, 0xE7, 0xBF}
	if !bytes.Equal(got, want) {
		t.Fatalf("poll = % X, want % X", got, want)
	}
}

func TestAnalogMode(t *testing.T) {
	c := New()
	c.SetAnalog(0x11, 0x22, 0x33, 0x44)
	enterConfig(t, c)
	// Set analog mode (LED on) and lock it.
	got := run(t, c, 0x01, 0x44, 0x00, 0x01, 0x03, 0x00, 0x00, 0x00, 0x00)
	want := []byte{0xFF, 0xF3, 0x5A, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("set LED = % X, want % X", got, want)
	}
	// Command 45h reports Typ 01, Num 02, Led 01 (analog), Act 02,
	// Com 01.
	got = run(t, c, 0x01, 0x45, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	want = []byte{0xFF, 0xF3, 0x5A, 0x01, 0x02, 0x01, 0x02, 0x01, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("get LED = % X, want % X", got, want)
	}
	exitConfig(t, c)
	// Poll now returns ID 73h and the four stick bytes in order.
	c.SetButtons(0)
	got = run(t, c, 0x01, 0x42, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	want = []byte{0xFF, 0x73, 0x5A, 0xFF, 0xFF, 0x11, 0x22, 0x33, 0x44}
	if !bytes.Equal(got, want) {
		t.Fatalf("analog poll = % X, want % X", got, want)
	}
}

func TestConfigInfoCommands(t *testing.T) {
	c := New()
	enterConfig(t, c)
	// 46h with ii=0 (small motor) and ii=1 (big motor).
	got := run(t, c, 0x01, 0x46, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	want := []byte{0xFF, 0xF3, 0x5A, 0x00, 0x00, 0x01, 0x02, 0x00, 0x0A}
	if !bytes.Equal(got, want) {
		t.Fatalf("46h ii=0 = % X, want % X", got, want)
	}
	got = run(t, c, 0x01, 0x46, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00)
	want = []byte{0xFF, 0xF3, 0x5A, 0x00, 0x00, 0x01, 0x01, 0x01, 0x14}
	if !bytes.Equal(got, want) {
		t.Fatalf("46h ii=1 = % X, want % X", got, want)
	}
	// 47h with ii=0 returns the combiner list constants.
	got = run(t, c, 0x01, 0x47, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	want = []byte{0xFF, 0xF3, 0x5A, 0x00, 0x00, 0x02, 0x00, 0x01, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("47h = % X, want % X", got, want)
	}
	// 4Ch with ii=0 and ii=1.
	got = run(t, c, 0x01, 0x4C, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	want = []byte{0xFF, 0xF3, 0x5A, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("4Ch ii=0 = % X, want % X", got, want)
	}
	got = run(t, c, 0x01, 0x4C, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00)
	want = []byte{0xFF, 0xF3, 0x5A, 0x00, 0x00, 0x00, 0x07, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("4Ch ii=1 = % X, want % X", got, want)
	}
	exitConfig(t, c)
}

func TestVibrationMapping(t *testing.T) {
	c := New()
	enterConfig(t, c)
	// Map small motor to poll byte aa, big motor to bb. The response
	// echoes the OLD mapping (all FFh = unmapped).
	got := run(t, c, 0x01, 0x4D, 0x00, 0x00, 0x01, 0xFF, 0xFF, 0xFF, 0xFF)
	want := []byte{0xFF, 0xF3, 0x5A, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	if !bytes.Equal(got, want) {
		t.Fatalf("4Dh = % X, want % X", got, want)
	}
	exitConfig(t, c)
	// Poll with motor bytes at the mapped positions: small on (bit0),
	// big at A5h.
	run(t, c, 0x01, 0x42, 0x00, 0x01, 0xA5)
	small, big := c.Motors()
	if !small || big != 0xA5 {
		t.Fatalf("Motors = (%v, %02X), want (true, A5)", small, big)
	}
	// Poll again with both off.
	run(t, c, 0x01, 0x42, 0x00, 0x00, 0x00)
	small, big = c.Motors()
	if small || big != 0 {
		t.Fatalf("Motors = (%v, %02X), want (false, 00)", small, big)
	}
}

func xorBytes(bs ...byte) byte {
	var x byte
	for _, b := range bs {
		x ^= b
	}
	return x
}

func cardWrite(t *testing.T, c *Controller, sector uint16, data []byte, corrupt bool) []byte {
	t.Helper()
	chk := xorBytes(byte(sector>>8), byte(sector))
	for _, d := range data {
		chk ^= d
	}
	if corrupt {
		chk ^= 0xFF
	}
	send := []byte{0x81, 0x57, 0x00, 0x00, byte(sector >> 8), byte(sector)}
	send = append(send, data...)
	send = append(send, chk, 0x00, 0x00, 0x00)
	return run(t, c, send...)
}

func cardRead(t *testing.T, c *Controller, sector uint16) []byte {
	t.Helper()
	send := []byte{0x81, 0x52, 0x00, 0x00, byte(sector >> 8), byte(sector)}
	send = append(send, make([]byte, 134)...)
	return run(t, c, send...)
}

func TestCardWriteRead(t *testing.T) {
	c := New()
	data := make([]byte, 128)
	for i := range data {
		data[i] = byte(i*3 + 7)
	}
	got := cardWrite(t, c, 0, data, false)
	// FLAG starts at 08h; IDs 5Ah 5Dh; end byte 47h (good).
	if got[1] != 0x08 || got[2] != 0x5A || got[3] != 0x5D {
		t.Fatalf("write head = % X, want FLAG 08, 5A, 5D", got[:4])
	}
	if got[len(got)-1] != 0x47 {
		t.Fatalf("write end = %02X, want 47", got[len(got)-1])
	}
	if !bytes.Equal(c.CardImage()[0:128], data) {
		t.Fatal("card image does not match written sector")
	}
	// FLAG bit3 is cleared by writing, so the read sees FLAG 00h.
	got = cardRead(t, c, 0)
	if got[1] != 0x00 {
		t.Fatalf("read FLAG = %02X, want 00 after a write", got[1])
	}
	if got[2] != 0x5A || got[3] != 0x5D || got[6] != 0x5C || got[7] != 0x5D {
		t.Fatalf("read head = % X", got[:8])
	}
	if got[8] != 0x00 || got[9] != 0x00 {
		t.Fatalf("confirmed address = %02X %02X, want 00 00", got[8], got[9])
	}
	if !bytes.Equal(got[10:138], data) {
		t.Fatal("read data does not match written data")
	}
	wantChk := xorBytes(0x00, 0x00)
	for _, d := range data {
		wantChk ^= d
	}
	if got[138] != wantChk {
		t.Fatalf("read checksum = %02X, want %02X", got[138], wantChk)
	}
	if got[139] != 0x47 {
		t.Fatalf("read end = %02X, want 47", got[139])
	}
	// A non-zero sector round trips too, through CardImage directly.
	c.CardImage()[5*128] = 0xAB
	got = cardRead(t, c, 5)
	if got[10] != 0xAB || got[8] != 0x00 || got[9] != 0x05 {
		t.Fatalf("sector 5 read: data %02X addr %02X %02X", got[10], got[8], got[9])
	}
}

func TestCardBadChecksum(t *testing.T) {
	c := New()
	good := bytes.Repeat([]byte{0x11}, 128)
	cardWrite(t, c, 2, good, false)
	bad := bytes.Repeat([]byte{0x99}, 128)
	got := cardWrite(t, c, 2, bad, true)
	if got[len(got)-1] != 0x4E {
		t.Fatalf("bad checksum end = %02X, want 4E", got[len(got)-1])
	}
	// The corrupted write must not be stored.
	if !bytes.Equal(c.CardImage()[2*128:3*128], good) {
		t.Fatal("bad checksum write overwrote the sector")
	}
	// The next command reports the write error in FLAG bit2.
	got = run(t, c, 0x81, 0x53, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	if got[1]&0x04 == 0 {
		t.Fatalf("FLAG after bad write = %02X, want bit2 set", got[1])
	}
}

func TestCardGetID(t *testing.T) {
	c := New()
	// Both the documented 53h "S" and the 59h alias answer the same
	// sequence. Get ID is not a write, so FLAG stays 08h for both.
	for _, cmd := range []byte{0x53, 0x59} {
		got := run(t, c, 0x81, cmd, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
		want := []byte{0xFF, 0x08, 0x5A, 0x5D, 0x5C, 0x5D, 0x04, 0x00, 0x00, 0x80}
		if !bytes.Equal(got, want) {
			t.Fatalf("card ID cmd %02X = % X, want % X", cmd, got, want)
		}
	}
}

func TestCardAbsent(t *testing.T) {
	c := New()
	c.SetCardPresent(false)
	selectPort(c, 0x1000) // ACK IRQ enabled: absent card must not IRQ
	irq := 0
	c.OnIRQ = func() { irq++ }
	for _, b := range []byte{0x81, 0x52, 0x00, 0x00} {
		if got := xfer(t, c, b); got != 0xFF {
			t.Fatalf("absent card response = %02X, want FF", got)
		}
	}
	deselect(c)
	if irq != 0 {
		t.Fatalf("absent card raised %d IRQs, want 0", irq)
	}
	if s := statReg(t, c); s&(1<<7) != 0 {
		t.Fatalf("JOY_STAT /ACK low for absent card: %08X", s)
	}
}

func TestStatAndIRQ(t *testing.T) {
	c := New()
	irq := 0
	c.OnIRQ = func() { irq++ }
	selectPort(c, 0x1000) // TXEN + select + ACK interrupt enable
	if ok := c.WriteReg(0x1F801040, 1, 0x01); !ok {
		t.Fatal("JOY_DATA write not claimed")
	}
	if irq != 1 {
		t.Fatalf("OnIRQ fired %d times, want 1", irq)
	}
	s := statReg(t, c)
	// TX ready bits set, RX not empty, /ACK low, IRQ request set.
	if s&(1<<0) == 0 || s&(1<<2) == 0 {
		t.Fatalf("JOY_STAT TX ready bits clear: %08X", s)
	}
	if s&(1<<1) == 0 {
		t.Fatalf("JOY_STAT RX-not-empty clear: %08X", s)
	}
	if s&(1<<7) == 0 {
		t.Fatalf("JOY_STAT /ACK bit clear: %08X", s)
	}
	if s&(1<<9) == 0 {
		t.Fatalf("JOY_STAT IRQ bit clear: %08X", s)
	}
	// Reading JOY_DATA drains the RX byte.
	v, _ := c.ReadReg(0x1F801040, 1)
	if byte(v) != 0xFF {
		t.Fatalf("address response = %02X, want FF (Hi-Z)", byte(v))
	}
	if s = statReg(t, c); s&(1<<1) != 0 {
		t.Fatalf("JOY_STAT still RX-not-empty after drain: %08X", s)
	}
	// Acknowledging through JOY_CTRL bit4 clears JOY_STAT bit9.
	c.WriteReg(0x1F80104A, 2, 0x1003|(1<<4))
	if s = statReg(t, c); s&(1<<9) != 0 {
		t.Fatalf("JOY_STAT IRQ bit still set after ack: %08X", s)
	}
	deselect(c)
	// With transfers disabled, TX bytes are ignored: no IRQ, no FIFO.
	irq = 0
	c.WriteReg(0x1F801040, 1, 0x01)
	if irq != 0 {
		t.Fatal("IRQ fired while port deselected")
	}
	if s = statReg(t, c); s&(1<<1) != 0 {
		t.Fatal("RX FIFO filled while port deselected")
	}
}

func TestUnknownCommandInConfig(t *testing.T) {
	c := New()
	enterConfig(t, c)
	got := run(t, c, 0x01, 0x99, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	want := []byte{0xFF, 0xF3, 0x5A, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("unknown config cmd = % X, want % X", got, want)
	}
	// The state machine is still alive: exit and poll work.
	exitConfig(t, c)
	c.SetButtons(1 << 3)
	got = run(t, c, 0x01, 0x42, 0x00, 0x00, 0x00)
	want = []byte{0xFF, 0x41, 0x5A, 0xF7, 0xFF}
	if !bytes.Equal(got, want) {
		t.Fatalf("poll after unknown cmd = % X, want % X", got, want)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	c := New()
	data := make([]byte, 128)
	for i := range data {
		data[i] = byte(0xA0 + i)
	}
	cardWrite(t, c, 3, data, false)
	enterConfig(t, c)
	if len(c.Serialize()) != StateSize {
		t.Fatalf("Serialize size = %d, want %d", len(c.Serialize()), StateSize)
	}
	d := New()
	if err := d.Deserialize(c.Serialize()); err != nil {
		t.Fatal(err)
	}
	// Still in config mode on the restored controller: 45h answers
	// with the config ID, and exit works.
	got := run(t, d, 0x01, 0x45, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	if got[1] != 0xF3 {
		t.Fatalf("restored config ID = %02X, want F3", got[1])
	}
	exitConfig(t, d)
	// The written sector survived.
	got = cardRead(t, d, 3)
	if !bytes.Equal(got[10:138], data) {
		t.Fatal("restored card sector mismatch")
	}
	if err := d.Deserialize([]byte{1, 2, 3}); err == nil {
		t.Fatal("short state accepted")
	}
	bad := c.Serialize()
	bad[0] ^= 0xFF
	if err := d.Deserialize(bad); err == nil {
		t.Fatal("bad magic accepted")
	}
}

func TestRegisterClaiming(t *testing.T) {
	c := New()
	// Wrong sizes are not claimed, so the bus can fall back.
	if _, ok := c.ReadReg(0x1F801044, 2); ok {
		t.Fatal("JOY_STAT halfword read claimed")
	}
	if _, ok := c.ReadReg(0x1F801048, 4); ok {
		t.Fatal("JOY_MODE word read claimed")
	}
	if ok := c.WriteReg(0x1F801040, 2, 0x42); ok {
		t.Fatal("JOY_DATA halfword write claimed")
	}
	if _, ok := c.ReadReg(0x1F801050, 1); ok {
		t.Fatal("SIO address claimed by JOY block")
	}
	// Word write to JOY_DATA sends the low byte.
	selectPort(c, 0)
	if ok := c.WriteReg(0x1F801040, 4, 0x12345601); !ok {
		t.Fatal("JOY_DATA word write not claimed")
	}
	v, _ := c.ReadReg(0x1F801040, 1)
	if byte(v) != 0xFF {
		t.Fatalf("word write response = %02X, want FF", byte(v))
	}
	deselect(c)
	// MODE and BAUD are plain storage.
	c.WriteReg(0x1F801048, 2, 0x000D)
	c.WriteReg(0x1F80104E, 2, 0x0088)
	if v, _ := c.ReadReg(0x1F801048, 2); v != 0x000D {
		t.Fatalf("JOY_MODE = %04X, want 000D", v)
	}
	if v, _ := c.ReadReg(0x1F80104E, 2); v != 0x0088 {
		t.Fatalf("JOY_BAUD = %04X, want 0088", v)
	}
	// Fresh card image is the erased value FFh throughout.
	img := New().CardImage()
	if len(img) != 128*1024 {
		t.Fatalf("card image size = %d", len(img))
	}
	for i, b := range img {
		if b != 0xFF {
			t.Fatalf("card image[%d] = %02X, want FF (erased)", i, b)
		}
	}
}
