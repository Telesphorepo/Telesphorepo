// The one fetch wrapper of the UI. Every /api call goes through request(), which turns a non-2xx
// answer (an RFC 9457 problem document) into an ApiError and tells the auth store when the session
// is gone (401) or can only set up a second factor (403 second_factor_required).

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    /** Per-field messages of a validation error (settings): key to message. */
    public fields?: Record<string, string>,
    /** Seconds from the Retry-After header of a 429. */
    public retryAfter?: number,
    /** The problem document of the answer, when it had one (e.g. the received of a 416). */
    public problem?: unknown,
  ) {
    super(message);
  }
  get notFound() {
    return this.status === 404;
  }
}

/** The problem document of an error answer (RFC 9457), with the extension member fields. */
type Problem = { type?: unknown; title?: unknown; detail?: unknown; fields?: unknown };

/** The message of a problem document: its detail, else its title, else fallback. */
function problemMessage(p: Problem | undefined, fallback: string): string {
  if (typeof p?.detail === "string" && p.detail) return p.detail;
  if (typeof p?.title === "string" && p.title) return p.title;
  return fallback;
}

/** The per-field messages of a problem document, if it has them. */
function problemFields(p: Problem | undefined): Record<string, string> | undefined {
  return p?.fields && typeof p.fields === "object" ? (p.fields as Record<string, string>) : undefined;
}

/** The message to show for any thrown value. */
export const errorMessage = (e: unknown): string =>
  e instanceof Error ? e.message : typeof e === "string" ? e : "Something went wrong.";

type Listener = () => void;
const lost = new Set<Listener>();
const setupOnly = new Set<Listener>();

/** Calls fn when a request answers 401 outside the sign-in and second-factor routes. */
export function onSessionLost(fn: Listener): () => void {
  lost.add(fn);
  return () => lost.delete(fn);
}

/** Calls fn when a request answers 403 second_factor_required. */
export function onSecondFactorRequired(fn: Listener): () => void {
  setupOnly.add(fn);
  return () => setupOnly.delete(fn);
}

/**
 * Marks every request as coming from this UI. The server never answers such a request with
 * WWW-Authenticate, so the browser never opens its own password popup.
 */
const uiHeader = "X-Telesphorepo-UI";

// A wrong password or second-factor code answers 401 on these routes. It must not look like an
// expired session.
const carriesCredentials = /^\/api\/(auth\/login|devices\/approve|publish\/sessions\/manual|security\/)/;

export type Query = Record<string, string | number | boolean | null | undefined>;

export type RequestOptions = {
  /** JSON body. */
  body?: unknown;
  query?: Query;
  /** A publish session token, sent as a Bearer credential instead of the session cookie. */
  token?: string;
  /** A fresh second factor, sent in the header X-Telesphorepo-Proof (see proofHeader). */
  proof?: string;
  signal?: AbortSignal;
};

/** Builds "?a=1&b=2", leaving out empty values. */
export function qs(q: Query = {}): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? "?" + s : "";
}

export async function request<T = void>(method: string, url: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json", [uiHeader]: "1" };
  let body: BodyInit | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  if (opts.token) headers.Authorization = "Bearer " + opts.token;
  if (opts.proof) headers["X-Telesphorepo-Proof"] = opts.proof;

  let res: Response;
  try {
    res = await fetch(url + qs(opts.query), { method, headers, body, credentials: "same-origin", signal: opts.signal });
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError(0, "The server cannot be reached. Check your connection and try again.");
  }

  if (!res.ok) {
    let message = res.statusText || `Request failed (${res.status})`;
    let fields: Record<string, string> | undefined;
    try {
      const p = (await res.json()) as Problem;
      message = problemMessage(p, message);
      fields = problemFields(p);
    } catch {
      /* not JSON */
    }
    if (res.status === 401 && !opts.token && !carriesCredentials.test(url)) lost.forEach((f) => f());
    if (res.status === 403 && message === "second_factor_required") {
      setupOnly.forEach((f) => f());
      message = "Add an authenticator app or a passkey to your account first.";
    }
    const ra = Number(res.headers.get("Retry-After"));
    throw new ApiError(res.status, message, fields, Number.isFinite(ra) && ra > 0 ? ra : undefined);
  }
  if (res.status === 204 || res.headers.get("Content-Length") === "0") return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

/** A GET that answers plain text (a generated file), with the same errors as request(). */
export async function requestText(url: string): Promise<string> {
  let res: Response;
  try {
    res = await fetch(url, { headers: { Accept: "*/*", [uiHeader]: "1" }, credentials: "same-origin" });
  } catch {
    throw new ApiError(0, "The server cannot be reached. Check your connection and try again.");
  }
  if (!res.ok) {
    let message = res.statusText || `Request failed (${res.status})`;
    try {
      message = problemMessage((await res.json()) as Problem, message);
    } catch {
      /* not JSON */
    }
    if (res.status === 401) lost.forEach((f) => f());
    throw new ApiError(res.status, message);
  }
  return res.text();
}

/**
 * A multipart POST that reports the progress of its body. fetch() cannot do that, so this one
 * uses XMLHttpRequest. It answers like request(): JSON on success, ApiError otherwise, and an
 * AbortError DOMException after abort().
 */
export function requestWithProgress<T>(
  url: string,
  form: FormData,
  token: string,
  onProgress: (sent: number, total: number) => void,
): { done: Promise<T>; abort: () => void } {
  return sendWithProgress<T>("POST", url, form, token, {}, onProgress);
}

/** Like requestWithProgress, with any method, body and extra headers (a chunk of a resumable upload). */
export function sendWithProgress<T>(
  method: string,
  url: string,
  body: FormData | Blob | null,
  token: string,
  headers: Record<string, string>,
  onProgress: (sent: number, total: number) => void,
): { done: Promise<T>; abort: () => void } {
  const xhr = new XMLHttpRequest();
  const done = new Promise<T>((resolve, reject) => {
    xhr.open(method, url);
    xhr.setRequestHeader("Accept", "application/json");
    xhr.setRequestHeader(uiHeader, "1");
    xhr.setRequestHeader("Authorization", "Bearer " + token);
    for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded, e.total);
    };
    xhr.onabort = () => reject(new DOMException("The upload was cancelled.", "AbortError"));
    xhr.onerror = () => reject(new ApiError(0, "The server cannot be reached. Check your connection and try again."));
    xhr.onload = () => {
      let answer: unknown;
      try {
        answer = xhr.responseText ? JSON.parse(xhr.responseText) : undefined;
      } catch {
        answer = undefined;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(answer as T);
        return;
      }
      const p = answer as Problem | undefined;
      let message = problemMessage(p, xhr.statusText || `Request failed (${xhr.status})`);
      if (xhr.status === 413) message = "The files are larger than the upload limit of this server. Upload fewer or smaller files.";
      const fields = problemFields(p);
      reject(new ApiError(xhr.status, message, fields, undefined, answer));
    };
    xhr.send(body);
  });
  return { done, abort: () => xhr.abort() };
}

/** Like request, but resolves to null on 404. */
export async function requestOrNull<T>(method: string, url: string, opts?: RequestOptions): Promise<T | null> {
  try {
    return await request<T>(method, url, opts);
  } catch (e) {
    if (e instanceof ApiError && e.notFound) return null;
    throw e;
  }
}
