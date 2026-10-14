<!--
  The format of a repo ("maven", "debian", "raw", or any plugin). The dot color is stable per
  format name, so a format looks the same on every page.
-->
<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";

  let { format, title }: { format: string; /** Display title from /api/formats; the name when absent. */ title?: string } =
    $props();

  // Known formats get a fixed hue; other plugins get one from their name.
  const known: Record<string, number> = { maven: 25, debian: 340, raw: 220 };
  const hue = $derived(
    known[format] ?? [...format].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 360, 7),
  );
</script>

<Badge variant="outline" class="gap-1.5 font-normal" title={title ?? format}>
  <span aria-hidden="true" class="size-2 rounded-full" style="background: oklch(0.68 0.15 {hue})"></span>
  {title ?? format}
</Badge>
