// The Settings sidebar (spec §13): categories in display order, each with its pages. Admin
// categories are hidden from other users. A page here must also have a route in src/routes.ts.

import type { Component } from "svelte";
import UserIcon from "@lucide/svelte/icons/user";
import PackageIcon from "@lucide/svelte/icons/package";
import UsersIcon from "@lucide/svelte/icons/users";
import SendIcon from "@lucide/svelte/icons/send";
import HardDriveIcon from "@lucide/svelte/icons/hard-drive";
import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";
import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";
import ScrollTextIcon from "@lucide/svelte/icons/scroll-text";

export type NavItem = { label: string; path: string };
export type NavCategory = { label: string; icon: Component<{ class?: string }>; admin: boolean; items: NavItem[] };

export const settingsNav: NavCategory[] = [
  {
    label: "Account",
    icon: UserIcon,
    admin: false,
    items: [
      { label: "Profile and password", path: "/settings/profile" },
      { label: "Second factor", path: "/settings/security" },
      { label: "Devices", path: "/settings/devices" },
      { label: "Read tokens", path: "/settings/tokens" },
      { label: "New publish session", path: "/settings/publish" },
    ],
  },
  {
    label: "Repositories",
    icon: PackageIcon,
    admin: true,
    items: [{ label: "Repositories", path: "/settings/repos" }],
  },
  {
    label: "Users & access",
    icon: UsersIcon,
    admin: true,
    items: [
      { label: "Users", path: "/settings/users" },
      { label: "SSO", path: "/settings/sso" },
    ],
  },
  {
    label: "Publishing",
    icon: SendIcon,
    admin: true,
    items: [
      { label: "Trust policies", path: "/settings/trusts" },
      { label: "Publish log", path: "/settings/publish-log" },
    ],
  },
  {
    label: "Storage",
    icon: HardDriveIcon,
    admin: true,
    items: [{ label: "Storage", path: "/settings/storage" }],
  },
  {
    label: "Migration",
    icon: ArrowRightLeftIcon,
    admin: true,
    items: [{ label: "Nexus sources", path: "/settings/nexus" }],
  },
  {
    label: "System",
    icon: SlidersIcon,
    admin: true,
    items: [{ label: "General settings", path: "/settings/system" }],
  },
  {
    label: "Audit log",
    icon: ScrollTextIcon,
    admin: true,
    items: [{ label: "Audit log", path: "/settings/audit" }],
  },
];

/** The pages a setup-only session (no second factor yet) can open. */
export const setupOnlyPaths = ["/settings/security"];
