# File Processing Service — Comprehensive Upgrade Plan

## Executive Summary

The current service handles **2 file families**: PDFs (text-layer + OCR hybrid) and images (OCR + vision classification). This plan expands coverage to **~90+ file extensions** across **7 major categories**, creating a universal file-to-text pipeline optimized for RAG ingestion on a **learning-focused platform**.

**End-to-end flow:**
```
Frontend upload → R2 object storage → Worker (edge) → Container (extract) → Formatted markdown → RAG pipeline
```

---

## Current Architecture (As-Is)

```
worker/src/index.ts          Cloudflare Worker — edge proxy, R2 presigning, rate limiting
cmd/server/main.go           Go HTTP server — routes, middleware, download-to-temp
internal/
  hybrid/hybrid.go           PDF pipeline: text-layer → quality check → OCR fallback
  image/image.go             Image pipeline: vision classification → OCR / description
  extractor/poppler.go       pdfinfo + pdftotext (Poppler CLI)
  ocr/mistral.go             Mistral OCR API
  vision/openrouter.go       OpenRouter vision classification
  quality/quality.go         Text quality scoring (decides OCR trigger)
  format/format.go           Markdown cleanup, HTML table conversion, RAG formatting
  types/types.go             Request/response structs
  config/config.go           Environment-based configuration
```

**Key patterns already established:**
- Download presigned URL → temp file → process → cleanup
- Concurrent processing with semaphores (`requestSem`, `ocrSem`)
- Quality scoring to avoid unnecessary API calls
- Structured JSON responses with `success` / `error` / `code`
- Rate limiting per IP, auth via `X-Internal-Auth`
- Docker container with `debian:bookworm-slim` + CLI tools

---

## Target Architecture (To-Be)

### New Package Structure

```
internal/
  extract/                        ← NEW: Unified extraction framework
    registry.go                   File type registry + MIME detection
    extractor.go                  Common Extractor interface
    result.go                     Unified ExtractionResult type
    download.go                   Generic file downloader (moved from main.go)
    router.go                     File type → extractor routing

  extractors/                     ← NEW: One package per extractor family
    pdf/
      pdf.go                      Wraps existing hybrid/ pipeline
    image/
      image.go                    Wraps existing image/ pipeline
    office/
      docx.go                     DOCX → text (pure Go, unzip+XML)
      xlsx.go                     XLSX → text (pure Go or excelize)
      pptx.go                     PPTX → text (pure Go, unzip+XML)
      legacy.go                   DOC/XLS/PPT via LibreOffice headless
    opendocument/
      odt.go                      ODT/ODS/ODP → text (unzip+XML)
    ebook/
      epub.go                     EPUB → text (unzip+XHTML)
    plaintext/
      plaintext.go                TXT, MD, LOG, code files
      html_strip.go               HTML → plain text
      rtf.go                      RTF → plain text
    structured/
      csv.go                      CSV/TSV → markdown table
      json_extract.go             JSON → formatted text
      xml_extract.go              XML → text content
      yaml_extract.go             YAML → formatted text
    code/
      code.go                     Source code → markdown with lang tag + structure
      notebook.go                 Jupyter .ipynb → markdown cells
      latex.go                    LaTeX .tex → text content
    audio/
      transcribe.go               Audio → text via Whisper API
    video/
      transcribe.go               Video → extract audio → Whisper API

  format/format.go                Existing (expanded with new formatters)
  quality/quality.go              Existing (no changes)
  ocr/mistral.go                  Existing (no changes)
  vision/openrouter.go            Existing (no changes)
  hybrid/hybrid.go                Existing (no changes, wrapped by extractors/pdf/)
  config/config.go                Existing (expanded with new config keys)
```

---

## Phase 0: Core Abstraction Layer

**Priority: CRITICAL — all subsequent phases depend on this.**

### 0.1 — Unified Extractor Interface

```go
// internal/extract/extractor.go
package extract

import "context"

// Extractor is implemented by every file-type handler.
type Extractor interface {
    // Extract converts the file at localPath into text.
    // The presignedURL is provided for extractors that call external APIs
    // (e.g., OCR) and need the original URL rather than a local file.
    Extract(ctx context.Context, job Job) (Result, error)

    // SupportedTypes returns the MIME types this extractor handles.
    SupportedTypes() []string

    // SupportedExtensions returns file extensions (with leading dot).
    SupportedExtensions() []string

    // Name returns a human-readable name for logging/metrics.
    Name() string

    // MaxFileSize returns the maximum file size in bytes (0 = no limit).
    MaxFileSize() int64
}
```

