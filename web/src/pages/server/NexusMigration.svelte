<!--
  Nexus migration (Nexus migration permission): Nexus servers whose repositories this server copies and keeps
  in sync until you switch over. Add a server here; its page chooses the repositories, shows the
  syncs and switches over.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { api, type NexusSource } from "$lib/api";
  import { Keyset, type TableSource } from "$lib/load.svelte";
  import { Live } from "$lib/live.svelte";
  import LiveStatus from "$lib/components/LiveStatus.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import SourceGrid from "$lib/components/SourceGrid.svelte";
  import ItemCard from "$lib/components/ItemCard.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import { count } from "$lib/words";
  import { progressLine, sourceStatus } from "./nexus/words";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const fid = $props.id();
  const sources = new Keyset((p) => api.nexus.list(p));
  const open = (s: NexusSource) => href(`/settings/nexus/${s.id}`);

  // While a source syncs, the progress line of its card follows the sync.
  const syncing = $derived(sources.items.some((s) => s.status === "syncing"));
  const live = new Live({ every: 3000, while: () => syncing, run: sources.fetch });
  // While a sync runs, LiveStatus says when a refresh fails.
  const source: TableSource<NexusSource> = {
    get items() {
      return sources.items;
    },
    get loading() {
      return sources.loading && sources.items.length === 0;
    },
    get error() {
      return live.active && sources.items.length ? "" : sources.error;
    },
    reload: sources.reload,
    get hasMore() {
      return sources.hasMore;
    },
    get loadingMore() {
      return sources.loadingMore;
    },
    more: sources.more,
  };

  let addOpen = $state(false);
  let name = $state("");
  let baseURL = $state("");
  let username = $state("");
  let password = $state("");

  const nameError = $derived(name.trim() && !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name.trim()) ? "Use lowercase letters, digits, dots, dashes and underscores." : "");

  function startAdd() {
    name = baseURL = username = password = "";
    addOpen = true;
  }

  async function add() {
    try {
      const s = await api.nexus.create({ name: name.trim(), base_url: baseURL.trim(), username, password });
      navigate(`/settings/nexus/${s.id}`);
    } finally {
      password = "";
    }
  }
</script>

<PageHeader title="Nexus migration" description="Copy the repositories of a Nexus server, keep them in sync, then switch over when every build uses this server.">
  {#snippet actions()}<LiveStatus {live} />{#if sources.items.length}<Button onclick={startAdd}><PlusIcon />Add a Nexus server</Button>{/if}{/snippet}
</PageHeader>

<Section plain>
<SourceGrid {source} label="Nexus servers" key={(s) => s.id}>
  {#snippet card(s)}
    <ItemCard title={s.name} href={open(s)}>
      {#snippet icon()}<ArrowRightLeftIcon class="size-5" />{/snippet}
      {#snippet badges()}
        <StatusBadge tone={sourceStatus[s.status].tone} title={s.status === "failed" ? s.last_error : undefined}>{sourceStatus[s.status].word}</StatusBadge>
      {/snippet}
      <p class="truncate" title={s.base_url}>{s.base_url}</p>
      <p class="tabular-nums">{count(s.repo_count, "repository", "repositories")}</p>
      {#if s.status === "syncing" && s.last_sync}
        <p class="truncate tabular-nums" title={progressLine(s.last_sync)}>{progressLine(s.last_sync)}</p>
      {:else if s.next_sync_at}
        <p>Next check <RelativeTime at={s.next_sync_at} /></p>
      {/if}
    </ItemCard>
  {/snippet}
  {#snippet empty()}
    <CompactEmpty
      icon={ArrowRightLeftIcon}
      text="No Nexus server yet. Add one with its address and an account that can read its repositories."
    >
      <Button variant="outline" onclick={startAdd}><PlusIcon />Add a Nexus server</Button>
    </CompactEmpty>
  {/snippet}
</SourceGrid>
</Section>

<ShortFormDialog
  bind:open={addOpen}
  title="Add a Nexus server"
  description="The server keeps the password encrypted and uses it only to read the repositories."
  submitLabel="Add server"
  canSubmit={!!name.trim() && !nameError && !!baseURL.trim() && !!username && !!password}
  onSubmit={add}
>
  <FormField label="Name" for="{fid}-name" hint="Use lowercase letters, digits, dots and dashes, for example old-nexus." error={nameError || undefined}>
    <Input id="{fid}-name" autocomplete="off" spellcheck={false} aria-invalid={nameError ? true : undefined} bind:value={name} />
  </FormField>
  <FormField label="Address" for="{fid}-url">
    <Input id="{fid}-url" type="url" placeholder="https://nexus.example.com" autocomplete="off" bind:value={baseURL} />
  </FormField>
  <FormField label="Username" for="{fid}-user" hint="The account must be able to read every repository that you copy.">
    <Input id="{fid}-user" autocomplete="off" bind:value={username} />
  </FormField>
  <FormField label="Password" for="{fid}-pass">
    <Input id="{fid}-pass" type="password" autocomplete="new-password" bind:value={password} />
  </FormField>
</ShortFormDialog>
