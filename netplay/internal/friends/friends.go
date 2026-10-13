// Package friends is the friends list service for iTaK Gaming.
//
// A friend edge is one-directional: player A follows player B.
// Following is how a player keeps track of people they play with,
// so the app can show those people first and say whether any of
// them are in a lobby or online with the app open right now.
// There is no consent handshake in
// this version: player names are already public on the global
// leaderboard, so following a name reveals nothing that the board
// does not already show. A follow is not mutual unless both
// players follow each other.
//
// Like the leaderboard, the list is keyed by the raw player name
// alone, exactly as reported. Names are case-sensitive and are
// stored as given (after trimming), so "David" and "david" are
// different players here just as they are on the board.
package friends

// Limits.
const (
	// MaxFriendsPerPlayer caps how many players one player may
	// follow. The cap keeps a single list (and its JSON file)
	// small enough to load and render whole.
	MaxFriendsPerPlayer = 200

	// MaxNameChars bounds a player or friend name, counted in
	// runes after trimming. It matches the leaderboard's player
	// name limit (stats.MaxPlayerChars): names come from the same
	// place, so they follow the same rule.
	MaxNameChars = 40
)

// Status values for a friend's presence, in precedence order:
// in_lobby beats online beats offline.
const (
	// StatusInLobby means the friend is the host or a guest of a
	// lobby that exists in the lobby registry right now.
	StatusInLobby = "in_lobby"
	// StatusOnline means the friend's app pinged the presence
	// endpoint within PresenceWindow (see presence.go) but the
	// friend is not in a lobby.
	StatusOnline = "online"
	// StatusOffline means the friend is not in any live lobby
	// and their app has not pinged recently: either it is closed
	// or it has never pinged since the server started.
	StatusOffline = "offline"
)

// Entry is one friend in a list response: the followed name, the
// identity the leaderboard knows for that name (empty when the
// name has never reported one), and live presence.
type Entry struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	Region  string `json:"region"`
	Status  string `json:"status"`
	Game    string `json:"game,omitempty"` // set only when Status is in_lobby
}

// ListResponse is the wire shape of every friends endpoint: the
// player whose list it is, and their friends in name order.
type ListResponse struct {
	Player  string  `json:"player"`
	Friends []Entry `json:"friends"`
}
