package stats

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestHandler(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	store := Open("", discardLog())
	h := NewHandler(store, discardLog())
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store
}

func postRaw(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func getJSON[T any](t *testing.T, url string) (int, string, T) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), out
}

func TestReportAndLeaderboardRoundTrip(t *testing.T) {
	ts, _ := newTestHandler(t)

	// Two devices report; David appears on both.
	for _, body := range []string{
		`{"deviceId":"tv","player":"David","game":"SMW","plays":3,"seconds":600,"lastPlayed":1000}`,
		`{"deviceId":"handheld","player":"David","game":"SMW","plays":1,"seconds":100,"lastPlayed":2000}`,
		`{"deviceId":"tv","player":"Mary","game":"Kart","plays":2,"seconds":300,"lastPlayed":1500}`,
	} {
		resp := postRaw(t, ts.URL+"/stats/report", body)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("report status = %d, want 204", resp.StatusCode)
		}
	}

	status, ct, players := getJSON[[]PlayerStat](t, ts.URL+"/leaderboard/players")
	if status != 200 {
		t.Fatalf("players status = %d, want 200", status)
	}
	if ct != "application/json" {
		t.Errorf("players content type = %q, want application/json", ct)
	}
	if len(players) != 2 || players[0].Player != "David" ||
		players[0].Seconds != 700 || players[0].Plays != 4 || players[0].Games != 1 {
		t.Errorf("players board = %+v, want David 700s/4 plays/1 game first", players)
	}

	_, _, games := getJSON[[]GameStat](t, ts.URL+"/leaderboard/games")
	if len(games) != 2 || games[0].Game != "SMW" || games[0].Seconds != 700 || games[0].Players != 1 {
		t.Errorf("games board = %+v, want SMW 700s 1 player first", games)
	}

	_, _, recent := getJSON[[]RecentEntry](t, ts.URL+"/leaderboard/recent")
	if len(recent) != 3 || recent[0].LastPlayed != 2000 || recent[0].Player != "David" {
		t.Errorf("recent = %+v, want David's 2000 entry first", recent)
	}

	// A stale re-report is accepted (204) but does not roll the
	// board back.
	resp := postRaw(t, ts.URL+"/stats/report",
		`{"deviceId":"tv","player":"David","game":"SMW","plays":1,"seconds":50,"lastPlayed":500}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stale report status = %d, want 204", resp.StatusCode)
	}
	_, _, players = getJSON[[]PlayerStat](t, ts.URL+"/leaderboard/players")
	if players[0].Seconds != 700 {
		t.Errorf("after stale report David seconds = %d, want 700", players[0].Seconds)
	}
}

func TestReportIdentityRoundTrip(t *testing.T) {
	ts, _ := newTestHandler(t)

	// A report carrying the optional identity fields surfaces them
	// on both the players board and the recent feed.
	resp := postRaw(t, ts.URL+"/stats/report",
		`{"deviceId":"tv","player":"David","game":"SMW","plays":3,"seconds":600,"lastPlayed":1000,"display":"David M. P.","region":"KY"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("report status = %d, want 204", resp.StatusCode)
	}
	_, _, players := getJSON[[]PlayerStat](t, ts.URL+"/leaderboard/players")
	if len(players) != 1 || players[0].Display != "David M. P." || players[0].Region != "KY" {
		t.Errorf("players board = %+v, want identity David M. P. / KY", players)
	}
	_, _, recent := getJSON[[]RecentEntry](t, ts.URL+"/leaderboard/recent")
	if len(recent) != 1 || recent[0].Display != "David M. P." || recent[0].Region != "KY" {
		t.Errorf("recent = %+v, want identity David M. P. / KY", recent)
	}

	// An over-long display name is rejected.
	resp = postRaw(t, ts.URL+"/stats/report",
		`{"deviceId":"tv","player":"David","game":"SMW","plays":3,"seconds":600,"lastPlayed":1000,"display":"`+strings.Repeat("x", MaxDisplayChars+1)+`"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("over-long display status = %d, want 400", resp.StatusCode)
	}
}

func TestReportValidation(t *testing.T) {
	ts, _ := newTestHandler(t)
	cases := map[string]string{
		"malformed JSON":   `{`,
		"unknown field":    `{"deviceId":"d","player":"P","game":"G","plays":0,"seconds":0,"lastPlayed":0,"nope":1}`,
		"missing deviceId": `{"player":"P","game":"G","plays":0,"seconds":0,"lastPlayed":0}`,
		"missing player":   `{"deviceId":"d","game":"G","plays":0,"seconds":0,"lastPlayed":0}`,
		"missing game":     `{"deviceId":"d","player":"P","plays":0,"seconds":0,"lastPlayed":0}`,
		"blank player":     `{"deviceId":"d","player":"   ","game":"G","plays":0,"seconds":0,"lastPlayed":0}`,
		"player too long":  `{"deviceId":"d","player":"` + strings.Repeat("x", 41) + `","game":"G","plays":0,"seconds":0,"lastPlayed":0}`,
		"game too long":    `{"deviceId":"d","player":"P","game":"` + strings.Repeat("g", 121) + `","plays":0,"seconds":0,"lastPlayed":0}`,
		"negative plays":   `{"deviceId":"d","player":"P","game":"G","plays":-1,"seconds":0,"lastPlayed":0}`,
		"negative seconds": `{"deviceId":"d","player":"P","game":"G","plays":0,"seconds":-5,"lastPlayed":0}`,
		"float plays":      `{"deviceId":"d","player":"P","game":"G","plays":1.5,"seconds":0,"lastPlayed":0}`,
	}
	for name, body := range cases {
		resp := postRaw(t, ts.URL+"/stats/report", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}

	// Oversized body: 16 KB cap.
	big := `{"deviceId":"d","player":"` + strings.Repeat("y", 20000) + `","game":"G","plays":0,"seconds":0,"lastPlayed":0}`
	resp := postRaw(t, ts.URL+"/stats/report", big)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized body: status = %d, want 400", resp.StatusCode)
	}
}

func TestReportSanitizesAndClamps(t *testing.T) {
	ts, store := newTestHandler(t)
	// Control characters are stripped from display strings, and an
	// absurd cumulative seconds value is clamped to MaxValue.
	body := `{"deviceId":"tv","player":"Da\nvid","game":"S\u0000MW","plays":1,"seconds":99999999999,"lastPlayed":42}`
	resp := postRaw(t, ts.URL+"/stats/report", body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	players := store.TopPlayers(BoardSize)
	if len(players) != 1 || players[0].Player != "David" {
		t.Fatalf("players = %+v, want sanitized David", players)
	}
	if players[0].Seconds != MaxValue {
		t.Errorf("seconds = %d, want clamped %d", players[0].Seconds, MaxValue)
	}
	games := store.TopGames(BoardSize)
	if len(games) != 1 || games[0].Game != "SMW" {
		t.Errorf("games = %+v, want sanitized SMW", games)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	ts, _ := newTestHandler(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/stats/report", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /stats/report = %d, want 405", resp.StatusCode)
	}

	resp2 := postRaw(t, ts.URL+"/leaderboard/players", `{}`)
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /leaderboard/players = %d, want 405", resp2.StatusCode)
	}
}

func TestReportRateLimitPerDevice(t *testing.T) {
	store := Open("", discardLog())
	h := &Handler{
		store:   store,
		log:     discardLog(),
		limiter: newDeviceLimiter(2, time.Minute),
	}
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	body := `{"deviceId":"noisy","player":"P","game":"G","plays":1,"seconds":1,"lastPlayed":1}`
	for i := 0; i < 2; i++ {
		resp := postRaw(t, ts.URL+"/stats/report", body)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("report %d status = %d, want 204", i, resp.StatusCode)
		}
	}
	resp := postRaw(t, ts.URL+"/stats/report", body)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("third report status = %d, want 429", resp.StatusCode)
	}
	// A different device is unaffected.
	other := `{"deviceId":"quiet","player":"P","game":"G","plays":1,"seconds":1,"lastPlayed":1}`
	resp = postRaw(t, ts.URL+"/stats/report", other)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("other device status = %d, want 204", resp.StatusCode)
	}
}

func TestEmptyLeaderboardReturnsArray(t *testing.T) {
	ts, _ := newTestHandler(t)
	resp, err := http.Get(ts.URL + "/leaderboard/players")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(data)) != "[]" {
		t.Errorf("empty board body = %s, want []", data)
	}
}
