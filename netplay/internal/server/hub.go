package server

import (
	"sync"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
	"github.com/davidpatton/itak-gaming/netplay/internal/ws"
)

// peer is one connected signaling socket in a lobby room.
type peer struct {
	conn    *ws.Conn
	lobbyID string
	role    lobby.Role
	token   string // kept only to revoke membership on disconnect
}

// hub tracks live sockets per lobby and relays messages between them.
// Messages are opaque: the hub never parses client payloads, it only
// forwards the exact bytes (and opcode) to everyone else in the room.
type hub struct {
	mu    sync.Mutex
	rooms map[string]map[*peer]struct{}
}

func newHub() *hub {
	return &hub{rooms: make(map[string]map[*peer]struct{})}
}

// add registers p in its room and returns the room's peer count.
func (h *hub) add(p *peer) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[p.lobbyID]
	if !ok {
		room = make(map[*peer]struct{})
		h.rooms[p.lobbyID] = room
	}
	room[p] = struct{}{}
	return len(room)
}

// remove unregisters p and returns the room's remaining peer count.
func (h *hub) remove(p *peer) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[p.lobbyID]
	if !ok {
		return 0
	}
	delete(room, p)
	if len(room) == 0 {
		delete(h.rooms, p.lobbyID)
		return 0
	}
	return len(room)
}

// others returns a snapshot of the other peers in p's room.
func (h *hub) others(p *peer) []*peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*peer, 0, 2)
	for q := range h.rooms[p.lobbyID] {
		if q != p {
			out = append(out, q)
		}
	}
	return out
}

// relay forwards a client message verbatim to everyone else in the room.
func (h *hub) relay(from *peer, op int, data []byte) {
	for _, q := range h.others(from) {
		// A dead socket is cleaned up by its own read loop; a failed
		// relay write is not the sender's problem.
		_ = q.conn.WriteMessage(op, data)
	}
}

// notify sends a server-generated JSON control message to a peer.
func (h *hub) notify(p *peer, msg []byte) {
	_ = p.conn.WriteMessage(ws.OpText, msg)
}

// broadcastOthers sends a server-generated message to the rest of the room.
func (h *hub) broadcastOthers(from *peer, msg []byte) {
	for _, q := range h.others(from) {
		h.notify(q, msg)
	}
}
