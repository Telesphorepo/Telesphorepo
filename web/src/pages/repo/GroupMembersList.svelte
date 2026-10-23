<!--
  The members of a group on its page, in read order. A row opens the member's page. A group with
  no member left says so.
-->
<script lang="ts">
  import type { GroupMember, Repo } from "$lib/api";
  import type { TableSource } from "$lib/load.svelte";
  import { href, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import { kindPhrase } from "$lib/words";
  import LockIcon from "@lucide/svelte/icons/lock";
  import LayersIcon from "@lucide/svelte/icons/layers";

  let { repo }: { repo: Repo } = $props();

  type Row = GroupMember & { n: number };
  const source = $derived<TableSource<Row>>({
    items: (repo.members ?? []).map((m, i) => ({ ...m, n: i + 1 })),
    loading: false,
    error: "",
  });
</script>

<Section title="Members" description="The group reads these repositories in this order. The first one that has a file serves it.">
  <ItemList {source} caption="Members of {repo.name}" key={(m) => m.name} href={(m) => href(repoPath(m.name))}>
    {#snippet leading(m)}
      <span class="flex items-center gap-3">
        <span class="w-5 text-right text-label tabular-nums" title="Read order">{m.n}</span>
        <FormatIcon format={m.format} class="size-5" />
      </span>
    {/snippet}
    {#snippet title(m)}{m.name}{/snippet}
    {#snippet badges(m)}
      {#if !m.public}
        <span class="flex text-muted-foreground" title="Private: only people with access get its files"><LockIcon aria-hidden="true" class="size-3.5" /><span class="sr-only">Private</span></span>
      {/if}
    {/snippet}
    {#snippet meta(m)}{kindPhrase(m)}{/snippet}
    {#snippet empty()}
      <CompactEmpty icon={LayersIcon} text="None. The group serves no files until a person with Edit settings adds a repository to it.">
        {#if repo.permissions?.includes("repo.settings")}
          <Button variant="outline" href={href(repoSettingsPath(repo.name))}>Add repositories</Button>
        {/if}
      </CompactEmpty>
    {/snippet}
  </ItemList>
</Section>
