// The answer of an edge hit: the headers of assets.Reader.ServeAsset from the pointer, Range,
// HEAD and 304 (section 5 of the edge spec). An answer that the origin gives differently (416)
// returns null, and the request goes to the origin.

import { knows } from "./features";

export interface Pointer {
  repo: number;
  path: string;
  sha256: string;
  sha1: string;
  md5: string;
  size: number;
  ct: string;
  mod: number;
  kind: string;
}

const isInt = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v);
const optStr = (v: unknown): string | null => (v === undefined || v === null ? "" : typeof v === "string" ? v : null);

// parsePointer reads a pointer object: a Pointer, "pending" while a replacement runs, or null
// when it is malformed. A pointer with a need that this Worker lacks is pending: the server
// answers it.
export function parsePointer(v: unknown): Pointer | "pending" | null {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return null;
  const o = v as Record<string, unknown>;
  if (o.pending === true || !knows(o.needs)) return "pending";
  const sha1 = optStr(o.sha1), md5 = optStr(o.md5), kind = optStr(o.kind);
  if (
    !isInt(o.repo) || typeof o.path !== "string" || typeof o.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(o.sha256) ||
    !isInt(o.size) || o.size < 0 || typeof o.ct !== "string" || !isInt(o.mod) || sha1 === null || md5 === null || kind === null
  ) {
    return null;
  }
  return { repo: o.repo, path: o.path, sha256: o.sha256, sha1, md5, size: o.size, ct: o.ct, mod: o.mod, kind };
}

// mediaType is a port of mime.ParseMediaType for the one question the server asks of it: the
// lower-case media type, or null when the value does not parse.
const tokenRE = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

