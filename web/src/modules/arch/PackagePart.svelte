<!--
  The Arch part of the details of a package file in the pool: what its index entry says (the
  description, home page, licenses, dependencies, packager, build date and installed size), then
  how to install it (the format's artifact snippets).
-->
<script lang="ts">
  import { Resource } from "$lib/load.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ArtifactSnippets from "../../pages/repo/snippets/ArtifactSnippets.svelte";
  import type { FilePartProps } from "../types";
  import { archApi } from "./api";

  let { repo, file }: FilePartProps = $props();

  /* svelte-ignore state_referenced_locally */
  const pkg = new Resource(() => archApi.package(repo.name, file.path));
  const p = $derived(pkg.data);
  const one = (k: string) => p?.fields[k]?.[0] ?? "";
  const list = (k: string) => p?.fields[k] ?? [];
  const built = $derived.by(() => {
    const s = Number(one("BUILDDATE"));
    return s > 0 ? new Date(s * 1000).toLocaleString() : "";
  });
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
      {#if one("DESC")}
        <dt class="text-muted-foreground">Description</dt>
        <dd class="break-words">{one("DESC")}</dd>
      {/if}
      {#if one("URL")}
        <dt class="text-muted-foreground">Home page</dt>
        <dd class="break-all">{one("URL")}</dd>
      {/if}
      {#if list("LICENSE").length}
        <dt class="text-muted-foreground">Licenses</dt>
        <dd class="break-words">{list("LICENSE").join(", ")}</dd>
      {/if}
      {#if list("DEPENDS").length}
        <dt class="text-muted-foreground">Depends on</dt>
        <dd class="break-words">{list("DEPENDS").join(", ")}</dd>
      {/if}
      {#if list("OPTDEPENDS").length}
        <dt class="text-muted-foreground">Optional</dt>
        <dd class="break-words">{list("OPTDEPENDS").join(", ")}</dd>
      {/if}
      {#if one("PACKAGER")}
        <dt class="text-muted-foreground">Packager</dt>
        <dd class="break-words">{one("PACKAGER")}</dd>
      {/if}
      {#if built}
        <dt class="text-muted-foreground">Built</dt>
        <dd>{built}</dd>
      {/if}
      {#if Number(one("ISIZE")) > 0}
        <dt class="text-muted-foreground">Installed</dt>
        <dd><FileSize bytes={Number(one("ISIZE"))} /></dd>
      {/if}
    </dl>
  {/if}
</section>

<ArtifactSnippets {repo} path={file.path} />
