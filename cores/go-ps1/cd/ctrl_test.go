package cd

import (
	"bytes"
	"fmt"
	"testing"
)

// memDisc is a synthetic Reader: sector LBA n holds
// 2048 bytes all equal to byte(n+1).
type memDisc struct{ sectors int }

func (d memDisc) Tracks() []Track {
	return []Track{{Number: 1, Mode: TrackData, SectorSize: 2048, UserSize: 2048, Length: d.sectors}}
}
func (d memDisc) ReadSector(lba int, raw bool) ([]byte, error) {
	if lba < 0 || lba >= d.sectors {
		return nil, fmt.Errorf("lba %d out of range", lba)
	}
	out := make([]byte, 2048)
	for i := range out {
		out[i] = byte(lba + 1)
	}
	return out, nil
}
func (d memDisc) Close() error { return nil }

// fakeDisc is a synthetic 3-track disc: two Mode 2
// data tracks of 750 sectors each, then one audio
// track of 750 sectors. Total 2250 sectors.
// Audio sector frames are a known pattern: frame g
// (counted from the audio track start) is
// (int16(g), int16(-g)).
type fakeDisc struct{}

func (fakeDisc) Tracks() []Track {
	return []Track{
		{Number: 1, Mode: TrackData, SectorSize: 2352, UserOffset: 24, UserSize: 2048, StartLBA: 0, DataLBA: 0, Length: 750},
		{Number: 2, Mode: TrackData, SectorSize: 2352, UserOffset: 24, UserSize: 2048, StartLBA: 750, DataLBA: 750, Length: 750},
		{Number: 3, Mode: TrackAudio, SectorSize: 2352, UserOffset: 0, UserSize: 2352, StartLBA: 1500, DataLBA: 1500, Length: 750},
	}
}

func (fakeDisc) ReadSector(lba int, raw bool) ([]byte, error) {
	if lba < 0 || lba >= 2250 {
		return nil, fmt.Errorf("lba %d out of range", lba)
	}
	if lba >= 1500 {
		out := make([]byte, 2352)
		for i := 0; i < 588; i++ {
			g := int16((lba-1500)*588 + i)
			out[i*4] = byte(g)
			out[i*4+1] = byte(g >> 8)
			out[i*4+2] = byte(-g)
			out[i*4+3] = byte((-g) >> 8)
		}
		return out, nil
	}
	if raw {
		return makeMode2Sector(lba, 0, 0, 0x08, 0), nil
	}
	out := make([]byte, 2048)
	for i := range out {
		out[i] = byte(lba + 1)
	}
	return out, nil
}
func (fakeDisc) Close() error { return nil }

// makeMode2Sector builds a raw 2352-byte Mode 2
// sector: sync, BCD header, subheader (twice), then
// data bytes filled with byte(lba+1).
func makeMode2Sector(lba int, file, channel, submode, coding uint8) []byte {
	out := make([]byte, 2352)
	copy(out, syncPattern)
	m, s, f := LBAToMSF(lba)
	out[12], out[13], out[14], out[15] = toBCD(m), toBCD(s), toBCD(f), 2
	out[16], out[17], out[18], out[19] = file, channel, submode, coding
	copy(out[20:24], out[16:20])
	for i := 24; i < len(out); i++ {
		out[i] = byte(lba + 1)
	}
	return out
}

// xaDisc is a synthetic disc whose raw sectors are
// Mode 2 Form 2 XA audio sectors (file 1, channel
// 2, submode audio+realtime+form2, mono, 37.8 kHz).
// In 4-bit format, portion 0 block 0 carries the
// nibble pattern (j%7)+1 for sample j; every other
// nibble is 1. In 8-bit format, portion 0 block 0
// carries the byte pattern j+1; other bytes are 0.
// All header bytes are 0 (shift 0, filter 0), so a
// 4-bit sample decodes to nibble<<12 and an 8-bit
// sample to byte<<8.
type xaDisc struct{ bits8 bool }

