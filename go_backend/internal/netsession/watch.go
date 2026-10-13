package netsession

import (
	"errors"
	"fmt"
	"time"
)

// Spectating (PROTOCOL.md section 4.8, SPECTATOR_DESIGN.md
// slice S2). A spectator joins the room with a watch session:
// it runs the same core and game locally and reconstructs the
// match from both players' input streams, the way GGPO
// spectating works. No video crosses the wire, just the same
// tiny input envelopes the players already exchange.
//
// A watch session is a Session with watch set. It introduces
// itself with a hello marked role "spectator", chats like
// everyone else, and never sends input, ready, countdown, or
// session envelopes: nothing it transmits can be mistaken for
// a player's game traffic, and the reader filtering in step.go
// keeps the reverse true on the players' side.

const (
	// watchBehindFrames is how far behind the live edge (in
	// frames holding both inputs) a watcher may fall before
	// it asks for a fresh snapshot instead of grinding
	// through the backlog. 600 frames is ten seconds.
	watchBehindFrames = 600
	// watchSyncThrottle is the minimum spacing between a
	// watcher's automatic snapshot requests while behind.
	watchSyncThrottle = 5 * time.Second
	// watchSyncQueueCap bounds the per-session queue of
	// pending snapshot requests from spectators.
	watchSyncQueueCap = 16
)

// NewWatchSession builds a spectator session on an already
// connected transport. cfg.PlayerName is the spectator's
// name; cfg.Game names the game being watched (informational
// only: there is no handshake to check it against).
func NewWatchSession(t Transport, core Core, cfg Config) *Session {
	s := NewSession(t, core, cfg)
	s.watch = true
	s.watchIn1 = make(map[int]uint16)
	s.watchIn2 = make(map[int]uint16)
	s.watchDelay = defaultInputDelay
	s.watchSnapFrame = -1
	s.lastWatchStep = time.Now()
	return s
}

// StartWatch connects the spectator to the room: it starts
// the reader, introduces itself, and asks for a snapshot so
// it can join a match already in progress. It does not block
// on the match: the room may still be forming, and WatchStep
// simply reports no frame until play is underway.
func (s *Session) StartWatch() error {
	if !s.watch {
		return errors.New("not a watch session")
	}
	go s.reader()
	_ = s.sendEnv(envelope{
		Type: "hello", Player: s.cfg.PlayerName, Role: "spectator",
	})
	return s.sendWatchSync()
}

// sendWatchSync asks the room's players for a current
// snapshot addressed to us. The host's bridge answers with
// SendSnapshotTo; until one arrives (or the match starts and
// inputs flow from frame 0) WatchStep has nothing to deliver.
func (s *Session) sendWatchSync() error {
	s.mu.Lock()
	s.lastWatchSync = time.Now()
	s.mu.Unlock()
	return s.sendEnv(envelope{Type: "watch_sync", Player: s.cfg.PlayerName})
}

// noteWatchSyncLocked queues a spectator's snapshot request
// on a player session. Called by the reader with s.mu held.
// Repeat asks from the same spectator collapse into one.
func (s *Session) noteWatchSyncLocked(name string) {
	for _, n := range s.watchSyncReqs {
		if n == name {
			return
		}
	}
	s.watchSyncReqs = append(s.watchSyncReqs, name)
	if len(s.watchSyncReqs) > watchSyncQueueCap {
		s.watchSyncReqs = s.watchSyncReqs[len(s.watchSyncReqs)-watchSyncQueueCap:]
	}
}

// PollWatchSync returns the oldest pending snapshot
// requester's name, or "" when nobody is waiting. The bridge
// polls it and answers with NetplayAnswerWatchSync.
func (s *Session) PollWatchSync() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.watchSyncReqs) == 0 {
		return ""
	}
	name := s.watchSyncReqs[0]
	s.watchSyncReqs = s.watchSyncReqs[1:]
	return name
}

