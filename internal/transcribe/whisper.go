package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const (
	defaultGroqURL = "https://api.groq.com/openai/v1/audio/transcriptions"
	defaultModel   = "whisper-large-v3-turbo"
)

var ErrAPIKeyMissing = errors.New("GROQ_API_KEY not set")

type Client struct {
	apiKey  string
	apiURL  string
	timeout time.Duration
}

type Options struct {
	Model          string
	Language       string
	Prompt         string
	Temperature    *float64
	ResponseFormat string
}

type Response struct {
	Text     string    `json:"text"`
	Language string    `json:"language"`
	Duration float64   `json:"duration"`
	Segments []Segment `json:"segments"`
}

type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type APIError struct {
	StatusCode int
	Type       string
	Message    string
}

func (e *APIError) Error() string {
	if strings.TrimSpace(e.Type) != "" {
		return fmt.Sprintf("groq %d (%s): %s", e.StatusCode, e.Type, e.Message)
	}
	return fmt.Sprintf("groq %d: %s", e.StatusCode, e.Message)
}

type groqErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func NewClient(apiKey, apiURL string, timeout time.Duration) *Client {
	if strings.TrimSpace(apiURL) == "" {
		apiURL = defaultGroqURL
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Client{apiKey: apiKey, apiURL: apiURL, timeout: timeout}
}

func (c *Client) Transcribe(ctx context.Context, fileName string, fileContent []byte, opts Options) (Response, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return Response{}, ErrAPIKeyMissing
	}
	if len(fileContent) == 0 {
		return Response{}, errors.New("audio file is empty")
	}
	if strings.TrimSpace(fileName) == "" {
		fileName = "audio.bin"
	}

	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = defaultModel
	}
	responseFormat := strings.TrimSpace(opts.ResponseFormat)
	if responseFormat == "" {
		responseFormat = "verbose_json"
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	fw, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return Response{}, err
	}
	_, _ = fw.Write(fileContent)

	_ = writer.WriteField("model", model)
	_ = writer.WriteField("response_format", responseFormat)
	if strings.TrimSpace(opts.Language) != "" {
		_ = writer.WriteField("language", strings.TrimSpace(opts.Language))
	}
	if strings.TrimSpace(opts.Prompt) != "" {
		_ = writer.WriteField("prompt", strings.TrimSpace(opts.Prompt))
	}
	if opts.Temperature != nil {
		_ = writer.WriteField("temperature", fmt.Sprintf("%g", *opts.Temperature))
	}
	_ = writer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, body)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", "fileproc/2.0")

	httpClient := &http.Client{Timeout: c.timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return Response{}, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, parseAPIError(resp.StatusCode, bodyBytes)
	}

	var out Response
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return Response{}, err
	}
	return out, nil
}

func parseAPIError(statusCode int, body []byte) error {
	var parsed groqErrorResponse
	if err := json.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return &APIError{StatusCode: statusCode, Type: strings.TrimSpace(parsed.Error.Type), Message: parsed.Error.Message}
	}

	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(statusCode)
	}
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return &APIError{StatusCode: statusCode, Message: msg}
}
