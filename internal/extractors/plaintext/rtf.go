package plaintext

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type RTFExtractor struct {
	maxBytes int64
}

func NewRTF(maxBytes int64) *RTFExtractor { return &RTFExtractor{maxBytes: maxBytes} }

func (e *RTFExtractor) Name() string                  { return "document/rtf" }
func (e *RTFExtractor) MaxFileSize() int64            { return e.maxBytes }
func (e *RTFExtractor) SupportedTypes() []string      { return []string{"application/rtf", "text/rtf"} }
func (e *RTFExtractor) SupportedExtensions() []string { return []string{".rtf"} }

func (e *RTFExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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
	s = regexp.MustCompile(`\\par[d]?`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`\\tab`).ReplaceAllString(s, "\t")
	s = regexp.MustCompile(`\\'[0-9a-fA-F]{2}`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\\[a-zA-Z]+-?\d* ?`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "{", "")
	s = strings.ReplaceAll(s, "}", "")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)

	w, c := extract.BuildCounts(s)
	return extract.Result{Success: true, Text: s, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: w, CharCount: c}, nil
}
