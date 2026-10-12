package netsession

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"
)

// --- fake transport pair (stands in for the relaying hub) ---

type fakeTransport struct {
	inbox  chan []byte
	peer   *fakeTransport
	closed chan struct{}
	once   sync.Once
}

func transportPair() (*fakeTransport, *fakeTransport) {
	a := &fakeTransport{inbox: make(chan []byte, 4096), closed: make(chan struct{})}
	b := &fakeTransport{inbox: make(chan []byte, 4096), closed: make(chan struct{})}
	a.peer = b
	b.peer = a
	return a, b
}

func (t *fakeTransport) WriteText(text string) error {
	select {
	case <-t.closed:
		return errors.New("closed")
	default:
	}
	select {
	case t.peer.inbox <- []byte(text):
		return nil
	case <-t.peer.closed:
		return errors.New("peer closed")
	}
}

func (t *fakeTransport) ReadMessage() ([]byte, error) {
	select {
	case msg := <-t.inbox:
		return msg, nil
	case <-t.closed:
		return nil, errors.New("closed")
	}
}

func (t *fakeTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *fakeTransport) inject(text string) {
	t.inbox <- []byte(text)
}

// --- deterministic fake core ---

type fakeCore struct {
	mu        sync.Mutex
	state     [64]byte
	masks     [2]uint16
	frames    int
	corruptAt int // frame at which to flip a state byte once, -1 never
}

func newFakeCore() *fakeCore {
	c := &fakeCore{corruptAt: -1}
	for i := range c.state {
		c.state[i] = byte(i * 7)
	}
	return c
}

func (c *fakeCore) SetButtons(player int, mask uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.masks[player] = mask
}

func (c *fakeCore) RunFrame() {
	c.mu.Lock()
	defer c.mu.Unlock()
	m0, m1 := c.masks[0], c.masks[1]
	for i := range c.state {
		c.state[i] = c.state[i]*33 ^ byte(m0) ^ byte(m0>>8) ^ byte(m1<<1) ^ byte(m1>>3) ^ byte(i)
	}
	c.frames++
	if c.corruptAt >= 0 && c.frames == c.corruptAt {
		c.state[0] ^= 0xFF
		c.corruptAt = -1
	}
}

func (c *fakeCore) SaveState() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, 0, 72)
	out = append(out, c.state[:]...)
	var fb [8]byte
	binary.LittleEndian.PutUint64(fb[:], uint64(c.frames))
	return append(out, fb[:]...)
}

func (c *fakeCore) LoadState(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(data) != 72 {
		return errors.New("bad state size")
	}
	copy(c.state[:], data[:64])
	c.frames = int(binary.LittleEndian.Uint64(data[64:]))
	return nil
}

func (c *fakeCore) stateBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, 64)
	copy(out, c.state[:])
	return out
}

// --- session driver ---

