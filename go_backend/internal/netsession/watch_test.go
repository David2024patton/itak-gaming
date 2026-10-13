package netsession

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// three-party fake hub
// The fakeTransport pair in session_test.go models the relay
// for two clients. Spectating needs the real hub's fan-out:
// every message lands in every other room member's inbox.
// hubEnd is one room member's socket; it also records
// everything it sent, which the "watcher never sends game
// traffic" test asserts on.

type hubEnd struct {
	hub    *fakeHub
	inbox  chan []byte
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex
	sent   [][]byte
}

type fakeHub struct {
	mu   sync.Mutex
	ends map[*hubEnd]bool
}

func newFakeHub() *fakeHub { return &fakeHub{ends: make(map[*hubEnd]bool)} }

func (h *fakeHub) join() *hubEnd {
	e := &hubEnd{
		hub: h, inbox: make(chan []byte, 4096), closed: make(chan struct{}),
	}
	h.mu.Lock()
	h.ends[e] = true
	h.mu.Unlock()
	return e
}

func (e *hubEnd) WriteText(text string) error {
	select {
	case <-e.closed:
		return errors.New("closed")
	default:
	}
	e.mu.Lock()
	e.sent = append(e.sent, []byte(text))
	e.mu.Unlock()
	e.hub.mu.Lock()
	targets := make([]*hubEnd, 0, len(e.hub.ends))
	for o := range e.hub.ends {
		if o != e {
			targets = append(targets, o)
		}
	}
	e.hub.mu.Unlock()
	for _, o := range targets {
		select {
		case o.inbox <- []byte(text):
		case <-o.closed:
		}
	}
	return nil
}

func (e *hubEnd) ReadMessage() ([]byte, error) {
	select {
	case msg := <-e.inbox:
		return msg, nil
	case <-e.closed:
		return nil, errors.New("closed")
	}
}

func (e *hubEnd) Close() error {
	e.once.Do(func() { close(e.closed) })
	return nil
}

// inject delivers a server-style notice straight to this end,
// the way session_test.go injects peer_joined.
func (e *hubEnd) inject(text string) { e.inbox <- []byte(text) }

func (e *hubEnd) sentEnvelopes() []envelope {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]envelope, 0, len(e.sent))
	for _, raw := range e.sent {
		var env envelope
		if err := json.Unmarshal(raw, &env); err == nil {
			out = append(out, env)
		}
	}
	return out
}

func pollUntil(t *testing.T, what string, pred func() bool) {
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

// Mask conventions mirror runPair: the mask sampled at step f
// is applied at frame f+delay, and frames below the delay use
// the agreed neutral input.
func watchHostMask(f int) uint16  { return uint16((f * 3) % 512) }
func watchGuestMask(f int) uint16 { return uint16((f * 5) % 1024) }

func appliedMask(mask func(int) uint16, frame, delay int) uint16 {
	if frame < delay {
		return 0
	}
	return mask(frame - delay)
}

// sender tagging (brief item 1)

func TestSendEnvStampsFromAndSeat(t *testing.T) {
	readEnv := func(t *testing.T, tb *fakeTransport) envelope {
		t.Helper()
		select {
		case raw := <-tb.inbox:
			var e envelope
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatalf("sent bytes are not an envelope: %v", err)
			}
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("nothing was sent")
			return envelope{}
		}
	}

	ta, tb := transportPair()
	host := NewSession(ta, newFakeCore(), Config{PlayerName: "Host", IsHost: true})
	if err := host.sendEnv(envelope{Type: "input", Frame: 9, Buttons: 7}); err != nil {
		t.Fatalf("sendEnv input: %v", err)
	}
	if e := readEnv(t, tb); e.From != "Host" || e.Seat != 1 || e.Frame != 9 {
		t.Errorf("host input envelope = %+v, want from Host seat 1", e)
	}
	if err := host.sendEnv(envelope{Type: "pause", Paused: true}); err != nil {
		t.Fatalf("sendEnv pause: %v", err)
	}
	if e := readEnv(t, tb); e.From != "Host" || e.Seat != 0 {
		t.Errorf("host pause envelope = %+v, want from Host and no seat", e)
	}
	if got := host.helloEnv(); got.Role != "host" || got.Player != "Host" {
		t.Errorf("host hello = %+v, want role host", got)
	}

	ta2, tb2 := transportPair()
	guest := NewSession(ta2, newFakeCore(), Config{PlayerName: "Guest"})
	if err := guest.sendEnv(envelope{Type: "input", Frame: 3, Buttons: 1}); err != nil {
		t.Fatalf("guest sendEnv input: %v", err)
	}
	if e := readEnv(t, tb2); e.From != "Guest" || e.Seat != 2 {
		t.Errorf("guest input envelope = %+v, want from Guest seat 2", e)
	}
	if got := guest.helloEnv(); got.Role != "guest" {
		t.Errorf("guest hello role = %q, want guest", got.Role)
	}
}

