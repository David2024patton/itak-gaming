package netsession

import (
	"errors"
	"strings"
)

// Seat assignment (PROTOCOL.md section 4.9, SPECTATOR_DESIGN.md
// slice S4). The host client is the room's referee: it alone
// decides who fills the two player seats next, and it says so
// out loud with a seat_assign envelope so the guest and every
// spectator hear the same call at the same time. The envelope
// is a broadcast of a decision, not a request; the named player
// takes the seat through the normal room flow (joining as that
// seat, or being promoted from the watcher row), and naming
// someone else for seat 2 is how the current guest rotates
// out. No server state is involved.
//
// Honor rules: the send API refuses anyone but the host, and
// receivers never trust claims inside the envelope. A player
// session records an assignment only when it arrives from the
// peer (for the guest, the peer IS the host; see the fromPeer
// gate in step.go), and a watch session only when it carries
// the known host's stamp. An assignment from a spectator, or
// one naming nobody, or one for a seat that does not exist,
// is dropped on the floor.

// seatAssignQueueCap bounds the pending assignment queue the
// same way the watch sync queue is bounded: a referee making
// calls faster than the UI polls cannot grow it forever.
const seatAssignQueueCap = 16

// SeatAssign is one recorded seat call: who the host named,
// and for which seat (1 or 2).
type SeatAssign struct {
	Player string
	Seat   int
}

// SendSeatAssign broadcasts the host's seat call. Only the
// host client may send it (a guest or a spectator gets an
// error and nothing goes out), the player must be named, and
// the seat must be 1 or 2.
func (s *Session) SendSeatAssign(player string, seat int) error {
	if s.watch {
		return errors.New("a spectator cannot assign seats")
	}
	if !s.cfg.IsHost {
		return errors.New("only the host assigns seats")
	}
	player = strings.TrimSpace(player)
	if player == "" {
		return errors.New("seat assignment names nobody")
	}
	if seat != 1 && seat != 2 {
		return errors.New("seat must be 1 or 2")
	}
	return s.sendEnv(envelope{Type: "seat_assign", Player: player, Seat: seat})
}

// noteSeatAssignLocked records an assignment the reader has
// already attributed to a legitimate referee (the peer for a
// player session, the known host for a watch session).
// Malformed calls are ignored rather than trusted. Called
// with s.mu held.
func (s *Session) noteSeatAssignLocked(player string, seat int) {
	player = strings.TrimSpace(player)
	if player == "" || (seat != 1 && seat != 2) {
		return
	}
	s.seatAssigns = append(s.seatAssigns, SeatAssign{Player: player, Seat: seat})
	if len(s.seatAssigns) > seatAssignQueueCap {
		s.seatAssigns = s.seatAssigns[len(s.seatAssigns)-seatAssignQueueCap:]
	}
}

// PollSeatAssign drains the oldest recorded seat call, the
// same drain-one shape as PollWatchSync: the player (or
// spectator) it names, the seat, and false when no call is
// waiting.
func (s *Session) PollSeatAssign() (player string, seat int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seatAssigns) == 0 {
		return "", 0, false
	}
	a := s.seatAssigns[0]
	s.seatAssigns = s.seatAssigns[1:]
	return a.Player, a.Seat, true
}
