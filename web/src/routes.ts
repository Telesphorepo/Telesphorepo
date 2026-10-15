// The route table: one entry per page, first match wins. See src/README.md for the conventions.
// Pages load lazily, so each page is its own chunk. Settings pages also need an item in
// src/lib/nav.ts (its sub-pages do not). The Settings sitemap is docs/design/information-architecture.md.

import type { RedirectDef, RouteDef } from "$lib/router.svelte";

export const routes: RouteDef[] = [
  // --- Browse (anyone; signed-out visitors see the public repositories) ---
  { pattern: "/", layout: "app", access: "any", title: "Repositories", page: () => import("./pages/Home.svelte") },
  { pattern: "/search", layout: "app", access: "any", title: "Search", page: () => import("./pages/Search.svelte") },
  { pattern: "/login", layout: "app", access: "any", title: "Sign in", page: () => import("./pages/SignIn.svelte") },
  { pattern: "/device", layout: "app", access: "user", title: "Approve a device", page: () => import("./pages/DeviceApprove.svelte") },
  // One page for a repository: Files, Setup and Upload (the page checks the repo permission).
  { pattern: "/repo/:name/*rest", layout: "app", access: "any", title: "Repository", page: () => import("./pages/repo/RepoPage.svelte") },

  // --- Settings › Your account (any signed-in user) ---
  { pattern: "/settings/account", layout: "settings", access: "user", title: "Overview", page: () => import("./pages/account/Overview.svelte") },
  { pattern: "/settings/security", layout: "settings", access: "user", title: "Sign-in and security", page: () => import("./pages/account/Security.svelte") },
  { pattern: "/settings/devices", layout: "settings", access: "user", title: "Devices", page: () => import("./pages/account/Devices.svelte") },
  { pattern: "/settings/tokens", layout: "settings", access: "user", title: "Tokens", page: () => import("./pages/account/Tokens.svelte") },

  // --- Settings › Repositories (All repositories, or more than download on a repository) ---
  { pattern: "/settings/repositories", layout: "settings", access: "repositories", title: "Repositories", page: () => import("./pages/repositories/AllRepositories.svelte") },
  { pattern: "/settings/repositories/new", layout: "settings", access: "repos", title: "New repository", page: () => import("./pages/repositories/NewRepository.svelte") },
  // A repository's settings: Overview, Access, Format settings, Signing (Debian). Each tab needs its repository permission. Delete is a header action.
  { pattern: "/settings/repositories/:name/:tab", layout: "settings", access: "repo", title: "Repository settings", page: () => import("./pages/settings/repos/RepoSettings.svelte") },

  // --- Settings › People and CI (each page needs its permission) ---
  { pattern: "/settings/people", layout: "settings", access: "people", title: "People", page: () => import("./pages/server/People.svelte") },
  { pattern: "/settings/people/:name", layout: "settings", access: "people", title: "Person", page: () => import("./pages/server/people/Person.svelte") },
  { pattern: "/settings/ci-pipelines", layout: "settings", access: "pipelines", title: "CI pipelines", page: () => import("./pages/server/CIPublishing.svelte") },
  { pattern: "/settings/ci-pipelines/new", layout: "settings", access: "pipelines", title: "New CI pipeline", page: () => import("./pages/server/CIPublishingRule.svelte") },
  { pattern: "/settings/ci-pipelines/:id", layout: "settings", access: "pipelines", title: "CI pipeline", page: () => import("./pages/server/CIPublishingRule.svelte") },
  { pattern: "/settings/networks", layout: "settings", access: "network", title: "Networks", page: () => import("./pages/server/IPLists.svelte") },
  { pattern: "/settings/networks/new", layout: "settings", access: "network", title: "New network list", page: () => import("./pages/server/IPListPage.svelte") },
  { pattern: "/settings/networks/list/:name", layout: "settings", access: "network", title: "Network list", page: () => import("./pages/server/IPListPage.svelte") },

  // --- Settings › Server (each page needs its server permission) ---
  { pattern: "/settings/sign-in", layout: "settings", access: "signin", title: "Sign-in options", page: () => import("./pages/server/SingleSignOn.svelte") },
  { pattern: "/settings/signing-keys", layout: "settings", access: "keys", title: "Signing keys", page: () => import("./pages/server/SigningKeys.svelte") },
  { pattern: "/settings/signing-keys/new", layout: "settings", access: "keys", title: "New signing key", page: () => import("./pages/server/NewSigningKey.svelte") },
  { pattern: "/settings/storage", layout: "settings", access: "storage", title: "Storage", page: () => import("./pages/server/Storage.svelte") },
  { pattern: "/settings/nexus", layout: "settings", access: "nexus", title: "Nexus migration", page: () => import("./pages/server/NexusMigration.svelte") },
  { pattern: "/settings/nexus/:id", layout: "settings", access: "nexus", title: "Nexus server", page: () => import("./pages/server/NexusSource.svelte") },
  { pattern: "/settings/proxy", layout: "settings", access: "network", title: "Network and proxy", page: () => import("./pages/server/ReverseProxy.svelte") },
  { pattern: "/settings/general", layout: "settings", access: "general", title: "General", page: () => import("./pages/server/General.svelte") },
  { pattern: "/settings/activity", layout: "settings", access: "activity", title: "Activity", page: () => import("./pages/server/Activity.svelte") },
];

/** Paths that open another path (the query is kept). /settings opens the first page of your account. */
export const redirects: RedirectDef[] = [
  { pattern: "/settings", to: () => "/settings/account" },
];
