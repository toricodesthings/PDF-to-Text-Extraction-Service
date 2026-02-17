package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

type OCRPage struct {
	Index    int    `json:"index"`
	Markdown string `json:"markdown"`
}

type OCRResponse struct {
	Pages     []OCRPage `json:"pages"`
	Model     string    `json:"model"`
	UsageInfo UsageInfo `json:"usage_info"`
}

type UsageInfo struct {
	PagesProcessed int  `json:"pages_processed"`
	DocSizeBytes   *int `json:"doc_size_bytes"`
}

type mistralErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

const (
	mistralAPIURL  = "https://api.mistral.ai/v1/ocr"
	maxRetries     = 2
	retryDelay     = 2 * time.Second
	requestTimeout = 120 * time.Second
)

func RunMistralOCR(ctx context.Context, presignedURL string, model string, pages0 []int, extractHeader, extractFooter bool) (OCRResponse, error) {
	key := os.Getenv("MISTRAL_API_KEY")
	if key == "" {
		return OCRResponse{}, fmt.Errorf("MISTRAL_API_KEY not configured")
	}

	if presignedURL == "" {
		return OCRResponse{}, fmt.Errorf("presigned URL required")
	}
	if model == "" {
		model = "mistral-ocr-latest" // Also: mistral-ocr-2512
	}

	// Normalize pages (0-indexed, sorted, unique)
	if len(pages0) > 0 {
		sort.Ints(pages0)
		pages0 = uniqueInts(pages0)

		for _, p := range pages0 {
			if p < 0 || p > 10000 {
				return OCRResponse{}, fmt.Errorf("invalid page: %d", p)
			}
		}
	}

	// Build request
	body := map[string]any{
		"model": model,
		"document": map[string]any{
			"type":         "document_url",
			"document_url": presignedURL,
		},
	}

	// Optional params (only add if non-default)
	if len(pages0) > 0 {
		body["pages"] = pages0
	}
	if extractHeader {
		body["extract_header"] = true
	}
	if extractFooter {
		body["extract_footer"] = true
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return OCRResponse{}, fmt.Errorf("marshal: %w", err)
	}

	return withConcurrencyLimit(ctx, func() (OCRResponse, error) {
		// Retry logic
		var lastErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return OCRResponse{}, ctx.Err()
				case <-time.After(retryDelay * time.Duration(attempt)):
				}
			}

			result, err := executeOCRRequest(ctx, key, bodyBytes)
			if err == nil {
				return result, nil
			}

			lastErr = err

			// Don't retry client errors (4xx)
			if isClientError(err) {
				break
			}
		}

		return OCRResponse{}, fmt.Errorf("OCR failed after %d attempts: %w", maxRetries+1, lastErr)
	})
}

func executeOCRRequest(ctx context.Context, apiKey string, bodyBytes []byte) (OCRResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", mistralAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return OCRResponse{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fileproc/1.0")

	client := &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return OCRResponse{}, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OCRResponse{}, parseErrorResponse(resp)
	}

	// Parse response (limit to 100MB)
	var result OCRResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 100<<20))
	if err := decoder.Decode(&result); err != nil {
		return OCRResponse{}, fmt.Errorf("decode: %w", err)
	}

	// Validate
	if len(result.Pages) == 0 {
		return OCRResponse{}, fmt.Errorf("OCR returned no pages")
	}

	for i, page := range result.Pages {
		if page.Index < 0 {
			return OCRResponse{}, fmt.Errorf("invalid page index at %d: %d", i, page.Index)
		}
		if len(page.Markdown) > 10<<20 {
			return OCRResponse{}, fmt.Errorf("page %d markdown too large: %dMB", page.Index, len(page.Markdown)/(1<<20))
		}
	}

	return result, nil
}

func parseErrorResponse(resp *http.Response) error {
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var errResp mistralErrorResponse
	if json.Unmarshal(bodyBytes, &errResp) == nil && errResp.Error.Message != "" {
		return &OCRError{
			StatusCode: resp.StatusCode,
			Message:    errResp.Error.Message,
			Type:       errResp.Error.Type,
		}
	}

	return &OCRError{
		StatusCode: resp.StatusCode,
		Message:    string(bodyBytes),
		Type:       "unknown",
	}
}

type OCRError struct {
	StatusCode int
	Message    string
	Type       string
}

func (e *OCRError) Error() string {
	return fmt.Sprintf("mistral OCR %d (%s): %s", e.StatusCode, e.Type, e.Message)
}

func isClientError(err error) bool {
	if ocrErr, ok := err.(*OCRError); ok {
		return ocrErr.StatusCode >= 400 && ocrErr.StatusCode < 500
	}
	return false
}

func uniqueInts(xs []int) []int {
	if len(xs) == 0 {
		return xs
	}

	seen := make(map[int]bool, len(xs))
	out := make([]int, 0, len(xs))

	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}

	return out
}

// RunMistralImageOCR calls the Mistral OCR API with an image URL using the
// "image_url" document type (as opposed to "document_url" for PDFs). The image
// is not downloaded — the URL is sent directly to Mistral.
func RunMistralImageOCR(ctx context.Context, imageURL string, model string) (OCRResponse, error) {
	key := os.Getenv("MISTRAL_API_KEY")
	if key == "" {
		return OCRResponse{}, fmt.Errorf("MISTRAL_API_KEY not configured")
	}

	if imageURL == "" {
		return OCRResponse{}, fmt.Errorf("image URL required")
	}
	if model == "" {
		model = "mistral-ocr-latest"
	}

	body := map[string]any{
		"model": model,
		"document": map[string]any{
			"type":      "image_url",
			"image_url": imageURL,
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return OCRResponse{}, fmt.Errorf("marshal: %w", err)
	}

	return withConcurrencyLimit(ctx, func() (OCRResponse, error) {
		// Retry logic (same as PDF variant)
		var lastErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return OCRResponse{}, ctx.Err()
				case <-time.After(retryDelay * time.Duration(attempt)):
				}
			}

			result, err := executeOCRRequest(ctx, key, bodyBytes)
			if err == nil {
				return result, nil
			}

			lastErr = err

			if isClientError(err) {
				break
			}
		}

		return OCRResponse{}, fmt.Errorf("image OCR failed after %d attempts: %w", maxRetries+1, lastErr)
	})
}
