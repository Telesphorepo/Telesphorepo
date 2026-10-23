<!--
  The page of one network list. A list of your own has a name, a title, a description and its
  addresses, which the user pastes in any format (RangePaste); a new list ends with Create, an
  existing one saves from the SaveBar. A downloaded list shows its source, its ranges and its last
  refresh, and refreshes on request. Both show the repositories that use the list, linked to their
  Access tab. The parent keys it by name (null: a new list).
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type IPList } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import RangePaste from "$lib/components/RangePaste.svelte";
  import { repoAccessHref } from "../access";
  import { usageLines } from "./usage";
  import { Paged } from "./paged.svelte";
  import NetworkIcon from "@lucide/svelte/icons/network";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import WaypointsIcon from "@lucide/svelte/icons/waypoints";
  import RefreshIcon from "@lucide/svelte/icons/refresh-cw";
  import XIcon from "@lucide/svelte/icons/x";

  let { name: listName }: { name: string | null } = $props();

  const id = $props.id();
  const listPath = "/settings/networks";
  const isNew = untrack(() => listName === null);
  const nameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

  const list = new Resource<IPList | null>(() => (listName === null ? Promise.resolve(null) : api.ipLists.get(listName)));

  type Value = { name: string; title: string; description: string; ranges: string[] };
  const fromList = (l: IPList): Value => ({ name: l.name, title: l.title, description: l.description, ranges: [...(l.ranges ?? [])] });
  let draft = $state<Draft<Value> | null>(null);

  // Start the form once the list loads.
  $effect(() => {
    if (draft || list.loading) return;
    const l = list.data;
    untrack(() => {
      if (l === null) draft = new Draft<Value>({ name: "", title: "", description: "", ranges: [] });
      else if (l && l.kind === "custom") draft = new Draft(fromList(l));
    });
  });

  const ranges = new Paged<string>(() => draft?.value.ranges ?? list.data?.ranges ?? []);
  const addRanges = (r: string[]) => {
    if (draft) draft.value.ranges = [...draft.value.ranges, ...r.filter((x) => !draft!.value.ranges.includes(x))];
  };
  const removeRange = (r: string) => {
    if (draft) draft.value.ranges = draft.value.ranges.filter((x) => x !== r);
  };

  const nameError = $derived(!draft || !draft.value.name || nameRE.test(draft.value.name) ? "" : "Use lowercase letters, digits, dots, dashes and underscores.");

  function body(v: Value) {
    if (!v.name) throw new Error("Give the list a name.");
    if (nameError) throw new Error(nameError);
    return { name: v.name, title: v.title.trim(), description: v.description.trim(), ranges: v.ranges };
  }

  // A new list: Create at the end of the form.
  let busy = $state(false);
  let error = $state("");
  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!draft || busy) return;
    busy = true;
    error = "";
    try {
      const l = await api.ipLists.create(body(draft.value));
      toast.success(`Created the network list ${l.title}`);
      navigate(`${listPath}/list/${encodeURIComponent(l.name)}`, { replace: true });
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  // An existing list: the SaveBar.
  async function save() {
    if (!draft || !list.data) return;
    const before = list.data.name;
    const l = await api.ipLists.update(before, body(draft.value));
    draft.commit(fromList({ ...l, ranges: l.ranges ?? draft.value.ranges }));
    toast.success(`Saved the network list ${l.title}`);
    if (l.name !== before) navigate(`${listPath}/list/${encodeURIComponent(l.name)}`, { replace: true });
    else list.data = { ...l, ranges: l.ranges ?? draft.value.ranges };
  }

  let refreshing = $state(false);
  let refreshError = $state("");
  async function refresh() {
    if (!list.data) return;
    refreshing = true;
    refreshError = "";
    try {
      const l = await api.ipLists.refresh(list.data.name);
      if (l.ranges) list.data = l;
      else await list.reload();
      toast.success(`Refreshed ${l.title}: ${formatCount(l.count)} ranges`);
    } catch (e) {
      refreshError = errorMessage(e);
    } finally {
      refreshing = false;
    }
  }

  let deleteOpen = $state(false);
  async function remove() {
    if (!list.data) return;
    await api.ipLists.delete(list.data.name);
    toast.success(`Deleted the network list ${list.data.title}`);
    navigate(listPath, { force: true });
  }

  // What uses the list: a row per repository and rule, and the trusted proxies.
  type Use = { key: string; label: string; href: string; scope: string; proxy: boolean };
  const uses = $derived.by((): Use[] => {
    const u = list.data?.used_by;
    if (!u) return [];
    return [
      ...u.repos.map((r) => ({ key: r.repo + r.scope, label: r.repo, href: repoAccessHref(r.repo), scope: r.scope === "read" ? "Downloads" : "Publishes", proxy: false })),
      ...(u.trusted_proxies ? [{ key: "proxy", label: "Network and proxy", href: href("/settings/proxy"), scope: "Trusted proxies", proxy: true }] : []),
    ];
  });
  const usesSource: TableSource<Use> = {
    get items() {
      return uses;
    },
    loading: false,
    error: "",
  };
  const quietDanger = "hover:bg-destructive/10 hover:text-destructive";
</script>

{#snippet usedBy()}
  <Section title="Used by">
    <ItemList source={usesSource} caption="What uses this list" key={(u) => u.key} href={(u) => u.href}>
      {#snippet leading(u)}{#if u.proxy}<WaypointsIcon class="size-5" />{:else}<DatabaseIcon class="size-5" />{/if}{/snippet}
      {#snippet title(u)}{u.label}{/snippet}
      {#snippet meta(u)}{u.scope}{/snippet}
      {#snippet empty()}
        <CompactEmpty icon={DatabaseIcon} text="Choose this list under From where on a repository's Access tab.">
          <Button variant="outline" size="sm" href={href("/settings/repositories")}>All repositories</Button>
        </CompactEmpty>
      {/snippet}
    </ItemList>
  </Section>
{/snippet}

{#snippet rangeList(editable: boolean)}
  <ItemList source={ranges} caption="Address ranges" key={(r) => r}>
    {#snippet title(r)}<span class="tabular-nums">{r}</span>{/snippet}
    {#snippet trailing(r)}
      <CopyButton value={r} label="Copy {r}" />
      {#if editable}
        <Button variant="ghost" size="icon-sm" class={quietDanger} aria-label="Remove {r}" onclick={() => removeRange(r)}><XIcon /></Button>
      {/if}
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={NetworkIcon} text={editable ? "An empty list allows no address." : "The first refresh fills the list."} />
    {/snippet}
  </ItemList>
{/snippet}

{#if list.loading && list.data === undefined}
  <PageHeader title={isNew ? "New network list" : "Network list"} />
  <Loading rows={6} />
{:else if list.error && list.data === undefined}
  <PageHeader title="Network list" />
  <ErrorState error={list.error} title="The network list did not load" onRetry={list.reload} />
{:else if list.data && list.data.kind === "preset"}
  {@const l = list.data}
  <PageHeader title={l.title} description={l.description || undefined}>
    {#snippet actions()}
      <Button variant="outline" disabled={refreshing} onclick={refresh}>
        {#if refreshing}<Spinner />{:else}<RefreshIcon />{/if}
        Refresh now
      </Button>
    {/snippet}
  </PageHeader>

  <Section title="Address ranges" description="The server downloads them from {l.source}.">
    {#snippet actions()}
      <span class="text-meta text-muted-foreground">
        {#if l.fetched_at}Refreshed <RelativeTime at={l.fetched_at} />{:else}Not downloaded yet{/if}
      </span>
    {/snippet}
    {#if l.error || refreshError}<div class="mb-4"><ErrorState inline error="The last refresh failed: {refreshError || l.error}" /></div>{/if}
    {@render rangeList(false)}
  </Section>
  {@render usedBy()}{:else if draft}
  {@const v = draft.value}
  <PageHeader title={list.data ? list.data.title : "New network list"}>
    {#snippet actions()}
      {#if list.data}
        <Button
          variant="ghost"
          class={quietDanger}
          disabled={uses.length > 0}
          title={uses.length > 0 ? "Remove the list from every place that uses it first." : undefined}
          onclick={() => (deleteOpen = true)}>Delete</Button
        >
      {/if}
    {/snippet}
  </PageHeader>

  <FormPage>
    <form onsubmit={isNew ? create : (e) => e.preventDefault()} autocomplete="off">
      <FormSection title="List">
        <FormGrid>
          <FormRow label="Name" for="{id}-name" help="Repositories refer to the list by this name." error={nameError || undefined}>
            <Input id="{id}-name" spellcheck={false} aria-describedby="{id}-name-help" aria-invalid={nameError ? true : undefined} bind:value={v.name} />
            {#snippet more()}Use lowercase letters, digits, dots, dashes and underscores. When you rename the list, every repository that uses it uses the new name.{/snippet}
          </FormRow>
          <FormRow label="Title" for="{id}-title" help="A readable name, such as Berlin office. Leave it empty to use the name.">
            <Input id="{id}-title" aria-describedby="{id}-title-help" bind:value={v.title} />
          </FormRow>
          <FormRow label="Description" for="{id}-desc" help="Say what the addresses are, for example the office in Berlin.">
            <Input id="{id}-desc" aria-describedby="{id}-desc-help" bind:value={v.description} />
          </FormRow>
        </FormGrid>
      </FormSection>

      <FormSection title="Addresses">
        {#snippet actions()}
          {#if v.ranges.length}<Button variant="ghost" size="sm" class={quietDanger} onclick={() => (v.ranges = [])}>Remove all</Button>{/if}
        {/snippet}
        <div class="flex flex-col gap-6">
          {@render rangeList(true)}
          <RangePaste id="{id}-paste" label="Add addresses" onAdd={addRanges} onReplace={v.ranges.length ? (r) => (v.ranges = r) : undefined} />
        </div>
        {#snippet footer()}
          {#if isNew}
            <Button type="submit" disabled={busy || !v.name || !!nameError}>
              {#if busy}<Spinner />{/if}
              Create network list
            </Button>
            <Button variant="ghost" href={href(listPath)}>Cancel</Button>
            {#if error}<div class="w-full"><ErrorState inline {error} /></div>{/if}
          {/if}
        {/snippet}
      </FormSection>

      {#if list.data}{@render usedBy()}{/if}

    </form>
  </FormPage>

  {#if !isNew}<SaveBar count={draft.count} onDiscard={draft.reset} onSave={save} />{/if}

  <ConfirmDialog
    bind:open={deleteOpen}
    title="Delete the network list {list.data?.title ?? ''}?"
    confirmLabel="Delete network list"
    destructive
    changes={[`Deletes the list and its ${formatCount(list.data?.count ?? 0)} addresses`, ...(list.data ? usageLines(list.data.used_by) : [])]}
    onConfirm={remove}
  />
{/if}
