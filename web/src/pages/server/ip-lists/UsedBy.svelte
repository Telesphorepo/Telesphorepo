<!--
  What uses a network list, on one line: the repositories (links to their Access tab) and the
  trusted proxies (a link to Network and proxy).
-->
<script lang="ts">
  import type { IPListUsage } from "$lib/api";
  import { href } from "$lib/router.svelte";
  import { repoAccessHref } from "../access";
  import { usedRepos } from "./usage";

  let { usage }: { usage: IPListUsage } = $props();

  const items = $derived([
    ...usedRepos(usage).map((r) => ({ label: r, href: repoAccessHref(r) })),
    ...(usage.trusted_proxies ? [{ label: "Network and proxy", href: href("/settings/proxy") }] : []),
  ]);
  const link = "rounded-sm text-link underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50";
</script>

{#if items.length === 0}
  <span class="text-muted-foreground">Nothing</span>
{:else}
  <span class="block truncate" title={items.map((i) => i.label).join(", ")}>
    {#each items as it, i (it.label)}{#if i > 0}<span class="text-muted-foreground">{", "}</span>{/if}<a href={it.href} class={link}>{it.label}</a>{/each}
  </span>
{/if}
