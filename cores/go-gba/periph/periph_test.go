package periph

import "testing"

// mockMem is a flat Memory for DMA tests: a 256 KB EWRAM
// region at 0x02000000 and a 32 KB IWRAM region at
// 0x03000000, both indexed by masking the address into
// the region. Accesses behave like the bus: wide accesses
// align down and move the unit little endian.
type mockMem struct {
	ew [0x40000]byte
	iw [0x8000]byte
}

func (m *mockMem) byteAt(addr uint32) *byte {
	switch addr >> 24 {
	case 0x02:
		return &m.ew[addr&0x3FFFF]
	case 0x03:
		return &m.iw[addr&0x7FFF]
	}
	return nil
}

func (m *mockMem) Read(addr uint32, size int) uint32 {
	if size == 2 {
		addr &^= 1
	} else if size == 4 {
		addr &^= 3
	}
	var v uint32
	for i := 0; i < size; i++ {
		if p := m.byteAt(addr + uint32(i)); p != nil {
			v |= uint32(*p) << (8 * i)
		}
	}
	return v
}

func (m *mockMem) Write(addr uint32, size int, val uint32) {
	if size == 2 {
		addr &^= 1
	} else if size == 4 {
		addr &^= 3
	}
	for i := 0; i < size; i++ {
		if p := m.byteAt(addr + uint32(i)); p != nil {
			*p = byte(val >> (8 * i))
		}
	}
}

// fifoSink records FIFO samples in delivery order.
type fifoSample struct {
	channel int
	value   uint32
}

type fifoSink struct {
	samples []fifoSample
}

func (s *fifoSink) FIFOSample(channel int, value uint32) {
	s.samples = append(s.samples, fifoSample{channel, value})
}

// DMA register offsets for channel ch.
func dmaSAD(ch int) uint32  { return uint32(0xB0 + 0xC*ch) }
func dmaDAD(ch int) uint32  { return uint32(0xB4 + 0xC*ch) }
func dmaCntL(ch int) uint32 { return uint32(0xB8 + 0xC*ch) }
func dmaCntH(ch int) uint32 { return uint32(0xBA + 0xC*ch) }

// armDMA programs a channel's registers, control last so
// the enable write sees the full configuration.
func armDMA(d *DMA, ch int, sad, dad uint32, count, cntH uint16) {
	d.WriteReg(dmaSAD(ch), 4, sad)
	d.WriteReg(dmaDAD(ch), 4, dad)
	d.WriteReg(dmaCntL(ch), 2, uint32(count))
	d.WriteReg(dmaCntH(ch), 2, uint32(cntH))
}

const (
	cntHEnable = 1 << 15
	cntHIRQ    = 1 << 14
	cntH32     = 1 << 10
	cntHRepeat = 1 << 9
)

