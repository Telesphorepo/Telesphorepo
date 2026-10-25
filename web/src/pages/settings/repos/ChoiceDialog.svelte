<!--
  A short form of the Access tab that adds existing things to a repository: a CI pipeline or a
  network list. The dialog is the wide one (768px), since each row has a line of help. Each thing
  is a checkbox row, a 4px gap between rows; the submit button names how many it adds. A link
  under the rows creates a new one on its own page. The parent adds the chosen ids to its draft,
  and the page's save bar saves them.

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
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";

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
    onAdd: (ids: string[]) => void;
  } = $props();

  const id = $props.id();
  let chosen = $state<string[]>([]);
  $effect(() => {
    if (open) chosen = [];
  });

  const toggle = (key: string, on: boolean) => (chosen = on ? [...chosen, key] : chosen.filter((x) => x !== key));
  const submitLabel = $derived(chosen.length === 0 ? "Add" : `Add ${chosen.length} ${chosen.length === 1 ? noun[0] : noun[1]}`);
</script>

<ShortFormDialog bind:open wide {title} {description} {submitLabel} canSubmit={chosen.length > 0} onSubmit={() => onAdd(chosen)}>
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
    </div>
  {:else}
    <p class="text-body text-muted-foreground">{none}</p>
  {/if}
  {#if newHref}
    <div>
      <Button variant="outline" size="sm" href={newHref} onclick={() => (open = false)}><PlusIcon />{newLabel}</Button>
    </div>
  {/if}
  {#if items.length}<p class="text-meta text-muted-foreground">Save the page to keep the change.</p>{/if}
</ShortFormDialog>