function mediaType(v: string): string | null {
  const semi = v.indexOf(";");
  const base = (semi < 0 ? v : v.slice(0, semi)).trim().toLowerCase();
  const slash = base.indexOf("/");
  if (slash < 0 ? !tokenRE.test(base) : !tokenRE.test(base.slice(0, slash)) || !tokenRE.test(base.slice(slash + 1))) {
    return null;
  }
  let rest = semi < 0 ? "" : v.slice(semi);
  const seen = new Set<string>();
  // Each parameter: ";" key "=" (token | quoted-string).
  const paramRE = /^;\s*([!#$%&'*+\-.^_`|~0-9A-Za-z]+)\s*=\s*("(?:[^"\\]|\\.)*"|[!#$%&'*+\-.^_`|~0-9A-Za-z]+)\s*/;
  while ((rest = rest.trimStart()) !== "") {
    if (/^;\s*$/.test(rest)) break; // a trailing ";"
    const m = paramRE.exec(rest);
    if (!m) return null;
    const key = m[1].toLowerCase();
    if (seen.has(key)) return null;
    seen.add(key);
    rest = rest.slice(m[0].length);
  }
  return base;
}

// inline reports whether a browser may show content of type ct: plain text only.
export function inline(ct: string): boolean {
  return mediaType(ct) === "text/plain";
}

function etagMatch(header: string, etag: string): boolean {
  for (let t of header.split(",")) {
    t = t.trim();
    if (t.startsWith("W/")) t = t.slice(2);
    if (t === "*" || t === etag) return true;
  }
  return false;
}

const INT64_MAX = 9223372036854775807n;

// parseInt64 is strconv.ParseInt(s, 10, 64): an optional sign and decimal digits.
function parseInt64(s: string): bigint | null {
  if (!/^[+-]?[0-9]+$/.test(s)) return null;
  const n = BigInt(s);
  return n > INT64_MAX || n < -INT64_MAX - 1n ? null : n;
}

export type Range = { kind: "whole" } | { kind: "outside" } | { kind: "part"; offset: number; length: number };

// byteRange is assets.byteRange: one range "bytes=a-b", "bytes=a-" or "bytes=-n". whole: no
// Range header, several ranges, a header that does not parse, or an If-Range that names another
// version. outside: a range that the origin answers with 416.
export function byteRange(h: Headers, size: number, etag: string): Range {
  const header = h.get("Range") ?? "";
  if (!header.startsWith("bytes=")) return { kind: "whole" };
  const spec = header.slice(6);
  if (spec.includes(",")) return { kind: "whole" };
  const ir = h.get("If-Range");
  if (ir !== null && ir !== "" && ir !== etag) return { kind: "whole" };
  const trimmed = spec.trim();
  const dash = trimmed.indexOf("-");
  if (dash < 0) return { kind: "whole" };
  const from = trimmed.slice(0, dash), to = trimmed.slice(dash + 1);
  const sz = BigInt(size);
  if (from === "") {
    const last = parseInt64(to);
    if (last === null || last < 0n) return { kind: "whole" };
    if (last === 0n || sz === 0n) return { kind: "outside" };
    const n = last < sz ? last : sz;
    return { kind: "part", offset: Number(sz - n), length: Number(n) };
  }
  const start = parseInt64(from);
  if (start === null || start < 0n) return { kind: "whole" };
  let end = sz - 1n;
  if (to !== "") {
    const e = parseInt64(to);
    if (e === null || e < start) return { kind: "whole" };
    end = e < sz - 1n ? e : sz - 1n;
  }
  if (start >= sz) return { kind: "outside" };
  return { kind: "part", offset: Number(start), length: Number(end - start + 1n) };
}

export interface ServeOptions {
  private: boolean;
  version: string;
  oci: boolean;
  // pointerHint is the hint of the bucket that held the pointer.
  pointerHint: string;
  // blob reads the blob (a range of it) from the chosen bucket, then the home bucket; it returns
  // the body and the hint of the bucket that had it, or null.
  blob: (range?: { offset: number; length: number }) => Promise<{ body: R2ObjectBody; hint: string } | null>;
}

// serve answers a GET or HEAD from pointer p, or returns null when the origin must answer.
export async function serve(request: Request, p: Pointer, o: ServeOptions): Promise<Response | null> {
  const etag = `"${p.sha256}"`;
  const h = new Headers();
  if (o.oci) {
    h.set("Docker-Distribution-API-Version", "registry/2.0");
    h.set("Docker-Content-Digest", "sha256:" + p.sha256);
  }
  h.set("X-Checksum-SHA256", p.sha256);
  if (p.sha1 !== "") h.set("X-Checksum-SHA1", p.sha1);
  if (p.md5 !== "") h.set("X-Checksum-MD5", p.md5);
  h.set("X-Content-Type-Options", "nosniff");
  h.set("Content-Security-Policy", "default-src 'none'; sandbox");
  if (!inline(p.ct)) h.set("Content-Disposition", "attachment");
  h.set("Content-Type", p.ct);
  h.set("ETag", etag);
  h.set("Last-Modified", new Date(p.mod * 1000).toUTCString());
  h.set("Telesphorepo-Version", o.version);
  if (o.private) h.set("Cache-Control", "private");
  const mark = (hint: string) => h.set("Telesphorepo-Edge", "hit; region=" + hint);

  const inm = request.headers.get("If-None-Match");
  if (inm !== null && inm !== "" && etagMatch(inm, etag)) {
    mark(o.pointerHint);
    return new Response(null, { status: 304, headers: h });
  }
  h.set("Accept-Ranges", "bytes");
  if (request.method === "HEAD") {
    mark(o.pointerHint);
    h.set("Content-Length", String(p.size));
    return new Response(null, { status: 200, headers: h });
  }
  const r = byteRange(request.headers, p.size, etag);
  if (r.kind === "outside") return null;
  if (r.kind === "part") {
    const got = await o.blob({ offset: r.offset, length: r.length });
    if (!got || got.body.size !== p.size) {
      if (got) await got.body.body.cancel();
      return null;
    }
    mark(got.hint);
    h.set("Content-Range", `bytes ${r.offset}-${r.offset + r.length - 1}/${p.size}`);
    h.set("Content-Length", String(r.length));
    return new Response(fixed(got.body.body, r.length), { status: 206, headers: h });
  }
  const got = await o.blob();
  if (!got || got.body.size !== p.size) {
    if (got) await got.body.body.cancel();
    return null;
  }
  mark(got.hint);
  h.set("Content-Length", String(p.size));
  return new Response(fixed(got.body.body, p.size), { status: 200, headers: h });
}

// fixed gives a stream a known length, so the answer carries Content-Length.
function fixed(body: ReadableStream, length: number): ReadableStream {
  const { readable, writable } = new FixedLengthStream(length);
  body.pipeTo(writable).catch(() => {
    // The client went away or the bucket stream failed; the answer ends short.
  });
  return readable;
}
