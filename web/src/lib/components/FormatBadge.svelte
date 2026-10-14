<!--
  The format of a repo ("maven", "debian", "raw", or any plugin): its icon and its title. The icon
  and its color come from formatLook, so a format looks the same on every page.
-->
<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import { formatLook } from "$lib/formats";

  let { format, title }: { format: string; /** Display title from /api/formats; the name when absent. */ title?: string } =
    $props();

  const look = $derived(formatLook(format));
</script>

<Badge variant="outline" class="gap-1.5 font-normal" title={title ?? format}>
  <span aria-hidden="true" class="flex" style="color: oklch(0.62 0.15 {look.hue})"><look.icon class="size-3.5" /></span>
  {title ?? format}
</Badge>
