<!--
  The one place to sign in: single sign-on (when configured), or username and password, then the
  second step when the account has a second factor. query: next (a UI path to open after),
  error (a single sign-on error code).
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { navigate } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, errorMessage, type AuthConfig, type Proof, type SecondFactorMethod } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import { Separator } from "$lib/components/ui/separator";
  import FormField from "$lib/components/FormField.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import SecondFactorForm from "$lib/components/SecondFactorForm.svelte";
  import Logo from "$lib/components/Logo.svelte";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";

  let { query }: PageProps = $props();

  const ssoErrors: Record<string, string> = {
    username_conflict: "A local account has the same username, so this single sign-on account cannot sign in.",
    invalid_username: "Your single sign-on account has a username that is not valid here.",
    account_disabled: "This account is disabled.",
    sso_failed: "Single sign-on failed. Try again.",
  };

  const next = $derived.by(() => {
    const n = query.get("next") ?? "";
    // Only UI paths: never an absolute URL.
    return n.startsWith("/") && !n.startsWith("//") ? n : "/";
  });
  const why = $derived(
    next.startsWith("/device")
      ? "Sign in to approve the device."
      : next.startsWith("/settings")
        ? "Sign in to open your settings."
        : undefined,
  );

  let config = $state<AuthConfig | null>(null);
  let username = $state("");
  let password = $state("");
  let busy = $state(false);
  let error = $state("");
  let methods = $state<SecondFactorMethod[] | null>(null);

  $effect(() => {
    const code = query.get("error");
    if (code) error = ssoErrors[code] ?? "Sign-in failed. Try again.";
  });

  api.auth.config().then(
    (c) => (config = c),
    () => {},
  );

  function done() {
    navigate(next, { replace: true });
  }

  // A visitor who is already signed in goes on.
  $effect(() => {
    if (auth.user && methods === null && !busy) done();
  });

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = "";
    try {
      const r = await auth.signIn(username.trim(), password);
      password = "";
      if (r.step === "second") methods = r.methods;
      else done();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  async function second(proof: Proof) {
    await auth.signInSecond(proof);
    done();
  }
</script>

<div class="mx-auto flex w-full max-w-sm flex-col gap-6 pt-8 sm:pt-16">
  <Logo size={40} />
  {#if methods}
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Second step</h1>
      <p class="mt-2 text-sm text-muted-foreground">
        Signing in as <span class="font-medium text-foreground">{username}</span>. Enter a code from your authenticator
        app, or use a passkey.
      </p>
    </div>
    <SecondFactorForm {methods} actionLabel="Sign in" passkeyOptions={api.auth.loginPasskeyOptions} onProof={second} />
    <Button
      variant="ghost"
      size="sm"
      class="-ml-3 self-start"
      onclick={() => {
        methods = null;
        error = "";
      }}
    >
      <ArrowLeftIcon />
      Use another account
    </Button>
  {:else}
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Sign in to Telesphorepo</h1>
      {#if why}<p class="mt-2 text-sm text-muted-foreground">{why}</p>{/if}
    </div>
    {#if config?.oidc}
      <Button size="lg" variant="outline" href={api.auth.oidcLoginURL}>Continue with single sign-on</Button>
      <div class="flex items-center gap-3 text-xs text-muted-foreground">
        <Separator class="flex-1" />
        or
        <Separator class="flex-1" />
      </div>
    {/if}
    <form class="flex flex-col gap-4" onsubmit={submit}>
      <FormField label="Username" for="username">
        <!-- svelte-ignore a11y_autofocus -->
        <Input id="username" bind:value={username} autocomplete="username" autofocus={!config?.oidc} required />
      </FormField>
      <FormField label="Password" for="password">
        <Input id="password" type="password" bind:value={password} autocomplete="current-password" required />
      </FormField>
      {#if error}<ErrorState inline {error} />{/if}
      <Button type="submit" size="lg" class="mt-2" disabled={busy || !username.trim() || !password}>
        {#if busy}<Spinner />{/if}
        Sign in
      </Button>
    </form>
  {/if}
</div>
