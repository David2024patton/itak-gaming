package matches

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davidpatton/itak-gaming/netplay/internal/stats"
)

// world wires a matches handler to a real stats store as its
// identity source, the same collaborator main wires in.
type world struct {
	ts      *httptest.Server
	handler *Handler
	matches *Store
	stats   *stats.Store
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{
		matches: Open("", discardLog()),
		stats:   stats.Open("", discardLog()),
	}
	h := NewHandler(w.matches, w.stats, discardLog())
	w.handler = h
	mux := http.NewServeMux()
	h.Register(mux)
	w.ts = httptest.NewServer(mux)
	t.Cleanup(w.ts.Close)
	return w
}

func (w *world) post(t *testing.T, path, body string) int {
	t.Helper()
	resp, err := http.Post(w.ts.URL+path, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func (w *world) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(w.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func (w *world) leaderboard(t *testing.T) LeaderboardResponse {
	t.Helper()
	code, body := w.get(t, "/matches/leaderboard")
	if code != http.StatusOK {
		t.Fatalf("GET /matches/leaderboard = %d, want 200 (%s)", code, body)
	}
	var out LeaderboardResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode leaderboard: %v", err)
	}
	return out
}

func (w *world) recent(t *testing.T) RecentResponse {
	t.Helper()
	code, body := w.get(t, "/matches/recent")
	if code != http.StatusOK {
		t.Fatalf("GET /matches/recent = %d, want 200 (%s)", code, body)
	}
	var out RecentResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode recent: %v", err)
	}
	return out
}

func boardRow(board LeaderboardResponse, name string) (PlayerRecord, bool) {
	for _, rec := range board.Players {
		if rec.Name == name {
			return rec, true
		}
	}
	return PlayerRecord{}, false
}

func TestReportAndBoardOverHTTP(t *testing.T) {
	w := newWorld(t)
	code := w.post(t, "/matches/report",
		`{"tournament":"Friday Night Fights","game":"Mortal Kombat (USA).md",`+
			`"winner":"David","loser":"Brother","host":"David"}`)
	if code != http.StatusNoContent {
		t.Fatalf("POST /matches/report = %d, want 204", code)
	}

	board := w.leaderboard(t)
	if len(board.Players) != 2 {
		t.Fatalf("board = %+v, want 2 players", board.Players)
	}
	// Equal titles and matches: the win puts David first.
	if board.Players[0].Name != "David" || board.Players[1].Name != "Brother" {
		t.Errorf("board order = %q, %q; want David, Brother",
			board.Players[0].Name, board.Players[1].Name)
	}
	david, _ := boardRow(board, "David")
	if david.Wins != 1 || david.Matches != 1 || david.Losses != 0 ||
		david.LastTournament != "Friday Night Fights" || david.LastGame != "Mortal Kombat (USA).md" {
		t.Errorf("David row = %+v, want 1 win in Friday Night Fights", david)
	}

	recent := w.recent(t)
	if len(recent.Recent) != 1 {
		t.Fatalf("recent = %+v, want 1 match", recent.Recent)
	}
	got := recent.Recent[0]
	if got.Tournament != "Friday Night Fights" || got.Game != "Mortal Kombat (USA).md" ||
		got.Winner != "David" || got.Loser != "Brother" || got.At == 0 {
		t.Errorf("recent[0] = %+v, want the reported match with a timestamp", got)
	}

	// A champion report moves Brother to the top of the board:
	// titles outrank wins.
	code = w.post(t, "/matches/champion",
		`{"tournament":"Friday Night Fights","champion":"Brother","host":"David"}`)
	if code != http.StatusNoContent {
		t.Fatalf("POST /matches/champion = %d, want 204", code)
	}
	board = w.leaderboard(t)
	if board.Players[0].Name != "Brother" || board.Players[0].TournamentsWon != 1 {
		t.Errorf("board head = %+v, want Brother with 1 title", board.Players[0])
	}
	// The champion report is not a match: the feed still holds 1.
	if recent := w.recent(t); len(recent.Recent) != 1 {
		t.Errorf("recent after champion = %d rows, want 1", len(recent.Recent))
	}
}

func TestRecentNewestFirstOverHTTP(t *testing.T) {
	w := newWorld(t)
	for _, body := range []string{
		`{"tournament":"Cup","game":"A (USA).md","winner":"David","loser":"Brother","host":"David"}`,
		`{"tournament":"Cup","game":"B (USA).md","winner":"Brother","loser":"David","host":"David"}`,
		`{"tournament":"Cup","game":"C (USA).md","winner":"David","loser":"Mary","host":"David"}`,
	} {
		if code := w.post(t, "/matches/report", body); code != http.StatusNoContent {
			t.Fatalf("report = %d, want 204", code)
		}
	}
	recent := w.recent(t)
	if len(recent.Recent) != 3 {
		t.Fatalf("recent = %d rows, want 3", len(recent.Recent))
	}
	if recent.Recent[0].Game != "C (USA).md" || recent.Recent[2].Game != "A (USA).md" {
		t.Errorf("recent order = %q .. %q, want newest (C) first",
			recent.Recent[0].Game, recent.Recent[2].Game)
	}
}

func TestIdentityFromReportAndStatsFallback(t *testing.T) {
	w := newWorld(t)
	// Mary has an identity on the playtime board but her match
	// report carries none: the board falls back to it.
	w.stats.Report(stats.Report{
		DeviceID: "box-1", Player: "Mary", Game: "Tetris (USA).gb",
		Plays: 3, Seconds: 900, LastPlayed: 1000,
		Display: "Mary P.", Region: "MO",
	})
	code := w.post(t, "/matches/report",
		`{"tournament":"Cup","game":"Mortal Kombat (USA).md","winner":"David","loser":"Mary","host":"David",`+
			`"winnerDisplay":"David P.","winnerRegion":"KY"}`)
	if code != http.StatusNoContent {
		t.Fatalf("report = %d, want 204", code)
	}
	board := w.leaderboard(t)
	david, _ := boardRow(board, "David")
	if david.Display != "David P." || david.Region != "KY" {
		t.Errorf("David identity = %q/%q, want from the match report", david.Display, david.Region)
	}
	mary, _ := boardRow(board, "Mary")
	if mary.Display != "Mary P." || mary.Region != "MO" {
		t.Errorf("Mary identity = %q/%q, want the stats fallback", mary.Display, mary.Region)
	}

	// A record's own identity beats the fallback: a later report
	// carrying one for Mary replaces what the board shows.
	w.stats.Report(stats.Report{
		DeviceID: "box-1", Player: "David", Game: "Tetris (USA).gb",
		Plays: 1, Seconds: 60, LastPlayed: 1000,
		Display: "David From Stats", Region: "ZZ",
	})
	code = w.post(t, "/matches/report",
		`{"tournament":"Cup","game":"Mortal Kombat (USA).md","winner":"Mary","loser":"David","host":"David"}`)
	if code != http.StatusNoContent {
		t.Fatalf("report = %d, want 204", code)
	}
	board = w.leaderboard(t)
	david, _ = boardRow(board, "David")
	if david.Display != "David P." || david.Region != "KY" {
		t.Errorf("David identity = %q/%q, want the match report's to win over the fallback",
			david.Display, david.Region)
	}
}

func TestNilIdentitySourceIsSafe(t *testing.T) {
	store := Open("", discardLog())
	h := NewHandler(store, nil, discardLog())
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/matches/report", "application/json",
		strings.NewReader(`{"tournament":"Cup","game":"G (USA).md","winner":"David","loser":"Brother","host":"David"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("report = %d, want 204", resp.StatusCode)
	}
	resp, err = http.Get(ts.URL + "/matches/leaderboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var board LeaderboardResponse
	if err := json.NewDecoder(resp.Body).Decode(&board); err != nil {
		t.Fatal(err)
	}
	if len(board.Players) != 2 || board.Players[0].Display != "" {
		t.Errorf("board = %+v, want 2 players with empty identities", board.Players)
	}
}

func TestHTTPValidation(t *testing.T) {
	w := newWorld(t)
	bad := []struct {
		name, path, body string
	}{
		{"malformed JSON", "/matches/report", `{"winner":`},
		{"unknown field", "/matches/report",
			`{"tournament":"Cup","game":"G","winner":"David","loser":"Brother","host":"David","ranked":true}`},
		{"empty tournament", "/matches/report",
			`{"tournament":"","game":"G","winner":"David","loser":"Brother","host":"David"}`},
		{"empty winner", "/matches/report",
			`{"tournament":"Cup","game":"G","winner":"","loser":"Brother","host":"David"}`},
		{"empty loser", "/matches/report",
			`{"tournament":"Cup","game":"G","winner":"David","loser":"","host":"David"}`},
		{"empty host", "/matches/report",
			`{"tournament":"Cup","game":"G","winner":"David","loser":"Brother","host":""}`},
		{"self match", "/matches/report",
			`{"tournament":"Cup","game":"G","winner":"David","loser":"David","host":"David"}`},
		{"empty champion", "/matches/champion",
			`{"tournament":"Cup","champion":"","host":"David"}`},
		{"empty champion tournament", "/matches/champion",
			`{"tournament":"","champion":"David","host":"David"}`},
		{"empty champion host", "/matches/champion",
			`{"tournament":"Cup","champion":"David","host":""}`},
	}
	for _, tc := range bad {
		if code := w.post(t, tc.path, tc.body); code != http.StatusBadRequest {
			t.Errorf("%s: POST %s = %d, want 400", tc.name, tc.path, code)
		}
	}
	if w.matches.PlayerCount() != 0 {
		t.Errorf("PlayerCount = %d, want 0: rejected reports record nothing", w.matches.PlayerCount())
	}

	// Wrong verbs are the mux's job: 405.
	resp, err := http.Get(w.ts.URL + "/matches/report")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /matches/report = %d, want 405", resp.StatusCode)
	}
}

func TestEmptyBoardsOverHTTP(t *testing.T) {
	w := newWorld(t)
	_, body := w.get(t, "/matches/leaderboard")
	if !strings.Contains(string(body), `"players":[]`) {
		t.Errorf("leaderboard body = %s, want an empty players array, not null", body)
	}
	_, body = w.get(t, "/matches/recent")
	if !strings.Contains(string(body), `"recent":[]`) {
		t.Errorf("recent body = %s, want an empty recent array, not null", body)
	}
}
