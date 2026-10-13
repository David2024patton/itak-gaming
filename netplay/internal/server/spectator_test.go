package server

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
	"github.com/davidpatton/itak-gaming/netplay/internal/ws"
)

// ---------------------------------------------------------------------------
// HTTP level
// ---------------------------------------------------------------------------

func TestWatchFlowOverHTTP(t *testing.T) {
	ts := newTestServer(t)

	status, created := post(t, ts.URL+"/lobbies",
		`{"name":"Fight night","game":"Mortal Kombat (USA).md","hostPlayer":"David","password":"pw"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %v", status, created)
	}
	id, _ := created["id"].(string)

	// Wrong password -> 401, ghost lobby -> 404, host name -> 409.
	status, _ = post(t, ts.URL+"/lobbies/"+id+"/watch", `{"player":"Fan","password":"nope"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("watch wrong password = %d, want 401", status)
	}
	status, _ = post(t, ts.URL+"/lobbies/ZZZZZ/watch", `{"player":"Fan","password":"pw"}`)
	if status != http.StatusNotFound {
		t.Fatalf("watch ghost lobby = %d, want 404", status)
	}
	status, _ = post(t, ts.URL+"/lobbies/"+id+"/watch", `{"player":"David","password":"pw"}`)
	if status != http.StatusConflict {
		t.Fatalf("watch with host name = %d, want 409", status)
	}

	// Right password -> a spectator token of its own.
	status, watched := post(t, ts.URL+"/lobbies/"+id+"/watch", `{"player":"Fan","password":"pw"}`)
	if status != http.StatusOK {
		t.Fatalf("watch = %d, want 200: %v", status, watched)
	}
	specToken, _ := watched["spectatorToken"].(string)
	if specToken == "" {
		t.Fatalf("watch response missing spectatorToken: %v", watched)
	}
	if wsURL, _ := watched["wsUrl"].(string); !strings.Contains(wsURL, "/ws?lobby="+id) {
		t.Fatalf("wsUrl bad: %v", watched)
	}
	pub, _ := watched["lobby"].(map[string]any)
	if pub["players"] != float64(1) || pub["spectators"] != float64(1) {
		t.Fatalf("lobby after watch = players %v spectators %v, want 1/1", pub["players"], pub["spectators"])
	}

	// The listing shows the spectator count too.
	resp, err := http.Get(ts.URL + "/lobbies")
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
	if len(list.Lobbies) != 1 || list.Lobbies[0].Players != 1 || list.Lobbies[0].Spectators != 1 {
		t.Fatalf("list wrong: %+v", list.Lobbies)
	}

	// The player seats are untouched: a guest still joins, and the
	// join response counts players only.
	status, joined := post(t, ts.URL+"/lobbies/"+id+"/join", `{"player":"Brother","password":"pw"}`)
	if status != http.StatusOK {
		t.Fatalf("join after watch = %d, want 200: %v", status, joined)
	}
	if tok, _ := joined["guestToken"].(string); tok == "" || tok == specToken {
		t.Fatalf("guest token bad: %v", joined)
	}
	pub, _ = joined["lobby"].(map[string]any)
	if pub["players"] != float64(2) || pub["spectators"] != float64(1) {
		t.Fatalf("lobby after join = players %v spectators %v, want 2/1", pub["players"], pub["spectators"])
	}
}

func TestWatchCapOverHTTP(t *testing.T) {
	ts := newTestServer(t)

	status, created := post(t, ts.URL+"/lobbies",
		`{"name":"L","game":"G","hostPlayer":"David"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %v", status, created)
	}
	id, _ := created["id"].(string)

	for i := range lobby.MaxSpectators {
		body := fmt.Sprintf(`{"player":"Fan%d"}`, i)
		if status, out := post(t, ts.URL+"/lobbies/"+id+"/watch", body); status != http.StatusOK {
			t.Fatalf("watch %d = %d, want 200: %v", i, status, out)
		}
	}
	status, _ = post(t, ts.URL+"/lobbies/"+id+"/watch", `{"player":"OneTooMany"}`)
	if status != http.StatusConflict {
		t.Fatalf("over-cap watch = %d, want 409", status)
	}
}

// ---------------------------------------------------------------------------
// WebSocket level
// ---------------------------------------------------------------------------

// wsTestClient is a minimal masked-frame WebSocket client: just
// enough of RFC 6455 to talk to the signaling endpoint in tests.
type wsTestClient struct {
	nc net.Conn
	br *bufio.Reader

	wmu sync.Mutex

	mu     sync.Mutex
	msgs   []string // text payloads, in arrival order
	closed bool
}

func dialWS(t *testing.T, ts *httptest.Server, lobbyID, token string) *wsTestClient {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	c := &wsTestClient{nc: nc, br: bufio.NewReader(nc)}
	req := "GET /ws?lobby=" + lobbyID + "&token=" + token + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := nc.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	status, err := c.br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("handshake status line = %q, want 101", status)
	}
	for {
		line, err := c.br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	go c.readLoop()
	t.Cleanup(func() { nc.Close() })
	return c
}

func (c *wsTestClient) readLoop() {
	for {
		op, payload, err := c.readFrame()
		if err != nil {
			c.markClosed()
			return
		}
		switch op {
		case ws.OpPing:
			_ = c.writeFrame(ws.OpPong, payload)
		case ws.OpClose:
			c.markClosed()
			return
		case ws.OpText, ws.OpBinary:
			c.mu.Lock()
			c.msgs = append(c.msgs, string(payload))
			c.mu.Unlock()
		}
	}
}

func (c *wsTestClient) readFrame() (int, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := int(hdr[0] & 0x0f)
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return op, payload, nil
}

func (c *wsTestClient) writeFrame(op int, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var hdr []byte
	switch n := len(payload); {
	case n < 126:
		hdr = []byte{0x80 | byte(op), 0x80 | byte(n)}
	case n <= 0xffff:
		hdr = make([]byte, 4)
		hdr[0] = 0x80 | byte(op)
		hdr[1] = 0x80 | 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0] = 0x80 | byte(op)
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	hdr = append(hdr, mask[:]...)
	if _, err := c.nc.Write(hdr); err != nil {
		return err
	}
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := c.nc.Write(masked)
	return err
}

// send transmits one text message.
func (c *wsTestClient) send(t *testing.T, msg string) {
	t.Helper()
	if err := c.writeFrame(ws.OpText, []byte(msg)); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// close drops the socket abruptly, the way a real disconnect looks.
func (c *wsTestClient) close() { c.nc.Close() }

func (c *wsTestClient) markClosed() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}

func (c *wsTestClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *wsTestClient) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// waitFor scans the received messages until pred matches one.
func waitFor(t *testing.T, c *wsTestClient, what string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, raw := range c.snapshot() {
			var m map[string]any
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				continue
			}
			if pred(m) {
				return m
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; received: %v", what, c.snapshot())
	return nil
}

// waitRaw waits for one exact payload, byte for byte.
func waitRaw(t *testing.T, c *wsTestClient, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, raw := range c.snapshot() {
			if raw == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for payload %q; received: %v", want, c.snapshot())
}

func waitClosed(t *testing.T, c *wsTestClient, who string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.isClosed() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s socket never closed; received: %v", who, c.snapshot())
}

// assertNone fails if any received message matches pred.
func assertNone(t *testing.T, c *wsTestClient, what string, pred func(map[string]any) bool) {
	t.Helper()
	for _, raw := range c.snapshot() {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			continue
		}
		if pred(m) {
			t.Errorf("%s unexpectedly received %s: %s", what, what, raw)
		}
	}
}

func typeIs(kind string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == kind }
}

// rosterIs matches a roster notice listing exactly these members, in
// order, as (name, role) pairs.
func rosterIs(want ...lobby.Member) func(map[string]any) bool {
	return func(m map[string]any) bool {
		if m["type"] != "roster" {
			return false
		}
		list, ok := m["members"].([]any)
		if !ok || len(list) != len(want) {
			return false
		}
		for i, w := range want {
			mm, ok := list[i].(map[string]any)
			if !ok || mm["name"] != w.Name || mm["role"] != string(w.Role) {
				return false
			}
		}
		return true
	}
}

func newStoreServer(t *testing.T) (*lobby.MemoryStore, *httptest.Server) {
	t.Helper()
	store := lobby.NewMemoryStore()
	s := New(store, log.New(io.Discard, "", 0))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return store, ts
}

// room is a full house over the store: host, guest, spectator, with
// their tokens.
type room struct {
	id         string
	hostToken  string
	guestToken string
	specToken  string
}

func newRoom(t *testing.T, store *lobby.MemoryStore) room {
	t.Helper()
	l, hostToken, err := store.Create(lobby.CreateInput{Name: "Fight night", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}
	guestToken, err := store.Join(l.ID, "Brother", "")
	if err != nil {
		t.Fatal(err)
	}
	specToken, err := store.Watch(l.ID, "Fan", "")
	if err != nil {
		t.Fatal(err)
	}
	return room{id: l.ID, hostToken: hostToken, guestToken: guestToken, specToken: specToken}
}

var (
	mDavid  = lobby.Member{Name: "David", Role: lobby.RoleHost}
	mBro    = lobby.Member{Name: "Brother", Role: lobby.RoleGuest}
	mFan    = lobby.Member{Name: "Fan", Role: lobby.RoleSpectator}
	roomAll = []lobby.Member{mDavid, mBro, mFan}
)

func TestSpectatorNoticesOverWS(t *testing.T) {
	store, ts := newStoreServer(t)
	r := newRoom(t, store)

	// Host connects: only a roster comes back, listing the seats
	// already taken over HTTP.
	host := dialWS(t, ts, r.id, r.hostToken)
	waitFor(t, host, "host roster", rosterIs(roomAll...))

	// Guest connects: host hears peer_joined for the guest and then
	// a fresh roster. The guest hears the roster only.
	guest := dialWS(t, ts, r.id, r.guestToken)
	waitFor(t, host, "peer_joined for guest", func(m map[string]any) bool {
		return m["type"] == "peer_joined" && m["role"] == "guest"
	})
	waitFor(t, guest, "guest roster", rosterIs(roomAll...))
	assertNone(t, guest, "peer_joined", typeIs("peer_joined"))

	// Spectator connects: the room hears spectator_joined naming the
	// watcher, plus a fresh roster. The spectator hears the roster
	// only, and nobody anywhere hears a peer_joined for a spectator.
	spec := dialWS(t, ts, r.id, r.specToken)
	for _, c := range []*wsTestClient{host, guest} {
		waitFor(t, c, "spectator_joined", func(m map[string]any) bool {
			return m["type"] == "spectator_joined" && m["player"] == "Fan"
		})
		waitFor(t, c, "roster after spectator joined", rosterIs(roomAll...))
	}
	waitFor(t, spec, "spectator roster", rosterIs(roomAll...))
	for _, c := range []*wsTestClient{host, guest, spec} {
		assertNone(t, c, "peer_joined for spectator", func(m map[string]any) bool {
			return m["type"] == "peer_joined" && m["role"] == "spectator"
		})
		assertNone(t, spec, "spectator_joined about itself", typeIs("spectator_joined"))
	}
}

func TestSpectatorTrafficRelays(t *testing.T) {
	store, ts := newStoreServer(t)
	r := newRoom(t, store)

	host := dialWS(t, ts, r.id, r.hostToken)
	guest := dialWS(t, ts, r.id, r.guestToken)
	spec := dialWS(t, ts, r.id, r.specToken)
	waitFor(t, spec, "spectator roster", rosterIs(roomAll...))

	// A spectator's chat reaches both players, byte for byte.
	chat := `{"type":"chat","from":"Fan","text":"gl hf"}`
	spec.send(t, chat)
	waitRaw(t, host, chat)
	waitRaw(t, guest, chat)

	// And a player's frames reach the spectator too: watching runs
	// on the same relayed stream the players exchange.
	input := `{"type":"input","frame":12,"buttons":9}`
	host.send(t, input)
	waitRaw(t, guest, input)
	waitRaw(t, spec, input)
}

func TestSpectatorDisconnectLeavesSessionAlone(t *testing.T) {
	store, ts := newStoreServer(t)
	r := newRoom(t, store)

	host := dialWS(t, ts, r.id, r.hostToken)
	guest := dialWS(t, ts, r.id, r.guestToken)
	spec := dialWS(t, ts, r.id, r.specToken)
	waitFor(t, spec, "spectator roster", rosterIs(roomAll...))

	spec.close()

	// The room hears spectator_left and a roster without the fan.
	for _, c := range []*wsTestClient{host, guest} {
		waitFor(t, c, "spectator_left", func(m map[string]any) bool {
			return m["type"] == "spectator_left" && m["player"] == "Fan"
		})
		waitFor(t, c, "roster after spectator left", rosterIs(mDavid, mBro))
		assertNone(t, c, "peer_left", typeIs("peer_left"))
		assertNone(t, c, "lobby_closed", typeIs("lobby_closed"))
	}

	// The lobby survives, the guest seat is untouched, and only the
	// spectator token died.
	if got := store.Count(); got != 1 {
		t.Fatalf("lobby count = %d, want 1", got)
	}
	if role, ok := store.Authenticate(r.id, r.guestToken); !ok || role != lobby.RoleGuest {
		t.Errorf("guest auth after spectator left = %v/%v, want guest/true", role, ok)
	}
	if _, ok := store.Authenticate(r.id, r.specToken); ok {
		t.Error("spectator token still authenticates after disconnect")
	}
	if pub, _ := store.Public(r.id); pub.Players != 2 || pub.Spectators != 0 {
		t.Errorf("players/spectators = %d/%d, want 2/0", pub.Players, pub.Spectators)
	}

	// The two players are still talking.
	still := `{"type":"input","frame":99,"buttons":1}`
	guest.send(t, still)
	waitRaw(t, host, still)
}

func TestHostDisconnectClosesRoomForEveryone(t *testing.T) {
	store, ts := newStoreServer(t)
	r := newRoom(t, store)

	host := dialWS(t, ts, r.id, r.hostToken)
	guest := dialWS(t, ts, r.id, r.guestToken)
	spec := dialWS(t, ts, r.id, r.specToken)
	waitFor(t, spec, "spectator roster", rosterIs(roomAll...))

	host.close()

	// Guest and spectator alike get lobby_closed, then the server
	// closes their sockets, and the lobby is gone.
	for _, c := range []*wsTestClient{guest, spec} {
		waitFor(t, c, "lobby_closed", typeIs("lobby_closed"))
	}
	waitClosed(t, guest, "guest")
	waitClosed(t, spec, "spectator")
	if got := store.Count(); got != 0 {
		t.Fatalf("lobby count = %d, want 0", got)
	}
}