func TestIRQ(t *testing.T) {
	t.Run("RaisePendingAssertedGating", func(t *testing.T) {
		q := NewIRQ()
		q.Raise(IRQVBlank)
		if got := q.Pending(); got != 0 {
			t.Fatalf("Pending with IE clear = %#x, want 0", got)
		}
		if q.Asserted() {
			t.Fatal("Asserted with IE clear, want false")
		}
		q.WriteReg(0x200, 2, IRQVBlank)
		if got := q.Pending(); got != IRQVBlank {
			t.Fatalf("Pending = %#x, want %#x", got, IRQVBlank)
		}
		if q.Asserted() {
			t.Fatal("Asserted with IME clear, want false")
		}
		q.WriteReg(0x208, 2, 1)
		if !q.Asserted() {
			t.Fatal("Asserted with IME set and pending bit, want true")
		}
	})

	t.Run("IFAcknowledgeClearsOnlyWrittenBits", func(t *testing.T) {
		q := NewIRQ()
		q.Raise(IRQVBlank | IRQHBlank | IRQTimer0)
		q.WriteReg(0x202, 2, IRQVBlank|IRQTimer0)
		if got := q.ReadReg(0x202, 2); got != IRQHBlank {
			t.Fatalf("IF after ack = %#x, want %#x", got, IRQHBlank)
		}
		// A zero write changes nothing; a byte write with
		// all ones clears the whole low byte.
		q.WriteReg(0x202, 2, 0)
		if got := q.ReadReg(0x202, 2); got != IRQHBlank {
			t.Fatalf("IF after zero ack = %#x, want %#x", got, IRQHBlank)
		}
		q.WriteReg(0x202, 1, 0xFF)
		if got := q.ReadReg(0x202, 2); got != 0 {
			t.Fatalf("IF after byte ack = %#x, want 0", got)
		}
	})

	t.Run("IEMaskingAndComposedAccess", func(t *testing.T) {
		q := NewIRQ()
		q.WriteReg(0x200, 2, 0xFFFF)
		if got := q.ReadReg(0x200, 2); got != 0x3FFF {
			t.Fatalf("IE = %#x, want 0x3FFF", got)
		}
		q.WriteReg(0x200, 2, 0x0003)
		q.Raise(0x0005)
		if got := q.ReadReg(0x200, 4); got != 0x00050003 {
			t.Fatalf("IE/IF word = %#x, want 0x00050003", got)
		}
		// IME keeps bit 0 only and reads back 0 or 1.
		q.WriteReg(0x208, 4, 0xFFFFFFFE)
		if got := q.ReadReg(0x208, 4); got != 0 {
			t.Fatalf("IME = %#x, want 0", got)
		}
		q.WriteReg(0x208, 4, 0xFFFFFFFF)
		if got := q.ReadReg(0x208, 4); got != 1 {
			t.Fatalf("IME = %#x, want 1", got)
		}
		if got := q.ReadReg(0x204, 2); got != 0 {
			t.Fatalf("unowned offset read = %#x, want 0", got)
		}
	})

	t.Run("SerializeRoundTrip", func(t *testing.T) {
		q := NewIRQ()
		q.WriteReg(0x200, 2, 0x1555)
		q.Raise(IRQTimer2 | IRQKeypad)
		q.WriteReg(0x208, 2, 1)
		buf := make([]byte, IRQStateSize)
		if n := q.Serialize(buf); n != IRQStateSize {
			t.Fatalf("Serialize wrote %d, want %d", n, IRQStateSize)
		}
		r := NewIRQ()
		if !r.Deserialize(buf) {
			t.Fatal("Deserialize returned false")
		}
		if r.ReadReg(0x200, 2) != 0x1555 ||
			r.ReadReg(0x202, 2) != q.ReadReg(0x202, 2) ||
			r.ReadReg(0x208, 2) != 1 {
			t.Fatal("round trip state mismatch")
		}
		if r.Deserialize(buf[:IRQStateSize-1]) {
			t.Fatal("Deserialize of short input returned true")
		}
	})
}

