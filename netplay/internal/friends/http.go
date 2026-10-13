package friends

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/davidpatton/itak-gaming/netplay/internal/lobby"
)

// maxBodyBytes caps a friends request body. A request is two short
// names; 4 KB is far more than enough.
const maxBodyBytes = 4 << 10

// IdentitySource resolves the display identity (display name and
// region) the server knows for a player name. The stats store is
// the implementation: it answers from the most recent stat report
// that carried an identity for the name. Both strings are empty
// when the name is unknown.
type IdentitySource interface {
	Identity(player string) (display, region string)
}

// PresenceSource reports which live lobby a player is in, if any.
// The lobby registry is the implementation.
type PresenceSource interface {
	PlayerLobby(player string) (lobby.PublicLobby, bool)
}

// Handler is the HTTP front door for friends lists. It registers
// on the same mux (and therefore the same port) as the lobby and
// leaderboard services.
type Handler struct {
	store    *Store
	identity IdentitySource // may be nil: identities come back empty
	presence PresenceSource // may be nil: everyone reads offline
	log      *log.Logger
}

// NewHandler builds a Handler over a friends store, an identity
// source (for display names and regions), and a presence source
// (for who is in a lobby right now).
func NewHandler(store *Store, identity IdentitySource, presence PresenceSource, logger *log.Logger) *Handler {
	if logger == nil {
		logger = log.Default()
	}
	return &Handler{store: store, identity: identity, presence: presence, log: logger}
}

// Register adds the friends routes to a mux. Method patterns make
// the mux itself answer 405 for wrong verbs on these paths.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /friends", h.handleList)
	mux.HandleFunc("POST /friends", h.handleAdd)
	mux.HandleFunc("POST /friends/remove", h.handleRemove)
}

// Handlers.

// changeRequest is the wire form of an add or remove: who is
// editing their list, and which name it concerns.
type changeRequest struct {
	Player string `json:"player"`
	Friend string `json:"friend"`
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	player := strings.TrimSpace(r.URL.Query().Get("player"))
	if player == "" {
		writeError(w, http.StatusBadRequest, "player query parameter is required")
		return
	}
	writeJSON(w, http.StatusOK, h.buildList(player))
}

func (h *Handler) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req changeRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if _, err := h.store.Add(req.Player, req.Friend); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.buildList(clean(req.Player)))
}

func (h *Handler) handleRemove(w http.ResponseWriter, r *http.Request) {
	var req changeRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if _, err := h.store.Remove(req.Player, req.Friend); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.buildList(clean(req.Player)))
}

// buildList assembles the response for one player: each followed
// name in order, enriched with the identity the leaderboard knows
// and with live presence from the lobby registry.
func (h *Handler) buildList(player string) ListResponse {
	names := h.store.List(player)
	out := ListResponse{Player: player, Friends: make([]Entry, 0, len(names))}
	for _, name := range names {
		e := Entry{Name: name, Status: StatusOffline}
		if h.identity != nil {
			e.Display, e.Region = h.identity.Identity(name)
		}
		if h.presence != nil {
			if pub, ok := h.presence.PlayerLobby(name); ok {
				e.Status = StatusInLobby
				e.Game = pub.Game
			}
		}
		out.Friends = append(out.Friends, e)
	}
	return out
}

// JSON helpers (kept local: the other packages' copies are unexported).

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
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
