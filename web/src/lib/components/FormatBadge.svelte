<!--
  The format of a repository: its icon (from the format plugin) and its title, in the text color
  of where it sits. No box and no tint: the icon is the mark.

    <FormatBadge format={repo.format} />
-->
<script lang="ts">
  import { formats } from "$lib/formats/registry.svelte";
  import FormatIcon from "./FormatIcon.svelte";

  let { format, title }: { format: string; /** Display title; by default the format's own. */ title?: string } = $props();

  formats.load();
  const text = $derived(title ?? formats.title(format));
</script>

<span class="inline-flex min-w-0 shrink-0 items-center gap-1.5 align-middle whitespace-nowrap">
  <FormatIcon {format} />
  <span class="truncate">{text}</span>
</span>
