// The edge Worker of Telesphorepo: it answers a GET or HEAD of a format route only when it can
// prove that the origin would give the same answer, and passes every other request to the origin.
// A request on a repo domain (config.json `domains`) reads the format route of the domain's repo.
// It never refuses: every 4xx comes from the origin. It answers 502 itself only when it cannot
// reach the origin.

import {
  type Caller,
  type Config,
  type CopyMessage,
  type Env,
  type Objects,
  type Region,
  type RepoObject,
  type Rights,
  blobKey,
  bucket,
  edgeKey,
  keyPrefix,
  limited,
  loadConfig,
  mayRead,
  resolveScopes,
  noteSeen,
  parseCaller,
  repoObject,
  rights,
} from "./access";
import { CHECK_PATH, PROBE_PATH, check, probe } from "./check";
import { copy } from "./copy";
import { gap } from "./gap";
import { immutable, immutableBy } from "./glob";
import { type Pointer, parsePointer, serve } from "./serve";
import { OVERRIDE_HEADER, PASS_HEADER, REGION_HEADER, type Override, parseOverride, passValid } from "./speedtest";
import { pacmanPath, parseTarget, rawPath } from "./shapes";

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    // The check of the server: answered here, before passThroughOnException, so a failure never
    // reaches the server's own answer on the path.
    if (request.method === "GET" && new URL(request.url).pathname === CHECK_PATH) return check(request, env);
    if (request.method === "GET" && new URL(request.url).pathname === PROBE_PATH) return probe(request, env);
    if (request.method !== "GET" && request.method !== "HEAD") return forward(request);
    ctx.passThroughOnException();
    const override = parseOverride(request.headers);
    const note: Note = { override: "unused" };
    let answer: Response | null = null;
    try {
      answer = await edge(request, env, ctx, override, note);
    } catch (err) {
      console.error("edge: read failed, passing to the origin:", err instanceof Error ? err.message : String(err));
    }
    const res = answer ?? (await forward(request));
    return override ? marked(res, note.override) : res;
  },
  queue: copy,
  scheduled: gap,
} satisfies ExportedHandler<Env, CopyMessage>;

// ---------------------------------------------------------------------------------------------
// Forwarding

// forward passes the request to the origin with a same-zone fetch of the request itself, which
// reaches the origin without running the Worker again. x-real-ip and X-Forwarded-For carry the address that Cloudflare saw, never a value
// from the client. The hop-by-hop headers stay on the client's hop: an Expect: 100-continue (mvn
// sends it on every PUT) would make the origin send the subrequest an interim 100 Continue that
// the subrequest does not expect. A HEAD skips the zone's cache: the cache shares one entry between
// GET and HEAD and sends a HEAD on a cold entry to the origin as a GET, whose answer on S3 storage is
// a redirect to a URL presigned for GET alone, so the client's HEAD of that URL fails with 403.
async function forward(request: Request): Promise<Response> {
  const headers = new Headers(request.headers);
  for (const h of HOP_BY_HOP) headers.delete(h);
  // The headers of the speed test are for the Worker only.
  headers.delete(REGION_HEADER);
  headers.delete(PASS_HEADER);
  const client = request.headers.get("CF-Connecting-IP");
  if (client) {
    headers.set("x-real-ip", client);
    headers.set("X-Forwarded-For", client);
  } else {
    headers.delete("x-real-ip");
    headers.delete("X-Forwarded-For");
  }
  const url = new URL(request.url);
  headers.set("X-Forwarded-Proto", url.protocol.replace(/:$/, ""));
  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  try {
    return await fetch(request.url, {
      method: request.method,
      headers,
      body: hasBody ? request.body : null,
      redirect: "manual",
      cache: request.method === "HEAD" ? "no-store" : undefined,
    });
  } catch (err) {
    console.error("edge: origin fetch failed:", err instanceof Error ? err.message : String(err));
    return problem();
  }
}