### 0.2 — Unified Job & Result Types

```go
// internal/extract/result.go
package extract

type Job struct {
    PresignedURL string            // Original URL (for API-based extractors)
    LocalPath    string            // Downloaded temp file path
    FileName     string            // Original filename (for extension detection)
    MIMEType     string            // Detected MIME type
    FileSize     int64             // File size in bytes
    Options      map[string]any    // Extractor-specific options
}

type Result struct {
    Success     bool              `json:"success"`
    Text        string            `json:"text"`           // Primary extracted text (markdown)
    Method      string            `json:"method"`         // "text-layer", "ocr", "native", "libreoffice", "whisper", etc.
    FileType    string            `json:"fileType"`       // Detected file type category
    MIMEType    string            `json:"mimeType"`       // Detected MIME type
    Pages       []PageResult      `json:"pages,omitempty"`
    Metadata    map[string]string `json:"metadata,omitempty"` // Title, author, dates, etc.
    WordCount   int               `json:"wordCount"`
    CharCount   int               `json:"charCount"`
    Error       *string           `json:"error,omitempty"`

}

type PageResult struct {
    PageNumber int    `json:"pageNumber"`
    Text       string `json:"text"`
    Method     string `json:"method"`
    WordCount  int    `json:"wordCount"`
}
```

### 0.3 — File Type Registry & Router

```go
// internal/extract/registry.go
package extract

// Registry maps MIME types and extensions to extractors.
type Registry struct {
    byMIME      map[string]Extractor
    byExtension map[string]Extractor
}

func NewRegistry() *Registry { ... }
func (r *Registry) Register(e Extractor) { ... }
func (r *Registry) Resolve(mimeType, extension string) (Extractor, error) { ... }
```

**MIME detection strategy** (in order):
1. Read first 512 bytes → `http.DetectContentType()` (Go stdlib)
2. Use `github.com/gabriel-vasile/mimetype` for deeper sniffing (supports 170+ types)
3. Fall back to file extension from original filename

### 0.4 — Unified API Endpoint

A new route alongside the existing ones:

```
POST /extract          ← NEW universal endpoint
POST /pdf/extract      ← Existing (preserved for backward compat)
POST /pdf/preview      ← Existing (preserved)
POST /image/extract    ← Existing (preserved)
```

Request body for `/extract`:
```json
{
  "presignedUrl": "https://...",      // OR the worker resolves from "key"
  "fileName": "quarterly-report.docx", // Original filename (for type detection)
  "options": {                         // Optional, extractor-specific
    "includeMetadata": true,
    "maxPages": 50,
    "language": "en"
  }
}
```

Response (unified):
```json
{
  "success": true,
  "text": "# Quarterly Report\n\n...",
  "method": "native",
  "fileType": "document/docx",
  "mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "wordCount": 4521,
  "charCount": 28340,
  "metadata": {
    "title": "Q3 2025 Report",
    "author": "Jane Doe",
    "created": "2025-09-15T10:00:00Z"
  }
}
```

---

## Phase 1: Document Formats (Office, OpenDocument, eBook)

### 1.1 — DOCX Extraction (Pure Go)

| Detail | Value |
|--------|-------|
| Extensions | `.docx` |
| MIME | `application/vnd.openxmlformats-officedocument.wordprocessingml.document` |
| Approach | Pure Go — unzip → parse `word/document.xml` → walk `<w:p>` / `<w:r>` / `<w:t>` nodes |
| Library | None needed (stdlib `archive/zip` + `encoding/xml`) |
| Metadata | `docProps/core.xml` → title, author, created, modified |
| Tables | Convert `<w:tbl>` to markdown tables |
| Headers/Footers | Optional extraction from `word/header*.xml`, `word/footer*.xml` |
| Images | Skip (not useful for RAG text) |
| Embedded objects | Extract text from embedded XLSX/charts if present |
| Output | Clean markdown with headings, lists, tables, paragraphs |

**RAG optimization:**
- Preserve heading hierarchy (`#`, `##`, `###`) for chunk boundaries
- Convert numbered/bulleted lists to markdown lists
- Convert tables to markdown tables (pipe format)
- Strip decorative elements (text boxes with no content, shapes)

### 1.2 — XLSX Extraction (Pure Go)

| Detail | Value |
|--------|-------|
| Extensions | `.xlsx` |
| MIME | `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` |
| Approach | Pure Go — use `github.com/xuri/excelize/v2` |
| Output | Each sheet → markdown table with header row |

