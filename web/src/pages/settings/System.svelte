<!--
  Admin: the settings groups that are not Storage or SSO, rendered generically from
  GET /api/settings (general, security, publishing, format groups), and the signing key card.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { toast } from "svelte-sonner";
  import { api, type Settings, type SettingsGroup } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Label } from "$lib/components/ui/label";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import CopyField from "$lib/components/CopyField.svelte";
  import SettingsGroupForm from "$lib/components/SettingsGroupForm.svelte";
  import { Skeleton } from "$lib/components/ui/skeleton";

  let { params, query }: PageProps = $props();

  // Storage and SSO have their own pages.
  const own = new Set(["storage", "sso"]);

  const settings = new Resource(() => api.settings.get());
  const key = new Resource(() => api.settings.signingKey());

  const groups = $derived((settings.data?.groups ?? []).filter((g) => !own.has(g.name)));

  function saved(result: unknown) {
    const r = result as Settings;
    if (r.groups) settings.data = r;
    else settings.reload();
  }

  // The signing key has its own routes; its settings group holds only the file options.
  const editable = (g: SettingsGroup) => g.settings.filter((s) => !(g.name === "signing" && s.read_only));

  let confirmGenerate = $state(false);
  let confirmUpload = $state(false);
  let armored = $state("");

  async function generate() {
    await api.settings.setSigningKey();
    toast.success("Generated a new signing key");
    key.reload();
  }

  async function upload() {
    if (!armored.trim()) throw new Error("Paste the armored private key first.");
    await api.settings.setSigningKey(armored.trim());
    armored = "";
    toast.success("Replaced the signing key");
    key.reload();
  }

  const downloadHref = (pub: string) => "data:text/plain;charset=utf-8," + encodeURIComponent(pub);
</script>

<PageHeader title="General settings" description="Server-wide settings. A setting forced by an environment variable is read-only here." />

{#if settings.loading && !settings.data}
  <LoadingRows rows={6} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if groups.length === 0}
  <EmptyState title="No settings" description="The server reports no settings in these groups." />
{:else}
  {#each groups as g (g.name)}
    {#if editable(g).length > 0}
      <Section narrow title={g.label}>
        <SettingsGroupForm settings={editable(g)} label={/settings$/i.test(g.label) ? g.label : `${g.label} settings`} onSaved={saved} />
      </Section>
    {/if}

    {#if g.name === "signing"}
      <Section narrow title="Signing key" description="Signs the Debian repository indexes. Apt clients trust its public key.">
        {#if key.loading && !key.data}
          <Skeleton class="h-20 w-full rounded-xl" />
        {:else if key.error}
          <ErrorState error={key.error} onRetry={key.reload} />
        {:else}
          <div class="flex flex-col gap-4">
            {#if key.data}
              <CopyField label="Fingerprint" value={key.data.fingerprint} />
              <CopyField label="Public key" value={key.data.public_key} block />
              <div>
                <Button variant="outline" size="sm" href={downloadHref(key.data.public_key)} download="signing-key.asc">
                  Download the public key
                </Button>
              </div>
            {:else}
              <p class="text-sm text-muted-foreground">No signing key is configured. Debian repositories stay unsigned until one exists.</p>
            {/if}
            <div class="flex flex-wrap gap-2">
              <Button variant={key.data ? "outline" : "default"} onclick={() => (confirmGenerate = true)}>
                {key.data ? "Generate a new key" : "Generate a key"}
              </Button>
              <Button variant="outline" onclick={() => (confirmUpload = true)}>Upload a key</Button>
            </div>
          </div>
        {/if}
      </Section>
    {/if}
  {/each}
{/if}

<ConfirmDialog
  bind:open={confirmGenerate}
  title="Generate a signing key"
  confirmLabel="Generate a new key"
  destructive={!!key.data}
  changes={[
    ...(key.data ? ["Discards the current key", "Apt clients must trust the new public key before they accept the next index"] : []),
    "Signs every Debian index with the new key from now on",
  ]}
  onConfirm={generate}
/>

<ConfirmDialog
  bind:open={confirmUpload}
  title="Upload a signing key"
  confirmLabel="Replace the signing key"
  destructive={!!key.data}
  changes={[
    ...(key.data ? ["Discards the current key", "Apt clients must trust the new public key"] : []),
    "Signs every Debian index with the uploaded key from now on",
  ]}
  onConfirm={upload}
>
  <div class="flex flex-col gap-2">
    <Label for="signing-armored">Armored private key</Label>
    <Textarea
      id="signing-armored"
      class="font-mono text-[0.8125rem]"
      rows={8}
      spellcheck={false}
      autocomplete="off"
      placeholder="-----BEGIN PGP PRIVATE KEY BLOCK-----"
      bind:value={armored}
    />
  </div>
</ConfirmDialog>
