<!--
  The mark of a repository's kind: a group shows the group icon, a proxy the proxy icon, a hosted
  repository nothing. The icon has its word for screen readers ("Group", "Proxy") and as a tooltip.

    <RepoKindIcon kind={repo.kind} />
-->
<script lang="ts">
  import type { RepoKind } from "$lib/api";
  import { cn } from "$lib/utils";
  import LayersIcon from "@lucide/svelte/icons/layers";
  import CloudDownloadIcon from "@lucide/svelte/icons/cloud-download";

  let { kind, class: className, iconClass = "size-3.5" }: { kind: RepoKind | undefined; class?: string; iconClass?: string } = $props();

  const look = $derived(
    kind === "group"
      ? { Icon: LayersIcon, label: "Group", title: "Group: reads several repositories through one address" }
      : kind === "proxy"
        ? { Icon: CloudDownloadIcon, label: "Proxy", title: "Proxy: keeps the files of a remote repository" }
        : null,
  );
</script>

{#if look}
  <span class={cn("flex shrink-0 text-muted-foreground", className)} title={look.title}>
    <look.Icon aria-hidden="true" class={iconClass} />
    <span class="sr-only">{look.label}</span>
  </span>
{/if}
