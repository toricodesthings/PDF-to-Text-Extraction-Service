package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/toricodesthings/file-processing-service/internal/config"
	"github.com/toricodesthings/file-processing-service/internal/extract"
	audioextractor "github.com/toricodesthings/file-processing-service/internal/extractors/audio"
	codeextractor "github.com/toricodesthings/file-processing-service/internal/extractors/code"
	ebookextractor "github.com/toricodesthings/file-processing-service/internal/extractors/ebook"
	imageextractor "github.com/toricodesthings/file-processing-service/internal/extractors/image"
	officeextractor "github.com/toricodesthings/file-processing-service/internal/extractors/office"
	opendocumentextractor "github.com/toricodesthings/file-processing-service/internal/extractors/opendocument"
	pdfextractor "github.com/toricodesthings/file-processing-service/internal/extractors/pdf"
	plaintextextractor "github.com/toricodesthings/file-processing-service/internal/extractors/plaintext"
	structuredextractor "github.com/toricodesthings/file-processing-service/internal/extractors/structured"
	videoextractor "github.com/toricodesthings/file-processing-service/internal/extractors/video"
	"github.com/toricodesthings/file-processing-service/internal/hybrid"
	"github.com/toricodesthings/file-processing-service/internal/types"
	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"
)

var (
	cfg config.Config

	requestSem *semaphore.Weighted
	ocrSem     *semaphore.Weighted
	extractRt  *extract.Router
	extractReg *extract.Registry
	hybridProc *hybrid.Processor

	// Per-IP rate limiters
	limiters = &sync.Map{}

	metrics = &serverMetrics{}
)

type serverMetrics struct {
	mu            sync.RWMutex
	totalRequests int64
	activeReqs    int64
}

func (m *serverMetrics) incActive() {
	m.mu.Lock()
	m.activeReqs++
	m.totalRequests++
	m.mu.Unlock()
}
func (m *serverMetrics) decActive() {
	m.mu.Lock()
	m.activeReqs--
	m.mu.Unlock()
}
func (m *serverMetrics) get() (total, active int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalRequests, m.activeReqs
}

func main() {
	cfg = config.Load()
	if err := cfg.Validate(); err != nil {
		panic(err)
	}

	requestSem = semaphore.NewWeighted(cfg.MaxConcurrentRequests)
	ocrSem = semaphore.NewWeighted(cfg.MaxOCRConcurrent)

	processor := hybrid.New(cfg)
	hybridProc = processor
	registry := extract.NewRegistry()
	extractReg = registry

	audioX := audioextractor.New(cfg.GroqAPIKey, cfg.GroqAPIURL, cfg.GroqModel, cfg.MaxAudioBytes, cfg.GroqTimeout)

	// Register extractors — order matters: more-specific first
	registry.Register(pdfextractor.New(processor, cfg.MaxPDFBytes))
	registry.Register(imageextractor.New(cfg.DefaultOCRModel, cfg.DefaultVisionModel, cfg.VisionRequestTimeout, cfg.MaxImageBytes))
	registry.Register(plaintextextractor.New(cfg.MaxCodeFileBytes))
	registry.Register(plaintextextractor.NewHTML(cfg.MaxCodeFileBytes))
	registry.Register(plaintextextractor.NewRTF(cfg.MaxCodeFileBytes))
	registry.Register(structuredextractor.NewCSV(cfg.MaxCodeFileBytes))
	registry.Register(structuredextractor.NewJSON(cfg.MaxCodeFileBytes))
	registry.Register(structuredextractor.NewXML(cfg.MaxCodeFileBytes))
	registry.Register(structuredextractor.NewYAML(cfg.MaxCodeFileBytes))
	registry.Register(codeextractor.NewSource(cfg.MaxCodeFileBytes))
	registry.Register(codeextractor.NewNotebook(cfg.MaxCodeFileBytes))
	registry.Register(codeextractor.NewLaTeX(cfg.MaxCodeFileBytes))
	registry.Register(officeextractor.NewDOCX(cfg.MaxFileBytes))
	registry.Register(officeextractor.NewXLSX(cfg.MaxFileBytes))
	registry.Register(officeextractor.NewPPTX(cfg.MaxFileBytes))
	registry.Register(officeextractor.NewLegacy(cfg.LibreOfficeBinary, cfg.LibreOfficeTimeout, cfg.MaxFileBytes))
	registry.Register(opendocumentextractor.New(cfg.MaxFileBytes))
	registry.Register(ebookextractor.NewEPUB(cfg.MaxFileBytes))
	registry.Register(audioX)
	registry.Register(videoextractor.New(cfg.FFmpegBinary, cfg.FFmpegTimeout, audioX, cfg.MaxVideoBytes))

	extractRt = extract.NewRouter(registry, cfg.MaxFileBytes, cfg.DownloadTimeout)

	mux := http.NewServeMux()

	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/metrics", withInternalAuth(handleMetrics))

	// Universal extraction endpoint — all file types route through here
	mux.HandleFunc("/extract",
		withInternalAuth(
			withRateLimit(
				withMethod("POST",
					withConcurrencyLimit(func(w http.ResponseWriter, r *http.Request) {
						handleUniversalExtract(w, r)
					})))))

	// Low-cost preview endpoint — free extraction paths only
	mux.HandleFunc("/preview",
		withInternalAuth(
			withRateLimit(
				withMethod("POST",
					withConcurrencyLimit(func(w http.ResponseWriter, r *http.Request) {
						handlePreview(w, r)
					})))))

	maxHeaderBytes := 1 << 20
	if cfg.MaxHeaderBytes > 0 {
		maxHeaderBytes = cfg.MaxHeaderBytes
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           withLogging(withRecovery(mux)),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	if strings.TrimSpace(cfg.MistralAPIKey) == "" {
		fmt.Fprintln(os.Stderr, "warning: MISTRAL_API_KEY not set (OCR will fail)")
	}
	if strings.TrimSpace(cfg.OpenRouterAPIKey) == "" {
		fmt.Fprintln(os.Stderr, "warning: OPENROUTER_API_KEY not set (vision classification will fall back to OCR-only)")
	}
	if strings.TrimSpace(cfg.GroqAPIKey) == "" {
		fmt.Fprintln(os.Stderr, "warning: GROQ_API_KEY not set (audio/video transcription will fail)")
	}

	go cleanupRateLimiters()

	fmt.Printf("fileproc listening on %s (max concurrent: %d, OCR: %d)\n",
		srv.Addr, cfg.MaxConcurrentRequests, cfg.MaxOCRConcurrent)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

func cleanupRateLimiters() {
	interval := cfg.CleanupInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		total, active := metrics.get()
		fmt.Printf("[stats] active=%d total=%d goroutines=%d mem=%dMB\n",
			active, total, runtime.NumGoroutine(), m.Alloc/(1<<20))

		limiters = &sync.Map{}
	}
}

// ---------- Handlers ----------

func handleHealth(w http.ResponseWriter, r *http.Request) {
	_, active := metrics.get()
	status := "healthy"
	code := http.StatusOK

	ratio := cfg.HealthDegradeRatio
	if ratio <= 0 || ratio > 1 {
		ratio = 0.9
	}

	if active >= int64(float64(cfg.MaxConcurrentRequests)*ratio) {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]any{
		"status":  status,
		"active":  active,
		"version": "2.0.0",
	})
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	total, active := metrics.get()

	writeJSON(w, http.StatusOK, map[string]any{
		"activeRequests": active,
		"totalRequests":  total,
		"goroutines":     runtime.NumGoroutine(),
		"memAllocMB":     m.Alloc / (1 << 20),
		"memSysMB":       m.Sys / (1 << 20),
	})
}

