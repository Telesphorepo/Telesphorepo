<!--
  Who can download from a repo: "Anyone" (anonymous_read) or "People with access". One wording on
  every page.
-->
<script lang="ts">
  import { Badge } from "$lib/components/ui/badge";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import LockIcon from "@lucide/svelte/icons/lock";

  let { anyone }: { /** The repo's anonymous_read. */ anyone: boolean } = $props();
</script>

{#if anyone}
  <Badge variant="secondary" class="font-normal" title="Anyone can download without signing in">
    <GlobeIcon />Anyone
  </Badge>
{:else}
  <Badge variant="outline" class="font-normal" title="Only people who have been given access can download">
    <LockIcon />People with access
  </Badge>
{/if}
