// The facts of an audit event in words: "with a password", "Public", "from Download to
// Everything", "12 files". A key without words here shows as its name and value.

import type { AuditEvent } from "$lib/api";
import { formatSize } from "$lib/format";
import { formats } from "$lib/formats/registry.svelte";
import { repoPhrase, serverPhrase } from "$lib/permissions";
import { count } from "$lib/words";

const methods: Record<string, string> = {
  password: "with a password",
  totp: "with an authenticator app",
  passkey: "with a passkey",
  recovery: "with a recovery code",
};
const vias: Record<string, string> = { manual: "by hand", device: "from a device", oidc: "from a CI pipeline" };
const kinds: Record<string, string> = { hosted: "Hosted", proxy: "Proxy", group: "Group" };

const text = (v: unknown) => (typeof v === "string" ? v : JSON.stringify(v));
const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** A permission set of a permission event in words: "Download, publish", "Administrator", "Member", or "none". */
function permsText(action: string, v: unknown): string {
  const set = Array.isArray(v) ? v : [];
  if (action === "user.permissions" || action === "user.create") return serverPhrase(set) || "Member";
  return repoPhrase(set) || "none";
}

function fact(action: string, k: string, v: unknown): string | null {
  switch (k) {
    case "repo":
    case "sha256":
    case "expires_at":
      return null;
    case "method":
      return methods[text(v)] ?? `with ${text(v)}`;
    case "via":
      return vias[text(v)] ?? `via ${text(v)}`;
    case "anonymous_read":
      return v ? "Public" : "Private";
    case "disabled":
      return v ? "Disabled" : "Enabled";
    case "permissions":
      return permsText(action, v);
    case "size":
      return typeof v === "number" ? formatSize(v) : text(v);
    case "count":
    case "files":
      return typeof v === "number" ? count(v, "file", "files") : text(v);
    case "ranges":
      return typeof v === "number" ? count(v, "range", "ranges") : text(v);
    case "format":
      return formats.title(text(v));
    case "kind":
      return kinds[text(v)] ?? text(v);
    case "password":
      return v === "changed" ? "Password changed" : `password ${text(v)}`;
    case "second_factors":
      return v === "reset" ? "Two-step verification reset" : `second factors ${text(v)}`;
    case "imported":
      return v ? "Imported" : null;
    case "fields":
      return Array.isArray(v) && v.length ? capital(`changed ${v.map((f) => String(f).replace(/_/g, " ")).join(", ")}`) : null;
    case "fingerprint":
      return typeof v === "string" ? `key …${v.slice(-16)}` : text(v);
  }
  return `${k.replace(/_/g, " ")} ${text(v)}`;
}

/** The target (when it adds to the person and the repository) and the facts of an event, in words. */
export function detailFacts(e: AuditEvent): string[] {
  const d = e.detail ?? {};
  const out: string[] = [];
  if (e.target && e.target !== e.actor && e.target !== e.repo) out.push(e.target);
  const sets = e.action === "grant.set" || e.action === "user.permissions";
  if (sets && ("old" in d || "new" in d)) out.push(`from ${permsText(e.action, d.old)} to ${permsText(e.action, d.new)}`);
  for (const [k, v] of Object.entries(d)) {
    if (sets && (k === "old" || k === "new")) continue;
    const f = fact(e.action, k, v);
    if (f) out.push(f);
  }
  return out;
}
