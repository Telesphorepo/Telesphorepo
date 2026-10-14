// The route table: one entry per page, first match wins. See src/README.md for the conventions.
// Pages load lazily, so each page is its own chunk.

import type { RouteDef } from "$lib/router.svelte";

export const routes: RouteDef[] = [
  // --- Browse (anyone; signed-out visitors see the repos that anyone can download from) ---
  { pattern: "/", layout: "browse", access: "any", title: "Repositories", page: () => import("./pages/browse/Home.svelte") },
  { pattern: "/repo/:name/*path", layout: "browse", access: "any", title: "Repository", page: () => import("./pages/browse/Repo.svelte") },
  { pattern: "/search", layout: "browse", access: "any", title: "Search", page: () => import("./pages/browse/Search.svelte") },
  { pattern: "/login", layout: "bare", access: "any", title: "Sign in", page: () => import("./pages/SignIn.svelte") },
  { pattern: "/device", layout: "bare", access: "any", title: "Approve a device", page: () => import("./pages/DeviceApprove.svelte") },

  // --- Settings: Account (any signed-in user) ---
  { pattern: "/settings/profile", layout: "settings", access: "user", title: "Profile", page: () => import("./pages/settings/Profile.svelte") },
  { pattern: "/settings/security", layout: "settings", access: "user", title: "Two-step verification", page: () => import("./pages/settings/Security.svelte") },
  { pattern: "/settings/devices", layout: "settings", access: "user", title: "Devices", page: () => import("./pages/settings/Devices.svelte") },
  { pattern: "/settings/tokens", layout: "settings", access: "user", title: "Download tokens", page: () => import("./pages/settings/Tokens.svelte") },
  { pattern: "/settings/publish", layout: "settings", access: "user", title: "Publish packages", page: () => import("./pages/settings/Publish.svelte") },

  // --- Settings: admin ---
  { pattern: "/settings/repos", layout: "settings", access: "admin", title: "Repositories", page: () => import("./pages/settings/Repos.svelte") },
  { pattern: "/settings/repos/:name", layout: "settings", access: "admin", title: "Repository settings", page: () => import("./pages/settings/RepoSettings.svelte") },
  { pattern: "/settings/repos/:name/:tab", layout: "settings", access: "admin", title: "Repository settings", page: () => import("./pages/settings/RepoSettings.svelte") },
  { pattern: "/settings/users", layout: "settings", access: "admin", title: "Users", page: () => import("./pages/settings/Users.svelte") },
  { pattern: "/settings/sso", layout: "settings", access: "admin", title: "Single sign-on", page: () => import("./pages/settings/SSO.svelte") },
  { pattern: "/settings/trusts", layout: "settings", access: "admin", title: "CI publishing rules", page: () => import("./pages/settings/Trusts.svelte") },
  { pattern: "/settings/publish-log", layout: "settings", access: "admin", title: "Publish history", page: () => import("./pages/settings/PublishLog.svelte") },
  { pattern: "/settings/storage", layout: "settings", access: "admin", title: "Storage", page: () => import("./pages/settings/Storage.svelte") },
  { pattern: "/settings/nexus", layout: "settings", access: "admin", title: "Import from Nexus", page: () => import("./pages/settings/Nexus.svelte") },
  { pattern: "/settings/nexus/:id", layout: "settings", access: "admin", title: "Nexus server", page: () => import("./pages/settings/NexusSource.svelte") },
  { pattern: "/settings/system", layout: "settings", access: "admin", title: "Server settings", page: () => import("./pages/settings/System.svelte") },
  { pattern: "/settings/audit", layout: "settings", access: "admin", title: "Activity log", page: () => import("./pages/settings/Audit.svelte") },
];

/**
 * Paths that open another page. /publishing is printed by the server (the write-gate error), so
 * it must keep working; /settings opens the first Account page.
 */
export const redirects: Record<string, string> = {
  "/publishing": "/settings/publish",
  "/settings": "/settings/profile",
};
