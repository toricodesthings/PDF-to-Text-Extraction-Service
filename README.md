# File Processing Service (v2)

Universal file-to-text extraction service for RAG ingestion.

This repository exposes two public file APIs:
- `POST /api/preview` (low-cost preview only)
- `POST /api/extract` (full universal extraction)

Legacy format-specific public endpoints (`/api/pdf/preview`, `/api/pdf/extract`, `/api/image/extract`) and legacy internal routes were removed.

---

## Architecture

Runtime layers:
- **Cloudflare Worker** (`worker/src/index.ts`): public API, rate limiting, R2 key validation/presigning, container lifecycle/health.
- **Go extraction container** (`cmd/server/main.go`): download, MIME/extension routing, format-specific extraction.

Universal extraction flow:
1. Client calls `POST /api/preview` or `POST /api/extract` with `presignedUrl` or `key`.
2. Worker resolves `key` (if provided) to a short-lived presigned URL.
3. Worker proxies to container `POST /preview` or `POST /extract` with internal auth.
4. Container downloads to temp file, detects MIME, resolves extractor by extension/MIME.
5. Extractor returns normalized unified result (`success`, `text`, `method`, `fileType`, `mimeType`, counts, optional metadata/pages).

Core packages:
- `internal/extract/` — extractor interface, registry, router, download, unified result types.
- `internal/extractors/*` — format-specific extractors.

---

## Public API (Worker)

### `GET /health`
Container-aware health check.

Example success:
```json
{ "status": "healthy", "active": 0, "version": "2.0.0" }
```

Example degraded:
```json
{ "status": "degraded", "active": 14, "version": "2.0.0" }
```

### `POST /api/preview`
Low-cost preview endpoint. It returns preview text only for formats that do **not** require paid OCR/vision/transcription.

Request body (same shape as `/api/extract`):
```json
{
  "presignedUrl": "https://...",
  "fileName": "report.pdf",
  "options": {
    "previewMaxChars": 12000,
    "previewMaxPages": 6
  }
}
```

Preview rules:
- PDF preview is **text-layer only** (`method: "preview-text-layer"`), no OCR execution.
- Image/audio/video and other paid/inference paths are rejected.
- Supported preview families include: PDF text layer, DOCX/XLSX/PPTX, OpenDocument, EPUB, RTF, HTML, plain text/markdown/config, structured formats, source code/notebooks/LaTeX.
- Response uses the same unified extract result envelope.

Preview-specific options:
- `previewMaxChars` (default from server config)
- `previewMaxPages` (PDF preview only)

### `POST /api/extract`
Universal extraction endpoint for all supported file types.

Request body:
```json
{
  "presignedUrl": "https://...",
  "fileName": "report.docx",
  "options": {
    "timestamps": true,
    "language": "en"
  }
}
```

You may send either:
- `presignedUrl` (string), or
- `key` (string; Worker resolves to presigned URL; must start with `user/` or `tests/`)

Fields:
- `presignedUrl` *(required unless `key` is provided)*
- `key` *(optional)*
- `fileName` *(optional but strongly recommended for better extension-based routing)*
- `options` *(optional, forwarded to extractor as `map[string]any`)*

Success response shape:
```json
{
  "success": true,
  "text": "...",
  "method": "native",
  "fileType": "document/docx",
  "mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "wordCount": 1234,
  "charCount": 9876,
  "metadata": {
    "title": "Q4 Report"
  },
  "pages": [
    { "pageNumber": 1, "text": "...", "method": "hybrid", "wordCount": 300 }
  ]
}
```

Failure behavior:
- Worker-layer validation/rate-limit failures use:
  ```json
  { "success": false, "error": "...", "code": "bad_request" }
  ```
- Extractor/router failures from container return unified extract result with `success: false` and `error` (no `code` field).

### `POST /api/file/presign`
Generates an R2 presigned URL for an existing object.

Request:
```json
{
  "key": "user/abc/files/123",
  "expiresIn": 600
}
```

Response:
```json
{
  "success": true,
  "presignedUrl": "https://..."
}
```

Rules:
- `key` must pass Worker validation (`user/` or `tests/`, no `..`, no backslash).
- `expiresIn` is clamped to `[60, 3600]` seconds; default `600`.

---

## Internal API (Container)

- `GET /health` (no internal auth)
- `GET /metrics` (requires `X-Internal-Auth`)
- `POST /preview` (requires `X-Internal-Auth`)
- `POST /extract` (requires `X-Internal-Auth`)

