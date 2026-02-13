package code

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

type SourceExtractor struct {
	maxBytes int64
}

func NewSource(maxBytes int64) *SourceExtractor { return &SourceExtractor{maxBytes: maxBytes} }

func (e *SourceExtractor) Name() string       { return "code/source" }
func (e *SourceExtractor) MaxFileSize() int64 { return e.maxBytes }
func (e *SourceExtractor) SupportedTypes() []string {
	return nil
}
func (e *SourceExtractor) SupportedExtensions() []string {
	return []string{
		".py", ".pyw", ".pyi", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts",
		".go", ".java", ".kt", ".kts", ".scala", ".groovy", ".gradle", ".c", ".h", ".cpp", ".hpp", ".cc", ".cxx", ".cs",
		".rb", ".php", ".swift", ".m", ".mm", ".rs", ".dart", ".ex", ".exs", ".erl", ".hs", ".ml", ".mli", ".clj", ".cljs",
		".lua", ".r", ".jl", ".pl", ".pm", ".zig", ".nim", ".v", ".cr", ".d", ".adb", ".ads", ".asm", ".s", ".S", ".cu", ".cuh",
		".sh", ".bash", ".zsh", ".fish", ".ksh", ".csh", ".ps1", ".psm1", ".psd1", ".bat", ".cmd", ".sql", ".graphql", ".gql", ".proto", ".tf", ".hcl", ".tfvars", ".nix",
	}
}

func (e *SourceExtractor) Extract(ctx context.Context, job extract.Job) (extract.Result, error) {
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
	text := strings.TrimSpace(string(b))
	ext := strings.ToLower(filepath.Ext(job.FileName))
	lang := langFromExt(ext)
	lines := strings.Count(text, "\n") + 1

	if lines > 10000 {
		text = summarizeLargeCode(text)
		lines = strings.Count(text, "\n") + 1
	}

	wrapped := fmt.Sprintf("<!-- lang: %s, lines: %d -->\n\n```%s\n%s\n```", lang, lines, lang, text)
	w, c := extract.BuildCounts(wrapped)
	meta := map[string]string{"language": lang}
	return extract.Result{Success: true, Text: wrapped, Method: "code", FileType: e.Name(), MIMEType: job.MIMEType, Metadata: meta, WordCount: w, CharCount: c}, nil
}

func summarizeLargeCode(src string) string {
	lines := strings.Split(src, "\n")
	head := lines
	if len(head) > 50 {
		head = head[:50]
	}

	sigs := make([]string, 0)
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "func ") || strings.HasPrefix(trim, "class ") || strings.HasPrefix(trim, "def ") || strings.HasPrefix(trim, "interface ") || strings.HasPrefix(trim, "type ") {
			sigs = append(sigs, line)
			continue
		}
		if strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "\"\"\"") || strings.HasPrefix(trim, "/*") {
			sigs = append(sigs, line)
		}
		if len(sigs) >= 500 {
			break
		}
	}
	return strings.TrimSpace(strings.Join(head, "\n") + "\n\n/* signatures + docs */\n" + strings.Join(sigs, "\n"))
}

var languageByExt = map[string]string{
	".py": "python", ".pyw": "python", ".pyi": "python", ".js": "javascript", ".jsx": "jsx", ".mjs": "javascript", ".cjs": "javascript", ".ts": "typescript", ".tsx": "tsx", ".mts": "typescript", ".cts": "typescript",
	".go": "go", ".java": "java", ".kt": "kotlin", ".kts": "kotlin", ".scala": "scala", ".groovy": "groovy", ".c": "c", ".h": "c", ".cpp": "cpp", ".hpp": "cpp", ".cc": "cpp", ".cxx": "cpp", ".cs": "csharp",
	".rb": "ruby", ".php": "php", ".swift": "swift", ".m": "objective-c", ".mm": "objective-c", ".rs": "rust", ".dart": "dart", ".ex": "elixir", ".exs": "elixir", ".erl": "erlang", ".hs": "haskell", ".ml": "ocaml", ".mli": "ocaml", ".clj": "clojure", ".cljs": "clojure",
	".lua": "lua", ".r": "r", ".jl": "julia", ".pl": "perl", ".pm": "perl", ".zig": "zig", ".nim": "nim", ".v": "v", ".cr": "crystal", ".d": "d", ".adb": "ada", ".ads": "ada", ".asm": "asm", ".s": "asm", ".S": "asm", ".cu": "cuda", ".cuh": "cuda",
	".sh": "bash", ".bash": "bash", ".zsh": "zsh", ".fish": "fish", ".ksh": "ksh", ".csh": "csh", ".ps1": "powershell", ".psm1": "powershell", ".psd1": "powershell", ".bat": "bat", ".cmd": "bat", ".sql": "sql", ".graphql": "graphql", ".gql": "graphql", ".proto": "proto", ".tf": "hcl", ".hcl": "hcl", ".tfvars": "hcl", ".nix": "nix",
}

func langFromExt(ext string) string {
	if v, ok := languageByExt[ext]; ok {
		return v
	}
	return "text"
}
