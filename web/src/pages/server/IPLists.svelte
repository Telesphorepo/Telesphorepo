<!--
  Networks (Network permission): named address ranges (network lists) that repositories allow
  downloads and publishes from. Lists the server downloads (GitHub Actions, Cloudflare) refresh
  every day; your own lists hold the addresses you paste. Each list is a card that opens it: its
  title, its number of ranges and what uses it, and when it was refreshed or added. A downloaded
  list refreshes from its card.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { api, errorMessage, type IPList } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { formatCount } from "$lib/format";
  import { count, someNames } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import SourceGrid from "$lib/components/SourceGrid.svelte";
  import ItemCard from "$lib/components/ItemCard.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import { usedNames } from "./ip-lists/usage";
  import NetworkIcon from "@lucide/svelte/icons/network";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RefreshIcon from "@lucide/svelte/icons/refresh-cw";

  let {}: PageProps = $props();

  const lists = new Resource(() => api.ipLists.list());
  const presets = listOf(lists, (l) => l.filter((x) => x.kind === "preset"));
  const custom = listOf(lists, (l) => l.filter((x) => x.kind === "custom"));
  const open = (l: IPList) => href(`/settings/networks/list/${encodeURIComponent(l.name)}`);
  const newList = href("/settings/networks/new");

  /** "120 ranges · used by maven and apt", "No ranges · used by nothing". */
  function facts(l: IPList): string {
    const names = usedNames(l.used_by);
    const ranges = l.count === 0 ? "No ranges" : count(l.count, "range", "ranges");
    return `${ranges} · used by ${names.length ? someNames(names, 3) : "nothing"}`;
  }

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

<Section plain title="Downloaded lists" description="The server refreshes these from their owners every day.">
  <SourceGrid source={presets} label="Downloaded network lists" key={(l) => l.name}>
    {#snippet card(l)}
      <ItemCard title={l.title} href={open(l)}>
        {#snippet icon()}<NetworkIcon class="size-5" />{/snippet}
        {#snippet badges()}{#if l.error}<StatusBadge tone="danger" title={l.error}>Refresh failed</StatusBadge>{/if}{/snippet}
        <p class="truncate" title={facts(l)}>{facts(l)}</p>
        <p>{#if l.fetched_at}Refreshed <RelativeTime at={l.fetched_at} />{:else}Not downloaded yet{/if}</p>
        {#snippet actions()}
          <Button variant="ghost" size="sm" disabled={refreshing !== null} onclick={() => refresh(l)}>
            {#if refreshing === l.name}<Spinner />{:else}<RefreshIcon />{/if}
            Refresh
          </Button>
        {/snippet}
      </ItemCard>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={NetworkIcon} text="This server downloads no lists." />
    {/snippet}
  </SourceGrid>
</Section>

<Section plain title="Your lists">
  {#snippet actions()}
    {#if custom.items.length}<Button variant="outline" size="sm" href={newList}><PlusIcon />New network list</Button>{/if}
  {/snippet}
  <SourceGrid source={custom} label="Your network lists" key={(l) => l.name}>
    {#snippet card(l)}
      <ItemCard title={l.title} href={open(l)}>
        {#snippet icon()}<NetworkIcon class="size-5" />{/snippet}
        {#snippet badges()}{#if l.count === 0}<StatusBadge tone="warning">Empty</StatusBadge>{/if}{/snippet}
        <p class="truncate" title={facts(l)}>{facts(l)}</p>
        <p>Added <RelativeTime at={l.created_at} /></p>
      </ItemCard>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={NetworkIcon} text="None yet. Name your office network or your CI runners once, then use them in many repositories.">
        <Button variant="outline" size="sm" href={newList}><PlusIcon />New network list</Button>
      </CompactEmpty>
    {/snippet}
  </SourceGrid>
</Section>