**RAG optimization:**
- Sheet name as `## Sheet: {name}` heading
- Convert each sheet to a markdown table
- Skip empty rows/columns
- Format numbers/dates as human-readable strings
- For very large sheets (>1000 rows), summarize with first 100 rows + note

### 1.3 — PPTX Extraction (Pure Go)

| Detail | Value |
|--------|-------|
| Extensions | `.pptx` |
| MIME | `application/vnd.openxmlformats-officedocument.presentationml.presentation` |
| Approach | Pure Go — unzip → parse `ppt/slides/slide*.xml` → extract `<a:t>` text nodes |
| Metadata | `docProps/core.xml` |
| Speaker notes | Extract from `ppt/notesSlides/notesSlide*.xml` |
| Output | `[Slide N]\n\n{title}\n\n{body}\n\n{speaker notes}` |

**RAG optimization:**
- Slide number markers for chunking boundaries
- Speaker notes often contain the most valuable content — always include
- Preserve bullet hierarchies
- Extract text from tables and charts

### 1.4 — Legacy Office (DOC/XLS/PPT) via LibreOffice

| Detail | Value |
|--------|-------|
| Extensions | `.doc`, `.xls`, `.ppt` |
| MIME | `application/msword`, `application/vnd.ms-excel`, `application/vnd.ms-powerpoint` |
| Approach | LibreOffice headless: `soffice --headless --convert-to txt:Text` (or convert to DOCX first, then use native parser) |
| Docker | Add `libreoffice-core libreoffice-writer libreoffice-calc libreoffice-impress` to Dockerfile |
| Timeout | 60s per file (LibreOffice can be slow) |
| Fallback | If LibreOffice fails, attempt with `catdoc`/`xls2csv`/`catppt` |

**Note:** Legacy formats are binary and complex. LibreOffice is the only reliable option. Consider running in a subprocess with resource limits.

### 1.5 — OpenDocument (ODT/ODS/ODP)

| Detail | Value |
|--------|-------|
| Extensions | `.odt`, `.ods`, `.odp` |
| MIME | `application/vnd.oasis.opendocument.*` |
| Approach | Pure Go — unzip → parse `content.xml` → walk ODF elements |
| Similar to | DOCX/XLSX/PPTX but uses ODF XML schema |

### 1.6 — EPUB (eBook)

| Detail | Value |
|--------|-------|
| Extensions | `.epub` |
| MIME | `application/epub+zip` |
| Approach | Pure Go — unzip → parse `META-INF/container.xml` → find OPF → read spine order → extract XHTML → strip HTML to text |
| Metadata | OPF file → title, author, publisher, ISBN |
| Output | Chapter-separated markdown with headings preserved |

**RAG optimization:**
- Chapters as natural chunk boundaries
- Preserve heading hierarchy
- Strip CSS, SVG, images
- Table of contents as metadata

### 1.7 — RTF (Rich Text Format)

| Detail | Value |
|--------|-------|
| Extensions | `.rtf` |
| MIME | `application/rtf` |
| Approach | Pure Go RTF parser — strip control words, extract text content |
| Library | `github.com/IntelligenceX/rtfparse` or custom parser |
| Fallback | LibreOffice headless |

---

## Phase 2: Plain Text & Code Files

### 2.1 — Plain Text Passthrough

| Extensions | `.txt`, `.text`, `.log`, `.ini`, `.cfg`, `.conf`, `.env.example`, `.properties`, `.gitignore`, `.dockerignore`, `.editorconfig` |
| Approach | Direct read with encoding detection |

**Processing:**
1. Detect encoding (UTF-8, UTF-16, Latin-1, etc.) via BOM or `golang.org/x/text/encoding`
2. Convert to UTF-8
3. Normalize line endings
4. Trim excessive whitespace
5. Cap at configurable max chars (e.g., 500KB of text)

### 2.2 — Markdown Passthrough

| Extensions | `.md`, `.mdx`, `.markdown` |
| Approach | Direct read — already in optimal format for RAG |

**Processing:**
- Strip front matter (YAML between `---` delimiters)
- Strip image references (not useful for text RAG)
- Preserve headings, lists, code blocks, tables
- Normalize line endings

### 2.3 — Source Code (Comprehensive)

This is a learning platform — students and educators upload code constantly. Broad language coverage is essential.

