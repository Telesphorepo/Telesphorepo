<!--
  A short form that gives access to many at once: a filter field, a list of checkbox rows with
  Select all and Select none, and the level (Download or Publish). A person's page lists the
  repositories that the person cannot reach yet; a repository's Access tab lists the people who
  have no access yet. onGive makes the change; on an error the dialog stays open and shows it.

    <GrantDialog bind:open title="Give alice access" noun={["repository", "repositories"]}
      items={[{ id: "maven", label: "maven", detail: "Private" }]} none="alice can use every repository."
      submitLabel={(n) => `Give access to ${n} repositories`} onGive={(ids, level) => api.permissions.set(["alice"], ids, level)} />
-->
<script lang="ts" module>
  export type GrantChoice = { id: string; label: string; detail?: string };
</script>

<script lang="ts">
  import { untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import type { Level } from "./access";

  let {
    open = $bindable(false),
    title,
    description,
    noun,
    items,
    loading = false,
    error = "",
    none,
    startLevel = "read",
    canPublish = true,
    publishNote,
    submitLabel,
    onGive,
  }: {
    open?: boolean;
    title: string;
    description?: string;
    /** The thing in the list, singular and plural: ["person", "people"]. */
    noun: [string, string];
    /** The things that have no access yet. */
    items: GrantChoice[];
    loading?: boolean;
    error?: string;
    /** One sentence for an empty list. */
    none: string;
    /** The level that the dialog starts with. */
    startLevel?: Level;
    /** False when the level cannot be Publish (a single sign-on account). */
    canPublish?: boolean;
    /** Why Publish is off, under the choice. */
    publishNote?: string;
    /** Names the action for the count of chosen things and the level. */
    submitLabel: (n: number, level: Level) => string;
    onGive: (ids: string[], level: Level) => unknown | Promise<unknown>;
  } = $props();

  const id = $props.id();
  let filter = $state("");
  let chosen = $state<string[]>([]);
  let level = $state<Level>("read");
  $effect(() => {
    if (open)
      untrack(() => {
        filter = "";
        chosen = [];
        level = startLevel === "write" && !canPublish ? "read" : startLevel;
      });
  });

  const shown = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    return q ? items.filter((i) => i.label.toLowerCase().includes(q)) : items;
  });
  const toggle = (key: string, on: boolean) => (chosen = on ? [...chosen, key] : chosen.filter((x) => x !== key));
  const selectAll = () => (chosen = [...new Set([...chosen, ...shown.map((i) => i.id)])]);
  const selectNone = () => (chosen = []);
</script>

<ShortFormDialog
  bind:open
  {title}
  {description}
  submitLabel={submitLabel(chosen.length, level)}
  canSubmit={chosen.length > 0}
  onSubmit={() => onGive(chosen, level)}
>
  {#if loading && !items.length}
    <Loading rows={3} />
  {:else if error}
    <ErrorState inline {error} />
  {:else if items.length}
    <FormField label="Filter" for="{id}-filter">
      <Input id="{id}-filter" type="search" autocomplete="off" spellcheck={false} bind:value={filter} />
    </FormField>
    <div class="flex flex-col gap-2">
      <div class="flex items-center gap-1">
        <span class="text-meta text-muted-foreground">{chosen.length} of {items.length} selected</span>
        <Button class="ml-auto" variant="ghost" size="xs" disabled={!shown.length} onclick={selectAll}>Select all</Button>
        <Button variant="ghost" size="xs" disabled={!chosen.length} onclick={selectNone}>Select none</Button>
      </div>
      <div class="-mx-3 flex max-h-72 flex-col overflow-y-auto" role="group" aria-label={noun[1]}>
        {#each shown as item (item.id)}
          <OptionRow id="{id}-{item.id}" label={item.label} description={item.detail}>
            {#snippet control()}
              <Checkbox id="{id}-{item.id}" checked={chosen.includes(item.id)} onCheckedChange={(v) => toggle(item.id, v === true)} />
            {/snippet}
          </OptionRow>
        {:else}
          <p class="px-3 py-2 text-body text-muted-foreground">No {noun[1]} match the filter.</p>
        {/each}
      </div>
    </div>
    <RadioGroup.Root class="gap-1" aria-label="Access" value={level} onValueChange={(v) => (level = v as Level)}>
      <OptionRow id="{id}-read" label="Download" description="Can download the files.">
        {#snippet control()}<RadioGroup.Item id="{id}-read" value="read" />{/snippet}
      </OptionRow>
      <OptionRow id="{id}-write" label="Publish" description={canPublish ? "Can download and publish the files." : publishNote}>
        {#snippet control()}<RadioGroup.Item id="{id}-write" value="write" disabled={!canPublish} />{/snippet}
      </OptionRow>
    </RadioGroup.Root>
  {:else}
    <p class="text-body text-muted-foreground">{none}</p>
  {/if}
</ShortFormDialog>
