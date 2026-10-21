// The check of the server (POST /api/edge/check on the server). The server writes check.json to
// the home bucket, {hash: hex SHA-256 of a one-time nonce, exp: unix seconds, pass: bool}, then
// sends GET /.well-known/telesphorepo-edge/check?n=<nonce> to its own base URL, which reaches this
// Worker on its route. The Worker runs the check only for the nonce of check.json before exp,
// deletes check.json first so the nonce works once, runs the gap check at once (a pass when pass
// is set) and answers with its version. Every other request on the path is refused here; none goes
// to the server, whose own answer on the path says that no Worker is on the route.

import { type Env, edgeKey } from "./access";
import { runGap } from "./gap";
import { VERSION } from "./version";

export const CHECK_PATH = "/.well-known/telesphorepo-edge/check";

// A check runs the gap check with at most this many subrequests and for at most this long; a pass
// that does not finish goes on at the next cron run.
const CHECK_SUBREQUESTS = 1000;
const CHECK_MS = 20_000;

export async function check(request: Request, env: Env): Promise<Response> {
  try {
    const n = new URL(request.url).searchParams.get("n") ?? "";
    const taken = await take(env, n);
    if (typeof taken === "string") return answer(403, { ran: false, error: taken });
    const pass = await runGap(env, { force: taken.pass, max: CHECK_SUBREQUESTS, deadline: Date.now() + CHECK_MS });
    return answer(200, { ran: true, pass });
  } catch (err) {
    console.error("edge: check failed:", err instanceof Error ? err.message : String(err));
    return answer(500, { ran: false, error: "the gap check failed" });
  }
}

// take returns the check of nonce n, or why there is none, and deletes check.json when it matches.
async function take(env: Env, n: string): Promise<{ pass: boolean } | string> {
  if (n === "" || n.length > 128) return "no nonce";
  const key = edgeKey(env, "check.json");
  const obj = await env.HOME.get(key);
  if (!obj) return "no check is waiting";
  let v: unknown;
  try {
    v = JSON.parse(await obj.text());
  } catch {
    return "the check request does not parse";
  }
  if (typeof v !== "object" || v === null) return "the check request does not parse";
  const req = v as { hash?: unknown; exp?: unknown; pass?: unknown };
  if (typeof req.hash !== "string" || typeof req.exp !== "number") return "the check request does not parse";
  const want = hexBytes(req.hash);
  const got = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(n)));
  if (!want || want.length !== got.length || !crypto.subtle.timingSafeEqual(want, got)) return "the nonce does not match";
  if (Math.floor(Date.now() / 1000) > req.exp) return "the check request expired";
  await env.HOME.delete(key);
  return { pass: req.pass === true };
}

export function hexBytes(s: string): Uint8Array | null {
  if (!/^[0-9a-fA-F]*$/.test(s) || s.length % 2 !== 0) return null;
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  return out;
}

function answer(status: number, body: Record<string, unknown>): Response {
  return new Response(JSON.stringify({ answered_by: "worker", version: VERSION, ...body }), {
    status,
    headers: { "Content-Type": "application/json", "Cache-Control": "no-store", "Telesphorepo-Edge-Check": "worker" },
  });
}
