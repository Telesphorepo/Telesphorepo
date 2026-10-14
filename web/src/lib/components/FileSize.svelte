<!-- A byte count in binary units ("1.5 MiB"); the exact count is in the tooltip. -->
<script lang="ts">
  import { formatSize } from "$lib/format";

  let { bytes }: { bytes: number } = $props();
</script>

<span class="tabular-nums whitespace-nowrap" title="{bytes.toLocaleString()} bytes">{formatSize(bytes)}</span>