func TestTimers(t *testing.T) {
	t.Run("Prescaler64", func(t *testing.T) {
		tm := NewTimers()
		irq := NewIRQ()
		tm.WriteReg(0x100, 2, 0xFFF0)    // CNT_L while stopped
		tm.WriteReg(0x102, 2, 0x80|0x01) // enable, prescaler 64
		tm.Advance(64*5, irq)
		if got := tm.Counter(0); got != 0xFFF5 {
			t.Fatalf("counter = %#x, want 0xFFF5", got)
		}
		if got := irq.ReadReg(0x202, 2); got&IRQTimer0 != 0 {
			t.Fatalf("IF = %#x, timer0 bit set without overflow", got)
		}
	})

	t.Run("OverflowReloadsAndRaisesIRQ", func(t *testing.T) {
		tm := NewTimers()
		irq := NewIRQ()
		tm.WriteReg(0x100, 2, 0xFFFE)
		tm.WriteReg(0x102, 2, 0x80|0x40) // enable, IRQ on, period 1
		tm.Advance(2, irq)
		if got := tm.Counter(0); got != 0xFFFE {
			t.Fatalf("counter = %#x, want 0xFFFE (reloaded)", got)
		}
		if got := irq.ReadReg(0x202, 2); got&IRQTimer0 == 0 {
			t.Fatalf("IF = %#x, want timer0 bit set", got)
		}
	})

	t.Run("Cascade", func(t *testing.T) {
		tm := NewTimers()
		irq := NewIRQ()
		tm.WriteReg(0x100, 2, 0xFFFF)    // ch0 reload
		tm.WriteReg(0x102, 2, 0x80)      // ch0 enable, period 1
		tm.WriteReg(0x104, 2, 0x1000)    // ch1 reload
		tm.WriteReg(0x106, 2, 0x80|0x04) // ch1 enable, cascade
		tm.Advance(3, irq)
		if got := tm.Counter(1); got != 0x1003 {
			t.Fatalf("ch1 counter = %#x, want 0x1003", got)
		}
		if got := tm.Counter(0); got != 0xFFFF {
			t.Fatalf("ch0 counter = %#x, want 0xFFFF (reloaded)", got)
		}
	})

	t.Run("StoppedTimerAndCNTLPrime", func(t *testing.T) {
		tm := NewTimers()
		irq := NewIRQ()
		tm.WriteReg(0x100, 2, 0x1234)
		if got := tm.Counter(0); got != 0x1234 {
			t.Fatalf("counter after CNT_L write = %#x, want 0x1234", got)
		}
		tm.Advance(100000, irq)
		if got := tm.Counter(0); got != 0x1234 {
			t.Fatalf("stopped counter = %#x, want 0x1234", got)
		}
	})

	t.Run("SerializeRoundTripKeepsDivider", func(t *testing.T) {
		tm := NewTimers()
		irq := NewIRQ()
		tm.WriteReg(0x100, 2, 0)
		tm.WriteReg(0x102, 2, 0x80|0x01) // prescaler 64
		tm.Advance(32, irq)              // half a period banked
		buf := make([]byte, TimersStateSize)
		if n := tm.Serialize(buf); n != TimersStateSize {
			t.Fatalf("Serialize wrote %d, want %d", n, TimersStateSize)
		}
		r := NewTimers()
		if !r.Deserialize(buf) {
			t.Fatal("Deserialize returned false")
		}
		r.Advance(32, irq) // completes the banked period
		if got := r.Counter(0); got != 1 {
			t.Fatalf("counter = %d, want 1 (divider survived)", got)
		}
		if r.Deserialize(buf[:TimersStateSize-1]) {
			t.Fatal("Deserialize of short input returned true")
		}
	})
}

