<!-- Account: authenticator apps, passkeys and recovery codes. A setup-only session sees only this page. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, errorMessage, type Passkey, type TOTPFactor, type TOTPSetup } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { createPasskey, passkeysSupported } from "$lib/webauthn";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CopyField from "$lib/components/CopyField.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import QR from "$lib/components/QR.svelte";
  import * as Table from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Spinner } from "$lib/components/ui/spinner";
  import SmartphoneIcon from "@lucide/svelte/icons/smartphone";
  import KeyRoundIcon from "@lucide/svelte/icons/key-round";

  let { params, query }: PageProps = $props();

  const factors = new Resource(() => api.security.get());

  /** Reloads the factors and the user, which lifts the setup-only limit after the first factor. */
  async function changed() {
    await Promise.all([factors.reload(), auth.load()]);
  }

  const count = $derived((factors.data?.totp.length ?? 0) + (factors.data?.passkeys.length ?? 0));

  // --- authenticator apps ---
  let totpName = $state("");
  let setup = $state<TOTPSetup | null>(null);
  let totpCode = $state("");
  let totpBusy = $state(false);
  let totpError = $state("");

  async function beginTOTP(e: SubmitEvent) {
    e.preventDefault();
    totpBusy = true;
    totpError = "";
    try {
      setup = await api.security.beginTOTP(totpName.trim());
    } catch (err) {
      totpError = errorMessage(err);
    } finally {
      totpBusy = false;
    }
  }

  async function confirmTOTP(e: SubmitEvent) {
    e.preventDefault();
    if (!setup) return;
    totpBusy = true;
    totpError = "";
    try {
      await api.security.confirmTOTP(setup.setup_id, totpCode.replace(/\s/g, ""));
      toast.success(`Added the authenticator app ${totpName.trim()}`);
      cancelTOTP();
      await changed();
    } catch (err) {
      totpError = errorMessage(err);
    } finally {
      totpBusy = false;
    }
  }

  function cancelTOTP() {
    setup = null;
    totpName = "";
    totpCode = "";
    totpError = "";
  }

  // --- passkeys ---
  let passkeyName = $state("");
  let passkeyBusy = $state(false);
  let passkeyError = $state("");

  async function addPasskey(e: SubmitEvent) {
    e.preventDefault();
    passkeyBusy = true;
    passkeyError = "";
    try {
      const name = passkeyName.trim();
      const options = await api.security.beginPasskey();
      const credential = await createPasskey(options);
      await api.security.finishPasskey(name, credential);
      toast.success(`Added the passkey ${name}`);
      passkeyName = "";
      await changed();
    } catch (err) {
      passkeyError = errorMessage(err);
    } finally {
      passkeyBusy = false;
    }
  }

  // --- removing a factor ---
  type Removal = { kind: "totp"; item: TOTPFactor } | { kind: "passkey"; item: Passkey };
  let removal = $state<Removal | null>(null);
  let removeOpen = $state(false);

  function askRemove(r: Removal) {
    removal = r;
    removeOpen = true;
  }

  const removeChanges = $derived.by(() => {
    if (!removal) return [];
    const what = removal.kind === "totp" ? "authenticator app" : "passkey";
    const out = [`Removes the ${what} ${removal.item.name}`, `You can no longer sign in with this ${what}`];
    if (count <= 1) out.push("Your account has no second factor left, so you must set up a new one");
    return out;
  });

  async function remove() {
    if (!removal) return;
    const { kind, item } = removal;
    if (kind === "totp") await api.security.deleteTOTP(item.id);
    else await api.security.deletePasskey(item.id);
    toast.success(`Removed the ${kind === "totp" ? "authenticator app" : "passkey"} ${item.name}`);
    await changed();
  }

  // --- recovery codes ---
  let codes = $state<string[] | null>(null);
  let recoveryOpen = $state(false);

  async function generate() {
    const r = await api.security.recoveryCodes();
    codes = r.codes;
    toast.success("Generated new recovery codes");
    await factors.reload();
  }
</script>

<PageHeader
  title="Second factor"
  description="Sign-in and sensitive actions ask for a code from an authenticator app or for a passkey."
/>