func (d xaDisc) Tracks() []Track {
	return []Track{{Number: 1, Mode: TrackData, SectorSize: 2352, UserOffset: 24, UserSize: 2048, StartLBA: 0, DataLBA: 0, Length: 300}}
}
func (d xaDisc) ReadSector(lba int, raw bool) ([]byte, error) {
	if lba < 0 || lba >= 300 {
		return nil, fmt.Errorf("lba %d out of range", lba)
	}
	if !raw {
		out := make([]byte, 2048)
		for i := range out {
			out[i] = byte(lba + 1)
		}
		return out, nil
	}
	ci := uint8(0)
	if d.bits8 {
		ci = 0x10
	}
	out := makeMode2Sector(lba, 1, 2, 0x64, ci)
	for i := 24; i < 24+0x900; i++ {
		out[i] = 0
	}
	p := out[24 : 24+128] // portion 0
	if d.bits8 {
		for j := 0; j < 28; j++ {
			p[16+j*4] = byte(j + 1)
		}
	} else {
		for i := 16; i < 128; i++ {
			p[i] = 0x11
		}
		for j := 0; j < 28; j++ {
			p[16+j*4] = 0x10 | byte((j%7)+1)
		}
	}
	return out, nil
}
func (d xaDisc) Close() error { return nil }

// filterDisc has Mode 2 data sectors alternating
// subheader file/channel: even LBAs are file 0
// channel 0, odd LBAs are file 9 channel 9.
type filterDisc struct{}

func (filterDisc) Tracks() []Track {
	return []Track{{Number: 1, Mode: TrackData, SectorSize: 2352, UserOffset: 24, UserSize: 2048, StartLBA: 0, DataLBA: 0, Length: 100}}
}
func (filterDisc) ReadSector(lba int, raw bool) ([]byte, error) {
	if lba < 0 || lba >= 100 {
		return nil, fmt.Errorf("lba %d out of range", lba)
	}
	if raw {
		file, channel := uint8(0), uint8(0)
		if lba%2 == 1 {
			file, channel = 9, 9
		}
		return makeMode2Sector(lba, file, channel, 0x08, 0), nil
	}
	out := make([]byte, 2048)
	for i := range out {
		out[i] = byte(lba + 1)
	}
	return out, nil
}
func (filterDisc) Close() error { return nil }

func writeReg(c *Controller, off uint32, v uint8) { c.WriteReg(off, v) }

// sendCmd writes parameters and a command at index
// 0 and returns the drained response FIFO bytes.
func sendCmd(c *Controller, cmd uint8, params ...uint8) []byte {
	c.WriteReg(0, 0)
	for _, p := range params {
		c.WriteReg(2, p)
	}
	c.WriteReg(1, cmd)
	return drainResp(c)
}

func drainResp(c *Controller) []byte {
	out := append([]byte(nil), c.response...)
	for len(c.response) > 0 {
		c.ReadReg(1)
	}
	return out
}

// setloc is a test helper: Setloc to an absolute
// MSF given in BCD bytes, response drained.
func setloc(c *Controller, mm, ss, ff uint8) {
	sendCmd(c, 0x02, mm, ss, ff)
}

func TestGetstatAndIRQ(t *testing.T) {
	c := NewController()
	c.SetDisc(memDisc{sectors: 10})
	irqs := 0
	c.OnIRQ = func() { irqs++ }
	writeReg(c, 3, 0x1F) // enable all IRQ types (index 0)
	writeReg(c, 1, 0x01) // Getstat
	if got := c.ReadReg(0); got&0x20 == 0 {
		t.Errorf("status = 0x%02X, want RSLRRDY set", got)
	}
	if v := c.ReadReg(1); v != 0x02 {
		t.Errorf("Getstat response = 0x%02X, want 0x02 (motor on)", v)
	}
	if irqs != 1 {
		t.Errorf("IRQ callbacks = %d, want 1", irqs)
	}
	if c.irqFlag != irqAck {
		t.Errorf("irqFlag = %d, want %d (ack)", c.irqFlag, irqAck)
	}
	// Acknowledge via the flag register (index 1).
	writeReg(c, 0, 1)
	writeReg(c, 3, irqAck)
	if c.irqFlag != 0 {
		t.Errorf("irqFlag after ack = %d, want 0", c.irqFlag)
	}
}

