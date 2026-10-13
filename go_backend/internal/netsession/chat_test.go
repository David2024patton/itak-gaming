package netsession

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// startChatPair brings up a host/guest pair through the waiting
// room (ReadyNow, named players) without stepping any frames.
func startChatPair(t *testing.T) (*Session, *Session, *fakeTransport, *fakeTransport) {
	t.Helper()
	ta, tb := transportPair()
	base := Config{
		Game: "Super Mario World (USA).sfc", ROMHash: "deadbeef",
		CoreName: "snes9x", ReadyNow: true, Countdown: 100 * time.Millisecond,
	}
	hostCfg := base
	hostCfg.IsHost = true
	hostCfg.PlayerName = "Host"
	guestCfg := base
	guestCfg.PlayerName = "Guest"
	host := NewSession(ta, newFakeCore(), hostCfg)
	guest := NewSession(tb, newFakeCore(), guestCfg)
	ta.inject(`{"type":"peer_joined","role":"guest"}`)

	errCh := make(chan error, 2)
	go func() { errCh <- host.Start() }()
	go func() { errCh <- guest.Start() }()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("start: %v", err)
		}
	}
	return host, guest, ta, tb
}

func waitChat(t *testing.T, s *Session, afterSeq, want int) []ChatMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := s.ChatSince(afterSeq)
		if len(msgs) >= want {
			return msgs
		}
		time.Sleep(10 * time.Millisecond)
	}
	msgs, latest := s.ChatSince(afterSeq)
	t.Fatalf("timed out waiting for %d chat messages after seq %d; got %d (latest %d)",
		want, afterSeq, len(msgs), latest)
	return nil
}

// lockstepSnapshot captures the state chat is forbidden to touch.
type lockstepSnapshot struct {
	frame     int
	localIn   int
	remoteIn  int
	localHash int
	remoteHas int
	desyncs   int
	phase     int
}

func snapLockstep(s *Session) lockstepSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return lockstepSnapshot{
		frame:     s.frame,
		localIn:   len(s.localIn),
		remoteIn:  len(s.remoteIn),
		localHash: len(s.localHash),
		remoteHas: len(s.remoteHash),
		desyncs:   s.desyncs,
		phase:     s.phase,
	}
}

// (a) A chat envelope from the peer lands in the log with the
// peer's name and trimmed text, and lockstep state does not move.
func TestChatFromPeerLandsAndTouchesNothing(t *testing.T) {
	host, _, ta, _ := startChatPair(t)
	if host.PeerName() != "Guest" {
		t.Fatalf("peer name = %q, want Guest", host.PeerName())
	}
	before := snapLockstep(host)

	ta.inject(`{"type":"chat","text":"  good game, ready when you are  "}`)
	// Noise that must be ignored: empty and whitespace-only.
	ta.inject(`{"type":"chat","text":""}`)
	ta.inject(`{"type":"chat","text":"   "}`)

	msgs := waitChat(t, host, 0, 1)
	if len(msgs) != 1 {
		t.Fatalf("log has %d messages, want exactly 1: %+v", len(msgs), msgs)
	}
	m := msgs[0]
	if m.From != "Guest" {
		t.Errorf("From = %q, want Guest", m.From)
	}
	if m.Text != "good game, ready when you are" {
		t.Errorf("Text = %q, want trimmed text", m.Text)
	}
	if m.Mine {
		t.Error("peer message marked Mine")
	}
	if m.Seq != 1 {
		t.Errorf("Seq = %d, want 1", m.Seq)
	}
	if after := snapLockstep(host); after != before {
		t.Errorf("lockstep state changed by chat:\nbefore %+v\nafter  %+v", before, after)
	}
}