// (b) foreign senders cannot move a player session

func TestForeignSenderFiltering(t *testing.T) {
	host, _, ta, _ := startChatPair(t)
	if host.PeerName() != "Guest" {
		t.Fatalf("peer name = %q, want Guest", host.PeerName())
	}
	remoteHas := func(m map[int]uint16, frame int) bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		_, ok := m[frame]
		return ok
	}

	// An input from a spectator's name is not the peer's input.
	ta.inject(`{"type":"input","frame":42,"buttons":9,"from":"Watcher"}`)
	time.Sleep(300 * time.Millisecond)
	if remoteHas(host.remoteIn, 42) {
		t.Fatal("foreign input entered remoteIn")
	}
	// The peer's identical frame is accepted...
	ta.inject(`{"type":"input","frame":42,"buttons":9,"from":"Guest"}`)
	pollUntil(t, "peer input accepted", func() bool {
		return remoteHas(host.remoteIn, 42)
	})
	// ...and so is an unstamped one (an older client).
	ta.inject(`{"type":"input","frame":43,"buttons":3}`)
	pollUntil(t, "unstamped input accepted", func() bool {
		return remoteHas(host.remoteIn, 43)
	})

	// Hash, pause, and countdown are filtered the same way.
	ta.inject(`{"type":"state_hash","frame":77,"hash":"cafe","from":"Watcher"}`)
	ta.inject(`{"type":"pause","paused":true,"from":"Watcher"}`)
	ta.inject(`{"type":"countdown","endsAt":9999999999999,"from":"Watcher"}`)
	time.Sleep(300 * time.Millisecond)
	host.mu.Lock()
	_, hashOK := host.remoteHash[77]
	paused := host.peerPaused
	countdownSet := !host.countdownEnds.IsZero()
	host.mu.Unlock()
	if hashOK {
		t.Error("foreign state_hash entered remoteHash")
	}
	if paused {
		t.Error("foreign pause set peerPaused")
	}
	if countdownSet {
		t.Error("foreign countdown set countdownEnds")
	}
	ta.inject(`{"type":"pause","paused":true,"from":"Guest"}`)
	pollUntil(t, "peer pause accepted", host.PeerPaused)

	// Chat is different: anyone in the room may speak, and the
	// stamp says who. Unstamped chat still reads as the peer.
	ta.inject(`{"type":"chat","text":"hi from the fan","from":"Watcher"}`)
	ta.inject(`{"type":"chat","text":"no stamp on this one"}`)
	msgs := waitChat(t, host, 0, 2)
	if msgs[0].From != "Watcher" || msgs[0].Text != "hi from the fan" {
		t.Errorf("spectator chat entry = %+v", msgs[0])
	}
	if msgs[1].From != "Guest" {
		t.Errorf("unstamped chat entry = %+v, want From Guest", msgs[1])
	}
}

// (c) snapshot addressing

