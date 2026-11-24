// An audit event in words, from the sentence and the fields of its action (GET /api/audit/actions):
// the sentence ("alice published 12 files to maven-releases"), the facts the sentence does not name
// ("with a password · 14 GiB"), and every fact with its label for the event's details. A detail
// key without a field is a quiet text fact named after the key; an action without a sentence
// reads as its label after the person, the repository and the target.

import type { AuditAction, AuditEvent, AuditField } from "$lib/api";
import { formatCount, formatDateTime, formatDuration, formatSize } from "$lib/format";
import { repoPhrase, serverPhrase } from "$lib/permissions";
import { count, someNames } from "$lib/words";
import { subjectParts } from "../ci/presets";

/** Who acted: a person, a CI job, the server itself (a job of a module), its command line, or someone without an account. */
export type Actor = {
  kind: "person" | "ci" | "server" | "cli" | "someone";
  /** The username, the CI subject, the module, "cli" or the name a failed sign-in tried. */
  name: string;
  /** How a sentence names it: "alice", "acme/app (branch main)", "the server". */
  label: string;
  /** The label is a phrase ("the server"), capitalized at the start of a sentence; a name stays as it is. */
  phrase: boolean;
  /** Who in full, for the event's details: the whole CI subject, the module of a job. */
  full: string;
};

/** One piece of a sentence: words, or a name the page shows in its own way. */
export type Part =
  | { t: "text"; text: string }
  | { t: "actor"; actor: Actor; text: string }
  | { t: "repo"; name: string }
  | { t: "target"; text: string }
  | { t: "value"; text: string };

/** A fact of the event's details: a label and its value in words. */
export type Fact = { key: string; label: string; value: string };

export type Worded = {
  sentence: Part[];
  /** The facts that are not quiet and that the sentence does not name, in words. */
  facts: string[];
  /** Every fact of the detail with its label, the hidden ones and the repository (its own line) left out. */
  details: Fact[];
};

/** Refusals carry a Refused badge. */
const refusals = new Set(["signin.failed", "sso.signin.failed"]);
export const refused = (action: string) => refusals.has(action);

const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** A detail key in words, for a key that no field names: "expires_at" is "Expires at". */
export const keyLabel = (key: string) => capital(key.replace(/[_.-]+/g, " ").trim());

export function actorOf(e: AuditEvent): Actor {
  const name = e.actor ?? "";
  if (e.actor_user_id != null) return { kind: "person", name, label: name, phrase: false, full: name };
  // A job of the server and its command line act without an address.
  if (!e.ip && name === "cli") return { kind: "cli", name, label: "the command line", phrase: true, full: "The server's command line" };
  if (!e.ip && name && name !== "anonymous") return { kind: "server", name, label: "the server", phrase: true, full: `The server (${name})` };
  const parts = subjectParts(name);
  if (parts.length > 1 || /[:/]/.test(name)) {
    const label = parts.length > 1 ? `${parts[0]} (${parts.slice(1).join(", ")})` : name;
    return { kind: "ci", name, label, phrase: false, full: `CI job ${name}` };
  }
  if (!name || name === "anonymous") return { kind: "someone", name, label: "someone", phrase: true, full: "Someone without an account" };
  return { kind: "someone", name, label: name, phrase: false, full: name };
}

/** Where a value shows: in the sentence, on the summary line, or in the details with its label. */
type Place = "sentence" | "facts" | "details";

const isEmpty = (v: unknown) => v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0);

/** Any value as text: a string as is, yes or no, a list joined, an object as JSON. */
function plain(v: unknown): string {
  if (typeof v === "string") return v;
  if (typeof v === "boolean") return v ? "Yes" : "No";
  if (typeof v === "number") return String(v);
  if (Array.isArray(v) && v.every((x) => typeof x !== "object")) return v.map(String).join(", ");
  return JSON.stringify(v);
}

const toDate = (v: unknown) => (typeof v === "number" ? new Date(v * 1000) : typeof v === "string" ? new Date(v) : null);

/** A value in the words of its field; "" shows nothing. */
export function formatValue(f: AuditField, v: unknown, place: Place): string {
  // An empty permission set is a value: no permissions.
  if (f.kind === "hidden" || (isEmpty(v) && !(f.kind === "perms" && Array.isArray(v)))) return "";
  switch (f.kind) {
    case "count":
      return typeof v === "number" ? (f.one || f.many ? count(v, f.one ?? f.many ?? "", f.many ?? f.one ?? "") : formatCount(v)) : plain(v);
    case "size":
      return typeof v === "number" ? formatSize(v) : plain(v);
    case "bool":
      if (place === "details") return v ? "Yes" : "No";
      return (v ? f.yes : f.no) ?? "";
    case "list": {
      if (!Array.isArray(v)) return plain(v);
      const names = v.map((x) => (typeof x === "object" ? JSON.stringify(x) : (f.values?.[String(x)] ?? String(x))));
      return place === "details" ? names.join(", ") : someNames(names, 3);
    }
    case "enum":
      return f.values?.[String(v)] ?? plain(v);
    case "perms": {
      const set = Array.isArray(v) ? v.map(String) : [];
      if (f.scope === "server") return serverPhrase(set) || "Member";
      return repoPhrase(set) || (place === "sentence" ? "no permissions" : "None");
    }
    case "time": {
      const d = toDate(v);
      return d && !isNaN(d.getTime()) ? formatDateTime(d.toISOString()) : plain(v);
    }
    case "duration":
      return typeof v === "number" ? formatDuration(v * 1000) : plain(v);
    default:
      return plain(v);
  }
}