func TestSetlocReadNAndDMAWords(t *testing.T) {
	c := NewController()
	c.SetDisc(memDisc{sectors: 300})
	// Setloc to MSF 00:02:00 = LBA 0.
	writeReg(c, 2, 0x00)
	writeReg(c, 2, 0x02)
	writeReg(c, 2, 0x00)
	writeReg(c, 1, 0x02) // Setloc command
	c.ReadReg(1)         // drain response
	writeReg(c, 1, 0x06) // ReadN
	// Timing model change from PS1-2: the first
	// sector now arrives only after the seek delay
	// plus one sector period, via Advance.
	c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
	if c.DataLen() != 2048 {
		t.Fatalf("data FIFO = %d bytes, want 2048", c.DataLen())
	}
	// Sector 0 is filled with 0x01.
	w, ok := c.ReadWord()
	if !ok || w != 0x01010101 {
		t.Fatalf("first DMA word = 0x%08X ok=%v, want 0x01010101", w, ok)
	}
	// Drain the remaining 511 words of sector 0. The
	// next sector is timing driven now: it arrives
	// one sector period after the first, via
	// Advance (PS1-2 queued it on drain).
	for i := 0; i < 511; i++ {
		if _, ok := c.ReadWord(); !ok {
			t.Fatalf("word %d of sector 0 unavailable", i+1)
		}
	}
	c.Advance(sectorPeriodCycles(0))
	if !c.Streaming() {
		t.Fatalf("stream stopped after first sector")
	}
	if c.DataLen() != 2048 {
		t.Fatalf("data FIFO = %d after next period, want 2048", c.DataLen())
	}
	w, ok = c.ReadWord()
	if !ok {
		t.Fatalf("no word available after next period")
	}
	if w != 0x02020202 {
		t.Errorf("second sector word = 0x%08X, want 0x02020202", w)
	}
	// Pause stops the stream: after draining what is
	// queued, nothing new arrives.
	writeReg(c, 1, 0x09)
	for c.DataLen() > 0 {
		before := c.DataLen()
		c.ReadReg(2)
		if c.DataLen() > before {
			t.Fatalf("FIFO refilled after Pause")
		}
	}
	if c.Streaming() {
		t.Errorf("still streaming after Pause")
	}
}

func TestUnknownCommandError(t *testing.T) {
	c := NewController()
	c.SetDisc(memDisc{sectors: 4})
	writeReg(c, 1, 0x7E)
	if c.irqFlag != irqError {
		t.Errorf("irqFlag = %d, want error type %d", c.irqFlag, irqError)
	}
	if v := c.ReadReg(1); v&0x01 == 0 {
		t.Errorf("status response = 0x%02X, want error bit set", v)
	}
}

func TestControllerSerializeRoundTrip(t *testing.T) {
	c := NewController()
	c.SetDisc(memDisc{sectors: 50})
	writeReg(c, 2, 0x00)
	writeReg(c, 2, 0x02)
	writeReg(c, 2, 0x25) // MSF 00:02:25 -> LBA 25
	writeReg(c, 1, 0x02)
	c.ReadReg(1)
	writeReg(c, 1, 0x1B) // ReadS
	// Timing model change from PS1-2: Advance past
	// the seek delay (25 sectors of distance) plus
	// one sector period before the sector lands.
	c.Advance(seekDelayCycles(25, true) + sectorPeriodCycles(0))
	if c.DataLen() != 2048 {
		t.Fatalf("data = %d, want 2048", c.DataLen())
	}
	// Read part of the sector, then save.
	for i := 0; i < 100; i++ {
		c.ReadReg(2)
	}
	s := c.Serialize()
	c2 := NewController()
	c2.SetDisc(memDisc{sectors: 50})
	if err := c2.Deserialize(s); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !bytes.Equal(c2.Serialize(), s) {
		t.Errorf("controller state diverged after round trip")
	}
	// Sector 25 is filled with 26 (0x1A).
	if v := c2.ReadReg(2); v != 0x1A {
		t.Errorf("restored FIFO byte = 0x%02X, want 0x1A", v)
	}
}

