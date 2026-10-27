// The calls to the routes of the arch module.
import { request } from "$lib/api/client";

/** GET /api/arch/{name}/package?path=: a package file's index entry. */
export type ArchPackage = {
  name: string;
  version: string;
  architecture: string;
  /** The fields of the entry, e.g. DESC, URL, LICENSE, DEPENDS, PACKAGER, BUILDDATE, ISIZE. */
  fields: Record<string, string[]>;
};

export const archApi = {
  /** The index entry of a package file of an Arch repo. */
  package: (name: string, path: string) =>
    request<ArchPackage>("GET", `/api/arch/${encodeURIComponent(name)}/package`, { query: { path } }),
};
