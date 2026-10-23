// The UI of the core module: Home, Browse, Publish, sign-in, your account, the Settings overview,
// repositories, people and the General settings.

import type { ModuleUI } from "../types";
import CircleUserIcon from "@lucide/svelte/icons/circle-user-round";
import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
import KeyRoundIcon from "@lucide/svelte/icons/key-round";
import DatabaseIcon from "@lucide/svelte/icons/database";
import UsersIcon from "@lucide/svelte/icons/users-round";
import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";

export const core: ModuleUI = {
  id: "core",
  pages: [
    // The front doors: Home and Browse for anyone (signed-out visitors see the public repositories), Publish signed in.
    { path: "/", layout: "app", access: "any", title: "Home", page: () => import("../../pages/Home.svelte") },
    { path: "/browse", layout: "app", access: "any", title: "Repositories", page: () => import("../../pages/Browse.svelte") },
    { path: "/publish", layout: "app", access: "user", title: "Publish", page: () => import("../../pages/Publish.svelte") },
    { path: "/search", layout: "app", access: "any", title: "Search", page: () => import("../../pages/Search.svelte") },
    { path: "/login", layout: "app", access: "any", title: "Sign in", page: () => import("../../pages/SignIn.svelte") },
    // One page for a repository: Files, Use, Publish and Upload (the page checks the repo permission).
    { path: "/repo/:name/*rest", layout: "app", access: "any", title: "Repository", page: () => import("../../pages/repo/RepoPage.svelte") },

    // Settings › All settings: the first sidebar item, above the groups (SettingsNav); a card per page you can open.
    {
      path: "/settings/overview", layout: "settings", access: "user", title: "All settings",
      page: () => import("../../pages/settings/Overview.svelte"),
    },

    // Your account (any signed-in user): its own sidebar, apart from the admin pages of Settings.
    {
      path: "/account", layout: "account", access: "user", title: "Overview",
      page: () => import("../../pages/account/Overview.svelte"),
      nav: {
        group: "account", label: "Overview", icon: CircleUserIcon, order: 10,
      },
    },
    {
      path: "/account/security", layout: "account", access: "user", title: "Sign-in and security", setup: true,
      page: () => import("../../pages/account/Security.svelte"),
      nav: {
        group: "account", label: "Sign-in and security", icon: ShieldCheckIcon, order: 20,
      },
    },
    {
      path: "/account/tokens", layout: "account", access: "user", title: "Tokens",
      page: () => import("../../pages/account/Tokens.svelte"),
      nav: {
        group: "account", label: "Tokens", icon: KeyRoundIcon, order: 40,
      },
    },

    // Settings › Repositories (All repositories, or more than download and publish on a repository).
    {
      path: "/settings/repositories", layout: "settings", access: "repositories", title: "Repositories",
      page: () => import("../../pages/repositories/AllRepositories.svelte"),
      nav: {
        group: "repositories", label: "Repositories", icon: DatabaseIcon, order: 10,
      },
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
      nav: {
        group: "people", label: "People", icon: UsersIcon, order: 10,
      },
    },
    {
      path: "/settings/people/:name", layout: "settings", access: "people.manage", title: "Person",
      page: () => import("../../pages/server/people/Person.svelte"),
    },

    // Settings › Server.
    {
      path: "/settings/general", layout: "settings", access: "settings.general", title: "General",
      page: () => import("../../pages/server/General.svelte"),
      nav: {
        group: "server", label: "General", icon: SlidersIcon, order: 60,
      },
    },
  ],
  // /settings opens the overview (which goes on to your account when it has no card). The
  // /settings/ address of an account page opens it: the server prints /#/settings/tokens in a 401.
  redirects: [
    { path: "/settings", to: () => "/settings/overview" },
    { path: "/settings/account", to: () => "/account" },
    { path: "/settings/security", to: () => "/account/security" },
    { path: "/settings/tokens", to: () => "/account/tokens" },
  ],
};
