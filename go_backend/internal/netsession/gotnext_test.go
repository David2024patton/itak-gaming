package netsession

import (
	"reflect"
	"testing"
	"time"
)

// The line end to end in a three-party room: the watcher
// and the guest join, only the host's list moves (theirs
// wait for the broadcast), the broadcast converges all
// three lists in the host's order, a duplicate join is a
// no-op, and a leave comes back out through the same path.
func TestGotNextReplication(t *testing.T) {
	r := startWatchRoom(t)

	if err := r.watcher.SendGotNext(true); err != nil {
		t.Fatalf("watcher SendGotNext: %v", err)
	}
	pollUntil(t, "host folds the watcher's join", func() bool {
		return reflect.DeepEqual(r.host.GotNextQueue(), []string{"Watcher"})
	})
	if err := r.guest.SendGotNext(true); err != nil {
		t.Fatalf("guest SendGotNext: %v", err)
	}
	pollUntil(t, "host folds the guest's join in order", func() bool {
		return reflect.DeepEqual(r.host.GotNextQueue(), []string{"Watcher", "Guest"})
	})
	if !r.host.GotNextDirty() {
		t.Error("host line not marked dirty after joins")
	}
	// The mirrors do not fold got_next themselves: until
	// the host broadcasts, their lists are still empty.
	if q := r.guest.GotNextQueue(); len(q) != 0 {
		t.Errorf("guest line before broadcast = %v, want empty", q)
	}
	if q := r.watcher.GotNextQueue(); len(q) != 0 {
		t.Errorf("watcher line before broadcast = %v, want empty", q)
	}

	if err := r.host.BroadcastGotNextQueue(); err != nil {
		t.Fatalf("BroadcastGotNextQueue: %v", err)
	}
	if r.host.GotNextDirty() {
		t.Error("host line still dirty after a successful broadcast")
	}
	for name, s := range map[string]*Session{"guest": r.guest, "watcher": r.watcher} {
		pollUntil(t, name+" mirror converges", func() bool {
			return reflect.DeepEqual(s.GotNextQueue(), []string{"Watcher", "Guest"})
		})
	}

	// Joining twice holds one spot.
	if err := r.watcher.SendGotNext(true); err != nil {
		t.Fatalf("watcher rejoin: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if q := r.host.GotNextQueue(); !reflect.DeepEqual(q, []string{"Watcher", "Guest"}) {
		t.Errorf("host line after duplicate join = %v", q)
	}
	if r.host.GotNextDirty() {
		t.Error("a duplicate join marked the line dirty")
	}

	// Leaving works the same way: host first, mirrors after
	// the next broadcast.
	if err := r.guest.SendGotNext(false); err != nil {
		t.Fatalf("guest SendGotNext leave: %v", err)
	}
	pollUntil(t, "host folds the guest's leave", func() bool {
		return reflect.DeepEqual(r.host.GotNextQueue(), []string{"Watcher"})
	})
	if err := r.host.BroadcastGotNextQueue(); err != nil {
		t.Fatalf("BroadcastGotNextQueue: %v", err)
	}
	pollUntil(t, "watcher mirror after leave", func() bool {
		return reflect.DeepEqual(r.watcher.GotNextQueue(), []string{"Watcher"})
	})
}

// Only the host mints the line. The broadcast API refuses
// the guest and the watcher outright, and a forged
// got_next_queue on the wire moves nobody: receivers
// believe the envelope only on the known host's stamp.
func TestGotNextQueueBroadcastGate(t *testing.T) {
	r := startWatchRoom(t)

	if err := r.guest.BroadcastGotNextQueue(); err == nil {
		t.Error("the guest was allowed to broadcast the line")
	}
	if err := r.watcher.BroadcastGotNextQueue(); err == nil {
		t.Error("a spectator was allowed to broadcast the line")
	}

	r.watcherEnd.inject(`{"type":"got_next_queue","queue":["Fake"],"from":"Guest"}`)
	r.guestEnd.inject(`{"type":"got_next_queue","queue":["Fake"],"from":"Watcher"}`)
	r.guestEnd.inject(`{"type":"got_next_queue","queue":["Fake"]}`)
	time.Sleep(300 * time.Millisecond)
	if q := r.watcher.GotNextQueue(); len(q) != 0 {
		t.Errorf("watcher adopted a guest-minted line: %v", q)
	}
	if q := r.guest.GotNextQueue(); len(q) != 0 {
		t.Errorf("guest adopted a watcher-minted or unstamped line: %v", q)
	}

	// The host's stamp is believed, even injected by hand.
	r.watcherEnd.inject(`{"type":"got_next_queue","queue":["Alpha","Beta"],"from":"Host"}`)
	pollUntil(t, "watcher adopts the host-stamped line", func() bool {
		return reflect.DeepEqual(r.watcher.GotNextQueue(), []string{"Alpha", "Beta"})
	})
}

// The host's own join applies to its own list at once (it
// IS the referee's list) and marks it dirty; the broadcast
// settles the flag and every other list.
func TestGotNextHostSelfJoin(t *testing.T) {
	host, guest, _, _ := startChatPair(t)

	if err := host.SendGotNext(true); err != nil {
		t.Fatalf("host SendGotNext: %v", err)
	}
	if q := host.GotNextQueue(); !reflect.DeepEqual(q, []string{"Host"}) {
		t.Fatalf("host line after self join = %v, want [Host]", q)
	}
	if queue, changed := host.PollGotNext(); !changed ||
		!reflect.DeepEqual(queue, []string{"Host"}) {
		t.Errorf("PollGotNext = %v, %v; want [Host], true", queue, changed)
	}
	// The guest folds nothing from the host's got_next.
	time.Sleep(300 * time.Millisecond)
	if q := guest.GotNextQueue(); len(q) != 0 {
		t.Errorf("guest line before broadcast = %v, want empty", q)
	}

	if err := host.BroadcastGotNextQueue(); err != nil {
		t.Fatalf("BroadcastGotNextQueue: %v", err)
	}
	if _, changed := host.PollGotNext(); changed {
		t.Error("line still dirty after the broadcast")
	}
	pollUntil(t, "guest mirror after broadcast", func() bool {
		return reflect.DeepEqual(guest.GotNextQueue(), []string{"Host"})
	})
	if err := guest.BroadcastGotNextQueue(); err == nil {
		t.Error("the guest was allowed to broadcast the line")
	}
}

// Taking a seat takes the name off the line (host side: the
// NoteSeatTaken call the UI makes with its seat_assign).
// Empty and unknown names change nothing, and the removal
// waits for the next broadcast like every other change.
func TestGotNextSeatTakenPrunes(t *testing.T) {
	r := startWatchRoom(t)

	if err := r.watcher.SendGotNext(true); err != nil {
		t.Fatalf("watcher join: %v", err)
	}
	pollUntil(t, "host folds the watcher's join", func() bool {
		return reflect.DeepEqual(r.host.GotNextQueue(), []string{"Watcher"})
	})
	if err := r.guest.SendGotNext(true); err != nil {
		t.Fatalf("guest join: %v", err)
	}
	pollUntil(t, "host line holds both in order", func() bool {
		return reflect.DeepEqual(r.host.GotNextQueue(), []string{"Watcher", "Guest"})
	})
	if err := r.host.BroadcastGotNextQueue(); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	pollUntil(t, "watcher mirror holds both", func() bool {
		return reflect.DeepEqual(r.watcher.GotNextQueue(), []string{"Watcher", "Guest"})
	})

	r.host.NoteSeatTaken("")
	r.host.NoteSeatTaken("Nobody")
	if q := r.host.GotNextQueue(); len(q) != 2 {
		t.Errorf("no-op seat notes changed the line: %v", q)
	}
	if r.host.GotNextDirty() {
		t.Error("a no-op seat note marked the line dirty")
	}

	r.host.NoteSeatTaken("Watcher")
	want := []string{"Guest"}
	if q := r.host.GotNextQueue(); !reflect.DeepEqual(q, want) {
		t.Fatalf("host line after seat taken = %v, want %v", q, want)
	}
	if !r.host.GotNextDirty() {
		t.Error("taking a seat did not mark the host line dirty")
	}
	if err := r.host.BroadcastGotNextQueue(); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	pollUntil(t, "watcher mirror pruned", func() bool {
		return reflect.DeepEqual(r.watcher.GotNextQueue(), want)
	})
}

// Leaving the room leaves the line, on every list that can
// learn of it: the host prunes on the server's spectator
// departure notice and on its peer's departure, and a
// mirror prunes a departed fellow watcher the same way.
func TestGotNextPruneOnLeave(t *testing.T) {
	r := startWatchRoom(t)

	if err := r.watcher.SendGotNext(true); err != nil {
		t.Fatalf("watcher join: %v", err)
	}
	if err := r.guest.SendGotNext(true); err != nil {
		t.Fatalf("guest join: %v", err)
	}
	pollUntil(t, "host line holds both", func() bool {
		return reflect.DeepEqual(r.host.GotNextQueue(), []string{"Watcher", "Guest"})
	})
	if err := r.host.BroadcastGotNextQueue(); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	pollUntil(t, "guest mirror holds both", func() bool {
		return reflect.DeepEqual(r.guest.GotNextQueue(), []string{"Watcher", "Guest"})
	})

	// The watcher leaves the room (its socket is still open
	// here; the server's notice is what the room acts on).
	r.hostEnd.inject(`{"type":"spectator_left","player":"Watcher"}`)
	r.guestEnd.inject(`{"type":"spectator_left","player":"Watcher"}`)
	pollUntil(t, "host prunes the departed watcher", func() bool {
		return reflect.DeepEqual(r.host.GotNextQueue(), []string{"Guest"})
	})
	if !r.host.GotNextDirty() {
		t.Error("pruning a departure did not mark the host line dirty")
	}
	pollUntil(t, "guest mirror prunes the departed watcher", func() bool {
		return reflect.DeepEqual(r.guest.GotNextQueue(), []string{"Guest"})
	})

	// The guest leaves mid match: the session ends, but the
	// name still comes off the host's line.
	r.hostEnd.inject(`{"type":"peer_left","role":"guest"}`)
	pollUntil(t, "host prunes the departed guest", func() bool {
		return len(r.host.GotNextQueue()) == 0
	})
}

// The line is room state, not match state: it is still
// standing, on both lists, after a match ends. That is the
// whole feature.
func TestGotNextSurvivesMatchEnd(t *testing.T) {
	host, guest, _, _ := startChatPair(t)

	if err := guest.SendGotNext(true); err != nil {
		t.Fatalf("guest join: %v", err)
	}
	pollUntil(t, "host folds the join", func() bool {
		return reflect.DeepEqual(host.GotNextQueue(), []string{"Guest"})
	})
	if err := host.BroadcastGotNextQueue(); err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	pollUntil(t, "guest mirror holds the line", func() bool {
		return reflect.DeepEqual(guest.GotNextQueue(), []string{"Guest"})
	})

	if err := host.SendMatchEnd("Guest lost"); err != nil {
		t.Fatalf("SendMatchEnd: %v", err)
	}
	pollUntil(t, "guest match end event", func() bool {
		_, ok := guest.PollMatchEnd()
		return ok
	})
	if host.RoomPhase() != "room" || guest.RoomPhase() != "room" {
		t.Fatal("the pair did not land back in the room")
	}
	if q := host.GotNextQueue(); !reflect.DeepEqual(q, []string{"Guest"}) {
		t.Errorf("host line after match end = %v, want [Guest]", q)
	}
	if q := guest.GotNextQueue(); !reflect.DeepEqual(q, []string{"Guest"}) {
		t.Errorf("guest line after match end = %v, want [Guest]", q)
	}
}
