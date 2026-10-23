<!--
  The Overview card of a hosted repository or a proxy that is in groups: the groups, by name. A
  row opens the group's settings.
-->
<script lang="ts">
  import type { TableSource } from "$lib/load.svelte";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import LayersIcon from "@lucide/svelte/icons/layers";

  let { repo, groups }: { repo: string; groups: string[] } = $props();

  const source = $derived<TableSource<string>>({ items: groups, loading: false, error: "" });
</script>

<Section title="Groups" description="Builds that use one of these groups also get the files of {repo}.">
  <ItemList {source} caption="Groups that read {repo}" key={(g) => g} href={(g) => href(repoSettingsPath(g))}>
    {#snippet leading()}<LayersIcon class="size-5" />{/snippet}
    {#snippet title(g)}{g}{/snippet}
  </ItemList>
</Section>
