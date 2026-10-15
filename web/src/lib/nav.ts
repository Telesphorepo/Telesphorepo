// The Settings sidebar (docs/design/information-architecture.md): four groups in display order,
// each with a heading and its pages. Every item has an icon. Admin groups are hidden from other
// users. A page here must also have a route in src/routes.ts; its sub-pages (path + "/...") mark
// the item as open, and the breadcrumbs (lib/crumbs.ts) start from the group and the item.

import type { Component } from "svelte";
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

export type NavItem = { label: string; path: string; icon: Component<{ class?: string }> };
export type NavGroup = { label: string; admin: boolean; items: NavItem[] };

export const settingsNav: NavGroup[] = [
  {
    label: "Your account",
    admin: false,
    items: [
      { label: "Overview", path: "/settings/account", icon: LayoutDashboardIcon },
      { label: "Sign-in and security", path: "/settings/security", icon: ShieldCheckIcon },
      { label: "Devices", path: "/settings/devices", icon: LaptopIcon },
      { label: "Tokens", path: "/settings/tokens", icon: KeyRoundIcon },
    ],
  },
  {
    label: "Repositories",
    admin: true,
    items: [
      { label: "All repositories", path: "/settings/repositories", icon: DatabaseIcon },
    ],
  },
  {
    label: "People and CI",
    admin: true,
    items: [
      { label: "People", path: "/settings/people", icon: UsersIcon },
      { label: "CI pipelines", path: "/settings/ci-pipelines", icon: WorkflowIcon },
      { label: "Networks", path: "/settings/networks", icon: NetworkIcon },
    ],
  },
  {
    label: "Server",
    admin: true,
    items: [
      { label: "Sign-in options", path: "/settings/sign-in", icon: LogInIcon },
      { label: "Signing keys", path: "/settings/signing-keys", icon: FileKeyIcon },
      { label: "Storage", path: "/settings/storage", icon: HardDriveIcon },
      { label: "Nexus migration", path: "/settings/nexus", icon: ArrowRightLeftIcon },
      { label: "Network and proxy", path: "/settings/proxy", icon: WaypointsIcon },
      { label: "General", path: "/settings/general", icon: SlidersIcon },
      { label: "Activity", path: "/settings/activity", icon: HistoryIcon },
    ],
  },
];

/** The pages a setup-only session (no second factor yet) can open. */
export const setupOnlyPaths = ["/settings/security"];

/** The group and the item that a Settings path belongs to (the item is the longest prefix). */
export function navPlace(path: string): { group: NavGroup; item: NavItem } | null {
  let best: { group: NavGroup; item: NavItem } | null = null;
  for (const group of settingsNav)
    for (const item of group.items)
      if ((path === item.path || path.startsWith(item.path + "/")) && (!best || item.path.length > best.item.path.length))
        best = { group, item };
  return best;
}
