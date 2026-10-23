<!--
  What an upload stored: each file with created / replaced / unchanged. The files that the server
  wrote besides them (generated: checksums, metadata) are counted in one line.
-->
<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import FileSize from "$lib/components/FileSize.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import type { Uploaded } from "$lib/api";

  let { files }: { files: Uploaded[] } = $props();

  const main = $derived(files.filter((f) => !f.generated));
  const added = $derived(files.length - main.length);

  const label: Record<Uploaded["status"], string> = { created: "New", replaced: "Replaced", unchanged: "Unchanged" };
</script>

{#if !files.length}
  <EmptyState title="Nothing was stored" />
{:else}
  <ul class="flex max-h-72 flex-col divide-y overflow-y-auto rounded-lg border">
    {#each main as f (f.path)}
      <li class="flex items-center gap-3 px-3 py-2 text-sm">
        <span class="min-w-0 flex-1 truncate" title={f.path}>{f.path}</span>
        <span class="text-xs text-muted-foreground"><FileSize bytes={f.size} /></span>
        <Badge variant={f.status === "unchanged" ? "outline" : "secondary"}>{label[f.status]}</Badge>
      </li>
    {/each}
  </ul>
  {#if added}
    <p class="text-sm text-muted-foreground">
      The server also wrote {added.toLocaleString()} checksum and metadata {added === 1 ? "file" : "files"}.
    </p>
  {/if}
{/if}
