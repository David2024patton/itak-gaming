package netsession

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func waitSignal(t *testing.T, s *Session, afterSeq, want int) []SignalMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := s.SignalsSince(afterSeq)
		if len(msgs) >= want {
			return msgs
		}
		time.Sleep(10 * time.Millisecond)
	}
	msgs, latest := s.SignalsSince(afterSeq)
	t.Fatalf("timed out waiting for %d signals after seq %d; got %d (latest %d)",
		want, afterSeq, len(msgs), latest)
	return nil
}

// (a) SendSignal emits the exact PROTOCOL.md 3.1 wire shapes:
// an offer or answer envelope is exactly {type, sdp} and an
// ice envelope is exactly {type, candidate, sdpMid,
// sdpMLineIndex} with sdpMLineIndex present even at 0. Own
// sends never enter the inbound queue.
func TestSendSignalEmitsWireShapes(t *testing.T) {
	ta, tb := transportPair()
	sess := NewSession(ta, newFakeCore(), Config{PlayerName: "Sender"})

	readOne := func() map[string]any {
		t.Helper()
		select {
		case raw := <-tb.inbox:
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("sent bytes are not JSON: %v", err)
			}
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("SendSignal wrote nothing to the transport")
			return nil
		}
	}

	const sdpOffer = "v=0 o=guest 1 IN IP4 0.0.0.0"
	if err := sess.SendSignal("offer", sdpOffer); err != nil {
		t.Fatalf("SendSignal offer: %v", err)
	}
	m := readOne()
	if len(m) != 2 || m["type"] != "offer" || m["sdp"] != sdpOffer {
		t.Errorf("offer envelope = %v, want exactly {type: offer, sdp: %q}", m, sdpOffer)
	}

	const sdpAnswer = "v=0 o=host 2 IN IP4 0.0.0.0"
	if err := sess.SendSignal("answer", sdpAnswer); err != nil {
		t.Fatalf("SendSignal answer: %v", err)
	}
	m = readOne()
	if len(m) != 2 || m["type"] != "answer" || m["sdp"] != sdpAnswer {
		t.Errorf("answer envelope = %v, want exactly {type: answer, sdp: %q}", m, sdpAnswer)
	}

	const cand = "candidate:1 1 udp 2113937151 192.0.2.1 54400 typ host"
	if err := sess.SendSignal("ice", cand); err != nil {
		t.Fatalf("SendSignal ice: %v", err)
	}
	m = readOne()
	if len(m) != 4 || m["type"] != "ice" || m["candidate"] != cand ||
		m["sdpMid"] != "0" {
		t.Errorf("ice envelope = %v, want {type: ice, candidate, sdpMid: 0, sdpMLineIndex: 0}", m)
	}
	if v, ok := m["sdpMLineIndex"]; !ok || v != float64(0) {
		t.Errorf("ice sdpMLineIndex = %v (present %v), want 0 present", v, ok)
	}

	if msgs, latest := sess.SignalsSince(0); latest != 0 || len(msgs) != 0 {
		t.Errorf("own sends entered the inbound queue: latest %d, %d entries",
			latest, len(msgs))
	}
}

