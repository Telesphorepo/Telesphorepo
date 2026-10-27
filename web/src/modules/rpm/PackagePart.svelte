<!--
  The RPM part of the details of a package file: what its index entry says (the summary,
  description, home page, license, dependencies, packager, build date and installed size), then
  how to install it (the format's artifact snippets).
-->
<script lang="ts">
  import { Resource } from "$lib/load.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ArtifactSnippets from "../../pages/repo/snippets/ArtifactSnippets.svelte";
  import type { FilePartProps } from "../types";
  import { rpmApi } from "./api";

  let { repo, file }: FilePartProps = $props();

  /* svelte-ignore state_referenced_locally */
  const pkg = new Resource(() => rpmApi.package(repo.name, file.path));
  const p = $derived(pkg.data);
  const deps = (k: string) => p?.dependencies[k] ?? [];
  const built = $derived(p && p.build_time > 0 ? new Date(p.build_time * 1000).toLocaleString() : "");
  const kinds: [string, string][] = [
    ["requires", "Requires"],
    ["recommends", "Recommends"],
    ["suggests", "Suggests"],
    ["conflicts", "Conflicts"],
    ["obsoletes", "Obsoletes"],
  ];
</script>

<section class="flex min-w-0 flex-col gap-3" aria-label="Package">
  <h3 class="text-section">Package</h3>
  {#if pkg.loading && !p}
    <Loading rows={4} />
  {:else if pkg.error && !p}
    <ErrorState error={pkg.error} title="The package did not load" onRetry={pkg.reload} />
  {:else if p}
    <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
      <dt class="text-muted-foreground">Name</dt>
      <dd class="break-all">{p.name}</dd>
      <dt class="text-muted-foreground">Version</dt>
      <dd class="break-all">{p.version}</dd>
      <dt class="text-muted-foreground">Architecture</dt>
      <dd>{p.architecture}</dd>
      {#if p.summary}
        <dt class="text-muted-foreground">Summary</dt>
        <dd class="break-words">{p.summary}</dd>
      {/if}
      {#if p.fields.description && p.fields.description !== p.summary}
        <dt class="text-muted-foreground">Description</dt>
        <dd class="break-words whitespace-pre-line">{p.fields.description}</dd>
      {/if}
      {#if p.fields.url}
        <dt class="text-muted-foreground">Home page</dt>
        <dd class="break-all">{p.fields.url}</dd>
      {/if}
      {#if p.fields.license}
        <dt class="text-muted-foreground">License</dt>
        <dd class="break-words">{p.fields.license}</dd>
      {/if}
      {#each kinds as [k, label] (k)}
        {#if deps(k).length}
          <dt class="text-muted-foreground">{label}</dt>
          <dd class="break-words">{deps(k).join(", ")}</dd>
        {/if}
      {/each}
      {#if p.fields.packager}
        <dt class="text-muted-foreground">Packager</dt>
        <dd class="break-words">{p.fields.packager}</dd>
      {/if}
      {#if built}
        <dt class="text-muted-foreground">Built</dt>
        <dd>{built}</dd>
      {/if}
      {#if p.installed_size > 0}
        <dt class="text-muted-foreground">Installed</dt>
        <dd><FileSize bytes={p.installed_size} /></dd>
      {/if}
    </dl>
  {/if}
</section>

<ArtifactSnippets {repo} path={file.path} />