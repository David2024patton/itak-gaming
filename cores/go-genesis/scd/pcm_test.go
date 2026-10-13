package scd

import (
	"bytes"
	"math"
	"testing"
)

// pcmSelectChannel selects ch through a MOD 1 CTRL write with the
// chip master on, which is the normal way test setups change the
// current channel.
func pcmSelectChannel(p *PCM, ch int) {
	p.WriteReg(0x07, 0xC0|uint8(ch))
}

// pcmSetupChannel programs the currently selected channel with the
// given register values. The loop start is written before the start
// register, because the start write retriggers the channel and its
// refetch can use the loop start.
func pcmSetupChannel(p *PCM, env, pan uint8, fd, ls uint16, st uint8) {
	p.WriteReg(0x00, env)
	p.WriteReg(0x01, pan)
	p.WriteReg(0x02, uint8(fd))
	p.WriteReg(0x03, uint8(fd>>8))
	p.WriteReg(0x04, uint8(ls))
	p.WriteReg(0x05, uint8(ls>>8))
	p.WriteReg(0x06, st)
}

// signMag encodes v as a sign-magnitude wave byte. Positive values
// clamp to 126 because a positive 127 would be the 0xFF loop marker.
func signMag(v int) uint8 {
	if v > 126 {
		v = 126
	}
	if v < -127 {
		v = -127
	}
	if v >= 0 {
		return 0x80 | uint8(v)
	}
	return uint8(-v)
}

func TestPCMFrequency(t *testing.T) {
	wave := make([]byte, 0x10000)
	for i := 0; i < 256; i++ {
		v := int(math.Round(126 * math.Sin(2*math.Pi*float64(i)/256)))
		wave[i] = signMag(v)
	}
	for i := 256; i < 288; i++ {
		wave[i] = 0xFF
	}
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 255, 0xFF, 0x0800, 0, 0)
	p.WriteReg(0x08, 0x01)

	crossings := 0
	prevSign := 0
	for i := 0; i < 44100; i++ {
		l, _ := p.RenderFrame()
		sign := 0
		if l > 0 {
			sign = 1
		} else if l < 0 {
			sign = -1
		}
		if sign != 0 {
			if prevSign != 0 && sign != prevSign {
				crossings++
			}
			prevSign = sign
		}
	}
	// 44,100 frames at 44,100 Hz is exactly one second, and a sine
	// crosses zero twice per period, so half the crossing count is
	// the measured frequency in Hz.
	measured := float64(crossings) / 2
	want := 32552.08 / 256
	t.Logf("measured frequency %.2f Hz from %d zero crossings, want %.2f Hz", measured, crossings, want)
	if math.Abs(measured-want) > 0.03*want {
		t.Fatalf("measured frequency %.2f Hz, want %.2f Hz within 3 percent", measured, want)
	}
}

func TestPCMLoopWrap(t *testing.T) {
	wave := make([]byte, 0x10000)
	for i := 0; i < 128; i++ {
		wave[i] = 0x80 | 100 // positive constant, magnitude 100
	}
	for i := 128; i < 256; i++ {
		wave[i] = 100 // negative constant, magnitude 100
	}
	wave[256] = 0xFF
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 255, 0xFF, 0x0800, 128, 0)
	p.WriteReg(0x08, 0x01)

	for i := 0; i < 10; i++ {
		p.Tick()
	}
	if got := p.ChannelSample(0); got != 0x80|100 {
		t.Fatalf("sample before the loop region = %#02x, want %#02x", got, 0x80|100)
	}
	for i := 10; i < 255; i++ {
		p.Tick()
	}
	if got := p.ChannelAddr(0) >> 11; got != 255 {
		t.Fatalf("sample index after 255 ticks = %d, want 255", got)
	}
	p.Tick() // tick 256 fetches the marker at 256 and jumps to 128
	if got := p.ChannelAddr(0) >> 11; got != 128 {
		t.Fatalf("sample index at the wrap tick = %d, want 128 (the loop start)", got)
	}
	if got := p.ChannelSample(0); got != 100 {
		t.Fatalf("sample after wrapping = %#02x, want %#02x (the negative constant)", got, 100)
	}
}

