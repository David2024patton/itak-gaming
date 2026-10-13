package lobby

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Store errors. The HTTP layer maps these to status codes.
var (
	ErrNotFound       = errors.New("lobby not found")
	ErrBadPassword    = errors.New("wrong password")
	ErrFull           = errors.New("lobby is full")
	ErrNameTaken      = errors.New("player name already in lobby")
	ErrTooMany        = errors.New("too many lobbies")
	ErrSpectatorsFull = errors.New("spectator seats are full")
)

// Limits and lifetimes.
const (
	DefaultMaxLobbies = 500
	DefaultMaxPlayers = 2

	// MaxSpectators caps how many watchers one lobby holds. Spectators
	// are tracked apart from the player seats, so this never interacts
	// with MaxPlayers.
	MaxSpectators = 10

	// StaleAfterPeers: a lobby whose peers all left is reaped once it
	// has sat empty this long (the game is over; lingering is a leak).
	StaleAfterPeers = 60 * time.Second
	// EmptyLifetime: a lobby nobody ever connected to expires this
	// long after creation (host created it and walked away).
	EmptyLifetime = 10 * time.Minute
)

// CreateInput is everything needed to open a lobby. Password may be
// empty, which creates an open lobby.
type CreateInput struct {
	Name       string
	Game       string
	HostPlayer string
	Password   string
	MaxPlayers int // 0 means DefaultMaxPlayers
}

// Store is the lobby store contract. The in-memory implementation below
// is the only one for now; the interface keeps the HTTP and signaling
// layers testable and leaves room for a persistent store later.
type Store interface {
	// Create opens a lobby and returns it plus the host's bearer token.
	Create(in CreateInput) (*Lobby, string, error)
	// List returns public views of all lobbies, oldest first.
	List() []PublicLobby
	// Public returns the public view of one lobby.
	Public(id string) (PublicLobby, bool)
	// PlayerLobby returns the public view of the live lobby the
	// named player belongs to as host or guest, if any. It backs
	// presence lookups ("is this player in a game right now").
	PlayerLobby(player string) (PublicLobby, bool)
	// Join adds a player and returns their bearer token.
	Join(id, player, password string) (string, error)
	// Watch adds a spectator and returns their bearer token.
	// Spectators get their own token class (it authenticates as
	// RoleSpectator) and never occupy a player seat.
	Watch(id, player, password string) (string, error)
	// Members returns the lobby's full roster, players and
	// spectators, in join order.
	Members(id string) ([]Member, bool)
	// Authenticate resolves a bearer token to a role in a lobby.
	Authenticate(id, token string) (Role, bool)
	// LookupMember resolves a bearer token to the member it belongs
	// to (name and role). The signaling layer uses it to greet a
	// connecting socket by name.
	LookupMember(id, token string) (Member, bool)
	// RemoveMemberByToken revokes the membership a token belongs to.
	// Used when a guest's socket drops for good.
	RemoveMemberByToken(id, token string) bool
	// SetPeers records the live WebSocket connection count for a lobby,
	// reported by the signaling hub. Drives the reaper.
	SetPeers(id string, n int)
	// Delete removes a lobby immediately (e.g. the host hung up).
	Delete(id string)
	// Sweep reaps expired lobbies as of now and returns how many died.
	Sweep(now time.Time) int
	// Count is the number of live lobbies.
	Count() int
}

// MemoryStore is a goroutine-safe in-memory Store.
type MemoryStore struct {
	mu         sync.RWMutex
	lobbies    map[string]*Lobby
	maxLobbies int
	now        func() time.Time // injectable clock for tests
}

// NewMemoryStore returns an empty store with default limits.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		lobbies:    make(map[string]*Lobby),
		maxLobbies: DefaultMaxLobbies,
		now:        time.Now,
	}
}

func (s *MemoryStore) Create(in CreateInput) (*Lobby, string, error) {
	maxPlayers := in.MaxPlayers
	if maxPlayers <= 0 {
		maxPlayers = DefaultMaxPlayers
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.lobbies) >= s.maxLobbies {
		return nil, "", ErrTooMany
	}

	// Allocate a unique short code; collisions are retried.
	var id string
	for range 10 {
		candidate, err := newLobbyID()
		if err != nil {
			return nil, "", err
		}
		if _, exists := s.lobbies[candidate]; !exists {
			id = candidate
			break
		}
	}
	if id == "" {
		return nil, "", ErrTooMany
	}

	now := s.now()
	l := &Lobby{
		ID:         id,
		Name:       in.Name,
		Game:       in.Game,
		HostPlayer: in.HostPlayer,
		MaxPlayers: maxPlayers,
		CreatedAt:  now,
		members:    make(map[string]*member),
		watchers:   make(map[string]*member),
		tokens:     make(map[string]string),
		emptySince: now,
	}

	if in.Password != "" {
		salt, err := randomBytes(saltBytes)
		if err != nil {
			return nil, "", err
		}
		l.salt = salt
		l.passHash = hashPassword(in.Password, salt)
	}

	token, err := s.addMemberLocked(l, in.HostPlayer, RoleHost)
	if err != nil {
		return nil, "", err
	}
	s.lobbies[id] = l
	return l, token, nil
}

