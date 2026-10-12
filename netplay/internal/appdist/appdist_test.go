package appdist

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testMux(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	apk := []byte("fake-apk-bytes")
	if err := os.WriteFile(filepath.Join(dir, "iTaKGaming-1.0.31.apk"), apk, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"versionCode":31,"versionName":"1.0.31","apk":"iTaKGaming-1.0.31.apk","sha256":"abc","size":14,"notes":"n","publishedAt":"2026-10-12T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "version.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(dir, log.New(io.Discard, "", 0)).Register(mux)
	return mux, dir
}

func get(t *testing.T, mux *http.ServeMux, path string) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, body
}

func TestManifestServed(t *testing.T) {
	mux, _ := testMux(t)
	code, body := get(t, mux, "/app/version.json")
	if code != 200 || len(body) == 0 {
		t.Fatalf("manifest: code %d body %q", code, body)
	}
}

func TestAPKServed(t *testing.T) {
	mux, _ := testMux(t)
	code, body := get(t, mux, "/app/latest.apk")
	if code != 200 || string(body) != "fake-apk-bytes" {
		t.Fatalf("apk: code %d body %q", code, body)
	}
}

func TestMissingDirIs404(t *testing.T) {
	mux := http.NewServeMux()
	New(filepath.Join(t.TempDir(), "nope"), log.New(io.Discard, "", 0)).Register(mux)
	if code, _ := get(t, mux, "/app/version.json"); code != 404 {
		t.Fatalf("want 404, got %d", code)
	}
	if code, _ := get(t, mux, "/app/latest.apk"); code != 404 {
		t.Fatalf("want 404, got %d", code)
	}
}

func TestTraversalRefused(t *testing.T) {
	mux, dir := testMux(t)
	evil := `{"versionCode":32,"versionName":"x","apk":"../secret.apk","sha256":"","size":1,"notes":"","publishedAt":""}`
	if err := os.WriteFile(filepath.Join(dir, "version.json"), []byte(evil), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(t, mux, "/app/latest.apk"); code != 404 {
		t.Fatalf("traversal: want 404, got %d", code)
	}
}
