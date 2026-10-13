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

func writeReg(c *Controller, off uint32, v uint8) { c.WriteReg(off, v) }

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
	if c.DataLen() != 2048 {
		t.Fatalf("data FIFO = %d bytes, want 2048", c.DataLen())
	}
	// Sector 0 is filled with 0x01.
	w, ok := c.ReadWord()
	if !ok || w != 0x01010101 {
		t.Fatalf("first DMA word = 0x%08X ok=%v, want 0x01010101", w, ok)
	}
	// Drain the remaining 511 words of sector 0; the
	// stream must queue sector 1 (all 0x02)
	// automatically when the FIFO empties.
	for i := 0; i < 511; i++ {
		if _, ok := c.ReadWord(); !ok {
			t.Fatalf("word %d of sector 0 unavailable", i+1)
		}
	}
	if !c.Streaming() {
		t.Fatalf("stream stopped after first sector")
	}
	if c.DataLen() != 2048 {
		t.Fatalf("data FIFO = %d after auto-queue, want 2048", c.DataLen())
	}
	w, ok = c.ReadWord()
	if !ok {
		t.Fatalf("no word available after auto-queue")
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
	writeReg(c, 1, 0x1B) // ReadS also streams in this subset
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
