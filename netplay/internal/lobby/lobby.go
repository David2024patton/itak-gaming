// Package lobby is the in-memory lobby store for iTaK Gaming netplay.
//
// A lobby is a room created by a host for one game session. Other players
// join with an optional password and receive a bearer token. Tokens and
// password hashes never leave this package: the public view of a lobby
// (PublicLobby) is the only shape the HTTP layer serializes for listing.
package lobby

import "time"

// Role is a member's role inside a lobby.
type Role string

const (
	RoleHost      Role = "host"
	RoleGuest     Role = "guest"
	RoleSpectator Role = "spectator"
)

// member is one joined member. Members exist independently of WebSocket
// connections: a player joins over HTTP first, then connects to the
// signaling socket with the token they were issued.
type member struct {
	id     string // internal random id
	player string // display name chosen by the player
	role   Role
	seq    int // join order within the lobby, for stable rosters
}

// Member is the public view of one lobby member: a display name and a
// role, nothing else. The signaling layer uses it to build the room
// roster notice; it carries no ids and no tokens.
type Member struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
}

// Lobby is a single netplay room. All fields are guarded by the owning
// Store's mutex; nothing outside this package touches them directly.
type Lobby struct {
	ID         string
	Name       string
	Game       string // ROM path or title, opaque to the server
	HostPlayer string
	MaxPlayers int
	CreatedAt  time.Time

	salt     []byte // nil when the lobby has no password
	passHash []byte // nil when the lobby has no password

	members  map[string]*member // member id -> player member (host, guests)
	watchers map[string]*member // member id -> spectator member
	tokens   map[string]string  // sha256(token) hex -> member id
	nextSeq  int                // next member join-order value

	peers        int       // live WebSocket connections, reported by the hub
	everHadPeers bool      // at least one peer connected at some point
	emptySince   time.Time // last time the peer count dropped to zero
}

// PublicLobby is the wire-safe view of a Lobby: no hashes, no tokens.
type PublicLobby struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Game       string    `json:"game"`
	HostPlayer string    `json:"hostPlayer"`
	Players    int       `json:"players"`
	MaxPlayers int       `json:"maxPlayers"`
	Spectators int       `json:"spectators"`
	Locked     bool      `json:"locked"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Public returns the wire-safe view of the lobby.
func (l *Lobby) Public() PublicLobby {
	return PublicLobby{
		ID:         l.ID,
		Name:       l.Name,
		Game:       l.Game,
		HostPlayer: l.HostPlayer,
		Players:    len(l.members),
		MaxPlayers: l.MaxPlayers,
		Spectators: len(l.watchers),
		Locked:     l.passHash != nil,
		CreatedAt:  l.CreatedAt,
	}
}

// HasPassword reports whether the lobby is password protected.
func (l *Lobby) HasPassword() bool { return l.passHash != nil }
