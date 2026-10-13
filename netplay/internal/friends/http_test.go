package friends

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
	"github.com/davidpatton/itak-gaming/netplay/internal/stats"
)

// world wires a friends handler to a real lobby registry and a
// real stats store, the same collaborators main wires together.
type world struct {
	ts      *httptest.Server
	friends *Store
	lobbies *lobby.MemoryStore
	stats   *stats.Store
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{
		friends: Open("", discardLog()),
		lobbies: lobby.NewMemoryStore(),
		stats:   stats.Open("", discardLog()),
	}
	h := NewHandler(w.friends, w.stats, w.lobbies, discardLog())
	mux := http.NewServeMux()
	h.Register(mux)
	w.ts = httptest.NewServer(mux)
	t.Cleanup(w.ts.Close)
	return w
}

func (w *world) getList(t *testing.T, player string) (int, ListResponse, []map[string]any) {
	t.Helper()
	resp, err := http.Get(w.ts.URL + "/friends?player=" + player)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw struct {
		Player  string           `json:"player"`
		Friends []map[string]any `json:"friends"`
	}
	var out ListResponse
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	out.Player = raw.Player
	out.Friends = make([]Entry, 0, len(raw.Friends))
	for _, m := range raw.Friends {
		b, _ := json.Marshal(m)
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		out.Friends = append(out.Friends, e)
	}
	return resp.StatusCode, out, raw.Friends
}

func (w *world) post(t *testing.T, path, body string) (int, ListResponse) {
	t.Helper()
	resp, err := http.Post(w.ts.URL+path, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out ListResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s response: %v", path, err)
		}
	}
	return resp.StatusCode, out
}

