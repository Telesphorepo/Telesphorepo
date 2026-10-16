<!--
  Where the user drops files or a folder, or chooses them. Dropped or chosen files go to onFiles;
  a dropped folder goes to onFolderDrop (read its entries with filesFromEntries), a chosen folder
  to onFolderInput (read it with filesFromInput). `files` and `folders` say what the format takes
  (the upload of its descriptor).
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import FolderUpIcon from "@lucide/svelte/icons/folder-up";
  import FileUpIcon from "@lucide/svelte/icons/file-up";
  import { dropEntries, dropHasFolder, type FolderDrop } from "./folder";

  let {
    onFiles,
    onFolderDrop,
    onFolderInput,
    accept,
    hint,
    files = true,
    folders = true,
    disabled = false,
  }: {
    onFiles: (files: File[]) => void;
    onFolderDrop: (drop: FolderDrop) => void;
    onFolderInput: (list: FileList) => void;
    /** The `accept` of the file input, e.g. ".deb". */
    accept?: string;
    /** What to drop, e.g. "A pom and its jars, or a Maven repository folder". */
    hint: string;
    /** The zone takes chosen and dropped files. */
    files?: boolean;
    /** The zone takes a chosen or dropped folder. */
    folders?: boolean;
    disabled?: boolean;
  } = $props();

  let fileInput = $state<HTMLInputElement>();
  let folderInput = $state<HTMLInputElement>();
  let over = $state(false);

  function drop(e: DragEvent) {
    e.preventDefault();
    over = false;
    if (disabled || !e.dataTransfer) return;
    // Take the entries now: the browser empties the DataTransfer after this handler.
    if (dropHasFolder(e.dataTransfer)) {
      if (folders) onFolderDrop(dropEntries(e.dataTransfer));
      return;
    }
    const list = [...e.dataTransfer.files];
    if (list.length && files) onFiles(list);
  }

  function pickedFiles() {
    const list = [...(fileInput?.files ?? [])];
    if (fileInput) fileInput.value = "";
    if (list.length) onFiles(list);
  }

  function pickedFolder() {
    if (folderInput?.files?.length) onFolderInput(folderInput.files);
    if (folderInput) folderInput.value = "";
  }
</script>

<div
  role="presentation"
  class="flex flex-col items-center gap-4 rounded-lg border border-dashed px-6 py-12 text-center transition-colors {over
    ? 'border-primary bg-selected'
    : 'bg-panel'}"
  ondragenter={(e) => {
    e.preventDefault();
    if (!disabled) over = true;
  }}
  ondragover={(e) => e.preventDefault()}
  ondragleave={(e) => {
    if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node | null)) over = false;
  }}
  ondrop={drop}
>
  <UploadIcon aria-hidden="true" class="size-6 text-muted-foreground" />
  <div class="text-sm">
    <p class="font-medium">{files && folders ? "Drop files or a folder here" : folders ? "Drop a folder here" : "Drop files here"}</p>
    <p class="mt-1 text-muted-foreground">{hint}</p>
  </div>
  <div class="flex flex-wrap justify-center gap-2">
    {#if files}<Button variant="outline" {disabled} onclick={() => fileInput?.click()}><FileUpIcon />Choose files</Button>{/if}
    {#if folders}<Button variant="outline" {disabled} onclick={() => folderInput?.click()}><FolderUpIcon />Choose a folder</Button>{/if}
  </div>
  <input bind:this={fileInput} type="file" multiple {accept} class="sr-only" tabindex="-1" aria-hidden="true" onchange={pickedFiles} />
  <input bind:this={folderInput} type="file" webkitdirectory multiple class="sr-only" tabindex="-1" aria-hidden="true" onchange={pickedFolder} />
</div>
