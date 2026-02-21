package image

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/toricodesthings/file-processing-service/internal/extract"
	img "github.com/toricodesthings/file-processing-service/internal/image"
)

type Extractor struct {
	ocrModel      string
	visionModel   string
	visionTimeout time.Duration
	maxBytes      int64
}

func New(ocrModel, visionModel string, visionTimeout time.Duration, maxBytes int64) *Extractor {
	return &Extractor{ocrModel: ocrModel, visionModel: visionModel, visionTimeout: visionTimeout, maxBytes: maxBytes}
}

func (e *Extractor) Name() string { return "image" }

func (e *Extractor) MaxFileSize() int64 { return e.maxBytes }

func (e *Extractor) SupportedTypes() []string {
	return []string{
		"image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/tiff", "image/svg+xml", "image/avif",
	}
}

func (e *Extractor) SupportedExtensions() []string {
	return []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tiff", ".tif", ".svg", ".avif"}
}

func (e *Extractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	imageURL := job.PresignedURL
	if imageURL == "" && job.LocalPath != "" {
		// Binary upload path (R2 binding stream): no presigned URL available.
		// Both Mistral OCR and OpenRouter Vision accept base64 data URIs,
		// so we encode the local file directly — zero presigning overhead.
		data, err := os.ReadFile(job.LocalPath)
		if err != nil {
			msg := fmt.Sprintf("failed to read local image file: %v", err)
			return extract.Result{Success: false, Method: "image", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
		}
		mime := job.MIMEType
		if mime == "" {
			mime = "image/png"
		}
		imageURL = fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data))
	}

	res, err := img.ProcessImage(ctx, imageURL, e.ocrModel, e.visionModel, e.visionTimeout)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, Method: "image", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	words, chars := extract.BuildCounts(res.Text)
	metadata := map[string]string{}
	if res.ImageType != "" {
		metadata["imageType"] = res.ImageType
	}
	if res.Description != "" {
		metadata["description"] = res.Description
	}

	return extract.Result{
		Success:   true,
		Text:      res.Text,
		Method:    res.Method,
		FileType:  e.Name(),
		MIMEType:  job.MIMEType,
		Metadata:  metadata,
		WordCount: words,
		CharCount: chars,
	}, nil
}
