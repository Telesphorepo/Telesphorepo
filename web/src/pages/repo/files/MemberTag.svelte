<!--
  The repository that serves a file or a version of a group (`repo` of the server's answer), muted
  after its name. Nothing when the answer names no repository (any repository but a group).
-->
<script lang="ts">
  import { cn } from "$lib/utils";

  let { repo, class: className = "" }: { repo?: string; class?: string } = $props();
</script>

{#if repo}
  <span class={cn("flex h-5 max-w-40 shrink-0 items-center rounded-sm bg-muted px-1.5 text-meta text-muted-foreground", className)} title="Served by {repo}">
    <span class="sr-only">from</span><span class="truncate">{repo}</span>
  </span>
{/if}
