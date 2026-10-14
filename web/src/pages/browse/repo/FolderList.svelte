<!--
  The children of one folder of a repo, folders first, then files with size, modified time and
  uploader. Pages load by key ("Load more"). The parent keys it by folder, so a new folder starts
  a new list.
-->
<script lang="ts" module>
  /** A file, or a folder (path ends with "/"), to delete. */
  export type DeleteTarget = { path: string; dir: boolean };
</script>

<script lang="ts">
  import { api, type Repo, type TreeEntry } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { encodePath, href } from "$lib/router.svelte";
  import * as Table from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import LoadMore from "$lib/components/LoadMore.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import FileIcon from "@lucide/svelte/icons/file";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import Trash2Icon from "@lucide/svelte/icons/trash-2";
  import UploadIcon from "@lucide/svelte/icons/upload";

  let {
    repo,
    folder,
    selected,
    canWrite,
    onDelete,
    onUpload,
  }: {
    repo: Repo;
    /** "" (the root) or a path that ends with "/". */
    folder: string;
    /** The open file, highlighted in the list. */
    selected: string;
    canWrite: boolean;
    onDelete: (t: DeleteTarget) => void;
    /** Set when the visitor can upload: the empty state offers it. */
    onUpload?: () => void;
  } = $props();

  type Entry = TreeEntry;

  const list = new Keyset<Entry>((p) => api.repos.tree(repo.name, folder, p), 100);

  const pathOf = (e: Entry) => e.path;
  const label = (e: Entry) => e.name.replace(/\/$/, "");

  // The server sends path order; folders come first here, over every page loaded so far.
  const rows = $derived(
    [...list.items].sort((a, b) => (a.type === b.type ? 0 : a.type === "dir" ? -1 : 1)),
  );

  const columns = $derived(canWrite ? 5 : 4);
  const entryHref = (e: Entry) => href("/repo/" + encodeURIComponent(repo.name) + "/" + encodePath(pathOf(e)));
</script>

{#if list.error && list.items.length === 0 && !list.loading}
  <ErrorState error={list.error} onRetry={list.reload} />
{:else if !list.loading && list.items.length === 0}
  {#if folder === ""}
    <EmptyState
      icon={FolderOpenIcon}
      title="No files yet"
      description={onUpload
        ? "Publish with your build tool, or upload files here."
        : "Files appear here when someone publishes to this repository."}
    >
      {#if onUpload}
        <Button onclick={onUpload}><UploadIcon />Upload files</Button>
      {/if}
    </EmptyState>
  {:else}
    <EmptyState icon={FolderOpenIcon} title="This folder is empty" description="Its files were deleted or moved.">
      <Button variant="outline" href={href("/repo/" + encodeURIComponent(repo.name) + "/")}>Open the top folder</Button>
    </EmptyState>
  {/if}
{:else}
  <div class="w-full overflow-x-auto rounded-2xl border">
    <Table.Root>
      <Table.Caption class="sr-only">Files in {folder || "the top folder"} of {repo.name}</Table.Caption>
      <Table.Header>
        <Table.Row class="hover:bg-transparent">
          <Table.Head class="w-full">Name</Table.Head>
          <Table.Head class="text-right">Size</Table.Head>
          <Table.Head>Modified</Table.Head>
          <Table.Head>Uploaded by</Table.Head>
          {#if canWrite}<Table.Head><span class="sr-only">Actions</span></Table.Head>{/if}
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {#if list.loading}
          <LoadingRows rows={6} {columns} />
        {:else}
          {#each rows as e (pathOf(e))}
            {@const p = pathOf(e)}
            {@const current = e.type === "file" && p === selected}
            <Table.Row data-state={current ? "selected" : undefined}>
              <Table.Cell class="max-w-xl whitespace-normal">
                <a
                  href={entryHref(e)}
                  aria-current={current ? "page" : undefined}
                  class="flex items-start gap-2 font-mono text-[0.8125rem] break-all underline-offset-4 hover:underline {e.type === 'dir' ? 'font-medium' : ''}"
                >
                  {#if e.type === "dir"}
                    <FolderIcon class="mt-0.5 size-4 shrink-0 text-link" />
                  {:else}
                    <FileIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  {/if}
                  {label(e)}{e.type === "dir" ? "/" : ""}
                </a>
              </Table.Cell>
              <Table.Cell class="text-right">
                {#if e.size !== undefined}<FileSize bytes={e.size} />{/if}
              </Table.Cell>
              <Table.Cell class="text-muted-foreground">
                {#if e.type === "file"}<RelativeTime at={e.updated_at} empty="" />{/if}
              </Table.Cell>
              <Table.Cell class="text-muted-foreground">
                {#if e.uploaded_by}
                  <span class="font-mono text-[0.8125rem]">{e.uploaded_by}</span>
                {/if}
              </Table.Cell>
              {#if canWrite}
                <Table.Cell class="py-1">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={e.type === "dir" ? `Delete folder ${label(e)}` : `Delete ${label(e)}`}
                    title={e.type === "dir" ? "Delete folder" : "Delete file"}
                    onclick={() => onDelete({ path: p, dir: e.type === "dir" })}
                  >
                    <Trash2Icon />
                  </Button>
                </Table.Cell>
              {/if}
            </Table.Row>
          {/each}
        {/if}
      </Table.Body>
    </Table.Root>
  </div>
  <LoadMore {list} />
{/if}
