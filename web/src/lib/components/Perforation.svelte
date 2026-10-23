<!--
  The perforation of the ticket (branding/README.md, Perforation): the mark's slot column as a row
  of rounded slots, a quiet divider where a ticket is torn in two. Slots are 2 by 4 units with a
  gap of 2, as on the mark, at 1.5px a unit; the row fits whole slots to its length. Use it only
  inside a ticket shape (the admission ticket of a CI pipeline), never as a plain divider: a
  Separator parts sub-groups everywhere else.

    <Perforation />                    a horizontal row across its container
    <Perforation vertical class="h-24" />

  The slots are the canvas color by default, so they read as holes punched through a ticket on the
  page; `class` can give them another color (`text-foreground/20`).
-->
<script lang="ts">
  import { cn } from "$lib/utils";

  let { vertical = false, class: className }: { vertical?: boolean; class?: string } = $props();

  // One slot and its gap, as a mask tile: 6 by 3 px and 3 px of gap (turned for a column).
  const row = "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='9' height='3'%3E%3Crect width='6' height='3' rx='1.5'/%3E%3C/svg%3E\")";
  const column = "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='3' height='9'%3E%3Crect width='3' height='6' rx='1.5'/%3E%3C/svg%3E\")";
</script>

<div
  aria-hidden="true"
  class={cn("shrink-0 bg-current text-background", vertical ? "w-[3px] self-stretch" : "h-[3px] w-full", className)}
  style:mask-image={vertical ? column : row}
  style:mask-repeat={vertical ? "no-repeat round" : "round no-repeat"}
  style:mask-position="center"
></div>