func TestPCMTwoChannelsSum(t *testing.T) {
	wave := make([]byte, 0x10000)
	wave[0] = 0xFE // positive 126
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 128, 0xFF, 0, 0, 0)
	pcmSelectChannel(p, 1)
	pcmSetupChannel(p, 128, 0xFF, 0, 0, 0)

	p.WriteReg(0x08, 0x01)
	p.Tick()
	singleL, singleR := p.RenderFrame()
	// One channel contributes (126 * 128 * 15) >> 5 = 7560 per side.
	if singleL != 7560 || singleR != 7560 {
		t.Fatalf("single channel output = %d, %d, want 7560, 7560", singleL, singleR)
	}
	p.WriteReg(0x08, 0x03)
	p.Tick()
	doubleL, doubleR := p.RenderFrame()
	// Exact equality holds here: both channels produce the identical
	// per channel term, and the documented truncation is applied per
	// channel before the sum, so the two channel sum is exactly
	// twice the single channel value with no rounding difference.
	if doubleL != 2*singleL || doubleR != 2*singleR {
		t.Fatalf("two channel output = %d, %d, want exactly twice %d, %d", doubleL, doubleR, singleL, singleR)
	}
}

func TestPCMPanFullLeft(t *testing.T) {
	wave := make([]byte, 0x10000)
	wave[0] = 0xFE
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 255, 0xF0, 0, 0, 0)
	p.WriteReg(0x08, 0x01)

	nonzeroLeft := 0
	for i := 0; i < 200; i++ {
		l, r := p.RenderFrame()
		if r != 0 {
			t.Fatalf("frame %d: right output = %d, want exactly 0 with full left pan", i, r)
		}
		if l != 0 {
			nonzeroLeft++
		}
	}
	if nonzeroLeft == 0 {
		t.Fatal("left output was never nonzero with full left pan")
	}
}

func TestPCMStartRetrigger(t *testing.T) {
	wave := make([]byte, 0x10000)
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 255, 0xFF, 0x0800, 0, 2)
	p.WriteReg(0x08, 0x01)

	start := uint32(2) << 19
	if got := p.ChannelAddr(0); got != start {
		t.Fatalf("address after the start write = %d, want %d", got, start)
	}
	for i := 0; i < 5; i++ {
		p.Tick()
	}
	if got := p.ChannelAddr(0); got != start+5*0x0800 {
		t.Fatalf("address after 5 ticks = %d, want %d", got, start+5*0x0800)
	}
	p.WriteReg(0x06, 2) // write the start register again: retrigger
	if got := p.ChannelAddr(0); got != start {
		t.Fatalf("address after the start rewrite = %d, want %d", got, start)
	}
}

func TestPCMOffSilences(t *testing.T) {
	wave := make([]byte, 0x10000)
	wave[0] = 0xFE
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 255, 0xFF, 0, 0, 0)

	// Channel off: the on and off bit for channel 0 stays clear.
	p.WriteReg(0x08, 0x00)
	for i := 0; i < 50; i++ {
		if l, r := p.RenderFrame(); l != 0 || r != 0 {
			t.Fatalf("channel off, frame %d: output = %d, %d, want 0, 0", i, l, r)
		}
	}
	// Chip master off: the channel is enabled but CTRL bit 7 is
	// clear. A MOD 1 CTRL write with bit 7 clear selects channel 0
	// and keeps the master off.
	p.WriteReg(0x08, 0x01)
	p.WriteReg(0x07, 0x40)
	for i := 0; i < 50; i++ {
		if l, r := p.RenderFrame(); l != 0 || r != 0 {
			t.Fatalf("master off, frame %d: output = %d, %d, want 0, 0", i, l, r)
		}
	}
}

func TestPCMSignMagnitudeDecode(t *testing.T) {
	wave := make([]byte, 0x10000)
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 255, 0xFF, 0, 0, 0)
	p.WriteReg(0x08, 0x01)

	// (126 * 255 * 15) >> 5 = 15060.
	wave[0] = 0xFE // positive 126
	p.Tick()
	if l, r := p.RenderFrame(); l != 15060 || r != 15060 {
		t.Fatalf("positive 126 output = %d, %d, want 15060, 15060", l, r)
	}
	wave[0] = 0x7E // negative 126
	p.Tick()
	if l, r := p.RenderFrame(); l != -15060 || r != -15060 {
		t.Fatalf("negative 126 output = %d, %d, want -15060, -15060", l, r)
	}
}

