<!--
  Where the user drops files or chooses them. onFiles gets the new files; the parent keeps the list.
  A drop that holds a folder goes to onFolder instead, with nothing added.
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import { dropEntries, dropHasFolder, type FolderDrop } from "./folder";

  let {
    onFiles,
    onFolder,
    accept,
    hint,
    disabled = false,
  }: {
    onFiles: (files: File[]) => void;
    /** Gets a dropped folder (read its entries with filesFromEntries). */
    onFolder: (drop: FolderDrop) => void;
    /** The `accept` of the file input, e.g. ".deb". */
    accept?: string;
    /** What to drop, e.g. "A pom and its jars". */
    hint: string;
    disabled?: boolean;
  } = $props();

  let input = $state<HTMLInputElement>();
  let over = $state(false);

  function drop(e: DragEvent) {
    e.preventDefault();
    over = false;
    if (disabled || !e.dataTransfer) return;
    // A folder shows up as a broken file in dataTransfer.files, so it never goes to onFiles.
    if (dropHasFolder(e.dataTransfer)) {
      onFolder(dropEntries(e.dataTransfer));
      return;
    }
    const list = [...e.dataTransfer.files];
    if (list.length) onFiles(list);
  }

  function picked() {
    const list = [...(input?.files ?? [])];
    if (input) input.value = "";
    if (list.length) onFiles(list);
  }
</script>

<div
  role="presentation"
  class="flex flex-col items-center gap-3 rounded-2xl border border-dashed px-4 py-8 text-center transition-colors {over
    ? 'border-primary bg-primary/10'
    : 'bg-muted/30'}"
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
  <UploadIcon class="size-6 text-muted-foreground" aria-hidden="true" />
  <div class="text-sm">
    <p class="font-medium">Drop files here</p>
    <p class="text-muted-foreground">{hint}</p>
  </div>
  <Button variant="outline" size="sm" {disabled} onclick={() => input?.click()}>Choose files</Button>
  <input bind:this={input} type="file" multiple {accept} class="sr-only" tabindex="-1" aria-hidden="true" onchange={picked} />
</div>
