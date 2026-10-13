package sound

import (
	"encoding/binary"
	"testing"

	"github.com/davidpatton/itak-gaming/cores/go-genesis/chips/psg"
)

// setTone writes an audible channel 0 tone to the PSG: period 0xEE (238,
// about 470 Hz) at maximum volume.
func setTone(s *Sound) {
	s.WritePSG(0x8E) // latch: channel 0, tone, low 4 bits of period = 0xE
	s.WritePSG(0x0E) // data: high 6 bits of period = 0x0E
	s.WritePSG(0x90) // latch: channel 0, volume, value 0 = loudest
}

func countNonzero(out []int16) int {
	n := 0
	for _, v := range out {
		if v != 0 {
			n++
		}
	}
	return n
}

func TestWriteYMLatchAndData(t *testing.T) {
	s := New()

	s.WriteYM(0, 0x2A) // bank A latch: DAC data register
	s.WriteYM(1, 0x55) // bank A data
	if s.ymAddrA != 0x2A {
		t.Fatalf("ymAddrA = %#02x, want 0x2a", s.ymAddrA)
	}
	if got := s.ymRegsA[0x2A]; got != 0x55 {
		t.Fatalf("ymRegsA[0x2a] = %#02x, want 0x55", got)
	}

	// A second data write without re-latching hits the same register.
	s.WriteYM(1, 0x66)
	if got := s.ymRegsA[0x2A]; got != 0x66 {
		t.Fatalf("ymRegsA[0x2a] = %#02x after second data write, want 0x66", got)
	}

	s.WriteYM(2, 0x10) // bank B latch
	s.WriteYM(3, 0xAB) // bank B data
	if s.ymAddrB != 0x10 {
		t.Fatalf("ymAddrB = %#02x, want 0x10", s.ymAddrB)
	}
	if got := s.ymRegsB[0x10]; got != 0xAB {
		t.Fatalf("ymRegsB[0x10] = %#02x, want 0xab", got)
	}
	if got := s.ymRegsA[0x10]; got != 0 {
		t.Fatalf("bank B write leaked into bank A: ymRegsA[0x10] = %#02x", got)
	}
	if got := s.ymRegsB[0x2A]; got != 0 {
		t.Fatalf("bank A write leaked into bank B: ymRegsB[0x2a] = %#02x", got)
	}
}

func TestWriteYMTimerRegistersStored(t *testing.T) {
	s := New()
	s.WriteYM(0, 0x24)
	s.WriteYM(1, 0xF0) // timer A period high 8 bits
	s.WriteYM(0, 0x25)
	s.WriteYM(1, 0x03) // timer A period low 2 bits in top bits
	s.WriteYM(0, 0x26)
	s.WriteYM(1, 0x7E) // timer B period
	if s.ymRegsA[0x24] != 0xF0 || s.ymRegsA[0x25] != 0x03 || s.ymRegsA[0x26] != 0x7E {
		t.Fatalf("timer regs = %02x %02x %02x, want f0 03 7e",
			s.ymRegsA[0x24], s.ymRegsA[0x25], s.ymRegsA[0x26])
	}
}

func TestWriteYMReg27ClearsTimerFlags(t *testing.T) {
	s := New()
	s.ymStatus = 0x03 // pretend both timer flags are raised

	s.WriteYM(0, 0x27)
	s.WriteYM(1, 0x10) // reset timer A flag only
	if s.ymStatus != 0x02 {
		t.Fatalf("ymStatus = %#02x after 0x27 reset A, want 0x02", s.ymStatus)
	}
	if got := s.ReadYM(); got != 0x02 {
		t.Fatalf("ReadYM = %#02x, want 0x02", got)
	}

	s.WriteYM(1, 0x20) // reset timer B flag (address still latched to 0x27)
	if s.ymStatus != 0x00 {
		t.Fatalf("ymStatus = %#02x after 0x27 reset B, want 0x00", s.ymStatus)
	}
	if got := s.ymRegsA[0x27]; got != 0x20 {
		t.Fatalf("ymRegsA[0x27] = %#02x, want the raw value 0x20 stored", got)
	}
}

