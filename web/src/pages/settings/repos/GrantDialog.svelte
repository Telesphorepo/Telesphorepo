<!--
  The grant dialog: repository permissions for people or repositories, in one of two modes.

  - `search`: Give access. A wide dialog of two columns of one fixed height, each scrolling on its
    own (stacked on a phone). Left, Available: a field that finds things by the start of the name
    (`search`, a page at a time from the server, with Show more; an empty field shows the first
    page) over the results. A row adds with a click, Enter, its "+" or a drag into the right
    column, and Add all shown adds every row of the results; a row never moves: an added one stays
    where it is with a check, and a thing in `exclude` (it holds access already) shows "Has
    access". Right, Giving access: the added things in the order they were added, the newest at
    the bottom, scrolled into view and briefly tinted (not under reduced motion); the whole column
    takes a drop. On top, Permissions for new items (`start`, editable) with Apply to all. Each
    thing is a card: its name and its permissions at a glance (the labels of the registry, or "No
    access" in the warning color while it has none); a click opens its PermissionPicker (one card
    open at a time), × removes it. Submit stays off while a thing has no permission, and the
    column says why. onGive gets every thing with its own set.
  - `targets`: Change permissions. Fixed things and one PermissionPicker (no list); onGive gets
    each target with the one set.

  `held` limits the checkboxes to the permissions the person can give (a list, or a function of
  the things it is asked for: one thing for its card). On an error the dialog stays open and
  shows it.

    <GrantDialog bind:open title="Give alice access" noun={["repository", "repositories"]} search={findRepos}
      submitLabel={(n) => `Give access to ${n} repositories`}
      onGive={(items) => api.grants.setGroups(groupsOf(items))} />
-->
<script lang="ts" module>
  import type { PermId as Perm } from "$lib/api";
  export type GrantChoice = { id: string; label: string; detail?: string };
  /** One thing of the dialog with the permissions it gets. */
  export type GrantItem = { id: string; perms: Perm[] };

  /** Groups items by identical permission sets, in the order the sets first appear. */
  export function bySet(items: GrantItem[]): { ids: string[]; perms: Perm[] }[] {
    const out = new Map<string, { ids: string[]; perms: Perm[] }>();
    for (const i of items) {
      const perms = [...i.perms].sort();
      const key = perms.join(",");
      const g = out.get(key);
      if (g) g.ids.push(i.id);
      else out.set(key, { ids: [i.id], perms: i.perms });
    }
    return [...out.values()];
  }
</script>

