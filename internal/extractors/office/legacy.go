package office

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type LegacyExtractor struct {
	binary  string
	timeout time.Duration
	maxSize int64
}

func NewLegacy(binary string, timeout time.Duration, maxSize int64) *LegacyExtractor {
	if strings.TrimSpace(binary) == "" {
		binary = "soffice"
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &LegacyExtractor{binary: binary, timeout: timeout, maxSize: maxSize}
}

func (e *LegacyExtractor) Name() string       { return "document/legacy-office" }
func (e *LegacyExtractor) MaxFileSize() int64 { return e.maxSize }
func (e *LegacyExtractor) SupportedTypes() []string {
	return []string{"application/msword", "application/vnd.ms-excel", "application/vnd.ms-powerpoint"}
}
func (e *LegacyExtractor) SupportedExtensions() []string { return []string{".doc", ".xls", ".ppt"} }

func (e *LegacyExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	localCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	outDir := filepath.Dir(job.LocalPath)
	cmd := exec.CommandContext(localCtx, e.binary, "--headless", "--convert-to", "txt:Text", "--outdir", outDir, job.LocalPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := fmt.Sprintf("libreoffice conversion failed: %v: %s", err, strings.TrimSpace(string(out)))
		return extract.Result{Success: false, Method: "libreoffice", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	txtPath := strings.TrimSuffix(job.LocalPath, filepath.Ext(job.LocalPath)) + ".txt"
	b, err := os.ReadFile(txtPath)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, Method: "libreoffice", FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	text := strings.TrimSpace(string(b))
	words, chars := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "libreoffice", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: words, CharCount: chars}, nil
}
