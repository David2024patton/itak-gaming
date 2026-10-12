package netsession

import (
	"errors"
	"time"
)

// The waiting room (PROTOCOL.md section 4.0): before the session
// setup handshake, the two players see each other in a room, both
// press Ready, and a shared countdown runs so both sides (and the
// relay) are settled before frame 0. The host owns the countdown.

const (
	phaseRoom = iota
	phasePlaying
)

const defaultCountdown = 5 * time.Second

// A peer that connects but never says hello is an older build;
// after this grace the room is skipped so mixed versions can
// still play.
const legacyPeerGrace = 20 * time.Second

func (s *Session) roomPhase() error {
	if s.cfg.ReadyNow {
		// Headless peers (tests, the netplayguest rig): introduce
		// and ready up immediately, then let the normal setup
		// waits take over.
		s.mu.Lock()
		s.localReady = true
		s.mu.Unlock()
		_ = s.sendEnv(envelope{Type: "hello", Player: s.cfg.PlayerName})
		_ = s.sendEnv(envelope{Type: "ready"})
		return nil
	}
	countdown := s.cfg.Countdown
	if countdown <= 0 {
		countdown = defaultCountdown
	}
	_ = s.sendEnv(envelope{Type: "hello", Player: s.cfg.PlayerName})
	deadline := time.Now().Add(600 * time.Second)
	var helloAt, readyAt time.Time
	for {
		if err := s.checkEnded(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the other player")
		}
		s.mu.Lock()
		offer := s.offer
		present := s.peerPresent
		presentAt := s.peerPresentAt
		name := s.peerName
		bothReady := s.localReady && s.peerReady
		ends := s.countdownEnds
		s.mu.Unlock()

		// Legacy peers skip the room entirely.
		if offer != nil && !s.cfg.IsHost {
			return nil // an old host already sent its offer
		}
		if present && name == "" &&
			time.Since(presentAt) > legacyPeerGrace {
			return nil // an old peer never says hello
		}

		if bothReady {
			if s.cfg.IsHost && ends.IsZero() {
				ends = time.Now().Add(countdown)
				s.mu.Lock()
				s.countdownEnds = ends
				s.mu.Unlock()
				_ = s.sendEnv(envelope{
					Type: "countdown", EndsAt: ends.UnixMilli(),
				})
			}
			if !ends.IsZero() && !time.Now().Before(ends) {
				return nil
			}
		}

		// Re-introduce and re-state readiness (at most once a
		// second) until the peer has them: messages sent before
		// the peer's socket finished joining are lost.
		if present && name == "" && time.Since(helloAt) > time.Second {
			helloAt = time.Now()
			_ = s.sendEnv(envelope{Type: "hello", Player: s.cfg.PlayerName})
		}
		s.mu.Lock()
		lr := s.localReady
		s.mu.Unlock()
		if lr && time.Since(readyAt) > time.Second {
			readyAt = time.Now()
			_ = s.sendEnv(envelope{Type: "ready"})
		}

		select {
		case <-s.notifyCh:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// SetReady marks the local player ready in the room. One-way by
// design: ready means ready.
func (s *Session) SetReady() {
	s.mu.Lock()
	s.localReady = true
	s.mu.Unlock()
	_ = s.sendEnv(envelope{Type: "ready"})
	s.signal()
}

// RoomPhase reports where the session is for the UI:
// "waiting" (nobody else here yet), "room" (peer present, not
// both ready), "countdown", or "playing".
func (s *Session) RoomPhase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == phasePlaying {
		return "playing"
	}
	if !s.countdownEnds.IsZero() {
		return "countdown"
	}
	if s.peerName != "" {
		return "room"
	}
	return "waiting"
}

// PeerName is the other player's display name, once known.
func (s *Session) PeerName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerName
}

// PeerReady reports whether the other player has pressed Ready.
func (s *Session) PeerReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerReady
}

// LocalReady reports whether this player has pressed Ready.
func (s *Session) LocalReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localReady
}

// CountdownSeconds is the whole seconds left on the shared
// countdown (0 when none is running).
func (s *Session) CountdownSeconds() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.countdownEnds.IsZero() {
		return 0
	}
	d := time.Until(s.countdownEnds)
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
