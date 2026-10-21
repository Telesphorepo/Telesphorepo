<!--
  The body of a form page. Every Settings page uses the full content column, so this sets no
  width: the page header, the summary and the sections line up with every other page, and the
  form grid inside keeps its own fixed columns (14rem labels, 32rem controls).
-->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { children }: { children: Snippet } = $props();
</script>

<!-- Its own section gap: the first section inside is the wrapper's first child, so after a
  section above the form the gap would be lost. -->
<div class="mt-(--section-gap) first:mt-0">{@render children()}</div>
