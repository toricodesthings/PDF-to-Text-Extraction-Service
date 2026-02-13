package plaintext

import (
	"bytes"
	"context"
	"os"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
	"golang.org/x/net/html"
)

type HTMLExtractor struct {
	maxBytes int64
}

func NewHTML(maxBytes int64) *HTMLExtractor { return &HTMLExtractor{maxBytes: maxBytes} }

func (e *HTMLExtractor) Name() string             { return "document/html" }
func (e *HTMLExtractor) MaxFileSize() int64       { return e.maxBytes }
func (e *HTMLExtractor) SupportedTypes() []string { return []string{"text/html"} }
func (e *HTMLExtractor) SupportedExtensions() []string {
	return []string{".html", ".htm", ".xhtml", ".mhtml"}
}

func (e *HTMLExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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
	text, meta := htmlStripToMarkdownLike(b)
	w, c := extract.BuildCounts(text)
	return extract.Result{Success: true, Text: text, Method: "native", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: w, CharCount: c}, nil
}

func htmlStripToMarkdownLike(b []byte) (string, map[string]string) {
	meta := map[string]string{}
	node, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return string(b), meta
	}
	var lines []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if tag == "script" || tag == "style" || tag == "nav" || tag == "footer" || tag == "aside" {
				return
			}
			if tag == "title" && n.FirstChild != nil {
				meta["title"] = strings.TrimSpace(n.FirstChild.Data)
			}
			if tag == "h1" || tag == "h2" || tag == "h3" {
				lvl := map[string]string{"h1": "#", "h2": "##", "h3": "###"}[tag]
				lines = append(lines, lvl+" "+strings.TrimSpace(htmlStripNodeText(n)))
			}
			if tag == "p" || tag == "li" {
				t := strings.TrimSpace(htmlStripNodeText(n))
				if t != "" {
					lines = append(lines, t)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	if len(lines) == 0 {
		plain := strings.TrimSpace(htmlStripNodeText(node))
		if plain != "" {
			lines = append(lines, plain)
		}
	}
	return strings.Join(lines, "\n\n"), meta
}

func htmlStripNodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(htmlStripNodeText(c))
	}
	return sb.String()
}