// (b) Signaling envelopes from the peer land in the queue in
// order with their fields intact, and the cursor drains them.
func TestSignalFromPeerLandsInOrder(t *testing.T) {
	host, _, ta, _ := startChatPair(t)

	ta.inject(`{"type":"offer","sdp":"v=0 o=guest 1 IN IP4 0.0.0.0"}`)
	ta.inject(`{"type":"ice","candidate":"candidate:a","sdpMid":"0","sdpMLineIndex":0}`)
	ta.inject(`{"type":"ice","candidate":"candidate:b","sdpMid":"1","sdpMLineIndex":1}`)
	ta.inject(`{"type":"answer","sdp":"v=0 o=guest 2 IN IP4 0.0.0.0"}`)

	msgs := waitSignal(t, host, 0, 4)
	wantKinds := []string{"offer", "ice", "ice", "answer"}
	for i, m := range msgs {
		if m.Seq != i+1 {
			t.Errorf("entry %d Seq = %d, want %d", i, m.Seq, i+1)
		}
		if m.Kind != wantKinds[i] {
			t.Errorf("entry %d Kind = %q, want %q", i, m.Kind, wantKinds[i])
		}
	}
	if msgs[0].SDP != "v=0 o=guest 1 IN IP4 0.0.0.0" || msgs[0].Candidate != "" {
		t.Errorf("offer entry = %+v", msgs[0])
	}
	if msgs[1].Candidate != "candidate:a" || msgs[1].SDPMid != "0" ||
		msgs[1].SDPMLineIndex != 0 || msgs[1].SDP != "" {
		t.Errorf("first ice entry = %+v", msgs[1])
	}
	if msgs[2].Candidate != "candidate:b" || msgs[2].SDPMid != "1" ||
		msgs[2].SDPMLineIndex != 1 {
		t.Errorf("second ice entry = %+v", msgs[2])
	}
	if msgs[3].SDP != "v=0 o=guest 2 IN IP4 0.0.0.0" {
		t.Errorf("answer entry = %+v", msgs[3])
	}
	// Cursors: nothing new after the latest seq, tail works.
	if rest, _ := host.SignalsSince(4); len(rest) != 0 {
		t.Errorf("SignalsSince(latest) returned %d entries, want 0", len(rest))
	}
	if tail, _ := host.SignalsSince(2); len(tail) != 2 || tail[0].Seq != 3 {
		t.Errorf("SignalsSince(2) = %+v, want seqs 3 and 4", tail)
	}
}

// (c) Outbound payloads over the caps are rejected and never
// reach the wire; exactly at the cap is fine.
func TestSendSignalRejectsOversized(t *testing.T) {
	ta, tb := transportPair()
	sess := NewSession(ta, newFakeCore(), Config{PlayerName: "Picky"})

	bigSDP := strings.Repeat("a", signalSDPMaxBytes+1)
	bigICE := strings.Repeat("c", signalICEMaxBytes+1)
	for _, tc := range []struct{ kind, payload string }{
		{"offer", bigSDP},
		{"answer", bigSDP},
		{"ice", bigICE},
	} {
		if err := sess.SendSignal(tc.kind, tc.payload); err == nil {
			t.Errorf("SendSignal(%s, %d bytes) accepted, want error",
				tc.kind, len(tc.payload))
		}
	}
	select {
	case raw := <-tb.inbox:
		t.Errorf("rejected signal reached the wire: %.40s", raw)
	default:
	}
	if err := sess.SendSignal("offer", strings.Repeat("a", signalSDPMaxBytes)); err != nil {
		t.Errorf("SendSignal offer at the cap: %v", err)
	}
	if err := sess.SendSignal("ice", strings.Repeat("c", signalICEMaxBytes)); err != nil {
		t.Errorf("SendSignal ice at the cap: %v", err)
	}
}

// (d) A peer envelope over the caps is dropped whole, not
// truncated, and does not consume a seq.
func TestPeerSignalOversizedDropped(t *testing.T) {
	host, _, ta, _ := startChatPair(t)

	ta.inject(`{"type":"offer","sdp":"` + strings.Repeat("a", signalSDPMaxBytes+1) + `"}`)
	ta.inject(`{"type":"ice","candidate":"` + strings.Repeat("c", signalICEMaxBytes+1) + `"}`)
	ta.inject(`{"type":"ice","candidate":"candidate:ok","sdpMid":"0","sdpMLineIndex":0}`)

	msgs := waitSignal(t, host, 0, 1)
	if len(msgs) != 1 || msgs[0].Seq != 1 || msgs[0].Kind != "ice" ||
		msgs[0].Candidate != "candidate:ok" {
		t.Errorf("queue = %+v, want only the valid ice at seq 1", msgs)
	}
}

// (e) Unknown kinds and empty payloads are rejected outbound
// and nothing is sent or queued.
func TestSendSignalRejectsUnknownKindAndEmpty(t *testing.T) {
	ta, tb := transportPair()
	sess := NewSession(ta, newFakeCore(), Config{PlayerName: "Picky"})
	for _, tc := range []struct{ kind, payload string }{
		{"bogus", "v=0"},
		{"", "v=0"},
		{"OFFER", "v=0"},
		{"chat", "hello"},
		{"offer", ""},
		{"answer", ""},
		{"ice", ""},
	} {
		if err := sess.SendSignal(tc.kind, tc.payload); err == nil {
			t.Errorf("SendSignal(%q, %q) accepted, want error", tc.kind, tc.payload)
		}
	}
	select {
	case raw := <-tb.inbox:
		t.Errorf("rejected signal reached the wire: %s", raw)
	default:
	}
	if msgs, latest := sess.SignalsSince(0); latest != 0 || len(msgs) != 0 {
		t.Errorf("rejected sends were queued: latest %d, %d entries", latest, len(msgs))
	}
}

