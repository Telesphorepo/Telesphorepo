<!--
  A time as "3 minutes ago" / "in 2 hours", with the local date and time in the tooltip. null
  renders `empty` ("never" by default). The text refreshes every minute.
-->
<script lang="ts">
  import { formatDateTime, formatRelative } from "$lib/format";

  let { at, empty = "never" }: { at: string | null | undefined; empty?: string } = $props();

  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 60_000);
    return () => clearInterval(t);
  });
</script>

{#if at}
  <time datetime={at} title={formatDateTime(at)} class="whitespace-nowrap">{formatRelative(at, now)}</time>
{:else}
  <span class="text-muted-foreground">{empty}</span>
{/if}
