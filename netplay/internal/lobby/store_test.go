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

func TestPlayerLobby(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{
		Name:       "Fight night",
		Game:       "Mortal Kombat (USA).md",
		HostPlayer: "Mary",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Join(l.ID, "Terry", ""); err != nil {
		t.Fatalf("join: %v", err)
	}

	// Both the host and the guest resolve to the lobby, with its
	// game attached.
	for _, name := range []string{"Mary", "Terry"} {
		pub, ok := s.PlayerLobby(name)
		if !ok {
			t.Errorf("PlayerLobby(%q) not found, want the live lobby", name)
			continue
		}
		if pub.ID != l.ID || pub.Game != "Mortal Kombat (USA).md" {
			t.Errorf("PlayerLobby(%q) = %+v, want lobby %s with its game", name, pub, l.ID)
		}
	}

	// A player in no lobby, and the empty name, find nothing.
	if _, ok := s.PlayerLobby("Ghost"); ok {
		t.Error("PlayerLobby(Ghost) found a lobby, want none")
	}
	if _, ok := s.PlayerLobby(""); ok {
		t.Error("PlayerLobby(empty) found a lobby, want none")
	}

	// Once the lobby is deleted, its members no longer resolve.
	s.Delete(l.ID)
	if _, ok := s.PlayerLobby("Mary"); ok {
		t.Error("PlayerLobby(Mary) after delete found a lobby, want none")
	}
}

func TestWatchPasswordAndTokenClass(t *testing.T) {
	s, _ := testStore(t0)
	l, hostToken, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Watch(l.ID, "Fan", "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Errorf("watch wrong password err = %v, want ErrBadPassword", err)
	}
	if _, err := s.Watch(l.ID, "Fan", ""); !errors.Is(err, ErrBadPassword) {
		t.Errorf("watch empty password err = %v, want ErrBadPassword", err)
	}
	if _, err := s.Watch("ZZZZZ", "Fan", "hunter2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("watch missing lobby err = %v, want ErrNotFound", err)
	}

	specToken, err := s.Watch(l.ID, "Fan", "hunter2")
	if err != nil {
		t.Fatalf("watch with correct password: %v", err)
	}
	if specToken == "" || specToken == hostToken {
		t.Error("spectator token must be its own token")
	}
	// The spectator token authenticates as its own class, apart from
	// the guest token a Join would have issued.
	if role, ok := s.Authenticate(l.ID, specToken); !ok || role != RoleSpectator {
		t.Errorf("spectator auth = %v/%v, want spectator/true", role, ok)
	}
	if m, ok := s.LookupMember(l.ID, specToken); !ok || m.Name != "Fan" || m.Role != RoleSpectator {
		t.Errorf("spectator lookup = %+v/%v, want Fan/spectator/true", m, ok)
	}

	// Watching never fills a player seat: players still counts the
	// host alone, and the listing gains the spectator count.
	if pub, _ := s.Public(l.ID); pub.Players != 1 || pub.Spectators != 1 {
		t.Errorf("players/spectators = %d/%d, want 1/1", pub.Players, pub.Spectators)
	}

	// A spectator is not "in a lobby" for presence: only seats count.
	if _, ok := s.PlayerLobby("Fan"); ok {
		t.Error("PlayerLobby(Fan) found a lobby for a spectator, want none")
	}

	// The guest seat is still open after watching started.
	guestToken, err := s.Join(l.ID, "Brother", "hunter2")
	if err != nil {
		t.Fatalf("guest join after watch: %v", err)
	}
	if guestToken == specToken {
		t.Error("guest token equals spectator token")
	}
	if role, ok := s.Authenticate(l.ID, guestToken); !ok || role != RoleGuest {
		t.Errorf("guest auth = %v/%v, want guest/true", role, ok)
	}
	if pub, _ := s.Public(l.ID); pub.Players != 2 || pub.Spectators != 1 {
		t.Errorf("players/spectators = %d/%d, want 2/1", pub.Players, pub.Spectators)
	}
}

func TestWatchNameTakenAcrossRoom(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(l.ID, "Brother", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Watch(l.ID, "Fan", ""); err != nil {
		t.Fatal(err)
	}

	// A watcher cannot take the host's name, the guest's name, or
	// another watcher's name: the roster shows one person per name.
	for _, name := range []string{"David", "Brother", "Fan"} {
		if _, err := s.Watch(l.ID, name, ""); !errors.Is(err, ErrNameTaken) {
			t.Errorf("watch as %q err = %v, want ErrNameTaken", name, err)
		}
	}
}

func TestWatchCapLeavesPlayerSeatsAlone(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxSpectators {
		name := "Fan" + strings.Repeat("x", i) // Fan, Fanx, Fanxx, ...
		if _, err := s.Watch(l.ID, name, ""); err != nil {
			t.Fatalf("watch %d: %v", i, err)
		}
	}
	if _, err := s.Watch(l.ID, "OneTooMany", ""); !errors.Is(err, ErrSpectatorsFull) {
		t.Errorf("over-cap watch err = %v, want ErrSpectatorsFull", err)
	}
	if pub, _ := s.Public(l.ID); pub.Spectators != MaxSpectators {
		t.Errorf("spectators = %d, want %d", pub.Spectators, MaxSpectators)
	}

	// A full watcher bench does not block the guest seat.
	if _, err := s.Join(l.ID, "Brother", ""); err != nil {
		t.Errorf("guest join with full spectator bench: %v", err)
	}
}

func TestWatchRemovalRevokesSpectatorOnly(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}
	guestToken, err := s.Join(l.ID, "Brother", "")
	if err != nil {
		t.Fatal(err)
	}
	specToken, err := s.Watch(l.ID, "Fan", "")
	if err != nil {
		t.Fatal(err)
	}

	if !s.RemoveMemberByToken(l.ID, specToken) {
		t.Fatal("remove spectator returned false")
	}
	if _, ok := s.Authenticate(l.ID, specToken); ok {
		t.Error("revoked spectator token still authenticates")
	}
	if pub, _ := s.Public(l.ID); pub.Players != 2 || pub.Spectators != 0 {
		t.Errorf("players/spectators = %d/%d, want 2/0", pub.Players, pub.Spectators)
	}
	// The guest seat and token survive a spectator leaving.
	if role, ok := s.Authenticate(l.ID, guestToken); !ok || role != RoleGuest {
		t.Errorf("guest auth after spectator removal = %v/%v, want guest/true", role, ok)
	}
	// The freed name can watch again.
	if _, err := s.Watch(l.ID, "Fan", ""); err != nil {
		t.Errorf("re-watch after removal: %v", err)
	}
}

func TestMembersRosterOrder(t *testing.T) {
	s, _ := testStore(t0)
	l, _, err := s.Create(CreateInput{Name: "L", Game: "G", HostPlayer: "David"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Watch(l.ID, "EarlyFan", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(l.ID, "Brother", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Watch(l.ID, "LateFan", ""); err != nil {
		t.Fatal(err)
	}

	members, ok := s.Members(l.ID)
	if !ok {
		t.Fatal("Members returned not ok")
	}
	want := []Member{
		{Name: "David", Role: RoleHost},
		{Name: "EarlyFan", Role: RoleSpectator},
		{Name: "Brother", Role: RoleGuest},
		{Name: "LateFan", Role: RoleSpectator},
	}
	if len(members) != len(want) {
		t.Fatalf("members = %+v, want %+v", members, want)
	}
	for i := range want {
		if members[i] != want[i] {
			t.Errorf("members[%d] = %+v, want %+v", i, members[i], want[i])
		}
	}

	if _, ok := s.Members("ZZZZZ"); ok {
		t.Error("Members on a ghost lobby returned ok")
	}
}
