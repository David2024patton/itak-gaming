package mobile

import "testing"

// The S4 bridge surface with nothing running: the roster
// reads as an empty list, the next match refuses, and
// leaving a seat nobody holds is a quiet no-op. The UI polls
// all three from screens that also exist outside a session,
// so none of them may panic or invent state.

func clearNetplayGlobals(t *testing.T) {
	t.Helper()
	if npSession != nil || npPending != nil || npWatch != nil {
		t.Fatal("netplay globals not clean at test start")
	}
}

func TestNetplayRosterJSONNoSession(t *testing.T) {
	clearNetplayGlobals(t)
	if got := NetplayRosterJSON(); got != `{"members":[]}` {
		t.Errorf("NetplayRosterJSON() = %s, want an empty member list", got)
	}
}

func TestNetplayStartNextMatchNoSession(t *testing.T) {
	clearNetplayGlobals(t)
	if got := NetplayStartNextMatch(); got != "no netplay session" {
		t.Errorf("NetplayStartNextMatch() = %q, want %q", got, "no netplay session")
	}
}

func TestNetplayLeaveSeatNoSession(t *testing.T) {
	clearNetplayGlobals(t)
	NetplayLeaveSeat() // must not panic
	if got := NetplayRosterJSON(); got != `{"members":[]}` {
		t.Errorf("after leaving nothing, roster = %s, want empty", got)
	}
}
