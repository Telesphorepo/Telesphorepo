<!--
  The CI build that published a file or a version, as facts with links: the CI system, the
  commit, the branch or tag, the workflow and its run, the repository and who started the run.
  The server verified each value from the job's identity token; its CI provider made the links.
-->
<script lang="ts">
  import type { BuildRecord } from "$lib/api";
  import { buildLink, shortSha } from "../browse";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";

  let { p }: { p: BuildRecord } = $props();

  const commit = $derived(buildLink(p, "commit"));
  const run = $derived(buildLink(p, "run"));
  const repository = $derived(buildLink(p, "repository"));
  const workflow = $derived(buildLink(p, "workflow"));
  const linkClass =
    "inline-flex min-w-0 items-center gap-1 rounded-sm text-link underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50";
</script>

<dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
  <dt class="text-muted-foreground">CI</dt>
  <dd class="truncate">{p.provider_title}</dd>
  {#if p.revision}
    <dt class="text-muted-foreground">Commit</dt>
    <dd class="flex min-w-0 items-center gap-1">
      {#if commit}
        <a href={commit.url} target="_blank" rel="noopener noreferrer" class={linkClass} title={p.revision}>
          <span class="tabular-nums">{shortSha(p.revision)}</span><ExternalLinkIcon aria-hidden="true" class="size-3.5 shrink-0" />
        </a>
      {:else}
        <span class="tabular-nums" title={p.revision}>{shortSha(p.revision)}</span>
      {/if}
      <CopyButton value={p.revision} label="Copy the commit" size="icon-xs" class="text-muted-foreground" />
    </dd>
  {/if}
  {#if p.ref_name}
    <dt class="text-muted-foreground">{p.ref_type === "tag" ? "Tag" : p.ref_type === "branch" ? "Branch" : "Ref"}</dt>
    <dd class="truncate" title={p.ref}>{p.ref_name}</dd>
  {/if}
  {#if p.workflow || run}
    <dt class="text-muted-foreground">Workflow</dt>
    <dd class="flex min-w-0 flex-col items-start">
      {#if workflow}
        <a href={workflow.url} target="_blank" rel="noopener noreferrer" class={linkClass} title={workflow.label}>
          <span class="truncate">{p.workflow || workflow.label}</span><ExternalLinkIcon aria-hidden="true" class="size-3.5 shrink-0" />
        </a>
      {:else if p.workflow}
        <span class="max-w-full truncate">{p.workflow}</span>
      {/if}
      {#if run}
        <a href={run.url} target="_blank" rel="noopener noreferrer" class={linkClass}>
          <span class="tabular-nums">{run.label}</span><ExternalLinkIcon aria-hidden="true" class="size-3.5 shrink-0" />
        </a>
      {:else if p.run}
        <span class="tabular-nums">Run {p.run}</span>
      {/if}
    </dd>
  {/if}
  {#if p.repository}
    <dt class="text-muted-foreground">Repository</dt>
    <dd class="min-w-0">
      {#if repository}
        <a href={repository.url} target="_blank" rel="noopener noreferrer" class={linkClass}>
          <span class="truncate">{p.repository}</span><ExternalLinkIcon aria-hidden="true" class="size-3.5 shrink-0" />
        </a>
      {:else}
        <span class="truncate">{p.repository}</span>
      {/if}
    </dd>
  {/if}
  {#if p.actor}
    <dt class="text-muted-foreground">Started by</dt>
    <dd class="truncate">{p.actor}</dd>
  {/if}
</dl>
