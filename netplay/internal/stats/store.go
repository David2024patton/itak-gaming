package stats

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// key identifies one stored triple: one player, in one game, on one
// device. Aggregation happens at read time, keyed by player name or
// game title alone.
type key struct {
	device string
	player string
	game   string
}

// Store keeps every reported triple in memory behind a mutex and
// persists them to a JSON file so a restart does not wipe the global
// board. All methods are safe for concurrent use.
type Store struct {
	mu         sync.Mutex
	entries    map[key]Report
	path       string // "" means ephemeral: never touch disk
	log        *log.Logger
	dirty      bool
	maxTriples int
}

// Open creates a store backed by the JSON file at path, loading any
// entries a previous run saved. A missing file is a fresh board, not
// an error; a corrupt file is logged and also degrades to a fresh
// board (the leaderboard is a nice-to-have, never a reason for the
// server to refuse to start). Path "" keeps everything in memory.
func Open(path string, logger *log.Logger) *Store {
	if logger == nil {
		logger = log.Default()
	}
	s := &Store{
		entries:    make(map[key]Report),
		path:       path,
		log:        logger,
		maxTriples: MaxTriples,
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
			s.log.Printf("stats: cannot read %s (%v); starting empty", s.path, err)
		}
		return
	}
	var file diskFile
	if err := json.Unmarshal(data, &file); err != nil {
		s.log.Printf("stats: %s is corrupt (%v); starting empty", s.path, err)
		return
	}
	for _, r := range file.Entries {
		s.entries[key{r.DeviceID, r.Player, r.Game}] = r
	}
	s.enforceCap()
	s.log.Printf("stats: loaded %d entries from %s", len(s.entries), s.path)
}

// diskFile is the on-disk shape of the stats file.
type diskFile struct {
	Version int      `json:"version"`
	Entries []Report `json:"entries"`
}

// Save writes the current entries to the backing file atomically
// (temp file + rename, so a crash mid-save cannot truncate the last
// good board). It is a no-op for ephemeral stores.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	file := diskFile{Version: 1, Entries: make([]Report, 0, len(s.entries))}
	for _, r := range s.entries {
		file.Entries = append(file.Entries, r)
	}
	s.dirty = false
	s.mu.Unlock()

	// Stable file layout, diff-friendly and easy to eyeball.
	sort.Slice(file.Entries, func(i, j int) bool {
		a, b := file.Entries[i], file.Entries[j]
		if a.DeviceID != b.DeviceID {
			return a.DeviceID < b.DeviceID
		}
		if a.Player != b.Player {
			return a.Player < b.Player
		}
		return a.Game < b.Game
	})
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

// SaveLoop persists the board every interval, but only when a report
// changed it, and once more when ctx ends (server shutdown). It runs
// until ctx is cancelled.
func (s *Store) SaveLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.isDirty() {
				if err := s.Save(); err != nil {
					s.log.Printf("stats: final save failed: %v", err)
				}
			}
			return
		case <-ticker.C:
			if s.isDirty() {
				if err := s.Save(); err != nil {
					s.log.Printf("stats: save failed: %v", err)
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

// Count returns how many triples are stored.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Report records one client's cumulative totals for a triple. The
// report REPLACES the stored entry only when it is at least as new:
// both plays and seconds must be >= the stored values (clients send
// cumulative totals, so anything lower is a stale echo from an older
// sync and is ignored). It returns true when the stored entry
// changed.
func (s *Store) Report(r Report) bool {
	k := key{r.DeviceID, r.Player, r.Game}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.entries[k]; ok {
		if r.Plays < cur.Plays || r.Seconds < cur.Seconds {
			return false
		}
		if r == cur {
			return false
		}
		s.entries[k] = r
		s.dirty = true
		return true
	}
	if len(s.entries) >= s.maxTriples {
		s.evictOldest()
	}
	s.entries[k] = r
	s.dirty = true
	return true
}

// evictOldest drops the entry whose last play is longest ago. Caller
// holds s.mu and the store is at capacity.
func (s *Store) evictOldest() {
	var oldest key
	var oldestReport Report
	first := true
	for k, r := range s.entries {
		if first || lessRecent(r, k, oldestReport, oldest) {
			oldest, oldestReport, first = k, r, false
		}
	}
	delete(s.entries, oldest)
}

// lessRecent orders entries for eviction: smaller LastPlayed first,
// key fields as a deterministic tie-break.
func lessRecent(a Report, ak key, b Report, bk key) bool {
	if a.LastPlayed != b.LastPlayed {
		return a.LastPlayed < b.LastPlayed
	}
	if ak.device != bk.device {
		return ak.device < bk.device
	}
	if ak.player != bk.player {
		return ak.player < bk.player
	}
	return ak.game < bk.game
}

func (s *Store) enforceCap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.entries) > s.maxTriples {
		s.evictOldest()
	}
}

