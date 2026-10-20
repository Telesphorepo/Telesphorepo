// What a CI pipeline accepts, in the words a developer knows: a GitHub repository with its
// branches, tags and workflow file, or a Jenkins address and its jobs. The page builds the token
// conditions (claims, in the API) from these answers and reads them back. A pipeline is a trust
// policy in the API.

import type { Trust } from "$lib/api";

export type System = "github" | "jenkins" | "other";

export const GITHUB_ISSUER = "https://token.actions.githubusercontent.com";

export const systemTitle: Record<System, string> = {
  github: "GitHub Actions",
  jenkins: "Jenkins",
  other: "Another CI system",
};

/** The runs of one GitHub repository that may publish. */
export type GitHubJobs = { repo: string; branches: string[]; tags: string[]; workflow: string };
/** The jobs of one Jenkins that may publish; jwks only when this server cannot reach Jenkins. */
export type JenkinsJobs = { url: string; jobs: string[]; jwks: string };
/** One token condition: a claim and its allowed values. */
export type Condition = { key: string; values: string[] };
/** What the API stores. */
export type Accepts = { issuer: string; audience: string; jwks: string; claims: Record<string, string[]> };

export const emptyGitHub = (): GitHubJobs => ({ repo: "", branches: ["main"], tags: [], workflow: "" });
export const emptyJenkins = (): JenkinsJobs => ({ url: "", jobs: [], jwks: "" });

const trimSlash = (s: string) => s.trim().replace(/\/+$/, "");

const WORKFLOWS = ".github/workflows/";

/**
 * The repository and the workflow file in what a person pastes from GitHub: the address of the
 * repository (with or without .git, a clone address git@github.com:acme/app.git), of any page in
 * it, of a file (blob, raw.githubusercontent.com), of a workflow's Actions page
 * (/actions/workflows/release.yml), or owner/name, .github/workflows/release.yml and release.yml.
 * A part it does not find is "".
 */