**General-purpose languages:**
| Category | Extensions |
|----------|------------|
| Python | `.py`, `.pyw`, `.pyi`, `.pyx` |
| JavaScript/TypeScript | `.js`, `.jsx`, `.ts`, `.tsx`, `.mjs`, `.cjs`, `.mts`, `.cts` |
| Go | `.go` |
| Java/JVM | `.java`, `.kt`, `.kts`, `.scala`, `.groovy`, `.gradle` |
| C/C++ | `.c`, `.h`, `.cpp`, `.hpp`, `.cc`, `.cxx`, `.hxx`, `.c++`, `.h++` |
| C# / .NET | `.cs`, `.fs`, `.fsx`, `.vb` |
| Ruby | `.rb`, `.erb`, `.rake`, `.gemspec` |
| PHP | `.php`, `.phtml` |
| Swift/Objective-C | `.swift`, `.m`, `.mm` |
| Rust | `.rs` |
| Dart/Flutter | `.dart` |
| Elixir/Erlang | `.ex`, `.exs`, `.erl`, `.hrl` |
| Haskell | `.hs`, `.lhs` |
| OCaml/ML | `.ml`, `.mli` |
| Clojure/Lisp | `.clj`, `.cljs`, `.cljc`, `.lisp`, `.el`, `.scm`, `.rkt` |
| Lua | `.lua` |
| R | `.r`, `.R`, `.Rmd` |
| Julia | `.jl` |
| Perl | `.pl`, `.pm`, `.t` |
| Zig | `.zig` |
| Nim | `.nim`, `.nims` |
| V | `.v` |
| Crystal | `.cr` |
| D | `.d` |
| Ada | `.adb`, `.ads` |

**Systems & low-level:**
| Category | Extensions |
|----------|------------|
| Assembly | `.asm`, `.s`, `.S` |
| VHDL/Verilog | `.vhdl`, `.vhd`, `.sv`, `.svh` |
| CUDA | `.cu`, `.cuh` |
| WASM text | `.wat`, `.wast` |

**Scripting & shell:**
| Category | Extensions |
|----------|------------|
| Shell | `.sh`, `.bash`, `.zsh`, `.fish`, `.ksh`, `.csh` |
| PowerShell | `.ps1`, `.psm1`, `.psd1` |
| Batch | `.bat`, `.cmd` |
| Makefile | `Makefile`, `.mk` |

**Data & query languages:**
| Category | Extensions |
|----------|------------|
| SQL | `.sql`, `.psql`, `.mysql`, `.sqlite` |
| GraphQL | `.graphql`, `.gql` |
| Cypher | `.cypher` |

**Schema & config-as-code:**
| Category | Extensions |
|----------|------------|
| Protobuf | `.proto` |
| Terraform/HCL | `.tf`, `.hcl`, `.tfvars` |
| Nix | `.nix` |
| Dockerfile | `Dockerfile`, `.dockerfile` |
| Docker Compose | `docker-compose.yml`, `compose.yml` |

**Notebook & literate programming:**
| Category | Extensions |
|----------|------------|
| Jupyter | `.ipynb` (special: parse JSON, extract code + markdown cells) |
| R Markdown | `.Rmd` (treat as markdown with embedded R) |
| Quarto | `.qmd` |

**Academic & scientific:**
| Category | Extensions |
|----------|------------|
| LaTeX | `.tex`, `.sty`, `.cls`, `.bib` |
| MATLAB/Octave | `.m` (when not Objective-C context) |
| Mathematica | `.wl`, `.nb` (text-extractable portions) |

**Processing (all source code):**
1. Detect language from extension → map to markdown language tag
2. Wrap in markdown code fence: `` ```python\n...\n``` ``
3. Prepend a metadata comment: `<!-- lang: python, lines: 342 -->`
4. For very large files (>10K lines): extract first 50 lines (imports/header) + all function/class/method signatures + docstrings
5. For `.ipynb` notebooks: parse JSON, extract each cell as either markdown (passthrough) or code (fenced), preserving cell order
6. For `.tex` files: strip LaTeX commands, extract text content + math equations, preserve `\section`/`\subsection` as markdown headings

**RAG optimization:**
- Language tag enables code-aware chunking
- Function/class signatures are high-value for search
- Docstrings/comments carry semantic meaning
- Notebook cell boundaries are natural chunk points
- LaTeX section structure maps directly to heading hierarchy

### 2.4 — HTML → Plain Text

| Extensions | `.html`, `.htm`, `.xhtml`, `.mhtml` |
| MIME | `text/html` |
| Approach | Go HTML parser → text extraction |
| Library | `golang.org/x/net/html` |

