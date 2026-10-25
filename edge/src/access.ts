// Access objects, config.json, and the credentials that the edge checks (section 3 of the edge
// spec). Every function here answers "the edge may serve" or "the origin decides": a credential
// that the edge does not check, or that fails a check, sends the request to the origin.

import { knows } from "./features";
import { VERSION } from "./version";

export interface Env {
  HOME: R2Bucket;
  COPY?: Queue<CopyMessage>;
  PREFIX?: string;
  [binding: string]: unknown;
}

// CopyMessage is a message of the copy queue: an R2 event notification of the home bucket
// (object.key), or a gap-check message (key, and the one region it is for).
export interface CopyMessage {
  object?: { key?: string };
  action?: string;
  key?: string;
  hint?: string;
}

export const HINTS = ["wnam", "enam", "weur", "eeur", "apac", "oc"] as const;

// keyPrefix is the s3.prefix of the server: "" or "dir/".
export function keyPrefix(env: Env): string {
  const p = (env.PREFIX ?? "").replace(/^\/+|\/+$/g, "");
  return p === "" ? "" : p + "/";
}

export function edgeKey(env: Env, key: string): string {
  return keyPrefix(env) + "edge/v1/" + key;
}

export function blobKey(env: Env, sha256: string): string {
  return keyPrefix(env) + "blobs/sha256/" + sha256.slice(0, 2) + "/" + sha256;
}

export function bucket(env: Env, binding: string): R2Bucket | null {
  const b = env[binding] as R2Bucket | undefined;
  return b && typeof b === "object" && typeof (b as R2Bucket).get === "function" ? b : null;
}

// limited runs fn over items with at most n at a time, keeping the order of the results.
export async function limited<T, R>(items: readonly T[], n: number, fn: (t: T) => Promise<R>): Promise<R[]> {
  const out = new Array<R>(items.length);
  let next = 0;
  const work = async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await fn(items[i]);
    }
  };
  await Promise.all(Array.from({ length: Math.min(n, items.length) }, work));
  return out;
}

// ---------------------------------------------------------------------------------------------
// config.json

export interface FormatConfig {
  name: string;
  shape: "standard" | "oci";
  prefix: string;
  immutable: string[];
  mutable: string[];
}

export interface Region {
  hint: string;
  binding: string;
  bucket: string; // the bucket that the bucket.json of the binding must name
  ready: boolean;
}

