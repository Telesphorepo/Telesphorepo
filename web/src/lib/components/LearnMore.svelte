<!--
  Longer help behind a disclosure: a quiet "Learn more" toggle, and the text in a panel under it.
  Help on a page stays one sentence; anything longer goes in here. A form row has the same
  disclosure built in (FormRow `more`), with the panel under the whole row.

    <LearnMore>
      The server checks every address against the trusted proxies before it reads X-Forwarded-For.
    </LearnMore>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { slide } from "svelte/transition";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import { cn } from "$lib/utils";

  let {
    open = $bindable(false),
    label = "Learn more",
    /** Render only the toggle; the parent shows the panel (FormRow). */
    toggleOnly = false,
    controls,
    children,
  }: {
    open?: boolean;
    label?: string;
    toggleOnly?: boolean;
    /** The id of the panel, when the parent renders it. */
    controls?: string;
    children?: Snippet;
  } = $props();

  const uid = $props.id();
  const panelId = $derived(controls ?? `${uid}-more`);
  const reduce = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
</script>

<button
  type="button"
  aria-expanded={open}
  aria-controls={panelId}
  onclick={() => (open = !open)}
  class="-mx-1 inline-flex w-fit items-center gap-1 rounded-sm px-1 text-meta text-link outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
>
  {label}
  <ChevronDownIcon aria-hidden="true" class={cn("size-3.5 transition-transform", open && "rotate-180")} />
</button>
{#if !toggleOnly && open && children}
  <div id={panelId} transition:slide={{ duration: reduce ? 0 : 160 }} class="mt-2 rounded-lg bg-panel px-4 py-3 text-body text-muted-foreground">
    {@render children()}
  </div>
{/if}
