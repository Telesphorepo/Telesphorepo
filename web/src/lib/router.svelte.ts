// Hash router. URLs look like /#/repo/maven-releases/com/acme/?q=x so that the server never sees
// UI paths and links that clients print (/#/device?code=, /#/publishing) keep working.
//
// The route table is in src/routes.ts. A pattern is a path with ":name" segments and an optional
// trailing "*rest" segment that takes the rest of the path (slashes included).

import type { Component } from "svelte";

export type Layout = "browse" | "settings" | "bare";
/** Who may open a page: anyone, a signed-in user, or an admin. */
export type Access = "any" | "user" | "admin";

export type RouteDef = {
  pattern: string;
  /** Lazy import of the page component. Pages receive `params` and `query` as props. */
  page: () => Promise<{ default: Component<PageProps> }>;
  layout: Layout;
  access: Access;
  /** Title in the browser tab: "<title> · Telesphorepo". */
  title: string;
};

export type Params = Record<string, string>;
export type PageProps = { params: Params; query: URLSearchParams };

export type Match = { route: RouteDef; params: Params };

const read = () => {
  const h = window.location.hash.replace(/^#/, "") || "/";
  const i = h.indexOf("?");
  return { path: i < 0 ? h : h.slice(0, i), search: i < 0 ? "" : h.slice(i + 1) };
};

class Router {
  path = $state("/");
  search = $state("");
  query = $derived(new URLSearchParams(this.search));

  constructor() {
    const sync = () => {
      const r = read();
      this.path = r.path;
      this.search = r.search;
    };
    sync();
    window.addEventListener("hashchange", sync);
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

/** Opens a UI path. replace: true does not add a history entry. */
export function navigate(path: string, opts: { query?: Record<string, string | undefined>; replace?: boolean } = {}) {
  const target = href(path, opts.query);
  if (opts.replace) {
    history.replaceState(history.state, "", target);
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  } else {
    window.location.hash = target;
  }
}

/** Encodes each segment of a repo path for use in a UI path. */
export const encodePath = (p: string) => p.split("/").map(encodeURIComponent).join("/");

/** True when `path` is `prefix` or below it; for "active" states in navigation. */
export const isActive = (prefix: string, path = router.path) =>
  prefix === "/" ? path === "/" : path === prefix || path.startsWith(prefix + "/");

function matchOne(pattern: string, path: string): Params | null {
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
    const params = matchOne(route.pattern, path);
    if (params) return { route, params };
  }
  return null;
}