// HOP_BY_HOP are the headers of one connection (RFC 9110 7.6.1), with Expect, which asks the next
// hop for an interim response.
const HOP_BY_HOP = ["Connection", "Keep-Alive", "Proxy-Connection", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Expect"];

function problem(): Response {
  const body = JSON.stringify({
    type: "about:blank",
    title: "Bad Gateway",
    status: 502,
    detail: "The edge could not reach the server.",
  });
  return new Response(body, {
    status: 502,
    headers: { "Content-Type": "application/problem+json", "Cache-Control": "no-store" },
  });
}

// ---------------------------------------------------------------------------------------------
// Buckets

// The point of each R2 location hint, an estimate of where the hint stores data.
const POINTS: Record<string, [number, number]> = {
  wnam: [37.7749, -122.4194], // San Francisco
  enam: [39.0438, -77.4874], // Ashburn
  weur: [51.5074, -0.1278], // London
  eeur: [52.2297, 21.0122], // Warsaw
  apac: [1.3521, 103.8198], // Singapore
  oc: [-33.8688, 151.2093], // Sydney
};

function distance(a: [number, number], b: [number, number]): number {
  const rad = Math.PI / 180;
  const dLat = (b[0] - a[0]) * rad;
  const dLon = (b[1] - a[1]) * rad;
  const s = Math.sin(dLat / 2) ** 2 + Math.cos(a[0] * rad) * Math.cos(b[0] * rad) * Math.sin(dLon / 2) ** 2;
  return 2 * Math.asin(Math.min(1, Math.sqrt(s)));
}

interface Bucket {
  hint: string;
  name: string; // the bucket that its bucket.json names
  r2: R2Bucket;
}

// nearest returns the nearest ready region with a binding whose bucket.json names the region's
// bucket, or the home bucket. A binding to another bucket (a hint moved to a new bucket before a
// deploy) is skipped. Without the client's coordinates it returns the home bucket.
async function nearest(request: Request, env: Env, config: Config, objects: Objects): Promise<Bucket> {
  const home: Bucket = { hint: config.homeHint, name: config.homeBucket, r2: env.HOME };
  const cf = request.cf as { latitude?: string; longitude?: string } | undefined;
  const lat = parseFloat(cf?.latitude ?? ""), lon = parseFloat(cf?.longitude ?? "");
  if (!Number.isFinite(lat) || !Number.isFinite(lon)) return home;
  const at: [number, number] = [lat, lon];
  const homeD = POINTS[config.homeHint] ? distance(at, POINTS[config.homeHint]) : Infinity;
  const near: { r: Region; r2: R2Bucket; d: number }[] = [];
  for (const r of config.regions) {
    const r2 = r.ready ? bucket(env, r.binding) : null;
    if (!r2 || !POINTS[r.hint] || r.bucket === "") continue;
    const d = distance(at, POINTS[r.hint]);
    if (d < homeD) near.push({ r, r2, d });
  }
  near.sort((a, b) => a.d - b.d);
  for (const n of near) {
    if ((await objects.bucketOf(n.r.binding, n.r2)) === n.r.bucket) return { hint: n.r.hint, name: n.r.bucket, r2: n.r2 };
  }
  return home;
}

// ---------------------------------------------------------------------------------------------
// Pointers

async function sha256Hex(s: string): Promise<string> {
  const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return Array.from(new Uint8Array(d), (b) => b.toString(16).padStart(2, "0")).join("");
}

type Lookup = { kind: "none" } | { kind: "pending" } | { kind: "bad" } | { kind: "hit"; pointer: Pointer; cached: boolean };

// PointerCache is the Cache API of the pointers of immutable paths on one request: base is the
// key prefix of the request's host and of the generation of config.json, gen the generation and
// ttl the max age.
interface PointerCache {
  base: string;
  gen: number;
  ttl: number;
  ctx: ExecutionContext;
}

// PUT_WINDOW_MS bounds the time from the start of an R2 read of a pointer to the end of its put
// into the cache; a put that ends later is deleted again. The server purges a pointer it made
// unusable 2 s after the change, so every copy read before the change is in the cache, or gone,
// before the purge.
const PUT_WINDOW_MS = 1000;

// pointerCache returns the pointer cache of a request on host: config.json names a generation and
// the host among those whose zones the server purges; null otherwise. Only the Cache API holds a
// pointer, never the isolate memory, which a purge cannot reach.
function pointerCache(env: Env, config: Config, host: string, ctx: ExecutionContext): PointerCache | null {
  const pc = config.pointerCache;
  if (!pc || !pc.hosts.has(host)) return null;
  return { base: `https://${host}/.telesphorepo-edge/${keyPrefix(env)}v1/pointers/${pc.gen}/`, gen: pc.gen, ttl: pc.ttl, ctx };
}

// pointerTag is the Cache-Tag of the cached pointer key of bucket in generation gen: the tag that
// the server purges (pointerTag of internal/edge/purge.go).
const pointerTag = (gen: number, bucket: string, key: string) => `tp/${gen}/${bucket}/${key}`;

// putTagged puts body under key with max age ttl and Cache-Tag tag, and deletes it again in this
// data center when the put ended more than PUT_WINDOW_MS after started.
async function putTagged(key: Request, body: string, ttl: number, tag: string, started: number): Promise<void> {
  const headers = new Headers({
    "Cache-Control": "max-age=" + ttl,
    "Cache-Tag": tag,
    "Content-Type": "application/json",
    "x-edge-expires": String(Date.now() + ttl * 1000),
  });
  try {
    await caches.default.put(key, new Response(body, { headers }));
    if (Date.now() - started > PUT_WINDOW_MS) await caches.default.delete(key);
  } catch {
    // Not cached: the next request reads R2.
  }
}

// lookup reads a pointer object of path in repo. A pointer of another repo or path is "bad": the
// request goes to the origin.
function lookup(v: unknown, repo: RepoObject, path: string, cached: boolean): Lookup {
  const p = parsePointer(v);
  if (p === "pending") {
    return typeof v === "object" && v !== null && (v as { repo?: unknown }).repo === repo.id ? { kind: "pending" } : { kind: "bad" };
  }
  if (!p || p.repo !== repo.id || p.path !== path) return { kind: "bad" };
  return { kind: "hit", pointer: p, cached };
}

// pointer reads the pointer of path in repo from bucket b. With cache (an immutable path of a
// single repo), a pointer that serves is kept in the Cache API under b's name: the server purges
// that copy before any change of the pointer, so the copy always holds the bytes of the asset now.
// A missing or pending pointer is never kept, so its R2 read follows every new pointer.
async function pointer(env: Env, b: Bucket, repo: RepoObject, path: string, cache: PointerCache | null): Promise<Lookup> {
  const key = `p/${repo.id}/${await sha256Hex(path)}`;
  const cacheKey = cache ? new Request(cache.base + b.name + "/" + key) : null;
  if (cacheKey) {
    try {
      const hit = await caches.default.match(cacheKey);
      const expires = Number(hit?.headers.get("x-edge-expires"));
      if (hit && Number.isFinite(expires) && expires > Date.now()) {
        const l = lookup(JSON.parse(await hit.text()), repo, path, true);
        if (l.kind === "hit") return l;
      }
    } catch {
      // A Cache API failure reads the bucket.
    }
  }
  const started = Date.now();
  const obj = await b.r2.get(edgeKey(env, key));
  if (!obj) return { kind: "none" };
  const text = await obj.text();
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return { kind: "bad" };
  }
  const l = lookup(v, repo, path, false);
  if (cache && cacheKey && l.kind === "hit" && Date.now() - started <= PUT_WINDOW_MS) {
    cache.ctx.waitUntil(putTagged(cacheKey, text, cache.ttl, pointerTag(cache.gen, b.name, key), started));
  }
  return l;
}

