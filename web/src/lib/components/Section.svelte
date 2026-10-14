<!--
  A titled block of a page (h2). Sections stack with the same gap; `narrow` keeps a form section
  in the form column (max-w-form) while tables use the full width.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  let {
    title,
    description,
    actions,
    narrow = false,
    tone = "default",
    class: className = "",
    children,
  }: {
    title?: string;
    description?: string;
    actions?: Snippet;
    narrow?: boolean;
    /** "danger" fills the section with a light destructive tint (deletes that cannot be undone). */
    tone?: "default" | "danger";
    class?: string;
    children: Snippet;
  } = $props();
</script>

<section
  class={cn(
    "mb-10 last:mb-0",
    narrow && "max-w-form",
    tone === "danger" && "rounded-2xl bg-destructive/8 p-5",
    className,
  )}
>
  {#if title || actions}
    <div class="mb-4 flex flex-wrap items-end justify-between gap-3">
      <div class="min-w-0">
        {#if title}
          <h2 class={cn("text-base font-semibold", tone === "danger" && "text-destructive")}>{title}</h2>
        {/if}
        {#if description}<p class="mt-1 max-w-prose text-sm text-muted-foreground">{description}</p>{/if}
      </div>
      {#if actions}<div class="flex shrink-0 items-center gap-2">{@render actions()}</div>{/if}
    </div>
  {/if}
  {@render children()}
</section>
