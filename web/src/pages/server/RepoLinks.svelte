<!--
  Repository names as links to each repository's Access tab, on one line that truncates (the full
  list in a tooltip). The signing key cards use it for "Used by".
-->
<script lang="ts">
  import { repoAccessHref } from "./access";

  let {
    repos,
    empty = "None",
    to = repoAccessHref,
  }: { repos: string[]; empty?: string; /** The address a name opens; by default its Access tab. */ to?: (repo: string) => string } = $props();
</script>

{#if repos.length === 0}
  <span class="text-muted-foreground">{empty}</span>
{:else}
  <span class="block truncate" title={repos.join(", ")}>
    {#each repos as r, i (r)}{#if i > 0}<span class="text-muted-foreground">{", "}</span>{/if}<a
        href={to(r)}
        class="rounded-sm text-link underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50">{r}</a
      >{/each}
  </span>
{/if}
