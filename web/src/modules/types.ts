// The shape of a module's UI contribution. Each module of the server that has UI
// exports one ModuleUI from web/src/modules/<id>/module.ts; modules/index.ts lists them in the
// server's module start order. The router, the Settings and Your account sidebars, the
// breadcrumbs, the setup-only allowlist, the repository settings tabs, the repository fields of
// New repository and the format parts of the file details read them.

import type { Component } from "svelte";
import type { FileInfo, Format, PermId, Repo } from "$lib/api";
import type { PageProps } from "$lib/router.svelte";

/**
 * Who may open a page: anyone, a signed-in user, "repositories" (repositories.all, or a
 * repository permission that needs a second factor on some repository), "repo" (the page checks
 * the permission on the repository `:name` itself), or a permission ID from GET /api/permissions
 * (a server permission, or a repository permission on at least one repository).
 */
/** A permission ID needs that permission; a list of them needs every one (a Nexus merge). */
export type Access = "any" | "user" | "repositories" | "repo" | PermId | PermId[];

/** The sidebar groups, in display order: "account" is the Your account sidebar, the others the Settings sidebar. */
export type NavGroup = "account" | "repositories" | "people" | "server";

/** A page with a sidebar: "settings" (the admin panel) or "account" (Your account). */
export type SidebarArea = "settings" | "account";

export type PageDef = {
  /** A path with ":name" segments and an optional trailing "*rest" segment, e.g. "/settings/nexus/:id". */
  path: string;
  /** The title in the browser tab ("<title> · Telesphorepo") and the last breadcrumb. */
  title: string;
  /** "app": the page in the container. "settings": the Settings sidebar and the page. "account": the Your account sidebar and the page. */
  layout: "app" | SidebarArea;
  access: Access;
  /** Lazy import of the page component. Pages receive `params` and `query` as props. */
  page: () => Promise<{ default: Component<PageProps> }>;
  /**
   * The page's item in its sidebar (the group "account" for an account page); its sub-pages
   * (path + "/...") need none.
   */
  nav?: { group: NavGroup; label: string; icon: Component<{ class?: string }>; order: number };
  /** A setup-only session (no second factor yet) may open it. */
  setup?: boolean;
};

/** A path that opens another path: `to` gets the pattern's params. The query is kept. */
export type RedirectDef = { path: string; to: (p: Record<string, string>) => string };

/** The props of a repository settings tab. */
export type RepoTabProps = { repo: Repo; onChanged: () => void };

/**
 * A repository settings tab of a module. The settings show it when the repository's format lists
 * its ID in repo_tabs, for a hosted repository, to a person with the repository permission access.
 */
export type RepoTabDef = {
  id: string;
  label: string;
  access: PermId;
  icon: typeof import("@lucide/svelte/icons/file-key").default;
  component: () => Promise<{ default: Component<RepoTabProps> }>;
};

/** What a repository field control reports to New repository. */
export type RepoFieldState = {
  /** The value of the field; undefined: the server's default. */
  value: unknown;
  /** False while the field needs an answer. */
  ready: boolean;
  /** Runs before the repository is created, e.g. creates a new signing key. */
  prepare?: () => Promise<void>;
  /** Undoes prepare after a failed create; answers a sentence when it fails. */
  undo?: () => Promise<string>;
};

/** The props of a repository field control. */
export type RepoFieldProps = { id: string; repoName: string; format: Format; onchange: (s: RepoFieldState) => void };

/** A repository field of a module (Format.repo_fields): its control on New repository. */
export type RepoFieldDef = { field: string; label: string; component: () => Promise<{ default: Component<RepoFieldProps> }> };

/** The props of a format part of the file details. */
export type FilePartProps = { repo: Repo; file: FileInfo };

export type ModuleUI = {
  /** The ID of the server module. */
  id: string;
  pages?: PageDef[];
  redirects?: RedirectDef[];
  repoTabs?: RepoTabDef[];
  repoFields?: RepoFieldDef[];
  /** Format -> part name (Format.file_details) -> the component. */
  formatParts?: Record<string, Record<string, () => Promise<{ default: Component<FilePartProps> }>>>;
};
