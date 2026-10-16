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
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import RepoKindIcon from "$lib/components/RepoKindIcon.svelte";
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
  <DataTable
    {source}
    caption="Members of {repo.name}"
    key={(m) => m.name}
    href={(m) => href(repoPath(m.name))}
    columns={[
      { label: "Order", align: "right" },
      { label: "Repository", grow: true },
    ]}
  >
    {#snippet row(m)}
      <Cell num>{m.n}</Cell>
      <Cell grow>
        <span class="flex min-w-0 items-center gap-1.5">
          <a href={href(repoPath(m.name))} class="truncate rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{m.name}</a>
          {#if !m.public}
            <span class="flex shrink-0 text-muted-foreground" title="Private: only people with access get its files"><LockIcon aria-hidden="true" class="size-3.5" /><span class="sr-only">Private</span></span>
          {/if}
          <RepoKindIcon kind={m.kind} />
        </span>
      </Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={LayersIcon} title="Members" state="None" text="The group serves no files until a person with Edit settings adds a repository to it.">
        {#if repo.permissions?.includes("repo.settings")}
          <Button variant="outline" href={href(repoSettingsPath(repo.name))}>Add repositories</Button>
        {/if}
      </CompactEmpty>
    {/snippet}
  </DataTable>
</Section>
