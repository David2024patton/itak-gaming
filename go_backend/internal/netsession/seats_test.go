package netsession

import (
	"testing"
	"time"
)

// Send side: only the host may assign, the call must name
// someone, the seat must exist, and a valid call reaches the
// guest's queue exactly once.
func TestSeatAssignSendValidationAndDelivery(t *testing.T) {
	host, guest, _, _ := startChatPair(t)

	if err := guest.SendSeatAssign("Fan", 2); err == nil {
		t.Error("a guest was allowed to assign seats")
	}
	for _, bad := range []struct {
		player string
		seat   int
	}{
		{"", 1}, {"   ", 2}, {"Fan", 0}, {"Fan", 3}, {"Fan", -1},
	} {
		if err := host.SendSeatAssign(bad.player, bad.seat); err == nil {
			t.Errorf("SendSeatAssign(%q, %d) was allowed", bad.player, bad.seat)
		}
	}
	// None of the refused calls may have leaked an envelope.
	time.Sleep(300 * time.Millisecond)
	if _, _, ok := guest.PollSeatAssign(); ok {
		t.Fatal("a refused seat assignment reached the guest")
	}

	if err := host.SendSeatAssign("Fan", 2); err != nil {
		t.Fatalf("SendSeatAssign: %v", err)
	}
	pollUntil(t, "guest records the seat call", func() bool {
		player, seat, ok := guest.PollSeatAssign()
		return ok && player == "Fan" && seat == 2
	})
	if _, _, ok := guest.PollSeatAssign(); ok {
		t.Error("the same seat call was handed out twice")
	}
}

// Receive side: malformed calls are dropped even from the
// host, and a well-formed call from a non-host (a spectator
// sharing the socket) is ignored by the guest. Authority is
// the sender's identity, never a field in the envelope.
func TestSeatAssignInboundGateAndValidation(t *testing.T) {
	_, guest, _, tb := startChatPair(t)

	tb.inject(`{"type":"seat_assign","player":"Fan","seat":1,"from":"Watcher"}`)
	tb.inject(`{"type":"seat_assign","player":"","seat":2,"from":"Host"}`)
	tb.inject(`{"type":"seat_assign","player":"Fan","seat":0,"from":"Host"}`)
	tb.inject(`{"type":"seat_assign","player":"Fan","seat":3,"from":"Host"}`)
	time.Sleep(300 * time.Millisecond)
	if player, seat, ok := guest.PollSeatAssign(); ok {
		t.Fatalf("guest recorded %q for seat %d from a foreign or malformed call",
			player, seat)
	}

	tb.inject(`{"type":"seat_assign","player":"Fan","seat":1,"from":"Host"}`)
	pollUntil(t, "guest records the host's call", func() bool {
		player, seat, ok := guest.PollSeatAssign()
		return ok && player == "Fan" && seat == 1
	})
}

// The watcher hears the referee too, on the host's stamp
// alone: a call stamped by the guest is not the referee's
// and is not recorded.
func TestWatchSeatAssignFromHostOnly(t *testing.T) {
	r := startWatchRoom(t)

	r.watcherEnd.inject(`{"type":"seat_assign","player":"Watcher","seat":1,"from":"Guest"}`)
	time.Sleep(300 * time.Millisecond)
	if _, _, ok := r.watcher.PollSeatAssign(); ok {
		t.Fatal("watcher recorded a seat call stamped by the guest")
	}

	if err := r.host.SendSeatAssign("Watcher", 2); err != nil {
		t.Fatalf("SendSeatAssign: %v", err)
	}
	pollUntil(t, "watcher records the host's call", func() bool {
		player, seat, ok := r.watcher.PollSeatAssign()
		return ok && player == "Watcher" && seat == 2
	})
	// The guest hears the same call from its peer.
	pollUntil(t, "guest records the host's call", func() bool {
		player, seat, ok := r.guest.PollSeatAssign()
		return ok && player == "Watcher" && seat == 2
	})
}
