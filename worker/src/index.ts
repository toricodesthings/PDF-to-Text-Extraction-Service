import { Container } from "@cloudflare/containers";
import { AwsClient } from "aws4fetch";
import { CONTAINER, CORS_HEADERS, LIMITS, ROUTES, json } from "./constants";

export interface Env {
  MISTRAL_API_KEY: string;
  OPENROUTER_API_KEY: string;
  GROQ_API_KEY: string;
  INTERNAL_SHARED_SECRET: string;
  ALLOWED_PRESIGNED_HOST_SUFFIXES?: string;
  ALLOW_PRIVATE_DOWNLOAD_URLS?: string;
  /** R2 binding — available in deployed Workers, local miniflare only has an empty bucket */
  FILE_BUCKET: R2Bucket;
  FILEPROC: { getByName(name: string): FileProcContainer };
  RATE_LIMITER: { limit(options: { key: string }): Promise<{ success: boolean }> };
  /** S3 API credentials for aws4fetch fallback (local dev via .dev.vars) */
  R2_ACCOUNT_ID?: string;
  R2_ACCESS_KEY_ID?: string;
  R2_SECRET_ACCESS_KEY?: string;
  R2_BUCKET_NAME?: string;
}

export class FileProcContainer extends Container {
  defaultPort = 8080;
  sleepAfter = "10m";
}



// cache ONLY plain data, never stubs/streams/requests
let containerHealthCache = {
  lastHealthCheck: 0,
  isHealthy: false,
};

async function getReadyInstance(env: Env): Promise<FileProcContainer> {
  const now = Date.now();
  const inst = env.FILEPROC.getByName(CONTAINER.NAME);

  if (
    containerHealthCache.isHealthy &&
    now - containerHealthCache.lastHealthCheck < CONTAINER.HEALTH_CHECK_INTERVAL_MS
  ) {
    return inst;
  }

  await inst.startAndWaitForPorts({
    startOptions: {
      envVars: {
        MISTRAL_API_KEY: env.MISTRAL_API_KEY || "",
        OPENROUTER_API_KEY: env.OPENROUTER_API_KEY || "",
        GROQ_API_KEY: env.GROQ_API_KEY || "",
        INTERNAL_SHARED_SECRET: env.INTERNAL_SHARED_SECRET || "",
        ALLOWED_PRESIGNED_HOST_SUFFIXES: env.ALLOWED_PRESIGNED_HOST_SUFFIXES || "",
        ALLOW_PRIVATE_DOWNLOAD_URLS: env.ALLOW_PRIVATE_DOWNLOAD_URLS || "",
      },
    },
    ports: CONTAINER.PORT,
    cancellationOptions: CONTAINER.START,
  });


  const healthResp = await inst.fetch(
    new Request(CONTAINER.HEALTH_URL, {
      method: "GET",
      signal: AbortSignal.timeout(CONTAINER.HEALTH_TIMEOUT_MS),
    })
  );

  containerHealthCache.lastHealthCheck = now;

  if (!healthResp.ok) {
    console.error("container health check failed", {
      status: healthResp.status,
    });
    containerHealthCache.isHealthy = false;
    throw new Error(`Container unhealthy: ${healthResp.status} - ${await healthResp.text()}`);
  }

  containerHealthCache.isHealthy = true;
  return inst;
}

async function checkRateLimit(
  limiter: { limit(options: { key: string }): Promise<{ success: boolean }> },
  identifier: string
): Promise<{ allowed: boolean }> {
  try {
    const result = await limiter.limit({ key: identifier });
    return { allowed: result.success };
  } catch (e) {
    console.error("Rate limit check failed:", e);
    // Fail closed to prevent cost-amplification bypass when limiter backend is unavailable.
    return { allowed: false };
  }
}

function getClientIdentifier(req: Request): string {
  return req.headers.get("CF-Connecting-IP") || "unknown";
}

function isAllowedR2Key(key: string): boolean {
  const trimmed = key.trim();
  if (trimmed === "") return false;
  const lower = trimmed.toLowerCase();
  if (trimmed.includes("..") || trimmed.includes("\\") || trimmed.includes("//")) return false;
  if (lower.includes("%2e") || lower.includes("%2f") || lower.includes("%5c") || lower.includes("%00")) return false;
  if (trimmed.startsWith("/") || trimmed.endsWith("/")) return false;
  if (!(trimmed.startsWith("user/") || trimmed.startsWith("tests/"))) return false;
  return /^[a-zA-Z0-9][a-zA-Z0-9/_.-]{0,1023}$/.test(trimmed);
}

