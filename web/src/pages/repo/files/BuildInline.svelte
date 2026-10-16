<!--
  Where a build came from, in one line for a table cell or a caption: the short commit (a link to
  it when the CI provider made one) and the branch or tag, or else the CI system and its run. A
  file without a CI build record renders `empty`.
-->
<script lang="ts">
  import type { BuildRecord } from "$lib/api";
  import { buildLink, shortSha } from "../browse";
  import GitCommitIcon from "@lucide/svelte/icons/git-commit-horizontal";

  let { p, empty = "" }: { p: BuildRecord | null | undefined; empty?: string } = $props();

  const commit = $derived(p ? buildLink(p, "commit") : undefined);
  const linkClass =
    "rounded-sm text-link tabular-nums underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50";
</script>

{#if p}
  <span class="inline-flex min-w-0 items-center gap-1.5 whitespace-nowrap">
    <GitCommitIcon aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" />
    {#if p.revision && commit}
      <a href={commit.url} target="_blank" rel="noopener noreferrer" title="Commit {p.revision} in {p.repository}" class={linkClass}
        >{shortSha(p.revision)}</a
      >
    {:else if p.revision}
      <span class="tabular-nums" title={p.revision}>{shortSha(p.revision)}</span>
    {:else}
      <span class="min-w-0 truncate">{p.provider_title}{p.run ? ` #${p.run}` : ""}</span>
    {/if}
    {#if p.ref_name}<span class="min-w-0 truncate text-muted-foreground">{p.ref_name}</span>{/if}
  </span>
{:else if empty}
  <span class="text-muted-foreground">{empty}</span>
{/if}