export interface Config {
  serverVersion: string; // for the Telesphorepo-Version header
  // serves: the Worker implements every feature that config.json requires (features.ts).
  serves: boolean;
  enabled: boolean;
  ttl: number; // access_ttl in seconds
  edgeKey: Uint8Array | null;
  formats: FormatConfig[];
  homeHint: string;
  // homeReady: the server wrote every pointer of its backfill. Before, a missing pointer may be one
  // not written yet, so the Worker answers nothing.
  homeReady: boolean;
  homeBucket: string; // the bucket that the bucket.json of HOME must name
  regions: Region[];
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const isStrs = (v: unknown): v is string[] => Array.isArray(v) && v.every((s) => typeof s === "string");

// parseDuration reads access_ttl: seconds as a number, or a Go duration string ("30s", "1m",
// "500ms"). It returns seconds, or NaN when the value does not parse.
export function parseDuration(v: unknown): number {
  if (typeof v === "number") return v >= 0 ? v : NaN;
  if (typeof v !== "string") return NaN;
  if (v === "0") return 0;
  const units: Record<string, number> = { ns: 1e-9, us: 1e-6, "µs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy;
  let total = 0;
  let pos = 0;
  for (let m = re.exec(v); m; m = re.exec(v)) {
    total += parseFloat(m[1]) * units[m[2]];
    pos = re.lastIndex;
  }
  return pos === v.length && pos > 0 ? total : NaN;
}

// decodeKey reads the Ed25519 public key: 64 hex digits, or base64 / base64url of 32 bytes.
export function decodeKey(v: unknown): Uint8Array | null {
  if (typeof v !== "string") return null;
  if (/^[0-9a-fA-F]{64}$/.test(v)) {
    const out = new Uint8Array(32);
    for (let i = 0; i < 32; i++) out[i] = parseInt(v.slice(i * 2, i * 2 + 2), 16);
    return out;
  }
  const b = b64decode(v);
  return b && b.length === 32 ? b : null;
}

// b64decode decodes base64 or base64url, with or without padding; null when it does not decode.
export function b64decode(s: string): Uint8Array | null {
  if (!/^[A-Za-z0-9+/_-]*={0,2}$/.test(s)) return null;
  let t = s.replace(/=+$/, "").replace(/-/g, "+").replace(/_/g, "/");
  if (t.length % 4 === 1) return null;
  t += "=".repeat((4 - (t.length % 4)) % 4);
  try {
    const bin = atob(t);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

// b64urlStrict decodes the base64url without padding of the parts of a core bearer token, the
// encoding of Go's base64.RawURLEncoding.
function b64urlStrict(s: string): Uint8Array | null {
  if (!/^[A-Za-z0-9_-]*$/.test(s) || s.length % 4 === 1) return null;
  return b64decode(s);
}

export function parseConfig(v: unknown): Config | null {
  if (!isObj(v)) return null;
  const ttl = parseDuration(v.access_ttl);
  const formats: FormatConfig[] = [];
  if (isObj(v.formats)) {
    // A format of a shape or with a need that this Worker does not know is left out: the server
    // answers its requests.
    for (const [name, f] of Object.entries(v.formats)) {
      if (!isObj(f) || (f.shape !== "standard" && f.shape !== "oci") || typeof f.prefix !== "string" || !knows(f.needs)) continue;
      if (!f.prefix.startsWith("/") || !f.prefix.endsWith("/")) continue;
      formats.push({
        name,
        shape: f.shape,
        prefix: f.prefix,
        immutable: isStrs(f.immutable) ? f.immutable : [],
        mutable: isStrs(f.mutable) ? f.mutable : [],
      });
    }
  }
  const regions: Region[] = [];
  if (Array.isArray(v.regions)) {
    for (const r of v.regions) {
      if (isObj(r) && typeof r.hint === "string" && typeof r.binding === "string") {
        regions.push({ hint: r.hint, binding: r.binding, bucket: typeof r.bucket === "string" ? r.bucket : "", ready: r.ready === true });
      }
    }
  }
  return {
    serverVersion: typeof v.server_version === "string" ? v.server_version : "",
    serves: knows(v.requires),
    enabled: v.enabled === true,
    ttl: Number.isFinite(ttl) ? Math.min(ttl, 300) : 0,
    edgeKey: decodeKey(v.edge_key),
    formats,
    homeHint: isObj(v.home) && typeof v.home.hint === "string" ? v.home.hint : "",
    homeBucket: isObj(v.home) && typeof v.home.bucket === "string" ? v.home.bucket : "",
    homeReady: isObj(v.home) && v.home.ready === true,
    regions,
  };
}

// readConfig reads config.json from the home bucket, without a cache (queue and cron).
export async function readConfig(env: Env): Promise<Config | null> {
  const obj = await env.HOME.get(edgeKey(env, "config.json"));
  if (!obj) return null;
  try {
    return parseConfig(JSON.parse(await obj.text()));
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------------------------
// Cached reads of config.json and the access objects (Cache API, TTL edge.access_ttl).

interface Held {
  value: unknown; // the parsed JSON, or null when the object is absent or does not parse
  expires: number; // ms since the epoch
}

const memory = new Map<string, Held>();
const MAX_MEMORY = 20000;

// held reads the object key of r2 (the home bucket unless binding names another) through the
// isolate memory, the Cache API and the bucket. An
// object stays held until ttlOf(value) seconds after the R2 read that fetched it, wherever it is
// held, so a change reaches the edge within edge.access_ttl. A TTL under 1 s reads the bucket
// every time.
async function held(
  env: Env,
  base: string,
  key: string,
  ttlOf: (v: unknown) => number,
  binding = "HOME",
  r2: R2Bucket = env.HOME,
): Promise<unknown> {
  const now = Date.now();
  const id = binding === "HOME" ? key : "@" + binding + "/" + key;
  const m = memory.get(id);
  if (m && m.expires > now) return m.value;
  if (m) memory.delete(id);
  const cacheKey = new Request(base + id);
  try {
    const hit = await caches.default.match(cacheKey);
    const expires = Number(hit?.headers.get("x-edge-expires"));
    if (hit && Number.isFinite(expires) && expires > now) {
      const value = hit.headers.get("x-edge-absent") === "1" ? null : parse(await hit.text());
      hold(id, { value, expires });
      return value;
    }
  } catch {
    // A Cache API failure reads the bucket.
  }
  const obj = await r2.get(edgeKey(env, key));
  const text = obj ? await obj.text() : null;
  const value = text === null ? null : parse(text);
  const ttl = Math.floor(ttlOf(value));
  if (ttl >= 1) {
    const expires = now + ttl * 1000;
    hold(id, { value, expires });
    try {
      const headers = new Headers({
        "Cache-Control": "max-age=" + ttl,
        "Content-Type": "application/json",
        "x-edge-expires": String(expires),
      });
      if (text === null) headers.set("x-edge-absent", "1");
      await caches.default.put(cacheKey, new Response(text ?? "", { headers }));
    } catch {
      // The isolate memory still holds it.
    }
  }
  return value;
}

// Objects reads the access objects of one request, with the TTL of its config.json.
export class Objects {
  constructor(
    private readonly env: Env,
    private readonly base: string,
    private readonly ttl: number,
  ) {}

  get(key: string): Promise<unknown> {
    return held(this.env, this.base, key, () => this.ttl);
  }

  // bucketOf returns the bucket that the bucket.json of binding r2 names, "" when it has none:
  // the server writes it into every bucket it uses, so a binding to another bucket is seen.
  async bucketOf(binding: string, r2: R2Bucket): Promise<string> {
    return markerBucket(await held(this.env, this.base, "bucket.json", () => this.ttl, binding, r2));
  }
}

function markerBucket(v: unknown): string {
  return isObj(v) && typeof v.bucket === "string" ? v.bucket : "";
}

// readMarker reads the bucket that bucket.json of r2 names, without a cache (the cron).
export async function readMarker(env: Env, r2: R2Bucket): Promise<string> {
  const obj = await r2.get(edgeKey(env, "bucket.json"));
  return obj ? markerBucket(parse(await obj.text())) : "";
}

function parse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

function hold(key: string, h: Held): void {
  if (memory.size >= MAX_MEMORY) memory.clear();
  memory.set(key, h);
}

// loadConfig reads config.json for a request, held for its own access_ttl. origin is the origin
// of the request URL: the Cache API keys live under it.
export async function loadConfig(env: Env, origin: string): Promise<{ config: Config; objects: Objects } | null> {
  const base = origin + "/.telesphorepo-edge/" + keyPrefix(env) + "v1/";
  const raw = await held(env, base, "config.json", (v) => parseConfig(v)?.ttl ?? 0);
  const config = parseConfig(raw);
  if (!config) return null;
  return { config, objects: new Objects(env, base, config.ttl) };
}

// ---------------------------------------------------------------------------------------------
// Access objects

export interface RepoObject {
  id: number;
  name: string;
  format: string;
  kind: string; // hosted, proxy or group
  anon: boolean;
  private: boolean;
  guarded: boolean;
  members: string[];
}

interface UserObject {
  enabled: boolean;
  local: boolean;
  all: boolean;
  read: number[];
}

interface TokenObject {
  id: number;
  user: number;
  hash: string;
  exp: number | null;
}

interface DeviceObject {
  user: number;
  exp: number | null;
}

const isInt = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v);
// exp is unix seconds; 0 (or none) never expires.
const expOf = (v: unknown): number | null | undefined => (v === null || v === undefined || v === 0 ? null : isInt(v) ? v : undefined);
// readable: an access object that this Worker can read, so it holds no need that the Worker lacks.
// Any other object is one the server decides on, as an absent one is.
const readable = (v: unknown): v is Record<string, unknown> => isObj(v) && knows(v.needs);

// repoObject reads the repo object of name. The object of a redirect is {redirect: <repo name>}:
// the Worker reads that repo's object, and the request is one to that repo (its name is the name
// of the scopes of a core bearer token). A redirect never names another redirect.
export async function repoObject(objects: Objects, name: string): Promise<RepoObject | null> {
  let v = await objects.get("a/repo/" + name);
  if (readable(v) && typeof v.redirect === "string") {
    name = v.redirect;
    v = await objects.get("a/repo/" + name);
    if (isObj(v) && v.redirect !== undefined) return null;
  }
  if (
    !readable(v) || !isInt(v.id) || typeof v.format !== "string" || typeof v.kind !== "string" ||
    typeof v.anon !== "boolean" || typeof v.private !== "boolean" || typeof v.guarded !== "boolean"
  ) {
    return null;
  }
  const members = v.members === undefined || v.members === null ? [] : v.members;
  if (!isStrs(members)) return null;
  return { id: v.id, name, format: v.format, kind: v.kind, anon: v.anon, private: v.private, guarded: v.guarded, members };
}

async function userObject(objects: Objects, id: number): Promise<UserObject | null> {
  const v = await objects.get("a/user/" + id);
  if (!readable(v) || typeof v.enabled !== "boolean" || typeof v.local !== "boolean" || typeof v.all !== "boolean") return null;
  const read = v.read === undefined || v.read === null ? [] : v.read;
  if (!Array.isArray(read) || !read.every(isInt)) return null;
  return { enabled: v.enabled, local: v.local, all: v.all, read: read as number[] };
}

async function tokenObject(objects: Objects, publicID: string): Promise<TokenObject | null> {
  const v = await objects.get("a/token/" + publicID);
  const exp = isObj(v) ? expOf(v.exp) : undefined;
  if (!readable(v) || !isInt(v.id) || !isInt(v.user) || typeof v.hash !== "string" || exp === undefined) return null;
  return { id: v.id, user: v.user, hash: v.hash, exp };
}

async function deviceObject(objects: Objects, id: number): Promise<DeviceObject | null> {
  const v = await objects.get("a/device/" + id);
  const exp = isObj(v) ? expOf(v.exp) : undefined;
  if (!readable(v) || !isInt(v.user) || exp === undefined) return null;
  return { user: v.user, exp };
}

const nowSec = () => Math.floor(Date.now() / 1000);
const live = (exp: number | null) => exp === null || nowSec() < exp;

// ---------------------------------------------------------------------------------------------
// Credentials

// Op numbers of auth.Op.
const OP_READ = 1;
// Cred bits of auth.Cred.
const CRED_PASSWORD = 1;
const CRED_READ_TOKEN = 2;
const CRED_DEVICE = 32;

interface BearerScope {
  repo: string;
  ops: number[];
  // id is the repo that repo names now (through a redirect after a rename), set by resolveScopes;
  // scopes match a repo by id.
  id?: number;
}

// Caller is a credential that the edge checks. Its rows (the user, token and device objects) are
// checked once per request by Caller.rights.
export type Caller =
  | { kind: "anonymous" }
  | { kind: "token"; publicID: string; secret: string } // trp_ by Basic or Bearer
  | {
      kind: "bearer";
      cred: number; // 0, CRED_PASSWORD, CRED_READ_TOKEN or CRED_DEVICE
      user: number;
      token: number;
      tokenPublic: string;
      device: number;
      scopes: BearerScope[];
    };

const TOKEN_RE = /^trp_([0-9a-f]{12})_([A-Za-z0-9_-]{43})$/;

// readToken parses a read token like token.Parse: "trp_<12 hex>_<43 base64url of 32 bytes>".
function readToken(tok: string): { publicID: string; secret: string } | null {
  const m = TOKEN_RE.exec(tok);
  if (!m) return null;
  const sec = b64urlStrict(m[2]);
  if (!sec || sec.length !== 32) return null;
  return { publicID: m[1], secret: m[2] };
}

// hasSessionCookie reports whether the request carries a tr_session cookie with a value.
function hasSessionCookie(h: Headers): boolean {
  const c = h.get("Cookie");
  if (!c) return false;
  for (const part of c.split(";")) {
    const [name, ...rest] = part.trim().split("=");
    if (name === "tr_session" && rest.join("=").trim() !== "") return true;
  }
  return false;
}

// parseCaller reads the credential of a request. null: the origin decides (a credential the edge
// does not check, or one that already fails a check).
export async function parseCaller(request: Request, shape: "standard" | "oci", config: Config): Promise<Caller | null> {
  const h = request.headers;
  if (hasSessionCookie(h)) return null;
  const auth = h.get("Authorization");
  if (auth === null || auth === "") return { kind: "anonymous" };
  if (auth.length >= 6 && auth.slice(0, 6).toLowerCase() === "basic ") {
    // Go's r.BasicAuth decodes with base64.StdEncoding: the standard alphabet, padded.
    const enc = auth.slice(6);
    if (enc.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(enc)) return null;
    const raw = b64decode(enc);
    if (!raw) return null;
    let text: string;
    try {
      text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(raw);
    } catch {
      return null;
    }
    const i = text.indexOf(":");
    if (i < 0) return null;
    const t = readToken(text.slice(i + 1));
    return t ? { kind: "token", ...t } : null;
  }
  if (auth.length > 7 && auth.slice(0, 7).toLowerCase() === "bearer ") {
    const tok = auth.slice(7).trim();
    if (tok.startsWith("trb.")) {
      return shape === "oci" ? verifyBearer(tok, config) : null;
    }
    const t = readToken(tok);
    return t ? { kind: "token", ...t } : null;
  }
  return null;
}

const keys = new Map<string, Promise<CryptoKey>>();

function importKey(raw: Uint8Array): Promise<CryptoKey> {
  const id = Array.from(raw, (b) => b.toString(16).padStart(2, "0")).join("");
  let k = keys.get(id);
  if (!k) {
    k = crypto.subtle.importKey("raw", raw, { name: "Ed25519" }, false, ["verify"]);
    keys.set(id, k);
  }
  return k;
}

// verifyBearer checks a core bearer token "trb.<payload>.<hmac>.<ed25519>" by its Ed25519 edge
// signature over the ASCII bytes of the payload part. It keeps only the tokens the edge serves:
// cred anonymous, password, read token or device; no scope with a session; no upload binding.
async function verifyBearer(tok: string, config: Config): Promise<Caller | null> {
  if (!config.edgeKey) return null;
  const parts = tok.slice(4).split(".");
  if (parts.length !== 3) return null;
  const [enc, , sigPart] = parts;
  const sig = b64urlStrict(sigPart);
  const body = b64urlStrict(enc);
  if (!sig || sig.length !== 64 || !body) return null;
  let ok = false;
  try {
    ok = await crypto.subtle.verify("Ed25519", await importKey(config.edgeKey), sig, new TextEncoder().encode(enc));
  } catch {
    return null;
  }
  if (!ok) return null;
  let pl: unknown;
  try {
    pl = JSON.parse(new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(body));
  } catch {
    return null;
  }
  if (!isObj(pl) || !isInt(pl.e) || nowSec() >= pl.e) return null;
  if (pl.up !== undefined && pl.up !== "") return null;
  const cred = pl.c === undefined ? 0 : pl.c;
  if (cred !== 0 && cred !== CRED_PASSWORD && cred !== CRED_READ_TOKEN && cred !== CRED_DEVICE) return null;
  const num = (v: unknown) => (v === undefined ? 0 : isInt(v) ? v : NaN);
  const user = num(pl.u), token = num(pl.t), device = num(pl.d);
  if (Number.isNaN(user) || Number.isNaN(token) || Number.isNaN(device)) return null;
  const tokenPublic = pl.tp === undefined ? "" : pl.tp;
  if (typeof tokenPublic !== "string") return null;
  if (cred === CRED_READ_TOKEN && !/^[0-9a-f]{12}$/.test(tokenPublic)) return null;
  const scopes: BearerScope[] = [];
  const raw = pl.s === null || pl.s === undefined ? [] : pl.s;
  if (!Array.isArray(raw)) return null;
  for (const s of raw) {
    if (!isObj(s) || typeof s.r !== "string") return null;
    if (s.x !== undefined && s.x !== 0) return null; // a scope with a publish session
    const ops = s.o === null || s.o === undefined ? [] : s.o;
    if (!Array.isArray(ops) || !ops.every(isInt)) return null;
    scopes.push({ repo: s.r, ops: ops as number[] });
  }
  return { kind: "bearer", cred, user, token, tokenPublic, device, scopes };
}

// Rights are the rows behind a credential that passed every edge check: whether its user reads
// every repo, and the repos it reads with repo.download. anonymous reads only anonymous repos.
export interface Rights {
  anonymous: boolean;
  all: boolean;
  read: Set<number>;
  // scopes of a core bearer token (null for any other credential).
  scopes: BearerScope[] | null;
  // seen is the public ID of a read token that the edge checked directly (for last_used_at).
  seen: string | null;
}

async function sha256Hex(s: string): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
}

function hexBytes(s: string): Uint8Array | null {
  if (!/^[0-9a-fA-F]*$/.test(s) || s.length % 2 !== 0) return null;
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  return out;
}

const userRights = (u: UserObject): Pick<Rights, "anonymous" | "all" | "read"> => ({
  anonymous: false,
  all: u.all,
  read: new Set(u.read),
});

// rights loads the rows of the caller and checks them as the origin does. null: the origin
// decides.
export async function rights(caller: Caller, objects: Objects): Promise<Rights | null> {
  if (caller.kind === "anonymous") return { anonymous: true, all: false, read: new Set(), scopes: null, seen: null };
  if (caller.kind === "token") {
    const t = await tokenObject(objects, caller.publicID);
    if (!t || !live(t.exp)) return null;
    const want = hexBytes(t.hash);
    const got = await sha256Hex(caller.secret);
    if (!want || want.length !== got.length || !crypto.subtle.timingSafeEqual(want, got)) return null;
    const u = await userObject(objects, t.user);
    if (!u || !u.enabled || !u.local) return null;
    return { ...userRights(u), scopes: null, seen: caller.publicID };
  }
  switch (caller.cred) {
    case 0:
      return { anonymous: true, all: false, read: new Set(), scopes: caller.scopes, seen: null };
    case CRED_PASSWORD: {
      const u = await userObject(objects, caller.user);
      if (!u || !u.enabled) return null;
      return { ...userRights(u), scopes: caller.scopes, seen: null };
    }
    // The token and the device must belong to the user of the bearer, so its user object is read
    // at the same time.
    case CRED_READ_TOKEN: {
      const [t, u] = await Promise.all([tokenObject(objects, caller.tokenPublic), userObject(objects, caller.user)]);
      if (!t || t.id !== caller.token || t.user !== caller.user || !live(t.exp)) return null;
      if (!u || !u.enabled || !u.local) return null;
      return { ...userRights(u), scopes: caller.scopes, seen: null };
    }
    case CRED_DEVICE: {
      const [d, u] = await Promise.all([deviceObject(objects, caller.device), userObject(objects, caller.user)]);
      if (!d || d.user !== caller.user || !live(d.exp)) return null;
      if (!u || !u.enabled) return null;
      return { ...userRights(u), scopes: caller.scopes, seen: null };
    }
  }
  return null;
}

// resolveScopes sets the repo id of each scope of a core bearer token from the repo object of its
// name, so a token issued before a rename of its repo still matches it, as on the server.
export async function resolveScopes(r: Rights, objects: Objects): Promise<void> {
  if (r.scopes === null) return;
  await Promise.all(
    r.scopes.map(async (s) => {
      s.id = (await repoObject(objects, s.repo))?.id;
    }),
  );
}

// mayRead reports whether the credential reads repo with repo.download. withScopes checks the
// scope of a core bearer token first (the repo of the request); a group member is checked
// without scopes, against the rows of the credential behind the token (Principal.InGroup).
export function mayRead(r: Rights, repo: RepoObject, withScopes: boolean): boolean {
  if (withScopes && r.scopes !== null) {
    const sc = r.scopes.find((s) => s.id === repo.id);
    if (!sc || !sc.ops.includes(OP_READ)) return false;
  }
  return repo.anon || r.all || r.read.has(repo.id);
}

// ---------------------------------------------------------------------------------------------
// last_used_at: the read tokens that this isolate served directly. A served token goes into the
// pending set of the isolate. When a request notes a token and no flush is scheduled, the request
// schedules one (waitUntil): it waits SEEN_DELAY_MS, then writes every pending token in one file,
// seen/<isolate>-<unix ms>.json. A write that succeeds takes its tokens out of the set; a failed
// one leaves them for the next flush. So an isolate writes at most one file per SEEN_DELAY_MS, and
// a token is lost only when the runtime ends the waitUntil before the write.

const SEEN_DELAY_MS = 20_000;

// The isolate ID is made on first use: workerd refuses random values in global scope.
let isolate = "";
const seenPending = new Map<string, number>(); // public ID -> last use, unix seconds
let seenScheduled = false;

// noteSeen records a read token served at the edge.
export function noteSeen(env: Env, ctx: ExecutionContext, publicID: string): void {
  seenPending.set(publicID, nowSec());
  if (seenScheduled) return;
  seenScheduled = true;
  if (!isolate) {
    isolate = Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => b.toString(16).padStart(2, "0")).join("");
  }
  ctx.waitUntil(flushSeen(env));
}

async function flushSeen(env: Env): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, SEEN_DELAY_MS));
  // A token noted from here on schedules the next flush.
  const batch = new Map(seenPending);
  seenScheduled = false;
  if (batch.size === 0) return;
  const tokens = Array.from(batch, ([id, at]) => ({ id, at }));
  const body = JSON.stringify({ version: VERSION, tokens });
  try {
    await env.HOME.put(edgeKey(env, `seen/${isolate}-${Date.now()}.json`), body, {
      httpMetadata: { contentType: "application/json" },
    });
  } catch {
    return; // the tokens stay pending for the next flush
  }
  for (const [id, at] of batch) {
    if (seenPending.get(id) === at) seenPending.delete(id);
  }
}