**Processing:**
1. Parse HTML DOM
2. Extract `<title>`, `<meta name="description">`, Open Graph tags → metadata
3. Extract main content (heuristic: largest `<article>`, `<main>`, or `<body>` block)
4. Convert headings to markdown headings
5. Convert `<table>` to markdown tables
6. Convert `<ul>`/`<ol>` to markdown lists
7. Strip `<script>`, `<style>`, `<nav>`, `<footer>`, `<aside>` (noise for RAG)
8. Strip all remaining HTML tags

### 2.5 — Structured Data Formats

**JSON** (`.json`, `.jsonl`, `.geojson`):
- Pretty-print with 2-space indent
- For large arrays, show schema of first object + count
- For JSONL, process line by line

**YAML** (`.yaml`, `.yml`):
- Direct read (already human-readable)
- Strip comments if too noisy

**XML** (`.xml`, `.svg`, `.xsl`, `.xsd`, `.plist`):
- Extract text content from elements
- Preserve structure as indented text
- For SVG, extract `<text>` elements only

**TOML** (`.toml`):
- Direct read (already human-readable)

**CSV/TSV** (`.csv`, `.tsv`):
- Convert to markdown table
- Auto-detect delimiter (comma, tab, pipe, semicolon)
- For large files (>500 rows): include header + first 200 rows + `... and N more rows`
- Include row/column count in metadata

---

## Phase 3: Audio & Video Transcription

### 3.1 — Audio Files

| Extensions | `.mp3`, `.wav`, `.m4a`, `.ogg`, `.flac`, `.aac`, `.wma`, `.opus`, `.webm` (audio) |
| Approach | External API — OpenAI Whisper or Groq Whisper |

**Architecture:**
```
Download audio → detect format → (optional: convert to wav/mp3 via ffmpeg) → send to Whisper API → receive transcript
```

**Config additions:**
```go
WhisperAPIKey     string
WhisperAPIURL     string        // Default: OpenAI, configurable for Groq
WhisperModel      string        // "whisper-1" or "whisper-large-v3"
MaxAudioDuration  time.Duration // e.g., 30 minutes
MaxAudioBytes     int64         // e.g., 100MB
```

**Processing:**
1. Validate file format and size
2. If format not supported by Whisper API directly, convert via `ffmpeg` to mp3
3. Send to Whisper API with `response_format=verbose_json` (includes timestamps)
4. Format transcript as markdown with optional timestamp markers

**Output:**
```markdown
## Audio Transcript

**Duration:** 12:34
**Language:** English (detected)

---

[00:00] Welcome everyone to today's meeting. We're going to discuss the quarterly results...

[02:15] Moving on to the sales numbers...
```

**RAG optimization:**
- Timestamp markers as chunk boundaries
- Language detection in metadata
- Speaker diarization if API supports it

**Docker:** Add `ffmpeg` to Dockerfile.

### 3.2 — Video Files

| Extensions | `.mp4`, `.mkv`, `.avi`, `.mov`, `.webm` (video), `.m4v`, `.flv`, `.wmv` |
| Approach | Extract audio track → transcribe via Whisper |

**Processing:**
1. Use `ffmpeg` to extract audio: `ffmpeg -i input.mp4 -vn -acodec mp3 -ab 128k output.mp3`
2. Feed extracted audio to same Whisper pipeline as 3.1
3. Optionally extract keyframes for vision description (future enhancement)

**Config:**
```go
MaxVideoBytes    int64          // e.g., 500MB
MaxVideoDuration time.Duration  // e.g., 60 minutes
```

**Note:** Audio/video transcription is the final phase because it requires the most expensive external dependencies (Whisper API, ffmpeg) and is less commonly needed in a learning context than document and code extraction. Implement Phases 0–2 first to cover the majority of learning-platform uploads.

---

## Phase 4: Infrastructure Changes

### 4.1 — Dockerfile Update

```dockerfile
FROM golang:1.25.6-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/fileproc ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends --no-install-suggests \
    poppler-utils \
    ca-certificates \
    ffmpeg \
    libreoffice-core \
    libreoffice-writer \
    libreoffice-calc \
    libreoffice-impress \
 && rm -rf /var/lib/apt/lists/*

# LibreOffice profile dir to avoid initialization delays
RUN mkdir -p /tmp/.libreoffice && \
    soffice --headless --convert-to txt --outdir /tmp /dev/null 2>/dev/null || true

WORKDIR /app
COPY --from=build /out/fileproc /app/fileproc
ENV PORT=8080
EXPOSE 8080
CMD ["/app/fileproc"]
```

### 4.2 — New Go Dependencies

```
github.com/xuri/excelize/v2          # XLSX parsing
github.com/gabriel-vasile/mimetype   # MIME type detection (170+ types)
golang.org/x/net/html                # HTML parsing
golang.org/x/text                    # Encoding detection/conversion
```

