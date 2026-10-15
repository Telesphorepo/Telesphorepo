<!--
  The body of the details column for one file: its facts (size, when and by whom it was published,
  the SHA-256 and its checksum files as attributes), the GitHub Actions build that published it,
  and how to use it: the format plugin's snippets of a Maven artifact or a .deb (the apt setup and
  the install pinned to the file's version), and a curl and wget download of any other file and of a .deb. The parent keys it by path. A path that turns out to be a folder
  (typed without its "/") opens the folder instead.
-->
<script lang="ts">
  import { api, ApiError, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { filesPath, navigate } from "$lib/router.svelte";
  import CopyValue from "$lib/components/CopyValue.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ProvenanceFacts from "./ProvenanceFacts.svelte";
  import ArtifactSnippets from "../snippets/ArtifactSnippets.svelte";
  import SnippetTabs from "../snippets/SnippetTabs.svelte";
  import { downloadSnippets } from "../clients";
  import { browse, sideLabel, type FileInfo } from "../browse";

  let { repo, path, base }: { repo: Repo; path: string; base: string } = $props();

  // null: no file at this path.
  /* svelte-ignore state_referenced_locally */
  const file = new Resource<FileInfo | null>(async () => {
    try {
      return await browse.file(repo.name, path);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  });
  // Its checksum and signature files (path.sha1, ...) sort right after it.
  /* svelte-ignore state_referenced_locally */
  const near = new Resource(() => api.repos.assets(repo.name, path, { limit: 12 }));
  const sides = $derived(
    (near.data?.items ?? []).filter((a) => /^\.(md5|sha1|sha256|sha512|asc|sig)$/.test(a.path.slice(path.length))),
  );

  // A folder typed without its slash opens the folder.
  $effect(() => {
    if (file.data === null && (near.data?.items ?? []).some((a) => a.path.startsWith(path + "/"))) {
      navigate(filesPath(repo.name, path + "/"), { replace: true });
    }
  });

  const f = $derived(file.data);
  const deb = $derived(repo.format === "debian" && path.endsWith(".deb"));
  const url = $derived(base + api.repos.fileURL(repo.format, repo.name, path));
  const download = $derived(downloadSnippets(repo, url));
  const by = $derived(f?.provenance ? "GitHub Actions" : f?.uploaded_by || "");
</script>

{#if file.loading && file.data === undefined}
  <Loading rows={5} />
{:else if file.error && file.data === undefined}
  <ErrorState error={file.error} title="The file did not load" onRetry={file.reload} />
{:else if !f}
  <p class="text-body text-muted-foreground">This file does not exist any more. Pick a file from the list.</p>
{:else}
  <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
    <dt class="text-muted-foreground">Size</dt>
    <dd class="tabular-nums"><FileSize bytes={f.size} /></dd>
    <dt class="text-muted-foreground">Published</dt>
    <dd><RelativeTime at={f.updated_at} /></dd>
    {#if by}
      <dt class="text-muted-foreground">Published by</dt>
      <dd class="truncate">{by}</dd>
    {/if}
    {#if f.content_type}
      <dt class="text-muted-foreground">Type</dt>
      <dd class="truncate" title={f.content_type}>{f.content_type}</dd>
    {/if}
    <dt class="text-muted-foreground">SHA-256</dt>
    <dd><CopyValue value={f.sha256} label="SHA-256" /></dd>
    {#if sides.length}
      <dt class="text-muted-foreground">Checksums</dt>
      <dd class="flex flex-wrap gap-x-3 gap-y-1">
        {#each sides as s (s.path)}
          <a
            href={api.repos.fileURL(repo.format, repo.name, s.path)}
            class="rounded-sm text-link underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >{sideLabel(s.path)}</a
          >
        {/each}
      </dd>
    {/if}
  </dl>

  {#if f.provenance}
    <section class="flex flex-col gap-3" aria-label="Build">
      <h3 class="text-section">Build</h3>
      <ProvenanceFacts p={f.provenance} />
    </section>
  {/if}

  {#if repo.format === "maven"}
    <ArtifactSnippets {repo} {path} />
  {:else}
    {#if deb}<ArtifactSnippets {repo} {path} />{/if}
    <section class="flex min-w-0 flex-col gap-4" aria-label="Download {f.name}">
      <h3 class="text-section">Download it</h3>
      <SnippetTabs snippets={download} label="Tool" fill />
    </section>
  {/if}
{/if}
