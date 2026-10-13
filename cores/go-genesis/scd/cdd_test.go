package scd

import (
	"bytes"
	"fmt"
	"math"
	"testing"
)

// buildCDDFixture builds a synthetic cue and bin disc in a temp
// dir: track 1 is MODE1/2352 data with 10 sectors, track 2 is
// audio with 150 sectors of a 440 Hz stereo sine, and track 3 is
// audio with 150 sectors of an 880 Hz sine. It returns the disc
// and the raw bin image bytes.
func buildCDDFixture(t *testing.T) (*Disc, []byte) {
	t.Helper()
	dir := t.TempDir()
	const total = 310
	bin := make([]byte, total*2352)
	for lba := 0; lba < total; lba++ {
		sector := bin[lba*2352 : (lba+1)*2352]
		for i := range sector {
			sector[i] = byte(lba + i)
		}
	}
	writeSine := func(startLBA, sectors int, freq float64) {
		for s := 0; s < sectors; s++ {
			sector := bin[(startLBA+s)*2352 : (startLBA+s+1)*2352]
			for frame := 0; frame < 588; frame++ {
				n := s*588 + frame
				v := int16(math.Round(math.Sin(2*math.Pi*freq*float64(n)/44100) * 12000))
				u := uint16(v)
				off := frame * 4
				sector[off] = byte(u)
				sector[off+1] = byte(u >> 8)
				sector[off+2] = byte(u)
				sector[off+3] = byte(u >> 8)
			}
		}
	}
	writeSine(10, 150, 440)
	writeSine(160, 150, 880)
	writeTestFile(t, dir, "test.bin", bin)
	msf := func(lba int) string {
		m, s, f := LBAToMSF(lba)
		return fmt.Sprintf("%02d:%02d:%02d", m, s, f)
	}
	cue := "FILE \"test.bin\" BINARY\n" +
		"  TRACK 1 MODE1/2352\n" +
		"    INDEX 01 " + msf(0) + "\n" +
		"  TRACK 2 AUDIO\n" +
		"    INDEX 01 " + msf(10) + "\n" +
		"  TRACK 3 AUDIO\n" +
		"    INDEX 01 " + msf(160) + "\n"
	cuePath := writeTestFile(t, dir, "test.cue", []byte(cue))
	return openAndClose(t, cuePath), bin
}

// cddWriteRaw writes nine command nibbles plus an explicit
// checksum nibble.
func cddWriteRaw(c *CDD, nib [9]uint8, checksum uint8) {
	for i := 0; i < 9; i++ {
		c.WriteCommandNibble(i, nib[i])
	}
	c.WriteCommandNibble(9, checksum)
}

// cddChecksum computes the correct checksum nibble for nine
// command nibbles.
func cddChecksum(nib [9]uint8) uint8 {
	sum := 0
	for _, n := range nib {
		sum += int(n)
	}
	return uint8((^sum) & 0xF)
}

// cddPacket snapshots the current RS packet.
func cddPacket(c *CDD) [10]uint8 {
	var p [10]uint8
	for i := range p {
		p[i] = c.StatusNibble(i)
	}
	return p
}

// cddCheckPacket asserts that a packet carries a valid checksum.
func cddCheckPacket(t *testing.T, p [10]uint8) {
	t.Helper()
	sum := 0
	for _, n := range p {
		sum += int(n)
	}
	if sum%16 != 0xF {
		t.Errorf("packet %v sums to %#x mod 16, want 0xF", p, sum%16)
	}
}

// cddDeliver writes a command with a correct checksum, advances
// one 75 Hz tick to deliver the response, and returns the RS
// packet. The packet checksum is verified.
func cddDeliver(t *testing.T, c *CDD, nib [9]uint8) [10]uint8 {
	t.Helper()
	cddWriteRaw(c, nib, cddChecksum(nib))
	c.AdvanceCycles(166667)
	p := cddPacket(c)
	cddCheckPacket(t, p)
	return p
}

