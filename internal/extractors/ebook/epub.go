package ebook

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type EPUBExtractor struct {
	maxBytes int64
}

func NewEPUB(maxBytes int64) *EPUBExtractor { return &EPUBExtractor{maxBytes: maxBytes} }

func (e *EPUBExtractor) Name() string                  { return "document/epub" }
func (e *EPUBExtractor) MaxFileSize() int64            { return e.maxBytes }
func (e *EPUBExtractor) SupportedTypes() []string      { return []string{"application/epub+zip"} }
func (e *EPUBExtractor) SupportedExtensions() []string { return []string{".epub"} }

func (e *EPUBExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	// 1. Find the OPF file path from META-INF/container.xml
	opfPath := findOPFPath(zr)
	if opfPath == "" {
		// Fallback: look for any .opf file
		for _, f := range zr.File {
			if strings.HasSuffix(strings.ToLower(f.Name), ".opf") {
				opfPath = f.Name
				break
			}
		}
	}

	meta := map[string]string{}
	var spineItems []string

	if opfPath != "" {
		opfData, err := readZipEntry(zr, opfPath, 4<<20)
		if err == nil {
			spineItems, meta = parseOPF(opfData, path.Dir(opfPath))
		}
	}

	// Fallback if no spine found: enumerate all XHTML/HTML files alphabetically
	if len(spineItems) == 0 {
		for _, f := range zr.File {
			name := strings.ToLower(f.Name)
			if strings.HasSuffix(name, ".xhtml") || strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".htm") {
				spineItems = append(spineItems, f.Name)
			}
		}
	}

	var chapters []string
	for i, item := range spineItems {
		b, err := readZipEntry(zr, item, 16<<20)
		if err != nil {
			continue
		}
		chapterText := epubStripHTML(string(b))
		if strings.TrimSpace(chapterText) == "" {
			continue
		}
		chapters = append(chapters, fmt.Sprintf("## Chapter %d\n\n%s", i+1, chapterText))
	}

	text := strings.Join(chapters, "\n\n---\n\n")

	if len(meta) > 0 {
		text = epubFrontmatter(meta) + text
	}

	text = strings.TrimSpace(text)
	words, chars := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: words, CharCount: chars}, nil
}

// findOPFPath reads META-INF/container.xml and returns the rootfile full-path.
func findOPFPath(zr *zip.ReadCloser) string {
	b, err := readZipEntry(zr, "META-INF/container.xml", 2<<20)
	if err != nil {
		return ""
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "rootfile" {
			for _, a := range se.Attr {
				if a.Name.Local == "full-path" {
					return a.Value
				}
			}
		}
	}
	return ""
}

// parseOPF parses the OPF file and returns spine-ordered item paths and metadata.
func parseOPF(data []byte, opfDir string) ([]string, map[string]string) {
	type manifestItem struct {
		ID   string
		Href string
	}

	dec := xml.NewDecoder(strings.NewReader(string(data)))
	manifest := map[string]manifestItem{}
	var spineOrder []string
	meta := map[string]string{}
	var currentTag string

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			currentTag = t.Name.Local
			switch t.Name.Local {
			case "item":
				var id, href string
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "id":
						id = a.Value
					case "href":
						href = a.Value
					}
				}
				if id != "" && href != "" {
					manifest[id] = manifestItem{ID: id, Href: href}
				}
			case "itemref":
				for _, a := range t.Attr {
					if a.Name.Local == "idref" {
						spineOrder = append(spineOrder, a.Value)
					}
				}
			}
		case xml.CharData:
			val := strings.TrimSpace(string(t))
			if val == "" {
				continue
			}
			switch currentTag {
			case "title":
				if _, exists := meta["title"]; !exists {
					meta["title"] = val
				}
			case "creator":
				meta["author"] = val
			case "publisher":
				meta["publisher"] = val
			case "language":
				meta["language"] = val
			case "identifier":
				meta["identifier"] = val
			case "description":
				meta["description"] = val
			case "date":
				if _, exists := meta["date"]; !exists {
					meta["date"] = val
				}
			}
		case xml.EndElement:
			currentTag = ""
		}
	}

	// Resolve spine order to file paths
	var paths []string
	for _, idref := range spineOrder {
		if item, ok := manifest[idref]; ok {
			p := item.Href
			if opfDir != "" && opfDir != "." {
				p = opfDir + "/" + p
			}
			paths = append(paths, p)
		}
	}

	return paths, meta
}

// epubStripHTML converts basic HTML to markdown-like text.
func epubStripHTML(s string) string {
	// Convert block elements
	replacer := strings.NewReplacer(
		"<br>", "\n", "<br/>", "\n", "<br />", "\n",
		"</p>", "\n\n", "</div>", "\n\n",
	)
	s = replacer.Replace(s)

	// Convert headings to markdown
	for _, level := range []string{"1", "2", "3", "4", "5", "6"} {
		prefix := strings.Repeat("#", int(level[0]-'0'))
		s = strings.ReplaceAll(s, "<h"+level+">", prefix+" ")
		s = strings.ReplaceAll(s, "<h"+level+" ", prefix+" <")
		s = strings.ReplaceAll(s, "</h"+level+">", "\n\n")
	}

	// Convert list items
	s = strings.ReplaceAll(s, "<li>", "- ")
	s = strings.ReplaceAll(s, "</li>", "\n")

	// Strip remaining tags
	for {
		i := strings.Index(s, "<")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], ">")
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+1:]
	}

	// Decode common HTML entities
	s = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", "\"", "&#39;", "'", "&apos;", "'",
		"&nbsp;", " ",
	).Replace(s)

	// Normalize whitespace
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n\n")
}

func readZipEntry(zr *zip.ReadCloser, name string, maxBytes int64) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			if f.UncompressedSize64 > uint64(maxBytes) {
				return nil, fmt.Errorf("%s exceeds %dMB uncompressed limit", name, maxBytes/(1<<20))
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			lr := &io.LimitedReader{R: rc, N: maxBytes + 1}
			b, err := io.ReadAll(lr)
			if err != nil {
				return nil, err
			}
			if int64(len(b)) > maxBytes {
				return nil, fmt.Errorf("%s exceeds %dMB uncompressed limit", name, maxBytes/(1<<20))
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", name)
}

func epubFrontmatter(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	for _, key := range []string{"title", "author", "publisher", "date", "language", "identifier", "description"} {
		if v, ok := meta[key]; ok && v != "" {
			sb.WriteString(key + ": " + v + "\n")
		}
	}
	sb.WriteString("---\n\n")
	return sb.String()
}