### 4.3 — Config Additions

```go
// New in config.go
MaxFileBytes         int64         // Universal max (default: 500MB)
MaxAudioBytes        int64         // Audio max (default: 100MB)
MaxVideoBytes        int64         // Video max (default: 500MB)
MaxCodeFileBytes     int64         // Source code max (default: 10MB)

UniversalExtractTimeout time.Duration  // Default: 300s

// API keys
WhisperAPIKey        string        // For audio/video transcription
WhisperAPIURL        string        // Default: OpenAI
WhisperModel         string        // Default: "whisper-1"

// LibreOffice
LibreOfficeTimeout   time.Duration // Default: 60s
LibreOfficeBinary    string        // Default: "soffice"

// FFmpeg
FFmpegTimeout        time.Duration // Default: 120s
FFmpegBinary         string        // Default: "ffmpeg"
```

### 4.4 — Worker Route Updates

New route in `worker/src/constants.ts`:
```typescript
export const ROUTES = {
  HEALTH: "/health",
  PREVIEW: "/api/pdf/preview",
  EXTRACT: "/api/pdf/extract",
  IMAGE_EXTRACT: "/api/image/extract",
  FILE_PRESIGN: "/api/file/presign",
  UNIVERSAL_EXTRACT: "/api/extract",        // ← NEW
} as const;

export const CONTAINER = {
  // ... existing ...
  UNIVERSAL_EXTRACT_URL: "http://container/extract",  // ← NEW
} as const;
```

New route in `worker/src/index.ts` — identical pattern to existing routes:
1. Rate limit
2. Parse body → resolve URL from `key` or `presignedUrl`
3. Pass `fileName` through for type detection
4. Proxy to container

---

## Complete File Type Support Matrix

| Category | Extensions | Method | Pure Go? | External Deps |
|----------|-----------|--------|----------|---------------|
| **PDF** | `.pdf` | Text-layer + OCR hybrid | Partial | `poppler-utils`, Mistral API |
| **Images** | `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp`, `.tiff`, `.tif`, `.avif`, `.svg` | Vision + OCR | No | Mistral API, OpenRouter API |
| **Word** | `.docx` | XML parsing | Yes | None |
| **Word (legacy)** | `.doc` | LibreOffice | No | LibreOffice |
| **Excel** | `.xlsx` | excelize | Yes | `excelize` module |
| **Excel (legacy)** | `.xls` | LibreOffice | No | LibreOffice |
| **PowerPoint** | `.pptx` | XML parsing | Yes | None |
| **PowerPoint (legacy)** | `.ppt` | LibreOffice | No | LibreOffice |
| **OpenDocument** | `.odt`, `.ods`, `.odp` | XML parsing | Yes | None |
| **RTF** | `.rtf` | Text parsing | Yes | None |
| **EPUB** | `.epub` | XHTML parsing | Yes | None |
| **Plain text** | `.txt`, `.text`, `.log`, `.ini`, `.cfg`, `.conf`, `.env`, `.properties` | Direct read | Yes | None |
| **Markdown** | `.md`, `.mdx`, `.markdown` | Direct read | Yes | None |
| **Source code** | `.py`, `.js`, `.ts`, `.go`, `.java`, `.c`, `.cpp`, `.rs`, `.rb`, `.php`, `.swift`, `.kt`, `.cs`, `.scala`, `.r`, `.jl`, `.lua`, `.sh`, `.sql`, `.dart`, `.ex`, `.hs`, `.clj`, `.zig`, `.nim`, `.cu`, +60 more | Direct read + lang tag | Yes | None |
| **HTML** | `.html`, `.htm`, `.xhtml` | DOM parse + strip | Yes | `x/net/html` |
| **CSV/TSV** | `.csv`, `.tsv` | Table conversion | Yes | None |
| **JSON** | `.json`, `.jsonl` | Pretty-print | Yes | None |
| **YAML** | `.yaml`, `.yml` | Direct read | Yes | None |
| **XML** | `.xml` | Text extraction | Yes | None |
| **TOML** | `.toml` | Direct read | Yes | None |
| **Jupyter notebooks** | `.ipynb` | JSON parse → cell extraction | Yes | None |
| **LaTeX** | `.tex`, `.sty`, `.cls`, `.bib` | Command stripping | Yes | None |
| **Audio** | `.mp3`, `.wav`, `.m4a`, `.ogg`, `.flac`, `.aac`, `.wma`, `.opus` | Whisper API | No | `ffmpeg`, Whisper API |
| **Video** | `.mp4`, `.mkv`, `.avi`, `.mov`, `.webm`, `.m4v` | Audio extract + Whisper | No | `ffmpeg`, Whisper API |

