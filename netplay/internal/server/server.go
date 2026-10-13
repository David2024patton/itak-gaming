// Package server is the HTTP + WebSocket front door for netplay
// lobbies: lobby CRUD over HTTP, and a signaling relay over WebSocket
// that passes client messages between the peers of a lobby verbatim.
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
	"github.com/davidpatton/itak-gaming/netplay/internal/ws"
)

// Rate limits (per client IP unless noted).
const (
	createLimit     = 10          // lobby creates per minute
	createWindow    = time.Minute //
	joinLimit       = 20          // join attempts per minute per IP+lobby
	joinWindow      = time.Minute //
	maxWSPerIP      = 10          // concurrent signaling sockets per IP
	maxBodyBytes    = 64 << 10    // HTTP request body cap
	pingInterval    = 25 * time.Second
	connIdleTimeout = 90 * time.Second
	hardMaxPlayers  = 8
)

// Server wires the lobby store, the signaling hub, and the HTTP mux.
type Server struct {
	store   lobby.Store
	hub     *hub
	log     *log.Logger
	create  *rateLimiter
	join    *rateLimiter
	wsCount *ipCounter
}

// New builds a Server on top of a lobby store.
func New(store lobby.Store, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{
		store:   store,
		hub:     newHub(),
		log:     logger,
		create:  newRateLimiter(createLimit, createWindow),
		join:    newRateLimiter(joinLimit, joinWindow),
		wsCount: newIPCounter(),
	}
}

// Handler returns the HTTP routing tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /lobbies", s.handleCreateLobby)
	mux.HandleFunc("GET /lobbies", s.handleListLobbies)
	mux.HandleFunc("POST /lobbies/{id}/join", s.handleJoinLobby)
	mux.HandleFunc("POST /lobbies/{id}/watch", s.handleWatchLobby)
	mux.HandleFunc("GET /ws", s.handleWS)
	return mux
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"lobbies": s.store.Count(),
	})
}

type createLobbyRequest struct {
	Name       string `json:"name"`
	Game       string `json:"game"`
	HostPlayer string `json:"hostPlayer"`
	Password   string `json:"password"`
	MaxPlayers int    `json:"maxPlayers"`
}

func (s *Server) handleCreateLobby(w http.ResponseWriter, r *http.Request) {
	if !s.create.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many lobbies created; slow down")
		return
	}
	var req createLobbyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch {
	case len(req.Name) == 0 || len(req.Name) > 64:
		writeError(w, http.StatusBadRequest, "name must be 1-64 characters")
		return
	case len(req.Game) == 0 || len(req.Game) > 256:
		writeError(w, http.StatusBadRequest, "game must be 1-256 characters")
		return
	case len(req.HostPlayer) == 0 || len(req.HostPlayer) > 32:
		writeError(w, http.StatusBadRequest, "hostPlayer must be 1-32 characters")
		return
	case len(req.Password) > 128:
		writeError(w, http.StatusBadRequest, "password too long")
		return
	case req.MaxPlayers != 0 && (req.MaxPlayers < 2 || req.MaxPlayers > hardMaxPlayers):
		writeError(w, http.StatusBadRequest, "maxPlayers must be between 2 and 8")
		return
	}

	l, hostToken, err := s.store.Create(lobby.CreateInput{
		Name:       req.Name,
		Game:       req.Game,
		HostPlayer: req.HostPlayer,
		Password:   req.Password,
		MaxPlayers: req.MaxPlayers,
	})
	if errors.Is(err, lobby.ErrTooMany) {
		writeError(w, http.StatusServiceUnavailable, "server is full of lobbies; try again later")
		return
	}
	if err != nil {
		s.log.Printf("create lobby: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create lobby")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":        l.ID,
		"hostToken": hostToken,
		"wsUrl":     wsURL(l.ID, hostToken),
		"lobby":     l.Public(),
	})
}

func (s *Server) handleListLobbies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"lobbies": s.store.List(),
	})
}

type joinLobbyRequest struct {
	Player   string `json:"player"`
	Password string `json:"password"`
}

