<!--
  A value in running text with a copy button: a checksum, a fingerprint, a path. Sans-serif with
  tabular numerals; it is data to read, not code.

    <CopyValue value={asset.sha256} label="SHA-256" />
-->
<script lang="ts">
  import CopyButton from "./CopyButton.svelte";

  let { value, label = "value" }: { value: string; /** What is copied, for the button's name. */ label?: string } = $props();
</script>

<span class="flex min-w-0 items-start gap-1">
  <span class="min-w-0 flex-1 pt-0.5 text-sm break-all tabular-nums">{value}</span>
  <CopyButton {value} label="Copy {label}" size="icon-xs" class="mt-px" />
</span>
