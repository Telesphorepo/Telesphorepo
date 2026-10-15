<!--
  The build that published a file or a version, as facts with links: the commit, the branch or
  tag, the workflow run, the GitHub repository and who started the run. GitHub Actions builds
  only: the server verified each value from the job's identity token.
-->
<script lang="ts">
  import { shortSha, type Provenance } from "../browse";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";

  let { p }: { p: Provenance } = $props();

  // A rerun of the workflow: the run link opens that attempt.
  const attempt = $derived(!!p.run_attempt && p.run_attempt !== "1");
  const linkClass =
    "inline-flex min-w-0 items-center gap-1 rounded-sm text-link underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50";
</script>

<dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
  <dt class="text-muted-foreground">Commit</dt>
  <dd class="flex min-w-0 items-center gap-1">
    <a href={p.commit_url} target="_blank" rel="noopener noreferrer" class={linkClass} title={p.sha}>
      <span class="tabular-nums">{shortSha(p.sha)}</span><ExternalLinkIcon aria-hidden="true" class="size-3.5 shrink-0" />
    </a>
    <CopyButton value={p.sha} label="Copy the commit" size="icon-xs" class="text-muted-foreground" />
  </dd>
  {#if p.ref_name}
    <dt class="text-muted-foreground">{p.ref_type === "tag" ? "Tag" : p.ref_type === "branch" ? "Branch" : "Ref"}</dt>
    <dd class="truncate" title={p.ref}>{p.ref_name}</dd>
  {/if}
  <dt class="text-muted-foreground">Workflow</dt>
  <dd class="flex min-w-0 flex-col items-start">
    <span class="max-w-full truncate" title={p.workflow_path}>{p.workflow}</span>
    <a href={p.run_url} target="_blank" rel="noopener noreferrer" class={linkClass}>
      <span class="tabular-nums">Run {p.run_id}</span><ExternalLinkIcon aria-hidden="true" class="size-3.5 shrink-0" />
    </a>
    {#if attempt}<span class="text-meta text-muted-foreground">Attempt {p.run_attempt}</span>{/if}
  </dd>
  <dt class="text-muted-foreground">Repository</dt>
  <dd class="min-w-0">
    <a href={p.repository_url} target="_blank" rel="noopener noreferrer" class={linkClass}>
      <span class="truncate">{p.repository}</span><ExternalLinkIcon aria-hidden="true" class="size-3.5 shrink-0" />
    </a>
  </dd>
  {#if p.actor}
    <dt class="text-muted-foreground">Started by</dt>
    <dd class="truncate">{p.actor}</dd>
  {/if}
</dl>
