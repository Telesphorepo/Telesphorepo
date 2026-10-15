<!--
  A fresh second-factor check before a sensitive action (approve a device, create a publish token,
  upload, delete), inline in the page. It loads the caller's methods and shows the code field or
  the passkey button. A user without a second factor gets a link to set one up; under
  `telesphorepo dev` (second_factor_optional) also a way to go on without one. onProof does the
  action; when it throws, the form shows the error.

    <FreshFactor actionLabel="Approve device" onProof={approve} />
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import SecondFactorForm from "./SecondFactorForm.svelte";
  import Loading from "./Loading.svelte";
  import ErrorState from "./ErrorState.svelte";
  import { api, errorMessage, type Proof, type SecondFactorMethod } from "$lib/api";
  import { href } from "$lib/router.svelte";

  let {
    actionLabel,
    onProof,
    onSetup,
  }: {
    /** Names the action: "Approve device", "Create token", "Continue". */
    actionLabel: string;
    onProof: (p: Proof) => Promise<unknown>;
    /** Runs when the user follows the link to set up a second factor (a dialog closes itself). */
    onSetup?: () => void;
  } = $props();

  let methods = $state<SecondFactorMethod[] | null>(null);
  let dev = $state(false);
  let loadError = $state("");
  let devBusy = $state(false);
  let devError = $state("");

  async function load() {
    methods = null;
    loadError = "";
    try {
      const [f, cfg] = await Promise.all([api.security.get(), api.auth.config()]);
      const m: SecondFactorMethod[] = [];
      if (f.totp.length) m.push("totp");
      if (f.passkeys.length) m.push("passkey");
      methods = m;
      dev = cfg.second_factor_optional;
    } catch (e) {
      loadError = errorMessage(e);
    }
  }
  load();

  async function withoutFactor() {
    devBusy = true;
    devError = "";
    try {
      await onProof({});
    } catch (e) {
      devError = errorMessage(e);
    } finally {
      devBusy = false;
    }
  }
</script>

{#if loadError}
  <div class="flex flex-wrap items-center gap-3">
    <ErrorState inline error={loadError} />
    <Button variant="outline" size="sm" onclick={load}>Try again</Button>
  </div>
{:else if methods === null}
  <Loading inline />
{:else if methods.length === 0}
  <div class="flex flex-col items-start gap-3 text-sm">
    <p>This action needs two-step verification, and you have not set it up yet.</p>
    <Button href={href("/settings/security")} onclick={onSetup}>Set up two-step verification</Button>
    {#if dev}
      <p class="text-muted-foreground">Dev mode makes two-step verification optional.</p>
      <Button variant="outline" disabled={devBusy} onclick={withoutFactor}>
        {#if devBusy}<Spinner />{/if}
        {actionLabel} without two-step verification
      </Button>
      {#if devError}<ErrorState inline error={devError} />{/if}
    {/if}
  </div>
{:else}
  <SecondFactorForm {methods} {actionLabel} passkeyOptions={api.security.assertPasskeyOptions} {onProof} />
{/if}
