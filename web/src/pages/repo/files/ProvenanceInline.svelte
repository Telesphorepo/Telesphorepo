<!--
  Where a build came from, in one line for a table cell or a caption: the short commit (a link to
  it) and the branch or tag. Only GitHub Actions builds carry provenance; anything else renders
  `empty`.
-->
<script lang="ts">
  import { shortSha, type Provenance } from "../browse";
  import GitCommitIcon from "@lucide/svelte/icons/git-commit-horizontal";

  let { p, empty = "" }: { p: Provenance | null | undefined; empty?: string } = $props();
</script>

{#if p}
  <span class="inline-flex min-w-0 items-center gap-1.5 whitespace-nowrap">
    <GitCommitIcon aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" />
    <a
      href={p.commit_url}
      target="_blank"
      rel="noopener noreferrer"
      title="Commit {p.sha} in {p.repository}"
      class="rounded-sm text-link tabular-nums underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
      >{shortSha(p.sha)}</a
    >
    {#if p.ref_name}<span class="min-w-0 truncate text-muted-foreground">{p.ref_name}</span>{/if}
  </span>
{:else if empty}
  <span class="text-muted-foreground">{empty}</span>
{/if}
