// Publish session tokens that the Browse pages created for uploads and deletes, one per repo.
// They stay in memory only (never in storage or the URL) and are dropped shortly before they
// expire, or when the server refuses one.

import type { PublishSession } from "$lib/api";

const sessions = new Map<string, { token: string; expires: number }>();
const margin = 30_000;

/** The token of a live session for the repo, or undefined. */
export function sessionToken(repo: string): string | undefined {
  const s = sessions.get(repo);
  if (!s) return undefined;
  if (s.expires - margin < Date.now()) {
    sessions.delete(repo);
    return undefined;
  }
  return s.token;
}

export function keepSession(repo: string, s: PublishSession) {
  sessions.set(repo, { token: s.token, expires: new Date(s.expires_at).getTime() });
}

export function dropSession(repo: string) {
  sessions.delete(repo);
}
