import { Container } from "@cloudflare/containers";
import { CONTAINER, CORS_HEADERS, LIMITS, ROUTES, json } from "./constants";

export interface Env {
  MISTRAL_API_KEY: string;
  INTERNAL_SHARED_SECRET: string;
  PDF_BUCKET: R2Bucket;
  PDFPROC: { getByName(name: string): PdfProcContainer };
  RATE_LIMITER_PREVIEW: { limit(options: { key: string }): Promise<{ success: boolean }> };
  RATE_LIMITER_EXTRACT: { limit(options: { key: string }): Promise<{ success: boolean }> };
}

export class PdfProcContainer extends Container {
  defaultPort = 8080;
  sleepAfter = "10m";
}

// cache ONLY plain data, never stubs/streams/requests
let containerHealthCache = {
  lastHealthCheck: 0,
  isHealthy: false,
};

async function getReadyInstance(env: Env): Promise<PdfProcContainer> {
  const now = Date.now();
  const inst = env.PDFPROC.getByName(CONTAINER.NAME);

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
        INTERNAL_SHARED_SECRET: env.INTERNAL_SHARED_SECRET || "",
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
    return { allowed: true };
  }
}

function getClientIdentifier(req: Request): string {
  return (
    req.headers.get("CF-Connecting-IP") ||
    req.headers.get("X-Forwarded-For")?.split(",")[0].trim() ||
    "unknown"
  );
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
        const rateLimit = await checkRateLimit(env.RATE_LIMITER_PREVIEW, clientId);
        if (!rateLimit.allowed) {
          return json(
            { success: false, error: "Rate limit exceeded", code: "rate_limit" },
            { status: 429, headers: { "Retry-After": "60" } }
          );
        }

        const body = await parseJSONBody(req);
        if (!body.presignedUrl) {
          return json({ success: false, error: "presignedUrl required", code: "bad_request" }, { status: 400 });
        }

        const inst = await getReadyInstance(env);
        const resp = await inst.fetch(
          new Request(CONTAINER.PREVIEW_URL, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-Internal-Auth": env.INTERNAL_SHARED_SECRET,
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
        const rateLimit = await checkRateLimit(env.RATE_LIMITER_EXTRACT, clientId);
        if (!rateLimit.allowed) {
          return json(
            { success: false, error: "Rate limit exceeded", code: "rate_limit" },
            { status: 429, headers: { "Retry-After": "60" } }
          );
        }

        const body = await parseJSONBody(req);
        if (!body.presignedUrl) {
          return json({ success: false, error: "presignedUrl required", code: "bad_request" }, { status: 400 });
        }

        const inst = await getReadyInstance(env);
        const resp = await inst.fetch(
          new Request(CONTAINER.EXTRACT_URL, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-Internal-Auth": env.INTERNAL_SHARED_SECRET,
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
