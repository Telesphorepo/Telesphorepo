<!--
  The top of every page: the breadcrumbs, the title (the page's only h1), an optional one-line
  description that says what the page is for, and actions on the right. `meta` sits next to the
  title (badges such as the format and visibility of a repository). `status` is one muted line under
  them: an icon and one sentence about a state that has no other place on the page (a repository
  whose uploads wait for the switch-over from Nexus).

  On a Settings page the breadcrumbs come from the sidebar: Settings › group › item, then the title
  on a sub-page (Settings › People and CI › CI pipelines › acme/app). `crumbs` replaces what
  follows the group when the page sits deeper (Settings › Repositories › maven › Access). A Browse
  page shows breadcrumbs only when it passes `crumbs` (its whole trail).

    <PageHeader title="Devices" description="Computers that publish with the publisher client of their build tool.">
      {#snippet actions()}<Button href={href("/device")}>Approve a device</Button>{/snippet}
    </PageHeader>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import Breadcrumbs from "./Breadcrumbs.svelte";
  import { settingsTrail, type Crumb } from "$lib/crumbs";
  import { current } from "$lib/current.svelte";
  import { href, router } from "$lib/router.svelte";

  let {
    title,
    description,
    crumbs,
    back,
    meta,
    status,
    actions,
  }: {
    title: string;
    /** One sentence. Leave it out rather than repeat the title. */
    description?: string;
    /** Settings: the trail after the group. Browse: the whole trail. The last step is this page. */
    crumbs?: Crumb[];
    /** @deprecated Pass `crumbs`. A Browse sub-page's parent; it shows as the breadcrumb before the title. */
    back?: { href: string; label: string };
    meta?: Snippet;
    /** One muted line under the title: an icon and one sentence. */
    status?: Snippet;
    actions?: Snippet;
  } = $props();

  const trail = $derived.by((): Crumb[] => {
    if (current.route?.layout === "settings") return settingsTrail(router.path, title, crumbs);
    if (crumbs) return crumbs;
    if (back) return [{ label: "Repositories", href: href("/") }, { label: back.label, href: back.href }, { label: title }];
    return [];
  });
</script>

<header class="mb-8">
  {#if trail.length}<div class="mb-3"><Breadcrumbs items={trail} /></div>{/if}
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
    {#if actions}<div class="flex shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>{/if}
  </div>
</header>