**Total: ~90+ extensions across 7 categories.**

---

## Implementation Phases & Estimates

| Phase | Scope | Depends On | Estimated Effort |
|-------|-------|-----------|-----------------|
| **0** | Core abstraction (interface, registry, router, unified endpoint) | — | 2–3 days |
| **1A** | DOCX, XLSX, PPTX (pure Go, modern Office) | Phase 0 | 3–4 days |
| **1B** | Legacy DOC/XLS/PPT (LibreOffice), RTF, ODT/ODS/ODP, EPUB | Phase 0 | 2–3 days |
| **2A** | Plain text, markdown, HTML, CSV, JSON, YAML, XML, TOML | Phase 0 | 2 days |
| **2B** | Source code (90+ extensions), Jupyter notebooks, LaTeX | Phase 0 | 2–3 days |
| **3** | Audio transcription (Whisper), video (ffmpeg + Whisper) | Phase 0 | 2–3 days |
| **4** | Worker updates, Dockerfile, config, integration tests | All | 2–3 days |
| **5** | RAG formatting polish, chunking hints, metadata enrichment | Phase 1–3 | 2 days |

**Total: ~15–21 days of focused implementation.**

---

## RAG Output Formatting Principles

All extractors feed into the existing `format/` package (expanded). Every output follows these rules:

1. **Markdown format** — Universal, chunk-friendly, heading-aware
2. **Heading hierarchy** — `#` for document title, `##` for sections/sheets/slides, `###` for subsections
3. **Page/section markers** — `---` separators between logical sections (pages, slides, chapters, files)
4. **Tables as pipe tables** — Chunk-friendly, searchable
5. **Code in fences** — Language-tagged for search relevance
6. **Metadata as frontmatter** — Title, author, date in structured header block
7. **No decorative content** — Strip images, shapes, colors, fonts
8. **Max output size** — Configurable cap (default: 500KB text) with truncation notice
9. **UTF-8 normalized** — All outputs in UTF-8 with normalized whitespace

**Example output structure:**
```markdown
---
title: Quarterly Report
author: Jane Doe
date: 2025-09-15
source: quarterly-report.docx
type: document/docx
---

# Quarterly Report

## Executive Summary

Revenue increased 15% year-over-year...

## Financial Results

| Metric | Q3 2025 | Q3 2024 | Change |
|--------|---------|---------|--------|
| Revenue | $12.5M | $10.9M | +15% |
| EBITDA | $3.2M | $2.8M | +14% |

---

## Appendix

...
```

---

## Error Handling & Graceful Degradation

Each extractor implements a fallback chain:

```
Primary method → Fallback method → Error with actionable message
```

Examples:
- DOCX: native parse → LibreOffice → error
- Audio: direct Whisper → ffmpeg convert + Whisper → error
- Legacy Office: LibreOffice → error
- Jupyter notebook: JSON parse → extract cells → format as markdown → error
- LaTeX: regex-based stripping → plain text fallback → error

---

## Capacity & Resource Management

### Semaphore Strategy

```go
requestSem       // Existing: total concurrent requests
ocrSem           // Existing: OCR API calls
libreOfficeSem   // NEW: LibreOffice processes (heavy, limit to 2)
whisperSem       // NEW: Whisper API calls (expensive, limit to 2)
ffmpegSem        // NEW: ffmpeg processes (CPU-heavy, limit to 3)
```

### Temp File Cleanup

All extractors follow the existing pattern:
```go
tmpDir, cleanup, err := downloadToTemp(ctx, url, maxBytes, timeout)
defer cleanup()
```

---

## Testing Strategy

1. **Unit tests per extractor** — fixtures for each format with known expected text
2. **Integration tests** — Full endpoint tests with sample files
3. **Fuzz testing** — Malformed files should return errors, never panic
4. **Benchmark tests** — Track extraction speed per format
5. **Fixture files** — `testdata/` directory with sample files for each supported format

---

## Migration Path

1. **Phase 0 ships first** — new `/extract` endpoint alongside existing routes
2. **Existing routes preserved** — `/pdf/extract`, `/pdf/preview`, `/image/extract` continue working
3. **Worker detects file type** — Can auto-route to correct old or new endpoint
4. **Frontend migrates gradually** — Switch to `/api/extract` at their pace
5. **Old routes deprecated** — Remove after all clients migrate (semver major bump)