// ---------------------------------------------------------------------------------------------
// The edge answer

// Note is what became of the speed test's choice of bucket, for OVERRIDE_HEADER.
interface Note {
  override: string;
}

// marked returns res with OVERRIDE_HEADER set to value.
function marked(res: Response, value: string): Response {
  try {
    const out = new Response(res.body, res);
    out.headers.set(OVERRIDE_HEADER, value);
    return out;
  } catch {
    return res;
  }
}

// chosen returns the bucket that the speed test names: "home", or a ready region whose binding's
// bucket.json names the region's bucket; null when the Worker does not read it.
async function chosen(env: Env, config: Config, objects: Objects, region: string): Promise<Bucket | null> {
  if (region === "home") return { hint: config.homeHint, name: config.homeBucket, r2: env.HOME };
  const r = config.regions.find((x) => x.hint === region);
  const r2 = r?.ready && r.bucket !== "" ? bucket(env, r.binding) : null;
  if (!r || !r2) return null;
  return (await objects.bucketOf(r.binding, r2)) === r.bucket ? { hint: r.hint, name: r.bucket, r2 } : null;
}

// edge answers the request at the edge, or returns null when the origin must answer. override is
// the speed test's choice of bucket; it applies only to a request that passes every check below,
// and note says what became of it.
async function edge(request: Request, env: Env, ctx: ExecutionContext, override: Override | null, note: Note): Promise<Response | null> {
  const url = new URL(request.url);
  if (url.searchParams.has("_state")) return null; // a Docker upload token
  // The time of each step up to the answer, for Server-Timing, with where its held reads were
  // answered so far (memory, Cache API, R2). Date.now() moves only across I/O, so each step
  // measures its reads.
  const steps: string[] = [];
  let mark = Date.now();
  const loaded = await loadConfig(env, url.origin);
  const step = (name: string, desc?: string) => {
    const now = Date.now();
    const t = loaded!.objects.tally;
    steps.push(`${name};dur=${now - mark};desc="${desc ?? `mem ${t.mem} cache ${t.cache} r2 ${t.r2}`}"`);
    mark = now;
  };
  if (!loaded) return null;
  step("config");
  const { config, objects } = loaded;
  if (!config.enabled || !config.homeReady || !config.serves) return null;
  if (config.homeBucket === "") return null;
  let routed: string | null = request.url;
  const domain = config.domains.get(url.hostname.toLowerCase());
  if (domain !== undefined) routed = await onDomain(request.url, domain, config, objects);
  if (!routed) return null;
  const target = parseTarget(routed, config.formats);
  if (!target) return null;
  const caller: Caller | null = await parseCaller(request, target.format.shape, config);
  if (!caller) return null;
  step("caller");
  // The reads behind these checks do not depend on each other, so after a cache expiry they cost
  // one far read, not one each.
  const [homeBucket, repo, r, nearestBucket, passOK] = await Promise.all([
    objects.bucketOf("HOME", env.HOME),
    repoObject(objects, target.repo),
    rights(caller, objects),
    nearest(request, env, config, objects),
    override ? passValid(env, override.pass) : Promise.resolve(false),
  ]);
  step("checks");
  // The HOME binding must be the home bucket of config.json (its bucket.json).
  if (homeBucket !== config.homeBucket) return null;
  if (!repo || repo.format !== target.format.name || repo.guarded) return null;
  if (!r) return null;
  await resolveScopes(r, objects);
  step("scopes");
  if (!mayRead(r, repo, true)) return null;
  // A hosted repo of the pacman shape answers pacman's URLs from where it keeps the files.
  if (target.format.shape === "pacman" && repo.kind === "hosted") target.path = pacmanPath(target.repo, target.path);

  // The speed test picks the bucket only now, after every check of the request.
  let near = nearestBucket;
  let picked = false;
  if (override) {
    if (!passOK) {
      note.override = "refused; reason=pass";
    } else if (override.region === "origin") {
      note.override = "applied; region=origin";
      return null;
    } else {
      const b = await chosen(env, config, objects, override.region);
      if (b) [near, picked] = [b, true];
      else note.override = "refused; reason=region";
    }
  }

  // A repo object with paths classifies its own paths (the overwrite rules of raw).
  const imm = repo.paths
    ? immutableBy(target.path, repo.paths)
    : immutable(target.path, target.format.immutable, target.format.mutable);
  const home: Bucket = { hint: config.homeHint, name: config.homeBucket, r2: env.HOME };
  const at = imm ? near : home; // the bucket of the pointer

  const started = Date.now();
  let found: { repo: RepoObject; pointer: Pointer } | null;
  let fromCache = false;
  if (repo.kind === "group") {
    found = await groupPointer(env, objects, r, repo, target.format.name, target.path, imm, at);
  } else {
    // A mutable path reads its pointer in R2 every time.
    const cache = imm ? pointerCache(env, config, url.hostname.toLowerCase(), ctx) : null;
    const l = await pointer(env, at, repo, target.path, cache);
    found = l.kind === "hit" ? { repo, pointer: l.pointer } : null;
    fromCache = l.kind === "hit" && l.cached;
  }
  if (!found) return null;
  const pointerMs = Date.now() - started;
  step("pointer", fromCache ? "cache" : "r2");

  const p = found.pointer;
  let blobRead: { ms: number; hint: string } | null = null;
  const answer = await serve(request, p, {
    private: repo.private,
    version: config.serverVersion,
    oci: target.format.shape === "oci",
    pointerHint: at.hint,
    blob: async (range) => {
      const key = blobKey(env, p.sha256);
      const t = Date.now();
      for (const b of near.r2 === env.HOME ? [home] : [near, home]) {
        const body = await b.r2.get(key, range ? { range } : undefined);
        if (body) {
          blobRead = { ms: Date.now() - t, hint: b.hint };
          return { body, hint: b.hint };
        }
      }
      return null;
    },
  });
  if (answer && r.seen) noteSeen(env, ctx, r.seen);
  if (!answer) return null;
  // The time to the headers of each bucket read; the body streams after.
  const read = blobRead as { ms: number; hint: string } | null;
  if (picked && override) {
    note.override = "applied; region=" + override.region;
    const timing = [`pointer;dur=${pointerMs};desc="${at.hint}"`];
    if (read) timing.push(`blob;dur=${read.ms};desc="${read.hint}"`);
    answer.headers.set("Server-Timing", timing.join(", "));
  } else {
    if (read) steps.push(`blob;dur=${read.ms};desc="${read.hint}"`);
    answer.headers.set("Server-Timing", steps.join(", "));
  }
  return answer;
}

