package netsession

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Text chat between the two players (PROTOCOL.md section 4.7).
// Chat rides the same signaling socket as everything else, in
// the waiting room and during play, and is deliberately kept out
// of the lockstep machinery: messages never touch frames,
// inputs, hashes, or the session phase, so a chatty player can
// neither desync the game nor stall it.

const (
	// chatLogCap is how many messages the session log keeps.
	// Older ones drop off the front.
	chatLogCap = 200
	// chatMaxRunes caps one message. The 256 KB socket limit in
	// PROTOCOL.md section 3 is the hard ceiling; this keeps a
	// single message to something a person would actually type.
	chatMaxRunes = 500
)

// ChatMessage is one entry in the session chat log.
type ChatMessage struct {
	Seq  int    // 1-based, grows by one per message, never reused
	From string // sender's player name (the peer's, or our own)
	Text string // trimmed message body
	Mine bool   // true when this side sent it
}

// appendChatLocked adds an entry to the log, dropping the oldest
// entries past the cap. The caller holds s.mu.
func (s *Session) appendChatLocked(from, text string, mine bool) {
	s.chatSeq++
	s.chatLog = append(s.chatLog, ChatMessage{
		Seq: s.chatSeq, From: from, Text: text, Mine: mine,
	})
	if len(s.chatLog) > chatLogCap {
		kept := make([]ChatMessage, chatLogCap)
		copy(kept, s.chatLog[len(s.chatLog)-chatLogCap:])
		s.chatLog = kept
	}
}

// notePeerChat records a chat message from the peer. Called by
// the reader with s.mu held. Empty messages are dropped; an
// over-long one from a misbehaving peer is cut to the cap
// rather than trusted.
func (s *Session) notePeerChat(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if utf8.RuneCountInString(text) > chatMaxRunes {
		text = string([]rune(text)[:chatMaxRunes])
	}
	s.appendChatLocked(s.peerName, text, false)
}

// SendChat sends a chat message to the other player and records
// it in the local log. Empty (after trimming) and over-long
// messages are rejected and nothing is sent.
func (s *Session) SendChat(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("chat message is empty")
	}
	if utf8.RuneCountInString(text) > chatMaxRunes {
		return errors.New("chat message is too long (500 characters max)")
	}
	if err := s.sendEnv(envelope{Type: "chat", Text: text}); err != nil {
		return err
	}
	s.mu.Lock()
	s.appendChatLocked(s.cfg.PlayerName, text, true)
	s.mu.Unlock()
	s.signal()
	return nil
}

// ChatSince returns the log entries newer than afterSeq (pass 0
// for everything kept) plus the seq of the newest entry overall,
// so a UI can poll with the returned seq and only ever fetch
// what it has not rendered yet.
func (s *Session) ChatSince(afterSeq int) (msgs []ChatMessage, latest int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.chatLog {
		if m.Seq > afterSeq {
			msgs = append(msgs, m)
		}
	}
	return msgs, s.chatSeq
}