func handleUniversalExtract(w http.ResponseWriter, r *http.Request) {
	req, err := parseJSON[extract.UniversalExtractRequest](r, cfg.MaxJSONBodyBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", sanitizeError(err))
		return
	}

	if strings.TrimSpace(req.PresignedURL) == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "presignedUrl required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cfg.UniversalExtractTimeout)
	defer cancel()

	res, err := extractRt.Extract(ctx, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, res)
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func handlePreview(w http.ResponseWriter, r *http.Request) {
	req, err := parseJSON[extract.UniversalExtractRequest](r, cfg.MaxJSONBodyBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", sanitizeError(err))
		return
	}

	if strings.TrimSpace(req.PresignedURL) == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "presignedUrl required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cfg.UniversalExtractTimeout)
	defer cancel()

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = "input.bin"
	}

	dl, err := extract.DownloadToTemp(ctx, req.PresignedURL, fileName, cfg.MaxFileBytes, cfg.DownloadTimeout)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": sanitizeError(err)})
		return
	}
	defer dl.Cleanup()

	ext := strings.ToLower(filepath.Ext(fileName))
	extractor, err := extractReg.Resolve(dl.MIMEType, ext)
	if err != nil {
		msg := sanitizeError(err)
		writeJSON(w, http.StatusBadRequest, extract.Result{Success: false, MIMEType: dl.MIMEType, FileType: "unknown", Error: &msg})
		return
	}

	if !isPreviewAllowed(extractor.Name()) {
		msg := "preview unsupported for this file type"
		writeJSON(w, http.StatusBadRequest, extract.Result{Success: false, MIMEType: dl.MIMEType, FileType: extractor.Name(), Error: &msg})
		return
	}

	previewMaxChars := previewMaxCharsOption(req.Options, cfg.DefaultPreviewMaxChars)

	if extractor.Name() == "document/pdf" {
		opts := hybridProc.ApplyDefaults(types.HybridProcessorOptions{})
		if req.Options != nil {
			opts.PreviewMaxPages = intOption(req.Options, "previewMaxPages", opts.PreviewMaxPages)
			opts.PreviewMaxChars = intOption(req.Options, "previewMaxChars", opts.PreviewMaxChars)
			opts.MinWordsThreshold = intOption(req.Options, "minWordsThreshold", opts.MinWordsThreshold)
		}
		prev := hybridProc.ProcessPreview(ctx, dl.Path, opts)
		if prev.Error != nil {
			writeJSON(w, http.StatusBadRequest, extract.Result{Success: false, Method: "preview-text-layer", FileType: "document/pdf", MIMEType: dl.MIMEType, Error: prev.Error})
			return
		}
		text := prev.Text
		if len(text) > previewMaxChars {
			text = text[:previewMaxChars] + "..."
		}
		wcount, ccount := extract.BuildCounts(text)
		meta := map[string]string{
			"needsOcr":       strconv.FormatBool(prev.NeedsOCR),
			"totalPages":     strconv.Itoa(prev.TotalPages),
			"textLayerPages": strconv.Itoa(prev.TextLayerPages),
		}
		writeJSON(w, http.StatusOK, extract.Result{
			Success:   true,
			Text:      text,
			Method:    "preview-text-layer",
			FileType:  "document/pdf",
			MIMEType:  dl.MIMEType,
			Metadata:  meta,
			WordCount: wcount,
			CharCount: ccount,
		})
		return
	}

	job := extract.Job{
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
			msg := sanitizeError(err)
			res.Error = &msg
		}
		res.Success = false
		if res.MIMEType == "" {
			res.MIMEType = dl.MIMEType
		}
		writeJSON(w, http.StatusBadRequest, res)
		return
	}

	if previewMaxChars > 0 && len(res.Text) > previewMaxChars {
		res.Text = res.Text[:previewMaxChars] + "..."
		res.WordCount, res.CharCount = extract.BuildCounts(res.Text)
	}
	res.Success = true
	if res.MIMEType == "" {
		res.MIMEType = dl.MIMEType
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------- Middleware ----------

func withMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method must be "+method)
			return
		}
		next(w, r)
	}
}

