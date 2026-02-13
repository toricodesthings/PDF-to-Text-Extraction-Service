package code

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toricodesthings/file-processing-service/internal/extract"
)

func TestNotebookExtractor(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "sample.ipynb")
	content := `{"cells":[{"cell_type":"markdown","source":["# Title\\n"]},{"cell_type":"code","source":["print(1)\\n"]}]}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write notebook: %v", err)
	}

	e := NewNotebook(1 << 20)
	res, err := e.Extract(context.Background(), extract.Job{LocalPath: p, FileName: "sample.ipynb", MIMEType: "application/x-ipynb+json"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success")
	}
	if !strings.Contains(res.Text, "# Title") {
		t.Fatalf("missing markdown cell")
	}
	if !strings.Contains(res.Text, "```python") {
		t.Fatalf("missing code fence")
	}
}