func TestSnapshotAddressing(t *testing.T) {
	ta, tb := transportPair()
	receiver := NewSession(tb, newFakeCore(), Config{PlayerName: "Receiver"})
	go receiver.reader()
	sender := NewSession(ta, newFakeCore(), Config{PlayerName: "Sender"})
	state := newFakeCore().SaveState()

	snapState := func() (ready bool, bufLen int) {
		receiver.mu.Lock()
		defer receiver.mu.Unlock()
		return receiver.snapReady, len(receiver.snapBuf)
	}

	// Addressed to someone else: not even the chunks assemble.
	if err := sender.deliverSnapshot(7, state, "SomeoneElse"); err != nil {
		t.Fatalf("deliverSnapshot: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if ready, bufLen := snapState(); ready || bufLen != 0 {
		t.Fatalf("foreign snapshot assembled: ready %v, %d bytes buffered", ready, bufLen)
	}

	// Addressed to us: assembles and applies cleanly.
	if err := sender.deliverSnapshot(7, state, "Receiver"); err != nil {
		t.Fatalf("deliverSnapshot: %v", err)
	}
	pollUntil(t, "own snapshot ready", func() bool {
		ready, _ := snapState()
		return ready
	})
	got, frame, from := receiver.takeSnapshot()
	if frame != 7 || from != "Sender" || !bytes.Equal(got, state) {
		t.Fatalf("took snapshot frame %d from %q, %d bytes; want 7/Sender/%d equal",
			frame, from, len(got), len(state))
	}

	// Unaddressed (an older sender): still accepted.
	if err := sender.deliverSnapshot(9, state, ""); err != nil {
		t.Fatalf("deliverSnapshot: %v", err)
	}
	pollUntil(t, "unaddressed snapshot ready", func() bool {
		ready, _ := snapState()
		return ready
	})
	_, frame, _ = receiver.takeSnapshot()
	if frame != 9 {
		t.Fatalf("unaddressed snapshot frame = %d, want 9", frame)
	}
}

// the shared three-party room

type watchRoom struct {
	host, guest, watcher          *Session
	hostEnd, guestEnd, watcherEnd *hubEnd
	hostCore, guestCore           *fakeCore
	watchCore                     *fakeCore
}

func startWatchRoom(t *testing.T) *watchRoom {
	t.Helper()
	hub := newFakeHub()
	he, ge, we := hub.join(), hub.join(), hub.join()
	hc, gc, wc := newFakeCore(), newFakeCore(), newFakeCore()
	base := Config{
		Game: "Super Mario World (USA).sfc", ROMHash: "deadbeef",
		CoreName: "snes9x", ReadyNow: true, Countdown: 100 * time.Millisecond,
	}
	hostCfg := base
	hostCfg.IsHost = true
	hostCfg.PlayerName = "Host"
	guestCfg := base
	guestCfg.PlayerName = "Guest"
	r := &watchRoom{
		host: NewSession(he, hc, hostCfg), guest: NewSession(ge, gc, guestCfg),
		watcher: NewWatchSession(we, wc, Config{Game: base.Game, PlayerName: "Watcher"}),
		hostEnd: he, guestEnd: ge, watcherEnd: we,
		hostCore: hc, guestCore: gc, watchCore: wc,
	}
	he.inject(`{"type":"peer_joined","role":"guest"}`)
	if err := r.watcher.StartWatch(); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	errCh := make(chan error, 2)
	go func() { errCh <- r.host.Start() }()
	go func() { errCh <- r.guest.Start() }()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("start: %v", err)
		}
	}
	return r
}

// (d) the watcher reconstructs the match

