// The signed-in user. App.svelte calls auth.load() once; pages read auth.user, auth.setupOnly and
// the permission checks with permission IDs (GET /api/permissions): auth.can(id) for a server
// permission, auth.canRepo(repo, id) for a repository permission (repositories.all holds every
// one), auth.allows(access) for a route.
// Sign-in has two steps for an account with a second factor: signIn() answers {step: "second",
// methods}, then finish with signInSecond().

import {
  api,
  onSecondFactorRequired,
  onSessionLost,
  type Me,
  type Proof,
  type PermId,
  type SecondFactorMethod,
} from "$lib/api";
import { navigate, type Params } from "$lib/router.svelte";
import type { Access } from "../modules/types";
import { allReposPermissions, loadPermissions, managingRepoPermissions, repoPermissions } from "$lib/permissions";

export type SignInResult = { step: "done"; user: Me } | { step: "second"; methods: SecondFactorMethod[] };

class Auth {
  /** null when signed out (anonymous visitors still browse anonymous-read repos). */
  user = $state<Me | null>(null);
  /** False until the first /api/me answers. */
  ready = $state(false);

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
    onSessionLost(() => {
      this.user = null;
    });
    onSecondFactorRequired(() => {
      if (this.user) this.user = { ...this.user, second_factor_required: true };
    });
  }

  /** Reads the permission registry and the current user; signed out on any error of /api/me. */
  async load() {
    const registry = loadPermissions();
    try {
      this.user = await api.me();
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
    return { step: "done", user: a.session };
  }

  async signInSecond(proof: Proof): Promise<Me> {
    const { session } = await api.auth.loginSecond(proof);
    this.user = session;
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