Internal auth header:
- `X-Internal-Auth: <INTERNAL_SHARED_SECRET>`

---

## Current response patterns

### Standard Worker error envelope
Used by Worker-native failures and middleware errors:
```json
{ "success": false, "error": "...", "code": "..." }
```

Common `code` values:
- `bad_request`
- `rate_limit`
- `not_found`
- `timeout`
- `request_too_large`
- `internal_error`
- `unauthorized`
- `method_not_allowed`
- `capacity`
- `validation_failed`

### Universal extraction result envelope
Used by successful extraction and extractor/router-level failures:
```json
{
  "success": true,
  "text": "...",
  "method": "native|hybrid|code|groq|ffmpeg+groq|vision|ocr|ocr+vision|libreoffice",
  "fileType": "...",
  "mimeType": "...",
  "wordCount": 0,
  "charCount": 0,
  "metadata": {},
  "pages": []
}
```

When extraction fails at router/extractor level:
```json
{
  "success": false,
  "fileType": "unknown",
  "mimeType": "...",
  "error": "..."
}
```

---

## Supported formats (registered extractors)

### Documents
- PDF: `.pdf` (`document/pdf`, method `hybrid`)
- Office OpenXML:
  - DOCX `.docx`
  - XLSX `.xlsx`
  - PPTX `.pptx`
- Legacy Office via LibreOffice:
  - `.doc`, `.xls`, `.ppt` (method `libreoffice`)
- OpenDocument:
  - `.odt`, `.ods`, `.odp`
- EPUB: `.epub`
- RTF: `.rtf`
- HTML: `.html`, `.htm`, `.xhtml`, `.mhtml`

### Images
- `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.bmp`, `.tiff`, `.tif`, `.svg`, `.avif`
- Method depends on classifier path: `ocr`, `vision`, or `ocr+vision`.

### Plain text / markdown / config
- `.txt`, `.text`, `.log`, `.ini`, `.cfg`, `.conf`, `.env`, `.properties`
- `.gitignore`, `.dockerignore`, `.editorconfig`, `.env.example`
- `.md`, `.mdx`, `.markdown`

### Structured data
- CSV/TSV: `.csv`, `.tsv`
- JSON: `.json`, `.jsonl`, `.geojson`
- XML: `.xml`, `.xsd`, `.xsl`, `.svg`, `.plist`
- YAML/TOML: `.yaml`, `.yml`, `.toml`

