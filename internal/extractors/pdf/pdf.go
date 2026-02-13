package pdf

import (
	"context"

	"github.com/toricodesthings/file-processing-service/internal/extract"
	"github.com/toricodesthings/file-processing-service/internal/hybrid"
	"github.com/toricodesthings/file-processing-service/internal/types"
)

type Extractor struct {
	processor *hybrid.Processor
	maxBytes  int64
}

func New(processor *hybrid.Processor, maxBytes int64) *Extractor {
	return &Extractor{processor: processor, maxBytes: maxBytes}
}

func (e *Extractor) Name() string { return "document/pdf" }

func (e *Extractor) MaxFileSize() int64 { return e.maxBytes }

func (e *Extractor) SupportedTypes() []string {
	return []string{"application/pdf"}
}

func (e *Extractor) SupportedExtensions() []string {
	return []string{".pdf"}
}

func (e *Extractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	opts := e.processor.ApplyDefaults(types.HybridProcessorOptions{})
	out, err := e.processor.ProcessHybrid(ctx, job.PresignedURL, job.LocalPath, opts)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, Method: "hybrid", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	pages := make([]extract.PageResult, 0, len(out.Pages))
	for _, p := range out.Pages {
		pages = append(pages, extract.PageResult{
			PageNumber: p.PageNumber,
			Text:       p.Text,
			Method:     p.Method,
			WordCount:  p.WordCount,
		})
	}

	words, chars := extract.BuildCounts(out.Text)
	return extract.Result{
		Success:   true,
		Text:      out.Text,
		Method:    "hybrid",
		FileType:  e.Name(),
		MIMEType:  job.MIMEType,
		Pages:     pages,
		WordCount: words,
		CharCount: chars,
	}, nil
}
