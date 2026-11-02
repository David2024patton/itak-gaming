// Package appdist serves app self-update files: a small JSON
// manifest (version.json) describing the newest published build,
// and the APK that manifest points at. The Android app polls the
// manifest, compares version codes, downloads the APK, verifies
// its SHA-256, and hands it to the system installer.
//
// Publishing a build is a file operation only: drop the APK and a
// fresh version.json into the served directory. Nothing here
// accepts uploads.
package appdist

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Manifest is the version.json shape the app consumes.
type Manifest struct {
	VersionCode int    `json:"versionCode"`
	VersionName string `json:"versionName"`
	APK         string `json:"apk"` // file name inside the served dir
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"publishedAt"`
}

// Handler serves one update directory.
type Handler struct {
	dir    string
	logger *log.Logger
}

// New returns a handler for dir. The dir may be empty or missing
// files; requests then answer 404 rather than failing startup, so
// the lobby service runs fine before the first build is published.
func New(dir string, logger *log.Logger) *Handler {
	return &Handler{dir: dir, logger: logger}
}

// Register mounts:
//
//	GET /app/version.json  the manifest, as stored on disk
//	GET /app/latest.apk    the APK file the manifest names
//	GET /app/embed.html    the stream embed host page the app's
//	                       video player loads (PATRIOTS streams)
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/app/version.json", h.manifest)
	mux.HandleFunc("/app/latest.apk", h.apk)
	mux.HandleFunc("/app/embed.html", h.embed)
}

// embed serves the stream player host page stored in the served
// directory. YouTube's embedded player refuses app-side wrappers
// (errors 152 and 153, proven on hardware 2026-11-02): the embed
// must be framed by a page served over HTTPS from a real domain
// so the iframe request carries a genuine Referer. The page
// itself ships as a file next to version.json; publishing a new
// one is a file operation, like every other publish here.
func (h *Handler) embed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, "embed.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func (h *Handler) readManifest() (Manifest, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(h.dir, "version.json"))
	if err != nil {
		return Manifest{}, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, nil, err
	}
	return m, raw, nil
}

func (h *Handler) manifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, raw, err := h.readManifest()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func (h *Handler) apk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m, _, err := h.readManifest()
	if err != nil || m.APK == "" {
		http.NotFound(w, r)
		return
	}
	// The manifest names a plain file inside the served dir; refuse
	// anything that tries to walk out of it.
	name := filepath.Base(m.APK)
	if name != m.APK || !strings.HasSuffix(name, ".apk") {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(h.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	http.ServeContent(w, r, name, st.ModTime(), f)
}
