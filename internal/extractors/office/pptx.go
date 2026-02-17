package office

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type PPTXExtractor struct {
	maxBytes int64
}

func NewPPTX(maxBytes int64) *PPTXExtractor {
	return &PPTXExtractor{maxBytes: maxBytes}
}

func (e *PPTXExtractor) Name() string       { return "document/pptx" }
func (e *PPTXExtractor) MaxFileSize() int64 { return e.maxBytes }
func (e *PPTXExtractor) SupportedTypes() []string {
	return []string{"application/vnd.openxmlformats-officedocument.presentationml.presentation"}
}
func (e *PPTXExtractor) SupportedExtensions() []string { return []string{".pptx"} }

func (e *PPTXExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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

	// Collect slide files in order
	slideNames := make([]string, 0)
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slideNames = append(slideNames, f.Name)
		}
	}
	sort.Strings(slideNames)

	meta := parseCoreMetadata(&zr.Reader, defaultMaxZipMetadataBytes)
	if meta == nil {
		meta = map[string]string{}
	}
	meta["slides"] = fmt.Sprintf("%d", len(slideNames))

	parts := make([]string, 0, len(slideNames))
	for i, name := range slideNames {
		slideNum := i + 1
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("## Slide %d", slideNum))

		// Extract slide body text
		b, err := readZipFile(&zr.Reader, name, defaultMaxZipEntryBytes)
		if err != nil {
			continue
		}
		slideText := pptxExtractTextBlocks(b)
		if slideText != "" {
			sb.WriteString("\n\n" + slideText)
		}

		// Extract speaker notes from ppt/notesSlides/notesSlideN.xml
		notesPath := fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", slideNum)
		if nb, err := readZipFile(&zr.Reader, notesPath, defaultMaxZipEntryBytes); err == nil {
			notesText := pptxExtractTextBlocks(nb)
			// Filter out the slide number placeholder text that's often in notes
			notesText = strings.TrimSpace(notesText)
			if notesText != "" {
				sb.WriteString("\n\n> **Speaker Notes:**\n> " + strings.ReplaceAll(notesText, "\n", "\n> "))
			}
		}

		parts = append(parts, sb.String())
	}

	text := strings.Join(parts, "\n\n---\n\n")

	if len(meta) > 0 {
		text = metadataFrontmatter(meta) + text
	}

	text = strings.TrimSpace(text)
	words, chars := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: words, CharCount: chars}, nil
}

// pptxExtractTextBlocks walks OOXML slide/notes XML and returns text organized by paragraphs.
// Groups <a:p> elements, joining <a:r>/<a:t> text runs within each paragraph.
func pptxExtractTextBlocks(b []byte) string {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var paragraphs []string
	var currentPara []string
	inParagraph := false

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				if t.Name.Space == "http://schemas.openxmlformats.org/drawingml/2006/main" || t.Name.Space == "" {
					inParagraph = true
					currentPara = nil
				}
			}
		case xml.CharData:
			if inParagraph {
				s := strings.TrimSpace(string(t))
				if s != "" {
					currentPara = append(currentPara, s)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "p" && inParagraph {
				text := strings.TrimSpace(strings.Join(currentPara, " "))
				if text != "" {
					paragraphs = append(paragraphs, text)
				}
				inParagraph = false
				currentPara = nil
			}
		}
	}

	return strings.Join(paragraphs, "\n\n")
}

func readAll(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	return io.ReadAll(rc)
}
