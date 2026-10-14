<!--
  The file panel: a side sheet over the folder list for one file of a repo. It shows size, time,
  checksums, a download, and the client snippet for the artifact. Closing it opens the folder.
-->
<script lang="ts">
  import { api, type Repo } from "$lib/api";
  import * as Sheet from "$lib/components/ui/sheet";
  import { Button } from "$lib/components/ui/button";
  import FileDetails from "./FileDetails.svelte";
  import type { DeleteTarget } from "./FolderList.svelte";

  let {
    open,
    onClose,
    repo,
    path,
    base,
    canDelete,
    onDelete,
  }: {
    open: boolean;
    onClose: () => void;
    repo: Repo;
    /** The open file; "" when the panel is closed. */
    path: string;
    /** The public base URL of the server, without a trailing slash. */
    base: string;
    canDelete: boolean;
    onDelete: (t: DeleteTarget) => void;
  } = $props();

  // Keep the last file while the sheet animates closed.
  let shown = $state("");
  $effect(() => {
    if (path) shown = path;
  });
  const fileName = $derived(shown.slice(shown.lastIndexOf("/") + 1));
</script>

<Sheet.Root bind:open={() => open, (v) => { if (!v) onClose(); }}>
  <Sheet.Content side="right" class="gap-0 overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
    <Sheet.Header class="border-b pr-12">
      <Sheet.Title class="font-mono text-base break-all">{fileName}</Sheet.Title>
      <Sheet.Description class="font-mono text-xs break-all">{repo.name}/{shown}</Sheet.Description>
    </Sheet.Header>
    {#if shown}
      {#key shown}
        <FileDetails {repo} path={shown} {base} />
      {/key}
    {/if}
    <Sheet.Footer class="mt-auto flex-row flex-wrap border-t">
      <Button href={shown ? api.repos.fileURL(repo.format, repo.name, shown) : undefined} download={fileName}>
        Download
      </Button>
      {#if canDelete}
        <Button variant="destructive" onclick={() => onDelete({ path: shown, dir: false })}>Delete file</Button>
      {/if}
    </Sheet.Footer>
  </Sheet.Content>
</Sheet.Root>
