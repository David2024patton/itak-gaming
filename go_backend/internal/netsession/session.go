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
	// PlayerName is this player's display name, exchanged in the
	// waiting room.
	PlayerName string
	// ReadyNow skips the waiting room's ready ritual: the peer
	// introduces itself and readies immediately. For headless
	// drivers (tests, the netplayguest rig) that have no UI.
	ReadyNow bool
	// Countdown is the shared countdown once both players are
	// ready. Zero means the default (5s).
	Countdown time.Duration
	// SilenceLimit is how long the peer may go quiet mid-session
	// before the session is declared lost. Time the peer spends
	// paused (see SetPaused) does not count. Zero means the
	// default (15s).
	SilenceLimit time.Duration
}

// RosterMember is one entry of the server's roster notice
// (PROTOCOL.md section 3): a room member's name and role
// ("host", "guest", or "spectator"). Exported because the
// mobile bridge hands the list to the UI (roster.go).
type RosterMember struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type envelope struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
	// sender tagging (PROTOCOL.md section 4.8): with spectators
	// in the room a socket carries frames from several people,
	// so senders stamp who they are. From is set by sendEnv on
	// every envelope; Seat (host 1, guest 2) rides input
	// envelopes so a watcher can attribute the two streams.
	From string `json:"from,omitempty"`
	Seat int    `json:"seat,omitempty"`
	// To addresses a snapshot transfer to one room member
	// (section 4.4); empty means unaddressed, the pre-spectator
	// behavior.
	To string `json:"to,omitempty"`
	// common fields
	Role     string   `json:"role,omitempty"`
	Game     string   `json:"game,omitempty"`
	ROMHash  string   `json:"romHash,omitempty"`
	Core     string   `json:"core,omitempty"`
	Protocol int      `json:"protocol,omitempty"`
	Cheats   []string `json:"cheats,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	// roster notice (server minted)
	Members []RosterMember `json:"members,omitempty"`
	// input
	Frame   int    `json:"frame,omitempty"`
	Buttons uint16 `json:"buttons,omitempty"`
	// hash check
	Hash string `json:"hash,omitempty"`
	// session accept
	InputDelay   int `json:"inputDelay,omitempty"`
	HashInterval int `json:"hashInterval,omitempty"`
	// waiting room
	Player string `json:"player,omitempty"`
	EndsAt int64  `json:"endsAt,omitempty"`
	// pause
	Paused bool `json:"paused,omitempty"`
	// chat
	Text string `json:"text,omitempty"`
	// voice signaling (PROTOCOL.md 3.1)
	SDP           string `json:"sdp,omitempty"`
	Candidate     string `json:"candidate,omitempty"`
	SDPMid        string `json:"sdpMid,omitempty"`
	SDPMLineIndex int    `json:"sdpMLineIndex,omitempty"`
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
	snapFrom    string // sender of the snapshot being assembled
	snapApplied int    // last frame the peer confirmed applying, -1 none

	// snapMu serializes whole snapshot transfers on the wire:
	// a transfer is a run of envelopes that must not interleave
	// with another transfer (a desync resync to the guest and a
	// watch answer to a spectator can be in flight together).
	snapMu sync.Mutex

	// watchSyncReqs holds the names of spectators who asked
	// this player session for a snapshot (watch.go), oldest
	// first. Guarded by mu.
	watchSyncReqs []string

	// Spectator state (watch.go): only used when watch is true.
	// Guarded by mu like the rest.
	watch             bool
	watchHost         string // seat 1 player's name, once known
	watchGuest        string // seat 2 player's name, once known
	watchIn1          map[int]uint16
	watchIn2          map[int]uint16
	watchFrame        int // next frame the watcher will deliver
	watchNeutralUntil int // frames below this are agreed neutral
	watchDelay        int // input delay, learned from session_accept
	watchLive         bool
	watchSnapFrame    int // frame of the last applied snapshot, -1 none
	watchSnapPending  bool
	watchMaxPair      int // highest frame holding both seats' inputs
	watchGoneHost     bool
	watchGoneGuest    bool
	watchNeedSync     bool // a fresh snapshot is owed (match ended)
	lastWatchSync     time.Time
	lastWatchStep     time.Time // last frame delivered or snapshot applied

	// host side: a resync was requested
	resyncPending   bool
	resyncReqFrame  int
	lastResyncFrame int // snapshot frame of the last completed resync

	lastPeerMsg time.Time
	desyncs     int
	stalls      int

	// waiting room state (room.go)
	phase         int
	peerPresent   bool
	peerPresentAt time.Time
	peerName      string
	peerReady     bool
	localReady    bool
	countdownEnds time.Time

	// chat log (chat.go): every message sent or received this
	// session, oldest first, capped. Guarded by mu like the rest.
	chatLog []ChatMessage
	chatSeq int // seq of the most recent log entry, 0 when empty

	// voice signaling queue (signal.go): inbound offer,
	// answer, and ice entries from the peer, oldest first,
	// capped. Guarded by mu like the rest.
	signalQueue []SignalMessage
	signalSeq   int // seq of the most recent entry, 0 when empty

	// match end and seat assignment (matchend.go, seats.go):
	// the most recent match_end's reason and whether the UI
	// has yet to collect it, plus the queue of seat calls the
	// host referee has made. Guarded by mu like the rest.
	matchEndReason string
	matchEndSet    bool
	seatAssigns    []SeatAssign

	// roster is the latest member list the server sent
	// (roster.go), guarded by mu like the rest. Empty until
	// the first roster notice lands.
	roster []RosterMember

	// started reports whether Start has launched the reader;
	// StartNextMatch refuses to run without it.
	started bool

	// pause state: while either side is paused, lockstep stalls
	// by design and the silence limit does not apply.
	localPaused bool
	peerPaused  bool
	// lastResume is when this side last unpaused. The silence
	// clock restarts there: the quiet during our own pause was
	// our doing, and the peer needs a moment to answer anyway.
	lastResume time.Time
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

func (s *Session) silenceLimit() time.Duration {
	if s.cfg.SilenceLimit > 0 {
		return s.cfg.SilenceLimit
	}
	return peerSilenceLimit
}

// SetPaused tells the peer this side has paused (its menu is
// open) or resumed. While either side is paused the other side
// waits without tripping the silence limit: a paused game is
// stalled, not lost. The transport itself still reports a peer
// that truly vanishes (socket close ends the session as before).
func (s *Session) SetPaused(p bool) {
	s.mu.Lock()
	s.localPaused = p
	if !p {
		s.lastResume = time.Now()
	}
	s.mu.Unlock()
	_ = s.sendEnv(envelope{Type: "pause", Paused: p})
}

// PeerPaused reports whether the other player has told us they
// are paused.
func (s *Session) PeerPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerPaused
}

// LocalPaused reports whether this side is marked paused.
func (s *Session) LocalPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localPaused
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

// sendEnv is the single send path for session envelopes, so
// it is where sender tagging happens (PROTOCOL.md section
// 4.8): every envelope carries "from" with our player name,
// and input envelopes additionally carry our seat number. No
// call site can forget, and a receiver can always tell a
// player's frames apart from a spectator's on the same socket.
// (Voice signaling in signal.go deliberately keeps its own
// exact wire shape and does not go through here.)
func (s *Session) sendEnv(e envelope) error {
	if s.cfg.PlayerName != "" {
		e.From = s.cfg.PlayerName
	}
	if e.Type == "input" {
		e.Seat = s.seatNumber()
	}
	return s.send(e)
}

// seatNumber is our seat at the table: host 1, guest 2.
// Spectators hold no seat (0) and never send inputs.
func (s *Session) seatNumber() int {
	if s.watch {
		return 0
	}
	if s.cfg.IsHost {
		return 1
	}
	return 2
}

// playerRole is how a player marks its hello: the seat it
// holds, "host" or "guest". A spectator's hello says
// "spectator" instead (watch.go), which is how player
// sessions know never to mistake it for the peer.
func (s *Session) playerRole() string {
	if s.cfg.IsHost {
		return "host"
	}
	return "guest"
}

func (s *Session) helloEnv() envelope {
	return envelope{
		Type: "hello", Player: s.cfg.PlayerName, Role: s.playerRole(),
	}
}

// Start runs the waiting room (PROTOCOL.md section 4.0) and then
// the session setup handshake (section 4.1), returning once both
// sides are ready to step frames from 0.
func (s *Session) Start() error {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	go s.reader()
	return s.beginMatch()
}

// StartNextMatch runs the room and the setup handshake again on
// a session a match_end already returned to the room
// (PROTOCOL.md section 4.9): the socket, the reader, and the
// room membership all survive a match, so the next one needs
// no new connection, just the same ritual as the first. It
// refuses to run while a match is live or the session is over.
func (s *Session) StartNextMatch() error {
	if s.watch {
		return errors.New("a spectator has no match to start")
	}
	s.mu.Lock()
	ready := s.started && s.phase == phaseRoom && !s.ended
	s.mu.Unlock()
	if !ready {
		return errors.New("the session is not waiting in the room")
	}
	return s.beginMatch()
}

// beginMatch is the shared body of Start and StartNextMatch:
// the waiting room ritual, then the setup handshake.
func (s *Session) beginMatch() error {
	if err := s.roomPhase(); err != nil {
		return err
	}
	s.mu.Lock()
	s.phase = phasePlaying
	s.mu.Unlock()
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
	state, frame, from := s.takeSnapshot()
	if err := s.core.LoadState(state); err != nil {
		return fmt.Errorf("loading starting snapshot: %w", err)
	}
	s.mu.Lock()
	s.frame = frame
	s.mu.Unlock()
	s.prefillNeutral(frame)
	return s.sendEnv(envelope{Type: "snapshot_applied", Frame: frame, To: from})
}
