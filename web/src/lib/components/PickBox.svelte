<!--
  The checkbox of a row (or, with `all`, of the head) in a collection with bulk actions: a 40px
  wide hit target over the height its parent gives it (`class`).
-->
<script lang="ts" generics="T, K">
  import { Checkbox } from "$lib/components/ui/checkbox";
  import type { Pick } from "$lib/pick.svelte";
  import { cn } from "$lib/utils";

  let {
    pick,
    item,
    label,
    pickOff = false,
    class: className,
  }: {
    pick: Pick<T, K>;
    /** The row; leave it out for the head checkbox, which picks every pickable loaded row. */
    item?: T;
    /** The checkbox's name ("Select oasis"); the head checkbox names itself. */
    label?: string;
    pickOff?: boolean;
    class?: string;
  } = $props();
</script>

<label data-pick class={cn("flex w-10 shrink-0 items-center justify-center select-none", !pickOff && "cursor-pointer", className)}>
  {#if item === undefined}
    <Checkbox
      checked={pick.all}
      indeterminate={pick.bar && !pick.all}
      disabled={pickOff || pick.rows.length === 0}
      aria-label={pick.allLabel}
      onCheckedChange={(v) => (v ? pick.pickAll() : pick.clear())}
    />
  {:else}
    <Checkbox disabled={pickOff} checked={pick.has(item)} aria-label={label} onCheckedChange={(v) => pick.toggle(item, v === true)} />
  {/if}
</label>
