// Hash router. URLs look like /#/repo/maven-releases/com/acme/?q=x so that the server never sees
// UI paths and links that clients print (/#/device?code=, /#/settings/tokens) keep working. Every view
// has its own address, so the browser's back button returns to the previous view.
//
// The route table is in src/routes.ts. A pattern is a path with ":name" segments and an optional
// trailing "*rest" segment that takes the rest of the path (slashes included).

import type { Component } from "svelte";
import type { ServerPermission } from "$lib/api";

/** "app": the page in the container. "settings": the Settings sidebar and the page. */
export type Layout = "app" | "settings";
/**
 * Who may open a page: anyone, a signed-in user, or a signed-in user with a server permission.
 * Three more depend on repository permissions: "repositories" (All repositories, or more than
 * download on a repository), "repo" (a settings tab of the repository `:name`) and "pipelines"
 * (All repositories, or CI pipelines on a repository). `auth.allows` checks them.
 */
export type Access = "any" | "user" | ServerPermission | "repositories" | "repo" | "pipelines";

export type RouteDef = {
  pattern: string;
  /** Lazy import of the page component. Pages receive `params` and `query` as props. */
  page: () => Promise<{ default: Component<PageProps> }>;
  layout: Layout;
  access: Access;
  /** Title in the browser tab ("<title> · Telesphorepo") and the last breadcrumb. */
  title: string;
};

/** A path that opens another path: `to` gets the pattern's params. The query is kept. */
export type RedirectDef = { pattern: string; to: (p: Params) => string };

export type Params = Record<string, string>;
export type PageProps = { params: Params; query: URLSearchParams };

export type Match = { route: RouteDef; params: Params };

const read = () => {
  const h = window.location.hash.replace(/^#/, "") || "/";
  const i = h.indexOf("?");
  return { path: i < 0 ? h : h.slice(0, i), search: i < 0 ? "" : h.slice(i + 1) };
};

/**
 * Decides whether a navigation to `target` ("#/path?query") may go ahead. It returns false to
 * stop it; it then asks the user itself (the unsaved-changes guard). `fromHistory` is true for the
 * back and forward buttons, whose address has already changed.
 */
export type Guard = (target: string, fromHistory: boolean) => boolean;
let guard: Guard | null = null;

/** Installs the one navigation guard (lib/unsaved.svelte.ts). */
export const setGuard = (g: Guard | null) => {
  guard = g;
};

class Router {
  path = $state("/");
  search = $state("");
  query = $derived(new URLSearchParams(this.search));

  constructor() {
    const r = read();
    this.path = r.path;
    this.search = r.search;
    window.addEventListener("hashchange", () => {
      const next = read();
      const target = "#" + next.path + (next.search ? "?" + next.search : "");
      if (target === this.hash) return;
      if (guard && !guard(target, true)) {
        // Put the current view's address back; the guard asks and goes on with `target` if the user agrees.
        history.replaceState(history.state, "", this.hash);
        return;
      }
      this.path = next.path;
      this.search = next.search;
    });
  }

  /** The current address: "#/path?query". */
  get hash() {
    return "#" + this.path + (this.search ? "?" + this.search : "");
  }
}

export const router = new Router();

/** The href for a UI path: href("/repo/x") gives "#/repo/x". */
export const href = (path: string, query?: Record<string, string | undefined>) => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) if (v) q.set(k, v);
  const s = q.toString();
  return "#" + path + (s ? "?" + s : "");
};

/**
 * Opens a UI path. replace: true does not add a history entry. force: true skips the
 * unsaved-changes guard (the guard itself uses it once the user agreed to leave).
 */
export function navigate(path: string, opts: { query?: Record<string, string | undefined>; replace?: boolean; force?: boolean } = {}) {
  go(href(path, opts.query), opts);
}

/** Opens an address that is already a "#/path?query" href. */
export function go(target: string, opts: { replace?: boolean; force?: boolean } = {}) {
  if (!opts.force && !opts.replace && guard && target !== router.hash && !guard(target, false)) return;
  if (opts.replace) {
    history.replaceState(history.state, "", target);
  } else {
    history.pushState(history.state, "", target);
  }
  const next = read();
  router.path = next.path;
  router.search = next.search;
}

/** Encodes each segment of a repo path for use in a UI path. */
export const encodePath = (p: string) => p.split("/").map(encodeURIComponent).join("/");

/** The UI path of a repository page in Browse: "" is Files, else "use", "publish", "upload". */
export const repoPath = (name: string, rest = "") => "/repo/" + encodeURIComponent(name) + (rest ? "/" + rest : "");

/** The UI path of a folder ("" is the top, else ends with "/") or a file in the Files tab. */
export const filesPath = (name: string, path = "") => repoPath(name, path ? "files/" + encodePath(path) : "");

/** The tabs of a repository's settings, in the Settings shell. */
export type RepoSettingsTab = "overview" | "access" | "format" | "signing" | "remote";

/** The UI path of a repository's settings tab: /settings/repositories/<name>/<tab>. */
export const repoSettingsPath = (name: string, tab: RepoSettingsTab = "overview") =>
  "/settings/repositories/" + encodeURIComponent(name) + "/" + tab;

/** True when `path` is `prefix` or below it; for "active" states in navigation. */
export const isActive = (prefix: string, path = router.path) =>
  prefix === "/" ? path === "/" : path === prefix || path.startsWith(prefix + "/");

/** The params of `path` under `pattern`, or null when it does not match. */
export function matchPattern(pattern: string, path: string): Params | null {
  const ps = pattern.split("/").filter(Boolean);
  const xs = path.split("/").filter(Boolean);
  const params: Params = {};
  for (let i = 0; i < ps.length; i++) {
    const p = ps[i];
    if (p.startsWith("*")) {
      // The rest keeps a trailing slash, which marks a folder in repo paths.
      const rest = xs.slice(i).join("/");
      params[p.slice(1)] = decodeSegments(rest) + (rest && path.endsWith("/") ? "/" : "");
      return params;
    }
    if (i >= xs.length) return null;
    if (p.startsWith(":")) params[p.slice(1)] = safeDecode(xs[i]);
    else if (p !== xs[i]) return null;
  }
  return xs.length === ps.length ? params : null;
}

const safeDecode = (s: string) => {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
};
const decodeSegments = (s: string) => s.split("/").map(safeDecode).join("/");

/** The first route whose pattern matches the path. */
export function match(routes: RouteDef[], path: string): Match | null {
  for (const route of routes) {
    const params = matchPattern(route.pattern, path);
    if (params) return { route, params };
  }
  return null;
}

/** The path that a redirect sends `path` to, or null. */
export function redirectOf(redirects: RedirectDef[], path: string): string | null {
  for (const r of redirects) {
    const params = matchPattern(r.pattern, path);
    if (params) return r.to(params);
  }
  return null;
}