// onDomain maps a request on the repo domain of repo name to the format route of its repo, as the
// server does: path P of the domain is /<format>/<repo>P, its escaping kept. null when the server
// answers: "/", a repo whose object this Worker cannot read, or a format that the edge does not
// serve under /<format>/.
async function onDomain(requestURL: string, name: string, config: Config, objects: Objects): Promise<string | null> {
  const raw = rawPath(requestURL);
  if (raw === "/") return null;
  const repo = await repoObject(objects, name);
  if (!repo) return null;
  const f = config.formats.find((x) => x.name === repo.format);
  if (!f || f.shape === "oci" || f.prefix !== "/" + repo.format + "/") return null;
  return new URL(requestURL).origin + f.prefix + encodeURIComponent(repo.name) + raw;
}

// groupPointer walks the members of a group in order, like assets.Reader.Resolve, and returns the
// pointer that serves, or null when the origin must answer: a mutable path (every Merger path is
// mutable), a guarded member, a pending pointer, or a proxy member without a pointer. A hosted
// member without a pointer has no such file: the edge answers only once config.json says home
// ready (every pointer of the backfill written), and a new file of a member gets a pending
// pointer before its commit.
async function groupPointer(
  env: Env,
  objects: Objects,
  r: Rights,
  group: RepoObject,
  format: string,
  path: string,
  imm: boolean,
  at: Bucket,
): Promise<{ repo: RepoObject; pointer: Pointer } | null> {
  if (!imm) return null;
  const members = await limited(group.members, 6, (name) => repoObject(objects, name));
  const readable: RepoObject[] = [];
  for (const m of members) {
    // A member that classifies its own paths may keep the path in another bucket.
    if (!m || m.format !== format || m.kind === "group" || m.guarded || m.paths) return null;
    if (mayRead(r, m, false)) readable.push(m);
  }
  // Read in R2, never through the pointer cache: a member answers only while every earlier member
  // has no pointer, and a cache holds no absence.
  const lookups = await limited(readable, 6, (m) => pointer(env, at, m, path, null));
  for (let i = 0; i < readable.length; i++) {
    const l = lookups[i];
    switch (l.kind) {
      case "hit":
        return { repo: readable[i], pointer: l.pointer };
      case "pending":
      case "bad":
        return null;
      case "none":
        if (readable[i].kind !== "hosted") return null; // a proxy would fetch here
    }
  }
  return null;
}
