package netsession

import (
	"fmt"
)

// Voice chat signaling (PROTOCOL.md section 3.1). The media
// itself is peer to peer WebRTC audio handled by the client;
// Go only relays the three signaling envelopes (offer, answer,
// ice) between the two players over the same signaling socket
// as everything else, in the waiting room and during play.
// Like chat, signaling is deliberately kept out of the
// lockstep machinery: it never touches frames, inputs,
// hashes, or the session phase.

const (
	// signalQueueCap is how many inbound signaling entries
	// the session keeps. A voice setup is one offer, one
	// answer and a burst of ICE candidates, so this is
	// generous headroom, not a working limit.
	signalQueueCap = 100
	// signalSDPMaxBytes caps an offer or answer payload.
	// Real SDP bodies are a few KB; the 256 KB socket limit
	// in PROTOCOL.md section 3 is the hard ceiling.
	signalSDPMaxBytes = 32 * 1024
	// signalICEMaxBytes caps one ICE candidate payload.
	signalICEMaxBytes = 4 * 1024
)

// SignalMessage is one inbound signaling entry from the peer.
type SignalMessage struct {
	Seq           int    // 1-based, grows by one per entry, never reused
	Kind          string // "offer", "answer", or "ice"
	SDP           string // offer/answer payload, empty for ice
	Candidate     string // ice payload, empty for offer/answer
	SDPMid        string // ice only
	SDPMLineIndex int    // ice only
}

// signalWire is the exact on the wire shape of an outbound
// signaling envelope. It is separate from envelope so an ICE
// envelope always carries sdpMLineIndex, even when it is 0:
// the shared envelope marks that field omitempty.
type signalWire struct {
	Type          string `json:"type"`
	SDP           string `json:"sdp,omitempty"`
	Candidate     string `json:"candidate,omitempty"`
	SDPMid        string `json:"sdpMid,omitempty"`
	SDPMLineIndex *int   `json:"sdpMLineIndex,omitempty"`
}

// SendSignal sends one WebRTC signaling envelope to the other
// player: kind "offer" or "answer" with an SDP payload, or
// kind "ice" with a candidate payload. Empty payloads, unknown
// kinds, and payloads over the caps are rejected and nothing
// is sent. Sent signals are not echoed into the local queue:
// the sender's client produced them and already has them.
func (s *Session) SendSignal(kind, payload string) error {
	switch kind {
	case "offer", "answer":
		if payload == "" {
			return fmt.Errorf("signal %s has an empty SDP", kind)
		}
		if len(payload) > signalSDPMaxBytes {
			return fmt.Errorf("signal %s SDP is too large (%d bytes max)",
				kind, signalSDPMaxBytes)
		}
		return s.send(signalWire{Type: kind, SDP: payload})
	case "ice":
		if payload == "" {
			return fmt.Errorf("signal ice has an empty candidate")
		}
		if len(payload) > signalICEMaxBytes {
			return fmt.Errorf("signal ice candidate is too large (%d bytes max)",
				signalICEMaxBytes)
		}
		zero := 0
		return s.send(signalWire{
			Type: "ice", Candidate: payload, SDPMid: "0",
			SDPMLineIndex: &zero,
		})
	default:
		return fmt.Errorf("unknown signal kind %q", kind)
	}
}

// notePeerSignal records a signaling envelope from the peer.
// Called by the reader with s.mu held. Envelopes with an empty
// payload or a payload over the caps are dropped whole: a
// truncated SDP is worse than none, because the receiver
// would try to apply half a description.
func (s *Session) notePeerSignal(e envelope) {
	switch e.Type {
	case "offer", "answer":
		if e.SDP == "" || len(e.SDP) > signalSDPMaxBytes {
			return
		}
	case "ice":
		if e.Candidate == "" || len(e.Candidate) > signalICEMaxBytes {
			return
		}
	default:
		return
	}
	s.signalSeq++
	s.signalQueue = append(s.signalQueue, SignalMessage{
		Seq: s.signalSeq, Kind: e.Type, SDP: e.SDP,
		Candidate: e.Candidate, SDPMid: e.SDPMid,
		SDPMLineIndex: e.SDPMLineIndex,
	})
	if len(s.signalQueue) > signalQueueCap {
		kept := make([]SignalMessage, signalQueueCap)
		copy(kept, s.signalQueue[len(s.signalQueue)-signalQueueCap:])
		s.signalQueue = kept
	}
}

// SignalsSince returns the queued entries newer than afterSeq
// (pass 0 for everything kept) plus the seq of the newest
// entry overall, mirroring ChatSince so the UI polls both
// with the same cursor pattern.
func (s *Session) SignalsSince(afterSeq int) (msgs []SignalMessage, latest int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.signalQueue {
		if m.Seq > afterSeq {
			msgs = append(msgs, m)
		}
	}
	return msgs, s.signalSeq
}
