package netsession

import (
	"errors"
	"strings"
)

// Got Next (PROTOCOL.md section 4.10, SPECTATOR_DESIGN.md
// slice S5): the winner-stays line. Anyone in the room (a
// watcher, the guest, even the host) can join the line with
// a got_next envelope; the host client is the referee, like
// it is for seats (section 4.9). The host alone tracks the
// authoritative order: it folds every join and leave into
// its list and broadcasts the result as got_next_queue, and
// every other client mirrors that broadcast and nothing
// else. When a match ends, the host's UI reads the head of
// the line and names it for the loser's seat (seat 2 only in
// v1: seat 1 is the host referee's house seat), and the S4
// seat machinery does the rest.
//
// The line is room state, not match state: it survives
// match_end on purpose (returnToRoomLocked never touches
// it), because the whole point is who plays NEXT. Taking a
// seat removes a name (NoteSeatTaken), and leaving the room
// removes a name (the readers prune on peer_left and
// spectator_left), so the line never holds ghosts.
//
// Like chat, the line never touches lockstep: no frames, no
// inputs, no hashes, no phase changes live here.

// gotNextQueueCap bounds the tracked line. A room holds at
// most two players and ten spectators; 32 leaves generous
// headroom while keeping a forged queue broadcast from
// stuffing the list.
const gotNextQueueCap = 32

// SendGotNext joins the line (join true) or leaves it (join
// false). Anyone in the room may send it, players and
// spectators alike. The host applies its own call to its
// list at once (its envelope also goes out, and every other
// referee-side fold ignores it: only the host folds
// got_next). Everyone else's local list changes when the
// host's next got_next_queue broadcast lands, which is what
// makes the host's order the only order.
func (s *Session) SendGotNext(join bool) error {
	action := "leave"
	if join {
		action = "join"
	}
	if err := s.sendEnv(envelope{Type: "got_next", Action: action}); err != nil {
		return err
	}
	if s.isGotNextReferee() && s.cfg.PlayerName != "" {
		s.mu.Lock()
		s.applyGotNextLocked(s.cfg.PlayerName, join)
		s.mu.Unlock()
	}
	return nil
}

// BroadcastGotNextQueue sends the host's authoritative line
// order to the room. Host player sessions only: the guest
// and the watchers mirror the line, they never mint it. A
// successful broadcast is what clears the dirty flag
// (GotNextDirty), so a failed send is retried by the UI on
// its next poll instead of being forgotten.
func (s *Session) BroadcastGotNextQueue() error {
	if s.watch {
		return errors.New("a spectator does not run the line")
	}
	if !s.cfg.IsHost {
		return errors.New("only the host runs the line")
	}
	s.mu.Lock()
	queue := make([]string, len(s.gotNextQueue))
	copy(queue, s.gotNextQueue)
	s.mu.Unlock()
	if err := s.sendEnv(envelope{Type: "got_next_queue", Queue: queue}); err != nil {
		return err
	}
	s.ClearGotNextDirty()
	return nil
}

// GotNextQueue returns the tracked line, head first. The
// result is a copy; callers may keep it.
func (s *Session) GotNextQueue() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.gotNextQueue))
	copy(out, s.gotNextQueue)
	return out
}

// GotNextDirty reports whether the host's line changed
// since the last successful broadcast. Always false on
// non-host sessions: they hold no authority to broadcast.
func (s *Session) GotNextDirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotNextDirty
}

// ClearGotNextDirty marks the line as broadcast.
// BroadcastGotNextQueue calls it after a successful send;
// it is exported so a UI that broadcasts by another path
// can settle the flag itself.
func (s *Session) ClearGotNextDirty() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gotNextDirty = false
}

// PollGotNext returns the tracked line and whether it
// changed since the last broadcast, the pair the host UI
// polls on its room ticker: changed means "rebroadcast me".
// Unlike PollSeatAssign it drains nothing; the dirty flag
// clears only when a broadcast succeeds.
func (s *Session) PollGotNext() (queue []string, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.gotNextQueue))
	copy(out, s.gotNextQueue)
	return out, s.gotNextDirty
}

