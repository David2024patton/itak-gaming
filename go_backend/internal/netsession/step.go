package netsession

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// reader pumps peer messages into session state until the socket
// dies or the session ends.
func (s *Session) reader() {
	for {
		raw, err := s.t.ReadMessage()
		if err != nil {
			s.mu.Lock()
			if !s.ended {
				s.ended = true
				if errors.Is(err, io.EOF) {
					s.endErr = errors.New("peer disconnected")
				} else {
					s.endErr = err
				}
			}
			s.mu.Unlock()
			s.signal()
			return
		}
		var e envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		s.mu.Lock()
		s.lastPeerMsg = time.Now()
		if s.watch {
			// A spectator session reads the whole room; its
			// dispatch lives in watch.go.
			s.watchNoteLocked(e)
			s.mu.Unlock()
			s.signal()
			continue
		}
		// Sender filtering (PROTOCOL.md section 4.8): with
		// spectators in the room, this socket also carries
		// frames from people who are not the peer. Game
		// critical envelopes count only when they come from
		// the peer. Senders stamp "from" (see sendEnv); an
		// empty from is an older client and is always
		// accepted, and before the peer's name is known
		// nothing is filtered, so a pre-spectator session is
		// handled exactly as it always was.
		fromPeer := s.peerName == "" || e.From == "" || e.From == s.peerName
		switch e.Type {
		case "peer_joined":
			s.peerJoined = true
			if !s.peerPresent {
				s.peerPresent = true
				s.peerPresentAt = time.Now()
			}
		case "hello":
			// A spectator's hello (role "spectator") never
			// names our peer, and once the peer is known a
			// hello from anyone else cannot rename them.
			if e.Role == "spectator" {
				break
			}
			if s.peerName == "" || e.From == "" || e.From == s.peerName {
				s.peerName = e.Player
				if !s.peerPresent {
					s.peerPresent = true
					s.peerPresentAt = time.Now()
				}
			}
		case "ready":
			if fromPeer {
				s.peerReady = true
				if !s.peerPresent {
					s.peerPresent = true
					s.peerPresentAt = time.Now()
				}
			}
		case "countdown":
			if fromPeer && e.EndsAt > 0 {
				s.countdownEnds = time.UnixMilli(e.EndsAt)
			}
		case "pause":
			if fromPeer {
				s.peerPaused = e.Paused
			}
		case "chat":
			// Recorded for the UI only. Chat must never touch
			// lockstep state (see chat.go). Anyone in the room
			// may speak; the sender stamp says who did.
			from := e.From
			if from == "" {
				from = s.peerName
			}
			s.notePeerChat(from, e.Text)
		case "offer", "answer", "ice":
			// Voice signaling, queued for the UI layer only.
			// Like chat, it must never touch lockstep state
			// (see signal.go).
			s.notePeerSignal(e)
		case "watch_sync":
			// A spectator asking for a snapshot (watch.go).
			// Not game traffic: anyone may ask, the bridge
			// decides whether to answer.
			name := e.From
			if name == "" {
				name = e.Player
			}
			if name != "" {
				s.noteWatchSyncLocked(name)
			}
		case "peer_left":
			if s.phase == phaseRoom {
				// Still in the waiting room: the seat simply
				// empties and both readys reset, so whoever
				// joins next starts the ritual fresh.
				s.peerPresent = false
				s.peerName = ""
				s.peerReady = false
				s.localReady = false
				s.countdownEnds = time.Time{}
			} else {
				s.peerGone = true
				s.ended = true
				s.endErr = errors.New("the other player left")
			}
		case "lobby_closed":
			s.peerGone = true
			s.ended = true
			s.endErr = errors.New("the host closed the lobby")
		case "session_offer":
			if fromPeer {
				cp := e
				s.offer = &cp
			}
		case "session_accept":
			if fromPeer {
				cp := e
				s.accept = &cp
			}
		case "session_reject":
			if fromPeer {
				s.reject = e.Reason
			}
		case "input":
			if fromPeer {
				s.remoteIn[e.Frame] = e.Buttons
			}
		case "state_hash":
			if fromPeer {
				s.remoteHash[e.Frame] = e.Hash
			}
		case "resync_request":
			// Ignore requests for desyncs already healed: their
			// snapshot frame is at or below the last completed one.
			if fromPeer && s.cfg.IsHost && e.Frame >= s.lastResyncFrame {
				s.resyncPending = true
				s.resyncReqFrame = e.Frame
			}
		case "snapshot_begin", "snapshot_chunk", "snapshot_end":
			s.noteSnapshotLocked(e)
		case "snapshot_applied":
			if fromPeer && e.Frame > s.snapApplied {
				s.snapApplied = e.Frame
			}
		case "session_end":
			if fromPeer {
				s.ended = true
				s.endErr = fmt.Errorf("the other player ended the session: %s", e.Reason)
			}
		}
		s.mu.Unlock()
		s.signal()
	}
}

