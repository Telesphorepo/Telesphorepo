<!-- One merge of the Nexus migration: /settings/nexus/merges/:id. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import MergeView from "./nexus/MergeView.svelte";

  let { params, query }: PageProps = $props();
</script>

{#key params.id}
  <MergeView id={Number(params.id)} {query} />
{/key}
