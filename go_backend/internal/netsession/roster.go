package netsession

// Room roster (PROTOCOL.md sections 3 and 4.8). The server
// mints a roster notice on every membership change: the full
// member list, seats and watchers alike. Every client in the
// room keeps the latest copy so the UI can show who holds the
// seats and who is watching, which is also what the host
// referee's seat controls (section 4.9) are built on. The
// roster is display state only: it never touches lockstep.

// noteRosterLocked stores the server's latest member list.
// Called by the reader with s.mu held (both the player
// dispatch in step.go and the watch dispatch in watch.go).
func (s *Session) noteRosterLocked(members []RosterMember) {
	cp := make([]RosterMember, len(members))
	copy(cp, members)
	s.roster = cp
}

// Roster returns the room's member list, host first, in the
// order the server sent it. The result is a copy; callers may
// keep it.
//
// Until the first roster notice arrives (an older server, or
// the moment before it lands), the list is synthesized from
// what the session already knows: a player session knows
// itself and its peer from the hello exchange, and a watch
// session knows the two seat holders and itself. A plain
// two-player room therefore reports the right list either
// way; only the watchers are missing from a synthesized one.
func (s *Session) Roster() []RosterMember {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roster) > 0 {
		out := make([]RosterMember, len(s.roster))
		copy(out, s.roster)
		return out
	}
	out := []RosterMember{}
	if s.watch {
		if s.watchHost != "" {
			out = append(out, RosterMember{Name: s.watchHost, Role: "host"})
		}
		if s.watchGuest != "" {
			out = append(out, RosterMember{Name: s.watchGuest, Role: "guest"})
		}
		if s.cfg.PlayerName != "" {
			out = append(out, RosterMember{Name: s.cfg.PlayerName, Role: "spectator"})
		}
		return out
	}
	hostName, guestName := s.peerName, s.cfg.PlayerName
	if s.cfg.IsHost {
		hostName, guestName = s.cfg.PlayerName, s.peerName
	}
	if hostName != "" {
		out = append(out, RosterMember{Name: hostName, Role: "host"})
	}
	if guestName != "" {
		out = append(out, RosterMember{Name: guestName, Role: "guest"})
	}
	return out
}
