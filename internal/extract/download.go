package extract

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	if err := validateDownloadURL(url); err != nil {
		return DownloadedFile{}, err
	}

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

// SaveBodyToTemp writes an io.Reader (e.g. http.Request.Body) to a temp file,
// sniffs the MIME type, and returns a DownloadedFile identical to DownloadToTemp.
// This avoids a network round-trip when the Worker streams the R2 object directly.
func SaveBodyToTemp(body io.Reader, fileName string, maxBytes int64) (DownloadedFile, error) {
	tmpDir, err := os.MkdirTemp("", "fileproc-*")
	if err != nil {
		return DownloadedFile{}, fmt.Errorf("temp dir: %w", err)
	}

	safeName := strings.TrimSpace(fileName)
	if safeName == "" {
		safeName = "input.bin"
	}
	outPath := filepath.Join(tmpDir, filepath.Base(safeName))

	f, err := os.Create(outPath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return DownloadedFile{}, fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	lr := &io.LimitedReader{R: body, N: maxBytes + 1}
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
	return DownloadedFile{
		TempDir:  tmpDir,
		Path:     outPath,
		MIMEType: mt,
		Size:     n,
	}, nil
}

func validateDownloadURL(rawURL string) error {
	allowPrivate := allowPrivateDownloadURLs()

	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return fmt.Errorf("invalid download URL")
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return fmt.Errorf("download URL host is required")
	}

	isLocalName := host == "localhost" || strings.HasSuffix(host, ".localhost")
	isPrivateIP := false

	ip := net.ParseIP(host)
	if ip != nil {
		isPrivateIP = isPrivateOrLocalIP(ip)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "https":
		// Allowed to continue; host validation below still applies.
	case "http":
		if !(allowPrivate && (isLocalName || isPrivateIP)) {
			return fmt.Errorf("download URL must use https")
		}
	default:
		return fmt.Errorf("download URL must use https")
	}

	if isLocalName || isPrivateIP {
		if allowPrivate {
			return nil
		}
		return fmt.Errorf("download URL host is not allowed")
	}

	return nil
}

func allowPrivateDownloadURLs() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("ALLOW_PRIVATE_DOWNLOAD_URLS")))
	return v == "1" || v == "true" || v == "yes"
}

func isPrivateOrLocalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}

	// RFC6598 carrier-grade NAT range: 100.64.0.0/10
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
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