func TestDMA(t *testing.T) {
	t.Run("Immediate32BitCopy", func(t *testing.T) {
		mem := &mockMem{}
		irq := NewIRQ()
		d := NewDMA(mem, irq, &fifoSink{})
		want := []uint32{0x11223344, 0x55667788, 0x99AABBCC, 0xDDEEFF00}
		for i, v := range want {
			mem.Write(0x02000000+uint32(4*i), 4, v)
		}
		armDMA(d, 0, 0x02000000, 0x03000000, 4, cntHEnable|cntH32)
		for i, w := range want {
			if got := mem.Read(0x03000000+uint32(4*i), 4); got != w {
				t.Fatalf("dest word %d = %#x, want %#x", i, got, w)
			}
		}
		if got := d.ReadReg(dmaCntH(0), 2); got&cntHEnable != 0 {
			t.Fatal("enable still set after one shot transfer")
		}
		// Registers read back as written.
		if got := d.ReadReg(dmaSAD(0), 4); got != 0x02000000 {
			t.Fatalf("SAD readback = %#x, want 0x02000000", got)
		}
		if got := d.ReadReg(dmaCntL(0), 2); got != 4 {
			t.Fatalf("CNT_L readback = %d, want 4", got)
		}
	})

	t.Run("FixedSourceFill", func(t *testing.T) {
		mem := &mockMem{}
		d := NewDMA(mem, NewIRQ(), &fifoSink{})
		mem.Write(0x02000100, 4, 0xCAFEBABE)
		// Source adjust 2 (fixed), destination increment.
		armDMA(d, 1, 0x02000100, 0x03000000, 3, cntHEnable|cntH32|(2<<7))
		for i := 0; i < 3; i++ {
			if got := mem.Read(0x03000000+uint32(4*i), 4); got != 0xCAFEBABE {
				t.Fatalf("dest word %d = %#x, want 0xCAFEBABE", i, got)
			}
		}
	})

	t.Run("DecrementDest", func(t *testing.T) {
		mem := &mockMem{}
		d := NewDMA(mem, NewIRQ(), &fifoSink{})
		vals := []uint32{0xAAAA0001, 0xBBBB0002, 0xCCCC0003}
		for i, v := range vals {
			mem.Write(0x02000000+uint32(4*i), 4, v)
		}
		// Destination adjust 1 (decrement) from the top slot.
		armDMA(d, 2, 0x02000000, 0x03000008, 3, cntHEnable|cntH32|(1<<5))
		for i, w := range []uint32{vals[2], vals[1], vals[0]} {
			if got := mem.Read(0x03000000+uint32(4*i), 4); got != w {
				t.Fatalf("dest word %d = %#x, want %#x", i, got, w)
			}
		}
	})

	t.Run("HalfwordUnits", func(t *testing.T) {
		mem := &mockMem{}
		d := NewDMA(mem, NewIRQ(), &fifoSink{})
		mem.Write(0x02000000, 2, 0x1234)
		mem.Write(0x02000002, 2, 0x5678)
		armDMA(d, 0, 0x02000000, 0x03000000, 2, cntHEnable) // 16 bit units
		if got := mem.Read(0x03000000, 4); got != 0x56781234 {
			t.Fatalf("dest = %#x, want 0x56781234", got)
		}
	})

	t.Run("RepeatHBlankDestReload", func(t *testing.T) {
		mem := &mockMem{}
		d := NewDMA(mem, NewIRQ(), &fifoSink{})
		words := []uint32{0x11110000, 0x22220000, 0x33330000, 0x44440000}
		for i, v := range words {
			mem.Write(0x02000000+uint32(4*i), 4, v)
		}
		cntH := cntHEnable | cntHRepeat | cntH32 | (2 << 12) | (3 << 5)
		armDMA(d, 2, 0x02000000, 0x03000000, 2, uint16(cntH))
		d.OnHBlank()
		if got := mem.Read(0x03000000, 4); got != words[0] {
			t.Fatalf("first burst dest[0] = %#x, want %#x", got, words[0])
		}
		if got := mem.Read(0x03000004, 4); got != words[1] {
			t.Fatalf("first burst dest[1] = %#x, want %#x", got, words[1])
		}
		mem.Write(0x03000000, 4, 0)
		mem.Write(0x03000004, 4, 0)
		d.OnHBlank()
		// Destination latch reloaded: the second burst
		// lands at the start again, carrying the next
		// source words.
		if got := mem.Read(0x03000000, 4); got != words[2] {
			t.Fatalf("second burst dest[0] = %#x, want %#x", got, words[2])
		}
		if got := mem.Read(0x03000004, 4); got != words[3] {
			t.Fatalf("second burst dest[1] = %#x, want %#x", got, words[3])
		}
		if got := d.ReadReg(dmaCntH(2), 2); got&cntHEnable == 0 {
			t.Fatal("repeat channel lost its enable bit")
		}
	})

	t.Run("VBlankTimingWaitsForTrigger", func(t *testing.T) {
		mem := &mockMem{}
		d := NewDMA(mem, NewIRQ(), &fifoSink{})
		mem.Write(0x02000000, 4, 0xFEEDFACE)
		armDMA(d, 3, 0x02000000, 0x03000000, 1, cntHEnable|cntH32|(1<<12))
		if got := mem.Read(0x03000000, 4); got != 0 {
			t.Fatalf("dest before trigger = %#x, want 0", got)
		}
		d.OnHBlank() // wrong trigger: must not run
		if got := mem.Read(0x03000000, 4); got != 0 {
			t.Fatalf("dest after HBlank = %#x, want 0", got)
		}
		d.OnVBlank()
		if got := mem.Read(0x03000000, 4); got != 0xFEEDFACE {
			t.Fatalf("dest after VBlank = %#x, want 0xFEEDFACE", got)
		}
	})

	t.Run("FIFOChannel1", func(t *testing.T) {
		mem := &mockMem{}
		sink := &fifoSink{}
		d := NewDMA(mem, NewIRQ(), sink)
		want := []uint32{0x50000001, 0x50000002, 0x50000003, 0x50000004}
		for i, v := range want {
			mem.Write(0x02000000+uint32(4*i), 4, v)
		}
		armDMA(d, 1, 0x02000000, 0x040000A0, 0, cntHEnable|cntH32|(3<<12))
		d.OnFIFO(1)
		if len(sink.samples) != 4 {
			t.Fatalf("FIFO delivered %d samples, want 4", len(sink.samples))
		}
		for i, s := range sink.samples {
			if s.channel != 1 || s.value != want[i] {
				t.Fatalf("sample %d = (%d, %#x), want (1, %#x)", i, s.channel, s.value, want[i])
			}
		}
		// A disabled channel and the non FIFO channels
		// deliver nothing.
		d.OnFIFO(2)
		armDMA(d, 0, 0x02000000, 0x040000A0, 0, cntHEnable|cntH32|(3<<12))
		d.OnFIFO(0)
		if len(sink.samples) != 4 {
			t.Fatalf("FIFO delivered %d samples after dead triggers, want 4", len(sink.samples))
		}
	})

	t.Run("IRQOnCompletion", func(t *testing.T) {
		mem := &mockMem{}
		irq := NewIRQ()
		d := NewDMA(mem, irq, &fifoSink{})
		mem.Write(0x02000000, 4, 0x12345678)
		armDMA(d, 0, 0x02000000, 0x03000000, 1, cntHEnable|cntH32|cntHIRQ)
		if got := irq.ReadReg(0x202, 2); got&IRQDMA0 == 0 {
			t.Fatalf("IF = %#x, want DMA0 bit set", got)
		}
	})

	t.Run("SerializeRoundTrip", func(t *testing.T) {
		mem := &mockMem{}
		irq := NewIRQ()
		d := NewDMA(mem, irq, &fifoSink{})
		mem.Write(0x02000000, 4, 0x0BADF00D)
		armDMA(d, 1, 0x02000000, 0x03000000, 1, cntHEnable|cntH32|(1<<12))
		buf := make([]byte, DMAStateSize)
		if n := d.Serialize(buf); n != DMAStateSize {
			t.Fatalf("Serialize wrote %d, want %d", n, DMAStateSize)
		}
		r := NewDMA(mem, irq, &fifoSink{})
		if !r.Deserialize(buf) {
			t.Fatal("Deserialize returned false")
		}
		r.OnVBlank()
		if got := mem.Read(0x03000000, 4); got != 0x0BADF00D {
			t.Fatalf("dest after restored trigger = %#x, want 0x0BADF00D", got)
		}
		if r.Deserialize(buf[:DMAStateSize-1]) {
			t.Fatal("Deserialize of short input returned true")
		}
	})
}

