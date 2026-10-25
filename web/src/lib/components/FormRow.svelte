<!--
  One field of a FormGrid: the label above the control, the control filling its cell, then one
  line of help. Every text control fills its cell, so every text control in a grid has the same
  width: an input, a select, a list, a secret, and a number with its unit (UnitInput) alike.
  `full` spans both columns. `width="auto"` is only for a control that sizes itself at the start
  of the cell: a switch, a checkbox, a button.

    <FormRow label="Provider address" for="issuer" help="Where your provider publishes its sign-in details."
      aka="Issuer URL">
      <Input id="issuer" bind:value={issuer} aria-describedby="issuer-help" />
      {#snippet more()}Find it in your provider's app settings, often under "OpenID configuration".{/snippet}
    </FormRow>

  The help sits under the control, so the controls of a row of the grid line up; it has the id
  `{for}-help`: point the control's aria-describedby at it. `more` is longer help behind a "Learn
  more" disclosure. `aka` is the technical name of the value, after `akaLabel` ("Also called"; the
  Sign-in options page says "In your provider"). `badge` sits next to the label (e.g. "Set by
  TR_LISTEN"). `length` and `max` count the characters of a text control at the end of the help
  line, with the id `{for}-count`.
-->
<script lang="ts" module>
  /** "full" (the default): the whole cell. "auto": a control that sizes itself. */
  export type ControlWidth = "full" | "auto";
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  import { slide } from "svelte/transition";
  import { Label } from "$lib/components/ui/label";
  import ErrorState from "./ErrorState.svelte";
  import LearnMore from "./LearnMore.svelte";
  import { cn } from "$lib/utils";
  import { formatCount } from "$lib/format";

  let {
    label,
    for: id,
    help,
    aka,
    akaLabel = "Also called",
    width = "full",
    full = false,
    error,
    badge,
    more,
    length,
    max,
    children,
  }: {
    label: string;
    /** The id of the control. */
    for?: string;
    /** One sentence: what the value does for the reader. */
    help?: string;
    aka?: string;
    akaLabel?: string;
    width?: ControlWidth;
    /** Span both columns of the grid. */
    full?: boolean;
    error?: string;
    badge?: Snippet;
    /** Longer help, behind "Learn more". */
    more?: Snippet;
    /** The characters in the control, with `max`: the counter at the end of the help line. */
    length?: number;
    max?: number;
    children: Snippet;
  } = $props();

  const uid = $props.id();
  const counted = $derived(max !== undefined);
  let open = $state(false);
  const reduce = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
</script>

<div class={cn("flex min-w-0 flex-col gap-2", full && "sm:col-span-2")}>
  <div class="flex min-h-5 flex-wrap items-center gap-2">
    <Label for={id} class="text-label">{label}</Label>
    <!-- The badge keeps the label row at one line, so the controls of a grid row line up. -->
    {#if badge}<span class="-my-0.5 flex">{@render badge()}</span>{/if}
  </div>
  <div class={width === "auto" ? "flex min-h-9 items-center" : "w-full"}>{@render children()}</div>
  {#if help || more || aka || counted}
    <div class="flex flex-col gap-0.5 text-meta text-muted-foreground">
      {#if help || more || counted}
        <div class="flex items-start justify-between gap-4">
          <p>
            {#if help}<span id={id ? `${id}-help` : undefined}>{help}</span>{/if}
            {#if more}<LearnMore bind:open toggleOnly controls="{uid}-more" />{/if}
          </p>
          {#if counted}<p id={id ? `${id}-count` : undefined} class="shrink-0 tabular-nums">{formatCount(length ?? 0)} of {formatCount(max ?? 0)}</p>{/if}
        </div>
      {/if}
      {#if aka}<p>{akaLabel}: <span class="text-foreground">{aka}</span></p>{/if}
    </div>
  {/if}
  {#if error}<ErrorState inline {error} />{/if}
  {#if more && open}
    <div id="{uid}-more" transition:slide={{ duration: reduce ? 0 : 160 }} class="rounded-lg bg-panel px-4 py-3 text-body text-muted-foreground">
      {@render more()}
    </div>
  {/if}
</div>
