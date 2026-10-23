<!--
  The Members card of a group's Overview: the members in read order, with Move up, Move down and
  Remove on each row and "Add repositories" in the header. The edits go into the parent's draft
  (`members`), and the page's save bar saves them (PUT /api/repos/{name}/members).

  A row says when the group is public and the member is private (people without access to the
  member do not get its files through the group), and warns when a proxy comes before a hosted
  member (the remote can shadow your own files).
-->
<script lang="ts">
  import { api, type Repo, type RepoKind } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { Button } from "$lib/components/ui/button";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import { kindPhrase } from "$lib/words";
  import ChoiceDialog from "./ChoiceDialog.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import ArrowUpIcon from "@lucide/svelte/icons/arrow-up";
  import XIcon from "@lucide/svelte/icons/x";
  import ArrowDownIcon from "@lucide/svelte/icons/arrow-down";
  import LockIcon from "@lucide/svelte/icons/lock";
  import TriangleAlertIcon from "@lucide/svelte/icons/triangle-alert";
  import LayersIcon from "@lucide/svelte/icons/layers";

  let { repo, members = $bindable() }: { repo: Repo; members: string[] } = $props();

  const maxMembers = 50;
  const all = new Resource(() => api.repos.list());

  type Info = { kind: RepoKind; public: boolean; description: string };
  // What the page knows of each repository that can be a member: the group's own answer first,
  // then the list of repositories (for members added since the last save).
  const info = $derived.by(() => {
    const m = new Map<string, Info>();
    for (const r of all.data ?? []) {
      if (r.format === repo.format && r.kind !== "group") m.set(r.name, { kind: r.kind, public: r.anonymous_read, description: r.description });
    }
    for (const x of repo.members ?? []) {
      m.set(x.name, { kind: x.kind, public: x.public, description: m.get(x.name)?.description ?? "" });
    }
    return m;
  });

  type Row = { name: string; n: number; shadows: boolean; privateNote: boolean };
  const rows = $derived<Row[]>(
    members.map((name, i) => ({
      name,
      n: i + 1,
      shadows: info.get(name)?.kind === "proxy" && members.slice(i + 1).some((x) => info.get(x)?.kind === "hosted"),
      privateNote: repo.anonymous_read && info.get(name)?.public === false,
    })),
  );
  const source = $derived<TableSource<Row>>({ items: rows, loading: false, error: "" });
  const shadowText = "This proxy comes before a repository of your own, so the remote can shadow your files. Move it down.";
  const privateText = "Only people with access to this repository get its files through the group.";

  function move(i: number, by: -1 | 1) {
    const next = [...members];
    [next[i], next[i + by]] = [next[i + by], next[i]];
    members = next;
  }

  function remove(name: string) {
    members = members.filter((x) => x !== name);
  }

  // --- add ---

  let addOpen = $state(false);
  const rank = (name: string) => (info.get(name)?.kind === "proxy" ? 1 : 0);
  const choices = $derived(
    [...info.entries()]
      .filter(([name]) => name !== repo.name && !members.includes(name))
      .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
      .map(([name, i]) => ({
        id: name,
        label: name,
        detail: [i.kind === "proxy" ? "Proxy" : "Repository", i.public ? "" : "Private", i.description].filter(Boolean).join(" · "),
      })),
  );

  // New hosted repositories go before the first proxy, new proxies at the end, so a remote never
  // shadows your own files.
  function add(names: string[]) {
    const hosted = names.filter((n) => rank(n) === 0);
    const proxies = names.filter((n) => rank(n) === 1);
    const firstProxy = members.findIndex((n) => rank(n) === 1);
    const at = firstProxy < 0 ? members.length : firstProxy;
    members = [...members.slice(0, at), ...hosted, ...members.slice(at), ...proxies];
  }
</script>

{#snippet addButton()}
  <Button variant="outline" disabled={members.length >= maxMembers} onclick={() => (addOpen = true)}><PlusIcon />Add repositories</Button>
{/snippet}

<Section title="Members" description="The group reads its members in this order. The first member that has a file serves it." actions={members.length ? addButton : undefined}>
  <ItemList {source} caption="Members of {repo.name}" key={(r) => r.name} href={(r) => href(repoPath(r.name))}>
    {#snippet leading(r)}
      <span class="flex items-center gap-3">
        <span class="w-5 text-right text-label tabular-nums" title="Read order">{r.n}</span>
        <FormatIcon format={repo.format} class="size-5" />
      </span>
    {/snippet}
    {#snippet title(r)}{r.name}{/snippet}
    {#snippet badges(r)}
      {#if info.get(r.name)?.public === false}
        <span class="flex text-muted-foreground" title="Private"><LockIcon aria-hidden="true" class="size-3.5" /><span class="sr-only">Private</span></span>
      {/if}
    {/snippet}
    {#snippet meta(r)}
      {@const kind = info.get(r.name)?.kind}
      {#if r.shadows}
        <span class="flex min-w-0 items-center gap-1.5 text-warning" title={shadowText}>
          <TriangleAlertIcon aria-hidden="true" class="size-3.5 shrink-0" /><span class="truncate">{shadowText}</span>
        </span>
      {:else}
        <span title={r.privateNote ? privateText : undefined}>{[kind && kindPhrase({ kind }), r.privateNote && privateText].filter(Boolean).join(" · ")}</span>
      {/if}
    {/snippet}
    {#snippet trailing(r)}
      <Button variant="ghost" size="icon-sm" aria-label="Move {r.name} up" title="Move up" disabled={r.n === 1} onclick={() => move(r.n - 1, -1)}>
        <ArrowUpIcon />
      </Button>
      <Button variant="ghost" size="icon-sm" aria-label="Move {r.name} down" title="Move down" disabled={r.n === members.length} onclick={() => move(r.n - 1, 1)}>
        <ArrowDownIcon />
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class="hover:bg-destructive/10 hover:text-destructive"
        aria-label="Remove {r.name} from the group"
        title={members.length === 1 ? "A group needs at least one repository. Add another one first." : undefined}
        disabled={members.length === 1}
        onclick={() => remove(r.name)}><XIcon class="sm:hidden" /><span class="max-sm:hidden">Remove</span></Button
      >
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={LayersIcon} title="Members" state="None" text="The group serves no files until you add a repository to it.">
        {@render addButton()}
      </CompactEmpty>
    {/snippet}
  </ItemList>
</Section>

<ChoiceDialog
  bind:open={addOpen}
  title="Add repositories to {repo.name}"
  description="The group reads your own repositories first and proxies after them."
  noun={["repository", "repositories"]}
  items={choices}
  loading={all.loading}
  error={all.error}
  none="Every repository of this format is in the group already."
  newLabel="New repository"
  newHref={auth.can("repositories.all") ? href("/settings/repositories/new") : undefined}
  onAdd={add}
/>
