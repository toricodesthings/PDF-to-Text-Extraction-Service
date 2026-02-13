package opendocument

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type Extractor struct {
	maxBytes int64
}

func New(maxBytes int64) *Extractor { return &Extractor{maxBytes: maxBytes} }

func (e *Extractor) Name() string       { return "document/opendocument" }
func (e *Extractor) MaxFileSize() int64 { return e.maxBytes }
func (e *Extractor) SupportedTypes() []string {
	return []string{"application/vnd.oasis.opendocument.text", "application/vnd.oasis.opendocument.spreadsheet", "application/vnd.oasis.opendocument.presentation"}
}
func (e *Extractor) SupportedExtensions() []string { return []string{".odt", ".ods", ".odp"} }

func (e *Extractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
	select {
	case <-ctx.Done():
		return extract.Result{Success: false}, ctx.Err()
	default:
	}

	zr, err := zip.OpenReader(job.LocalPath)
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}
	defer zr.Close()

	var content []byte
	for _, f := range zr.File {
		if f.Name == "content.xml" {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			content, _ = io.ReadAll(rc)
			rc.Close()
			break
		}
	}
	if len(content) == 0 {
		err = fmt.Errorf("content.xml not found")
		msg := err.Error()
		return extract.Result{Success: false, FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	text := odfToMarkdown(content)
	meta := odfParseMetadata(zr)

	if len(meta) > 0 {
		text = odfFrontmatter(meta) + text
	}

	text = strings.TrimSpace(text)
	words, chars := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: words, CharCount: chars}, nil
}

// odfToMarkdown walks ODF content.xml and produces markdown.
func odfToMarkdown(b []byte) string {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var blocks []string

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch {
		case se.Name.Local == "h" && se.Name.Space == "urn:oasis:names:tc:opendocument:xmlns:text:1.0":
			// Heading element - extract outline level
			level := 1
			for _, a := range se.Attr {
				if a.Name.Local == "outline-level" {
					if len(a.Value) == 1 && a.Value[0] >= '1' && a.Value[0] <= '6' {
						level = int(a.Value[0] - '0')
					}
				}
			}
			text := odfCollectText(dec, "h")
			if text != "" {
				blocks = append(blocks, strings.Repeat("#", level)+" "+text)
			}

		case se.Name.Local == "p" && se.Name.Space == "urn:oasis:names:tc:opendocument:xmlns:text:1.0":
			text := odfCollectText(dec, "p")
			if text != "" {
				blocks = append(blocks, text)
			}

		case se.Name.Local == "list" && se.Name.Space == "urn:oasis:names:tc:opendocument:xmlns:text:1.0":
			items := odfCollectList(dec, 0)
			if len(items) > 0 {
				blocks = append(blocks, strings.Join(items, "\n"))
			}

		case se.Name.Local == "table" && se.Name.Space == "urn:oasis:names:tc:opendocument:xmlns:table:1.0":
			table := odfCollectTable(dec)
			if table != "" {
				blocks = append(blocks, table)
			}
		}
	}

	return strings.Join(blocks, "\n\n")
}

// odfCollectText reads all text inside an element until its closing tag.
func odfCollectText(dec *xml.Decoder, endTag string) string {
	var texts []string
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "tab" {
				texts = append(texts, "\t")
			} else if t.Name.Local == "line-break" {
				texts = append(texts, "\n")
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			s := string(t)
			if strings.TrimSpace(s) != "" {
				texts = append(texts, s)
			}
		}
	}
	return strings.TrimSpace(strings.Join(texts, ""))
}

// odfCollectList reads a text:list element and returns markdown list items.
func odfCollectList(dec *xml.Decoder, indentLevel int) []string {
	var items []string
	depth := 1
	indent := strings.Repeat("  ", indentLevel)

	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "p" {
				text := odfCollectText(dec, "p")
				depth-- // odfCollectText consumed the end tag
				if text != "" {
					items = append(items, indent+"- "+text)
				}
			} else if t.Name.Local == "list" {
				sub := odfCollectList(dec, indentLevel+1)
				depth-- // recursive call consumed the end tag
				items = append(items, sub...)
			}
		case xml.EndElement:
			depth--
		}
	}
	return items
}

// odfCollectTable reads a table:table element and returns a markdown table.
func odfCollectTable(dec *xml.Decoder) string {
	var rows [][]string
	depth := 1

	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "table-row" {
				row := odfCollectTableRow(dec)
				depth-- // consumed by recursive function
				if len(row) > 0 {
					rows = append(rows, row)
				}
			}
		case xml.EndElement:
			depth--
		}
	}

	if len(rows) == 0 {
		return ""
	}

	// Normalize column count
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	for i := range rows {
		for len(rows[i]) < maxCols {
			rows[i] = append(rows[i], "")
		}
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
	return sb.String()
}

func odfCollectTableRow(dec *xml.Decoder) []string {
	var cells []string
	depth := 1

	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "table-cell" {
				cell := odfCollectCellText(dec)
				depth--
				cells = append(cells, cell)
			}
		case xml.EndElement:
			depth--
		}
	}
	return cells
}

func odfCollectCellText(dec *xml.Decoder) string {
	var texts []string
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			s := strings.TrimSpace(string(t))
			if s != "" {
				texts = append(texts, s)
			}
		}
	}
	return strings.Join(texts, " ")
}

// odfParseMetadata reads meta.xml from the zip and extracts title, author, etc.
func odfParseMetadata(zr *zip.ReadCloser) map[string]string {
	for _, f := range zr.File {
		if f.Name != "meta.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		b, _ := io.ReadAll(rc)
		rc.Close()

		meta := map[string]string{}
		dec := xml.NewDecoder(strings.NewReader(string(b)))
		var tag string
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				tag = t.Name.Local
			case xml.CharData:
				val := strings.TrimSpace(string(t))
				if val == "" {
					continue
				}
				switch tag {
				case "title":
					meta["title"] = val
				case "initial-creator", "creator":
					meta["author"] = val
				case "creation-date":
					meta["created"] = val
				case "date":
					meta["modified"] = val
				case "description":
					meta["description"] = val
				case "subject":
					meta["subject"] = val
				}
			case xml.EndElement:
				tag = ""
			}
		}
		if len(meta) == 0 {
			return nil
		}
		return meta
	}
	return nil
}

func odfFrontmatter(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	for _, key := range []string{"title", "author", "subject", "description", "created", "modified"} {
		if v, ok := meta[key]; ok && v != "" {
			sb.WriteString(key + ": " + v + "\n")
		}
	}
	sb.WriteString("---\n\n")
	return sb.String()
}
