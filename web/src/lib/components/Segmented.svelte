<!--
  A choice of two or three short options as one segmented control (a radio group), with the look
  of the ViewSwitcher: the version mode of an artifact's snippets, the build tool of a CI file.
  Arrow keys move. It fills its cell; `compact` makes it as wide as its labels and 32px tall, for
  the header of a code block.

    <Segmented bind:value={mode} label="Version" options={[{ value: "pinned", label: "1.4.2" }, { value: "latest", label: "Always the newest" }]} />
-->
<script lang="ts" generics="V extends string">
  import { cn } from "$lib/utils";

  let {
    value = $bindable(),
    options,
    label,
    compact = false,
  }: {
    value: V;
    options: { value: V; label: string; title?: string }[];
    /** Accessible name of the group. */
    label: string;
    /** As wide as the labels and 32px tall. */
    compact?: boolean;
  } = $props();

  let buttons = $state<HTMLButtonElement[]>([]);

  function onkeydown(e: KeyboardEvent, i: number) {
    const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const next = (i + step + options.length) % options.length;
    value = options[next].value;
    buttons[next]?.focus();
  }
</script>

<div role="radiogroup" aria-label={label} class={cn("flex items-center gap-0.5 rounded-lg bg-muted p-0.5", compact ? "h-8 w-fit max-w-full" : "h-9 w-full")}>
  {#each options as o, i (o.value)}
    {@const on = o.value === value}
    <button
      bind:this={buttons[i]}
      type="button"
      role="radio"
      aria-checked={on}
      title={o.title}
      tabindex={on ? 0 : -1}
      onclick={() => (value = o.value)}
      onkeydown={(e) => onkeydown(e, i)}
      class={cn(
        "flex min-w-0 items-center justify-center rounded-lg font-medium outline-none transition-[color,background-color,box-shadow] focus-visible:ring-[3px] focus-visible:ring-ring/50",
        compact ? "h-7 px-2.5 text-sm" : "h-8 flex-1 px-2.5 text-sm",
        on ? "bg-background text-foreground shadow-sm ring-1 ring-border" : "text-muted-foreground hover:text-foreground",
      )}
    >
      <span class="truncate">{o.label}</span>
    </button>
  {/each}
</div>
