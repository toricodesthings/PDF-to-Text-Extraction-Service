package office

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type DOCXExtractor struct {
	maxBytes int64
}

func NewDOCX(maxBytes int64) *DOCXExtractor {
	return &DOCXExtractor{maxBytes: maxBytes}
}

func (e *DOCXExtractor) Name() string       { return "document/docx" }
func (e *DOCXExtractor) MaxFileSize() int64 { return e.maxBytes }
func (e *DOCXExtractor) SupportedTypes() []string {
	return []string{"application/vnd.openxmlformats-officedocument.wordprocessingml.document"}
}
func (e *DOCXExtractor) SupportedExtensions() []string { return []string{".docx"} }

func (e *DOCXExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	body, err := readZipFile(&zr.Reader, "word/document.xml")
	if err != nil {
		msg := err.Error()
		return extract.Result{Success: false, FileType: e.Name(), MIMEType: job.MIMEType, Error: &msg}, err
	}

	text := docxToMarkdown(body)
	meta := parseCoreMetadata(&zr.Reader)

	// Prepend metadata frontmatter if available
	if len(meta) > 0 {
		text = metadataFrontmatter(meta) + text
	}

	text = strings.TrimSpace(text)
	words, chars := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: words, CharCount: chars}, nil
}

// docxToMarkdown walks <w:body> in word/document.xml producing markdown.
// Handles paragraphs with heading styles, numbered/bulleted lists, and tables.
func docxToMarkdown(b []byte) string {
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
		switch se.Name.Local {
		case "p":
			blocks = append(blocks, docxParagraph(dec, se))
		case "tbl":
			blocks = append(blocks, docxTable(dec))
		}
	}

	var out []string
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		if b != "" {
			out = append(out, b)
		}
	}
	return strings.Join(out, "\n\n")
}

// docxParagraph reads one <w:p> element and returns markdown text.
func docxParagraph(dec *xml.Decoder, start xml.StartElement) string {
	var style string
	var numID string
	var numLvl string
	var runs []string
	depth := 1

	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						style = a.Value
					}
				}
			case "numId":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						numID = a.Value
					}
				}
			case "ilvl":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						numLvl = a.Value
					}
				}
			case "t":
				text := readCharData(dec, &depth)
				runs = append(runs, text)
			case "tab":
				runs = append(runs, "\t")
			case "br":
				runs = append(runs, "\n")
			}
		case xml.EndElement:
			depth--
		}
	}

	text := strings.Join(runs, "")
	if strings.TrimSpace(text) == "" {
		return ""
	}

	// Check for heading styles (Heading1, Heading2, etc. or HeadingN patterns)
	if h := headingLevel(style); h > 0 {
		prefix := strings.Repeat("#", h)
		return prefix + " " + strings.TrimSpace(text)
	}

	// List items
	if numID != "" && numID != "0" {
		indent := ""
		if numLvl != "" && numLvl != "0" {
			lvl := 0
			for _, c := range numLvl {
				lvl = lvl*10 + int(c-'0')
			}
			indent = strings.Repeat("  ", lvl)
		}
		return indent + "- " + strings.TrimSpace(text)
	}

	return strings.TrimSpace(text)
}

// headingLevel returns the markdown heading level for OOXML paragraph styles.
func headingLevel(style string) int {
	s := strings.ToLower(style)
	if s == "title" {
		return 1
	}
	if s == "subtitle" {
		return 2
	}
	if strings.HasPrefix(s, "heading") {
		n := strings.TrimPrefix(s, "heading")
		if len(n) == 1 && n[0] >= '1' && n[0] <= '6' {
			return int(n[0] - '0')
		}
	}
	return 0
}

// docxTable reads one <w:tbl> element and returns a markdown table.
func docxTable(dec *xml.Decoder) string {
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
			if t.Name.Local == "tr" {
				rows = append(rows, docxTableRow(dec, &depth))
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

// docxTableRow reads one <w:tr> element and returns cell texts.
func docxTableRow(dec *xml.Decoder, outerDepth *int) []string {
	var cells []string
	depth := 0 // already counted by caller

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "tc" {
				cells = append(cells, docxTableCell(dec, &depth))
			}
		case xml.EndElement:
			if depth == 0 {
				// end of <w:tr>
				return cells
			}
			depth--
		}
	}
	return cells
}

// docxTableCell reads one <w:tc> element and returns its text content.
func docxTableCell(dec *xml.Decoder, outerDepth *int) string {
	var texts []string
	depth := 0

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "t" {
				texts = append(texts, readCharData(dec, &depth))
			}
		case xml.EndElement:
			if depth == 0 {
				return strings.TrimSpace(strings.Join(texts, " "))
			}
			depth--
		}
	}
	return strings.TrimSpace(strings.Join(texts, " "))
}

// readCharData reads character data inside a text element, tracking depth.
func readCharData(dec *xml.Decoder, depth *int) string {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.StartElement:
			*depth++
		case xml.EndElement:
			*depth--
			return sb.String()
		}
	}
	return sb.String()
}

// --- Shared helpers ---

func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("missing %s", name)
}

// parseCoreMetadata extracts title, author, dates from docProps/core.xml.
func parseCoreMetadata(zr *zip.Reader) map[string]string {
	b, err := readZipFile(zr, "docProps/core.xml")
	if err != nil {
		return nil
	}

	meta := map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var currentTag string

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			currentTag = t.Name.Local
		case xml.CharData:
			val := strings.TrimSpace(string(t))
			if val == "" {
				continue
			}
			switch currentTag {
			case "title":
				meta["title"] = val
			case "creator":
				meta["author"] = val
			case "created":
				meta["created"] = val
			case "modified":
				meta["modified"] = val
			case "description":
				meta["description"] = val
			case "subject":
				meta["subject"] = val
			case "lastModifiedBy":
				meta["lastModifiedBy"] = val
			}
		case xml.EndElement:
			currentTag = ""
		}
	}

	if len(meta) == 0 {
		return nil
	}
	return meta
}

// metadataFrontmatter generates a YAML frontmatter block from metadata.
func metadataFrontmatter(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	for _, key := range []string{"title", "author", "subject", "description", "created", "modified", "lastModifiedBy"} {
		if v, ok := meta[key]; ok && v != "" {
			sb.WriteString(key + ": " + v + "\n")
		}
	}
	sb.WriteString("---\n\n")
	return sb.String()
}
