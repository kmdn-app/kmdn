/**
 * Typed client for the kmdn REST API. Types are generated from
 * api/openapi.yaml (`pnpm --filter @kmdn/api-client generate`).
 */
import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema.gen";

export type { components, paths };
export type Schemas = components["schemas"];
export type User = Schemas["User"];
export type Me = Schemas["Me"];
export type Problem = Schemas["Problem"];
export type SetupStatus = Schemas["SetupStatus"];

export const CSRF_COOKIE = "kmdn_csrf";
export const CSRF_HEADER = "X-Kmdn-CSRF";

/** Reads a cookie value from a document.cookie string. */
export function readCookie(cookies: string, name: string): string | undefined {
  for (const part of cookies.split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === name) return decodeURIComponent(v.join("="));
  }
  return undefined;
}

const UNSAFE = new Set(["POST", "PUT", "PATCH", "DELETE"]);

/** Adds the CSRF header to unsafe requests, from the kmdn_csrf cookie. */
export function csrfMiddleware(getCookies: () => string): Middleware {
  return {
    onRequest({ request }) {
      if (UNSAFE.has(request.method)) {
        const token = readCookie(getCookies(), CSRF_COOKIE);
        if (token) request.headers.set(CSRF_HEADER, token);
      }
      return request;
    },
  };
}

/** An API error carrying the server's problem details. */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: Problem;
  constructor(problem: Problem) {
    super(problem.detail || problem.title || problem.code);
    this.status = problem.status;
    this.problem = problem;
  }
  get code(): string {
    return this.problem.code;
  }
}

export function toApiError(error: unknown, response: Response): ApiError {
  const p = error as Partial<Problem> | undefined;
  return new ApiError({
    type: p?.type ?? "about:blank",
    title: p?.title ?? response.statusText,
    status: p?.status ?? response.status,
    code: p?.code ?? "http_" + response.status,
    detail: p?.detail,
    params: p?.params,
  });
}

export function createApi(opts: { baseUrl?: string; fetch?: typeof fetch; cookies?: () => string } = {}) {
  const client = createClient<paths>({
    baseUrl: opts.baseUrl ?? "/api/v1",
    fetch: opts.fetch,
    credentials: "same-origin",
  });
  client.use(csrfMiddleware(opts.cookies ?? (() => (typeof document === "undefined" ? "" : document.cookie))));
  return client;
}

export type Api = ReturnType<typeof createApi>;

/** Unwraps an openapi-fetch result, throwing ApiError on failure. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  const { data, error, response } = await p;
  if (!response.ok) throw toApiError(error, response);
  return data as T;
}
