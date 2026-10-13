package matches

import (
	"encoding/json"
	"log"
	"net/http"
)

// maxBodyBytes caps a matches request body. A report is a handful
// of short strings; 16 KB is far more than enough (the same cap
// the stats report uses).
const maxBodyBytes = 16 << 10

// IdentitySource resolves the display identity (display name and
// region) the server knows for a player name. The stats store is
// the implementation: it answers from the most recent stat report
// that carried an identity for the name. Both strings are empty
// when the name is unknown.
type IdentitySource interface {
	Identity(player string) (display, region string)
}

// Handler is the HTTP front door for tournament results. It
// registers on the same mux (and therefore the same port) as the
// lobby, leaderboard, and friends services.
type Handler struct {
	store    *Store
	identity IdentitySource // may be nil: identities come from reports only
	log      *log.Logger
}

// NewHandler builds a Handler over a matches store and an
// identity source (used to fill board identities for players
// whose match reports never carried one).
func NewHandler(store *Store, identity IdentitySource, logger *log.Logger) *Handler {
	if logger == nil {
		logger = log.Default()
	}
	return &Handler{store: store, identity: identity, log: logger}
}

// Register adds the matches routes to a mux. Method patterns
// make the mux itself answer 405 for wrong verbs on these paths.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /matches/report", h.handleReport)
	mux.HandleFunc("POST /matches/champion", h.handleChampion)
	mux.HandleFunc("GET /matches/leaderboard", h.handleLeaderboard)
	mux.HandleFunc("GET /matches/recent", h.handleRecent)
}

// Handlers.

// reportRequest is the wire form of a match report.
type reportRequest struct {
	Tournament    string `json:"tournament"`
	Game          string `json:"game"`
	Winner        string `json:"winner"`
	Loser         string `json:"loser"`
	Host          string `json:"host"`
	WinnerDisplay string `json:"winnerDisplay"`
	WinnerRegion  string `json:"winnerRegion"`
	LoserDisplay  string `json:"loserDisplay"`
	LoserRegion   string `json:"loserRegion"`
}

// handleReport records one finished match. Success is 204 with
// no body, like the stats report: the host's client fires it at
// match end and reads nothing back; it refreshes the board on
// its own schedule.
func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	_, err := h.store.ReportMatch(MatchReport{
		Tournament:    req.Tournament,
		Game:          req.Game,
		Winner:        req.Winner,
		Loser:         req.Loser,
		Host:          req.Host,
		WinnerDisplay: req.WinnerDisplay,
		WinnerRegion:  req.WinnerRegion,
		LoserDisplay:  req.LoserDisplay,
		LoserRegion:   req.LoserRegion,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// championRequest is the wire form of a champion report. The
// identity extras are optional, as on a match report.
type championRequest struct {
	Tournament      string `json:"tournament"`
	Champion        string `json:"champion"`
	Host            string `json:"host"`
	ChampionDisplay string `json:"championDisplay"`
	ChampionRegion  string `json:"championRegion"`
}

func (h *Handler) handleChampion(w http.ResponseWriter, r *http.Request) {
	var req championRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	err := h.store.ReportChampion(req.Tournament, req.Champion, req.Host,
		req.ChampionDisplay, req.ChampionRegion)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLeaderboard serves the tournament board. A row's
// identity comes from the player's match reports; when no report
// ever carried one, the identity source (the playtime
// leaderboard) fills it, so a known player is never anonymous
// here just because their host's client sent no identity.
func (h *Handler) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	players := h.store.Leaderboard(LeaderboardSize)
	for i := range players {
		if players[i].Display == "" && h.identity != nil {
			players[i].Display, players[i].Region = h.identity.Identity(players[i].Name)
		}
	}
	writeJSON(w, http.StatusOK, LeaderboardResponse{Players: players})
}

func (h *Handler) handleRecent(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, RecentResponse{Recent: h.store.Recent(RecentSize)})
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