func entryByName(list ListResponse, name string) (Entry, bool) {
	for _, e := range list.Friends {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// TestPresenceAndIdentity is the headline behavior: a friend who
// is in a live lobby reads in_lobby with the game name, identity
// comes from the stats store, and everyone else reads offline.
func TestPresenceAndIdentity(t *testing.T) {
	w := newWorld(t)
	const game = "Mortal Kombat (USA).md"

	// Mary hosts a lobby for the game; Terry joins as guest.
	l, _, err := w.lobbies.Create(lobby.CreateInput{
		Name: "Fight night", Game: game, HostPlayer: "Mary",
	})
	if err != nil {
		t.Fatalf("create lobby: %v", err)
	}
	if _, err := w.lobbies.Join(l.ID, "Terry", ""); err != nil {
		t.Fatalf("join lobby: %v", err)
	}

	// Mary has reported stats with an identity; Terry reported
	// without one; Ghost has never reported anything.
	w.stats.Report(stats.Report{
		DeviceID: "tv", Player: "Mary", Game: game,
		Plays: 5, Seconds: 3600, LastPlayed: 1000,
		Display: "Mary P.", Region: "MO",
	})
	w.stats.Report(stats.Report{
		DeviceID: "tv", Player: "Terry", Game: game,
		Plays: 2, Seconds: 600, LastPlayed: 900,
	})

	for _, name := range []string{"Mary", "Terry", "Ghost"} {
		if _, err := w.friends.Add("David", name); err != nil {
			t.Fatalf("follow %s: %v", name, err)
		}
	}

	status, list, raw := w.getList(t, "David")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", status)
	}
	if list.Player != "David" || len(list.Friends) != 3 {
		t.Fatalf("list = %+v, want David with 3 friends", list)
	}

	mary, ok := entryByName(list, "Mary")
	if !ok {
		t.Fatal("Mary missing from list")
	}
	if mary.Status != StatusInLobby {
		t.Errorf("Mary status = %q, want in_lobby (she hosts a live lobby)", mary.Status)
	}
	if mary.Game != game {
		t.Errorf("Mary game = %q, want %q", mary.Game, game)
	}
	if mary.Display != "Mary P." || mary.Region != "MO" {
		t.Errorf("Mary identity = %q / %q, want Mary P. / MO from the stats store", mary.Display, mary.Region)
	}

	terry, ok := entryByName(list, "Terry")
	if !ok {
		t.Fatal("Terry missing from list")
	}
	if terry.Status != StatusInLobby || terry.Game != game {
		t.Errorf("Terry = status %q game %q, want in_lobby with %q (he is the guest)", terry.Status, terry.Game, game)
	}
	if terry.Display != "" || terry.Region != "" {
		t.Errorf("Terry identity = %q / %q, want empty (he never reported one)", terry.Display, terry.Region)
	}

	ghost, ok := entryByName(list, "Ghost")
	if !ok {
		t.Fatal("Ghost missing from list")
	}
	if ghost.Status != StatusOffline {
		t.Errorf("Ghost status = %q, want offline (not in any lobby)", ghost.Status)
	}
	if ghost.Display != "" || ghost.Region != "" || ghost.Game != "" {
		t.Errorf("Ghost = %+v, want empty identity and no game", ghost)
	}

	// The game key is omitted from the wire for offline friends.
	for _, m := range raw {
		if m["name"] == "Ghost" {
			if _, present := m["game"]; present {
				t.Errorf("offline entry carries a game field: %v", m)
			}
		}
		if m["name"] == "Mary" {
			if _, present := m["game"]; !present {
				t.Errorf("in-lobby entry is missing its game field: %v", m)
			}
		}
	}

	// Presence is live, not cached: once the lobby is gone, Mary
	// reads offline.
	w.lobbies.Delete(l.ID)
	_, list, _ = w.getList(t, "David")
	mary, _ = entryByName(list, "Mary")
	if mary.Status != StatusOffline || mary.Game != "" {
		t.Errorf("after lobby delete, Mary = status %q game %q, want offline", mary.Status, mary.Game)
	}
}

func TestAddAndRemoveOverHTTP(t *testing.T) {
	w := newWorld(t)

	status, list := w.post(t, "/friends", `{"player":"David","friend":"Mary"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /friends status = %d, want 200", status)
	}
	if len(list.Friends) != 1 || list.Friends[0].Name != "Mary" {
		t.Fatalf("list after add = %+v, want one friend Mary", list)
	}
	if list.Friends[0].Status != StatusOffline {
		t.Errorf("Mary status = %q, want offline (no lobby)", list.Friends[0].Status)
	}

	// Adding again returns the same list, no duplicate.
	status, list = w.post(t, "/friends", `{"player":"David","friend":"Mary"}`)
	if status != http.StatusOK || len(list.Friends) != 1 {
		t.Fatalf("re-add: status %d list %+v, want 200 with one friend", status, list)
	}

	status, list = w.post(t, "/friends/remove", `{"player":"David","friend":"Mary"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /friends/remove status = %d, want 200", status)
	}
	if len(list.Friends) != 0 {
		t.Errorf("list after remove = %+v, want no friends", list)
	}
	if list.Friends == nil {
		t.Error("friends must marshal as [] after the last remove, not null")
	}
}

func TestHTTPValidation(t *testing.T) {
	w := newWorld(t)

	for _, tc := range []struct {
		name, path, body string
	}{
		{"self follow", "/friends", `{"player":"David","friend":"David"}`},
		{"empty friend", "/friends", `{"player":"David","friend":""}`},
		{"empty player", "/friends", `{"player":"","friend":"Mary"}`},
		{"blank friend", "/friends", `{"player":"David","friend":"   "}`},
		{"empty remove", "/friends/remove", `{"player":"David","friend":""}`},
		{"bad json", "/friends", `{"player":`},
		{"unknown field", "/friends", `{"player":"David","friend":"Mary","x":1}`},
	} {
		status, _ := w.post(t, tc.path, tc.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, status)
		}
	}

	// GET with no player parameter.
	resp, err := http.Get(w.ts.URL + "/friends")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /friends without player: status = %d, want 400", resp.StatusCode)
	}

	// GET for a player with no list is a 200 with an empty list.
	status, list, _ := w.getList(t, "Nobody")
	if status != http.StatusOK || len(list.Friends) != 0 {
		t.Errorf("GET unknown player: status %d list %+v, want 200 with an empty list", status, list)
	}
}

func TestNilSourcesAreSafe(t *testing.T) {
	store := Open("", discardLog())
	if _, err := store.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, nil, nil, discardLog())
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/friends?player=David")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Friends) != 1 || out.Friends[0].Status != StatusOffline ||
		out.Friends[0].Display != "" || out.Friends[0].Region != "" {
		t.Errorf("list with nil sources = %+v, want Mary offline with empty identity", out)
	}
}
