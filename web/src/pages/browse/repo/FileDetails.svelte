<!--
  The body of the file panel for one file: facts, checksums and client snippets. The parent keys
  it by path. A path that turns out to be a folder opens the folder instead.
-->
<script lang="ts">
  import { api, type Asset, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { encodePath, href, navigate } from "$lib/router.svelte";
  import CopyField from "$lib/components/CopyField.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FileXIcon from "@lucide/svelte/icons/file-x";
  import { aptSnippets, curlSnippet, debFile, mavenCoords, mavenSnippets, type ClientSnippet } from "./clients";

  let { repo, path, base }: { repo: Repo; path: string; base: string } = $props();

  const checksumExt = /\.(md5|sha1|sha256|sha512|asc)$/;

  // The file and its checksum and signature files (path.sha1, ...) sort next to it.
  const assets = new Resource(() => api.repos.assets(repo.name, path, { limit: 12 }));
  const items = $derived(assets.data?.items ?? []);
  const asset = $derived(items.find((a) => a.path === path));
  const sidecars = $derived(
    items.filter((a) => a.path.startsWith(path + ".") && checksumExt.test(a.path) && !a.path.slice(path.length).includes("/")),
  );

  // A folder path without its trailing "/" (typed by hand): open the folder.
  $effect(() => {
    if (assets.data && !asset && items.some((a) => a.path.startsWith(path + "/"))) {
      navigate("/repo/" + encodeURIComponent(repo.name) + "/" + encodePath(path + "/"), { replace: true });
    }
  });

  const deb = $derived(repo.format === "debian" ? debFile(path) : null);
  // The distributions of a Debian repo (folders under dists/), for the apt line.
  const dists = deb
    ? new Resource(async () => (await api.repos.tree(repo.name, "dists/", { limit: 20 })).items.filter((e) => e.type === "dir").map((e) => e.name.replace(/\/$/, "")))
    : null;

  const downloadURL = $derived(base + api.repos.fileURL(repo.format, repo.name, path));

  const snippets = $derived.by((): ClientSnippet[] => {
    if (repo.format === "maven") {
      const c = mavenCoords(path);
      return c ? mavenSnippets(c) : [curlSnippet(repo, downloadURL)];
    }
    if (deb && dists?.data) {
      return [...aptSnippets(repo, base, deb, dists.data[0] ?? "stable"), curlSnippet(repo, downloadURL)];
    }
    return [curlSnippet(repo, downloadURL)];
  });

  const sidecarLabel = (a: Asset) => a.path.slice(path.length + 1).toUpperCase();
</script>

<div class="flex flex-col gap-8 p-6">
  {#if assets.loading}
    <LoadingRows rows={4} />
  {:else if assets.error}
    <ErrorState error={assets.error} onRetry={assets.reload} />
  {:else if !asset}
    <EmptyState icon={FileXIcon} title="File not found" description="It was deleted, or the address is wrong. Pick a file from the list." />
  {:else}
    <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
      <dt class="text-muted-foreground">Size</dt>
      <dd><FileSize bytes={asset.size} /></dd>
      <dt class="text-muted-foreground">Modified</dt>
      <dd><RelativeTime at={asset.updated_at} /></dd>
      {#if asset.content_type}
        <dt class="text-muted-foreground">Type</dt>
        <dd class="font-mono text-[0.8125rem] break-all">{asset.content_type}</dd>
      {/if}
    </dl>

    <section class="flex flex-col gap-4">
      <h3 class="text-sm font-semibold">Checksums</h3>
      <CopyField label="SHA-256" value={asset.sha256} />
      {#if sidecars.length}
        <p class="text-sm text-muted-foreground">
          Checksum and signature files:
          {#each sidecars as s, i (s.path)}
            <a
              href={api.repos.fileURL(repo.format, repo.name, s.path)}
              class="font-mono text-[0.8125rem] text-link underline-offset-4 hover:underline">{sidecarLabel(s)}</a
            >{i < sidecars.length - 1 ? ", " : ""}
          {/each}
        </p>
      {/if}
    </section>

    <section class="flex flex-col gap-4">
      <h3 class="text-sm font-semibold">Client setup</h3>
      {#if dists?.loading}
        <LoadingRows rows={2} />
      {:else}
        {#if dists?.error}
          <ErrorState inline error="The distributions did not load ({dists.error}). The apt line is left out." />
        {/if}
        {#if deb && dists?.data && dists.data.length > 1}
          <p class="text-sm text-muted-foreground">
            This repository has the distributions {dists.data.join(", ")}. Replace {dists.data[0]} with the one that holds the package.
          </p>
        {/if}
        {#each snippets as s (s.title)}
          <CopyField label={s.title} value={s.code} block={s.code.includes("\n")} />
        {/each}
      {/if}
      {#if !repo.anonymous_read}
        <p class="text-sm text-muted-foreground">
          This repository is private. Downloads ask for your username and a
          <a href={href("/settings/tokens")} class="text-link underline-offset-4 hover:underline">read token</a>.
        </p>
      {/if}
    </section>
  {/if}
</div>
