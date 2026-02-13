package audio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

func TestExtractSuccessWithTimestamps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("missing auth header, got=%q", got)
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := r.FormValue("model"); got != "whisper-large-v3-turbo" {
			t.Fatalf("model mismatch: %q", got)
		}
		if got := r.FormValue("language"); got != "en" {
			t.Fatalf("language mismatch: %q", got)
		}
		if got := r.FormValue("prompt"); got != "meeting" {
			t.Fatalf("prompt mismatch: %q", got)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"text":     "fallback text",
			"language": "en",
			"duration": 12.7,
			"segments": []map[string]any{
				{"start": 0.0, "end": 3.2, "text": "Hello team"},
				{"start": 3.2, "end": 8.1, "text": "This is a test"},
			},
		})
	}))
	defer srv.Close()

	audioPath := writeTempAudioFile(t)
	e := New("test-key", srv.URL, "whisper-large-v3-turbo", 2<<20, 5*time.Second)

	res, err := e.Extract(context.Background(), extract.Job{
		LocalPath: audioPath,
		FileName:  filepath.Base(audioPath),
		MIMEType:  "audio/mpeg",
		FileSize:  16,
		Options: map[string]any{
			"timestamps": true,
			"language":   "en",
			"prompt":     "meeting",
		},
	})
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success result")
	}
	if !strings.Contains(res.Text, "[00:00] Hello team") {
		t.Fatalf("expected timestamped text, got: %q", res.Text)
	}
	if !strings.Contains(res.Text, "[00:03] This is a test") {
		t.Fatalf("expected second timestamped segment, got: %q", res.Text)
	}
	if res.Metadata["language"] != "en" {
		t.Fatalf("expected language metadata")
	}
	if res.Metadata["model"] != "whisper-large-v3-turbo" {
		t.Fatalf("expected model metadata")
	}
}

func TestExtractHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid key","type":"auth_error"}}`)
	}))
	defer srv.Close()

	audioPath := writeTempAudioFile(t)
	e := New("bad-key", srv.URL, "whisper-large-v3-turbo", 2<<20, 5*time.Second)

	res, err := e.Extract(context.Background(), extract.Job{
		LocalPath: audioPath,
		FileName:  filepath.Base(audioPath),
		MIMEType:  "audio/mpeg",
		FileSize:  16,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if res.Error == nil {
		t.Fatalf("expected result error")
	}
	if !strings.Contains(*res.Error, "groq 401") {
		t.Fatalf("unexpected error message: %q", *res.Error)
	}
}

func TestFormatTimecode(t *testing.T) {
	if got := formatTimecode(5.1); got != "00:05" {
		t.Fatalf("unexpected mm:ss: %q", got)
	}
	if got := formatTimecode(3723.1); got != "01:02:03" {
		t.Fatalf("unexpected hh:mm:ss: %q", got)
	}
}

func writeTempAudioFile(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, "sample.mp3")
	if err := os.WriteFile(p, []byte("fake-audio-content"), 0o644); err != nil {
		t.Fatalf("write temp audio: %v", err)
	}
	return p
}