/** The fields of an action by key, read once per action. */
const byKey = new WeakMap<AuditAction, Map<string, AuditField>>();
function fieldsOf(a: AuditAction | undefined): Map<string, AuditField> {
  if (!a) return new Map();
  let m = byKey.get(a);
  if (!m) byKey.set(a, (m = new Map((a.fields ?? []).map((f) => [f.key, f]))));
  return m;
}

/** A detail key without a field: a quiet text fact named after the key. */
const textField = (key: string): AuditField => ({ key, label: keyLabel(key), kind: "text", quiet: true });

const placeholder = /\{([A-Za-z0-9_.-]+)\}/g;

/** Joins the words around names that came out empty, so no double space or space before a comma stays. */
function tidy(parts: Part[]): Part[] {
  const out: Part[] = [];
  for (const p of parts) {
    const last = out[out.length - 1];
    if (p.t === "text" && last?.t === "text") last.text += p.text;
    else out.push({ ...p });
  }
  for (const p of out) if (p.t === "text") p.text = p.text.replace(/\s+/g, " ").replace(/ ([,.;:])/g, "$1");
  const first = out[0];
  const last = out[out.length - 1];
  if (first?.t === "text") first.text = first.text.trimStart();
  if (last?.t === "text") last.text = last.text.trimEnd();
  const kept = out.filter((p) => !(p.t === "text" && p.text === ""));
  const lead = kept[0];
  if (lead?.t === "text" || (lead?.t === "actor" && lead.actor.phrase)) lead.text = capital(lead.text);
  return kept;
}

/** The event in words, with its action (undefined until the actions load). */
export function wordEvent(e: AuditEvent, action: AuditAction | undefined): Worded {
  const fields = fieldsOf(action);
  const detail = e.detail ?? {};
  const actor = actorOf(e);
  const field = (key: string) => fields.get(key) ?? textField(key);
  const keys = Object.keys(detail);
  const target = e.target && e.target !== e.repo && e.target !== e.actor ? e.target : "";

  const used = new Set<string>();
  let sentence: Part[] = [];
  const text = action?.sentence ?? "";
  if (text) {
    let at = 0;
    for (const m of text.matchAll(placeholder)) {
      sentence.push({ t: "text", text: text.slice(at, m.index) });
      at = (m.index ?? 0) + m[0].length;
      const key = m[1];
      if (key === "actor") sentence.push({ t: "actor", actor, text: actor.label });
      else if (key === "repo") {
        used.add("repo");
        sentence.push(e.repo ? { t: "repo", name: e.repo } : { t: "text", text: "a repository" });
      }
      else if (key === "target") {
        if (e.target) sentence.push({ t: "target", text: e.target });
      } else {
        used.add(key);
        const v = formatValue(field(key), detail[key], "sentence");
        if (v) sentence.push({ t: "value", text: v });
      }
    }
    sentence.push({ t: "text", text: text.slice(at) });
  } else {
    // No sentence: the person, what happened, then the repository and the target.
    sentence = [{ t: "actor", actor, text: actor.label }, { t: "text", text: ` · ${action?.label ?? e.action}` }];
    if (e.repo) sentence.push({ t: "text", text: " · " }, { t: "repo", name: e.repo });
    used.add("repo");
    if (target) sentence.push({ t: "text", text: " · " }, { t: "target", text: target });
  }

  const facts: string[] = [];
  const details: Fact[] = [];
  // The registered fields in their order, then the keys that no field names.
  const order = [...(action?.fields ?? []).map((f) => f.key).filter((k) => keys.includes(k)), ...keys.filter((k) => !fields.has(k))];
  for (const key of order) {
    const f = field(key);
    if (f.kind === "hidden") continue;
    const v = detail[key];
    if (!f.quiet && !used.has(key)) {
      const s = formatValue(f, v, "facts");
      if (s) facts.push(s);
    }
    const d = formatValue(f, v, "details");
    // The repository has its own line in the details, with its format icon.
    if (d && key !== "repo") details.push({ key, label: f.label || keyLabel(key), value: d });
  }
  return { sentence: tidy(sentence), facts, details };
}

const clock = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
/** The time of day of an event in the reader's time zone: "14:05", "2:05 PM". */
export const timeOfDay = (iso: string) => clock.format(new Date(iso));
/** True when the reader's clock counts AM and PM, so the column of times is wider. */
export const twelveHour = /^h1[12]$/.test(clock.resolvedOptions().hourCycle ?? "");

const exact = new Intl.DateTimeFormat(undefined, {
  weekday: "long",
  day: "numeric",
  month: "long",
  year: "numeric",
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit",
  timeZoneName: "short",
});
/** The exact time of an event with its time zone: "Tuesday, 24 November 2026 at 14:05:32 CET". */
export const exactTime = (iso: string) => exact.format(new Date(iso));