{#if factors.data}
  {@const data = factors.data}

  <Section title="Authenticator apps" description="An app such as Aegis, 1Password or Google Authenticator shows a 6-digit code.">
    {#if data.totp.length === 0}
      <EmptyState icon={SmartphoneIcon} title="No authenticator app yet" description="Add one below." />
    {:else}
      <div class="w-full overflow-x-auto rounded-2xl border">
        <Table.Root>
          <Table.Caption class="sr-only">Authenticator apps</Table.Caption>
          <Table.Header>
            <Table.Row class="hover:bg-transparent">
              <Table.Head>Name</Table.Head>
              <Table.Head>Added</Table.Head>
              <Table.Head class="w-0"><span class="sr-only">Actions</span></Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each data.totp as t (t.id)}
              <Table.Row>
                <Table.Cell class="font-medium">{t.name}</Table.Cell>
                <Table.Cell><RelativeTime at={t.created_at} /></Table.Cell>
                <Table.Cell class="text-right">
                  <Button variant="destructive" size="sm" onclick={() => askRemove({ kind: "totp", item: t })}>Remove</Button>
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      </div>
    {/if}

    <div class="mt-4 max-w-form">
      {#if !setup}
        <form class="flex flex-col gap-4" onsubmit={beginTOTP}>
          <div class="flex flex-col gap-2">
            <Label for="totp-name">Name of the new app</Label>
            <Input id="totp-name" class="max-w-sm" placeholder="Phone" required bind:value={totpName} disabled={totpBusy} />
          </div>
          {#if totpError}<ErrorState inline error={totpError} />{/if}
          <div class="flex gap-2">
            <Button type="submit" disabled={totpBusy || !totpName.trim()}>
              {#if totpBusy}<Spinner />{/if}
              Add authenticator app
            </Button>
          </div>
        </form>
      {:else}
        <form class="flex flex-col gap-4 rounded-2xl border p-5" onsubmit={confirmTOTP}>
          <p class="text-sm">Scan the code with your authenticator app. Then enter the 6-digit code that it shows.</p>
          <div class="flex flex-wrap items-start gap-6">
            <QR text={setup.otpauth_url} />
            <div class="flex min-w-0 flex-1 basis-56 flex-col gap-2">
              <CopyField label="Key to enter by hand" value={setup.secret} />
              <a class="text-sm break-all text-link underline-offset-4 hover:underline" href={setup.otpauth_url}>
                Open the setup link on the device with the app
              </a>
            </div>
          </div>
          <div class="flex flex-col gap-2">
            <Label for="totp-code">Code from the app</Label>
            <!-- svelte-ignore a11y_autofocus -->
            <Input
              id="totp-code"
              class="w-44 font-mono tracking-wider"
              autofocus
              inputmode="numeric"
              autocomplete="one-time-code"
              placeholder="123 456"
              bind:value={totpCode}
              disabled={totpBusy}
            />
          </div>
          {#if totpError}<ErrorState inline error={totpError} />{/if}
          <div class="flex gap-2">
            <Button type="submit" disabled={totpBusy || !totpCode.trim()}>
              {#if totpBusy}<Spinner />{/if}
              Confirm app
            </Button>
            <Button type="button" variant="outline" disabled={totpBusy} onclick={cancelTOTP}>Cancel</Button>
          </div>
        </form>
      {/if}
    </div>
  </Section>

  <Section title="Passkeys" description="A passkey is a key in your device, password manager or security key.">
    {#if data.passkeys.length === 0}
      <EmptyState icon={KeyRoundIcon} title="No passkey yet" description="Add one below." />
    {:else}
      <div class="w-full overflow-x-auto rounded-2xl border">
        <Table.Root>
          <Table.Caption class="sr-only">Passkeys</Table.Caption>
          <Table.Header>
            <Table.Row class="hover:bg-transparent">
              <Table.Head>Name</Table.Head>
              <Table.Head>Added</Table.Head>
              <Table.Head>Last used</Table.Head>
              <Table.Head class="w-0"><span class="sr-only">Actions</span></Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each data.passkeys as p (p.id)}
              <Table.Row>
                <Table.Cell class="font-medium">{p.name}</Table.Cell>
                <Table.Cell><RelativeTime at={p.created_at} /></Table.Cell>
                <Table.Cell><RelativeTime at={p.last_used_at} empty="never" /></Table.Cell>
                <Table.Cell class="text-right">
                  <Button variant="destructive" size="sm" onclick={() => askRemove({ kind: "passkey", item: p })}>Remove</Button>
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      </div>
    {/if}

    <div class="mt-4 max-w-form">
      {#if passkeysSupported()}
        <form class="flex flex-col gap-4" onsubmit={addPasskey}>
          <div class="flex flex-col gap-2">
            <Label for="passkey-name">Name of the new passkey</Label>
            <Input id="passkey-name" class="max-w-sm" placeholder="Laptop" required bind:value={passkeyName} disabled={passkeyBusy} />
          </div>
          {#if passkeyError}<ErrorState inline error={passkeyError} />{/if}
          <div class="flex gap-2">
            <Button type="submit" disabled={passkeyBusy || !passkeyName.trim()}>
              {#if passkeyBusy}<Spinner />{/if}
              Add passkey
            </Button>
          </div>
        </form>
      {:else}
        <p class="text-sm text-muted-foreground">
          This browser cannot create passkeys. Passkeys need HTTPS or localhost.
        </p>
      {/if}
    </div>
  </Section>

  <Section
    title="Recovery codes"
    description={data.recovery_codes > 0
      ? `Each code signs you in once if you lose your authenticator app or passkey. ${data.recovery_codes} of 10 codes are unused.`
      : "Each code signs you in once if you lose your authenticator app or passkey. You have no recovery codes."}
    narrow
  >
    {#if codes}
      <div class="mb-4 flex flex-col gap-2">
        <CopyField label="New recovery codes" value={codes.join("\n")} block secret />
        <div><Button variant="outline" size="sm" onclick={() => (codes = null)}>Hide the codes</Button></div>
      </div>
    {/if}
    <div class="flex flex-wrap items-center gap-2">
      <Button
        variant="outline"
        disabled={count === 0}
        onclick={() => (data.recovery_codes > 0 ? (recoveryOpen = true) : generate())}
      >
        {data.recovery_codes > 0 ? "Generate new codes" : "Generate codes"}
      </Button>
      {#if count === 0}
        <span class="text-sm text-muted-foreground">Add an authenticator app or a passkey first.</span>
      {/if}
    </div>
  </Section>
{:else if factors.error}
  <ErrorState error={factors.error} onRetry={factors.reload} />
{:else}
  <LoadingRows rows={5} />
{/if}

<ConfirmDialog
  bind:open={removeOpen}
  title={removal?.kind === "passkey" ? "Remove passkey" : "Remove authenticator app"}
  confirmLabel={removal?.kind === "passkey" ? "Remove passkey" : "Remove app"}
  destructive
  changes={removeChanges}
  onConfirm={remove}
/>

<ConfirmDialog
  bind:open={recoveryOpen}
  title="Generate new recovery codes"
  confirmLabel="Generate new codes"
  destructive
  changes={["Makes your 10 new codes the only valid ones", "Invalidates every unused recovery code you have now"]}
  onConfirm={generate}
/>
