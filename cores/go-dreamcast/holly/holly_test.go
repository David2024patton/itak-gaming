package holly

import (
	"bytes"
	"testing"
)

// Physically addressed register window base used by the tests.
const (
	sbISTNRM  = 0x005F6900
	sbISTEXT  = 0x005F6904
	sbISTERR  = 0x005F6908
	sbIML2NRM = 0x005F6910
	sbIML4NRM = 0x005F6920
	sbIML6NRM = 0x005F6930
	sbIML6ERR = 0x005F6938
	sbIML4EXT = 0x005F6924
)

// fakeSink records the request stream the controller presents.
type fakeSink struct {
	raises    [][2]uint32 // (code, level) per RaiseInterrupt call
	withdraws int
	latched   bool
	code      uint32
	level     int
}

func (s *fakeSink) RaiseInterrupt(code uint32, level int) {
	s.raises = append(s.raises, [2]uint32{code, uint32(level)})
	if !s.latched || level > s.level {
		s.latched = true
		s.code = code
		s.level = level
	}
}

func (s *fakeSink) WithdrawInterrupt() {
	s.withdraws++
	s.latched = false
	s.code = 0
	s.level = 0
}

func TestMaskGatesPresentation(t *testing.T) {
	sink := &fakeSink{}
	c := New(sink)
	c.Raise(BankNormal, 1<<EvtVBlankIn)
	if sink.latched {
		t.Fatal("unmasked source presented an interrupt")
	}
	// Assign the vblank-in source to level 6: it is presented as
	// IRL9, INTEVT 0x320.
	c.Write32(sbIML6NRM, 1<<EvtVBlankIn)
	if !sink.latched || sink.code != 0x320 || sink.level != 6 {
		t.Errorf("presented = (%#x, %d, %v), want (0x320, 6, true)", sink.code, sink.level, sink.latched)
	}
	if got := c.Level(); got != 6 {
		t.Errorf("Level = %d, want 6", got)
	}
}

func TestHighestLevelWinsAndRepresents(t *testing.T) {
	sink := &fakeSink{}
	c := New(sink)
	c.Write32(sbIML6NRM, 1<<EvtRenderDoneTSP)
	c.Write32(sbIML4NRM, 1<<EvtVBlankIn)
	c.Raise(BankNormal, 1<<EvtVBlankIn)
	if sink.level != 4 || sink.code != 0x360 {
		t.Fatalf("presented = (%#x, %d), want (0x360, 4)", sink.code, sink.level)
	}
	c.Raise(BankNormal, 1<<EvtRenderDoneTSP)
	if sink.level != 6 || sink.code != 0x320 {
		t.Fatalf("presented = (%#x, %d), want (0x320, 6)", sink.code, sink.level)
	}
	// Acknowledge the level 6 source: the level 4 source is
	// re-presented, not lost.
	c.Write32(sbISTNRM, 1<<EvtRenderDoneTSP)
	if !sink.latched || sink.level != 4 || sink.code != 0x360 {
		t.Errorf("after ack presented = (%#x, %d, %v), want (0x360, 4, true)", sink.code, sink.level, sink.latched)
	}
	// Acknowledge the last source: presentation withdraws.
	c.Write32(sbISTNRM, 1<<EvtVBlankIn)
	if sink.latched {
		t.Error("interrupt still presented after all sources acked")
	}
	if got := c.Read32(sbISTNRM); got != 0 {
		t.Errorf("ISTNRM = %#x, want 0", got)
	}
}

func TestWriteOneToClearIsSelective(t *testing.T) {
	sink := &fakeSink{}
	c := New(sink)
	c.Raise(BankNormal, (1<<EvtVBlankIn)|(1<<EvtHBlank))
	c.Write32(sbISTNRM, 1<<EvtVBlankIn)
	if got := c.Status(BankNormal); got != 1<<EvtHBlank {
		t.Errorf("status = %#x, want only hblank", got)
	}
	// ISTERR clears the same way.
	c.Raise(BankError, 1<<ErrTAInputOverflow)
	c.Write32(sbISTERR, 1<<ErrTAInputOverflow)
	if got := c.Status(BankError); got != 0 {
		t.Errorf("error status = %#x, want 0", got)
	}
}

func TestExternalBankAndSummaryBits(t *testing.T) {
	sink := &fakeSink{}
	c := New(sink)
	c.Raise(BankExternal, 0x40)
	v := c.Read32(sbISTNRM)
	if v&(1<<30) == 0 {
		t.Errorf("ISTNRM = %#x, want external summary bit 30 set", v)
	}
	if got := c.Read32(sbISTEXT); got != 0x40 {
		t.Errorf("ISTEXT = %#x, want 0x40", got)
	}
	// A register write must not clear external sources.
	c.Write32(sbISTEXT, 0x40)
	if got := c.Status(BankExternal); got != 0x40 {
		t.Errorf("external status after write = %#x, want 0x40", got)
	}
	// The device clears its own source.
	c.ClearExternal(0x40)
	if got := c.Read32(sbISTNRM); got&(1<<30) != 0 {
		t.Errorf("ISTNRM = %#x, summary bit still set", got)
	}
	// Error sources raise the bit 31 summary.
	c.Raise(BankError, 1<<ErrISPOutOfMemory)
	if got := c.Read32(sbISTNRM); got&(1<<31) == 0 {
		t.Errorf("ISTNRM = %#x, want error summary bit 31 set", got)
	}
	// Masked external and error sources present at their levels.
	c.Write32(sbIML4EXT, 0x80)
	c.Raise(BankExternal, 0x80)
	if sink.level != 4 || sink.code != 0x360 {
		t.Errorf("external presented = (%#x, %d), want (0x360, 4)", sink.code, sink.level)
	}
}

func TestMaskRowsArePerBank(t *testing.T) {
	sink := &fakeSink{}
	c := New(sink)
	// A mask word written for the error bank must not enable the
	// same bit in the normal bank.
	c.Write32(sbIML6ERR, 1<<EvtVBlankIn)
	c.Raise(BankNormal, 1<<EvtVBlankIn)
	if sink.latched {
		t.Error("normal source presented through the error bank mask")
	}
	c.Raise(BankError, 1<<EvtVBlankIn)
	if !sink.latched || sink.level != 6 {
		t.Errorf("error source presented level = %d, want 6", sink.level)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	sink := &fakeSink{}
	c := New(sink)
	c.Write32(sbIML6NRM, 1<<EvtRenderDoneTSP)
	c.Write32(sbIML2NRM, 1<<EvtHBlank)
	c.Raise(BankNormal, (1<<EvtRenderDoneTSP)|(1<<EvtHBlank))
	b := c.Serialize()
	if len(b) != SerializeSize {
		t.Fatalf("Serialize len = %d, want %d", len(b), SerializeSize)
	}
	sink2 := &fakeSink{}
	c2 := New(sink2)
	if !c2.Deserialize(b) {
		t.Fatal("Deserialize failed")
	}
	if got := c2.Serialize(); !bytes.Equal(got, b) {
		t.Error("round trip state differs")
	}
	// The restored controller re-presents its winner (level 6).
	if !sink2.latched || sink2.level != 6 || sink2.code != 0x320 {
		t.Errorf("restored presentation = (%#x, %d, %v)", sink2.code, sink2.level, sink2.latched)
	}
	if c2.Deserialize(b[:SerializeSize-1]) {
		t.Error("Deserialize accepted a truncated state")
	}
}
