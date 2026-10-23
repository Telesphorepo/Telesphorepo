<!--
  Signing keys (Signing keys permission): the keys that sign the indexes of repositories, and
  which repositories use each one (links to their Signing tab), each key a card with the end of its
  fingerprint. With two keys or more, a select over the cards keeps the keys of one repository
  that uses a key. Download a public key; delete a key that no repository uses. New signing key opens
  a page that creates or imports one.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { api, type SigningKey } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { groupFingerprint } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import * as Select from "$lib/components/ui/select";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import SourceGrid from "$lib/components/SourceGrid.svelte";
  import ItemCard from "$lib/components/ItemCard.svelte";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import RepoLinks from "./RepoLinks.svelte";
  import FileKeyIcon from "@lucide/svelte/icons/file-key";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import DownloadIcon from "@lucide/svelte/icons/download";

  let {}: PageProps = $props();

  const keys = new Resource(() => api.signingKeys.list());
  // The repositories that use a key, for the filter; their formats come from the list of repositories.
  const repos = new Resource(() => api.repos.list());
  const formatOf = (name: string) => repos.data?.find((r) => r.name === name)?.format ?? "";
  const ALL = "*";
  let repoFilter = $state(ALL);
  const used = $derived([...new Set((keys.data ?? []).flatMap((k) => k.repos))].sort((a, b) => a.localeCompare(b)));
  const source = listOf(keys, (list) => (repoFilter === ALL ? list : list.filter((k) => k.repos.includes(repoFilter))));
  const newKey = href("/settings/signing-keys/new");
  const signingTab = (r: string) => href(auth.canRepo(r, "repo.settings") ? repoSettingsPath(r, "signing") : repoPath(r));


  let target = $state<SigningKey | null>(null);
  let deleteOpen = $state(false);

  async function remove() {
    if (!target) return;
    await api.signingKeys.delete(target.name);
    toast.success(`Deleted the signing key ${target.name}`);
    await keys.reload();
  }
  const quietDanger = "hover:bg-destructive/10 hover:text-destructive";
</script>

<PageHeader title="Signing keys" description="Keys that sign the indexes of repositories, so clients can check them.">
  {#snippet actions()}{#if keys.data?.length}<Button href={newKey}><PlusIcon />New signing key</Button>{/if}{/snippet}
</PageHeader>

{#snippet repoIcon()}<FormatIcon format={formatOf(repoFilter)} />{/snippet}

<Section plain>
  {#if (keys.data?.length ?? 0) > 1 && used.length > 1}
    <Toolbar>
      <Select.Root type="single" bind:value={repoFilter}>
        <Select.Trigger class="w-full sm:w-56" aria-label="Repository" icon={repoFilter === ALL ? undefined : repoIcon}>
          <span class="truncate">{repoFilter === ALL ? "All repositories" : repoFilter}</span>
        </Select.Trigger>
        <Select.Content>
          <Select.Item value={ALL} label="All repositories" inset />
          {#each used as r (r)}
            <Select.Item value={r} label={r}>
              {#snippet icon()}<FormatIcon format={formatOf(r)} />{/snippet}
            </Select.Item>
          {/each}
        </Select.Content>
      </Select.Root>
    </Toolbar>
  {/if}
  <SourceGrid {source} label="Signing keys" key={(k) => k.name}>
    {#snippet card(k)}
      <ItemCard title={k.name}>
        {#snippet icon()}<FileKeyIcon class="size-5" />{/snippet}
        {#snippet badges()}{#if k.error}<StatusBadge tone="danger" title={k.error}>Cannot sign</StatusBadge>{/if}{/snippet}
        <p class="flex items-center gap-1">
          <span class="tabular-nums" title={groupFingerprint(k.fingerprint)}>Fingerprint …{k.fingerprint.slice(-16)}</span>
          <CopyButton value={k.fingerprint} size="icon-xs" label="Copy the fingerprint of {k.name}" />
        </p>
        <p class="flex min-w-0 gap-1"><span class="shrink-0">Used by</span><RepoLinks repos={k.repos} to={signingTab} empty="no repository" /></p>
        <p>Added <RelativeTime at={k.created_at} /></p>
        {#snippet actions()}
          <Button variant="ghost" size="sm" href={api.signingKeys.publicURL(k.name)} download="{k.name}.asc" aria-label="Download the public key of {k.name}">
            <DownloadIcon />Public key
          </Button>
          {#if k.repos.length > 0}
            <!-- A key in use cannot be deleted: the card says why where Delete would be. -->
            <span>Move its repositories to another key to delete it</span>
          {:else}
            <Button
              variant="ghost"
              size="sm"
              class={quietDanger}
              onclick={() => {
                target = k;
                deleteOpen = true;
              }}>Delete</Button
            >
          {/if}
        {/snippet}
      </ItemCard>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={FileKeyIcon} text="None yet.">
        <Button variant="outline" href={newKey}><PlusIcon />New signing key</Button>
      </CompactEmpty>
    {/snippet}
  </SourceGrid>
</Section>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete the signing key {target?.name ?? ''}?"
  confirmLabel="Delete key"
  destructive
  changes={["Deletes the private key from this server. You cannot get it back", "Machines that trust its public key keep it until you remove it there"]}
  onConfirm={remove}
/>
