<!--
  Device approval. The Gradle plugin or the Maven extension prints a code and this address
  (/#/device?code=ABCD-EFGH). The signed-in user looks up the code, checks the device, and
  approves it with a fresh second factor.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, ApiError, errorMessage, type PendingEnrollment, type Proof, type SecondFactorMethod } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import SecondFactorForm from "$lib/components/SecondFactorForm.svelte";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";

  let { query }: PageProps = $props();

  // "abcd efgh" → "ABCD-EFGH".
  const normalize = (s: string) =>
    s
      .toUpperCase()
      .replace(/[^A-Z0-9]/g, "")
      .slice(0, 8)
      .replace(/^(.{4})(.+)$/, "$1-$2");

  let code = $state(untrack(() => normalize(query.get("code") ?? "")));
  const complete = $derived(code.length === 9);

  let looking = $state(false);
  let lookupError = $state("");
  let pending = $state<PendingEnrollment | null>(null);
  let approved = $state<PendingEnrollment | null>(null);

  // The caller's factors, for the approval form.
  let methods = $state<SecondFactorMethod[] | null>(null);
  let optional = $state(false);
  let factorError = $state("");
  let devBusy = $state(false);
  let devError = $state("");

  async function lookup() {
    looking = true;
    lookupError = "";
    pending = null;
    try {
      pending = await api.devices.pending(code);
      if (methods === null) loadFactors();
    } catch (e) {
      lookupError =
        e instanceof ApiError && e.notFound
          ? "This code is unknown or has expired. Run the login command again to get a new code."
          : errorMessage(e);
    } finally {
      looking = false;
    }
  }

  async function loadFactors() {
    factorError = "";
    try {
      const [f, cfg] = await Promise.all([api.security.get(), api.auth.config()]);
      const m: SecondFactorMethod[] = [];
      if (f.totp.length) m.push("totp");
      if (f.passkeys.length) m.push("passkey");
      methods = m;
      optional = cfg.second_factor_optional;
    } catch (e) {
      factorError = errorMessage(e);
    }
  }

  // A code in the address is looked up once the user is known.
  let autoLooked = false;
  $effect(() => {
    if (auth.user && complete && !autoLooked) {
      autoLooked = true;
      untrack(lookup);
    }
  });

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (complete) lookup();
  }

  async function approve(proof: Proof) {
    if (!pending) return;
    await api.devices.approve(pending.user_code, proof);
    approved = pending;
    pending = null;
  }

  async function approveWithoutFactor() {
    devBusy = true;
    devError = "";
    try {
      await approve({});
    } catch (e) {
      devError = errorMessage(e);
    } finally {
      devBusy = false;
    }
  }

  const clientName = (k: PendingEnrollment["kind"]) => (k === "gradle" ? "Gradle plugin" : "Maven extension");
  const next = $derived("/device" + (code ? "?code=" + code : ""));
</script>

<div class="flex flex-col gap-6">
  {#if approved}
    <EmptyState
      icon={CircleCheckIcon}
      title="Device approved"
      description="{approved.name} can now publish with your account. Return to your terminal: the login command finishes by itself."
    >
      <Button variant="outline" href={href("/settings/devices")}>Open your devices</Button>
    </EmptyState>
  {:else}
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Approve a device</h1>
      <p class="mt-1.5 text-sm text-muted-foreground">
        The login command of the Gradle plugin or the Maven extension prints a code. Approve the device only if the
        code matches the one in your terminal.
      </p>
    </div>

    {#if !auth.user}
      <div class="flex flex-col gap-3">
        {#if complete}
          <p class="text-sm">Code <span class="font-mono font-medium tracking-wider">{code}</span></p>
        {/if}
        <Button class="self-start" href={href("/login", { next })}>Sign in to approve</Button>
      </div>
    {:else}
      <form class="flex flex-col gap-2" onsubmit={submit}>
        <Label for="device-code">Code from the terminal</Label>
        <div class="flex flex-wrap gap-2">
          <Input
            id="device-code"
            class="w-44 font-mono tracking-wider uppercase"
            placeholder="ABCD-EFGH"
            autocomplete="off"
            spellcheck={false}
            value={code}
            oninput={(e) => {
              code = normalize(e.currentTarget.value);
              e.currentTarget.value = code;
            }}
            disabled={looking}
          />
          <Button type="submit" variant={pending ? "outline" : "default"} disabled={looking || !complete}>
            {#if looking}<Spinner />{/if}
            Look up
          </Button>
        </div>
        {#if lookupError}<ErrorState inline error={lookupError} />{/if}
      </form>

      {#if pending}
        <section class="flex flex-col gap-5 rounded-2xl border p-5" aria-label="Device to approve">
          <dl class="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-sm">
            <dt class="text-muted-foreground">Device</dt>
            <dd class="font-medium break-words">{pending.name}</dd>
            <dt class="text-muted-foreground">Client</dt>
            <dd>{clientName(pending.kind)}</dd>
            <dt class="text-muted-foreground">Code</dt>
            <dd class="font-mono tracking-wider">{pending.user_code}</dd>
            <dt class="text-muted-foreground">Code expires</dt>
            <dd><RelativeTime at={pending.expires_at} /></dd>
          </dl>
          <p class="text-sm text-muted-foreground">
            The device can publish to the repositories you can write to, as {auth.user?.username}, until you revoke it.
          </p>

          {#if factorError}
            <ErrorState inline error={factorError} />
          {:else if methods === null}
            <LoadingRows rows={2} />
          {:else if methods.length === 0}
            <div class="flex flex-col gap-3 text-sm">
              <p>Approving a device needs a second factor, and your account has none yet.</p>
              <Button class="self-start" href={href("/settings/security")}>Set up a second factor</Button>
              {#if optional}
                <p class="text-muted-foreground">Dev mode makes the second factor optional.</p>
                <Button variant="outline" class="self-start" disabled={devBusy} onclick={approveWithoutFactor}>
                  {#if devBusy}<Spinner />{/if}
                  Approve without a second factor
                </Button>
                {#if devError}<ErrorState inline error={devError} />{/if}
              {/if}
            </div>
          {:else}
            <SecondFactorForm
              {methods}
              actionLabel="Approve device"
              passkeyOptions={api.security.assertPasskeyOptions}
              onProof={approve}
            />
          {/if}
        </section>
      {/if}
    {/if}
  {/if}
</div>
