<!--
  List view of a plain folder: folders first, then files, one dense line each (name, size, when and
  by whom it was published). Checksum and signature files are not rows: they are attributes of
  their file, shown in its details. A click on a row opens the folder or shows the file's details.
  Pages load by key ("Load more"). The parent keys it by folder.
-->
<script lang="ts" module>
  /** A file, or a folder (path ends with "/"), to delete. */
  export type DeleteTarget = { path: string; dir: boolean };
</script>

<script lang="ts">
  import type { Repo } from "$lib/api";
  import { Keyset, type TableSource } from "$lib/load.svelte";
  import { filesPath, href, repoPath } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import DataTable, { type Column } from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import Truncate from "$lib/components/Truncate.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ProvenanceInline from "./ProvenanceInline.svelte";
  import { browse, sideOf, type TreeNode } from "../browse";
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
    canUpload,
    compact,
    onDelete,
  }: {
    repo: Repo;
    /** "" (the top) or a path that ends with "/". */
    folder: string;
    /** The file whose details show; its row is filled. */
    selected: string;
    canWrite: boolean;
    canUpload: boolean;
    /** The details column is open: fewer columns. */
    compact: boolean;
    onDelete: (t: DeleteTarget) => void;
  } = $props();

  /* svelte-ignore state_referenced_locally */
  const list = new Keyset<TreeNode>((p) => browse.tree(repo.name, folder, p), 100);

  // Folders first; a checksum or signature whose file is listed is not a row of its own.
  const rows = $derived.by(() => {
    const paths = new Set(list.items.map((e) => e.path));
    const shown = list.items.filter((e) => {
      const of = e.type === "file" ? sideOf(e.path) : null;
      return !of || !paths.has(of);
    });
    return shown.sort((a, b) => (a.type === b.type ? 0 : a.type === "dir" ? -1 : 1));
  });
  const source: TableSource<TreeNode> = {
    get items() {
      return rows;
    },
    get loading() {
      return list.loading;
    },
    get error() {
      return list.error;
    },
    get hasMore() {
      return list.hasMore;
    },
    get loadingMore() {
      return list.loadingMore;
    },
    reload: list.reload,
    more: list.more,
  };

  const label = (e: TreeNode) => e.name.replace(/\/$/, "");
  const columns = $derived<Column[]>([
    { label: "Name", grow: true },
    { label: "Size", align: "right" },
    { label: "Published", align: "right", hideOnPhone: true },
    ...(compact ? [] : [{ label: "Published by", hideOnPhone: true }]),
    ...(canWrite ? [{ label: "", srLabel: "Actions" }] : []),
  ]);
</script>

<DataTable
  {source}
  {columns}
  caption="Files in {folder || 'the top folder'} of {repo.name}"
  key={(e) => e.path}
  href={(e) => href(filesPath(repo.name, e.path))}
  selected={(e) => e.type === "file" && e.path === selected}
>
  {#snippet row(e)}
    <Cell grow>
      <a
        href={href(filesPath(repo.name, e.path))}
        aria-current={e.type === "file" && e.path === selected ? "true" : undefined}
        class="flex min-w-0 items-center gap-2 rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 {e.type === 'dir' ? 'font-medium' : ''}"
      >
        {#if e.type === "dir"}
          <FolderIcon aria-hidden="true" class="size-4 shrink-0 text-link" />
        {:else}
          <FileIcon aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" />
        {/if}
        <Truncate text={label(e)} />
      </a>
    </Cell>
    <Cell num>{#if e.size !== undefined}<FileSize bytes={e.size} />{/if}</Cell>
    <Cell num muted hideOnPhone>{#if e.type === "file"}<RelativeTime at={e.updated_at} empty="" />{/if}</Cell>
    {#if !compact}
      <Cell hideOnPhone muted class="max-w-56">
        {#if e.provenance}<ProvenanceInline p={e.provenance} />{:else if e.uploaded_by}<Truncate text={e.uploaded_by} />{/if}
      </Cell>
    {/if}
    {#if canWrite}
      <Cell actions>
        <Button
          variant="ghost"
          size="icon-sm"
          class="text-muted-foreground hover:text-destructive"
          aria-label={e.type === "dir" ? `Delete the folder ${label(e)}` : `Delete ${label(e)}`}
          title={e.type === "dir" ? "Delete the folder" : "Delete the file"}
          onclick={() => onDelete({ path: e.path, dir: e.type === "dir" })}
        >
          <Trash2Icon />
        </Button>
      </Cell>
    {/if}
  {/snippet}
  {#snippet empty()}
    {#if folder === ""}
      <CompactEmpty
        icon={FolderOpenIcon}
        title="Files"
        state="None yet"
        text={canUpload ? "Publish with your build tool, or upload files here." : "Files appear here when someone publishes to this repository."}
      >
        {#if canUpload}<Button variant="outline" href={href(repoPath(repo.name, "upload"))}><UploadIcon />Upload</Button>{/if}
      </CompactEmpty>
    {:else}
      <CompactEmpty icon={FolderOpenIcon} title="This folder" state="Empty" text="Its files were deleted or moved.">
        <Button variant="outline" href={href(filesPath(repo.name))}>Open the top folder</Button>
      </CompactEmpty>
    {/if}
  {/snippet}
</DataTable>
