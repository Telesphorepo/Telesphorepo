<!--
  A place inside a repository: the folder path of the Files tab, in the toolbar of its panel. Body
  text after a folder icon, the steps parted by slashes. A step with `href` is a link; the last
  step is the current folder, in medium weight. A long step truncates and shows its full text on
  hover.

    <Breadcrumbs label="Folder" items={[{ label: "maven", href }, { label: "com", href }, { label: "acme" }]} />
-->
<script lang="ts">
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import type { Crumb } from "$lib/crumbs";

  let {
    items,
    label = "Folder",
  }: {
    items: Crumb[];
    /** Accessible name of the path. */
    label?: string;
  } = $props();
</script>

<nav aria-label={label} class="flex min-w-0 items-start gap-2">
  <FolderOpenIcon aria-hidden="true" class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
  <ol class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-body text-muted-foreground">
    {#each items as c, i (i)}
      {@const last = i === items.length - 1}
      <li class="flex min-w-0 items-center gap-1.5">
        {#if c.href && !last}
          <a
            href={c.href}
            title={c.label.length > 24 ? c.label : undefined}
            class="-mx-1 block max-w-[16rem] truncate rounded-sm px-1 outline-none transition-colors hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >{c.label}</a
          >
        {:else}
          <span title={c.label.length > 24 ? c.label : undefined} class="block max-w-[16rem] truncate {last ? 'font-medium text-foreground' : ''}">{c.label}</span>
        {/if}
        {#if !last}<span aria-hidden="true" class="opacity-60">/</span>{/if}
      </li>
    {/each}
  </ol>
</nav>
