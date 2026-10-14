// The Settings sidebar (spec §13): categories in display order, each with its pages. Admin
// categories are hidden from other users. A page here must also have a route in src/routes.ts.
// Labels use plain words (see src/README.md); a category with one page shows the category label.

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
      { label: "Profile", path: "/settings/profile" },
      { label: "Two-step verification", path: "/settings/security" },
      { label: "Devices", path: "/settings/devices" },
      { label: "Download tokens", path: "/settings/tokens" },
      { label: "Publish packages", path: "/settings/publish" },
    ],
  },
  {
    label: "Repositories",
    icon: PackageIcon,
    admin: true,
    items: [{ label: "Repositories", path: "/settings/repos" }],
  },
  {
    label: "People and sign-in",
    icon: UsersIcon,
    admin: true,
    items: [
      { label: "Users", path: "/settings/users" },
      { label: "Single sign-on", path: "/settings/sso" },
    ],
  },
  {
    label: "Publishing",
    icon: SendIcon,
    admin: true,
    items: [
      { label: "CI publishing rules", path: "/settings/trusts" },
      { label: "Publish history", path: "/settings/publish-log" },
    ],
  },
  {
    label: "Storage",
    icon: HardDriveIcon,
    admin: true,
    items: [{ label: "Storage", path: "/settings/storage" }],
  },
  {
    label: "Import from Nexus",
    icon: ArrowRightLeftIcon,
    admin: true,
    items: [{ label: "Import from Nexus", path: "/settings/nexus" }],
  },
  {
    label: "Server settings",
    icon: SlidersIcon,
    admin: true,
    items: [{ label: "Server settings", path: "/settings/system" }],
  },
  {
    label: "Activity log",
    icon: ScrollTextIcon,
    admin: true,
    items: [{ label: "Activity log", path: "/settings/audit" }],
  },
];

/** The pages a setup-only session (no second factor yet) can open. */
export const setupOnlyPaths = ["/settings/security"];
