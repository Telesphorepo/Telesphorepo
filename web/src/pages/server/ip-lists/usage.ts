import type { IPListUsage } from "$lib/api";

/** The repositories that use a network list, once each, by name. */
export const usedRepos = (u: IPListUsage): string[] => [...u.repos].sort();

/** What uses a network list, by name: the repositories, then "Network and proxy" for the trusted proxies. */
export const usedNames = (u: IPListUsage): string[] => [...usedRepos(u), ...(u.trusted_proxies ? ["Network and proxy"] : [])];

/** Every use of a network list, one line each, for a confirm dialog. */
export function usageLines(u: IPListUsage): string[] {
  return [
    ...u.repos.map((r) => `The allowed networks of ${r}`),
    ...(u.trusted_proxies ? ["The trusted proxies of Network and proxy"] : []),
  ];
}
