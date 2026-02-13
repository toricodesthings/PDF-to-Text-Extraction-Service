package structured

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type JSONExtractor struct {
	maxBytes int64
}

func NewJSON(maxBytes int64) *JSONExtractor { return &JSONExtractor{maxBytes: maxBytes} }

func (e *JSONExtractor) Name() string             { return "structured/json" }
func (e *JSONExtractor) MaxFileSize() int64       { return e.maxBytes }
func (e *JSONExtractor) SupportedTypes() []string { return []string{"application/json"} }
func (e *JSONExtractor) SupportedExtensions() []string {
	return []string{".json", ".jsonl", ".geojson"}
}

func (e *JSONExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	var text string
	if strings.HasSuffix(strings.ToLower(job.FileName), ".jsonl") {
		text = formatJSONL(string(b))
	} else {
		text = prettyJSON(b)
	}
	text = strings.TrimSpace(text)
	w, c := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: w, CharCount: c}, nil
}

func prettyJSON(b []byte) string {
	var obj any
	if err := json.Unmarshal(b, &obj); err != nil {
		return string(b)
	}
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return string(b)
	}
	return string(out)
}

func formatJSONL(s string) string {
	lines := strings.Split(s, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		parts = append(parts, prettyJSON([]byte(trim)))
	}
	return strings.Join(parts, "\n\n---\n\n")
}
