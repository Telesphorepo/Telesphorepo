// The UI modules in the server's module start order (internal/modules/modules.go), and the
// registry that the router, the Settings and Your account sidebars, the breadcrumbs, the
// setup-only allowlist, the repository settings tabs, New repository and the file details read. A
// module without UI (raw) has no entry.

import type { NavGroup, PageDef, RedirectDef, RepoFieldDef, RepoTabDef, ModuleUI, SidebarArea } from "./types";
import { auth } from "$lib/auth.svelte";
import { core } from "./core/module";
import { audit } from "./audit/module";
import { storage } from "./storage/module";
import { backup } from "./backup/module";
import { network } from "./network/module";
import { signing } from "./signing/module";
import { publish } from "./publish/module";
import { edge } from "./edge/module";
import { sso } from "./sso/module";
import { nexus } from "./nexus/module";
import { maven } from "./maven/module";
import { debian } from "./debian/module";
import { arch } from "./arch/module";
import { rpm } from "./rpm/module";
import { docker } from "./docker/module";

export const modules: ModuleUI[] = [core, audit, storage, backup, network, signing, publish, edge, sso, nexus, maven, debian, arch, rpm, docker];

/** Every page, in module order; the first whose path matches wins. */
export const pages: PageDef[] = modules.flatMap((m) => m.pages ?? []);

/** Every redirect. */
export const redirects: RedirectDef[] = modules.flatMap((m) => m.redirects ?? []);

/** The paths a setup-only session (no second factor yet) can open. */
export const setupPaths: string[] = pages.filter((p) => p.setup).map((p) => p.path);

/** One group of a sidebar and its pages, in order. */
export type NavSection = { id: NavGroup; label: string; items: PageDef[] };

const groupLabels: Record<NavGroup, string> = {
  account: "Your account",
  repositories: "Repositories",
  people: "People and CI",
  server: "Server",
};

/** Every sidebar group (Your account, then the groups of Settings), each with its pages in order. */
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

/** The sidebar a group belongs to. */
export const areaOf = (g: NavGroup): SidebarArea => (g === "account" ? "account" : "settings");

/** The groups of a sidebar with the pages that `opens` lets the person open; a group without one is left out. */
export const visibleNav = (area: SidebarArea, opens: (p: PageDef) => boolean): NavSection[] =>
  navSections
    .filter((g) => areaOf(g.id) === area)
    .map((g) => ({ ...g, items: g.items.filter(opens) }))
    .filter((g) => g.items.length > 0);

/** True when the signed-in person may open the page (a setup-only session: only its setup pages). */
export const canOpen = (p: PageDef): boolean => auth.allows(p.access) && (!auth.setupOnly || !!p.setup);

/**
 * The groups of the Settings sidebar and overview with the pages the person may open. Empty: the
 * person opens no admin page, and the top bar has no Settings.
 */
export const adminNav = (): NavSection[] => visibleNav("settings", canOpen);

/** The group and the sidebar page that a Settings or account path belongs to (the page is the longest prefix). */
export function navPlace(path: string): { group: NavSection; item: PageDef } | null {
  let best: { group: NavSection; item: PageDef } | null = null;
  for (const group of navSections)
    for (const item of group.items)
      if ((path === item.path || path.startsWith(item.path + "/")) && (!best || item.path.length > best.item.path.length))
        best = { group, item };
  return best;
}
