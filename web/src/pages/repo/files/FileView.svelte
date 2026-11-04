<!--
  One file, shown in the Files panel in place of the open folder's view (which stays mounted
  behind it, so Back returns to the same rows, open folders and scroll). The header holds the
  name, one line of facts and the actions people come for: Download and Copy link. Under it, two
  columns: what the file is (SHA-256, checksum files, the CI build) and how to use it (the
  format's part for the file, then the command line). A path that turns out to be a folder (typed
  without its "/") opens the folder instead.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, ApiError, type FileInfo, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { filesPath, href, navigate, repoPath } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import CopyValue from "$lib/components/CopyValue.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import BuildFacts from "./BuildFacts.svelte";
  import SnippetTabs from "../snippets/SnippetTabs.svelte";
  import { cliSnippet, downloadSnippets, downloadTools } from "../clients";
  import { formats } from "$lib/formats/registry.svelte";
  import { globMatch } from "$lib/glob";
  import { formatPart } from "../../../modules";
  import { baseName, sideLabel } from "../browse";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import LinkIcon from "@lucide/svelte/icons/link";
  import Trash2Icon from "@lucide/svelte/icons/trash-2";
  import FileIcon from "@lucide/svelte/icons/file";

  let {
    repo,
    path,
    base,
    back,
    backLabel,
    canWrite,
    onDelete,
  }: {
    repo: Repo;
    path: string;
    base: string;
    /** Where Back goes: the folder (with its version and build) the file was opened from. */
    back: () => void;
    backLabel: string;
    canWrite: boolean;
    onDelete: () => void;
  } = $props();

  // null: no file at this path.
  /* svelte-ignore state_referenced_locally */
  const file = new Resource<FileInfo | null>(async () => {
    try {
      return await api.repos.file(repo.name, path);
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
  const partName = $derived(formats.get(repo.format)?.file_details.find((d) => globMatch(d.match, path))?.part);
  const partLoader = $derived(partName ? formatPart(repo.format, partName) : undefined);
  const rel = $derived(api.repos.fileURL(repo.format, repo.name, path));
  const url = $derived(base + rel);
  const by = $derived(f?.build ? f.build.provider_title : f?.uploaded_by || "");
  const others = $derived(downloadSnippets(repo, url));

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(url);
      toast.success(repo.anonymous_read ? "Link copied." : "Link copied. It needs a sign-in or a read token to download.");
    } catch {
      toast.error("Copy failed. Select the address and copy it by hand.");
    }
  }
</script>

<div class="flex flex-col gap-6 px-3 pt-3 pb-4 sm:px-4">
  <div>
    <Button variant="ghost" size="sm" class="-ml-2 text-muted-foreground" onclick={back}><ArrowLeftIcon />{backLabel}</Button>
  </div>

  <header class="flex flex-col gap-4 md:flex-row md:items-start">
    <div class="flex min-w-0 flex-1 items-start gap-3">
      <FileIcon aria-hidden="true" class="mt-1 size-6 shrink-0 text-muted-foreground" />
      <div class="flex min-w-0 flex-col gap-1">
        <h2 class="text-section font-semibold break-all">{baseName(path)}</h2>
        {#if f}
          <p class="flex flex-wrap gap-x-1.5 text-meta text-muted-foreground">
            <span class="tabular-nums"><FileSize bytes={f.size} /></span>
            <span aria-hidden="true">·</span>
            <span>Published <RelativeTime at={f.updated_at} />{#if by}{" "}by {by}{/if}</span>
            {#if f.repo}
              <span aria-hidden="true">·</span>
              <span>Served by <a class="text-link hover:underline" href={href(repoPath(f.repo))}>{f.repo}</a></span>
            {/if}
          </p>
        {/if}
      </div>
    </div>
    <div class="flex shrink-0 flex-wrap gap-2">
      <Button href={rel} download={baseName(path)}><DownloadIcon />Download</Button>
      <Button variant="outline" onclick={copyLink}><LinkIcon />Copy link</Button>
      {#if canWrite}
        <Button variant="outline" class="hover:text-destructive" aria-label="Delete {baseName(path)}" onclick={onDelete}><Trash2Icon /></Button>
      {/if}
    </div>
  </header>

  {#if file.loading && file.data === undefined}
    <Loading rows={4} />
  {:else if file.error && file.data === undefined}
    <ErrorState error={file.error} title="The file did not load" onRetry={file.reload} />
  {:else if !f}
    <p class="text-body text-muted-foreground">This file does not exist any more.</p>
  {:else}
    <div class="grid gap-8 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
      <div class="flex min-w-0 flex-col gap-6">
        <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
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
        {#if f.build}
          <section class="flex flex-col gap-3" aria-label="Build">
            <h3 class="text-section">Build</h3>
            <BuildFacts p={f.build} />
          </section>
        {/if}
      </div>

      <div class="flex min-w-0 flex-col gap-6">
        {#if partLoader}
          {#await partLoader() then mod}
            <mod.default {repo} file={f} />
          {/await}
        {/if}
        <section class="flex min-w-0 flex-col gap-3" aria-label="Download {f.name} from a terminal">
          <h3 class="text-section">From a terminal</h3>
          <CodeSnippet value={cliSnippet(url)} />
          <p class="text-meta text-muted-foreground">
            {#if repo.anonymous_read}
              Or any tool that downloads a link.
            {:else}
              The CLI signs in as you once (<code class="font-mono">telesphorepo login</code>), then downloads from private repositories.
              <a class="text-link hover:underline" href={href("/install")}>Install the CLI</a>
            {/if}
          </p>
          <details class="group">
            <summary class="cursor-pointer text-meta text-muted-foreground hover:text-foreground">curl or wget</summary>
            <div class="pt-3"><SnippetTabs snippets={others} tools={downloadTools} label="Tool" fill /></div>
          </details>
        </section>
      </div>
    </div>
  {/if}
</div>
