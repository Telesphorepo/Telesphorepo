<!--
  A short form that sets repository permissions: the things to give them to, then the permissions
  (PermissionPicker). The things are one of:
  - `items`: a list with a filter field, Select all and Select none (a person's page lists the
    repositories);
  - `search`: a field that finds people by the start of the name (the Access tab);
  - `targets`: fixed things, for a change of what they hold now (no list).
  `held` limits the checkboxes to the permissions the person can give (a list, or a function of the
  chosen things). onGive makes the change; on
  an error the dialog stays open and shows it.

    <GrantDialog bind:open title="Give alice access" noun={["repository", "repositories"]}
      items={[{ id: "maven", label: "maven", detail: "Private" }]} none="alice can use every repository."
      submitLabel={(n) => `Give access to ${n} repositories`} onGive={(ids, perms) => api.permissions.set(["alice"], ids, perms)} />
-->
<script lang="ts" module>
  export type GrantChoice = { id: string; label: string; detail?: string };
</script>

<script lang="ts">
  import { untrack } from "svelte";
  import type { RepoPermission } from "$lib/api";
  import { errorMessage } from "$lib/api";
  import { REPO_NEEDS, REPO_PICKER_GROUPS } from "$lib/permissions";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import PermissionPicker from "$lib/components/PermissionPicker.svelte";

  let {
    open = $bindable(false),
    title,
    description,
    noun,
    items,
    search,
    exclude = [],
    targets,
    loading = false,
    error = "",
    none = "",
    start = ["download"],
    held,
    submitLabel,
    onGive,
  }: {
    open?: boolean;
    title: string;
    description?: string;
    /** The thing in the list, singular and plural: ["person", "people"]. */
    noun: [string, string];
    /** The things to choose from. */
    items?: GrantChoice[];
    /** Finds things by the start of their name, in place of `items`. */
    search?: (q: string) => Promise<GrantChoice[]>;
    /** Ids that `search` leaves out (people who have permissions already). */
    exclude?: string[];
    /** Fixed things: the dialog changes what they hold and shows no list. */
    targets?: string[];
    loading?: boolean;
    error?: string;
    /** One sentence for an empty `items` list. */
    none?: string;
    /** The permissions that the dialog starts with. */
    start?: RepoPermission[];
    /** The permissions the person can give and remove on the chosen things; undefined: every one. */
    held?: RepoPermission[] | ((ids: string[]) => RepoPermission[] | undefined);
    /** Names the action for the count of chosen things and the permissions. */
    submitLabel: (n: number, perms: RepoPermission[]) => string;
    onGive: (ids: string[], perms: RepoPermission[]) => unknown | Promise<unknown>;
  } = $props();

  const id = $props.id();
  let filter = $state("");
  let chosen = $state<string[]>([]);
  let perms = $state<RepoPermission[]>([]);
  // Results of `search`; chosen people stay in the list while the query changes.
  let found = $state<GrantChoice[]>([]);
  let searching = $state(false);
  let searchError = $state("");
  let known = $state<Record<string, GrantChoice>>({});

  $effect(() => {
    if (open)
      untrack(() => {
        filter = "";
        chosen = [];
        perms = [...start];
        found = [];
        searchError = "";
        known = {};
      });
  });

  // `search` runs 250 ms after the last key.
  $effect(() => {
    if (!open || !search) return;
    const q = filter.trim().toLowerCase();
    const run = search;
    let live = true;
    const t = setTimeout(async () => {
      searching = true;
      try {
        const r = await run(q);
        if (!live) return;
        found = r.filter((x) => !exclude.includes(x.id));
        searchError = "";
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

  const shown = $derived.by(() => {
    if (search) {
      const picked = chosen.map((c) => known[c]).filter((x): x is GrantChoice => !!x);
      return [...picked, ...found.filter((x) => !chosen.includes(x.id))];
    }
    const q = filter.trim().toLowerCase();
    const all = items ?? [];
    return q ? all.filter((i) => i.label.toLowerCase().includes(q)) : all;
  });
  function toggle(item: GrantChoice, on: boolean) {
    if (on) known = { ...known, [item.id]: item };
    chosen = on ? [...chosen, item.id] : chosen.filter((x) => x !== item.id);
  }
  const selectAll = () => (chosen = [...new Set([...chosen, ...shown.map((i) => i.id)])]);
  const selectNone = () => (chosen = []);

  const ids = $derived(targets ?? chosen);
  const heldNow = $derived(typeof held === "function" ? held(ids) : held);
  // A change of fixed things can clear every permission (that removes the access); a new grant needs one.
  const canSubmit = $derived(ids.length > 0 && (!!targets || perms.length > 0));
</script>

<ShortFormDialog bind:open {title} {description} submitLabel={submitLabel(ids.length, perms)} {canSubmit} onSubmit={() => onGive(ids, perms)}>
  {#if !targets}
    {#if search}
      <FormField label="Find a {noun[0]}" for="{id}-filter" hint="Type the start of the name.">
        <Input id="{id}-filter" type="search" autocomplete="off" spellcheck={false} bind:value={filter} />
      </FormField>
    {:else if loading && !items?.length}
      <Loading rows={3} />
    {:else if error}
      <ErrorState inline {error} />
    {:else if items?.length}
      <FormField label="Filter" for="{id}-filter">
        <Input id="{id}-filter" type="search" autocomplete="off" spellcheck={false} bind:value={filter} />
      </FormField>
    {:else}
      <p class="text-body text-muted-foreground">{none}</p>
    {/if}
    {#if search || items?.length}
      <div class="flex flex-col gap-2">
        <div class="flex items-center gap-1">
          <span class="text-meta text-muted-foreground">{chosen.length} selected</span>
          {#if !search}
            <Button class="ml-auto" variant="ghost" size="xs" disabled={!shown.length} onclick={selectAll}>Select all</Button>
          {/if}
          <Button class={search ? "ml-auto" : ""} variant="ghost" size="xs" disabled={!chosen.length} onclick={selectNone}>Select none</Button>
        </div>
        <div class="-mx-3 flex max-h-56 flex-col overflow-y-auto" role="group" aria-label={noun[1]}>
          {#if searchError}
            <ErrorState inline error={searchError} />
          {/if}
          {#each shown as item (item.id)}
            <OptionRow id="{id}-{item.id}" label={item.label} description={item.detail}>
              {#snippet control()}
                <Checkbox id="{id}-{item.id}" checked={chosen.includes(item.id)} onCheckedChange={(v) => toggle(item, v === true)} />
              {/snippet}
            </OptionRow>
          {:else}
            {#if search && searching}
              <Loading inline />
            {:else if !searchError}
              <p class="px-3 py-2 text-body text-muted-foreground">No {noun[1]} match.</p>
            {/if}
          {/each}
        </div>
      </div>
    {/if}
  {/if}
  <PermissionPicker id="{id}-perms" groups={REPO_PICKER_GROUPS} needs={REPO_NEEDS} held={heldNow} bind:value={perms} />
</ShortFormDialog>