func withInternalAuth(next http.HandlerFunc) http.HandlerFunc {
	shared := cfg.InternalSharedSecret
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Internal-Auth")
		if subtle.ConstantTimeCompare([]byte(got), []byte(shared)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "Invalid authentication")
			return
		}
		next(w, r)
	}
}

func withConcurrencyLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := requestSem.Acquire(r.Context(), 1); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "capacity", "Service at capacity")
			return
		}
		defer requestSem.Release(1)

		metrics.incActive()
		defer metrics.decActive()

		next(w, r)
	}
}

func withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		limiter := getRateLimiter(ip)

		if !limiter.Allow() {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limit", "Rate limit exceeded")
			return
		}
		next(w, r)
	}
}

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				fmt.Fprintf(os.Stderr, "panic: %v\n", err)
				writeErr(w, http.StatusInternalServerError, "internal_error", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &wrapWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)

		fmt.Printf("%s %s -> %d (%s)\n",
			r.Method, sanitizeLogString(r.URL.Path), ww.status, time.Since(start))
	})
}

type wrapWriter struct {
	http.ResponseWriter
	status int
}

func (w *wrapWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// ---------- Helpers ----------

func getRateLimiter(ip string) *rate.Limiter {
	if v, ok := limiters.Load(ip); ok {
		return v.(*rate.Limiter)
	}

	every := cfg.RateLimitEvery
	if every <= 0 {
		every = 600 * time.Millisecond // ~100/min
	}
	burst := cfg.RateLimitBurst
	if burst <= 0 {
		burst = 20
	}

	limiter := rate.NewLimiter(rate.Every(every), burst)
	limiters.Store(ip, limiter)
	return limiter
}

func getClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		if idx := strings.Index(ip, ","); idx > 0 {
			return strings.TrimSpace(ip[:idx])
		}
		return strings.TrimSpace(ip)
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}

	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, os.TempDir(), "[tmp]")
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}

func sanitizeLogString(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func isPreviewAllowed(fileType string) bool {
	switch fileType {
	case "document/pdf", "document/docx", "document/xlsx", "document/pptx", "document/opendocument", "document/epub", "document/rtf", "document/html", "text", "structured/csv", "structured/json", "structured/xml", "structured/yaml", "code/source", "code/notebook", "code/latex":
		return true
	default:
		return false
	}
}

func previewMaxCharsOption(options map[string]any, fallback int) int {
	v := intOption(options, "previewMaxChars", fallback)
	if v <= 0 {
		return fallback
	}
	return v
}

func intOption(options map[string]any, key string, fallback int) int {
	if options == nil {
		return fallback
	}
	v, ok := options[key]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i)
		}
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err == nil {
			return i
		}
	}
	return fallback
}

func parseJSON[T any](r *http.Request, limit int64) (T, error) {
	var out T
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&out); err != nil {
		return out, err
	}

	// Ensure there's nothing else after the first JSON value
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return out, fmt.Errorf("unexpected trailing data")
		}
		return out, err
	}

	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"success": false,
		"error":   message,
		"code":    code,
	})
}
