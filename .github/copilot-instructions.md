# Copilot Instructions for file-processing-service

## Big picture architecture
- This service has two runtime layers: Cloudflare Worker edge proxy (`worker/src/index.ts`) and Go extraction container (`cmd/server/main.go`). Keep edge concerns (R2 key handling, container lifecycle, edge rate limits) in Worker; keep extraction logic in Go.
- Public API routes are Worker `/api/*`; internal Go routes are `/pdf/*`, `/image/extract`, `/extract` and require `X-Internal-Auth` (`withInternalAuth` in `cmd/server/main.go`).
- Universal extraction flow is: request -> `internal/extract/router.go` -> MIME/extension resolution via `internal/extract/registry.go` -> extractor implementation in `internal/extractors/*`.
- PDF extraction is hybrid (`internal/hybrid/hybrid.go`): poppler text-layer first (`internal/extractor/poppler.go`), then OCR fallback based on quality scoring (`internal/quality/quality.go`).
- Image extraction is vision-first (`internal/image/image.go`): OpenRouter classification routes to OCR-only, vision-only, or OCR+vision.

## High-value coding patterns in this repo
- New file formats should be added as a new `internal/extractors/<type>` package implementing `internal/extract.Extractor`; then register it in `main()` in `cmd/server/main.go`.
- Return unified extraction outputs (`internal/extract/result.go`) with `Success`, `Text`, `Method`, `FileType`, `MIMEType`, counts, optional `Metadata`.
- Prefer temp-file download + cleanup pattern (`internal/extract/download.go`, `downloadPDFToTemp` in `cmd/server/main.go`) rather than streaming directly to extractors.
- Keep user-facing errors sanitized/truncated (see `sanitizeError` in `cmd/server/main.go`) and preserve API error shape `{ success, error, code }`.
- Use bounded execution: request timeout contexts, semaphore gates (`requestSem`, `ocrSem`), and body/file size caps from `internal/config/config.go`.

## Developer workflows (verified)
- Install JS deps from repo root: `npm install` (runs `postinstall` to install `worker/` deps).
- Worker local dev: `npx wrangler dev`.
- Go server local run: `go run ./cmd/server` (requires `INTERNAL_SHARED_SECRET` >= 32 chars).
- Targeted tests currently present: `go test ./internal/extractors/audio -run Test -v`.
- `go test ./...` and `go build ./cmd/server` currently fail until module metadata is synced (`go mod tidy` message appears).

## Environment and external dependencies
- External APIs: Mistral OCR (`MISTRAL_API_KEY`), OpenRouter vision (`OPENROUTER_API_KEY`), Groq transcription (`GROQ_API_KEY`).
- Missing `OPENROUTER_API_KEY` should preserve OCR fallback behavior for images (do not hard-fail image extraction).
- Poppler CLI tools (`pdfinfo`, `pdftotext`) are required for PDF extraction and are installed in `Dockerfile`.
- Legacy Office extraction depends on LibreOffice; video extraction depends on ffmpeg (also in `Dockerfile`).

## Agent guardrails for edits
- Do not bypass Worker key validation/presigning rules (`user/` and `tests/` prefixes in `worker/src/index.ts`).
- Keep route/method/auth middleware layering consistent with `main.go` (method -> rate limit -> concurrency -> handler).
- When adding options/configs, wire through `internal/config/config.go` defaults and validation instead of hardcoding.
- Match existing timeout- and limit-first style; avoid introducing unbounded reads/processes.
