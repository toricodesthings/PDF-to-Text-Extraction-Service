package extractor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ExtractorConfig struct {
	PDFInfoTimeout      time.Duration
	PDFToTextTimeout    time.Duration
	PDFToTextAllTimeout time.Duration
}

// Sensible defaults if you pass zeros.
func (c ExtractorConfig) withDefaults() ExtractorConfig {
	out := c
	if out.PDFInfoTimeout <= 0 {
		out.PDFInfoTimeout = 3 * time.Second
	}
	if out.PDFToTextTimeout <= 0 {
		out.PDFToTextTimeout = 10 * time.Second
	}
	if out.PDFToTextAllTimeout <= 0 {
		out.PDFToTextAllTimeout = 30 * time.Second
	}
	return out
}

type PDFInfo struct {
	Pages     int
	Encrypted bool
	Raw       string // full pdfinfo stdout (for debugging if needed)
}

var (
	pageCountRegex = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)
	encryptedRegex = regexp.MustCompile(`(?mi)^Encrypted:\s+yes\s*$`)
)

// GetPDFInfo runs pdfinfo once and extracts page count + encryption flag.
func GetPDFInfo(ctx context.Context, pdfPath string, cfg ExtractorConfig) (PDFInfo, error) {
	cfg = cfg.withDefaults()

	ctx, cancel := context.WithTimeout(ctx, cfg.PDFInfoTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pdfinfo", pdfPath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return PDFInfo{}, classifyPopplerErr("pdfinfo", err, ctx, stderr.String())
	}

	out := stdout.String()

	pages, err := parsePages(out)
	if err != nil {
		return PDFInfo{}, err
	}

	info := PDFInfo{
		Pages:     pages,
		Encrypted: encryptedRegex.MatchString(out),
		Raw:       out,
	}
	return info, nil
}

// PageCount extracts total pages using pdfinfo (compat wrapper).
func PageCount(ctx context.Context, pdfPath string, cfg ExtractorConfig) (int, error) {
	info, err := GetPDFInfo(ctx, pdfPath, cfg)
	if err != nil {
		return 0, err
	}
	return info.Pages, nil
}

// ValidatePDF performs basic validation using pdfinfo (single call).
func ValidatePDF(ctx context.Context, pdfPath string, cfg ExtractorConfig) error {
	_, err := GetPDFInfo(ctx, pdfPath, cfg)
	return err
}

// TextForPage extracts text for one page using pdftotext.
// Output is capped to maxPerPageBytes to avoid OOM.
func TextForPage(ctx context.Context, pdfPath string, page int, cfg ExtractorConfig) (string, error) {
	cfg = cfg.withDefaults()

	if page < 1 {
		return "", fmt.Errorf("invalid page number: %d (must be >= 1)", page)
	}

	// Cap output to 10 MiB per page
	const maxPerPageBytes = 10<<20 + 1

	ctx, cancel := context.WithTimeout(ctx, cfg.PDFToTextTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"pdftotext",
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-layout",
		"-nopgbrk",
		"-enc", "UTF-8",
		pdfPath,
		"-",
	)

	text, stderrStr, err := runCommandCaptureLimited(ctx, cmd, maxPerPageBytes)
	if err != nil {
		// If stderr has something meaningful, prefer it
		return "", classifyPdftotextErr(err, ctx, stderrStr, page)
	}

	if len(text) > 10<<20 {
		return "", fmt.Errorf("extracted text too large: %d bytes", len(text))
	}
	return text, nil
}

// ExtractAllPages extracts text for whole PDF using pdftotext.
// Output is capped to maxAllBytes to avoid OOM.
func ExtractAllPages(ctx context.Context, pdfPath string, cfg ExtractorConfig) (string, error) {
	cfg = cfg.withDefaults()

	// Cap output to 50 MiB total
	const maxAllBytes = 50<<20 + 1

	ctx, cancel := context.WithTimeout(ctx, cfg.PDFToTextAllTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"pdftotext",
		"-layout",
		"-nopgbrk",
		"-enc", "UTF-8",
		pdfPath,
		"-",
	)

	text, stderrStr, err := runCommandCaptureLimited(ctx, cmd, maxAllBytes)
	if err != nil {
		return "", classifyPdftotextErr(err, ctx, stderrStr, 0)
	}

	if len(text) > 50<<20 {
		return "", fmt.Errorf("extracted text too large: %d bytes", len(text))
	}
	return text, nil
}

// --- internals ---

func parsePages(pdfinfoOut string) (int, error) {
	// First attempt: regex
	matches := pageCountRegex.FindStringSubmatch(pdfinfoOut)
	if len(matches) == 2 {
		n, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, fmt.Errorf("pdfinfo: invalid page count: %w", err)
		}
		return validatePages(n)
	}

	// Fallback: scan lines to handle formatting variations
	sc := bufio.NewScanner(strings.NewReader(pdfinfoOut))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Some poppler builds use "Pages:" exactly, but be defensive anyway
		if strings.HasPrefix(strings.ToLower(line), "pages:") {
			rest := strings.TrimSpace(line[len("Pages:"):])
			rest = strings.Fields(rest)[0]
			n, err := strconv.Atoi(rest)
			if err != nil {
				return 0, fmt.Errorf("pdfinfo: invalid page count: %w", err)
			}
			return validatePages(n)
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("pdfinfo: scan failed: %w", err)
	}

	return 0, fmt.Errorf("pdfinfo: pages field not found in output")
}

