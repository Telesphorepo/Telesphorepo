<!--
  A short form that adds existing things to a repository (the members of a group). The dialog is the wide one (768px), since each row has a line of help. Each thing
  is a checkbox row, a 4px gap between rows; the submit button names how many it adds. A link
  under the rows creates a new one on its own page. With `more` (a paged source of the items),
  "Load more" under the rows asks for the next page. With `search` (a `Searched` of the parent), a
  field over the rows finds things by name; Enter chooses the first one not chosen yet, and the
  choices of an earlier search stay chosen. The parent adds the chosen ids to its draft, and the
  page's save bar saves them.

    <ChoiceDialog bind:open title="Add CI pipelines to maven" noun={["CI pipeline", "CI pipelines"]}
      items={[{ id: "3", label: "release", detail: "GitHub Actions · acme/app" }]}
      none="Every CI pipeline can publish to maven already."
      newLabel="New CI pipeline" newHref={href("/settings/ci-pipelines/new", { repo: "maven" })}
      onAdd={(ids) => ...} />
-->
<script lang="ts" module>
  export type Choice = { id: string; label: string; detail: string };
</script>

<script lang="ts">
  import { untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import SourceMore from "$lib/components/SourceMore.svelte";
  import * as InputGroup from "$lib/components/ui/input-group";
  import type { Searched, TableSource } from "$lib/load.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import SearchIcon from "@lucide/svelte/icons/search";

  let {
    open = $bindable(false),
    title,
    description,
    noun,
    items,
    loading = false,
    error = "",
    none,
    newLabel,
    newHref,
    more,
    search,
    notFound,
    onAdd,
  }: {
    open?: boolean;
    title: string;
    description?: string;
    /** The thing, singular and plural, for the submit button: "Add 2 CI pipelines". */
    noun: [string, string];
    /** The things that are not on the repository yet. */
    items: Choice[];
    loading?: boolean;
    error?: string;
    /** One sentence for an empty list. */
    none: string;
    newLabel: string;
    /** The page that creates a new one; undefined when the person cannot create one. */
    newHref?: string;
    /** The paged source that `items` come from: its next page. */
    more?: TableSource<unknown>;
    /** The search over the items: a field shows over the rows. */
    search?: Searched<unknown>;
    /** One sentence for a search that finds nothing to add. */
    notFound?: (q: string) => string;
    onAdd: (ids: string[]) => void;
  } = $props();

  const id = $props.id();
  let chosen = $state<string[]>([]);
  $effect(() => {
    if (open) untrack(() => ((chosen = []), search?.clear()));
  });

  // Enter in the field chooses the first thing for the typed text, once its page is here, and
  // never submits the dialog.
  let pickFirst = $state(false);
  $effect(() => {
    if (!pickFirst || !search || search.pending) return;
    pickFirst = false;
    const first = items.find((i) => !chosen.includes(i.id));
    if (search.query && first) chosen = [...chosen, first.id];
  });
  function onKey(e: KeyboardEvent) {
    if (e.key !== "Enter") return;
    e.preventDefault();
    search?.flush();
    pickFirst = true;
  }

  const toggle = (key: string, on: boolean) => (chosen = on ? [...chosen, key] : chosen.filter((x) => x !== key));
  const submitLabel = $derived(chosen.length === 0 ? "Add" : `Add ${chosen.length} ${chosen.length === 1 ? noun[0] : noun[1]}`);
</script>

<ShortFormDialog bind:open wide {title} {description} {submitLabel} canSubmit={chosen.length > 0} onSubmit={() => onAdd(chosen)}>
  {#if search}
    <InputGroup.Root>
      <InputGroup.Addon><SearchIcon /></InputGroup.Addon>
      <InputGroup.Input
        id="{id}-find"
        bind:value={search.text}
        type="search"
        autocomplete="off"
        spellcheck={false}
        placeholder="Find a {noun[0]} by name"
        aria-label="Find a {noun[0]} by name"
        oninput={() => (pickFirst = false)}
        onkeydown={onKey}
      />
    </InputGroup.Root>
  {/if}
  {#if loading && !items.length}
    <Loading rows={3} />
  {:else if error}
    <ErrorState inline {error} />
  {:else if items.length}
    <!-- A gap between the rows keeps the fills of two chosen rows apart. -->
    <div class="-mx-3 flex max-h-[min(28rem,55dvh)] flex-col gap-1 overflow-y-auto" role="group" aria-label={noun[1]}>
      {#each items as item (item.id)}
        <OptionRow id="{id}-{item.id}" label={item.label} description={item.detail}>
          {#snippet control()}
            <Checkbox id="{id}-{item.id}" checked={chosen.includes(item.id)} onCheckedChange={(v) => toggle(item.id, v === true)} />
          {/snippet}
        </OptionRow>
      {/each}
      {#if more}<SourceMore source={more} />{/if}
    </div>
  {:else}
    <p class="text-body text-muted-foreground">{search?.query ? (notFound?.(search.query) ?? `No ${noun[0]}'s name holds “${search.query}”.`) : none}</p>
    {#if more}<SourceMore source={more} />{/if}
  {/if}
  {#if newHref}
    <div>
      <Button variant="outline" size="sm" href={newHref} onclick={() => (open = false)}><PlusIcon />{newLabel}</Button>
    </div>
  {/if}
  {#if items.length}<p class="text-meta text-muted-foreground">Save the page to keep the change.</p>{/if}
</ShortFormDialog>
