package gte

import (
	"errors"
	"testing"
)

func TestRegisterStorage(t *testing.T) {
	g := New()
	g.SetData(3, 0xDEADBEEF)
	g.SetData(63, 0x12345678) // wraps to register 31
	g.SetControl(31, 0x0BADF00D)
	if g.Data(3) != 0xDEADBEEF {
		t.Errorf("Data(3) = 0x%08X, want 0xDEADBEEF", g.Data(3))
	}
	if g.Data(31) != 0x12345678 {
		t.Errorf("Data(31) = 0x%08X, want 0x12345678", g.Data(31))
	}
	if g.Control(31) != 0x0BADF00D {
		t.Errorf("Control(31) = 0x%08X, want 0x0BADF00D", g.Control(31))
	}
}

func TestCommandsRefusedNotFaked(t *testing.T) {
	g := New()
	g.SetData(0, 0x11111111)
	err := g.Execute(0x01) // RTPS
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Execute(RTPS) error = %v, want ErrNotImplemented", err)
	}
	if g.Data(0) != 0x11111111 {
		t.Errorf("refused command changed a register: Data(0) = 0x%08X", g.Data(0))
	}
	if g.Unsupported() != 1 {
		t.Errorf("Unsupported() = %d, want 1", g.Unsupported())
	}
	if err := g.Execute(0x3B); !errors.Is(err, ErrUnknownCommand) {
		t.Errorf("Execute(0x3B) error = %v, want ErrUnknownCommand", err)
	}
	name, ok := CommandName(0x01)
	if !ok || name != "RTPS" {
		t.Errorf("CommandName(0x01) = %q, %v; want RTPS, true", name, ok)
	}
	if _, ok := CommandName(0x3B); ok {
		t.Errorf("CommandName(0x3B) reported a name for an undocumented opcode")
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	g := New()
	g.SetData(7, 0xFEEDFACE)
	g.SetControl(2, 0x0D15EA5E)
	state := g.Serialize()
	if len(state) != StateSize {
		t.Fatalf("state size = %d, want %d", len(state), StateSize)
	}
	g2 := New()
	if err := g2.Deserialize(state); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if g2.Data(7) != 0xFEEDFACE || g2.Control(2) != 0x0D15EA5E {
		t.Errorf("round trip mismatch: data=0x%08X control=0x%08X", g2.Data(7), g2.Control(2))
	}
	if err := g2.Deserialize(state[:8]); err == nil {
		t.Errorf("short state accepted, want error")
	}
}