func TestWatchStepsInSync(t *testing.T) {
	r := startWatchRoom(t)
	// The roster path names the seats too (it may land before
	// or after the hellos; both must agree).
	r.watcherEnd.inject(`{"type":"roster","members":[{"name":"Host","role":"host"},{"name":"Guest","role":"guest"},{"name":"Watcher","role":"spectator"}]}`)

	const frames = 300
	stepErr := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for f := 0; f < frames; f++ {
			if err := r.host.Step(watchHostMask(f)); err != nil {
				stepErr <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for f := 0; f < frames; f++ {
			if err := r.guest.Step(watchGuestMask(f)); err != nil {
				stepErr <- err
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	got := make([][2]uint16, 0, frames)
	deadline := time.Now().Add(60 * time.Second)
	for len(got) < frames && time.Now().Before(deadline) {
		p1, p2, fr, ok := r.watcher.WatchStep()
		if !ok {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		if fr != len(got) {
			t.Fatalf("watcher delivered frame %d, want %d", fr, len(got))
		}
		got = append(got, [2]uint16{p1, p2})
	}
	if len(got) < frames {
		t.Fatalf("watcher delivered %d of %d frames", len(got), frames)
	}
	select {
	case <-done:
	case err := <-stepErr:
		t.Fatalf("player step: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("players did not finish stepping")
	}

	for f := 0; f < frames; f++ {
		want := [2]uint16{
			appliedMask(watchHostMask, f, defaultInputDelay),
			appliedMask(watchGuestMask, f, defaultInputDelay),
		}
		if got[f] != want {
			t.Fatalf("frame %d: watcher applied %v, players applied %v", f, got[f], want)
		}
	}
	if !bytes.Equal(r.watchCore.stateBytes(), r.hostCore.stateBytes()) ||
		!bytes.Equal(r.watchCore.stateBytes(), r.guestCore.stateBytes()) {
		t.Fatal("watcher core state differs from both players at the end")
	}
	phase, hostName, guestName, _ := r.watcher.WatchInfo()
	if phase != "playing" || hostName != "Host" || guestName != "Guest" {
		t.Fatalf("WatchInfo = %q/%q/%q, want playing/Host/Guest", phase, hostName, guestName)
	}
}

// (e) mid-match watch join via watch_sync

func stepBurst(t *testing.T, host, guest *Session, from, count int) {
	t.Helper()
	done := make(chan error, 2)
	go func() {
		for f := from; f < from+count; f++ {
			if err := host.Step(watchHostMask(f)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	go func() {
		for f := from; f < from+count; f++ {
			if err := guest.Step(watchGuestMask(f)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	timeout := time.After(60 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("burst step: %v", err)
			}
		case <-timeout:
			t.Fatal("burst timed out")
		}
	}
}

func TestWatchMidMatchJoin(t *testing.T) {
	hub := newFakeHub()
	he, ge := hub.join(), hub.join()
	hc, gc := newFakeCore(), newFakeCore()
	base := Config{
		Game: "Super Mario World (USA).sfc", ROMHash: "deadbeef",
		CoreName: "snes9x", ReadyNow: true, Countdown: 100 * time.Millisecond,
	}
	hostCfg := base
	hostCfg.IsHost = true
	hostCfg.PlayerName = "Host"
	guestCfg := base
	guestCfg.PlayerName = "Guest"
	host := NewSession(he, hc, hostCfg)
	guest := NewSession(ge, gc, guestCfg)
	he.inject(`{"type":"peer_joined","role":"guest"}`)
	errCh := make(chan error, 2)
	go func() { errCh <- host.Start() }()
	go func() { errCh <- guest.Start() }()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("start: %v", err)
		}
	}

	// The match runs 300 frames with nobody watching.
	stepBurst(t, host, guest, 0, 300)
	if host.Frame() != 300 {
		t.Fatalf("host frame = %d, want 300", host.Frame())
	}

	// A spectator arrives mid-match and asks for a snapshot.
	we := hub.join()
	wc := newFakeCore()
	watcher := NewWatchSession(we, wc, Config{Game: base.Game, PlayerName: "Watcher"})
	if err := watcher.StartWatch(); err != nil {
		t.Fatalf("StartWatch: %v", err)
	}
	pollUntil(t, "host sees the watch_sync request", func() bool {
		return host.PollWatchSync() == "Watcher"
	})
	if err := host.SendSnapshotTo("Watcher"); err != nil {
		t.Fatalf("SendSnapshotTo: %v", err)
	}
	snapFrame := host.Frame()

	// Play continues; the watcher locks onto the snapshot and
	// steps in sync from its frame.
	stepErr := make(chan error, 2)
	go func() {
		for f := 300; f < 500; f++ {
			if err := host.Step(watchHostMask(f)); err != nil {
				stepErr <- err
				return
			}
		}
		stepErr <- nil
	}()
	go func() {
		for f := 300; f < 500; f++ {
			if err := guest.Step(watchGuestMask(f)); err != nil {
				stepErr <- err
				return
			}
		}
		stepErr <- nil
	}()

	startFrame := -1
	got := make(map[int][2]uint16)
	deadline := time.Now().Add(60 * time.Second)
	for len(got) < 200 && time.Now().Before(deadline) {
		p1, p2, fr, ok := watcher.WatchStep()
		if !ok {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		if startFrame < 0 {
			startFrame = fr
		}
		got[fr] = [2]uint16{p1, p2}
	}
	for i := 0; i < 2; i++ {
		if err := <-stepErr; err != nil {
			t.Fatalf("player step: %v", err)
		}
	}
	if startFrame != snapFrame {
		t.Fatalf("watcher started at frame %d, want the snapshot frame %d",
			startFrame, snapFrame)
	}
	// From the snapshot frame on, the watcher applies the
	// exact inputs the players apply, including the in-flight
	// frames right after the snapshot (those are the ones the
	// host's answer repeats; see SendSnapshotTo).
	for f := snapFrame; f < 500; f++ {
		pair, ok := got[f]
		if !ok {
			t.Fatalf("watcher never delivered frame %d", f)
		}
		want := [2]uint16{
			appliedMask(watchHostMask, f, defaultInputDelay),
			appliedMask(watchGuestMask, f, defaultInputDelay),
		}
		if pair != want {
			t.Fatalf("frame %d: watcher applied %v, want %v", f, pair, want)
		}
	}
	if !bytes.Equal(wc.stateBytes(), hc.stateBytes()) {
		t.Fatal("watcher core state differs from the host after the join")
	}
}

// (f) a watcher never sends game traffic

func TestWatchNeverSendsGameTraffic(t *testing.T) {
	r := startWatchRoom(t)

	const frames = 40
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for f := 0; f < frames; f++ {
			_ = r.host.Step(watchHostMask(f))
		}
	}()
	go func() {
		defer wg.Done()
		for f := 0; f < frames; f++ {
			_ = r.guest.Step(watchGuestMask(f))
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	stepped := 0
	for stepped < frames && time.Now().Before(deadline) {
		if _, _, _, ok := r.watcher.WatchStep(); ok {
			stepped++
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}
	wg.Wait()
	if stepped < frames {
		t.Fatalf("watcher stepped %d of %d frames", stepped, frames)
	}
	if err := r.watcher.SendChat("great match, both of you"); err != nil {
		t.Fatalf("watcher SendChat: %v", err)
	}
	// The players hear the spectator under the spectator's name.
	seen := waitChat(t, r.host, 0, 1)
	if seen[0].From != "Watcher" {
		t.Fatalf("host chat entry From = %q, want Watcher", seen[0].From)
	}

	forbidden := map[string]bool{
		"input": true, "ready": true, "countdown": true,
		"session_offer": true, "session_accept": true,
		"session_reject": true, "session_end": true,
		"state_hash": true, "resync_request": true,
	}
	sawHello, sawSync, sawChat := false, false, false
	for _, e := range r.watcherEnd.sentEnvelopes() {
		if forbidden[e.Type] {
			t.Errorf("watcher sent forbidden envelope type %q", e.Type)
		}
		switch e.Type {
		case "hello":
			sawHello = true
			if e.Role != "spectator" {
				t.Errorf("watcher hello role = %q, want spectator", e.Role)
			}
		case "watch_sync":
			sawSync = true
		case "chat":
			sawChat = true
		}
	}
	if !sawHello || !sawSync || !sawChat {
		t.Errorf("watcher sent log missing basics: hello %v, watch_sync %v, chat %v",
			sawHello, sawSync, sawChat)
	}
}
