package debugchute

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCode = "0123456789abcdef0123456789abcdef"

func newTestMux(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "labels.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(dir, log.New(io.Discard, "", 0)).Register(mux)
	return mux, dir
}

func post(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func reportBody(reason string) string {
	return `{"install_code":"` + testCode + `","versionName":"1.69","versionCode":70,` +
		`"model":"Chromecast HD","sdk":34,"freeStorageMB":512,` +
		`"timingLines":["native timing: nes avg frame 9ms over 600 frames"],` +
		`"logTail":"some log","reason":"` + reason + `","timestamp":"2026-10-23T04:00:00Z"}`
}

func pingBody() string {
	return `{"install_code":"` + testCode + `","versionName":"1.69","versionCode":70,` +
		`"model":"Chromecast HD","sdk":34,"freeStorageMB":512,` +
		`"timingLines":["native timing: gb avg frame 4ms over 600 frames"],` +
		`"timestamp":"2026-10-23T04:00:00Z"}`
}

func TestReportStoresLatestAndReport(t *testing.T) {
	mux, dir := newTestMux(t)
	key := keyFor(testCode)
	rec := post(t, mux, "/debug/report", reportBody("manual"))
	if rec.Code != http.StatusOK {
		t.Fatalf("report status = %d, body %s", rec.Code, rec.Body)
	}
	latest, err := os.ReadFile(filepath.Join(dir, key, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(latest, &m); err != nil {
		t.Fatal(err)
	}
	if m["key"] != key || m["reason"] != "manual" || m["lastSeen"] == nil {
		t.Fatalf("latest.json missing fields: %v", m)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, key))
	reports := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "report-") {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("report files = %d, want 1", reports)
	}
}

func TestPingWantFlagAndClearOnReport(t *testing.T) {
	mux, dir := newTestMux(t)
	key := keyFor(testCode)
	labels := map[string]labelEntry{key: {Label: "Test Stick", WantFullReport: true}}
	raw, _ := json.Marshal(labels)
	if err := os.WriteFile(filepath.Join(dir, "labels.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := post(t, mux, "/debug/ping", pingBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("ping status = %d", rec.Code)
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp["want_full_report"] {
		t.Fatalf("ping response should carry want_full_report=true: %s", rec.Body)
	}
	latest, _ := os.ReadFile(filepath.Join(dir, key, "latest.json"))
	if !strings.Contains(string(latest), `"Test Stick"`) || !strings.Contains(string(latest), `"lastSeen"`) {
		t.Fatalf("ping latest.json should carry label and lastSeen: %s", latest)
	}

	// The pulled full report lands and clears the flag.
	rec = post(t, mux, "/debug/report", reportBody("pull"))
	if rec.Code != http.StatusOK {
		t.Fatalf("report status = %d", rec.Code)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "labels.json"))
	var back map[string]labelEntry
	if err := json.Unmarshal(after, &back); err != nil {
		t.Fatal(err)
	}
	if back[key].WantFullReport {
		t.Fatal("want flag should be cleared after a full report")
	}
	if back[key].Label != "Test Stick" {
		t.Fatalf("label lost: %+v", back[key])
	}
	stored, _ := os.ReadFile(filepath.Join(dir, key, "latest.json"))
	if !strings.Contains(string(stored), `"label": "Test Stick"`) {
		t.Fatalf("stored report should carry the label: %s", stored)
	}

	rec = post(t, mux, "/debug/ping", pingBody())
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["want_full_report"] {
		t.Fatal("flag should stay cleared")
	}
}

func TestRejectsBadCodeAndOversize(t *testing.T) {
	mux, _ := newTestMux(t)
	rec := post(t, mux, "/debug/report", `{"install_code":"short","reason":"manual"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short code status = %d, want 400", rec.Code)
	}
	big := `{"install_code":"` + testCode + `","logTail":"` + strings.Repeat("x", MaxReportBytes) + `"}`
	rec = post(t, mux, "/debug/report", big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d, want 413", rec.Code)
	}
}

func TestRotationKeepsNewest20(t *testing.T) {
	mux, dir := newTestMux(t)
	key := keyFor(testCode)
	for i := 0; i < keepReports+3; i++ {
		rec := post(t, mux, "/debug/report", reportBody("launch"))
		if rec.Code != http.StatusOK {
			t.Fatalf("report %d status = %d", i, rec.Code)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, key))
	reports := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "report-") {
			reports++
		}
	}
	if reports != keepReports {
		t.Fatalf("report files after rotation = %d, want %d", reports, keepReports)
	}
}
