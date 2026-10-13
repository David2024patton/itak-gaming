package matches

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Store errors. The HTTP layer maps all of them to 400: every one
// is a problem with the report, not with the server.
var (
	ErrEmptyTournament = errors.New("tournament is required")
	ErrEmptyWinner     = errors.New("winner is required")
	ErrEmptyLoser      = errors.New("loser is required")
	ErrEmptyChampion   = errors.New("champion is required")
	ErrEmptyHost       = errors.New("host is required")
	ErrSelfMatch       = errors.New("winner and loser must be different players")
	ErrNameLong        = errors.New("name must be at most 40 characters")
	ErrTournamentLong  = errors.New("tournament must be at most 80 characters")
	ErrGameLong        = errors.New("game must be at most 120 characters")
	ErrDisplayLong     = errors.New("display must be at most 60 characters")
	ErrRegionLong      = errors.New("region must be at most 40 characters")
)

// Store keeps every player's tournament record and the recent
// match feed in memory behind a mutex, and persists them to a
// JSON file so a restart does not wipe the board. It mirrors the
// stats and friends stores' shape on purpose: same open, save and
// save-loop lifecycle, same degrade-to-empty behavior on a
// corrupt file. All methods are safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	players map[string]*PlayerRecord
	recent  []MatchEntry // newest first, capped at MaxRecentStored
	path    string       // "" means ephemeral: never touch disk
	log     *log.Logger
	dirty   bool
	now     func() time.Time // tests substitute a fake clock
}

// Open creates a store backed by the JSON file at path, loading
// any records a previous run saved. A missing file is a fresh
// board, not an error; a corrupt file is logged and also degrades
// to a fresh board (the tournament board is a nice-to-have, never
// a reason for the server to refuse to start). Path "" keeps
// everything in memory.
func Open(path string, logger *log.Logger) *Store {
	if logger == nil {
		logger = log.Default()
	}
	s := &Store{
		players: make(map[string]*PlayerRecord),
		path:    path,
		log:     logger,
		now:     time.Now,
	}
	s.load()
	return s
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.log.Printf("matches: cannot read %s (%v); starting empty", s.path, err)
		}
		return
	}
	var file diskFile
	if err := json.Unmarshal(data, &file); err != nil {
		s.log.Printf("matches: %s is corrupt (%v); starting empty", s.path, err)
		return
	}
	for _, p := range file.Players {
		rec := p
		s.players[rec.Name] = &rec
	}
	s.recent = file.Recent
	if len(s.recent) > MaxRecentStored {
		s.recent = s.recent[:MaxRecentStored]
	}
	s.log.Printf("matches: loaded %d players and %d recent matches from %s",
		len(s.players), len(s.recent), s.path)
}

// diskFile is the on-disk shape of the matches file: the player
// records in name order, and the recent feed newest first.
type diskFile struct {
	Version int            `json:"version"`
	Players []PlayerRecord `json:"players"`
	Recent  []MatchEntry   `json:"recent"`
}

// Save writes the current records to the backing file atomically
// (temp file + rename, so a crash mid-save cannot truncate the
// last good board). It is a no-op for ephemeral stores.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	file := diskFile{Version: 1, Recent: append([]MatchEntry{}, s.recent...)}
	names := make([]string, 0, len(s.players))
	for name := range s.players {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file.Players = append(file.Players, *s.players[name])
	}
	s.dirty = false
	s.mu.Unlock()

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
		return err
	}
	if dir := filepath.Dir(s.path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// SaveLoop persists the board every interval, but only when a
// report changed it, and once more when ctx ends (server
// shutdown). It runs until ctx is cancelled.
func (s *Store) SaveLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.isDirty() {
				if err := s.Save(); err != nil {
					s.log.Printf("matches: final save failed: %v", err)
				}
			}
			return
		case <-ticker.C:
			if s.isDirty() {
				if err := s.Save(); err != nil {
					s.log.Printf("matches: save failed: %v", err)
				}
			}
		}
	}
}

func (s *Store) isDirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// clean normalizes a name the same way the other boards normalize
// the names they store: control characters stripped, edges
// trimmed. Case is preserved; names are case-sensitive.
func clean(name string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	return strings.TrimSpace(stripped)
}

// validateName applies the rules every stored player name must
// satisfy. emptyErr says which field was empty, so the client
// hears about the actual problem.
func validateName(name string, emptyErr error) error {
	switch {
	case name == "":
		return emptyErr
	case utf8.RuneCountInString(name) > MaxNameChars:
		return ErrNameLong
	}
	return nil
}

// validateDisplay checks an optional identity pair: either may be
// empty, but a present one must fit its cap.
func validateIdentity(display, region string) error {
	switch {
	case utf8.RuneCountInString(display) > MaxDisplayChars:
		return ErrDisplayLong
	case utf8.RuneCountInString(region) > MaxRegionChars:
		return ErrRegionLong
	}
	return nil
}

// recordFor returns the record for a cleaned name, creating and
// storing a fresh one when the name is new. Caller holds s.mu.
func (s *Store) recordFor(name string) *PlayerRecord {
	rec := s.players[name]
	if rec == nil {
		rec = &PlayerRecord{Name: name}
		s.players[name] = rec
	}
	return rec
}