// cddMSFCommand builds the nine nibbles for a command with an MSF
// target taken from an LBA.
func cddMSFCommand(code uint8, lba int) [9]uint8 {
	m, s, f := LBAToMSF(lba)
	return [9]uint8{code >> 4, code & 0xF,
		uint8(m / 10), uint8(m % 10),
		uint8(s / 10), uint8(s % 10),
		uint8(f / 10), uint8(f % 10), 0}
}

// cddSimpleCommand builds the nine nibbles for a command with no
// parameters.
func cddSimpleCommand(code uint8) [9]uint8 {
	return [9]uint8{code >> 4, code & 0xF, 0, 0, 0, 0, 0, 0, 0}
}

// cddTOCCommand builds the nine nibbles for a TOC query.
func cddTOCCommand(sel uint8, track int) [9]uint8 {
	return [9]uint8{0, 2, 0, sel, uint8(track / 10), uint8(track % 10), 0, 0, 0}
}

// cddPacketMSF decodes the BCD MSF in packet nibbles 2 to 7.
func cddPacketMSF(p [10]uint8) (m, s, f int) {
	m = int(p[2])*10 + int(p[3])
	s = int(p[4])*10 + int(p[5])
	f = int(p[6]&0x7)*10 + int(p[7])
	return m, s, f
}

// cddPosTotal returns the play position in absolute samples.
func cddPosTotal(c *CDD) int {
	return c.PositionLBA()*588 + c.PositionSample()
}

func TestCDDTOC(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)

	p := cddDeliver(t, c, cddTOCCommand(0x03, 0))
	m, s, f := cddPacketMSF(p)
	wm, ws, wf := LBAToMSF(310)
	if m != wm || s != ws || f != wf {
		t.Errorf("total length MSF = %02d:%02d:%02d, want %02d:%02d:%02d", m, s, f, wm, ws, wf)
	}
	if p[0] != 0x04 || p[1] != 0x03 {
		t.Errorf("total length packet RS0/RS1 = %#x/%#x, want 0x04/0x03", p[0], p[1])
	}

	p = cddDeliver(t, c, cddTOCCommand(0x04, 0))
	if p[2] != 0 || p[3] != 1 || p[4] != 0 || p[5] != 3 {
		t.Errorf("first/last tracks nibbles = %v, want first 1 and last 3", p[2:6])
	}

	p = cddDeliver(t, c, cddTOCCommand(0x05, 2))
	m, s, f = cddPacketMSF(p)
	wm, ws, wf = LBAToMSF(10)
	if m != wm || s != ws || f != wf {
		t.Errorf("track 2 start MSF = %02d:%02d:%02d, want %02d:%02d:%02d", m, s, f, wm, ws, wf)
	}
	if p[8] != 2 {
		t.Errorf("track 2 start query RS8 = %#x, want 2", p[8])
	}
	if p[6]&0x08 != 0 {
		t.Errorf("track 2 start query RS6 = %#x, has the data flag, want clear", p[6])
	}

	p = cddDeliver(t, c, cddTOCCommand(0x05, 1))
	if p[6]&0x08 == 0 {
		t.Errorf("track 1 start query RS6 = %#x, data flag clear, want set", p[6])
	}

	cddDeliver(t, c, cddMSFCommand(0x03, 10))
	p = cddDeliver(t, c, cddTOCCommand(0x02, 0))
	if p[2] != 0 || p[3] != 2 {
		t.Errorf("current track nibbles = %#x/%#x, want 0/2", p[2], p[3])
	}

	p = cddDeliver(t, c, cddTOCCommand(0x05, 9))
	for i := 2; i <= 7; i++ {
		if p[i] != 0 {
			t.Errorf("nonexistent track 9 query RS%d = %#x, want 0", i, p[i])
		}
	}
	if p[8] != 9 {
		t.Errorf("nonexistent track 9 query RS8 = %#x, want 9", p[8])
	}
}

