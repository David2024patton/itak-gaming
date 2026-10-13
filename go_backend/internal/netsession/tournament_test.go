package netsession

import (
	"strings"
	"testing"
	"time"
)

// The name end to end in a three-party room: the host sends
// it, its own session holds it at once, and the guest and
// the watcher learn it from the wire, stamped by the host.
func TestTournamentNameBroadcast(t *testing.T) {
	r := startWatchRoom(t)

	if name := r.host.TournamentName(); name != "" {
		t.Fatalf("host tournament name before send = %q, want empty", name)
	}
	if err := r.host.SendTournamentName("Friday Night Fights"); err != nil {
		t.Fatalf("host SendTournamentName: %v", err)
	}
	if name := r.host.TournamentName(); name != "Friday Night Fights" {
		t.Fatalf("host tournament name after send = %q", name)
	}
	for label, s := range map[string]*Session{"guest": r.guest, "watcher": r.watcher} {
		pollUntil(t, label+" learns the tournament name", func() bool {
			return s.TournamentName() == "Friday Night Fights"
		})
	}
}

// Only the host names the tournament. The send API refuses
// the guest and the watcher outright, and an empty name
// names nothing.
func TestTournamentNameSendGate(t *testing.T) {
	r := startWatchRoom(t)

	if err := r.guest.SendTournamentName("Guest Night"); err == nil {
		t.Error("the guest was allowed to name the tournament")
	}
	if err := r.watcher.SendTournamentName("Watcher Night"); err == nil {
		t.Error("a spectator was allowed to name the tournament")
	}
	if err := r.host.SendTournamentName(""); err == nil {
		t.Error("an empty name was allowed")
	}
	if err := r.host.SendTournamentName("   "); err == nil {
		t.Error("a blank name was allowed")
	}
	time.Sleep(300 * time.Millisecond)
	for label, s := range map[string]*Session{
		"host": r.host, "guest": r.guest, "watcher": r.watcher,
	} {
		if name := s.TournamentName(); name != "" {
			t.Errorf("%s tournament name = %q, want empty", label, name)
		}
	}
}

// A forged call moves nobody. Receivers believe the
// envelope only on the known host's stamp: a spectator's
// claim at the guest, the guest's claim at the watcher and
// at the host itself, and an unstamped claim are all
// dropped, while the same envelope carrying the host's
// stamp is adopted.
func TestTournamentNameForgedIgnored(t *testing.T) {
	r := startWatchRoom(t)

	r.guestEnd.inject(`{"type":"tournament","name":"Fake Night","from":"Watcher"}`)
	r.watcherEnd.inject(`{"type":"tournament","name":"Fake Night","from":"Guest"}`)
	r.hostEnd.inject(`{"type":"tournament","name":"Fake Night","from":"Guest"}`)
	r.guestEnd.inject(`{"type":"tournament","name":"Fake Night"}`)
	time.Sleep(300 * time.Millisecond)
	for label, s := range map[string]*Session{
		"host": r.host, "guest": r.guest, "watcher": r.watcher,
	} {
		if name := s.TournamentName(); name != "" {
			t.Errorf("%s adopted a forged tournament name: %q", label, name)
		}
	}

	r.guestEnd.inject(`{"type":"tournament","name":"Real Night","from":"Host"}`)
	r.watcherEnd.inject(`{"type":"tournament","name":"Real Night","from":"Host"}`)
	pollUntil(t, "guest adopts the host-stamped name", func() bool {
		return r.guest.TournamentName() == "Real Night"
	})
	pollUntil(t, "watcher adopts the host-stamped name", func() bool {
		return r.watcher.TournamentName() == "Real Night"
	})
}

// Names are trimmed and capped at the server's 80 character
// limit, in runes, on the way in and on the way out.
func TestTournamentNameCleaning(t *testing.T) {
	r := startWatchRoom(t)

	long := strings.Repeat("é", 100)
	if err := r.host.SendTournamentName("  " + long + "  "); err != nil {
		t.Fatalf("host SendTournamentName: %v", err)
	}
	want := strings.Repeat("é", 80)
	if name := r.host.TournamentName(); name != want {
		t.Fatalf("host name = %d runes, want the trimmed 80", len([]rune(name)))
	}
	pollUntil(t, "watcher learns the cleaned name", func() bool {
		return r.watcher.TournamentName() == want
	})
}

// The name is room state: it is still standing on both
// players after a match ends, like the Got Next line the
// next match is played for.
func TestTournamentNameSurvivesMatchEnd(t *testing.T) {
	host, guest, _, _ := startChatPair(t)

	if err := host.SendTournamentName("Friday Night Fights"); err != nil {
		t.Fatalf("host SendTournamentName: %v", err)
	}
	pollUntil(t, "guest learns the name", func() bool {
		return guest.TournamentName() == "Friday Night Fights"
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
	if name := host.TournamentName(); name != "Friday Night Fights" {
		t.Errorf("host name after match end = %q", name)
	}
	if name := guest.TournamentName(); name != "Friday Night Fights" {
		t.Errorf("guest name after match end = %q", name)
	}
}
