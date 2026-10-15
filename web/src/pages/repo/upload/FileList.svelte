<!--
  The files of an upload before it is sent: name, size, where the server stores it (when the
  browser can tell), a problem with the file, and a button to take it out.
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import FileSize from "$lib/components/FileSize.svelte";
  import XIcon from "@lucide/svelte/icons/x";
  import CircleAlertIcon from "@lucide/svelte/icons/circle-alert";
  import type { FileRow } from "./types";

  let {
    rows,
    onRemove,
    disabled = false,
  }: {
    rows: FileRow[];
    onRemove: (f: File) => void;
    disabled?: boolean;
  } = $props();
</script>

{#if rows.length}
  <ul class="flex max-h-64 flex-col divide-y overflow-y-auto rounded-lg border">
    {#each rows as r (r.file)}
      <li class="flex items-start gap-3 px-3 py-2 text-sm">
        <div class="min-w-0 flex-1">
          <p class="truncate" title={r.file.name}>{r.file.name}</p>
          {#if r.error}
            <p class="mt-0.5 flex items-center gap-2 text-xs text-destructive">
              <CircleAlertIcon class="size-3.5 shrink-0" aria-hidden="true" />
              {r.error}
            </p>
          {:else if r.target}
            <p class="mt-0.5 truncate text-xs text-muted-foreground" title={r.target}>
              Stored as {r.target}
            </p>
          {/if}
        </div>
        <span class="pt-0.5 text-xs text-muted-foreground"><FileSize bytes={r.file.size} /></span>
        <Button
          variant="ghost"
          size="icon-sm"
          class="-my-1"
          {disabled}
          aria-label="Remove {r.file.name}"
          onclick={() => onRemove(r.file)}
        >
          <XIcon />
        </Button>
      </li>
    {/each}
  </ul>
{/if}
