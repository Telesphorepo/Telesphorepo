// The edge Worker of Telesphorepo: it answers a GET or HEAD of a format route only when it can
// prove that the origin would give the same answer, and passes every other request to the origin.
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
  limited,
  loadConfig,
  mayRead,
  noteSeen,
  parseCaller,
  repoObject,
  rights,
} from "./access";
import { copy } from "./copy";
import { gap } from "./gap";
import { immutable } from "./glob";
import { type Pointer, parsePointer, serve } from "./serve";
import { parseTarget, rawPath } from "./shapes";
import { VERSION } from "./version";

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    if (request.method !== "GET" && request.method !== "HEAD") return forward(request, env);
    ctx.passThroughOnException();
    let answer: Response | null = null;
    try {
      answer = await edge(request, env, ctx);
    } catch (err) {
      console.error("edge: read failed, passing to the origin:", err instanceof Error ? err.message : String(err));
    }
    return answer ?? forward(request, env);
  },
  queue: copy,
  scheduled: gap,
} satisfies ExportedHandler<Env, CopyMessage>;

// ---------------------------------------------------------------------------------------------
// Forwarding

// forward passes the request to the origin: to the ORIGIN var when it is set (the test fixture),
// else with a same-zone fetch of the request itself, which reaches the origin without running the
// Worker again. x-real-ip and X-Forwarded-For carry the address that Cloudflare saw, never a value
// from the client. The hop-by-hop headers stay on the client's hop: an Expect: 100-continue (mvn
// sends it on every PUT) would make the origin send the subrequest an interim 100 Continue that
// the subrequest does not expect.
async function forward(request: Request, env: Env): Promise<Response> {
  const headers = new Headers(request.headers);
  for (const h of HOP_BY_HOP) headers.delete(h);
  if (env.ORIGIN) headers.delete("Host");
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
  let target = request.url;
  if (env.ORIGIN) {
    // The path and query as the client sent them.
    const q = request.url.indexOf("?");
    const query = q >= 0 ? request.url.slice(q).split("#")[0] : "";
    target = env.ORIGIN.replace(/\/+$/, "") + rawPath(request.url) + query;
  }
  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  try {
    return await fetch(target, {
      method: request.method,
      headers,
      body: hasBody ? request.body : null,
      redirect: "manual",
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
  r2: R2Bucket;
}

// nearest returns the nearest ready region with a binding whose bucket.json names the region's
// bucket, or the home bucket. A binding to another bucket (a hint moved to a new bucket before a
// deploy) is skipped. Without the client's coordinates it returns the home bucket.
async function nearest(request: Request, env: Env, config: Config, objects: Objects): Promise<Bucket> {
  const home: Bucket = { hint: config.homeHint, r2: env.HOME };
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
    if ((await objects.bucketOf(n.r.binding, n.r2)) === n.r.bucket) return { hint: n.r.hint, r2: n.r2 };
  }
  return home;
}

// ---------------------------------------------------------------------------------------------
// Pointers

async function sha256Hex(s: string): Promise<string> {
  const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return Array.from(new Uint8Array(d), (b) => b.toString(16).padStart(2, "0")).join("");
}

type Lookup = { kind: "none" } | { kind: "pending" } | { kind: "bad" } | { kind: "hit"; pointer: Pointer };

// pointer reads the pointer of path in repo from b. A pointer of another repo or path is "bad":
// the request goes to the origin.
async function pointer(env: Env, b: R2Bucket, repo: RepoObject, path: string): Promise<Lookup> {
  const obj = await b.get(edgeKey(env, `p/${repo.id}/${await sha256Hex(path)}`));
  if (!obj) return { kind: "none" };
  let v: unknown;
  try {
    v = JSON.parse(await obj.text());
  } catch {
    return { kind: "bad" };
  }
  const p = parsePointer(v);
  if (p === "pending") {
    return typeof v === "object" && v !== null && (v as { repo?: unknown }).repo === repo.id ? { kind: "pending" } : { kind: "bad" };
  }
  if (!p || p.repo !== repo.id || p.path !== path) return { kind: "bad" };
  return { kind: "hit", pointer: p };
}

// ---------------------------------------------------------------------------------------------
// The edge answer

// edge answers the request at the edge, or returns null when the origin must answer.
async function edge(request: Request, env: Env, ctx: ExecutionContext): Promise<Response | null> {
  const url = new URL(request.url);
  if (url.searchParams.has("_state")) return null; // a Docker upload token
  const loaded = await loadConfig(env, url.origin);
  if (!loaded) return null;
  const { config, objects } = loaded;
  if (!config.enabled || !config.homeReady || config.version !== VERSION) return null;
  // The HOME binding must be the home bucket of config.json (its bucket.json).
  if (config.homeBucket === "" || (await objects.bucketOf("HOME", env.HOME)) !== config.homeBucket) return null;
  const target = parseTarget(request.url, config.formats);
  if (!target) return null;
  const caller: Caller | null = await parseCaller(request, target.format.shape, config);
  if (!caller) return null;
  const repo = await repoObject(objects, target.repo);
  if (!repo || repo.format !== target.format.name || repo.guarded) return null;
  const r: Rights | null = await rights(caller, objects);
  if (!r || !mayRead(r, repo, true)) return null;

  const imm = immutable(target.path, target.format.immutable, target.format.mutable);
  const near = await nearest(request, env, config, objects);
  const home: Bucket = { hint: config.homeHint, r2: env.HOME };
  const at = imm ? near : home; // the bucket of the pointer

  let found: { repo: RepoObject; pointer: Pointer } | null;
  if (repo.kind === "group") {
    found = await groupPointer(env, objects, r, repo, target.format.name, target.path, imm, at);
  } else {
    const l = await pointer(env, at.r2, repo, target.path);
    found = l.kind === "hit" ? { repo, pointer: l.pointer } : null;
  }
  if (!found) return null;

  const p = found.pointer;
  const answer = await serve(request, p, {
    private: repo.private,
    version: config.version,
    oci: target.format.shape === "oci",
    pointerHint: at.hint,
    blob: async (range) => {
      const key = blobKey(env, p.sha256);
      for (const b of near.r2 === env.HOME ? [home] : [near, home]) {
        const body = await b.r2.get(key, range ? { range } : undefined);
        if (body) return { body, hint: b.hint };
      }
      return null;
    },
  });
  if (answer && r.seen) noteSeen(env, ctx, r.seen);
  return answer;
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
    if (!m || m.format !== format || m.kind === "group" || m.guarded) return null;
    if (mayRead(r, m, false)) readable.push(m);
  }
  const lookups = await limited(readable, 6, (m) => pointer(env, at.r2, m, path));
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