<script lang="ts">
  import { tick, untrack, type Snippet } from "svelte";
  import type { Page, PermId } from "$lib/api";
  import { errorMessage } from "$lib/api";
  import { labelOf, needsOf, pickerGroups, repoPermissions, sortRepoPermissions } from "$lib/permissions";
  import { count } from "$lib/words";
  import { cn } from "$lib/utils";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import PermissionPicker from "$lib/components/PermissionPicker.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import CheckIcon from "@lucide/svelte/icons/check";
  import XIcon from "@lucide/svelte/icons/x";
  import GripVerticalIcon from "@lucide/svelte/icons/grip-vertical";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import SearchIcon from "@lucide/svelte/icons/search";
  import TriangleAlertIcon from "@lucide/svelte/icons/triangle-alert";
  import ListPlusIcon from "@lucide/svelte/icons/list-plus";

  let {
    open = $bindable(false),
    title,
    description,
    noun,
    search,
    exclude = [],
    targets,
    none = "",
    start = ["repo.download"],
    held,
    leading,
    submitLabel,
    onGive,
  }: {
    open?: boolean;
    title: string;
    description?: string;
    /** The thing in the list, singular and plural: ["person", "people"]. */
    noun: [string, string];
    /** A page of the things whose name starts with q, after the cursor `after`. */
    search?: (q: string, after?: string) => Promise<Page<GrantChoice>>;
    /** Ids that hold access already: they show as such and cannot be added. */
    exclude?: string[];
    /** Fixed things: the dialog changes what they hold and shows no list. */
    targets?: string[];
    /** One sentence for an empty first page of `search` with an empty field. */
    none?: string;
    /** The permissions that the dialog starts with (Permissions for new items). */
    start?: PermId[];
    /** The permissions the person can give and remove on the things; undefined: every one. */
    held?: PermId[] | ((ids: string[]) => PermId[] | undefined);
    /** The mark before a thing's name: a FormatIcon, a small UserAvatar. */
    leading?: Snippet<[GrantChoice]>;
    /** Names the action for the count of things (and, for `targets`, the permissions). */
    submitLabel: (n: number, perms: PermId[]) => string;
    onGive: (items: GrantItem[]) => unknown | Promise<unknown>;
  } = $props();

  const id = $props.id();
  const reduce = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  const heldOn = (ids: string[]) => (typeof held === "function" ? held(ids) : held);
  /** Keeps of perms the ones the person can give on the thing id. */
  const keepHeld = (thing: string, perms: PermId[]) => {
    const h = heldOn([thing]);
    return h ? perms.filter((p) => h.includes(p)) : [...perms];
  };

  // --- targets: one set for fixed things ---
  let perms = $state<PermId[]>([]);

  // --- search: the left column ---
  let filter = $state("");
  let found = $state<GrantChoice[]>([]);
  let next = $state<string | null>(null);
  let searching = $state(false);
  let loadingMore = $state(false);
  let searchError = $state("");
  let searched = $state(false);

  // --- the right column ---
  type Chosen = GrantChoice & { perms: PermId[] };
  let chosen = $state<Chosen[]>([]);
  let defaults = $state<PermId[]>([]);
  // The open card: a thing's id, "" for Permissions for new items, null for none.
  let expanded = $state<string | null>(null);
  let dragging = $state<GrantChoice | null>(null);
  let over = $state(false);
  let said = $state("");
  let listEl = $state<HTMLElement | null>(null);

  $effect(() => {
    if (open)
      untrack(() => {
        perms = [...start];
        filter = "";
        found = [];
        next = null;
        searchError = "";
        searched = false;
        chosen = [];
        defaults = [...start];
        expanded = null;
        said = "";
      });
  });

  // `search` runs 250 ms after the last key; a new query drops the pages of the last one.
  $effect(() => {
    if (!open || !search || targets) return;
    const q = filter.trim().toLowerCase();
    const run = search;
    let live = true;
    const t = setTimeout(async () => {
      searching = true;
      try {
        const r = await run(q);
        if (!live) return;
        found = r.items;
        next = r.next;
        searchError = "";
        searched = true;
      } catch (e) {
        if (live) searchError = errorMessage(e);
      } finally {
        if (live) searching = false;
      }
    }, 250);
    return () => {
      live = false;
      clearTimeout(t);
    };
  });

  async function more() {
    if (!search || !next || loadingMore) return;
    const q = filter.trim().toLowerCase();
    const after = next;
    loadingMore = true;
    try {
      const r = await search(q, after);
      // A new query while this page loaded owns the list now.
      if (q !== filter.trim().toLowerCase() || after !== next) return;
      const seen = new Set(found.map((f) => f.id));
      found = [...found, ...r.items.filter((x) => !seen.has(x.id))];
      next = r.next;
    } catch (e) {
      searchError = errorMessage(e);
    } finally {
      loadingMore = false;
    }
  }

  const chosenIds = $derived(new Set(chosen.map((c) => c.id)));
  const excluded = $derived(new Set(exclude));
  const addable = (c: GrantChoice) => !chosenIds.has(c.id) && !excluded.has(c.id);
  const addableShown = $derived(found.filter(addable));

  async function show(thing: string) {
    await tick();
    const el = listEl?.querySelector<HTMLElement>(`[data-thing="${CSS.escape(thing)}"]`);
    if (!el) return;
    el.scrollIntoView({ block: "nearest", behavior: reduce ? "auto" : "smooth" });
    if (reduce) return;
    const brand = getComputedStyle(el).getPropertyValue("--primary").trim();
    if (brand)
      el.animate([{ backgroundColor: `color-mix(in oklch, ${brand} 30%, transparent)` }, { backgroundColor: "transparent" }], {
        duration: 1200,
        easing: "ease-out",
      });
  }

  function add(things: GrantChoice[]) {
    const fresh = things.filter(addable);
    if (!fresh.length) return;
    chosen = [...chosen, ...fresh.map((c) => ({ ...c, perms: keepHeld(c.id, defaults) }))];
    said = fresh.length === 1 ? `Added ${fresh[0].label}.` : `Added ${count(fresh.length, noun[0], noun[1])}.`;
    show(fresh[fresh.length - 1].id);
  }
  /** A click on a row: adds it, or shows its card when it is added already. */
  function pick(c: GrantChoice) {
    if (chosenIds.has(c.id)) show(c.id);
    else add([c]);
  }
  function remove(thing: string) {
    const c = chosen.find((x) => x.id === thing);
    chosen = chosen.filter((x) => x.id !== thing);
    if (expanded === thing) expanded = null;
    if (c) said = `Removed ${c.label}.`;
  }
  function setPerms(thing: string, p: PermId[]) {
    chosen = chosen.map((c) => (c.id === thing ? { ...c, perms: p } : c));
  }
  function applyAll() {
    chosen = chosen.map((c) => ({ ...c, perms: keepHeld(c.id, defaults) }));
    said = `Every ${noun[0]} gets the permissions for new items.`;
  }
  const toggle = (key: string) => (expanded = expanded === key ? null : key);

  function onSearchKey(e: KeyboardEvent) {
    if (e.key !== "Enter") return;
    // Enter in the field adds the first result instead of submitting the dialog.
    e.preventDefault();
    if (filter.trim() && addableShown.length) add([addableShown[0]]);
  }
  function onRowKey(e: KeyboardEvent, c: GrantChoice) {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      pick(c);
    }
  }

  // Drag and drop: a row of the left column onto the right column.
  function dragStart(e: DragEvent, c: GrantChoice) {
    if (!addable(c)) return e.preventDefault();
    dragging = c;
    e.dataTransfer?.setData("text/plain", c.label);
    if (e.dataTransfer) e.dataTransfer.effectAllowed = "copy";
  }
  function dragEnd() {
    dragging = null;
    over = false;
  }
  function dragOver(e: DragEvent) {
    if (!dragging) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
    over = true;
  }
  function dragLeave(e: DragEvent) {
    if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node | null)) over = false;
  }
  function drop(e: DragEvent) {
    if (!dragging) return;
    e.preventDefault();
    add([dragging]);
    dragEnd();
  }

  const everything = $derived(repoPermissions().length);
  const missing = $derived(chosen.filter((c) => !c.perms.length));

  const ids = $derived(targets ?? chosen.map((c) => c.id));
  // A change of fixed things can clear every permission (that removes the access); a new grant needs one each.
  const canSubmit = $derived(targets ? targets.length > 0 : chosen.length > 0 && missing.length === 0);
  const submit = () => onGive(targets ? targets.map((t) => ({ id: t, perms })) : chosen.map((c) => ({ id: c.id, perms: c.perms })));
