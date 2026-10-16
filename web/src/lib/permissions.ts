// The permission registry of the server (GET /api/permissions): every subsystem with its
// permissions, their labels, help and needs. Server permissions belong to a person; repository
// permissions belong to a person on one repository. auth.load() reads the registry before the
// first page renders. PermissionPicker shows one group of checkboxes per subsystem; tables show a
// set as one short phrase.

import { api, type PermId, type PermissionSubsystem } from "$lib/api";

export type PermissionInfo<P extends string = PermId> = { name: P; label: string; help: string };
export type PermissionGroup = { title: string; help: string; items: PermissionInfo[] };

let subsystems_: PermissionSubsystem[] = [];

/** Reads the registry from the server. */
export async function loadPermissions(): Promise<void> {
  subsystems_ = (await api.permissions()).subsystems;
}

const of = (scope: "server" | "repo") => subsystems_.filter((s) => s.scope === scope).flatMap((s) => s.permissions);

/** Every subsystem, in module start order. */
export const subsystems = (): PermissionSubsystem[] => subsystems_;

/** The server permissions in registry order. */
export const serverPermissions = (): PermissionInfo[] => of("server").map((p) => ({ name: p.id, label: p.label, help: p.help }));

/** The repository permissions in registry order. */
export const repoPermissions = (): PermissionInfo[] => of("repo").map((p) => ({ name: p.id, label: p.label, help: p.help }));

/** The permissions that each permission of scope needs. */
export const needsOf = (scope: "server" | "repo"): Record<PermId, PermId[]> =>
  Object.fromEntries(of(scope).map((p) => [p.id, p.needs]));

/** The groups of PermissionPicker: one per subsystem of scope, with its label and help. */
export const pickerGroups = (scope: "server" | "repo"): PermissionGroup[] =>
  subsystems_
    .filter((s) => s.scope === scope && s.permissions.length > 0)
    .map((s) => ({ title: s.label, help: s.help, items: s.permissions.map((p) => ({ name: p.id, label: p.label, help: p.help })) }));

/** The repository permissions whose holder manages the repository (they need a second factor). */
export const managingRepoPermissions = (): PermId[] => of("repo").filter((p) => p.second_factor).map((p) => p.id);

/** The server permissions that give every repository permission on every repository. */
export const allReposPermissions = (): PermId[] => of("server").filter((p) => p.all_repos).map((p) => p.id);

/** The label of a permission; the ID when the registry does not know it. */
export const labelOf = (id: PermId): string => [...of("server"), ...of("repo")].find((p) => p.id === id)?.label ?? id;

/** Puts a set in registry order. */
export const sortRepoPermissions = (set: readonly PermId[]): PermId[] => of("repo").map((p) => p.id).filter((x) => set.includes(x));
export const sortServerPermissions = (set: readonly PermId[]): PermId[] => of("server").map((p) => p.id).filter((x) => set.includes(x));

const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const phrase = (labels: string[]) => capital(labels.map((l, i) => (i === 0 ? l : l.toLowerCase())).join(", "));

/** A repository permission set as a short phrase: "Download", "Download, publish", "Everything". "" for none. */
export function repoPhrase(set: readonly PermId[] | null | undefined): string {
  const s = sortRepoPermissions(set ?? []);
  if (s.length > 0 && s.length === of("repo").length) return "Everything";
  return phrase(s.map(labelOf));
}

/** True when the set holds every server permission. */
export const isAdministrator = (set: readonly PermId[] | null | undefined): boolean =>
  of("server").length > 0 && of("server").every((p) => (set ?? []).includes(p.id));

/** A person's role from their server permissions: "Administrator", the list ("Manage people, manage storage"), or "". */
export function serverPhrase(set: readonly PermId[] | null | undefined): string {
  if (isAdministrator(set)) return "Administrator";
  return phrase(sortServerPermissions(set ?? []).map(labelOf));
}
