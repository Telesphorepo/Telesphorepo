<!--
  Where the user drops a whole folder or chooses one. onRead gets the files that `keep` takes,
  with their paths inside the folder, at most `limit` of them (`more` says the folder holds more,
  `skipped` counts the files that `keep` left out). `initial` is a folder dropped elsewhere (on
  "Upload files"); the zone reads it at once.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import FolderUpIcon from "@lucide/svelte/icons/folder-up";
  import { dropEntries, filesFromEntries, filesFromInput, type FolderDrop, type FolderRead, type Keep } from "./folder";

  let {
    onRead,
    limit,
    hint,
    keep = undefined,
    initial = null,
    disabled = false,
  }: {
    onRead: (read: FolderRead) => void;
    limit: number;
    /** What to drop, e.g. "A Maven repository folder or a build output folder". */
    hint: string;
    /** The files worth sending (see folderKeep); undefined sends every file. */
    keep?: Keep;
    /** A folder that the user already dropped, read when the zone appears. */
    initial?: FolderDrop | null;
    disabled?: boolean;
  } = $props();

  let input = $state<HTMLInputElement>();
  let over = $state(false);
  let reading = $state(false);
  let error = $state("");

  /** The answer for a read that holds no file to send. */
  const emptyText = (read: FolderRead) =>
    read.skipped
      ? `None of the ${read.skipped.toLocaleString()} files in the folder can go to this repository.`
      : "The folder is empty.";

  async function readDrop(d: FolderDrop) {
    reading = true;
    error = "";
    try {
      const read = await filesFromEntries(d, limit, keep);
      if (read.files.length) onRead(read);
      else error = emptyText(read);
    } catch {
      error = "The browser could not read the folder. Choose it with the button instead.";
    } finally {
      reading = false;
    }
  }

  function drop(e: DragEvent) {
    e.preventDefault();
    over = false;
    if (disabled || reading || !e.dataTransfer) return;
    // Take the entries now: the browser empties the DataTransfer after this handler.
    readDrop(dropEntries(e.dataTransfer));
  }

  onMount(() => {
    if (initial) readDrop(initial);
  });

  function picked() {
    if (!input?.files) return;
    const read = filesFromInput(input.files, limit, keep);
    input.value = "";
    if (read.files.length) onRead(read);
    else error = emptyText(read);
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
  {#if reading}
    <p class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner /> Reading the folder…</p>
  {:else}
    <FolderUpIcon class="size-6 text-muted-foreground" aria-hidden="true" />
    <div class="text-sm">
      <p class="font-medium">Drop a folder here</p>
      <p class="text-muted-foreground">{hint}</p>
    </div>
    <Button variant="outline" size="sm" {disabled} onclick={() => input?.click()}>Choose a folder</Button>
    {#if error}<p class="text-sm text-destructive" role="alert">{error}</p>{/if}
  {/if}
  <input bind:this={input} type="file" webkitdirectory multiple class="sr-only" tabindex="-1" aria-hidden="true" onchange={picked} />
</div>
