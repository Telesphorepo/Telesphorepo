<!-- The plan of a merge of the Nexus migration: /settings/nexus/merge?from=&into=. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import MergePlanView from "./nexus/MergePlanView.svelte";

  let { query }: PageProps = $props();
  const from = $derived(query.get("from") ?? "");
  const into = $derived(query.get("into") ?? "");
</script>

{#key `${from}\n${into}`}
  <MergePlanView {from} {into} />
{/key}
