package audio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/toricodesthings/file-processing-service/internal/extract"
	"github.com/toricodesthings/file-processing-service/internal/transcribe"
)

type Extractor struct {
	client   *transcribe.Client
	model    string
	maxBytes int64
}

func New(apiKey, apiURL, model string, maxBytes int64, timeout time.Duration) *Extractor {
	if strings.TrimSpace(model) == "" {
		model = "whisper-large-v3-turbo"
	}
	return &Extractor{client: transcribe.NewClient(apiKey, apiURL, timeout), model: model, maxBytes: maxBytes}
}

func (e *Extractor) Name() string       { return "media/audio" }
func (e *Extractor) MaxFileSize() int64 { return e.maxBytes }
func (e *Extractor) SupportedTypes() []string {
	return []string{"audio/mpeg", "audio/wav", "audio/x-wav", "audio/mp4", "audio/ogg", "audio/flac", "audio/aac", "audio/webm", "audio/opus"}
}
func (e *Extractor) SupportedExtensions() []string {
	return []string{".mp3", ".wav", ".m4a", ".ogg", ".flac", ".aac", ".wma", ".opus", ".webm"}
}

func (e *Extractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	if e.client == nil {
		msg := "transcribe client is nil"
		return extract.Result{Success: false, Method: "groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, errors.New(msg)
	}

	if max := e.MaxFileSize(); max > 0 && job.FileSize > max {
		msg := fmt.Sprintf("audio file exceeds limit (%dMB)", max/(1<<20))
		return extract.Result{Success: false, Method: "groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, errors.New(msg)
	}

	b, err := os.ReadFile(job.LocalPath)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, Method: "groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}
	if len(b) == 0 {
		msg := "audio file is empty"
		return extract.Result{Success: false, Method: "groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, errors.New(msg)
	}

	model := stringOption(job.Options, "model", e.model)
	responseFormat := stringOption(job.Options, "responseFormat", "verbose_json")

	var temperature *float64
	if temp, ok := floatOption(job.Options, "temperature"); ok {
		temperature = &temp
	}
	payload, err := e.client.Transcribe(ctx, filepath.Base(job.LocalPath), b, transcribe.Options{
		Model:          model,
		Language:       stringOption(job.Options, "language", ""),
		Prompt:         stringOption(job.Options, "prompt", ""),
		Temperature:    temperature,
		ResponseFormat: responseFormat,
	})
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, Method: "groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	text := strings.TrimSpace(payload.Text)
	if boolOption(job.Options, "timestamps", false) && len(payload.Segments) > 0 {
		text = formatTimestampedTranscript(payload.Segments)
	}
	if text == "" {
		msg := "groq transcription returned empty transcript"
		return extract.Result{Success: false, Method: "groq", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, errors.New(msg)
	}

	words, chars := extract.BuildCounts(text)
	meta := map[string]string{}
	if payload.Language != "" {
		meta["language"] = payload.Language
	}
	if payload.Duration > 0 {
		meta["durationSeconds"] = strconv.FormatFloat(payload.Duration, 'f', 3, 64)
	}
	meta["model"] = model

	return extract.Result{Success: true, Text: text, Method: "groq", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: words, CharCount: chars}, nil
}

func formatTimestampedTranscript(segments []transcribe.Segment) string {
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		t := strings.TrimSpace(seg.Text)
		if t == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", formatTimecode(seg.Start), t))
	}
	return strings.Join(parts, "\n\n")
}

func formatTimecode(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int(seconds + 0.5)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func stringOption(options map[string]any, key, fallback string) string {
	if options == nil {
		return fallback
	}
	v, ok := options[key]
	if !ok {
		return fallback
	}
	s, ok := v.(string)
	if !ok {
		return fallback
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

func floatOption(options map[string]any, key string) (float64, bool) {
	if options == nil {
		return 0, false
	}
	v, ok := options[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func boolOption(options map[string]any, key string, fallback bool) bool {
	if options == nil {
		return fallback
	}
	v, ok := options[key]
	if !ok {
		return fallback
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}