// addMemberLocked registers a member and returns their bearer token.
// Spectators land in the watchers set, players in the members set; the
// two never mix, so player counts and the MaxPlayers cap only ever
// see player members. Callers hold s.mu.
func (s *MemoryStore) addMemberLocked(l *Lobby, player string, role Role) (string, error) {
	raw, err := randomBytes(8)
	if err != nil {
		return "", err
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	m := &member{id: hexID(raw), player: player, role: role, seq: l.nextSeq}
	l.nextSeq++
	if role == RoleSpectator {
		l.watchers[m.id] = m
	} else {
		l.members[m.id] = m
	}
	l.tokens[tokenDigest(token)] = m.id
	return token, nil
}

// memberByIDLocked finds a member by id in either set.
// Callers hold s.mu (read or write).
func memberByIDLocked(l *Lobby, id string) (*member, bool) {
	if m, ok := l.members[id]; ok {
		return m, true
	}
	m, ok := l.watchers[id]
	return m, ok
}

func hexID(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

func (s *MemoryStore) List() []PublicLobby {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PublicLobby, 0, len(s.lobbies))
	for _, l := range s.lobbies {
		out = append(out, l.Public())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *MemoryStore) Public(id string) (PublicLobby, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.lobbies[id]
	if !ok {
		return PublicLobby{}, false
	}
	return l.Public(), true
}

// PlayerLobby finds the live lobby the named player belongs to.
// The host is a member too, so one pass over the members covers
// both roles; the host name check is belt and braces for a lobby
// observed mid-create. Lobbies are few (hundreds at most), so a
// scan beats keeping a second index in sync.
func (s *MemoryStore) PlayerLobby(player string) (PublicLobby, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.lobbies {
		if l.HostPlayer == player {
			return l.Public(), true
		}
		for _, m := range l.members {
			if m.player == player {
				return l.Public(), true
			}
		}
	}
	return PublicLobby{}, false
}

func (s *MemoryStore) Join(id, player, password string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.lobbies[id]
	if !ok {
		return "", ErrNotFound
	}
	if l.passHash != nil && !checkPassword(password, l.salt, l.passHash) {
		return "", ErrBadPassword
	}
	if len(l.members) >= l.MaxPlayers {
		return "", ErrFull
	}
	for _, m := range l.members {
		if m.player == player {
			return "", ErrNameTaken
		}
	}
	return s.addMemberLocked(l, player, RoleGuest)
}

// Watch adds a spectator to a lobby and returns their bearer token.
// The password is checked exactly like a guest join. Names must be
// unique across the whole room (players and spectators alike) so the
// roster never shows two people with one name. Spectators have their
// own cap (MaxSpectators) and never touch the player seats: a full
// player lobby can still take watchers, and a full watcher bench
// never blocks a guest join.
func (s *MemoryStore) Watch(id, player, password string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.lobbies[id]
	if !ok {
		return "", ErrNotFound
	}
	if l.passHash != nil && !checkPassword(password, l.salt, l.passHash) {
		return "", ErrBadPassword
	}
	if len(l.watchers) >= MaxSpectators {
		return "", ErrSpectatorsFull
	}
	for _, m := range l.members {
		if m.player == player {
			return "", ErrNameTaken
		}
	}
	for _, m := range l.watchers {
		if m.player == player {
			return "", ErrNameTaken
		}
	}
	return s.addMemberLocked(l, player, RoleSpectator)
}

// Members returns the lobby's full roster in join order: the host
// first, then whoever joined or started watching after.
func (s *MemoryStore) Members(id string) ([]Member, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.lobbies[id]
	if !ok {
		return nil, false
	}
	all := make([]*member, 0, len(l.members)+len(l.watchers))
	for _, m := range l.members {
		all = append(all, m)
	}
	for _, m := range l.watchers {
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	out := make([]Member, 0, len(all))
	for _, m := range all {
		out = append(out, Member{Name: m.player, Role: m.role})
	}
	return out, true
}

func (s *MemoryStore) Authenticate(id, token string) (Role, bool) {
	m, ok := s.LookupMember(id, token)
	if !ok {
		return "", false
	}
	return m.Role, true
}

func (s *MemoryStore) LookupMember(id, token string) (Member, bool) {
	if token == "" {
		return Member{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.lobbies[id]
	if !ok {
		return Member{}, false
	}
	memberID, ok := l.tokens[tokenDigest(token)]
	if !ok {
		return Member{}, false
	}
	m, ok := memberByIDLocked(l, memberID)
	if !ok {
		return Member{}, false
	}
	return Member{Name: m.player, Role: m.role}, true
}

func (s *MemoryStore) RemoveMemberByToken(id, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lobbies[id]
	if !ok {
		return false
	}
	digest := tokenDigest(token)
	memberID, ok := l.tokens[digest]
	if !ok {
		return false
	}
	m, ok := memberByIDLocked(l, memberID)
	if !ok {
		return false
	}
	delete(l.tokens, digest)
	if m.role == RoleSpectator {
		delete(l.watchers, memberID)
	} else {
		delete(l.members, memberID)
	}
	return true
}

func (s *MemoryStore) SetPeers(id string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lobbies[id]
	if !ok {
		return
	}
	prev := l.peers
	l.peers = n
	if n > 0 {
		l.everHadPeers = true
	} else if prev > 0 {
		l.emptySince = s.now()
	}
}

func (s *MemoryStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lobbies, id)
}

func (s *MemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.lobbies)
}

// Sweep deletes:
//   - lobbies that had peers, then sat empty for StaleAfterPeers
//   - lobbies nobody ever connected to once EmptyLifetime has passed
//
// Lobbies with live peers are never swept.
func (s *MemoryStore) Sweep(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	reaped := 0
	for id, l := range s.lobbies {
		if l.peers > 0 {
			continue
		}
		switch {
		case l.everHadPeers && now.Sub(l.emptySince) > StaleAfterPeers:
			delete(s.lobbies, id)
			reaped++
		case !l.everHadPeers && now.Sub(l.CreatedAt) > EmptyLifetime:
			delete(s.lobbies, id)
			reaped++
		}
	}
	return reaped
}
