<!--
  The Docker part of the details of a tag or a manifest: the manifest's digest, kind and sizes, its
  platforms (an index) or its config and layers (an image), its subject, its tags and how many
  manifests refer to it, then how to pull it (the format's artifact snippets).
-->
<script lang="ts">
  import { Resource } from "$lib/load.svelte";
  import CopyValue from "$lib/components/CopyValue.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ArtifactSnippets from "../../pages/repo/snippets/ArtifactSnippets.svelte";
  import type { FilePartProps } from "../types";
  import { dockerApi } from "./api";

  let { repo, file }: FilePartProps = $props();

  /* svelte-ignore state_referenced_locally */
  const manifest = new Resource(() => dockerApi.manifest(repo.name, file.path));
  const m = $derived(manifest.data);
  const short = (digest: string) => digest.replace(/^sha256:/, "").slice(0, 12);
  const platform = (p: { os: string; architecture: string; variant?: string }) =>
    p.os ? [p.os, p.architecture, p.variant].filter(Boolean).join("/") : "unknown/unknown";
</script>

<section class="flex min-w-0 flex-col gap-3" aria-label="Manifest">
  <h3 class="text-section">Manifest</h3>
  {#if manifest.loading && !m}
    <Loading rows={4} />
  {:else if manifest.error && !m}
    <ErrorState error={manifest.error} title="The manifest did not load" onRetry={manifest.reload} />
  {:else if m}
    <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
      <dt class="text-muted-foreground">Digest</dt>
      <dd><CopyValue value={m.digest} label="digest" /></dd>
      <dt class="text-muted-foreground">Kind</dt>
      <dd class="break-all">{m.media_type}</dd>
      {#if m.artifact_type}
        <dt class="text-muted-foreground">Artifact type</dt>
        <dd class="break-all">{m.artifact_type}</dd>
      {/if}
      <dt class="text-muted-foreground">Size</dt>
      <dd><FileSize bytes={m.total_size} /></dd>
      {#if m.tags.length}
        <dt class="text-muted-foreground">Tags</dt>
        <dd class="break-words">{m.tags.join(", ")}</dd>
      {/if}
      {#if m.subject}
        <dt class="text-muted-foreground">Refers to</dt>
        <dd><CopyValue value={m.subject.digest} label="subject digest" /></dd>
      {/if}
      {#if m.referrers > 0}
        <dt class="text-muted-foreground">Referrers</dt>
        <dd class="tabular-nums">{m.referrers}</dd>
      {/if}
    </dl>

    {#if m.platforms.length}
      <h4 class="text-body font-medium">Platforms</h4>
      <ul class="flex flex-col gap-1 text-body">
        {#each m.platforms as p (p.digest)}
          <li class="flex min-w-0 justify-between gap-3">
            <span class="truncate">{platform(p)}</span>
            <span class="shrink-0 text-muted-foreground tabular-nums" title={p.digest}>{short(p.digest)}</span>
          </li>
        {/each}
      </ul>
    {:else}
      <h4 class="text-body font-medium">Layers</h4>
      {#if m.layers.length === 0}
        <p class="text-body text-muted-foreground">This manifest has no layers.</p>
      {:else}
        <ul class="flex flex-col gap-1 text-body">
          {#each m.layers as l, i (l.digest + i)}
            <li class="flex min-w-0 justify-between gap-3">
              <span class="truncate text-muted-foreground tabular-nums" title="{l.digest} ({l.media_type})">{short(l.digest)}</span>
              <span class="shrink-0"><FileSize bytes={l.size} /></span>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  {/if}
</section>

<ArtifactSnippets {repo} path={file.path} />
