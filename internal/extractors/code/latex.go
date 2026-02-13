package code

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type LaTeXExtractor struct {
	maxBytes int64
}

func NewLaTeX(maxBytes int64) *LaTeXExtractor { return &LaTeXExtractor{maxBytes: maxBytes} }

func (e *LaTeXExtractor) Name() string       { return "code/latex" }
func (e *LaTeXExtractor) MaxFileSize() int64 { return e.maxBytes }
func (e *LaTeXExtractor) SupportedTypes() []string {
	return []string{"application/x-tex", "text/x-tex"}
}
func (e *LaTeXExtractor) SupportedExtensions() []string {
	return []string{".tex", ".sty", ".cls", ".bib"}
}

func (e *LaTeXExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	s := string(b)
	s = regexp.MustCompile(`(?m)^%.*$`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\\section\{([^}]+)\}`).ReplaceAllString(s, "# $1")
	s = regexp.MustCompile(`\\subsection\{([^}]+)\}`).ReplaceAllString(s, "## $1")
	s = regexp.MustCompile(`\\subsubsection\{([^}]+)\}`).ReplaceAllString(s, "### $1")
	s = regexp.MustCompile(`\\[a-zA-Z]+\*?(\[[^\]]*\])?(\{[^}]*\})?`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "{", "")
	s = strings.ReplaceAll(s, "}", "")
	s = strings.TrimSpace(s)

	w, c := extract.BuildCounts(s)
	return extract.Result{Success: true, Text: s, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: w, CharCount: c}, nil
}
