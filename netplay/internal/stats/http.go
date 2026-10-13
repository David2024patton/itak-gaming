package stats

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxReportBody caps a single stats report. A report is six small
// fields; 16 KB is far more than enough.
const maxReportBody = 16 << 10

// Handler is the HTTP front door for the global leaderboard. It
// registers on the same mux (and therefore the same port) as the
// lobby service.
type Handler struct {
	store   *Store
	log     *log.Logger
	limiter *deviceLimiter
}

// NewHandler builds a Handler over a stats store.
func NewHandler(store *Store, logger *log.Logger) *Handler {
	if logger == nil {
		logger = log.Default()
	}
	return &Handler{
		store:   store,
		log:     logger,
		limiter: newDeviceLimiter(reportLimit, reportWindow),
	}
}

// Register adds the leaderboard routes to a mux. Method patterns
// make the mux itself answer 405 for wrong verbs on these paths.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /stats/report", h.handleReport)
	mux.HandleFunc("GET /leaderboard/players", h.handleTopPlayers)
	mux.HandleFunc("GET /leaderboard/games", h.handleTopGames)
	mux.HandleFunc("GET /leaderboard/recent", h.handleRecent)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// reportRequest is the wire form of a stats report. It decodes into
// Report only after validation and sanitization.
type reportRequest struct {
	DeviceID   string `json:"deviceId"`
	Player     string `json:"player"`
	Game       string `json:"game"`
	Plays      int    `json:"plays"`
	Seconds    int    `json:"seconds"`
	LastPlayed int64  `json:"lastPlayed"`
	Display    string `json:"display"`
	Region     string `json:"region"`
}

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	var req reportRequest
	if err := decodeReport(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rep, err := validateReport(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.limiter.allow(rep.DeviceID) {
		writeError(w, http.StatusTooManyRequests, "too many stats reports; slow down")
		return
	}
	// A stale report (lower than what is stored) is still a success:
	// the client's totals were received and the board already holds
	// newer ones.
	h.store.Report(rep)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleTopPlayers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.store.TopPlayers(BoardSize))
}

func (h *Handler) handleTopGames(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.store.TopGames(BoardSize))
}

func (h *Handler) handleRecent(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.store.Recent(BoardSize))
}

// ---------------------------------------------------------------------------
// Validation and sanitization
// ---------------------------------------------------------------------------

// validateReport cleans a decoded request into a storable Report:
// control characters are stripped, edges are trimmed, lengths are
// enforced, and counters are range-checked and clamped.
func validateReport(req reportRequest) (Report, error) {
	device := sanitize(req.DeviceID)
	player := sanitize(req.Player)
	game := sanitize(req.Game)
	display := sanitize(req.Display)
	region := sanitize(req.Region)
	switch {
	case device == "":
		return Report{}, errors.New("deviceId is required")
	case utf8.RuneCountInString(device) > MaxDeviceChars:
		return Report{}, errors.New("deviceId is too long")
	case player == "":
		return Report{}, errors.New("player is required")
	case utf8.RuneCountInString(player) > MaxPlayerChars:
		return Report{}, errors.New("player must be at most 40 characters")
	case game == "":
		return Report{}, errors.New("game is required")
	case utf8.RuneCountInString(game) > MaxGameChars:
		return Report{}, errors.New("game must be at most 120 characters")
	case req.Plays < 0:
		return Report{}, errors.New("plays must be >= 0")
	case req.Seconds < 0:
		return Report{}, errors.New("seconds must be >= 0")
	case req.LastPlayed < 0:
		return Report{}, errors.New("lastPlayed must be >= 0")
	case utf8.RuneCountInString(display) > MaxDisplayChars:
		return Report{}, errors.New("display must be at most 60 characters")
	case utf8.RuneCountInString(region) > MaxRegionChars:
		return Report{}, errors.New("region must be at most 40 characters")
	}
	return Report{
		DeviceID:   device,
		Player:     player,
		Game:       game,
		Plays:      minInt(req.Plays, MaxValue),
		Seconds:    minInt(req.Seconds, MaxValue),
		LastPlayed: req.LastPlayed,
		Display:    display,
		Region:     region,
	}, nil
}

// sanitize strips control characters (newlines, NULs, escape codes)
// from a display string so a hostile name cannot smuggle terminal
// tricks or broken rows into other clients' leaderboard screens.
func sanitize(s string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(clean)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// JSON helpers (kept local: the lobby server's copies are unexported)
// ---------------------------------------------------------------------------

func decodeReport(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxReportBody)
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
