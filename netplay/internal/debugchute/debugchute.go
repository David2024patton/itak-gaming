// Package debugchute is the receiving end of the app's debug
// chute: every install of iTaK Gaming may POST small health
// reports to our own server so a stick can be diagnosed without
// Android wireless debugging (which turns itself off whenever it
// likes) or any inbound connection to the box. The flow is
// one-way only: this package accepts reports, it never answers
// with anything but a receipt and the want-full-report flag.
//
// Two endpoints:
//
//	POST /debug/ping    tiny check-in (version, model, free
//	                    storage, newest timing lines). Refreshes
//	                    the stick's latest.json and answers
//	                    {"want_full_report": bool} from the
//	                    per-stick flag in labels.json.
//	POST /debug/report  the full report (adds the app log tail).
//	                    Stored as a timestamped file, refreshes
//	                    latest.json, and clears the stick's
//	                    want-full-report flag.
//
// On disk, under the configured root:
//
//	<key>/report-<UTC stamp>.json   newest 20 kept per stick
//	<key>/latest.json               newest state, ping or report
//	labels.json                     key -> "Label" or
//	                                {"label": "...", "wantFullReport": true}
//
// <key> is the first 12 hex chars of the SHA-256 of the stick's
// install code, so the code itself never names a directory and
// sticks self-register on first contact, unlabeled until a human
// adds them to labels.json.
package debugchute

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxReportBytes caps a full report body; anything larger is
// refused with 413 instead of being stored.
const MaxReportBytes = 300 << 10

// MaxPingBytes caps a check-in body; pings are tiny by design.
const MaxPingBytes = 64 << 10

// keepReports is how many full reports survive per stick.
const keepReports = 20

// report is the full-report payload the app sends.
type report struct {
	InstallCode   string   `json:"install_code"`
	VersionName   string   `json:"versionName"`
	VersionCode   int      `json:"versionCode"`
	Model         string   `json:"model"`
	SDK           int      `json:"sdk"`
	FreeStorageMB *int64   `json:"freeStorageMB,omitempty"`
	TimingLines   []string `json:"timingLines"`
	LogTail       string   `json:"logTail"`
	Reason        string   `json:"reason"`
	Timestamp     string   `json:"timestamp"`
}

// ping is the tiny check-in payload.
type ping struct {
	InstallCode   string   `json:"install_code"`
	VersionName   string   `json:"versionName"`
	VersionCode   int      `json:"versionCode"`
	Model         string   `json:"model"`
	SDK           int      `json:"sdk"`
	FreeStorageMB *int64   `json:"freeStorageMB,omitempty"`
	TimingLines   []string `json:"timingLines"`
	Timestamp     string   `json:"timestamp"`
}

// labelEntry is one stick's labels.json value. The file may hold
// a bare string (just the label) or this object.
type labelEntry struct {
	Label          string `json:"label"`
	WantFullReport bool   `json:"wantFullReport"`
}

// Handler stores debug chute traffic under one root directory.
type Handler struct {
	dir    string
	logger *log.Logger
	mu     sync.Mutex // serializes file updates (labels, latest)
}

// New returns a handler rooted at dir (created on first use).
func New(dir string, logger *log.Logger) *Handler {
	return &Handler{dir: dir, logger: logger}
}

// Register mounts:
//
//	POST /debug/report  store a full report
//	POST /debug/ping    store a check-in, answer the want flag
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/debug/report", h.report)
	mux.HandleFunc("/debug/ping", h.ping)
}

// keyFor derives the on-disk key for an install code.
func keyFor(installCode string) string {
	sum := sha256.Sum256([]byte(installCode))
	return hex.EncodeToString(sum[:])[:12]
}

// validInstallCode accepts at least 16 hex characters.
func validInstallCode(code string) bool {
	if len(code) < 16 {
		return false
	}
	for _, c := range code {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !ok {
			return false
		}
	}
	return true
}

// readBody reads a capped request body; errTooLarge means 413.
var errTooLarge = errors.New("body too large")

func readBody(w http.ResponseWriter, r *http.Request, capBytes int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, capBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			return nil, errTooLarge
		}
		return nil, err
	}
	return raw, nil
}

func (h *Handler) labelsPath() string { return filepath.Join(h.dir, "labels.json") }

// loadLabels reads labels.json, tolerating both value shapes
// (bare string or object) and a missing file.
func (h *Handler) loadLabels() map[string]labelEntry {
	out := map[string]labelEntry{}
	raw, err := os.ReadFile(h.labelsPath())
	if err != nil {
		return out
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		h.logger.Printf("debugchute: labels.json unreadable: %v", err)
		return out
	}
	for k, v := range generic {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = labelEntry{Label: s}
			continue
		}
		var e labelEntry
		if err := json.Unmarshal(v, &e); err == nil {
			out[k] = e
		}
	}
	return out
}

