package stats

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return Open("", log.New(io.Discard, "", 0))
}

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

func TestReportReplaceIfGreater(t *testing.T) {
	s := newTestStore(t)
	base := Report{DeviceID: "box1", Player: "David", Game: "SMW", Plays: 3, Seconds: 600, LastPlayed: 1000}
	if !s.Report(base) {
		t.Fatal("first report should be stored")
	}
	if s.Count() != 1 {
		t.Fatalf("count = %d, want 1", s.Count())
	}

	// Identical re-report: no change.
	if s.Report(base) {
		t.Error("identical report should report no change")
	}

	// Lower plays: stale, ignored.
	stale := base
	stale.Plays--
	stale.LastPlayed = 2000
	if s.Report(stale) {
		t.Error("report with lower plays should be ignored")
	}

	// Lower seconds: stale, ignored (both counters must be >=).
	stale = base
	stale.Seconds--
	stale.Plays++
	if s.Report(stale) {
		t.Error("report with lower seconds should be ignored even with higher plays")
	}

	// Higher totals replace, including a newer LastPlayed.
	newer := base
	newer.Plays = 4
	newer.Seconds = 900
	newer.LastPlayed = 3000
	if !s.Report(newer) {
		t.Fatal("higher report should replace")
	}
	recent := s.Recent(10)
	if len(recent) != 1 || recent[0].Seconds != 900 || recent[0].LastPlayed != 3000 {
		t.Errorf("stored entry = %+v, want seconds 900 lastPlayed 3000", recent)
	}

	// Equal counters with a newer LastPlayed still refresh recency.
	refresh := newer
	refresh.LastPlayed = 4000
	if !s.Report(refresh) {
		t.Error("equal totals with newer lastPlayed should replace")
	}
	if got := s.Recent(1)[0].LastPlayed; got != 4000 {
		t.Errorf("lastPlayed = %d, want 4000", got)
	}
}

func TestTopPlayersAggregatesAcrossDevices(t *testing.T) {
	s := newTestStore(t)
	// Same player name on two devices is one person on the board.
	s.Report(Report{DeviceID: "tv", Player: "David", Game: "SMW", Plays: 2, Seconds: 300, LastPlayed: 10})
	s.Report(Report{DeviceID: "handheld", Player: "David", Game: "SMW", Plays: 1, Seconds: 100, LastPlayed: 20})
	s.Report(Report{DeviceID: "handheld", Player: "David", Game: "Kart", Plays: 5, Seconds: 700, LastPlayed: 30})
	s.Report(Report{DeviceID: "tv", Player: "Mary", Game: "SMW", Plays: 9, Seconds: 500, LastPlayed: 40})

	players := s.TopPlayers(BoardSize)
	if len(players) != 2 {
		t.Fatalf("players = %d, want 2", len(players))
	}
	top := players[0]
	if top.Player != "David" || top.Seconds != 1100 || top.Plays != 8 || top.Games != 2 {
		t.Errorf("top = %+v, want David 1100s 8 plays 2 games", top)
	}
	if players[1].Player != "Mary" || players[1].Games != 1 {
		t.Errorf("second = %+v, want Mary with 1 game", players[1])
	}
}

func TestTopGamesAggregates(t *testing.T) {
	s := newTestStore(t)
	s.Report(Report{DeviceID: "a", Player: "David", Game: "SMW", Plays: 2, Seconds: 300, LastPlayed: 10})
	s.Report(Report{DeviceID: "b", Player: "David", Game: "SMW", Plays: 1, Seconds: 50, LastPlayed: 11})
	s.Report(Report{DeviceID: "a", Player: "Mary", Game: "SMW", Plays: 4, Seconds: 900, LastPlayed: 12})
	s.Report(Report{DeviceID: "a", Player: "David", Game: "Kart", Plays: 7, Seconds: 2000, LastPlayed: 13})

	games := s.TopGames(BoardSize)
	if len(games) != 2 {
		t.Fatalf("games = %d, want 2", len(games))
	}
	if games[0].Game != "Kart" || games[0].Seconds != 2000 || games[0].Players != 1 {
		t.Errorf("top game = %+v, want Kart 2000s 1 player", games[0])
	}
	smw := games[1]
	if smw.Game != "SMW" || smw.Seconds != 1250 || smw.Plays != 7 || smw.Players != 2 {
		t.Errorf("SMW = %+v, want 1250s 7 plays 2 players (David counted once across devices)", smw)
	}
}

