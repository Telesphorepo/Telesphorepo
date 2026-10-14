<!--
  A square tile with the icon of a format, tinted with the format's hue. The repository list uses
  it in front of each name. Decorative: the format title is always written next to it.
-->
<script lang="ts">
  import { formatLook } from "$lib/formats";
  import { cn } from "$lib/utils";

  let { format, class: className = "" }: { format: string; class?: string } = $props();

  const look = $derived(formatLook(format));
</script>

<span
  aria-hidden="true"
  class={cn("flex size-10 shrink-0 items-center justify-center rounded-xl", className)}
  style="background: oklch(0.62 0.15 {look.hue} / 0.14); color: oklch(0.6 0.15 {look.hue})"
>
  <look.icon class="size-5" />
</span>