// SendSnapshotTo sends this session's current state as a
// snapshot addressed to the named room member (a spectator
// who asked with watch_sync). It reuses the exact delivery
// the desync resync uses; only the address differs.
//
// The snapshot alone is not quite enough: the inputs for the
// frames right after it were sampled and sent before the
// spectator joined, so it never saw them, and without them it
// would stall on the very first frame (a player's resync
// heals this by agreeing a neutral window with the peer; the
// players make no such agreement for a watcher, they just
// keep playing the real inputs). So the answer also repeats
// the input envelopes this session still holds for
// [frame, frame+delay), ours and the peer's, each stamped
// with its true sender. Everyone else in the room already saw
// these when they were first sent; for the other player they
// are duplicates with identical contents.
func (s *Session) SendSnapshotTo(name string) error {
	if name == "" {
		return errors.New("no spectator name given")
	}
	state := s.core.SaveState()
	if len(state) == 0 {
		return errors.New("core returned an empty state for the watch snapshot")
	}
	s.mu.Lock()
	frame := s.frame
	delay := s.delay
	myName, peerName := s.cfg.PlayerName, s.peerName
	mySeat, peerSeat := s.seatNumber(), 0
	if mySeat == 1 {
		peerSeat = 2
	} else if mySeat == 2 {
		peerSeat = 1
	}
	type heldInput struct {
		frame   int
		buttons uint16
		from    string
		seat    int
	}
	held := make([]heldInput, 0, 2*delay)
	for f := frame; f < frame+delay; f++ {
		if b, ok := s.localIn[f]; ok {
			held = append(held, heldInput{f, b, myName, mySeat})
		}
		if b, ok := s.remoteIn[f]; ok && peerName != "" {
			held = append(held, heldInput{f, b, peerName, peerSeat})
		}
	}
	s.mu.Unlock()

	if err := s.deliverSnapshot(frame, state, name); err != nil {
		return err
	}
	for _, h := range held {
		// Sent through s.send, not sendEnv: these envelopes
		// carry their original sender's stamp, not ours.
		_ = s.send(envelope{
			Type: "input", Frame: h.frame, Buttons: h.buttons,
			From: h.from, Seat: h.seat,
		})
	}
	return nil
}