// saveLabels writes labels.json atomically.
func (h *Handler) saveLabels(labels map[string]labelEntry) error {
	raw, err := json.MarshalIndent(labels, "", "  ")
	if err != nil {
		return err
	}
	tmp := h.labelsPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.labelsPath())
}

// writeJSONFile writes indented JSON to path atomically.
func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// stampName is the report file name for a receive time.
func stampName(now time.Time) string {
	return "report-" + now.UTC().Format("20060102T150405.000Z") + ".json"
}

// rotateLocked keeps only the newest keepReports report files
// in dir. Caller holds h.mu.
func (h *Handler) rotateLocked(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "report-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamped names sort oldest first
	for len(names) > keepReports {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := readBody(w, r, MaxReportBytes)
	if err == errTooLarge {
		http.Error(w, "report too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	var rep report
	if err := json.Unmarshal(raw, &rep); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if !validInstallCode(rep.InstallCode) {
		http.Error(w, "install_code must be at least 16 hex chars", http.StatusBadRequest)
		return
	}

	key := keyFor(rep.InstallCode)
	now := time.Now().UTC()

	h.mu.Lock()
	defer h.mu.Unlock()

	labels := h.loadLabels()
	entry := labels[key]

	// Stored copy: the report as sent, plus server-side fields.
	stored := map[string]any{}
	_ = json.Unmarshal(raw, &stored)
	stored["key"] = key
	if entry.Label != "" {
		stored["label"] = entry.Label
	}
	stored["receivedAt"] = now.Format(time.RFC3339)
	stored["lastSeen"] = now.Format(time.RFC3339)

	dir := filepath.Join(h.dir, key)
	name := stampName(now)
	path := filepath.Join(dir, name)
	for i := 1; ; i++ { // same-millisecond collision guard
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		name = strings.TrimSuffix(stampName(now), ".json") + "-" + strings.Repeat("x", i) + ".json"
		path = filepath.Join(dir, name)
	}
	if err := writeJSONFile(path, stored); err != nil {
		h.logger.Printf("debugchute: store report %s: %v", key, err)
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	stored["lastFullReport"] = name
	if err := writeJSONFile(filepath.Join(dir, "latest.json"), stored); err != nil {
		h.logger.Printf("debugchute: store latest %s: %v", key, err)
	}
	h.rotateLocked(dir)

	// A landed full report satisfies a pending pull request.
	if entry.WantFullReport {
		entry.WantFullReport = false
		labels[key] = entry
		if err := h.saveLabels(labels); err != nil {
			h.logger.Printf("debugchute: clear want flag %s: %v", key, err)
		}
	}

	label := entry.Label
	if label == "" {
		label = "UNLABELED"
	}
	h.logger.Printf("debugchute: report key=%s label=%s reason=%s bytes=%d", key, label, rep.Reason, len(raw))

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *Handler) ping(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := readBody(w, r, MaxPingBytes)
	if err == errTooLarge {
		http.Error(w, "ping too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	var p ping
	if err := json.Unmarshal(raw, &p); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if !validInstallCode(p.InstallCode) {
		http.Error(w, "install_code must be at least 16 hex chars", http.StatusBadRequest)
		return
	}

	key := keyFor(p.InstallCode)
	now := time.Now().UTC()

	h.mu.Lock()
	defer h.mu.Unlock()

	labels := h.loadLabels()
	entry := labels[key]

	dir := filepath.Join(h.dir, key)
	// Carry the newest full report's file name forward so
	// latest.json always points at it even after a ping.
	lastFull := ""
	if prev, err := os.ReadFile(filepath.Join(dir, "latest.json")); err == nil {
		var prevMap map[string]any
		if json.Unmarshal(prev, &prevMap) == nil {
			if s, ok := prevMap["lastFullReport"].(string); ok {
				lastFull = s
			}
		}
	}

	state := map[string]any{
		"key":             key,
		"installCode":     p.InstallCode,
		"versionName":     p.VersionName,
		"versionCode":     p.VersionCode,
		"model":           p.Model,
		"sdk":             p.SDK,
		"timingLines":     p.TimingLines,
		"deviceTimestamp": p.Timestamp,
		"reason":          "ping",
		"lastSeen":        now.Format(time.RFC3339),
		"lastFullReport":  lastFull,
	}
	if p.FreeStorageMB != nil {
		state["freeStorageMB"] = *p.FreeStorageMB
	}
	if entry.Label != "" {
		state["label"] = entry.Label
	}
	if err := writeJSONFile(filepath.Join(dir, "latest.json"), state); err != nil {
		h.logger.Printf("debugchute: store ping latest %s: %v", key, err)
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}

	label := entry.Label
	if label == "" {
		label = "UNLABELED"
	}
	h.logger.Printf("debugchute: ping key=%s label=%s bytes=%d", key, label, len(raw))

	resp, _ := json.Marshal(map[string]bool{"want_full_report": entry.WantFullReport})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}
