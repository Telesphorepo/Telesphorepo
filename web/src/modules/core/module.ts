// The UI of the core module: browse, sign-in, your account, repositories, people and the General
// settings.

import type { ModuleUI } from "../types";
import LayoutDashboardIcon from "@lucide/svelte/icons/layout-dashboard";
import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
import KeyRoundIcon from "@lucide/svelte/icons/key-round";
import DatabaseIcon from "@lucide/svelte/icons/database";
import UsersIcon from "@lucide/svelte/icons/users-round";
import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";

export const core: ModuleUI = {
  id: "core",
  pages: [
    // Browse (anyone; signed-out visitors see the public repositories).
    { path: "/", layout: "app", access: "any", title: "Repositories", page: () => import("../../pages/Home.svelte") },
    { path: "/search", layout: "app", access: "any", title: "Search", page: () => import("../../pages/Search.svelte") },
    { path: "/login", layout: "app", access: "any", title: "Sign in", page: () => import("../../pages/SignIn.svelte") },
    // One page for a repository: Files, Use, Publish and Upload (the page checks the repo permission).
    { path: "/repo/:name/*rest", layout: "app", access: "any", title: "Repository", page: () => import("../../pages/repo/RepoPage.svelte") },

    // Settings › Your account (any signed-in user).
    {
      path: "/settings/account", layout: "settings", access: "user", title: "Overview",
      page: () => import("../../pages/account/Overview.svelte"),
      nav: { group: "account", label: "Overview", icon: LayoutDashboardIcon, order: 10 },
    },
    {
      path: "/settings/security", layout: "settings", access: "user", title: "Sign-in and security", setup: true,
      page: () => import("../../pages/account/Security.svelte"),
      nav: { group: "account", label: "Sign-in and security", icon: ShieldCheckIcon, order: 20 },
    },
    {
      path: "/settings/tokens", layout: "settings", access: "user", title: "Tokens",
      page: () => import("../../pages/account/Tokens.svelte"),
      nav: { group: "account", label: "Tokens", icon: KeyRoundIcon, order: 40 },
    },

    // Settings › Repositories (All repositories, or more than download and publish on a repository).
    {
      path: "/settings/repositories", layout: "settings", access: "repositories", title: "Repositories",
      page: () => import("../../pages/repositories/AllRepositories.svelte"),
      nav: { group: "repositories", label: "Repositories", icon: DatabaseIcon, order: 10 },
    },
    {
      path: "/settings/repositories/new", layout: "settings", access: "repositories.all", title: "New repository",
      page: () => import("../../pages/repositories/NewRepository.svelte"),
    },
    // A repository's settings: Overview, Access, Format settings, Remote and the tabs of other modules. Each tab needs its permission.
    {
      path: "/settings/repositories/:name/:tab", layout: "settings", access: "repo", title: "Repository settings",
      page: () => import("../../pages/settings/repos/RepoSettings.svelte"),
    },

    // Settings › People and CI.
    {
      path: "/settings/people", layout: "settings", access: "people.manage", title: "People",
      page: () => import("../../pages/server/People.svelte"),
      nav: { group: "people", label: "People", icon: UsersIcon, order: 10 },
    },
    {
      path: "/settings/people/:name", layout: "settings", access: "people.manage", title: "Person",
      page: () => import("../../pages/server/people/Person.svelte"),
    },

    // Settings › Server.
    {
      path: "/settings/general", layout: "settings", access: "settings.general", title: "General",
      page: () => import("../../pages/server/General.svelte"),
      nav: { group: "server", label: "General", icon: SlidersIcon, order: 60 },
    },
  ],
  // /settings opens the first page of your account.
  redirects: [{ path: "/settings", to: () => "/settings/account" }],
};