// TestCommandCoverage drives every implemented
// command on the synthetic 3-track disc and checks
// the first response bytes and the INT type.
func TestCommandCoverage(t *testing.T) {
	playSetup := func(c *Controller) {
		setloc(c, 0x00, 0x22, 0x00) // audio track start, LBA 1500
		sendCmd(c, 0x03)
		c.Advance(seekDelayCycles(1500, true))
		drainResp(c)
	}
	readSetup := func(c *Controller) {
		setloc(c, 0x00, 0x02, 0x00)
		sendCmd(c, 0x06)
		c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
		drainResp(c)
	}
	stopSetup := func(c *Controller) {
		sendCmd(c, 0x08)
		c.Advance(stopSecondSpinning + 1)
		drainResp(c)
	}
	cases := []struct {
		name   string
		setup  func(*Controller)
		cmd    uint8
		params []uint8
		want   []byte
	}{
		{"Getstat", nil, 0x01, nil, []byte{0x02}},
		{"Setloc", nil, 0x02, []uint8{0x00, 0x02, 0x00}, []byte{0x02}},
		{"Play", func(c *Controller) { setloc(c, 0x00, 0x22, 0x00) }, 0x03, nil, []byte{0x02}},
		{"Forward", playSetup, 0x04, nil, []byte{0x82}},
		{"Backward", playSetup, 0x05, nil, []byte{0x82}},
		{"ReadN", nil, 0x06, nil, []byte{0x02}},
		{"MotorOn", stopSetup, 0x07, nil, []byte{0x00}},
		{"Stop", nil, 0x08, nil, []byte{0x02}},
		{"Pause", nil, 0x09, nil, []byte{0x02}},
		{"Init", nil, 0x0A, nil, []byte{0x02}},
		{"Mute", nil, 0x0B, nil, []byte{0x02}},
		{"Demute", nil, 0x0C, nil, []byte{0x02}},
		{"Setfilter", nil, 0x0D, []uint8{1, 2}, []byte{0x02}},
		{"Setmode", nil, 0x0E, []uint8{0x80}, []byte{0x02}},
		{"Getparam", nil, 0x0F, nil, []byte{0x02, 0x00, 0x00, 0x00, 0x00}},
		{"GetlocL", readSetup, 0x10, nil, []byte{0x00, 0x02, 0x00, 0x02, 0, 0, 0x08, 0}},
		{"GetlocP", nil, 0x11, nil, []byte{0x01, 0x01, 0, 0, 0, 0, 0x02, 0}},
		{"SetSession", nil, 0x12, []uint8{1}, []byte{0x02}},
		{"GetTN", nil, 0x13, nil, []byte{0x02, 0x01, 0x03}},
		{"GetTD", nil, 0x14, []uint8{0x02}, []byte{0x02, 0x00, 0x12}},
		{"SeekL", func(c *Controller) { setloc(c, 0x00, 0x12, 0x00) }, 0x15, nil, []byte{0x02}},
		{"SeekP", func(c *Controller) { setloc(c, 0x00, 0x12, 0x00) }, 0x16, nil, []byte{0x02}},
		// Test 20h: the version response published in
		// psx-spx for LATE-PU-8 vC1 is 95h,07h,24h,C1h
		// (24 Jul 1995, version C1).
		{"Test20", nil, 0x19, []uint8{0x20}, []byte{0x95, 0x07, 0x24, 0xC1}},
		{"GetID", nil, 0x1A, nil, []byte{0x02}},
		{"ReadS", nil, 0x1B, nil, []byte{0x02}},
		{"Reset", nil, 0x1C, nil, []byte{0x02}},
		{"GetQ", nil, 0x1D, nil, []byte{0x02}},
		{"ReadTOC", nil, 0x1E, nil, []byte{0x02}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewController()
			c.SetDisc(fakeDisc{})
			if tc.setup != nil {
				tc.setup(c)
			}
			drainResp(c)
			c.irqFlag = 0
			got := sendCmd(c, tc.cmd, tc.params...)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("cmd %02X first response = % X, want % X", tc.cmd, got, tc.want)
			}
			if c.irqFlag != irqAck {
				t.Errorf("cmd %02X irqFlag = %d, want INT3 (%d)", tc.cmd, c.irqFlag, irqAck)
			}
		})
	}
}

