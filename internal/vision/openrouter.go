package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ── Public types ─────────────────────────────────────────────────────────────

// VisionResult is the structured output returned by the vision model.
type VisionResult struct {
	ContentType string `json:"contentType"` // "text" | "visual" | "mixed"
	ImageType   string `json:"imageType"`   // "handwriting" | "document" | "screenshot" | "whiteboard" | "photo" | "diagram" | "chart" | "artwork" | "meme" | "other"
	Description string `json:"description"` // Detailed description for embedding / search
}

// ── Config ───────────────────────────────────────────────────────────────────

const (
	openRouterAPIURL   = "https://openrouter.ai/api/v1/chat/completions"
	visionMaxRetries   = 1
	visionRetryDelay   = 2 * time.Second
	defaultVisionModel = "google/gemma-3-27b-it"
)

// classificationPrompt asks the model to classify the image and produce a
// description in a single pass.  The structured-output JSON schema enforces
// the shape, so the prompt can be short.
const classificationPrompt = `Analyze this image. Respond ONLY with the requested JSON.

"contentType" rules:
- "text": The image contains readable text as its primary content — handwritten notes, printed documents, screenshots of text, whiteboards, receipts, code, sticky notes, signs. The user likely wants the text itself.
- "visual": The image is primarily visual — photos, artwork, illustrations, product images. Text is absent or incidental (a watermark, a tiny label).
- "mixed": Significant text AND significant visual content — annotated diagrams, charts with data, infographics, labeled maps.

"imageType": Pick the single best label from: handwriting, document, screenshot, whiteboard, photo, diagram, chart, artwork, meme, other.

"description": Describe what the image contains in detail. Be specific: subjects, objects, context, visual style, and note what topics any visible text covers (but do NOT transcribe it). This description will be used for search indexing — be thorough and factual.`

// jsonSchema is the structured-output schema sent to OpenRouter.
var classificationSchema = map[string]any{
	"type": "json_schema",
	"json_schema": map[string]any{
		"name":   "image_classification",
		"strict": true,
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"contentType": map[string]any{
					"type":        "string",
					"description": "Whether the image is primarily text, visual, or mixed content",
					"enum":        []string{"text", "visual", "mixed"},
				},
				"imageType": map[string]any{
					"type":        "string",
					"description": "The best single-word label for the image type",
					"enum":        []string{"handwriting", "document", "screenshot", "whiteboard", "photo", "diagram", "chart", "artwork", "meme", "other"},
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Detailed description of the image for search indexing",
				},
			},
			"required":             []string{"contentType", "imageType", "description"},
			"additionalProperties": false,
		},
	},
}

// ── OpenRouter response types ────────────────────────────────────────────────

// We only parse the fields we actually use.  OpenRouter may return additional
// fields (provider, system_fingerprint, usage, etc.) which we silently ignore.

type chatCompletionResponse struct {
	ID      string                  `json:"id"`
	Choices []chatCompletionChoice  `json:"choices"`
	Error   *openRouterErrorPayload `json:"error,omitempty"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ── Error type ───────────────────────────────────────────────────────────────

type VisionError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *VisionError) Error() string {
	return fmt.Sprintf("openrouter vision %d (%s): %s", e.StatusCode, e.Code, e.Message)
}

func isVisionClientError(err error) bool {
	if ve, ok := err.(*VisionError); ok {
		return ve.StatusCode >= 400 && ve.StatusCode < 500
	}
	return false
}

// ── Public API ───────────────────────────────────────────────────────────────

// RunVisionClassification sends an image URL to a vision model via OpenRouter
// and returns a structured classification + description.
func RunVisionClassification(ctx context.Context, imageURL string, model string, timeout time.Duration) (VisionResult, error) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		return VisionResult{}, fmt.Errorf("OPENROUTER_API_KEY not configured")
	}
	if imageURL == "" {
		return VisionResult{}, fmt.Errorf("image URL required")
	}
	if model == "" {
		model = defaultVisionModel
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// Build the chat completion request body
	body := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "image_url",
						"image_url": map[string]any{
							"url": imageURL,
						},
					},
					{
						"type": "text",
						"text": classificationPrompt,
					},
				},
			},
		},
		"response_format": classificationSchema,
		"temperature":     0.0,
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return VisionResult{}, fmt.Errorf("marshal request: %w", err)
	}

	// Retry loop
	var lastErr error
	for attempt := 0; attempt <= visionMaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return VisionResult{}, ctx.Err()
			case <-time.After(visionRetryDelay * time.Duration(attempt)):
			}
		}

		result, err := executeVisionRequest(ctx, key, bodyBytes, timeout)
		if err == nil {
			return result, nil
		}
		lastErr = err

		// Don't retry client errors (4xx)
		if isVisionClientError(err) {
			break
		}
	}

	return VisionResult{}, fmt.Errorf("vision classification failed after %d attempts: %w", visionMaxRetries+1, lastErr)
}

// ── Internal ─────────────────────────────────────────────────────────────────

func executeVisionRequest(ctx context.Context, apiKey string, bodyBytes []byte, timeout time.Duration) (VisionResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", openRouterAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return VisionResult{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fileproc/1.0")

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return VisionResult{}, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	// Read body (limit to 1MB — vision text responses are small)
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return VisionResult{}, fmt.Errorf("read body: %w", err)
	}

	// Non-2xx → error
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return VisionResult{}, parseVisionError(resp.StatusCode, rawBody)
	}

	// Parse OpenRouter chat completion response
	var completionResp chatCompletionResponse
	if err := json.Unmarshal(rawBody, &completionResp); err != nil {
		return VisionResult{}, fmt.Errorf("decode response: %w", err)
	}

	// Check for inline error (OpenRouter can return 200 with an error object)
	if completionResp.Error != nil && completionResp.Error.Message != "" {
		return VisionResult{}, &VisionError{
			StatusCode: resp.StatusCode,
			Code:       completionResp.Error.Code,
			Message:    completionResp.Error.Message,
		}
	}

	// Extract assistant message content
	if len(completionResp.Choices) == 0 {
		return VisionResult{}, fmt.Errorf("empty choices in response")
	}

	content := strings.TrimSpace(completionResp.Choices[0].Message.Content)
	if content == "" {
		return VisionResult{}, fmt.Errorf("empty content in response")
	}

	// Parse structured JSON from content
	var result VisionResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return VisionResult{}, fmt.Errorf("decode structured output: %w (raw: %.200s)", err, content)
	}

	// Validate required fields
	if result.ContentType == "" {
		result.ContentType = "visual" // safe default: don't accidentally skip OCR
	}
	if result.ImageType == "" {
		result.ImageType = "other"
	}

	return result, nil
}

func parseVisionError(statusCode int, body []byte) error {
	// Try to extract structured error
	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		return &VisionError{
			StatusCode: statusCode,
			Code:       errResp.Error.Code,
			Message:    errResp.Error.Message,
		}
	}

	msg := string(body)
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return &VisionError{
		StatusCode: statusCode,
		Code:       "unknown",
		Message:    msg,
	}
}
