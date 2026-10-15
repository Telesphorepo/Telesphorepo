// The signed-in user. App.svelte calls auth.load() once; pages read auth.user, auth.setupOnly and
// the permission checks: auth.can(perm) for a server permission, auth.canRepo(repo, perm) for a
// repository permission (All repositories holds every one), auth.allows(access) for a route.
// Sign-in has two steps for an account with a second factor: signIn() answers {step: "second",
// methods}, then finish with signInSecond().

import {
  api,
  onSecondFactorRequired,
  onSessionLost,
  type Me,
  type Proof,
  type RepoPermission,
  type SecondFactorMethod,
  type ServerPermission,
} from "$lib/api";
import { navigate, type Access, type Params } from "$lib/router.svelte";

export type SignInResult = { step: "done"; user: Me } | { step: "second"; methods: SecondFactorMethod[] };

class Auth {
  /** null when signed out (anonymous visitors still browse anonymous-read repos). */
  user = $state<Me | null>(null);
  /** False until the first /api/me answers. */
  ready = $state(false);

  /** A local account without a second factor: its session reaches only the setup routes. */
  setupOnly = $derived(!!this.user?.second_factor_required);

  /** The repositories where the user holds more than download, by name, with the whole set of each. */
  repoPermissions = $derived<Record<string, RepoPermission[]>>(this.user?.repo_permissions ?? {});

  /** True when the user holds the server permission. */
  can(perm: ServerPermission): boolean {
    return !!this.user?.permissions?.includes(perm);
  }

  /**
   * True when the user holds the repository permission on repo. It knows only the repositories of
   * repo_permissions (more than download): for download and publish read `repo.permissions`.
   */
  canRepo(repo: string, perm: RepoPermission): boolean {
    return this.can("repos") || !!this.repoPermissions[repo]?.includes(perm);
  }

  /** True when the user can open a settings tab of repo (Edit settings, Manage access or CI pipelines). */
  managesRepo(repo: string): boolean {
    return this.canRepo(repo, "settings") || this.canRepo(repo, "access") || this.canRepo(repo, "pipelines");
  }

  /**
   * The repository permissions the user holds on every one of repos: what they can give and
   * remove there. undefined for no repos. Like canRepo it knows only the repo_permissions.
   */
  heldOnAll(repos: string[]): RepoPermission[] | undefined {
    if (!repos.length) return undefined;
    const all: RepoPermission[] = ["download", "publish", "delete", "settings", "access", "pipelines"];
    return all.filter((p) => repos.every((r) => this.canRepo(r, p)));
  }

  /** True when the user holds the repository permission on at least one repository. */
  canSomeRepo(perm: RepoPermission): boolean {
    return this.can("repos") || Object.values(this.repoPermissions).some((s) => s.includes(perm));
  }

  /** True when the user may open a page with this access (see Access in router.svelte.ts). */
  allows(access: Access, params: Params = {}): boolean {
    if (access === "any") return true;
    if (!this.user) return false;
    switch (access) {
      case "user":
        return true;
      case "repositories":
        return this.can("repos") || Object.keys(this.repoPermissions).length > 0;
      case "repo":
        return this.managesRepo(params.name ?? "");
      case "pipelines":
        return this.canSomeRepo("pipelines");
      default:
        return this.can(access);
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

  /** Reads the current user; signed out on any error. */
  async load() {
    try {
      this.user = await api.me();
    } catch {
      this.user = null;
    }
    this.ready = true;
  }

  async signIn(username: string, password: string): Promise<SignInResult> {
    const a = await api.auth.login(username, password);
    if ("second_factor" in a) return { step: "second", methods: a.methods };
    const u = await this.withRepoPermissions(a);
    return { step: "done", user: u };
  }

  async signInSecond(proof: Proof): Promise<Me> {
    return this.withRepoPermissions(await api.auth.loginSecond(proof));
  }

  /** The sign-in answer leaves out repo_permissions; /api/me holds them. */
  private async withRepoPermissions(u: Me): Promise<Me> {
    const me = await api.me().catch(() => u);
    this.user = me;
    return me;
  }

  /** Signs out and returns to Browse. */
  async signOut() {
    try {
      await api.auth.logout();
    } catch {
      /* the session is gone either way */
    }
    this.user = null;
    navigate("/");
  }
}

export const auth = new Auth();
