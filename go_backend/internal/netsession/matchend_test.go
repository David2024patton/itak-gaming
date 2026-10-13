package netsession

import (
	"bytes"
	"testing"
	"time"
)

// startMatchPair is startChatPair with the cores handed back:
// the match end tests compare core states across two matches.
func startMatchPair(t *testing.T) (*Session, *Session, *fakeCore, *fakeCore) {
	t.Helper()
	ta, tb := transportPair()
	hostCore := newFakeCore()
	guestCore := newFakeCore()
	base := Config{
		Game: "Super Mario World (USA).sfc", ROMHash: "deadbeef",
		CoreName: "snes9x", ReadyNow: true, Countdown: 100 * time.Millisecond,
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
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("start: %v", err)
		}
	}
	return host, guest, hostCore, guestCore
}

// The money test for section 4.9: a match ends, both players
// land back in the room on the SAME sessions and sockets, and
// a second full handshake plus a full second match (hash
// checks included) completes in sync on them.
func TestMatchEndReturnsToRoomAndSecondMatchRuns(t *testing.T) {
	host, guest, hostCore, guestCore := startMatchPair(t)

	// Match 1: a short game, in sync.
	stepBurst(t, host, guest, 0, 150)
	if host.Frame() != 150 || guest.Frame() != 150 {
		t.Fatalf("match 1 frames: host %d guest %d", host.Frame(), guest.Frame())
	}
	// No next match while one is live.
	if err := host.StartNextMatch(); err == nil {
		t.Fatal("StartNextMatch ran during a live match")
	}

	// The host ends the match.
	if err := host.SendMatchEnd("good game"); err != nil {
		t.Fatalf("SendMatchEnd: %v", err)
	}
	// The sender sees its own event exactly once.
	if reason, ok := host.PollMatchEnd(); !ok || reason != "good game" {
		t.Fatalf("host PollMatchEnd = %q, %v; want good game, true", reason, ok)
	}
	if _, ok := host.PollMatchEnd(); ok {
		t.Fatal("host match end event reported twice")
	}
	// So does the guest.
	pollUntil(t, "guest match end event", func() bool {
		reason, ok := guest.PollMatchEnd()
		return ok && reason == "good game"
	})
	// A second end while already back in the room is an error.
	if err := host.SendMatchEnd("again"); err == nil {
		t.Fatal("SendMatchEnd in the room was allowed")
	}

	// Both sides are in the room: readys reset, frame state
	// cleared, session alive, socket untouched.
	if host.RoomPhase() != "room" || guest.RoomPhase() != "room" {
		t.Fatalf("phases after match end: host %q guest %q",
			host.RoomPhase(), guest.RoomPhase())
	}
	if host.LocalReady() || host.PeerReady() || guest.LocalReady() || guest.PeerReady() {
		t.Error("readys were not reset by the match end")
	}
	if host.Frame() != 0 || guest.Frame() != 0 {
		t.Errorf("frames after match end: host %d guest %d, want 0",
			host.Frame(), guest.Frame())
	}
	if err := host.checkEnded(); err != nil {
		t.Errorf("host session ended by match end: %v", err)
	}
	if err := guest.checkEnded(); err != nil {
		t.Errorf("guest session ended by match end: %v", err)
	}
	// The socket still carries traffic both ways.
	if err := host.SendChat("good game, run it back"); err != nil {
		t.Fatalf("chat after match end: %v", err)
	}
	msgs := waitChat(t, guest, 0, 1)
	if msgs[0].From != "Host" || msgs[0].Text != "good game, run it back" {
		t.Errorf("chat after match end = %+v", msgs[0])
	}

	// Match 2: a full second handshake on the same sessions,
	// then a full game, long enough to cross a hash check,
	// ending in sync.
	errCh := make(chan error, 2)
	go func() { errCh <- host.StartNextMatch() }()
	go func() { errCh <- guest.StartNextMatch() }()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("second start: %v", err)
		}
	}
	stepBurst(t, host, guest, 0, 320)
	if host.Desyncs() != 0 || guest.Desyncs() != 0 {
		t.Fatalf("match 2 desyncs: host %d guest %d", host.Desyncs(), guest.Desyncs())
	}
	if !bytes.Equal(hostCore.stateBytes(), guestCore.stateBytes()) {
		t.Fatal("core states differ after match 2")
	}
	if host.Frame() != 320 || guest.Frame() != 320 {
		t.Fatalf("match 2 frames: host %d guest %d", host.Frame(), guest.Frame())
	}
}

// A spectator sharing the socket cannot end the match: the
// envelope is gated on the peer, exactly like the other game
// traffic of section 4.8.
func TestMatchEndFromForeignSenderIgnored(t *testing.T) {
	r := startWatchRoom(t)
	stepBurst(t, r.host, r.guest, 0, 30)

	r.hostEnd.inject(`{"type":"match_end","reason":"rigged","from":"Watcher"}`)
	r.guestEnd.inject(`{"type":"match_end","reason":"rigged","from":"Watcher"}`)
	time.Sleep(300 * time.Millisecond)

	if r.host.RoomPhase() != "playing" || r.guest.RoomPhase() != "playing" {
		t.Fatalf("a spectator ended the match: host %q guest %q",
			r.host.RoomPhase(), r.guest.RoomPhase())
	}
	if _, ok := r.host.PollMatchEnd(); ok {
		t.Error("host recorded a spectator's match end")
	}
	if _, ok := r.guest.PollMatchEnd(); ok {
		t.Error("guest recorded a spectator's match end")
	}
}

// The watch side of match end: the watcher does not end, it
// falls back to the room with the players, hears the event,
// and asks for a fresh snapshot so the next match has its
// landing.
func TestWatchMatchEndReturnsToRoom(t *testing.T) {
	r := startWatchRoom(t)
	stepBurst(t, r.host, r.guest, 0, 30)

	if err := r.host.SendMatchEnd("tournament break"); err != nil {
		t.Fatalf("SendMatchEnd: %v", err)
	}
	if r.host.RoomPhase() != "room" {
		t.Fatalf("host phase after its own match end = %q, want room", r.host.RoomPhase())
	}
	pollUntil(t, "guest back in the room", func() bool {
		return r.guest.RoomPhase() == "room"
	})
	if reason, ok := r.guest.PollMatchEnd(); !ok || reason != "tournament break" {
		t.Errorf("guest PollMatchEnd = %q, %v", reason, ok)
	}

	pollUntil(t, "watcher back in the room", func() bool {
		phase, hostName, guestName, _ := r.watcher.WatchInfo()
		return phase == "room" && hostName == "Host" && guestName == "Guest"
	})
	if reason, ok := r.watcher.PollMatchEnd(); !ok || reason != "tournament break" {
		t.Errorf("watcher PollMatchEnd = %q, %v; want tournament break, true", reason, ok)
	}
	if _, ok := r.watcher.PollMatchEnd(); ok {
		t.Error("watcher match end event reported twice")
	}

	// The watcher asks for a fresh snapshot for the next
	// match (its second watch_sync: StartWatch sent the
	// first).
	syncs := func() int {
		n := 0
		for _, e := range r.watcherEnd.sentEnvelopes() {
			if e.Type == "watch_sync" {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for syncs() < 2 && time.Now().Before(deadline) {
		r.watcher.WatchStep()
		time.Sleep(10 * time.Millisecond)
	}
	if syncs() < 2 {
		t.Fatal("watcher never asked for a fresh snapshot after the match end")
	}
}
