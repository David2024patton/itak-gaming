package matches

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return Open("", discardLog())
}

// fakeClock replaces the store's clock with one the test steps by
// hand, so timestamps and ordering are deterministic.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time {
	c.t = c.t.Add(time.Second)
	return c.t
}

func newClockedStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	s := newTestStore(t)
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s.now = c.now
	return s, c
}

func mustReport(t *testing.T, s *Store, r MatchReport) MatchEntry {
	t.Helper()
	entry, err := s.ReportMatch(r)
	if err != nil {
		t.Fatalf("ReportMatch(%+v): %v", r, err)
	}
	return entry
}

func recordByName(t *testing.T, s *Store, name string) PlayerRecord {
	t.Helper()
	for _, rec := range s.Leaderboard(LeaderboardSize) {
		if rec.Name == name {
			return rec
		}
	}
	t.Fatalf("no record for %q", name)
	return PlayerRecord{}
}

func TestReportMatchAccounting(t *testing.T) {
	s, _ := newClockedStore(t)
	entry := mustReport(t, s, MatchReport{
		Tournament: "Friday Night Fights",
		Game:       "Mortal Kombat (USA).md",
		Winner:     "David",
		Loser:      "Brother",
		Host:       "David",
	})
	if entry.Winner != "David" || entry.Loser != "Brother" ||
		entry.Tournament != "Friday Night Fights" || entry.Game != "Mortal Kombat (USA).md" {
		t.Errorf("entry = %+v, want the reported match", entry)
	}
	if entry.At == 0 {
		t.Error("entry.At = 0, want a server timestamp")
	}

	winner := recordByName(t, s, "David")
	if winner.Wins != 1 || winner.Losses != 0 || winner.Matches != 1 || winner.TournamentsWon != 0 {
		t.Errorf("winner record = %+v, want 1 win, 0 losses, 1 match, 0 titles", winner)
	}
	if winner.LastTournament != "Friday Night Fights" || winner.LastGame != "Mortal Kombat (USA).md" {
		t.Errorf("winner stamps = %q/%q, want the match's tournament and game",
			winner.LastTournament, winner.LastGame)
	}
	if winner.LastPlayed != entry.At {
		t.Errorf("winner LastPlayed = %d, want the match time %d", winner.LastPlayed, entry.At)
	}

	loser := recordByName(t, s, "Brother")
	if loser.Wins != 0 || loser.Losses != 1 || loser.Matches != 1 {
		t.Errorf("loser record = %+v, want 0 wins, 1 loss, 1 match", loser)
	}

	// A rematch the other way accumulates on the same records.
	mustReport(t, s, MatchReport{
		Tournament: "Friday Night Fights",
		Game:       "Mortal Kombat (USA).md",
		Winner:     "Brother",
		Loser:      "David",
		Host:       "David",
	})
	winner = recordByName(t, s, "David")
	if winner.Wins != 1 || winner.Losses != 1 || winner.Matches != 2 {
		t.Errorf("after rematch, David = %+v, want 1 win, 1 loss, 2 matches", winner)
	}
	if s.PlayerCount() != 2 {
		t.Errorf("PlayerCount = %d, want 2", s.PlayerCount())
	}
}

