package netsession

import (
	"errors"
	"strings"
)

// Tournament naming (PROTOCOL.md section 8, SPECTATOR_DESIGN.md
// slice S6). A hosted room can run under a tournament name:
// the host's client collects it on the Online screen, and the
// room's match results are reported to the server's tournament
// board under it. The name itself has to reach everyone in the
// room (the guest's waiting room shows it, a watcher's screen
// too), so it travels as a room envelope from the host:
//
//	{"type": "tournament", "name": "Friday Night Fights"}
//
// Like the seats (section 4.9) and the Got Next line (section
// 4.10), this is referee state: only the host names the
// tournament, and receivers believe the envelope only on the
// known host's stamp (the guest's peer, the watcher's seat 1
// holder, the host resolution gotnext.go already uses). A
// claim stamped by anyone else, and an unstamped claim, are
// dropped. The name is room state, not match state: it
// survives match_end on purpose, and the host re-sends it as
// the room lands again anyway, which also covers a client
// whose reader was not ready for the first send. It never
// touches lockstep: no frames, no inputs, no hashes.

// tournamentNameMax bounds the name in characters, the same
// cap the server's results store puts on tournament names
// (PROTOCOL.md section 8).
const tournamentNameMax = 80

// cleanTournamentName trims a tournament name and caps it at
// the server's limit, in runes rather than bytes.
func cleanTournamentName(name string) string {
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > tournamentNameMax {
		name = string(r[:tournamentNameMax])
	}
	return name
}

// SendTournamentName broadcasts the room's tournament name.
// Host player sessions only: the guest and the watchers learn
// the name, they never mint it, and an empty name names
// nothing. The host's own session records the name at once,
// the way its own match_end send is authoritative: the
// broadcast does not come back to settle it.
func (s *Session) SendTournamentName(name string) error {
	if s.watch {
		return errors.New("a spectator does not name the tournament")
	}
	if !s.cfg.IsHost {
		return errors.New("only the host names the tournament")
	}
	name = cleanTournamentName(name)
	if name == "" {
		return errors.New("the tournament name is empty")
	}
	if err := s.sendEnv(envelope{Type: "tournament", Name: name}); err != nil {
		return err
	}
	s.mu.Lock()
	s.tournamentName = name
	s.mu.Unlock()
	return nil
}

// TournamentName returns the room's tournament name as this
// session knows it: "" for a casual room, or for a client the
// host's word has not reached yet.
func (s *Session) TournamentName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tournamentName
}

// noteTournamentLocked folds an inbound tournament envelope
// into the room state. The name is believed only on the known
// host's stamp: for the guest that is its peer, for a watcher
// the seat 1 holder, for the host itself only its own name
// (its own sends settle locally and never need the wire). A
// guest's claim, a spectator's claim, and an unstamped
// envelope all leave the name alone. Called by the readers
// with s.mu held.
func (s *Session) noteTournamentLocked(e envelope) {
	host := s.gotNextHostNameLocked()
	if host == "" || e.From != host {
		return
	}
	if name := cleanTournamentName(e.Name); name != "" {
		s.tournamentName = name
	}
}