func (s *Session) waitFor(pred func() bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		ok := pred()
		ended := s.ended
		endErr := s.endErr
		s.mu.Unlock()
		if ok {
			return nil
		}
		if ended {
			if endErr != nil {
				return endErr
			}
			return errors.New("session ended")
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the other player")
		}
		select {
		case <-s.notifyCh:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *Session) checkEnded() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		if s.endErr != nil {
			return s.endErr
		}
		return errors.New("session ended")
	}
	return nil
}

// takeSnapshot drains the assembled snapshot, returning its
// bytes, its frame, and who sent it (so the reply can be
// addressed back, section 4.4).
func (s *Session) takeSnapshot() ([]byte, int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.snapBuf
	frame := s.snapFrame
	from := s.snapFrom
	s.snapBuf = nil
	s.snapReady = false
	s.snapFrom = ""
	return buf, frame, from
}

// noteSnapshotLocked folds one snapshot envelope into the
// assembly state. Called by the reader with s.mu held. A
// transfer addressed to another room member (a nonempty "to"
// that is not us) is ignored envelope by envelope: several
// transfers can share one room (a desync resync to the guest,
// a watch answer to a spectator), and each receiver assembles
// only its own. An empty "to" is the pre-spectator behavior
// and is always assembled.
func (s *Session) noteSnapshotLocked(e envelope) {
	if e.To != "" && e.To != s.cfg.PlayerName {
		return
	}
	switch e.Type {
	case "snapshot_begin":
		s.snapBuf = nil
		s.snapWant = e.Bytes
		s.snapChunks = e.Chunks
		s.snapGot = 0
		s.snapFrame = e.Frame
		s.snapReady = false
		s.snapFrom = e.From
	case "snapshot_chunk":
		if e.Seq == s.snapGot {
			data, err := base64.StdEncoding.DecodeString(e.Data)
			if err == nil {
				s.snapBuf = append(s.snapBuf, data...)
				s.snapGot++
			}
		}
	case "snapshot_end":
		if s.snapGot == s.snapChunks && len(s.snapBuf) == s.snapWant {
			sum := sha256.Sum256(s.snapBuf)
			if hex.EncodeToString(sum[:]) == e.SHA256 {
				s.snapReady = true
			}
		}
	}
}

// sendSnapshot chunks a save state per PROTOCOL.md section
// 4.4, addressed to the peer. Guest side of the resync and
// starting-snapshot paths; SendSnapshotTo (watch.go) is the
// same delivery aimed at a spectator.
func (s *Session) sendSnapshot(frame int, state []byte) error {
	s.mu.Lock()
	to := s.peerName
	s.mu.Unlock()
	return s.deliverSnapshot(frame, state, to)
}

