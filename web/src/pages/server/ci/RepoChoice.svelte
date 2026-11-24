<!--
  The repositories a CI pipeline publishes to, for a person who may hold CI pipelines on thousands
  of them. On top, the chosen ones as small chips (format icon, name, ×), whatever the search
  shows. Under them a search field: the hosted repositories where the person holds CI pipelines
  whose name holds what is typed (GET /api/repos?perm=ci.pipelines&kind=hosted&q=, 100 a page,
  250 ms after the last key); Enter chooses the first result that is not chosen yet, the down
  arrow moves to the results. The results are one-line checkbox rows in a grid (three columns on a
  wide card, two, one on a phone), the format in the row's tooltip since its icon says it, in an
  area of its own height that loads the next page as its end comes into view, so the form keeps
  its length. A rehearsal repository says "Rehearsal for <name>: publish here first" under its
  name and stands right before the repository it rehearses when both are loaded; the server's
  order holds otherwise.

  The parent keeps the choice (`chosen`, `onchoose`); `onrepos` hears every repository the part
  reads (the search pages, and the chosen ones it had not seen), so the parent knows their formats.
-->
<script lang="ts">
  import { SvelteMap } from "svelte/reactivity";
  import { api, type Repo } from "$lib/api";
  import { Keyset, Searched } from "$lib/load.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href } from "$lib/router.svelte";
  import { fadeEdges } from "$lib/fade";
  import { formats } from "$lib/formats/registry.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import * as InputGroup from "$lib/components/ui/input-group";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import ChosenChips from "$lib/components/ChosenChips.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SourceMore from "$lib/components/SourceMore.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import SearchIcon from "@lucide/svelte/icons/search";
  import SearchXIcon from "@lucide/svelte/icons/search-x";

  let {
    id,
    chosen,
    onchoose,
    onrepos,
  }: {
    id: string;
    /** The names of the chosen repositories, in order. */
    chosen: string[];
    onchoose: (name: string, on: boolean) => void;
    /** Hears the repositories the part reads. */
    onrepos?: (repos: Repo[]) => void;
  } = $props();

  const rehearses = (r?: Repo) => (typeof r?.fields?.rehearsal_for === "string" ? r.fields.rehearsal_for : "");
  const hint = (r?: Repo) => (rehearses(r) ? `Rehearsal for ${rehearses(r)}: publish here first` : "");

  // Every repository the part has read, by name: the chips take their icon and hint from it.
  const known = new SvelteMap<string, Repo>();
  function learn(repos: Repo[]) {
    for (const r of repos) known.set(r.name, r);
    if (repos.length) onrepos?.(repos);
  }

  const found = new Searched(
    (q) =>
      new Keyset(async (p) => {
        const page = await api.repos.list({ perm: ["ci.pipelines"], kind: "hosted", q: q.toLowerCase() }, p);
        learn(page.items);
        return page;
      }, 100),
  );

  // The chosen repositories that no page showed yet (an existing pipeline, a repository to start
  // from): read by name, 200 at a time.
  const asked = new Set<string>();
  $effect(() => {
    const ask = chosen.filter((n) => !known.has(n) && !asked.has(n));
    for (const n of ask) asked.add(n);
    for (let i = 0; i < ask.length; i += 200) {
      const part = ask.slice(i, i + 200);
      api.repos.named(part).then(learn, () => part.forEach((n) => asked.delete(n)));
    }
  });

  // A rehearsal repository right before the repository it rehearses, within the loaded pages only.
  const rows = $derived.by(() => {
    const items = found.items;
    const here = new Set(items.map((r) => r.name));
    const before = new Map<string, Repo[]>();
    for (const r of items) {
      const t = rehearses(r);
      if (t && t !== r.name && here.has(t)) before.set(t, [...(before.get(t) ?? []), r]);
    }
    const out: Repo[] = [];
    for (const r of items) {
      const t = rehearses(r);
      if (t && t !== r.name && here.has(t)) continue;
      out.push(...(before.get(r.name) ?? []), r);
    }
    return out;
  });

  const picked = $derived(new Set(chosen));

  // The search shows once there is a repository to choose, also while it finds none.
  let any = $state(false);
  $effect(() => {
    if (found.items.length > 0) any = true;
  });

  let grid = $state<HTMLElement | null>(null);
  // Enter chooses the first result for the typed text, once its page is here.
  let pickFirst = $state(false);
  $effect(() => {
    if (!pickFirst || found.pending) return;
    pickFirst = false;
    const first = rows.find((r) => !picked.has(r.name));
    if (found.query && first) onchoose(first.name, true);
  });
  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      // Enter in the field never submits the form.
      e.preventDefault();
      found.flush();
      pickFirst = true;
    } else if (e.key === "ArrowDown") {
      const box = grid?.querySelector<HTMLElement>("[role=checkbox]");
      if (box) {
        e.preventDefault();
        box.focus();
      }
    }
  }
</script>

<div class="@container flex flex-col gap-4">
  <ChosenChips
    label="Chosen repositories"
    items={chosen.map((name) => ({ key: name, label: name, hint: hint(known.get(name)) || undefined }))}
    none={any ? "None chosen yet." : undefined}
    onremove={(c) => onchoose(c.key, false)}
  >
    {#snippet icon(c)}<FormatIcon format={known.get(c.key)?.format ?? ""} />{/snippet}
  </ChosenChips>

  {#if any || found.text.trim()}
    <InputGroup.Root>
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input
        id="{id}-find"
        bind:value={found.text}
        type="search"
        autocomplete="off"
        spellcheck={false}
        placeholder="Find a repository by name"
        aria-label="Find a repository by name"
        oninput={() => (pickFirst = false)}
        onkeydown={onKey}
      />
    </InputGroup.Root>
  {/if}

  {#if found.loading && found.items.length === 0}
    <Loading rows={3} />
  {:else if found.error && found.items.length === 0}
    <ErrorState error={found.error} title="The repositories did not load" onRetry={found.reload} />
  {:else if rows.length === 0 && found.query}
    <CompactEmpty icon={SearchXIcon} text="No repository's name holds “{found.query}”." />
  {:else if rows.length === 0}
    {#if auth.can("repositories.all")}
      <CompactEmpty icon={DatabaseIcon} text="Create a repository first, then come back to choose it.">
        <Button variant="outline" href={href("/settings/repositories/new")}><PlusIcon />New repository</Button>
      </CompactEmpty>
    {:else}
      <CompactEmpty icon={DatabaseIcon} text="You hold CI pipelines on no repository that takes publishes." />
    {/if}
  {:else}
    <!-- One-line rows in their own scrolling area; the end that cuts rows off fades. -->
    <div
      bind:this={grid}
      use:fadeEdges={"y"}
      class="grid max-h-96 grid-cols-1 content-start gap-1 overflow-y-auto @min-[26rem]:grid-cols-2 @min-[40rem]:grid-cols-3"
      role="group"
      aria-label="Repositories"
    >
      {#each rows as r (r.name)}
        <OptionRow id="{id}-repo-{r.name}" label={r.name} description={hint(r) || undefined} tooltip="{r.name} ({formats.title(r.format)})" compact>
          {#snippet icon()}<FormatIcon format={r.format} />{/snippet}
          {#snippet control()}
            <Checkbox id="{id}-repo-{r.name}" checked={picked.has(r.name)} onCheckedChange={(on) => onchoose(r.name, on === true)} />
          {/snippet}
        </OptionRow>
      {/each}
      <div class="col-span-full"><SourceMore source={found} auto /></div>
    </div>
  {/if}
</div>