func TestCDDChecksum(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	beforeRS := cddPacket(c)
	cddCheckPacket(t, beforeRS)
	beforeState := c.State()

	nib := cddMSFCommand(0x03, 10)
	cddWriteRaw(c, nib, cddChecksum(nib)^0x01)
	if c.State() != beforeState {
		t.Errorf("broken checksum changed state to %v", c.State())
	}
	if got := cddPacket(c); got != beforeRS {
		t.Errorf("broken checksum changed RS to %v, want %v", got, beforeRS)
	}
	c.AdvanceCycles(166667)
	if got := cddPacket(c); got != beforeRS {
		t.Errorf("broken checksum packet appeared after a tick: %v", got)
	}
	// cddDeliver verifies the checksum of every packet it returns,
	// and the other tests exercise it on every command.
	cddDeliver(t, c, cddSimpleCommand(0x00))
}

func TestCDDPlayTimingAndPosition(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	nib := cddMSFCommand(0x03, 10)
	cddWriteRaw(c, nib, cddChecksum(nib))
	if c.TakeInterrupt() {
		t.Fatal("interrupt fired before the delivery tick")
	}
	start := cddPosTotal(c)
	c.AdvanceCycles(12500000)
	if !c.TakeInterrupt() {
		t.Fatal("interrupt did not fire on the delivery tick")
	}
	if got := c.StatusNibble(0); got != 0x01 {
		t.Errorf("RS0 after play = %#x, want 0x01", got)
	}
	delta := cddPosTotal(c) - start
	t.Logf("play position delta after 1 s: %d samples (LBA %d, sample %d)", delta, c.PositionLBA(), c.PositionSample())
	if c.PositionLBA()-10 < 74 || c.PositionLBA()-10 > 76 {
		t.Errorf("LBA delta = %d, want within [74, 76]", c.PositionLBA()-10)
	}
	if delta < 44100-588 || delta > 44100+588 {
		t.Errorf("sample delta = %d, want 44100 within 588", delta)
	}
}

func TestCDDPauseResume(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	cddDeliver(t, c, cddMSFCommand(0x03, 10))
	c.AdvanceCycles(3125000)
	p := cddDeliver(t, c, cddSimpleCommand(0x06))
	if p[0] != 0x04 {
		t.Errorf("pause RS0 = %#x, want 0x04", p[0])
	}
	frozen := cddPosTotal(c)
	c.AdvanceCycles(12500000)
	if got := cddPosTotal(c); got != frozen {
		t.Errorf("paused position moved from %d to %d", frozen, got)
	}
	p = cddDeliver(t, c, cddSimpleCommand(0x07))
	if p[0] != 0x01 || p[1] != 0x02 || p[3] != 2 {
		t.Errorf("resume packet = %v, want RS0 0x01, RS1 0x02, track 2", p)
	}
	c.AdvanceCycles(3125000)
	if got := cddPosTotal(c); got <= frozen {
		t.Errorf("resumed position %d did not advance past frozen %d", got, frozen)
	}
}

func TestCDDSeek(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	cddDeliver(t, c, cddMSFCommand(0x04, 170))
	if c.State() != CDDPaused {
		t.Errorf("state after seek = %v, want paused", c.State())
	}
	if got := c.PositionLBA(); got < 169 || got > 171 {
		t.Errorf("position after seek = LBA %d, want within one sector of 170", got)
	}
	p := cddDeliver(t, c, cddSimpleCommand(0x00))
	if p[0] != 0x04 {
		t.Errorf("get status after seek RS0 = %#x, want 0x04", p[0])
	}
}

func TestCDDStop(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	cddDeliver(t, c, cddMSFCommand(0x03, 10))
	p := cddDeliver(t, c, cddSimpleCommand(0x01))
	if p[0] != 0x00 {
		t.Errorf("stop packet RS0 = %#x, want 0x00", p[0])
	}
	p = cddDeliver(t, c, cddSimpleCommand(0x00))
	if p[0] != 0x09 {
		t.Errorf("get status after stop RS0 = %#x, want 0x09", p[0])
	}
	if c.State() != CDDStopped || c.DisplayStatus() != 0x09 {
		t.Errorf("state %v display %#x, want stopped 0x09", c.State(), c.DisplayStatus())
	}
}