func TestGetTNGetTDExact(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	if got := sendCmd(c, 0x13); !bytes.Equal(got, []byte{0x02, 0x01, 0x03}) {
		t.Errorf("GetTN = % X, want [02 01 03]", got)
	}
	// Track starts: t1 LBA0 -> 00:02, t2 LBA750 ->
	// 00:12, t3 LBA1500 -> 00:22, track 0 = end of
	// last track LBA2250 -> 00:32 (mm:ss, BCD).
	for param, want := range map[uint8][]byte{
		0x01: {0x02, 0x00, 0x02},
		0x02: {0x02, 0x00, 0x12},
		0x03: {0x02, 0x00, 0x22},
		0x00: {0x02, 0x00, 0x32},
	} {
		if got := sendCmd(c, 0x14, param); !bytes.Equal(got, want) {
			t.Errorf("GetTD(%02X) = % X, want % X", param, got, want)
		}
	}
	// Past the last track: documented error 10h.
	c.irqFlag = 0
	if got := sendCmd(c, 0x14, 0x04); !bytes.Equal(got, []byte{0x03, 0x10}) {
		t.Errorf("GetTD(04) = % X, want [03 10]", got)
	}
	if c.irqFlag != irqError {
		t.Errorf("GetTD(04) irqFlag = %d, want INT5", c.irqFlag)
	}
}

func TestGetIDSecondResponse(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	sendCmd(c, 0x1A)
	c.Advance(getIDSecondDelay - 1)
	if len(c.response) != 0 {
		t.Fatalf("GetID second response arrived early")
	}
	c.Advance(1)
	got := drainResp(c)
	want := []byte{0x02, 0x00, 0x20, 0x00, 'S', 'C', 'E', 'A'}
	if !bytes.Equal(got, want) {
		t.Errorf("GetID second response = % X, want % X", got, want)
	}
}

// crcSubQ recomputes the subchannel Q CRC in the
// test, bit by bit, from the documented
// CRC-16-CCITT polynomial (x^16+x^12+x^5+1, hex
// 1021h), initial value zero, result inverted,
// transmitted big-endian. It shares no code with
// the controller's nibble routine.
func crcSubQ(data []byte) uint16 {
	crc := uint16(0)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return ^crc
}

func TestGetQDuringPlay(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	sendCmd(c, 0x03, 0x03) // Play track 3
	c.Advance(seekDelayCycles(1500, true))
	drainResp(c)
	if got := sendCmd(c, 0x1D); !bytes.Equal(got, []byte{0x82}) {
		t.Fatalf("GetQ first response = % X, want [82] (playing)", got)
	}
	c.Advance(sectorPeriod1x - 1)
	if len(c.response) != 0 {
		t.Fatalf("GetQ second response arrived early")
	}
	c.Advance(1)
	got := drainResp(c)
	if len(got) != 13 {
		t.Fatalf("GetQ second response = % X, want 13 bytes", got)
	}
	// Position: audio track 3, index 1, relative
	// 00:00:00, absolute 00:22:00 (LBA 1500 + the
	// 150-frame lead-in), control/ADR 01h.
	wantData := []byte{0x01, 0x03, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x22, 0x00}
	if !bytes.Equal(got[:10], wantData) {
		t.Errorf("GetQ data = % X, want % X", got[:10], wantData)
	}
	crc := crcSubQ(got[:10])
	if got[10] != uint8(crc>>8) || got[11] != uint8(crc) {
		t.Errorf("GetQ CRC = %02X %02X, recomputed %04X", got[10], got[11], crc)
	}
	if got[12] != 0 {
		t.Errorf("GetQ peak byte = %02X, want 0", got[12])
	}
}

func TestSeekTiming(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	setloc(c, 0x00, 0x12, 0x00) // LBA 750
	sendCmd(c, 0x15)            // SeekL
	delay := seekDelayCycles(750, true)
	c.Advance(delay - 1)
	if len(c.response) != 0 {
		t.Fatalf("seek INT2 arrived before %d cycles", delay)
	}
	if !c.seeking {
		t.Errorf("seeking flag cleared early")
	}
	c.Advance(1)
	got := drainResp(c)
	if !bytes.Equal(got, []byte{0x02}) {
		t.Errorf("seek INT2 response = % X, want [02]", got)
	}
	if c.seeking || c.curLBA != 750 {
		t.Errorf("after seek: seeking=%v cur=%d, want false/750", c.seeking, c.curLBA)
	}
}

