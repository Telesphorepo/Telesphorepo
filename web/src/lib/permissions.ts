// The permission names, their words, and the rules between them (docs/superpowers/specs, granular
// permissions). Server permissions belong to a person; repository permissions belong to a person
// on one repository. PermissionPicker shows them; tables show a set as one short phrase.

import type { RepoPermission, ServerPermission } from "$lib/api";

export type PermissionInfo<P extends string> = { name: P; label: string; help: string };

/** The server permissions in display order. */
export const SERVER_PERMISSIONS: PermissionInfo<ServerPermission>[] = [
  { name: "repos", label: "All repositories", help: "Create and delete repositories, and use every repository with every permission." },
  { name: "people", label: "People", help: "Add, change and disable people, and give them the permissions you hold." },
  { name: "signin", label: "Sign-in options", help: "Change single sign-on and password sign-in." },
  { name: "storage", label: "Storage", help: "Change where the server keeps files, and move them." },
  { name: "network", label: "Network", help: "Change network lists and trusted proxies." },
  { name: "keys", label: "Signing keys", help: "Create, import and delete the keys that sign Debian repositories." },
  { name: "nexus", label: "Nexus migration", help: "Copy repositories from Nexus and switch over." },
  { name: "general", label: "General", help: "Change the public address, upload limits, logs and retention." },
  { name: "activity", label: "Activity", help: "Read the activity of the server and the publish tokens." },
];

/** The repository permissions in display order. */
export const REPO_PERMISSIONS: PermissionInfo<RepoPermission>[] = [
  { name: "download", label: "Download files", help: "Read the files of the repository." },
  { name: "publish", label: "Publish", help: "Publish from a computer or the browser." },
  { name: "delete", label: "Delete files", help: "Delete files and folders." },
  { name: "settings", label: "Edit settings", help: "Change the settings and the network rules." },
  { name: "access", label: "Manage access", help: "Give and remove the permissions of this repository that you hold." },
  { name: "pipelines", label: "CI pipelines", help: "Add and change the CI pipelines that publish here." },
];

/** A permission and the permission it needs: checking the first checks the second. */
export const REPO_NEEDS: Partial<Record<RepoPermission, RepoPermission>> = { publish: "download", pipelines: "publish" };

/** Puts a set in display order. */
export const sortRepoPermissions = (set: readonly RepoPermission[]): RepoPermission[] =>
  REPO_PERMISSIONS.map((x) => x.name).filter((x) => set.includes(x));
export const sortServerPermissions = (set: readonly ServerPermission[]): ServerPermission[] =>
  SERVER_PERMISSIONS.map((x) => x.name).filter((x) => set.includes(x));

/** Lower-case words of a repository permission inside a phrase. */
const repoWord: Record<RepoPermission, string> = {
  download: "download",
  publish: "publish",
  delete: "delete files",
  settings: "edit settings",
  access: "manage access",
  pipelines: "CI pipelines",
};

const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** A repository permission set as a short phrase: "Download", "Download, publish", "Everything". "" for none. */
export function repoPhrase(set: readonly RepoPermission[] | null | undefined): string {
  const s = sortRepoPermissions(set ?? []);
  if (s.length === REPO_PERMISSIONS.length) return "Everything";
  return capital(s.map((p) => repoWord[p]).join(", "));
}

/** True when the set holds every server permission. */
export const isAdministrator = (set: readonly ServerPermission[] | null | undefined): boolean =>
  SERVER_PERMISSIONS.every((p) => (set ?? []).includes(p.name));

/** A person's role from their server permissions: "Administrator", the list ("People, storage"), or "". */
export function serverPhrase(set: readonly ServerPermission[] | null | undefined): string {
  if (isAdministrator(set)) return "Administrator";
  return capital(sortServerPermissions(set ?? []).map((p) => serverWord[p]).join(", "));
}

/** Lower-case words of a server permission inside a phrase. */
const serverWord: Record<ServerPermission, string> = {
  repos: "all repositories",
  people: "people",
  signin: "sign-in options",
  storage: "storage",
  network: "network",
  keys: "signing keys",
  nexus: "Nexus migration",
  general: "general",
  activity: "activity",
};

const pick = <P extends string>(all: PermissionInfo<P>[], names: P[]) => all.filter((x) => names.includes(x.name));

/** The groups of PermissionPicker for server permissions. */
export const SERVER_PICKER_GROUPS = [
  { title: "People and repositories", items: pick(SERVER_PERMISSIONS, ["repos", "people"]) },
  { title: "Server settings", items: pick(SERVER_PERMISSIONS, ["signin", "storage", "network", "keys", "nexus", "general", "activity"]) },
];

/** The group of PermissionPicker for repository permissions. */
export const REPO_PICKER_GROUPS = [{ title: "Permissions on the repository", items: REPO_PERMISSIONS }];