// ---------------------------------------------------------------------------
// Leaderboard reads
// ---------------------------------------------------------------------------

// TopPlayers aggregates every triple by player name across all
// devices, ranked by total seconds (then plays, then name).
func (s *Store) TopPlayers(limit int) []PlayerStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	type agg struct {
		seconds, plays int
		games          map[string]struct{}
		// Identity from the most recent report that carried one.
		display, region string
		idSeen          int64
	}
	byPlayer := make(map[string]*agg)
	for _, r := range s.entries {
		a := byPlayer[r.Player]
		if a == nil {
			a = &agg{games: make(map[string]struct{})}
			byPlayer[r.Player] = a
		}
		a.seconds += r.Seconds
		a.plays += r.Plays
		a.games[r.Game] = struct{}{}
		if r.Display != "" && r.LastPlayed >= a.idSeen {
			a.display = r.Display
			a.region = r.Region
			a.idSeen = r.LastPlayed
		}
	}
	out := make([]PlayerStat, 0, len(byPlayer))
	for name, a := range byPlayer {
		out = append(out, PlayerStat{
			Player:  name,
			Seconds: a.seconds,
			Plays:   a.plays,
			Games:   len(a.games),
			Display: a.display,
			Region:  a.region,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		if out[i].Plays != out[j].Plays {
			return out[i].Plays > out[j].Plays
		}
		return out[i].Player < out[j].Player
	})
	return truncate(out, limit)
}

// Identity returns the display name and region the store knows
// for one player: the values from the most recent report (by
// LastPlayed) that carried a display name, the same rule the
// players board uses. Both come back empty when the player never
// reported an identity, or never reported at all.
func (s *Store) Identity(player string) (display, region string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var seen int64
	for _, r := range s.entries {
		if r.Player != player || r.Display == "" {
			continue
		}
		if display == "" || r.LastPlayed >= seen {
			display, region, seen = r.Display, r.Region, r.LastPlayed
		}
	}
	return display, region
}

// TopGames aggregates every triple by game title across all players
// and devices, ranked by total seconds (then plays, then title).
func (s *Store) TopGames(limit int) []GameStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	type agg struct {
		seconds, plays int
		players        map[string]struct{}
	}
	byGame := make(map[string]*agg)
	for _, r := range s.entries {
		a := byGame[r.Game]
		if a == nil {
			a = &agg{players: make(map[string]struct{})}
			byGame[r.Game] = a
		}
		a.seconds += r.Seconds
		a.plays += r.Plays
		a.players[r.Player] = struct{}{}
	}
	out := make([]GameStat, 0, len(byGame))
	for title, a := range byGame {
		out = append(out, GameStat{
			Game:    title,
			Seconds: a.seconds,
			Plays:   a.plays,
			Players: len(a.players),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		if out[i].Plays != out[j].Plays {
			return out[i].Plays > out[j].Plays
		}
		return out[i].Game < out[j].Game
	})
	return truncate(out, limit)
}

// Recent lists the most recently played triples, newest first
// (player, then game, as deterministic tie-breaks).
func (s *Store) Recent(limit int) []RecentEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecentEntry, 0, len(s.entries))
	for _, r := range s.entries {
		out = append(out, RecentEntry{
			Player:     r.Player,
			Game:       r.Game,
			LastPlayed: r.LastPlayed,
			Seconds:    r.Seconds,
			Display:    r.Display,
			Region:     r.Region,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastPlayed != out[j].LastPlayed {
			return out[i].LastPlayed > out[j].LastPlayed
		}
		if out[i].Player != out[j].Player {
			return out[i].Player < out[j].Player
		}
		return out[i].Game < out[j].Game
	})
	return truncate(out, limit)
}

func truncate[T any](rows []T, limit int) []T {
	if limit < 0 {
		limit = 0
	}
	if len(rows) > limit {
		return rows[:limit]
	}
	return rows
}
