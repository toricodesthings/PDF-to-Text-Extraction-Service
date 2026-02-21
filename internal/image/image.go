package image

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/toricodesthings/file-processing-service/internal/ocr"
	"github.com/toricodesthings/file-processing-service/internal/types"
	"github.com/toricodesthings/file-processing-service/internal/vision"
)

// Supported image extensions (matched case-insensitively).
var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".tiff": true, ".tif": true,
	".avif": true, ".svg": true,
}

// cleanOCRText applies light-touch cleaning to raw OCR output:
//   - Strips zero-width / invisible unicode characters
//   - Removes standalone image-filename lines
//   - Normalises line endings and collapses excessive blank lines
var (
	zeroWidthChars     = regexp.MustCompile("[\u200B-\u200D\uFEFF\u00AD\u2060]")
	standaloneImgName  = regexp.MustCompile(`(?mi)^[\w-]*(?:img|image|figure|fig|photo|pic)[\w-]*\.(jpeg|jpg|png|gif|webp|svg|bmp|tiff?)[ \t]*$`)
	standaloneFileName = regexp.MustCompile(`(?mi)^[\w-]+\.(jpeg|jpg|png|gif|webp|svg|bmp|tiff?)[ \t]*$`)
	markdownImageRef   = regexp.MustCompile(`(?m)!\[[^\]]*\]\([^)]*\)`)
	markdownLinkRef    = regexp.MustCompile(`(?m)\[[^\]]*\]\([^)]*\.(jpeg|jpg|png|gif|webp|svg|bmp|tiff?)\)`)
	excessiveNewlines  = regexp.MustCompile(`\n{4,}`)
	trailingSpaces     = regexp.MustCompile(`(?m)[ \t]+$`)
)

func cleanOCRText(text string) string {
	if text == "" {
		return ""
	}

	text = zeroWidthChars.ReplaceAllString(text, "")
	text = markdownImageRef.ReplaceAllString(text, "")
	text = markdownLinkRef.ReplaceAllString(text, "")
	text = standaloneImgName.ReplaceAllString(text, "")
	text = standaloneFileName.ReplaceAllString(text, "")

	// Normalise line endings
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	text = trailingSpaces.ReplaceAllString(text, "")
	text = excessiveNewlines.ReplaceAllString(text, "\n\n\n")

	return strings.TrimSpace(text)
}

// isOCRMeaningful checks whether OCR output contains actual readable text
// rather than just symbols, emoji, or OCR artifacts from non-text images.
// Returns false for garbage output like lone emoji, stray punctuation, etc.
func isOCRMeaningful(text string) bool {
	if text == "" {
		return false
	}

	// Count actual alphanumeric / letter characters vs total runes.
	var letters, total int
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		}
	}

	// Fewer than 3 letter/digit characters is almost certainly not real text.
	if letters < 3 {
		return false
	}

	// If less than 30% of non-space characters are letters/digits,
	// it's likely OCR artifacts (symbols, emoji, markdown remnants).
	if total > 0 && float64(letters)/float64(total) < 0.30 {
		return false
	}

	return true
}

// combineOCRPages joins OCR page markdown into a single string.
func combineOCRPages(ocrResp ocr.OCRResponse) string {
	pageSep := "\n\n-----\n\n"
	var parts []string
	for _, p := range ocrResp.Pages {
		md := strings.TrimSpace(p.Markdown)
		if md == "" || md == "." {
			continue
		}
		parts = append(parts, md)
	}
	return strings.Join(parts, pageSep)
}

