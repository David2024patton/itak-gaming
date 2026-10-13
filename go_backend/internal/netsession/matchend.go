package netsession

import (
	"errors"
	"strings"
	"time"
)

// Match end (PROTOCOL.md section 4.9, SPECTATOR_DESIGN.md
// slice S4). A game ending used to end the session; rotation
// (seat swaps, Got Next, tournaments) needs the opposite: the
// match ends, the session does not. Either player sends
// match_end, both sides fall back to the waiting room on the
// same sockets with their readys reset and their frame state
// cleared, and the next match starts through StartNextMatch
// whenever the room is ready again. The lobby persists
// because nothing here ever closes the transport.
//
// The envelope is game traffic, so the reader honors it only
// from the peer (the same fromPeer gate as section 4.8): a
// spectator sharing the socket cannot end a match it is only
// watching.

// SendMatchEnd ends the current match and returns both
// players to the room. The sender's own transition happens
// here too (the host's client treats its own send as
// authoritative for rotation), so the sender's bridge sees
// the event through PollMatchEnd exactly like the receiver.
// Only a live match can end: calling this from the room is
// an error, as is calling it on a watch session, which must
// never send session traffic at all.
func (s *Session) SendMatchEnd(reason string) error {
	if s.watch {
		return errors.New("a spectator cannot end a match")
	}
	if err := s.checkEnded(); err != nil {
		return err
	}
	s.mu.Lock()
	playing := s.phase == phasePlaying
	s.mu.Unlock()
	if !playing {
		return errors.New("no match is running")
	}
	reason = strings.TrimSpace(reason)
	if err := s.sendEnv(envelope{Type: "match_end", Reason: reason}); err != nil {
		return err
	}
	s.mu.Lock()
	s.returnToRoomLocked(reason)
	s.mu.Unlock()
	s.signal()
	return nil
}

// PollMatchEnd reports the most recent match end, ours or the
// peer's, exactly once: the reason (possibly empty) and true,
// then ("", false) until another match ends. The bridge
// flattens this to a plain string for the UI.
func (s *Session) PollMatchEnd() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.matchEndSet {
		return "", false
	}
	reason := s.matchEndReason
	s.matchEndSet = false
	return reason, true
}

// noteMatchEndLocked folds a match_end from the peer into the
// session. Called by the reader with s.mu held.
func (s *Session) noteMatchEndLocked(reason string) {
	if s.ended {
		return
	}
	s.returnToRoomLocked(reason)
}

// returnToRoomLocked records the match-end event and, when a
// match was actually running, resets everything a match owns:
// the frame counter, both input maps, the hash books (frame
// numbers restart at 0, so a stale entry would corrupt the
// next match's checks), the setup handshake, both readys, the
// countdown, any half-assembled snapshot, resync bookkeeping,
// and pause state. Room membership is NOT reset: the peer is
// still here, their name is still known, chat and signaling
// history survive, and the socket was never touched. Called
// with s.mu held; idempotent, because both players may send
// match_end at the same moment and each side then processes
// the other's copy after its own transition.
func (s *Session) returnToRoomLocked(reason string) {
	s.matchEndReason = reason
	s.matchEndSet = true
	if s.phase != phasePlaying {
		return
	}
	s.phase = phaseRoom
	s.frame = 0
	s.localIn = make(map[int]uint16)
	s.remoteIn = make(map[int]uint16)
	s.localHash = make(map[int]string)
	s.remoteHash = make(map[int]string)
	s.checked = make(map[int]bool)
	s.offer = nil
	s.accept = nil
	s.reject = ""
	s.peerReady = false
	s.localReady = false
	s.countdownEnds = time.Time{}
	s.snapBuf = nil
	s.snapWant, s.snapChunks, s.snapGot, s.snapFrame = 0, 0, 0, 0
	s.snapReady = false
	s.snapFrom = ""
	s.snapApplied = -1
	s.resyncPending = false
	s.resyncReqFrame = 0
	s.lastResyncFrame = -1
	s.localPaused = false
	s.peerPaused = false
	s.lastPeerMsg = time.Now()
}
