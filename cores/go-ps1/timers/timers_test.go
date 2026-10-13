package timers

import "testing"

func sysTicks(n int) Inputs { return Inputs{SysCycles: n} }

func TestFreeRunCounterExact(t *testing.T) {
	tm := New()
	tm.WriteMode(2, 0x0000) // system clock, free run
	if got := tm.Advance(sysTicks(1000)); got != 0 {
		t.Fatalf("asserted = %b, want 0", got)
	}
	if tm.T[2].Counter != 1000 {
		t.Errorf("counter = %d, want 1000", tm.T[2].Counter)
	}
	tm.Advance(sysTicks(64536))
	if tm.T[2].Counter != 0 {
		t.Errorf("counter after wrap = %d, want 0", tm.T[2].Counter)
	}
	m := tm.ReadMode(2)
	if m&(1<<12) == 0 {
		t.Errorf("reachedFFFF not set after wrap: mode %04X", m)
	}
	if m2 := tm.ReadMode(2); m2&(1<<12) != 0 {
		t.Errorf("reachedFFFF not cleared by read: mode %04X", m2)
	}
}

func TestTargetResetPeriodExact(t *testing.T) {
	tm := New()
	// Timer 2: reset at target, IRQ at target, repeat, pulse.
	tm.WriteMode(2, 0x0008|0x0010|0x0040)
	tm.WriteTarget(2, 100)
	periods := 0
	for p := 0; p < 10; p++ {
		a := tm.Advance(sysTicks(100))
		if a != 1<<2 {
			t.Fatalf("period %d: asserted = %b, want timer2", p, a)
		}
		if tm.T[2].Counter != 0 {
			t.Fatalf("period %d: counter = %d, want 0 after reset", p, tm.T[2].Counter)
		}
		periods++
	}
	if periods != 10 {
		t.Fatalf("periods = %d, want 10", periods)
	}
	// A partial interval leaves the exact remainder.
	tm.Advance(sysTicks(37))
	if tm.T[2].Counter != 37 {
		t.Errorf("counter = %d, want 37", tm.T[2].Counter)
	}
	m := tm.ReadMode(2)
	if m&(1<<11) == 0 {
		t.Errorf("reachedTarget not set: mode %04X", m)
	}
	if m&(1<<10) == 0 {
		t.Errorf("INTF should read idle (1) in pulse mode after the interval: mode %04X", m)
	}
}

func TestOneShotSuppressesSecondIRQ(t *testing.T) {
	tm := New()
	tm.WriteMode(0, 0x0008|0x0010) // one-shot (bit 6 clear)
	tm.WriteTarget(0, 10)
	if a := tm.Advance(sysTicks(10)); a != 1 {
		t.Fatalf("first target: asserted = %b, want timer0", a)
	}
	if a := tm.Advance(sysTicks(10)); a != 0 {
		t.Errorf("second target in one-shot: asserted = %b, want 0", a)
	}
	// A mode write re-arms one-shot delivery.
	tm.WriteMode(0, 0x0008|0x0010)
	tm.WriteTarget(0, 10)
	if a := tm.Advance(sysTicks(10)); a != 1 {
		t.Errorf("after mode rewrite: asserted = %b, want timer0", a)
	}
}

func TestToggleModeAssertsEverySecondCondition(t *testing.T) {
	tm := New()
	tm.WriteMode(1, 0x0008|0x0010|0x0040|0x0080) // repeat + toggle
	tm.WriteTarget(1, 5)
	if a := tm.Advance(sysTicks(5)); a != 1<<1 {
		t.Fatalf("first toggle: asserted = %b, want timer1 (INTF 1 to 0)", a)
	}
	if a := tm.Advance(sysTicks(5)); a != 0 {
		t.Errorf("second toggle: asserted = %b, want 0 (INTF 0 to 1)", a)
	}
	if a := tm.Advance(sysTicks(5)); a != 1<<1 {
		t.Errorf("third toggle: asserted = %b, want timer1", a)
	}
}

func TestTimer2DivideBy8(t *testing.T) {
	tm := New()
	tm.WriteMode(2, 0x0200) // clock source 2 = system/8
	tm.Advance(sysTicks(7))
	if tm.T[2].Counter != 0 {
		t.Fatalf("counter = %d after 7 cycles, want 0", tm.T[2].Counter)
	}
	tm.Advance(sysTicks(1))
	if tm.T[2].Counter != 1 {
		t.Errorf("counter = %d after 8 cycles, want 1", tm.T[2].Counter)
	}
	tm.Advance(sysTicks(800))
	if tm.T[2].Counter != 101 {
		t.Errorf("counter = %d, want 101", tm.T[2].Counter)
	}
}

