export const ROUTES = {
  HEALTH: "/health",
  PREVIEW: "/api/preview",
  EXTRACT: "/api/extract",
  FILE_PRESIGN: "/api/file/presign",
} as const;

export const CONTAINER = {
  NAME: "fileproc-singleton",
  PORT: 8080,

  HEALTH_URL: "http://container/health",
  PREVIEW_URL: "http://container/preview",
  EXTRACT_URL: "http://container/extract",

  START_TIMEOUT_MS: 30_000,
  HEALTH_TIMEOUT_MS: 5_000,

  HEALTH_CHECK_INTERVAL_MS: 60_000,

  START: {
    instanceGetTimeoutMS: 30_000,
    portReadyTimeoutMS: 30_000,
    waitInterval: 250,
  },
} as const;

export const LIMITS = {
  JSON_BODY_MAX_BYTES: 2 * 1024 * 1024,
} as const;

export const CORS_HEADERS: Record<string, string> = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, POST, OPTIONS",
  "Access-Control-Allow-Headers": "Content-Type",
  "Access-Control-Max-Age": "86400",
};

// OPTIONAL: standard error shape (edge-localized message later)
export type ApiErrorCode =
  | "rate_limit"
  | "bad_request"
  | "not_found"
  | "timeout"
  | "request_too_large"
  | "internal_error";

export function json(
  body: unknown,
  init?: ResponseInit & { headers?: Record<string, string> }
): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...CORS_HEADERS,
      ...(init?.headers ?? {}),
    },
  });
}