func TestSectorPacing(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x06) // ReadN
	first := seekDelayCycles(0, true) + sectorPeriodCycles(0)
	c.Advance(first - 1)
	if c.DataLen() != 0 {
		t.Fatalf("sector arrived before seek+period: %d bytes", c.DataLen())
	}
	c.Advance(1)
	if c.DataLen() != 2048 {
		t.Fatalf("after first period: %d bytes, want 2048", c.DataLen())
	}
	c.Advance(sectorPeriodCycles(0) - 1)
	if c.DataLen() != 2048 {
		t.Fatalf("second sector arrived early: %d bytes", c.DataLen())
	}
	c.Advance(1)
	if c.DataLen() != 4096 {
		t.Fatalf("after second period: %d bytes, want 4096", c.DataLen())
	}
}

func TestDoubleSpeedHalvesPeriod(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	sendCmd(c, 0x0E, 0x80) // Setmode double speed
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x06)
	half := sectorPeriodCycles(0x80)
	first := seekDelayCycles(0, true) + half
	c.Advance(first - 1)
	if c.DataLen() != 0 {
		t.Fatalf("double-speed sector arrived early")
	}
	c.Advance(1)
	if c.DataLen() != 2048 {
		t.Fatalf("double-speed first sector: %d bytes", c.DataLen())
	}
	c.Advance(half - 1)
	if c.DataLen() != 2048 {
		t.Fatalf("double-speed second sector arrived early")
	}
	c.Advance(1)
	if c.DataLen() != 4096 {
		t.Fatalf("double-speed second sector: %d bytes, want 4096", c.DataLen())
	}
}

func TestGetstatStates(t *testing.T) {
	// Reading bit during ReadN (after first sector).
	c := NewController()
	c.SetDisc(fakeDisc{})
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x06)
	c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
	drainResp(c)
	if got := sendCmd(c, 0x01); !bytes.Equal(got, []byte{0x22}) {
		t.Errorf("Getstat during ReadN = % X, want [22] (read+motor)", got)
	}
	// Playing bit during Play.
	c2 := NewController()
	c2.SetDisc(fakeDisc{})
	sendCmd(c2, 0x03, 0x03)
	c2.Advance(seekDelayCycles(1500, true))
	drainResp(c2)
	if got := sendCmd(c2, 0x01); !bytes.Equal(got, []byte{0x82}) {
		t.Errorf("Getstat during Play = % X, want [82] (play+motor)", got)
	}
	// Shell open with no disc.
	c3 := NewController()
	if got := sendCmd(c3, 0x01); !bytes.Equal(got, []byte{0x10}) {
		t.Errorf("Getstat with no disc = % X, want [10] (shell open)", got)
	}
}

func TestSetlocBadBCD(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	c.irqFlag = 0
	// 0Ah in the minute nibble is not valid BCD:
	// documented answer is INT5 with error code
	// 10h (invalid parameter value).
	got := sendCmd(c, 0x02, 0x0A, 0x02, 0x00)
	if !bytes.Equal(got, []byte{0x03, 0x10}) {
		t.Errorf("bad Setloc response = % X, want [03 10]", got)
	}
	if c.irqFlag != irqError {
		t.Errorf("bad Setloc irqFlag = %d, want INT5", c.irqFlag)
	}
	// Commands needing a disc, with no disc:
	// INT5 with code 80h.
	c2 := NewController()
	got = sendCmd(c2, 0x02, 0x00, 0x02, 0x00)
	if !bytes.Equal(got, []byte{0x11, 0x80}) {
		t.Errorf("no-disc Setloc = % X, want [11 80]", got)
	}
}

