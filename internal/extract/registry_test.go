package extract

import (
	"context"
	"testing"
)

type stubExtractor struct {
	name string
	mts  []string
	exts []string
}

func (s *stubExtractor) Extract(ctx context.Context, job Job) (Result, error) {
	return Result{Success: true}, nil
}
func (s *stubExtractor) SupportedTypes() []string      { return s.mts }
func (s *stubExtractor) SupportedExtensions() []string { return s.exts }
func (s *stubExtractor) Name() string                  { return s.name }
func (s *stubExtractor) MaxFileSize() int64            { return 0 }

func TestResolvePrefersExtension(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubExtractor{name: "generic-text", mts: []string{"text/plain"}, exts: []string{".txt"}})
	r.Register(&stubExtractor{name: "go-code", exts: []string{".go"}})

	e, err := r.Resolve("text/plain", ".go")
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if e.Name() != "go-code" {
		t.Fatalf("expected go-code extractor, got %q", e.Name())
	}
}
