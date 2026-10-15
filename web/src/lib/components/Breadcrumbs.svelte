<!--
  Where the page sits: a trail of links that ends with the current page. Settings pages get it from
  PageHeader (Settings › Repositories › maven › Access); a Browse page passes its own trail to
  PageHeader `crumbs`. Every step but the last is a link; a long step truncates and shows its
  full text on hover.

    <Breadcrumbs items={[{ label: "Settings", href: "#/settings" }, { label: "Repositories", href: "#/settings/repositories" }, { label: "maven" }]} />
-->
<script lang="ts">
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import type { Crumb } from "$lib/crumbs";

  let { items }: { items: Crumb[] } = $props();
</script>

<nav aria-label="Breadcrumb">
  <ol class="flex min-w-0 flex-wrap items-center gap-x-1 gap-y-1 text-meta text-muted-foreground">
    {#each items as c, i (i)}
      {@const last = i === items.length - 1}
      <li class="flex min-w-0 items-center gap-1">
        {#if c.href && !last}
          <a
            href={c.href}
            title={c.label.length > 24 ? c.label : undefined}
            class="-mx-1 block max-w-[16rem] truncate rounded-sm px-1 outline-none transition-colors hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >{c.label}</a
          >
        {:else}
          <span
            aria-current={last ? "page" : undefined}
            title={c.label.length > 24 ? c.label : undefined}
            class="block max-w-[16rem] truncate {last ? 'text-foreground' : ''}">{c.label}</span
          >
        {/if}
        {#if !last}<ChevronRightIcon aria-hidden="true" class="size-3.5 shrink-0 opacity-60" />{/if}
      </li>
    {/each}
  </ol>
</nav>
