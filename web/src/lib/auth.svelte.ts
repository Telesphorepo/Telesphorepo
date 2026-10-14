// The signed-in user. App.svelte calls auth.load() once; pages read auth.user, auth.isAdmin and
// auth.setupOnly. Sign-in has two steps for an account with a second factor: signIn() answers
// {step: "second", methods}, then finish with signInSecond().

import { api, onSecondFactorRequired, onSessionLost, type Me, type Proof, type SecondFactorMethod } from "$lib/api";
import { navigate } from "$lib/router.svelte";

export type SignInResult = { step: "done"; user: Me } | { step: "second"; methods: SecondFactorMethod[] };

class Auth {
  /** null when signed out (anonymous visitors still browse anonymous-read repos). */
  user = $state<Me | null>(null);
  /** False until the first /api/me answers. */
  ready = $state(false);

  isAdmin = $derived(!!this.user?.is_admin);
  /** A local account without a second factor: its session reaches only the setup routes. */
  setupOnly = $derived(!!this.user?.second_factor_required);

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
    this.user = a;
    return { step: "done", user: a };
  }

  async signInSecond(proof: Proof): Promise<Me> {
    const u = await api.auth.loginSecond(proof);
    this.user = u;
    return u;
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
