<!--
  Device approval. The login command of the Gradle plugin or the Maven extension prints a code and
  this address (/#/device?code=ABCD-EFGH). Two steps: enter the code, then check the device (a felt
  ticket with the code on its stub) and confirm it is you. A local account confirms with a fresh
  second factor. A single sign-on account signs in again at its identity provider; the server then
  returns here with a single-use grant (&grant=) and the page approves at once, or with an error
  code (&error=). Approving tears the stub.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, ApiError, errorMessage, type PendingEnrollment, type Proof } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import Step from "$lib/components/Step.svelte";
  import FeltTicket from "$lib/components/FeltTicket.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FreshFactor from "$lib/components/FreshFactor.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
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

  // Single sign-on: the server comes back here with a grant or an error code.
  const sso = $derived(auth.user?.kind === "oidc");
  const ssoErrors: Record<string, string> = {
    stale_sign_in:
      "Your identity provider did not ask you to sign in again: the sign-in it sent is more than 5 minutes old. The provider ignored the request for a new sign-in (prompt=login, max_age=0). Ask an administrator to let the provider force a new sign-in for this app.",
    no_auth_time:
      "Your identity provider did not say when you signed in (the ID token has no auth_time), so the server cannot check that the sign-in is new. Ask an administrator to add auth_time to the ID token.",
    wrong_account: "You signed in to your identity provider with another account. Sign in again with this account.",
    not_authorized: "Your single sign-on account is not allowed to sign in here.",
    account_disabled: "This account is disabled.",
    not_sso: "You sign in with a password. Confirm with two-step verification.",
    sso_failed: "Single sign-on failed. Try again.",
  };
  let ssoError = $state(
    untrack(() => {
      const e = query.get("error");
      return e ? (ssoErrors[e] ?? ssoErrors.sso_failed) : "";
    }),
  );
  let grant = untrack(() => query.get("grant") ?? "");
  let ssoBusy = $state(false);
  // Keep the grant and the error out of the address: the grant works once, and a reload must not show the error again.
  if (untrack(() => query.has("grant") || query.has("error"))) navigate("/device", { query: { code: untrack(() => code) }, replace: true });

  async function lookup() {
    looking = true;
    lookupError = "";
    pending = null;
    try {
      pending = await api.devices.pending(code);
      if (grant) {
        const g = grant;
        grant = "";
        approveSSO(g);
      }
    } catch (e) {
      lookupError =
        e instanceof ApiError && e.notFound
          ? "This code is unknown or has expired. Run the login command again to get a new code."
          : errorMessage(e);
    } finally {
      looking = false;
    }
  }

  // A code in the address is looked up at once.
  if (untrack(() => complete)) lookup();

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (complete) lookup();
  }

  async function approve(proof: Proof | { grant: string }) {
    if (!pending) return;
    await api.devices.approve(pending.user_code, proof);
    approved = pending;
    pending = null;
  }

  async function approveSSO(g: string) {
    ssoBusy = true;
    ssoError = "";
    try {
      await approve({ grant: g });
    } catch (e) {
      ssoError = errorMessage(e);
    } finally {
      ssoBusy = false;
    }
  }

  const clientName = (k: PendingEnrollment["kind"]) => (k === "gradle" ? "Gradle plugin" : "Maven extension");
</script>

<PageHeader title="Approve a device" description="Approve the device only if the code matches the one in your terminal." />

<FormPage>
  {#if approved}
    <div class="flex flex-col gap-x-14 gap-y-8 sm:flex-row sm:items-center">
      <!-- 20rem on a phone leaves room for the torn stub to drop to the right. -->
      <div class="w-full max-w-[20rem] shrink-0 sm:max-w-[24rem]">
        <FeltTicket
          top={approved.name}
          bottom={clientName(approved.kind)}
          serial={approved.user_code}
          moment="tear"
          label="The ticket of {approved.name}, its stub torn off"
        />
      </div>
      <div class="flex min-w-0 flex-col gap-3" aria-live="polite">
        <p class="flex items-center gap-2 text-section text-success"><CircleCheckIcon class="size-5" />Device approved</p>
        <p class="text-body text-muted-foreground">
          {approved.name} can now publish as {auth.user?.username}. Go back to your terminal: the login command finishes by itself.
        </p>
        <div><Button variant="outline" href={href("/settings/devices")}>Open your devices</Button></div>
      </div>
    </div>
  {:else}
    <Step n={1} title="Enter the code from your terminal" done={!!pending} last={!pending}>
      <form onsubmit={submit}>
        <FormGrid>
          <FormRow label="Code" for="device-code" help="The code has eight letters and digits." error={lookupError}>
            <div class="flex gap-2">
              <Input
                id="device-code"
                class="min-w-0 flex-1 tracking-widest tabular-nums"
                placeholder="ABCD-EFGH"
                autocomplete="off"
                spellcheck={false}
                value={code}
                oninput={(e) => {
                  code = normalize(e.currentTarget.value);
                  e.currentTarget.value = code;
                }}
                disabled={looking}
                aria-describedby="device-code-help"
                aria-invalid={lookupError ? true : undefined}
              />
              <Button type="submit" variant={pending ? "outline" : "default"} disabled={looking || !complete}>
                {#if looking}<Spinner />{/if}
                Look up
              </Button>
            </div>
          </FormRow>
        </FormGrid>
      </form>
    </Step>

    {#if pending}
      <Step n={2} title="Check the device, then confirm it is you" description="The device can publish to the repositories that you can publish to, until you revoke it." last>
        <div class="flex flex-col gap-x-8 gap-y-6 sm:flex-row sm:items-center">
          <div class="w-full max-w-[20rem] shrink-0">
            <FeltTicket
              top={pending.name}
              bottom={clientName(pending.kind)}
              serial={pending.user_code}
              label="The ticket of {pending.name}, {clientName(pending.kind)}"
            />
          </div>
          <!-- The ticket shows the name and the client. The code on its stub reads sideways, so the
               code to compare with the terminal shows here once more, level. -->
          <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-body">
            <dt class="text-muted-foreground">Code</dt>
            <dd class="tracking-widest tabular-nums">{pending.user_code}</dd>
            <dt class="text-muted-foreground">Code expires</dt>
            <dd><RelativeTime at={pending.expires_at} /></dd>
          </dl>
        </div>
        <div class="mt-8">
          {#if sso}
            <div class="flex flex-col items-start gap-3 text-sm">
              <p>Your identity provider asks you to sign in again. Then this page approves the device.</p>
              <Button href={api.devices.ssoApproveURL(pending.user_code)} disabled={ssoBusy}>
                {#if ssoBusy}<Spinner />{/if}
                Sign in again with single sign-on to approve
              </Button>
              {#if ssoError}<ErrorState inline error={ssoError} />{/if}
            </div>
          {:else}
            <FreshFactor actionLabel="Approve device" onProof={approve} back="/device?code={pending.user_code}" />
          {/if}
        </div>
      </Step>
    {/if}
  {/if}
</FormPage>
