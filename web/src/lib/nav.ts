// The Settings sidebar (docs/design/information-architecture.md): four groups in display order,
// each with a heading and its pages. Every item has an icon and the access of its route: a person
// sees only the items they may open (visibleNav), and a group without one is hidden. A page here
// must also have a route in src/routes.ts; its sub-pages (path + "/...") mark the item as open,
// and the breadcrumbs (lib/crumbs.ts) start from the group and the item.

import type { Component } from "svelte";
import type { Access } from "$lib/router.svelte";
import { auth } from "$lib/auth.svelte";
import LayoutDashboardIcon from "@lucide/svelte/icons/layout-dashboard";
import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
import LaptopIcon from "@lucide/svelte/icons/laptop";
import KeyRoundIcon from "@lucide/svelte/icons/key-round";
import DatabaseIcon from "@lucide/svelte/icons/database";
import UsersIcon from "@lucide/svelte/icons/users-round";
import WorkflowIcon from "@lucide/svelte/icons/workflow";
import NetworkIcon from "@lucide/svelte/icons/network";
import LogInIcon from "@lucide/svelte/icons/log-in";
import FileKeyIcon from "@lucide/svelte/icons/file-key";
import HardDriveIcon from "@lucide/svelte/icons/hard-drive";
import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";
import WaypointsIcon from "@lucide/svelte/icons/waypoints";
import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";
import HistoryIcon from "@lucide/svelte/icons/history";

export type NavItem = { label: string; path: string; icon: Component<{ class?: string }>; access: Access };
export type NavGroup = { label: string; items: NavItem[] };

export const settingsNav: NavGroup[] = [
  {
    label: "Your account",
    items: [
      { label: "Overview", path: "/settings/account", icon: LayoutDashboardIcon, access: "user" },
      { label: "Sign-in and security", path: "/settings/security", icon: ShieldCheckIcon, access: "user" },
      { label: "Devices", path: "/settings/devices", icon: LaptopIcon, access: "user" },
      { label: "Tokens", path: "/settings/tokens", icon: KeyRoundIcon, access: "user" },
    ],
  },
  {
    label: "Repositories",
    items: [
      { label: "Repositories", path: "/settings/repositories", icon: DatabaseIcon, access: "repositories" },
    ],
  },
  {
    label: "People and CI",
    items: [
      { label: "People", path: "/settings/people", icon: UsersIcon, access: "people" },
      { label: "CI pipelines", path: "/settings/ci-pipelines", icon: WorkflowIcon, access: "pipelines" },
      { label: "Networks", path: "/settings/networks", icon: NetworkIcon, access: "network" },
    ],
  },
  {
    label: "Server",
    items: [
      { label: "Sign-in options", path: "/settings/sign-in", icon: LogInIcon, access: "signin" },
      { label: "Signing keys", path: "/settings/signing-keys", icon: FileKeyIcon, access: "keys" },
      { label: "Storage", path: "/settings/storage", icon: HardDriveIcon, access: "storage" },
      { label: "Nexus migration", path: "/settings/nexus", icon: ArrowRightLeftIcon, access: "nexus" },
      { label: "Network and proxy", path: "/settings/proxy", icon: WaypointsIcon, access: "network" },
      { label: "General", path: "/settings/general", icon: SlidersIcon, access: "general" },
      { label: "Activity", path: "/settings/activity", icon: HistoryIcon, access: "activity" },
    ],
  },
];

/** The pages a setup-only session (no second factor yet) can open. */
export const setupOnlyPaths = ["/settings/security"];

/** The groups and items that the signed-in person may open; a setup-only session sees only its page. */
export function visibleNav(): NavGroup[] {
  return settingsNav
    .map((g) => ({
      ...g,
      items: g.items.filter((i) => auth.allows(i.access) && (!auth.setupOnly || setupOnlyPaths.includes(i.path))),
    }))
    .filter((g) => g.items.length > 0);
}

/** The group and the item that a Settings path belongs to (the item is the longest prefix). */
export function navPlace(path: string): { group: NavGroup; item: NavItem } | null {
  let best: { group: NavGroup; item: NavItem } | null = null;
  for (const group of settingsNav)
    for (const item of group.items)
      if ((path === item.path || path.startsWith(item.path + "/")) && (!best || item.path.length > best.item.path.length))
        best = { group, item };
  return best;
}
