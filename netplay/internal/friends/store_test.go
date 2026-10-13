package friends

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return Open("", discardLog())
}

func TestAddAndListSorted(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"Terry", "alice", "Mary"} {
		changed, err := s.Add("David", name)
		if err != nil {
			t.Fatalf("Add(%q): %v", name, err)
		}
		if !changed {
			t.Errorf("Add(%q) should report a change", name)
		}
	}
	got := s.List("David")
	want := []string{"Mary", "Terry", "alice"} // plain byte order, case-sensitive
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("List = %v, want %v", got, want)
	}
	if s.Count() != 3 {
		t.Errorf("Count = %d, want 3", s.Count())
	}
}

func TestAddIdempotent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Add("David", "Mary")
	if err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	if changed {
		t.Error("re-Add should report no change")
	}
	if got := s.List("David"); len(got) != 1 {
		t.Errorf("List = %v, want exactly one Mary", got)
	}
}

func TestAddTrimsAndRejectsEmpty(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("  David  ", "  Mary "); err != nil {
		t.Fatalf("Add with padded names: %v", err)
	}
	if got := s.List("David"); len(got) != 1 || got[0] != "Mary" {
		t.Errorf("List = %v, want [Mary] (names stored trimmed)", got)
	}
	for _, tc := range [][2]string{
		{"", "Mary"},
		{"David", ""},
		{"   ", "Mary"},
		{"David", "  "},
		{"", ""},
	} {
		if _, err := s.Add(tc[0], tc[1]); !errors.Is(err, ErrEmptyName) {
			t.Errorf("Add(%q, %q) err = %v, want ErrEmptyName", tc[0], tc[1], err)
		}
		if _, err := s.Remove(tc[0], tc[1]); !errors.Is(err, ErrEmptyName) {
			t.Errorf("Remove(%q, %q) err = %v, want ErrEmptyName", tc[0], tc[1], err)
		}
	}
}

func TestAddRejectsSelf(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("David", "David"); !errors.Is(err, ErrSelf) {
		t.Errorf("self follow err = %v, want ErrSelf", err)
	}
	// Trimming happens before the self check.
	if _, err := s.Add("David", "  David "); !errors.Is(err, ErrSelf) {
		t.Errorf("padded self follow err = %v, want ErrSelf", err)
	}
	// Case differs: a different player, allowed.
	if _, err := s.Add("David", "david"); err != nil {
		t.Errorf("case-different follow should be allowed, got %v", err)
	}
}

func TestAddNameTooLong(t *testing.T) {
	s := newTestStore(t)
	long := make([]rune, MaxNameChars+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := s.Add("David", string(long)); !errors.Is(err, ErrNameLong) {
		t.Errorf("long friend err = %v, want ErrNameLong", err)
	}
}

func TestAddCap(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < MaxFriendsPerPlayer; i++ {
		if _, err := s.Add("David", fmt.Sprintf("friend%03d", i)); err != nil {
			t.Fatalf("Add #%d: %v", i, err)
		}
	}
	if _, err := s.Add("David", "one too many"); !errors.Is(err, ErrTooMany) {
		t.Errorf("add past cap err = %v, want ErrTooMany", err)
	}
	// Re-adding an existing friend at the cap still succeeds:
	// idempotence wins over the cap because nothing grows.
	if _, err := s.Add("David", "friend000"); err != nil {
		t.Errorf("re-Add at cap: %v", err)
	}
	// The cap is per player, not global.
	if _, err := s.Add("Mary", "David"); err != nil {
		t.Errorf("another player adding: %v", err)
	}
}

func TestRemove(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("David", "Terry"); err != nil {
		t.Fatal(err)
	}

	changed, err := s.Remove("David", "Mary")
	if err != nil || !changed {
		t.Errorf("Remove existing = (%v, %v), want (true, nil)", changed, err)
	}
	if got := s.List("David"); len(got) != 1 || got[0] != "Terry" {
		t.Errorf("List after remove = %v, want [Terry]", got)
	}

	// Removing again, and removing from a player with no list, are
	// quiet no-ops.
	if changed, err := s.Remove("David", "Mary"); err != nil || changed {
		t.Errorf("re-Remove = (%v, %v), want (false, nil)", changed, err)
	}
	if changed, err := s.Remove("Nobody", "Mary"); err != nil || changed {
		t.Errorf("Remove with no list = (%v, %v), want (false, nil)", changed, err)
	}
}

func TestListUnknownPlayerIsEmptyNotNil(t *testing.T) {
	s := newTestStore(t)
	got := s.List("Ghost")
	if got == nil {
		t.Fatal("List for unknown player returned nil; the API marshals that as null")
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

func TestFollowsAreOneDirectional(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	if got := s.List("Mary"); len(got) != 0 {
		t.Errorf("Mary's list = %v, want empty: a follow is not mutual", got)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "friends.json")

	s := Open(path, discardLog())
	if _, err := s.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("David", "Terry"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("Mary", "David"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("friends file missing after Save: %v", err)
	}

	reopened := Open(path, discardLog())
	if got := reopened.List("David"); fmt.Sprint(got) != "[Mary Terry]" {
		t.Errorf("reopened David list = %v, want [Mary Terry]", got)
	}
	if got := reopened.List("Mary"); fmt.Sprint(got) != "[David]" {
		t.Errorf("reopened Mary list = %v, want [David]", got)
	}
	if reopened.Count() != 3 {
		t.Errorf("reopened Count = %d, want 3", reopened.Count())
	}

	// A removal survives the same trip.
	if _, err := reopened.Remove("David", "Terry"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Save(); err != nil {
		t.Fatal(err)
	}
	again := Open(path, discardLog())
	if got := again.List("David"); fmt.Sprint(got) != "[Mary]" {
		t.Errorf("after remove, David list = %v, want [Mary]", got)
	}
}

func TestCorruptFileDegradesToEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "friends.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Open(path, discardLog())
	if s.Count() != 0 {
		t.Errorf("Count = %d, want 0 for a corrupt file", s.Count())
	}
	// And the store still works afterwards.
	if _, err := s.Add("David", "Mary"); err != nil {
		t.Errorf("Add after corrupt load: %v", err)
	}
}

func TestEphemeralStoreNeverTouchesDisk(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("David", "Mary"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Errorf("ephemeral Save should be a no-op, got %v", err)
	}
}
