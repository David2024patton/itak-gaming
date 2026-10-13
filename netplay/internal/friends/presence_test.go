package friends

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
)

// fakeClock is a manually advanced clock for presence tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestPresence() (*Presence, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	p := NewPresence()
	p.now = clock.now
	return p, clock
}

func TestPresencePingAndWindow(t *testing.T) {
	p, clock := newTestPresence()

	if p.Online("Mary") {
		t.Fatal("Mary reads online before any ping")
	}
	if err := p.Ping("Mary"); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if !p.Online("Mary") {
		t.Fatal("Mary must read online right after a ping")
	}

	clock.advance(PresenceWindow - time.Second)
	if !p.Online("Mary") {
		t.Error("Mary must still read online just inside the window")
	}

	clock.advance(2 * time.Second) // now past the window
	if p.Online("Mary") {
		t.Error("Mary must read offline once the window passes")
	}

	// The expired read pruned the entry instead of leaving it.
	p.mu.Lock()
	_, kept := p.seen["Mary"]
	p.mu.Unlock()
	if kept {
		t.Error("expired entry should be pruned on read")
	}
}

func TestPresenceRefreshExtendsWindow(t *testing.T) {
	p, clock := newTestPresence()
	if err := p.Ping("Mary"); err != nil {
		t.Fatal(err)
	}
	clock.advance(80 * time.Second)
	if err := p.Ping("Mary"); err != nil {
		t.Fatal(err)
	}
	clock.advance(80 * time.Second) // 160 s since first, 80 since refresh
	if !p.Online("Mary") {
		t.Error("a fresh ping must restart the window")
	}
}

func TestPresencePingValidation(t *testing.T) {
	p, _ := newTestPresence()

	for _, tc := range []struct {
		name string
		in   string
		err  error
	}{
		{"empty", "", ErrEmptyName},
		{"blank", "   ", ErrEmptyName},
		{"too long", strings.Repeat("a", MaxNameChars+1), ErrNameLong},
		{"too long in runes", strings.Repeat("é", MaxNameChars+1), ErrNameLong},
	} {
		if err := p.Ping(tc.in); err != tc.err {
			t.Errorf("%s: Ping error = %v, want %v", tc.name, err, tc.err)
		}
		if p.Online(tc.in) {
			t.Errorf("%s: a rejected ping must not record presence", tc.name)
		}
	}

	// Exactly at the cap is fine.
	if err := p.Ping(strings.Repeat("a", MaxNameChars)); err != nil {
		t.Errorf("name at the cap: Ping error = %v, want nil", err)
	}

	// Hygiene matches the store: padding and control characters
	// are cleaned away, so the ping lands on the bare name.
	if err := p.Ping("  Mary\x00 "); err != nil {
		t.Fatalf("padded ping: %v", err)
	}
	if !p.Online("Mary") {
		t.Error("cleaned ping should record presence for Mary")
	}
	if !p.Online("  Mary ") {
		t.Error("Online should clean its argument the same way")
	}
}

func TestPresenceConcurrentUse(t *testing.T) {
	p := NewPresence() // real clock: nothing expires during the test
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("Player%d", i%8)
			for j := 0; j < 50; j++ {
				if err := p.Ping(name); err != nil {
					t.Error(err)
					return
				}
				p.Online(name)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		if !p.Online(fmt.Sprintf("Player%d", i)) {
			t.Errorf("Player%d should read online after concurrent pings", i)
		}
	}
}

// pingPresence posts to the presence endpoint and returns the
// HTTP status.
func (w *world) pingPresence(t *testing.T, body string) int {
	t.Helper()
	resp, err := http.Post(w.ts.URL+"/friends/presence", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestPresenceEndpointShowsOnline is the headline HTTP behavior:
// after a ping, the friend reads online in the list (not in a
// lobby, not offline), until the window runs out.
func TestPresenceEndpointShowsOnline(t *testing.T) {
	w := newWorld(t)
	for _, name := range []string{"Mary", "Ghost"} {
		if _, err := w.friends.Add("David", name); err != nil {
			t.Fatalf("follow %s: %v", name, err)
		}
	}

	status := w.pingPresence(t, `{"player":"Mary"}`)
	if status != http.StatusNoContent {
		t.Fatalf("POST /friends/presence status = %d, want 204", status)
	}

	_, list, _ := w.getList(t, "David")
	mary, _ := entryByName(list, "Mary")
	if mary.Status != StatusOnline {
		t.Errorf("Mary status = %q, want online after her ping", mary.Status)
	}
	if mary.Game != "" {
		t.Errorf("Mary game = %q, want empty when merely online", mary.Game)
	}
	ghost, _ := entryByName(list, "Ghost")
	if ghost.Status != StatusOffline {
		t.Errorf("Ghost status = %q, want offline (never pinged)", ghost.Status)
	}

	// Age Mary's ping past the window: she falls back to offline
	// on the next read.
	base := time.Now()
	w.handler.tracker.now = func() time.Time { return base.Add(PresenceWindow + time.Minute) }
	_, list, _ = w.getList(t, "David")
	mary, _ = entryByName(list, "Mary")
	if mary.Status != StatusOffline {
		t.Errorf("Mary status after window = %q, want offline", mary.Status)
	}
}

// TestPresenceLobbyBeatsOnline pins the precedence: a friend who
// is in a lobby AND pinged reads in_lobby with the game, never
// plain online.
func TestPresenceLobbyBeatsOnline(t *testing.T) {
	w := newWorld(t)
	const game = "Mortal Kombat (USA).md"
	if _, _, err := w.lobbies.Create(lobby.CreateInput{
		Name: "Fight night", Game: game, HostPlayer: "Mary",
	}); err != nil {
		t.Fatalf("create lobby: %v", err)
	}
	if _, err := w.friends.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	if status := w.pingPresence(t, `{"player":"Mary"}`); status != http.StatusNoContent {
		t.Fatalf("presence status = %d, want 204", status)
	}

	_, list, _ := w.getList(t, "David")
	mary, _ := entryByName(list, "Mary")
	if mary.Status != StatusInLobby || mary.Game != game {
		t.Errorf("Mary = status %q game %q, want in_lobby with %q (lobby beats app-open)", mary.Status, mary.Game, game)
	}
}

func TestPresenceEndpointValidation(t *testing.T) {
	w := newWorld(t)

	for _, tc := range []struct {
		name, body string
	}{
		{"empty player", `{"player":""}`},
		{"blank player", `{"player":"   "}`},
		{"too long", `{"player":"` + strings.Repeat("a", MaxNameChars+1) + `"}`},
		{"bad json", `{"player":`},
		{"unknown field", `{"player":"Mary","x":1}`},
	} {
		if status := w.pingPresence(t, tc.body); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, status)
		}
	}

	// The route is POST only; the mux answers 405 for other verbs.
	resp, err := http.Get(w.ts.URL + "/friends/presence")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /friends/presence: status = %d, want 405", resp.StatusCode)
	}
}

// TestPresenceWithoutLobbySource: the tracker is built into the
// handler, so app-open presence works even when the handler has
// no lobby registry or stats store behind it.
func TestPresenceWithoutLobbySource(t *testing.T) {
	store := Open("", discardLog())
	if _, err := store.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, nil, nil, discardLog())
	mux := http.NewServeMux()
	h.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/friends/presence", "application/json", bytes.NewBufferString(`{"player":"Mary"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("presence status = %d, want 204", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/friends?player=David")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Friends) != 1 || out.Friends[0].Status != StatusOnline {
		t.Errorf("list = %+v, want Mary online with no lobby source wired", out)
	}
}
