<!--
  Where the page sits: a trail of links that ends with the current page. Settings pages get it from
  PageHeader (Settings › Repositories › maven › Access); a Browse page passes its own trail to
  PageHeader `crumbs`. Every step but the last is a link; a long step truncates and shows its
  full text on hover.

    <Breadcrumbs items={[{ label: "Settings", href: "#/settings" }, { label: "Repositories", href: "#/settings/repositories" }, { label: "maven" }]} />

  `path` is the other look, for a place inside a repository (the folder path of the Files tab, in
  the toolbar of its panel): body text after a folder icon, the steps parted by slashes, so it
  never reads as a second page trail.

    <Breadcrumbs path label="Folder" items={[{ label: "maven", href }, { label: "com", href }, { label: "acme" }]} />
-->
<script lang="ts">
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import type { Crumb } from "$lib/crumbs";

  let {
    items,
    path = false,
    label = "Breadcrumb",
  }: {
    items: Crumb[];
    /** A path inside a repository: body text, a folder icon, slashes between the steps. */
    path?: boolean;
    /** Accessible name of the trail. */
    label?: string;
  } = $props();
</script>

<nav aria-label={label} class={path ? "flex min-w-0 items-start gap-2" : undefined}>
  {#if path}<FolderOpenIcon aria-hidden="true" class="mt-0.5 size-4 shrink-0 text-muted-foreground" />{/if}
  <ol class="flex min-w-0 flex-wrap items-center gap-y-1 text-muted-foreground {path ? 'gap-x-1.5 text-body' : 'gap-x-1 text-meta'}">
    {#each items as c, i (i)}
      {@const last = i === items.length - 1}
      <li class="flex min-w-0 items-center {path ? 'gap-1.5' : 'gap-1'}">
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
            class="block max-w-[16rem] truncate {last ? 'text-foreground' : ''} {last && path ? 'font-medium' : ''}">{c.label}</span
          >
        {/if}
        {#if !last}
          {#if path}<span aria-hidden="true" class="opacity-60">/</span>{:else}<ChevronRightIcon aria-hidden="true" class="size-3.5 shrink-0 opacity-60" />{/if}
        {/if}
      </li>
    {/each}
  </ol>
</nav>
