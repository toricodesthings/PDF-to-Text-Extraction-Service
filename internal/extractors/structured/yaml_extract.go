package structured

import (
	"context"
	"os"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
	"gopkg.in/yaml.v3"
)

type YAMLExtractor struct {
	maxBytes int64
}

func NewYAML(maxBytes int64) *YAMLExtractor { return &YAMLExtractor{maxBytes: maxBytes} }

func (e *YAMLExtractor) Name() string       { return "structured/yaml" }
func (e *YAMLExtractor) MaxFileSize() int64 { return e.maxBytes }
func (e *YAMLExtractor) SupportedTypes() []string {
	return []string{"application/yaml", "text/yaml", "application/x-yaml"}
}
func (e *YAMLExtractor) SupportedExtensions() []string { return []string{".yaml", ".yml", ".toml"} }

func (e *YAMLExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	text := strings.TrimSpace(string(b))
	if strings.HasSuffix(strings.ToLower(job.FileName), ".yaml") || strings.HasSuffix(strings.ToLower(job.FileName), ".yml") {
		var obj any
		if err := yaml.Unmarshal(b, &obj); err == nil {
			if out, mErr := yaml.Marshal(obj); mErr == nil {
				text = strings.TrimSpace(string(out))
			}
		}
	}

	w, c := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: w, CharCount: c}, nil
}
