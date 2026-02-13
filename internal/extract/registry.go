package extract

import (
	"fmt"
	"strings"
)

type Registry struct {
	byMIME      map[string]Extractor
	byExtension map[string]Extractor
	extractors  []Extractor
}

func NewRegistry() *Registry {
	return &Registry{
		byMIME:      make(map[string]Extractor),
		byExtension: make(map[string]Extractor),
		extractors:  make([]Extractor, 0),
	}
}

func (r *Registry) Register(e Extractor) {
	r.extractors = append(r.extractors, e)
	for _, mt := range e.SupportedTypes() {
		key := strings.ToLower(strings.TrimSpace(mt))
		if key != "" {
			r.byMIME[key] = e
		}
	}
	for _, ext := range e.SupportedExtensions() {
		key := strings.ToLower(strings.TrimSpace(ext))
		if key != "" {
			r.byExtension[key] = e
		}
	}
}

func (r *Registry) Resolve(mimeType, extension string) (Extractor, error) {
	mt := strings.ToLower(strings.TrimSpace(mimeType))
	ext := strings.ToLower(strings.TrimSpace(extension))

	if e, ok := r.byExtension[ext]; ok {
		return e, nil
	}

	if e, ok := r.byMIME[mt]; ok {
		return e, nil
	}

	if i := strings.Index(mt, ";"); i > 0 {
		if e, ok := r.byMIME[strings.TrimSpace(mt[:i])]; ok {
			return e, nil
		}
	}

	if strings.HasPrefix(mt, "text/") {
		if e, ok := r.byMIME["text/plain"]; ok {
			return e, nil
		}
	}

	return nil, fmt.Errorf("no extractor registered for mime=%q extension=%q", mimeType, extension)
}
