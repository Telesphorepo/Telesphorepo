<!--
  A slower process outside the page that the reader waits for, with its cadence, where its value
  is: "The Worker checks every 10 minutes. Next check in about 7 min." The interval comes from the
  server that owns it; the next time is `next`, or else `last` plus the interval. A next time
  more than one interval past says the process is late, in the warning color, so a stuck process
  does not look like one that waits.

    <Cadence what="The Worker checks" noun="check" every={st.gap_check_every} last={st.gap_checked_at} />
-->
<script lang="ts">
  import { clock } from "$lib/live.svelte";
  import { formatEvery, formatSpan } from "$lib/format";
  import { cn } from "$lib/utils";

  let {
    what,
    noun,
    every,
    last = null,
    next = null,
    class: className,
  }: {
    /** The start of the sentence, before "every …": "The Worker checks", "The server counts the files". */
    what: string;
    /** One run: "check", "count", "sync". */
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
  const after = $derived.by(() => {
    if (left === null) return "";
    if (left > 0) return ` Next ${noun} in about ${formatSpan(left)}.`;
    if (!late) return ` Next ${noun} any moment now.`;
    return ` The ${noun} is ${formatSpan(-left)} late.`;
  });
</script>

<p class={cn("text-meta text-pretty", late ? "text-warning" : "text-muted-foreground", className)}>{what} every {formatEvery(every)}.{after}</p>
