<!--
  When a slower process outside the page runs next, as a value where the value it moves is:
  "Next gap check in about 7 min", with the interval in the tooltip ("Every 10 minutes"). The
  interval comes from the server that owns it; the next time is `next`, or else `last` plus the
  interval. Before a first run it is the interval ("Every 10 minutes"). A next time more than one
  interval past is late ("Gap check 14 min late"), in the warning color, so a stuck process does
  not look like one that waits.

    <Cadence noun="gap check" every={st.gap_check_every} last={st.gap_checked_at} />
-->
<script lang="ts">
  import { clock } from "$lib/live.svelte";
  import { formatEvery, formatSpan } from "$lib/format";
  import { cn } from "$lib/utils";

  let {
    noun,
    every,
    last = null,
    next = null,
    class: className,
  }: {
    /** One run, in lower case: "gap check", "count of missing files", "sync". */
    noun: string;
    /** Seconds between two runs. */
    every: number;
    last?: string | null;
    next?: string | null;
    class?: string;
  } = $props();

  const due = $derived(next ? Date.parse(next) : last ? Date.parse(last) + every * 1000 : null);
  const left = $derived(due === null ? null : due - clock.now);
  const late = $derived(left !== null && -left > every * 1000);
  const interval = $derived(`Every ${formatEvery(every)}`);
  const text = $derived.by(() => {
    if (left === null) return interval;
    if (left > 0) return `Next ${noun} in about ${formatSpan(left)}`;
    if (!late) return `Next ${noun} due now`;
    return `${noun.charAt(0).toUpperCase()}${noun.slice(1)} ${formatSpan(-left)} late`;
  });
</script>

<p class={cn("text-meta text-pretty tabular-nums", late ? "text-warning" : "text-muted-foreground", className)} title={left === null ? undefined : interval}>{text}</p>