// deliverSnapshot sends one snapshot transfer addressed to
// the named room member (empty name: unaddressed, the
// pre-spectator behavior). The transfer is serialized on
// snapMu so two transfers never interleave their chunks.
func (s *Session) deliverSnapshot(frame int, state []byte, to string) error {
	s.snapMu.Lock()
	defer s.snapMu.Unlock()
	chunks := (len(state) + snapshotChunkBytes - 1) / snapshotChunkBytes
	if err := s.sendEnv(envelope{
		Type: "snapshot_begin", Frame: frame, Bytes: len(state),
		Chunks: chunks, To: to,
	}); err != nil {
		return err
	}
	for i := 0; i < chunks; i++ {
		lo := i * snapshotChunkBytes
		hi := lo + snapshotChunkBytes
		if hi > len(state) {
			hi = len(state)
		}
		if err := s.sendEnv(envelope{
			Type: "snapshot_chunk", Seq: i, To: to,
			Data: base64.StdEncoding.EncodeToString(state[lo:hi]),
		}); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(state)
	return s.sendEnv(envelope{
		Type: "snapshot_end", SHA256: hex.EncodeToString(sum[:]), To: to,
	})
}

func (s *Session) mySlot() int {
	if s.cfg.IsHost {
		return 0
	}
	return 1
}

func (s *Session) peerSlot() int { return 1 - s.mySlot() }

// Step simulates exactly one frame using the sampled local buttons,
// blocking while the peer's input for the frame is in flight. It is
// called from the emulator thread; a false step with an error means
// the session is over.
func (s *Session) Step(local uint16) error {
	if err := s.checkEnded(); err != nil {
		return err
	}
	// Host duty: answer a resync request before simulating further.
	if s.cfg.IsHost {
		s.mu.Lock()
		pending := s.resyncPending
		s.mu.Unlock()
		if pending {
			if err := s.hostResync(); err != nil {
				return err
			}
		}
	}
	f := s.frame
	applyFrame := f + s.delay
	s.mu.Lock()
	s.localIn[applyFrame] = local
	s.mu.Unlock()
	if err := s.sendEnv(envelope{Type: "input", Frame: applyFrame, Buttons: local}); err != nil {
		return err
	}
	// Wait for the peer's input labeled f.
	waitStart := time.Now()
	waitLogged := false
	for {
		s.mu.Lock()
		_, have := s.remoteIn[f]
		ended := s.ended
		endErr := s.endErr
		// Silence is measured from the last peer message, or
		// from our own resume if that is later (see lastResume).
		silentSince := s.lastPeerMsg
		if s.lastResume.After(silentSince) {
			silentSince = s.lastResume
		}
		silent := time.Since(silentSince)
		paused := s.localPaused || s.peerPaused
		s.mu.Unlock()
		if have {
			break
		}
		if ended {
			if endErr != nil {
				return endErr
			}
			return errors.New("session ended")
		}
		// A paused peer is stalled, not lost: wait as long as the
		// pause lasts. (A peer that truly vanishes still ends the
		// session through the transport error above.)
		if !paused && silent > s.silenceLimit() {
			dbg("SILENCE TRIP frame=%d silent=%v localPaused=%v peerPaused=%v host=%v",
				f, silent, s.localPaused, s.peerPaused, s.cfg.IsHost)
			return errors.New("connection lost: no word from the other player")
		}
		if !waitLogged && time.Since(waitStart) > 500*time.Millisecond {
			waitLogged = true
			dbg("step waiting frame=%d host=%v silent=%v", f, s.cfg.IsHost, silent)
		}
		select {
		case <-s.notifyCh:
		case <-time.After(20 * time.Millisecond):
			s.mu.Lock()
			s.stalls++
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	myButtons := s.localIn[f]
	peerButtons := s.remoteIn[f]
	delete(s.localIn, f)
	delete(s.remoteIn, f)
	s.mu.Unlock()
	s.core.SetButtons(s.mySlot(), myButtons)
	s.core.SetButtons(s.peerSlot(), peerButtons)
	s.core.RunFrame()
	s.frame++
	simulated := s.frame - 1
	// Hash check on interval frames (PROTOCOL.md 4.3).
	if simulated > 0 && s.hashInterval > 0 && simulated%s.hashInterval == 0 {
		if err := s.hashCheck(simulated); err != nil {
			return err
		}
	}
	return nil
}

func dbg(format string, args ...any) {
	if os.Getenv("NETPLAY_DEBUG") != "" {
		fmt.Printf("[netsession] "+format+"\n", args...)
	}
}

func (s *Session) hashCheck(frame int) error {
	dbg("hashCheck frame=%d host=%v", frame, s.cfg.IsHost)
	state := s.core.SaveState()
	sum := sha256.Sum256(state)
	mine := hex.EncodeToString(sum[:])
	s.mu.Lock()
	s.localHash[frame] = mine
	theirs, haveTheirs := s.remoteHash[frame]
	s.mu.Unlock()
	if err := s.sendEnv(envelope{Type: "state_hash", Frame: frame, Hash: mine}); err != nil {
		return err
	}
	if !haveTheirs {
		// The peer's hash for this frame may still be in flight;
		// give it a moment, then compare if it arrived. If it never
		// comes, the next interval or the silence limit catches it.
		_ = s.waitFor(func() bool {
			_, ok := s.remoteHash[frame]
			return ok || s.ended
		}, 2*time.Second)
		s.mu.Lock()
		theirs, haveTheirs = s.remoteHash[frame]
		s.mu.Unlock()
	}
	s.mu.Lock()
	already := s.checked[frame]
	s.mu.Unlock()
	dbg("hashCheck frame=%d haveTheirs=%v equal=%v", frame, haveTheirs, theirs == mine)
	if haveTheirs && theirs != mine && !already {
		s.mu.Lock()
		s.checked[frame] = true
		s.desyncs++
		s.mu.Unlock()
		if s.cfg.IsHost {
			// Host is the authority: push our state to the guest.
			return s.hostResync()
		}
		if err := s.sendEnv(envelope{Type: "resync_request", Frame: frame}); err != nil {
			return err
		}
		return s.guestResync()
	}
	return nil
}

// hostResync sends the current state; the frame counter stays put
// (the snapshot's frame is the next frame to simulate).
func (s *Session) hostResync() error {
	dbg("hostResync begin frame=%d", s.frame)
	s.mu.Lock()
	s.resyncPending = false
	frame := s.frame
	s.mu.Unlock()
	state := s.core.SaveState()
	if len(state) == 0 {
		return errors.New("core returned an empty state for resync")
	}
	if err := s.sendSnapshot(frame, state); err != nil {
		return err
	}
	err := s.waitFor(func() bool {
		return s.snapApplied >= frame || s.ended
	}, 30*time.Second)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.lastResyncFrame = frame
	s.mu.Unlock()
	// Clear only the neutral window [frame, frame+delay): those
	// frames are re-agreed as neutral by prefillNeutral. Inputs at
	// or beyond frame+delay were sampled after the snapshot and
	// must be kept, or the peer waits forever for a resend that
	// never comes.
	s.clearInputWindow(frame)
	s.prefillNeutral(frame)
	dbg("hostResync done frame=%d", frame)
	return nil
}

// guestResync waits for the host's snapshot and applies it.
func (s *Session) guestResync() error {
	dbg("guestResync begin")
	if err := s.waitFor(func() bool { return s.snapReady || s.ended }, 30*time.Second); err != nil {
		return err
	}
	if err := s.checkEnded(); err != nil {
		return err
	}
	state, frame, from := s.takeSnapshot()
	dbg("guestResync got snapshot frame=%d bytes=%d", frame, len(state))
	if err := s.core.LoadState(state); err != nil {
		return fmt.Errorf("loading resync snapshot: %w", err)
	}
	s.mu.Lock()
	s.frame = frame
	s.mu.Unlock()
	s.clearInputWindow(frame)
	s.prefillNeutral(frame)
	return s.sendEnv(envelope{Type: "snapshot_applied", Frame: frame, To: from})
}

// clearInputWindow drops queued inputs inside the neutral window
// [from, from+delay) from both input maps.
func (s *Session) clearInputWindow(from int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for f := from; f < from+s.delay; f++ {
		delete(s.localIn, f)
		delete(s.remoteIn, f)
	}
}

// Frame reports the next frame to simulate.
func (s *Session) Frame() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame
}

// Desyncs reports how many hash mismatches were detected.
func (s *Session) Desyncs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.desyncs
}

// End politely closes the session.
func (s *Session) End(reason string) {
	_ = s.sendEnv(envelope{Type: "session_end", Reason: reason})
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	s.signal()
	_ = s.t.Close()
}

// Close tears the session down without a polite envelope.
// Watch sessions use it: a spectator must never send session
// traffic, not even a session_end.
func (s *Session) Close() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	s.signal()
	_ = s.t.Close()
}
