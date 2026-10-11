package save

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Player represents a family member with their own saves
type Player struct {
	ID   string
	Name string
}

// SaveSlot represents a single save state
type SaveSlot struct {
	PlayerID  string
	GameID    string // ROM filename without extension
	Slot      int    // 0-9, 10 slots per player per game
	Timestamp time.Time
	Path      string
}

// Manager handles save states for multiple players
type Manager struct {
	baseDir string
}

// NewManager creates a save manager rooted at baseDir
func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// playerDir returns the directory for a player's saves
func (m *Manager) playerDir(playerID string) string {
	return filepath.Join(m.baseDir, "saves", playerID)
}

// Save writes a save state for a player/game/slot
func (m *Manager) Save(playerID, gameID string, slot int, data []byte) error {
	if slot < 0 || slot > 9 {
		return fmt.Errorf("slot must be 0-9, got %d", slot)
	}
	dir := filepath.Join(m.playerDir(playerID), gameID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("slot%d.sav", slot))
	return os.WriteFile(path, data, 0644)
}

// Load reads a save state for a player/game/slot
func (m *Manager) Load(playerID, gameID string, slot int) ([]byte, error) {
	path := filepath.Join(m.playerDir(playerID), gameID, fmt.Sprintf("slot%d.sav", slot))
	return os.ReadFile(path)
}

// ListSaves returns all saves for a player (optionally filtered by game)
func (m *Manager) ListSaves(playerID, gameID string) ([]SaveSlot, error) {
	var saves []SaveSlot
	base := m.playerDir(playerID)
	if gameID != "" {
		base = filepath.Join(base, gameID)
	}

	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Parse: saves/<player>/<game>/slot<N>.sav
		rel, _ := filepath.Rel(m.playerDir(playerID), path)
		parts := filepath.SplitList(rel)
		if len(parts) < 2 {
			return nil
		}
		var slot int
		fmt.Sscanf(filepath.Base(path), "slot%d.sav", &slot)
		saves = append(saves, SaveSlot{
			PlayerID:  playerID,
			GameID:    filepath.Dir(rel),
			Slot:      slot,
			Timestamp: info.ModTime(),
			Path:      path,
		})
		return nil
	})
	return saves, err
}

// Delete removes a save state
func (m *Manager) Delete(playerID, gameID string, slot int) error {
	path := filepath.Join(m.playerDir(playerID), gameID, fmt.Sprintf("slot%d.sav", slot))
	return os.Remove(path)
}
