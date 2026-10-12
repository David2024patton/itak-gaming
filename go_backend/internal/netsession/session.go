package netsession

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Core is the slice of the emulator bridge the session drives. The
// mobile package adapts the real bridge to it; tests use a fake.
type Core interface {
	RunFrame()
	// SetButtons applies a full 16-bit button mask (libretro order)
	// to one player slot.
	SetButtons(player int, mask uint16)
	SaveState() []byte
	LoadState(data []byte) error
}

// Transport is the signaling socket (WSConn satisfies it).
type Transport interface {
	WriteText(text string) error
	ReadMessage() ([]byte, error)
	Close() error
}

type Config struct {
	Game     string // ROM file name both sides must agree on
	ROMHash  string // sha256 hex of the local ROM file
	CoreName string
	IsHost   bool
	// InputDelay is what the guest proposes in session_accept.
	// Zero means the default (4).
	InputDelay int
}

type envelope struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
	// common fields
	Role     string   `json:"role,omitempty"`
	Game     string   `json:"game,omitempty"`
	ROMHash  string   `json:"romHash,omitempty"`
	Core     string   `json:"core,omitempty"`
	Protocol int      `json:"protocol,omitempty"`
	Cheats   []string `json:"cheats,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	// input
	Frame   int    `json:"frame,omitempty"`
	Buttons uint16 `json:"buttons,omitempty"`
	// hash check
	Hash string `json:"hash,omitempty"`
	// session accept
	InputDelay   int `json:"inputDelay,omitempty"`
	HashInterval int `json:"hashInterval,omitempty"`
	// snapshot
	Bytes  int    `json:"bytes,omitempty"`
	Chunks int    `json:"chunks,omitempty"`
	Seq    int    `json:"seq,omitempty"`
	Data   string `json:"data,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

const (
	defaultInputDelay   = 4
	defaultHashInterval = 300
	snapshotChunkBytes  = 64 * 1024
	peerSilenceLimit    = 15 * time.Second
)

type Session struct {
	t    Transport
	core Core
	cfg  Config

	delay        int
	hashInterval int

	mu       sync.Mutex
	notifyCh chan struct{}

	frame    int // next frame to simulate
	localIn  map[int]uint16
	remoteIn map[int]uint16

	localHash  map[int]string
	remoteHash map[int]string
	checked    map[int]bool

	offer      *envelope
	accept     *envelope
	reject     string
	peerJoined bool
	peerGone   bool
	ended      bool
	endErr     error

	// snapshot assembly (guest side) / application
	snapBuf     []byte
	snapWant    int
	snapChunks  int
	snapGot     int
	snapFrame   int
	snapReady   bool
	snapApplied int // last frame the peer confirmed applying, -1 none

	// host side: a resync was requested
	resyncPending   bool
	resyncReqFrame  int
	lastResyncFrame int // snapshot frame of the last completed resync

	lastPeerMsg time.Time
	desyncs     int
	stalls      int
}

func NewSession(t Transport, core Core, cfg Config) *Session {
	return &Session{
		t:               t,
		core:            core,
		cfg:             cfg,
		delay:           defaultInputDelay,
		hashInterval:    defaultHashInterval,
		notifyCh:        make(chan struct{}, 1),
		localIn:         make(map[int]uint16),
		remoteIn:        make(map[int]uint16),
		localHash:       make(map[int]string),
		remoteHash:      make(map[int]string),
		checked:         make(map[int]bool),
		snapApplied:     -1,
		lastResyncFrame: -1,
		lastPeerMsg:     time.Now(),
	}
}

func (s *Session) signal() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func (s *Session) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.t.WriteText(string(b))
}

func (s *Session) sendEnv(e envelope) error { return s.send(e) }

// Start runs the session setup handshake (PROTOCOL.md section 4.1)
// and returns once both sides are ready to step frames from 0.
func (s *Session) Start() error {
	go s.reader()
	if s.cfg.IsHost {
		return s.startHost()
	}
	return s.startGuest()
}

func (s *Session) startHost() error {
	// Wait for the guest to show up on the socket.
	if err := s.waitFor(func() bool { return s.peerJoined || s.ended }, 600*time.Second); err != nil {
		return err
	}
	if err := s.checkEnded(); err != nil {
		return err
	}
	if err := s.sendEnv(envelope{
		Type: "session_offer", Game: s.cfg.Game, ROMHash: s.cfg.ROMHash,
		Core: s.cfg.CoreName, Protocol: 1, Cheats: []string{},
	}); err != nil {
		return err
	}
	if err := s.waitFor(func() bool {
		return s.accept != nil || s.reject != "" || s.ended
	}, 60*time.Second); err != nil {
		return err
	}
	if s.reject != "" {
		return fmt.Errorf("guest rejected the session: %s", s.reject)
	}
	if err := s.checkEnded(); err != nil {
		return err
	}
	s.mu.Lock()
	s.delay = s.accept.InputDelay
	if s.delay <= 0 {
		s.delay = defaultInputDelay
	}
	s.hashInterval = s.accept.HashInterval
	if s.hashInterval <= 0 {
		s.hashInterval = defaultHashInterval
	}
	s.mu.Unlock()
	// Starting snapshot: the power-on state, captured before any
	// frame runs, so both sides begin byte-identical.
	state := s.core.SaveState()
	if len(state) == 0 {
		return errors.New("core returned an empty starting state")
	}
	if err := s.sendSnapshot(0, state); err != nil {
		return err
	}
	if err := s.waitFor(func() bool {
		return s.snapApplied >= 0 || s.ended
	}, 60*time.Second); err != nil {
		return err
	}
	s.prefillNeutral(0)
	return nil
}

// prefillNeutral seeds both input maps with neutral (no buttons)
// inputs for frames [from, from+delay). Both sides do this wherever
// a snapshot (re)starts play: those frames were never sampled by
// either player, and lockstep needs an agreed value for them.
func (s *Session) prefillNeutral(from int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for f := from; f < from+s.delay; f++ {
		s.localIn[f] = 0
		s.remoteIn[f] = 0
	}
}

func (s *Session) startGuest() error {
	if err := s.waitFor(func() bool {
		return s.offer != nil || s.ended
	}, 120*time.Second); err != nil {
		return err
	}
	if err := s.checkEnded(); err != nil {
		return err
	}
	s.mu.Lock()
	offer := s.offer
	s.mu.Unlock()
	if offer.ROMHash != s.cfg.ROMHash || offer.Game != s.cfg.Game {
		_ = s.sendEnv(envelope{
			Type:   "session_reject",
			Reason: "different game or ROM version",
		})
		return fmt.Errorf("session rejected: ROM mismatch (host has %q)", offer.Game)
	}
	delay := s.cfg.InputDelay
	if delay <= 0 {
		delay = defaultInputDelay
	}
	if err := s.sendEnv(envelope{
		Type: "session_accept", InputDelay: delay,
		HashInterval: defaultHashInterval,
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.delay = delay
	s.hashInterval = defaultHashInterval
	s.mu.Unlock()
	// Wait for the starting snapshot and apply it.
	if err := s.waitFor(func() bool { return s.snapReady || s.ended }, 60*time.Second); err != nil {
		return err
	}
	if err := s.checkEnded(); err != nil {
		return err
	}
	state, frame := s.takeSnapshot()
	if err := s.core.LoadState(state); err != nil {
		return fmt.Errorf("loading starting snapshot: %w", err)
	}
	s.mu.Lock()
	s.frame = frame
	s.mu.Unlock()
	s.prefillNeutral(frame)
	return s.sendEnv(envelope{Type: "snapshot_applied", Frame: frame})
}
