package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := New(lobby.NewMemoryStore(), log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, out
}

func TestLobbyFlowOverHTTP(t *testing.T) {
	ts := newTestServer(t)

	// Health.
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}

	// Create.
	status, created := post(t, ts.URL+"/lobbies",
		`{"name":"Kart night","game":"Super Mario Kart (USA).sfc","hostPlayer":"David","password":"pw"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %v", status, created)
	}
	id, _ := created["id"].(string)
	hostToken, _ := created["hostToken"].(string)
	if id == "" || hostToken == "" {
		t.Fatalf("create response missing id/token: %v", created)
	}

	// List: the lobby shows up, locked, without secrets.
	resp, err = http.Get(ts.URL + "/lobbies")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Lobbies []lobby.PublicLobby `json:"lobbies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Lobbies) != 1 || list.Lobbies[0].ID != id || !list.Lobbies[0].Locked {
		t.Fatalf("list wrong: %+v", list.Lobbies)
	}

	// Wrong password -> 401. Right password -> token.
	status, _ = post(t, ts.URL+"/lobbies/"+id+"/join", `{"player":"Bro","password":"nope"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", status)
	}
	status, joined := post(t, ts.URL+"/lobbies/"+id+"/join", `{"player":"Bro","password":"pw"}`)
	if status != http.StatusOK {
		t.Fatalf("join = %d, want 200: %v", status, joined)
	}
	if tok, _ := joined["guestToken"].(string); tok == "" || tok == hostToken {
		t.Fatalf("guest token bad: %v", joined)
	}
	if wsURL, _ := joined["wsUrl"].(string); !strings.Contains(wsURL, "/ws?lobby="+id) {
		t.Fatalf("wsUrl bad: %v", joined)
	}

	// Lobby is full at 2 players.
	status, _ = post(t, ts.URL+"/lobbies/"+id+"/join", `{"player":"Third","password":"pw"}`)
	if status != http.StatusConflict {
		t.Fatalf("third join = %d, want 409", status)
	}

	// Joining a ghost lobby -> 404.
	status, _ = post(t, ts.URL+"/lobbies/ZZZZZ/join", `{"player":"X"}`)
	if status != http.StatusNotFound {
		t.Fatalf("ghost join = %d, want 404", status)
	}

	// WS endpoint rejects a bad token before upgrading.
	resp, err = http.Get(ts.URL + "/ws?lobby=" + id + "&token=bogus")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ws bad token = %d, want 401", resp.StatusCode)
	}
}

func TestCreateValidation(t *testing.T) {
	ts := newTestServer(t)
	for _, body := range []string{
		`{"name":"","game":"G","hostPlayer":"D"}`,
		`{"name":"N","game":"","hostPlayer":"D"}`,
		`{"name":"N","game":"G","hostPlayer":""}`,
		`{"name":"N","game":"G","hostPlayer":"D","maxPlayers":99}`,
		`{"name":"N","game":"G","hostPlayer":"D","bogusField":1}`,
	} {
		status, _ := post(t, ts.URL+"/lobbies", body)
		if status != http.StatusBadRequest {
			t.Errorf("body %s -> %d, want 400", body, status)
		}
	}
}