const DEFAULT_ALLOWED_PRESIGNED_SUFFIXES = [".r2.cloudflarestorage.com", ".r2.dev"];

function allowedPresignedHostSuffixes(env: Env): string[] {
  const raw = env.ALLOWED_PRESIGNED_HOST_SUFFIXES;
  if (!raw) return DEFAULT_ALLOWED_PRESIGNED_SUFFIXES;
  const parsed = raw
    .split(",")
    .map((v) => v.trim().toLowerCase())
    .filter(Boolean);
  return parsed.length > 0 ? parsed : DEFAULT_ALLOWED_PRESIGNED_SUFFIXES;
}

function allowPrivateDownloadUrls(env: Env): boolean {
  const raw = env.ALLOW_PRIVATE_DOWNLOAD_URLS;
  if (!raw) return false;
  const value = raw.trim().toLowerCase();
  return value === "1" || value === "true" || value === "yes" || value === "on";
}

function isPrivateOrLocalIPv4(host: string): boolean {
  const parts = host.split(".");
  if (parts.length !== 4) return false;

  const octets: number[] = [];
  for (const part of parts) {
    if (!/^\d+$/.test(part)) return false;
    const n = Number(part);
    if (!Number.isInteger(n) || n < 0 || n > 255) return false;
    octets.push(n);
  }

  const [a, b] = octets;
  if (a === 127 || a === 10 || a === 0) return true;
  if (a === 169 && b === 254) return true;
  if (a === 192 && b === 168) return true;
  if (a === 172 && b >= 16 && b <= 31) return true;
  if (a === 100 && b >= 64 && b <= 127) return true;
  if (a >= 224) return true;
  return false;
}

function isPrivateOrLocalIPv6(host: string): boolean {
  const normalized = host.toLowerCase();
  return (
    normalized === "::1" ||
    normalized === "::" ||
    normalized.startsWith("fe80:") ||
    normalized.startsWith("fc") ||
    normalized.startsWith("fd") ||
    normalized.startsWith("ff")
  );
}

function isPrivateOrLocalHost(host: string): boolean {
  const normalized = host.trim().toLowerCase();
  if (!normalized) return false;
  if (normalized === "localhost" || normalized.endsWith(".localhost")) return true;
  if (normalized.includes(":")) return isPrivateOrLocalIPv6(normalized);
  return isPrivateOrLocalIPv4(normalized);
}

function isAllowedPresignedUrl(rawUrl: string, allowedSuffixes: string[], allowPrivate: boolean): boolean {
  let parsed: URL;
  try {
    parsed = new URL(rawUrl);
  } catch {
    return false;
  }

  const host = parsed.hostname.trim().toLowerCase();
  if (!host) return false;
  const privateOrLocalHost = isPrivateOrLocalHost(host);
  const protocol = parsed.protocol.trim().toLowerCase();
  if (protocol === "http:") {
    if (!(allowPrivate && privateOrLocalHost)) return false;
  } else if (protocol !== "https:") {
    return false;
  }

  if (privateOrLocalHost) return allowPrivate;

  return allowedSuffixes.some((suffix) => {
    const s = suffix.toLowerCase();
    if (!s) return false;
    if (s.startsWith(".")) {
      const base = s.slice(1);
      return host === base || host.endsWith(s);
    }
    return host === s || host.endsWith(`.${s}`);
  });
}

function getStringField(body: any, name: string): string {
  const value = body?.[name];
  if (typeof value !== "string") return "";
  return value.trim();
}

type R2PresignBucket = R2Bucket & {
  createSignedUrl: (key: string, options: { expiresIn: number }) => Promise<string>;
};

/**
 * Check if the S3 API credentials are available (local dev via .dev.vars).
 */
function hasS3Credentials(env: Env): boolean {
  return Boolean(env.R2_ACCOUNT_ID && env.R2_ACCESS_KEY_ID && env.R2_SECRET_ACCESS_KEY);
}

/**
 * Generate a presigned download URL via the S3-compatible API (aws4fetch).
 * Used as fallback when the R2 binding cannot presign (local dev).
 */
async function createPresignedUrlViaS3(
  env: Env,
  key: string,
  expiresInSeconds: number
): Promise<string> {
  const { R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY } = env;
  if (!R2_ACCOUNT_ID || !R2_ACCESS_KEY_ID || !R2_SECRET_ACCESS_KEY) {
    throw new Error("presign_unsupported");
  }

  const bucket = env.R2_BUCKET_NAME || "users-knowledge-base";
  const client = new AwsClient({
    accessKeyId: R2_ACCESS_KEY_ID,
    secretAccessKey: R2_SECRET_ACCESS_KEY,
    region: "auto",
    service: "s3",
  });

  const encodedKey = key.split("/").map(encodeURIComponent).join("/");
  const url = new URL(
    `https://${encodeURIComponent(bucket)}.${R2_ACCOUNT_ID}.r2.cloudflarestorage.com/${encodedKey}`
  );
  url.searchParams.set("X-Amz-Expires", String(expiresInSeconds));

  const signed = await client.sign(new Request(url.toString(), { method: "GET" }), {
    aws: { signQuery: true },
  });
  return signed.url;
}

