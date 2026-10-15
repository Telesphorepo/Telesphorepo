<!--
  A person's page (administrators): /settings/people/:name. The content starts again when the name
  changes, so no edit of one person carries over to another.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import PersonView from "./PersonView.svelte";

  let { params }: PageProps = $props();
</script>

{#key params.name}
  <PersonView name={params.name} />
{/key}