// cddMeasureFreq plays a track and measures the rendered left
// channel frequency by positive going zero crossings over one
// second of rendered output.
func cddMeasureFreq(t *testing.T, c *CDD, trackStartLBA int) float64 {
	t.Helper()
	renderMark := trackStartLBA * 588
	var left []int32
	for len(left) < 44100 {
		c.AdvanceCycles(166667)
		now := cddPosTotal(c)
		n := now - renderMark
		if n <= 0 {
			continue
		}
		buf := make([]int32, n*2)
		c.RenderCDDA(buf)
		renderMark = now
		for i := 0; i < n && len(left) < 44100; i++ {
			left = append(left, buf[i*2])
		}
	}
	crossings := 0
	for i := 1; i < len(left); i++ {
		if left[i-1] < 0 && left[i] >= 0 {
			crossings++
		}
	}
	return float64(crossings) * 44100.0 / float64(len(left))
}

func TestCDDDAFrequency(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	cddDeliver(t, c, cddMSFCommand(0x03, 10))
	f440 := cddMeasureFreq(t, c, 10)
	t.Logf("measured track 2 frequency: %.2f Hz", f440)
	if f440 < 440*0.98 || f440 > 440*1.02 {
		t.Errorf("track 2 frequency = %.2f Hz, want 440 within 2 percent", f440)
	}
	cddDeliver(t, c, cddMSFCommand(0x03, 160))
	f880 := cddMeasureFreq(t, c, 160)
	t.Logf("measured track 3 frequency: %.2f Hz", f880)
	if f880 < 880*0.98 || f880 > 880*1.02 {
		t.Errorf("track 3 frequency = %.2f Hz, want 880 within 2 percent", f880)
	}
}

func cddRenderWindow(t *testing.T, disc *Disc, fd int) []int32 {
	t.Helper()
	c := NewCDD(disc)
	if fd != 1024 {
		word := fd << 4
		c.WriteFaderByte(0, byte(word>>8))
		c.WriteFaderByte(1, byte(word))
		if got := c.FaderFD(); got != fd {
			t.Fatalf("FaderFD = %d, want %d", got, fd)
		}
	}
	cddDeliver(t, c, cddMSFCommand(0x03, 10))
	c.AdvanceCycles(6250000)
	n := cddPosTotal(c) - 10*588
	buf := make([]int32, n*2)
	c.RenderCDDA(buf)
	return buf
}

func TestCDDFader(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	full := cddRenderWindow(t, disc, 1024)
	half := cddRenderWindow(t, disc, 512)
	if len(full) != len(half) {
		t.Fatalf("render lengths differ: %d vs %d", len(full), len(half))
	}
	peak := func(buf []int32) int64 {
		var p int64
		for i := 0; i < len(buf); i += 2 {
			v := int64(buf[i])
			if v < 0 {
				v = -v
			}
			if v > p {
				p = v
			}
		}
		return p
	}
	pf, ph := peak(full), peak(half)
	ratio := float64(ph) / float64(pf)
	t.Logf("fader peaks: FD1024 %d, FD512 %d, ratio %.4f", pf, ph, ratio)
	if ratio < 0.475 || ratio > 0.525 {
		t.Errorf("fader ratio = %.4f, want 0.5 within 5 percent", ratio)
	}
	zero := cddRenderWindow(t, disc, 0)
	for i, v := range zero {
		if v != 0 {
			t.Fatalf("FD 0 render nonzero at %d: %d", i, v)
		}
	}
}

func TestCDDTrackEndAdvance(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	cddDeliver(t, c, cddMSFCommand(0x03, 159))
	// The delivery tick inside cddDeliver already advanced one
	// sector of playback, so the remaining advance is 2 seconds
	// of cycles minus that one tick.
	c.AdvanceCycles(25000000 - 166667)
	if c.State() != CDDPlaying {
		t.Errorf("state after crossing into track 3 = %v, want playing", c.State())
	}
	if got := c.PositionLBA(); got < 160 || got >= 310 {
		t.Errorf("position after crossing = LBA %d, want inside track 3", got)
	}

	c2 := NewCDD(disc)
	cddDeliver(t, c2, cddMSFCommand(0x03, 309))
	c2.AdvanceCycles(25000000)
	if c2.State() != CDDStopped || c2.DisplayStatus() != 0x09 {
		t.Errorf("past disc end: state %v display %#x, want stopped 0x09", c2.State(), c2.DisplayStatus())
	}
}

