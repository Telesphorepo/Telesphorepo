<!-- One Nexus server of the Nexus migration: /settings/nexus/:id (?sync= opens a sync). -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import SourceView from "./nexus/SourceView.svelte";

  let { params, query }: PageProps = $props();
</script>

{#key params.id}
  <SourceView id={Number(params.id)} sync={Number(query.get("sync")) || null} />
{/key}