func TestPCMSerializeRoundTrip(t *testing.T) {
	if PCMStateSize != 107 {
		t.Fatalf("PCMStateSize = %d, want 107", PCMStateSize)
	}
	wave := make([]byte, 0x10000)
	for i := range wave {
		wave[i] = 0x80 | uint8(i%64)
	}
	p := NewPCM(wave)
	pcmSelectChannel(p, 0)
	pcmSetupChannel(p, 200, 0xF8, 0x0100, 16, 0)
	pcmSelectChannel(p, 3)
	pcmSetupChannel(p, 90, 0x8F, 0x0200, 32, 1)
	p.WriteReg(0x07, 0x80|0x09) // MOD 0: wave bank 9, channel stays 3
	for off := uint8(0x09); off <= 0x0F; off++ {
		p.WriteReg(off, 0xA0+off)
	}
	p.WriteReg(0x08, 0x09) // channels 0 and 3 on
	for i := 0; i < 7; i++ {
		p.Tick()
	}
	for i := 0; i < 3; i++ {
		p.RenderFrame()
	}

	buf1 := make([]byte, PCMStateSize)
	p.SerializeState(buf1)

	restored := NewPCM(wave)
	restored.DeserializeState(buf1)
	buf2 := make([]byte, PCMStateSize)
	restored.SerializeState(buf2)
	if !bytes.Equal(buf1, buf2) {
		t.Fatalf("state bytes differ after a round trip:\nfirst  %x\nsecond %x", buf1, buf2)
	}
	if restored.Selected() != 3 || restored.WaveBank() != 9 || restored.OnOff() != 0x09 {
		t.Fatalf("restored globals: selected %d, bank %d, onOff %#02x, want 3, 9, 0x09",
			restored.Selected(), restored.WaveBank(), restored.OnOff())
	}
	for i := 0; i < 100; i++ {
		wantL, wantR := p.RenderFrame()
		gotL, gotR := restored.RenderFrame()
		if gotL != wantL || gotR != wantR {
			t.Fatalf("frame %d: restored output = %d, %d, want %d, %d", i, gotL, gotR, wantL, wantR)
		}
	}
}

func TestPCMControlSemantics(t *testing.T) {
	wave := make([]byte, 0x10000)
	p := NewPCM(wave)
	if p.Selected() != 0 || p.WaveBank() != 0 {
		t.Fatalf("power-on selection: channel %d, bank %d, want 0, 0", p.Selected(), p.WaveBank())
	}
	p.WriteReg(0x07, 0xC0|0x02) // MOD 1: select channel 2
	if p.Selected() != 2 {
		t.Fatalf("selected channel = %d, want 2", p.Selected())
	}
	p.WriteReg(0x07, 0x80|0x05) // MOD 0: wave bank 5 only
	if p.WaveBank() != 5 {
		t.Fatalf("wave bank = %d, want 5", p.WaveBank())
	}
	if p.Selected() != 2 {
		t.Fatalf("a MOD 0 write changed the selected channel to %d, want 2", p.Selected())
	}
	if got := p.Ctrl(); got != 0x85 {
		t.Fatalf("ctrl = %#02x, want the raw last write 0x85", got)
	}
	p.WriteReg(0x07, 0xC0|0x01) // MOD 1: select channel 1 only
	if p.Selected() != 1 {
		t.Fatalf("selected channel = %d, want 1", p.Selected())
	}
	if p.WaveBank() != 5 {
		t.Fatalf("a MOD 1 write changed the wave bank to %d, want 5", p.WaveBank())
	}

	// Register writes land in the selected channel only.
	pcmSelectChannel(p, 0)
	p.WriteReg(0x00, 0x11)
	pcmSelectChannel(p, 1)
	p.WriteReg(0x00, 0xAB)
	pcmSelectChannel(p, 0)
	if got := p.ReadReg(0x00); got != 0x11 {
		t.Fatalf("channel 0 ENV = %#02x, want 0x11 (its own write only)", got)
	}
	pcmSelectChannel(p, 1)
	if got := p.ReadReg(0x00); got != 0xAB {
		t.Fatalf("channel 1 ENV = %#02x, want 0xab", got)
	}

	// The unused offsets are store and read back mirrors.
	p.WriteReg(0x09, 0x5A)
	p.WriteReg(0x0F, 0xC3)
	if got := p.ReadReg(0x09); got != 0x5A {
		t.Fatalf("mirror offset 9 = %#02x, want 0x5a", got)
	}
	if got := p.ReadReg(0x0F); got != 0xC3 {
		t.Fatalf("mirror offset 15 = %#02x, want 0xc3", got)
	}
}