func TestReportMatchValidation(t *testing.T) {
	s := newTestStore(t)
	base := MatchReport{
		Tournament: "Friday Night Fights",
		Game:       "Mortal Kombat (USA).md",
		Winner:     "David",
		Loser:      "Brother",
		Host:       "David",
	}
	longName := strings.Repeat("x", MaxNameChars+1)
	cases := []struct {
		name   string
		mutate func(*MatchReport)
		want   error
	}{
		{"empty tournament", func(r *MatchReport) { r.Tournament = "" }, ErrEmptyTournament},
		{"blank tournament", func(r *MatchReport) { r.Tournament = "   " }, ErrEmptyTournament},
		{"empty winner", func(r *MatchReport) { r.Winner = "" }, ErrEmptyWinner},
		{"blank winner", func(r *MatchReport) { r.Winner = "  " }, ErrEmptyWinner},
		{"empty loser", func(r *MatchReport) { r.Loser = "" }, ErrEmptyLoser},
		{"empty host", func(r *MatchReport) { r.Host = "" }, ErrEmptyHost},
		{"self match", func(r *MatchReport) { r.Loser = "David" }, ErrSelfMatch},
		{"self match after clean", func(r *MatchReport) { r.Loser = "  David " }, ErrSelfMatch},
		{"winner too long", func(r *MatchReport) { r.Winner = longName }, ErrNameLong},
		{"host too long", func(r *MatchReport) { r.Host = longName }, ErrNameLong},
		{"tournament too long", func(r *MatchReport) {
			r.Tournament = strings.Repeat("x", MaxTournamentChars+1)
		}, ErrTournamentLong},
		{"game too long", func(r *MatchReport) {
			r.Game = strings.Repeat("x", MaxGameChars+1)
		}, ErrGameLong},
		{"winner display too long", func(r *MatchReport) {
			r.WinnerDisplay = strings.Repeat("x", MaxDisplayChars+1)
		}, ErrDisplayLong},
		{"loser region too long", func(r *MatchReport) {
			r.LoserDisplay = "Brother P."
			r.LoserRegion = strings.Repeat("x", MaxRegionChars+1)
		}, ErrRegionLong},
	}
	for _, tc := range cases {
		r := base
		tc.mutate(&r)
		if _, err := s.ReportMatch(r); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if s.PlayerCount() != 0 {
		t.Errorf("PlayerCount = %d, want 0: rejected reports must record nothing", s.PlayerCount())
	}
	if got := s.Recent(RecentSize); len(got) != 0 {
		t.Errorf("Recent = %v, want empty after rejected reports", got)
	}
}

func TestReportMatchCleansNames(t *testing.T) {
	s := newTestStore(t)
	mustReport(t, s, MatchReport{
		Tournament: "  Friday Night Fights\t",
		Game:       "Mortal Kombat (USA).md",
		Winner:     "  Da\x00vid ",
		Loser:      "Brother\n",
		Host:       " David",
	})
	rec := recordByName(t, s, "David")
	if rec.LastTournament != "Friday Night Fights" {
		t.Errorf("LastTournament = %q, want cleaned %q", rec.LastTournament, "Friday Night Fights")
	}
	recent := s.Recent(1)
	if len(recent) != 1 || recent[0].Winner != "David" || recent[0].Loser != "Brother" {
		t.Errorf("Recent = %+v, want cleaned names", recent)
	}
}

func TestReportChampion(t *testing.T) {
	s, _ := newClockedStore(t)
	// A champion with no reported matches gets a fresh record.
	if err := s.ReportChampion("Friday Night Fights", "David", "David", "", ""); err != nil {
		t.Fatalf("ReportChampion: %v", err)
	}
	rec := recordByName(t, s, "David")
	if rec.TournamentsWon != 1 || rec.Wins != 0 || rec.Matches != 0 {
		t.Errorf("champion record = %+v, want 1 title, 0 wins, 0 matches", rec)
	}
	if rec.LastTitle != "Friday Night Fights" || rec.LastTournament != "Friday Night Fights" {
		t.Errorf("title stamps = %q/%q, want the tournament", rec.LastTitle, rec.LastTournament)
	}
	if rec.LastPlayed == 0 {
		t.Error("LastPlayed = 0, want the championship time")
	}

	// A second title accumulates and replaces the latest title.
	if err := s.ReportChampion("Saturday Showdown", "David", "David", "", ""); err != nil {
		t.Fatal(err)
	}
	rec = recordByName(t, s, "David")
	if rec.TournamentsWon != 2 || rec.LastTitle != "Saturday Showdown" {
		t.Errorf("after second title: %+v, want 2 titles, latest Saturday Showdown", rec)
	}
	// Champions do not touch the recent match feed.
	if got := s.Recent(RecentSize); len(got) != 0 {
		t.Errorf("Recent = %v, want empty: champions are not matches", got)
	}
}

func TestReportChampionValidation(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name                       string
		tournament, champion, host string
		display, region            string
		want                       error
	}{
		{"empty tournament", "", "David", "David", "", "", ErrEmptyTournament},
		{"empty champion", "Cup", "", "David", "", "", ErrEmptyChampion},
		{"blank champion", "Cup", "   ", "David", "", "", ErrEmptyChampion},
		{"empty host", "Cup", "David", "", "", "", ErrEmptyHost},
		{"champion too long", "Cup", strings.Repeat("x", MaxNameChars+1), "David", "", "", ErrNameLong},
		{"display too long", "Cup", "David", "David", strings.Repeat("x", MaxDisplayChars+1), "", ErrDisplayLong},
	}
	for _, tc := range cases {
		err := s.ReportChampion(tc.tournament, tc.champion, tc.host, tc.display, tc.region)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if s.PlayerCount() != 0 {
		t.Errorf("PlayerCount = %d, want 0 after rejected champions", s.PlayerCount())
	}
}

func TestLeaderboardOrdering(t *testing.T) {
	s := newTestStore(t)
	play := func(winner, loser string) {
		mustReport(t, s, MatchReport{
			Tournament: "Cup", Game: "Game (USA).md",
			Winner: winner, Loser: loser, Host: "Host",
		})
	}
	// Titles first: Champ has 2.
	for i := 0; i < 2; i++ {
		if err := s.ReportChampion("Cup", "Champ", "Host", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	// Carol and Bob tie on 1 title and 3 wins; Carol has more
	// matches (5 to 3). Alice has 1 title but only 1 win.
	if err := s.ReportChampion("Cup", "Carol", "Host", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportChampion("Cup", "Bob", "Host", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportChampion("Cup", "Alice", "Host", "", ""); err != nil {
		t.Fatal(err)
	}
	play("Carol", "Opp1")
	play("Carol", "Opp2")
	play("Carol", "Opp3")
	play("Opp4", "Carol")
	play("Opp5", "Carol")
	play("Bob", "Opp1")
	play("Bob", "Opp2")
	play("Bob", "Opp3")
	play("Alice", "Opp1")
	// Dave and Erin are identical but for the name: 1 win,
	// 2 matches each, no titles. Their conquerors only ever win
	// once in one match, so Dave and Erin head the 1-win group
	// on the matches tie-break.
	play("Dave", "Fall1")
	play("Fall2", "Dave")
	play("Erin", "Fall3")
	play("Fall4", "Erin")

	board := s.Leaderboard(LeaderboardSize)
	var names []string
	for _, rec := range board {
		names = append(names, rec.Name)
	}
	wantHead := []string{"Champ", "Carol", "Bob", "Alice", "Dave", "Erin"}
	if len(names) < len(wantHead) || fmt.Sprint(names[:len(wantHead)]) != fmt.Sprint(wantHead) {
		t.Errorf("board head = %v, want %v", names, wantHead)
	}
}

func TestLeaderboardCap(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < LeaderboardSize+5; i++ {
		mustReport(t, s, MatchReport{
			Tournament: "Cup", Game: "Game (USA).md",
			Winner: fmt.Sprintf("Winner%03d", i), Loser: "PunchingBag", Host: "Host",
		})
	}
	board := s.Leaderboard(LeaderboardSize)
	if len(board) != LeaderboardSize {
		t.Fatalf("board rows = %d, want the %d cap", len(board), LeaderboardSize)
	}
	for _, rec := range board {
		if rec.Wins != 1 {
			t.Errorf("row %q has %d wins; the 0-win high-match player should be cut by ranking, not by cap order alone", rec.Name, rec.Wins)
		}
	}
}

func TestLeaderboardEmptyIsEmptySlice(t *testing.T) {
	s := newTestStore(t)
	if got := s.Leaderboard(LeaderboardSize); got == nil || len(got) != 0 {
		t.Errorf("Leaderboard = %v, want an empty slice", got)
	}
	if got := s.Recent(RecentSize); got == nil || len(got) != 0 {
		t.Errorf("Recent = %v, want an empty slice", got)
	}
}

func TestIdentityAttachment(t *testing.T) {
	s := newTestStore(t)
	mustReport(t, s, MatchReport{
		Tournament: "Cup", Game: "Game (USA).md",
		Winner: "David", Loser: "Brother", Host: "David",
		WinnerDisplay: "David P.", WinnerRegion: "KY",
		LoserDisplay: "Brother P.", LoserRegion: "MO",
	})
	rec := recordByName(t, s, "David")
	if rec.Display != "David P." || rec.Region != "KY" {
		t.Errorf("winner identity = %q/%q, want David P./KY", rec.Display, rec.Region)
	}
	rec = recordByName(t, s, "Brother")
	if rec.Display != "Brother P." || rec.Region != "MO" {
		t.Errorf("loser identity = %q/%q, want Brother P./MO", rec.Display, rec.Region)
	}

	// A report with no identity leaves the stored one alone.
	mustReport(t, s, MatchReport{
		Tournament: "Cup", Game: "Game (USA).md",
		Winner: "David", Loser: "Brother", Host: "David",
	})
	rec = recordByName(t, s, "David")
	if rec.Display != "David P." || rec.Region != "KY" {
		t.Errorf("after identity-free report, identity = %q/%q, want unchanged", rec.Display, rec.Region)
	}

	// A newer identity replaces the old one (latest wins), and a
	// champion report can carry one too.
	mustReport(t, s, MatchReport{
		Tournament: "Cup", Game: "Game (USA).md",
		Winner: "David", Loser: "Brother", Host: "David",
		WinnerDisplay: "David R. P.", WinnerRegion: "KY",
	})
	if err := s.ReportChampion("Cup", "Brother", "David", "Brother P.", "KS"); err != nil {
		t.Fatal(err)
	}
	rec = recordByName(t, s, "David")
	if rec.Display != "David R. P." {
		t.Errorf("identity = %q, want the newest report's", rec.Display)
	}
	rec = recordByName(t, s, "Brother")
	if rec.Region != "KS" {
		t.Errorf("champion identity region = %q, want KS from the champion report", rec.Region)
	}
}

func TestRecentOrderingAndStoredCap(t *testing.T) {
	s, _ := newClockedStore(t)
	for i := 0; i < MaxRecentStored+5; i++ {
		mustReport(t, s, MatchReport{
			Tournament: "Cup", Game: "Game (USA).md",
			Winner: fmt.Sprintf("W%02d", i), Loser: "Loser", Host: "Host",
		})
	}
	recent := s.Recent(RecentSize)
	if len(recent) != RecentSize {
		t.Fatalf("Recent(%d) returned %d rows", RecentSize, len(recent))
	}
	if recent[0].Winner != fmt.Sprintf("W%02d", MaxRecentStored+4) {
		t.Errorf("newest = %q, want the last reported match", recent[0].Winner)
	}
	for i := 1; i < len(recent); i++ {
		if recent[i-1].At <= recent[i].At {
			t.Fatalf("recent feed not newest first at %d: %d then %d", i, recent[i-1].At, recent[i].At)
		}
	}
	if got := s.Recent(MaxRecentStored + 10); len(got) != MaxRecentStored {
		t.Errorf("stored feed = %d rows, want the %d cap", len(got), MaxRecentStored)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matches.json")
	s, _ := newClockedStore(t)
	s.path = path
	mustReport(t, s, MatchReport{
		Tournament: "Friday Night Fights", Game: "Mortal Kombat (USA).md",
		Winner: "David", Loser: "Brother", Host: "David",
		WinnerDisplay: "David P.", WinnerRegion: "KY",
	})
	mustReport(t, s, MatchReport{
		Tournament: "Friday Night Fights", Game: "Mortal Kombat (USA).md",
		Winner: "Brother", Loser: "David", Host: "David",
	})
	if err := s.ReportChampion("Friday Night Fights", "David", "David", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("matches file missing after Save: %v", err)
	}

	reopened := Open(path, discardLog())
	want := s.Leaderboard(LeaderboardSize)
	got := reopened.Leaderboard(LeaderboardSize)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reopened board = %+v, want %+v", got, want)
	}
	wantRecent := s.Recent(RecentSize)
	gotRecent := reopened.Recent(RecentSize)
	if fmt.Sprint(gotRecent) != fmt.Sprint(wantRecent) {
		t.Errorf("reopened recent = %+v, want %+v", gotRecent, wantRecent)
	}
}

func TestCorruptFileDegradesToEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matches.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Open(path, discardLog())
	if s.PlayerCount() != 0 {
		t.Errorf("PlayerCount = %d, want 0 for a corrupt file", s.PlayerCount())
	}
	// And the store still works afterwards.
	mustReport(t, s, MatchReport{
		Tournament: "Cup", Game: "Game (USA).md",
		Winner: "David", Loser: "Brother", Host: "David",
	})
}

func TestEphemeralStoreNeverTouchesDisk(t *testing.T) {
	s := newTestStore(t)
	mustReport(t, s, MatchReport{
		Tournament: "Cup", Game: "Game (USA).md",
		Winner: "David", Loser: "Brother", Host: "David",
	})
	if err := s.Save(); err != nil {
		t.Errorf("ephemeral Save should be a no-op, got %v", err)
	}
}