// (f) Signaling traffic in both directions leaves every piece
// of lockstep state exactly as it was.
func TestSignalTrafficLeavesLockstepAlone(t *testing.T) {
	host, guest, ta, _ := startChatPair(t)
	before := snapLockstep(host)

	ta.inject(`{"type":"offer","sdp":"v=0 o=guest 1 IN IP4 0.0.0.0"}`)
	ta.inject(`{"type":"ice","candidate":"candidate:a","sdpMid":"0","sdpMLineIndex":0}`)
	if err := guest.SendSignal("ice", "candidate:b"); err != nil {
		t.Fatalf("guest SendSignal: %v", err)
	}
	if err := host.SendSignal("answer", "v=0 o=host 2 IN IP4 0.0.0.0"); err != nil {
		t.Fatalf("host SendSignal: %v", err)
	}

	// The host queue proves the reader ran: two injected plus
	// the guest's send. The guest queue gets the host's answer.
	waitSignal(t, host, 0, 3)
	waitSignal(t, guest, 0, 1)

	if after := snapLockstep(host); after != before {
		t.Errorf("lockstep state changed by signaling:\nbefore %+v\nafter  %+v",
			before, after)
	}
}

// (g) The queue is capped at signalQueueCap; the oldest drop
// off and seqs keep climbing.
func TestSignalQueueCapDropsOldest(t *testing.T) {
	ta, _ := transportPair()
	sess := NewSession(ta, newFakeCore(), Config{PlayerName: "Cap"})
	const total = signalQueueCap + 5
	sess.mu.Lock()
	for i := 0; i < total; i++ {
		sess.notePeerSignal(envelope{
			Type: "ice", Candidate: "candidate:n", SDPMid: "0",
		})
	}
	sess.mu.Unlock()

	msgs, latest := sess.SignalsSince(0)
	if latest != total {
		t.Errorf("latest = %d, want %d", latest, total)
	}
	if len(msgs) != signalQueueCap {
		t.Fatalf("queue holds %d entries, want cap %d", len(msgs), signalQueueCap)
	}
	if msgs[0].Seq != total-signalQueueCap+1 {
		t.Errorf("oldest kept seq = %d, want %d", msgs[0].Seq, total-signalQueueCap+1)
	}
	if msgs[len(msgs)-1].Seq != total {
		t.Errorf("newest seq = %d, want %d", msgs[len(msgs)-1].Seq, total)
	}
}

// (h) Signaling works while the session is still in the
// waiting room, and what arrived there is still queued once
// play begins. Voice starts in the room, so the pipe has to.
func TestSignalArrivesDuringWaitingRoom(t *testing.T) {
	ta, tb := transportPair()
	base := Config{
		Game: "Super Mario World (USA).sfc", ROMHash: "deadbeef",
		CoreName: "snes9x", Countdown: 100 * time.Millisecond,
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

	// Wait until both sides can see each other in the room.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if host.RoomPhase() == "room" && guest.RoomPhase() == "room" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if host.RoomPhase() != "room" {
		t.Fatalf("host phase = %q, want room", host.RoomPhase())
	}

	// The guest offers before anyone readies up.
	if err := guest.SendSignal("offer", "v=0 o=guest 1 IN IP4 0.0.0.0"); err != nil {
		t.Fatalf("guest SendSignal in room: %v", err)
	}
	msgs := waitSignal(t, host, 0, 1)
	if msgs[0].Kind != "offer" {
		t.Errorf("room signal kind = %q, want offer", msgs[0].Kind)
	}

	// Finish the ritual; the queued offer survives into play.
	host.SetReady()
	guest.SetReady()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("start: %v", err)
		}
	}
	if msgs, _ := host.SignalsSince(0); len(msgs) != 1 {
		t.Errorf("after start the queue holds %d entries, want the room offer", len(msgs))
	}
}
