<!--
  Nexus migration (administrators): Nexus servers whose repositories this server copies and keeps
  in sync until you switch over. Add a server here; its page chooses the repositories, shows the
  syncs and switches over.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { api, type NexusSource } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import { formatCount } from "$lib/format";
  import { progressLine, sourceStatus } from "./nexus/words";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {}: PageProps = $props();

  const fid = $props.id();
  const sources = new Keyset((p) => api.nexus.list(p));
  const open = (s: NexusSource) => href(`/settings/nexus/${s.id}`);

  // While a source syncs, its progress line in the table follows the sync.
  const syncing = $derived(sources.items.some((s) => s.status === "syncing"));
  $effect(() => {
    if (!syncing) return;
    const timer = setInterval(() => sources.reload(), 3000);
    return () => clearInterval(timer);
  });

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
  {#snippet actions()}{#if sources.items.length}<Button onclick={startAdd}><PlusIcon />Add a Nexus server</Button>{/if}{/snippet}
</PageHeader>

<Section>
<DataTable
  source={sources}
  caption="Nexus servers"
  key={(s) => s.id}
  href={open}
  columns={[
    { label: "Server", grow: true },
    { label: "Status" },
    { label: "Repositories", align: "right", hideOnPhone: true },
    { label: "Last check", align: "right", hideOnPhone: true },
    { label: "Next check", align: "right", hideOnPhone: true },
  ]}
>
  {#snippet row(s)}
    <Cell grow>
      <span class="flex min-w-0 items-baseline gap-2">
        <a href={open(s)} class="shrink-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{s.name}</a>
        <span class="min-w-0 truncate text-muted-foreground max-sm:hidden" title={s.base_url}>{s.base_url}</span>
      </span>
    </Cell>
    <Cell>
      <StatusBadge tone={sourceStatus[s.status].tone} title={s.status === "failed" ? s.last_error : undefined}>{sourceStatus[s.status].word}</StatusBadge>
    </Cell>
    <Cell num hideOnPhone>{formatCount(s.repo_count)}</Cell>
    {#if s.status === "syncing" && s.last_sync}
      <Cell num muted hideOnPhone text={progressLine(s.last_sync)} />
    {:else}
      <Cell num muted hideOnPhone><RelativeTime at={s.last_sync?.finished_at} empty="Not yet" /></Cell>
    {/if}
    <Cell num muted hideOnPhone><RelativeTime at={s.next_sync_at} empty="" /></Cell>
  {/snippet}
  {#snippet empty()}
    <CompactEmpty
      icon={ArrowRightLeftIcon}
      text="No Nexus server yet. Add one with its address and an account that can read its repositories."
    >
      <Button variant="outline" onclick={startAdd}><PlusIcon />Add a Nexus server</Button>
    </CompactEmpty>
  {/snippet}
</DataTable>
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
