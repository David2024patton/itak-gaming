package netsession

import (
	"reflect"
	"testing"
)

// Before any roster notice lands, a plain two-player room
// synthesizes its list from the hello exchange: both sides
// report the same two members, host first.
func TestRosterSynthesizedFromHello(t *testing.T) {
	host, guest, _, _ := startChatPair(t)
	want := []RosterMember{
		{Name: "Host", Role: "host"},
		{Name: "Guest", Role: "guest"},
	}
	if got := host.Roster(); !reflect.DeepEqual(got, want) {
		t.Errorf("host roster = %+v, want %+v", got, want)
	}
	if got := guest.Roster(); !reflect.DeepEqual(got, want) {
		t.Errorf("guest roster = %+v, want %+v", got, want)
	}
}

// A roster notice from the server is stored whole and wins
// over the synthesis, watchers included. The returned list is
// a copy: mutating it must not corrupt the session's.
func TestRosterNoticeStoredForPlayer(t *testing.T) {
	_, guest, _, tb := startChatPair(t)
	tb.inject(`{"type":"roster","members":[` +
		`{"name":"Host","role":"host"},` +
		`{"name":"Guest","role":"guest"},` +
		`{"name":"Fan","role":"spectator"}]}`)
	want := []RosterMember{
		{Name: "Host", Role: "host"},
		{Name: "Guest", Role: "guest"},
		{Name: "Fan", Role: "spectator"},
	}
	pollUntil(t, "guest stores the roster notice", func() bool {
		return reflect.DeepEqual(guest.Roster(), want)
	})
	got := guest.Roster()
	got[0].Name = "Mutated"
	if guest.Roster()[0].Name != "Host" {
		t.Error("Roster handed out the session's own slice")
	}
}

// A later notice replaces the earlier one whole: a member
// who left is gone, not merged.
func TestRosterNoticeReplaces(t *testing.T) {
	_, guest, _, tb := startChatPair(t)
	tb.inject(`{"type":"roster","members":[` +
		`{"name":"Host","role":"host"},` +
		`{"name":"Guest","role":"guest"},` +
		`{"name":"Fan","role":"spectator"}]}`)
	pollUntil(t, "first roster stored", func() bool {
		return len(guest.Roster()) == 3
	})
	tb.inject(`{"type":"roster","members":[` +
		`{"name":"Host","role":"host"},` +
		`{"name":"Guest","role":"guest"}]}`)
	want := []RosterMember{
		{Name: "Host", Role: "host"},
		{Name: "Guest", Role: "guest"},
	}
	pollUntil(t, "second roster replaces the first", func() bool {
		return reflect.DeepEqual(guest.Roster(), want)
	})
}

// The watcher synthesizes its room from the players' hellos
// (plus itself, as a spectator) before any roster notice, and
// stores the notice whole when one lands.
func TestRosterWatchSynthesisAndNotice(t *testing.T) {
	r := startWatchRoom(t)
	wantSynth := []RosterMember{
		{Name: "Host", Role: "host"},
		{Name: "Guest", Role: "guest"},
		{Name: "Watcher", Role: "spectator"},
	}
	pollUntil(t, "watcher synthesizes from hellos", func() bool {
		return reflect.DeepEqual(r.watcher.Roster(), wantSynth)
	})

	r.watcherEnd.inject(`{"type":"roster","members":[` +
		`{"name":"Host","role":"host"},` +
		`{"name":"Guest","role":"guest"},` +
		`{"name":"Watcher","role":"spectator"},` +
		`{"name":"SecondFan","role":"spectator"}]}`)
	want := append(wantSynth, RosterMember{Name: "SecondFan", Role: "spectator"})
	pollUntil(t, "watcher stores the roster notice", func() bool {
		return reflect.DeepEqual(r.watcher.Roster(), want)
	})
}
