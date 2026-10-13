// Package stats is the global leaderboard service for iTaK Gaming.
//
// Every copy of the app keeps per-player play stats locally (plays,
// seconds, last played, per game). Clients periodically report those
// cumulative totals here, keyed by an opaque device ID, and anyone
// can read the aggregated rankings: top players by time played, top
// games, and the most recent activity across every device that has
// ever reported in.
//
// Player identity for display is the player NAME: the same name
// reported from two devices (David on the TV box and on a handheld)
// is one person on the board, so their totals are summed together.
package stats

// Limits.
const (
	// MaxTriples caps how many (device, player, game) entries the
	// store keeps. Inserts beyond the cap evict the least recently
	// played entry.
	MaxTriples = 20_000

	// MaxPlayerChars and MaxGameChars bound the display strings a
	// report may carry (counted in runes, after sanitization).
	MaxPlayerChars = 40
	MaxGameChars   = 120
	// MaxDeviceChars bounds the opaque device ID.
	MaxDeviceChars = 128
	// MaxDisplayChars and MaxRegionChars bound the optional
	// identity strings a report may carry.
	MaxDisplayChars = 60
	MaxRegionChars  = 40

	// MaxValue clamps cumulative counters to a sane range (about
	// 31 years of seconds); anything larger is treated as corrupt
	// or abusive and pinned to the cap.
	MaxValue = 1_000_000_000

	// BoardSize is how many rows each leaderboard endpoint returns.
	BoardSize = 50
)

// Report is one client's cumulative totals for one player in one
// game, as kept for that (device, player, game) triple. Clients send
// totals for the whole history of that triple, never deltas.
//
// Display and Region are optional identity extras (a display name
// with initials, a home state or region) the client attaches so the
// boards can tell players apart. Older clients omit them; the raw
// Player name remains the identity and aggregation key either way.
type Report struct {
	DeviceID   string `json:"deviceId"`
	Player     string `json:"player"`
	Game       string `json:"game"`
	Plays      int    `json:"plays"`
	Seconds    int    `json:"seconds"`
	LastPlayed int64  `json:"lastPlayed"` // unix milliseconds
	Display    string `json:"display,omitempty"`
	Region     string `json:"region,omitempty"`
}

// PlayerStat is one row of the players leaderboard: a player name
// aggregated across every device that reported it. Display and
// Region carry the identity from that player's most recent report
// that included them.
type PlayerStat struct {
	Player  string `json:"player"`
	Seconds int    `json:"seconds"`
	Plays   int    `json:"plays"`
	Games   int    `json:"games"` // distinct games played
	Display string `json:"display,omitempty"`
	Region  string `json:"region,omitempty"`
}

// GameStat is one row of the games leaderboard: a game aggregated
// across every player and device.
type GameStat struct {
	Game    string `json:"game"`
	Seconds int    `json:"seconds"`
	Plays   int    `json:"plays"`
	Players int    `json:"players"` // distinct player names
}

// RecentEntry is one row of the recent-activity feed: the latest
// totals a device reported for one player in one game.
type RecentEntry struct {
	Player     string `json:"player"`
	Game       string `json:"game"`
	LastPlayed int64  `json:"lastPlayed"` // unix milliseconds
	Seconds    int    `json:"seconds"`
	Display    string `json:"display,omitempty"`
	Region     string `json:"region,omitempty"`
}