---

## Summary

This upgrade transforms the service from a **PDF + image** processor into a **universal file-to-text** pipeline tailored for a learning platform. The abstraction layer (Phase 0) ensures every new format plugs in cleanly. Pure Go implementations for modern formats keep the Docker image lean and fast, while CLI tools handle legacy formats and media.

**Prioritization rationale for a learning platform:**
- **Documents first** (Phase 1) — students submit DOCX, PPTX, XLSX constantly
- **Code & text next** (Phase 2) — source code, notebooks, LaTeX, and data files are core to STEM education
- **Audio/video last** (Phase 3) — lecture recordings are valuable but less frequent and more expensive to process
- **Email/calendar excluded** — not relevant to learning workflows
- **Archives excluded** — adds complexity disproportionate to value; users can upload individual files

The unified API simplifies the frontend integration to a single endpoint regardless of file type.

---

## Implementation Status — COMPLETED

All phases of this upgrade plan have been implemented. Legacy endpoints and backwards compatibility have been fully removed.

### Completed Phases

| Phase | Description | Status |
|-------|-------------|--------|
| **Phase 0** | Core abstraction layer (`Extractor` interface, `Registry`, `Router`, unified `/extract` endpoint, download helper) | ✅ Complete |
| **Phase 1A** | Office documents — DOCX (headings, tables, lists, metadata), XLSX (multi-sheet markdown tables, row filtering), PPTX (speaker notes, slide structure, metadata) | ✅ Complete |
| **Phase 1B** | Legacy Office (DOC/XLS/PPT via LibreOffice), RTF, OpenDocument (ODT/ODS/ODP with proper ODF XML walking), EPUB (OPF spine ordering, metadata) | ✅ Complete |
| **Phase 2A** | Structured data — CSV, JSON, XML, YAML with dedicated extractors | ✅ Complete |
| **Phase 2B** | Source code (90+ extensions), Jupyter notebooks, LaTeX with dedicated extractors | ✅ Complete |
| **Phase 3** | Audio transcription (Whisper API), video transcription (ffmpeg + Whisper) with shared `internal/transcribe` client | ✅ Complete |
| **Phase 4** | Worker routes, Dockerfile (ffmpeg, libreoffice), config wiring, README | ✅ Complete |
| **Phase 5** | RAG metadata frontmatter in DOCX, PPTX, OpenDocument, EPUB extractors | ✅ Complete |

### Legacy Removal (Breaking Change)

The following legacy endpoints and code have been **permanently removed**:

**Go server (`cmd/server/main.go`):**
- `/pdf/extract` route and `handleExtract()` handler
- `/pdf/preview` route and `handlePreview()` handler
- `/image/extract` route and `handleImageExtract()` handler
- `downloadPDFToTemp()`, `validatePDFMagic()`, `validateExtractRequest()`, `validateImageRequest()` helpers
- Direct imports of `internal/image` and `internal/types` from main.go

**Worker (`worker/src/index.ts`):**
- `ROUTES.PREVIEW` (`/api/pdf/preview`)
- `ROUTES.EXTRACT` (`/api/pdf/extract`) — replaced by `ROUTES.EXTRACT` (`/api/extract`)
- `ROUTES.IMAGE_EXTRACT` (`/api/image/extract`)
- `resolvePdfUrl()` and `resolveImageUrl()` helpers
- Dual rate limiters (`RATE_LIMITER_PREVIEW`, `RATE_LIMITER_EXTRACT`) consolidated to single `RATE_LIMITER`

**Config (`internal/config/config.go`):**
- `ExtractTimeout`, `PreviewTimeout`, `ImageExtractTimeout`, `MaxImageURLLen` fields removed

**Wrangler (`wrangler.jsonc`):**
- Dual rate limiter bindings consolidated to single `RATE_LIMITER`

### Current API Surface

| Route | Method | Description |
|-------|--------|-------------|
| `/health` | GET | Health check (no auth) |
| `/metrics` | GET | Server metrics (internal auth) |
| `/api/extract` | POST | Universal file extraction — all formats (rate limited, internal auth) |
| `/api/file/presign` | POST | R2 presigned URL generation (rate limited) |

### Frontend Migration Required

The frontend must be updated to use the universal `/api/extract` endpoint for ALL file types. The request shape is:

```json
{
  "presignedUrl": "https://...",
  "filename": "document.docx"
}
```

Or with R2 key:

```json
{
  "key": "user/abc123/document.docx"
}
```

The response shape is consistent across all file types — see `internal/extract/result.go` for `ExtractionResult`.
