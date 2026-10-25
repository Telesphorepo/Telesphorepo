<!--
  The top of every page: the way back, the title (the page's only h1), an optional one-line
  description that says what the page is for, and actions on the right. `meta` sits next to the
  title (badges such as the format and visibility of a repository). `status` is one muted line under
  them: an icon and one sentence about a state that has no other place on the page (a repository
  whose uploads wait for the switch-over from Nexus).

  The way back is one link above the title, "← <parent>": the page one level up. The level comes
  from the page's trail. On a Settings page the trail comes from the sidebar (Settings › group ›
  item, then the title on a sub-page: CI pipelines over acme/app); on an account page Your
  account › item. `crumbs` replaces what follows the group when the page sits deeper (a repository's
  settings pass none: Repositories over maven). A Browse page passes `crumbs` (its whole
  trail: Repositories over maven). A page of the sidebar has no way back: the sidebar is the way
  to it.

    <PageHeader title="Devices" description="Computers that publish with the publisher client of their build tool.">
      {#snippet actions()}<Button href={href("/device")}>Approve a device</Button>{/snippet}
    </PageHeader>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import { sidebarTrail, type Crumb } from "$lib/crumbs";
  import { navPlace } from "../../modules";
  import { current } from "$lib/current.svelte";
  import { href, router } from "$lib/router.svelte";

  let {
    title,
    description,
    crumbs,
    meta,
    status,
    actions,
  }: {
    title: string;
    /** One sentence. Leave it out rather than repeat the title. */
    description?: string;
    /** Settings: the trail after the group. Browse: the whole trail. The last step is this page. */
    crumbs?: Crumb[];
    meta?: Snippet;
    /** One muted line under the title: an icon and one sentence. */
    status?: Snippet;
    actions?: Snippet;
  } = $props();

  const trail = $derived.by((): Crumb[] => {
    if (current.route && current.route.layout !== "app") return sidebarTrail(router.path, title, crumbs);
    return crumbs ?? [];
  });
  // The way back is the page's parent: the last step of the trail that links somewhere else. A page
  // of the sidebar has none, since the sidebar is the way to it.
  const inSidebar = $derived(!!current.route && current.route.layout !== "app" && !crumbs && navPlace(router.path)?.item.path === router.path);
  const parent = $derived.by((): Crumb | null => {
    if (inSidebar) return null;
    const here = href(router.path);
    return [...trail].reverse().find((c) => !!c.href && c.href !== here) ?? null;
  });
</script>

<header class="mb-(--header-gap)">
  {#if parent}
    <a
      href={parent.href}
      class="-mx-1 mb-3 inline-flex max-w-full items-center gap-1 rounded-sm px-1 text-meta text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
    >
      <ArrowLeftIcon aria-hidden="true" class="size-3.5 shrink-0" /><span class="truncate">{parent.label}</span>
    </a>
  {/if}
  <div class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
    <div class="min-w-0 grow basis-64">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 class="text-title break-words">{title}</h1>
        {#if meta}<div class="flex flex-wrap items-center gap-2">{@render meta()}</div>{/if}
      </div>
      {#if description}<p class="mt-1.5 max-w-prose text-body text-muted-foreground">{description}</p>{/if}
      {#if status}
        <p class="mt-2 flex max-w-prose items-start gap-2 text-meta text-pretty text-muted-foreground [&_svg]:mt-0.5 [&_svg]:size-3.5 [&_svg]:shrink-0">
          {@render status()}
        </p>
      {/if}
    </div>
    {#if actions}<div data-slot="header-actions" class="flex max-w-full shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>{/if}
  </div>
</header>
