package hybrid

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/toricodesthings/PDF-to-Text-Extraction-Service/internal/config"
	"github.com/toricodesthings/PDF-to-Text-Extraction-Service/internal/extractor"
	"github.com/toricodesthings/PDF-to-Text-Extraction-Service/internal/format"
	"github.com/toricodesthings/PDF-to-Text-Extraction-Service/internal/ocr"
	"github.com/toricodesthings/PDF-to-Text-Extraction-Service/internal/quality"
	"github.com/toricodesthings/PDF-to-Text-Extraction-Service/internal/types"
	"golang.org/x/sync/semaphore"
)

type Processor struct {
	cfg config.Config

	// Extractor config (your PageCount signature requires this)
	extractCfg extractor.ExtractorConfig
}

func New(cfg config.Config) *Processor {
	return &Processor{
		cfg: cfg,
		extractCfg: extractor.ExtractorConfig{
			PDFInfoTimeout:      cfg.PDFInfoTimeout,
			PDFToTextTimeout:    cfg.PDFToTextTimeout,
			PDFToTextAllTimeout: cfg.PDFToTextAllTimeout,
		},
	}
}

// ApplyDefaults merges server defaults into request options without overwriting valid user choices.
func (p *Processor) ApplyDefaults(opts types.HybridProcessorOptions) types.HybridProcessorOptions {
	if opts.MinWordsThreshold <= 0 {
		opts.MinWordsThreshold = p.cfg.DefaultMinWordsThreshold
	}
	if opts.PageSeparator == "" {
		opts.PageSeparator = p.cfg.DefaultPageSeparator
	}
	if opts.OCRTriggerRatio <= 0 {
		opts.OCRTriggerRatio = p.cfg.DefaultOCRTriggerRatio
	}
	if opts.OCRModel == nil {
		m := p.cfg.DefaultOCRModel
		opts.OCRModel = &m
	}
	if opts.PreviewMaxPages <= 0 {
		opts.PreviewMaxPages = p.cfg.DefaultPreviewMaxPages
	}
	if opts.PreviewMaxChars <= 0 {
		opts.PreviewMaxChars = p.cfg.DefaultPreviewMaxChars
	}
	return opts
}

func (p *Processor) ProcessHybrid(
	ctx context.Context,
	presignedURL, pdfPath string,
	opts types.HybridProcessorOptions,
) (types.HybridExtractionResult, error) {
	result := types.HybridExtractionResult{
		Success: false,
		Pages:   []types.PageExtractionResult{},
	}

	// Your compiler says PageCount wants ExtractorConfig
	totalPages, err := extractor.PageCount(ctx, pdfPath, p.extractCfg)
	if err != nil {
		msg := fmt.Sprintf("page count failed: %v", err)
		result.Error = &msg
		return result, err
	}
	result.TotalPages = totalPages

	if totalPages == 0 {
		msg := "PDF has no pages"
		result.Error = &msg
		return result, fmt.Errorf(msg)
	}

	// Determine pages to process
	pages := opts.Pages
	if len(pages) == 0 {
		pages = make([]int, totalPages)
		for i := range pages {
			pages[i] = i + 1
		}
	}

	// Phase 1: Extract text from all pages in parallel
	pageResults := p.extractPagesParallel(ctx, pdfPath, pages, opts.MinWordsThreshold)

	// Phase 2: Analyze quality
	needsOCRPages := make([]int, 0)
	for _, pr := range pageResults {
		result.Pages = append(result.Pages, pr)

		if pr.Method == "text-layer" {
			result.TextLayerPages++
		} else {
			needsOCRPages = append(needsOCRPages, pr.PageNumber)
		}
	}

	// Decide OCR strategy
	ocrRatio := float64(len(needsOCRPages)) / float64(len(pages))
	shouldDoFullOCR := ocrRatio >= opts.OCRTriggerRatio

	// Phase 3: Execute OCR if needed
	if len(needsOCRPages) > 0 {
		var ocrPages []int
		if shouldDoFullOCR {
			ocrPages = pages
		} else {
			ocrPages = needsOCRPages
		}

		ocrResults, err := runOCRBatch(ctx, presignedURL, ocrPages, opts)
		if err != nil {
			msg := fmt.Sprintf("OCR failed: %v", err)
			result.Error = &msg
		} else {
			mergeOCRResults(&result, ocrResults, shouldDoFullOCR)
		}
	}

	// Phase 4: Combine and format
	result.Text = format.Combine(result.Pages, opts.PageSeparator, opts.IncludePageNumbers)
	result.OCRPages = countOCRPages(result.Pages)
	result.TextLayerPages = len(result.Pages) - result.OCRPages
	result.CostSavingsPercent = calculateSavings(result.TextLayerPages, result.TotalPages)
	result.Success = true

	return result, nil
}

func (p *Processor) ProcessPreview(ctx context.Context, pdfPath string, opts types.HybridProcessorOptions) types.PreviewResult {
	result := types.PreviewResult{Success: false}

	// Your compiler says PageCount wants ExtractorConfig
	totalPages, err := extractor.PageCount(ctx, pdfPath, p.extractCfg)
	if err != nil {
		msg := fmt.Sprintf("page count: %v", err)
		result.Error = &msg
		return result
	}
	result.TotalPages = totalPages

	previewPages := opts.PreviewMaxPages
	if previewPages > totalPages {
		previewPages = totalPages
	}
	if previewPages < 1 {
		previewPages = 1
	}

	pages := make([]int, previewPages)
	for i := range pages {
		pages[i] = i + 1
	}

	pageResults := p.extractPagesParallel(ctx, pdfPath, pages, opts.MinWordsThreshold)

	needsOCR := 0
	totalWords := 0
	var textParts []string

	for _, pr := range pageResults {
		totalWords += pr.WordCount
		if pr.Method == "needs-ocr" {
			needsOCR++
		} else {
			result.TextLayerPages++
			textParts = append(textParts, pr.Text)
		}
	}

	result.WordCount = totalWords
	threshold := p.cfg.DefaultPreviewNeedsOCRRatio
	if threshold <= 0 {
		threshold = 0.25
	}
	result.NeedsOCR = float64(needsOCR)/float64(len(pages)) > threshold

	combined := strings.Join(textParts, "\n\n")
	if len(combined) > opts.PreviewMaxChars {
		combined = combined[:opts.PreviewMaxChars] + "..."
	}
	result.Text = combined
	result.Success = true

	return result
}

