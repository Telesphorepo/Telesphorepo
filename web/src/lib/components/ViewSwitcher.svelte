<!--
  The choice of view for a list of files or versions: List · Tree · Columns · Cover Flow, as one
  segmented control. Offer only the views that carry meaning for the place (Cover Flow only where
  time is the axis: the versions and builds of one artifact). Every view renders in the same area,
  so a switch keeps the layout around it. Hold the value in a ViewChoice
  (lib/view.svelte.ts), which remembers it per user. Arrow keys move between the views; under
  640px the labels hide and the icons keep their names for screen readers.

    const view = new ViewChoice("versions", ["list", "coverflow"]);
    <ViewSwitcher bind:value={view.value} views={view.views} />
-->
<script lang="ts">
  import { viewLook, type ViewKind } from "$lib/view.svelte";
  import { cn } from "$lib/utils";

  let {
    value = $bindable(),
    views,
    label = "View",
  }: {
    value: ViewKind;
    views: ViewKind[];
    /** Accessible name of the control. */
    label?: string;
  } = $props();

  let buttons = $state<HTMLButtonElement[]>([]);

  function onkeydown(e: KeyboardEvent, i: number) {
    const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const next = (i + step + views.length) % views.length;
    value = views[next];
    buttons[next]?.focus();
  }
</script>

<div role="radiogroup" aria-label={label} class="inline-flex h-9 shrink-0 items-center gap-0.5 rounded-lg bg-muted p-0.5">
  {#each views as v, i (v)}
    {@const look = viewLook[v]}
    {@const on = v === value}
    <button
      bind:this={buttons[i]}
      type="button"
      role="radio"
      aria-checked={on}
      aria-label={look.label}
      title={look.label}
      tabindex={on ? 0 : -1}
      onclick={() => (value = v)}
      onkeydown={(e) => onkeydown(e, i)}
      class={cn(
        "flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-sm font-medium outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4 [&_svg]:shrink-0",
        on ? "bg-background text-foreground shadow-sm ring-1 ring-border" : "text-muted-foreground hover:text-foreground",
      )}
    >
      <look.icon />
      <span class="hidden sm:inline">{look.label}</span>
    </button>
  {/each}
</div>
