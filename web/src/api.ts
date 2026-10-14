import { useEffect, useState } from "react";

export type User = { id: number; username: string; is_admin: boolean; disabled: boolean; created_at: string; kind?: "local" | "oidc"; email?: string };
export type RepoConfig = Record<string, unknown>;
export type Repo = { name: string; format: string; anonymous_read: boolean; config?: RepoConfig; permission: string };
export type Format = { name: string; title: string; default_config: RepoConfig; settings_group?: string };
export type Snippet = { title: string; lang: string; code: string };
export type Usage = { base_url: string; snippets: Snippet[] };
export type Asset = { path: string; size: number; sha256: string; content_type: string; kind: string; updated_at: string };
export type Page<T> = { items: T[]; next: string | null };
export type Token = { id: string; name: string; created_at: string; expires_at: string | null; last_used_at: string | null; token?: string };

export class ApiError extends Error {
  constructor(public status: number, message: string, public fields?: Record<string, string>) { super(message); }
}

export async function api<T = unknown>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401 && url !== "/api/auth/login") {
    window.dispatchEvent(new Event("unauthorized"));
  }
  if (!res.ok) {
    let msg = res.statusText;
    let fields: Record<string, string> | undefined;
    try { const j = await res.json(); msg = j.error ?? msg; fields = j.fields; } catch { /* not JSON */ }
    throw new ApiError(res.status, msg, fields);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}

export const qs = (o: Record<string, string | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v) p.set(k, v);
  const s = p.toString();
  return s ? "?" + s : "";
};

export function useAsync<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState("");
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    setError("");
    fn().then((d) => live && setData(d), (e) => live && setError(e.message));
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, n]);
  return { data, error, reload: () => setN((x) => x + 1) };
}

export function useHash() {
  const get = () => window.location.hash.replace(/^#/, "") || "/";
  const [h, setH] = useState(get);
  useEffect(() => {
    const f = () => setH(get());
    window.addEventListener("hashchange", f);
    return () => window.removeEventListener("hashchange", f);
  }, []);
  return h;
}

export const go = (to: string) => { window.location.hash = to; };

export const fmtSize = (n: number) =>
  n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / 1048576).toFixed(1)} MiB`;
export const fmtTime = (s: string | null) => (s ? new Date(s).toLocaleString() : "never");
