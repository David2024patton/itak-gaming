// Package matches is the tournament results service for iTaK
// Gaming: a win/loss board for host-run tournaments, kept
// completely separate from the playtime leaderboard (package
// stats). Nothing here counts seconds or plays; a row exists
// because a match was reported, not because time passed.
//
// The reporting model is the honor system, stated plainly in the
// design doc: when a tournament match ends, the HOST's client
// reports the result (POST /matches/report), and when a
// tournament finishes, the host reports its champion
// (POST /matches/champion). The server trusts the report; there
// is no referee on the server in this version.
//
// Player identity is the raw player name, exactly as on the
// other boards: case-sensitive, trimmed, control characters
// stripped. A record may carry a display name and region (the
// identity extras the stats reports introduced); the newest
// report that carries one wins, and reads fall back to the
// leaderboard's identity for names that have none here.
package matches

// Limits.
const (
	// MaxNameChars bounds a player name (winner, loser, champion,
	// host), counted in runes after cleaning. It matches the
	// leaderboard's player name limit (stats.MaxPlayerChars):
	// names come from the same place, so they follow the same
	// rule.
	MaxNameChars = 40
	// MaxTournamentChars bounds a tournament name.
	MaxTournamentChars = 80
	// MaxGameChars bounds a game title. It matches the
	// leaderboard's game limit (stats.MaxGameChars).
	MaxGameChars = 120
	// MaxDisplayChars and MaxRegionChars bound the optional
	// identity strings a report may carry, as in stats.
	MaxDisplayChars = 60
	MaxRegionChars  = 40

	// LeaderboardSize is how many rows the tournament board
	// returns at most.
	LeaderboardSize = 100
	// RecentSize is how many matches the recent feed returns.
	RecentSize = 20
	// MaxRecentStored caps how many reported matches the store
	// keeps (and persists) for the recent feed.
	MaxRecentStored = 50
)

// MatchReport is one finished tournament match, as reported by
// the host's client. Display and region fields are optional
// identity extras for the two players, in the same spirit as the
// stats report: when present and non-empty they become the
// identity attached to that player's record until a newer report
// replaces them.
type MatchReport struct {
	Tournament    string
	Game          string
	Winner        string
	Loser         string
	Host          string
	WinnerDisplay string
	WinnerRegion  string
	LoserDisplay  string
	LoserRegion   string
}

// MatchEntry is one reported match in the recent feed: who beat
// whom, in which tournament and game, and when the server
// recorded it (unix milliseconds).
type MatchEntry struct {
	Tournament string `json:"tournament"`
	Game       string `json:"game"`
	Winner     string `json:"winner"`
	Loser      string `json:"loser"`
	At         int64  `json:"at"`
}

// PlayerRecord is one player's tournament standing: the row the
// leaderboard serves and the unit the store persists.
type PlayerRecord struct {
	Name           string `json:"name"`
	Display        string `json:"display"`
	Region         string `json:"region"`
	Wins           int    `json:"wins"`
	Losses         int    `json:"losses"`
	Matches        int    `json:"matches"`
	TournamentsWon int    `json:"tournamentsWon"`
	// LastTournament and LastGame name the event of the player's
	// most recent reported match (or, for LastTournament, of a
	// championship reported after it).
	LastTournament string `json:"lastTournament"`
	LastGame       string `json:"lastGame"`
	// LastTitle is the tournament this player most recently won
	// outright, empty until a champion report names them.
	LastTitle string `json:"lastTitle,omitempty"`
	// LastPlayed is when the player's latest tournament activity
	// was recorded (unix milliseconds).
	LastPlayed int64 `json:"lastPlayed"`
}

// LeaderboardResponse is the wire shape of GET
// /matches/leaderboard.
type LeaderboardResponse struct {
	Players []PlayerRecord `json:"players"`
}

// RecentResponse is the wire shape of GET /matches/recent.
type RecentResponse struct {
	Recent []MatchEntry `json:"recent"`
}
