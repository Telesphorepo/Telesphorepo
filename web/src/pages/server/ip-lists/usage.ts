import type { IPListUsage } from "$lib/api";

/** The repositories that use a network list, once each, by name. */
export const usedRepos = (u: IPListUsage): string[] => [...new Set(u.repos.map((r) => r.repo))].sort();

/** Every use of a network list, one line each, for a confirm dialog. */
export function usageLines(u: IPListUsage): string[] {
  return [
    ...u.repos.map((r) => `${r.scope === "read" ? "Downloads" : "Publishes"} of ${r.repo}`),
    ...(u.trusted_proxies ? ["The trusted proxies of Network and proxy"] : []),
  ];
}