func TestTimer1HBlankClock(t *testing.T) {
	tm := New()
	tm.WriteMode(1, 0x0100) // clock source 1 = hblank
	tm.Advance(Inputs{HBlankPulse: 263})
	if tm.T[1].Counter != 263 {
		t.Errorf("counter = %d, want 263 (one frame of lines)", tm.T[1].Counter)
	}
	// System clock ticks must not leak into an
	// hblank-clocked timer.
	tm.Advance(Inputs{SysCycles: 5000})
	if tm.T[1].Counter != 263 {
		t.Errorf("counter = %d after sys ticks, want 263", tm.T[1].Counter)
	}
}

func TestTimer0DotClock(t *testing.T) {
	tm := New()
	tm.WriteMode(0, 0x0100) // clock source 1 = dotclock
	tm.Advance(Inputs{DotTicks: 341, SysCycles: 9999})
	if tm.T[0].Counter != 341 {
		t.Errorf("counter = %d, want 341 dot ticks", tm.T[0].Counter)
	}
}

func TestSyncPauseDuringHBlank(t *testing.T) {
	tm := New()
	tm.WriteMode(0, 0x0001) // sync enable, mode 0: pause in hblank
	tm.Advance(Inputs{SysCycles: 100, InHBlank: true})
	if tm.T[0].Counter != 0 {
		t.Errorf("counter = %d, want 0 (paused in hblank)", tm.T[0].Counter)
	}
	tm.Advance(Inputs{SysCycles: 100, InHBlank: false})
	if tm.T[0].Counter != 100 {
		t.Errorf("counter = %d, want 100", tm.T[0].Counter)
	}
}

func TestSyncResetAtHBlank(t *testing.T) {
	tm := New()
	tm.WriteMode(0, 0x0001|0x0002) // sync mode 1: reset at hblank
	tm.Advance(sysTicks(500))
	if tm.T[0].Counter != 500 {
		t.Fatalf("counter = %d, want 500", tm.T[0].Counter)
	}
	tm.Advance(Inputs{SysCycles: 10, HBlankPulse: 1})
	if tm.T[0].Counter != 10 {
		t.Errorf("counter = %d, want 10 (reset then counted)", tm.T[0].Counter)
	}
}

func TestSyncMode3WaitsForFirstPulse(t *testing.T) {
	tm := New()
	tm.WriteMode(1, 0x0001|0x0006) // sync mode 3 on vblank
	tm.Advance(sysTicks(100))
	if tm.T[1].Counter != 0 {
		t.Errorf("counter = %d, want 0 before first vblank", tm.T[1].Counter)
	}
	tm.Advance(Inputs{SysCycles: 50, VBlankPulse: 1})
	if tm.T[1].Counter != 50 {
		t.Errorf("counter = %d, want 50 after arming pulse", tm.T[1].Counter)
	}
}

func TestTimer2SyncStop(t *testing.T) {
	tm := New()
	tm.WriteMode(2, 0x0001) // sync enable, mode 0: stop forever
	tm.Advance(sysTicks(10000))
	if tm.T[2].Counter != 0 {
		t.Errorf("counter = %d, want 0 (stopped)", tm.T[2].Counter)
	}
	tm.WriteMode(2, 0x0001|0x0002) // mode 1: free run
	tm.Advance(sysTicks(25))
	if tm.T[2].Counter != 25 {
		t.Errorf("counter = %d, want 25 (free run)", tm.T[2].Counter)
	}
}

func TestModeWriteResetsCounter(t *testing.T) {
	tm := New()
	tm.Advance(sysTicks(777))
	if tm.T[0].Counter != 777 {
		t.Fatalf("counter = %d, want 777", tm.T[0].Counter)
	}
	tm.WriteMode(0, 0)
	if tm.T[0].Counter != 0 {
		t.Errorf("counter = %d after mode write, want 0 (documented reset)", tm.T[0].Counter)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	tm := New()
	tm.WriteMode(0, 0x0008|0x0010|0x0040)
	tm.WriteTarget(0, 55)
	tm.WriteMode(2, 0x0200)
	tm.Advance(Inputs{SysCycles: 1234, DotTicks: 42})
	s := tm.Serialize()
	if len(s) != StateSize() {
		t.Fatalf("state size = %d, want %d", len(s), StateSize())
	}
	tm2 := New()
	tm2.Deserialize(s)
	if tm2.T != tm.T {
		t.Errorf("timers diverged after round trip")
	}
	// Continuation must be identical.
	a := tm.Advance(sysTicks(100))
	b := tm2.Advance(sysTicks(100))
	if a != b || tm2.T != tm.T {
		t.Errorf("continuation diverged: asserted %b vs %b", a, b)
	}
}
