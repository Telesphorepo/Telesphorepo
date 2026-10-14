<!--
  What a list or page shows when it has nothing yet. Say what is missing and, in the `children`
  snippet, give the action that fills it ("Create a repository").
-->
<script lang="ts">
  import type { Component, Snippet } from "svelte";
  import * as Empty from "$lib/components/ui/empty";
  import InboxIcon from "@lucide/svelte/icons/inbox";

  let {
    title,
    description,
    icon: Icon = InboxIcon,
    children,
  }: {
    title: string;
    description?: string;
    icon?: Component;
    children?: Snippet;
  } = $props();
</script>

<Empty.Root class="rounded-2xl border border-dashed py-10">
  <Empty.Header>
    <Empty.Media variant="icon"><Icon /></Empty.Media>
    <Empty.Title>{title}</Empty.Title>
    {#if description}<Empty.Description>{description}</Empty.Description>{/if}
  </Empty.Header>
  {#if children}<Empty.Content>{@render children()}</Empty.Content>{/if}
</Empty.Root>