export function parseGitHub(s: string): { repo: string; workflow: string } {
  let t = s.trim().replace(/[?#].*$/, "");
  let repo = "";
  let rest = "";
  const host = /^(?:(?:https?|ssh|git):\/\/)?(?:[^@/]+@)?(?:www\.)?(github\.com|raw\.githubusercontent\.com)[:/](.*)$/i.exec(t);
  if (host) {
    const parts = host[2].split("/").filter(Boolean);
    if (parts.length >= 2) {
      repo = `${parts[0]}/${parts[1].replace(/\.git$/i, "")}`;
      rest = parts.slice(2).join("/");
    }
  } else if (/^[\w.-]+\/[\w.-]+\/?$/.test(t) && !t.startsWith(".github/")) {
    repo = trimSlash(t).replace(/\.git$/i, "");
  } else {
    rest = t;
  }
  t = rest;
  let workflow = "";
  const i = t.indexOf(WORKFLOWS);
  const actions = /^actions\/workflows\/([^/]+)$/.exec(t);
  if (i >= 0) workflow = t.slice(i + WORKFLOWS.length).split("/")[0];
  else if (actions) workflow = actions[1];
  else if (!host && /^[\w.-]+\.ya?ml$/i.test(t)) workflow = t;
  try {
    workflow = decodeURIComponent(workflow);
  } catch {
    /* not percent-encoded: keep it as it is */
  }
  return { repo, workflow };
}

/** "https://github.com/acme/app.git", a page or a file in it, and "acme/app/" become "acme/app". */
export function gitHubRepo(s: string): string {
  return parseGitHub(s).repo || trimSlash(s.trim());
}

/** "release.yml" from ".github/workflows/release.yml" or a pasted address of the file or of its Actions page; "" from a repository's address. */
export function workflowFile(s: string): string {
  const p = parseGitHub(s);
  if (p.workflow || p.repo) return p.workflow;
  const t = trimSlash(s.trim().replace(/[?#].*$/, ""));
  return t.slice(t.lastIndexOf("/") + 1);
}

export function gitHubAccepts(g: GitHubJobs): Accepts {
  const repo = gitHubRepo(g.repo);
  const claims: Record<string, string[]> = { repository: [repo] };
  const refs = [...g.branches.map((b) => `refs/heads/${b}`), ...g.tags.map((t) => `refs/tags/${t}`)];
  if (refs.length) claims.ref = refs;
  const file = workflowFile(g.workflow);
  if (file) claims.workflow_ref = [`${repo}/.github/workflows/${file}@*`];
  return { issuer: GITHUB_ISSUER, audience: "", jwks: "", claims };
}

/** The GitHub answers behind a pipeline, or null when its conditions say more than they can. */
export function readGitHub(a: Accepts): GitHubJobs | null {
  if (a.issuer !== GITHUB_ISSUER || a.audience || a.jwks) return null;
  const { repository, ref = [], workflow_ref, ...rest } = a.claims;
  if (Object.keys(rest).length || repository?.length !== 1) return null;
  const repo = repository[0];
  const branches = ref.filter((r) => r.startsWith("refs/heads/")).map((r) => r.slice(11));
  const tags = ref.filter((r) => r.startsWith("refs/tags/")).map((r) => r.slice(10));
  if (branches.length + tags.length !== ref.length || branches.some((b) => !b) || tags.some((t) => !t)) return null;
  let workflow = "";
  if (workflow_ref) {
    const prefix = `${repo}/.github/workflows/`;
    const w = workflow_ref.length === 1 ? workflow_ref[0] : "";
    if (!w.startsWith(prefix) || !w.endsWith("@*")) return null;
    workflow = w.slice(prefix.length, -2);
    if (!workflow || workflow.includes("/")) return null;
  }
  return { repo, branches, tags, workflow };
}

/** The Jenkins address in a pasted address of Jenkins or of a job in it: "https://ci.example.com/job/app/" → "https://ci.example.com". */
export function jenkinsURL(s: string): string {
  const t = s.trim().replace(/[?#].*$/, "");
  const i = t.indexOf("/job/");
  return trimSlash(i >= 0 ? t.slice(0, i) : t);
}

/** The full job name in a pasted job address: "https://ci.example.com/job/team/job/app/" → "team/app"; a name stays as it is. */
export function jenkinsJob(s: string): string {
  const t = s.trim().replace(/[?#].*$/, "");
  if (!/^https?:\/\//i.test(t)) return t;
  const i = t.indexOf("/job/");
  if (i < 0) return t;
  // "/job/team/job/app/lastBuild/": each name follows a "job".
  const parts = ["job", ...t.slice(i + 5).split("/")];
  const names: string[] = [];
  for (let k = 0; k + 1 < parts.length && parts[k] === "job" && parts[k + 1]; k += 2) names.push(parts[k + 1]);
  return names.join("/");
}

export const jenkinsIssuer =(url: string) => `${trimSlash(url)}/oidc`;

/** The job address that Jenkins puts in the token: "team/app" → "<url>/job/team/job/app/". */
function jobAddress(url: string, job: string): string {
  const parts = job.split("/").map((p) => p.trim()).filter(Boolean);
  const path = parts.join("/job/");
  return `${trimSlash(url)}/job/${path}${path.endsWith("*") ? "" : "/"}`;
}

export function jenkinsAccepts(j: JenkinsJobs): Accepts {
  return { issuer: jenkinsIssuer(j.url), audience: "", jwks: j.jwks.trim(), claims: { sub: j.jobs.map((job) => jobAddress(j.url, job)) } };
}

/** The Jenkins answers behind a pipeline, or null when its conditions say more than they can. */
export function readJenkins(a: Accepts): JenkinsJobs | null {
  if (!a.issuer.endsWith("/oidc") || a.audience) return null;
  const url = a.issuer.slice(0, -5);
  const { sub, ...rest } = a.claims;
  if (Object.keys(rest).length || !sub?.length) return null;
  const jobs: string[] = [];
  for (const s of sub) {
    if (!s.startsWith(`${url}/job/`)) return null;
    const job = s.slice(url.length + 5).replace(/\/$/, "").split("/job/").join("/");
    if (!job || jobAddress(url, job) !== s) return null;
    jobs.push(job);
  }
  return { url, jobs, jwks: a.jwks };
}

/** The CI system of a stored pipeline. */
export function systemOf(issuer: string): System {
  if (issuer === GITHUB_ISSUER) return "github";
  if (issuer.endsWith("/oidc")) return "jenkins";
  return "other";
}

/** One token field that a condition checks, in the words of the page. */
export type ConditionField = { key: string; label: string; help: string; example: string };

const githubFields: ConditionField[] = [
  { key: "repository", label: "GitHub repository", help: "The owner and the name of the repository that runs the workflow.", example: "acme/app" },
  { key: "ref", label: "Branches and tags", help: "A branch starts with refs/heads/ and a tag with refs/tags/.", example: "refs/heads/main" },
  { key: "workflow_ref", label: "Workflow", help: "The workflow file that runs the job, with @ and the branch or tag.", example: "acme/app/.github/workflows/release.yml@*" },
  { key: "environment", label: "Environment", help: "The GitHub environment that the job uses.", example: "production" },
];
const jenkinsFields: ConditionField[] = [
  { key: "sub", label: "Jenkins job", help: "The address of the job, with a slash at the end.", example: "https://jenkins.example.com/job/app/" },
];

/** The token fields that a pipeline of this CI system usually checks. */
export const knownFields: Record<System, ConditionField[]> = { github: githubFields, jenkins: jenkinsFields, other: [] };

/** The words of a token field: a known field's label and help, or the field name itself. */
export function conditionField(system: System, key: string): ConditionField {
  return knownFields[system].find((f) => f.key === key) ?? { key, label: key, help: "The token must carry one of these values in this field.", example: "" };
}

/** The conditions of a pipeline, known fields first in their order. */
export function conditionsOf(system: System, claims: Record<string, string[]>): Condition[] {
  const order = (k: string) => {
    const i = knownFields[system].findIndex((f) => f.key === k);
    return i < 0 ? 99 : i;
  };
  return Object.entries(claims)
    .map(([key, values]) => ({ key, values: [...values] }))
    .sort((a, b) => order(a.key) - order(b.key) || a.key.localeCompare(b.key));
}

/** The name of the CI system with this address. */
export function ciSystemTitle(issuer: string): string {
  const s = systemOf(issuer);
  if (s !== "other") return systemTitle[s];
  try {
    return new URL(issuer).host || issuer;
  } catch {
    return issuer;
  }
}

const list = (xs: string[]) => (xs.length <= 1 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);

/** What a pipeline accepts, in a few words: "acme/app, branch main, release.yml". */
export function acceptsText(t: Pick<Trust, "issuer" | "audience" | "jwks" | "claims">): string {
  const g = readGitHub(t);
  if (g) {
    const parts = [g.repo];
    if (g.branches.length) parts.push(`${g.branches.length === 1 ? "branch" : "branches"} ${list(g.branches)}`);
    if (g.tags.length) parts.push(`${g.tags.length === 1 ? "tag" : "tags"} ${list(g.tags)}`);
    if (!g.branches.length && !g.tags.length) parts.push("every branch and tag");
    if (g.workflow) parts.push(g.workflow);
    return parts.join(", ");
  }
  const j = readJenkins(t);
  if (j) return list(j.jobs);
  const n = Object.keys(t.claims).length;
  return `${n} ${n === 1 ? "condition" : "conditions"}`;
}