func runPair(t *testing.T, frames int, corruptGuestAt int) (*Session, *Session, *fakeCore, *fakeCore) {
	t.Helper()
	ta, tb := transportPair()
	hostCore := newFakeCore()
	guestCore := newFakeCore()
	guestCore.corruptAt = corruptGuestAt
	cfg := Config{Game: "Super Mario World (USA).sfc", ROMHash: "deadbeef", CoreName: "snes9x", ReadyNow: true}
	hostCfg := cfg
	hostCfg.IsHost = true
	host := NewSession(ta, hostCore, hostCfg)
	guest := NewSession(tb, guestCore, cfg)
	// The hub tells the host a guest connected.
	ta.inject(`{"type":"peer_joined","role":"guest"}`)

	errCh := make(chan error, 2)
	go func() { errCh <- host.Start() }()
	go func() { errCh <- guest.Start() }()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("start: %v", err)
		}
	}

	hostMask := func(f int) uint16 { return uint16((f * 3) % 512) }
	guestMask := func(f int) uint16 { return uint16((f * 5) % 1024) }
	stepErr := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for f := 0; f < frames; f++ {
			if err := host.Step(hostMask(f)); err != nil {
				stepErr <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for f := 0; f < frames; f++ {
			if err := guest.Step(guestMask(f)); err != nil {
				stepErr <- err
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case err := <-stepErr:
		t.Fatalf("step: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("timed out: lockstep deadlocked")
	}
	return host, guest, hostCore, guestCore
}

func TestLockstepStaysInSync(t *testing.T) {
	host, guest, hc, gc := runPair(t, 700, -1)
	if host.Desyncs() != 0 || guest.Desyncs() != 0 {
		t.Fatalf("unexpected desyncs: host %d guest %d", host.Desyncs(), guest.Desyncs())
	}
	if !bytes.Equal(hc.stateBytes(), gc.stateBytes()) {
		t.Fatal("final states differ after 700 in-sync frames")
	}
	if host.Frame() != 700 || guest.Frame() != 700 {
		t.Fatalf("frame counters: host %d guest %d", host.Frame(), guest.Frame())
	}
}

// The waiting room: names are exchanged, the session must not
// start until BOTH players ready up, then the shared countdown
// runs and lockstep proceeds in sync.
func TestRoomReadyThenLockstep(t *testing.T) {
	ta, tb := transportPair()
	hostCore := newFakeCore()
	guestCore := newFakeCore()
	base := Config{
		Game: "Super Mario World (USA).sfc", ROMHash: "deadbeef",
		CoreName: "snes9x", Countdown: 300 * time.Millisecond,
	}
	hostCfg := base
	hostCfg.IsHost = true
	hostCfg.PlayerName = "Host"
	guestCfg := base
	guestCfg.PlayerName = "Guest"
	host := NewSession(ta, hostCore, hostCfg)
	guest := NewSession(tb, guestCore, guestCfg)
	ta.inject(`{"type":"peer_joined","role":"guest"}`)

	errCh := make(chan error, 2)
	go func() { errCh <- host.Start() }()
	go func() { errCh <- guest.Start() }()

	waitUntil := func(what string, pred func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if pred() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitUntil("name exchange", func() bool {
		return host.PeerName() == "Guest" && guest.PeerName() == "Host"
	})
	if host.RoomPhase() != "room" || guest.RoomPhase() != "room" {
		t.Fatalf("phases: host %q guest %q", host.RoomPhase(), guest.RoomPhase())
	}

	guest.SetReady()
	waitUntil("host sees guest ready", host.PeerReady)
	// The host has not readied: nobody may have started.
	select {
	case err := <-errCh:
		t.Fatalf("session started before both players were ready: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	host.SetReady()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("start after ready: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("countdown never released the session")
		}
	}

	// Lockstep from frame 0, in sync.
	stepErr := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for f := 0; f < 150; f++ {
			if err := host.Step(uint16(f % 256)); err != nil {
				stepErr <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for f := 0; f < 150; f++ {
			if err := guest.Step(uint16((f * 2) % 256)); err != nil {
				stepErr <- err
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case err := <-stepErr:
		t.Fatalf("step: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("lockstep deadlocked after the room")
	}
	if !bytes.Equal(hostCore.stateBytes(), guestCore.stateBytes()) {
		t.Fatal("final states differ after the room test")
	}
}

func TestDesyncHealsViaSnapshot(t *testing.T) {
	host, guest, hc, gc := runPair(t, 900, 350)
	if host.Desyncs()+guest.Desyncs() == 0 {
		t.Fatal("corruption at frame 350 was never detected")
	}
	if !bytes.Equal(hc.stateBytes(), gc.stateBytes()) {
		t.Fatal("states did not reconverge after resync")
	}
	if host.Frame() != guest.Frame() {
		t.Fatalf("frame counters diverged: host %d guest %d", host.Frame(), guest.Frame())
	}
}