// NoteSeatTaken records that name now holds a seat, so it
// comes off the line: you cannot be playing and waiting to
// play at once. The host UI calls it right after a seat
// assignment goes out (its own manual pick, a random pick,
// or a Got Next promotion). On the host the removal marks
// the line dirty for rebroadcast; elsewhere it just keeps
// the local mirror fresh until the broadcast lands.
func (s *Session) NoteSeatTaken(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteGotNextRemoveLocked(name)
}

// isGotNextReferee reports whether this session keeps the
// authoritative line: the host's player session. Watch
// sessions never referee, and neither does the guest.
func (s *Session) isGotNextReferee() bool {
	return !s.watch && s.cfg.IsHost
}

// applyGotNextLocked folds one join or leave for name into
// the tracked line, referee side. Joining twice is a no-op
// (one spot per name), as is leaving a line you are not in.
// A real change on the host marks the line dirty. Called
// with s.mu held.
func (s *Session) applyGotNextLocked(name string, join bool) {
	if join {
		for _, n := range s.gotNextQueue {
			if n == name {
				return
			}
		}
		if len(s.gotNextQueue) >= gotNextQueueCap {
			return
		}
		s.gotNextQueue = append(s.gotNextQueue, name)
	} else {
		if !removeGotNextName(&s.gotNextQueue, name) {
			return
		}
	}
	if s.isGotNextReferee() {
		s.gotNextDirty = true
	}
}

// noteGotNextLocked folds an inbound got_next into the
// line. Only the referee folds these at all: the guest and
// the watchers take their line from got_next_queue alone,
// so a join the host never saw (or pruned) cannot haunt
// their lists. The sender is the envelope's stamp; an
// unstamped call names nobody and is dropped. Called by
// the readers with s.mu held.
func (s *Session) noteGotNextLocked(e envelope) {
	if !s.isGotNextReferee() {
		return
	}
	name := strings.TrimSpace(e.From)
	if name == "" {
		return
	}
	switch e.Action {
	case "join":
		s.applyGotNextLocked(name, true)
	case "leave":
		s.applyGotNextLocked(name, false)
	}
}

// noteGotNextQueueLocked folds an inbound got_next_queue:
// the line is replaced wholesale, but only when the sender
// is the known host (the guest's peer, the watcher's seat 1
// holder). A queue minted by anyone else, a spectator or
// the guest, is a claim, not the referee's word, and is
// ignored. Names are cleaned (trimmed, nonempty, one spot
// each) and the list is capped before it is trusted. Called
// by the readers with s.mu held.
func (s *Session) noteGotNextQueueLocked(e envelope) {
	host := s.gotNextHostNameLocked()
	if host == "" || e.From != host {
		return
	}
	queue := make([]string, 0, len(e.Queue))
	for _, raw := range e.Queue {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		dup := false
		for _, n := range queue {
			if n == name {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		queue = append(queue, name)
		if len(queue) >= gotNextQueueCap {
			break
		}
	}
	s.gotNextQueue = queue
}

// gotNextHostNameLocked is whose got_next_queue this
// session believes: for a watch session the seat 1 holder,
// for the guest its peer, for the host itself (its own
// broadcasts never come back, but the rule stays uniform).
// Called with s.mu held.
func (s *Session) gotNextHostNameLocked() string {
	if s.watch {
		return s.watchHost
	}
	if s.cfg.IsHost {
		return s.cfg.PlayerName
	}
	return s.peerName
}

// noteGotNextRemoveLocked drops name from the tracked line
// wherever it sits. A removal that changes the host's list
// marks it dirty for rebroadcast. Called with s.mu held.
func (s *Session) noteGotNextRemoveLocked(name string) {
	if removeGotNextName(&s.gotNextQueue, name) && s.isGotNextReferee() {
		s.gotNextDirty = true
	}
}

// noteMemberLeftLocked is the readers' hook for the room's
// departure notices (peer_left, spectator_left): whoever
// left the room leaves the line too, or the head of the
// line would be a ghost when the next match ends. Called
// with s.mu held.
func (s *Session) noteMemberLeftLocked(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	s.noteGotNextRemoveLocked(name)
}

// removeGotNextName deletes the first (only) occurrence of
// name from the queue in place and reports whether it was
// there.
func removeGotNextName(queue *[]string, name string) bool {
	q := *queue
	for i, n := range q {
		if n == name {
			*queue = append(q[:i], q[i+1:]...)
			return true
		}
	}
	return false
}
