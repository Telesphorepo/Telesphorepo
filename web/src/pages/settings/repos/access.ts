// The facts of a repository's access, as the Access tab reads and changes them. Every change of the
// tab applies at once: the Public switch, the allowed networks, the people and the CI pipelines.

import type { AccessPipeline, IPRule, RepoAccess, Trust } from "$lib/api";

/** The allowed networks as rules: addresses and ranges, then IP lists. */
export function rulesOf(a: RepoAccess): IPRule[] {
  const n = a.download.networks;
  return [
    ...n.cidrs.map((c) => ({ kind: "cidr" as const, value: c.cidr, note: c.note })),
    ...n.lists.map((l) => ({ kind: "list" as const, value: l.name, note: "" })),
  ];
}

export const sameRule = (a: IPRule, b: IPRule) => a.kind === b.kind && a.value === b.value;

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

/** What a pipeline's conditions allow: "GitHub Actions · acme/app · branch main · release.yml". */
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

function and(xs: string[]): string {
  return xs.length <= 1 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`;
}
