<!-- A person's avatar: the first two letters of the username in a circle. -->
<script lang="ts">
  import * as Avatar from "$lib/components/ui/avatar";
  import { cn } from "$lib/utils.js";

  let { name, size = "sm" }: { name: string; size?: "sm" | "lg" } = $props();
  const initials = $derived((name || "?").slice(0, 2).toUpperCase());
</script>

<Avatar.Root {size} aria-hidden="true">
  <Avatar.Fallback class={cn("font-semibold", size === "sm" ? "text-[0.75rem]" : "text-sm")}>{initials}</Avatar.Fallback>
</Avatar.Root>