// attachIdentity updates a record's identity from a report's
// extras. A report that carries no display leaves the stored
// identity alone; one that carries a display replaces both
// fields (latest identity wins, the stats rule). Caller holds
// s.mu.
func attachIdentity(rec *PlayerRecord, display, region string) {
	if display == "" {
		return
	}
	rec.Display = display
	rec.Region = region
}

// ReportMatch records one finished match: the winner gains a win
// and a match, the loser a loss and a match, both are stamped
// with the tournament and game as their most recent event, and
// the match joins the head of the recent feed. Every string is
// cleaned before validation. It returns the recorded entry,
// stamped with the server's clock.
func (s *Store) ReportMatch(r MatchReport) (MatchEntry, error) {
	r.Tournament = clean(r.Tournament)
	r.Game = clean(r.Game)
	r.Winner = clean(r.Winner)
	r.Loser = clean(r.Loser)
	r.Host = clean(r.Host)
	r.WinnerDisplay = clean(r.WinnerDisplay)
	r.WinnerRegion = clean(r.WinnerRegion)
	r.LoserDisplay = clean(r.LoserDisplay)
	r.LoserRegion = clean(r.LoserRegion)
	switch {
	case r.Tournament == "":
		return MatchEntry{}, ErrEmptyTournament
	case utf8.RuneCountInString(r.Tournament) > MaxTournamentChars:
		return MatchEntry{}, ErrTournamentLong
	case utf8.RuneCountInString(r.Game) > MaxGameChars:
		return MatchEntry{}, ErrGameLong
	}
	if err := validateName(r.Winner, ErrEmptyWinner); err != nil {
		return MatchEntry{}, err
	}
	if err := validateName(r.Loser, ErrEmptyLoser); err != nil {
		return MatchEntry{}, err
	}
	if err := validateName(r.Host, ErrEmptyHost); err != nil {
		return MatchEntry{}, err
	}
	if r.Winner == r.Loser {
		return MatchEntry{}, ErrSelfMatch
	}
	if err := validateIdentity(r.WinnerDisplay, r.WinnerRegion); err != nil {
		return MatchEntry{}, err
	}
	if err := validateIdentity(r.LoserDisplay, r.LoserRegion); err != nil {
		return MatchEntry{}, err
	}

	at := s.now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	winner := s.recordFor(r.Winner)
	winner.Wins++
	winner.Matches++
	winner.LastTournament = r.Tournament
	winner.LastGame = r.Game
	winner.LastPlayed = at
	attachIdentity(winner, r.WinnerDisplay, r.WinnerRegion)

	loser := s.recordFor(r.Loser)
	loser.Losses++
	loser.Matches++
	loser.LastTournament = r.Tournament
	loser.LastGame = r.Game
	loser.LastPlayed = at
	attachIdentity(loser, r.LoserDisplay, r.LoserRegion)

	entry := MatchEntry{
		Tournament: r.Tournament,
		Game:       r.Game,
		Winner:     r.Winner,
		Loser:      r.Loser,
		At:         at,
	}
	s.recent = append([]MatchEntry{entry}, s.recent...)
	if len(s.recent) > MaxRecentStored {
		s.recent = s.recent[:MaxRecentStored]
	}
	s.dirty = true
	return entry, nil
}

// ReportChampion records that a player won a tournament outright:
// their titles count rises by one and the tournament becomes
// their latest title and most recent event. The record is
// created when the champion has never played a reported match.
// display and region are the champion's optional identity extras.
func (s *Store) ReportChampion(tournament, champion, host, display, region string) error {
	tournament = clean(tournament)
	champion = clean(champion)
	host = clean(host)
	display = clean(display)
	region = clean(region)
	switch {
	case tournament == "":
		return ErrEmptyTournament
	case utf8.RuneCountInString(tournament) > MaxTournamentChars:
		return ErrTournamentLong
	}
	if err := validateName(champion, ErrEmptyChampion); err != nil {
		return err
	}
	if err := validateName(host, ErrEmptyHost); err != nil {
		return err
	}
	if err := validateIdentity(display, region); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.recordFor(champion)
	rec.TournamentsWon++
	rec.LastTournament = tournament
	rec.LastTitle = tournament
	rec.LastPlayed = s.now().UnixMilli()
	attachIdentity(rec, display, region)
	s.dirty = true
	return nil
}

// Leaderboard returns the tournament board: every recorded
// player, ranked by tournaments won, then match wins, then
// matches played, then name, capped at limit rows.
func (s *Store) Leaderboard(limit int) []PlayerRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PlayerRecord, 0, len(s.players))
	for _, rec := range s.players {
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TournamentsWon != out[j].TournamentsWon {
			return out[i].TournamentsWon > out[j].TournamentsWon
		}
		if out[i].Wins != out[j].Wins {
			return out[i].Wins > out[j].Wins
		}
		if out[i].Matches != out[j].Matches {
			return out[i].Matches > out[j].Matches
		}
		return out[i].Name < out[j].Name
	})
	if limit < 0 {
		limit = 0
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Recent returns the newest reported matches, newest first,
// capped at limit entries. It hands back a copy: callers may not
// mutate the store's feed.
func (s *Store) Recent(limit int) []MatchEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 0 {
		limit = 0
	}
	if len(s.recent) < limit {
		limit = len(s.recent)
	}
	out := make([]MatchEntry, limit)
	copy(out, s.recent[:limit])
	return out
}

// PlayerCount returns how many players have records.
func (s *Store) PlayerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.players)
}
