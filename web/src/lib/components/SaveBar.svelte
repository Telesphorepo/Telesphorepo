<!--
  The save bar of a settings page: it appears only while there are unsaved changes, and sticks to
  the bottom of the window over the content column: "2 unsaved changes · Discard · Save". Put it
  last on the page. While it shows, leaving the page asks first (lib/unsaved.svelte.ts), and
  Ctrl+S (⌘S) saves. onSave runs with a spinner; if it throws, the bar shows the error and keeps
  the edits.

    const draft = new Draft({ description: repo.description, anonymous_read: repo.anonymous_read });
    <SaveBar count={draft.count} onDiscard={draft.reset} onSave={save} />
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { fly } from "svelte/transition";
  import { cubicOut } from "svelte/easing";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import { errorMessage } from "$lib/api";
  import { unsaved } from "$lib/unsaved.svelte";
  import CircleAlertIcon from "@lucide/svelte/icons/circle-alert";

  let {
    count,
    onSave,
    onDiscard,
    saveLabel = "Save",
  }: {
    /** The number of unsaved changes; 0 hides the bar. */
    count: number;
    onSave: () => unknown | Promise<unknown>;
    onDiscard: () => void;
    /** Names the action when "Save" is not enough, e.g. "Save and move files". */
    saveLabel?: string;
  } = $props();

  let busy = $state(false);
  let error = $state("");

  // Register once; the count is read live through the function.
  $effect(() => untrack(() => unsaved.register({ count: () => count, discard: () => onDiscard() })));
  $effect(() => {
    if (count === 0) error = "";
  });

  async function save() {
    if (busy || count === 0) return;
    busy = true;
    error = "";
    try {
      await onSave();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  function discard() {
    error = "";
    onDiscard();
  }

  function keydown(e: KeyboardEvent) {
    if (count > 0 && (e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "s") {
      e.preventDefault();
      save();
    }
  }

  const reduce = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
</script>

<svelte:window onkeydown={keydown} />

{#if count > 0}
  <div class="sticky bottom-4 z-20 mt-10" transition:fly={{ y: reduce ? 0 : 12, duration: reduce ? 0 : 180, easing: cubicOut }}>
    <div
      role="region"
      aria-label="Unsaved changes"
      class="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg bg-popover py-2.5 pr-2.5 pl-4 shadow-lg ring-1 ring-border"
    >
      <p class="flex min-w-0 flex-1 items-center gap-2 text-label" aria-live="polite">
        <span aria-hidden="true" class="size-2 shrink-0 rounded-full bg-warning"></span>
        {count === 1 ? "1 unsaved change" : `${count} unsaved changes`}
      </p>
      {#if error}
        <p class="order-last flex min-w-0 basis-full items-start gap-2 text-meta text-destructive">
          <CircleAlertIcon class="mt-px size-4 shrink-0" />{error}
        </p>
      {/if}
      <div class="flex items-center gap-2">
        <Button variant="ghost" disabled={busy} onclick={discard}>Discard</Button>
        <Button disabled={busy} onclick={save}>
          {#if busy}<Spinner />{/if}
          {saveLabel}
        </Button>
      </div>
    </div>
  </div>
{/if}
