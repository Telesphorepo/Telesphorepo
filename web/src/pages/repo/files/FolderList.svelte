<!--
  List view of a plain folder: folders first, then files, one dense line each (name, size, when and
  by whom it was published). Checksum and signature files are not rows: they are attributes of
  their file, shown in its details. A click on a row opens the folder or shows the file's details.
  Pages load by key ("Load more"). The parent keys it by folder. People who can delete pick rows
  (files and folders mix) and delete them together; the parent holds the pick and shows its
  summary in the details column.

  In a group the list is the merged folder of its members: a file names the member that serves
  it, muted after its name, and its row opens the file in that member.
-->
<script lang="ts" module>
  /**
   * A file, or a folder (path ends with "/"), to delete. A version without a folder of its own
   * (a Debian package keeps every version in one folder) is its files: `files` lists them, `path`
   * is the artifact folder and `version` names the version.
   */
  export type DeleteTarget = { path: string; dir: boolean; files?: string[]; version?: string };
</script>

<script lang="ts">
  import { api, type Repo } from "$lib/api";
  import { Keyset, type TableSource } from "$lib/load.svelte";
  import { filesPath, href, repoPath } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import DataTable, { type Column } from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import Truncate from "$lib/components/Truncate.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import BuildInline from "./BuildInline.svelte";
  import type { TreeEntry as TreeNode } from "$lib/api";
  import { sideOf } from "../browse";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import FileIcon from "@lucide/svelte/icons/file";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import UploadIcon from "@lucide/svelte/icons/upload";

  let {
    repo,
    folder,
    selected,
    canWrite,
    canUpload,
    compact,
    picked = $bindable(new Set<string>()),
    onPicked,
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
    /** The paths of the rows picked for a bulk delete. */
    picked?: Set<string>;
    /** Gets the picked rows that are loaded, whenever the pick or the rows change. */
    onPicked: (rows: TreeNode[]) => void;
    /** Deletes the picked files and folders (after a confirm). */
    onDelete: (targets: DeleteTarget[]) => void;
  } = $props();

  /* svelte-ignore state_referenced_locally */
  const list = new Keyset<TreeNode>((p) => api.repos.tree(repo.name, folder, p), 100);

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

  const pickedRows = $derived(rows.filter((e) => picked.has(e.path)));
  $effect(() => {
    onPicked(pickedRows);
    return () => onPicked([]);
  });

  const label = (e: TreeNode) => e.name.replace(/\/$/, "");
  // A group's file opens in the member that serves it; a folder stays in the group.
  const open = (e: TreeNode) => href(filesPath(e.type === "file" && e.repo ? e.repo : repo.name, e.path));
  const group = $derived(repo.kind === "group");
  const columns = $derived<Column[]>([
    { label: "Name", grow: true },
    { label: "Size", align: "right" },
    { label: "Published", align: "right", hideOnPhone: true },
    ...(compact ? [] : [{ label: "Published by", hideOnPhone: true }]),
  ]);
</script>

{#snippet bulkActions()}
  <Button
    variant="ghost"
    size="sm"
    class="hover:bg-destructive/10 hover:text-destructive"
    onclick={() => onDelete(pickedRows.map((e) => ({ path: e.path, dir: e.type === "dir" })))}>Delete</Button
  >
{/snippet}

<DataTable
  {source}
  {columns}
  caption="Files in {folder || 'the top folder'} of {repo.name}"
  key={(e) => e.path}
  href={open}
  selected={(e) => e.type === "file" && e.path === selected}
  bind:picked
  pickLabel={label}
  bulk={canWrite ? bulkActions : undefined}
>
  {#snippet row(e)}
    <Cell grow>
      <a
        href={open(e)}
        aria-current={e.type === "file" && e.path === selected ? "true" : undefined}
        class="flex min-w-0 items-center gap-2 rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 {e.type === 'dir' ? 'font-medium' : ''}"
      >
        {#if e.type === "dir"}
          <FolderIcon aria-hidden="true" class="size-4 shrink-0 text-link" />
        {:else}
          <FileIcon aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" />
        {/if}
        <Truncate text={label(e)} />
        {#if group && e.type === "file" && e.repo}
          <span class="flex h-5 max-w-40 shrink-0 items-center rounded-sm bg-muted px-1.5 text-meta text-muted-foreground" title="Served by {e.repo}">
            <span class="sr-only">from</span><span class="truncate">{e.repo}</span>
          </span>
        {/if}
      </a>
    </Cell>
    <Cell num>{#if e.size !== undefined}<FileSize bytes={e.size} />{/if}</Cell>
    <Cell num muted hideOnPhone>{#if e.type === "file"}<RelativeTime at={e.updated_at} empty="" />{/if}</Cell>
    {#if !compact}
      <Cell hideOnPhone muted class="max-w-56">
        {#if e.build}<BuildInline p={e.build} />{:else if e.uploaded_by}<Truncate text={e.uploaded_by} />{/if}
      </Cell>
    {/if}
  {/snippet}
  {#snippet empty()}
    {#if folder === ""}
      <CompactEmpty
        icon={FolderOpenIcon}
        title="Files"
        state="None yet"
        text={repo.kind === "proxy"
          ? "The proxy keeps a file here after a build asks for it the first time."
          : group
            ? "The files of its repositories appear here, once one of them has files you can download."
            : canUpload
            ? "Publish with your build tool, or upload files here."
            : "Files appear here when someone publishes to this repository."}
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