// watchNoteLocked is the watch session's reader dispatch.
// Called with s.mu held. Everything the room says lands here:
// the two players' hellos and setup envelopes name the seats,
// their inputs accumulate per frame, snapshots addressed to
// us assemble in the shared snapshot state, and chat and
// voice signaling queue exactly as they do for a player.
func (s *Session) watchNoteLocked(e envelope) {
	switch e.Type {
	case "hello":
		// Player hellos carry their seat as the role marker.
		name := e.Player
		if name == "" {
			name = e.From
		}
		switch e.Role {
		case "host":
			s.watchHost = name
		case "guest":
			s.watchGuest = name
		}
	case "roster":
		// The server's member list is authoritative for who
		// holds which seat, and it reaches a mid-match joiner
		// who missed the hellos entirely. The full list is
		// also kept for the UI (roster.go).
		s.noteRosterLocked(e.Members)
		for _, m := range e.Members {
			switch m.Role {
			case "host":
				s.watchHost = m.Name
			case "guest":
				s.watchGuest = m.Name
			}
		}
	case "session_offer":
		if e.From != "" {
			s.watchHost = e.From
		}
	case "session_accept":
		if e.From != "" {
			s.watchGuest = e.From
		}
		if e.InputDelay > 0 {
			s.watchDelay = e.InputDelay
			// The neutral window after a snapshot (or at the
			// start) is exactly the delay long; if we learned
			// the real delay late, size it from the snapshot
			// frame, or from 0 before play starts.
			if s.watchSnapFrame >= 0 {
				s.watchNeutralUntil = s.watchSnapFrame + s.watchDelay
			} else if !s.watchLive {
				s.watchNeutralUntil = s.watchDelay
			}
		}
		s.watchLive = true
	case "input":
		seat := e.Seat
		if seat != 1 && seat != 2 {
			// An untagged sender (older build): attribute by
			// the name stamp when we know the seats.
			switch {
			case s.watchHost != "" && e.From == s.watchHost:
				seat = 1
			case s.watchGuest != "" && e.From == s.watchGuest:
				seat = 2
			default:
				return
			}
		}
		switch seat {
		case 1:
			s.watchIn1[e.Frame] = e.Buttons
		case 2:
			s.watchIn2[e.Frame] = e.Buttons
		}
		if _, ok1 := s.watchIn1[e.Frame]; ok1 {
			if _, ok2 := s.watchIn2[e.Frame]; ok2 && e.Frame > s.watchMaxPair {
				s.watchMaxPair = e.Frame
			}
		}
	case "snapshot_begin", "snapshot_chunk", "snapshot_end":
		s.noteSnapshotLocked(e)
		if s.snapReady {
			s.watchSnapPending = true
		}
	case "chat":
		from := e.From
		if from == "" {
			from = s.peerName
		}
		s.notePeerChat(from, e.Text)
	case "offer", "answer", "ice":
		s.notePeerSignal(e)
	case "match_end":
		// The match is over; the room is not (section 4.9).
		// The watcher falls back to the room with everyone
		// else: reconstruction state is cleared, the seat
		// holders stay known, and a snapshot is owed so the
		// next match has the same landing the first one did.
		// Only the two players can end a match: an envelope
		// stamped by another spectator is not believed, and
		// an unstamped one is a player from before sender
		// stamps, so it is.
		fromPlayer := e.From == "" ||
			(s.watchHost != "" && e.From == s.watchHost) ||
			(s.watchGuest != "" && e.From == s.watchGuest) ||
			(s.watchHost == "" && s.watchGuest == "")
		if s.ended || !fromPlayer {
			break
		}
		s.matchEndReason = e.Reason
		s.matchEndSet = true
		s.watchLive = false
		s.watchIn1 = make(map[int]uint16)
		s.watchIn2 = make(map[int]uint16)
		s.watchFrame = 0
		s.watchSnapFrame = -1
		s.watchNeutralUntil = 0
		s.watchMaxPair = 0
		s.watchSnapPending = false
		s.snapBuf = nil
		s.snapWant, s.snapChunks, s.snapGot, s.snapFrame = 0, 0, 0, 0
		s.snapReady = false
		s.snapFrom = ""
		s.watchNeedSync = true
		s.lastWatchSync = time.Time{}
		s.lastWatchStep = time.Now()
	case "seat_assign":
		// Seat calls are the host referee's (section 4.9).
		// The watcher records them on the known host's stamp
		// alone; anyone else's call is not the referee's.
		if s.watchHost != "" && e.From == s.watchHost {
			s.noteSeatAssignLocked(e.Player, e.Seat)
		}
	case "got_next_queue":
		// The host referee's line order (section 4.10).
		// The watcher mirrors it on the known host's stamp
		// alone; the helper enforces that. A watch session
		// never folds got_next itself: only the host keeps
		// the authoritative list.
		s.noteGotNextQueueLocked(e)
	case "tournament":
		// The room's tournament name (section 8). The
		// watcher believes it on the known host's stamp
		// alone, like the line; the helper enforces that.
		s.noteTournamentLocked(e)
	case "spectator_left":
		// A fellow watcher who left the room leaves the
		// line too (gotnext.go), so the mirrored list the
		// UI shows never holds a ghost between broadcasts.
		s.noteMemberLeftLocked(e.Player)
	case "peer_left":
		// A player who left the room leaves the line too;
		// the notice names only the role, and the seat
		// holders are known.
		switch e.Role {
		case "host":
			s.noteMemberLeftLocked(s.watchHost)
			s.watchGoneHost = true
		case "guest":
			s.noteMemberLeftLocked(s.watchGuest)
			s.watchGoneGuest = true
		}
		if s.watchGoneHost && s.watchGoneGuest && !s.ended {
			s.ended = true
			s.endErr = errors.New("both players left")
		}
	case "lobby_closed":
		s.peerGone = true
		s.ended = true
		s.endErr = errors.New("the host closed the lobby")
	case "session_end":
		if !s.ended {
			s.ended = true
			if e.Reason != "" {
				s.endErr = fmt.Errorf("the match ended: %s", e.Reason)
			} else {
				s.endErr = errors.New("the match ended")
			}
		}
	}
}

