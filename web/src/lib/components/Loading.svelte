<!--
  The one loading state (principle 11). It shows nothing for the first 300 ms, so a fast answer
  never flashes. After that it shows a quiet spinner and "Loading" in the space the content will
  take: `rows` reserves that many table rows (40px each), so nothing jumps when the content comes.
  `inline` is one line in a form, a dialog or a row.

    {#if repo.loading && !repo.data}<Loading rows={5} />{:else if repo.error}...{/if}

  Keep the previous content on screen while it reloads: test `loading && !data`, never `loading`
  alone. Never draw skeletons.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import { Spinner } from "$lib/components/ui/spinner";
  import { cn } from "$lib/utils";

  let {
    rows = 4,
    inline = false,
    label = "Loading",
    class: className,
  }: {
    /** The height to reserve, in table rows. */
    rows?: number;
    /** One line, at the start of the column. */
    inline?: boolean;
    label?: string;
    class?: string;
  } = $props();

  let shown = $state(false);
  onMount(() => {
    const t = setTimeout(() => (shown = true), 300);
    return () => clearTimeout(t);
  });
</script>

<div
  role="status"
  class={cn("flex items-center", inline ? "min-h-9" : "justify-center", className)}
  style={inline ? undefined : `min-height: calc(${rows} * var(--spacing-row))`}
>
  {#if shown}
    <span class="flex items-center gap-2 text-meta text-muted-foreground animate-in fade-in duration-200">
      <Spinner class="size-4" />{label}
    </span>
  {:else}
    <span class="sr-only">{label}</span>
  {/if}
</div>
