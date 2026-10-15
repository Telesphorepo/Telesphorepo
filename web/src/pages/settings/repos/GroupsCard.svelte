<!--
  The Overview card of a hosted repository or a proxy that is in groups: the groups, by name. A
  row opens the group's settings.
-->
<script lang="ts">
  import type { TableSource } from "$lib/load.svelte";
  import { href, repoSettingsPath } from "$lib/router.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";

  let { repo, groups }: { repo: string; groups: string[] } = $props();

  const source = $derived<TableSource<string>>({ items: groups, loading: false, error: "" });
</script>

<Section title="Groups" description="Builds that use one of these groups also get the files of {repo}.">
  <DataTable {source} caption="Groups that read {repo}" key={(g) => g} href={(g) => href(repoSettingsPath(g))} columns={[{ label: "Group", grow: true }]}>
    {#snippet row(g)}
      <Cell grow strong><a href={href(repoSettingsPath(g))} class="rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{g}</a></Cell>
    {/snippet}
  </DataTable>
</Section>
