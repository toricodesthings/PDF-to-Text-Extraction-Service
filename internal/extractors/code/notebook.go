package code

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type NotebookExtractor struct {
	maxBytes int64
}

func NewNotebook(maxBytes int64) *NotebookExtractor { return &NotebookExtractor{maxBytes: maxBytes} }

func (e *NotebookExtractor) Name() string                  { return "code/notebook" }
func (e *NotebookExtractor) MaxFileSize() int64            { return e.maxBytes }
func (e *NotebookExtractor) SupportedTypes() []string      { return []string{"application/x-ipynb+json"} }
func (e *NotebookExtractor) SupportedExtensions() []string { return []string{".ipynb"} }

func (e *NotebookExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	type cell struct {
		CellType string   `json:"cell_type"`
		Source   []string `json:"source"`
	}
	type notebook struct {
		Cells []cell `json:"cells"`
	}
	var nb notebook
	if err := json.Unmarshal(b, &nb); err != nil {
		msg := err.Error()
		return extract.Result{Success: false, FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	parts := make([]string, 0, len(nb.Cells))
	for _, c := range nb.Cells {
		src := strings.TrimSpace(strings.Join(c.Source, ""))
		if src == "" {
			continue
		}
		if c.CellType == "code" {
			parts = append(parts, "```python\n"+src+"\n```")
		} else {
			parts = append(parts, src)
		}
	}

	text := strings.Join(parts, "\n\n---\n\n")
	w, c := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: w, CharCount: c}, nil
}