func TestPauseSecondResponse(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x06)
	c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
	drainResp(c)
	for c.DataLen() > 0 {
		c.ReadReg(2)
	}
	// Pause during ReadN: first response still
	// shows the reading bit (22h, the doc's own
	// example), the second arrives after the pause
	// delay with the bit cleared (02h).
	if got := sendCmd(c, 0x09); !bytes.Equal(got, []byte{0x22}) {
		t.Fatalf("Pause first response = % X, want [22]", got)
	}
	if c.Streaming() {
		t.Fatalf("still streaming after Pause")
	}
	delay := 5 * sectorPeriodCycles(0)
	c.Advance(delay - 1)
	if len(c.response) != 0 {
		t.Fatalf("Pause second response arrived early")
	}
	c.Advance(1)
	if got := drainResp(c); !bytes.Equal(got, []byte{0x02}) {
		t.Errorf("Pause second response = % X, want [02]", got)
	}
	if c.DataLen() != 0 {
		t.Errorf("data arrived after Pause: %d bytes", c.DataLen())
	}
}

func TestBusyQueuedCommand(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	setloc(c, 0x00, 0x12, 0x00)
	sendCmd(c, 0x15) // SeekL, completion pending
	// A command issued while the seek completion is
	// pending is held by the busy rule: no response
	// until the completion fires.
	if got := sendCmd(c, 0x13); len(got) != 0 {
		t.Fatalf("GetTN answered during busy seek: % X", got)
	}
	delay := seekDelayCycles(750, true)
	c.Advance(delay - 1)
	if len(c.response) != 0 {
		t.Fatalf("queued command fired early")
	}
	c.Advance(1)
	got := drainResp(c)
	want := []byte{0x02, 0x02, 0x01, 0x03} // seek INT2, then GetTN
	if !bytes.Equal(got, want) {
		t.Errorf("after busy completion: % X, want % X", got, want)
	}
}

func TestCDDAPlay(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	sendCmd(c, 0x03, 0x03) // Play track 3
	c.Advance(seekDelayCycles(1500, true))
	drainResp(c)
	for i := 0; i < 4; i++ {
		l, r := c.PullCDSample()
		if l != int16(i) || r != int16(-i) {
			t.Fatalf("CD-DA sample %d = (%d,%d), want (%d,%d)", i, l, r, i, -i)
		}
	}
	// Pull the rest of the sector: the position
	// must advance to the next LBA.
	for i := 4; i < 588; i++ {
		c.PullCDSample()
	}
	if c.playLBA != 1501 {
		t.Errorf("after 588 samples playLBA = %d, want 1501", c.playLBA)
	}
	l, _ := c.PullCDSample()
	if l != 588 {
		t.Errorf("first sample of next sector = %d, want 588", l)
	}
}

func TestCDDADoubleSpeed(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	sendCmd(c, 0x0E, 0x80)
	sendCmd(c, 0x03, 0x03)
	c.Advance(seekDelayCycles(1500, true))
	drainResp(c)
	// At double speed the position advances two
	// frames per output sample.
	for i, want := range []int16{0, 2, 4, 6} {
		l, _ := c.PullCDSample()
		if l != want {
			t.Fatalf("double-speed sample %d = %d, want %d", i, l, want)
		}
	}
}

func TestScanForward(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	sendCmd(c, 0x03, 0x03)
	c.Advance(seekDelayCycles(1500, true))
	drainResp(c)
	if got := sendCmd(c, 0x04); !bytes.Equal(got, []byte{0x82}) {
		t.Fatalf("Forward response = % X, want [82]", got)
	}
	if l, _ := c.PullCDSample(); l != 0 {
		t.Errorf("scan output = %d, want muted 0", l)
	}
	// Scan model: 10 sectors per sector period.
	c.Advance(2 * sectorPeriodCycles(0))
	if c.playLBA != 1520 {
		t.Errorf("after scan playLBA = %d, want 1520", c.playLBA)
	}
}

func TestXAADPCM4Bit(t *testing.T) {
	c := NewController()
	c.SetDisc(xaDisc{})
	sendCmd(c, 0x0E, 0x40) // Setmode: XA on
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x1B) // ReadS
	c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
	if c.DataLen() != 0 {
		t.Fatalf("XA sector leaked into the data FIFO: %d bytes", c.DataLen())
	}
	// Source pattern (block 0): sample j is
	// ((j%7)+1)<<12. Sample-and-hold at
	// 37.8k -> 44.1k repeats source indices in the
	// pattern 0,0,1,2,3,4,5,6,6,7.
	wantIdx := []int{0, 0, 1, 2, 3, 4, 5, 6, 6, 7}
	for i, idx := range wantIdx {
		l, r := c.PullCDSample()
		want := int16(((idx % 7) + 1) << 12)
		if l != want || r != want {
			t.Fatalf("XA sample %d = (%d,%d), want %d (source index %d)", i, l, r, want, idx)
		}
	}
}