func (s *Server) handleJoinLobby(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Every attempt counts against the limit, right or wrong: this is
	// the password-guessing brake.
	if !s.join.allow(clientIP(r) + ":" + id) {
		writeError(w, http.StatusTooManyRequests, "too many join attempts; slow down")
		return
	}
	var req joinLobbyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Player) == 0 || len(req.Player) > 32 {
		writeError(w, http.StatusBadRequest, "player must be 1-32 characters")
		return
	}

	token, err := s.store.Join(id, req.Player, req.Password)
	switch {
	case errors.Is(err, lobby.ErrNotFound):
		writeError(w, http.StatusNotFound, "lobby not found")
		return
	case errors.Is(err, lobby.ErrBadPassword):
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	case errors.Is(err, lobby.ErrFull):
		writeError(w, http.StatusConflict, "lobby is full")
		return
	case errors.Is(err, lobby.ErrNameTaken):
		writeError(w, http.StatusConflict, "that name is already in the lobby")
		return
	case err != nil:
		s.log.Printf("join lobby: %v", err)
		writeError(w, http.StatusInternalServerError, "could not join lobby")
		return
	}
	public, _ := s.store.Public(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"guestToken": token,
		"wsUrl":      wsURL(id, token),
		"lobby":      public,
	})
}

// handleWatchLobby admits a spectator. Same request shape and the
// same password brake as a guest join; the difference is the seat:
// a spectator gets a spectator token, never a player seat.
func (s *Server) handleWatchLobby(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.join.allow(clientIP(r) + ":" + id) {
		writeError(w, http.StatusTooManyRequests, "too many join attempts; slow down")
		return
	}
	var req joinLobbyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Player) == 0 || len(req.Player) > 32 {
		writeError(w, http.StatusBadRequest, "player must be 1-32 characters")
		return
	}

	token, err := s.store.Watch(id, req.Player, req.Password)
	switch {
	case errors.Is(err, lobby.ErrNotFound):
		writeError(w, http.StatusNotFound, "lobby not found")
		return
	case errors.Is(err, lobby.ErrBadPassword):
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	case errors.Is(err, lobby.ErrSpectatorsFull):
		writeError(w, http.StatusConflict, "spectator seats are full")
		return
	case errors.Is(err, lobby.ErrNameTaken):
		writeError(w, http.StatusConflict, "that name is already in the lobby")
		return
	case err != nil:
		s.log.Printf("watch lobby: %v", err)
		writeError(w, http.StatusInternalServerError, "could not watch lobby")
		return
	}
	public, _ := s.store.Public(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"spectatorToken": token,
		"wsUrl":          wsURL(id, token),
		"lobby":          public,
	})
}

// ---------------------------------------------------------------------------
// WebSocket signaling relay
// ---------------------------------------------------------------------------

// handleWS upgrades the connection after authenticating the token. From
// then on, every message the client sends is relayed verbatim to the
// other peers in the lobby: SDP offers/answers, ICE candidates, and (as
// a fallback path) lockstep "input"/"relay" frames all look the same to
// this server. The direct-vs-relay choice belongs to the clients.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	lobbyID := r.URL.Query().Get("lobby")
	token := r.URL.Query().Get("token")
	self, ok := s.store.LookupMember(lobbyID, token)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid lobby or token")
		return
	}
	role := self.Role
	ip := clientIP(r)
	if !s.wsCount.acquire(ip, maxWSPerIP) {
		writeError(w, http.StatusTooManyRequests, "too many connections from this address")
		return
	}
	defer s.wsCount.release(ip)

	conn, err := ws.Upgrade(w, r)
	if err != nil {
		s.log.Printf("ws upgrade from %s: %v", ip, err)
		return
	}
	p := &peer{conn: conn, lobbyID: lobbyID, role: role, player: self.Name, token: token}

	count := s.hub.add(p)
	s.store.SetPeers(lobbyID, count)
	s.log.Printf("ws: %s joined lobby %s (%d peers)", role, lobbyID, count)
	// Spectators get their own arrival notice. peer_joined stays
	// player-only: player clients read it as "my opponent is here",
	// and a watcher is not an opponent. Everyone then gets the full
	// roster, the new arrival included.
	if role == lobby.RoleSpectator {
		s.hub.broadcastOthers(p, playerMsg("spectator_joined", p.player))
	} else {
		s.hub.broadcastOthers(p, controlMsg("peer_joined", role))
	}
	s.sendRoster(p)

	defer s.teardown(p)
	s.servePeer(p)
}

