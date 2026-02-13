package plaintext

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

// Extractor handles plain text, markdown, and config-file passthrough.
// Specialized formats (HTML, CSV, JSON, XML, code, notebooks, LaTeX) are
// handled by dedicated extractors in their own packages; this is the fallback
// for text/plain MIME and simple text-based extensions.
type Extractor struct {
	maxBytes int64
}

func New(maxBytes int64) *Extractor {
	return &Extractor{maxBytes: maxBytes}
}

func (e *Extractor) Name() string { return "text" }

func (e *Extractor) MaxFileSize() int64 { return e.maxBytes }

func (e *Extractor) SupportedTypes() []string {
	return []string{"text/plain", "text/markdown"}
}

func (e *Extractor) SupportedExtensions() []string {
	return []string{
		".txt", ".text", ".log", ".ini", ".cfg", ".conf", ".env", ".properties",
		".gitignore", ".dockerignore", ".editorconfig", ".env.example",
		".md", ".mdx", ".markdown",
	}
}

func (e *Extractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	select {
	case <-ctx.Done():
		return extract.Result{Success: false}, ctx.Err()
	default:
	}

	b, err := os.ReadFile(job.LocalPath)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	text := string(b)
	ext := strings.ToLower(filepath.Ext(job.FileName))
	method := "native"
	fileType := "text/plain"

	switch ext {
	case ".md", ".mdx", ".markdown":
		text = stripFrontMatter(text)
		fileType = "text/markdown"
	}

	text = normalizeText(text)
	words, chars := extract.BuildCounts(text)
	return extract.Result{
		Success:   true,
		Text:      text,
		Method:    method,
		FileType:  fileType,
		MIMEType:  job.MIMEType,
		WordCount: words,
		CharCount: chars,
	}, nil
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = regexp.MustCompile(`\n{4,}`).ReplaceAllString(s, "\n\n\n")
	return strings.TrimSpace(s)
}

func stripFrontMatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	idx := strings.Index(s[4:], "\n---\n")
	if idx < 0 {
		return s
	}
	return s[4+idx+5:]
}
