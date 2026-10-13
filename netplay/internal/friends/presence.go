package friends

import (
	"sync"
	"time"
)

// PresenceWindow is how long a player still counts as online
// after their app last pinged. Clients ping every 30 s or so
// while the app is open, so 90 s absorbs two missed pings before
// a friend flips to offline.
const PresenceWindow = 90 * time.Second

// sweepThreshold bounds the tracker's memory: once this many
// names are tracked, a ping also sweeps out the expired ones.
// Below the threshold, expiry is pruned lazily on read instead,
// so the hot paths stay proportional to one name.
const sweepThreshold = 1024

// Presence tracks which players have the app open right now.
// It is deliberately memory only: presence is a statement about
// this moment, so it is never written to disk and a server
// restart simply starts everyone at offline until their apps
// ping again. All methods are safe for concurrent use.
type Presence struct {
	mu   sync.Mutex
	seen map[string]time.Time // player name to last ping time
	now  func() time.Time     // the clock; tests replace it
}

// NewPresence returns an empty tracker reading the real clock.
func NewPresence() *Presence {
	return &Presence{seen: make(map[string]time.Time), now: time.Now}
}

// Ping records that player's app is open now. The name gets the
// package's usual hygiene (trimmed, control characters stripped)
// and must satisfy the usual rules; Ping returns the validation
// error otherwise and records nothing.
func (p *Presence) Ping(player string) error {
	player = clean(player)
	if err := validateName(player); err != nil {
		return err
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) >= sweepThreshold {
		p.sweepLocked(now)
	}
	p.seen[player] = now
	return nil
}

// Online reports whether player's app pinged within
// PresenceWindow. An expired entry is deleted on the way out,
// so stale names do not pile up behind the friends people
// actually look at.
func (p *Presence) Online(player string) bool {
	player = clean(player)
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	last, ok := p.seen[player]
	if !ok {
		return false
	}
	if now.Sub(last) > PresenceWindow {
		delete(p.seen, player)
		return false
	}
	return true
}

// sweepLocked deletes every expired entry. Caller holds p.mu.
func (p *Presence) sweepLocked(now time.Time) {
	for name, last := range p.seen {
		if now.Sub(last) > PresenceWindow {
			delete(p.seen, name)
		}
	}
}