// applyWatchSnapshot loads a pending watch snapshot into
// the core on the WatchStep thread (the same thread
// discipline as the guest applying a resync snapshot on its
// Step thread, so the core is only ever driven from one
// goroutine). The frame counter jumps to the snapshot frame
// and collected inputs before it are dropped.
//
// There is deliberately no neutral window after a mid-match
// snapshot: the players made no agreement to replay those
// frames as neutral (that agreement is what a two-player
// resync does), they simply keep applying the real inputs,
// and the watcher must do the same or diverge for good. The
// inputs for the first frames are the ones SendSnapshotTo
// repeats with its answer. Only a frame 0 snapshot (a match
// starting state) gets the players' own neutral window:
// frames [0, delay) are never transmitted by anyone, the
// prefill IS the agreement there.
func (s *Session) applyWatchSnapshot() {
	state, frame, from := s.takeSnapshot()
	s.mu.Lock()
	s.watchSnapPending = false
	s.mu.Unlock()
	if len(state) == 0 {
		return
	}
	if err := s.core.LoadState(state); err != nil {
		return
	}
	s.mu.Lock()
	s.watchFrame = frame
	s.watchSnapFrame = frame
	s.watchNeutralUntil = frame
	if frame == 0 {
		s.watchNeutralUntil = s.watchDelay
	}
	s.watchLive = true
	s.lastWatchStep = time.Now()
	for f := range s.watchIn1 {
		if f < frame {
			delete(s.watchIn1, f)
		}
	}
	for f := range s.watchIn2 {
		if f < frame {
			delete(s.watchIn2, f)
		}
	}
	s.watchMaxPair = frame
	for f := range s.watchIn1 {
		if _, ok := s.watchIn2[f]; ok && f > s.watchMaxPair {
			s.watchMaxPair = f
		}
	}
	s.mu.Unlock()
	if from != "" {
		_ = s.sendEnv(envelope{Type: "snapshot_applied", Frame: frame, To: from})
	}
}

// WatchStep simulates one frame of the watched match on the
// local core, once both seats' inputs for it are known. It
// returns the two players' button masks for the frame it
// just simulated and that frame's number. ok is false when
// the frame cannot run yet (the match has not started, an
// input is still in flight, or the watch is over); the
// caller simply calls again later, like a render loop poll.
func (s *Session) WatchStep() (p1, p2 uint16, frame int, ok bool) {
	s.mu.Lock()
	pending := s.watch && s.watchSnapPending
	s.mu.Unlock()
	if pending {
		s.applyWatchSnapshot()
	}

	s.mu.Lock()
	frame = s.watchFrame
	live := s.watchLive && !s.ended
	ended := s.ended
	needSync := s.watchNeedSync
	var m1, m2 uint16
	have := false
	if live {
		if frame < s.watchNeutralUntil {
			have = true // agreed neutral, like the players' prefill
		} else if a, ok1 := s.watchIn1[frame]; ok1 {
			if b, ok2 := s.watchIn2[frame]; ok2 {
				m1, m2, have = a, b, true
			}
		}
	}
	if have {
		delete(s.watchIn1, frame)
		delete(s.watchIn2, frame)
		s.watchFrame++
		s.lastWatchStep = time.Now()
	}
	behind := s.watchMaxPair - frame
	lastSync := s.lastWatchSync
	// Stuck exactly on the snapshot frame long after landing
	// means the inputs for it never reached us (an answer
	// built without them, e.g. an older build): ask again. A
	// watcher that has advanced past its snapshot never
	// matches this, so a paused game is not mistaken for it.
	stuckOnSnap := s.watchSnapFrame >= 0 && frame == s.watchSnapFrame &&
		time.Since(s.lastWatchStep) > watchSyncThrottle
	s.mu.Unlock()

	if !have {
		// A snapshot is owed (the last match ended and the
		// next needs its landing), or we have fallen far
		// behind the live edge and a fresh snapshot is
		// cheaper than grinding through the backlog.
		if !ended && (needSync || (live && (behind > watchBehindFrames || stuckOnSnap))) &&
			time.Since(lastSync) > watchSyncThrottle {
			if needSync {
				s.mu.Lock()
				s.watchNeedSync = false
				s.mu.Unlock()
			}
			_ = s.sendWatchSync()
		}
		return 0, 0, frame, false
	}
	s.core.SetButtons(0, m1)
	s.core.SetButtons(1, m2)
	s.core.RunFrame()
	return m1, m2, frame, true
}

// WatchInfo reports the watch for the UI: a phase string in
// the style of RoomPhase ("waiting" until a player is known,
// "room" while the room forms, "playing" once the match is
// being reconstructed, "ended"), the two seat holders' names
// ("" while unknown), and the error that ended the watch.
func (s *Session) WatchInfo() (phase, host, guest, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, guest = s.watchHost, s.watchGuest
	switch {
	case s.ended:
		phase = "ended"
		if s.endErr != nil {
			errText = s.endErr.Error()
		}
	case s.watchLive:
		phase = "playing"
	case s.watchHost != "" || s.watchGuest != "":
		phase = "room"
	default:
		phase = "waiting"
	}
	return phase, host, guest, errText
}
