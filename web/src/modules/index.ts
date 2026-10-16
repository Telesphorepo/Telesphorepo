// The UI modules in the server's module start order (internal/modules/modules.go), and the
// registry that the router, the Settings sidebar, the breadcrumbs, the setup-only allowlist, the
// repository settings tabs, New repository and the file details read. A module without UI (raw)
// has no entry.

import type { NavGroup, PageDef, RedirectDef, RepoFieldDef, RepoTabDef, ModuleUI } from "./types";
import { core } from "./core/module";
import { audit } from "./audit/module";
import { storage } from "./storage/module";
import { network } from "./network/module";
import { signing } from "./signing/module";
import { publish } from "./publish/module";
import { sso } from "./sso/module";
import { nexus } from "./nexus/module";
import { maven } from "./maven/module";
import { debian } from "./debian/module";

export const modules: ModuleUI[] = [core, audit, storage, network, signing, publish, sso, nexus, maven, debian];

/** Every page, in module order; the first whose path matches wins. */
export const pages: PageDef[] = modules.flatMap((m) => m.pages ?? []);

/** Every redirect. */
export const redirects: RedirectDef[] = modules.flatMap((m) => m.redirects ?? []);

/** The paths a setup-only session (no second factor yet) can open. */
export const setupPaths: string[] = pages.filter((p) => p.setup).map((p) => p.path);

/** One group of the Settings sidebar and its pages, in order. */
export type NavSection = { id: NavGroup; label: string; items: PageDef[] };

const groupLabels: Record<NavGroup, string> = {
  account: "Your account",
  repositories: "Repositories",
  people: "People and CI",
  server: "Server",
};

/** The groups of the Settings sidebar, each with its pages in order. */
export const navSections: NavSection[] = (Object.keys(groupLabels) as NavGroup[]).map((id) => ({
  id,
  label: groupLabels[id],
  items: pages.filter((p) => p.nav?.group === id).sort((a, b) => a.nav!.order - b.nav!.order),
}));

/** The repository settings tab of a module, by ID. */
export const repoTab = (id: string): RepoTabDef | undefined => modules.flatMap((m) => m.repoTabs ?? []).find((t) => t.id === id);

/** The control of a repository field, by field name. */
export const repoField = (field: string): RepoFieldDef | undefined =>
  modules.flatMap((m) => m.repoFields ?? []).find((f) => f.field === field);

/** The component of a format part of the file details, or undefined. */
export const formatPart = (format: string, part: string) => {
  for (const m of modules) {
    const c = m.formatParts?.[format]?.[part];
    if (c) return c;
  }
  return undefined;
};

/** The groups of the Settings sidebar with the pages that `opens` lets the person open; a group without one is left out. */
export const visibleNav = (opens: (p: PageDef) => boolean): NavSection[] =>
  navSections.map((g) => ({ ...g, items: g.items.filter(opens) })).filter((g) => g.items.length > 0);

/** The group and the sidebar page that a Settings path belongs to (the page is the longest prefix). */
export function navPlace(path: string): { group: NavSection; item: PageDef } | null {
  let best: { group: NavSection; item: PageDef } | null = null;
  for (const group of navSections)
    for (const item of group.items)
      if ((path === item.path || path.startsWith(item.path + "/")) && (!best || item.path.length > best.item.path.length))
        best = { group, item };
  return best;
}
