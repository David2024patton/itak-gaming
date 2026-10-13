package spu

import "testing"

func TestTransferPortSequential(t *testing.T) {
	s := New()
	s.SetAddress(0x0010) // byte address 0x40
	s.WriteData(0xBEEF)
	s.WriteData(0xCAFE)
	if s.Address() != 0x44 {
		t.Fatalf("address = 0x%X, want 0x44", s.Address())
	}
	s.SetAddress(0x0010)
	if v := s.ReadData(); v != 0xBEEF {
		t.Errorf("read = 0x%04X, want 0xBEEF", v)
	}
	if v := s.ReadData(); v != 0xCAFE {
		t.Errorf("read = 0x%04X, want 0xCAFE", v)
	}
	// The bytes landed in RAM at the register-derived
	// address.
	if s.RAM[0x40] != 0xEF || s.RAM[0x41] != 0xBE {
		t.Errorf("RAM bytes = %02X %02X, want EF BE", s.RAM[0x40], s.RAM[0x41])
	}
}

func TestWordAccess(t *testing.T) {
	s := New()
	s.SetAddress(0)
	s.WriteWord(0xDEADBEEF)
	s.SetAddress(0)
	if v := s.ReadWord(); v != 0xDEADBEEF {
		t.Errorf("word = 0x%08X, want 0xDEADBEEF", v)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	s := New()
	s.SetAddress(0x0100)
	s.WriteWord(0x11223344)
	b := s.Serialize()
	if len(b) != StateSize {
		t.Fatalf("state = %d, want %d", len(b), StateSize)
	}
	s2 := New()
	s2.Deserialize(b)
	if s2.RAM != s.RAM || s2.Address() != s.Address() || s2.AddrReg != s.AddrReg {
		t.Errorf("SPU state diverged after round trip")
	}
}