</script>

{#snippet summary(p: PermId[])}
  {#if !p.length}
    <StatusBadge tone="warning" icon={TriangleAlertIcon}>No access</StatusBadge>
  {:else if p.length === everything}
    <StatusBadge>Everything</StatusBadge>
  {:else}
    {#each sortRepoPermissions(p) as x (x)}<StatusBadge>{labelOf(x)}</StatusBadge>{/each}
  {/if}
{/snippet}

<ShortFormDialog wide={!!targets} full={!targets} bind:open {title} {description} submitLabel={submitLabel(ids.length, targets ? perms : [])} {canSubmit} onSubmit={submit}>
  {#if targets}
    <PermissionPicker id="{id}-perms" groups={pickerGroups("repo")} needs={needsOf("repo")} held={heldOn(targets)} bind:value={perms} />
  {:else}
    <div class="grid gap-4 md:grid-cols-2">
      <!-- Available -->
      <section class="flex h-96 min-w-0 flex-col rounded-lg border bg-card md:h-[60vh]" aria-labelledby="{id}-avail">
        <div class="flex flex-col gap-3 border-b p-4">
          <div class="flex items-center gap-2">
            <h3 id="{id}-avail" class="text-label">Available</h3>
            <Button class="-mr-2.5 ml-auto" variant="ghost" size="xs" disabled={!addableShown.length} onclick={() => add(addableShown)}>
              <ListPlusIcon />Add all shown
            </Button>
          </div>
          <div class="relative">
            <SearchIcon class="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
            <Input
              id="{id}-filter"
              class="pl-9"
              type="search"
              autocomplete="off"
              spellcheck={false}
              placeholder="Find a {noun[0]} by the start of the name"
              aria-label="Find a {noun[0]}"
              bind:value={filter}
              onkeydown={onSearchKey}
            />
          </div>
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable] p-2">
          {#if searchError}
            <div class="p-2"><ErrorState inline error={searchError} /></div>
          {/if}
          {#if !searched && searching}
            <div class="p-2"><Loading inline /></div>
          {:else if searched && !found.length && !searchError}
            <p class="px-2 py-2 text-body text-muted-foreground">{filter.trim() || !none ? `No ${noun[1]} match.` : none}</p>
          {/if}
          <ul class="flex flex-col gap-1" aria-label="Available {noun[1]}">
            {#each found as c (c.id)}
              {@const isAdded = chosenIds.has(c.id)}
              {@const has = excluded.has(c.id)}
              <li>
                <div
                  role="button"
                  tabindex={has ? -1 : 0}
                  aria-disabled={has}
                  aria-label={has ? `${c.label}: has access` : isAdded ? `${c.label}: added, show it` : `Add ${c.label}`}
                  draggable={!isAdded && !has}
                  ondragstart={(e) => dragStart(e, c)}
                  ondragend={dragEnd}
                  onclick={() => !has && pick(c)}
                  onkeydown={(e) => !has && onRowKey(e, c)}
                  class={cn(
                    "group flex min-h-12 items-center gap-2 rounded-lg px-2 py-1.5 outline-none select-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
                    has ? "cursor-default text-muted-foreground" : isAdded ? "cursor-pointer text-muted-foreground hover:bg-hover" : "cursor-grab hover:bg-hover active:cursor-grabbing",
                    dragging?.id === c.id && "opacity-50",
                  )}
                >
                  <GripVerticalIcon class={cn("size-4 shrink-0 text-muted-foreground", (isAdded || has) && "invisible")} aria-hidden="true" />
                  {#if leading}<span class="flex shrink-0 items-center">{@render leading(c)}</span>{/if}
                  <span class="flex min-w-0 flex-1 flex-col">
                    <span class="truncate text-label {isAdded || has ? 'font-normal' : ''}">{c.label}</span>
                    {#if c.detail}<span class="truncate text-meta text-muted-foreground">{c.detail}</span>{/if}
                  </span>
                  {#if has}
                    <span class="shrink-0 text-meta text-muted-foreground">Has access</span>
                  {:else if isAdded}
                    <span class="flex shrink-0 items-center gap-1 text-meta text-muted-foreground"><CheckIcon class="size-4" aria-hidden="true" />Added</span>
                  {:else}
                    <span
                      class="flex size-8 shrink-0 items-center justify-center rounded-lg border bg-input/30 text-foreground group-hover:bg-input/50"
                      aria-hidden="true"><PlusIcon class="size-4" /></span
                    >
                  {/if}
                </div>
              </li>
            {/each}
          </ul>
          {#if next}
            <div class="p-2">
              <Button variant="ghost" size="sm" class="-ml-3" disabled={loadingMore} onclick={more}>{loadingMore ? "Loading" : "Show more"}</Button>
            </div>
          {/if}
        </div>
      </section>

      <!-- Giving access: the whole column takes a drop. -->
      <section
        class={cn(
          "flex h-96 min-w-0 flex-col rounded-lg border bg-card transition-colors md:h-[60vh]",
          dragging && "border-dashed border-primary",
          over && "bg-selected",
        )}
        aria-labelledby="{id}-giving"
        ondragenter={dragOver}
        ondragover={dragOver}
        ondragleave={dragLeave}
        ondrop={drop}
      >
        <div class="flex flex-col gap-3 border-b p-4">
          <div class="flex items-center gap-2">
            <h3 id="{id}-giving" class="text-label">Giving access</h3>
            <span class="text-meta text-muted-foreground">{chosen.length ? count(chosen.length, noun[0], noun[1]) : ""}</span>
          </div>
          <!-- Permissions for new items -->
          <div class="rounded-lg bg-panel">
            <div class="flex items-center gap-2 p-2">
              <button
                type="button"
                class="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-1 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                aria-expanded={expanded === ""}
                aria-controls="{id}-defaults"
                onclick={() => toggle("")}
              >
                <ChevronDownIcon class={cn("size-4 shrink-0 text-muted-foreground transition-transform", expanded !== "" && "-rotate-90")} aria-hidden="true" />
                <span class="shrink-0 text-body">Permissions for new {noun[1]}</span>
                <span class="flex min-w-0 flex-wrap gap-1">{@render summary(defaults)}</span>
              </button>
              <Button variant="outline" size="xs" disabled={!chosen.length} onclick={applyAll}>Apply to all</Button>
            </div>
            {#if expanded === ""}
              <div id="{id}-defaults" class="max-h-64 overflow-y-auto [scrollbar-gutter:stable] px-4 pb-3">
                <PermissionPicker
                  id="{id}-defaults-perms"
                  groups={pickerGroups("repo")}
                  needs={needsOf("repo")}
                  held={heldOn(chosen.map((c) => c.id))}
                  bind:value={defaults}
                />
              </div>
            {/if}
          </div>
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable] p-2" bind:this={listEl}>
          {#if !chosen.length}
            <div
              class={cn(
                "flex h-full flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-6 text-center",
                over ? "border-primary" : "border-border",
              )}
            >
              <PlusIcon class="size-6 text-muted-foreground" aria-hidden="true" />
              <p class="text-body">Drag {noun[1]} here or press +</p>
              <p class="text-meta text-muted-foreground">Each one gets the permissions for new {noun[1]}; open it to change its own.</p>
            </div>
          {:else}
            <ul class="flex flex-col gap-2" aria-label="{noun[1]} getting access">
              {#each chosen as c (c.id)}
                {@const isOpen = expanded === c.id}
                <li data-thing={c.id} class="rounded-lg border bg-card">
                  <div class="flex items-center gap-1 p-1">
                    <button
                      type="button"
                      class="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-2 py-1.5 text-left outline-none hover:bg-hover focus-visible:ring-[3px] focus-visible:ring-ring/50"
                      aria-expanded={isOpen}
                      aria-controls="{id}-card-{c.id}"
                      onclick={() => toggle(c.id)}
                    >
                      <ChevronDownIcon class={cn("size-4 shrink-0 text-muted-foreground transition-transform", !isOpen && "-rotate-90")} aria-hidden="true" />
                      {#if leading}<span class="flex shrink-0 items-center">{@render leading(c)}</span>{/if}
                      <span class="flex min-w-0 flex-1 flex-col gap-1 sm:flex-row sm:items-center sm:gap-3">
                        <span class="truncate text-label">{c.label}</span>
                        <span class="flex flex-wrap gap-1">{@render summary(c.perms)}</span>
                      </span>
                    </button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      class="hover:bg-destructive/10 hover:text-destructive"
                      aria-label="Remove {c.label}"
                      onclick={() => remove(c.id)}><XIcon /></Button
                    >
                  </div>
                  {#if !c.perms.length && !isOpen}
                    <p class="px-3 pb-2 text-meta text-warning">Open it to pick a permission, or remove it.</p>
                  {/if}
                  {#if isOpen}
                    <div id="{id}-card-{c.id}" class="border-t px-4 py-3">
                      <PermissionPicker
                        id="{id}-p-{c.id}"
                        groups={pickerGroups("repo")}
                        needs={needsOf("repo")}
                        held={heldOn([c.id])}
                        value={c.perms}
                        onchange={(v) => setPerms(c.id, v)}
                      />
                    </div>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </div>
        {#if missing.length}
          <p class="flex items-center gap-2 border-t px-4 py-2 text-meta text-warning">
            <TriangleAlertIcon class="size-4 shrink-0" aria-hidden="true" />
            {missing.length === 1 ? `${missing[0].label} has` : `${count(missing.length, noun[0], noun[1])} have`} no permission yet: pick one or remove {missing.length === 1 ? "it" : "them"}.
          </p>
        {/if}
      </section>
    </div>
    <p class="sr-only" aria-live="polite">{said}</p>
  {/if}
</ShortFormDialog>
