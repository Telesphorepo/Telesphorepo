<!-- A network list: /settings/networks/new or /settings/networks/list/:name. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import ListEditor from "./ip-lists/ListEditor.svelte";

  let { params }: PageProps = $props();
</script>

{#key params.name ?? ""}
  <ListEditor name={params.name ?? null} />
{/key}
