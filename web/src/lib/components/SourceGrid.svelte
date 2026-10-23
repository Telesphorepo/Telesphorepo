<!--
  A CardGrid over a source (a Keyset, or listOf(resource)), with the states of DataTable and
  ItemList: Loading in the space of four rows (after 300 ms; a reload keeps the current cards), an
  error with Try again, `empty` (a CompactEmpty) in a card in place of an empty grid (the grid sits in a `plain` Section, so the card is the surface the empty state needs), and "Load more" for a
  Keyset. `card` renders one ItemCard.

    <SourceGrid source={listOf(keys)} label="Signing keys" key={(k) => k.name}>
      {#snippet card(k)}<ItemCard title={k.name}>...</ItemCard>{/snippet}
      {#snippet empty()}<CompactEmpty icon={KeyRoundIcon} title="Signing keys" state="None yet" text="..." />{/snippet}
    </SourceGrid>
-->
<script lang="ts" generics="T, K extends string | number">
  import type { Snippet } from "svelte";
  import * as Card from "$lib/components/ui/card";
  import CardGrid from "./CardGrid.svelte";
  import Loading from "./Loading.svelte";
  import SourceError from "./SourceError.svelte";
  import SourceMore from "./SourceMore.svelte";
  import type { TableSource } from "$lib/load.svelte";

  let {
    source,
    label,
    key,
    max,
    card,
    empty,
    emptyText = "Nothing here yet.",
    class: className,
  }: {
    source: TableSource<T>;
    /** Accessible name of the grid. */
    label: string;
    key: (item: T) => K;
    /** The most columns on a wide screen. */
    max?: number;
    /** One card: an ItemCard. */
    card: Snippet<[T]>;
    /** Shown in place of the grid when it has no items: a CompactEmpty. */
    empty?: Snippet;
    emptyText?: string;
    class?: string;
  } = $props();
</script>

{#if source.loading && source.items.length === 0}
  <Loading rows={4} />
{:else if source.error && source.items.length === 0}
  <div class="py-4"><SourceError {source} /></div>
{:else if source.items.length === 0}
  <!-- No cards to show: the empty state sits in the card a Section would give it. -->
  <Card.Root class="max-sm:[--card-spacing:--spacing(5)]">
    <Card.Content><div data-slot="table-empty">{#if empty}{@render empty()}{:else}<p class="text-body text-muted-foreground">{emptyText}</p>{/if}</div></Card.Content>
  </Card.Root>
{:else}
  <CardGrid {label} {max} class={className}>
    {#each source.items as item (key(item))}{@render card(item)}{/each}
  </CardGrid>
{/if}

<SourceMore {source} />
