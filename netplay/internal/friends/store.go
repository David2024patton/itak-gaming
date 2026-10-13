package friends

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
// is a problem with the request, not with the server.
var (
	ErrEmptyName = errors.New("player and friend names are required")
	ErrSelf      = errors.New("a player cannot follow themselves")
	ErrNameLong  = errors.New("name must be at most 40 characters")
	ErrTooMany   = errors.New("friend list is full (max 200)")
)

// Store keeps every follow edge in memory behind a mutex and
// persists them to a JSON file so a restart does not wipe anyone's
// list. It mirrors the stats store's shape on purpose: same open,
// save and save-loop lifecycle, same degrade-to-empty behavior on
// a corrupt file. All methods are safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	edges map[string]map[string]struct{} // player name to its set of friend names
	path  string                         // "" means ephemeral: never touch disk
	log   *log.Logger
	dirty bool
}

// Open creates a store backed by the JSON file at path, loading
// any edges a previous run saved. A missing file is a fresh set of
// lists, not an error; a corrupt file is logged and also degrades
// to empty. Path "" keeps everything in memory.
func Open(path string, logger *log.Logger) *Store {
	if logger == nil {
		logger = log.Default()
	}
	s := &Store{
		edges: make(map[string]map[string]struct{}),
		path:  path,
		log:   logger,
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
			s.log.Printf("friends: cannot read %s (%v); starting empty", s.path, err)
		}
		return
	}
	var file diskFile
	if err := json.Unmarshal(data, &file); err != nil {
		s.log.Printf("friends: %s is corrupt (%v); starting empty", s.path, err)
		return
	}
	for player, names := range file.Follows {
		set := make(map[string]struct{}, len(names))
		for _, n := range names {
			set[n] = struct{}{}
		}
		if len(set) > 0 {
			s.edges[player] = set
		}
	}
	count := 0
	for _, set := range s.edges {
		count += len(set)
	}
	s.log.Printf("friends: loaded %d follows for %d players from %s", count, len(s.edges), s.path)
}

// diskFile is the on-disk shape of the friends file: each player's
// followed names as one sorted list.
type diskFile struct {
	Version int                 `json:"version"`
	Follows map[string][]string `json:"follows"`
}

// Save writes the current edges to the backing file atomically
// (temp file + rename, so a crash mid-save cannot truncate the
// last good file). It is a no-op for ephemeral stores.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	file := diskFile{Version: 1, Follows: make(map[string][]string, len(s.edges))}
	for player, set := range s.edges {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		file.Follows[player] = names
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

// SaveLoop persists the edges every interval, but only when a
// change marked them dirty, and once more when ctx ends (server
// shutdown). It runs until ctx is cancelled.
func (s *Store) SaveLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.isDirty() {
				if err := s.Save(); err != nil {
					s.log.Printf("friends: final save failed: %v", err)
				}
			}
			return
		case <-ticker.C:
			if s.isDirty() {
				if err := s.Save(); err != nil {
					s.log.Printf("friends: save failed: %v", err)
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

// clean normalizes a name the same way the leaderboard normalizes
// the names it stores: control characters stripped, edges trimmed.
// Case is preserved; names are case-sensitive.
func clean(name string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	return strings.TrimSpace(stripped)
}

// validateName applies the rules every stored name must satisfy.
func validateName(name string) error {
	switch {
	case name == "":
		return ErrEmptyName
	case utf8.RuneCountInString(name) > MaxNameChars:
		return ErrNameLong
	}
	return nil
}

// Add records that player follows friend. It is idempotent:
// following someone already on the list succeeds and changes
// nothing. Both names are trimmed first; empty names, following
// yourself, and growing a list past MaxFriendsPerPlayer are
// rejected. It returns true when the list changed.
func (s *Store) Add(player, friend string) (bool, error) {
	player, friend = clean(player), clean(friend)
	if err := validateName(player); err != nil {
		return false, err
	}
	if err := validateName(friend); err != nil {
		return false, err
	}
	if player == friend {
		return false, ErrSelf
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.edges[player]
	if set == nil {
		set = make(map[string]struct{})
		s.edges[player] = set
	}
	if _, ok := set[friend]; ok {
		return false, nil
	}
	if len(set) >= MaxFriendsPerPlayer {
		return false, ErrTooMany
	}
	set[friend] = struct{}{}
	s.dirty = true
	return true, nil
}

// Remove drops friend from player's list. Removing someone who is
// not on the list (or a list that does not exist) succeeds and
// changes nothing. It returns true when the list changed.
func (s *Store) Remove(player, friend string) (bool, error) {
	player, friend = clean(player), clean(friend)
	if err := validateName(player); err != nil {
		return false, err
	}
	if err := validateName(friend); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.edges[player]
	if set == nil {
		return false, nil
	}
	if _, ok := set[friend]; !ok {
		return false, nil
	}
	delete(set, friend)
	if len(set) == 0 {
		delete(s.edges, player)
	}
	s.dirty = true
	return true, nil
}

// List returns the names player follows, sorted. A player with no
// list gets an empty slice, never nil.
func (s *Store) List(player string) []string {
	player = clean(player)
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.edges[player]
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Count returns how many follow edges are stored, across all
// players.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, set := range s.edges {
		n += len(set)
	}
	return n
}