/**
 * Presign an R2 key.
 *
 * Strategy:
 *   1. Try the R2 binding (deployed Workers — co-located, zero latency).
 *   2. Fall back to aws4fetch S3 API (local dev — binding bucket is empty).
 */
async function createPresignedUrl(bucket: R2Bucket, key: string, expiresInSeconds: number, env: Env): Promise<string> {
  // Attempt R2 binding first
  try {
    const maybe = await bucket.head(key);
    if (maybe) {
      const signer = (bucket as R2PresignBucket).createSignedUrl;
      if (typeof signer === "function") {
        return await signer.call(bucket, key, { expiresIn: expiresInSeconds });
      }
    }
  } catch {
    // Binding unavailable or failed — fall through to S3 fallback
  }

  // S3 API fallback (local dev)
  if (hasS3Credentials(env)) {
    return createPresignedUrlViaS3(env, key, expiresInSeconds);
  }

  throw new Error("not_found");
}

async function resolveUrl(body: any, bucket: R2Bucket, env: Env): Promise<string> {
  // Prefer R2 key — the Worker has a direct R2 binding so presigning
  // is co-located and instant.  S3 API is a fallback for local dev.
  const key = getStringField(body, "key");
  if (key) {
    if (!isAllowedR2Key(key)) {
      throw new Error("invalid_key");
    }
    return createPresignedUrl(bucket, key, 600, env);
  }

  const presignedUrl = getStringField(body, "presignedUrl");
  if (presignedUrl) {
    const allowedSuffixes = allowedPresignedHostSuffixes(env);
    const allowPrivate = allowPrivateDownloadUrls(env);
    if (!isAllowedPresignedUrl(presignedUrl, allowedSuffixes, allowPrivate)) {
      throw new Error("invalid_presigned_url");
    }
    return presignedUrl;
  }

  throw new Error("missing_presigned_or_key");
}

