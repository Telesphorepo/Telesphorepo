// The signed-in user. App.svelte calls auth.load() once; pages read auth.user, auth.setupOnly and
// the permission checks with permission IDs (GET /api/permissions): auth.can(id) for a server
// permission, auth.canRepo(repo, id) for a repository permission (repositories.all holds every
// one), auth.allows(access) for a route.
// Sign-in has two steps for an account with a second factor: signIn() answers {step: "second",
// methods}, then finish with signInSecond(). When a request finds the session ended, the person
// goes to the sign-in page, which opens the page they were on after the sign-in.

import {
  api,
  ApiError,
  onSecondFactorRequired,
  onSessionLost,
  type Me,
  type Proof,
  type PermId,
  type SecondFactorMethod,
} from "$lib/api";
import { navigate, router, type Params } from "$lib/router.svelte";
import type { Access } from "../modules/types";
import { allReposPermissions, loadPermissions, managingRepoPermissions, repoPermissions } from "$lib/permissions";

export type SignInResult = { step: "done"; user: Me } | { step: "second"; methods: SecondFactorMethod[] };

/**
 * Runs fn, and again after a growing wait (for about 15 seconds in all) while the server cannot be
 * reached or answers 5xx: a restart (a deploy, the dev server rebuilding) is not a sign-out.
 */
async function untilReached<T>(fn: () => Promise<T>): Promise<T> {
  for (let wait = 500; ; wait *= 2) {
    try {
      return await fn();
    } catch (e) {
      const transient = e instanceof ApiError && (e.status === 0 || e.status >= 500);
      if (!transient || wait > 8000) throw e;
      await new Promise((r) => setTimeout(r, wait));
    }
  }
}

class Auth {
  /** null when signed out (anonymous visitors still browse anonymous-read repos). */
  user = $state<Me | null>(null);
  /** False until the first /api/me answers. */
  ready = $state(false);
  /** True when a request found the session ended, until the next sign-in; the sign-in page says so. */
  ended = $state(false);

  /** A local account without a second factor: its session reaches only the setup routes. */
  setupOnly = $derived(!!this.user?.second_factor_required);

  /** Every repository where the user has a grant, by name, with the whole set of each ({} with All repositories). */
  repoPermissions = $derived<Record<string, PermId[]>>(this.user?.repo_permissions ?? {});

  /** True when the user holds the server permission. */
  can(perm: PermId): boolean {
    return !!this.user?.permissions?.includes(perm);
  }

  /** True when the user holds a server permission that gives every repository (repositories.all). */
  allRepos(): boolean {
    return allReposPermissions().some((p) => this.can(p));
  }

  /** True when the user holds the repository permission on repo through a grant or repositories.all. */
  canRepo(repo: string, perm: PermId): boolean {
    return this.allRepos() || !!this.repoPermissions[repo]?.includes(perm);
  }

  /** True when the user can open a settings tab of repo: a permission that manages it (repo.settings, repo.access, ci.pipelines, ...). */
  managesRepo(repo: string): boolean {
    return managingRepoPermissions().some((p) => this.canRepo(repo, p));
  }

  /**
   * The repository permissions the user holds on every one of repos: what they can give and
   * remove there. undefined for no repos. Like canRepo it reads the grants of repo_permissions.
   */
  heldOnAll(repos: string[]): PermId[] | undefined {
    if (!repos.length) return undefined;
    return repoPermissions()
      .map((p) => p.name)
      .filter((p) => repos.every((r) => this.canRepo(r, p)));
  }

  /** True when the Repositories page lists repo: repositories.all, or a grant there that manages it. */
  listsRepo(repo: string): boolean {
    return this.allRepos() || (this.repoPermissions[repo] ?? []).some((p) => managingRepoPermissions().includes(p));
  }

  /** True when the user holds the repository permission on at least one repository. */
  canSomeRepo(perm: PermId): boolean {
    return this.allRepos() || Object.values(this.repoPermissions).some((s) => s.includes(perm));
  }

  /** True when the user may open a page with this access (see Access in modules/types.ts). */
  allows(access: Access, params: Params = {}): boolean {
    if (access === "any") return true;
    if (!this.user) return false;
    if (Array.isArray(access)) return access.every((p) => this.can(p));
    switch (access) {
      case "user":
        return true;
      case "repositories":
        return this.allRepos() || Object.keys(this.repoPermissions).some((r) => this.listsRepo(r));
      case "repo":
        return this.managesRepo(params.name ?? "");
      default:
        return this.can(access) || this.canSomeRepo(access);
    }
  }

  constructor() {
    // A visitor who never signed in gets 401 too: only a person who was signed in goes to sign in.
    onSessionLost(() => {
      if (!this.user) return;
      this.user = null;
      this.ended = true;
      if (router.path !== "/login") {
        navigate("/login", { query: { next: router.path + (router.search ? "?" + router.search : "") }, replace: true });
      }
    });
    onSecondFactorRequired(() => {
      if (this.user) this.user = { ...this.user, second_factor_required: true };
    });
  }

  /**
   * Reads the permission registry and the current user; signed out on an error of /api/me (a 401
   * without a session), once the server can be reached (untilReached).
   */
  async load() {
    const registry = untilReached(loadPermissions);
    try {
      this.user = await untilReached(api.me);
    } catch {
      this.user = null;
    }
    await registry;
    this.ready = true;
  }

  async signIn(username: string, password: string): Promise<SignInResult> {
    const a = await api.auth.login(username, password);
    if ("second_factor" in a) return { step: "second", methods: a.methods };
    this.user = a.session;
    this.ended = false;
    return { step: "done", user: a.session };
  }

  async signInSecond(proof: Proof): Promise<Me> {
    const { session } = await api.auth.loginSecond(proof);
    this.user = session;
    this.ended = false;
    return session;
  }

  /** Signs out and returns to Home, or with `next` (a UI path) to the sign-in page, which opens `next` after. */
  async signOut(next?: string) {
    try {
      await api.auth.logout();
    } catch {
      /* the session is gone either way */
    }
    this.user = null;
    if (next) navigate("/login", { query: { next }, replace: true });
    else navigate("/");
  }
}

export const auth = new Auth();
