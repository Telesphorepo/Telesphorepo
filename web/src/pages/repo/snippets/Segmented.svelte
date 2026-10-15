<!--
  A choice of two or three short options as one segmented control (a radio group), with the look
  of the ViewSwitcher: the version mode of an artifact's snippets. Arrow keys move.

    <Segmented bind:value={mode} label="Version" options={[{ value: "pinned", label: "1.4.2" }, { value: "latest", label: "Always the newest" }]} />
-->
<script lang="ts" generics="V extends string">
  import { cn } from "$lib/utils";

  let {
    value = $bindable(),
    options,
    label,
  }: {
    value: V;
    options: { value: V; label: string; title?: string }[];
    /** Accessible name of the group. */
    label: string;
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

<div role="radiogroup" aria-label={label} class="flex h-9 w-full items-center gap-0.5 rounded-lg bg-muted p-0.5">
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
        "flex h-8 min-w-0 flex-1 items-center justify-center rounded-lg px-2.5 text-sm font-medium outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50",
        on ? "bg-background text-foreground shadow-sm ring-1 ring-border" : "text-muted-foreground hover:text-foreground",
      )}
    >
      <span class="truncate">{o.label}</span>
    </button>
  {/each}
</div>
