package extract

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type Router struct {
	registry        *Registry
	maxFileBytes    int64
	downloadTimeout time.Duration
}

func NewRouter(registry *Registry, maxFileBytes int64, downloadTimeout time.Duration) *Router {
	return &Router{registry: registry, maxFileBytes: maxFileBytes, downloadTimeout: downloadTimeout}
}

func (r *Router) Extract(ctx context.Context, req UniversalExtractRequest) (Result, error) {
	if strings.TrimSpace(req.PresignedURL) == "" {
		return errResult("presignedUrl required"), fmt.Errorf("presignedUrl required")
	}

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = "input.bin"
	}

	dl, err := DownloadToTemp(ctx, req.PresignedURL, fileName, r.maxFileBytes, r.downloadTimeout)
	if err != nil {
		return errResult(err.Error()), err
	}
	defer dl.Cleanup()

	ext := strings.ToLower(filepath.Ext(fileName))
	extractor, err := r.registry.Resolve(dl.MIMEType, ext)
	if err != nil {
		msg := err.Error()
		return Result{Success: false, MIMEType: dl.MIMEType, FileType: "unknown", Error: &msg}, err
	}

	if max := extractor.MaxFileSize(); max > 0 && dl.Size > max {
		msg := fmt.Sprintf("file exceeds extractor limit (%dMB)", max/(1<<20))
		return Result{Success: false, MIMEType: dl.MIMEType, FileType: extractor.Name(), Error: &msg}, errors.New(msg)
	}

	job := Job{
		PresignedURL: req.PresignedURL,
		LocalPath:    dl.Path,
		FileName:     fileName,
		MIMEType:     dl.MIMEType,
		FileSize:     dl.Size,
		Options:      req.Options,
	}

	res, err := extractor.Extract(ctx, job)
	if err != nil {
		if res.Error == nil {
			msg := err.Error()
			res.Error = &msg
		}
		res.Success = false
		if res.MIMEType == "" {
			res.MIMEType = dl.MIMEType
		}
		return res, err
	}

	res.Success = true
	if res.MIMEType == "" {
		res.MIMEType = dl.MIMEType
	}
	if res.CharCount == 0 && res.Text != "" {
		res.WordCount, res.CharCount = BuildCounts(res.Text)
	}
	return res, nil
}

type UniversalExtractRequest struct {
	PresignedURL string         `json:"presignedUrl"`
	FileName     string         `json:"fileName"`
	Options      map[string]any `json:"options"`
}

func errResult(message string) Result {
	return Result{Success: false, Error: &message}
}
