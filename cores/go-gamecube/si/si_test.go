package si_test

import (
	"bytes"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-gamecube/si"
)

func w32(s *si.SI, off uint32, v uint32) {
	s.Write16(off, uint16(v>>16))
	s.Write16(off+2, uint16(v))
}

func r32(s *si.SI, off uint32) uint32 {
	return uint32(s.Read16(off))<<16 | uint32(s.Read16(off+2))
}

func chanOff(ch int, within uint32) uint32 { return uint32(ch)*0x0C + within }

const (
	offPoll   = 0x30
	offComCSR = 0x34
	offStatus = 0x38
	offIOBuf  = 0x40
)

func setIO(s *si.SI, data []byte) {
	for i := 0; i+1 < len(data)+1 && i < 128; i += 2 {
		var v uint16
		v = uint16(data[i]) << 8
		if i+1 < len(data) {
			v |= uint16(data[i+1])
		}
		s.Write16(offIOBuf+uint32(i), v)
	}
}

func getIO(s *si.SI, n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; i < n; i += 2 {
		v := s.Read16(offIOBuf + uint32(i))
		out = append(out, byte(v>>8))
		if i+1 < n {
			out = append(out, byte(v))
		}
	}
	return out
}

func fillIO(s *si.SI, b byte) {
	setIO(s, bytes.Repeat([]byte{b}, 128))
}

func stdPad() si.Pad {
	return si.Pad{
		Buttons:  0x1100,
		StickX:   128,
		StickY:   130,
		CStickX:  100,
		CStickY:  110,
		TriggerL: 20,
		TriggerR: 30,
	}
}

// Expected packings for stdPad: INBUF_HI carries the
// buttons word, stick X, stick Y; INBUF_LO carries the
// C-stick pair and the two triggers.
const (
	stdHi = 0x11008082
	stdLo = 0x646E141E
)

var stdPacket = []byte{0x11, 0x00, 128, 130, 100, 110, 20, 30}

func newSI(pads *[4]si.Pad) *si.SI {
	return si.New(func() [4]si.Pad { return *pads })
}

func TestPollPackingAndRDST(t *testing.T) {
	var pads [4]si.Pad
	pads[0] = stdPad()
	s := newSI(&pads)

	// POLL: Y in bits 24-31, X in bits 16-22, EN bit 0.
	pollVal := uint32(200)<<24 | uint32(3)<<16 | 1
	w32(s, offPoll, pollVal)
	if got := r32(s, offPoll); got != pollVal {
		t.Fatalf("POLL readback = %#x, want %#x (X and Y stored)", got, pollVal)
	}
	if got := r32(s, chanOff(0, 0x04)); got != stdHi {
		t.Fatalf("INBUF_HI = %#x, want %#x", got, stdHi)
	}
	if got := r32(s, offStatus); got != 1<<28 {
		t.Fatalf("STATUS = %#x, want only RDST for channel 0 (bit 28)", got)
	}
	if got := r32(s, chanOff(0, 0x08)); got != stdLo {
		t.Fatalf("INBUF_LO = %#x, want %#x", got, stdLo)
	}
	if got := r32(s, offStatus); got != 0 {
		t.Fatalf("STATUS after reading INBUF_LO = %#x, want RDST cleared", got)
	}

	// PollFrame re-polls the enabled channel with fresh data.
	pads[0].Buttons = 0x0200
	s.PollFrame()
	if got := r32(s, chanOff(0, 0x04)); got != 0x02008082 {
		t.Fatalf("INBUF_HI after PollFrame = %#x, want 0x02008082", got)
	}
	if got := r32(s, offStatus); got != 1<<28 {
		t.Fatalf("STATUS after PollFrame = %#x, want RDST set again", got)
	}
}

func TestPollDisconnectedChannel(t *testing.T) {
	var pads [4]si.Pad
	pads[1] = stdPad()
	s := newSI(&pads)
	if s.Connected[1] {
		t.Fatal("channel 1 should start disconnected")
	}
	w32(s, offPoll, 1<<1) // EN channel 1 only
	if got := r32(s, chanOff(1, 0x04)); got != 0x80000000 {
		t.Fatalf("disconnected INBUF_HI = %#x, want the 0x8000 error packet", got)
	}
	if got := r32(s, chanOff(1, 0x08)); got != 0 {
		t.Fatalf("disconnected INBUF_LO = %#x, want 0", got)
	}
}

