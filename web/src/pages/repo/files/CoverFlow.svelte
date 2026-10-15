<!--
  Cover Flow for the versions or the builds of one artifact: a horizontal timeline of felt tickets,
  oldest on the left, newest on the right. Each ticket is printed with its version (or build), its
  date and its commit; the chosen ticket faces the reader and the others turn away on either side.
  A band at the foot holds a timeline slider through the whole history and the caption: the
  chosen ticket's status, provenance and actions. The whole is one box of the same size as the
  other views of the Files tab, so a switch to Cover Flow moves nothing around it.

  Arrow keys, Home and End move; Enter opens the chosen ticket (onOpen); a click on a side ticket
  chooses it, a click on the chosen one opens it. Trackpads scroll sideways through it and a swipe
  moves on a phone. With prefers-reduced-motion the tickets lie flat and move without animation.
  Only the tickets near the chosen one are drawn: the felt is expensive to render.

    <CoverFlow tickets={...} selected={key} onSelect={(k) => (key = k)} onOpen={open} label="Versions of com.acme:lib">
      {#snippet caption(t)}...{/snippet}
    </CoverFlow>
-->
<script lang="ts" module>
  export type Ticket = {
    key: string;
    /** Printed above the T: the version or the build. */
    top: string;
    /** Printed under the T: the date. */
    bottom: string;
    /** Printed down the stub: the short commit, or the kind. */
    serial: string;
    /** The accessible name of the ticket. */
    label: string;
  };
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  import FeltTicket from "$lib/components/FeltTicket.svelte";
  import { Spinner } from "$lib/components/ui/spinner";
  import { cn } from "$lib/utils";
  import { viewBox } from "./area";

  let {
    tickets,
    selected,
    onSelect,
    onOpen,
    label,
    hasMore = false,
    loadingMore = false,
    more,
    caption,
  }: {
    /** Newest first, as the API lists them. */
    tickets: Ticket[];
    selected: string;
    onSelect: (key: string) => void;
    onOpen?: (key: string) => void;
    label: string;
    /** Older tickets exist beyond the loaded ones. */
    hasMore?: boolean;
    loadingMore?: boolean;
    more?: () => unknown;
    caption?: Snippet<[Ticket]>;
  } = $props();

  const uid = $props.id();
  // The timeline runs left to right in time.
  const line = $derived([...tickets].reverse());
  const at = $derived(Math.max(0, line.findIndex((t) => t.key === selected)));
  const chosen = $derived(line[at]);

  // Load older tickets before the reader reaches the oldest one.
  $effect(() => {
    if (at < 3 && hasMore && !loadingMore && more) more();
  });

  let reduced = $state(false);
  $effect(() => {
    const mq = matchMedia("(prefers-reduced-motion: reduce)");
    reduced = mq.matches;
    const on = () => (reduced = mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  });

  const WINDOW = 4;
  /** The place of a ticket `i` steps from the chosen one. */
  function place(i: number): string {
    if (i === 0) return "translate3d(0,0,0)";
    const s = Math.sign(i);
    const n = Math.abs(i);
    if (reduced) return `translate3d(${s * (196 + (n - 1) * 64)}px,0,0) scale(0.82)`;
    return `translate3d(${s * (178 + (n - 1) * 58)}px,0,-${150 + n * 24}px) rotateY(${-s * 58}deg)`;
  }

  /** How far a fan of n side tickets reaches from the chosen one. */
  const reach = (n: number) => (n === 0 ? 0 : reduced ? 196 + (n - 1) * 64 : 178 + (n - 1) * 58);
  // Shift the group so the visible tickets sit centered, also when the chosen one is at an end.
  const shift = $derived(
    (reach(Math.min(at, WINDOW)) - reach(Math.min(line.length - 1 - at, WINDOW))) / 2,
  );

  function go(i: number) {
    const t = line[Math.max(0, Math.min(line.length - 1, i))];
    if (t && t.key !== selected) onSelect(t.key);
  }

  function onkeydown(e: KeyboardEvent) {
    const moves: Record<string, () => void> = {
      ArrowLeft: () => go(at - 1),
      ArrowRight: () => go(at + 1),
      Home: () => go(0),
      End: () => go(line.length - 1),
      Enter: () => chosen && onOpen?.(chosen.key),
    };
    const m = moves[e.key];
    if (!m) return;
    e.preventDefault();
    m();
  }

  // Sideways scroll (trackpads, Shift+wheel) moves one ticket per step.
  let wheel = 0;
  function onwheel(e: WheelEvent) {
    const dx = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.shiftKey ? e.deltaY : 0;
    if (!dx) return;
    e.preventDefault();
    wheel += dx;
    if (Math.abs(wheel) >= 60) {
      go(at + Math.sign(wheel));
      wheel = 0;
    }
  }

  let downX: number | null = null;
  const onpointerdown = (e: PointerEvent) => (downX = e.pointerType === "mouse" ? null : e.clientX);
  function onpointerup(e: PointerEvent) {
    if (downX === null) return;
    const dx = e.clientX - downX;
    downX = null;
    if (Math.abs(dx) > 40) go(at - Math.sign(dx));
  }

  function onclick(i: number) {
    if (i === at) onOpen?.(line[i].key);
    else go(i);
  }
</script>

<div class={cn("flex min-w-0 flex-col overflow-hidden rounded-lg bg-panel ring-1 ring-border", viewBox)}>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    role="listbox"
    aria-label={label}
    aria-orientation="horizontal"
    aria-activedescendant={chosen ? `${uid}-${at}` : undefined}
    tabindex="0"
    {onkeydown}
    {onwheel}
    {onpointerdown}
    {onpointerup}
    class="flow relative min-h-0 flex-1 touch-pan-y overflow-hidden rounded-t-lg outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:ring-inset"
  >
    {#each line as t, i (t.key)}
      {@const d = i - at}
      {#if Math.abs(d) <= WINDOW}
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <div
          id="{uid}-{i}"
          role="option"
          aria-selected={d === 0}
          aria-label={t.label}
          tabindex="-1"
          onclick={() => onclick(i)}
          class={cn("ticket absolute top-1/2 left-1/2 w-80 cursor-pointer", d !== 0 && "side")}
          style="transform: translate(calc(-50% + {shift}px), -54%) {place(d)}; z-index: {20 - Math.abs(d)};"
        >
          <FeltTicket top={t.top} bottom={t.bottom} serial={t.serial} label={t.label} />
        </div>
      {/if}
    {/each}
    {#if loadingMore && at < 3}
      <div class="absolute top-3 left-3 flex items-center gap-2 text-meta text-muted-foreground"><Spinner />Older</div>
    {/if}
  </div>

  <!-- The timeline and the chosen ticket's caption: one fixed band at the foot of the box. -->
  <div class="flex shrink-0 flex-col gap-3 border-t bg-card px-4 py-3">
  {#if line.length > 1}
    <div class="flex items-center gap-3 text-meta text-muted-foreground">
      <span class="shrink-0 tabular-nums">{line[0].bottom}</span>
      <input
        type="range"
        min="0"
        max={line.length - 1}
        value={at}
        oninput={(e) => go(Number((e.currentTarget as HTMLInputElement).value))}
        aria-label="Timeline"
        aria-valuetext={chosen ? `${chosen.top}, ${chosen.bottom}` : undefined}
        style="--at: {(at / (line.length - 1)) * 100}%"
        class="timeline min-w-0 flex-1"
      />
      <span class="shrink-0 tabular-nums">{line[line.length - 1].bottom}</span>
    </div>
  {/if}

  {#if chosen && caption}{@render caption(chosen)}{/if}
  </div>
</div>

<style>
  .flow {
    perspective: 1100px;
    perspective-origin: 50% 45%;
  }
  .ticket {
    transition:
      transform 320ms cubic-bezier(0.2, 0.7, 0.25, 1),
      filter 320ms ease-out;
    will-change: transform;
    transform-style: preserve-3d;
  }
  .ticket.side {
    filter: brightness(0.82) saturate(0.85);
  }
  .ticket.side:hover {
    filter: brightness(0.92) saturate(0.95);
  }
  /* The timeline: a thin rail, gold up to the chosen ticket, and a gold dot. */
  .timeline {
    appearance: none;
    height: 1.25rem;
    background: transparent;
    cursor: pointer;
  }
  .timeline::-webkit-slider-runnable-track {
    height: 4px;
    border-radius: 9999px;
    background: linear-gradient(to right, var(--primary) var(--at), var(--muted) var(--at));
  }
  .timeline::-webkit-slider-thumb {
    appearance: none;
    width: 1rem;
    height: 1rem;
    margin-top: -6px;
    border-radius: 9999px;
    background: var(--primary);
    box-shadow: 0 0 0 3px var(--card);
  }
  .timeline:focus-visible {
    outline: none;
  }
  .timeline:focus-visible::-webkit-slider-thumb {
    box-shadow:
      0 0 0 3px var(--card),
      0 0 0 6px var(--ring);
  }
  .timeline::-moz-range-track {
    height: 4px;
    border-radius: 9999px;
    background: var(--muted);
  }
  .timeline::-moz-range-progress {
    height: 4px;
    border-radius: 9999px;
    background: var(--primary);
  }
  .timeline::-moz-range-thumb {
    width: 1rem;
    height: 1rem;
    border: 0;
    border-radius: 9999px;
    background: var(--primary);
    box-shadow: 0 0 0 3px var(--card);
  }
</style>
