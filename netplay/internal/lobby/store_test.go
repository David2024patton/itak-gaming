package lobby

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// testStore returns a store pinned to a controllable clock.
func testStore(start time.Time) (*MemoryStore, *time.Time) {
	cur := new(time.Time)
	*cur = start
	s := NewMemoryStore()
	s.now = func() time.Time { return *cur }
	return s, cur
}

var t0 = time.Date(2026, 10, 11, 22, 0, 0, 0, time.UTC)

func TestCreateAndList(t *testing.T) {
	s, _ := testStore(t0)

	l, hostToken, err := s.Create(CreateInput{
		Name:       "Mario Kart night",
		Game:       "Super Mario Kart (USA).sfc",
		HostPlayer: "David",
		Password:   "secret",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if l.ID == "" || hostToken == "" {
		t.Fatal("create returned empty id or token")
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	pub := list[0]
	if pub.ID != l.ID || pub.Name != "Mario Kart night" || pub.HostPlayer != "David" {
		t.Errorf("public view wrong: %+v", pub)
	}
	if !pub.Locked {
		t.Error("password lobby should report locked")
	}
	if pub.Players != 1 || pub.MaxPlayers != DefaultMaxPlayers {
		t.Errorf("players/max = %d/%d, want 1/%d", pub.Players, pub.MaxPlayers, DefaultMaxPlayers)
	}

	// The public JSON must never carry secret material.
	raw, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"hash", "salt", "token", "secret"} {
		if strings.Contains(strings.ToLower(string(raw)), leak) {
			t.Errorf("public JSON leaks %q: %s", leak, raw)
		}
	}

	// The host token authenticates as host; garbage does not.
	if role, ok := s.Authenticate(l.ID, hostToken); !ok || role != RoleHost {
		t.Errorf("host auth = %v/%v, want host/true", role, ok)
	}
	if _, ok := s.Authenticate(l.ID, "deadbeef"); ok {
		t.Error("bogus token authenticated")
	}
	if _, ok := s.Authenticate("ZZZZZ", hostToken); ok {
		t.Error("token authenticated against the wrong lobby")
	}
}

func TestJoinPassword(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Join(l.ID, "Brother", "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Errorf("wrong password err = %v, want ErrBadPassword", err)
	}
	if _, err := s.Join(l.ID, "Brother", ""); !errors.Is(err, ErrBadPassword) {
		t.Errorf("empty password err = %v, want ErrBadPassword", err)
	}

	guestToken, err := s.Join(l.ID, "Brother", "hunter2")
	if err != nil {
		t.Fatalf("join with correct password: %v", err)
	}
	if role, ok := s.Authenticate(l.ID, guestToken); !ok || role != RoleGuest {
		t.Errorf("guest auth = %v/%v, want guest/true", role, ok)
	}
	if pub, _ := s.Public(l.ID); pub.Players != 2 {
		t.Errorf("players = %d, want 2", pub.Players)
	}
}

func TestJoinOpenLobbyNoPassword(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}
	if pub, _ := s.Public(l.ID); pub.Locked {
		t.Error("open lobby reported locked")
	}
	if _, err := s.Join(l.ID, " anyone", ""); err != nil {
		// note: names are stored verbatim; any non-empty name works
		t.Fatalf("join open lobby: %v", err)
	}
}

func TestJoinErrors(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David", MaxPlayers: 2})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Join("ZZZZZ", "X", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing lobby err = %v, want ErrNotFound", err)
	}
	if _, err := s.Join(l.ID, "David", ""); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name err = %v, want ErrNameTaken", err)
	}
	if _, err := s.Join(l.ID, "Guest", ""); err != nil {
		t.Fatalf("first guest join: %v", err)
	}
	if _, err := s.Join(l.ID, "Third", ""); !errors.Is(err, ErrFull) {
		t.Errorf("over-capacity err = %v, want ErrFull", err)
	}
}

func TestRemoveMemberFreesSlot(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David", MaxPlayers: 2})
	if err != nil {
		t.Fatal(err)
	}
	guestToken, err := s.Join(l.ID, "Guest", "")
	if err != nil {
		t.Fatal(err)
	}
	if !s.RemoveMemberByToken(l.ID, guestToken) {
		t.Fatal("remove member returned false")
	}
	if _, ok := s.Authenticate(l.ID, guestToken); ok {
		t.Error("revoked token still authenticates")
	}
	if _, err := s.Join(l.ID, "Replacement", ""); err != nil {
		t.Errorf("join after removal: %v", err)
	}
	if s.RemoveMemberByToken(l.ID, guestToken) {
		t.Error("second removal of same token returned true")
	}
}

func TestSweepEmptyLobbyExpires(t *testing.T) {
	s, cur := testStore(t0)
	if _, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"}); err != nil {
		t.Fatal(err)
	}
	*cur = t0.Add(5 * time.Minute)
	if n := s.Sweep(*cur); n != 0 || s.Count() != 1 {
		t.Fatalf("sweep at 5m reaped %d, count %d; want 0/1", n, s.Count())
	}
	*cur = t0.Add(EmptyLifetime + time.Second)
	if n := s.Sweep(*cur); n != 1 || s.Count() != 0 {
		t.Fatalf("sweep after EmptyLifetime reaped %d, count %d; want 1/0", n, s.Count())
	}
}

func TestSweepStaleAfterPeersLeave(t *testing.T) {
	s, cur := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}

	// Peers connect, play, and leave at t0.
	s.SetPeers(l.ID, 2)
	s.SetPeers(l.ID, 0)

	*cur = t0.Add(30 * time.Second)
	if n := s.Sweep(*cur); n != 0 {
		t.Fatalf("sweep 30s after peers left reaped %d, want 0", n)
	}
	*cur = t0.Add(StaleAfterPeers + time.Second)
	if n := s.Sweep(*cur); n != 1 || s.Count() != 0 {
		t.Fatalf("sweep after StaleAfterPeers reaped %d, count %d; want 1/0", n, s.Count())
	}
}

func TestSweepNeverReapsConnectedLobby(t *testing.T) {
	s, cur := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetPeers(l.ID, 1)
	*cur = t0.Add(24 * time.Hour)
	if n := s.Sweep(*cur); n != 0 || s.Count() != 1 {
		t.Fatalf("sweep reaped a connected lobby: %d, count %d", n, s.Count())
	}
}

func TestCreateCapEnforced(t *testing.T) {
	s, _ := testStore(t0)
	s.maxLobbies = 2
	for i := range 2 {
		if _, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"}); !errors.Is(err, ErrTooMany) {
		t.Errorf("over-cap err = %v, want ErrTooMany", err)
	}
}