func comcsr(channel, outLen, inLen int) uint32 {
	// TSTART | CHANNEL | OUTLNGTH | INLNGTH | RDSTINTMSK.
	return 1 | uint32(channel)<<1 | uint32(outLen)<<16 | uint32(inLen)<<24 | 1<<30
}

func TestGetStatusTransfer(t *testing.T) {
	var pads [4]si.Pad
	pads[0] = stdPad()
	s := newSI(&pads)
	fillIO(s, 0xAA)

	// OUTBUF bytes: command 0x42, rumble motor byte 0x01.
	w32(s, chanOff(0, 0x00), 0x42010000)
	w32(s, offComCSR, comcsr(0, 2, 8))

	if got := getIO(s, 8); !bytes.Equal(got, stdPacket) {
		t.Fatalf("IOBUF after 0x42 = %v, want the status packet %v", got, stdPacket)
	}
	if got := r32(s, chanOff(0, 0x04)); got != stdHi {
		t.Fatalf("INBUF_HI after 0x42 = %#x, want %#x", got, stdHi)
	}
	if got := r32(s, offStatus); got != 1<<28 {
		t.Fatalf("STATUS after transfer = %#x, want RDST channel 0", got)
	}
	if got := r32(s, chanOff(0, 0x08)); got != stdLo {
		t.Fatalf("INBUF_LO after 0x42 = %#x, want %#x", got, stdLo)
	}
	cc := r32(s, offComCSR)
	if cc&(1<<31) == 0 {
		t.Fatal("RDSTINT not set after the transfer completed")
	}
	if cc&(1<<28) != 0 {
		t.Fatal("COMERR set for a connected channel (INLNGTH 8 leaves bit 28 clear)")
	}
	if !s.IRQAsserted() {
		t.Fatal("IRQ not asserted with RDSTINT set and RDSTINTMSK enabled")
	}
	if got := s.Motor(0); got != 1 {
		t.Fatalf("Motor(0) = %d, want 1 (the second output byte)", got)
	}
	// RDSTINT is write 1 to clear.
	w32(s, offComCSR, 1<<31|1<<30)
	if s.IRQAsserted() {
		t.Fatal("IRQ still asserted after RDSTINT was cleared")
	}
	if r32(s, offComCSR)&(1<<31) != 0 {
		t.Fatal("RDSTINT still set after the clear write")
	}
}

func TestInfoAndOriginCommands(t *testing.T) {
	var pads [4]si.Pad
	pads[0] = stdPad()
	s := newSI(&pads)

	for _, cmd := range []uint32{0x00000000, 0xFF000000} {
		fillIO(s, 0xAA)
		w32(s, chanOff(0, 0x00), cmd)
		w32(s, offComCSR, comcsr(0, 1, 3))
		if got := getIO(s, 3); !bytes.Equal(got, []byte{0x09, 0x00, 0x00}) {
			t.Fatalf("info command %#x response = %v, want [09 00 00]", cmd>>24, got)
		}
	}

	fillIO(s, 0xAA)
	w32(s, chanOff(0, 0x00), 0x41000000)
	w32(s, offComCSR, comcsr(0, 1, 10))
	want := append(append([]byte{}, stdPacket...), 0, 0)
	if got := getIO(s, 10); !bytes.Equal(got, want) {
		t.Fatalf("origin response = %v, want %v (status packet plus two zero bytes)", got, want)
	}
	if got := r32(s, chanOff(0, 0x04)); got != stdHi {
		t.Fatalf("INBUF_HI after origin = %#x, want %#x", got, stdHi)
	}
}

