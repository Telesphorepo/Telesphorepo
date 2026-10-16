// The facts of a repository's access, as the Access tab edits them. The Access tab holds an
// AccessDraft; `changesOf` lists what a save does, one line each. The access of people is not in
// the draft: the tab changes it at once.

import type { AccessGuards, AccessNetworks, AccessPipeline, IPList, IPRule, RepoAccess, Trust } from "$lib/api";

/** What the Access tab saves from its save bar. ci: the ids of the CI pipelines that list the repo. */
export type AccessDraft = {
  public: boolean;
  ci: number[];
  read: IPRule[];
  publish: IPRule[];
};

/** The IP rules of one operation, from the report of the network guard (none without one). */
const rulesOf = (g: AccessGuards): IPRule[] => {
  const n = g.network?.detail as AccessNetworks | undefined;
  if (!n) return [];
  return [
    ...n.lists.map((l) => ({ kind: "list" as const, value: l.name, note: "" })),
    ...n.cidrs.map((c) => ({ kind: "cidr" as const, value: c.cidr, note: c.note })),
  ];
};

export function draftOf(a: RepoAccess): AccessDraft {
  return {
    public: a.download.public,
    ci: a.publish.ci.map((c) => c.id).sort((x, y) => x - y),
    read: rulesOf(a.download.guards),
    publish: rulesOf(a.publish.guards),
  };
}

const githubIssuer = "https://token.actions.githubusercontent.com";

/** A CI pipeline from its rule, read the way the server reads it (internal/access). */
export function pipelineOf(t: Trust): AccessPipeline {
  const p: AccessPipeline = {
    id: t.id,
    rule: t.name,
    system: t.issuer === githubIssuer || t.issuer.startsWith(githubIssuer + "/") ? "github-actions" : "other",
    issuer: t.issuer,
    disabled: t.disabled,
  };
  const add = (k: "repositories" | "branches" | "tags" | "workflows" | "subjects", v: string) => (p[k] = [...(p[k] ?? []), v]);
  for (const name of Object.keys(t.claims).sort())
    for (const v of t.claims[name]) {
      if (name === "repository") add("repositories", v);
      else if (name === "ref" && v.startsWith("refs/heads/")) add("branches", v.slice(11));
      else if (name === "ref" && v.startsWith("refs/tags/")) add("tags", v.slice(10));
      else if (name === "workflow_ref" || name === "job_workflow_ref") add("workflows", v.split("@")[0].replace(/^.*?(\.github\/workflows\/)/, "$1"));
      else if (name === "workflow") add("workflows", v);
      else if (name === "sub") add("subjects", v);
    }
  return p;
}

/** What a pipeline's conditions allow, for its row: "acme/app · branch main · release.yml". */
export function pipelineFacts(p: AccessPipeline): string {
  const parts: string[] = [p.system === "github-actions" ? "GitHub Actions" : p.system === "jenkins" ? "Jenkins" : hostOf(p.issuer)];
  if (p.repositories?.length) parts.push(and(p.repositories));
  if (p.branches?.length) parts.push(`${p.branches.length === 1 ? "branch" : "branches"} ${and(p.branches)}`);
  if (p.tags?.length) parts.push(`${p.tags.length === 1 ? "tag" : "tags"} ${and(p.tags)}`);
  if (p.workflows?.length) parts.push(and(p.workflows.map((w) => w.split("/").pop() ?? w)));
  if (p.subjects?.length && !p.repositories?.length) parts.push(and(p.subjects));
  return parts.join(" · ");
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

export function and(xs: string[]): string {
  return xs.length <= 1 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`;
}

/** The names that a rule's "only from" says: list titles, then the address or the count of addresses. */
export function networkNames(rules: IPRule[], lists: IPList[] | undefined): string[] {
  const title = (name: string) => lists?.find((l) => l.name === name)?.title ?? name;
  const out = rules.filter((r) => r.kind === "list").map((r) => title(r.value));
  const cidrs = rules.filter((r) => r.kind === "cidr");
  if (cidrs.length === 1) out.push(cidrs[0].value);
  else if (cidrs.length > 1) out.push(`${cidrs.length} address ranges`);
  return out;
}

const sameRules = (a: IPRule[], b: IPRule[]) =>
  JSON.stringify(a.map((r) => [r.kind, r.value, r.note])) === JSON.stringify(b.map((r) => [r.kind, r.value, r.note]));

/** Each unsaved change, one per line, in the words of the confirm dialog. */
export function changesOf(repo: string, saved: AccessDraft, d: AccessDraft, pipelines: Map<number, AccessPipeline>, lists: IPList[] | undefined): string[] {
  const out: string[] = [];
  if (d.public !== saved.public)
    out.push(
      d.public
        ? `Anyone can download every file of ${repo} without signing in, and search shows its files to visitors`
        : `Visitors who are not signed in can no longer download from ${repo}; builds without a read token stop working`,
    );
  const name = (id: number) => pipelines.get(id)?.rule ?? `CI pipeline ${id}`;
  for (const id of d.ci) if (!saved.ci.includes(id)) out.push(`The CI pipeline ${name(id)} can publish to ${repo}; its open publish tokens end`);
  for (const id of saved.ci) if (!d.ci.includes(id)) out.push(`The CI pipeline ${name(id)} can no longer publish to ${repo}; its open publish tokens end`);
  const scopes = [
    { key: "read" as const, verb: "Downloads from", who: "download" },
    { key: "publish" as const, verb: "Publishes to", who: "publish" },
  ];
  for (const s of scopes) {
    if (sameRules(saved[s.key], d[s.key])) continue;
    const n = networkNames(d[s.key], lists);
    out.push(n.length ? `${s.verb} ${repo} must come from ${and(n)}, for everyone` : `${s.verb} ${repo} can come from any network`);
  }
  return out;
}

/** How many separate changes the draft holds (the save bar's count). */
export function countOf(saved: AccessDraft, d: AccessDraft): number {
  let n = d.public !== saved.public ? 1 : 0;
  n += d.ci.filter((id) => !saved.ci.includes(id)).length + saved.ci.filter((id) => !d.ci.includes(id)).length;
  for (const k of ["read", "publish"] as const) {
    const key = (r: IPRule) => `${r.kind}:${r.value}:${r.note}`;
    const a = new Set(saved[k].map(key));
    const b = new Set(d[k].map(key));
    for (const x of a) if (!b.has(x)) n++;
    for (const x of b) if (!a.has(x)) n++;
  }
  return n;
}