async function parseJSONBody(req: Request, maxBytes = LIMITS.JSON_BODY_MAX_BYTES): Promise<any> {
  const contentLength = req.headers.get("content-length");
  if (contentLength && parseInt(contentLength, 10) > maxBytes) {
    throw new Error(`Request body too large: ${contentLength} bytes`);
  }
  const text = await req.text();
  if (text.length > maxBytes) {
    throw new Error(`Request body too large: ${text.length} bytes`);
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new Error("Invalid JSON");
  }
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);

    if (req.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: CORS_HEADERS });
    }

    try {
      if (url.pathname === ROUTES.HEALTH && req.method === "GET") {
        try {
          const inst = await getReadyInstance(env);
          const resp = await inst.fetch(new Request(CONTAINER.HEALTH_URL, { method: "GET" }));
          if (!resp.ok) return json({ status: "unhealthy", container: resp.status }, { status: 503 });
          return json(await resp.json(), { status: 200 });
        } catch (e: any) {
          console.error("health proxy failed", { error: e?.message || "unknown" });
          return json({ status: "unhealthy", error: e?.message || "unknown" }, { status: 503 });
        }
      }

      if (url.pathname === ROUTES.PREVIEW && req.method === "POST") {
        const clientId = getClientIdentifier(req);
        const rateLimit = await checkRateLimit(env.RATE_LIMITER, clientId);
        if (!rateLimit.allowed) {
          return json(
            { success: false, error: "Rate limit exceeded", code: "rate_limit" },
            { status: 429, headers: { "Retry-After": "60" } }
          );
        }

        const body = await parseJSONBody(req);
        try {
          body.presignedUrl = await resolveUrl(body, env.FILE_BUCKET, env);
        } catch (err: any) {
          if (err?.message === "invalid_key") {
            return json({ success: false, error: "Invalid key", code: "bad_request" }, { status: 400 });
          }
          if (err?.message === "invalid_presigned_url") {
            return json({ success: false, error: "Invalid presigned URL host", code: "bad_request" }, { status: 400 });
          }
          if (err?.message === "not_found") {
            return json({ success: false, error: "Not found", code: "not_found" }, { status: 404 });
          }
          return json(
            { success: false, error: "presignedUrl or key required", code: "bad_request" },
            { status: 400 }
          );
        }
        // key was consumed by resolveUrl — strip before forwarding to container
        delete body.key;

        const inst = await getReadyInstance(env);
        const resp = await inst.fetch(
          new Request(CONTAINER.PREVIEW_URL, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-Internal-Auth": env.INTERNAL_SHARED_SECRET,
              "X-Forwarded-For": clientId,
            },
            body: JSON.stringify(body),
          })
        );

        if (!resp.ok) {
          console.error("preview container response not ok", {
            status: resp.status,
          });
        }

        return new Response(await resp.text(), {
          status: resp.status,
          headers: { "Content-Type": "application/json", ...CORS_HEADERS },
        });
      }

      if (url.pathname === ROUTES.EXTRACT && req.method === "POST") {
        const clientId = getClientIdentifier(req);
        const rateLimit = await checkRateLimit(env.RATE_LIMITER, clientId);
        if (!rateLimit.allowed) {
          return json(
            { success: false, error: "Rate limit exceeded", code: "rate_limit" },
            { status: 429, headers: { "Retry-After": "60" } }
          );
        }

        const body = await parseJSONBody(req);
        try {
          body.presignedUrl = await resolveUrl(body, env.FILE_BUCKET, env);
        } catch (err: any) {
          if (err?.message === "invalid_key") {
            return json({ success: false, error: "Invalid key", code: "bad_request" }, { status: 400 });
          }
          if (err?.message === "invalid_presigned_url") {
            return json({ success: false, error: "Invalid presigned URL host", code: "bad_request" }, { status: 400 });
          }
          if (err?.message === "not_found") {
            return json({ success: false, error: "Not found", code: "not_found" }, { status: 404 });
          }
          return json(
            { success: false, error: "presignedUrl or key required", code: "bad_request" },
            { status: 400 }
          );
        }
        // key was consumed by resolveUrl — strip before forwarding to container
        delete body.key;

        const inst = await getReadyInstance(env);
        const resp = await inst.fetch(
          new Request(CONTAINER.EXTRACT_URL, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-Internal-Auth": env.INTERNAL_SHARED_SECRET,
              "X-Forwarded-For": clientId,
            },
            body: JSON.stringify(body),
          })
        );

        if (!resp.ok) {
          console.error("extract container response not ok", {
            status: resp.status,
          });
        }

        return new Response(await resp.text(), {
          status: resp.status,
          headers: { "Content-Type": "application/json", ...CORS_HEADERS },
        });
      }

      if (url.pathname === ROUTES.FILE_PRESIGN && req.method === "POST") {
        const clientId = getClientIdentifier(req);
        const rateLimit = await checkRateLimit(env.RATE_LIMITER, clientId);
        if (!rateLimit.allowed) {
          return json(
            { success: false, error: "Rate limit exceeded", code: "rate_limit" },
            { status: 429, headers: { "Retry-After": "60" } }
          );
        }

        const body = await parseJSONBody(req);
        const key = typeof body.key === "string" ? body.key : "";
        if (!isAllowedR2Key(key)) {
          return json({ success: false, error: "Invalid key", code: "bad_request" }, { status: 400 });
        }

        const rawExpires = Number(body.expiresIn);
        const expiresIn = Number.isFinite(rawExpires) ? Math.max(60, Math.min(3600, rawExpires)) : 600;

        try {
          const presignedUrl = await createPresignedUrl(env.FILE_BUCKET, key, expiresIn, env);
          return json({ success: true, presignedUrl }, { status: 200 });
        } catch (err: any) {
          if (err?.message === "not_found") {
            return json({ success: false, error: "Not found", code: "not_found" }, { status: 404 });
          }
          if (err?.message === "presign_unsupported") {
            return json(
              { success: false, error: "Presign not supported", code: "internal_error" },
              { status: 500 }
            );
          }
          console.error("presign failed", { error: err?.message || "unknown" });
          return json({ success: false, error: "Internal server error", code: "internal_error" }, { status: 500 });
        }
      }

      return json({ success: false, error: "Not found", code: "not_found" }, { status: 404 });
    } catch (error: any) {
      const isTimeout = error?.message?.includes("timeout") || error?.name === "TimeoutError";
      const isTooBig = error?.message?.includes("too large");

      return json(
        {
          success: false,
          error: isTimeout ? "Request timeout" : isTooBig ? "Request too large" : "Internal server error",
          code: isTimeout ? "timeout" : isTooBig ? "request_too_large" : "internal_error",
        },
        { status: isTimeout ? 504 : isTooBig ? 413 : 500 }
      );
    }
  },
};
