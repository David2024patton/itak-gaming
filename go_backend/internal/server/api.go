package server

import (
	"encoding/json"
	"net/http"

	"github.com/davidpatton/itak-gaming/go_backend/internal/core"
	"github.com/davidpatton/itak-gaming/go_backend/internal/save"
)

// Server is the Go backend API for the Flutter frontend
type Server struct {
	saveMgr *save.Manager
	is64Bit bool
	romDir  string
}

// New creates the backend server
func New(saveDir, romDir string, is64Bit bool) *Server {
	return &Server{
		saveMgr: save.NewManager(saveDir),
		is64Bit: is64Bit,
		romDir:  romDir,
	}
}

// SystemsResponse lists available systems for this build
type SystemsResponse struct {
	Systems []SystemInfo `json:"systems"`
}

type SystemInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Core string `json:"core"`
}

// HandleSystems returns the systems this build supports
func (s *Server) HandleSystems(w http.ResponseWriter, r *http.Request) {
	cores := core.Cores32Bit
	if s.is64Bit {
		cores = core.AllCores64Bit()
	}
	var systems []SystemInfo
	seen := make(map[core.System]bool)
	for _, c := range cores {
		if seen[c.System] {
			continue
		}
		seen[c.System] = true
		systems = append(systems, SystemInfo{
			ID:   string(c.System),
			Name: systemDisplayName(c.System),
			Core: c.Name,
		})
	}
	json.NewEncoder(w).Encode(SystemsResponse{Systems: systems})
}

func systemDisplayName(sys core.System) string {
	names := map[core.System]string{
		core.SystemNES:       "Nintendo",
		core.SystemSNES:      "Super Nintendo",
		core.SystemGenesis:   "Sega Genesis",
		core.SystemGameBoy:   "Game Boy",
		core.SystemGBC:       "Game Boy Color",
		core.SystemGBA:       "Game Boy Advance",
		core.SystemPS1:       "PlayStation",
		core.SystemSegaCD:    "Sega CD",
		core.SystemN64:       "Nintendo 64",
		core.SystemDreamcast: "Dreamcast",
		core.SystemPSP:       "PSP",
		core.SystemDS:        "Nintendo DS",
		core.SystemSaturn:    "Saturn",
		core.SystemWii:       "Wii / GameCube",
	}
	if n, ok := names[sys]; ok {
		return n
	}
	return string(sys)
}

// Start runs the HTTP API server
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/systems", s.HandleSystems)
	// TODO: /api/games, /api/launch, /api/save, /api/load, /api/players
	return http.ListenAndServe(addr, mux)
}
