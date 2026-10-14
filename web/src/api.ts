import { useEffect, useState } from "react";

export type User = { id: number; username: string; is_admin: boolean; disabled: boolean; created_at: string; kind?: "local" | "oidc"; email?: string };
export type RepoConfig = Record<string, unknown>;
export type Repo = { name: string; format: string; anonymous_read: boolean; config?: RepoConfig; permission: string; locked_by?: string | null };
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

export type Move = {
  id: number; state: "copying" | "done" | "failed" | "cancelled";
  total: number; copied: number; skipped: number; failed: number; error: string;
  started_at: string; finished_at: string | null;
  target: { storage: string; endpoint?: string; bucket?: string; prefix?: string; fs_root?: string };
};
export type MoveFailure = { asset_id: number; key: string; reason: string };

export const getCurrentMove = async (): Promise<Move | null> => {
  try { return await api<Move>("GET", "/api/storage/moves/current"); }
  catch (e) { if (e instanceof ApiError && e.status === 404) return null; throw e; }
};
export const getMove = (id: number) => api<Move>("GET", `/api/storage/moves/${id}`);
export const cancelMove = (id: number) => api("DELETE", `/api/storage/moves/${id}`);
export const moveFailures = (id: number, after?: string) =>
  api<Page<MoveFailure>>("GET", `/api/storage/moves/${id}/failures${qs({ after, limit: "50" })}`);

export type NexusSource = {
  id: number; name: string; base_url: string; state: "mirroring" | "cut_over";
  created_at: string; last_sync_at: string | null; last_error: string;
  cutover_sync_id: number; cutover_failed: number;
};
export type NexusSourceRepo = {
  source_repo: string; source_format: string; supported: boolean;
  target?: string | null; last_sync_at?: string | null;
};
export type NexusSourceDetail = NexusSource & { repos: NexusSourceRepo[] };
export type NexusMapping = { source_repo: string; target: string; create: boolean };
export type NexusSyncRepo = {
  source_repo: string; state: "pending" | "running" | "done" | "failed" | "cancelled";
  imported: number; skipped: number; ignored: number; failed: number;
};
export type NexusSync = {
  id: number; source_id: number; state: "running" | "done" | "failed" | "cancelled";
  error: string; started_at: string; finished_at: string | null; failed: number; repos?: NexusSyncRepo[];
};
export type NexusFailure = { source_repo: string; path: string; reason: string };

export const listNexusSources = () => api<NexusSource[]>("GET", "/api/nexus/sources");
export const addNexusSource = (name: string, base_url: string, username: string, password: string) =>
  api<NexusSource>("POST", "/api/nexus/sources", { name, base_url, username, password });
export const getNexusSource = (id: number) => api<NexusSourceDetail>("GET", `/api/nexus/sources/${id}`);
export const saveNexusRepos = (id: number, mappings: NexusMapping[]) =>
  api("PUT", `/api/nexus/sources/${id}/repos`, mappings);
export const updateNexusCredentials = (id: number, username: string, password: string) =>
  api("PATCH", `/api/nexus/sources/${id}`, { username, password });
export const deleteNexusSource = (id: number) => api("DELETE", `/api/nexus/sources/${id}`);
export const syncNexusNow = (id: number) => api<NexusSync>("POST", `/api/nexus/sources/${id}/sync`);
export const cutOverNexus = (id: number) => api("POST", `/api/nexus/sources/${id}/cutover`);
export const nexusSyncs = (id: number, after?: string) =>
  api<Page<NexusSync>>("GET", `/api/nexus/sources/${id}/syncs${qs({ after, limit: "10" })}`);
export const getNexusSync = (id: number) => api<NexusSync>("GET", `/api/nexus/syncs/${id}`);
export const cancelNexusSync = (id: number) => api("DELETE", `/api/nexus/syncs/${id}`);
export const nexusSyncFailures = (id: number, after?: string) =>
  api<Page<NexusFailure>>("GET", `/api/nexus/syncs/${id}/failures${qs({ after, limit: "50" })}`);