func TestRecentOrderingAndLimit(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 60; i++ {
		s.Report(Report{
			DeviceID:   "box",
			Player:     "P",
			Game:       fmt.Sprintf("Game%02d", i),
			Plays:      1,
			Seconds:    10,
			LastPlayed: int64(1000 + i),
		})
	}
	recent := s.Recent(BoardSize)
	if len(recent) != BoardSize {
		t.Fatalf("recent = %d rows, want %d", len(recent), BoardSize)
	}
	for i := 1; i < len(recent); i++ {
		if recent[i-1].LastPlayed < recent[i].LastPlayed {
			t.Fatalf("recent not sorted desc at %d: %d before %d", i, recent[i-1].LastPlayed, recent[i].LastPlayed)
		}
	}
	if recent[0].LastPlayed != 1059 {
		t.Errorf("newest = %d, want 1059", recent[0].LastPlayed)
	}
	if recent[len(recent)-1].LastPlayed != 1010 {
		t.Errorf("oldest shown = %d, want 1010 (50 of 60)", recent[len(recent)-1].LastPlayed)
	}
}

func TestCapEvictsLeastRecentlyPlayed(t *testing.T) {
	s := newTestStore(t)
	s.maxTriples = 3
	s.Report(Report{DeviceID: "d", Player: "A", Game: "G1", Plays: 1, Seconds: 1, LastPlayed: 100})
	s.Report(Report{DeviceID: "d", Player: "A", Game: "G2", Plays: 1, Seconds: 1, LastPlayed: 300})
	s.Report(Report{DeviceID: "d", Player: "A", Game: "G3", Plays: 1, Seconds: 1, LastPlayed: 200})
	s.Report(Report{DeviceID: "d", Player: "A", Game: "G4", Plays: 1, Seconds: 1, LastPlayed: 400})

	if s.Count() != 3 {
		t.Fatalf("count = %d, want cap of 3", s.Count())
	}
	for _, e := range s.Recent(10) {
		if e.Game == "G1" {
			t.Error("G1 (least recently played) should have been evicted")
		}
	}
	// Updating an existing triple at capacity must not evict.
	if !s.Report(Report{DeviceID: "d", Player: "A", Game: "G4", Plays: 2, Seconds: 5, LastPlayed: 500}) {
		t.Error("update at capacity should be accepted")
	}
	if s.Count() != 3 {
		t.Errorf("count after update = %d, want 3", s.Count())
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "stats.json")
	s := Open(path, discardLog())
	s.Report(Report{DeviceID: "tv", Player: "David", Game: "SMW", Plays: 3, Seconds: 600, LastPlayed: 1234})
	s.Report(Report{DeviceID: "tv", Player: "Mary", Game: "Kart", Plays: 1, Seconds: 60, LastPlayed: 999})
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	s2 := Open(path, discardLog())
	if s2.Count() != 2 {
		t.Fatalf("reloaded count = %d, want 2", s2.Count())
	}
	players := s2.TopPlayers(BoardSize)
	if len(players) != 2 || players[0].Player != "David" || players[0].Seconds != 600 {
		t.Errorf("reloaded board = %+v, want David on top with 600s", players)
	}
	recent := s2.Recent(BoardSize)
	if recent[0].LastPlayed != 1234 || recent[0].Game != "SMW" {
		t.Errorf("reloaded recent = %+v, want SMW at 1234 first", recent[0])
	}
}

func TestOpenMissingAndCorruptFile(t *testing.T) {
	missing := Open(filepath.Join(t.TempDir(), "nope.json"), discardLog())
	if missing.Count() != 0 {
		t.Errorf("missing file: count = %d, want 0", missing.Count())
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	corrupt := Open(bad, discardLog())
	if corrupt.Count() != 0 {
		t.Errorf("corrupt file: count = %d, want 0", corrupt.Count())
	}
}

func TestEmptyBoardsAreEmptySlices(t *testing.T) {
	s := newTestStore(t)
	if got := s.TopPlayers(BoardSize); got == nil || len(got) != 0 {
		t.Errorf("TopPlayers on empty store = %v, want empty non-nil slice", got)
	}
	if got := s.TopGames(BoardSize); got == nil || len(got) != 0 {
		t.Errorf("TopGames on empty store = %v, want empty non-nil slice", got)
	}
	if got := s.Recent(BoardSize); got == nil || len(got) != 0 {
		t.Errorf("Recent on empty store = %v, want empty non-nil slice", got)
	}
}