func TestCDDFastForward(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	cddDeliver(t, c, cddMSFCommand(0x03, 10))
	p := cddDeliver(t, c, cddSimpleCommand(0x08))
	if p[0] != 0x03 || c.State() != CDDScanFF {
		t.Errorf("FF packet RS0 %#x state %v, want 0x03 scanning forward", p[0], c.State())
	}
	scanBuf := make([]int32, 200)
	c.RenderCDDA(scanBuf)
	for i, v := range scanBuf {
		if v != 0 {
			t.Fatalf("render during scan nonzero at %d: %d", i, v)
		}
	}
	before := cddPosTotal(c)
	c.AdvanceCycles(3125000)
	delta := cddPosTotal(c) - before
	t.Logf("FF position delta over 0.25 s of cycles: %d samples (want about 88200)", delta)
	if delta < 88200-1176 || delta > 88200+1176 {
		t.Errorf("FF delta = %d samples, want 88200 within 2 sectors", delta)
	}
	// Play returns to normal playback.
	p = cddDeliver(t, c, cddMSFCommand(0x03, c.PositionLBA()))
	if p[0] != 0x01 || c.State() != CDDPlaying {
		t.Errorf("play after FF: RS0 %#x state %v, want 0x01 playing", p[0], c.State())
	}
}

func TestCDDDataReadEvents(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	nib := cddMSFCommand(0x03, 2)
	cddWriteRaw(c, nib, cddChecksum(nib))
	var got []int
	for i := 0; i < 20; i++ {
		ev := c.AdvanceCycles(166667)
		if ev.DataCount > 0 {
			for k := 0; k < ev.DataCount; k++ {
				got = append(got, ev.DataLBA+k)
			}
		}
		if i >= 2 && len(got) >= 3 && got[0] == 2 && got[1] == 3 && got[2] == 4 {
			// First three events verified; keep draining to the
			// end of track 1 below.
		}
		if c.State() == CDDStopped && len(got) > 0 {
			break
		}
	}
	want := []int{2, 3, 4, 5, 6, 7, 8, 9}
	if len(got) != len(want) {
		t.Fatalf("data read events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("data read events = %v, want %v", got, want)
		}
	}
	if c.State() != CDDStopped {
		t.Errorf("state after reading to track end = %v, want stopped", c.State())
	}
	ev := c.AdvanceCycles(166667)
	if ev.DataCount != 0 || ev.DataLBA != -1 {
		t.Errorf("event after read stop = %+v, want none", ev)
	}
}

func TestCDDOpenCloseTray(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	p := cddDeliver(t, c, cddSimpleCommand(0x0D))
	if p[0] != 0x05 || c.State() != CDDTrayOpen {
		t.Errorf("open: RS0 %#x state %v, want 0x05 tray open", p[0], c.State())
	}
	p = cddDeliver(t, c, cddMSFCommand(0x03, 10))
	if c.State() != CDDTrayOpen {
		t.Errorf("play while open changed state to %v", c.State())
	}
	if p[0] != 0x05 {
		t.Errorf("play while open RS0 = %#x, want 0x05", p[0])
	}
	p = cddDeliver(t, c, cddSimpleCommand(0x0C))
	if p[0] != 0x00 {
		t.Errorf("close packet RS0 = %#x, want 0x00", p[0])
	}
	if c.State() != CDDStopped || c.DisplayStatus() != 0x09 {
		t.Errorf("after close tick: state %v display %#x, want stopped 0x09", c.State(), c.DisplayStatus())
	}
	p = cddDeliver(t, c, cddSimpleCommand(0x00))
	if p[0] != 0x09 {
		t.Errorf("get status after close RS0 = %#x, want 0x09", p[0])
	}
}

