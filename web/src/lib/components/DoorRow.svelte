<!--
  A row of DoorCards: each door is one column of a three-column row, so one or two doors keep that
  width, centered; on a phone they stack. `label` names the row for screen readers.
-->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { label, children }: { label: string; children: Snippet } = $props();
</script>

<nav aria-label={label} class="mb-(--section-gap) flex flex-col gap-4 md:flex-row md:justify-center">
  {@render children()}
</nav>