// servePeer runs the read/ping loop for one connected peer.
func (s *Server) servePeer(p *peer) {
	conn := p.conn
	extendDeadline := func() {
		_ = conn.SetReadDeadline(time.Now().Add(connIdleTimeout))
	}
	conn.OnPong = extendDeadline
	extendDeadline()

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteMessage(ws.OpPing, nil); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		extendDeadline()
		s.hub.relay(p, op, data)
	}
}

// teardown handles a peer's socket going away.
//
// Host leaving ends the session for everyone, spectators included:
// the room is told the lobby closed, every socket is closed, and
// the lobby is deleted. A guest leaving frees their slot: the host
// is notified, the guest's membership token is revoked, and the
// lobby stays open for a replacement guest (the reaper collects it
// later if the host abandons it too). A spectator leaving is the
// quiet case: their token is revoked and the room hears about it,
// but no seat frees and the session carries on untouched. Guests
// and spectators both end with a fresh roster for the room.
func (s *Server) teardown(p *peer) {
	remaining := s.hub.remove(p)
	p.conn.Close()

	if p.role == lobby.RoleHost {
		s.hub.broadcastOthers(p, controlMsg("lobby_closed", p.role))
		for _, q := range s.hub.others(p) {
			q.conn.Close()
			s.hub.remove(q)
		}
		s.store.Delete(p.lobbyID)
		s.log.Printf("ws: host left, lobby %s closed", p.lobbyID)
		return
	}

	s.store.RemoveMemberByToken(p.lobbyID, p.token)
	s.store.SetPeers(p.lobbyID, remaining)
	if p.role == lobby.RoleSpectator {
		s.hub.broadcastOthers(p, playerMsg("spectator_left", p.player))
		s.sendRoster(p)
		s.log.Printf("ws: spectator left lobby %s (%d peers)", p.lobbyID, remaining)
		return
	}
	s.hub.broadcastOthers(p, controlMsg("peer_left", p.role))
	s.sendRoster(p)
	s.log.Printf("ws: guest left lobby %s (%d peers)", p.lobbyID, remaining)
}

// sendRoster delivers the room's current roster to every socket in
// p's room. It runs on every membership change so all clients,
// players and watchers alike, render the same room. The roster comes
// from lobby membership (names and roles), not from socket state:
// someone who joined over HTTP is on it before their socket lands.
func (s *Server) sendRoster(p *peer) {
	members, ok := s.store.Members(p.lobbyID)
	if !ok {
		return
	}
	msg := rosterMsg(members)
	for _, q := range s.hub.all(p) {
		s.hub.notify(q, msg)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// controlMsg builds a server-generated control envelope. Client
// messages pass through untouched; only these lifecycle notices are
// minted by the server.
func controlMsg(kind string, role lobby.Role) []byte {
	b, _ := json.Marshal(map[string]string{"type": kind, "role": string(role)})
	return b
}

// playerMsg builds a server-generated envelope naming one member,
// used by the spectator arrival and departure notices.
func playerMsg(kind string, player string) []byte {
	b, _ := json.Marshal(map[string]string{"type": kind, "player": player})
	return b
}

// rosterMsg builds the roster notice: the room's full member list,
// names and roles, in join order.
func rosterMsg(members []lobby.Member) []byte {
	b, _ := json.Marshal(map[string]any{"type": "roster", "members": members})
	return b
}

func wsURL(lobbyID, token string) string {
	return "/ws?lobby=" + lobbyID + "&token=" + token
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ipCounter tracks concurrent connections per IP.
type ipCounter struct {
	mu sync.Mutex
	m  map[string]int
}

func newIPCounter() *ipCounter { return &ipCounter{m: make(map[string]int)} }

func (c *ipCounter) acquire(ip string, max int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[ip] >= max {
		return false
	}
	c.m[ip]++
	return true
}

func (c *ipCounter) release(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[ip] <= 1 {
		delete(c.m, ip)
	} else {
		c.m[ip]--
	}
}