### Code and notebooks
- Source code (broad set including Python/JS/TS/Go/Java/C/C++/C#/Rust/etc.)
- Infra/query formats: `.sql`, `.graphql`, `.proto`, `.tf`, `.hcl`, `.tfvars`, `.nix`
- Notebook: `.ipynb`
- LaTeX: `.tex`, `.sty`, `.cls`, `.bib`

### Media transcription
- Audio: `.mp3`, `.wav`, `.m4a`, `.ogg`, `.flac`, `.aac`, `.wma`, `.opus`, `.webm` (method `groq`)
- Video: `.mp4`, `.mkv`, `.avi`, `.mov`, `.webm`, `.m4v`, `.flv`, `.wmv` (method `ffmpeg+groq`)

---

## Running locally

Prereqs:
- Go `1.25.6+`
- Node.js + npm
- Cloudflare Wrangler
- Poppler (`pdfinfo`, `pdftotext`)
- LibreOffice (`soffice`) for legacy Office extraction
- `ffmpeg` for video extraction

Install dependencies:
```bash
npm install
```

Run Worker (recommended):
```bash
npx wrangler dev
```

Run container server directly:
```bash
INTERNAL_SHARED_SECRET="your-32+char-secret" go run ./cmd/server
```

---

## Environment variables

### Required
- `INTERNAL_SHARED_SECRET` (must be at least 32 chars)

### API keys
- `MISTRAL_API_KEY` — OCR
- `OPENROUTER_API_KEY` — image classification/vision
- `GROQ_API_KEY` — audio/video transcription

### Key limits/timeouts (defaults)
- `PORT=8080`
- `MAX_JSON_BODY_BYTES=2MiB`
- `MAX_PDF_BYTES=200MiB`
- `MAX_FILE_BYTES=500MiB`
- `MAX_AUDIO_BYTES=100MiB`
- `MAX_VIDEO_BYTES=500MiB`
- `MAX_CODE_FILE_BYTES=10MiB`
- `MAX_IMAGE_BYTES=40MiB`
- `MAX_CONCURRENT_REQUESTS=15`
- `MAX_OCR_CONCURRENT=3`
- `UNIVERSAL_EXTRACT_TIMEOUT=300s`
- `DOWNLOAD_TIMEOUT=25s`
- `GROQ_TIMEOUT=120s`
- `VISION_REQUEST_TIMEOUT=30s`
- `LIBREOFFICE_TIMEOUT=60s`
- `FFMPEG_TIMEOUT=120s`

Groq transcription defaults:
- `GROQ_API_URL=https://api.groq.com/openai/v1/audio/transcriptions`
- `GROQ_MODEL=whisper-large-v3-turbo`

### Groq transcription API reference (explicit)
This service sends audio transcription requests to Groq using the endpoint and multipart shape documented by Groq API docs.

- Endpoint used by default:
  - `POST https://api.groq.com/openai/v1/audio/transcriptions`
- Auth header:
  - `Authorization: Bearer $GROQ_API_KEY`
- Multipart fields used by this service:
  - `file` (binary)
  - `model` (default: `whisper-large-v3-turbo`)
  - optional `language`
  - optional `prompt`
  - optional `temperature`
  - optional `response_format` (default set by extractor options)

Supported Groq STT models/fields can evolve; if you override `GROQ_MODEL` or `response_format`, verify against current Groq docs.

Hybrid defaults:
- `DEFAULT_MIN_WORDS=20`
- `DEFAULT_OCR_TRIGGER_RATIO=0.25`
- `DEFAULT_PAGE_SEPARATOR="\n\n---\n\n"`
- `DEFAULT_OCR_MODEL=mistral-ocr-latest`
- `DEFAULT_PREVIEW_PAGES=8`
- `DEFAULT_PREVIEW_CHARS=20000`
- `DEFAULT_PREVIEW_NEEDS_OCR_RATIO=0.25`

See `internal/config/config.go` for the full list.

---

## Example requests

### Extract with direct URL
```bash
curl -X POST "https://<worker>/api/extract" \
  -H "Content-Type: application/json" \
  -d '{
    "presignedUrl": "https://example.com/file.docx",
    "fileName": "file.docx"
  }'
```

### Preview with direct URL
```bash
curl -X POST "https://<worker>/api/preview" \
  -H "Content-Type: application/json" \
  -d '{
    "presignedUrl": "https://example.com/file.pdf",
    "fileName": "file.pdf",
    "options": {
      "previewMaxChars": 12000,
      "previewMaxPages": 6
    }
  }'
```

### Extract with R2 key
```bash
curl -X POST "https://<worker>/api/extract" \
  -H "Content-Type: application/json" \
  -d '{
    "key": "user/abc123/files/abc123",
    "fileName": "lecture-notes.pdf"
  }'
```

### Presign an R2 object
```bash
curl -X POST "https://<worker>/api/file/presign" \
  -H "Content-Type: application/json" \
  -d '{
    "key": "user/abc123/files/abc123",
    "expiresIn": 600
  }'
```

### Direct container debugging
```bash
curl -X POST "http://localhost:8080/extract" \
  -H "Content-Type: application/json" \
  -H "X-Internal-Auth: $INTERNAL_SHARED_SECRET" \
  -d '{
    "presignedUrl": "https://example.com/file.pdf",
    "fileName": "file.pdf"
  }'
```

---

## Troubleshooting

- `presignedUrl or key required` (Worker): request body missing both fields.
- `Invalid key`: key failed Worker prefix/path safety checks.
- `Not found` on presign/extract-by-key: R2 object does not exist.
- `request_too_large`: Worker JSON body exceeds limit.
- `rate_limit`: Worker or server limiter blocked request.
- `unauthorized`: invalid `X-Internal-Auth` when calling container directly.
- Extract result with `success: false` and `error`: extractor/router-level failure (format-specific).
- Missing `OPENROUTER_API_KEY`: image flow falls back to OCR-only.
- Missing `MISTRAL_API_KEY` or `GROQ_API_KEY`: OCR/transcription paths fail accordingly.

---

## Breaking changes from v1

Removed public endpoints:
- `POST /api/pdf/preview` (replaced by universal `POST /api/preview`)
- `POST /api/pdf/extract`
- `POST /api/image/extract`

Removed internal endpoints:
- `POST /pdf/preview`
- `POST /pdf/extract`
- `POST /image/extract`

Use:
- `POST /api/preview` (public low-cost preview)
- `POST /api/extract` (public full extraction)
- `POST /preview` (internal low-cost preview)
- `POST /extract` (internal full extraction)