func TestKeypad(t *testing.T) {
	t.Run("ActiveLowInput", func(t *testing.T) {
		k := NewKeypad()
		if got := k.ReadReg(0x130, 2); got != 0x3FF {
			t.Fatalf("KEYINPUT idle = %#x, want 0x3FF", got)
		}
		k.SetButtons(1<<0|1<<9, NewIRQ()) // A and L
		want := uint32(0x3FF) &^ (1<<0 | 1<<9)
		if got := k.ReadReg(0x130, 2); got != want {
			t.Fatalf("KEYINPUT = %#x, want %#x", got, want)
		}
		if got := k.Buttons(); got != 1<<0|1<<9 {
			t.Fatalf("Buttons = %#x, want %#x", got, 1<<0|1<<9)
		}
		// KEYINPUT ignores writes.
		k.WriteReg(0x130, 2, 0)
		if got := k.ReadReg(0x130, 2); got != want {
			t.Fatalf("KEYINPUT after write = %#x, want %#x", got, want)
		}
	})

	t.Run("ORConditionRaises", func(t *testing.T) {
		k := NewKeypad()
		irq := NewIRQ()
		k.WriteReg(0x132, 2, (1<<0)|(1<<14)) // select A, IRQ on, OR
		k.SetButtons(0, irq)
		if got := irq.ReadReg(0x202, 2); got&IRQKeypad != 0 {
			t.Fatal("keypad IRQ raised with nothing pressed")
		}
		k.SetButtons(1<<0, irq)
		if got := irq.ReadReg(0x202, 2); got&IRQKeypad == 0 {
			t.Fatal("keypad IRQ not raised on selected press")
		}
		// Holding the key does not raise twice: acknowledge
		// and re-evaluate, the flag must stay clear.
		irq.WriteReg(0x202, 2, IRQKeypad)
		k.SetButtons(1<<0, irq)
		if got := irq.ReadReg(0x202, 2); got&IRQKeypad != 0 {
			t.Fatal("keypad IRQ re-raised while key held")
		}
	})

	t.Run("ANDConditionRequiresAll", func(t *testing.T) {
		k := NewKeypad()
		irq := NewIRQ()
		k.WriteReg(0x132, 2, (1<<0)|(1<<1)|(1<<14)|(1<<15)) // A+B, IRQ on, AND
		k.SetButtons(1<<0, irq)
		if got := irq.ReadReg(0x202, 2); got&IRQKeypad != 0 {
			t.Fatal("keypad IRQ raised with only one of two AND keys")
		}
		k.SetButtons(1<<0|1<<1, irq)
		if got := irq.ReadReg(0x202, 2); got&IRQKeypad == 0 {
			t.Fatal("keypad IRQ not raised with both AND keys pressed")
		}
	})

	t.Run("NoRaiseWhenIRQDisabled", func(t *testing.T) {
		k := NewKeypad()
		irq := NewIRQ()
		k.WriteReg(0x132, 2, 1<<0) // select A, IRQ enable clear
		k.SetButtons(1<<0, irq)
		if got := irq.ReadReg(0x202, 2); got&IRQKeypad != 0 {
			t.Fatal("keypad IRQ raised with KEYCNT bit 14 clear")
		}
	})

	t.Run("SerializeRoundTrip", func(t *testing.T) {
		k := NewKeypad()
		irq := NewIRQ()
		k.WriteReg(0x132, 2, (1<<3)|(1<<14))
		k.SetButtons(1<<3|1<<4, irq) // raises, latching the edge state
		buf := make([]byte, KeypadStateSize)
		if n := k.Serialize(buf); n != KeypadStateSize {
			t.Fatalf("Serialize wrote %d, want %d", n, KeypadStateSize)
		}
		r := NewKeypad()
		if !r.Deserialize(buf) {
			t.Fatal("Deserialize returned false")
		}
		if r.Buttons() != k.Buttons() ||
			r.ReadReg(0x132, 2) != k.ReadReg(0x132, 2) ||
			r.ReadReg(0x130, 2) != k.ReadReg(0x130, 2) {
			t.Fatal("round trip state mismatch")
		}
		// The latched edge survives: the same held buttons
		// must not raise again on the restored keypad.
		irq2 := NewIRQ()
		r.SetButtons(1<<3|1<<4, irq2)
		if got := irq2.ReadReg(0x202, 2); got&IRQKeypad != 0 {
			t.Fatal("restored keypad re-raised for held buttons")
		}
		if r.Deserialize(buf[:KeypadStateSize-1]) {
			t.Fatal("Deserialize of short input returned true")
		}
	})
}