// ---------- Internal ----------

func (p *Processor) extractPagesParallel(ctx context.Context, pdfPath string, pages []int, minWords int) []types.PageExtractionResult {
	results := make([]types.PageExtractionResult, len(pages))

	workers := runtime.NumCPU()
	if p.cfg.MaxPageWorkers > 0 && workers > p.cfg.MaxPageWorkers {
		workers = p.cfg.MaxPageWorkers
	}
	if workers > len(pages) {
		workers = len(pages)
	}
	if workers < 1 {
		workers = 1
	}

	sem := semaphore.NewWeighted(int64(workers))
	var wg sync.WaitGroup

	for i, pageNum := range pages {
		wg.Add(1)
		go func(idx, page int) {
			defer wg.Done()

			if err := sem.Acquire(ctx, 1); err != nil {
				results[idx] = types.PageExtractionResult{
					PageNumber: page,
					Method:     "needs-ocr",
				}
				return
			}
			defer sem.Release(1)

			results[idx] = p.extractSinglePage(ctx, pdfPath, page, minWords)
		}(i, pageNum)
	}

	wg.Wait()
	return results
}

func (p *Processor) extractSinglePage(ctx context.Context, pdfPath string, pageNum, minWords int) types.PageExtractionResult {
	result := types.PageExtractionResult{
		PageNumber: pageNum,
		Method:     "text-layer",
	}

	// IMPORTANT:
	// Your compiler says TextForPage currently wants only (ctx, pdfPath, page).
	// If you later refactor it to accept config, change this ONE LINE:
	//
	// text, err := extractor.TextForPage(ctx, pdfPath, pageNum, p.extractCfg)
	//
	text, err := extractor.TextForPage(ctx, pdfPath, pageNum, p.extractCfg)
	if err != nil {
		result.Method = "needs-ocr"
		return result
	}

	text = cleanText(text)
	result.Text = text

	decision := quality.Score(text, minWords)
	result.WordCount = decision.WordCount

	if decision.NeedsOCR {
		result.Method = "needs-ocr"
		result.Text = ""
	}

	return result
}

func runOCRBatch(ctx context.Context, presignedURL string, pages []int, opts types.HybridProcessorOptions) (map[int]string, error) {
	if len(pages) == 0 {
		return map[int]string{}, nil
	}

	// Convert to 0-indexed
	pages0 := make([]int, len(pages))
	for i, p := range pages {
		pages0[i] = p - 1
	}

	ocrResp, err := ocr.RunMistralOCR(
		ctx,
		presignedURL,
		*opts.OCRModel,
		pages0,
		opts.ExtractHeader,
		opts.ExtractFooter,
	)
	if err != nil {
		return nil, err
	}

	results := make(map[int]string, len(ocrResp.Pages))
	for _, page := range ocrResp.Pages {
		pageNum := page.Index + 1
		results[pageNum] = cleanText(page.Markdown)
	}

	return results, nil
}

func mergeOCRResults(result *types.HybridExtractionResult, ocrResults map[int]string, fullOCR bool) {
	for i := range result.Pages {
		pageNum := result.Pages[i].PageNumber
		if ocrText, exists := ocrResults[pageNum]; exists {
			if fullOCR || result.Pages[i].Method == "needs-ocr" {
				result.Pages[i].Text = ocrText
				result.Pages[i].Method = "ocr"
				result.Pages[i].WordCount = quality.CountWords(ocrText)
			}
		}
	}
}

func cleanText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	text = strings.Map(func(r rune) rune {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\uFEFF':
			return -1
		case '\u00A0':
			return ' '
		case '\u00AD':
			return -1
		default:
			return r
		}
	}, text)

	lines := strings.Split(text, "\n")
	var cleaned []string
	consecutiveEmpty := 0

	for _, line := range lines {
		line = strings.TrimRight(line, " \t")

		if strings.TrimSpace(line) == "" {
			consecutiveEmpty++
			if consecutiveEmpty <= 2 {
				cleaned = append(cleaned, "")
			}
			continue
		}

		consecutiveEmpty = 0

		leadingSpaces := len(line) - len(strings.TrimLeft(line, " \t"))
		content := strings.TrimSpace(line)

		words := strings.Fields(content)
		normalizedContent := strings.Join(words, " ")

		if leadingSpaces > 0 {
			line = strings.Repeat(" ", leadingSpaces) + normalizedContent
		} else {
			line = normalizedContent
		}

		cleaned = append(cleaned, line)
	}

	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func countOCRPages(pages []types.PageExtractionResult) int {
	count := 0
	for _, p := range pages {
		if p.Method == "ocr" {
			count++
		}
	}
	return count
}

func calculateSavings(textLayerPages, totalPages int) int {
	if totalPages == 0 {
		return 0
	}
	return int(float64(textLayerPages) / float64(totalPages) * 100)
}

func isPasswordProtectedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "password protected") || strings.Contains(msg, "incorrect password")
}
