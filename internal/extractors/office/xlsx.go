package office

import (
	"context"
	"fmt"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
	"github.com/xuri/excelize/v2"
)

type XLSXExtractor struct {
	maxBytes int64
}

func NewXLSX(maxBytes int64) *XLSXExtractor {
	return &XLSXExtractor{maxBytes: maxBytes}
}

func (e *XLSXExtractor) Name() string       { return "document/xlsx" }
func (e *XLSXExtractor) MaxFileSize() int64 { return e.maxBytes }
func (e *XLSXExtractor) SupportedTypes() []string {
	return []string{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}
}
func (e *XLSXExtractor) SupportedExtensions() []string { return []string{".xlsx"} }

func (e *XLSXExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	select {
	case <-ctx.Done():
		return extract.Result{Success: false}, ctx.Err()
	default:
	}

	f, err := excelize.OpenFile(job.LocalPath)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}
	defer f.Close()

	sheets := f.GetSheetList()
	meta := map[string]string{
		"sheets": fmt.Sprintf("%d", len(sheets)),
	}

	var sections []string
	totalRows := 0
	for _, sheet := range sheets {
		rows, err := f.GetRows(sheet)
		if err != nil || len(rows) == 0 {
			continue
		}

		// Skip entirely empty rows
		filtered := make([][]string, 0, len(rows))
		for _, row := range rows {
			empty := true
			for _, cell := range row {
				if strings.TrimSpace(cell) != "" {
					empty = false
					break
				}
			}
			if !empty {
				filtered = append(filtered, row)
			}
		}
		if len(filtered) == 0 {
			continue
		}

		totalRows += len(filtered)
		table := xlsxRowsToMarkdown(filtered)
		sections = append(sections, "## Sheet: "+sheet+"\n\n"+table)
	}

	text := strings.Join(sections, "\n\n---\n\n")
	if strings.TrimSpace(text) == "" {
		text = "(empty workbook)"
	}

	meta["totalRows"] = fmt.Sprintf("%d", totalRows)

	words, chars := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: words, CharCount: chars}, nil
}

func xlsxRowsToMarkdown(rows [][]string) string {
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	if maxCols == 0 {
		return ""
	}

	// Pad rows to uniform column count and escape pipe chars
	for i := range rows {
		for len(rows[i]) < maxCols {
			rows[i] = append(rows[i], "")
		}
		for j := range rows[i] {
			rows[i][j] = strings.ReplaceAll(rows[i][j], "|", "\\|")
		}
	}

	truncated := false
	if len(rows) > 1001 {
		rows = rows[:1001]
		truncated = true
	}

	var sb strings.Builder
	sb.WriteString("| " + strings.Join(rows[0], " | ") + " |\n")
	sep := make([]string, maxCols)
	for i := range sep {
		sep[i] = "---"
	}
	sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")
	for _, row := range rows[1:] {
		sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	if truncated {
		sb.WriteString("\n... truncated to first 1000 data rows\n")
	}
	return sb.String()
}
