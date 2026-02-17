package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRouterCallsSuccessHookOnSuccessfulExtraction(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_DOWNLOAD_URLS", "1")

	reg := NewRegistry()
	reg.Register(&stubExtractor{
		name: "text/plain",
		mts:  []string{"text/plain"},
		exts: []string{".txt"},
	})

	router := NewRouter(reg, 1<<20, 5*time.Second)

	var (
		called      bool
		gotFileType string
		gotFileSize int64
		gotDuration time.Duration
	)

	router.SetSuccessHook(func(fileType string, fileSize int64, duration time.Duration) {
		called = true
		gotFileType = fileType
		gotFileSize = fileSize
		gotDuration = duration
	})

	const payload = "hello"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() {
		http.DefaultTransport = origTransport
	}()

	res, err := router.Extract(context.Background(), UniversalExtractRequest{
		PresignedURL: srv.URL,
		FileName:     "sample.txt",
	})
	if err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success=true")
	}
	if !called {
		t.Fatalf("expected success hook to be called")
	}
	if gotFileType != "text/plain" {
		t.Fatalf("expected fileType=text/plain, got %q", gotFileType)
	}
	if gotFileSize != int64(len(payload)) {
		t.Fatalf("expected fileSize=%d, got %d", len(payload), gotFileSize)
	}
	if gotDuration <= 0 {
		t.Fatalf("expected duration > 0, got %s", gotDuration)
	}
}