func TestDisconnectedTransferSetsCOMERR(t *testing.T) {
	var pads [4]si.Pad
	pads[1] = stdPad()
	s := newSI(&pads)
	fillIO(s, 0xAA)

	w32(s, chanOff(1, 0x00), 0x42000000)
	w32(s, offComCSR, comcsr(1, 2, 8))

	cc := r32(s, offComCSR)
	if cc&(1<<28) == 0 {
		t.Fatal("COMERR not set for a transfer on a disconnected channel")
	}
	if cc&(1<<31) == 0 {
		t.Fatal("RDSTINT not set after the disconnected transfer")
	}
	if got := r32(s, offStatus); got != 1<<29 {
		t.Fatalf("STATUS = %#x, want only RDST for channel 1 (bit 29)", got)
	}
	if got := getIO(s, 8); !bytes.Equal(got, []byte{0x80, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("IOBUF error response = %v, want [0x80 0 0 0 0 0 0 0]", got)
	}
	if got := r32(s, chanOff(1, 0x04)); got != 0x80000000 {
		t.Fatalf("INBUF_HI error packet = %#x, want 0x80000000", got)
	}
	if got := r32(s, chanOff(1, 0x08)); got != 0 {
		t.Fatalf("INBUF_LO error packet = %#x, want 0", got)
	}
}

func TestUnknownCommandAnswersZeros(t *testing.T) {
	var pads [4]si.Pad
	pads[0] = stdPad()
	s := newSI(&pads)
	fillIO(s, 0xAA)
	w32(s, chanOff(0, 0x00), 0x99000000)
	w32(s, offComCSR, comcsr(0, 1, 4))
	if got := getIO(s, 4); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("unknown command response = %v, want zeros", got)
	}
	if r32(s, offComCSR)&(1<<28) != 0 {
		t.Fatal("COMERR set for an unknown command on a connected channel")
	}
}

func TestSISerializeRoundTrip(t *testing.T) {
	var pads [4]si.Pad
	pads[0] = stdPad()
	s := newSI(&pads)
	s.Connected[2] = true

	w32(s, offPoll, 1) // poll channel 0 into INBUF
	w32(s, chanOff(0, 0x00), 0x42010000)
	w32(s, offComCSR, comcsr(0, 2, 8)) // motor 1, RDSTINT set
	// Distinctive IOBUF tail bytes that no transfer wrote.
	s.Write16(offIOBuf+100, 0xBEEF)

	pollBefore := r32(s, offPoll)
	comBefore := r32(s, offComCSR)
	statusBefore := r32(s, offStatus)
	ioBefore := getIO(s, 128)

	blob := s.Serialize()
	if len(blob) != si.SerializeSize {
		t.Fatalf("Serialize length = %d, want %d", len(blob), si.SerializeSize)
	}

	var pads2 [4]si.Pad
	s2 := newSI(&pads2)
	if err := s2.Deserialize(blob); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if s2.Connected != s.Connected {
		t.Fatalf("restored Connected = %v, want %v", s2.Connected, s.Connected)
	}
	if got := r32(s2, chanOff(0, 0x04)); got != stdHi {
		t.Fatalf("restored INBUF_HI = %#x, want %#x", got, stdHi)
	}
	// STATUS is checked before INBUF_LO on purpose:
	// reading INBUF_LO clears the channel RDST bit.
	if got := r32(s2, offStatus); got != statusBefore {
		t.Fatalf("restored STATUS = %#x, want %#x", got, statusBefore)
	}
	if got := r32(s2, chanOff(0, 0x08)); got != stdLo {
		t.Fatalf("restored INBUF_LO = %#x, want %#x", got, stdLo)
	}
	if got := r32(s2, offPoll); got != pollBefore {
		t.Fatalf("restored POLL = %#x, want %#x", got, pollBefore)
	}
	if got := r32(s2, offComCSR); got != comBefore {
		t.Fatalf("restored COMCSR = %#x, want %#x", got, comBefore)
	}
	if got := getIO(s2, 128); !bytes.Equal(got, ioBefore) {
		t.Fatal("restored IOBUF differs")
	}
	if got := s2.Motor(0); got != 1 {
		t.Fatalf("restored Motor(0) = %d, want 1", got)
	}
	if got := s2.Motor(9); got != 0 {
		t.Fatalf("Motor(9) = %d, want 0 for an out of range channel", got)
	}
	if err := s2.Deserialize([]byte{9}); err == nil {
		t.Fatal("Deserialize of a short state should fail")
	}
}