// ProcessImage classifies an image via a cheap vision model (OpenRouter) and
// routes to the appropriate extraction method:
//
//   - contentType "text"  → Mistral OCR (handwriting, documents, screenshots, …)
//   - contentType "visual"→ vision description only (photos, artwork, …)
//   - contentType "mixed" → OCR + vision description (diagrams, charts, …)
//
// If the vision classifier is unavailable, we fall back to OCR-only (current behaviour).
func ProcessImage(ctx context.Context, imageURL, ocrModel, visionModel string, visionTimeout time.Duration) (types.ImageExtractionResult, error) {
	// ── Validate ─────────────────────────────────────────────────────────────
	if strings.TrimSpace(imageURL) == "" {
		msg := "imageUrl required"
		return types.ImageExtractionResult{Error: &msg}, errors.New(msg)
	}

	lower := strings.ToLower(imageURL)
	isDataURI := strings.HasPrefix(lower, "data:")
	if !isDataURI && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		msg := "imageUrl must be a valid HTTP/HTTPS URL or data URI"
		return types.ImageExtractionResult{Error: &msg}, errors.New(msg)
	}

	// Reject PDFs — those go through the PDF pipeline (not applicable to data URIs)
	if !isDataURI && (strings.HasSuffix(lower, ".pdf") || strings.Contains(lower, ".pdf?")) {
		msg := "PDF extraction is handled by the PDF service, not the image endpoint"
		return types.ImageExtractionResult{Error: &msg}, errors.New(msg)
	}

	if ocrModel == "" {
		ocrModel = "mistral-ocr-latest"
	}

	// ── Step 1: Vision classification (cheap, ~$0.0001) ──────────────────────
	visionResult, visionErr := vision.RunVisionClassification(ctx, imageURL, visionModel, visionTimeout)
	if visionErr != nil {
		// Vision unavailable — fall back to OCR-only (preserves current behaviour)
		fmt.Printf("[image] vision classification failed, falling back to OCR-only: %v\n", visionErr)
		return processOCROnly(ctx, imageURL, ocrModel)
	}

	// ── Step 2: Route based on content type ──────────────────────────────────
	switch visionResult.ContentType {
	case "text":
		// Text-heavy content (handwriting, docs, screenshots, whiteboards)
		// → OCR provides the primary text; vision description is supplementary
		ocrResult, err := runOCR(ctx, imageURL, ocrModel)
		if err != nil || !isOCRMeaningful(ocrResult) {
			// OCR failed or produced garbage — fall back to vision description
			if err != nil {
				fmt.Printf("[image] OCR failed for text content, using vision description: %v\n", err)
			} else {
				fmt.Printf("[image] OCR produced non-meaningful output for text content, using vision description\n")
			}
			return types.ImageExtractionResult{
				Success:     true,
				Text:        visionResult.Description,
				Method:      "vision",
				ImageType:   visionResult.ImageType,
				Description: visionResult.Description,
			}, nil
		}

		return types.ImageExtractionResult{
			Success:     true,
			Text:        ocrResult,
			Method:      "ocr",
			ImageType:   visionResult.ImageType,
			Description: visionResult.Description,
		}, nil

	case "mixed":
		// Significant text AND visual content (diagrams, charts, infographics)
		// → OCR for text extraction + vision description for visual context
		ocrResult, err := runOCR(ctx, imageURL, ocrModel)
		if err != nil || !isOCRMeaningful(ocrResult) {
			if err != nil {
				fmt.Printf("[image] OCR failed for mixed content, using vision description: %v\n", err)
			} else {
				fmt.Printf("[image] OCR produced non-meaningful output for mixed content, using vision description\n")
			}
			return types.ImageExtractionResult{
				Success:     true,
				Text:        visionResult.Description,
				Method:      "vision",
				ImageType:   visionResult.ImageType,
				Description: visionResult.Description,
			}, nil
		}

		return types.ImageExtractionResult{
			Success:     true,
			Text:        ocrResult,
			Method:      "ocr+vision",
			ImageType:   visionResult.ImageType,
			Description: visionResult.Description,
		}, nil

	default:
		// "visual" or unknown — photo, artwork, no meaningful text
		// → vision description IS the primary content
		return types.ImageExtractionResult{
			Success:     true,
			Text:        visionResult.Description,
			Method:      "vision",
			ImageType:   visionResult.ImageType,
			Description: visionResult.Description,
		}, nil
	}
}

// runOCR calls Mistral OCR and returns cleaned text.
func runOCR(ctx context.Context, imageURL, model string) (string, error) {
	ocrResp, err := ocr.RunMistralImageOCR(ctx, imageURL, model)
	if err != nil {
		return "", err
	}

	if len(ocrResp.Pages) == 0 {
		return "", errors.New("OCR returned no pages")
	}

	raw := combineOCRPages(ocrResp)
	cleaned := cleanOCRText(raw)
	if cleaned == "" {
		return "", errors.New("OCR produced empty text")
	}

	return cleaned, nil
}

// processOCROnly is the fallback path when vision is unavailable.
// Applies the same quality gate as the vision-routed branches:
// if OCR produces garbage (emoji, stray symbols, etc.) we fail
// explicitly rather than returning meaningless text.
func processOCROnly(ctx context.Context, imageURL, model string) (types.ImageExtractionResult, error) {
	ocrText, err := runOCR(ctx, imageURL, model)
	if err != nil {
		msg := sanitiseOCRError(err)
		return types.ImageExtractionResult{Error: &msg}, err
	}

	if !isOCRMeaningful(ocrText) {
		msg := "image contains no extractable text"
		fmt.Printf("[image] OCR-only fallback produced non-meaningful output, failing extraction\n")
		return types.ImageExtractionResult{
			Success: false,
			Method:  "ocr",
			Error:   &msg,
		}, errors.New(msg)
	}

	return types.ImageExtractionResult{
		Success: true,
		Text:    ocrText,
		Method:  "ocr",
	}, nil
}

// sanitiseOCRError produces a user-facing error message from OCR errors.
func sanitiseOCRError(err error) string {
	msg := err.Error()

	switch {
	case strings.Contains(msg, "404") || strings.Contains(msg, "not found"):
		return "Image URL not accessible (404)"
	case strings.Contains(msg, "403") || strings.Contains(msg, "forbidden"):
		return "Access denied to image URL"
	case strings.Contains(msg, "timeout"):
		return "Request timeout — try again later"
	case strings.Contains(msg, "network") || strings.Contains(msg, "ECONNREFUSED"):
		return "Network error — check connectivity"
	}

	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}
