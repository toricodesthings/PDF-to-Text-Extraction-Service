package extract

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
)

type DownloadedFile struct {
	TempDir  string
	Path     string
	MIMEType string
	Size     int64
}

func (d DownloadedFile) Cleanup() {
	if d.TempDir != "" {
		_ = os.RemoveAll(d.TempDir)
	}
}

func DownloadToTemp(ctx context.Context, url string, fileName string, maxBytes int64, timeout time.Duration) (DownloadedFile, error) {
	tmpDir, err := os.MkdirTemp("", "fileproc-*")
	if err != nil {
		return DownloadedFile{}, fmt.Errorf("temp dir: %w", err)
	}

	safeName := strings.TrimSpace(fileName)
	if safeName == "" {
		safeName = "input.bin"
	}
	outPath := filepath.Join(tmpDir, filepath.Base(safeName))

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "fileproc/2.0")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return DownloadedFile{}, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = os.RemoveAll(tmpDir)
		return DownloadedFile{}, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(outPath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return DownloadedFile{}, fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	lr := &io.LimitedReader{R: resp.Body, N: maxBytes + 1}
	n, err := io.Copy(f, lr)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return DownloadedFile{}, fmt.Errorf("write: %w", err)
	}
	if n > maxBytes {
		_ = os.RemoveAll(tmpDir)
		return DownloadedFile{}, fmt.Errorf("file exceeds %dMB limit", maxBytes/(1<<20))
	}

	if err := f.Sync(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return DownloadedFile{}, fmt.Errorf("sync: %w", err)
	}

	mt := sniffMIMEType(outPath)
	if mt == "" {
		mt = strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
		if i := strings.Index(mt, ";"); i > 0 {
			mt = strings.TrimSpace(mt[:i])
		}
	}

	return DownloadedFile{
		TempDir:  tmpDir,
		Path:     outPath,
		MIMEType: mt,
		Size:     n,
	}, nil
}

func sniffMIMEType(path string) string {
	m, err := mimetype.DetectFile(path)
	if err == nil && m != nil {
		return strings.ToLower(strings.TrimSpace(m.String()))
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n <= 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(http.DetectContentType(buf[:n])))
}
