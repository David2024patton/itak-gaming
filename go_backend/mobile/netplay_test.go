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

// The S5 bridge surface with nothing running: the line
// reads as the empty shape, joins and broadcasts refuse,
// and the dirty flag is down. The room and watch screens
// poll all of these outside a session too.

func TestNetplayGotNextNoSession(t *testing.T) {
	clearNetplayGlobals(t)
	if got := NetplayGotNextJSON(); got != `{"queue":[],"mine":false,"position":0}` {
		t.Errorf("NetplayGotNextJSON() = %s, want the empty shape", got)
	}
	if got := NetplaySendGotNext(true); got != "not in a session" {
		t.Errorf("NetplaySendGotNext() = %q, want %q", got, "not in a session")
	}
	if got := NetplayBroadcastGotNextQueue(); got != "not in a session" {
		t.Errorf("NetplayBroadcastGotNextQueue() = %q, want %q", got, "not in a session")
	}
	if got := NetplayNoteSeatTaken("Fan"); got != "not in a session" {
		t.Errorf("NetplayNoteSeatTaken() = %q, want %q", got, "not in a session")
	}
	if NetplayGotNextDirty() {
		t.Error("NetplayGotNextDirty() = true with no session")
	}
}

// The S6 bridge surface with nothing running: the room has
// no tournament name and naming one refuses. The waiting
// room polls the getter on its ticker, session or not.

func TestNetplayTournamentNoSession(t *testing.T) {
	clearNetplayGlobals(t)
	if got := NetplayTournamentName(); got != "" {
		t.Errorf("NetplayTournamentName() = %q, want empty", got)
	}
	if got := NetplaySendTournamentName("Friday Night Fights"); got != "not in a session" {
		t.Errorf("NetplaySendTournamentName() = %q, want %q", got, "not in a session")
	}
}
