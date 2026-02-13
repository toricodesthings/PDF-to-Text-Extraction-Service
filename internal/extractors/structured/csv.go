package structured

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type CSVExtractor struct {
	maxBytes int64
}

func NewCSV(maxBytes int64) *CSVExtractor { return &CSVExtractor{maxBytes: maxBytes} }

func (e *CSVExtractor) Name() string       { return "structured/csv" }
func (e *CSVExtractor) MaxFileSize() int64 { return e.maxBytes }
func (e *CSVExtractor) SupportedTypes() []string {
	return []string{"text/csv", "text/tab-separated-values"}
}
func (e *CSVExtractor) SupportedExtensions() []string { return []string{".csv", ".tsv"} }

func (e *CSVExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	select {
	case <-ctx.Done():
		return extract.Result{Success: false}, ctx.Err()
	default:
	}

	b, err := os.ReadFile(job.LocalPath)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	recs, delim, err := readRecords(b)
	if err != nil || len(recs) == 0 {
		text := strings.TrimSpace(string(b))
		w, c := extract.BuildCounts(text)
		return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, WordCount: w, CharCount: c}, nil
	}

	text := recordsToMarkdown(recs)
	w, c := extract.BuildCounts(text)
	meta := map[string]string{
		"rows":      fmt.Sprintf("%d", len(recs)),
		"columns":   fmt.Sprintf("%d", maxCols(recs)),
		"delimiter": string(delim),
	}
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: w, CharCount: c}, nil
}

func readRecords(b []byte) ([][]string, rune, error) {
	for _, d := range []rune{',', '\t', ';', '|'} {
		r := csv.NewReader(bytes.NewReader(b))
		r.Comma = d
		r.FieldsPerRecord = -1
		recs, err := r.ReadAll()
		if err == nil && len(recs) > 0 && maxCols(recs) > 1 {
			return recs, d, nil
		}
	}
	return nil, ',', fmt.Errorf("unable to parse CSV/TSV")
}

func maxCols(recs [][]string) int {
	m := 0
	for _, row := range recs {
		if len(row) > m {
			m = len(row)
		}
	}
	return m
}

func recordsToMarkdown(recs [][]string) string {
	if len(recs) == 0 {
		return ""
	}
	max := maxCols(recs)
	for i := range recs {
		for len(recs[i]) < max {
			recs[i] = append(recs[i], "")
		}
	}

	rows := recs
	if len(rows) > 201 {
		rows = rows[:201]
	}

	var sb strings.Builder
	sb.WriteString("| " + strings.Join(rows[0], " | ") + " |\n")
	sep := make([]string, max)
	for i := range sep {
		sep[i] = "---"
	}
	sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")
	for _, row := range rows[1:] {
		sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	if len(recs) > 201 {
		sb.WriteString(fmt.Sprintf("\n... and %d more rows", len(recs)-201))
	}
	return strings.TrimSpace(sb.String())
}