func TestXAADPCM8Bit(t *testing.T) {
	c := NewController()
	c.SetDisc(xaDisc{bits8: true})
	sendCmd(c, 0x0E, 0x40)
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x1B)
	c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
	// Source pattern: sample j is (j+1)<<8, held in
	// the pattern 0,0,1,2,...
	want := []int16{256, 256, 512, 768, 1024}
	for i, w := range want {
		l, r := c.PullCDSample()
		if l != w || r != w {
			t.Fatalf("XA 8-bit sample %d = (%d,%d), want %d", i, l, r, w)
		}
	}
}

func TestSetfilterSkipsSectors(t *testing.T) {
	c := NewController()
	c.SetDisc(filterDisc{})
	sendCmd(c, 0x0D, 0, 0) // Setfilter file 0 channel 0
	sendCmd(c, 0x0E, 0x08) // Setmode: XA filter on
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x06) // ReadN
	c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
	if c.DataLen() != 2048 {
		t.Fatalf("matching sector: %d bytes, want 2048", c.DataLen())
	}
	c.Advance(sectorPeriodCycles(0)) // odd LBA: filtered out
	if c.DataLen() != 2048 {
		t.Fatalf("filtered sector was delivered: %d bytes", c.DataLen())
	}
	c.Advance(sectorPeriodCycles(0)) // even LBA again
	if c.DataLen() != 4096 {
		t.Fatalf("after next matching sector: %d bytes, want 4096", c.DataLen())
	}
	if c.data[2048] != 3 {
		t.Errorf("third sector's first byte = %d, want 3 (LBA 2 pattern)", c.data[2048])
	}
}

func TestSerializeMidStream(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	setloc(c, 0x00, 0x02, 0x00)
	sendCmd(c, 0x06)
	c.Advance(seekDelayCycles(0, true) + sectorPeriodCycles(0))
	for i := 0; i < 10; i++ {
		c.ReadWord()
	}
	s := c.Serialize()
	c2 := NewController()
	c2.SetDisc(fakeDisc{})
	if err := c2.Deserialize(s); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !bytes.Equal(c2.Serialize(), s) {
		t.Fatalf("mid-stream state diverged after round trip")
	}
	// The stream continues identically on both.
	c.Advance(sectorPeriodCycles(0))
	c2.Advance(sectorPeriodCycles(0))
	if c.DataLen() != c2.DataLen() {
		t.Fatalf("data lengths diverged: %d vs %d", c.DataLen(), c2.DataLen())
	}
	w1, _ := c.ReadWord()
	w2, _ := c2.ReadWord()
	if w1 != w2 {
		t.Errorf("stream words diverged: %08X vs %08X", w1, w2)
	}
}

func TestSerializeCDDAContinuation(t *testing.T) {
	c := NewController()
	c.SetDisc(fakeDisc{})
	sendCmd(c, 0x03, 0x03)
	c.Advance(seekDelayCycles(1500, true))
	drainResp(c)
	for i := 0; i < 100; i++ {
		c.PullCDSample()
	}
	s := c.Serialize()
	c2 := NewController()
	c2.SetDisc(fakeDisc{})
	if err := c2.Deserialize(s); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if !bytes.Equal(c2.Serialize(), s) {
		t.Fatalf("play state diverged after round trip")
	}
	for i := 0; i < 4; i++ {
		l1, r1 := c.PullCDSample()
		l2, r2 := c2.PullCDSample()
		if l1 != l2 || r1 != r2 {
			t.Fatalf("pull %d diverged: (%d,%d) vs (%d,%d)", i, l1, r1, l2, r2)
		}
		if l1 != int16(100+i) {
			t.Errorf("pull %d = %d, want %d", i, l1, 100+i)
		}
	}
}
