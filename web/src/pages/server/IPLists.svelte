<!--
  Networks (Network permission): named address ranges (network lists) that repositories allow
  downloads and publishes from. Lists the server downloads (GitHub Actions, Cloudflare) refresh
  every day; your own lists hold the addresses you paste. Each row links to the repositories that
  use the list; a row opens the list.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { api, errorMessage, type IPList } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import UsedBy from "./ip-lists/UsedBy.svelte";
  import NetworkIcon from "@lucide/svelte/icons/network";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RefreshIcon from "@lucide/svelte/icons/refresh-cw";

  let {}: PageProps = $props();

  const lists = new Resource(() => api.ipLists.list());
  const presets = listOf(lists, (l) => l.filter((x) => x.kind === "preset"));
  const custom = listOf(lists, (l) => l.filter((x) => x.kind === "custom"));
  const open = (l: IPList) => href(`/settings/networks/list/${encodeURIComponent(l.name)}`);
  const newList = href("/settings/networks/new");


  let refreshing = $state<string | null>(null);
  async function refresh(l: IPList) {
    refreshing = l.name;
    try {
      const fresh = await api.ipLists.refresh(l.name);
      if (lists.data) lists.data = lists.data.map((x) => (x.name === fresh.name ? { ...fresh, ranges: undefined } : x));
      toast.success(`Refreshed ${l.title}: ${formatCount(fresh.count)} ranges`);
    } catch (e) {
      toast.error(`${l.title} did not refresh: ${errorMessage(e)}`);
      lists.reload();
    } finally {
      refreshing = null;
    }
  }
</script>

<PageHeader title="Networks" description="Named address ranges that repositories allow downloads and publishes from." />

<Section title="Downloaded lists" description="The server refreshes these from their owners every day.">
  <DataTable
    source={presets}
    caption="Downloaded network lists"
    key={(l) => l.name}
    href={open}
    columns={[
      { label: "List", grow: true },
      { label: "Used by", hideOnPhone: true },
      { label: "Ranges", align: "right" },
      { label: "Refreshed", align: "right", hideOnPhone: true },
      { label: "", srLabel: "Actions" },
    ]}
  >
    {#snippet row(l)}
      <Cell grow>
        <span class="flex min-w-0 items-baseline gap-2">
          <a href={open(l)} class="shrink-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{l.title}</a>
          {#if l.error}
            <span class="min-w-0 truncate text-destructive" title={l.error}>The last refresh failed: {l.error}</span>
          {:else if l.description}
            <span class="min-w-0 truncate text-muted-foreground max-sm:hidden" title={l.description}>{l.description}</span>
          {/if}
        </span>
      </Cell>
      <Cell hideOnPhone class="max-w-56"><UsedBy usage={l.used_by} /></Cell>
      <Cell num>{formatCount(l.count)}</Cell>
      <Cell num muted hideOnPhone><RelativeTime at={l.fetched_at} empty="Not yet" /></Cell>
      <Cell actions>
        <Button variant="ghost" size="sm" disabled={refreshing !== null} onclick={() => refresh(l)}>
          {#if refreshing === l.name}<Spinner />{:else}<RefreshIcon />{/if}
          <span class="max-sm:sr-only">Refresh</span>
        </Button>
      </Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={NetworkIcon} text="This server downloads no lists." />
    {/snippet}
  </DataTable>
</Section>

<Section title="Your lists">
  {#snippet actions()}
    {#if custom.items.length}<Button variant="outline" size="sm" href={newList}><PlusIcon />New network list</Button>{/if}
  {/snippet}
  <DataTable
    source={custom}
    caption="Your network lists"
    key={(l) => l.name}
    href={open}
    columns={[
      { label: "List", grow: true },
      { label: "Used by", hideOnPhone: true },
      { label: "Ranges", align: "right" },
      { label: "Added", align: "right", hideOnPhone: true },
    ]}
  >
    {#snippet row(l)}
      <Cell grow>
        <span class="flex min-w-0 items-baseline gap-2">
          <a href={open(l)} class="shrink-0 rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{l.title}</a>
          {#if l.description}<span class="min-w-0 truncate text-muted-foreground max-sm:hidden" title={l.description}>{l.description}</span>{/if}
        </span>
      </Cell>
      <Cell hideOnPhone class="max-w-56"><UsedBy usage={l.used_by} /></Cell>
      <Cell num>{#if l.count === 0}<StatusBadge tone="warning">Empty</StatusBadge>{:else}{formatCount(l.count)}{/if}</Cell>
      <Cell num muted hideOnPhone><RelativeTime at={l.created_at} /></Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={NetworkIcon} text="None yet. Name your office network or your CI runners once, then use them in many repositories.">
        <Button variant="outline" size="sm" href={newList}><PlusIcon />New network list</Button>
      </CompactEmpty>
    {/snippet}
  </DataTable>
</Section>