// (b) SendChat writes the right envelope on the transport and
// logs the message as ours; the peer's session logs it too.
func TestSendChatEmitsEnvelopeAndLogsMine(t *testing.T) {
	// Wire level: no Start, so nothing consumes what we write.
	ta, tb := transportPair()
	sess := NewSession(ta, newFakeCore(), Config{PlayerName: "Sender"})
	if err := sess.SendChat("  hello wire  "); err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	select {
	case raw := <-tb.inbox:
		var e envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("sent bytes are not an envelope: %v", err)
		}
		if e.Type != "chat" || e.Text != "hello wire" {
			t.Errorf("sent envelope = %+v, want type chat with trimmed text", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendChat wrote nothing to the transport")
	}
	msgs, latest := sess.ChatSince(0)
	if latest != 1 || len(msgs) != 1 {
		t.Fatalf("own log: latest %d, %d messages, want 1/1", latest, len(msgs))
	}
	if !msgs[0].Mine || msgs[0].From != "Sender" || msgs[0].Text != "hello wire" {
		t.Errorf("own log entry = %+v", msgs[0])
	}

	// Session level: the peer sees it under our name, not as theirs.
	host, guest, _, _ := startChatPair(t)
	if err := guest.SendChat("gg, rematch?"); err != nil {
		t.Fatalf("guest SendChat: %v", err)
	}
	seen := waitChat(t, host, 0, 1)
	if seen[0].From != "Guest" || seen[0].Mine || seen[0].Text != "gg, rematch?" {
		t.Errorf("host log entry = %+v", seen[0])
	}
	mine := waitChat(t, guest, 0, 1)
	if !mine[0].Mine || mine[0].From != "Guest" {
		t.Errorf("guest log entry = %+v", mine[0])
	}
	// Cursors: nothing new after the latest seq.
	if msgs, _ := host.ChatSince(seen[0].Seq); len(msgs) != 0 {
		t.Errorf("ChatSince(latest) returned %d messages, want 0", len(msgs))
	}
}

// (c) The log is capped at 200; the oldest drop off and seqs
// keep climbing.
func TestChatLogCapDropsOldest(t *testing.T) {
	ta, _ := transportPair()
	sess := NewSession(ta, newFakeCore(), Config{PlayerName: "Cap"})
	const total = chatLogCap + 5
	for i := 1; i <= total; i++ {
		if err := sess.SendChat("msg"); err != nil {
			t.Fatalf("SendChat %d: %v", i, err)
		}
	}
	msgs, latest := sess.ChatSince(0)
	if latest != total {
		t.Errorf("latest = %d, want %d", latest, total)
	}
	if len(msgs) != chatLogCap {
		t.Fatalf("log holds %d messages, want cap %d", len(msgs), chatLogCap)
	}
	if msgs[0].Seq != total-chatLogCap+1 {
		t.Errorf("oldest kept seq = %d, want %d", msgs[0].Seq, total-chatLogCap+1)
	}
	if msgs[len(msgs)-1].Seq != total {
		t.Errorf("newest seq = %d, want %d", msgs[len(msgs)-1].Seq, total)
	}
}

// (d) Empty and over-long outgoing messages are rejected and
// never reach the log or the wire.
func TestSendChatRejectsEmptyAndOversized(t *testing.T) {
	ta, tb := transportPair()
	sess := NewSession(ta, newFakeCore(), Config{PlayerName: "Picky"})
	for _, bad := range []string{
		"",
		"   ",
		"\n\t ",
		strings.Repeat("x", chatMaxRunes+1),
		strings.Repeat("é", chatMaxRunes+1), // runes, not bytes
	} {
		if err := sess.SendChat(bad); err == nil {
			t.Errorf("SendChat(%q...) accepted, want error", bad[:min(len(bad), 12)])
		}
	}
	if msgs, latest := sess.ChatSince(0); latest != 0 || len(msgs) != 0 {
		t.Errorf("rejected messages were logged: latest %d, %d entries", latest, len(msgs))
	}
	select {
	case raw := <-tb.inbox:
		t.Errorf("rejected message reached the wire: %s", raw)
	default:
	}
	// Exactly at the cap is fine, including multibyte runes.
	if err := sess.SendChat(strings.Repeat("é", chatMaxRunes)); err != nil {
		t.Errorf("SendChat at the rune cap: %v", err)
	}
}