func validatePages(count int) (int, error) {
	if count <= 0 || count > 50000 {
		return 0, fmt.Errorf("pdfinfo: unreasonable page count: %d", count)
	}
	return count, nil
}

// runCommandCaptureLimited runs cmd and captures stdout up to maxBytes (inclusive of sentinel).
// It captures stderr fully (usually small) for error reporting.
func runCommandCaptureLimited(ctx context.Context, cmd *exec.Cmd, maxBytes int64) (stdoutText string, stderrText string, err error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("stdout pipe: %w", err)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start: %w", err)
	}

	// Read stdout with a hard cap so a cursed PDF can’t OOM your container.
	lr := io.LimitReader(stdoutPipe, maxBytes)
	outBytes, readErr := io.ReadAll(lr)

	waitErr := cmd.Wait()
	stderrStr := strings.TrimSpace(stderr.String())

	// If read failed, prefer that
	if readErr != nil {
		_ = cmd.Process.Kill()
		return "", stderrStr, fmt.Errorf("read stdout: %w", readErr)
	}

	// If we hit the cap, treat as error (caller enforces exact limits)
	if int64(len(outBytes)) >= maxBytes {
		return "", stderrStr, fmt.Errorf("output exceeds limit")
	}

	if waitErr != nil {
		return "", stderrStr, waitErr
	}

	return string(outBytes), stderrStr, nil
}

// isHelpOrUsageOutput returns true when stderr looks like a poppler
// usage / help dump rather than an actual processing error.
func isHelpOrUsageOutput(stderr string) bool {
	return strings.Contains(stderr, "version ") && strings.Contains(stderr, "Usage:")
}

func classifyPopplerErr(tool string, err error, ctx context.Context, stderr string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timeout: %w", tool, ctx.Err())
	}
	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		// If poppler printed its help/usage text (bad args, etc.) don't
		// match on keywords that appear in the help descriptions.
		if isHelpOrUsageOutput(stderr) {
			logPopplerErr(tool, stderr, 0)
			return fmt.Errorf("%s failed (bad invocation): %s", tool, truncate(stderr, 200))
		}

		// Map common poppler error messages (exact error strings, not
		// generic words that also appear in --help text).
		if containsAny(stderr,
			"Incorrect password",
			"Command Line Error: Incorrect password",
		) {
			logPopplerErr(tool, stderr, 0)
			return fmt.Errorf("PDF is password protected")
		}
		if containsAny(stderr,
			"PDF file is damaged",
			"Syntax Error",
			"Couldn't find trailer dictionary",
			"May not be a PDF file",
		) {
			logPopplerErr(tool, stderr, 0)
			return fmt.Errorf("PDF appears to be damaged or invalid")
		}
		return fmt.Errorf("%s failed: %s", tool, stderr)
	}
	return fmt.Errorf("%s failed: %w", tool, err)
}

func classifyPdftotextErr(err error, ctx context.Context, stderr string, page int) error {
	if ctx.Err() == context.DeadlineExceeded {
		if page > 0 {
			return fmt.Errorf("pdftotext timeout on page %d", page)
		}
		return fmt.Errorf("pdftotext timeout")
	}

	// Our own limit error
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("pdftotext canceled")
	}

	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		// Guard against help/usage text being misclassified.
		if isHelpOrUsageOutput(stderr) {
			logPopplerErr("pdftotext", stderr, page)
			if page > 0 {
				return fmt.Errorf("pdftotext page %d failed (bad invocation)", page)
			}
			return fmt.Errorf("pdftotext failed (bad invocation)")
		}

		if containsAny(stderr, "Incorrect password", "Command Line Error: Incorrect password") {
			logPopplerErr("pdftotext", stderr, page)
			return fmt.Errorf("PDF is password protected")
		}
		if containsAny(stderr, "PDF file is damaged", "Syntax Error", "Couldn't find trailer dictionary", "May not be a PDF file") {
			logPopplerErr("pdftotext", stderr, page)
			return fmt.Errorf("PDF file is damaged or corrupted")
		}
		if strings.Contains(stderr, "I/O Error") && strings.Contains(stderr, "Couldn't open file") {
			logPopplerErr("pdftotext", stderr, page)
			return fmt.Errorf("unable to open PDF")
		}
		if page > 0 {
			return fmt.Errorf("pdftotext page %d failed: %s", page, stderr)
		}
		return fmt.Errorf("pdftotext failed: %s", stderr)
	}

	// If stdout exceeded limit, give a clearer error
	if err != nil && strings.Contains(err.Error(), "output exceeds limit") {
		if page > 0 {
			return fmt.Errorf("extracted text too large on page %d", page)
		}
		return fmt.Errorf("extracted text too large")
	}

	if page > 0 {
		return fmt.Errorf("pdftotext page %d failed: %w", page, err)
	}
	return fmt.Errorf("pdftotext failed: %w", err)
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func logPopplerErr(tool, stderr string, page int) {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		return
	}
	msg = truncate(msg, 500)
	if page > 0 {
		fmt.Fprintf(os.Stderr, "%s error (page %d): %s\n", tool, page, msg)
		return
	}
	fmt.Fprintf(os.Stderr, "%s error: %s\n", tool, msg)
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
