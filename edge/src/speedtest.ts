// The speed test of the Edge delivery page. The server issues a pass (POST /api/edge/speedtest): it
// writes speedtest/<hex SHA-256 of a random token>.json = {hash, exp} to the home bucket and hands
// the token to the page. A download with the headers Telesphorepo-Edge-Region (a hint, "home" or
// "origin") and Telesphorepo-Edge-Pass (the token) then reads from that bucket, or goes to the
// server, instead of the nearest bucket. The choice is made only after the usual checks of the
// request, so it never serves what the Worker would not serve anyway; it only picks which bucket
// answers. Without a valid pass the headers change nothing.

import { type Env, edgeKey } from "./access";
import { hexBytes } from "./check";

export const REGION_HEADER = "Telesphorepo-Edge-Region";
export const PASS_HEADER = "Telesphorepo-Edge-Pass";
// OVERRIDE_HEADER says what became of the choice: "applied; region=<choice>", "refused;
// reason=pass" (no valid pass), "refused; reason=region" (the Worker does not read that region)
// or "unused" (the Worker passed the request to the server for its own reasons).
export const OVERRIDE_HEADER = "Telesphorepo-Edge-Override";

export interface Override {
  region: string; // a hint, "home" or "origin"
  pass: string;
}

// parseOverride reads the two headers; null when the request has neither.
export function parseOverride(h: Headers): Override | null {
  const region = (h.get(REGION_HEADER) ?? "").trim().toLowerCase();
  const pass = (h.get(PASS_HEADER) ?? "").trim();
  if (region === "" && pass === "") return null;
  return { region, pass };
}

// A pass that checked out stays valid in the isolate for at most a minute, never past its expiry.
const HOLD_MS = 60_000;
const MAX_HELD = 1000;
const held = new Map<string, number>(); // hex hash -> valid until (ms since the epoch)

// passValid reports whether token is the token of a pass that has not expired: the hash of a
// pass object of the home bucket, compared in constant time.
export async function passValid(env: Env, token: string): Promise<boolean> {
  if (token === "" || token.length > 128) return false;
  const got = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token)));
  const hex = Array.from(got, (b) => b.toString(16).padStart(2, "0")).join("");
  const now = Date.now();
  const until = held.get(hex);
  if (until !== undefined) {
    if (until > now) return true;
    held.delete(hex);
  }
  const obj = await env.HOME.get(edgeKey(env, "speedtest/" + hex + ".json"));
  if (!obj) return false;
  let v: unknown;
  try {
    v = JSON.parse(await obj.text());
  } catch {
    return false;
  }
  if (typeof v !== "object" || v === null) return false;
  const p = v as { hash?: unknown; exp?: unknown };
  if (typeof p.hash !== "string" || typeof p.exp !== "number") return false;
  const want = hexBytes(p.hash);
  if (!want || want.length !== got.length || !crypto.subtle.timingSafeEqual(want, got)) return false;
  if (p.exp * 1000 <= now) return false;
  if (held.size >= MAX_HELD) held.clear();
  held.set(hex, Math.min(p.exp * 1000, now + HOLD_MS));
  return true;
}
