package structured

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type XMLExtractor struct {
	maxBytes int64
}

func NewXML(maxBytes int64) *XMLExtractor { return &XMLExtractor{maxBytes: maxBytes} }

func (e *XMLExtractor) Name() string             { return "structured/xml" }
func (e *XMLExtractor) MaxFileSize() int64       { return e.maxBytes }
func (e *XMLExtractor) SupportedTypes() []string { return []string{"application/xml", "text/xml"} }
func (e *XMLExtractor) SupportedExtensions() []string {
	return []string{".xml", ".xsd", ".xsl", ".svg", ".plist"}
}

func (e *XMLExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	d := xml.NewDecoder(bytes.NewReader(b))
	out := make([]string, 0)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			s := strings.TrimSpace(string(t))
			if s != "" {
				out = append(out, s)
			}
		}
	}
	text := strings.Join(out, "\n")
	w, c := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: w, CharCount: c}, nil
}