func TestCDDOutOfRangePlay(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	c := NewCDD(disc)
	cddDeliver(t, c, cddMSFCommand(0x03, 400))
	if c.State() != CDDStopped || c.PositionLBA() != 0 {
		t.Errorf("out of range play: state %v LBA %d, want stopped at 0", c.State(), c.PositionLBA())
	}
	// Seconds field 75: invalid MSF.
	nib := [9]uint8{0, 3, 0, 0, 7, 5, 0, 0, 0}
	cddDeliver(t, c, nib)
	if c.State() != CDDStopped || c.PositionLBA() != 0 {
		t.Errorf("invalid seconds play: state %v LBA %d, want stopped at 0", c.State(), c.PositionLBA())
	}
	// A nibble over 9: also invalid BCD.
	nib = [9]uint8{0, 3, 0, 0, 1, 0x0A, 0, 0, 0}
	cddDeliver(t, c, nib)
	if c.State() != CDDStopped {
		t.Errorf("invalid BCD play changed state to %v", c.State())
	}
}

func TestCDDSerializeRoundTrip(t *testing.T) {
	disc, _ := buildCDDFixture(t)
	if CDDStateSize > 56 {
		t.Fatalf("CDDStateSize = %d, want at most 56", CDDStateSize)
	}
	c1 := NewCDD(disc)
	cddDeliver(t, c1, cddMSFCommand(0x03, 10))
	c1.AdvanceCycles(6250000)
	buf := make([]byte, CDDStateSize)
	c1.SerializeState(buf)

	c2 := NewCDD(disc)
	c2.DeserializeState(buf)

	renderBoth := func() ([]int32, []int32) {
		n := cddPosTotal(c1) - 10*588
		b1 := make([]int32, n*2)
		b2 := make([]int32, n*2)
		c1.RenderCDDA(b1)
		c2.RenderCDDA(b2)
		return b1, b2
	}
	c1.AdvanceCycles(6250000)
	c2.AdvanceCycles(6250000)
	b1, b2 := renderBoth()
	if !bytes.Equal(int32Bytes(b1), int32Bytes(b2)) {
		t.Error("rendered buffers differ after serialize round trip")
	}
	if cddPosTotal(c1) != cddPosTotal(c2) {
		t.Errorf("positions differ: %d vs %d", cddPosTotal(c1), cddPosTotal(c2))
	}
	buf1 := make([]byte, CDDStateSize)
	buf2 := make([]byte, CDDStateSize)
	c1.SerializeState(buf1)
	c2.SerializeState(buf2)
	if !bytes.Equal(buf1, buf2) {
		t.Error("re-serialized bytes differ after round trip")
	}
}

func int32Bytes(v []int32) []byte {
	out := make([]byte, len(v)*4)
	for i, x := range v {
		u := uint32(x)
		out[i*4] = byte(u)
		out[i*4+1] = byte(u >> 8)
		out[i*4+2] = byte(u >> 16)
		out[i*4+3] = byte(u >> 24)
	}
	return out
}

func TestCDDReadAudio(t *testing.T) {
	disc, bin := buildCDDFixture(t)
	got, err := disc.ReadAudio(20)
	if err != nil {
		t.Fatalf("ReadAudio(20): %v", err)
	}
	if len(got) != 2352 {
		t.Fatalf("ReadAudio(20) len = %d, want 2352", len(got))
	}
	if !bytes.Equal(got, bin[20*2352:21*2352]) {
		t.Error("ReadAudio(20) does not match the fixture sector bytes")
	}
	if _, err := disc.ReadAudio(310); err == nil {
		t.Error("ReadAudio(310) succeeded, want out of range error")
	}
	if _, err := disc.ReadAudio(-1); err == nil {
		t.Error("ReadAudio(-1) succeeded, want out of range error")
	}

	dir := t.TempDir()
	isoPath := writeTestFile(t, dir, "plain.iso", make([]byte, 4*2048))
	iso := openAndClose(t, isoPath)
	if _, err := iso.ReadAudio(0); err == nil {
		t.Error("ReadAudio on a 2048 byte sector disc succeeded, want error")
	}
}
