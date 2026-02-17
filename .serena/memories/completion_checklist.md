# Completion Checklist
- Run `go test ./...` and ensure tests pass.
- Run `go build ./...` to verify compile integrity.
- For worker changes, run `cd worker && npm run dev` (or equivalent lint/type checks if added).
- Verify extraction endpoints still return expected JSON envelope.
- Re-check security-sensitive changes: auth header checks, URL/key validation, and limits on costly OCR/transcription paths.
- Confirm no sensitive user identifiers/content are logged.