func TestReadYMBusyClear(t *testing.T) {
	s := New()
	st := s.ReadYM()
	if st&0x80 != 0 {
		t.Fatalf("busy bit set on fresh chip: status %#02x", st)
	}
	if st != 0 {
		t.Fatalf("fresh status = %#02x, want 0 (no timer flags)", st)
	}
}

func TestProducePSGTone(t *testing.T) {
	s := New()
	setTone(s)

	out := s.Produce(735)
	if len(out) != 1470 {
		t.Fatalf("len(Produce(735)) = %d, want 1470", len(out))
	}
	if n := countNonzero(out); n == 0 {
		t.Fatal("Produce returned all silence with an audible tone set")
	}
	for i := 0; i < len(out); i += 2 {
		if out[i] != out[i+1] {
			t.Fatalf("stereo pair %d differs: %d != %d", i/2, out[i], out[i+1])
		}
	}
}

func TestProduceSilentByDefault(t *testing.T) {
	s := New()
	out := s.Produce(735)
	if len(out) != 1470 {
		t.Fatalf("len(Produce(735)) = %d, want 1470", len(out))
	}
	if n := countNonzero(out); n != 0 {
		t.Fatalf("fresh chip produced %d nonzero samples, want silence", n)
	}
}

func TestSerializeSizeAndRoundTrip(t *testing.T) {
	s := New()
	s.WriteYM(0, 0x24)
	s.WriteYM(1, 0xF0)
	s.WriteYM(2, 0x30)
	s.WriteYM(3, 0x71)
	setTone(s)
	s.Produce(100) // run the PSG so its state is mid-stream

	if got := s.SerializeSize(); got != 1928 {
		t.Fatalf("SerializeSize() = %d, want 1928", got)
	}
	buf := make([]byte, s.SerializeSize())
	if err := s.Serialize(buf); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if buf[0] != s.ymAddrA || buf[1] != s.ymAddrB || buf[2] != s.ymStatus {
		t.Fatalf("header bytes = %02x %02x %02x, want %02x %02x %02x",
			buf[0], buf[1], buf[2], s.ymAddrA, s.ymAddrB, s.ymStatus)
	}
	if got := binary.LittleEndian.Uint32(buf[516:520]); got != psg.SerializeSize {
		t.Fatalf("psgLen = %d, want %d (psg.SerializeSize)", got, psg.SerializeSize)
	}

	s2 := New()
	if err := s2.Deserialize(buf); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if s2.ymRegsA != s.ymRegsA {
		t.Fatal("ymRegsA did not survive the round trip")
	}
	if s2.ymRegsB != s.ymRegsB {
		t.Fatal("ymRegsB did not survive the round trip")
	}
	if s2.ymAddrA != s.ymAddrA || s2.ymAddrB != s.ymAddrB || s2.ymStatus != s.ymStatus {
		t.Fatalf("latches/status = %02x %02x %02x, want %02x %02x %02x",
			s2.ymAddrA, s2.ymAddrB, s2.ymStatus, s.ymAddrA, s.ymAddrB, s.ymStatus)
	}

	// The restored PSG still has its tone: Produce works and is audible.
	out := s2.Produce(735)
	if len(out) != 1470 {
		t.Fatalf("len(Produce(735)) after Deserialize = %d, want 1470", len(out))
	}
	if n := countNonzero(out); n == 0 {
		t.Fatal("PSG tone lost across the serialize round trip")
	}
}

func TestSerializeBuffersTooSmall(t *testing.T) {
	s := New()
	if err := s.Serialize(make([]byte, serializeSize-1)); err == nil {
		t.Fatal("Serialize accepted a short buffer")
	}
	if err := s.Deserialize(make([]byte, serializeSize-1)); err == nil {
		t.Fatal("Deserialize accepted a short buffer")
	}
}
