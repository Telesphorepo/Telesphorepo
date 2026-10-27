// The calls to the routes of the rpm module.
import { request } from "$lib/api/client";

/** GET /api/rpm/{name}/package?path=: a package file's identity and its index entry. */
export type RPMPackage = {
  name: string;
  /** [epoch:]version-release */
  version: string;
  architecture: string;
  summary: string;
  installed_size: number;
  /** Unix seconds. */
  build_time: number;
  /** 4, or 6 for a package of rpm 6. */
  rpm_format: number;
  /** description, url, license, vendor, group, packager, sourcerpm: those the package has. */
  fields: Record<string, string>;
  /** requires, provides, conflicts, obsoletes, recommends, suggests, supplements, enhances. */
  dependencies: Record<string, string[]>;
};

export const rpmApi = {
  /** The identity and index entry of a package file of an RPM repo. */
  package: (name: string, path: string) =>
    request<RPMPackage>("GET", `/api/rpm/${encodeURIComponent(name)}/package`, { query: { path } }),
};