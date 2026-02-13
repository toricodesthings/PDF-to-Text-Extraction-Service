package extract

import "context"

// Extractor is implemented by every file-type handler.
type Extractor interface {
	Extract(ctx context.Context, job Job) (Result, error)
	SupportedTypes() []string
	SupportedExtensions() []string
	Name() string
	MaxFileSize() int64
}
