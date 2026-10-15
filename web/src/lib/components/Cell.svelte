<!--
  A cell of a DataTable row. Every cell is one line (the row has one fixed height), so a long value
  truncates: pass it as `text` and it ends in "…" with the full value in a tooltip, and `copy` adds
  a copy button that shows on row hover. `grow` marks the column that takes the free width (one per
  table; its header column has `grow` too). The grow column keeps at least 10rem; on a narrow screen
  the table scrolls sideways inside its box, never the page. `num` is a number or a time:
  right-aligned, tabular numerals.

    <Cell grow strong text={repo.name} />
    <Cell text={key.fingerprint} copy muted />
    <Cell num>{formatCount(repo.file_count)}</Cell>
    <Cell num muted><RelativeTime at={repo.last_published_at} /></Cell>
    <Cell actions><Button variant="ghost" size="sm">Revoke</Button></Cell>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Table from "$lib/components/ui/table";
  import Truncate from "./Truncate.svelte";
  import { cn } from "$lib/utils";

  let {
    text,
    copy = false,
    copyLabel,
    grow = false,
    num = false,
    actions = false,
    strong = false,
    muted = false,
    hideOnPhone = false,
    class: className = "",
    children,
  }: {
    /** One-line text that truncates with a tooltip. */
    text?: string;
    copy?: boolean | string;
    copyLabel?: string;
    /** The column that takes the free width. */
    grow?: boolean;
    /** A number, size or time: right-aligned, tabular numerals. */
    num?: boolean;
    /** The row's buttons, right-aligned at the end of the row. */
    actions?: boolean;
    /** The row's name: medium weight. */
    strong?: boolean;
    muted?: boolean;
    /** Hidden under 640px, with its column (Column `hideOnPhone`). */
    hideOnPhone?: boolean;
    class?: string;
    children?: Snippet;
  } = $props();
</script>

<Table.Cell
  class={cn(
    grow ? "w-full max-w-0 min-w-40" : text ? "max-w-80" : "",
    (num || actions) && "text-right",
    num && "tabular-nums",
    actions && "w-0",
    strong && "font-medium",
    muted && "text-muted-foreground",
    hideOnPhone && "max-sm:hidden",
    className,
  )}
>
  {#if text !== undefined}
    <Truncate {text} {copy} {copyLabel} />
  {:else if actions}
    <div class="flex items-center justify-end gap-1">{@render children?.()}</div>
  {:else}
    {@render children?.()}
  {/if}
</Table.Cell>
